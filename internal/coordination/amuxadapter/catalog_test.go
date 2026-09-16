package amuxadapter

import (
	"context"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/coordination"
)

func TestProfileCatalogListsStableNonDevCollaboratorsAndResolvesTarget(t *testing.T) {
	t.Parallel()

	catalog, err := NewProfileCatalog(func(context.Context) ([]agentprofile.Profile, error) {
		return []agentprofile.Profile{
			{Namespace: "team-dev", Name: "draft", Revision: "draft-rev", Type: "codex"},
			{
				Namespace:   "team",
				Name:        "reviewer",
				Revision:    "abc123",
				Type:        "codex",
				Description: "Reviews changes for correctness",
				Skills:      []string{"code-review", "verify-work"},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("NewProfileCatalog: %v", err)
	}

	collaborators, err := catalog.List(context.Background(), coordination.CatalogScope{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(collaborators) != 1 {
		t.Fatalf("collaborators = %#v, want one non-dev profile", collaborators)
	}
	got := collaborators[0]
	if got.ID != "agent-profile:team/reviewer@abc123" || got.Name != "reviewer" || got.Description != "Reviews changes for correctness" {
		t.Fatalf("collaborator = %#v", got)
	}
	if got.Kind != coordination.CollaboratorInternalAgent || got.Availability != coordination.AvailabilityConfigured {
		t.Fatalf("kind/availability = %q/%q", got.Kind, got.Availability)
	}
	if len(got.Capabilities) != 4 || len(got.Skills) != 2 || got.Skills[0] != "code-review" {
		t.Fatalf("capabilities/skills = %#v / %#v", got.Capabilities, got.Skills)
	}

	target, err := catalog.ResolveTarget(context.Background(), got.ID, coordination.CatalogScope{})
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if target.TargetRef != "team/reviewer@abc123" || target.TrustLevel != coordination.TrustInternal {
		t.Fatalf("target = %#v", target)
	}
}

func TestProfileCatalogRejectsConstructedOrStaleCollaboratorID(t *testing.T) {
	t.Parallel()

	catalog, err := NewProfileCatalog(func(context.Context) ([]agentprofile.Profile, error) {
		return []agentprofile.Profile{{Namespace: "team", Name: "reviewer", Revision: "current", Type: "codex"}}, nil
	})
	if err != nil {
		t.Fatalf("NewProfileCatalog: %v", err)
	}
	if _, err := catalog.ResolveTarget(context.Background(), "agent-profile:team/reviewer@stale", coordination.CatalogScope{}); err == nil {
		t.Fatal("ResolveTarget accepted a stale collaborator id")
	}
	if _, err := catalog.ResolveTarget(context.Background(), "team/reviewer@current", coordination.CatalogScope{}); err == nil {
		t.Fatal("ResolveTarget accepted a caller-constructed target ref")
	}
}
