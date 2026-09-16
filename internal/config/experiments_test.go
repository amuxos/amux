package config_test

import (
	"os"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/viper"
)

func TestExperimentsAtomicSaveAndRestart(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := writeConfig(t, "[hub]\naddr = \"localhost:7100\"\n[bridge.feishu.profiles.special.presentation]\ncompletion_notification_after_seconds = 30\n")
	requireLoad(t, path)
	initial, err := config.ReadExperimentsSettings()
	if err != nil || initial.GroupSettingsEnabled || config.GetBridgeFeishuGroupSettingsEnabled() {
		t.Fatalf("default group experiment = %+v %v", initial, err)
	}
	want := config.ExperimentsSettings{CompletionNotification: config.CompletionNotificationSettings{Enabled: true, AfterSeconds: 90}, GroupSettingsEnabled: true}
	if err := config.SaveExperimentsSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := config.ReadExperimentsSettings()
	if err != nil || got != want {
		t.Fatalf("saved = %+v %v", got, err)
	}
	if config.GetBridgeFeishuGroupSettingsEnabled() || config.GetBridgeFeishuPresentation().CompletionNotificationEnabled {
		t.Fatal("save changed running experiments")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := want
	invalid.GroupSettingsEnabled = false
	invalid.CompletionNotification.AfterSeconds = -1
	if err := config.SaveExperimentsSettings(invalid); err == nil {
		t.Fatal("accepted invalid form")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed save changed config: %v", err)
	}
	viper.Reset()
	requireLoad(t, path)
	if config.GetExperimentsSettings() != want {
		t.Fatal("reload did not enable both experiments")
	}
	if config.GetHub().Addr != "localhost:7100" || config.GetBridgeFeishuPresentationForProfile("special").CompletionNotificationAfterSeconds != 30 {
		t.Fatal("changed unrelated config")
	}
	want.GroupSettingsEnabled = false
	if err := config.SaveExperimentsSettings(want); err != nil {
		t.Fatal(err)
	}
	if !config.GetBridgeFeishuGroupSettingsEnabled() {
		t.Fatal("disable affected running Hub")
	}
	viper.Reset()
	requireLoad(t, path)
	if config.GetBridgeFeishuGroupSettingsEnabled() {
		t.Fatal("reload did not disable group settings")
	}
}
