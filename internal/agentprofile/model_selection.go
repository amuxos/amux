package agentprofile

import (
	"fmt"
	"strings"
)

func ResolveModelSelection(profile Profile, requested string) (ResolvedModelSelection, error) {
	ref := strings.TrimSpace(requested)
	if ref == "" {
		ref = strings.TrimSpace(configuredModel(profile))
	}
	if ref == "" {
		return ResolvedModelSelection{Kind: ModelKindDefault}, nil
	}
	if profile.ModelPolicyEnforced && !stringSliceContains(profile.SupportedModels, ref) {
		return ResolvedModelSelection{}, fmt.Errorf("model %q is not supported by profile %s", ref, profile.Ref())
	}
	if option, ok := profile.ModelOptions[ref]; ok {
		return ResolvedModelSelection{
			Ref:         option.Ref,
			Kind:        option.Kind,
			AgentModel:  option.AgentModel,
			ProviderRef: option.ProviderRef,
			Provider:    cloneProvider(option.Provider),
		}, nil
	}
	if !profile.ModelPolicyEnforced && profile.Provider != nil && ref == strings.TrimSpace(configuredModel(profile)) {
		return ResolvedModelSelection{
			Ref:         ref,
			Kind:        ModelKindProvider,
			AgentModel:  strings.TrimSpace(profile.Provider.UpstreamModel),
			ProviderRef: strings.TrimSpace(profile.Provider.Ref),
			Provider:    cloneProvider(profile.Provider),
		}, nil
	}
	return ResolvedModelSelection{Ref: ref, Kind: ModelKindNative, AgentModel: ref}, nil
}

func ProjectProfileLaunchChecked(profile Profile, explicitArgs []string, explicitEnv []string, opts LaunchOptions) (LaunchProjection, error) {
	if strings.TrimSpace(opts.Runtime) != "" {
		return projectProfileRuntimeLaunchChecked(profile, explicitArgs, explicitEnv, opts)
	}
	requested := strings.TrimSpace(opts.Model)
	if model, ok := AgentArgsModel(profile.Type, explicitArgs); ok {
		requested = model
	}
	selection, err := ResolveModelSelection(profile, requested)
	if err != nil {
		return LaunchProjection{}, err
	}
	selected := cloneProfile(profile)
	selected.Model = selection.Ref
	selected.ConfiguredModel = selection.Ref
	selected.Provider = cloneProvider(selection.Provider)
	if err := validateProvider(selected.Type, selected.Provider); err != nil {
		return LaunchProjection{}, fmt.Errorf("model %q: %w", selection.Ref, err)
	}
	launch := ProjectProfileLaunch(selected, explicitArgs, explicitEnv, LaunchOptions{Model: selection.Ref})
	return launch, nil
}

