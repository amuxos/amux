package config_test

import (
	"os"
	"path/filepath"
	"reflect"
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
	if err != nil || !reflect.DeepEqual(got, want) {
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
	if !reflect.DeepEqual(config.GetExperimentsSettings(), want) {
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

func TestExperimentsMentionTurnInitiatorDefaultOffPersistsAndOlderCardsPreserveIt(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	requireLoad(t, writeConfig(t, ""))
	if got, err := config.ReadExperimentsSettings(); err != nil || got.MentionTurnInitiator {
		t.Fatalf("default settings = %+v, %v", got, err)
	}
	on := true
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{MentionTurnInitiator: &on}); err != nil {
		t.Fatal(err)
	}
	got, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{})
	if err != nil || !got.MentionTurnInitiator {
		t.Fatalf("older card update = %+v, %v", got, err)
	}
	got, err = config.ReadExperimentsSettings()
	if err != nil || !got.MentionTurnInitiator {
		t.Fatalf("saved settings = %+v, %v", got, err)
	}
}

func TestExperimentsReplyEndNotificationDefaultOffAndPersists(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := writeConfig(t, "[hub]\naddr = \"localhost:7123\"\n")
	requireLoad(t, path)
	if config.GetBridgeFeishuReplyEndNotification() {
		t.Fatal("reply-end notification must default off")
	}
	on := true
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{ReplyEndNotification: &on}); err != nil {
		t.Fatal(err)
	}
	saved, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{})
	if err != nil || !saved.ReplyEndNotification {
		t.Fatalf("older form must preserve switch: %+v %v", saved, err)
	}
	requireLoad(t, path)
	if !config.GetExperimentsSettings().ReplyEndNotification || config.GetHub().Addr != "localhost:7123" {
		t.Fatal("saved experiment did not survive reload or changed unrelated settings")
	}
	off := false
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{ReplyEndNotification: &off}); err != nil {
		t.Fatal(err)
	}
	saved, err = config.ReadExperimentsSettings()
	if err != nil || saved.ReplyEndNotification {
		t.Fatalf("explicit off did not persist: %+v %v", saved, err)
	}
}

func TestExperimentsModelRecoveryQueueWaitDefaultsPersistsAndValidates(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	requireLoad(t, writeConfig(t, ""))
	if got, err := config.ReadExperimentsSettings(); err != nil || got.ModelRecoveryQueueWaitSeconds != 60 {
		t.Fatalf("default settings = %+v, %v", got, err)
	}
	v30 := 30
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{ModelRecoveryQueueWaitSeconds: &v30}); err != nil {
		t.Fatal(err)
	}
	// Omitted field preserves saved value.
	got, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{})
	if err != nil || got.ModelRecoveryQueueWaitSeconds != 30 {
		t.Fatalf("older card update = %+v, %v", got, err)
	}
	// Zero disables queued recovery and persists distinctly from unset.
	v0 := 0
	got, err = config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{ModelRecoveryQueueWaitSeconds: &v0})
	if err != nil || got.ModelRecoveryQueueWaitSeconds != 0 {
		t.Fatalf("zero update = %+v, %v", got, err)
	}
	// Out-of-range values are rejected.
	badNeg := -1
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{ModelRecoveryQueueWaitSeconds: &badNeg}); err == nil {
		t.Fatal("expected negative seconds to fail")
	}
	badHigh := config.MaxFeishuModelRecoveryQueueWaitSeconds + 1
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{ModelRecoveryQueueWaitSeconds: &badHigh}); err == nil {
		t.Fatal("expected oversized seconds to fail")
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

func TestExperimentsCodexExecutablePreservesOmittedAndRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	viper.SetConfigFile(filepath.Join(dir, "config.toml"))
	t.Cleanup(func() { viper.SetConfigFile("") })
	program := "~/.local/bin/codex-team"
	saved, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{CodexExecutable: &program})
	if err != nil || saved.CodexExecutable != program {
		t.Fatalf("save: %+v %v", saved, err)
	}
	saved, err = config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{})
	if err != nil || saved.CodexExecutable != program {
		t.Fatalf("old card: %+v %v", saved, err)
	}
	invalid := "bad\nprogram"
	if _, err = config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{CodexExecutable: &invalid}); err == nil {
		t.Fatal("accepted multiline executable")
	}
	saved, err = config.ReadExperimentsSettings()
	if err != nil || saved.CodexExecutable != program {
		t.Fatalf("invalid update changed config: %+v %v", saved, err)
	}
	empty := ""
	saved, err = config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{CodexExecutable: &empty})
	if err != nil || saved.CodexExecutable != "" {
		t.Fatalf("reset: %+v %v", saved, err)
	}
}
