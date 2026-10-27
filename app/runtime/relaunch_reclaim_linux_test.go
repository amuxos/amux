//go:build linux

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/google/uuid"
)

// reclaimTestEnv is a server backed by a real isolation helper running
// unprivileged against a temporary principal root: the smallest setup in which
// "the helper created a run directory" is observable end to end. Mirrors the
// unprivileged lifecycle fixtures in internal/isolation.
type reclaimTestEnv struct {
	srv           *server
	mgr           *session.Manager
	rt            *isolation.Runtime
	principalRoot string
	identity      principal.ExecutionIdentity
}

func newReclaimTestEnv(t *testing.T, materializer string) *reclaimTestEnv {
	t.Helper()
	// The isolated-home projection reads credentials from the service user's
	// HOME; an empty home keeps each launch bundle deterministic.
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	cfg := isolation.HelperConfig{
		SocketPath:           filepath.Join(root, "helper.sock"),
		StateDir:             filepath.Join(root, "state"),
		PrincipalRoot:        filepath.Join(root, "principals"),
		ServiceUID:           os.Getuid(),
		ServiceGID:           os.Getgid(),
		UIDRangeStart:        65536,
		UIDRangeEnd:          65546,
		MaterializerPath:     materializer,
		AllowUnprivileged:    true,
		SkipAccountProvision: true,
	}
	helperCtx, cancelHelper := context.WithCancel(context.Background())
	t.Cleanup(cancelHelper)
	go func() { _ = isolation.ServeHelper(helperCtx, cfg) }()
	client := isolation.NewHelperClient(cfg.SocketPath)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := client.Handshake(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolation helper did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	rt, err := isolation.NewRuntime(config.IsolationConfig{
		Enabled:       true,
		Backend:       principal.BackendLinuxDAC,
		UIDRangeStart: 65536,
		UIDRangeEnd:   65546,
		HelperSocket:  cfg.SocketPath,
		MappingDB:     filepath.Join(root, "mappings.db"),
		PrincipalRoot: cfg.PrincipalRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	db, err := store.Open(filepath.Join(root, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := session.NewManager(db)
	identity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: "11111111-1111-4111-8111-111111111111",
		EffectivePrincipalID:   "22222222-2222-4222-8222-222222222222",
		Source:                 principal.SourceCLI,
		ExecutionStateRef:      "reclaim-test",
		IsolationBackend:       principal.BackendLinuxDAC,
	}
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	return &reclaimTestEnv{
		srv:           &server{mgr: mgr, isolationRuntime: rt},
		mgr:           mgr,
		rt:            rt,
		principalRoot: cfg.PrincipalRoot,
		identity:      identity,
	}
}

// newPreparedSession creates a session whose saved launch points at the
// principal workspace the helper prepared, mirroring a session that already
// ran once. AgentType and AgentProfile are both set so restart defaults keep
// the snapshot instead of rebuilding a launch.
func (env *reclaimTestEnv) newPreparedSession(t *testing.T, name string) *session.Session {
	t.Helper()
	seedLaunchID := "launch-" + uuid.NewString()
	paths, _, err := env.rt.PrepareExecution(context.Background(), env.identity, seedLaunchID, "")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := env.mgr.CreateWithOptions(name, "claude", session.CreateOptions{Cwd: paths.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.mgr.SetExecutionIdentity(sess, env.identity, seedLaunchID); err != nil {
		t.Fatal(err)
	}
	if err := env.mgr.RecordLaunchParams(sess, nil, nil, "", "", "test/profile", "", ""); err != nil {
		t.Fatal(err)
	}
	// Drop the seed launch's directory; the workspace above it is the session
	// cwd and stays. A relaunch rotates to a fresh launch id anyway.
	_ = env.rt.RemoveRun(context.Background(), env.identity, seedLaunchID)
	return sess
}

func launchRunDir(principalRoot, launchID string) string {
	matches, _ := filepath.Glob(filepath.Join(principalRoot, "*", "run", launchID))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func assertLaunchRunReclaimed(t *testing.T, principalRoot, launchID string) {
	t.Helper()
	if dir := launchRunDir(principalRoot, launchID); dir != "" {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("run directory %s still exists with %d entries after the launch ended", dir, len(entries))
	}
}

func assertLaunchRunIntact(t *testing.T, principalRoot, launchID string) {
	t.Helper()
	dir := launchRunDir(principalRoot, launchID)
	if dir == "" {
		t.Fatalf("run directory for live launch %s is missing", launchID)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("run path %s: %v", dir, err)
	}
}

// A relaunch whose process never attaches must reclaim the run directory
// PrepareRun created. This is the leak that filled the disk: the maintenance
// recovery loop retried a failing resume every few seconds, and each attempt
// abandoned a full run tree.
func TestRelaunchFailureReclaimsPreparedRunDirectory(t *testing.T) {
	env := newReclaimTestEnv(t, "/bin/false")
	sess := env.newPreparedSession(t, "reclaim-failed-launch")
	failing := errors.New("provider refused to start")
	env.srv.startSessionContextHook = func(context.Context, adapter.Adapter, *session.Session, []string, []string) error {
		return failing
	}
	if _, err := env.srv.relaunchSessionLocked(sess, "restarting"); !errors.Is(err, failing) {
		t.Fatalf("relaunch error = %v, want the launch failure", err)
	}
	snap := sess.Snapshot()
	if snap.State == session.StateRunning || snap.LaunchID == "" {
		t.Fatalf("session after failed relaunch = state %s launch %q", snap.State, snap.LaunchID)
	}
	assertLaunchRunReclaimed(t, env.principalRoot, snap.LaunchID)
}

// A failure before the process launch (the materialization bundle never
// lands) must reclaim the run directory too.
func TestRelaunchMaterializeFailureReclaimsRunDirectory(t *testing.T) {
	env := newReclaimTestEnv(t, "/bin/false")
	env.srv.amuxDir = t.TempDir()
	sess := env.newPreparedSession(t, "reclaim-materialize-failure")
	configDir := filepath.Join(env.srv.amuxDir, "sessions", sess.Snapshot().ID)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := env.srv.relaunchSessionLocked(sess, "restarting")
	if err == nil || !strings.Contains(err.Error(), "materialize") {
		t.Fatalf("relaunch error = %v, want the materialization failure", err)
	}
	assertLaunchRunReclaimed(t, env.principalRoot, sess.Snapshot().LaunchID)
}

// The other half of the contract: a launch that attaches keeps its directory.
func TestRelaunchSuccessKeepsPreparedRunDirectory(t *testing.T) {
	env := newReclaimTestEnv(t, "/bin/false")
	sess := env.newPreparedSession(t, "reclaim-success")
	env.srv.startSessionContextHook = func(context.Context, adapter.Adapter, *session.Session, []string, []string) error {
		return nil
	}
	if _, err := env.srv.relaunchSessionLocked(sess, "restarting"); err != nil {
		t.Fatalf("relaunch failed: %v", err)
	}
	assertLaunchRunIntact(t, env.principalRoot, sess.Snapshot().LaunchID)
}

// A broker that died without shutting down leaves its provider running inside
// the helper; the exit watcher must kill that launch before the run directory
// goes away, or the orphan keeps provider-native locks and every future resume
// of the session fails against it.
func TestProcessExitReclaimsOrphanedProviderLaunch(t *testing.T) {
	env := newReclaimTestEnv(t, "/bin/false")
	ctx := context.Background()
	launchID := "launch-" + uuid.NewString()
	paths, _, err := env.rt.PrepareExecution(ctx, env.identity, launchID, "")
	if err != nil {
		t.Fatal(err)
	}
	launchContext := isolation.LaunchContext{LaunchID: launchID, Identity: &env.identity, Backend: principal.BackendLinuxDAC}
	backend, err := env.rt.BackendForContext(launchContext)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := backend.Start(ctx, isolation.LaunchSpec{
		Path:            "/bin/sh",
		Args:            []string{"-c", "sleep 300"},
		Env:             []string{"PATH=/usr/bin:/bin"},
		Dir:             paths.Workspace,
		SetProcessGroup: true,
		Context:         launchContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	pid := handle.PID()
	t.Cleanup(func() {
		_ = handle.Kill(context.Background())
		_ = handle.Close()
	})
	sess, err := env.mgr.CreateWithOptions("process-exit", "claude", session.CreateOptions{Cwd: paths.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.mgr.SetExecutionIdentity(sess, env.identity, launchID); err != nil {
		t.Fatal(err)
	}
	// The broker dies (procDone closes) and the exit watcher runs.
	procDone := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		env.srv.waitForSessionProcess(sess, procDone, launchID, env.identity, sess.ProcessGeneration())
		close(watcherDone)
	}()
	close(procDone)
	select {
	case <-watcherDone:
	case <-time.After(10 * time.Second):
		t.Fatal("exit watcher did not finish")
	}
	waitForProcessExit(t, pid)
	assertLaunchRunReclaimed(t, env.principalRoot, launchID)
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider process %d still running after its broker exited", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
