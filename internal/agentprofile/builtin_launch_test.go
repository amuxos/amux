package agentprofile

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/task"
)

func TestBuiltinV2ShortcutAndFrozenExecution(t *testing.T) {
	for _, name := range []string{"codex", "claude", "pi"} {
		t.Run(name, func(t *testing.T) {
			profile, _ := ResolveBuiltin("builtin/" + name)
			before := cloneProfile(profile)
			if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{}); err == nil {
				t.Fatal("content projector inferred builtin execution")
			}
			launch, err := ProjectProfileLaunchWithDefaults(profile, nil, nil, LaunchOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if launch.AgentType != name || launch.Runtime != name || launch.ProfileSpec.RuntimeRef != name {
				t.Fatalf("shortcut = %+v", launch)
			}
			if !reflect.DeepEqual(profile, before) {
				t.Fatal("launch mutated content")
			}
			frozen, err := FromPBProfileSpec(launch.ProfileSpec)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := BuiltinLaunchDefaults(frozen); ok {
				t.Fatal("snapshot consumes live defaults")
			}
			retry, err := ProjectProfileLaunchWithDefaults(frozen, nil, nil, LaunchOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(retry.ExtraArgs, launch.ExtraArgs) {
				t.Fatalf("retry args %v != %v", retry.ExtraArgs, launch.ExtraArgs)
			}
		})
	}
}

func TestBuiltinCallerOverridesAndExternalIsolation(t *testing.T) {
	profile, _ := ResolveBuiltin("builtin/codex")
	launch, err := ProjectProfileLaunchWithDefaults(profile, []string{"--permission-mode", "plan", "--model", "stale"}, nil, LaunchOptions{Runtime: "claude", Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.AgentType != "claude" || launch.Model != "sonnet" || strings.Contains(strings.Join(launch.ExtraArgs, " "), "stale") {
		t.Fatalf("explicit selection lost: %+v", launch)
	}
	if got := strings.Join(launch.ExtraArgs, " "); !strings.HasSuffix(got, "--permission-mode plan") {
		t.Fatalf("permission override lost: %s", got)
	}
	launch, err = ProjectProfileLaunchWithDefaults(profile, []string{"--model", "native-override", "-c", `model_reasoning_effort="high"`}, nil, LaunchOptions{Model: "configured"})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Model != "native-override" || !slices.Contains(launch.ExtraArgs, `model_reasoning_effort="high"`) {
		t.Fatalf("native shortcut args lost: %+v", launch)
	}
	profile.Source = SourcePack
	if _, err := ProjectProfileLaunchWithDefaults(profile, nil, nil, LaunchOptions{}); err == nil {
		t.Fatal("external content acquired shortcut defaults")
	}
	launch, err = ProjectProfileLaunchWithDefaults(profile, nil, nil, LaunchOptions{Runtime: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(launch.ProfileSpec.DefaultArgs, []string{"--permission-mode", "bypassPermissions"}) {
		t.Fatalf("external Session did not receive caller bypass defaults: %q", launch.ProfileSpec.DefaultArgs)
	}
}

func TestBuiltinTaskUsesCallerDefaultsAndSnapshot(t *testing.T) {
	spec := task.Spec{Agent: "builtin/claude"}
	if err := ResolveTaskSpec(&spec, ""); err != nil {
		t.Fatal(err)
	}
	if spec.Agent != "claude" || spec.ResolvedProfile == nil {
		t.Fatalf("task = %+v", spec)
	}
	before := spec.ResolvedProfile
	if err := ResolveTaskSpec(&spec, ""); err != nil {
		t.Fatal(err)
	}
	if spec.ResolvedProfile != before {
		t.Fatal("retry re-resolved source content")
	}
}
