package agentprofile

import (
	"context"
	"strings"
	"testing"
)

func TestPublicContractRequiresExplicitSourceBeforeDownload(t *testing.T) {
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
