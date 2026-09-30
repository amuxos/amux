package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestGroupWatchSettingsRouteByChatAndSkipPausedPolling(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner", AppId: "bot"}
	hub.targets = []*pb.Target{{Id: "local", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	b.setGroupWatchConfig(config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"group_a", "group_b", "group_paused"}, SenderTypes: []string{"app"},
		ChatSettings: map[string]config.BridgeFeishuGroupWatchChatConfig{
			"group_a":      {Enabled: true, AgentProfileRef: "builtin/codex", RuntimeRef: "codex", MaxActiveSessionsPerChat: 1, IdleTimeoutSeconds: 120, ThinkingProcess: "hidden"},
			"group_b":      {Enabled: true, AgentProfileRef: "builtin/claude", RuntimeRef: "claude", MaxActiveSessionsPerChat: 4, IdleTimeoutSeconds: 600, ThinkingProcess: "expanded"},
			"group_paused": {Enabled: false},
		},
	})
	for _, id := range []string{"group_a", "group_b", "group_paused"} {
		if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{ChatID: id, MessageID: id + "_root", SenderType: "app", SenderID: "alert", Text: "investigate"}); err != nil {
			t.Fatal(err)
		}
	}
	runs := []*pb.RunRequest{}
	for _, route := range hub.routes {
		if route.method == "Run" {
			runs = append(runs, route.payload.(*pb.RunRequest))
		}
	}
	if len(runs) != 2 || runs[0].AgentProfile != "builtin/codex" || runs[0].IdleTimeoutSeconds != 120 || runs[1].AgentProfile != "builtin/claude" || runs[1].IdleTimeoutSeconds != 600 {
		t.Fatalf("per-group launch: %+v", runs)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: runs[0].Name, State: "running"}, {Name: runs[1].Name, State: "running"}}}
	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{ChatID: "group_a", MessageID: "a_second", SenderType: "app", SenderID: "alert", Text: "another alert"}); err != nil {
		t.Fatal(err)
	}
	for _, route := range hub.routes {
		if route.method == "Run" && route.payload.(*pb.RunRequest).Name != runs[0].Name && route.payload.(*pb.RunRequest).Name != runs[1].Name {
			t.Fatal("group A bypassed its own capacity")
		}
	}
	polled := map[string]bool{}
	b.groupWatchListMessages = func(_ context.Context, chatID string, _, _ time.Time) ([]*larkim.Message, error) {
		polled[chatID] = true
		return nil, nil
	}
	if err := b.pollGroupWatchOnce(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if polled["group_paused"] || !polled["group_a"] || !polled["group_b"] {
		t.Fatalf("polled groups = %v", polled)
	}
	// The snapshot must not expose the mutable map held by the Bridge.
	copy := b.groupWatchSnapshot()
	delete(copy.ChatSettings, "group_a")
	if _, ok := b.groupWatchSnapshot().ChatSettings["group_a"]; !ok {
		t.Fatal("snapshot aliases live map")
	}
}

func TestGroupWatchSettingsDraftKeepsScopeAndAllFieldsAcrossRefresh(t *testing.T) {
	state := managementState{DefaultAgent: "builtin/codex", DefaultRuntimeRef: "codex", Profiles: []managementProfileSummary{{Ref: "builtin/codex", AgentType: "codex"}}, Runtimes: []agentprofile.RuntimeOption{{Ref: "codex"}}, GroupWatch: config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"group_a", "group_b"}}}
	draft := managementGroupWatchDraft{ChatID: "group_a", ChatName: "Group A", Version: "0", Enabled: "false", MaxActive: "7", IdleSeconds: "180", Mode: "custom", AgentRef: "builtin/codex", RuntimeRef: "codex", ThinkingProcess: "hidden"}
	values := map[string]any{}
	for key, value := range managementGroupWatchDraftValues(draft) {
		values[key] = value
	}
	action := &callback.CallBackAction{Value: values, FormValue: map[string]any{"group_watch_max_active": "11", "group_watch_idle_seconds": "300"}, Name: "group_watch_model_ref", Option: managementProfileDefaultModelOption}
	got := managementGroupWatchDraftFromCallback(action, state)
	if got.ChatID != "group_a" || got.Enabled != "false" || got.MaxActive != "11" || got.IdleSeconds != "300" || got.ThinkingProcess != "hidden" || got.Version != "0" {
		t.Fatalf("lost draft: %+v", got)
	}
	if err := managementGroupWatchSubmissionError(action, state); err != "" {
		t.Fatalf("valid form rejected: %v", err)
	}
	action.FormValue["group_watch_idle_seconds"] = "59"
	if err := managementGroupWatchSubmissionError(action, state); err == "" {
		t.Fatal("invalid timeout accepted")
	}
	action.FormValue["group_watch_idle_seconds"] = "300"
	state.GroupWatch.ChatSettings = map[string]config.BridgeFeishuGroupWatchChatConfig{"group_a": {Version: 1}}
	if err := managementGroupWatchSubmissionError(action, state); err == "" {
		t.Fatal("stale card accepted")
	}
}

