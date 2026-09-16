package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
)

func TestResolveSessionUniqueName(t *testing.T) {
	r := resolverForTests([]targetSessions{
		{target: targetRef{Name: "local"}, sessions: []*sessionEntry{{Session: pb.Session{Name: "work"}}}},
	})

	got, err := r.ResolveSession(context.Background(), "work")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if got.Target.Name != "local" || got.Session.Name != "work" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveSessionTargetSlashName(t *testing.T) {
	r := resolverForTests([]targetSessions{
		{target: targetRef{Name: "local"}, sessions: []*sessionEntry{{Session: pb.Session{Name: "work"}}}},
		{target: targetRef{Name: "devbox"}, sessions: []*sessionEntry{{Session: pb.Session{Name: "work"}}}},
	})

	got, err := r.ResolveSession(context.Background(), "devbox/work")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if got.Target.Name != "devbox" || got.Session.Name != "work" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveSessionAmbiguousNameReportsRefs(t *testing.T) {
	r := resolverForTests([]targetSessions{
		{target: targetRef{Name: "local"}, sessions: []*sessionEntry{{Session: pb.Session{Name: "work"}}}},
		{target: targetRef{Name: "devbox"}, sessions: []*sessionEntry{{Session: pb.Session{Name: "work"}}}},
	})

	_, err := r.ResolveSession(context.Background(), "work")
	if err == nil {
		t.Fatal("ResolveSession error = nil, want ambiguity")
	}
	if !strings.Contains(err.Error(), "local/work") || !strings.Contains(err.Error(), "devbox/work") {
		t.Fatalf("error = %v, want available refs", err)
	}
}

func TestResolveSessionID(t *testing.T) {
	r := resolverForTests([]targetSessions{
		{target: targetRef{Name: "devbox"}, sessions: []*sessionEntry{{Session: pb.Session{Id: "sess-1", Name: "work"}}}},
	})

	got, err := r.ResolveSession(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if got.Target.Name != "devbox" || got.Session.Id != "sess-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveTargetByName(t *testing.T) {
	r := resolverForTests([]targetSessions{
		{target: targetRef{Name: "devbox", ID: "sb-1"}},
	})

	got, err := r.ResolveTarget(context.Background(), "devbox")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if got.ID != "sb-1" || got.Name != "devbox" {
		t.Fatalf("target = %+v", got)
	}
}

func TestResolveTargetLocalAliasesRuntimeTarget(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	runtimeCfg := config.GetRuntime()
	r := resolverForTests([]targetSessions{
		{target: targetRef{Name: runtimeCfg.Name, ID: runtimeCfg.ID}},
	})

	got, err := r.ResolveTarget(context.Background(), "local")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if got.ID != runtimeCfg.ID || got.Name != runtimeCfg.Name {
		t.Fatalf("target = %+v, want runtime target %+v", got, runtimeCfg)
	}
}

func TestResolveTargetSessionLocalAliasesRuntimeTarget(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	runtimeCfg := config.GetRuntime()
	r := resolverForTests([]targetSessions{
		{
			target:   targetRef{Name: runtimeCfg.Name, ID: runtimeCfg.ID},
			sessions: []*sessionEntry{{Session: pb.Session{Name: "work"}}},
		},
	})

	got, err := r.ResolveSession(context.Background(), "local/work")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if got.Target.ID != runtimeCfg.ID || got.Session.Name != "work" {
		t.Fatalf("resolved = %+v", got)
	}
}

func TestTargetsToTargetSessionsIncludesEmptyTargets(t *testing.T) {
	targets := targetsToTargetSessions(nil, []*pb.Target{
		{Id: "sb-1", Name: "devbox"},
	})
	r := resolverForTests(targets)

	got, err := r.ResolveTarget(context.Background(), "devbox")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if got.ID != "sb-1" || got.Name != "devbox" {
		t.Fatalf("target = %+v", got)
	}
}

func TestTargetsToTargetSessionsCopiesSessionRouting(t *testing.T) {
	targets := targetsToTargetSessions(nil, []*pb.Target{
		{
			Id:       "sb-1",
			Name:     "devbox",
			Sessions: []*pb.Session{{Id: "sess-1", Name: "work"}},
		},
	})
	r := resolverForTests(targets)

	got, err := r.ResolveSession(context.Background(), "devbox/work")
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if got.Session.TargetID != "sb-1" || got.Session.TargetName != "devbox" {
		t.Fatalf("session routing = %+v", got.Session)
	}
}

func resolverForTests(targets []targetSessions) *sessionResolver {
	return &sessionResolver{
		listTargets: func(context.Context) ([]targetSessions, error) {
			return targets, nil
		},
	}
}

// AMUX_ACTOR_TOKEN_FILE points at the credential file amuxlet keeps current for
// the session; it is re-read on every call and wins over the AMUX_ACTOR_TOKEN
// value baked into the process environment, which the hub revokes when it
// re-mints the credential on restart. Both the JSON and bare-token on-disk
// forms are accepted.
func TestActorTokenPrefersTokenFileJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.actor-token")
	if err := os.WriteFile(path, []byte(`{"token":"tok-file","expires_at":2000000000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AMUX_ACTOR_TOKEN_FILE", path)
	t.Setenv("AMUX_ACTOR_TOKEN", "tok-env")
	if got := actorToken(); got != "tok-file" {
		t.Fatalf("actorToken() = %q, want tok-file from AMUX_ACTOR_TOKEN_FILE", got)
	}
	// A re-minted credential is picked up on the next call without a restart.
	if err := os.WriteFile(path, []byte(`{"token":"tok-rotated","expires_at":2000000000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := actorToken(); got != "tok-rotated" {
		t.Fatalf("actorToken() = %q, want tok-rotated after the file was rewritten", got)
	}
}

func TestActorTokenPrefersTokenFileBare(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.actor-token")
	if err := os.WriteFile(path, []byte("tok-bare\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AMUX_ACTOR_TOKEN_FILE", path)
	t.Setenv("AMUX_ACTOR_TOKEN", "tok-env")
	if got := actorToken(); got != "tok-bare" {
		t.Fatalf("actorToken() = %q, want tok-bare from a bare token file", got)
	}
}

func TestActorTokenFallsBackToEnvWhenTokenFileMissing(t *testing.T) {
	t.Setenv("AMUX_ACTOR_TOKEN_FILE", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("AMUX_ACTOR_TOKEN", "tok-env")
	if got := actorToken(); got != "tok-env" {
		t.Fatalf("actorToken() = %q, want AMUX_ACTOR_TOKEN fallback", got)
	}
}
