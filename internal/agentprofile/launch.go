package agentprofile

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/amuxhome"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

var ErrProfileNotFound = errors.New("agent profile not found")

type LaunchOptions struct {
	Model           string
	Runtime         string
	ReasoningEffort string
}

type LaunchProjection struct {
	AgentType       string
	AgentProfile    string
	ProfileSpec     *pb.AgentProfileSpec
	ExtraArgs       []string
	Env             []string
	Model           string
	Runtime         string
	ReasoningEffort string
}

func DefaultPackRegistryPath() string {
	return filepath.Join(amuxhome.Dir(), "agent-packs", "registry.json")
}

func ResolveProfile(registryPath, ref string) (Profile, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Profile{}, fmt.Errorf("agent profile is required")
	}
	if profile, ok := ResolveBuiltin(ref); ok {
		// Attach any installed Git-backed pack's same-named skill as an external
		// override for this builtin profile so discovery/launch surfaces the
		// periodically-synced (autosync) version instead of only the embedded one.
		// Missing/unreadable registry degrades to the embedded skill unchanged.
		return attachExternalSkillOverrides(profile, registryPath)
	}
	if strings.HasPrefix(ref, BuiltinNamespace+"/") {
		return Profile{}, fmt.Errorf("unknown agent profile %q: %w", ref, ErrProfileNotFound)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		return Profile{}, err
	}
	profile, ok, err := ResolvePackProfile(registry, ref)
	if err != nil {
		return Profile{}, err
	}
	if ok {
		return profile, nil
	}
	return Profile{}, fmt.Errorf("unknown agent profile %q: %w", ref, ErrProfileNotFound)
}

func IsProfileNotFound(err error) bool {
	return errors.Is(err, ErrProfileNotFound)
}

func ProjectProfileLaunch(profile Profile, explicitArgs []string, explicitEnv []string, opts LaunchOptions) LaunchProjection {
	defaultArgs := cloneStrings(profile.DefaultArgs)
	args := append(cloneStrings(defaultArgs), explicitArgs...)
	effectiveModel := effectiveLaunchModel(profile.Type, profile.Model, args, opts.Model)
	if !AgentArgsContainModel(profile.Type, args) {
		defaultArgs = append(ModelArgs(profile.Type, effectiveModel), defaultArgs...)
	}
	extraArgs := launchProcessArgs(profile.Type, defaultArgs)
	extraArgs = appendDistributionDefaultProviderArgs(profile, effectiveModel, args, extraArgs)
	extraArgs = append(extraArgs, cloneStrings(explicitArgs)...)
	extraArgs = appendUpstreamProviderArgs(profile, effectiveModel, extraArgs)
	spec := ToPBProfileSpec(profile)
	spec.Model = effectiveModel
	return LaunchProjection{
		AgentType:    profile.Type,
		AgentProfile: profile.PinnedRef(),
		ProfileSpec:  spec,
		ExtraArgs:    extraArgs,
		Env:          append(cloneStrings(profile.DefaultEnv), explicitEnv...),
		Model:        effectiveModel,
	}
}

// appendUpstreamProviderArgs applies the provider projections that route the
// agent process to a custom upstream (openai-compat, codex
// custom provider, pi). Where the wire protocol sends the top-level model
// value, the earlier `--model` is rewritten to the upstream model name; only
// the user-facing model ref stays in the launch spec. Shared by
// ProjectProfileLaunch and ProjectModelSwitch.
func appendUpstreamProviderArgs(profile Profile, effectiveModel string, args []string) []string {
	args = appendDistributionUpstreamProviderArgs(profile, effectiveModel, args)
	if providerArgs := codexOpenAICompatLaunchArgs(profile, effectiveModel); len(providerArgs) > 0 {
		args = replaceStructuredModelArg(args, strings.TrimSpace(profile.Provider.UpstreamModel))
		args = append(args, providerArgs...)
	}
	if providerArgs := piProviderLaunchArgs(profile, effectiveModel); len(providerArgs) > 0 {
		args = append(args, providerArgs...)
	}
	return args
}

