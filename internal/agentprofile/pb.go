package agentprofile

import (
	"fmt"
	"strings"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func ToPBProfileSpec(profile Profile) *pb.AgentProfileSpec {
	spec := &pb.AgentProfileSpec{
		Ref:                      profile.Ref(),
		PinnedRef:                profile.PinnedRef(),
		Namespace:                profile.Namespace,
		Name:                     profile.Name,
		Revision:                 profile.Revision,
		Type:                     profile.Type,
		Model:                    profile.Model,
		ConfiguredModel:          configuredModel(profile),
		Extends:                  profile.Extends,
		Source:                   string(profile.Source),
		Description:              profile.Description,
		Agents:                   profile.Agents,
		AgentsContent:            profile.AgentsContent,
		Skills:                   cloneStrings(profile.Skills),
		Subagents:                cloneStrings(profile.Subagents),
		SkillDirs:                cloneStrings(profile.SkillDirs),
		DefaultArgs:              cloneStrings(profile.DefaultArgs),
		DefaultEnv:               cloneStrings(profile.DefaultEnv),
		Envs:                     cloneStrings(profile.Envs),
		SupportedModels:          cloneStrings(profile.SupportedModels),
		ModelRef:                 configuredModel(profile),
		ModelKind:                string(profileModelKind(profile)),
		ProviderRef:              profileProviderRef(profile),
		CredentialEnvSuggestions: credentialEnvSuggestions(profile),
		SnapshotSkillFiles:       toPBSnapshotSkillFiles(profile.SnapshotSkillFiles),
		RuntimeRef:               profile.ExecutionRuntimeRef,
		ReasoningEffort:          profile.ReasoningEffort,
		EnvRefs:                  cloneStrings(profile.EnvRefs),
		McpConfig:                profile.MCPConfig,
		PermissionPolicy:         profile.PermissionPolicy,
		Provider:                 toPBProvider(profile.Provider),
		Placement: &pb.AgentProfilePlacement{
			PreferredTargets: cloneStrings(profile.Placement.PreferredTargets),
			Requires: &pb.AgentProfileRequirements{
				AgentTypes:   cloneStrings(profile.Placement.Requires.AgentTypes),
				RuntimeTools: cloneStrings(profile.Placement.Requires.RuntimeTools),
				Features:     cloneStrings(profile.Placement.Requires.Features),
				Credentials:  cloneStrings(profile.Placement.Requires.Credentials),
			},
		},
	}
	return spec
}

func toPBProvider(provider *Provider) *pb.AgentProfileProvider {
	if provider == nil {
		return nil
	}
	return &pb.AgentProfileProvider{
		Platform:          provider.Platform,
		WireApi:           provider.WireAPI,
		ApiKeyEnv:         provider.APIKeyEnv,
		ApiKeyLiteral:     provider.APIKeyLiteral,
		BaseUrl:           provider.BaseURL,
		UpstreamModel:     provider.UpstreamModel,
		ContextWindow:     int64(provider.ContextWindow),
		ReadTimeout:       provider.ReadTimeout,
		MaxTokens:         int64(provider.MaxTokens),
		Description:       provider.Description,
		PromptHint:        provider.PromptHint,
		ByAzure:           cloneBoolPtr(provider.ByAzure),
		DisableFailover:   cloneBoolPtr(provider.DisableFailover),
		Thinking:          cloneStringMap(provider.Thinking),
		Ref:               provider.Ref,
		CredentialProfile: provider.CredentialProfile,
	}
}

func FromPBProfileSpec(spec *pb.AgentProfileSpec) (Profile, error) {
	if spec == nil {
		return Profile{}, fmt.Errorf("profile spec is required")
	}
	namespace := strings.TrimSpace(spec.Namespace)
	name := strings.TrimSpace(spec.Name)
	if namespace == "" || name == "" {
		namespace, name, _, _ = splitProfileRef(spec.Ref)
	}
	if namespace == "" || name == "" {
		return Profile{}, fmt.Errorf("profile spec ref is required")
	}
	profile := Profile{
		Namespace:                namespace,
		Name:                     name,
		Revision:                 strings.TrimSpace(spec.Revision),
		Type:                     strings.TrimSpace(spec.Type),
		Model:                    strings.TrimSpace(spec.Model),
		ConfiguredModel:          strings.TrimSpace(spec.ConfiguredModel),
		Extends:                  strings.TrimSpace(spec.Extends),
		Description:              spec.Description,
		Agents:                   strings.TrimSpace(spec.Agents),
		AgentsContent:            spec.AgentsContent,
		Skills:                   cloneStrings(spec.Skills),
		Subagents:                cloneStrings(spec.Subagents),
		SkillDirs:                cloneStrings(spec.SkillDirs),
		DefaultArgs:              cloneStrings(spec.DefaultArgs),
		DefaultEnv:               cloneStrings(spec.DefaultEnv),
		Envs:                     cloneStrings(spec.Envs),
		SupportedModels:          cloneStrings(spec.SupportedModels),
		EnvRefs:                  cloneStrings(spec.EnvRefs),
		MCPConfig:                strings.TrimSpace(spec.McpConfig),
		PermissionPolicy:         strings.TrimSpace(spec.PermissionPolicy),
		Provider:                 fromPBProvider(spec.Provider),
		CredentialEnvSuggestions: cloneStringMap(spec.CredentialEnvSuggestions),
		SnapshotSkillFiles:       fromPBSnapshotSkillFiles(spec.SnapshotSkillFiles),
		Source:                   Source(strings.TrimSpace(spec.Source)),
		ExecutionRuntimeRef:      strings.TrimSpace(spec.RuntimeRef),
		ReasoningEffort:          strings.TrimSpace(spec.ReasoningEffort),
	}
	profile.Writable = profile.Source == SourceLocal
	if profile.Revision == "" && strings.TrimSpace(spec.PinnedRef) != "" {
		_, _, revision, _ := splitProfileRef(spec.PinnedRef)
		profile.Revision = revision
	}
	if spec.Placement != nil {
		profile.Placement.PreferredTargets = cloneStrings(spec.Placement.PreferredTargets)
		if spec.Placement.Requires != nil {
			profile.Placement.Requires = Requirements{
				AgentTypes:   cloneStrings(spec.Placement.Requires.AgentTypes),
				RuntimeTools: cloneStrings(spec.Placement.Requires.RuntimeTools),
				Features:     cloneStrings(spec.Placement.Requires.Features),
				Credentials:  cloneStrings(spec.Placement.Requires.Credentials),
			}
		}
	}
	if profile.Type == "" {
		return Profile{}, fmt.Errorf("profile spec type is required")
	}
	if profile.ConfiguredModel == "" {
		profile.ConfiguredModel = strings.TrimSpace(spec.ModelRef)
	}
	if profile.ConfiguredModel == "" {
		profile.ConfiguredModel = profile.Model
	}
	if profile.Source == "" {
		profile.Source = SourcePack
	}
	return profile, nil
}

func toPBSnapshotSkillFiles(files []ProfileSkillFile) []*pb.AgentProfileSkillFile {
	out := make([]*pb.AgentProfileSkillFile, 0, len(files))
	for _, file := range files {
		out = append(out, &pb.AgentProfileSkillFile{Skill: file.Skill, Path: file.Path, Data: append([]byte(nil), file.Data...), Mode: file.Mode})
	}
	return out
}

func fromPBSnapshotSkillFiles(files []*pb.AgentProfileSkillFile) []ProfileSkillFile {
	out := make([]ProfileSkillFile, 0, len(files))
	for _, file := range files {
		if file != nil {
			out = append(out, ProfileSkillFile{Skill: file.GetSkill(), Path: file.GetPath(), Data: append([]byte(nil), file.GetData()...), Mode: file.GetMode()})
		}
	}
	return out
}

func configuredModel(profile Profile) string {
	if model := strings.TrimSpace(profile.ConfiguredModel); model != "" {
		return model
	}
	return strings.TrimSpace(profile.Model)
}

func fromPBProvider(provider *pb.AgentProfileProvider) *Provider {
	if provider == nil {
		return nil
	}
	return &Provider{
		Platform:          strings.TrimSpace(provider.Platform),
		WireAPI:           strings.TrimSpace(provider.WireApi),
		APIKeyEnv:         strings.TrimSpace(provider.ApiKeyEnv),
		APIKeyLiteral:     strings.TrimSpace(provider.ApiKeyLiteral),
		BaseURL:           strings.TrimSpace(provider.BaseUrl),
		UpstreamModel:     strings.TrimSpace(provider.UpstreamModel),
		ContextWindow:     int(provider.ContextWindow),
		ReadTimeout:       strings.TrimSpace(provider.ReadTimeout),
		MaxTokens:         int(provider.MaxTokens),
		Description:       provider.Description,
		PromptHint:        strings.TrimSpace(provider.PromptHint),
		ByAzure:           cloneBoolPtr(provider.ByAzure),
		DisableFailover:   cloneBoolPtr(provider.DisableFailover),
		Thinking:          cloneStringMap(provider.Thinking),
		Ref:               strings.TrimSpace(provider.Ref),
		CredentialProfile: strings.TrimSpace(provider.CredentialProfile),
	}
}

func profileModelKind(profile Profile) ModelKind {
	if strings.TrimSpace(configuredModel(profile)) == "" {
		return ModelKindDefault
	}
	if profile.Provider != nil {
		return ModelKindProvider
	}
	return ModelKindNative
}

func profileProviderRef(profile Profile) string {
	if profile.Provider == nil {
		return ""
	}
	return strings.TrimSpace(profile.Provider.Ref)
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	next := *value
	return &next
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}
