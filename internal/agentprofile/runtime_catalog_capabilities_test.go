package agentprofile

import (
	"reflect"
	"testing"
)

// declaredSource is a distribution runtime that declares per-model capabilities
// exactly as the internal distribution source does.
func declaredSource() runtimeCatalogSource {
	return runtimeCatalogSource{
		ref:         "codex",
		displayName: "Codex",
		models:      []string{"GPT-5.6-Sol", "Seed-Evolving", "vendor/model-a"},
		capabilities: map[string]RuntimeModelCapability{
			"GPT-5.6-Sol":    {SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "ultra"}, DefaultReasoningEffort: "medium"},
			"Seed-Evolving":  {DefaultReasoningEffort: "high"},
			"vendor/model-a": {SupportedReasoningEfforts: []string{"low", "high"}, DefaultReasoningEffort: "high"},
		},
		variants: []RuntimeModelVariant{
			{Ref: "GPT-5.6-Sol-Max", AgentModel: "GPT-5.6-Sol", BackendVariant: "gpt-5.6-sol__max"},
		},
	}
}

func catalogModel(t *testing.T, options []RuntimeOption, runtimeRef, modelRef string) RuntimeModelOption {
	t.Helper()
	for _, option := range options {
		if option.Ref != runtimeRef {
			continue
		}
		for _, model := range option.Models {
			if model.Ref == modelRef {
				return model
			}
		}
	}
	t.Fatalf("runtime %s model %s not in catalog", runtimeRef, modelRef)
	return RuntimeModelOption{}
}

func TestDeclaredCapabilitiesReplaceNameHeuristics(t *testing.T) {
	options, err := buildRuntimeCatalog([]runtimeCatalogSource{declaredSource()}, packManifest{})
	if err != nil {
		t.Fatal(err)
	}

	// A declared model keeps its exact enum. The name heuristic would have
	// produced the standard codex list, which lacks "ultra".
	sol := catalogModel(t, options, "codex", "GPT-5.6-Sol")
	if want := []string{"low", "medium", "high", "xhigh", "ultra"}; !reflect.DeepEqual(sol.SupportedReasoningEfforts, want) {
		t.Fatalf("GPT-5.6-Sol efforts = %v, want %v", sol.SupportedReasoningEfforts, want)
	}
	if sol.DefaultReasoningEffort != "medium" {
		t.Fatalf("GPT-5.6-Sol default = %q, want medium", sol.DefaultReasoningEffort)
	}

	// A declared model with no efforts accepts none. The heuristic would have
	// offered the full standard list because the ref has no matching substring.
	seed := catalogModel(t, options, "codex", "Seed-Evolving")
	if len(seed.SupportedReasoningEfforts) != 0 {
		t.Fatalf("Seed-Evolving efforts = %v, want none", seed.SupportedReasoningEfforts)
	}
	if seed.DefaultReasoningEffort != "high" {
		t.Fatalf("Seed-Evolving default = %q, want high", seed.DefaultReasoningEffort)
	}

	// A declaration also overrides the name heuristics: this ref has no
	// recognised substring yet declares a narrower enum than the defaults.
	glm := catalogModel(t, options, "codex", "vendor/model-a")
	if want := []string{"low", "high"}; !reflect.DeepEqual(glm.SupportedReasoningEfforts, want) {
		t.Fatalf("vendor/model-a efforts = %v, want %v", glm.SupportedReasoningEfforts, want)
	}
}

func TestUndeclaredModelsKeepProtocolDefaults(t *testing.T) {
	// "codex" is the registered Codex-compatible runtime in the public module,
	// so an undeclared model there keeps the shared codex defaults.
	source := runtimeCatalogSource{ref: "codex", displayName: "Codex", models: []string{"gpt-5.4"}}
	options, err := buildRuntimeCatalog([]runtimeCatalogSource{source}, packManifest{})
	if err != nil {
		t.Fatal(err)
	}
	model := catalogModel(t, options, "codex", "gpt-5.4")
	if want := standardReasoningEfforts; !reflect.DeepEqual(model.SupportedReasoningEfforts, want) {
		t.Fatalf("undeclared model efforts = %v, want %v", model.SupportedReasoningEfforts, want)
	}
}

