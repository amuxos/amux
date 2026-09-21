package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func groupSettingsCardTestBridge(t *testing.T) (*Bridge, <-chan string) {
	t.Helper()
	enableGroupSettingsExperiment(t)
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner", AppId: "test-app", AppSecret: "test-app-secret"}
	hub.targets = []*pb.Target{{Id: "local", Name: "local"}}
	cards := make(chan string, 20)
	b.managementCardPatch = func(_ context.Context, _, content string) error { cards <- content; return nil }
	b.dmInteractive = func(_ context.Context, _, content string) (string, string, error) {
		cards <- content
		return "dm", "card", nil
	}
	return b, cards
}

func groupSettingsTestCard(t *testing.T, cards <-chan string) string {
	t.Helper()
	select {
	case card := <-cards:
		return card
	case <-time.After(5 * time.Second):
		t.Fatal("group settings card was not delivered")
		return ""
	}
}

func TestGroupSettingsProfileRuntimeReasoningUsesEffectiveProfileModel(t *testing.T) {
	state := managementState{
		Profiles: []managementProfileSummary{{Ref: "team/coder", AgentType: "codex", Model: "profile-default"}},
		Runtimes: []agentprofile.RuntimeOption{{
			Ref: "codex", DefaultReasoningEfforts: []string{"high"},
			Models: []agentprofile.RuntimeModelOption{{Ref: "profile-default", SupportedReasoningEfforts: []string{"low"}}},
		}},
	}
	values := func(options []cardOption) []string {
		out := make([]string, 0, len(options))
		for _, option := range options {
			out = append(out, option.Value)
		}
		return out
	}
	if got := values(managementProfileReasoningOptions(state, "team/coder", "custom-native")); !reflect.DeepEqual(got, []string{managementRuntimeDefaultReasoningOption, "high"}) {
		t.Fatalf("custom Profile model options = %v", got)
	}
	if got := values(managementProfileReasoningOptions(state, "team/coder", "")); !reflect.DeepEqual(got, []string{managementRuntimeDefaultReasoningOption, "low"}) {
		t.Fatalf("Profile default model options = %v", got)
	}
}

func groupSettingsTestDraft(t *testing.T, b *Bridge, chatID string) *GroupSettings {
	t.Helper()
	settings, err := b.groupSettingsDraft(chatID)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func groupSettingsTestAction(b *Bridge, settings *GroupSettings, action string, fields map[string]any) *callback.CardActionTriggerEvent {
	values := map[string]any{managementActionKey: action}
	for key, value := range b.groupSettingsCardScope(settings, "Test group") {
		values[key] = value
	}
	for key, value := range groupSettingsFormDraft(settings) {
		values[key] = value
	}
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "owner"}, Context: &callback.Context{OpenMessageID: "card"},
		Action: &callback.CallBackAction{Value: values, FormValue: fields},
	}}
}

func groupSettingsTestCallback(t *testing.T, b *Bridge, event *callback.CardActionTriggerEvent, toastType string) *callback.CardActionTriggerResponse {
	t.Helper()
	response, handled, err := b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || response == nil || response.Toast == nil || response.Toast.Type != toastType {
		t.Fatalf("callback = %+v, handled=%v, err=%v; want %s toast", response, handled, err, toastType)
	}
	return response
}

func TestGroupSettingsCardOpenAndEditAreReadOnly(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	if err := b.sendCurrentGroupSettingsCard(context.Background(), "owner", "group-a", "Test group"); err != nil {
		t.Fatal(err)
	}
	card := groupSettingsTestCard(t, cards)
	for _, text := range []string{"当前群设置", "沿用助手设置", "修改本群设置", "group-a", "已有话题"} {
		if !strings.Contains(card, text) {
			t.Errorf("card missing %q", text)
		}
	}
	settings := groupSettingsTestDraft(t, b, "group-a")
	for _, action := range []string{managementActionGroupSettingsOpen, managementActionGroupSettingsEdit} {
		groupSettingsTestCallback(t, b, groupSettingsTestAction(b, settings, action, nil), "success")
		groupSettingsTestCard(t, cards)
	}
	for _, table := range []string{"feishu_bot_profiles", "feishu_workspaces", "feishu_group_settings", "feishu_group_session_origins", "lark_chats", "feishu_bot_sessions"} {
		var count int
		if err := b.db.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("opening settings wrote %d rows to %s", count, table)
		}
	}
}

