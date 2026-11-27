package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/spf13/viper"
)

type nativeModelsHub struct {
	Hub
	result  *pb.ListNativeModelsResponse
	err     error
	target  string
	request *pb.ListNativeModelsRequest
}

func (h *nativeModelsHub) ListTargets() []*pb.Target {
	return []*pb.Target{{Id: "remote", Name: "Remote"}}
}
func (h *nativeModelsHub) Route(_ context.Context, target, method string, request any) (*pb.TunnelMsg, error) {
	if method != "ListNativeModels" {
		return nil, errors.New("unexpected method")
	}
	h.target, h.request = target, request.(*pb.ListNativeModelsRequest)
	data, _ := json.Marshal(h.result)
	return &pb.TunnelMsg{Payload: data}, h.err
}

func TestNativeModelsRefreshUsesTargetIdentityAndDropsStaleOptions(t *testing.T) {
	old := viper.Get(config.CodexExecutableKey)
	defer viper.Set(config.CodexExecutableKey, old)
	viper.Set(config.CodexExecutableKey, "~/bin/codex-team")
	hub := &nativeModelsHub{result: &pb.ListNativeModelsResponse{Status: "ready", Models: []*pb.NativeModel{{Model: "future-model", DisplayName: "Future", ReasoningEfforts: []string{"ultra"}, DefaultReasoningEffort: "ultra", IsDefault: true}}}}
	b := &Bridge{hub: hub}
	b.SetExecutionIdentityResolver(func(openID, ref, target string, shared bool) (LaunchIdentity, error) {
		if openID != "editor" || target != "remote" || shared || !strings.HasPrefix(ref, "feishu-session:models-") {
			t.Errorf("bad identity scope: %s %s %s %v", openID, ref, target, shared)
		}
		return LaunchIdentity{Identity: &pb.ExecutionIdentity{EffectivePrincipalId: "principal-a"}}, nil
	})
	base := managementState{DefaultRuntimeRef: "codex", DefaultModelRef: "saved-model", DefaultReasoningEffort: "old-effort", Runtimes: []agentprofile.RuntimeOption{{Ref: "codex", AgentType: "codex", Models: []agentprofile.RuntimeModelOption{
		{Ref: "stale-model", Kind: agentprofile.ModelKindNative}, {Ref: "custom-provider/model", Kind: agentprofile.ModelKindProvider},
	}}}}
	state := b.loadNativeModels(context.Background(), base, "editor")
	if hub.target != "remote" || hub.request.ExecutionIdentity.EffectivePrincipalId != "principal-a" || hub.request.AgentExecutable != "~/bin/codex-team" {
		t.Fatalf("wrong execution environment: %+v", hub)
	}
	models := state.Runtimes[0].Models
	if len(models) != 2 || models[0].Ref != "custom-provider/model" || models[1].Ref != "future-model" || models[1].DefaultReasoningEffort != "ultra" {
		t.Fatalf("catalog=%+v", models)
	}
	if base.Runtimes[0].Models[0].Ref != "stale-model" {
		t.Fatal("mutated base snapshot")
	}
	if !managementValidRuntimeReasoningEffort(state, "codex", "future-model", "ultra") || managementValidRuntimeReasoningEffort(state, "codex", "future-model", "high") {
		t.Fatal("ignored discovered capabilities")
	}
	draft := managementDraftFromCallback(&callback.CallBackAction{FormValue: map[string]any{"default_runtime_ref": "codex", "default_model_ref": "future-model", "default_reasoning_effort": "ultra"}}, state)
	if draft.DefaultModelRef != "future-model" || draft.DefaultReasoningEffort != "ultra" {
		t.Fatalf("selection lost: %+v", draft)
	}
	// The next refresh replaces, rather than appends to, the old catalog.
	hub.result.Models = []*pb.NativeModel{{Model: "replacement"}}
	next := b.loadNativeModels(context.Background(), state, "editor")
	if len(next.Runtimes[0].Models) != 2 || next.Runtimes[0].Models[1].Ref != "replacement" {
		t.Fatalf("stale catalog survived: %+v", next.Runtimes)
	}
	for _, failure := range []string{"signed_out", "unavailable"} {
		hub.result.Status = failure
		if failure == "unavailable" {
			hub.err = errors.New("offline")
		}
		failed := b.loadNativeModels(context.Background(), state, "editor")
		if failed.NativeModelsStatus != failure || len(failed.Runtimes[0].Models) != 1 {
			t.Fatalf("fallback models leaked: %+v", failed)
		}
		opts := managementRuntimeModelOptions(failed, "codex")
		found := false
		for _, option := range opts {
			if option.Value == "saved-model" {
				found = true
			}
		}
		if !found || !managementValidRuntimeReasoningEffort(failed, "codex", "saved-model", "old-effort") {
			t.Fatal("saved selection lost on discovery failure")
		}
	}
}

func TestNativeModelsStatusCardsBilingualAndUnique(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"ready", "signed_out", "unavailable", "loading"} {
		card := marshalNativeManagementCard(managementCardView{Elements: nativeModelElements(managementState{NativeModelsStatus: status}, "codex", map[string]string{"default_model_ref": "saved"}, managementActionSettingsEdit)})
		for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
			rendered := selectManagementCardLocale(card, locale)
			if strings.Contains(rendered, "management.model.native") {
				t.Fatalf("untranslated key: %s", rendered)
			}
			names := map[string]int{}
			collectCardElementNames(t, rendered, names)
			for name, count := range names {
				if count > 1 {
					t.Fatalf("duplicate %s", name)
				}
			}
		}
	}
}
