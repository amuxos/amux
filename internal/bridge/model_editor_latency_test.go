package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/modelservice"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/spf13/viper"
)

type slowEditorModelsHub struct {
	Hub
	release <-chan struct{}
	calls   atomic.Int32
}

func (h *slowEditorModelsHub) ListTargets() []*pb.Target {
	return []*pb.Target{{Id: "remote", Name: "Remote"}}
}
func (h *slowEditorModelsHub) Route(ctx context.Context, _, _ string, _ any) (*pb.TunnelMsg, error) {
	h.calls.Add(1)
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, _ := json.Marshal(&pb.ListNativeModelsResponse{Status: "ready", Models: []*pb.NativeModel{{Model: "discovered"}}})
	return &pb.TunnelMsg{Payload: data}, nil
}

func TestModelEditorRendersBeforeSlowDiscovery(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	release := make(chan struct{})
	patched := make(chan string, 4)
	defer func() {
		close(release)
		waitForManagementCard(t, patched, func(card string) bool { return strings.Contains(card, "discovered") })
	}()
	b.hub = &slowEditorModelsHub{release: release}
	b.managementCardPatch = func(_ context.Context, _, card string) error { patched <- card; return nil }
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Action: &callback.CallBackAction{Value: map[string]any{"default_runtime_ref": "codex"}},
	}}
	if _, ok := b.handleModelServiceEditor(event, managementActionSettingsEdit, "owner", "card", b.advanceModelServiceEditor("card")); !ok {
		t.Fatal("editor not handled")
	}
	select {
	case <-patched:
	case <-time.After(time.Second):
		t.Fatal("settings card waits for model discovery before showing the draft")
	}
}

func TestModelEditorGroupDraftRendersBeforeSlowDiscovery(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	release := make(chan struct{})
	b.hub = &slowEditorModelsHub{release: release}
	t.Cleanup(func() {
		close(release)
		if card := waitForManagementCard(t, cards, func(card string) bool { return strings.Contains(card, "discovered") }); card == "" {
			t.Error("group model discovery did not finish")
		}
	})
	settings := groupSettingsTestDraft(t, b, "group-a")
	event := groupSettingsTestAction(b, settings, managementActionGroupSettingsEdit, map[string]any{
		"group_runtime": "codex", "group_target": "remote", "group_interaction": "thread", "group_model": "unsaved-model",
	})
	groupSettingsTestCallback(t, b, event, "success")
	card := groupSettingsTestCard(t, cards)
	if !strings.Contains(card, "正在加载原生模型") || !strings.Contains(card, "unsaved-model") {
		t.Fatal("group loading preview lost its unsaved draft")
	}
}

func editorTestState() managementState {
	return managementState{Mode: "thread", DefaultRuntimeRef: "codex", Runtimes: []agentprofile.RuntimeOption{{Ref: "codex", AgentType: "codex"}}}
}

