package cli

import (
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestPortableProfileDiscoveryShowsContentWithoutAdapterError(t *testing.T) {
	profile := agentprofile.Profile{Namespace: "team", Name: "reviewer", Description: "Review changes"}
	decoded, err := discoveredAgentProfileFromPB(&pb.AgentProfileInfo{Profile: agentprofile.ToPBProfileSpec(profile)})
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []map[string]any{profileMCPView(decoded.Profile), profileMCPViewListSummary(decoded.Profile, nil)} {
		if _, exists := view["agent_type"]; exists {
			t.Fatalf("content has engine: %v", view)
		}
		if view["runtime_required"] != true {
			t.Fatalf("missing execution requirement: %v", view)
		}
	}
	if len(profileMCPCapabilitySets([]discoveredAgentProfile{decoded})) != 0 {
		t.Fatal("engine-free content reports an adapter")
	}
	for _, marker := range []string{"runtime", "专家"} {
		if !strings.Contains(profileSelectionGuidance(), marker) {
			t.Fatal("selection guidance missing language")
		}
	}
}
