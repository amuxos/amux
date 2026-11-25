package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

func TestScheduleTaskFlagsCarryPortableExecutionSelection(t *testing.T) {
	for _, language := range []string{"en", "zh-CN", "zz"} {
		t.Run(language, func(t *testing.T) {
			t.Setenv("AMUX_LANGUAGE", language)
			var flags scheduleTaskFlags
			cmd := &cobra.Command{}
			registerScheduleTaskFlags(cmd, &flags)
			if err := cmd.Flags().Parse([]string{"--agent", "team/reviewer", "--task", "review",
				"--runtime", "claude", "--model", "review-model", "--reasoning-effort", "high"}); err != nil {
				t.Fatal(err)
			}
			spec := flags.clientopSpec().ToTaskSpec()
			if spec.Agent != "team/reviewer" || spec.Runtime != "claude" || spec.Model != "review-model" || spec.ReasoningEffort != "high" {
				t.Fatalf("scheduled execution selection was lost: %#v", spec)
			}
			for flag, markers := range map[string][2]string{
				"runtime":          {"omitted uses", "省略时使用"},
				"model":            {"pass the current session model by default", "默认应传入当前会话模型"},
				"reasoning-effort": {"pass the current session effort by default", "默认应传入当前会话思考强度"},
			} {
				usage := cmd.Flags().Lookup(flag).Usage
				want := markers[0]
				if language == "zh-CN" {
					want = markers[1]
				}
				if !strings.Contains(usage, want) {
					t.Fatalf("untranslated --%s help: %s", flag, usage)
				}
			}
		})
	}
}

func TestScheduleUpdateSharesTaskFlagsWithCreateTask(t *testing.T) {
	update := clientopCoverageFindSubcommandOrNil(newScheduleCmd(), "update")
	create := clientopCoverageFindSubcommandOrNil(newScheduleCmd(), "create-task")
	if update == nil || create == nil {
		t.Fatal("missing update or create-task command")
	}
	for _, flag := range []string{"name", "spec"} {
		if update.Flags().Lookup(flag) == nil {
			t.Errorf("schedule update has no --%s flag", flag)
		}
	}
	// Every task-spec flag on create-task must also be on update, so a field a
	// scheduled task can carry is settable through both. (update adds --spec for
	// the time rule and --name for the label, which create-task spells
	// differently; those two are not part of this set.)
	for _, flag := range []string{"agent", "runtime", "model", "reasoning-effort", "task", "isolation", "credential-profile", "permission-policy", "workspace-policy", "local-dir", "repo-kind", "repo-remote", "repo-cache-key", "repo-commit", "repo-subdir", "timeout", "trusted-mode-ack", "run-as"} {
		if create.Flags().Lookup(flag) == nil {
			t.Fatalf("create-task lost --%s", flag)
		}
		if update.Flags().Lookup(flag) == nil {
			t.Errorf("update is missing --%s that create-task has", flag)
		}
	}
	if update.Annotations[jsonOutputAnnotation] != "true" {
		t.Error("update does not declare supportsJSONOutput")
	}
}

type fakeScheduleUpdateHub struct {
	pb.AmuxdClient
	item      *pb.ScheduleItem
	updateReq *pb.UpdateScheduleRequest
}

func (f *fakeScheduleUpdateHub) GetSchedule(_ context.Context, _ *pb.GetScheduleRequest, _ ...grpc.CallOption) (*pb.GetScheduleResponse, error) {
	return &pb.GetScheduleResponse{Schedule: f.item}, nil
}

func (f *fakeScheduleUpdateHub) UpdateSchedule(_ context.Context, req *pb.UpdateScheduleRequest, _ ...grpc.CallOption) (*pb.UpdateScheduleResponse, error) {
	f.updateReq = req
	return &pb.UpdateScheduleResponse{
		Schedule: &pb.ScheduleItem{
			Id:        req.Id,
			Name:      f.item.Name,
			Spec:      f.item.Spec,
			NextRunAt: 1800000000,
		},
	}, nil
}

func TestScheduleUpdatePatchesOnlyChangedTaskFlagsAndSupportsJSON(t *testing.T) {
	existingSpec, err := json.Marshal(task.Spec{
		Agent:             "team/reviewer",
		Task:              "review recent commits",
		Runtime:           "codex",
		Model:             "gpt-5",
		ReasoningEffort:   "medium",
		Isolation:         task.IsolationPrivateHome,
		CredentialProfile: "corp-creds",
		PermissionPolicy:  task.PolicyAllow,
		Workspace: task.WorkspaceSpec{
			Policy:   task.WorkspaceGitWorktree,
			LocalDir: "/srv/repo",
		},
		TimeoutSeconds: 300,
		ExtraArgs:      []string{"--fast"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeScheduleUpdateHub{
		item: &pb.ScheduleItem{
			Id:           8,
			Name:         "nightly",
			ScheduleType: schedule.ScheduleTypeSubtask,
			Spec:         "daily 09:00",
			TaskSpecJson: existingSpec,
		},
	}
	prevNewClient := newScheduleClient
	newScheduleClient = func() (pb.AmuxdClient, func() error, error) {
		return fake, func() error { return nil }, nil
	}
	t.Cleanup(func() { newScheduleClient = prevNewClient })

	cmd := newRootCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"schedule", "update", "8", "--timeout", "900", "--reasoning-effort", "high", "--permission-policy", "deny-all", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("schedule update execute: %v", err)
	}
	if fake.updateReq == nil || len(fake.updateReq.TaskSpecJson) == 0 {
		t.Fatalf("expected UpdateSchedule to receive patched task spec, got %+v", fake.updateReq)
	}
	var updated task.Spec
	if err := json.Unmarshal(fake.updateReq.TaskSpecJson, &updated); err != nil {
		t.Fatalf("unmarshal updated task spec: %v", err)
	}
	if updated.TimeoutSeconds != 900 || updated.ReasoningEffort != "high" || updated.PermissionPolicy != task.PolicyDenyAll {
		t.Fatalf("changed flags not applied: %+v", updated)
	}
	if updated.Agent != "team/reviewer" || updated.Task != "review recent commits" || updated.Model != "gpt-5" ||
		updated.CredentialProfile != "corp-creds" || updated.Isolation != task.IsolationPrivateHome ||
		updated.Workspace.Policy != task.WorkspaceGitWorktree || updated.Workspace.LocalDir != "/srv/repo" ||
		len(updated.ExtraArgs) != 1 || updated.ExtraArgs[0] != "--fast" {
		t.Fatalf("unchanged task fields were lost during partial update: %+v", updated)
	}
	var jsonOut map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &jsonOut); err != nil {
		t.Fatalf("invalid json output %q: %v", stdout.String(), err)
	}
	if jsonOut["schema"] != "amux.schedule.update.v1" {
		t.Fatalf("json schema = %v, want amux.schedule.update.v1", jsonOut["schema"])
	}
}
