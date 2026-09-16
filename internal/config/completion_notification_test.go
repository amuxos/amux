package config_test

import (
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/viper"
)

func TestCompletionNotificationExperimentPersistsUntilRestart(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := writeConfig(t, "[hub]\naddr = \"localhost:7100\"\n[bridge.feishu.profiles.special.presentation]\ncompletion_notification_after_seconds = 30\n")
	requireLoad(t, path)
	initial, err := config.ReadCompletionNotificationSettings()
	if err != nil || initial.Enabled || initial.AfterSeconds != 60 {
		t.Fatalf("default settings = %+v, %v", initial, err)
	}
	want := config.CompletionNotificationSettings{Enabled: true, AfterSeconds: 90}
	if err := config.SaveCompletionNotificationSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := config.ReadCompletionNotificationSettings()
	if err != nil || got != want {
		t.Fatalf("saved settings = %+v, %v", got, err)
	}
	if config.GetBridgeFeishuPresentation().CompletionNotificationEnabled {
		t.Fatal("saving should not enable the running process before restart")
	}
	viper.Reset()
	requireLoad(t, path)
	if !config.GetBridgeFeishuPresentation().CompletionNotificationEnabled {
		t.Fatal("reload did not enable experiment")
	}
	if config.GetBridgeFeishuPresentationForProfile("special").CompletionNotificationAfterSeconds != 30 || config.GetHub().Addr != "localhost:7100" {
		t.Fatal("save changed unrelated settings or profile override")
	}
	if err := config.SaveCompletionNotificationSettings(config.CompletionNotificationSettings{AfterSeconds: -1}); err == nil {
		t.Fatal("invalid threshold was saved")
	}
}
