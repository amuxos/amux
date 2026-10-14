package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
)

// errResumeFailed is returned by the startSessionHook to simulate a provider
// resume that cannot restore the prior session (e.g. expired provider session
// id).
var errResumeFailed = &resumeError{"provider session expired"}

type resumeError struct{ msg string }

func (e *resumeError) Error() string { return e.msg }

func TestCreateChildAddsEffectivePATHToLaunchAndSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	var launchEnv []string
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			launchEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	child, err := newTaskController(srv, "t-1").CreateChild(context.Background(), task.ChildSpec{
		TaskID:      "task-path",
		AgentType:   "claude",
		OwnerTaskId: "task-path",
		Env:         []string{"TMPDIR=/tmp/task-path", "SECRET_TOKEN=secret-value"},
		RecordEnv:   []string{"TMPDIR=/tmp/task-path"},
	})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	wantUserBin := filepath.Join(home, ".local", "bin")
	if got := envValueForTest(launchEnv, "PATH"); !strings.Contains(got, wantUserBin) {
		t.Fatalf("launch PATH = %q, want standard user bin %q", got, wantUserBin)
	}
	got, err := mgr.Get(child.ID())
	if err != nil {
		t.Fatalf("get child session: %v", err)
	}
	if path := envValueForTest(got.Snapshot().Env, "PATH"); !strings.Contains(path, wantUserBin) {
		t.Fatalf("snapshot PATH = %q, want standard user bin %q", path, wantUserBin)
	}
	if secret := envValueForTest(got.Snapshot().Env, "SECRET_TOKEN"); secret != "" {
		t.Fatalf("snapshot SECRET_TOKEN = %q, want credential omitted", secret)
	}
	if processPath := os.Getenv("PATH"); processPath != "/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("test PATH unexpectedly changed to %q", processPath)
	}
}

// TestCreateChildPersistentResumesSleepingSession verifies that when a
// persistent agent instance has a sleeping child session (left by a prior task
// at terminal state) with a recorded provider AgentSessionID, a follow-up
// CreateChild relaunches it in-process (provider resume) rather than returning
// the sleeping session as-is. The relaunch must restart the agent process and
// bring the session back to Running, reusing the same session identity.
func TestCreateChildPersistentResumesSleepingSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	var launches int
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, _ []string) error {
			launches++
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	// First task: create the persistent child and record a provider session id,
	// then sleep it (as the runner would at task terminal state).
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 1: %v", err)
	}
	if err := mgr.RecordAgentSessionID(c1.(*childSession).sess, "provider-sess-1"); err != nil {
		t.Fatalf("record agent session id: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	launches = 0

	// Follow-up task for the same instance: must resume the sleeping session.
	c2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-b", AgentType: "claude", OwnerTaskId: "task-b", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 2: %v", err)
	}
	if c2.ID() != c1.ID() {
		t.Fatalf("resume should reuse session: c1=%s c2=%s", c1.ID(), c2.ID())
	}
	if launches != 1 {
		t.Fatalf("relaunch launches = %d, want 1 (sleeping session must be resumed, not returned as-is)", launches)
	}
	got, err := mgr.Get("instance-inst-1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.State != session.StateRunning {
		t.Fatalf("state after resume = %s, want Running", got.State)
	}
	if got.AgentSessionID != "provider-sess-1" {
		t.Fatalf("AgentSessionID after resume = %q, want provider-sess-1 (must survive sleep for ResumeSessionID)", got.AgentSessionID)
	}
}

func TestCreateChildRejectsCancelledContextBeforeLaunch(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	launches := 0
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, _ *session.Session, _, _ []string) error {
			launches++
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = newTaskController(srv, "t-1").CreateChild(ctx, task.ChildSpec{TaskID: "cancelled", AgentType: "claude", OwnerTaskId: "cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateChild error = %v, want context.Canceled", err)
	}
	if launches != 0 {
		t.Fatalf("launches = %d, want no launch after cancellation", launches)
	}
}

func TestCreateChildCancelsLaunchInProgress(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	started := make(chan struct{})
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionContextHook: func(ctx context.Context, _ adapter.Adapter, _ *session.Session, _, _ []string) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newTaskController(srv, "t-1").CreateChild(ctx, task.ChildSpec{TaskID: "cancelled", AgentType: "claude", OwnerTaskId: "cancelled"})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CreateChild error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CreateChild did not stop after context cancellation")
	}
}

