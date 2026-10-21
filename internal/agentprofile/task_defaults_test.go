package agentprofile

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/amuxos/amux/internal/task"
)

func TestPortableTaskUsesDistributionDefaultsWithoutReplacingContent(t *testing.T) {
	profile := portableTestProfile(t)
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{"team": {Namespace: "team", MaterializedPath: profile.PackPath}}}); err != nil {
		t.Fatal(err)
	}
	oldDefault, oldPresets := DefaultProfileRef, builtinLaunchPresets
	DefaultProfileRef = "builtin/claude"
	builtinLaunchPresets = map[string]BuiltinLaunchPreset{
		"claude": {Runtime: "claude", Model: "default-review-model", PermissionMode: "bypass"},
		"codex":  {Runtime: "codex", PermissionMode: "bypass"},
	}
	t.Cleanup(func() { DefaultProfileRef, builtinLaunchPresets = oldDefault, oldPresets })
	for _, tc := range []struct {
		name, agent, runtime, model, wantRuntime, wantModel string
		args                                                []string
	}{
		{name: "distribution runtime and model", wantRuntime: "claude", wantModel: "default-review-model"},
		{name: "explicit model", model: "chosen-model", wantRuntime: "claude", wantModel: "chosen-model"},
		{name: "explicit runtime uses its native model", runtime: "codex", wantRuntime: "codex"},
		{name: "explicit runtime and model", runtime: "codex", model: "chosen-model", wantRuntime: "codex", wantModel: "chosen-model"},
		{name: "native args survive inherited runtime", args: []string{"--model", "native-model", "--effort", "high", "--permission-mode", "plan"}, wantRuntime: "claude", wantModel: "native-model"},
		{name: "builtin keeps its own preset", agent: "builtin/codex", wantRuntime: "codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := profile.Ref()
			if tc.agent != "" {
				ref = tc.agent
			}
			spec := task.Spec{Agent: ref, Runtime: tc.runtime, Model: tc.model, Task: "Review", ExtraArgs: tc.args, PermissionPolicy: task.PolicyDenyAll}
			if err := ResolveTaskSpec(&spec, registryPath); err != nil {
				t.Fatal(err)
			}
			if spec.Agent != tc.wantRuntime || spec.ResolvedProfile.RuntimeRef != tc.wantRuntime || spec.ResolvedProfile.ConfiguredModel != tc.wantModel {
				t.Fatalf("execution = %+v, want %s/%s", spec.ResolvedProfile, tc.wantRuntime, tc.wantModel)
			}
			if tc.agent == "" && (spec.ResolvedProfile.Ref != profile.Ref() || spec.ResolvedProfile.AgentsContent != profile.AgentsContent || slices.Contains(spec.ExtraArgs, "bypassPermissions")) {
				t.Fatalf("borrowed content or permissions from default expert: %+v", spec)
			}
			if spec.PermissionPolicy != task.PolicyDenyAll {
				t.Fatal("default runtime changed Task permission policy")
			}
			if tc.args != nil && !slices.Equal(spec.ExtraArgs[len(spec.ExtraArgs)-len(tc.args):], tc.args) {
				t.Fatalf("native arguments changed: %q", spec.ExtraArgs)
			}
			frozen := spec.ResolvedProfile
			if err := ResolveTaskSpec(&spec, "/missing/registry.json"); err != nil || spec.ResolvedProfile != frozen {
				t.Fatalf("retry must retain its execution snapshot: %v", err)
			}
		})
	}
}

func TestResolveTaskSpecCarriesReasoningEffort(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	spec := task.Spec{Agent: "builtin/claude", Task: "review", ReasoningEffort: "high"}
	if err := ResolveTaskSpec(&spec, registryPath); err != nil {
		t.Fatal(err)
	}
	if spec.ResolvedProfile == nil || spec.ResolvedProfile.ReasoningEffort != "high" {
		t.Fatalf("resolved profile = %+v, want the requested reasoning effort", spec.ResolvedProfile)
	}
	effortArgs := false
	for i := 0; i+1 < len(spec.ExtraArgs); i++ {
		if spec.ExtraArgs[i] == "--effort" && spec.ExtraArgs[i+1] == "high" {
			effortArgs = true
		}
	}
	if !effortArgs {
		t.Fatalf("extra args = %q, want --effort high", spec.ExtraArgs)
	}
}
