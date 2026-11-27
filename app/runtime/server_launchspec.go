package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/distribution"
	"github.com/amuxos/amux/internal/mux"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func launchSpecSnapshot(req *pb.RunRequest) string {
	if req == nil {
		return ""
	}
	type profileSnapshot struct {
		Ref              string                    `json:"ref,omitempty"`
		PinnedRef        string                    `json:"pinned_ref,omitempty"`
		Namespace        string                    `json:"namespace,omitempty"`
		Name             string                    `json:"name,omitempty"`
		Revision         string                    `json:"revision,omitempty"`
		Type             string                    `json:"type,omitempty"`
		Model            string                    `json:"model,omitempty"`
		ConfiguredModel  string                    `json:"configured_model,omitempty"`
		Source           string                    `json:"source,omitempty"`
		Description      string                    `json:"description,omitempty"`
		Agents           string                    `json:"agents,omitempty"`
		Skills           []string                  `json:"skills,omitempty"`
		SkillDirs        []string                  `json:"skill_dirs,omitempty"`
		Envs             []string                  `json:"envs,omitempty"`
		EnvRefs          []string                  `json:"env_refs,omitempty"`
		MCPConfig        string                    `json:"mcp_config,omitempty"`
		PermissionPolicy string                    `json:"permission_policy,omitempty"`
		RuntimeRef       string                    `json:"runtime_ref,omitempty"`
		ReasoningEffort  string                    `json:"reasoning_effort,omitempty"`
		ProviderRef      string                    `json:"provider_ref,omitempty"`
		Provider         *pb.AgentProfileProvider  `json:"provider,omitempty"`
		Placement        *pb.AgentProfilePlacement `json:"placement,omitempty"`
	}
	type snapshot struct {
		AgentExecutable       string           `json:"agent_executable,omitempty"`
		AgentProfile          string           `json:"agent_profile,omitempty"`
		AgentType             string           `json:"agent_type,omitempty"`
		Name                  string           `json:"name,omitempty"`
		Cwd                   string           `json:"cwd,omitempty"`
		Repo                  string           `json:"repo,omitempty"`
		RepoRef               string           `json:"repo_ref,omitempty"`
		IdleTimeoutSeconds    int64            `json:"idle_timeout_seconds,omitempty"`
		IdleTimeoutSecondsSet bool             `json:"idle_timeout_seconds_set,omitempty"`
		WorkspaceSource       string           `json:"workspace_source,omitempty"`
		WorkspaceScope        string           `json:"workspace_scope,omitempty"`
		ProfileSpec           *profileSnapshot `json:"profile_spec,omitempty"`
	}
	out := snapshot{AgentExecutable: req.AgentExecutable, AgentProfile: req.AgentProfile, AgentType: req.AgentType, Name: req.Name, Cwd: req.Cwd, Repo: req.Repo, RepoRef: req.RepoRef, IdleTimeoutSeconds: req.IdleTimeoutSeconds, IdleTimeoutSecondsSet: req.IdleTimeoutSecondsSet, WorkspaceSource: req.WorkspaceSource, WorkspaceScope: req.WorkspaceScope}
	if spec := req.ProfileSpec; spec != nil {
		profile := &profileSnapshot{Ref: spec.Ref, PinnedRef: spec.PinnedRef, Namespace: spec.Namespace, Name: spec.Name, Revision: spec.Revision, Type: spec.Type, Model: spec.Model, ConfiguredModel: spec.ConfiguredModel, Source: spec.Source, Description: spec.Description, Agents: spec.Agents, Skills: append([]string(nil), spec.Skills...), SkillDirs: append([]string(nil), spec.SkillDirs...), Envs: append([]string(nil), spec.Envs...), EnvRefs: append([]string(nil), spec.EnvRefs...), MCPConfig: spec.McpConfig, PermissionPolicy: spec.PermissionPolicy, RuntimeRef: spec.RuntimeRef, ReasoningEffort: spec.ReasoningEffort, ProviderRef: spec.ProviderRef, Provider: spec.Provider, Placement: spec.Placement}
		out.ProfileSpec = profile
	}
	data, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(data)
}
func launchSpecExplicitCwd(spec string) string {
	var decoded struct {
		Cwd string `json:"cwd"`
	}
	if decodeLaunchSpec(spec, &decoded) {
		return decoded.Cwd
	}
	return ""
}
func launchSpecPermissionPolicy(spec string) string {
	var decoded struct {
		ProfileSpec *struct {
			PermissionPolicy string `json:"permission_policy"`
		} `json:"profile_spec"`
	}
	if decodeLaunchSpec(spec, &decoded) && decoded.ProfileSpec != nil {
		return strings.TrimSpace(decoded.ProfileSpec.PermissionPolicy)
	}
	return ""
}