func TestVariantInheritsBaseModelCapabilities(t *testing.T) {
	options, err := buildRuntimeCatalog([]runtimeCatalogSource{declaredSource()}, packManifest{})
	if err != nil {
		t.Fatal(err)
	}
	variant := catalogModel(t, options, "codex", "GPT-5.6-Sol-Max")
	if variant.AgentModel != "GPT-5.6-Sol" {
		t.Fatalf("variant AgentModel = %q, want GPT-5.6-Sol", variant.AgentModel)
	}
	if variant.BackendVariant != "gpt-5.6-sol__max" {
		t.Fatalf("variant BackendVariant = %q", variant.BackendVariant)
	}
	if !reflect.DeepEqual(variant.SupportedReasoningEfforts, []string{"low", "medium", "high", "xhigh", "ultra"}) {
		t.Fatalf("variant efforts = %v, want the base model's", variant.SupportedReasoningEfforts)
	}
	if variant.DefaultReasoningEffort != "medium" {
		t.Fatalf("variant default = %q, want medium", variant.DefaultReasoningEffort)
	}
}

func TestRuntimeModelVariantSurvivesClone(t *testing.T) {
	model := RuntimeModelOption{
		Ref: "GPT-5.6-Sol-Max", AgentModel: "GPT-5.6-Sol",
		BackendVariant:            "gpt-5.6-sol__max",
		SupportedReasoningEfforts: []string{"high"},
	}
	cloned := cloneRuntimeModelOption(model)
	if cloned.BackendVariant != model.BackendVariant || cloned.AgentModel != model.AgentModel {
		t.Fatalf("clone lost variant identity: %+v", cloned)
	}
	cloned.SupportedReasoningEfforts[0] = "low"
	if model.SupportedReasoningEfforts[0] != "high" {
		t.Fatal("clone shares the effort slice with the original")
	}
}

func TestRuntimeLaunchProjectsVariantWithItsUnderlyingModel(t *testing.T) {
	source := runtimeCatalogSource{
		ref:    "codex",
		models: []string{"Base-Model"},
		capabilities: map[string]RuntimeModelCapability{
			"Base-Model": {SupportedReasoningEfforts: []string{"low", "high"}, DefaultReasoningEffort: "high"},
		},
		variants: []RuntimeModelVariant{
			{Ref: "Base-Model-Max", AgentModel: "Base-Model", BackendVariant: "base-model__max"},
		},
	}
	previous := runtimeCatalogSources
	runtimeCatalogSources = []runtimeCatalogSource{source}
	t.Cleanup(func() { runtimeCatalogSources = previous })

	profile := Profile{
		Namespace: "common", Name: "variant", Type: "codex",
		Model: "Base-Model-Max", ConfiguredModel: "Base-Model-Max",
	}
	launch, err := projectProfileRuntimeLaunchChecked(profile, nil, nil, LaunchOptions{
		Runtime: "codex", Model: "Base-Model-Max", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if launch.BackendVariant != "base-model__max" {
		t.Fatalf("variant = %q", launch.BackendVariant)
	}
	if !argsContainConfigKey(launch.ExtraArgs, "model_backend_variant", "base-model__max") {
		t.Fatalf("variant flag missing from args: %v", launch.ExtraArgs)
	}
	// The variant ref is not a model the runtime knows, so the launch must also
	// name the underlying model on every surface that carries a model name.
	if launch.Model != "Base-Model" {
		t.Fatalf("launch model = %q, want the underlying model", launch.Model)
	}
	if !argsContainConfigKey(launch.ExtraArgs, "model", "Base-Model") {
		t.Fatalf("underlying model missing from args: %v", launch.ExtraArgs)
	}

	// A variant reports its base model's reasoning surface, so an effort the
	// base model does not accept is rejected rather than silently degraded.
	if _, err := projectProfileRuntimeLaunchChecked(profile, nil, nil, LaunchOptions{
		Runtime: "codex", Model: "Base-Model-Max", ReasoningEffort: "xhigh",
	}); err == nil {
		t.Fatal("variant accepted an effort its base model does not support")
	}

	// The plain model stays free of variant flags.
	plain, err := projectProfileRuntimeLaunchChecked(profile, nil, nil, LaunchOptions{
		Runtime: "codex", Model: "Base-Model", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.BackendVariant != "" || argsContainConfigKey(plain.ExtraArgs, "model_backend_variant", "") {
		t.Fatalf("plain model emitted a variant: %v", plain.ExtraArgs)
	}
}

func TestSelectionCarriesBackendVariant(t *testing.T) {
	profile := Profile{
		Namespace: "common", Name: "variant", Type: "codex",
		Model: "GPT-5.6-Sol-Max", ConfiguredModel: "GPT-5.6-Sol-Max",
		SupportedModels:     []string{"GPT-5.6-Sol-Max", "GPT-5.6-Sol"},
		ModelPolicyEnforced: true,
		ModelOptions: map[string]ModelOption{
			"GPT-5.6-Sol-Max": {
				Ref: "GPT-5.6-Sol-Max", Kind: ModelKindNative,
				AgentModel: "GPT-5.6-Sol", BackendVariant: "gpt-5.6-sol__max",
			},
			"GPT-5.6-Sol": {Ref: "GPT-5.6-Sol", Kind: ModelKindNative, AgentModel: "GPT-5.6-Sol"},
		},
	}
	launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Model: "GPT-5.6-Sol-Max"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.BackendVariant != "gpt-5.6-sol__max" {
		t.Fatalf("launch BackendVariant = %q, want gpt-5.6-sol__max", launch.BackendVariant)
	}
	if !argsContainConfigKey(launch.ExtraArgs, "model_backend_variant", "gpt-5.6-sol__max") {
		t.Fatalf("variant was not projected into args: %v", launch.ExtraArgs)
	}
	// Switching back to the standard model must clear the variant, or a stale
	// backend selection would leak into the new launch.
	switched, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Model: "GPT-5.6-Sol"})
	if err != nil {
		t.Fatal(err)
	}
	if switched.BackendVariant != "" {
		t.Fatalf("standard model kept a variant: %q", switched.BackendVariant)
	}
	if argsContainConfigKey(switched.ExtraArgs, "model_backend_variant", "") {
		t.Fatalf("standard model emitted a variant flag: %v", switched.ExtraArgs)
	}
}

// argsContainConfigKey reports whether args set the given `-c key="value"` key.
// An empty want matches any value for the key.
func argsContainConfigKey(args []string, key, want string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-c" && args[i] != "--config" {
			continue
		}
		value, ok := codexConfigValue(args[i+1], key)
		if ok && (want == "" || value == want) {
			return true
		}
	}
	return false
}

