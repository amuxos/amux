package runtime

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/session"
)

func TestPortableRefreshPreservesFrozenExecution(t *testing.T) {
	frozen := agentprofile.Profile{Namespace: "team", Name: "reviewer", Revision: "old", Type: "codex", Model: "service/review", ConfiguredModel: "service/review", ExecutionRuntimeRef: "codex", ReasoningEffort: "high", Provider: &agentprofile.Provider{Platform: "openai-compat", BaseURL: "https://api.example.com/v1", UpstreamModel: "review-model", APIKeyEnv: "MODEL_TOKEN"}}
	saved, err := json.Marshal(map[string]any{"profile_spec": agentprofile.ToPBProfileSpec(frozen)})
	if err != nil {
		t.Fatal(err)
	}
	snap := session.Snapshot{AgentType: "codex", LaunchSpec: string(saved), EffectiveModel: "service/review", ExtraArgs: []string{"-c", `approval_policy="on-request"`, "-c", `sandbox_mode="read-only"`, "--model", "review-model"}, Env: []string{"CALLER_ENV=value"}}
	content := agentprofile.Profile{Namespace: "team", Name: "reviewer", Revision: "new", AgentsContent: "Updated review instructions", Envs: []string{"REVIEW_TOKEN"}}
	launch, err := projectRefreshedProfileLaunch(snap, content)
	if err != nil {
		t.Fatal(err)
	}
	if launch.AgentProfile != "team/reviewer@new" || launch.ProfileSpec.AgentsContent != content.AgentsContent {
		t.Fatal("content was not refreshed")
	}
	if !reflect.DeepEqual(launch.ExtraArgs, snap.ExtraArgs) || !reflect.DeepEqual(launch.Env, snap.Env) {
		t.Fatal("execution arguments changed")
	}
	if launch.ProfileSpec.Type != "codex" || launch.ProfileSpec.RuntimeRef != "codex" || launch.ProfileSpec.Provider.BaseUrl != frozen.Provider.BaseURL || launch.ProfileSpec.Model != frozen.Model {
		t.Fatalf("execution changed: %+v", launch.ProfileSpec)
	}
	if !reflect.DeepEqual(launch.ProfileSpec.Envs, content.Envs) {
		t.Fatal("new content requirements lost")
	}
}
