package mux

import (
	"os"
	"testing"

	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
)

func TestBrokerLaunchContextFileRoundTripAndRemove(t *testing.T) {
	dir := t.TempDir()
	want := isolation.LaunchContext{
		LaunchID: "launch-1",
		Identity: &principal.ExecutionIdentity{
			OriginActorPrincipalID: "ad406ae7-4e41-41f5-bd55-6169009e90bc",
			IdentityMode:           principal.IdentityModeIdentified,
			EffectivePrincipalID:   "3b19ccbd-4462-4c21-91ed-9bc98ea94dc9",
		},
	}
	path, err := writeBrokerLaunchContextFile(dir, "session-1", want)
	if err != nil {
		t.Fatalf("writeBrokerLaunchContextFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat launch context: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	got, err := readBrokerLaunchContextFile(path)
	if err != nil {
		t.Fatalf("readBrokerLaunchContextFile: %v", err)
	}
	if got.LaunchID != want.LaunchID || got.Identity == nil ||
		got.Identity.EffectivePrincipalID != want.Identity.EffectivePrincipalID {
		t.Fatalf("got = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("launch context file still exists: %v", err)
	}
}