func TestModelSwitchClearsStaleBackendVariantArg(t *testing.T) {
	profile := Profile{
		Namespace: "common", Name: "variant", Type: "codex",
		Model: "GPT-5.6-Sol", ConfiguredModel: "GPT-5.6-Sol",
		SupportedModels:     []string{"GPT-5.6-Sol"},
		ModelPolicyEnforced: true,
		ModelOptions: map[string]ModelOption{
			"GPT-5.6-Sol": {Ref: "GPT-5.6-Sol", Kind: ModelKindNative, AgentModel: "GPT-5.6-Sol"},
		},
	}
	stored := []string{"-c", `model_backend_variant="gpt-5.6-sol__max"`, "-c", `model="GPT-5.6-Sol"`}
	projection, err := ProjectModelSwitch(profile, stored, "GPT-5.6-Sol")
	if err != nil {
		t.Fatal(err)
	}
	if argsContainConfigKey(projection.ExtraArgs, "model_backend_variant", "") {
		t.Fatalf("stale backend variant survived the switch: %v", projection.ExtraArgs)
	}

	// Switching *to* a variant model projects the new variant.
	profile.ModelOptions["GPT-5.6-Sol-Max"] = ModelOption{
		Ref: "GPT-5.6-Sol-Max", Kind: ModelKindNative,
		AgentModel: "GPT-5.6-Sol", BackendVariant: "gpt-5.6-sol__max",
	}
	profile.SupportedModels = append(profile.SupportedModels, "GPT-5.6-Sol-Max")
	toVariant, err := ProjectModelSwitch(profile, stored, "GPT-5.6-Sol-Max")
	if err != nil {
		t.Fatal(err)
	}
	if !argsContainConfigKey(toVariant.ExtraArgs, "model_backend_variant", "gpt-5.6-sol__max") {
		t.Fatalf("switch to a variant model did not project the variant: %v", toVariant.ExtraArgs)
	}
}