func TestCreateChildDoesNotPersistCredentialEnv(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	var launchEnv []string
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			launchEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	child, err := ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:      "task-secret",
		AgentType:   "claude",
		OwnerTaskId: "task-secret",
		Env:         []string{"TMPDIR=/tmp/task-secret", "SECRET_TOKEN=secret-value"},
		RecordEnv:   []string{"TMPDIR=/tmp/task-secret"},
	})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if got := envValueForTest(launchEnv, "SECRET_TOKEN"); got != "secret-value" {
		t.Fatalf("launch SECRET_TOKEN = %q, want secret-value", got)
	}
	got, err := mgr.Get(child.ID())
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got := envValueForTest(got.Env, "SECRET_TOKEN"); got != "" {
		t.Fatalf("persisted SECRET_TOKEN = %q, want empty", got)
	}
	if got := envValueForTest(got.Env, "TMPDIR"); got != "/tmp/task-secret" {
		t.Fatalf("persisted TMPDIR = %q, want /tmp/task-secret", got)
	}
}

func TestCreateChildPersistentResumeUsesFreshCredentialEnvWithoutPersisting(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	var launches [][]string
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			launches = append(launches, append([]string(nil), env...))
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:      "task-a",
		AgentType:   "claude",
		OwnerTaskId: "task-a",
		InstanceID:  "inst-1",
		Env:         []string{"TMPDIR=/tmp/task-a", "SECRET_TOKEN=first-secret"},
		RecordEnv:   []string{"TMPDIR=/tmp/task-a"},
	})
	if err != nil {
		t.Fatalf("CreateChild 1: %v", err)
	}
	if err := mgr.RecordAgentSessionID(c1.(*childSession).sess, "provider-sess-1"); err != nil {
		t.Fatalf("record agent session id: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}

	c2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:      "task-b",
		AgentType:   "claude",
		OwnerTaskId: "task-b",
		InstanceID:  "inst-1",
		Env:         []string{"TMPDIR=/tmp/task-b", "SECRET_TOKEN=fresh-secret"},
		RecordEnv:   []string{"TMPDIR=/tmp/task-b"},
	})
	if err != nil {
		t.Fatalf("CreateChild 2: %v", err)
	}
	if c2.ID() != c1.ID() {
		t.Fatalf("resume should reuse session: c1=%s c2=%s", c1.ID(), c2.ID())
	}
	if len(launches) != 2 {
		t.Fatalf("launches = %d, want 2", len(launches))
	}
	if got := envValueForTest(launches[1], "SECRET_TOKEN"); got != "fresh-secret" {
		t.Fatalf("resume launch SECRET_TOKEN = %q, want fresh-secret", got)
	}
	got, err := mgr.Get("instance-inst-1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got := envValueForTest(got.Env, "SECRET_TOKEN"); got != "" {
		t.Fatalf("persisted SECRET_TOKEN = %q, want empty", got)
	}
	if got := envValueForTest(got.Env, "TMPDIR"); got != "/tmp/task-b" {
		t.Fatalf("persisted TMPDIR after resume = %q, want /tmp/task-b", got)
	}
}

func TestCreateChildPersistentResumePreservesLaunchArgsSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	var launches [][]string
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, args, _ []string) error {
			launches = append(launches, append([]string(nil), args...))
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:      "task-a",
		AgentType:   "claude",
		OwnerTaskId: "task-a",
		InstanceID:  "inst-1",
		ExtraArgs:   []string{"--model", "auto-selected"},
	})
	if err != nil {
		t.Fatalf("CreateChild 1: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}

	_, err = ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:      "task-b",
		AgentType:   "claude",
		OwnerTaskId: "task-b",
		InstanceID:  "inst-1",
		ExtraArgs:   []string{"--model", "new-task-default"},
	})
	if err != nil {
		t.Fatalf("CreateChild 2: %v", err)
	}
	if len(launches) != 2 {
		t.Fatalf("launches = %d, want 2", len(launches))
	}
	want := []string{"--model", "auto-selected", "--permission-mode", "default"}
	if got := c1.(*childSession).sess.Snapshot().ExtraArgs; !slices.Equal(got, want) {
		t.Fatalf("persisted snapshot = %v, want %v", got, want)
	}
	if got := launches[1]; len(got) != len(want)+2 || !slices.Equal(got[:len(want)], want) || got[len(want)] != "--append-system-prompt" || !strings.Contains(got[len(want)+1], "Task-owned child") {
		t.Fatalf("resume args = %v, want persisted model, permission policy and native restricted guidance", got)
	}
}

