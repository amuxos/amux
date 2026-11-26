package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/config"
)

type stubLiveExperimentTarget struct {
	updates []config.ExperimentsSettings
}

func (s *stubLiveExperimentTarget) ApplyLiveExperimentSettings(saved config.ExperimentsSettings) {
	s.updates = append(s.updates, saved)
}

func TestLocalManagementConfigActionsBroadcastsLiveExperimentsAndPersistsCleanup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AMUX_HOME", home)
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[memory]\nenabled = false\n"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	if err := config.Load(cfgPath); err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	actions := newLocalManagementConfigActions()
	botA := &stubLiveExperimentTarget{}
	botB := &stubLiveExperimentTarget{}
	actions.RegisterTarget(botA)
	actions.RegisterTarget(botB)

	steer := true
	bg := true
	saved, err := actions.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{
		SteerForeground:     &steer,
		ShowBackgroundTasks: &bg,
	})
	if err != nil {
		t.Fatalf("UpdateExperimentsSettings: %v", err)
	}
	if !saved.SteerForeground || !saved.ShowBackgroundTasks {
		t.Fatalf("saved experiments = %+v, want both true", saved)
	}
	if len(botA.updates) != 1 || len(botB.updates) != 1 {
		t.Fatalf("broadcast counts = (%d, %d), want (1, 1)", len(botA.updates), len(botB.updates))
	}
	if !botB.updates[0].SteerForeground || !botB.updates[0].ShowBackgroundTasks {
		t.Fatalf("botB live update = %+v", botB.updates[0])
	}

	if err := actions.SaveDailyCleanupEnabled(context.Background(), false); err != nil {
		t.Fatalf("SaveDailyCleanupEnabled(false): %v", err)
	}
	cleanupSettings, err := actions.ReadDailyCleanupSettings(context.Background())
	if err != nil {
		t.Fatalf("ReadDailyCleanupSettings: %v", err)
	}
	if cleanupSettings.Enabled {
		t.Fatalf("cleanupSettings.Enabled = true, want false")
	}
}
