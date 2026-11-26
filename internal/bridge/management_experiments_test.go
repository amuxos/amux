package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/spf13/viper"
)

func TestManagementExperimentsOwnerSaveAndValidation(t *testing.T) {
	loadBridgeTestConfig(t, "[memory]\nenabled = false\n")
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	cards := make(chan string, 5)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		cards <- content
		return nil
	}
	event := settingsCallback("system.experiments.save_restart", nil)
	event.Event.Action.FormValue = map[string]interface{}{"completion_enabled": "true", "completion_seconds": "120", "memory_enabled": "true", "group_settings_enabled": "true", "codex_executable": "~/.local/bin/codex-team"}
	event.Event.Operator.OpenID = "not-owner"
	resp, handled, err := b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || resp.Toast.Type != "error" {
		t.Fatalf("non-owner save = %+v, %v", resp, err)
	}
	saved, err := config.ReadExperimentsSettings()
	if err != nil || saved.CompletionNotification.Enabled || saved.MemoryEnabled || saved.GroupSettingsEnabled || saved.CodexExecutable != "" {
		t.Fatalf("non-owner changed settings: %+v %v", saved, err)
	}
	event.Event.Operator.OpenID = "owner-open"
	resp, handled, err = b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || resp.Toast.Type != "success" {
		t.Fatalf("owner save = %+v, %v", resp, err)
	}
	saved, err = config.ReadExperimentsSettings()
	if err != nil || !saved.CompletionNotification.Enabled || saved.CompletionNotification.AfterSeconds != 120 || !saved.MemoryEnabled || !saved.GroupSettingsEnabled || saved.CodexExecutable != "~/.local/bin/codex-team" {
		t.Fatalf("owner settings = %+v, %v", saved, err)
	}
	select {
	case card := <-cards:
		if !json.Valid([]byte(card)) {
			t.Fatal("invalid card JSON")
		}
		for _, want := range []string{"确认重启", "已保存", "取消", "confirmation_nonce", managementActionExperimentsOpen} {
			if !strings.Contains(card, want) {
				t.Fatalf("card missing %s: %s", want, card)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no patched card")
	}
	if config.GetBridgeFeishuPresentation().CompletionNotificationEnabled || config.GetMemory().Enabled || config.GetBridgeFeishuGroupSettingsEnabled() {
		t.Fatal("saving must not enable the running experiment before restart confirmation")
	}
	resp, _, err = b.handleManagementCardAction(context.Background(), settingsCallback(managementActionExperimentsOpen, nil))
	if err != nil || resp.Toast.Type != "success" {
		t.Fatalf("cancel back to experiments = %+v %v", resp, err)
	}
	select {
	case card := <-cards:
		if !strings.Contains(card, "待重启") || !strings.Contains(card, "120 秒") {
			t.Fatalf("cancel must retain saved settings and pending state: %s", card)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no experiments card after cancellation")
	}
	event.Event.Action.FormValue["completion_seconds"] = "-1"
	resp, _, err = b.handleManagementCardAction(context.Background(), event)
	if err != nil || resp.Toast.Type != "error" {
		t.Fatalf("invalid save = %+v %v", resp, err)
	}
	after, _ := config.ReadExperimentsSettings()
	if !reflect.DeepEqual(after, saved) {
		t.Fatal("invalid callback changed saved settings")
	}
	event.Event.Action.FormValue["completion_seconds"] = "120"
	delete(event.Event.Action.FormValue, "memory_enabled")
	delete(event.Event.Action.FormValue, "group_settings_enabled")
	resp, _, err = b.handleManagementCardAction(context.Background(), event)
	if err != nil || resp.Toast.Type != "success" {
		t.Fatalf("older card save = %+v %v", resp, err)
	}
	after, _ = config.ReadExperimentsSettings()
	if !reflect.DeepEqual(after, saved) {
		t.Fatal("older card changed the saved memory value")
	}
}

func TestManagementExperimentsCompactList(t *testing.T) {
	saved := config.ExperimentsSettings{CompletionNotification: config.CompletionNotificationSettings{Enabled: true, AfterSeconds: 30}, MemoryEnabled: true}
	running := saved
	card := renderManagementExperimentsCard(saved, running)
	for _, want := range []string{"长会话完成通知", "30 秒", "高级设置", "保存并重启", "对当前实例的全部助手生效", "completion_seconds", "群独立配置", "group_settings_enabled", "Agent 记忆", "无需 Agent Profile", "memory_enabled", "用户隔离", "关闭阻止读写并保留记忆文件", "支持 Codex、Claude、Pi", "新对话", "旧对话", "完成回复后 @ 本轮发起人", "mention_turn_initiator_enabled"} {
		if !strings.Contains(card, want) {
			t.Fatalf("missing %q: %s", want, card)
		}
	}
	for _, unwanted := range []string{"已保存设置", "当前运行状态", "待重启", "刷新", "重启使配置生效"} {
		if strings.Contains(card, unwanted) {
			t.Fatalf("unexpected %q", unwanted)
		}
	}
	var doc cardDoc
	if err := json.Unmarshal([]byte(card), &doc); err != nil {
		t.Fatal(err)
	}
	var visit func([]cardElement)
	panels := 0
	visit = func(elements []cardElement) {
		for _, el := range elements {
			if el.Tag == "collapsible_panel" {
				panels++
				if el.Expanded || el.Header == nil || el.Header.Title.Content != "高级设置" {
					t.Fatal("advanced settings must start collapsed")
				}
			}
			visit(el.Elements)
			for _, col := range el.Columns {
				visit(col.Elements)
			}
		}
	}
	visit(doc.Body.Elements)
	if panels != 4 {
		t.Fatalf("panels = %d", panels)
	}
	running.CompletionNotification.AfterSeconds = 60
	if !strings.Contains(renderManagementExperimentsCard(saved, running), "待重启") {
		t.Fatal("missing pending indicator")
	}
}

func TestManagementExperimentsInvalidSwitchRejectsWholeForm(t *testing.T) {
	for _, field := range []string{"memory_enabled", "group_settings_enabled", "mention_turn_initiator_enabled", "reply_end_enabled"} {
		for _, invalid := range []any{"", "yes", "TRUE", false, 1, nil} {
			t.Run(fmt.Sprintf("%s_%T_%v", field, invalid, invalid), func(t *testing.T) {
				loadBridgeTestConfig(t, "[memory]\nenabled = true\n[bridge.feishu.group_settings]\nenabled = true\n[hub]\naddr = \"localhost:7123\"\n")
				b, _ := newBridgeForInputTest(t)
				b.secrets = &Secrets{OpenId: "owner-open"}
				b.managementCardPatch = func(context.Context, string, string) error {
					t.Error("invalid form must not change the card")
					return nil
				}
				before, err := os.ReadFile(viper.ConfigFileUsed())
				if err != nil {
					t.Fatal(err)
				}
				event := settingsCallback(managementActionExperimentsSaveRestart, nil)
				event.Event.Action.FormValue = map[string]interface{}{"completion_enabled": "true", "completion_seconds": "120", "memory_enabled": "false", "group_settings_enabled": "false"}
				event.Event.Action.FormValue[field] = invalid
				resp, _, err := b.handleManagementCardAction(context.Background(), event)
				if err != nil || resp.Toast.Type != "error" {
					t.Fatalf("invalid experiment save = %+v %v", resp, err)
				}
				after, err := os.ReadFile(viper.ConfigFileUsed())
				if err != nil || string(after) != string(before) {
					t.Fatalf("invalid form changed config: %s, %v", after, err)
				}
			})
		}
	}
}

func TestManagementReplyEndNotificationAppliesLiveAndOldFormsPreserveIt(t *testing.T) {
	loadBridgeTestConfig(t, "")
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	other, _ := newBridgeForInputTest(t)
	other.profile = "other-bot"
	event := settingsCallback(managementActionExperimentsSaveRestart, map[string]any{managementSurfaceKey: string(managementSurfaceAmux)})
	event.Event.Action.FormValue = map[string]any{"completion_enabled": "false", "reply_end_enabled": "true"}
	response, err := b.handleCardAction(context.Background(), event)
	if err != nil || response == nil || response.Toast.Type != "success" {
		t.Fatalf("save = %+v %v", response, err)
	}
	saved, err := config.ReadExperimentsSettings()
	if err != nil || !saved.ReplyEndNotification || !b.runningExperimentsSettings().ReplyEndNotification || !other.currentReplyEndNotification() {
		t.Fatalf("saved/live = %+v / %+v (%v)", saved, b.runningExperimentsSettings(), err)
	}
	if config.GetBridgeFeishuReplyEndNotification() {
		t.Fatal("live save must not mutate Viper startup state")
	}
	delete(event.Event.Action.FormValue, "reply_end_enabled")
	response, err = b.handleCardAction(context.Background(), event)
	if err != nil || response.Toast.Type != "success" || !b.replyEndNotification.Load() {
		t.Fatalf("older form disabled live switch: %+v %v", response, err)
	}
	card := renderManagementExperimentsCard(saved, b.runningExperimentsSettings())
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		selected := selectManagementCardLocale(card, locale)
		if !strings.Contains(selected, "reply_end_enabled") || strings.Contains(selected, "management.reply.end.") {
			t.Fatalf("missing localized reply-end switch (%s): %s", locale, selected)
		}
	}
	event.Event.Action.FormValue["reply_end_enabled"] = "false"
	response, err = b.handleCardAction(context.Background(), event)
	if err != nil || response.Toast.Type != "success" || b.replyEndNotification.Load() || other.currentReplyEndNotification() {
		t.Fatalf("disable = %+v %v", response, err)
	}
}

func TestManagementExperimentsOldCardPreservesOmittedSwitches(t *testing.T) {
	for _, omitted := range []string{"both", "group", "memory"} {
		t.Run(omitted, func(t *testing.T) {
			saved := config.ExperimentsSettings{GroupSettingsEnabled: true, MemoryEnabled: true, CompletionNotification: config.CompletionNotificationSettings{AfterSeconds: 60}}
			action := &callback.CallBackAction{FormValue: map[string]any{"completion_enabled": "true", "completion_seconds": "90"}}
			if omitted == "group" {
				action.FormValue["memory_enabled"] = "false"
			}
			if omitted == "memory" {
				action.FormValue["group_settings_enabled"] = "false"
			}
			update, err := managementExperimentsSettingsFromCallback(action, saved)
			if err != nil || !update.CompletionNotification.Enabled || update.CompletionNotification.AfterSeconds != 90 {
				t.Fatalf("old card completion update = %+v %v", update, err)
			}
			if (update.GroupSettingsEnabled == nil) != (omitted != "memory") || (update.MemoryEnabled == nil) != (omitted != "group") {
				t.Fatalf("omitted switches were materialized from a stale snapshot: %+v", update)
			}
			if update.GroupSettingsEnabled != nil && *update.GroupSettingsEnabled || update.MemoryEnabled != nil && *update.MemoryEnabled {
				t.Fatal("explicit false switch was not preserved")
			}
		})
	}
}

func TestManagementExperimentsMentionTurnInitiatorCallbackPreservesOmittedValue(t *testing.T) {
	saved := config.ExperimentsSettings{MentionTurnInitiator: true}
	action := &callback.CallBackAction{FormValue: map[string]any{"completion_enabled": "false"}}
	update, err := managementExperimentsSettingsFromCallback(action, saved)
	if err != nil || update.MentionTurnInitiator != nil {
		t.Fatalf("older card update = %+v, %v", update, err)
	}
	action.FormValue["mention_turn_initiator_enabled"] = "false"
	update, err = managementExperimentsSettingsFromCallback(action, saved)
	if err != nil || update.MentionTurnInitiator == nil || *update.MentionTurnInitiator {
		t.Fatalf("explicit off update = %+v, %v", update, err)
	}
}

func TestManagementExperimentsModelRecoveryQueueWaitCallbackAndBilingualCard(t *testing.T) {
	saved := config.ExperimentsSettings{ModelRecoveryQueueWaitSeconds: 60}
	// Blank or omitted value keeps the existing setting.
	action := &callback.CallBackAction{FormValue: map[string]any{
		"completion_enabled": "false",
		"recovery_wait_sec":  "   ",
	}}
	update, err := managementExperimentsSettingsFromCallback(action, saved)
	if err != nil || update.ModelRecoveryQueueWaitSeconds != nil {
		t.Fatalf("blank wait seconds update = %+v, %v", update, err)
	}

	// Valid integer (including 0 to disable) is parsed.
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{raw: "0", want: 0},
		{raw: "15", want: 15},
		{raw: "3600", want: 3600},
	} {
		action.FormValue["recovery_wait_sec"] = tc.raw
		update, err = managementExperimentsSettingsFromCallback(action, saved)
		if err != nil || update.ModelRecoveryQueueWaitSeconds == nil || *update.ModelRecoveryQueueWaitSeconds != tc.want {
			t.Fatalf("raw=%q update = %+v, %v", tc.raw, update, err)
		}
	}

	// Legacy long field name remains accepted as fallback.
	legacyAction := &callback.CallBackAction{FormValue: map[string]any{
		"completion_enabled":                "false",
		"model_recovery_queue_wait_seconds": "42",
	}}
	update, err = managementExperimentsSettingsFromCallback(legacyAction, saved)
	if err != nil || update.ModelRecoveryQueueWaitSeconds == nil || *update.ModelRecoveryQueueWaitSeconds != 42 {
		t.Fatalf("legacy field update = %+v, %v", update, err)
	}

	// Out-of-range or non-numeric values are rejected.
	for _, bad := range []string{"-1", "3601", "abc"} {
		action.FormValue["recovery_wait_sec"] = bad
		if _, err := managementExperimentsSettingsFromCallback(action, saved); err == nil {
			t.Fatalf("raw=%q unexpectedly accepted", bad)
		}
	}

	// Bilingual rendering covers both active and disabled states and stays
	// well within Feishu CardKit 2.0 component (200) and size (30 KB) budgets.
	for _, sec := range []int{45, 0} {
		rawCard := renderManagementExperimentsCard(
			config.ExperimentsSettings{ModelRecoveryQueueWaitSeconds: sec},
			config.ExperimentsSettings{ModelRecoveryQueueWaitSeconds: sec},
		)
		if len(rawCard) >= 26*1024 {
			t.Fatalf("sec=%d: experiments card bytes = %d, want < 26 KiB to leave headroom for extensions", sec, len(rawCard))
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(rawCard), &doc); err != nil {
			t.Fatalf("sec=%d: invalid card JSON: %v", sec, err)
		}
		tags := 0
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if tag, ok := x["tag"].(string); ok {
					tags++
					if tag == "input" {
						if name, _ := x["name"].(string); len(name) > 20 {
							t.Fatalf("sec=%d: input name %q exceeds 20 chars (%d)", sec, name, len(name))
						}
					}
				}
				for _, child := range x {
					walk(child)
				}
			case []any:
				for _, child := range x {
					walk(child)
				}
			}
		}
		walk(doc["body"])
		if tags >= 170 {
			t.Fatalf("sec=%d: public experiments card tag count = %d, want < 170 to stay under Feishu 200-tag limit with extensions", sec, tags)
		}

		zhCard := selectManagementCardLocale(rawCard, i18n.Chinese)
		enCard := selectManagementCardLocale(rawCard, i18n.English)
		if !strings.Contains(zhCard, "recovery_wait_sec") || !strings.Contains(enCard, "recovery_wait_sec") {
			t.Fatalf("sec=%d: missing input field in rendered card", sec)
		}
		if sec == 45 {
			if !strings.Contains(zhCard, "45 秒") || !strings.Contains(enCard, "45s") {
				t.Fatalf("sec=45: expected formatted threshold in zh/en cards:\nzh=%s\nen=%s", zhCard, enCard)
			}
		} else {
			if !strings.Contains(zhCard, "已关闭") || !strings.Contains(enCard, "Off") {
				t.Fatalf("sec=0: expected disabled copy in zh/en cards:\nzh=%s\nen=%s", zhCard, enCard)
			}
		}
	}

	// Saving through the management callback persists and applies live to Bridge.
	loadBridgeTestConfig(t, "")
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	saveEvent := settingsCallback(managementActionExperimentsSaveRestart, map[string]any{
		managementSurfaceKey: string(managementSurfaceAmux),
	})
	saveEvent.Event.Action.FormValue = map[string]any{
		"completion_enabled": "false",
		"recovery_wait_sec":  "25",
	}
	resp, err := b.handleCardAction(context.Background(), saveEvent)
	if err != nil || resp == nil {
		t.Fatalf("save experiments: %+v, %v", resp, err)
	}
	if got := b.currentModelRecoveryQueueWaitSeconds(); got != 25 {
		t.Fatalf("live bridge queue wait = %d, want 25", got)
	}
	persisted, err := config.ReadExperimentsSettings()
	if err != nil || persisted.ModelRecoveryQueueWaitSeconds != 25 {
		t.Fatalf("persisted config queue wait = %+v, %v (want 25)", persisted, err)
	}
}