// replaceStructuredModelArg swaps the first `--model <x>` (or `--model=<x>`)
// in args with a custom provider's upstream model name. The user-facing model
// ref remains in the LaunchSpec; only the structured provider process sees the
// upstream id.
func replaceStructuredModelArg(args []string, upstreamModel string) []string {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return args
	}
	out := make([]string, 0, len(args))
	replaced := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !replaced && a == "--model" {
			out = append(out, "--model", upstreamModel)
			if i+1 < len(args) {
				i++ // skip the existing model value
			}
			replaced = true
			continue
		}
		if !replaced && strings.HasPrefix(a, "--model=") {
			out = append(out, "--model="+upstreamModel)
			replaced = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// codexOpenAICompatLaunchArgs registers an Agent Profile openai-compat binding
// through Codex's process-global custom provider configuration. Codex custom
// providers use the Responses API; workspace-local AGENTS/config projection is
// intentionally not used for connection or credential settings.
func codexOpenAICompatLaunchArgs(profile Profile, effectiveModel string) []string {
	if strings.TrimSpace(profile.Type) != "codex" || profile.Provider == nil {
		return nil
	}
	provider := profile.Provider
	if strings.TrimSpace(provider.Platform) != "openai-compat" ||
		strings.TrimSpace(effectiveModel) != strings.TrimSpace(configuredModel(profile)) {
		return nil
	}
	baseURL := strings.TrimSpace(provider.BaseURL)
	upstreamModel := strings.TrimSpace(provider.UpstreamModel)
	apiKeyEnv := strings.TrimSpace(provider.APIKeyEnv)
	if baseURL == "" || upstreamModel == "" || apiKeyEnv == "" {
		return nil
	}
	providerName := strings.TrimSpace(provider.Ref)
	if providerName == "" {
		providerName = "amux openai-compatible"
	}
	args := []string{
		"-c", `model_provider="amux"`,
		"-c", fmt.Sprintf("model=%q", upstreamModel),
		"-c", "disable_response_storage=true",
		"-c", fmt.Sprintf("model_providers.amux.name=%q", providerName),
		"-c", fmt.Sprintf("model_providers.amux.base_url=%q", baseURL),
		"-c", fmt.Sprintf("model_providers.amux.env_key=%q", apiKeyEnv),
		"-c", `model_providers.amux.wire_api="responses"`,
		"-c", `model_providers.amux.env_http_headers={"X-Session-Id"="AMUX_SESSION_ID"}`,
	}
	if strings.TrimSpace(provider.WireAPI) == "responses-via-chat-completions" {
		args = append(args, "-c", `web_search="disabled"`)
	}
	if provider.ContextWindow > 0 {
		args = append(args, "-c", fmt.Sprintf("model_context_window=%d", provider.ContextWindow))
	}
	if provider.MaxTokens > 0 {
		args = append(args, "-c", fmt.Sprintf("model_max_output_tokens=%d", provider.MaxTokens))
	}
	if effort := strings.TrimSpace(provider.Thinking["reasoning_effort"]); effort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	return args
}

func ProjectLegacyLaunch(agentType string, explicitArgs []string, opts LaunchOptions) LaunchProjection {
	agentType = strings.TrimSpace(agentType)
	args := cloneStrings(explicitArgs)
	effectiveModel := effectiveLaunchModel(agentType, "", args, opts.Model)
	if !AgentArgsContainModel(agentType, args) {
		args = append(ModelArgs(agentType, effectiveModel), args...)
	}
	return LaunchProjection{
		AgentType: agentType,
		ExtraArgs: args,
		Model:     effectiveModel,
	}
}

func effectiveLaunchModel(agentType, profileModel string, args []string, overrideModel string) string {
	if model, ok := AgentArgsModel(agentType, args); ok {
		return model
	}
	if model := strings.TrimSpace(overrideModel); model != "" {
		return model
	}
	return strings.TrimSpace(profileModel)
}

func ModelArgs(agentType, model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	switch profileConfigProtocol(agentType) {
	case "claude", "codex", "pi":
		return []string{"--model", model}
	default:
		return nil
	}
}

func AgentArgsContainModel(agentType string, args []string) bool {
	_, ok := AgentArgsModel(agentType, args)
	return ok
}

func AgentArgsModel(agentType string, args []string) (string, bool) {
	for i := len(args) - 1; i >= 0; i-- {
		arg := args[i]
		arg = strings.TrimSpace(arg)
		switch {
		case arg == "--model":
			if i+1 < len(args) {
				model := strings.TrimSpace(args[i+1])
				return model, model != ""
			}
		case strings.HasPrefix(arg, "--model="):
			model := strings.TrimSpace(strings.TrimPrefix(arg, "--model="))
			return model, model != ""
		}
	}
	return "", false
}

// ReplaceArgsModel removes any existing `--model <v>` or `--model=<v>`
// selection from args and prepends ModelArgs(agentType, model).
// An empty model only strips the existing selection.
func ReplaceArgsModel(agentType string, args []string, model string) []string {
	stripped := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--model":
			if i+1 < len(args) {
				i++ // skip the existing model value
			}
			continue
		case strings.HasPrefix(arg, "--model="):
			continue
		}
		stripped = append(stripped, args[i])
	}
	modelArgs := ModelArgs(agentType, model)
	if len(modelArgs) == 0 {
		return stripped
	}
	return append(modelArgs, stripped...)
}
