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

func TestBuiltinV1SnapshotRefreshesV2ContentWithoutNewDefaults(t *testing.T) {
	// An old builtin/claude session explicitly used Codex with restricted args.
	// Migrating the source must not apply today's Claude shortcut or bypass mode.
	frozen := agentprofile.Profile{Namespace: "builtin", Name: "claude", Source: agentprofile.SourceBuiltin, Type: "codex", Model: "old-model", DefaultArgs: []string{"-c", `sandbox_mode="read-only"`}}
	saved, err := json.Marshal(map[string]any{"profile_spec": agentprofile.ToPBProfileSpec(frozen)})
	if err != nil {
		t.Fatal(err)
	}
	snap := session.Snapshot{AgentType: "codex", LaunchSpec: string(saved), EffectiveModel: "old-model", ExtraArgs: []string{"--model", "old-model", "-c", `sandbox_mode="read-only"`}}
	content, _ := agentprofile.ResolveBuiltin("builtin/claude")
	content.AgentsContent = "New builtin content"
	launch, err := projectRefreshedProfileLaunch(snap, content)
	if err != nil {
		t.Fatal(err)
	}
	if launch.AgentType != "codex" || launch.Model != "old-model" || !reflect.DeepEqual(launch.ExtraArgs, snap.ExtraArgs) || launch.ProfileSpec.AgentsContent != content.AgentsContent {
		t.Fatalf("old execution changed: %+v", launch)
	}
}