func TestGroupWatchSettingsEditorExposesAllFieldsWithinCardBudget(t *testing.T) {
	state := managementState{DefaultAgent: "builtin/codex", DefaultRuntimeRef: "codex", Runtimes: []agentprofile.RuntimeOption{{Ref: "codex"}}}
	for i := 0; i < 80; i++ {
		state.Profiles = append(state.Profiles, managementProfileSummary{Ref: fmt.Sprintf("expert/%d", i), Name: fmt.Sprintf("Expert %d", i), AgentType: "codex"})
	}
	draft := managementGroupWatchDraft{ChatID: "group_a", ChatName: "A", Enabled: "true", Mode: "custom", AgentRef: "expert/1", RuntimeRef: "codex", ThinkingProcess: "expanded", MaxActive: "50", IdleSeconds: "10800"}
	seen := map[string]bool{}
	for page := 0; page < 12; page++ {
		draft.Page = page
		card := renderGroupWatchEditCard(state, draft)
		for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
			selected := selectManagementCardLocale(card, locale)
			var doc any
			if err := json.Unmarshal([]byte(selected), &doc); err != nil {
				t.Fatal(err)
			}
			assertManagementFormScope(t, doc, false)
			if count := managementCardTaggedNodeCount(selected); count > 200 {
				t.Fatalf("page %d has %d elements", page, count)
			}
			if strings.Contains(selected, "management.watch.") {
				t.Fatalf("untranslated key on page %d", page)
			}
		}
		for _, key := range []string{"group_watch_enabled", "group_watch_mode", "group_watch_agent", "group_watch_runtime_ref", "group_watch_model_ref", "group_watch_reasoning_effort", "group_watch_thinking_process", "group_watch_max_active", "group_watch_idle_seconds"} {
			if strings.Contains(card, `"name":"`+key+`"`) {
				seen[key] = true
			}
		}
	}
	if len(seen) != 9 {
		t.Fatalf("missing editable fields: %v", seen)
	}
}

