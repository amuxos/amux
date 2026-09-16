package agentprofile

import (
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/task"
)

func ResolveTaskSpec(spec *task.Spec, registryPath string) error {
	if spec == nil || strings.TrimSpace(spec.Agent) == "" || spec.ResolvedProfile != nil {
		return nil
	}
	profile, err := ResolveProfile(registryPath, spec.Agent)
	if err != nil {
		if IsProfileNotFound(err) {
			_, adapterErr := adapter.Get(strings.TrimSpace(spec.Agent))
			return adapterErr
		}
		return err
	}
	if profile.Source == SourceLocal || IsManagedEvaluationProfileRef(profile.Ref()) {
		profile, err = attachSnapshotSkillFiles(profile)
		if err != nil {
			return err
		}
	}
	projection, err := ProjectProfileLaunchChecked(profile, spec.ExtraArgs, nil, LaunchOptions{Runtime: spec.Runtime, Model: spec.Model})
	if err != nil {
		return err
	}
	spec.Agent = projection.AgentType
	spec.ExtraArgs = projection.ExtraArgs
	spec.ResolvedProfile = projection.ProfileSpec
	if spec.PermissionPolicy == "" && strings.TrimSpace(profile.PermissionPolicy) != "" {
		spec.PermissionPolicy = task.PermissionPolicy(strings.TrimSpace(profile.PermissionPolicy))
	}
	return nil
}
