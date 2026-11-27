package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const maintenanceTestToken = "maintenance-test-token"

func maintenanceContext() context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.UnixAddr{Name: "local", Net: "unix"}})
}
func newMaintenanceTest(t *testing.T) (*runtimeMaintenance, *session.Manager) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := session.NewManager(db)
	m, err := newRuntimeMaintenance(&server{mgr: mgr, amuxDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.resume = func(_ context.Context, s *session.Session) error {
		s.SetProc(&serverTestProc{})
		return mgr.SetRunning(s)
	}
	return m, mgr
}
func maintenanceCall(t *testing.T, m *runtimeMaintenance, action string) *pb.MaintenanceResponse {
	t.Helper()
	out, err := m.Maintain(maintenanceContext(), &pb.MaintenanceRequest{Token: maintenanceTestToken, Action: action})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func maintenanceSessionForTest(t *testing.T, mgr *session.Manager, name string) (*session.Session, *serverTestProc) {
	t.Helper()
	s, err := mgr.Create(name, "claude")
	if err != nil {
		t.Fatal(err)
	}
	p := &serverTestProc{}
	s.SetProc(p)
	if err := mgr.SetRunning(s); err != nil {
		t.Fatal(err)
	}
	return s, p
}
func TestMaintenanceDrainsAcceptedQueueAndPreservesSleepingSessions(t *testing.T) {
	m, mgr := newMaintenanceTest(t)
	active, p := maintenanceSessionForTest(t, mgr, "active")
	idle, _ := maintenanceSessionForTest(t, mgr, "idle")
	sleeping, _ := maintenanceSessionForTest(t, mgr, "already-sleeping")
	if err := mgr.SetSleeping(sleeping); err != nil {
		t.Fatal(err)
	}
	p.holdTurnOpen = true
	if _, err := active.SubmitInput(context.Background(), "cli", session.TextUserInput("work")); err != nil {
		t.Fatal(err)
	}
	if err := active.SubmitAsync("cli", "accepted-followup"); err != nil {
		t.Fatal(err)
	}
	maintenanceCall(t, m, "begin")
	rpc := &maintenanceSessionServer{AmuxletServer: m.srv, maintenance: m}
	if _, err := rpc.Submit(context.Background(), &pb.SubmitRequest{Name: active.Name, Text: "new"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("new input admitted: %v", err)
	}
	out := maintenanceCall(t, m, "poll")
	if out.Phase != "draining" || len(out.BusySessions) != 1 || p.closed {
		t.Fatalf("active turn stopped: %v", out)
	}
	// Complete the original turn, then the previously accepted follow-up.
	p.holdTurnOpen = false
	p.turnSink(testSuccessEmission())
	deadline := time.Now().Add(3 * time.Second)
	for {
		busy, pending := active.QueueState()
		if !busy && pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	out = maintenanceCall(t, m, "poll")
	if out.Phase != "ready" || out.SleepingSessions != 2 || !p.closed {
		t.Fatalf("not ready: %v", out)
	}
	if idle.StateSnapshot() != session.StateSleeping {
		t.Fatal("idle session still running")
	}
	maintenanceCall(t, m, "end")
	if active.StateSnapshot() != session.StateRunning || idle.StateSnapshot() != session.StateRunning || sleeping.StateSnapshot() != session.StateSleeping {
		t.Fatal("restore set changed")
	}
	if _, err := os.Stat(m.path()); !os.IsNotExist(err) {
		t.Fatalf("journal not cleared: %v", err)
	}
}

type handoffTestProc struct{ serverTestProc }

func (*handoffTestProc) SupportsTurnRebind() bool { return true }

func TestMaintenanceHelperHandoffPreservesHostTurnAndDrainsIsolatedTurn(t *testing.T) {
	m, mgr := newMaintenanceTest(t)
	start := func(name string, identity principal.ExecutionIdentity) (*session.Session, *handoffTestProc) {
		sess, err := mgr.Create(name, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if err := mgr.SetExecutionIdentity(sess, identity, name+"-launch"); err != nil {
			t.Fatal(err)
		}
		proc := &handoffTestProc{serverTestProc: serverTestProc{holdTurnOpen: true}}
		sess.SetProc(proc)
		sess.SetBrokerSocket(filepath.Join(m.srv.amuxDir, name+".sock"))
		if err := mgr.SetRunning(sess); err != nil {
			t.Fatal(err)
		}
		if _, err := sess.SubmitInput(t.Context(), "cli", session.TextUserInput(name)); err != nil {
			t.Fatal(err)
		}
		return sess, proc
	}
	host, hostProc := start("host", principal.LegacyHostIdentity(principal.SourceCLI))
	isolated, isolatedProc := start("isolated", principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		Source:                 principal.SourceCLI,
		OriginActorPrincipalID: "96d3a2d2-3b8a-4cd1-96aa-fe5660001000",
		EffectivePrincipalID:   "96d3a2d2-3b8a-4cd1-96aa-fe5660001000",
		IsolationBackend:       principal.BackendLinuxDAC,
	})
	maintenanceCall(t, m, "begin-helper-handoff")
	out := maintenanceCall(t, m, "poll")
	if out.Phase != "draining" || len(out.BusySessions) != 1 || out.BusySessions[0] != isolated.Name || isolatedProc.closed {
		t.Fatalf("helper did not wait for the isolated turn: %v", out)
	}
	isolatedProc.turnSink(testSuccessEmission())
	for deadline := time.Now().Add(time.Second); out.Phase != "ready"; {
		if time.Now().After(deadline) {
			t.Fatalf("isolated session did not drain: %v", out)
		}
		time.Sleep(time.Millisecond)
		out = maintenanceCall(t, m, "poll")
	}
	if len(m.handoffBrokers()) != 0 {
		t.Fatal("broker handoff was enabled before the updater requested a restart")
	}
	maintenanceCall(t, m, "arm-handoff")
	keep := m.handoffBrokers()
	if !keep[host.ID] || keep[isolated.ID] || !isolatedProc.closed {
		t.Fatalf("incorrect handoff set: %v", keep)
	}
	m.srv.shutdownSessions(keep)
	if hostProc.closed || host.AgentTimeline().ActiveTurn() == nil {
		t.Fatal("host turn was stopped during handoff")
	}
	hostProc.turnSink(testSuccessEmission())
}
func TestMaintenanceWaitsForAcceptedRequestsAndTaskCleanup(t *testing.T) {
	m, _ := newMaintenanceTest(t)
	activeTasks := 1
	m.tasks = func() int { return activeTasks }
	done, err := m.enter(true)
	if err != nil {
		t.Fatal(err)
	}
	maintenanceCall(t, m, "begin")
	out := maintenanceCall(t, m, "poll")
	if out.ActiveTasks != 1 || out.ActiveRequests != 1 || out.Phase != "draining" {
		t.Fatal(out)
	}
	done()
	out = maintenanceCall(t, m, "poll")
	if out.Phase != "draining" {
		t.Fatal("did not wait for task cleanup")
	}
	activeTasks = 0
	if out = maintenanceCall(t, m, "poll"); out.Phase != "ready" {
		t.Fatal(out)
	}
	if _, err := m.enter(false); status.Code(err) != codes.Unavailable {
		t.Fatalf("helper worker admitted after ready: %v", err)
	}
	maintenanceCall(t, m, "end")
}
func TestMaintenanceCancelsBackgroundObserversAndWaitsForCleanup(t *testing.T) {
	m, _ := newMaintenanceTest(t)
	ctx, done, err := m.enterObserver(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	maintenanceCall(t, m, "begin-helper-handoff")
	if ctx.Err() != context.Canceled {
		t.Fatalf("observer context after maintenance began: %v", ctx.Err())
	}
	if out := maintenanceCall(t, m, "poll"); out.Phase != "draining" || out.ActiveRequests != 1 {
		t.Fatalf("maintenance did not wait for observer cleanup: %v", out)
	}
	if _, _, err := m.enterObserver(context.Background()); status.Code(err) != codes.Unavailable {
		t.Fatalf("observer admitted during maintenance: %v", err)
	}
	done()
	if out := maintenanceCall(t, m, "poll"); out.Phase != "ready" {
		t.Fatal(out)
	}
	maintenanceCall(t, m, "end")
	ctx, done, err = m.enterObserver(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if ctx.Err() != nil {
		t.Fatalf("observer admitted after recovery has canceled context: %v", ctx.Err())
	}
}

func TestMaintenanceJournalSurvivesRuntimeRestartAndRetriesRecovery(t *testing.T) {
	m, mgr := newMaintenanceTest(t)
	s, _ := maintenanceSessionForTest(t, mgr, "resume-me")
	maintenanceCall(t, m, "begin")
	maintenanceCall(t, m, "poll")
	db, err := store.Open(filepath.Join(m.srv.amuxDir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restored := session.NewManager(db)
	if err := restored.LoadFromDB(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newRuntimeMaintenance(&server{mgr: restored, amuxDir: m.srv.amuxDir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.enter(true); status.Code(err) != codes.Unavailable {
		t.Fatal("lost maintenance on restart")
	}
	reloaded.resume = func(context.Context, *session.Session) error { return errors.New("provider unavailable") }
	_, err = reloaded.Maintain(maintenanceContext(), &pb.MaintenanceRequest{Token: maintenanceTestToken, Action: "end"})
	if err == nil || len(reloaded.record.Sessions) != 1 {
		t.Fatal("lost failed recovery entry")
	}
	done, err := reloaded.enter(true)
	if err != nil {
		t.Fatalf("recovery failure stuck admission: %v", err)
	}
	done()
	reloaded.resume = func(_ context.Context, s *session.Session) error {
		s.SetProc(&serverTestProc{})
		return restored.SetRunning(s)
	}
	maintenanceCall(t, reloaded, "end")
	s, err = restored.Get(s.Name)
	if err != nil {
		t.Fatal(err)
	}
	if s.StateSnapshot() != session.StateRunning {
		t.Fatal("retry did not restore session")
	}
}
func TestMaintenanceAbandonmentAndTokenIsolation(t *testing.T) {
	m, mgr := newMaintenanceTest(t)
	s, _ := maintenanceSessionForTest(t, mgr, "restore")
	maintenanceCall(t, m, "begin")
	maintenanceCall(t, m, "poll")
	_, err := m.Maintain(maintenanceContext(), &pb.MaintenanceRequest{Token: "another-maintenance-token", Action: "end"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatal(err)
	}
	if _, err := m.Maintain(context.Background(), &pb.MaintenanceRequest{Token: maintenanceTestToken, Action: "end"}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("nonlocal maintenance accepted")
	}
	m.now = func() time.Time { return time.Now().Add(maintenanceLease + time.Second) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.watch(ctx)
	deadline := time.Now().Add(7 * time.Second)
	for s.StateSnapshot() != session.StateRunning {
		if time.Now().After(deadline) {
			t.Fatal("abandoned lease not recovered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestMaintenanceCopyLocales(t *testing.T) {
	en := maintenanceCatalog.Format(i18n.English, "unavailable")
	zh := maintenanceCatalog.Format(i18n.Chinese, "unavailable")
	if en == zh || maintenanceCatalog.Format("fr", "unavailable") != en {
		t.Fatal("maintenance translations/fallback missing")
	}
}

func TestMaintenanceAbortLeavesBusyTurnAndAcceptedInputIntact(t *testing.T) {
	m, mgr := newMaintenanceTest(t)
	s, p := maintenanceSessionForTest(t, mgr, "busy")
	p.holdTurnOpen = true
	if _, err := s.SubmitInput(context.Background(), "cli", session.TextUserInput("first")); err != nil {
		t.Fatal(err)
	}
	if err := s.SubmitAsync("cli", "second"); err != nil {
		t.Fatal(err)
	}
	turn := s.AgentTimeline().ActiveTurn()
	maintenanceCall(t, m, "begin")
	taskRPC := &maintenanceTaskServer{maintenance: m}
	if _, err := taskRPC.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("new Task admitted: %v", err)
	}
	maintenanceCall(t, m, "end")
	if p.closed || s.AgentTimeline().ActiveTurn() == nil || s.AgentTimeline().ActiveTurn().Ref() != turn.Ref() || len(s.QueueSnapshot()) != 1 {
		t.Fatal("aborting maintenance disrupted accepted work")
	}
	p.holdTurnOpen = false
	p.turnSink(testSuccessEmission())
	deadline := time.Now().Add(time.Second)
	for {
		busy, pending := s.QueueState()
		if !busy && pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted queue did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestMaintenanceJournalFailureDoesNotStopProcess(t *testing.T) {
	m, mgr := newMaintenanceTest(t)
	_, p := maintenanceSessionForTest(t, mgr, "keep-running")
	maintenanceCall(t, m, "begin")
	if err := os.Remove(m.path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(m.path(), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := m.Maintain(maintenanceContext(), &pb.MaintenanceRequest{Token: maintenanceTestToken, Action: "poll"})
	if err == nil || p.closed {
		t.Fatal("process stopped without durable recovery record")
	}
	if err := os.Remove(m.path()); err != nil {
		t.Fatal(err)
	}
	maintenanceCall(t, m, "end")
}

func TestMaintenanceRecoveryRetainsJournalWhenRunningStateCannotPersist(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(db)
	m, err := newRuntimeMaintenance(&server{mgr: mgr, amuxDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := maintenanceSessionForTest(t, mgr, "restore")
	maintenanceCall(t, m, "begin")
	maintenanceCall(t, m, "poll")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	m.resume = func(_ context.Context, s *session.Session) error {
		s.SetProc(&serverTestProc{})
		return mgr.SetRunning(s)
	}
	for i := 0; i < 2; i++ {
		_, err := m.Maintain(maintenanceContext(), &pb.MaintenanceRequest{Token: maintenanceTestToken, Action: "end"})
		if err == nil || m.record == nil || len(m.record.Sessions) != 1 {
			t.Fatal("failed durable resume was reported as recovered")
		}
	}
	if !s.HasProcess() {
		t.Fatal("retry stopped the recovered process")
	}
	if _, err := os.Stat(m.path()); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceWatchIntervalBacksOffAfterFailedRecovery(t *testing.T) {
	if got := nextMaintenanceWatchInterval(maintenanceWatchInterval, false); got != 10*time.Second {
		t.Fatalf("interval after one failed attempt = %s, want 10s", got)
	}
	if got := nextMaintenanceWatchInterval(10*time.Second, false); got != 20*time.Second {
		t.Fatalf("interval after two failed attempts = %s, want 20s", got)
	}
	if got := nextMaintenanceWatchInterval(4*time.Minute, false); got != maintenanceWatchMaxInterval {
		t.Fatalf("interval above the cap = %s, want %s", got, maintenanceWatchMaxInterval)
	}
	if got := nextMaintenanceWatchInterval(maintenanceWatchMaxInterval, false); got != maintenanceWatchMaxInterval {
		t.Fatalf("interval at the cap = %s, want %s", got, maintenanceWatchMaxInterval)
	}
	if got := nextMaintenanceWatchInterval(maintenanceWatchMaxInterval, true); got != maintenanceWatchInterval {
		t.Fatalf("interval after progress = %s, want %s", got, maintenanceWatchInterval)
	}
	if got := nextMaintenanceWatchInterval(0, false); got != maintenanceWatchInterval {
		t.Fatalf("interval from zero = %s, want %s", got, maintenanceWatchInterval)
	}
}

func TestMaintenanceRecoveryRetriesUntouchedSessionsFirst(t *testing.T) {
	m, mgr := newMaintenanceTest(t)
	slow, _ := maintenanceSessionForTest(t, mgr, "slow")
	next, _ := maintenanceSessionForTest(t, mgr, "next")
	maintenanceCall(t, m, "begin")
	maintenanceCall(t, m, "poll")
	m.record.Sessions = []maintenanceSession{{ID: slow.ID, Name: slow.Name}, {ID: next.ID, Name: next.Name}}
	ctx, cancel := context.WithCancel(maintenanceContext())
	resume := m.resume
	m.resume = func(_ context.Context, s *session.Session) error {
		if s != slow {
			t.Fatal("started a session after the batch deadline")
		}
		cancel()
		return context.Canceled
	}
	if _, err := m.Maintain(ctx, &pb.MaintenanceRequest{Token: maintenanceTestToken, Action: "end"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery returned %v, want cancellation", err)
	}
	if len(m.record.Sessions) != 2 || m.record.Sessions[0].ID != next.ID {
		t.Fatalf("next batch order = %+v", m.record.Sessions)
	}
	var tried []string
	m.resume = func(ctx context.Context, s *session.Session) error {
		tried = append(tried, s.Name)
		return resume(ctx, s)
	}
	maintenanceCall(t, m, "end")
	if len(tried) != 2 || tried[0] != next.Name {
		t.Fatalf("second batch order = %v", tried)
	}
}

func TestMaintenanceRecoveryDoesNotBlockMaintenanceRPC(t *testing.T) {
	m, mgr := newMaintenanceTest(t)
	maintenanceSessionForTest(t, mgr, "slow")
	maintenanceCall(t, m, "begin")
	maintenanceCall(t, m, "poll")
	entered, release := make(chan struct{}), make(chan struct{})
	m.resume = func(context.Context, *session.Session) error {
		close(entered)
		<-release
		return errors.New("provider unavailable")
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = m.Maintain(maintenanceContext(), &pb.MaintenanceRequest{Token: maintenanceTestToken, Action: "end"})
	}()
	defer func() {
		close(release)
		<-finished
	}()
	<-entered
	result := make(chan error, 1)
	go func() {
		_, err := m.Maintain(maintenanceContext(), &pb.MaintenanceRequest{Token: "other-maintenance-token", Action: "begin"})
		result <- err
	}()
	select {
	case err := <-result:
		if status.Code(err) != codes.AlreadyExists {
			t.Fatalf("second upgrade returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second upgrade blocked behind the provider")
	}
}