// TestCreateChildPersistentResumeFailureFallsBackToFresh verifies that when
// resuming a sleeping instance fails (e.g. provider session expired), the
// stale sleeping session is discarded and a fresh child is created so the task
// can still execute — conversation context is lost but the instance remains
// addressable.
func TestCreateChildPersistentResumeFailureFallsBackToFresh(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	var launches int
	// failLaunch = 2 makes only the second startSession (the resume launch)
	// fail; the first create and the post-fallback fresh create both succeed.
	failLaunch := 2
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, _ []string) error {
			launches++
			if launches == failLaunch {
				return errResumeFailed
			}
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	// Create + sleep a persistent child with a provider session id.
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 1: %v", err)
	}
	firstID := c1.ID()
	if err := mgr.RecordAgentSessionID(c1.(*childSession).sess, "provider-sess-1"); err != nil {
		t.Fatalf("record agent session id: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}

	// Follow-up task: resume fails, so a fresh child is created.
	c2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-b", AgentType: "claude", OwnerTaskId: "task-b", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 2: %v", err)
	}
	if c2.ID() == firstID {
		t.Fatal("fallback should create a fresh session, not reuse the stale sleeping one")
	}
	if c2.Name() != "instance-inst-1" {
		t.Fatalf("fallback name = %q, want instance-inst-1 (instance addressing preserved)", c2.Name())
	}
	// The stale sleeping session must be gone (deleted, not left lingering).
	if _, gerr := mgr.Get(firstID); gerr == nil {
		t.Fatal("stale sleeping session should be deleted after resume failure")
	}
}

// TestCreateChildPersistentNonSleepingReusesAsIs verifies that a persistent
// child found in a non-sleeping state (e.g. still Running because the prior
// task never reached terminal sleep) is returned as-is without a relaunch.
func TestCreateChildPersistentNonSleepingReusesAsIs(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	var launches int
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, _ []string) error {
			launches++
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 1: %v", err)
	}
	// c1 is now Running (not sleeping).
	launches = 0

	c2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-b", AgentType: "claude", OwnerTaskId: "task-b", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 2: %v", err)
	}
	if c2.ID() != c1.ID() {
		t.Fatalf("non-sleeping persistent should reuse session: c1=%s c2=%s", c1.ID(), c2.ID())
	}
	if launches != 0 {
		t.Fatalf("non-sleeping reuse should not relaunch: launches = %d, want 0", launches)
	}
}

// TestCreateChildLaunchEnvNamesAResolvablePATH pins a task's child launch to the
// same PATH guarantee session launches already get. A task env carries only
// TMPDIR and credential material, so without an explicit PATH the agent binary
// is resolved against whatever environment amuxlet itself was started from — and
// a daemon started from a non-login shell has no ~/.local/bin, which is exactly
// where user-installed agents live. That made tasks fail at
// broker start with `executable "example-agent" not found in launch env PATH` while
// sessions on the same runtime kept working.
func TestCreateChildLaunchEnvNamesAResolvablePATH(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	daemonBin := filepath.Join(t.TempDir(), "bin")
	t.Setenv("PATH", daemonBin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	var capturedEnv []string
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			capturedEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := newTaskController(srv, "t-1").CreateChild(context.Background(), task.ChildSpec{
		TaskID:      "task-a",
		AgentType:   "claude",
		OwnerTaskId: "task-a",
		Env:         []string{"TMPDIR=" + t.TempDir()},
	}); err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	got := envValueForTest(capturedEnv, "PATH")
	if got == "" {
		t.Fatalf("child launch env names no PATH, so the agent resolves against amuxlet's own environment")
	}
	entries := filepath.SplitList(got)
	for _, want := range []string{daemonBin, filepath.Join(home, ".local", "bin")} {
		if !slices.Contains(entries, want) {
			t.Fatalf("child launch PATH = %q, want it to contain %s", got, want)
		}
	}
}

// TestCreateChildInjectsAgentActorTokenIntoLaunchEnvOnly pins the task actor
// credential channel: the hub-minted token reaches the agent process env but is
// stripped from the env recorded as launch params (persisted with the session,
// replayed on restart, and rendered by session show) — mirroring the session
// Run path. A token inherited from the daemon's own environment is dropped when
// the launch carries no credential of its own: the agent speaks for its own
// principal, never for whoever started amuxlet.
func TestCreateChildInjectsAgentActorTokenIntoLaunchEnvOnly(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	var launches [][]string
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			launches = append(launches, append([]string(nil), env...))
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	child, err := ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:          "task-actor",
		AgentType:       "claude",
		OwnerTaskId:     "task-actor",
		Env:             []string{"TMPDIR=/tmp/task-actor"},
		RecordEnv:       []string{"TMPDIR=/tmp/task-actor"},
		AgentActorToken: "tok-123",
	})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if got := envValueForTest(launches[0], agentActorTokenEnv); got != "tok-123" {
		t.Fatalf("launch %s = %q, want tok-123", agentActorTokenEnv, got)
	}
	got, err := mgr.Get(child.ID())
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got := envValueForTest(got.Env, agentActorTokenEnv); got != "" {
		t.Fatalf("persisted %s = %q, want empty", agentActorTokenEnv, got)
	}

	// A launch without a credential of its own must not inherit one from the
	// task env (e.g. a value exported into the daemon's own environment).
	child2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:      "task-no-actor",
		AgentType:   "claude",
		OwnerTaskId: "task-no-actor",
		Env:         []string{"TMPDIR=/tmp/task-no-actor", agentActorTokenEnv + "=daemon-token"},
		RecordEnv:   []string{"TMPDIR=/tmp/task-no-actor", agentActorTokenEnv + "=daemon-token"},
	})
	if err != nil {
		t.Fatalf("CreateChild without credential: %v", err)
	}
	if got := envValueForTest(launches[1], agentActorTokenEnv); got != "" {
		t.Fatalf("credential-less launch inherited %s = %q, want stripped", agentActorTokenEnv, got)
	}
	got2, err := mgr.Get(child2.ID())
	if err != nil {
		t.Fatalf("get session 2: %v", err)
	}
	if got := envValueForTest(got2.Env, agentActorTokenEnv); got != "" {
		t.Fatalf("persisted inherited %s = %q, want empty", agentActorTokenEnv, got)
	}
}

