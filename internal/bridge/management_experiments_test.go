package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
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
	event.Event.Action.FormValue = map[string]interface{}{"completion_enabled": "true", "completion_seconds": "120", "memory_enabled": "true", "group_settings_enabled": "true"}
	event.Event.Operator.OpenID = "not-owner"
	resp, handled, err := b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || resp.Toast.Type != "error" {
		t.Fatalf("non-owner save = %+v, %v", resp, err)
	}
	saved, err := config.ReadExperimentsSettings()
	if err != nil || saved.CompletionNotification.Enabled || saved.MemoryEnabled || saved.GroupSettingsEnabled {
		t.Fatalf("non-owner changed settings: %+v %v", saved, err)
	}
	event.Event.Operator.OpenID = "owner-open"
	resp, handled, err = b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || resp.Toast.Type != "success" {
		t.Fatalf("owner save = %+v, %v", resp, err)
	}
	saved, err = config.ReadExperimentsSettings()
	if err != nil || !saved.CompletionNotification.Enabled || saved.CompletionNotification.AfterSeconds != 120 || !saved.MemoryEnabled || !saved.GroupSettingsEnabled {
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
	if after != saved {
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
	if after != saved {
		t.Fatal("older card changed the saved memory value")
	}
}

func TestManagementExperimentsCompactList(t *testing.T) {
	saved := config.ExperimentsSettings{CompletionNotification: config.CompletionNotificationSettings{Enabled: true, AfterSeconds: 30}, MemoryEnabled: true}
	running := saved
	card := renderManagementExperimentsCard(saved, running)
	for _, want := range []string{"长会话完成通知", "30 秒", "高级设置", "保存并重启", "对当前实例的全部助手生效", "completion_seconds", "群独立配置", "group_settings_enabled", "Agent 记忆", "无需 Agent Profile", "memory_enabled", "用户隔离", "关闭阻止读写并保留记忆文件", "支持 Codex、Claude、Pi", "新对话", "旧对话"} {
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
	if panels != 2 {
		t.Fatalf("panels = %d", panels)
	}
	running.CompletionNotification.AfterSeconds = 60
	if !strings.Contains(renderManagementExperimentsCard(saved, running), "待重启") {
		t.Fatal("missing pending indicator")
	}
}

func TestManagementExperimentsInvalidSwitchRejectsWholeForm(t *testing.T) {
	for _, field := range []string{"memory_enabled", "group_settings_enabled"} {
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

func TestManagementExperimentsPendingStateBelongsToEachItem(t *testing.T) {
	for _, changed := range []string{"completion_notification_item", "group_settings_item", "agent_memory_item"} {
		t.Run(changed, func(t *testing.T) {
			saved := config.ExperimentsSettings{CompletionNotification: config.CompletionNotificationSettings{AfterSeconds: 30}}
			running := saved
			if changed == "agent_memory_item" {
				saved.MemoryEnabled = true
			} else if changed == "group_settings_item" {
				saved.GroupSettingsEnabled = true
			} else {
				saved.CompletionNotification.Enabled = true
			}
			var doc cardDoc
			if err := json.Unmarshal([]byte(renderManagementExperimentsCard(saved, running)), &doc); err != nil {
				t.Fatal(err)
			}
			found := 0
			var visit func([]cardElement)
			visit = func(elements []cardElement) {
				for _, el := range elements {
					if el.ElementID == "completion_notification_item" || el.ElementID == "group_settings_item" || el.ElementID == "agent_memory_item" {
						found++
						body, err := json.Marshal(el)
						if err != nil || strings.Contains(string(body), "待重启") != (el.ElementID == changed) {
							t.Fatalf("wrong pending state for %s: %s, %v", el.ElementID, body, err)
						}
					}
					visit(el.Elements)
					for _, col := range el.Columns {
						visit(col.Elements)
					}
				}
			}
			visit(doc.Body.Elements)
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
