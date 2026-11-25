package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
)

type developmentLeaseReconcileCall struct {
	live map[string]struct{}
}

func TestDevelopmentLeaseWatcherReconcilesOnSessionArchive(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("lease-owner", "codex")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}

	calls := make(chan developmentLeaseReconcileCall, 4)
	oldReconcile := reconcileDevelopmentLeases
	reconcileDevelopmentLeases = func(
		_ string,
		_ time.Time,
		live map[string]struct{},
	) ([]agentprofile.DevelopmentLeaseCleanupResult, error) {
		copied := make(map[string]struct{}, len(live))
		for id := range live {
			copied[id] = struct{}{}
		}
		calls <- developmentLeaseReconcileCall{live: copied}
		return nil, nil
	}
	t.Cleanup(func() { reconcileDevelopmentLeases = oldReconcile })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDevelopmentLeaseWatcher(ctx, mgr, "/tmp/test-pack-registry.json", time.Hour)

	first := waitDevelopmentLeaseReconcileCall(t, calls)
	if _, ok := first.live[sess.ID]; !ok {
		t.Fatalf("initial live sessions = %#v, want %s", first.live, sess.ID)
	}
	if err := mgr.SetState(sess, session.StateArchived); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	second := waitDevelopmentLeaseReconcileCall(t, calls)
	if _, ok := second.live[sess.ID]; ok {
		t.Fatalf("archived session %s still reported live: %#v", sess.ID, second.live)
	}
}

func waitDevelopmentLeaseReconcileCall(
	t *testing.T,
	calls <-chan developmentLeaseReconcileCall,
) developmentLeaseReconcileCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for development lease reconciliation")
		return developmentLeaseReconcileCall{}
	}
}

func TestRestoreRunningSessionsSleepsSessionsWithoutBrokerSocket(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("acp-session", "codex")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}

	if err := (&server{mgr: mgr}).restoreRunningSessions(); err != nil {
		t.Fatalf("restore running sessions: %v", err)
	}

	got, err := mgr.Get("acp-session")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.State != session.StateSleeping {
		t.Fatalf("state = %s, want %s", got.State, session.StateSleeping)
	}
	if got.LastSleepReason != session.SleepReasonRestartFailed {
		t.Fatalf("LastSleepReason = %q, want %q", got.LastSleepReason, session.SleepReasonRestartFailed)
	}
}

func TestRestoreRunningSessionsSleepsSessionsWhenReconnectFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("acp-session", "codex")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.SetBrokerSocket(filepath.Join(t.TempDir(), "missing.sock"))
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}

	if err := (&server{mgr: mgr}).restoreRunningSessions(); err != nil {
		t.Fatalf("restore running sessions: %v", err)
	}

	got, err := mgr.Get("acp-session")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.State != session.StateSleeping {
		t.Fatalf("state = %s, want %s", got.State, session.StateSleeping)
	}
	if got.LastSleepReason != session.SleepReasonRestartFailed {
		t.Fatalf("LastSleepReason = %q, want %q", got.LastSleepReason, session.SleepReasonRestartFailed)
	}
	if got.BrokerSocket != "" {
		t.Fatalf("BrokerSocket = %q, want empty", got.BrokerSocket)
	}
	if got.PID != 0 {
		t.Fatalf("PID = %d, want 0", got.PID)
	}
}

func TestRestoreRunningSessionsReturnsDurableTerminalFailure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr := session.NewManager(db)
	sess, err := mgr.Create("active-session", "codex")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := sess.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return nil
	}); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err = (&server{mgr: mgr}).restoreRunningSessions()
	if !errors.Is(err, agentstream.ErrJournalFault) {
		t.Fatalf("restore running sessions error = %v, want ErrJournalFault", err)
	}
	if got := sess.StateSnapshot(); got != session.StateRunning {
		t.Fatalf("state after terminal failure = %s, want %s", got, session.StateRunning)
	}
}

func TestRestoreRunningSessionsReturnsReconnectTerminalFailure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr := session.NewManager(db)
	sess, err := mgr.Create("reconnect-session", "codex")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.SetBrokerSocket(filepath.Join(t.TempDir(), "missing.sock"))
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := sess.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return nil
	}); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err = (&server{mgr: mgr}).restoreRunningSessions()
	if !errors.Is(err, agentstream.ErrJournalFault) {
		t.Fatalf("restore running sessions error = %v, want ErrJournalFault", err)
	}
	if got := sess.StateSnapshot(); got != session.StateRunning {
		t.Fatalf("state after terminal failure = %s, want %s", got, session.StateRunning)
	}
}

func TestRestoreRunningSessionsReturnsSleepStateFailure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr := session.NewManager(db)
	sess, err := mgr.Create("inactive-session", "codex")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err = (&server{mgr: mgr}).restoreRunningSessions()
	if err == nil || !strings.Contains(err.Error(), "sleep recovered session") {
		t.Fatalf("restore running sessions error = %v, want sleep state failure", err)
	}
}

func TestRuntimeLogMessagesUseRuntimeTerminology(t *testing.T) {
	for _, text := range []string{
		runtimeListeningMessage("/tmp/runtime.sock"),
		runtimeShuttingDownMessage(),
		runtimeReconnectSleepMessage("work", assertErr{}),
		runtimeReconnectedMessage("work", 123),
	} {
		if !strings.Contains(strings.ToLower(text), "runtime") {
			t.Fatalf("message should mention runtime: %q", text)
		}
		if strings.Contains(strings.ToLower(text), "amuxlet") || strings.Contains(strings.ToLower(text), "sandbox") {
			t.Fatalf("message should not expose old terminology: %q", text)
		}
	}
}

func TestBuildCapabilitiesUsesDefaultCapacity(t *testing.T) {
	caps := buildCapabilities(config.RuntimeConfig{})
	if caps.MaxSessions != 100 {
		t.Fatalf("MaxSessions = %d, want 100", caps.MaxSessions)
	}
	if len(caps.Agents) == 0 {
		t.Fatal("Agents is empty")
	}
	for _, agent := range caps.Agents {
		if agent.MaxConcurrency != 10 {
			t.Fatalf("%s MaxConcurrency = %d, want 10", agent.Type, agent.MaxConcurrency)
		}
	}
}

func TestBuildCapabilitiesDeclaresIsolationPolicyWhileHelperIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	rt, err := isolation.NewRuntime(config.IsolationConfig{
		Enabled:       true,
		Backend:       "auto",
		UIDRangeStart: 700000,
		UIDRangeEnd:   799999,
		HelperSocket:  filepath.Join(dir, "missing-helper.sock"),
		MappingDB:     filepath.Join(dir, "mappings.db"),
		PrincipalRoot: filepath.Join(dir, "principals"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	caps := buildCapabilities(config.RuntimeConfig{}, rt)
	if !containsCapabilityFeature(caps.Features, registry.FeaturePrincipalIsolation) {
		t.Fatalf("features = %v, want %q while isolation is configured", caps.Features, registry.FeaturePrincipalIsolation)
	}
	if caps.IsolationHelperProtocol != "" {
		t.Fatalf("helper protocol = %q, want empty while helper is unavailable", caps.IsolationHelperProtocol)
	}
}

func containsCapabilityFeature(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }
