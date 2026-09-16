package amuxadapter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/coordination"
	"github.com/amuxos/amux/internal/task"
)

const profileCollaboratorPrefix = "agent-profile:"

// ProfileLoader returns the Agent Profiles visible to the amuxd control plane.
type ProfileLoader func(context.Context) ([]agentprofile.Profile, error)

// ProfileCatalog exposes resolvable, non-development Agent Profiles as
// internal collaborators and also builds their fixed Task identity.
type ProfileCatalog struct {
	load ProfileLoader
}

// NewProfileCatalog constructs an internal collaborator catalog.
func NewProfileCatalog(load ProfileLoader) (*ProfileCatalog, error) {
	if load == nil {
		return nil, errors.New("agent profile loader is required")
	}
	return &ProfileCatalog{load: load}, nil
}

// List returns the public, deterministic collaborator catalog.
func (c *ProfileCatalog) List(ctx context.Context, _ coordination.CatalogScope) ([]coordination.CollaboratorDescriptor, error) {
	profiles, err := c.load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load agent profiles: %w", err)
	}
	result := make([]coordination.CollaboratorDescriptor, 0, len(profiles))
	for _, profile := range profiles {
		if strings.HasSuffix(profile.Namespace, "-dev") {
			continue
		}
		result = append(result, descriptorForProfile(profile))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// ResolveTarget accepts only a collaborator ID that is present in the current
// catalog. This prevents callers from bypassing discovery with a raw Profile
// ref and rejects a stale pinned revision after a Pack update.
func (c *ProfileCatalog) ResolveTarget(ctx context.Context, collaboratorID string, _ coordination.CatalogScope) (coordination.ResolvedCollaborator, error) {
	collaboratorID = strings.TrimSpace(collaboratorID)
	if !strings.HasPrefix(collaboratorID, profileCollaboratorPrefix) {
		return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown collaborator %q", collaboratorID)
	}
	profiles, err := c.load(ctx)
	if err != nil {
		return coordination.ResolvedCollaborator{}, fmt.Errorf("load agent profiles: %w", err)
	}
	for _, profile := range profiles {
		if strings.HasSuffix(profile.Namespace, "-dev") {
			continue
		}
		if profileCollaboratorID(profile) == collaboratorID {
			return coordination.ResolvedCollaborator{BackendKind: coordination.BackendKindAmuxTask, TargetRef: profile.PinnedRef(), TrustLevel: coordination.TrustInternal}, nil
		}
	}
	return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown or stale collaborator %q", collaboratorID)
}

// BuildTaskSpec resolves the Delegation's already-validated pinned target into
// the Task control plane's Agent field.
func (c *ProfileCatalog) BuildTaskSpec(ctx context.Context, route *coordination.Route) (task.Spec, error) {
	if route == nil {
		return task.Spec{}, errors.New("delegation route is required")
	}
	profiles, err := c.load(ctx)
	if err != nil {
		return task.Spec{}, fmt.Errorf("load agent profiles: %w", err)
	}
	for _, profile := range profiles {
		if !strings.HasSuffix(profile.Namespace, "-dev") && profile.PinnedRef() == route.TargetRef {
			return task.Spec{Agent: profile.PinnedRef()}, nil
		}
	}
	return task.Spec{}, fmt.Errorf("delegation target %q is no longer available", route.TargetRef)
}

func descriptorForProfile(profile agentprofile.Profile) coordination.CollaboratorDescriptor {
	return coordination.CollaboratorDescriptor{
		ID:           profileCollaboratorID(profile),
		Name:         profile.Name,
		Description:  strings.TrimSpace(profile.Description),
		Kind:         coordination.CollaboratorInternalAgent,
		Availability: coordination.AvailabilityConfigured,
		Capabilities: []coordination.CollaboratorCapability{
			coordination.CapabilityProgress,
			coordination.CapabilityStructuredResult,
			coordination.CapabilityPermissionResponse,
			coordination.CapabilityCancelable,
		},
		Skills: append([]string(nil), profile.Skills...),
	}
}

func profileCollaboratorID(profile agentprofile.Profile) string {
	return profileCollaboratorPrefix + profile.PinnedRef()
}
