package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
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

func TestFeishuBrokerDisconnectRestoresSameSessionOnNextReply(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("feishu-topic", "codex", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordLaunchParams(sess, nil, nil, "feishu", "scope", "builtin/codex", "", `{}`); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "saved-thread"); err != nil {
		t.Fatal(err)
	}
	srv := &server{mgr: mgr, amuxDir: dir, disableMCPAutoInject: true}
	srv.wireSessionEventTap(sess)
	sess.SetProc(&serverTestProc{holdTurnOpen: true})
	sess.SetBrokerSocket(filepath.Join(dir, "dead.sock"))
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := srv.Submit(ctx, &pb.SubmitRequest{Name: sess.Name, Source: "feishu", Text: "initial prompt"})
	if err != nil {
		t.Fatal(err)
	}
	procDone := make(chan struct{})
	close(procDone)
	snap := sess.Snapshot()
	srv.waitForSessionProcess(sess, procDone, snap.LaunchID, snap.ExecutionIdentity, sess.ProcessGeneration())

	reloaded := session.NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatal(err)
	}
	srv.mgr = reloaded
	persisted, err := reloaded.Get(sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	got := persisted.Snapshot()
	if got.State != session.StateSleeping || got.ID != snap.ID || got.AgentSessionID != "saved-thread" ||
		got.PID != 0 || got.BrokerSocket != "" || got.LastSleepReason != session.SleepReasonProcessExit {
		t.Fatalf("persisted Session does not match recoverable disconnect: state=%s hasPid=%v hasSocket=%v reason=%s", got.State, got.PID != 0, got.BrokerSocket != "", got.LastSleepReason)
	}
	t.Log("Broker disconnect persisted a sleeping Session with its original identity and native thread")

	proc := &serverTestProc{}
	srv.startSessionContextHook = func(_ context.Context, _ adapter.Adapter, current *session.Session, _, _ []string) error {
		if state := current.Snapshot(); state.ID != snap.ID || state.AgentSessionID != "saved-thread" || state.Cwd != dir {
			t.Fatal("automatic wake changed the Session identity, thread, or workspace")
		}
		srv.wireSessionEventTap(current)
		current.SetProc(proc)
		return reloaded.SetRunning(current)
	}
	next, err := srv.Submit(ctx, &pb.SubmitRequest{Name: sess.Name, Source: "feishu", Text: "follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	if next.SessionId != first.SessionId || next.TurnId == first.TurnId || persisted.StateSnapshot() != session.StateRunning {
		t.Fatal("new input did not start a new Turn in the recovered Session")
	}
	events, err := db.LoadAgentEvents(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	started := map[string]int{}
	finished := map[string]agentstream.Terminal{}
	for _, event := range events {
		switch body := event.Body.(type) {
		case agentstream.TurnStarted:
			started[event.Turn.ID]++
		case agentstream.TurnFinished:
			finished[event.Turn.ID] = body.Terminal
		}
	}
	if len(started) != 2 || started[first.TurnId] != 1 || started[next.TurnId] != 1 ||
		finished[first.TurnId].Code != "provider_stream_closed" || finished[next.TurnId].Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("unexpected Turn history: starts=%v terminals=%v", started, finished)
	}
	if len(proc.writes) != 2 || string(proc.writes[0]) != "follow-up" {
		t.Fatalf("replacement process received unexpected input: %q", proc.writes)
	}
	t.Log("Original Turn failed once. Only the new reply ran with the same Session ID.")
}

func TestProcessExitArchiveBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		opts         session.CreateOptions
		source       string
		recordLaunch bool
		stop         bool
	}{
		{name: "local exit", recordLaunch: true},
		{name: "Feishu without launch settings", source: "feishu"},
		{name: "explicit Feishu stop", source: "feishu", recordLaunch: true, stop: true},
		{name: "task child", source: "feishu", recordLaunch: true, opts: session.CreateOptions{OwnerTaskID: "task-1"}},
		{name: "persistent instance", source: "feishu", recordLaunch: true, opts: session.CreateOptions{AgentInstanceID: "instance-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			tc.opts.Cwd = dir
			sess, err := mgr.CreateWithOptions("topic", "codex", tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			profile := ""
			if tc.recordLaunch {
				profile = "builtin/codex"
			}
			if err := mgr.RecordLaunchParams(sess, nil, nil, tc.source, "", profile, "", ""); err != nil {
				t.Fatal(err)
			}
			sess.SetProc(&serverTestProc{})
			if err := mgr.SetRunning(sess); err != nil {
				t.Fatal(err)
			}
			if tc.stop {
				if err := sess.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			procDone := make(chan struct{})
			close(procDone)
			srv := &server{mgr: mgr}
			snap := sess.Snapshot()
			srv.waitForSessionProcess(sess, procDone, snap.LaunchID, snap.ExecutionIdentity, sess.ProcessGeneration())
			if got := sess.StateSnapshot(); got != session.StateArchived {
				t.Fatalf("Session state = %s, want archived", got)
			}
		})
	}
}

func waitForDrainSettled(t *testing.T) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
}
