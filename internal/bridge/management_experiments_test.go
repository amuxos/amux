package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestManagementExperimentsOldCardPreservesGroupSetting(t *testing.T) {
	saved := config.ExperimentsSettings{GroupSettingsEnabled: true, CompletionNotification: config.CompletionNotificationSettings{AfterSeconds: 60}}
	action := &callback.CallBackAction{FormValue: map[string]any{"completion_enabled": "true", "completion_seconds": "90"}}
	got, err := managementExperimentsSettingsFromCallback(action, saved)
	if err != nil || !got.GroupSettingsEnabled || !got.CompletionNotification.Enabled || got.CompletionNotification.AfterSeconds != 90 {
		t.Fatalf("old card lost saved group setting: %+v %v", got, err)
	}
	for _, invalid := range []any{"", "invalid", nil} {
		action.FormValue["group_settings_enabled"] = invalid
		got, err := managementExperimentsSettingsFromCallback(action, saved)
		if err == nil || got != saved {
			t.Fatalf("invalid group setting partially changed form: %+v %v", got, err)
		}
	}
}

func TestManagementExperimentsOwnerSaveAndValidation(t *testing.T) {
	loadBridgeTestConfig(t, "")
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	cards := make(chan string, 5)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		cards <- content
		return nil
	}
	event := settingsCallback("system.experiments.save_restart", nil)
	event.Event.Action.FormValue = map[string]interface{}{"completion_enabled": "true", "completion_seconds": "120", "group_settings_enabled": "true"}
	event.Event.Operator.OpenID = "not-owner"
	resp, handled, err := b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || resp.Toast.Type != "error" {
		t.Fatalf("non-owner save = %+v, %v", resp, err)
	}
	saved, err := config.ReadExperimentsSettings()
	if err != nil || saved.CompletionNotification.Enabled {
		t.Fatalf("non-owner changed settings: %+v %v", saved, err)
	}
	event.Event.Operator.OpenID = "owner-open"
	resp, handled, err = b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || resp.Toast.Type != "success" {
		t.Fatalf("owner save = %+v, %v", resp, err)
	}
	saved, err = config.ReadExperimentsSettings()
	if err != nil || !saved.CompletionNotification.Enabled || !saved.GroupSettingsEnabled || saved.CompletionNotification.AfterSeconds != 120 {
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
	if config.GetExperimentsSettings().CompletionNotification.Enabled || config.GetExperimentsSettings().GroupSettingsEnabled {
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
	if after != saved {
		t.Fatal("invalid callback changed saved settings")
	}
}

func TestManagementExperimentsCompactList(t *testing.T) {
	saved := config.ExperimentsSettings{CompletionNotification: config.CompletionNotificationSettings{Enabled: true, AfterSeconds: 30}}
	running := saved
	card := renderManagementExperimentsCard(saved, running)
	for _, want := range []string{"长会话完成通知", "群独立配置", "group_settings_enabled", "默认关闭", "30 秒", "高级设置", "保存并重启", "对当前实例的全部助手生效", "completion_seconds"} {
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
	if panels != 1 {
		t.Fatalf("panels = %d", panels)
	}
	running.GroupSettingsEnabled = true
	if !strings.Contains(renderManagementExperimentsCard(saved, running), "待重启") {
		t.Fatal("missing pending indicator")
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