func TestManagementExperimentsOpenReturnsValidCardComponents(t *testing.T) {
	loadBridgeTestConfig(t, "[memory]\nenabled = true\n")
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	event := settingsCallback(managementActionExperimentsOpen, map[string]any{managementSurfaceKey: string(managementSurfaceAmux)})
	resp, err := b.handleCardAction(context.Background(), event)
	if err != nil || resp == nil || resp.Card == nil {
		t.Fatalf("open experiments: %+v, %v", resp, err)
	}
	idPattern := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)
	for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese, "fr"} {
		card := selectManagementCardLocale(string(resp.Card.Data.(json.RawMessage)), locale)
		findManagementButtonByAction(t, card, managementActionStatusOpen)
		findManagementButtonByAction(t, card, managementActionExperimentsSaveRestart)
		var doc cardDoc
		if err := json.Unmarshal([]byte(card), &doc); err != nil {
			t.Fatal(err)
		}
		var visit func([]cardElement)
		visit = func(elements []cardElement) {
			for _, el := range elements {
				if el.ElementID != "" && !idPattern.MatchString(el.ElementID) {
					t.Errorf("%s: invalid element ID %q", locale, el.ElementID)
				}
				if strings.HasSuffix(el.Width, "px") {
					t.Errorf("%s: invalid fixed width %q on %s", locale, el.Width, el.Tag)
				}
				visit(el.Elements)
				for _, col := range el.Columns {
					visit(col.Elements)
				}
			}
		}
		visit(doc.Body.Elements)
	}
}