func TestGroupWatchSettingsPresentationIsIndependent(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.coder.group_watch]
thinking_process = "collapsed"
[bridge.feishu.profiles.coder.group_watch.chat_settings.group_a]
thinking_process = "hidden"
[bridge.feishu.profiles.coder.group_watch.chat_settings.group_b]
thinking_process = "expanded"
`)
	b := &Bridge{profile: "coder"}
	for chatID, want := range map[string]string{"group_a": "hidden", "group_b": "expanded", "legacy": "collapsed"} {
		if got := b.streamThinkingProcess(true, chatID); got != want {
			t.Fatalf("%s: %s, want %s", chatID, got, want)
		}
	}
}

func TestGroupWatchSettingsSaveCallbackPersistsOneGroupAndReopens(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.coder.group_watch]
enabled = true
chat_ids = ["group_a", "group_b"]
max_active_sessions_per_chat = 50
idle_timeout_seconds = 10800
`)
	b, _ := newBridgeForInputTest(t)
	b.profile = "coder"
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.setGroupWatchConfig(config.GetBridgeFeishuGroupWatchForProfile("coder"))
	b.chatNameLookup = func(_ context.Context, chatID string) (string, error) { return chatID, nil }
	cards := make(chan string, 10)
	b.managementCardPatch = func(_ context.Context, _, content string) error { cards <- content; return nil }
	draft := managementGroupWatchDraft{ChatID: "group_a", ChatName: "A", Version: "0", Enabled: "false", MaxActive: "9", IdleSeconds: "240", Mode: "inherit", ThinkingProcess: "hidden"}
	values := map[string]any{}
	for key, value := range managementGroupWatchDraftValues(draft) {
		values[key] = value
	}
	event := settingsCallback(managementActionGroupWatchSave, values)
	response, handled, err := b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || response.Toast.Type != "success" {
		t.Fatalf("save: %+v, %v", response, err)
	}
	card := waitForManagementCard(t, cards, func(card string) bool { return strings.Contains(card, "已保存本群值守设置") })
	if !strings.Contains(card, `"chat_id":"group_a"`) || !strings.Contains(card, "240") {
		t.Fatalf("wrong saved summary: %s", card)
	}
	current := b.groupWatchSnapshot()
	if got := current.ForChat("group_a"); got.Enabled || got.MaxActiveSessionsPerChat != 9 || got.IdleTimeoutSeconds != 240 || got.ThinkingProcess != "hidden" {
		t.Fatalf("saved A: %+v", got)
	}
	if got := current.ForChat("group_b"); !got.Enabled || got.MaxActiveSessionsPerChat != 50 || got.IdleTimeoutSeconds != 10800 {
		t.Fatalf("B changed: %+v", got)
	}
	reopened := managementGroupWatchDraftFromCallback(&callback.CallBackAction{Value: map[string]any{"chat_id": "group_a"}}, b.groupWatchManagementState(b.managementState(), "group_a"))
	if reopened.Version != "1" || reopened.Enabled != "false" || reopened.MaxActive != "9" || reopened.IdleSeconds != "240" {
		t.Fatalf("reopened: %+v", reopened)
	}
	// The old card cannot silently overwrite the new version.
	response, _, err = b.handleManagementCardAction(context.Background(), event)
	if err != nil || response.Toast.Type != "error" {
		t.Fatalf("stale callback: %+v %v", response, err)
	}
	// A shared member never gets the owner's configuration authority.
	event.Event.Operator.OpenID = "member"
	response, _, err = b.handleManagementCardAction(context.Background(), event)
	if err != nil || response.Toast.Type != "error" {
		t.Fatalf("member callback: %+v %v", response, err)
	}
	if b.groupWatchSnapshot().ChatSettings["group_a"].Version != 1 {
		t.Fatal("rejected callback changed settings")
	}
}

func TestGroupWatchSettingsInheritGroupExecutionAndModelDiscoveryScope(t *testing.T) {
	b, _ := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group_a")
	settings.InteractionMode = config.FeishuInteractionThread
	settings.IsolationRequired = true
	settings.DefaultAgent, settings.DefaultRuntimeRef = "builtin/claude", "claude"
	settings.DefaultModelRef, settings.DefaultReasoningEffort = "group-model", "high"
	settings.DefaultTarget = "group-target"
	if _, err := b.db.SaveGroupSettings(*settings, 0); err != nil {
		t.Fatal(err)
	}
	base := managementState{Mode: config.FeishuInteractionSingleton, DefaultAgent: "builtin/codex", DefaultRuntimeRef: "codex", DefaultModelRef: "bot-model"}
	state := b.groupWatchManagementState(base, "group_a")
	if state.Mode != interactionModeThreadIsolated || state.DefaultAgent != "builtin/claude" || state.DefaultRuntimeRef != "claude" || state.DefaultModelRef != "group-model" || state.DefaultReasoningEffort != "high" || state.NativeModelsTargetRef != "group-target" {
		t.Fatalf("group editor inherited Bot scope: %+v", state)
	}
	if other := b.groupWatchManagementState(base, "group_b"); other.DefaultModelRef != "bot-model" || other.Mode != config.FeishuInteractionSingleton {
		t.Fatalf("group policy leaked: %+v", other)
	}
}

func TestGroupWatchSettingsCardsShowEffectiveGroupPolicy(t *testing.T) {
	state := managementState{Mode: "thread", GroupWatch: config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"group_a"}, AgentProfileRef: "global-watch-expert",
		ChatSettings: map[string]config.BridgeFeishuGroupWatchChatConfig{"group_a": {Enabled: false, AgentProfileRef: "group-watch-expert"}},
	}}
	card := renderCurrentGroupSettingsCard(state, &GroupSettings{ChatID: "group_a", InteractionMode: "thread"}, "", nil, map[string]string{"chat_id": "group_a"})
	if !strings.Contains(card, "group-watch-expert") || strings.Contains(card, "global-watch-expert") {
		t.Fatalf("group card displays another group's expert: %s", card)
	}
	card = renderCurrentGroupWatchCard(state, "group_a", "A", true)
	if !strings.Contains(card, managementDefaultText(managementCopy("management.paused"))) {
		t.Fatalf("group card hides its paused state: %s", card)
	}
}

