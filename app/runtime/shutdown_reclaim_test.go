package runtime

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
)

func timeSleepShort() { time.Sleep(10 * time.Millisecond) }

// A graceful service-restart shutdown (SIGTERM from `amux update --restart` /
// `amux system restart`) kills every session broker before amuxlet exits. The
// drain goroutine in startSessionContext normally persists Archived once the
// process ends; during shutdown that would permanently end every live topic,
// because LoadFromDB never queues archived sessions for auto-restart. The
// shuttingDown flag must keep those sessions in their recorded Running states
// so the next amuxlet restores them.
func TestGracefulShutdownDoesNotArchiveRunningSessions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("feishu-topic", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.RecordLaunchParams(sess, []string{"--model", "m"}, nil, "feishu", "scope", "builtin/codex", "m", `{"agent_type":"codex"}`); err != nil {
		t.Fatalf("record launch params: %v", err)
	}
	procDone := make(chan struct{})
	proc := &serverTestProc{}
	sess.SetProc(proc)
	sess.SetBrokerSocket(filepath.Join(t.TempDir(), "s.sock"))
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	srv := &server{mgr: mgr}
	startedLaunchID := sess.Snapshot().LaunchID
	go srv.waitForSessionProcess(sess, procDone, startedLaunchID, sess.Snapshot().ExecutionIdentity, sess.ProcessGeneration())

	// Simulate the signal handler: mark shutdown, then kill the session
	// process (which is what closes the process-done channel).
	srv.shuttingDown.Store(true)
	if err := sess.Kill(); err != nil {
		t.Fatalf("kill session: %v", err)
	}
	if !proc.closed {
		t.Fatal("session process was not closed")
	}
	close(procDone)
	waitForDrainSettled(t)

	// The DB row must still be Running with its PID: LoadFromDB queues exactly
	// those rows (dead PID + Running + recorded launch params) for auto-restart.
	if got := sess.StateSnapshot(); got != session.StateRunning {
		t.Fatalf("session state = %s, want %s", got, session.StateRunning)
	}
	rows, err := db.AllSessions()
	if err != nil {
		t.Fatalf("read sessions: %v", err)
	}
	if len(rows) != 1 || rows[0].State != string(session.StateRunning) || rows[0].PID == 0 {
		t.Fatalf("db row = %+v, want state=%s pid=%d", rows, session.StateRunning, proc.PID())
	}
}

// Outside shutdown, a process that exits on its own still archives the
// session — that is the normal dead-broker recovery path and must not regress.
func TestUnexpectedProcessExitStillArchives(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("local-topic", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	procDone := make(chan struct{})
	proc := &serverTestProc{}
	sess.SetProc(proc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	srv := &server{mgr: mgr}
	startedLaunchID := sess.Snapshot().LaunchID
	go srv.waitForSessionProcess(sess, procDone, startedLaunchID, sess.Snapshot().ExecutionIdentity, sess.ProcessGeneration())

	if err := sess.Kill(); err != nil {
		t.Fatalf("kill session: %v", err)
	}
	close(procDone)
	waitForDrainState(t, sess, session.StateArchived)

	rows, err := db.AllSessions()
	if err != nil {
		t.Fatalf("read sessions: %v", err)
	}
	if len(rows) != 1 || rows[0].State != string(session.StateArchived) {
		t.Fatalf("db state = %+v, want archived", rows)
	}
}

// waitForDrainState polls until the drain goroutine's state transition is
// visible, then asserts the terminal state. The Kill above exercises the same
// proc.Close → drain-wakeup sequence the real shutdown path uses.
func waitForDrainState(t *testing.T, sess *session.Session, want session.State) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if sess.StateSnapshot() == want {
			return
		}
		timeSleepShort()
	}
	t.Fatalf("session state = %s, want %s", sess.StateSnapshot(), want)
}

// waitForDrainSettled gives the drain goroutine time to run its exit cleanup,
// for the negative case: the state must stay Running because the cleanup was
// suppressed, so there is no transition to poll for.
func waitForDrainSettled(t *testing.T) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
}
