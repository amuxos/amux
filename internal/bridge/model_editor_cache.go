package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/modelservice"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

const editorModelTTL = 30 * time.Second
const editorModelRetention = 5 * time.Minute
const editorModelCacheLimit = 64

// Snapshots contain model presentation data only. Drafts, credentials and
// ownership are always read from the current callback, never from this cache.
type editorModelSnapshot struct {
	Runtimes                   []agentprofile.RuntimeOption
	Services                   []managementModelService
	NativeStatus, NativeTarget string
}

func editorModelsFromState(s managementState) editorModelSnapshot {
	runtimes := slices.Clone(s.Runtimes)
	for i := range runtimes {
		runtimes[i].DefaultReasoningEfforts = slices.Clone(runtimes[i].DefaultReasoningEfforts)
		runtimes[i].Models = slices.Clone(runtimes[i].Models)
		for j := range runtimes[i].Models {
			runtimes[i].Models[j].Provider = nil // Presentation never retains provider credentials.
			runtimes[i].Models[j].SupportedReasoningEfforts = slices.Clone(runtimes[i].Models[j].SupportedReasoningEfforts)
		}
	}
	services := slices.Clone(s.ModelServices)
	for i := range services {
		services[i].Service.Query, services[i].Service.Proxy = nil, nil
		services[i].Service.Runtimes = slices.Clone(services[i].Service.Runtimes)
		services[i].State.Models = slices.Clone(services[i].State.Models)
	}
	return editorModelSnapshot{runtimes, services, s.NativeModelsStatus, s.NativeModelsTargetName}
}

func (s editorModelSnapshot) apply(base managementState) managementState {
	copy := editorModelsFromState(managementState{Runtimes: s.Runtimes, ModelServices: s.Services,
		NativeModelsStatus: s.NativeStatus, NativeModelsTargetName: s.NativeTarget})
	base.Runtimes, base.ModelServices = copy.Runtimes, copy.Services
	base.NativeModelsStatus, base.NativeModelsTargetName = copy.NativeStatus, copy.NativeTarget
	return base
}

func (s editorModelSnapshot) cacheable() bool {
	if s.NativeStatus == "unavailable" {
		return false
	}
	for _, entry := range s.Services {
		switch entry.State.Status {
		case "unavailable", "pending", "installing":
			return false
		}
	}
	return true
}

type editorModelEntry struct {
	done          chan struct{}
	previous      editorModelSnapshot
	previousUntil time.Time
	result        editorModelSnapshot
	expires       time.Time
	retainUntil   time.Time
}

type editorModelCache struct {
	mu      sync.Mutex
	entries map[[32]byte]*editorModelEntry
}

