//go:build linux

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
)

// spawnFakeBroker re-execs the test binary as a fake `_structured_broker`
// process (Setsid, blocking) whose --socket points at sock. The process only
// needs to *look* like a broker in /proc; it never binds the socket or starts
// an agent.
func spawnFakeBroker(t *testing.T, sock string) *exec.Cmd {
	t.Helper()
	sessDir := filepath.Dir(sock)
	base := filepath.Base(sock)
	sid := base[:len(base)-len(".structured.sock")]
	cmd := exec.Command(os.Args[0], "_structured_broker",
		"--socket", sock,
		"--pid-file", filepath.Join(sessDir, sid+".pid"),
		"--cwd", "/tmp")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake broker: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// TestReapOrphanBrokersEndToEnd runs the reaper against real processes on a
// real /proc: an orphan broker (session absent from the DB) must be killed,
// while a running session's broker must be spared.
func TestReapOrphanBrokersEndToEnd(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := session.NewManager(db)
	srv := &server{mgr: mgr, amuxDir: dir}

	// Orphan broker: its session is not in the DB.
	orphanSock := filepath.Join(sessDir, "00000000-0000-0000-0000-0000000000aa.structured.sock")
	orphanCmd := spawnFakeBroker(t, orphanSock)
	orphanWait := make(chan error, 1)
	go func() { orphanWait <- orphanCmd.Wait() }()

	// Live broker: its session is running in the DB.
	live, err := mgr.Create("live", "claude")
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	if err := mgr.SetRunning(live); err != nil {
		t.Fatalf("set running: %v", err)
	}
	liveSock := filepath.Join(sessDir, live.ID+".structured.sock")
	liveCmd := spawnFakeBroker(t, liveSock)

	// Give both brokers a moment to appear in /proc.
	time.Sleep(200 * time.Millisecond)

	if _, err := srv.reapOrphanBrokers(); err != nil {
		t.Fatalf("reapOrphanBrokers: %v", err)
	}

	select {
	case <-orphanWait:
		// orphan was killed and reaped — good
	case <-time.After(5 * time.Second):
		t.Fatalf("orphan broker pid=%d was not reaped", orphanCmd.Process.Pid)
	}
	if err := liveCmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("live session broker pid=%d was wrongly reaped: %v", liveCmd.Process.Pid, err)
	}
}