func TestGroupSettingsCardCompleteSaveAndIndependentDefaults(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	fields := map[string]any{
		"group_interaction": config.FeishuInteractionSingleton,
		"group_agent":       "builtin/codex", "group_runtime": "codex", "group_model": managementGroupNativeModel,
		"group_native_model": "my-native-model", "group_reasoning": "high",
		"group_thinking": config.FeishuThinkingProcessHidden, "group_access": WorkspaceModeShared,
		"group_target": "local", "group_cwd": "/project/review",
		"group_args":         `["--name", "value with spaces", "a\"b", "C:\\path"]`,
		"group_idle_seconds": "300", "group_context_limit": "17", "group_max_total": "6", "group_max_per_user": "2",
	}
	groupSettingsTestCallback(t, b, groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, fields), "success")
	card := groupSettingsTestCard(t, cards)
	saved := groupSettingsTestDraft(t, b, "group-a")
	want := *settings
	want.InteractionMode, want.DefaultAgent, want.DefaultRuntimeRef = config.FeishuInteractionSingleton, "builtin/codex", "codex"
	want.DefaultModelRef, want.DefaultReasoningEffort, want.ThinkingProcess = "my-native-model", "high", config.FeishuThinkingProcessHidden
	want.Mode, want.DefaultTarget, want.DefaultCwd = WorkspaceModeShared, "local", "/project/review"
	want.DefaultArgs = []string{"--name", "value with spaces", `a"b`, `C:\path`}
	want.IdleTimeoutSeconds, want.ThreadContextLimit, want.MaxSessionsTotal, want.MaxSessionsPerUser = 300, 17, 6, 2
	want.Version, want.UpdatedBy, want.UpdatedAt = 1, "owner", saved.UpdatedAt
	if !reflect.DeepEqual(*saved, want) {
		t.Fatalf("saved settings = %+v\nwant %+v", saved, want)
	}
	requireGroupWorkspaceProjection(t, b.db, saved)
	if root, err := b.db.GetGroupSessionRoot(saved.BridgeID, saved.ChatID); err != nil || root != "" {
		t.Fatalf("save created a session: %q, %v", root, err)
	}
	if !strings.Contains(card, "新开连续会话") || !strings.Contains(card, "尚未创建") {
		t.Fatalf("missing lazy singleton summary: %s", card)
	}
	setBridgeTestBotDefaultAgent(t, b, "builtin/claude")
	if got := groupSettingsTestDraft(t, b, "group-a"); got.DefaultAgent != "builtin/codex" || got.DefaultModelRef != "my-native-model" {
		t.Fatalf("Bot default overwrote group: %+v", got)
	}
	// A field cleared in the actual form overrides the rendered button's value.
	groupSettingsTestCallback(t, b, groupSettingsTestAction(b, saved, managementActionGroupSettingsSave, map[string]any{
		"group_agent": managementGroupInheritProfile, "group_runtime": managementGroupNativeRuntime,
		"group_model": managementProfileDefaultModelOption, "group_reasoning": managementRuntimeDefaultReasoningOption,
		"group_target": "", "group_cwd": "", "group_args": "[]",
	}), "success")
	groupSettingsTestCard(t, cards)
	saved = groupSettingsTestDraft(t, b, "group-a")
	if saved.DefaultAgent != "" || saved.DefaultRuntimeRef != "" || saved.DefaultModelRef != "" || saved.DefaultReasoningEffort != "" || saved.DefaultTarget != "" || saved.DefaultCwd != "" || len(saved.DefaultArgs) != 0 {
		t.Fatalf("cleared form fields did not persist: %+v", saved)
	}
}

