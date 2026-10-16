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
