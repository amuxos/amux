package hub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/hubweb"
)

type stubLiveExperimentTarget struct {
	updates []config.ExperimentsSettings
}

func TestTurnProcessExperimentStartsLiveAndSurvivesRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AMUX_HOME", home)
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[bridge.feishu.presentation]\ncompletion_notification_enabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Load(cfgPath); err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reserved.Close()
	options := config.TurnViewerConfig{Addr: reserved.Addr().String()}
	keyPath := filepath.Join(home, "secrets", "turn-viewer.key")
	viewer := &managedTurnViewer{ctx: context.Background(), config: options, keyPath: keyPath}
	defer viewer.setEnabled(false)
	actions := newLocalManagementConfigActions()
	actions.turnViewer = viewer
	target := &stubLiveExperimentTarget{}
	actions.RegisterTarget(target)
	sidebar, inline, bubble := config.FeishuTurnProcessSidebar, config.FeishuTurnProcessInline, config.FeishuTurnProcessBubble
	enable := config.ExperimentsSettingsUpdate{TurnProcessMode: &sidebar}
	if _, err := actions.UpdateExperimentsSettings(enable); !errors.Is(err, bridge.ErrTurnProcessViewerUnavailable) {
		t.Fatalf("busy port should fail to enable: %v", err)
	}
	if saved, err := actions.ReadExperimentsSettings(); err != nil || saved.TurnProcessMode != inline || len(target.updates) != 0 {
		t.Fatalf("failed enable changed configuration or live settings: %+v, %v", saved, err)
	}
	_ = reserved.Close()
	saved, err := actions.UpdateExperimentsSettings(enable)
	if err != nil || saved.TurnProcessMode != sidebar || !saved.CompletionNotification.Enabled ||
		len(target.updates) != 1 || target.updates[0].TurnProcessMode != sidebar {
		t.Fatalf("enable did not persist and broadcast only the requested setting: %+v, %v", saved, err)
	}
	baseURL, key := viewer.settings()
	link := baseURL + "/t/" + hubweb.SignTurnProcessToken(key, "target", "session", "turn", false, false, time.Now().Add(time.Minute))
	client := &http.Client{Timeout: 3 * time.Second}
	assertStatus := func(path string, want int) {
		t.Helper()
		resp, err := client.Get(path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("viewer status = %d, want %d", resp.StatusCode, want)
		}
	}
	assertStatus(link, http.StatusOK)
	assertStatus(baseURL+"/api/sessions", http.StatusNotFound)

	_ = viewer.setEnabled(false)
	if err := config.Load(cfgPath); err != nil || config.GetFeishuTurnProcessMode() != sidebar {
		t.Fatalf("restart did not read the experiment: %v", err)
	}
	restarted := &managedTurnViewer{ctx: context.Background(), config: options, keyPath: keyPath}
	defer restarted.setEnabled(false)
	if err := restarted.setEnabled(config.GetFeishuTurnProcessMode() == sidebar); err != nil {
		t.Fatal(err)
	}
	assertStatus(link, http.StatusOK)
	actions.turnViewer = restarted
	if saved, err := actions.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{TurnProcessMode: &bubble}); err != nil || saved.TurnProcessMode != bubble {
		t.Fatalf("disable: %+v, %v", saved, err)
	}
	if baseURL, _ := restarted.settings(); baseURL != "" {
		t.Fatal("viewer still running in bubble mode")
	}
	if err := config.Load(cfgPath); err != nil || config.GetFeishuTurnProcessMode() != bubble {
		t.Fatalf("restart did not read bubble mode: %v", err)
	}
	if saved, err := actions.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{TurnProcessMode: &inline}); err != nil || saved.TurnProcessMode != inline {
		t.Fatalf("restore inline: %+v, %v", saved, err)
	}
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
