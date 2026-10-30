package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/modelservice"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestRuntimeSwitchToCodexWithModelServicesPreservesSaveDraft(t *testing.T) {
	for _, status := range []string{"", "signed_in", "signed_out", "expired", "pending", "unavailable", "setup_required", "installing"} {
		for _, services := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/services=%d", status, services), func(t *testing.T) {
				state := managementState{
					Mode: config.FeishuInteractionSingleton, Version: 7,
					DefaultAgent: "builtin/claude", DefaultRuntimeRef: "claude",
					DefaultModelRef: "GPT-5.6-Sol", DefaultReasoningEffort: "high",
					ThinkingProcess: config.FeishuThinkingProcessExpanded,
				}
				for n := 0; n < services; n++ {
					state.ModelServices = append(state.ModelServices, managementModelService{
						Service: modelservice.Service{ID: fmt.Sprintf("external-%d", n), Name: "External", Runtimes: []string{"codex"}},
						State:   modelservice.State{Status: status, CanInstall: true},
					})
				}
				// Replay a runtime select callback, then save using the values from
				// the replacement card, just as Feishu does after a successful patch.
				draft := managementDraftFromCallback(&callback.CallBackAction{
					Option: "codex",
					Value: map[string]any{managementActionKey: managementActionSettingsEdit,
						managementDraftFieldKey: "default_runtime_ref", "default_runtime_ref": "claude"},
				}, state)
				card := renderSettingsEditCard(state, draft)
				for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
					names := map[string]int{}
					collectCardElementNames(t, selectManagementCardLocale(card, locale), names)
					for name, count := range names {
						if count > 1 {
							t.Fatalf("%s card repeats element name %q %d times", locale, name, count)
						}
					}
				}
				button := findManagementButtonByAction(t, card, managementActionSettingsSave)
				save := &callback.CallBackAction{Name: button.Name}
				if err := json.Unmarshal(button.Value, &save.Value); err != nil {
					t.Fatal(err)
				}
				savedDraft := managementDraftFromCallback(save, state)
				req := managementBotSettingsRequest(state, savedDraft, "settings-card", "owner")
				if req.DefaultRuntimeRef != "codex" || req.DefaultAgent != state.DefaultAgent ||
					req.DefaultModelRef != "" || req.DefaultReasoningEffort != "" ||
					req.Interaction != state.Mode || req.ExpectedVersion != state.Version || savedDraft.ThinkingProcess != state.ThinkingProcess {
					t.Fatalf("runtime switch lost the save draft: request=%+v draft=%+v", req, savedDraft)
				}
			})
		}
	}
}

func TestRuntimeSelectionRetainsModelWhenRuntimeUnchanged(t *testing.T) {
	state := managementState{DefaultRuntimeRef: "codex", DefaultModelRef: "gpt-5.6-sol", DefaultReasoningEffort: "high"}
	draft := managementDraftFromCallback(&callback.CallBackAction{Option: "codex", Value: map[string]any{
		managementDraftFieldKey: "default_runtime_ref", "default_runtime_ref": "codex",
	}}, state)
	if draft.DefaultModelRef != state.DefaultModelRef || draft.DefaultReasoningEffort != state.DefaultReasoningEffort {
		t.Fatalf("unchanged runtime lost selection: %+v", draft)
	}
}

func TestModelServiceStateDoesNotLeakChallengeAndPreservesDraft(t *testing.T) {
	state := managementState{ModelServices: []managementModelService{{Service: modelservice.Service{ID: "external", Name: "External", Runtimes: []string{"codex"}}, State: modelservice.State{Status: "pending", LoginURL: "https://auth.example/login"}}}}
	elements := modelServiceElements(state, "codex", map[string]string{"default_runtime_ref": "codex", "default_model_ref": "native", "default_reasoning_effort": "high"}, managementActionSettingsEdit)
	card := marshalNativeManagementCard(managementCardView{Elements: elements})
	for _, want := range []string{"登录中", "https://auth.example/login", "刷新登录状态", "default_model_ref", "native", "high"} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q", want)
		}
	}
	if len(modelServiceElements(state, "unsupported", nil, managementActionSettingsEdit)) != 0 {
		t.Fatal("service leaked into unsupported engine")
	}
	id, operation := modelServiceSelection(&callback.CallBackAction{Option: modelServiceEntryPrefix + "external"})
	if id != "external" || operation != "login" {
		t.Fatal(id, operation)
	}
}
func TestModelServiceEditorRejectsStaleCompletion(t *testing.T) {
	b := &Bridge{}
	old := b.advanceModelServiceEditor("card")
	b.advanceModelServiceEditor("card")
	if b.modelServiceEditorCurrent("card", old) {
		t.Fatal("stale login completion may overwrite newer selection")
	}
}

