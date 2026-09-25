package agentprofile

import (
	"testing"

	"github.com/amuxos/amux/internal/task"
)

func TestPublicRuntimePassesExplicitNativeModelWithoutProviderBinding(t *testing.T) {
	for _, runtime := range []string{"codex", "claude", "pi"} {
		t.Run(runtime, func(t *testing.T) {
			spec := task.Spec{Agent: "builtin/claude", Runtime: runtime, Model: "native-provider/custom-model", Task: "review"}
			if err := ResolveTaskSpec(&spec, ""); err != nil {
				t.Fatal(err)
			}
			profile := spec.ResolvedProfile
			if spec.Agent != runtime || profile == nil || profile.RuntimeRef != runtime || profile.ModelKind != string(ModelKindNative) || profile.Model != spec.Model {
				t.Fatalf("resolved native model = %+v", spec)
			}
			if profile.Provider != nil || profile.ProviderRef != "" || len(profile.CredentialEnvSuggestions) != 0 {
				t.Fatalf("native model inferred provider credentials: %+v", profile)
			}
		})
	}
}

func TestPublicNativeRuntimeModelsKeepReasoningValidation(t *testing.T) {
	profile, ok := ResolveBuiltin("builtin/codex")
	if !ok {
		t.Fatal("missing builtin/codex")
	}
	if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: "codex", Model: "custom-model", ReasoningEffort: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: "claude", Model: "custom-model", ReasoningEffort: "invalid"}); err == nil {
		t.Fatal("unsupported reasoning effort accepted")
	}
	if _, err := ResolveRuntimeModel(RuntimeOption{Ref: "unknown"}, "custom-model"); err == nil {
		t.Fatal("unknown runtime accepted a native model")
	}
}

func TestNativeCodexSelectionSurvivesDistributionAllowlistAndLaunchRoundTrip(t *testing.T) {
	previous := allowUnlistedNativeRuntimeModels
	allowUnlistedNativeRuntimeModels = false
	t.Cleanup(func() { allowUnlistedNativeRuntimeModels = previous })
	profile, _ := ResolveBuiltin("builtin/claude")
	launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: "codex", Model: "future-model", ReasoningEffort: "future-effort"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Model != "future-model" || launch.ReasoningEffort != "future-effort" || launch.ProfileSpec.Provider != nil {
		t.Fatalf("incorrect native projection: %+v", launch)
	}
	restored, err := FromPBProfileSpec(launch.ProfileSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectProfileLaunchChecked(restored, nil, nil, LaunchOptions{Model: "future-model", ReasoningEffort: "future-effort"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRuntimeModel(RuntimeOption{Ref: "claude"}, "future-model"); err == nil {
		t.Fatal("relaxed another runtime's policy")
	}
}
