package clientop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/task"
)

func validScheduledTaskSpec() ScheduledTaskSpec {
	return ScheduledTaskSpec{
		Agent:     "builtin/codex",
		Task:      "review the diff",
		Isolation: string(task.IsolationPrivateHome),
		Workspace: WorkspaceSpec{Policy: string(task.WorkspaceTempDir)},
	}
}

func TestScheduledTaskSpecValidateReportsFields(t *testing.T) {
	for _, tt := range []struct {
		name         string
		mut          func(*ScheduledTaskSpec)
		wantField    Field
		wantAlso     Field
		wantRawValue string
	}{
		{"missing agent", func(s *ScheduledTaskSpec) { s.Agent = "  " }, FieldAgent, "", ""},
		{"missing task", func(s *ScheduledTaskSpec) { s.Task = "\t" }, FieldTask, "", ""},
		{"unknown policy", func(s *ScheduledTaskSpec) { s.PermissionPolicy = "  custom-policy  " }, FieldPermissionPolicy, "", "custom-policy"},
		{"escalate policy", func(s *ScheduledTaskSpec) { s.PermissionPolicy = "  " + string(task.PolicyEscalateToMain) + "  " }, FieldPermissionPolicy, "", string(task.PolicyEscalateToMain)},
		{"git worktree without dir or cache key", func(s *ScheduledTaskSpec) {
			s.Workspace.Policy = string(task.WorkspaceGitWorktree)
		}, FieldWorkspaceLocalDir, FieldRepoCacheKey, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := validScheduledTaskSpec()
			tt.mut(&spec)
			err := spec.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want a FieldError")
			}
			if err.Field != tt.wantField {
				t.Fatalf("Field = %q, want %q", err.Field, tt.wantField)
			}
			if err.Also != tt.wantAlso {
				t.Fatalf("Also = %q, want %q", err.Also, tt.wantAlso)
			}
			if err.RawValue != tt.wantRawValue {
				t.Fatalf("RawValue = %q, want %q", err.RawValue, tt.wantRawValue)
			}
		})
	}
}