func TestGroupSettingsCardRejectsUnauthorizedAndForgedScope(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Bridge, *callback.CardActionTriggerEvent)
	}{
		{"member", func(_ *Bridge, event *callback.CardActionTriggerEvent) { event.Event.Operator.OpenID = "member" }},
		{"no operator", func(_ *Bridge, event *callback.CardActionTriggerEvent) { event.Event.Operator = nil }},
		{"another Bot", func(_ *Bridge, event *callback.CardActionTriggerEvent) {
			event.Event.Action.Value["bridge_id"] = "another-bot"
		}},
		{"another group", func(_ *Bridge, event *callback.CardActionTriggerEvent) {
			event.Event.Action.Value["chat_id"] = "group-b"
		}},
		{"renamed group", func(_ *Bridge, event *callback.CardActionTriggerEvent) {
			event.Event.Action.Value["chat_name"] = "misleading group"
		}},
		{"empty group", func(_ *Bridge, event *callback.CardActionTriggerEvent) { event.Event.Action.Value["chat_id"] = "" }},
		{"forged version", func(_ *Bridge, event *callback.CardActionTriggerEvent) {
			event.Event.Action.Value["expected_version"] = "1"
		}},
		{"missing token", func(_ *Bridge, event *callback.CardActionTriggerEvent) {
			delete(event.Event.Action.Value, "group_scope")
		}},
		{"private chat", func(b *Bridge, _ *callback.CardActionTriggerEvent) {
			if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), "group-a", WorkspaceKindPrivate); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, _ := groupSettingsCardTestBridge(t)
			settings := groupSettingsTestDraft(t, b, "group-a")
			for _, action := range []string{managementActionGroupSettingsOpen, managementActionGroupSettingsEdit, managementActionGroupSettingsSave, managementActionGroupSettingsWatch, managementActionGroupConversationNew} {
				event := groupSettingsTestAction(b, settings, action, nil)
				test.mutate(b, event)
				groupSettingsTestCallback(t, b, event, "error")
			}
			if saved, err := b.db.ReadGroupSettings(b.currentBridgeId(), "group-a"); err != nil || saved != nil {
				t.Fatalf("rejected card wrote settings: %+v %v", saved, err)
			}
		})
	}
	// The app key is part of authentication even if two instances share a bridge id.
	b, _ := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	event := groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, nil)
	b.secrets.AppSecret = "another-app-secret"
	groupSettingsTestCallback(t, b, event, "error")
}

func TestGroupSettingsCardSaveRejectsInvalidWholeTuple(t *testing.T) {
	for _, test := range []struct {
		name, key string
		value     any
	}{
		{"runtime", "group_runtime", "unknown-runtime"},
		{"profile", "group_agent", "missing/profile"},
		{"reasoning", "group_reasoning", "unsupported-effort"},
		{"interaction", "group_interaction", "invalid"},
		{"access", "group_access", "public"},
		{"thinking", "group_thinking", "invalid"},
		{"relative cwd", "group_cwd", "relative/path"},
		{"target", "group_target", "missing-target"},
		{"args syntax", "group_args", "--arg unquoted"},
		{"args scalar", "group_args", "null"},
		{"args mixed", "group_args", `["arg", 1]`},
		{"args null item", "group_args", `[null]`},
		{"args nul", "group_args", `["\u0000"]`},
		{"idle minimum", "group_idle_seconds", "1"},
		{"idle fraction", "group_idle_seconds", "60.5"},
		{"context negative", "group_context_limit", "-1"},
		{"total quota text", "group_max_total", "many"},
		{"user quota overflow", "group_max_per_user", "9999999999999999999999999"},
		{"non text", "group_cwd", false},
		{"native empty", "group_model", managementGroupNativeModel},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, _ := groupSettingsCardTestBridge(t)
			settings := groupSettingsTestDraft(t, b, "group-a")
			fields := map[string]any{"group_access": WorkspaceModeShared, "group_runtime": "codex", test.key: test.value}
			groupSettingsTestCallback(t, b, groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, fields), "error")
			if saved, err := b.db.ReadGroupSettings(b.currentBridgeId(), "group-a"); err != nil || saved != nil {
				t.Fatalf("invalid tuple partly saved: %+v %v", saved, err)
			}
			workspace, err := b.db.ReadWorkspace(b.currentBridgeId(), "group-a")
			if err != nil || workspace.Mode != WorkspaceModeOwnerOnly {
				t.Fatalf("invalid tuple partly changed workspace: %+v %v", workspace, err)
			}
		})
	}
}