func TestModelEditorCacheCoalescesAndKeepsCurrentDraft(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	hub := &slowEditorModelsHub{release: release}
	b := &Bridge{hub: hub}
	const readers = 8
	previews := make(chan struct{}, readers)
	done := make(chan managementState, readers)
	for n := 0; n < readers; n++ {
		go func() {
			base := editorTestState()
			base.DefaultModelRef = "unsaved-model"
			done <- b.refreshEditorModels(context.Background(), base, "owner", "card", "codex", "", "status", false, func(s managementState) {
				if s.NativeModelsStatus == "loading" {
					previews <- struct{}{}
				}
			})
		}()
	}
	for n := 0; n < readers; n++ {
		select {
		case <-previews:
		case <-time.After(time.Second):
			t.Fatal("concurrent editor did not render preview")
		}
	}
	close(release)
	for n := 0; n < readers; n++ {
		select {
		case got := <-done:
			if got.DefaultModelRef != "unsaved-model" || got.NativeModelsStatus != "ready" {
				t.Fatalf("draft lost: %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("reader did not finish")
		}
	}
	if hub.calls.Load() != 1 {
		t.Fatalf("duplicate discoveries: %d", hub.calls.Load())
	}
	base := editorTestState()
	base.DefaultModelRef, base.DefaultReasoningEffort = "new-selection", "high"
	renders := 0
	got := b.refreshEditorModels(context.Background(), base, "owner", "card", "codex", "", "status", false, func(s managementState) {
		renders++
		if s.NativeModelsStatus != "ready" {
			t.Error("warm edit flashed loading state")
		}
	})
	if hub.calls.Load() != 1 || renders != 1 || got.DefaultModelRef != "new-selection" || got.DefaultReasoningEffort != "high" {
		t.Fatalf("warm edit rediscovered or restored old draft: calls=%d renders=%d state=%+v", hub.calls.Load(), renders, got)
	}
	// A caller changing the returned slices must not corrupt future snapshots.
	got.Runtimes[0].Models[0].Ref = "mutated"
	got = b.refreshEditorModels(context.Background(), base, "owner", "card", "codex", "", "status", false, func(managementState) {})
	if got.Runtimes[0].Models[0].Ref != "discovered" {
		t.Fatal("cache aliases returned model slices")
	}
}

func TestModelEditorCacheRefreshExpiryAndIdentityIsolation(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	close(release)
	hub := &slowEditorModelsHub{release: release}
	b := &Bridge{hub: hub}
	principal := "principal-a"
	environment := "env-a"
	b.SetExecutionIdentityResolver(func(_, _, _ string, _ bool) (LaunchIdentity, error) {
		return LaunchIdentity{Identity: &pb.ExecutionIdentity{EffectivePrincipalId: principal, PrincipalEnvironmentRef: environment}}, nil
	})
	base := editorTestState()
	load := func(openID string, force bool) {
		b.refreshEditorModels(context.Background(), base, openID, "card", "codex", "", "status", force, func(managementState) {})
	}
	load("owner", false)
	load("owner", false)
	if hub.calls.Load() != 1 {
		t.Fatal("cache miss for unchanged identity")
	}
	load("owner", true)
	if hub.calls.Load() != 2 {
		t.Fatal("explicit refresh reused cache")
	}
	load("other-owner", false)
	principal = "principal-b"
	load("owner", false)
	environment = "env-b"
	load("owner", false)
	base.Mode = interactionModeThreadIsolated
	load("owner", false)
	if hub.calls.Load() != 6 {
		t.Fatalf("scope crossed: calls=%d", hub.calls.Load())
	}
	b.editorModels.mu.Lock()
	for _, entry := range b.editorModels.entries {
		entry.expires = time.Now().Add(-time.Second)
	}
	b.editorModels.mu.Unlock()
	load("owner", false)
	if hub.calls.Load() != 7 {
		t.Fatal("expired cache reused")
	}
}

func TestModelEditorExplicitRefreshSupersedesInflightCache(t *testing.T) {
	t.Parallel()
	var cache editorModelCache
	key := [32]byte{1}
	old, _ := cache.begin(key, false, time.Now())
	newest, owner := cache.begin(key, true, time.Now())
	if !owner || newest == old {
		t.Fatal("refresh joined obsolete flight")
	}
	cache.finish(newest, editorModelSnapshot{NativeStatus: "ready", NativeTarget: "new"})
	cache.finish(old, editorModelSnapshot{NativeStatus: "ready", NativeTarget: "old"})
	got, owner := cache.begin(key, false, time.Now())
	if owner || got.result.NativeTarget != "new" {
		t.Fatal("old result replaced current generation")
	}
	for n := 0; n < editorModelCacheLimit*2; n++ {
		cache.begin([32]byte{byte(n), 1}, true, time.Now())
	}
	if len(cache.entries) > editorModelCacheLimit {
		t.Fatal("unbounded cache")
	}
}

func TestModelEditorFailurePreservesChoicesButSignoutRemovesThem(t *testing.T) {
	t.Parallel()
	previous := editorModelSnapshot{
		NativeStatus: "ready",
		Runtimes: []agentprofile.RuntimeOption{{Ref: "codex", Models: []agentprofile.RuntimeModelOption{
			{Ref: "native", Kind: agentprofile.ModelKindNative}, {Ref: "service/model", Kind: agentprofile.ModelKindProvider},
		}}},
		Services: []managementModelService{{Service: modelservice.Service{ID: "service"}, State: modelservice.State{Status: "signed_in", Models: []modelservice.Model{{Ref: "service/model"}}}}},
	}
	for _, status := range []string{"unavailable", "signed_out"} {
		state := editorTestState()
		state.NativeModelsStatus = status
		state.ModelServices = []managementModelService{{Service: modelservice.Service{ID: "service"}, State: modelservice.State{Status: status}}}
		state.DefaultModelRef = "unsaved"
		got := retainUnavailableEditorModels(state, previous)
		want := 0
		if status == "unavailable" {
			want = 2
		}
		if len(got.Runtimes[0].Models) != want || got.DefaultModelRef != "unsaved" || got.ModelServices[0].State.Status != status {
			t.Fatalf("bad %s result: %+v", status, got)
		}
	}
}

func TestModelEditorQueuedWriteCannotOverwriteNavigation(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	var writes atomic.Int32
	b.managementCardPatch = func(context.Context, string, string) error { writes.Add(1); return nil }
	version := b.advanceModelServiceEditor("card")
	unlock := b.lockRoute("management-card\x00card")
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		b.writeManagementCardAtVersion(context.Background(), "owner", "card", "{}", version)
		close(done)
	}()
	<-started
	b.advanceModelServiceEditor("card")
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queued writer stuck")
	}
	if writes.Load() != 0 {
		t.Fatal("stale editor overwrote newer page")
	}
}

