package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func isolationEnabledServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rt, err := isolation.NewRuntime(config.IsolationConfig{
		Enabled:       true,
		Backend:       principal.BackendLinuxDAC,
		UIDRangeStart: 700000,
		UIDRangeEnd:   799999,
		HelperSocket:  filepath.Join(dir, "helper.sock"),
		MappingDB:     filepath.Join(dir, "mapping.db"),
		PrincipalRoot: filepath.Join(dir, "principals"),
	})
	if err != nil {
		t.Fatalf("isolation runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return &server{mgr: session.NewManager(db), amuxDir: dir, isolationRuntime: rt}
}

// The escape path this closes: an isolated agent shells out to `amux run`, its
// call reaches the control plane with no actor token, and the session it asks
// for would otherwise launch as the service account.
func TestRunRejectsUnidentifiedExecutionWhenIsolationIsConfigured(t *testing.T) {
	s := isolationEnabledServer(t)
	_, err := s.Run(context.Background(), &pb.RunRequest{
		AgentType: "claude",
		Name:      "escalate-probe",
		Cwd:       t.TempDir(),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unidentified Run error = %v (code %s), want PermissionDenied", err, status.Code(err))
	}
	if len(s.mgr.List()) != 0 {
		t.Fatal("rejected launch left a session behind")
	}
}

// The token reaches the agent process but never the session record: recorded
// env is replayed on restart and shown by `amux session show`, and a bearer
// credential has no business in either. The token-file path travels with the
// token so in-session binaries re-read the credential after the hub re-mints
// it, and it is kept out of the record for the same reason.
func TestAgentActorTokenReachesLaunchEnvOnly(t *testing.T) {
	runEnv, recordEnv := applyAgentActorToken(
		[]string{"PATH=/usr/bin"}, []string{"PATH=/usr/bin"}, "tok-123", "/amux/sessions/sess-1.actor-token")
	if !hasEnv(runEnv, "AMUX_ACTOR_TOKEN=tok-123") {
		t.Fatalf("launch env = %v, want AMUX_ACTOR_TOKEN", runEnv)
	}
	if !hasEnv(runEnv, "AMUX_ACTOR_TOKEN_FILE=/amux/sessions/sess-1.actor-token") {
		t.Fatalf("launch env = %v, want AMUX_ACTOR_TOKEN_FILE", runEnv)
	}
	for _, entry := range recordEnv {
		if strings.HasPrefix(entry, "AMUX_ACTOR_TOKEN=") || strings.HasPrefix(entry, "AMUX_ACTOR_TOKEN_FILE=") {
			t.Fatalf("recorded env leaked the agent actor token or its file: %v", recordEnv)
		}
	}
}

// A launch that inherited an actor token from the daemon environment must not
// keep it: the agent acts as its own principal, not as whoever started amuxlet.
func TestAgentActorTokenReplacesAnInheritedOne(t *testing.T) {
	runEnv, _ := applyAgentActorToken(
		[]string{"AMUX_ACTOR_TOKEN=stale", "AMUX_ACTOR_TOKEN_FILE=/stale", "PATH=/usr/bin"}, nil, "tok-123", "/fresh")
	if !hasEnv(runEnv, "AMUX_ACTOR_TOKEN=tok-123") {
		t.Fatalf("launch env = %v, want the freshly minted token", runEnv)
	}
	if hasEnv(runEnv, "AMUX_ACTOR_TOKEN=stale") {
		t.Fatalf("launch env kept the stale token: %v", runEnv)
	}
	if !hasEnv(runEnv, "AMUX_ACTOR_TOKEN_FILE=/fresh") {
		t.Fatalf("launch env = %v, want the fresh token file path", runEnv)
	}
	if hasEnv(runEnv, "AMUX_ACTOR_TOKEN_FILE=/stale") {
		t.Fatalf("launch env kept the stale token file: %v", runEnv)
	}
}

// No token means no variable at all — an unidentified launch must not inherit
// the daemon's credential by accident.
func TestAgentActorTokenAbsentClearsInheritedValue(t *testing.T) {
	runEnv, _ := applyAgentActorToken([]string{"AMUX_ACTOR_TOKEN=stale", "AMUX_ACTOR_TOKEN_FILE=/stale"}, nil, "", "")
	for _, entry := range runEnv {
		if strings.HasPrefix(entry, "AMUX_ACTOR_TOKEN=") || strings.HasPrefix(entry, "AMUX_ACTOR_TOKEN_FILE=") {
			t.Fatalf("launch env kept an actor token without one being minted: %v", runEnv)
		}
	}
}

func hasEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

// The token is minted once, at creation, but the agent process it lives in dies
// on every restart, resume and crash-recovery. Persisting it beside the session
// is what lets a relaunched agent keep speaking as its own principal instead of
// silently losing the ability to create anything.
func TestSessionActorTokenSurvivesForRelaunch(t *testing.T) {
	dir := t.TempDir()
	if got, _ := readSessionActorToken(dir, "sess-1"); got != "" {
		t.Fatalf("token for a session that never had one = %q, want empty", got)
	}
	if err := writeSessionActorToken(dir, "sess-1", "tok-123", 1800000000); err != nil {
		t.Fatal(err)
	}
	got, expires := readSessionActorToken(dir, "sess-1")
	if got != "tok-123" {
		t.Fatalf("persisted token = %q, want tok-123", got)
	}
	if expires != 1800000000 {
		t.Fatalf("persisted expiry = %d, want 1800000000", expires)
	}
	info, err := os.Stat(sessionActorTokenPath(dir, "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token file mode = %o, want 600", mode)
	}
	removeSessionActorToken(dir, "sess-1")
	if got, _ := readSessionActorToken(dir, "sess-1"); got != "" {
		t.Fatalf("token survived removal: %q", got)
	}
}

// An unidentified launch mints nothing; writing an empty token must clear any
// stale file rather than leave the previous principal's credential in place.
func TestSessionActorTokenEmptyClearsPreviousFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeSessionActorToken(dir, "sess-1", "tok-123", 0); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionActorToken(dir, "sess-1", "", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := readSessionActorToken(dir, "sess-1"); got != "" {
		t.Fatalf("stale token survived an unidentified relaunch: %q", got)
	}
}

// Files written before the expiry was recorded hold a bare token. They must
// keep working — the alternative is a relaunch that silently loses its
// credential because the format changed under it.
func TestSessionActorTokenReadsALegacyBareTokenFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionActorTokenPath(dir, "sess-1"), []byte("tok-legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, expires := readSessionActorToken(dir, "sess-1")
	if token != "tok-legacy" || expires != 0 {
		t.Fatalf("legacy file → token %q expiry %d, want tok-legacy / 0", token, expires)
	}
}

// A relaunch replays the credential minted at creation; nothing re-mints it
// locally. Once it lapses the agent's own control-plane calls start failing,
// and "the agent stopped being able to create things" is not a diagnosis
// anyone reaches unaided — so the launch says it out loud.
func TestAgentActorTokenExpiryWarning(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	if got := agentActorTokenExpiryWarning(0, now); got != "" {
		t.Fatalf("no recorded expiry → %q, want no warning", got)
	}
	if got := agentActorTokenExpiryWarning(now.Add(20*24*time.Hour).Unix(), now); got != "" {
		t.Fatalf("expiry far away → %q, want no warning", got)
	}
	soon := agentActorTokenExpiryWarning(now.Add(12*time.Hour).Unix(), now)
	if !strings.Contains(soon, "expires") || !strings.Contains(soon, "restart") {
		t.Fatalf("near expiry → %q, want it to name the expiry and the recovery", soon)
	}
	past := agentActorTokenExpiryWarning(now.Add(-time.Hour).Unix(), now)
	if !strings.Contains(past, "expired") || !strings.Contains(past, "restart") {
		t.Fatalf("already expired → %q, want it to say so and name the recovery", past)
	}
}

// The drain goroutine that reacts to a process ending must decide from what the
// launch it was started for, not from what the session looks like once it
// finally runs. A restart kills the old process, rotates the launch id and
// prepares a fresh run directory; a cleanup that asks "which launch is this
// session on?" after the fact reads the *new* id and deletes the incoming
// launch's directory out from under it. That is a race — sometimes the cleanup
// wins, sometimes it does not — which is exactly how it presented: restarts
// that fail intermittently with "lstat .../run/launch-…: no such file".
func TestProcessExitCleanupRemovesTheLaunchThatEnded(t *testing.T) {
	got := processExitCleanup("launch-old", "launch-new")
	if got.RemoveLaunch != "launch-old" {
		t.Fatalf("removing %q, want the launch that actually ended", got.RemoveLaunch)
	}
	if got.Archive {
		t.Fatal("archived a session that a relaunch has already taken over")
	}
}

func TestProcessExitCleanupArchivesWhenNoRelaunchTookOver(t *testing.T) {
	got := processExitCleanup("launch-1", "launch-1")
	if !got.Archive {
		t.Fatal("a process that ended with no relaunch in flight must archive its session")
	}
	if got.RemoveLaunch != "launch-1" {
		t.Fatalf("removing %q, want launch-1", got.RemoveLaunch)
	}
}
