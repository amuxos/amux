package cli

import (
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestShouldAutoDeferCwdSwitchForCurrentSession(t *testing.T) {
	t.Setenv("AMUX_SESSION_ID", "session-123")
	if !shouldAutoDeferCwdSwitch("session-123", false, false) {
		t.Fatal("current session should auto-defer cwd switch")
	}
	if shouldAutoDeferCwdSwitch("other-session", false, false) {
		t.Fatal("different session must not auto-defer cwd switch")
	}
	if shouldAutoDeferCwdSwitch("session-123", true, false) {
		t.Fatal("explicit interrupt must override auto-defer")
	}
	if shouldAutoDeferCwdSwitch("session-123", false, true) {
		t.Fatal("explicit no-wait must override auto-defer")
	}
}

func TestShouldAutoDeferCwdSwitchRequiresSessionEnvironment(t *testing.T) {
	t.Setenv("AMUX_SESSION_ID", "")
	if shouldAutoDeferCwdSwitch("session-123", false, false) {
		t.Fatal("empty AMUX_SESSION_ID must not auto-defer")
	}
}

func TestAmuxdChangeCwdRequestPreservesDeferredMode(t *testing.T) {
	got := amuxdChangeCwdRequest("target-1", &pb.ChangeCwdRequest{
		Name: "work", Cwd: "/repo", ExpectedCwd: "/old", DeferUntilIdle: true,
	})
	if got.TargetId != "target-1" || got.Name != "work" || got.Cwd != "/repo" || got.ExpectedCwd != "/old" || !got.DeferUntilIdle {
		t.Fatalf("hub cwd request = %+v", got)
	}
}
