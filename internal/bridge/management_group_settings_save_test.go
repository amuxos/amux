package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/spf13/viper"
)

type gatedGroupSettingsActions struct {
	*fakeManagementAgentPackActions
	entered chan struct{}
	release chan struct{}
	fail    error
}

func (a *gatedGroupSettingsActions) WithRegistryStableContext(ctx context.Context, commit func() error) error {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.release:
		if a.fail != nil {
			return a.fail
		}
		return commit()
	}
}

func TestGroupSettingsSaveAcknowledgesBeforeRegistryWaitAndDeduplicates(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	actions := &gatedGroupSettingsActions{fakeManagementAgentPackActions: &fakeManagementAgentPackActions{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	b.managementPackActions = actions
	event := groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, map[string]any{"group_cwd": "/original"})
	start := time.Now()
	response := groupSettingsTestCallback(t, b, event, "success")
	if time.Since(start) > 250*time.Millisecond || !strings.Contains(response.Toast.Content, "正在保存") {
		t.Fatalf("save did not promptly acknowledge: %+v", response.Toast)
	}
	select {
	case <-actions.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("save did not reach registry wait")
	}
	// Duplicates must not invalidate the pending result version.
	duplicate := groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, map[string]any{"group_cwd": "/duplicate"})
	groupSettingsTestCallback(t, b, duplicate, "warning")
	event.Event.Action.FormValue["group_cwd"] = "/mutated-after-ack"
	close(actions.release)
	card := groupSettingsTestCard(t, cards)
	if !strings.Contains(card, "/original") {
		t.Fatalf("save result was suppressed or callback draft was not copied: %s", card)
	}
	got := groupSettingsTestDraft(t, b, "group-a")
	if got.Version != 1 || got.DefaultCwd != "/original" {
		t.Fatalf("saved %+v", got)
	}
}

func TestGroupSettingsSaveAcknowledgesWhileDatabaseConnectionBusy(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	conn, err := b.db.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	groupSettingsTestCallback(t, b, groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, nil), "success")
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("callback waited for the database")
	}
	conn.Close()
	groupSettingsTestCard(t, cards)
	if got := groupSettingsTestDraft(t, b, "group-a"); got.Version != 1 {
		t.Fatalf("save did not finish after the connection became available: %+v", got)
	}
}

func TestGroupSettingsSaveTimeoutResultRetainsDraftAndDoesNotWrite(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	actions := &gatedGroupSettingsActions{fakeManagementAgentPackActions: &fakeManagementAgentPackActions{}, entered: make(chan struct{}, 1), release: make(chan struct{}), fail: context.DeadlineExceeded}
	close(actions.release)
	b.managementPackActions = actions
	groupSettingsTestCallback(t, b, groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, map[string]any{"group_cwd": "/keep-this-draft"}), "success")
	card := groupSettingsTestCard(t, cards)
	for _, text := range []string{"保存超时", "/keep-this-draft", "稍后继续提交"} {
		if !strings.Contains(card, text) {
			t.Fatalf("timeout card missing %q: %s", text, card)
		}
	}
	if got, err := b.db.ReadGroupSettings(b.currentBridgeId(), "group-a"); err != nil || got != nil {
		t.Fatalf("timed out save committed: %+v %v", got, err)
	}
	b.settingsMu.Lock()
	pending := b.groupSettingsSaves["group-a"]
	b.settingsMu.Unlock()
	if pending {
		t.Fatal("failed save retained the duplicate guard")
	}
}

func TestGroupSettingsSaveNavigationSuppressesLateResultNotCommit(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	actions := &gatedGroupSettingsActions{fakeManagementAgentPackActions: &fakeManagementAgentPackActions{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	b.managementPackActions = actions
	groupSettingsTestCallback(t, b, groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, nil), "success")
	<-actions.entered
	<-cards // loading
	b.advanceModelServiceEditor("card")
	close(actions.release)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		b.settingsMu.Lock()
		pending := b.groupSettingsSaves["group-a"]
		b.settingsMu.Unlock()
		if !pending {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("save did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	if got := groupSettingsTestDraft(t, b, "group-a"); got.Version != 1 {
		t.Fatalf("navigation revoked an accepted save: %+v", got)
	}
	select {
	case card := <-cards:
		t.Fatalf("late result overwrote new view: %s", card)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestGroupSettingsSaveStatusI18n(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"management.group_settings.saving", "management.group_settings.save_pending", "management.group_settings.save_failed", "management.group_settings.save_timeout", "management.group_settings.retry_after_timeout", "management.group_settings.cleanup_partial", "management.group_settings.saved_refresh_failed"} {
		text := managementCopy(key)
		if text.For(i18n.English) == text.For(i18n.Chinese) || strings.Contains(text.For(i18n.English), "management.") {
			t.Errorf("missing translation for %s", key)
		}
		card := renderGroupSettingsSaveStatusCard(&GroupSettings{ChatID: "group-a"}, "Example", text, "blue")
		for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
			selected := selectManagementCardLocale(card, locale)
			if strings.Contains(selected, "management.group_settings.") {
				t.Errorf("raw key in %s card", locale)
			}
		}
	}
}

func TestGroupSettingsSaveReportsCleanupPartialSuccess(t *testing.T) {
	b, cards := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "group-a")
	// A directory is an invalid config file, without depending on permission
	// behavior (the gate can also run as root).
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	groupSettingsTestCallback(t, b, groupSettingsTestAction(b, settings, managementActionGroupSettingsSave, map[string]any{"daily_cleanup": "false", "group_interaction": "thread"}), "success")
	card := groupSettingsTestCard(t, cards)
	if !strings.Contains(card, managementDefaultText(managementCopy("management.group_settings.cleanup_partial"))) {
		t.Fatalf("missing partial-success result: %s", card)
	}
	if got := groupSettingsTestDraft(t, b, "group-a"); got.Version != 1 {
		t.Fatalf("group policy was not committed: %+v", got)
	}
}