func TestManagementExperimentsPendingStateBelongsToEachItem(t *testing.T) {
	fields := []string{"completion_enabled", "group_settings_enabled", "memory_enabled"}
	for _, changed := range fields {
		t.Run(changed, func(t *testing.T) {
			saved := config.ExperimentsSettings{CompletionNotification: config.CompletionNotificationSettings{AfterSeconds: 30}}
			running := saved
			if changed == "memory_enabled" {
				saved.MemoryEnabled = true
			} else if changed == "group_settings_enabled" {
				saved.GroupSettingsEnabled = true
			} else {
				saved.CompletionNotification.Enabled = true
			}
			var doc cardDoc
			if err := json.Unmarshal([]byte(renderManagementExperimentsCard(saved, running)), &doc); err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, form := range doc.Body.Elements {
				if form.Name != "experiments_form" {
					continue
				}
				for _, item := range form.Elements {
					body, err := json.Marshal(item)
					if err != nil {
						t.Fatal(err)
					}
					for _, field := range fields {
						if strings.Contains(string(body), `"name":"`+field+`"`) {
							found++
							if strings.Contains(string(body), "待重启") != (field == changed) {
								t.Fatalf("wrong pending state for %s", field)
							}
						}
					}
				}
			}
			if found != 3 {
				t.Fatalf("experiment items = %d", found)
			}
		})
	}
}

