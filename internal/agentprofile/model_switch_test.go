package agentprofile

import (
	"reflect"
	"strings"
	"testing"
)

func TestModelSwitchExplicitRuntimeProviderRoundTrip(t *testing.T) {
	previousResolver := customization.ResolveServiceModel
	customization.ResolveServiceModel = func(runtime, ref string) (RuntimeModelOption, bool, error) {
		if runtime != "codex" || (ref != "service/first" && ref != "service/second") {
			return RuntimeModelOption{}, false, nil
		}
		return RuntimeModelOption{
			Ref: ref, Kind: ModelKindProvider, AgentModel: "upstream-" + modelRefSuffix(ref), ProviderRef: "service",
			SupportedReasoningEfforts: []string{"high"},
			Provider: &Provider{Ref: "service", Platform: "openai-compat", WireAPI: "responses",
				BaseURL: "https://" + modelRefSuffix(ref) + ".example/v1", APIKeyEnv: "MODEL_API_KEY",
				UpstreamModel: "upstream-" + modelRefSuffix(ref), Thinking: map[string]string{"reasoning_effort": "low"}},
		}, true, nil
	}
	t.Cleanup(func() { customization.ResolveServiceModel = previousResolver })
	profile := Profile{
		Namespace: "team", Name: "expert", Revision: "rev1", Type: "codex", Source: SourcePack,
		Model: "profile-only", ConfiguredModel: "profile-only",
		SupportedModels: []string{"profile-only"}, ModelPolicyEnforced: true,
		AgentsContent: "Expert instructions", Skills: []string{"expert-skill"},
		DefaultArgs: []string{"-c", `approval_policy="never"`},
	}
	launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: "codex", Model: "service/first", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	args, spec := launch.ExtraArgs, launch.ProfileSpec
	for _, target := range []string{"service/second", "gpt-5.5", "service/first"} {
		frozen, err := FromPBProfileSpec(spec)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := ProjectModelSwitch(frozen, args, target)
		if err != nil {
			t.Fatalf("switch to %s: %v", target, err)
		}
		got := projection.ProfileSpec
		if got.ModelRef != target || got.RuntimeRef != "codex" || got.ReasoningEffort != "high" || got.AgentsContent != profile.AgentsContent || !reflect.DeepEqual(got.Skills, profile.Skills) || got.PinnedRef != profile.PinnedRef() {
			t.Fatalf("switch to %s lost execution or profile: %+v", target, got)
		}
		joined := strings.Join(projection.ExtraArgs, " ")
		if strings.Count(joined, `approval_policy="never"`) != 1 || !strings.Contains(joined, `model_reasoning_effort="high"`) {
			t.Fatalf("switch to %s changed unrelated args or reasoning: %v", target, projection.ExtraArgs)
		}
		if target == "gpt-5.5" {
			if got.Provider != nil || strings.Contains(joined, "model_provider=") || strings.Contains(joined, ".example") || projection.Selection.Kind != ModelKindNative {
				t.Fatalf("native switch retained provider settings: %v", projection.ExtraArgs)
			}
		} else {
			upstream := "upstream-" + modelRefSuffix(target)
			if got.Provider == nil || got.Provider.UpstreamModel != upstream || got.Provider.Thinking["reasoning_effort"] != "high" || projection.Selection.Kind != ModelKindProvider {
				t.Fatalf("provider switch lost binding: %+v", got)
			}
			if model, _ := AgentArgsModel("codex", projection.ExtraArgs); model != upstream {
				t.Fatalf("agent model = %s, want %s", model, upstream)
			}
			if target == "service/second" && strings.Contains(joined, "first.example") {
				t.Fatalf("previous provider leaked: %s", joined)
			}
		}
		args, spec = projection.ExtraArgs, projection.ProfileSpec
	}
	if !reflect.DeepEqual(profile.SupportedModels, []string{"profile-only"}) || profile.Model != "profile-only" {
		t.Fatal("source profile was mutated")
	}
}

func TestModelSwitchExplicitRuntimeBackendVariant(t *testing.T) {
	previous := runtimeCatalogSources
	runtimeCatalogSources = []runtimeCatalogSource{{ref: "codex", models: []string{"base-model"},
		capabilities: map[string]RuntimeModelCapability{"base-model": {SupportedReasoningEfforts: []string{"high"}}},
		variants:     []RuntimeModelVariant{{Ref: "base-model-fast", AgentModel: "base-model", BackendVariant: "fast"}},
	}}
	t.Cleanup(func() { runtimeCatalogSources = previous })
	profile := Profile{Namespace: "team", Name: "expert", Type: "codex", ExecutionRuntimeRef: "codex", ReasoningEffort: "high"}
	projection, err := ProjectModelSwitch(profile, []string{"--model", "old-model"}, "base-model-fast")
	if err != nil {
		t.Fatal(err)
	}
	if projection.ProfileSpec.ModelRef != "base-model-fast" || projection.BackendVariant != "fast" || !argsContainConfigKey(projection.ExtraArgs, "model", "base-model") || !argsContainConfigKey(projection.ExtraArgs, "model_backend_variant", "fast") {
		t.Fatalf("variant projection = %+v", projection)
	}
	if model, _ := AgentArgsModel("codex", projection.ExtraArgs); model != "base-model" {
		t.Fatalf("agent model = %q, want base-model", model)
	}
	projection, err = ProjectModelSwitch(profile, projection.ExtraArgs, "base-model")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(projection.ExtraArgs, " "), "model_backend_variant") {
		t.Fatal("old variant survived native model switch")
	}
}