func TestGroupSettingsCardStaleSaveAndReset(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	draft := groupSettingsTestDraft(t, b, "group-a")
	draft.InteractionMode = config.FeishuInteractionSingleton
	saved, err := b.applyGroupSettings(context.Background(), draft, 0, "owner")
	if err != nil {
		t.Fatal(err)
	}
	createGroupSessionTopic(t, b.db, *saved, "first-root")
	if err := b.db.SetGroupSessionRoot(saved.BridgeID, saved.ChatID, saved.Version, "first-root"); err != nil {
		t.Fatal(err)
	}
	// Keep the old authenticated card after another edit advances the version.
	staleSave := groupSettingsTestAction(b, saved, managementActionGroupSettingsSave, map[string]any{"group_cwd": "/stale"})
	staleReset := groupSettingsTestAction(b, saved, managementActionGroupConversationNew, nil)
	saved.DefaultCwd = "/current"
	saved, err = b.applyGroupSettings(context.Background(), saved, saved.Version, "owner")
	if err != nil {
		t.Fatal(err)
	}
	groupSettingsTestCallback(t, b, staleSave, "warning")
	groupSettingsTestCallback(t, b, staleReset, "warning")
	if root, err := b.db.GetGroupSessionRoot(saved.BridgeID, saved.ChatID); err != nil || root != "first-root" {
		t.Fatalf("stale callback reset root: %q %v", root, err)
	}
	if current := groupSettingsTestDraft(t, b, "group-a"); current.DefaultCwd != "/current" {
		t.Fatalf("stale save overwrote cwd: %+v", current)
	}
	reset := groupSettingsTestAction(b, saved, managementActionGroupConversationNew, nil)
	groupSettingsTestCallback(t, b, reset, "success")
	groupSettingsTestCard(t, cards)
	if root, err := b.db.GetGroupSessionRoot(saved.BridgeID, saved.ChatID); err != nil || root != "" {
		t.Fatalf("reset root = %q %v", root, err)
	}
	if binding, err := b.db.FindByRootMsg(saved.BridgeID, "first-root"); err != nil || binding == nil || binding.SessionName != "session-first-root" {
		t.Fatalf("reset removed history binding: %+v %v", binding, err)
	}
	groupSettingsTestCallback(t, b, reset, "warning")
	if after := groupSettingsTestDraft(t, b, "group-a"); after.Version != saved.Version+1 {
		t.Fatalf("reset version = %d", after.Version)
	}
}

