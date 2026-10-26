package agentprofile

import (
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/task"
)

func ResolveTaskSpec(spec *task.Spec, registryPath string) error {
	if spec == nil || strings.TrimSpace(spec.Agent) == "" {
		return nil
	}
	if spec.ResolvedProfile != nil {
		return ApplyTaskPermissionPolicy(spec, false)
	}
	profile, err := ResolveProfile(registryPath, spec.Agent)
	if err != nil {
		if IsProfileNotFound(err) {
			_, adapterErr := adapter.Get(strings.TrimSpace(spec.Agent))
			if adapterErr != nil {
				return adapterErr
			}
			return ApplyTaskPermissionPolicy(spec, false)
		}
		return err
	}
	if profile.Source == SourceLocal || IsManagedEvaluationProfileRef(profile.Ref()) {
		profile, err = attachSnapshotSkillFiles(profile)
		if err != nil {
			return err
		}
	}
	opts := LaunchOptions{Runtime: spec.Runtime, Model: spec.Model, ReasoningEffort: spec.ReasoningEffort}
	preset, hasPreset := BuiltinLaunchDefaults(profile)
	if !hasPreset && profile.Type == "" && strings.TrimSpace(opts.Runtime) == "" {
		// Task defaults belong to the distribution, not to portable Pack content.
		// Borrow only execution selection, never another expert's permissions.
		if defaultProfile, ok := ResolveBuiltin(DefaultProfileRef); ok {
			preset, hasPreset = BuiltinLaunchDefaults(defaultProfile)
			preset.PermissionMode = ""
		}
	}
	var projection LaunchProjection
	if hasPreset {
		// Task permissions belong to permission_policy, including builtin Tasks.
		preset.PermissionMode = ""
		projection, err = projectProfileLaunchWithPreset(profile, spec.ExtraArgs, nil, opts, preset)
	} else {
		projection, err = ProjectProfileLaunchChecked(profile, spec.ExtraArgs, nil, opts)
	}
	if err != nil {
		return err
	}
	spec.Agent = projection.AgentType
	spec.ExtraArgs = projection.ExtraArgs
	spec.ResolvedProfile = projection.ProfileSpec
	if spec.PermissionPolicy == "" && strings.TrimSpace(profile.PermissionPolicy) != "" {
		spec.PermissionPolicy = task.PermissionPolicy(strings.TrimSpace(profile.PermissionPolicy))
	}
	return ApplyTaskPermissionPolicy(spec, false)
}
