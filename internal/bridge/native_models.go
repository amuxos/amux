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

func (b *Bridge) loadNativeModels(parent context.Context, state managementState, openID string) managementState {
	state.NativeModelsStatus = "unavailable"
	// Never mutate a shared editor snapshot or accumulate stale models on refresh.
	state.Runtimes = slices.Clone(managementRuntimeCatalog(state))
	index := -1
	for i := range state.Runtimes {
		if state.Runtimes[i].Ref != "codex" {
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
	if index < 0 || b.hub == nil {
		return state
	}
	targetRef := state.NativeModelsTargetRef
	if targetRef == "" && state.Mode == config.FeishuInteractionSingleton && state.BotSession != nil && state.BotSession.Enabled {
		targetRef = state.BotSession.TargetID
	}
	target, issue := b.resolveRunTarget(targetRef)
	if issue != "" {
		return state
	}
	identity, err := b.executionIdentity(openID, "feishu-session:models-"+uuid.NewString(), target.ID,
		state.Mode == config.FeishuInteractionSingleton, state.Mode == interactionModeThreadIsolated)
	if err != nil {
		return state
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	response, err := b.hub.Route(ctx, target.ID, "ListNativeModels", &pb.ListNativeModelsRequest{
		AgentType: "codex", ExecutionIdentity: identity.Identity, AgentExecutable: config.GetCodexExecutable(),
	})
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
	state.NativeModelsTargetName = valueOrPlaceholder(target.Name, target.ID)
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
		if model.IsDefault {
			runtime.NativeDefaultCapabilitiesKnown = true
			runtime.DefaultReasoningEfforts = slices.Clone(model.ReasoningEfforts)
			runtime.DefaultReasoningEffort = model.DefaultReasoningEffort
		}
	}
	return state
}

func nativeModelElements(state managementState, runtime string, values map[string]string, action string) []cardElement {
	if runtime != "codex" {
		return nil
	}
	status := state.NativeModelsStatus
	if status == "" {
		status = "unavailable"
	}
	var hint any = managementCopy("management.model.native." + status)
	if state.NativeModelsTargetName != "" {
		hint = managementConcat(hint, " · ", state.NativeModelsTargetName)
	}
	return []cardElement{
		managementHintElement(hint),
		managementButtonElement(managementButton{
			Action: action, Name: action + ".native_models.refresh", Text: managementCopy("management.model.native.refresh"), Values: values,
		}),
	}
}
