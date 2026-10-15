package config_test

import (
	"os"
	"strings"
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
	if err != nil || initial.GroupSettingsEnabled || initial.MemoryEnabled || config.GetBridgeFeishuGroupSettingsEnabled() || config.GetMemory().Enabled {
		t.Fatalf("default group experiment = %+v %v", initial, err)
	}
	want := config.ExperimentsSettings{CompletionNotification: config.CompletionNotificationSettings{Enabled: true, AfterSeconds: 90}, GroupSettingsEnabled: true, MemoryEnabled: true}
	if err := config.SaveExperimentsSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := config.ReadExperimentsSettings()
	if err != nil || got != want {
		t.Fatalf("saved = %+v %v", got, err)
	}
	if config.GetBridgeFeishuGroupSettingsEnabled() || config.GetMemory().Enabled || config.GetBridgeFeishuPresentation().CompletionNotificationEnabled {
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
		t.Fatal("reload did not enable all experiments")
	}
	if config.GetHub().Addr != "localhost:7100" || config.GetBridgeFeishuPresentationForProfile("special").CompletionNotificationAfterSeconds != 30 {
		t.Fatal("changed unrelated config")
	}
	want.GroupSettingsEnabled = false
	want.MemoryEnabled = false
	if err := config.SaveExperimentsSettings(want); err != nil {
		t.Fatal(err)
	}
	if !config.GetBridgeFeishuGroupSettingsEnabled() || !config.GetMemory().Enabled {
		t.Fatal("disable affected running Hub")
	}
	viper.Reset()
	requireLoad(t, path)
	if config.GetBridgeFeishuGroupSettingsEnabled() || config.GetMemory().Enabled {
		t.Fatal("reload did not disable group settings")
	}
}

func TestExperimentsOlderCardPreservesLatestSavedSettings(t *testing.T) {
	for _, omitted := range []string{"both", "group", "memory"} {
		t.Run(omitted, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			path := writeConfig(t, "[memory]\nenabled = false\n[bridge.feishu.group_settings]\nenabled = false\n")
			requireLoad(t, path)
			initial, err := config.ReadExperimentsSettings()
			if err != nil {
				t.Fatal(err)
			}
			// Prepare an old card, then change both switches before it saves.
			// Omitted fields must retain their latest value under the file lock.
			on, off := true, false
			older := config.ExperimentsSettingsUpdate{CompletionNotification: initial.CompletionNotification}
			if omitted == "group" {
				older.MemoryEnabled = &off
			}
			if omitted == "memory" {
				older.GroupSettingsEnabled = &off
			}
			if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{
				CompletionNotification: initial.CompletionNotification, GroupSettingsEnabled: &on, MemoryEnabled: &on,
			}); err != nil {
				t.Fatal(err)
			}
			older.CompletionNotification.Enabled = true
			saved, err := config.UpdateExperimentsSettings(older)
			if err != nil || !saved.CompletionNotification.Enabled || saved.GroupSettingsEnabled != (omitted != "memory") || saved.MemoryEnabled != (omitted != "group") {
				t.Fatalf("older card lost a concurrent update or ignored explicit false: %+v, %v", saved, err)
			}
			if err := config.SaveCompletionNotificationSettings(initial.CompletionNotification); err != nil {
				t.Fatal(err)
			}
			got, err := config.ReadExperimentsSettings()
			if err != nil || got.GroupSettingsEnabled != saved.GroupSettingsEnabled || got.MemoryEnabled != saved.MemoryEnabled {
				t.Fatalf("legacy setter changed other experiments: %+v, %v", got, err)
			}
		})
	}
}

func TestExperimentsSteerForegroundPersists(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := writeConfig(t, "[hub]\naddr = \"localhost:7100\"\n")
	requireLoad(t, path)
	if got, err := config.ReadExperimentsSettings(); err != nil || got.SteerForeground {
		t.Fatalf("default = %+v %v", got, err)
	}
	on := true
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{SteerForeground: &on}); err != nil {
		t.Fatal(err)
	}
	got, err := config.ReadExperimentsSettings()
	if err != nil || !got.SteerForeground {
		t.Fatalf("saved = %+v %v", got, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "steer_foreground = true") {
		t.Fatalf("config file = %s", raw)
	}
}

func TestExperimentsShowBackgroundTasksDefaultOffAndPersists(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := writeConfig(t, "[hub]\naddr = \"localhost:7100\"\n")
	requireLoad(t, path)
	if got, err := config.ReadExperimentsSettings(); err != nil || got.ShowBackgroundTasks {
		t.Fatalf("default = %+v %v", got, err)
	}
	on := true
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{ShowBackgroundTasks: &on}); err != nil {
		t.Fatal(err)
	}
	got, err := config.ReadExperimentsSettings()
	if err != nil || !got.ShowBackgroundTasks || got.SteerForeground {
		t.Fatalf("saved = %+v %v", got, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "show_background_tasks = true") {
		t.Fatalf("config file = %s", raw)
	}
}

func TestLiveSteerForegroundOverridesConfig(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Cleanup(func() { config.SetLiveSteerForeground(false) })
	config.SetLiveSteerForeground(true)
	if !config.LiveSteerForeground() {
		t.Fatal("live on not reported")
	}
	config.SetLiveSteerForeground(false)
	if config.LiveSteerForeground() {
		t.Fatal("live off not reported")
	}
}
