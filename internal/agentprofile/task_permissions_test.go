package agentprofile

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestTaskPermissionNativeMatrix(t *testing.T) {
	for _, tc := range []struct {
		agent  string
		policy task.PermissionPolicy
		outer  bool
		want   []string
	}{
		{"codex", "", false, []string{"-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}},
		{"codex", task.PolicyAllow, false, []string{"-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}},
		{"codex", task.PolicyAllow, true, []string{"-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}},
		{"codex", task.PolicyDenyAll, false, []string{"-c", `approval_policy="never"`, "-c", `sandbox_mode="read-only"`}},
		{"codex", task.PolicyDenyAll, true, []string{"-c", `approval_policy="never"`, "-c", `sandbox_mode="read-only"`}},
		{"codex", task.PolicyEscalateToMain, false, []string{"-c", `approval_policy="on-request"`, "-c", `sandbox_mode="danger-full-access"`}},
		{"codex", task.PolicyEscalateToMain, true, []string{"-c", `approval_policy="on-request"`, "-c", `sandbox_mode="danger-full-access"`}},
		{"claude", task.PolicyAllow, false, []string{"--permission-mode", "bypassPermissions"}},
		{"claude", task.PolicyAllow, true, []string{"--permission-mode", "bypassPermissions"}},
		{"claude", task.PolicyDenyAll, true, []string{"--permission-mode", "plan"}},
		{"claude", task.PolicyEscalateToMain, true, []string{"--permission-mode", "default"}},
		{"pi", task.PolicyAllow, false, nil},
	} {
		t.Run(tc.agent+"/"+string(tc.policy)+"/"+strings.Join(tc.want, " "), func(t *testing.T) {
			got, err := TaskPermissionArgs(tc.agent, tc.policy, nil, tc.outer)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("args = %q, err = %v, want %q", got, err, tc.want)
			}
		})
	}
	for _, policy := range []task.PermissionPolicy{task.PolicyDenyAll, task.PolicyEscalateToMain} {
		_, err := TaskPermissionArgs("pi", policy, nil, true)
		if err == nil || !strings.Contains(err.Error(), "pi") || !strings.Contains(err.Error(), string(policy)) || !strings.Contains(err.Error(), "无法实现") {
			t.Fatalf("unsupported policy = %v", err)
		}
	}
	if _, err := TaskPermissionArgs("codex", "invalid", nil, false); err == nil {
		t.Fatal("accepted invalid policy")
	}
}

func TestTaskPermissionNormalizesLegacyFlagsWithoutLosingOtherArguments(t *testing.T) {
	args := []string{
		"--model", "review-model", "-c", `model_reasoning_effort="high"`,
		"-c", `approval_policy="on-failure"`, "--config=sandbox_mode=\"danger-full-access\"",
		"-csandbox_workspace_write.writable_roots=[\"/\"]", "--config", "sandbox_workspace_write.network_access=true",
		"-c", "sandbox_workspace_write={network_access=true}",
		"--ask-for-approval=never", "-a", "never", "-sread-only", "--sandbox", "danger-full-access",
		"--full-auto", "--yolo", "--dangerously-bypass-approvals-and-sandbox",
		"--permission-mode", "bypassPermissions", "--dangerously-skip-permissions",
		"--allow-dangerously-skip-permissions", "--bypass-permissions", "--add-dir", "/outside",
		"--debug", "--", "--literal-prompt",
	}
	original := slices.Clone(args)
	got, err := TaskPermissionArgs("codex", task.PolicyDenyAll, args, true)
	want := []string{"--model", "review-model", "-c", `model_reasoning_effort="high"`, "--debug", "-c", `approval_policy="never"`, "-c", `sandbox_mode="read-only"`, "--", "--literal-prompt"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("args = %q, err = %v, want %q", got, err, want)
	}
	if !slices.Equal(args, original) {
		t.Fatal("mutated caller arguments")
	}
	again, err := TaskPermissionArgs("codex", task.PolicyDenyAll, got, true)
	if err != nil || !slices.Equal(again, got) {
		t.Fatalf("projection is not idempotent: %q, %v", again, err)
	}
}

func TestTaskPermissionFlagsAfterSeparatorCannotOverrideClaudePolicy(t *testing.T) {
	// The SDK sidecar continues parsing known flags after --, unlike a CLI.
	args := []string{"--", "--dangerously-skip-permissions", "--permission-mode=bypassPermissions", "--debug"}
	got, err := TaskPermissionArgs("claude", task.PolicyDenyAll, args, true)
	want := []string{"--permission-mode", "plan", "--", "--debug"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("SDK permission override survived: %q, %v", got, err)
	}
}

func TestTaskPermissionDoesNotConsumeOtherEnginesShortOptions(t *testing.T) {
	args := []string{"-c", "approval_policy=literal", "-s", "session.json", "-append", "content", "--model", "chosen"}
	got, err := TaskPermissionArgs("pi", task.PolicyAllow, args, false)
	if err != nil || !slices.Equal(got, args) {
		t.Fatalf("non-permission arguments changed: %q, %v", got, err)
	}
}

func TestTaskPermissionsIndependentOfV2ExpertAndFrozenForRetry(t *testing.T) {
	profile := portableTestProfile(t)
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{"team": {Namespace: "team", MaterializedPath: profile.PackPath}}}); err != nil {
		t.Fatal(err)
	}
	oldDefault, oldPresets := DefaultProfileRef, builtinLaunchPresets
	DefaultProfileRef = "builtin/codex"
	builtinLaunchPresets = map[string]BuiltinLaunchPreset{"codex": {Runtime: "codex", PermissionMode: "bypass"}}
	t.Cleanup(func() { DefaultProfileRef, builtinLaunchPresets = oldDefault, oldPresets })
	for _, ref := range []string{"builtin/codex", profile.Ref()} {
		for _, policy := range []task.PermissionPolicy{task.PolicyAllow, task.PolicyDenyAll, task.PolicyEscalateToMain} {
			spec := task.Spec{Agent: ref, Task: "review", PermissionPolicy: policy, ExtraArgs: []string{"--yolo"}}
			if err := ResolveTaskSpec(&spec, registryPath); err != nil {
				t.Fatal(err)
			}
			want, _ := TaskPermissionArgs("codex", policy, nil, false)
			if !slices.Equal(spec.ExtraArgs, want) || spec.ResolvedProfile.PermissionPolicy != string(policy) || spec.ResolvedProfile.Ref != ref {
				t.Fatalf("resolved Task = %+v", spec)
			}
			if ref == profile.Ref() && spec.ResolvedProfile.AgentsContent != profile.AgentsContent {
				t.Fatal("replaced expert content")
			}
			encoded, _ := json.Marshal(spec)
			var retry task.Spec
			if err := json.Unmarshal(encoded, &retry); err != nil {
				t.Fatal(err)
			}
			if err := ResolveTaskSpec(&retry, "/missing/registry"); err != nil || !slices.Equal(retry.ExtraArgs, spec.ExtraArgs) || retry.PermissionPolicy != policy {
				t.Fatalf("retry changed frozen policy: %+v, %v", retry, err)
			}
		}
	}
	// Named shortcuts still use their native preset for direct Sessions.
	builtin, _ := ResolveBuiltin("builtin/codex")
	direct, err := ProjectProfileLaunchWithDefaults(builtin, nil, nil, LaunchOptions{})
	if err != nil || !slices.Contains(direct.ExtraArgs, `sandbox_mode="danger-full-access"`) {
		t.Fatalf("direct Session preset changed: %q, %v", direct.ExtraArgs, err)
	}
}

func TestTaskPermissionErrorsBilingualAndFallback(t *testing.T) {
	for key, args := range map[string][]any{
		"task_permission_invalid":        {"unknown"},
		"task_permission_unsupported":    {"pi", "deny-all"},
		"task_permission_resume":         {"deny-all", "policy-allow"},
		"task_permission_resume_unknown": nil,
	} {
		en := profileCatalog.Format(i18n.English, key, args...)
		zh := profileCatalog.Format(i18n.Chinese, key, args...)
		fallback := profileCatalog.Format(i18n.Locale("unknown"), key, args...)
		if en == key || zh == key || zh == en || fallback != en || strings.Contains(en+zh, "%!") || profileError(key, args...).Error() != en+"\n"+zh {
			t.Fatalf("invalid translated error for %s: %q / %q / %q", key, en, zh, fallback)
		}
	}
}

func TestConfigOnlyPermissionProjectionRequiresProfileForRestrictedLaunch(t *testing.T) {
	old := customization
	t.Cleanup(func() { customization = old })
	customization.PermissionModeArgs = func(_ string, mode string) ([]string, error) {
		return []string{"--config-permission", mode}, nil
	}
	stripConfigPermission := func(_ string, args []string) []string {
		var out []string
		for i := 0; i < len(args); i++ {
			if args[i] == "--config-permission" {
				i++
				continue
			}
			if strings.HasPrefix(args[i], "--config-permission=") {
				continue
			}
			out = append(out, args[i])
		}
		return out
	}
	customization.StripTaskPermissionArgs = stripConfigPermission
	customization.LaunchProcessArgs = stripConfigPermission
	raw := task.Spec{Agent: "config-only", PermissionPolicy: task.PolicyDenyAll}
	if err := ApplyTaskPermissionPolicy(&raw, false); err == nil {
		t.Fatal("raw launch silently lost config-only restriction")
	}
	spec := task.Spec{Agent: "config-only", PermissionPolicy: task.PolicyDenyAll, ExtraArgs: []string{"--debug", "--config-permission=allow"}, ResolvedProfile: &pb.AgentProfileSpec{Type: "config-only", DefaultArgs: []string{"--config-permission", "bypass"}}}
	if err := ApplyTaskPermissionPolicy(&spec, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.ExtraArgs, []string{"--debug"}) || !slices.Equal(spec.ResolvedProfile.DefaultArgs, []string{"--config-permission", "plan"}) {
		t.Fatalf("config-only native projection = %+v", spec)
	}
}
