package agentprofile

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/config"
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
			Ref:            option.Ref,
			Kind:           option.Kind,
			AgentModel:     option.AgentModel,
			ProviderRef:    option.ProviderRef,
			Provider:       cloneProvider(option.Provider),
			BackendVariant: option.BackendVariant,
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
	if strings.TrimSpace(profile.Type) == "" {
		return LaunchProjection{}, profileError("runtime_required", profile.Ref())
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
	effort := strings.TrimSpace(opts.ReasoningEffort)
	if effort != "" {
		if err := validateProfileReasoningEffort(selected, effort); err != nil {
			return LaunchProjection{}, err
		}
		applyProfileReasoningEffort(&selected, effort)
	}
	if err := validateProvider(selected.Type, selected.Provider); err != nil {
		return LaunchProjection{}, fmt.Errorf("model %q: %w", selection.Ref, err)
	}
	launch := ProjectProfileLaunch(selected, explicitArgs, explicitEnv, LaunchOptions{Model: variantBaseModel(selection.Ref, selection.AgentModel, selection.BackendVariant)})
	applyLaunchReasoningEffort(&launch, selected, effort)
	applyLaunchBackendVariant(&launch, selection.AgentModel, selection.BackendVariant)
	applyDeclaredDefaultEffort(&launch, selected, profileModelCapability(selected), effort)
	return launch, nil
}

// profileModelCapability resolves the reasoning capability of the Profile's
// effective model. Providers carry their default in the runtime binding; other
// models report what their runtime catalog declares.
func profileModelCapability(profile Profile) RuntimeModelOption {
	if profile.Provider != nil {
		return RuntimeModelOption{Ref: configuredModel(profile), DefaultReasoningEffort: providerReasoningEffort(profile.Provider)}
	}
	runtime, err := ResolveRuntimeOption(profile.Type)
	if err != nil {
		return RuntimeModelOption{Ref: configuredModel(profile)}
	}
	return ProfileNativeRuntimeModel(runtime, configuredModel(profile))
}

// applyDeclaredDefaultEffort applies the model's declared default effort when
// the caller selected none, so a model's default is what actually runs rather
// than an implicit choice made by the runtime. An explicit selection always
// wins, and a model that declares no default keeps the runtime's own behavior.
//
// A provider-backed model is normally skipped: its effort travels inside the
// provider binding (the runtime thinking settings, applied by the distribution
// provider projections), so emitting a launch flag here would duplicate it.
// pi is the exception — its provider projection consumes the binding only as a
// reasoning on/off switch and drops the level itself, so the level has to
// travel on the launch flag.
//
// A model that declares no selectable effort is also skipped. Its declaration
// says the runtime accepts no effort for it, so sending the model's default
// anyway would push a value the runtime never agreed to honor.
func applyDeclaredDefaultEffort(launch *LaunchProjection, profile Profile, model RuntimeModelOption, requested string) {
	if strings.TrimSpace(requested) != "" {
		return
	}
	if profile.Provider != nil && profile.Type != "pi" {
		return
	}
	defaultEffort := strings.TrimSpace(model.DefaultReasoningEffort)
	if defaultEffort == "" {
		return
	}
	applyLaunchReasoningEffort(launch, profile, defaultEffort)
}

func validateProfileReasoningEffort(profile Profile, effort string) error {
	runtime, err := ResolveRuntimeOption(profile.Type)
	if err != nil {
		return err
	}
	// The Profile already resolved and authorized its model/provider. Consult
	// only the catalog's reasoning capabilities; its model policy and bindings
	// must not replace the Profile's selection.
	model := ProfileNativeRuntimeModel(runtime, configuredModel(profile))
	if profile.Provider != nil {
		model.NativeCapabilitiesUnknown = false
	}
	return ValidateRuntimeReasoningEffort(runtime, model, effort)
}

