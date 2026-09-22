package agentprofile

import "testing"

func TestServiceModelSurvivesRuntimeLaunchSnapshot(t *testing.T) {
	previous := customization.ResolveServiceModel
	customization.ResolveServiceModel = func(runtime, ref string) (RuntimeModelOption, bool, error) {
		if ref != "service/test" {
			return RuntimeModelOption{}, false, nil
		}
		return RuntimeModelOption{Ref: ref, Kind: ModelKindNative, AgentModel: ref, DisplayName: "Service model", SupportedReasoningEfforts: []string{"ultra"}, DefaultReasoningEffort: "ultra"}, true, nil
	}
	defer func() { customization.ResolveServiceModel = previous }()
	runtime, err := ResolveRuntimeOption("codex")
	if err != nil {
		t.Fatal(err)
	}
	model, err := ResolveRuntimeModel(runtime, "service/test")
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateRuntimeReasoningEffort(runtime, model, "ultra"); err != nil {
		t.Fatal(err)
	}
	if err = ValidateRuntimeReasoningEffort(runtime, model, "unsupported"); err == nil {
		t.Fatal("unadvertised effort accepted")
	}
	launch, err := ProjectProfileLaunchChecked(Profile{Type: "codex"}, nil, nil, LaunchOptions{Runtime: "codex", Model: "service/test", ReasoningEffort: "ultra"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Runtime != "codex" || launch.ReasoningEffort != "ultra" {
		t.Fatalf("changed engine or effort: %+v", launch)
	}
}