func projectProfileRuntimeLaunchChecked(profile Profile, explicitArgs []string, explicitEnv []string, opts LaunchOptions) (LaunchProjection, error) {
	runtime, err := ResolveRuntimeOption(opts.Runtime)
	if err != nil {
		return LaunchProjection{}, err
	}
	requestedModel := strings.TrimSpace(opts.Model)
	model, err := ResolveRuntimeModel(runtime, requestedModel)
	if err != nil {
		return LaunchProjection{}, err
	}
	if err := ValidateRuntimeReasoningEffort(runtime, model, opts.ReasoningEffort); err != nil {
		return LaunchProjection{}, err
	}

	selected := cloneProfile(profile)
	selected.Type = runtime.AgentType
	selected.Model = model.Ref
	selected.ConfiguredModel = model.Ref
	selected.Provider = cloneProvider(model.Provider)
	selected.DefaultArgs, err = projectProfilePermissionArgs(profile, runtime.AgentType)
	if err != nil {
		return LaunchProjection{}, err
	}
	selected.SupportedModels = make([]string, 0, len(runtime.Models))
	selected.ModelOptions = make(map[string]ModelOption, len(runtime.Models))
	selected.ModelPolicyEnforced = true
	for _, runtimeModel := range runtime.Models {
		selected.SupportedModels = append(selected.SupportedModels, runtimeModel.Ref)
		selected.ModelOptions[runtimeModel.Ref] = ModelOption{
			Ref:         runtimeModel.Ref,
			Kind:        runtimeModel.Kind,
			AgentModel:  runtimeModel.AgentModel,
			ProviderRef: runtimeModel.ProviderRef,
			Provider:    cloneProvider(runtimeModel.Provider),
		}
	}
	selected.Placement.Requires.AgentTypes = []string{runtime.AgentType}

	effort := strings.TrimSpace(opts.ReasoningEffort)
	selected.ExecutionRuntimeRef = runtime.Ref
	selected.ReasoningEffort = effort
	if effort != "" && selected.Provider != nil {
		if selected.Provider.Thinking == nil {
			selected.Provider.Thinking = map[string]string{}
		}
		selected.Provider.Thinking["reasoning_effort"] = effort
		if _, ok := selected.Provider.Thinking["thinking_effort"]; ok {
			selected.Provider.Thinking["thinking_effort"] = effort
		}
	}
	if err := validateProvider(selected.Type, selected.Provider); err != nil {
		return LaunchProjection{}, fmt.Errorf("runtime %s model %q: %w", runtime.Ref, model.Ref, err)
	}

	// The explicit Runtime Catalog selection owns model/provider/reasoning.
	// Workspace default_args predate that selection surface and may still carry
	// legacy execution flags; retain unrelated args without allowing those flags
	// to silently replace the saved Bot launch tuple.
	explicitArgs = stripRuntimeExecutionSelectionArgs(runtime.AgentType, explicitArgs)
	launch := ProjectProfileLaunch(selected, explicitArgs, explicitEnv, LaunchOptions{Model: model.Ref})
	if effort != "" && selected.Provider == nil && profileConfigProtocol(runtime.AgentType) == "codex" {
		launch.ExtraArgs = append(launch.ExtraArgs, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	} else if effort != "" && runtime.AgentType == "claude" {
		launch.ExtraArgs = append(launch.ExtraArgs, "--effort", effort)
	}
	launch.Runtime = runtime.Ref
	launch.ReasoningEffort = effort
	return launch, nil
}

func stripRuntimeExecutionSelectionArgs(agentType string, args []string) []string {
	agentType = strings.TrimSpace(agentType)
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--model" || (agentType == "claude" && arg == "--effort"):
			if i+1 < len(args) {
				i++
			}
			continue
		case strings.HasPrefix(arg, "--model=") ||
			(agentType == "claude" && strings.HasPrefix(arg, "--effort=")):
			continue
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) && runtimeConfigOwnsExecutionSelection(agentType, args[i+1]) {
				i++
				continue
			}
		case strings.HasPrefix(arg, "-c="):
			if runtimeConfigOwnsExecutionSelection(agentType, strings.TrimPrefix(arg, "-c=")) {
				continue
			}
		case strings.HasPrefix(arg, "--config="):
			if runtimeConfigOwnsExecutionSelection(agentType, strings.TrimPrefix(arg, "--config=")) {
				continue
			}
		}
		out = append(out, args[i])
	}
	return out
}

func runtimeConfigOwnsExecutionSelection(agentType, value string) bool {
	value = strings.TrimSpace(value)
	if profileConfigProtocol(agentType) != "codex" {
		return false
	}
	for _, key := range []string{"model", "model_provider", "model_reasoning_effort", "plan_mode_reasoning_effort"} {
		if _, ok := codexConfigValue(value, key); ok {
			return true
		}
	}
	return false
}

func projectProfilePermissionArgs(profile Profile, runtimeAgentType string) ([]string, error) {
	if len(profile.DefaultArgs) == 0 {
		return nil, nil
	}
	for _, mode := range []string{"default", "plan", "bypass"} {
		sourceArgs, err := permissionModeArgs(profile.Type, packProfilePermission{PermissionMode: mode})
		if err != nil {
			return nil, err
		}
		if stringSlicesEqual(profile.DefaultArgs, sourceArgs) {
			return permissionModeArgs(runtimeAgentType, packProfilePermission{PermissionMode: mode})
		}
	}
	if strings.TrimSpace(profile.Type) == strings.TrimSpace(runtimeAgentType) {
		return cloneStrings(profile.DefaultArgs), nil
	}
	// Unknown default args belong to the Profile's legacy execution hint. They
	// cannot constrain portable content or leak into a different adapter.
	return nil, nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
