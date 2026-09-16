package cli

import (
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestDefaultAmuxDirUsesAMUXHome(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), ".amux-real")
	t.Setenv("AMUX_HOME", amuxHome)
	if got := defaultAmuxDir(); got != amuxHome {
		t.Fatalf("defaultAmuxDir = %q, want %q", got, amuxHome)
	}
}

func TestGuardRuntimeStopRequiresForceForRunningSessions(t *testing.T) {
	sessions := []*sessionEntry{{Session: pb.Session{Name: "work", AgentType: "codex", State: "running"}}}
	err := guardRuntimeStopWithGuidance(false, sessions, "amux system stop --force")
	if err == nil {
		t.Fatal("guardRuntimeStop without force should fail for running sessions")
	}
	if !strings.Contains(err.Error(), "--force") || !strings.Contains(err.Error(), "work") {
		t.Fatalf("error = %v, want --force guidance and session name", err)
	}
	if err := guardRuntimeStopWithGuidance(true, sessions, "amux system stop --force"); err != nil {
		t.Fatalf("guardRuntimeStopWithGuidance with force = %v", err)
	}
	if err := guardRuntimeStopWithGuidance(false, []*sessionEntry{{Session: pb.Session{Name: "old", State: "archived"}}}, "amux system stop --force"); err != nil {
		t.Fatalf("archived sessions should not require force: %v", err)
	}
}

func TestGuardRuntimeStopWithGuidanceUsesCallerCommand(t *testing.T) {
	sessions := []*sessionEntry{{Session: pb.Session{Name: "work", AgentType: "codex", State: "running"}}}
	err := guardRuntimeStopWithGuidance(false, sessions, "amux system restart --force")
	if err == nil {
		t.Fatal("guardRuntimeStopWithGuidance without force should fail for running sessions")
	}
	text := err.Error()
	if !strings.Contains(text, "amux system restart --force") {
		t.Fatalf("error = %v, want system restart guidance", err)
	}
	if strings.Contains(text, "amux runtime stop --force") {
		t.Fatalf("error should not mention legacy runtime command: %v", err)
	}
}
