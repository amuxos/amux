package amuxadapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/coordination"
	"github.com/amuxos/amux/internal/task"
)

func TestBackendReadsTaskAsOnlyLifecycleTruth(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	tasks := &fakeTaskControl{run: &task.TaskRun{ID: "task-1", Status: task.StatusSucceeded, RequestedAt: now, FinishedAt: now.Add(time.Second), Result: &task.TaskResult{Status: task.ResultOK, Summary: "done"}}}
	backend, err := New(Options{Tasks: tasks, BuildSpec: SpecBuilderFunc(func(context.Context, *coordination.Route) (task.Spec, error) {
		return task.Spec{Agent: "team/reviewer@abc"}, nil
	}), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	route := &coordination.Route{ID: "delegation-1", TargetRef: "team/reviewer@abc", Input: "review", Deadline: now.Add(time.Minute), TrustLevel: coordination.TrustInternal, CreatedAt: now}
	ref, err := backend.Create(context.Background(), route)
	if err != nil || ref != "amux-task:task-1" {
		t.Fatalf("Create = %q, %v", ref, err)
	}
	if tasks.submitted.Task != "review" || tasks.submitted.Mode != task.ModeEphemeral || tasks.submitted.PermissionPolicy != task.PolicyEscalateToMain {
		t.Fatalf("submitted = %#v", tasks.submitted)
	}
	route.BackendRef = ref
	snapshot, err := backend.Get(context.Background(), route)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snapshot.Delegation.Status != coordination.StatusSucceeded || snapshot.Delegation.Result == nil || snapshot.Delegation.Result.Summary != "done" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Delegation.BackendRef != "" {
		t.Fatalf("backend ref leaked: %#v", snapshot.Delegation)
	}
}

func TestBackendWaitProjectsTaskEventSequenceWithoutPersistingIt(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	tasks := &fakeTaskControl{run: &task.TaskRun{ID: "task-1", Status: task.StatusRunning, RequestedAt: now}, events: []task.TaskEvent{{Seq: 7, Kind: task.TaskEventOutput, Output: []byte("working"), At: now}}}
	backend, _ := New(Options{Tasks: tasks, BuildSpec: SpecBuilderFunc(func(context.Context, *coordination.Route) (task.Spec, error) { return task.Spec{}, nil })})
	route := &coordination.Route{ID: "delegation-1", BackendRef: "amux-task:task-1", CreatedAt: now}
	snapshot, err := backend.Wait(context.Background(), route, 6)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].Seq != 7 || snapshot.Events[0].Content != "working" || snapshot.NextSeq != 7 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestBackendWaitBoundsSnapshotReadAfterCallerCancellation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()
	tasks := &fakeTaskControl{
		run: &task.TaskRun{ID: "task-1", Status: task.StatusRunning, RequestedAt: now},
		streamFn: func(ctx context.Context, _ string, _ uint64, _ func(task.TaskEvent) error) error {
			return ctx.Err()
		},
		getFn: func(ctx context.Context, _ string) (*task.TaskRun, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("snapshot read context has no deadline")
			}
			if ctx.Err() != nil {
				t.Fatalf("snapshot read inherited caller cancellation: %v", ctx.Err())
			}
			if remaining := time.Until(deadline); remaining <= 0 || remaining > 3*time.Second {
				t.Fatalf("snapshot read deadline is not short and bounded: %v", remaining)
			}
			return tasksRun("task-1", now), nil
		},
	}
	backend, _ := New(Options{Tasks: tasks, BuildSpec: SpecBuilderFunc(func(context.Context, *coordination.Route) (task.Spec, error) { return task.Spec{}, nil })})
	route := &coordination.Route{ID: "delegation-1", BackendRef: "amux-task:task-1", CreatedAt: now}

	snapshot, err := backend.Wait(callerCtx, route, 0)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if snapshot.Delegation.Status != coordination.StatusRunning {
		t.Fatalf("status = %q, want %q", snapshot.Delegation.Status, coordination.StatusRunning)
	}
}

func tasksRun(id string, requestedAt time.Time) *task.TaskRun {
	return &task.TaskRun{ID: id, Status: task.StatusRunning, RequestedAt: requestedAt}
}