func TestGroupWatchSettingsEditorSelectsRefreshModelsOutsideForm(t *testing.T) {
	state := managementState{DefaultAgent: "builtin/codex", Profiles: []managementProfileSummary{{Ref: "builtin/codex", Name: "Codex", AgentType: "codex"}}, GroupWatch: config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"group_a"}},
		Runtimes: []agentprofile.RuntimeOption{
			{Ref: "codex", DisplayName: "Codex", Models: []agentprofile.RuntimeModelOption{{Ref: "codex-model-a", DisplayName: "Codex A"}, {Ref: "codex-model-b", DisplayName: "Codex B"}}},
			{Ref: "claude", DisplayName: "Claude", Models: []agentprofile.RuntimeModelOption{{Ref: "claude-model-x", DisplayName: "Claude X"}}},
		}}
	draft := managementGroupWatchDraft{ChatID: "group_a", ChatName: "A", Version: "0", Enabled: "true", MaxActive: "100", IdleSeconds: "10800", Mode: "custom", AgentRef: "builtin/codex", RuntimeRef: "codex", ModelRef: "codex-model-a", ThinkingProcess: "expanded"}
	card := renderGroupWatchEditCard(state, draft)
	var doc any
	if err := json.Unmarshal([]byte(card), &doc); err != nil {
		t.Fatal(err)
	}
	// Every select must sit outside the form container and carry its callback
	// behavior: Feishu only delivers selection callbacks for components that
	// are not embedded in a form, and this is what refreshes the model list
	// when the runtime changes.
	var walk func(node any, inForm bool)
	walk = func(node any, inForm bool) {
		switch element := node.(type) {
		case map[string]any:
			tag, _ := element["tag"].(string)
			inForm = inForm || tag == "form"
			name, _ := element["name"].(string)
			if tag == "select_static" && strings.HasPrefix(name, "group_watch_") {
				if inForm {
					t.Errorf("select %s rendered inside the form; selection changes would not fire", name)
				}
				if list, ok := element["behaviors"].([]any); !ok || len(list) == 0 {
					t.Errorf("select %s has no callback behavior", name)
				}
			}
			if tag == "input" && (name == "group_watch_max_active" || name == "group_watch_idle_seconds") && !inForm {
				t.Errorf("input %s must stay inside the form so typing reaches the save submit", name)
			}
			for key, child := range element {
				if key != "value" && key != "behaviors" {
					walk(child, inForm)
				}
			}
		case []any:
			for _, child := range element {
				walk(child, inForm)
			}
		}
	}
	walk(doc, false)
	// Simulate the runtime switch behavior callback: the freshly rendered card
	// must offer the new runtime's models without any manual refresh.
	values := map[string]any{}
	for key, value := range managementGroupWatchDraftValues(draft) {
		values[key] = value
	}
	action := &callback.CallBackAction{Name: "group_watch_runtime_ref", Option: "claude", Value: values, FormValue: map[string]any{}}
	switched := managementGroupWatchDraftFromCallback(action, state)
	if switched.RuntimeRef != "claude" {
		t.Fatalf("runtime switch draft: %+v", switched)
	}
	refreshed := renderGroupWatchEditCard(state, switched)
	var refreshedDoc any
	if err := json.Unmarshal([]byte(refreshed), &refreshedDoc); err != nil {
		t.Fatal(err)
	}
	options := map[string]bool{}
	var collect func(node any)
	collect = func(node any) {
		switch element := node.(type) {
		case map[string]any:
			if element["tag"] == "select_static" && element["name"] == "group_watch_model_ref" {
				for _, raw := range element["options"].([]any) {
					option, _ := raw.(map[string]any)
					value, _ := option["value"].(string)
					options[value] = true
				}
			}
			for key, child := range element {
				if key != "value" && key != "behaviors" {
					collect(child)
				}
			}
		case []any:
			for _, child := range element {
				collect(child)
			}
		}
	}
	collect(refreshedDoc)
	if !options["claude-model-x"] || options["codex-model-a"] || options["codex-model-b"] {
		t.Fatalf("model options did not follow the runtime switch: %v", options)
	}
}
