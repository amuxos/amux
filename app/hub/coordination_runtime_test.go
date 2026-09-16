package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestCoordinationRuntimeReopensRouteAndReadsCurrentTaskTruth(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "coordination.db")
	tasks := &runtimeTaskControl{run: &task.TaskRun{ID: "task-1", Status: task.StatusRunning, RequestedAt: time.Now().UTC()}}
	profiles := func(context.Context) ([]agentprofile.Profile, error) {
		return []agentprofile.Profile{{Namespace: "team", Name: "reviewer", Revision: "abc", Type: "codex"}}, nil
	}
	runtime, err := openCoordinationRuntime(context.Background(), path, tasks, profiles)
	if err != nil {
		t.Fatalf("openCoordinationRuntime: %v", err)
	}
	listed, _ := runtime.server.ListCollaborators(context.Background(), &pb.ListCollaboratorsRequest{})
	created, err := runtime.server.Delegate(context.Background(), &pb.DelegateRequest{CollaboratorId: listed.Collaborators[0].Id, Input: "review", InitiatorRef: "amux-session:main", ScopeRef: "turn:1"})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if tasks.submitted.ParentSessionID != "main" || tasks.submitted.ParentTurnID != "" {
		t.Fatalf("delegation lost parent or fabricated server-owned turn: %+v", tasks.submitted)
	}
	id := created.Snapshot.Delegation.Id
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	tasks.run = &task.TaskRun{ID: "task-1", Status: task.StatusSucceeded, RequestedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), Result: &task.TaskResult{Status: task.ResultOK, Summary: "survived restart"}}
	reopened, err := openCoordinationRuntime(context.Background(), path, tasks, profiles)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.server.GetDelegation(context.Background(), &pb.GetDelegationRequest{DelegationId: id})
	if err != nil {
		t.Fatalf("GetDelegation: %v", err)
	}
	if got.Snapshot.Delegation.Status != "succeeded" || got.Snapshot.Delegation.Result.Summary != "survived restart" {
		t.Fatalf("got = %#v", got)
	}
}

type runtimeTaskControl struct {
	submitted task.Spec
	run       *task.TaskRun
}

func (f *runtimeTaskControl) Submit(_ context.Context, spec task.Spec) (*task.TaskRun, error) {
	f.submitted = spec
	return &task.TaskRun{ID: "task-1", Status: task.StatusPending, RequestedAt: time.Now().UTC()}, nil
}
func (f *runtimeTaskControl) Stream(ctx context.Context, _ string, _ uint64, _ func(task.TaskEvent) error) error {
	<-ctx.Done()
	return nil
}
func (f *runtimeTaskControl) Get(context.Context, string) (*task.TaskRun, error) { return f.run, nil }
func (f *runtimeTaskControl) Cancel(context.Context, string) error               { return nil }
func (f *runtimeTaskControl) RespondPermission(context.Context, string, task.PermissionResponse) error {
	return nil
}
