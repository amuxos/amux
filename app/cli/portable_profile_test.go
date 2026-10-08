package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestPortableTaskToolSchemasExposeRuntimeSelection(t *testing.T) {
	for _, name := range []string{"amux.task_run", "amux.task_submit", "amux.instance_spawn"} {
		t.Run(name, func(t *testing.T) {
			tool := findMCPTool(t, amuxTools(), name)
			var schema struct {
				Properties map[string]struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"properties"`
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"runtime", "model"} {
				property, ok := schema.Properties[field]
				if !ok || property.Type != "string" {
					t.Fatalf("%s cannot select execution %s: %s", name, field, tool.InputSchema)
				}
				for _, language := range []string{"v2", "专家"} {
					if !strings.Contains(property.Description, language) {
						t.Fatalf("%s guidance does not cover portable profiles in both languages: %q", field, property.Description)
					}
				}
			}
			for _, field := range schema.Required {
				if field == "runtime" || field == "model" {
					t.Fatalf("legacy profile callers must retain their defaults: %s required", field)
				}
			}
		})
	}
}

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
