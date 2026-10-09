package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

func isolatedNativeIdentity() principal.ExecutionIdentity {
	return principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: "01234567-89ab-4cde-8fab-0123456789ab",
		EffectivePrincipalID:   "01234567-89ab-4cde-8fab-0123456789ab",
		Source:                 principal.SourceSystem, IsolationBackend: principal.BackendLinuxDAC,
	}
}

func TestPiIsolatedLaunchUsesNativeTrustOverride(t *testing.T) {
	for _, args := range [][]string{nil, {"--no-approve"}, {"-na"}, {"--approve"}} {
		s, launchedArgs, _ := memoryTestServer(t)
		sess, err := s.mgr.Create("pi-isolated", "pi", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.mgr.SetExecutionIdentity(sess, isolatedNativeIdentity(), "launch"); err != nil {
			t.Fatal(err)
		}
		a, _ := adapter.Get("pi")
		if err := s.startSession(a, sess, args, nil); err != nil {
			t.Fatal(err)
		}
		got := (*launchedArgs)[0]
		want := args
		if len(want) == 0 {
			want = []string{"--approve"}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Pi trust arguments = %v, want %v", got, want)
		}
	}
}

func TestIsolatedNativeCleanupDoesNotTouchServiceTrust(t *testing.T) {
	for _, agent := range []string{"claude", "pi"} {
		t.Run(agent, func(t *testing.T) {
			s, _, _ := memoryTestServer(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AMUX_HOME", "")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
			cwd := t.TempDir()
			a, _ := adapter.Get(agent)
			truster := a.(adapter.WorkspaceTruster)
			if err := truster.EnsureWorkspaceTrusted(cwd, os.Environ()); err != nil {
				t.Fatal(err)
			}
			trustPath := filepath.Join(home, ".claude.json")
			if agent == "pi" {
				trustPath = filepath.Join(home, ".pi", "agent", "trust.json")
			}
			before, err := os.ReadFile(trustPath)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := s.mgr.Create("isolated", agent, cwd)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.mgr.SetExecutionIdentity(sess, isolatedNativeIdentity(), "launch"); err != nil {
				t.Fatal(err)
			}
			s.cleanupSessionArtifacts(sess)
			after, err := os.ReadFile(trustPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("isolated cleanup changed the service user's trust: %v", err)
			}
			if hostWorkspaceTrust(sess.Snapshot()) {
				t.Fatal("isolated launch would write the service user's trust")
			}
			if !hostWorkspaceTrust(session.Snapshot{AgentType: agent}) {
				t.Fatal("ordinary local launch lost workspace trust preparation")
			}
		})
	}
}