// TestCreateChildPersistentResumeKeepsAgentActorToken pins the persistent
// instance resume credential channel: a follow-up task carries a fresh
// hub-minted actor credential, and resuming the sleeping instance must put it
// in the relaunched agent's env. Task child sessions never persist the
// credential (the hub mints one per task), so a resume that only replays the
// stored credential would strip the token and the agent's control-plane calls
// would go anonymous again.
func TestCreateChildPersistentResumeKeepsAgentActorToken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	amuxDir := t.TempDir()
	var launches [][]string
	srv := &server{
		mgr:     mgr,
		amuxDir: amuxDir,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			launches = append(launches, append([]string(nil), env...))
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:          "task-a",
		AgentType:       "claude",
		OwnerTaskId:     "task-a",
		InstanceID:      "inst-1",
		Env:             []string{"TMPDIR=/tmp/task-a"},
		RecordEnv:       []string{"TMPDIR=/tmp/task-a"},
		AgentActorToken: "tok-first",
	})
	if err != nil {
		t.Fatalf("CreateChild 1: %v", err)
	}
	if err := mgr.RecordAgentSessionID(c1.(*childSession).sess, "provider-sess-1"); err != nil {
		t.Fatalf("record agent session id: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}

	c2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{
		TaskID:                   "task-b",
		AgentType:                "claude",
		OwnerTaskId:              "task-b",
		InstanceID:               "inst-1",
		Env:                      []string{"TMPDIR=/tmp/task-b"},
		RecordEnv:                []string{"TMPDIR=/tmp/task-b"},
		AgentActorToken:          "tok-resume",
		AgentActorTokenExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("CreateChild 2: %v", err)
	}
	if c2.ID() != c1.ID() {
		t.Fatalf("resume should reuse session: c1=%s c2=%s", c1.ID(), c2.ID())
	}
	if len(launches) != 2 {
		t.Fatalf("launches = %d, want 2", len(launches))
	}
	if got := envValueForTest(launches[0], agentActorTokenEnv); got != "tok-first" {
		t.Fatalf("first launch %s = %q, want tok-first", agentActorTokenEnv, got)
	}
	if got := envValueForTest(launches[1], agentActorTokenEnv); got != "tok-resume" {
		t.Fatalf("resume launch %s = %q, want tok-resume (the follow-up task's fresh credential must survive resume)", agentActorTokenEnv, got)
	}
	got, err := mgr.Get("instance-inst-1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got := envValueForTest(got.Env, agentActorTokenEnv); got != "" {
		t.Fatalf("persisted %s = %q, want empty (task credentials must never persist)", agentActorTokenEnv, got)
	}
	if _, err := os.Stat(sessionActorTokenPath(amuxDir, c1.ID())); !os.IsNotExist(err) {
		t.Fatalf("task child session must not write an actor token file: stat err = %v", err)
	}
}
