package agentprofile

import (
	"encoding/json"
	"slices"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestExternalSessionDefaultsToBypassAndFreezesPermissions(t *testing.T) {
	profile := portableTestProfile(t)
	before, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		engine string
		want   []string
	}{
		{"codex", []string{"-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}},
		{"claude", []string{"--permission-mode", "bypassPermissions"}},
		{"pi", nil},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			launch, err := ProjectProfileLaunchWithDefaults(profile, nil, nil, LaunchOptions{Runtime: tc.engine})
			if err != nil || !slices.Equal(launch.ExtraArgs, tc.want) || !slices.Equal(launch.ProfileSpec.DefaultArgs, tc.want) {
				t.Fatalf("launch = %+v, error = %v, want %q", launch, err, tc.want)
			}
			frozen, err := FromPBProfileSpec(launch.ProfileSpec)
			if err != nil {
				t.Fatal(err)
			}
			retry, err := ProjectProfileLaunchWithDefaults(frozen, nil, nil, LaunchOptions{})
			if err != nil || !slices.Equal(retry.ExtraArgs, launch.ExtraArgs) {
				t.Fatalf("retry changed permissions: %q, %v", retry.ExtraArgs, err)
			}
			after, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("source expert acquired execution settings")
			}
		})
	}
}

func TestSessionExplicitNativePermissionsOverrideBypass(t *testing.T) {
	for _, tc := range []struct {
		engine string
		args   []string
	}{
		{"codex", []string{"-a", "on-request", "-s", "read-only"}},
		{"codex", []string{"--ask-for-approval=untrusted"}},
		{"codex", []string{"--sandbox=workspace-write"}},
		{"codex", []string{"--config", `approval_policy="on-request"`}},
		{"codex", []string{"--config=sandbox_mode=\"read-only\""}},
		{"codex", []string{"-csandbox_mode=\"read-only\""}},
		{"codex", []string{"-sread-only"}},
		{"codex", []string{"--full-auto"}},
		{"claude", []string{"--permission-mode", "plan"}},
		{"claude", []string{"--permission-mode=default"}},
	} {
		for _, profile := range []Profile{portableTestProfile(t), mustBuiltinProfile(t, "builtin/"+tc.engine)} {
			launch, err := ProjectProfileLaunchWithDefaults(profile, tc.args, nil, LaunchOptions{Runtime: tc.engine})
			if err != nil || !slices.Equal(launch.ExtraArgs, tc.args) {
				t.Fatalf("%s %s: explicit %q became %q, error = %v", profile.Ref(), tc.engine, tc.args, launch.ExtraArgs, err)
			}
		}
	}
}

func mustBuiltinProfile(t *testing.T, ref string) Profile {
	t.Helper()
	p, ok := ResolveBuiltin(ref)
	if !ok {
		t.Fatalf("missing builtin %s", ref)
	}
	return p
}

func TestSessionDefaultsRespectSavedPermissionsAndTaskPolicy(t *testing.T) {
	for _, spec := range []*pb.AgentProfileSpec{
		{DefaultArgs: []string{"--permission-mode", "plan"}},
		{PermissionPolicy: "deny-all"},
		{PermissionPolicy: "escalate-to-main"},
	} {
		args := []string{"--model", "review-model"}
		got, err := SessionPermissionDefaults("claude", args, spec)
		if err != nil || !slices.Equal(got, args) {
			t.Fatalf("overrode saved permissions: %q, %v", got, err)
		}
	}
	args := []string{"--add-dir", "/extra", "--model", "review-model"}
	got, err := SessionPermissionDefaults("codex", args, nil)
	if err != nil || !slices.Contains(got, `approval_policy="never"`) || !slices.Equal(got[len(got)-len(args):], args) {
		t.Fatalf("unrelated native arguments suppressed defaults: %q, %v", got, err)
	}
}