func TestModelEditorLoadingAndResultWritesStayOrdered(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	var mu sync.Mutex
	var cards []string
	b.managementCardPatch = func(_ context.Context, _, card string) error {
		mu.Lock()
		defer mu.Unlock()
		cards = append(cards, card)
		return nil
	}
	version := b.advanceModelServiceEditor("card")
	b.writeManagementCardAtVersion(context.Background(), "owner", "card", "loading", version)
	b.writeManagementCardAtVersion(context.Background(), "owner", "card", "ready", version)
	if strings.Join(cards, ",") != "loading,ready" {
		t.Fatal(cards)
	}
}

func TestModelEditorNativeRefreshSurvivesMissingElementName(t *testing.T) {
	t.Parallel()
	values := map[string]string{"default_model_ref": "unsaved", "default_runtime_ref": "codex"}
	elements := nativeModelElements(managementState{}, "codex", values, managementActionSettingsEdit)
	card := marshalNativeManagementCard(managementCardView{Elements: elements})
	button := findManagementButtonByAction(t, card, managementActionSettingsEdit)
	action := &callback.CallBackAction{} // Some callbacks omit the element name.
	if err := json.Unmarshal(button.Value, &action.Value); err != nil {
		t.Fatal(err)
	}
	if !forceEditorModelRefresh(action) {
		t.Fatal("explicit refresh reused the cached models")
	}
	if len(values) != 2 {
		t.Fatal("refresh mutated the draft values")
	}
}

func TestModelEditorCacheSeparatesCardLauncherServiceAndCatalog(t *testing.T) {
	release := make(chan struct{})
	close(release)
	hub := &slowEditorModelsHub{release: release}
	b := &Bridge{hub: hub}
	principal := "principal-a"
	b.SetFeishuPrincipalResolver(func(string) (string, error) { return principal, nil })
	old := viper.Get(config.CodexExecutableKey)
	t.Cleanup(func() { viper.Set(config.CodexExecutableKey, old) })
	viper.Set(config.CodexExecutableKey, "codex")
	base := editorTestState()
	load := func(card string) {
		b.refreshEditorModels(context.Background(), base, "owner", card, "codex", "", "status", false, func(managementState) {})
	}
	load("card-a")
	load("card-a")
	if hub.calls.Load() != 1 {
		t.Fatal("unchanged card missed cache")
	}
	load("card-b")
	viper.Set(config.CodexExecutableKey, "~/bin/codex-other")
	load("card-a")
	principal = "principal-b"
	load("card-a")
	base.Runtimes[0].Models = []agentprofile.RuntimeModelOption{{Ref: "catalog-change", Kind: agentprofile.ModelKindProvider}}
	load("card-a")
	if hub.calls.Load() != 5 {
		t.Fatalf("cache crossed scope: calls=%d", hub.calls.Load())
	}
}

func TestModelEditorFailureRetentionExpiresAndDoesNotRestoreSignedOutNativeModels(t *testing.T) {
	t.Parallel()
	var cache editorModelCache
	key := [32]byte{1}
	ready := editorModelSnapshot{
		NativeStatus: "ready",
		Runtimes: []agentprofile.RuntimeOption{{Ref: "codex", Models: []agentprofile.RuntimeModelOption{
			{Ref: "native", Kind: agentprofile.ModelKindNative}, {Ref: "service/model", Kind: agentprofile.ModelKindProvider},
		}}},
		Services: []managementModelService{{Service: modelservice.Service{ID: "service"}, State: modelservice.State{Status: "signed_in", Models: []modelservice.Model{{Ref: "service/model"}}}}},
	}
	first, _ := cache.begin(key, false, time.Now())
	cache.finish(first, ready)
	failure := editorModelSnapshot{NativeStatus: "signed_out", Runtimes: editorTestState().Runtimes,
		Services: []managementModelService{{Service: modelservice.Service{ID: "service"}, State: modelservice.State{Status: "unavailable"}}}}
	next, _ := cache.begin(key, true, time.Now())
	cache.finish(next, failure)
	models := next.result.Runtimes[0].Models
	if len(models) != 1 || models[0].Ref != "service/model" {
		t.Fatalf("sign-out retained native models: %+v", models)
	}
	if !next.retainUntil.Equal(first.retainUntil) {
		t.Fatal("failure extended stale retention")
	}
	next.retainUntil = time.Now().Add(-time.Second)
	expired, _ := cache.begin(key, true, time.Now())
	failure.NativeStatus = "unavailable"
	cache.finish(expired, failure)
	if len(expired.result.Runtimes[0].Models) != 0 {
		t.Fatal("expired choices survived")
	}
}
