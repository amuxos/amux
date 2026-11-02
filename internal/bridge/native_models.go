package bridge

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
)

type nativeModelQuery struct {
	target  resolvedBridgeTarget
	request *pb.ListNativeModelsRequest
}

func (b *Bridge) prepareNativeModels(state managementState, openID string, agents ...string) *nativeModelQuery {
	agent := "codex"
	if len(agents) > 0 {
		agent = agents[0]
	}
	if b.hub == nil {
		return nil
	}
	targetRef := state.NativeModelsTargetRef
	if targetRef == "" && state.Mode == config.FeishuInteractionSingleton && state.BotSession != nil && state.BotSession.Enabled {
		targetRef = state.BotSession.TargetID
	}
	target, issue := b.resolveRunTarget(targetRef)
	if issue != "" {
		return nil
	}
	identity, err := b.executionIdentity(openID, "feishu-session:models-"+uuid.NewString(), target.ID,
		state.Mode == config.FeishuInteractionSingleton, state.Mode == interactionModeThreadIsolated)
	if err != nil {
		return nil
	}
	return &nativeModelQuery{target: target, request: &pb.ListNativeModelsRequest{
		AgentType: agent, ExecutionIdentity: identity.Identity, AgentExecutable: config.GetAgentExecutable(agent),
	}}
}

func (b *Bridge) loadNativeModels(parent context.Context, state managementState, openID string, agents ...string) managementState {
	return b.loadNativeModelsQuery(parent, state, b.prepareNativeModels(state, openID, agents...), agents...)
}

func (b *Bridge) loadNativeModelsQuery(parent context.Context, state managementState, query *nativeModelQuery, agents ...string) managementState {
	agent := "codex"
	if len(agents) > 0 {
		agent = agents[0]
	}
	if query != nil {
		agent = query.request.AgentType
	}
	state.NativeModelsStatus = "unavailable"
	// Never mutate a shared editor snapshot or accumulate stale models on refresh.
	state.Runtimes = slices.Clone(managementRuntimeCatalog(state))
	index := -1
	for i := range state.Runtimes {
		if state.Runtimes[i].Ref != agent {
			continue
		}
		index = i
		runtime := &state.Runtimes[i]
		runtime.NativeDefaultCapabilitiesKnown = false
		runtime.Models = slices.DeleteFunc(slices.Clone(runtime.Models), func(model agentprofile.RuntimeModelOption) bool {
			return model.Kind == agentprofile.ModelKindNative
		})
		runtime.DefaultReasoningEfforts = nil
		runtime.DefaultReasoningEffort = ""
	}
	if index < 0 || query == nil {
		return state
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	response, err := b.hub.Route(ctx, query.target.ID, "ListNativeModels", query.request)
	if err != nil || response == nil || response.Error != "" {
		return state
	}
	var result pb.ListNativeModelsResponse
	if json.Unmarshal(response.Payload, &result) != nil {
		return state
	}
	if result.Status != "ready" && result.Status != "signed_out" {
		return state
	}
	state.NativeModelsStatus = result.Status
	state.NativeModelsTargetName = valueOrPlaceholder(query.target.Name, query.target.ID)
	if result.Status != "ready" {
		return state
	}
	runtime := &state.Runtimes[index]
	seen := map[string]bool{}
	for _, model := range runtime.Models {
		seen[model.Ref] = true
	}
	for _, model := range result.Models {
		if model.Model == "" || seen[model.Model] {
			continue
		}
		seen[model.Model] = true
		runtime.Models = append(runtime.Models, agentprofile.RuntimeModelOption{
			Ref: model.Model, AgentModel: model.Model, Kind: agentprofile.ModelKindNative,
			DisplayName: model.DisplayName, SupportedReasoningEfforts: slices.Clone(model.ReasoningEfforts),
			DefaultReasoningEffort: model.DefaultReasoningEffort,
		})
		for _, variant := range model.Variants {
			if variant.BackendVariant == "" {
				continue
			}
			ref := agentprofile.NativeVariantRef(model.Model, variant.BackendVariant)
			if seen[ref] {
				continue
			}
			seen[ref] = true
			runtime.Models = append(runtime.Models, agentprofile.RuntimeModelOption{Ref: ref, AgentModel: model.Model, BackendVariant: variant.BackendVariant, Kind: agentprofile.ModelKindNative, DisplayName: variant.DisplayName, SupportedReasoningEfforts: slices.Clone(model.ReasoningEfforts), DefaultReasoningEffort: model.DefaultReasoningEffort})
		}
		if model.IsDefault {
			runtime.NativeDefaultCapabilitiesKnown = true
			runtime.DefaultReasoningEfforts = slices.Clone(model.ReasoningEfforts)
			runtime.DefaultReasoningEffort = model.DefaultReasoningEffort
		}
	}
	return state
}

func nativeModelElements(state managementState, runtime string, values map[string]string, action string) []cardElement {
	if !config.SupportsAgentExecutable(runtime) {
		return nil
	}
	status := state.NativeModelsStatus
	if status == "" {
		status = "unavailable"
	}
	var hint any = managementCopy("management.model.native."+status, config.AgentExecutableLabel(runtime))
	if state.NativeModelsTargetName != "" {
		hint = managementConcat(hint, " · ", state.NativeModelsTargetName)
	}
	refreshValues := make(map[string]string, len(values)+1)
	for key, value := range values {
		refreshValues[key] = value
	}
	refreshValues["native_models_action"] = "refresh"
	return []cardElement{
		managementHintElement(hint),
		managementButtonElement(managementButton{
			Action: action, Name: action + ".native_models.refresh", Text: managementCopy("management.model.native.refresh", config.AgentExecutableLabel(runtime)), Values: refreshValues,
		}),
	}
}