// begin coalesces ordinary reads. Explicit actions create a new generation;
// completion of an older query cannot replace the map's current generation.
func (c *editorModelCache) begin(key [32]byte, force bool, now time.Time) (*editorModelEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[32]byte]*editorModelEntry)
	}
	old := c.entries[key]
	if old != nil && !force {
		select {
		case <-old.done:
			if now.Before(old.expires) {
				return old, false
			}
		default:
			return old, false
		}
	}
	e := &editorModelEntry{done: make(chan struct{})}
	if old != nil {
		select {
		case <-old.done:
			if now.Before(old.retainUntil) {
				e.previous, e.previousUntil = old.result, old.retainUntil
			}
		default:
			e.previous, e.previousUntil = old.previous, old.previousUntil
		}
	}
	if len(c.entries) >= editorModelCacheLimit {
		// Eviction bounds memory, but leaves any existing waiters' entry alive.
		var oldest [32]byte
		var expiry time.Time
		first := true
		for k, entry := range c.entries {
			if first || entry.expires.Before(expiry) {
				oldest, expiry, first = k, entry.expires, false
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = e
	return e, true
}

func (c *editorModelCache) finish(e *editorModelEntry, result editorModelSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	e.result = result
	if result.cacheable() {
		e.expires = now.Add(editorModelTTL)
		e.retainUntil = now.Add(editorModelRetention)
	} else if now.Before(e.previousUntil) {
		// Carry the latest authoritative sign-outs forward, never the entire
		// older snapshot, when another independent service is unavailable.
		e.result = editorModelsFromState(retainUnavailableEditorModels(result.apply(managementState{}), e.previous))
		e.retainUntil = e.previousUntil
	}
	close(e.done)
}

// Re-resolve trusted identity on each edit, even for a cache hit. Never key by
// a principal supplied by the card. Ephemeral launch grants are excluded.
type editorModelScope struct {
	key              [32]byte
	cacheable        bool
	native           *nativeModelQuery
	servicePrincipal string
}

func (b *Bridge) prepareEditorModelScope(base managementState, openID, messageID, runtimeRef string) editorModelScope {
	scope := editorModelScope{cacheable: messageID != ""}
	parts := []any{openID, messageID, runtimeRef, base.Mode, base.Runtimes}
	if runtimeRef == "codex" {
		scope.native = b.prepareNativeModels(base, openID)
		if scope.native == nil {
			scope.cacheable = false
		} else {
			i := scope.native.request.ExecutionIdentity
			parts = append(parts, scope.native.target.ID, scope.native.request.AgentExecutable,
				i.GetIdentityMode(), i.GetOriginActorPrincipalId(), i.GetEffectivePrincipalId(),
				i.GetRequestedRunAs(), i.GetPrincipalEnvironmentRef(), i.GetIsolationBackend(), i.GetCreationPolicyRevision())
		}
	}
	if b.resolveModelServicePrincipal != nil {
		principal, err := b.resolveModelServicePrincipal(openID)
		if err != nil {
			scope.cacheable = false
		} else {
			scope.servicePrincipal = principal
			parts = append(parts, principal)
		}
	}
	data, err := json.Marshal(parts)
	scope.key = sha256.Sum256(data)
	scope.cacheable = scope.cacheable && err == nil
	return scope
}

func forceEditorModelRefresh(action *callback.CallBackAction) bool {
	if action == nil {
		return false
	}
	service, operation := modelServiceSelection(action)
	if service != "" || operation != "status" || managementCallbackString(action, "native_models_action") == "refresh" || strings.HasSuffix(action.Name, ".native_models.refresh") {
		return true
	}
	field := managementCallbackString(action, managementDraftFieldKey)
	if field == "" {
		field = action.Name
	}
	return strings.Contains(field, "runtime") || field == "group_target" || field == "interaction" || field == "group_interaction"
}

func loadingEditorModels(base managementState, runtimeRef string) managementState {
	base = editorModelsFromState(base).apply(base)
	if runtimeRef == "codex" {
		base.NativeModelsStatus = "loading"
		for i := range base.Runtimes {
			if base.Runtimes[i].Ref != "codex" {
				continue
			}
			base.Runtimes[i].Models = slices.DeleteFunc(base.Runtimes[i].Models, func(m agentprofile.RuntimeModelOption) bool { return m.Kind == agentprofile.ModelKindNative })
			base.Runtimes[i].NativeDefaultCapabilitiesKnown = false
			base.Runtimes[i].DefaultReasoningEfforts = nil
			base.Runtimes[i].DefaultReasoningEffort = ""
		}
	}
	base.ModelServices = nil
	for _, service := range modelservice.List() {
		base.ModelServices = append(base.ModelServices, managementModelService{Service: service, State: modelservice.State{Status: "loading"}})
	}
	return base
}

// publish is synchronous and version-checked at the card write boundary. This
// preserves preview/result ordering without holding a cache lock over I/O.
func (b *Bridge) refreshEditorModels(ctx context.Context, base managementState, openID, messageID, runtimeRef, service, operation string, force bool, publish func(managementState)) managementState {
	scope := b.prepareEditorModelScope(base, openID, messageID, runtimeRef)
	var entry *editorModelEntry
	owner := true
	if scope.cacheable {
		entry, owner = b.editorModels.begin(scope.key, force, time.Now())
	} else {
		entry = &editorModelEntry{done: make(chan struct{})}
	}
	if owner {
		go func() {
			queryCtx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
			defer cancel()
			state := base
			started := time.Now()
			if runtimeRef == "codex" {
				state = b.loadNativeModelsQuery(queryCtx, state, scope.native)
			}
			nativeElapsed := time.Since(started)
			serviceStarted := time.Now()
			state = b.loadModelServicesForPrincipal(queryCtx, state, scope.servicePrincipal, service, operation)
			log.Debug().Dur("native_models_ms", nativeElapsed).Dur("model_services_ms", time.Since(serviceStarted)).Msg("bridge: editor model discovery completed")
			b.editorModels.finish(entry, editorModelsFromState(state))
		}()
	}
	completed := func() managementState {
		state := entry.result.apply(base)
		publish(state)
		return state
	}
	select {
	case <-entry.done:
		return completed()
	default:
	}
	preview := loadingEditorModels(base, runtimeRef)
	if time.Now().Before(entry.previousUntil) {
		preview = entry.previous.apply(base)
	}
	publish(preview)
	select {
	case <-ctx.Done():
		return preview
	case <-entry.done:
		return completed()
	}
}

func retainUnavailableEditorModels(state managementState, previous editorModelSnapshot) managementState {
	refs := map[string]bool{}
	for i := range state.ModelServices {
		current := &state.ModelServices[i]
		if current.State.Status != "unavailable" {
			continue
		}
		for _, old := range previous.Services {
			if old.Service.ID != current.Service.ID {
				continue
			}
			current.State.Models = slices.Clone(old.State.Models)
			for _, model := range old.State.Models {
				refs[model.Ref] = true
			}
		}
	}
	for i := range state.Runtimes {
		current := &state.Runtimes[i]
		for _, old := range previous.Runtimes {
			if current.Ref != old.Ref {
				continue
			}
			native := current.Ref == "codex" && state.NativeModelsStatus == "unavailable"
			if native {
				current.NativeDefaultCapabilitiesKnown = old.NativeDefaultCapabilitiesKnown
				current.DefaultReasoningEfforts = slices.Clone(old.DefaultReasoningEfforts)
				current.DefaultReasoningEffort = old.DefaultReasoningEffort
			}
			for _, model := range old.Models {
				if !refs[model.Ref] && !(native && model.Kind == agentprofile.ModelKindNative) {
					continue
				}
				if slices.ContainsFunc(current.Models, func(m agentprofile.RuntimeModelOption) bool { return m.Ref == model.Ref }) {
					continue
				}
				model.SupportedReasoningEfforts = slices.Clone(model.SupportedReasoningEfforts)
				current.Models = append(current.Models, model)
			}
		}
	}
	return state
}