func launchSpecReasoningEffort(spec string) string {
	var decoded struct {
		ProfileSpec *struct {
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"profile_spec"`
	}
	if decodeLaunchSpec(spec, &decoded) && decoded.ProfileSpec != nil {
		return strings.TrimSpace(decoded.ProfileSpec.ReasoningEffort)
	}
	return ""
}

// launchSpecModel returns the frozen requested model, never the live model
// reported by a provider (which may include provider-specific variant suffixes).
func launchSpecModel(spec string) string {
	var decoded struct {
		ProfileSpec *struct {
			Model           string `json:"model"`
			ConfiguredModel string `json:"configured_model"`
		} `json:"profile_spec"`
	}
	if !decodeLaunchSpec(spec, &decoded) || decoded.ProfileSpec == nil {
		return ""
	}
	if model := strings.TrimSpace(decoded.ProfileSpec.Model); model != "" {
		return model
	}
	return strings.TrimSpace(decoded.ProfileSpec.ConfiguredModel)
}

func launchSpecModelVariantRef(spec, agentType, effectiveModel string) string {
	var decoded struct {
		ProfileSpec *struct {
			ConfiguredModel string `json:"configured_model"`
			RuntimeRef      string `json:"runtime_ref"`
		} `json:"profile_spec"`
	}
	if effectiveModel == "" || !decodeLaunchSpec(spec, &decoded) || decoded.ProfileSpec == nil {
		return ""
	}
	ref := strings.TrimSpace(decoded.ProfileSpec.ConfiguredModel)
	if ref == "" || ref == effectiveModel {
		return ""
	}
	runtimeRef := strings.TrimSpace(decoded.ProfileSpec.RuntimeRef)
	if runtimeRef == "" {
		runtimeRef = agentType
	}
	runtime, err := agentprofile.ResolveRuntimeOption(runtimeRef)
	if err != nil {
		return ""
	}
	model, err := agentprofile.ResolveRuntimeModel(runtime, ref)
	if err != nil || model.BackendVariant == "" || model.AgentModel != effectiveModel {
		return ""
	}
	return ref
}

func launchSpecUsesProvider(spec string) bool {
	var decoded struct {
		ProfileSpec *struct {
			Provider *pb.AgentProfileProvider `json:"provider"`
		} `json:"profile_spec"`
	}
	return decodeLaunchSpec(spec, &decoded) && decoded.ProfileSpec != nil && decoded.ProfileSpec.Provider != nil
}

type providerConversationIdentity struct {
	configured    bool
	ref           string
	platform      string
	wireAPI       string
	baseURL       string
	upstreamModel string
}

func providerNativeResumeCompatible(previousLaunchSpec string, replacement *pb.RunRequest) bool {
	var previous struct {
		ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
	}
	_ = decodeLaunchSpec(previousLaunchSpec, &previous)
	return providerConversationIdentityFor(previous.ProfileSpec) == providerConversationIdentityFor(replacement.GetProfileSpec())
}

func providerConversationIdentityFor(spec *pb.AgentProfileSpec) providerConversationIdentity {
	if spec == nil || spec.Provider == nil {
		return providerConversationIdentity{}
	}
	provider := spec.Provider
	ref := strings.TrimSpace(spec.ProviderRef)
	if ref == "" {
		ref = strings.TrimSpace(provider.Ref)
	}
	return providerConversationIdentity{
		configured:    true,
		ref:           ref,
		platform:      strings.TrimSpace(provider.Platform),
		wireAPI:       strings.TrimSpace(provider.WireApi),
		baseURL:       strings.TrimSpace(provider.BaseUrl),
		upstreamModel: strings.TrimSpace(provider.UpstreamModel),
	}
}

func launchSpecProviderProfileSkillRoots(spec, cwd string) []string {
	var decoded struct {
		AgentType   string `json:"agent_type"`
		ProfileSpec *struct {
			Type   string   `json:"type"`
			Skills []string `json:"skills"`
		} `json:"profile_spec"`
	}
	if !decodeLaunchSpec(spec, &decoded) || decoded.ProfileSpec == nil {
		return nil
	}
	agentType := strings.TrimSpace(decoded.ProfileSpec.Type)
	if agentType == "" {
		agentType = strings.TrimSpace(decoded.AgentType)
	}
	if !adapter.IsCodexCompatible(agentType) {
		return nil
	}
	layout, err := adapter.Layout(agentType)
	if err != nil {
		return nil
	}
	placement := layout[adapter.SurfaceSkills]
	if !placement.Supported || placement.Kind != "cwd-rel" {
		return nil
	}
	skillDir := placement.Path
	roots := make([]string, 0, len(decoded.ProfileSpec.Skills))
	for _, skill := range decoded.ProfileSpec.Skills {
		skill = strings.TrimSpace(skill)
		if skill == "" || filepath.Base(skill) != skill {
			continue
		}
		roots = append(roots, filepath.Join(cwd, skillDir, skill))
	}
	return roots
}
func (s *server) materializeProviderCredentialEnvFromLaunchSpec(ctx context.Context, identity principal.ExecutionIdentity, spec, agentType string, env []string) ([]string, error) {
	var decoded struct {
		ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
	}
	if !decodeLaunchSpec(spec, &decoded) || decoded.ProfileSpec == nil {
		return nil, nil
	}
	return s.materializeProviderCredentialEnv(ctx, identity, &pb.RunRequest{AgentType: agentType, Env: append([]string(nil), env...), ProfileSpec: decoded.ProfileSpec})
}
func responsesChatOptionsFromLaunchSpec(spec, providerID, selectedBaseURL string) (*mux.ResponsesChatOptions, error) {
	var decoded struct {
		ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
	}
	if !decodeLaunchSpec(spec, &decoded) || decoded.ProfileSpec == nil || decoded.ProfileSpec.Provider == nil {
		return nil, nil
	}
	provider := decoded.ProfileSpec.Provider
	if strings.TrimSpace(provider.WireApi) != "responses-via-chat-completions" {
		return nil, nil
	}
	if err := distribution.RequireResponsesChat(); err != nil {
		return nil, err
	}
	if providerID != "codex-app-server" {
		return nil, fmt.Errorf("wire_api responses-via-chat-completions requires codex-app-server, got %s", providerID)
	}
	baseURL := strings.TrimSpace(provider.BaseUrl)
	if strings.TrimSpace(selectedBaseURL) != "" {
		baseURL = strings.TrimSpace(selectedBaseURL)
	}
	apiKeyEnv := strings.TrimSpace(provider.ApiKeyEnv)
	if baseURL == "" || apiKeyEnv == "" {
		return nil, fmt.Errorf("wire_api responses-via-chat-completions requires provider base_url and api_key_env")
	}
	return &mux.ResponsesChatOptions{UpstreamBaseURL: baseURL, APIKeyEnv: apiKeyEnv}, nil
}
func decodeLaunchSpec(spec string, dst any) bool {
	if strings.TrimSpace(spec) == "" {
		return false
	}
	return json.Unmarshal([]byte(spec), dst) == nil
}