// ProfileNativeRuntimeModel returns reasoning capabilities for a model already
// authorized by a Profile, including native models absent from the distribution
// runtime catalog.
func ProfileNativeRuntimeModel(runtime RuntimeOption, modelRef string) RuntimeModelOption {
	model := RuntimeModelOption{Ref: strings.TrimSpace(modelRef), SupportedReasoningEfforts: cloneStrings(runtime.DefaultReasoningEfforts), NativeCapabilitiesUnknown: config.SupportsAgentExecutable(runtime.Ref)}
	for _, candidate := range runtime.Models {
		if candidate.Ref == model.Ref {
			return cloneRuntimeModelOption(candidate)
		}
	}
	if runtimeModelUsesBinaryThinking(model.Ref) {
		model.SupportedReasoningEfforts = nil
	}
	return model
}

func projectProfileRuntimeLaunchChecked(profile Profile, explicitArgs []string, explicitEnv []string, opts LaunchOptions) (LaunchProjection, error) {
	selected, model, err := resolveProfileRuntimeSelection(profile, opts)
	if err != nil {
		return LaunchProjection{}, err
	}

	// The explicit Runtime Catalog selection owns model/provider/reasoning.
	// Workspace default_args predate that selection surface and may still carry
	// legacy execution flags; retain unrelated args without allowing those flags
	// to silently replace the saved Bot launch tuple.
	explicitArgs = stripRuntimeExecutionSelectionArgs(selected.Type, explicitArgs)
	launch := ProjectProfileLaunch(selected, explicitArgs, explicitEnv, LaunchOptions{Model: variantBaseModel(model.Ref, model.AgentModel, model.BackendVariant)})
	applyLaunchReasoningEffort(&launch, selected, selected.ReasoningEffort)
	launch.Runtime = selected.ExecutionRuntimeRef
	applyLaunchBackendVariant(&launch, model.AgentModel, model.BackendVariant)
	applyDeclaredDefaultEffort(&launch, selected, model, selected.ReasoningEffort)
	return launch, nil
}

// resolveProfileRuntimeSelection shares the runtime model policy and snapshot
// construction between initial launches and later model-only switches.
func resolveProfileRuntimeSelection(profile Profile, opts LaunchOptions) (Profile, RuntimeModelOption, error) {
	runtime, err := ResolveRuntimeOption(opts.Runtime)
	if err != nil {
		return Profile{}, RuntimeModelOption{}, err
	}
	requestedModel := strings.TrimSpace(opts.Model)
	model, err := ResolveRuntimeModel(runtime, requestedModel)
	if err != nil {
		return Profile{}, RuntimeModelOption{}, err
	}
	// A dynamically resolved model belongs in the immutable launch snapshot too.
	// Otherwise later materialization would reject a selection accepted here.
	if model.Ref != "" {
		if _, found := findRuntimeModelByRef(runtime.Models, model.Ref); !found {
			runtime.Models = append(runtime.Models, model)
		}
	}
	if err := ValidateRuntimeReasoningEffort(runtime, model, opts.ReasoningEffort); err != nil {
		return Profile{}, RuntimeModelOption{}, err
	}

	selected := cloneProfile(profile)
	selected.Type = runtime.AgentType
	selected.Model = model.Ref
	selected.ConfiguredModel = model.Ref
	selected.Provider = cloneProvider(model.Provider)
	selected.DefaultArgs, err = projectProfilePermissionArgs(profile, runtime.AgentType)
	if err != nil {
		return Profile{}, RuntimeModelOption{}, err
	}
	selected.SupportedModels = make([]string, 0, len(runtime.Models))
	selected.ModelOptions = make(map[string]ModelOption, len(runtime.Models))
	selected.ModelPolicyEnforced = true
	for _, runtimeModel := range runtime.Models {
		selected.SupportedModels = append(selected.SupportedModels, runtimeModel.Ref)
		selected.ModelOptions[runtimeModel.Ref] = ModelOption{
			Ref:            runtimeModel.Ref,
			Kind:           runtimeModel.Kind,
			AgentModel:     runtimeModel.AgentModel,
			ProviderRef:    runtimeModel.ProviderRef,
			Provider:       cloneProvider(runtimeModel.Provider),
			BackendVariant: runtimeModel.BackendVariant,
		}
	}
	selected.Placement.Requires.AgentTypes = []string{runtime.AgentType}

	effort := strings.TrimSpace(opts.ReasoningEffort)
	selected.ExecutionRuntimeRef = runtime.Ref
	applyProfileReasoningEffort(&selected, effort)
	if err := validateProvider(selected.Type, selected.Provider); err != nil {
		return Profile{}, RuntimeModelOption{}, fmt.Errorf("runtime %s model %q: %w", runtime.Ref, model.Ref, err)
	}
	return selected, model, nil
}

