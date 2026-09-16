package schedule

import (
	"path/filepath"
	"testing"
)

func TestDefaultSchedulePathUsesAMUXHome(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), "amux-home")
	t.Setenv("AMUX_HOME", amuxHome)

	if got, want := DefaultSchedulePath(), filepath.Join(amuxHome, "schedule.db"); got != want {
		t.Fatalf("DefaultSchedulePath() = %q, want %q", got, want)
	}
}