func TestGroupSettingsCardFormContractAndRefreshPreservesValues(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	event := groupSettingsTestAction(b, settings, managementActionGroupSettingsEdit, map[string]any{
		"group_agent": "builtin/claude", "group_runtime": "claude", "group_model": "old-incompatible-model",
		"group_reasoning": "old-incompatible-effort", "group_cwd": "/work/unsaved", "group_max_total": "invalid-number",
	})
	groupSettingsTestCallback(t, b, event, "success")
	card := groupSettingsTestCard(t, cards)
	var doc cardDoc
	if err := json.Unmarshal([]byte(card), &doc); err != nil {
		t.Fatal(err)
	}
	fields := map[string]cardElement{}
	buttons := map[string]cardElement{}
	var walk func([]cardElement, bool)
	walk = func(elements []cardElement, inForm bool) {
		for _, element := range elements {
			inside := inForm || element.Tag == "form"
			if element.Tag == "input" || element.Tag == "select_static" {
				if !inside {
					t.Errorf("field %s is outside submitted form", element.Name)
				}
				fields[element.Name] = element
			}
			if element.Tag == "button" {
				var value map[string]any
				if err := json.Unmarshal(element.Value, &value); err != nil {
					t.Fatal(err)
				}
				action := managementActionFromCallback(&callback.CallBackAction{Value: value})
				buttons[action] = element
				if strings.HasPrefix(action, "bot.group_settings.") && (value["bridge_id"] != settings.BridgeID || value["chat_id"] != "group-a" || value["expected_version"] != "0" || value["group_scope"] == "") {
					t.Errorf("unscoped callback: %+v", value)
				}
			}
			walk(element.Elements, inside)
			for _, column := range element.Columns {
				walk(column.Elements, inside)
			}
		}
	}
	walk(doc.Body.Elements, false)
	for key := range groupSettingsFormDraft(settings) {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing complete settings field %q", key)
		}
	}
	for _, key := range []string{"group_agent", "group_runtime", "group_model", "group_reasoning"} {
		if fields[key].Tag != "select_static" || len(fields[key].Options) < 1 {
			t.Errorf("%s lacks searchable options", key)
		}
	}
	if fields["group_model"].InitialOption != "old-incompatible-model" || fields["group_reasoning"].InitialOption != "old-incompatible-effort" || fields["group_cwd"].DefaultValue != "/work/unsaved" || fields["group_max_total"].DefaultValue != "invalid-number" {
		t.Fatalf("refresh silently changed draft: %+v", fields)
	}
	if save := buttons[managementActionGroupSettingsSave]; save.ActionType != "form_submit" || save.Confirm == nil {
		t.Fatalf("save lacks whole-form submit and impact confirmation: %+v", save)
	}
	if buttons[managementActionGroupSettingsEdit].ActionType != "form_submit" {
		t.Fatal("refresh must preserve all form fields")
	}
	if saved, err := b.db.ReadGroupSettings(settings.BridgeID, settings.ChatID); err != nil || saved != nil {
		t.Fatalf("refresh persisted draft: %+v %v", saved, err)
	}
}

