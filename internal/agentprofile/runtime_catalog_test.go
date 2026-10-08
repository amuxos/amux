package agentprofile

import (
	"github.com/amuxos/amux/internal/task"
	"strings"
	"testing"
)

func TestResolveTaskSpecComposesProfileRuntimeAndModel(t *testing.T) {
	spec := task.Spec{Agent: "builtin/codex", Runtime: "codex", Model: "gpt-5.4", Task: "probe"}
	if err := ResolveTaskSpec(&spec, ""); err != nil {
		t.Fatalf("ResolveTaskSpec: %v", err)
	}
	if spec.Agent != "codex" || spec.ResolvedProfile == nil {
		t.Fatalf("resolved task = %+v, want codex with profile snapshot", spec)
	}
	if spec.ResolvedProfile.Ref != "builtin/codex" || spec.ResolvedProfile.RuntimeRef != "codex" || spec.ResolvedProfile.Model != "gpt-5.4" {
		t.Fatalf("resolved profile = %+v", spec.ResolvedProfile)
	}
}

func TestRuntimeSelectionRejectsUnsupportedReasoningEffort(t *testing.T) {
	profile, ok := ResolveBuiltin("builtin/codex")
	if !ok {
		t.Fatal("builtin/codex missing")
	}
	_, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{
		Runtime:         "pi",
		ReasoningEffort: "high",
	})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v", err)
	}
}

func TestRuntimeSelectionProjectsClaudeReasoningEffortAndSnapshot(t *testing.T) {
	profile, ok := ResolveBuiltin("builtin/codex")
	if !ok {
		t.Fatal("builtin/codex missing")
	}
	launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: "claude", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.ProfileSpec.RuntimeRef != "claude" || launch.ProfileSpec.ReasoningEffort != "high" {
		t.Fatalf("snapshot = %+v", launch.ProfileSpec)
	}
	if !strings.Contains(strings.Join(launch.ExtraArgs, " "), "--effort high") {
		t.Fatalf("args = %#v", launch.ExtraArgs)
	}
	roundTrip, err := FromPBProfileSpec(launch.ProfileSpec)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.ExecutionRuntimeRef != "claude" || roundTrip.ReasoningEffort != "high" {
		t.Fatalf("round trip = %+v", roundTrip)
	}
}

func TestChangingProfileDoesNotChangeRuntimeCatalogSelection(t *testing.T) {
	piProfile, ok := ResolveBuiltin("builtin/pi")
	if !ok {
		t.Fatal("builtin/pi missing")
	}
	claudeProfile, ok := ResolveBuiltin("builtin/claude")
	if !ok {
		t.Fatal("builtin/claude missing")
	}
	opts := LaunchOptions{Runtime: "codex", Model: "gpt-5.4", ReasoningEffort: "xhigh"}
	first, err := ProjectProfileLaunchChecked(piProfile, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProjectProfileLaunchChecked(claudeProfile, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.AgentType != second.AgentType || first.Model != second.Model || first.ReasoningEffort != second.ReasoningEffort {
		t.Fatalf("execution drifted with profile: first=%+v second=%+v", first, second)
	}
	if first.AgentProfile == second.AgentProfile {
		t.Fatalf("profile identity did not change: %q", first.AgentProfile)
	}
}

func TestRuntimeSelectionReprojectsProfilePermissionMode(t *testing.T) {
	profile, ok := ResolveBuiltin("builtin/codex")
	if !ok {
		t.Fatal("builtin/codex missing")
	}
	profile.Type = "codex" // Legacy execution snapshot being reprojected.
	profile.DefaultArgs, _ = permissionModeArgs("codex", packProfilePermission{PermissionMode: "plan"})
	launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(launch.ExtraArgs, " "); !strings.Contains(got, "--permission-mode plan") {
		t.Fatalf("args = %q, want projected plan permission", got)
	}
}

func TestRuntimeSelectionOverridesLegacyWorkspaceExecutionArgs(t *testing.T) {
	profile, ok := ResolveBuiltin("builtin/codex")
	if !ok {
		t.Fatal("builtin/codex missing")
	}
	launch, err := ProjectProfileLaunchChecked(profile, []string{
		"--model", "gpt-5.5",
		"-c", `model_provider="legacy"`,
		"-c", `model_reasoning_effort="low"`,
		"-c", `plan_mode_reasoning_effort="minimal"`,
		"-c", `approval_policy="on-request"`,
	}, nil, LaunchOptions{Runtime: "codex", Model: "gpt-5.4", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want selected Runtime Catalog model", launch.Model)
	}
	joined := strings.Join(launch.ExtraArgs, " ")
	for _, stale := range []string{"gpt-5.5", `model_provider="legacy"`, `model_reasoning_effort="low"`, `plan_mode_reasoning_effort="minimal"`} {
		if strings.Contains(joined, stale) {
			t.Fatalf("launch args leaked legacy selection %q: %#v", stale, launch.ExtraArgs)
		}
	}
	for _, want := range []string{"gpt-5.4", `model_reasoning_effort="high"`, `approval_policy="on-request"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("launch args = %#v, want %q", launch.ExtraArgs, want)
		}
	}
}

func TestRuntimeDefaultSelectionClearsLegacyModelAndReasoningArgs(t *testing.T) {
	profile, ok := ResolveBuiltin("builtin/claude")
	if !ok {
		t.Fatal("builtin/claude missing")
	}
	launch, err := ProjectProfileLaunchWithDefaults(profile,
		[]string{"--model=opus", "--effort", "high", "--verbose"}, nil,
		LaunchOptions{Runtime: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Model != "" || launch.ReasoningEffort != "" {
		t.Fatalf("launch selection = model %q effort %q, want runtime defaults", launch.Model, launch.ReasoningEffort)
	}
	if got := strings.Join(launch.ExtraArgs, " "); got != "--permission-mode bypassPermissions --verbose" {
		t.Fatalf("launch args = %q", got)
	}
}