type fakeTaskControl struct {
	submitted task.Spec
	run       *task.TaskRun
	events    []task.TaskEvent
	streamFn  func(context.Context, string, uint64, func(task.TaskEvent) error) error
	getFn     func(context.Context, string) (*task.TaskRun, error)
}

func (f *fakeTaskControl) Submit(_ context.Context, spec task.Spec) (*task.TaskRun, error) {
	f.submitted = spec
	return &task.TaskRun{ID: "task-1", Status: task.StatusPending}, nil
}
func (f *fakeTaskControl) Stream(ctx context.Context, id string, fromSeq uint64, handle func(task.TaskEvent) error) error {
	if f.streamFn != nil {
		return f.streamFn(ctx, id, fromSeq, handle)
	}
	for _, event := range f.events {
		if err := handle(event); err != nil {
			if errors.Is(err, errWaitEventReady) {
				return err
			}
			return err
		}
	}
	return nil
}
func (f *fakeTaskControl) Get(ctx context.Context, id string) (*task.TaskRun, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return f.run, nil
}
func (f *fakeTaskControl) Cancel(context.Context, string) error { return nil }
func (f *fakeTaskControl) RespondPermission(context.Context, string, task.PermissionResponse) error {
	return nil
}

func TestBackendWaitSkipsToolAndUsageActivityAndAdvancesCursor(t *testing.T) {
	for _, withOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "activity-only", true: "next-output"}[withOutput], func(t *testing.T) {
			now := time.Now().UTC()
			events := []task.TaskEvent{{Seq: 7, Kind: task.TaskEventTool, Tool: &task.ToolActivity{ID: "t1", Status: "running"}}, {Seq: 8, Kind: task.TaskEventTool, Tool: &task.ToolActivity{ID: "t1", Status: "completed"}}}
			events = append(events, task.TaskEvent{Seq: 9, Kind: task.TaskEventUsage, Usage: []byte(`{"cumulative_input_tokens":343000}`)})
			if withOutput {
				events = append(events, task.TaskEvent{Seq: 10, Kind: task.TaskEventOutput, Output: []byte("working"), At: now})
			}
			backend, _ := New(Options{Tasks: &fakeTaskControl{run: tasksRun("task-1", now), events: events}, BuildSpec: SpecBuilderFunc(func(context.Context, *coordination.Route) (task.Spec, error) { return task.Spec{}, nil })})
			snapshot, err := backend.Wait(context.Background(), &coordination.Route{ID: "delegation-1", BackendRef: "amux-task:task-1", CreatedAt: now}, 6)
			if err != nil {
				t.Fatal(err)
			}
			if withOutput {
				if snapshot.NextSeq != 10 || len(snapshot.Events) != 1 || snapshot.Events[0].Content != "working" {
					t.Fatalf("snapshot = %+v", snapshot)
				}
			} else if snapshot.NextSeq != 9 || len(snapshot.Events) != 0 {
				t.Fatalf("tool-only wait invented event: %+v", snapshot)
			}
		})
	}
}

func TestBackendCreatePreservesOnlyAmuxSessionInitiator(t *testing.T) {
	for _, tc := range []struct{ initiator, parent string }{
		{"amux-session:parent-1", "parent-1"},
		{" amux-session:parent-2 ", "parent-2"},
		{"mcp:main", ""},
		{"feishu:chat-1", ""},
		{"session:other", ""},
	} {
		t.Run(tc.initiator, func(t *testing.T) {
			tasks := &fakeTaskControl{}
			backend, _ := New(Options{Tasks: tasks, BuildSpec: SpecBuilderFunc(func(context.Context, *coordination.Route) (task.Spec, error) {
				return task.Spec{Agent: "builtin/codex"}, nil
			})})
			_, err := backend.Create(context.Background(), &coordination.Route{ID: "delegation-1", InitiatorRef: tc.initiator, Input: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			if tasks.submitted.ParentSessionID != tc.parent || tasks.submitted.ParentTurnID != "" {
				t.Fatalf("spec = %+v", tasks.submitted)
			}
		})
	}
}
