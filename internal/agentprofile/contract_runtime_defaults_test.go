package agentprofile

import (
	"context"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/distribution"
)

func TestPublicContractDefaultsToPublicSCMRelease(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "")
	want := "https://github.com/amuxos/agent-pack-contract-releases/releases/latest/download/agent-pack-contract-scm-latest.tar.gz"
	if distribution.ContractReleaseURL != want {
		t.Fatalf("ContractReleaseURL = %q, want %q", distribution.ContractReleaseURL, want)
	}
	if got := managedContractSCMURL(); got != want {
		t.Fatalf("managedContractSCMURL = %q, want public default %q", got, want)
	}
}

func TestPublicContractEnvOverrideTakesPrecedence(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	if got := managedContractSCMURL(); got != "https://contract.example/runtime.tar.gz" {
		t.Fatalf("managedContractSCMURL = %q", got)
	}
}

func TestManagedContractEnsureRequiresConfiguredSource(t *testing.T) {
	previous := distribution.ContractReleaseURL
	distribution.ContractReleaseURL = ""
	t.Cleanup(func() { distribution.ContractReleaseURL = previous })
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "")
	installer := managedContractRuntimeInstaller{
		download: func(context.Context, string) ([]byte, error) {
			t.Fatal("attempted a download without an explicit Contract source")
			return nil, nil
		},
	}
	_, err := installer.ensure(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "AGENT_PACK_CONTRACT_SCM_URL") {
		t.Fatalf("ensure = %v; want source configuration guidance", err)
	}
}