func TestRuntimeCatalogRejectsInconsistentDeclarations(t *testing.T) {
	cases := []struct {
		name   string
		source RuntimeCatalogSource
	}{
		{
			name: "default effort outside the supported enum",
			source: RuntimeCatalogSource{
				Ref: "codex", Models: []string{"m"},
				Capabilities: map[string]RuntimeModelCapability{
					"m": {SupportedReasoningEfforts: []string{"low"}, DefaultReasoningEffort: "high"},
				},
			},
		},
		{
			name: "variant refers to an unknown model",
			source: RuntimeCatalogSource{
				Ref: "codex", Models: []string{"m"},
				Variants: []RuntimeModelVariant{{Ref: "m-max", AgentModel: "nope", BackendVariant: "nope__max"}},
			},
		},
		{
			name: "empty effort in a declaration",
			source: RuntimeCatalogSource{
				Ref: "codex", Models: []string{"m"},
				Capabilities: map[string]RuntimeModelCapability{"m": {SupportedReasoningEfforts: []string{" "}}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateRuntimeCatalogSources([]RuntimeCatalogSource{tc.source}); err == nil {
				t.Fatal("inconsistent declaration was accepted")
			}
		})
	}
}

func TestRuntimeCatalogAcceptsConsistentDeclarations(t *testing.T) {
	source := RuntimeCatalogSource{
		Ref: "codex", Models: []string{"m"},
		Capabilities: map[string]RuntimeModelCapability{
			"m": {SupportedReasoningEfforts: []string{"low", "high"}, DefaultReasoningEffort: "high"},
		},
		Variants: []RuntimeModelVariant{{Ref: "m-max", AgentModel: "m", BackendVariant: "m__max"}},
	}
	if err := validateRuntimeCatalogSources([]RuntimeCatalogSource{source}); err != nil {
		t.Fatalf("consistent declaration rejected: %v", err)
	}
}

// The declared default is what a launch actually uses when the caller selected
// no effort, so a model's own default reaches the runtime instead of an
// implicit choice.
func TestDeclaredDefaultEffortIsApplied(t *testing.T) {
	model := RuntimeModelOption{
		Ref:                       "Base-Model",
		SupportedReasoningEfforts: []string{"low", "high"},
		DefaultReasoningEffort:    "high",
	}
	native := Profile{Type: "codex"}

	applied := LaunchProjection{}
	applyDeclaredDefaultEffort(&applied, native, model, "")
	if !argsContainConfigKey(applied.ExtraArgs, "model_reasoning_effort", "high") {
		t.Fatalf("declared default was not applied: %v", applied.ExtraArgs)
	}

	// An explicit selection always wins over the declared default.
	explicit := LaunchProjection{}
	applyDeclaredDefaultEffort(&explicit, native, model, "low")
	if len(explicit.ExtraArgs) != 0 {
		t.Fatalf("default overrode an explicit selection: %v", explicit.ExtraArgs)
	}

	// A provider-backed model carries its effort inside the provider binding,
	// so it must not also gain a launch-level flag.
	provider := Profile{Type: "codex", Provider: &Provider{Ref: "vendor"}}
	withProvider := LaunchProjection{}
	applyDeclaredDefaultEffort(&withProvider, provider, model, "")
	if len(withProvider.ExtraArgs) != 0 {
		t.Fatalf("provider-backed model gained a flag: %v", withProvider.ExtraArgs)
	}

	// A model that declares no default keeps the runtime's own behavior.
	silent := LaunchProjection{}
	applyDeclaredDefaultEffort(&silent, native, RuntimeModelOption{Ref: "m"}, "")
	if len(silent.ExtraArgs) != 0 {
		t.Fatalf("model without a default emitted a flag: %v", silent.ExtraArgs)
	}
}

// pi takes the level as a launch flag rather than a config value, and spells
// the disabled level "off" while every amux runtime calls it "none". Sending
// pi the untranslated value makes it fall back to its own default instead of
// disabling thinking, so the translation is load-bearing.
func TestPiThinkingLevelFlag(t *testing.T) {
	profile := Profile{Type: "pi", Model: "vendor/model"}
	for effort, want := range map[string]string{
		"none":    "off",
		"minimal": "minimal",
		"xhigh":   "xhigh",
		"max":     "max",
	} {
		launch := LaunchProjection{}
		applyLaunchReasoningEffort(&launch, profile, effort)
		wantArgs := []string{"--thinking", want}
		if len(launch.ExtraArgs) != 2 || launch.ExtraArgs[0] != wantArgs[0] || launch.ExtraArgs[1] != wantArgs[1] {
			t.Errorf("effort %q: args = %v, want %v", effort, launch.ExtraArgs, wantArgs)
		}
	}

	// No effort selected and nothing declared: no flag at all.
	silent := LaunchProjection{}
	applyLaunchReasoningEffort(&silent, profile, "")
	if len(silent.ExtraArgs) != 0 {
		t.Errorf("empty effort emitted a flag: %v", silent.ExtraArgs)
	}
}