func applyProfileReasoningEffort(profile *Profile, effort string) {
	profile.ReasoningEffort = effort
	if effort == "" || profile.Provider == nil {
		return
	}
	if profile.Provider.Thinking == nil {
		profile.Provider.Thinking = map[string]string{}
	}
	profile.Provider.Thinking["reasoning_effort"] = effort
	if _, ok := profile.Provider.Thinking["thinking_effort"]; ok {
		profile.Provider.Thinking["thinking_effort"] = effort
	}
}

func applyLaunchReasoningEffort(launch *LaunchProjection, profile Profile, effort string) {
	if effort != "" && profile.Provider == nil && profileConfigProtocol(profile.Type) == "codex" {
		launch.ExtraArgs = append(launch.ExtraArgs, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	} else if effort != "" && profile.Type == "claude" {
		launch.ExtraArgs = append(launch.ExtraArgs, "--effort", effort)
	} else if effort != "" && profile.Type == "pi" {
		// pi takes the level as a launch flag and maps it to the provider's own
		// vocabulary through the projected model's thinkingLevelMap.
		launch.ExtraArgs = append(launch.ExtraArgs, "--thinking", piThinkingLevel(effort))
	}
	launch.ReasoningEffort = effort
}

// piThinkingLevel translates an effort into pi's thinking-level vocabulary.
// pi names the disabled level "off"; every shared value is already identical.
func piThinkingLevel(effort string) string {
	if strings.TrimSpace(effort) == "none" {
		return "off"
	}
	return strings.TrimSpace(effort)
}

// variantBaseModel returns the model name the runtime should be asked to run.
// A variant ref names a backend choice rather than a model the runtime knows,
// so a variant selection resolves to its underlying model; anything else keeps
// the ref it was given.
func variantBaseModel(ref, agentModel, variant string) string {
	if strings.TrimSpace(variant) == "" {
		return ref
	}
	if agentModel = strings.TrimSpace(agentModel); agentModel != "" {
		return agentModel
	}
	return ref
}

// applyLaunchBackendVariant appends the runtime's backend variant selection for
// a model that resolved to a non-default backend. Both the variant and the
// model it applies to are emitted as process-global `-c` overrides: the model
// name is a user-facing variant ref that the runtime does not recognize, so the
// launch must also name the underlying model for the variant to attach to.
func applyLaunchBackendVariant(launch *LaunchProjection, agentModel, variant string) {
	variant = strings.TrimSpace(variant)
	if variant == "" {
		return
	}
	if agentModel = strings.TrimSpace(agentModel); agentModel != "" {
		launch.ExtraArgs = append(launch.ExtraArgs, "-c", fmt.Sprintf("model=%q", agentModel))
	}
	launch.ExtraArgs = append(launch.ExtraArgs, "-c", fmt.Sprintf("model_backend_variant=%q", variant))
	launch.BackendVariant = variant
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
	for _, key := range []string{"model", "model_provider", "model_reasoning_effort", "plan_mode_reasoning_effort", "model_backend_variant"} {
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
		sourceArgs, err := permissionModeArgs(profile.Type, mode)
		if err != nil {
			return nil, err
		}
		if stringSlicesEqual(profile.DefaultArgs, sourceArgs) {
			return permissionModeArgs(runtimeAgentType, mode)
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