func TestCompletionNotificationExperimentDisabledSuppressesAllNewNotices(t *testing.T) {
	for _, body := range []string{"", "[bridge.feishu.presentation]\ncompletion_notification_enabled = false\ncompletion_notification_after_seconds = 1\n"} {
		loadBridgeTestConfig(t, body)
		b, _ := newBridgeForInputTest(t)
		b.threadNotice = func(context.Context, string, string) error {
			t.Fatal("disabled experiment sent a notification")
			return nil
		}
		b.dmInteractive = func(context.Context, string, string) (string, string, error) {
			t.Fatal("disabled experiment sent a DM card")
			return "", "", nil
		}
		if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: "feishu:test", RootMsgID: "root", MessageID: "message", TurnID: "turn", OpenID: "ou_user"}}); err != nil {
			t.Fatal(err)
		}
		for _, status := range []string{"done", "waiting", "failed"} {
			if err := b.notifyTurnCompletion(context.Background(), &LarkChat{BridgeId: "feishu:test", RootMsgID: "root"}, "turn", status, "", time.Hour); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCodexExecutableExperimentBilingual(t *testing.T) {
	saved := config.ExperimentsSettings{CodexExecutable: "~/bin/codex-team"}
	card := renderManagementExperimentsCard(saved, config.ExperimentsSettings{})
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		rendered := selectManagementCardLocale(card, locale)
		if !json.Valid([]byte(rendered)) || !strings.Contains(rendered, "codex_executable") || !strings.Contains(rendered, "~/bin/codex-team") || strings.Contains(rendered, "management.codex.executable.") {
			t.Fatalf("%s card: %s", locale, rendered)
		}
	}
}
