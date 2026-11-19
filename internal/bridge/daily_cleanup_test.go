package bridge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/amuxos/amux/internal/cleanup"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/spf13/viper"
)

func TestDailyCleanupChoiceDefaultAndOldCards(t *testing.T) {
	state := managementState{Mode: interactionModeThreadIsolated, DailyCleanup: "true"}
	draft := managementDraftFromState(state)
	if draft.DailyCleanup != "true" {
		t.Fatal("default is not enabled")
	}
	changed := managementDraftFromCallback(&callback.CallBackAction{Name: "daily_cleanup", Option: "false"}, state)
	if changed.DailyCleanup != "false" || !changed.DailyCleanupSubmitted {
		t.Fatalf("changed=%+v", changed)
	}
	state.DailyCleanup = "false"
	old := managementDraftFromCallback(&callback.CallBackAction{Value: map[string]any{"interaction": interactionModeThreadIsolated}}, state)
	if old.DailyCleanup != "false" || old.DailyCleanupSubmitted {
		t.Fatalf("old card overwrote choice: %+v", old)
	}
}
func TestDailyCleanupCardsBothLanguagesAndPrivacy(t *testing.T) {
	state := managementState{Mode: interactionModeThreadIsolated, DailyCleanup: "true", DefaultAgent: "builtin/codex"}
	draft := managementDraftFromState(state)
	report := cleanup.Report{ID: "report-id", Target: "local", FinishedAt: time.Now(), Steps: []cleanup.Step{{Name: "task_sessions", Deleted: 1}, {Name: "workspaces", Deleted: 2, Skipped: 3, Bytes: 1024, Errors: []string{"/private/secret-workspace/secret-token"}}}}
	group := groupSettingsFormDraft(&GroupSettings{InteractionMode: "thread", IsolationRequired: true})
	cards := []string{renderSettingsEditCard(state, draft), renderOnboardingCard(state), renderGroupSettingsEditCardAtPage(state, group, map[string]string{}, 0), renderDailyCleanupReport(report)}
	for _, card := range cards {
		for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
			selected := selectManagementCardLocale(card, locale)
			var doc any
			if err := json.Unmarshal([]byte(selected), &doc); err != nil {
				t.Fatal(err)
			}
			assertManagementFormScope(t, doc, false)
			if strings.Contains(selected, "secret-token") || strings.Contains(selected, "secret-workspace") {
				t.Fatal("report leaked diagnostic paths")
			}
			walkManagementTestText(doc, func(text string) {
				if strings.Contains(text, "%!") || strings.HasPrefix(text, "cleanup.") {
					t.Errorf("invalid copy %q", text)
				}
				text = strings.ReplaceAll(strings.ReplaceAll(text, "开启协作模式", ""), "关闭协作模式", "")
				if locale == i18n.English && strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
					t.Errorf("untranslated English %q", text)
				}
			})
		}
	}
}
func TestDailyCleanupNotificationOnlyGoesToOwner(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "app", OpenId: "owner"}
	sends := 0
	b.dmInteractive = func(_ context.Context, openID, card string) (string, string, error) {
		sends++
		if openID != "owner" {
			t.Errorf("recipient=%s", openID)
		}
		if !strings.Contains(card, "cleanup") && !strings.Contains(card, "清理") {
			t.Error("missing report")
		}
		return "message", "", nil
	}
	if err := b.NotifyDailyCleanup(context.Background(), cleanup.Report{ID: "empty", Target: "local", FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatalf("sends=%d", sends)
	}
	b.secrets.OpenId = ""
	if err := b.NotifyDailyCleanup(context.Background(), cleanup.Report{}); err == nil || sends != 1 {
		t.Fatal("sent without an owner")
	}
}
func TestDailyCleanupGroupChoiceSaveAndUnauthorizedRejection(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigFile(filepath.Join(t.TempDir(), "config.toml"))
	b, cards := groupSettingsCardTestBridge(t)
	b.SetIsolationSupportChecker(func() (bool, string) { return true, "" })
	settings := groupSettingsTestDraft(t, b, "group-a")
	fields := map[string]any{"group_interaction": interactionModeThreadIsolated, "daily_cleanup": "false"}
	event := groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, fields)
	groupSettingsTestCallback(t, b, event, "success")
	groupSettingsTestCard(t, cards)
	saved, err := config.ReadDailyCleanupSettings()
	if err != nil || saved.Enabled {
		t.Fatalf("choice not saved: %+v %v", saved, err)
	}
	current := groupSettingsTestDraft(t, b, "group-a")
	event = groupSettingsTestAction(b, current, managementActionGroupSettingsSave, map[string]any{"daily_cleanup": "true"})
	event.Event.Operator.OpenID = "intruder"
	_, _, _ = b.handleManagementCardAction(context.Background(), event)
	saved, err = config.ReadDailyCleanupSettings()
	if err != nil || saved.Enabled {
		t.Fatal("non-owner changed cleanup")
	}
}

func TestDailyCleanupOrdinaryTopicsChoiceAndSave(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigFile(filepath.Join(t.TempDir(), "config.toml"))
	b, cards := groupSettingsCardTestBridge(t)
	b.SetIsolationSupportChecker(func() (bool, string) { return false, "isolation disabled" })
	settings := groupSettingsTestDraft(t, b, "ordinary-topic")
	event := groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, map[string]any{"group_interaction": config.FeishuInteractionThread, "daily_cleanup": "false"})
	groupSettingsTestCallback(t, b, event, "success")
	groupSettingsTestCard(t, cards)
	saved, err := config.ReadDailyCleanupSettings()
	if err != nil || saved.Enabled {
		t.Fatalf("ordinary topic choice not saved: %+v %v", saved, err)
	}
	state := managementState{Mode: config.FeishuInteractionThread, DailyCleanup: "true", DefaultAgent: "builtin/codex"}
	draft := managementDraftFromState(state)
	group := groupSettingsFormDraft(&GroupSettings{InteractionMode: config.FeishuInteractionThread})
	for _, card := range []string{renderSettingsEditCard(state, draft), renderOnboardingCard(state), renderGroupSettingsEditCardAtPage(state, group, map[string]string{}, 0)} {
		if !strings.Contains(card, `"name":"daily_cleanup"`) {
			t.Fatal("ordinary topics missing cleanup choice")
		}
	}
}