func TestGroupSettingsCardSeparatesCurrentSessionAndFutureDefaults(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	settings.InteractionMode, settings.DefaultAgent = config.FeishuInteractionSingleton, "builtin/claude"
	saved, err := b.applyGroupSettings(context.Background(), settings, 0, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertChat(LarkChat{BridgeId: saved.BridgeID, ChatID: saved.ChatID, RootMsgID: "fixed-root", TargetID: "local", SessionName: "original-session", AgentProfile: "builtin/codex", EffectiveModel: "original-model"}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.SetGroupSessionRoot(saved.BridgeID, saved.ChatID, saved.Version, "fixed-root"); err != nil {
		t.Fatal(err)
	}
	if err := b.sendCurrentGroupSettingsCard(context.Background(), "owner", saved.ChatID, "Test group"); err != nil {
		t.Fatal(err)
	}
	card := groupSettingsTestCard(t, cards)
	for _, text := range []string{"新会话默认专家", "builtin/claude", "当前会话专家", "builtin/codex", "original-model", "直接在本群回复", "original-session", "从空白上下文开始", "已接受的任务继续完成"} {
		if !strings.Contains(card, text) {
			t.Errorf("summary missing %q", text)
		}
	}
}

func TestGroupSettingsCardSendRejectsPrivateAndUnauthorized(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	if err := b.sendCurrentGroupSettingsCard(context.Background(), "member", "group-a", "Test"); err == nil {
		t.Fatal("sent owner card to member")
	}
	if err := b.sendCurrentGroupSettingsCard(context.Background(), "owner", "", "Test"); err == nil {
		t.Fatal("sent card with empty group")
	}
	settings := groupSettingsTestDraft(t, b, "group-a")
	if _, err := b.applyGroupSettings(context.Background(), settings, 0, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.EnsureWorkspaceKind(settings.BridgeID, settings.ChatID, WorkspaceKindPrivate); err != nil {
		t.Fatal(err)
	}
	if err := b.sendCurrentGroupSettingsCard(context.Background(), "owner", "group-a", "Test"); err == nil {
		t.Fatal("sent private chat settings card")
	}
	select {
	case card := <-cards:
		t.Fatalf("unexpected card: %s", card)
	default:
	}
}

func TestGroupSettingsCardPreservesGroupWatchEntry(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	saved, err := b.applyGroupSettings(context.Background(), settings, 0, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// A group's explicit thread mode must retain its watch entry even when the
	// legacy Bot default uses a continuous conversation.
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), config.FeishuInteractionThread)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.CommitSingletonBotSettingsWithLaunch(b.currentBridgeId(), state.Version, "builtin/codex", "", "", "", BotSession{
		BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "local", TargetName: "local", SessionName: "legacy",
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.sendCurrentGroupSettingsCard(context.Background(), "owner", "group-a", "Test group"); err != nil {
		t.Fatal(err)
	}
	if card := groupSettingsTestCard(t, cards); !strings.Contains(card, "将本群加入值守") {
		t.Fatalf("missing watch entry: %s", card)
	}
	for _, operation := range []string{"add", "remove"} {
		event := groupSettingsTestAction(b, saved, managementActionGroupSettingsWatch, nil)
		event.Event.Action.Value["operation"] = operation
		groupSettingsTestCallback(t, b, event, "success")
		card := groupSettingsTestCard(t, cards)
		joined := groupWatchConfigHasChat(b.groupWatchSnapshot(), "group-a")
		if joined != (operation == "add") {
			t.Fatalf("watch state after %s: %+v", operation, b.groupWatchSnapshot())
		}
		if operation == "add" && (!strings.Contains(card, "将本群移出值守") || !strings.Contains(card, "修改本群设置")) {
			t.Fatalf("watch update lost settings surface: %s", card)
		}
	}
}

// groupSettingsBudgetState builds a management state whose profile and model
// catalogs are large enough to push the editor past Feishu's element ceiling.
// The numbers mirror a real deployment: a builtin assistant exposing a wide
// model list alongside many pack-provided profiles.
func groupSettingsBudgetState(profiles, models int) managementState {
	state := managementState{DefaultAgent: "builtin/codex", ProfileName: "amux test0"}
	if models < 1 {
		models = 1
	}
	supported := make([]string, 0, models)
	for i := 0; i < models; i++ {
		supported = append(supported, fmt.Sprintf("model-%03d", i))
	}
	state.Profiles = append(state.Profiles, managementProfileSummary{
		Ref: "builtin/codex", Name: "Codex", AgentType: "codex", Model: supported[0],
		SupportedModels: supported,
	})
	for i := 1; i < profiles; i++ {
		state.Profiles = append(state.Profiles, managementProfileSummary{
			Ref: fmt.Sprintf("expert-agents/agent-%03d", i), Name: fmt.Sprintf("专家 %03d", i),
			AgentType: "codex", Model: supported[0], Description: "来自专家库的专家",
			SupportedModels: []string{supported[0]},
		})
	}
	runtimeModels := make([]agentprofile.RuntimeModelOption, 0, models)
	for _, ref := range supported {
		runtimeModels = append(runtimeModels, agentprofile.RuntimeModelOption{Ref: ref})
	}
	state.Runtimes = []agentprofile.RuntimeOption{{Ref: "codex", DisplayName: "Codex", Models: runtimeModels}}
	return state
}

func groupSettingsBudgetScope() map[string]string {
	return map[string]string{"chat_name": "Test group", "chat_id": "oc_test"}
}

// Every rendered page must stay inside Feishu's ceiling. managementCardTaggedNodeCount
// matches Feishu's own accounting on live cards, so the assertion is exact.
func TestGroupSettingsEditCardStaysWithinElementLimit(t *testing.T) {
	enableGroupSettingsExperiment(t)
	scope := groupSettingsBudgetScope()
	for _, tc := range []struct{ profiles, models int }{
		{1, 1}, {31, 32}, {60, 32}, {120, 32}, {31, 80}, {200, 32}, {200, 200},
	} {
		t.Run(fmt.Sprintf("profiles=%d/models=%d", tc.profiles, tc.models), func(t *testing.T) {
			state := groupSettingsBudgetState(tc.profiles, tc.models)
			draft := managementGroupSettingsDraft{"group_agent": "builtin/codex"}
			pages := paginateManagementGroupSettings(managementGroupSettingsEditFields(state, draft), draft, scope)
			if len(pages) == 0 {
				t.Fatal("pagination produced no pages")
			}
			for index, page := range pages {
				card := renderGroupSettingsEditCardPage(draft, scope, page, index, len(pages))
				nodes := managementCardTaggedNodeCount(card)
				if nodes > managementGroupSettingsLimitElements {
					t.Fatalf("page %d has %d tagged nodes, over the %d limit",
						index+1, nodes, managementGroupSettingsLimitElements)
				}
				if len(card) > managementGroupSettingsMaxBytes {
					t.Fatalf("page %d is %d bytes, over the %d budget",
						index+1, len(card), managementGroupSettingsMaxBytes)
				}
			}
		})
	}
}

// Pagination must not engage while the editor still fits, or every existing
// group would gain a page switcher for no reason.
func TestGroupSettingsEditCardAvoidsPaginationWhenItFits(t *testing.T) {
	enableGroupSettingsExperiment(t)
	scope := groupSettingsBudgetScope()
	state := groupSettingsBudgetState(31, 32)
	draft := managementGroupSettingsDraft{"group_agent": "builtin/codex"}
	pages := paginateManagementGroupSettings(managementGroupSettingsEditFields(state, draft), draft, scope)
	if len(pages) != 1 {
		t.Fatalf("editor paginated into %d pages while it still fits on one", len(pages))
	}
	card := renderGroupSettingsEditCardPage(draft, scope, pages[0], 0, 1)
	if nodes := managementCardTaggedNodeCount(card); nodes > managementGroupSettingsLimitElements {
		t.Fatalf("single page has %d tagged nodes, over the limit", nodes)
	}
}

// A page request past the end must fall back to the last page rather than
// render an empty card after the catalog shrinks.
func TestGroupSettingsEditCardClampsOutOfRangePage(t *testing.T) {
	enableGroupSettingsExperiment(t)
	state := groupSettingsBudgetState(31, 32)
	draft := managementGroupSettingsDraft{"group_agent": "builtin/codex"}
	card := renderGroupSettingsEditCardAtPage(state, draft, groupSettingsBudgetScope(), 99)
	if !strings.Contains(card, "group_settings_form") {
		t.Fatal("out-of-range page did not fall back to a rendered editor")
	}
	if nodes := managementCardTaggedNodeCount(card); nodes > managementGroupSettingsLimitElements {
		t.Fatalf("clamped page has %d tagged nodes, over the limit", nodes)
	}
}

// Truncating an oversized select must keep the selected value visible, or the
// card would show a value the user can neither see nor re-select.
func TestGroupSettingsOversizedSelectKeepsSelectedOption(t *testing.T) {
	enableGroupSettingsExperiment(t)
	scope := groupSettingsBudgetScope()
	state := groupSettingsBudgetState(200, 200)
	for _, selected := range []string{"model-000", "model-100", "model-199"} {
		t.Run(selected, func(t *testing.T) {
			draft := managementGroupSettingsDraft{"group_agent": "builtin/codex", "group_model": selected}
			rows := groupSettingsFitRows(managementGroupSettingsEditFields(state, draft), draft, scope)
			for _, row := range rows {
				selectElement, ok := managementRowSelect(&row)
				if !ok || selectElement.Name != "group_model" {
					continue
				}
				for _, option := range selectElement.Options {
					if option.Value == selected {
						return
					}
				}
				t.Fatalf("selected model %q was truncated out of %d options", selected, len(selectElement.Options))
			}
			t.Fatal("model select not found in editor rows")
		})
	}
}

// Switching pages must carry the draft, so values entered on one page survive
// a trip to another.
func TestGroupSettingsPageButtonCarriesDraft(t *testing.T) {
	enableGroupSettingsExperiment(t)
	scope := groupSettingsBudgetScope()
	draft := managementGroupSettingsDraft{
		"group_agent": "builtin/codex", "group_cwd": "/tmp/work", groupSettingsPageKey: "0",
	}
	card := renderGroupSettingsEditCardPage(draft, scope, []cardElement{managementHintElement("x")}, 0, 3)
	if !strings.Contains(card, groupSettingsPageKey) {
		t.Fatal("page indicator missing from a multi-page editor card")
	}
	values := managementButtonValue(groupSettingsScopedButton(managementActionGroupSettingsEdit, "下一页", "default", map[string]string{}))
	if values[managementActionKey] != managementActionGroupSettingsEdit {
		t.Fatalf("page button action = %q", values[managementActionKey])
	}
}

// Feishu rejects a card whose elements share a name (ErrCode 11310,
// "name(...) duplicate"), which is easy to trip once a second control reuses an
// action name. Guard every rendered page, including the paginated shape.
func TestGroupSettingsEditCardElementNamesAreUnique(t *testing.T) {
	enableGroupSettingsExperiment(t)
	scope := groupSettingsBudgetScope()
	for _, tc := range []struct{ profiles, models int }{{31, 32}, {60, 32}, {200, 200}} {
		t.Run(fmt.Sprintf("profiles=%d/models=%d", tc.profiles, tc.models), func(t *testing.T) {
			state := groupSettingsBudgetState(tc.profiles, tc.models)
			draft := managementGroupSettingsDraft{"group_agent": "builtin/codex"}
			pages := paginateManagementGroupSettings(managementGroupSettingsEditFields(state, draft), draft, scope)
			for index, page := range pages {
				card := renderGroupSettingsEditCardPage(draft, scope, page, index, len(pages))
				names := map[string]int{}
				collectCardElementNames(t, card, names)
				for name, count := range names {
					if count > 1 {
						t.Fatalf("page %d repeats element name %q %d times", index+1, name, count)
					}
				}
			}
		})
	}
}

// collectCardElementNames records every non-empty element name in a card.
func collectCardElementNames(t *testing.T, raw string, names map[string]int) {
	t.Helper()
	var document any
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			if name, ok := typed["name"].(string); ok && strings.TrimSpace(name) != "" {
				names[name]++
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(document)
}

// The production entry point must never hand Feishu an over-limit card, for any
// page index and any catalog size.
func TestGroupSettingsEditCardProductionEntryStaysWithinLimit(t *testing.T) {
	enableGroupSettingsExperiment(t)
	scope := groupSettingsBudgetScope()
	draft := managementGroupSettingsDraft{"group_agent": "builtin/codex"}
	for _, tc := range []struct{ profiles, models int }{{3, 4}, {31, 32}, {200, 200}, {500, 500}} {
		t.Run(fmt.Sprintf("profiles=%d/models=%d", tc.profiles, tc.models), func(t *testing.T) {
			state := groupSettingsBudgetState(tc.profiles, tc.models)
			for page := 0; page < 8; page++ {
				card := renderGroupSettingsEditCardAtPage(state, draft, scope, page)
				if nodes := managementCardTaggedNodeCount(card); nodes > managementGroupSettingsLimitElements {
					t.Fatalf("page %d rendered %d tagged nodes, over the %d limit",
						page, nodes, managementGroupSettingsLimitElements)
				}
			}
		})
	}
}