func TestScheduledTaskSpecValidateAcceptsRepoCacheKeyForWorktree(t *testing.T) {
	spec := validScheduledTaskSpec()
	spec.Workspace.Policy = string(task.WorkspaceGitWorktree)
	spec.Workspace.Repo = &RepoSpec{CacheKey: "org/repo"}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestScheduledTaskSpecNormalizesDefaultPermissionPolicyToEmpty(t *testing.T) {
	// "default" is the caller-facing word for "let the profile decide"; the
	// wire representation of that is an empty string.
	spec := validScheduledTaskSpec()
	spec.PermissionPolicy = "default"
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if got := spec.ToTaskSpec().PermissionPolicy; got != "" {
		t.Fatalf("PermissionPolicy = %q, want empty", got)
	}
}

func TestScheduledTaskSpecToTaskSpecTrimsAndBuildsRepo(t *testing.T) {
	spec := ScheduledTaskSpec{
		Agent:             "  team/coder  ",
		Runtime:           "  claude  ",
		Model:             "  review-model  ",
		ReasoningEffort:   "  high  ",
		Task:              "  run tests  ",
		RunAs:             "  ops-bot  ",
		Isolation:         string(task.IsolationProcess),
		CredentialProfile: "ci-creds",
		PermissionPolicy:  string(task.PolicyDenyAll),
		Workspace: WorkspaceSpec{
			Policy:   string(task.WorkspaceGitWorktree),
			LocalDir: "/tmp/work",
			Repo: &RepoSpec{
				Kind: "git", Remote: "git@example.com:org/repo.git",
				CacheKey: "org/repo", Commit: "abc123", Subdir: "svc/api",
			},
		},
		TimeoutSeconds:       600,
		LegacyTrustedModeAck: true,
	}
	got := spec.ToTaskSpec()
	if got.Agent != "team/coder" || got.Task != "run tests" {
		t.Fatalf("agent/task = %q/%q, want trimmed", got.Agent, got.Task)
	}
	if got.Runtime != "claude" || got.Model != "review-model" {
		t.Fatalf("scheduled execution choice was not preserved: runtime=%q model=%q", got.Runtime, got.Model)
	}
	if got.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want trimmed %q", got.ReasoningEffort, "high")
	}
	if got.RunAs != "ops-bot" {
		t.Fatalf("RunAs = %q, want trimmed \"ops-bot\"", got.RunAs)
	}
	if got.Isolation != task.IsolationProcess {
		t.Fatalf("Isolation = %q, want %q", got.Isolation, task.IsolationProcess)
	}
	if got.CredentialProfile != "ci-creds" {
		t.Fatalf("CredentialProfile = %q, want %q", got.CredentialProfile, "ci-creds")
	}
	if got.PermissionPolicy != task.PolicyDenyAll {
		t.Fatalf("PermissionPolicy = %q, want %q", got.PermissionPolicy, task.PolicyDenyAll)
	}
	if got.Workspace.Policy != task.WorkspaceGitWorktree {
		t.Fatalf("Workspace.Policy = %q, want %q", got.Workspace.Policy, task.WorkspaceGitWorktree)
	}
	if got.Workspace.LocalDir != "/tmp/work" {
		t.Fatalf("Workspace.LocalDir = %q, want %q", got.Workspace.LocalDir, "/tmp/work")
	}
	if got.TimeoutSeconds != 600 {
		t.Fatalf("TimeoutSeconds = %d, want 600", got.TimeoutSeconds)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal task spec: %v", err)
	}
	if strings.Contains(string(encoded), "trusted_mode_ack") {
		t.Fatalf("task spec retained legacy trusted_mode_ack: %s", encoded)
	}
	if got.Workspace.Repo == nil ||
		got.Workspace.Repo.Kind != "git" ||
		got.Workspace.Repo.Remote != "git@example.com:org/repo.git" ||
		got.Workspace.Repo.CacheKey != "org/repo" ||
		got.Workspace.Repo.Commit != "abc123" ||
		got.Workspace.Repo.Subdir != "svc/api" {
		t.Fatalf("repo = %#v, want fully populated", got.Workspace.Repo)
	}
	if got.Blocking {
		t.Fatal("Blocking = true, want false: a scheduled task is never blocking")
	}
	if got.ExecutionIdentity != nil {
		t.Fatal("ExecutionIdentity must stay nil: it is server-resolved")
	}
}

func TestScheduledTaskSpecToTaskSpecPassesThroughPolicyAllow(t *testing.T) {
	spec := validScheduledTaskSpec()
	spec.PermissionPolicy = string(task.PolicyAllow)
	if got := spec.ToTaskSpec().PermissionPolicy; got != task.PolicyAllow {
		t.Fatalf("PermissionPolicy = %q, want %q", got, task.PolicyAllow)
	}
}

func TestScheduledTaskSpecToTaskSpecPanicsOnUnvalidatedSpec(t *testing.T) {
	// ToTaskSpec documents that callers must Validate first. A caller who
	// skips Validate and passes escalate-to-main must not silently get back
	// the permissive empty-string default; it must panic instead.
	spec := validScheduledTaskSpec()
	spec.PermissionPolicy = string(task.PolicyEscalateToMain)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ToTaskSpec() did not panic for an unvalidated escalate-to-main spec")
		}
	}()
	spec.ToTaskSpec()
}

func TestScheduledTaskSpecToTaskSpecOmitsRepoWhenAllFieldsEmpty(t *testing.T) {
	spec := validScheduledTaskSpec()
	spec.Workspace.Repo = &RepoSpec{}
	if got := spec.ToTaskSpec(); got.Workspace.Repo != nil {
		t.Fatalf("Repo = %#v, want nil for an all-empty repo", got.Workspace.Repo)
	}
}