func TestServiceLoginSelectionRetainsUnsavedModel(t *testing.T) {
	for _, field := range []string{"default_model_ref", "group_model", "document_comments_model_ref", "group_watch_model_ref"} {
		action := &callback.CallBackAction{Name: field, Option: modelServiceEntryPrefix + "external",
			Value:     map[string]interface{}{field: "previous-model", managementDraftFieldKey: field, "default_reasoning_effort": "high"},
			FormValue: map[string]interface{}{field: modelServiceEntryPrefix + "external", "default_agent": "draft-expert"},
		}
		clean := modelServiceDraftAction(action)
		if clean.Option != "" || clean.Name != "" || clean.Value[field] != "previous-model" || clean.Value["default_reasoning_effort"] != "high" || clean.FormValue["default_agent"] != "draft-expert" {
			t.Fatalf("draft changed: %+v", clean)
		}
		if _, ok := clean.FormValue[field]; ok {
			t.Fatal("service entry still treated as model")
		}
		if action.Option == "" || action.FormValue[field] == nil {
			t.Fatal("original callback mutated")
		}
	}
}

func TestModelServiceControlsReflectLoginState(t *testing.T) {
	for _, tc := range []struct{ status, want, operation string }{
		{"signed_out", "登录 External", "login"}, {"expired", "登录 External", "login"},
		{"signed_in", "刷新模型列表", "refresh"}, {"pending", "完成登录后自动刷新", "refresh"},
		{"setup_required", "重新检测", "refresh"}, {"installing", "查看安装进度", "refresh"},
	} {
		state := managementState{ModelServices: []managementModelService{{Service: modelservice.Service{ID: "external", Name: "External", Runtimes: []string{"codex"}}, State: modelservice.State{Status: tc.status}}}}
		card := marshalNativeManagementCard(managementCardView{Elements: modelServiceElements(state, "codex", nil, managementActionSettingsEdit)})
		if !strings.Contains(card, tc.want) || !strings.Contains(card, `"model_service_action":"`+tc.operation+`"`) {
			t.Fatalf("wrong controls for %s: %s", tc.status, card)
		}
	}
	id, op := modelServiceSelection(&callback.CallBackAction{Value: map[string]interface{}{"model_service": "external", "model_service_action": "login"}})
	if id != "external" || op != "login" {
		t.Fatal(id, op)
	}
}

func TestModelServiceInstallIsExplicitAndPreservesServiceSelection(t *testing.T) {
	state := managementState{ModelServices: []managementModelService{{
		Service: modelservice.Service{ID: "external", Name: "External", Runtimes: []string{"codex"}},
		State:   modelservice.State{Status: "setup_required", CanInstall: true},
	}}}
	card := marshalNativeManagementCard(managementCardView{Elements: modelServiceElements(state, "codex", nil, managementActionSettingsEdit)})
	if !strings.Contains(card, "一键安装") || !strings.Contains(card, `"model_service_action":"install"`) || strings.Contains(card, "登录 External") {
		t.Fatalf("incorrect setup controls: %s", card)
	}
	action := &callback.CallBackAction{
		Value:     map[string]interface{}{"model_service": "external", "model_service_action": "install"},
		FormValue: map[string]interface{}{"default_model_ref": modelServiceEntryPrefix + "external"},
	}
	id, op := modelServiceSelection(action)
	if id != "external" || op != "install" {
		t.Fatalf("explicit install overwritten by form selection: %s %s", id, op)
	}
	state.ModelServices[0].State.CanInstall = false
	card = marshalNativeManagementCard(managementCardView{Elements: modelServiceElements(state, "codex", nil, managementActionSettingsEdit)})
	if strings.Contains(card, "一键安装") {
		t.Fatal("install offered without distribution support")
	}
	_, op = modelServiceSelection(&callback.CallBackAction{Option: modelServiceEntryPrefix + "external"})
	if op == "install" {
		t.Fatal("picking a service must not install it")
	}
}
