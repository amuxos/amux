package system

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerPlanFreezesInputsAndApplyUsesFrozenPlan(t *testing.T) {
	journal := newMemoryJournal()
	setup := &SetupSpec{
		Profile:    "remote",
		Agent:      "builtin/codex",
		HubAddress: "hub.example.com:7100",
		NoStart:    true,
	}
	request := ChangeRequest{Action: ActionSetup, Scope: CurrentScope(), Setup: setup}
	snapshot := Snapshot{
		State:      StateHealthy,
		Components: []Component{{Name: "hub", State: "reachable"}},
	}
	var executed ChangePlan
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return snapshot, nil
		},
		Execute: func(_ context.Context, plan ChangePlan) error {
			executed = plan
			return nil
		},
		IDGen: func() string { return "op-1" },
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	})

	plan, err := lifecycle.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	setup.Profile = "local"
	setup.Agent = "builtin/claude"
	setup.HubAddress = "127.0.0.1:7100"
	setup.NoStart = false
	snapshot.Components[0].State = "stopped"

	id, err := lifecycle.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if id != "op-1" {
		t.Fatalf("id = %q", id)
	}
	if plan.Request.Setup == setup {
		t.Fatal("plan retained caller's setup pointer")
	}
	if got := *plan.Request.Setup; got != (SetupSpec{
		Profile:    "remote",
		Agent:      "builtin/codex",
		HubAddress: "hub.example.com:7100",
		NoStart:    true,
	}) {
		t.Fatalf("plan setup = %#v", got)
	}
	if got := plan.Before.Components[0].State; got != "reachable" {
		t.Fatalf("plan component state = %q", got)
	}
	if !reflect.DeepEqual(executed, plan) {
		t.Fatalf("executor plan = %#v, want %#v", executed, plan)
	}
	op, err := journal.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op.Plan, plan) {
		t.Fatalf("journal plan = %#v, want %#v", op.Plan, plan)
	}
}

func TestManagerPlanReturnsTypedValidationError(t *testing.T) {
	lifecycle := newTestLifecycle(newMemoryJournal(), "unused")

	_, err := lifecycle.Plan(context.Background(), ChangeRequest{
		Action: Action("upgrade"),
		Scope:  CurrentScope(),
	})
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Plan() error = %T %v, want ValidationError", err, err)
	}
	if validation.ExitCode() != 2 {
		t.Fatalf("ValidationError.ExitCode() = %d, want 2", validation.ExitCode())
	}
	if !strings.Contains(err.Error(), `unsupported system action "upgrade"`) {
		t.Fatalf("Plan() error = %q, want unsupported action", err)
	}
}

func TestManagerPublicInputValidationUsesValidationError(t *testing.T) {
	lifecycle := newTestLifecycle(newMemoryJournal(), "unused")
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "inspect invalid scope",
			call: func() error {
				_, err := lifecycle.Inspect(context.Background(), Scope{Kind: ScopeKind("remote")})
				return err
			},
		},
		{
			name: "get empty operation ID",
			call: func() error {
				_, err := lifecycle.GetOperation(context.Background(), "")
				return err
			},
		},
		{
			name: "cancel empty operation ID",
			call: func() error {
				_, err := lifecycle.Cancel(context.Background(), "")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("error = %T %v, want ValidationError", err, err)
			}
			if validation.ExitCode() != 2 {
				t.Fatalf("ValidationError.ExitCode() = %d, want 2", validation.ExitCode())
			}
		})
	}
}

func TestManagerApplyPersistsTerminalState(t *testing.T) {
	journal := newMemoryJournal()
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error { return nil },
		IDGen:   func() string { return "op-1" },
		Now:     func() time.Time { return time.Unix(100, 0).UTC() },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := lifecycle.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	op, err := lifecycle.GetOperation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != OperationSucceeded {
		t.Fatalf("state = %s", op.State)
	}
	if op.Action != ActionRestart || op.Scope != CurrentScope() {
		t.Fatalf("operation request = %s %#v", op.Action, op.Scope)
	}
}

func TestManagerInspectPreservesNextActions(t *testing.T) {
	journal := newMemoryJournal()
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{
				State: StateDegraded,
				NextActions: []SuggestedAction{
					{
						Command:              "amux system start",
						Reason:               "hub is not reachable",
						RequiresConfirmation: false,
					},
				},
			}, nil
		},
		Execute: func(context.Context, ChangePlan) error { return nil },
		IDGen:   func() string { return "op-1" },
		Now:     func() time.Time { return time.Unix(100, 0).UTC() },
	})

	snapshot, err := lifecycle.Inspect(context.Background(), CurrentScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.NextActions) != 1 || snapshot.NextActions[0].Command != "amux system start" {
		t.Fatalf("next actions = %#v", snapshot.NextActions)
	}
}

func TestManagerApplyPersistsExecutorFailureAndReturnsID(t *testing.T) {
	journal := newMemoryJournal()
	executeErr := errors.New("restart failed")
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error { return executeErr },
		IDGen:   func() string { return "op-failed" },
		Now:     func() time.Time { return time.Unix(100, 0).UTC() },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}

	id, err := lifecycle.Apply(context.Background(), plan)
	if !errors.Is(err, executeErr) {
		t.Fatalf("error = %v, want %v", err, executeErr)
	}
	if id != "op-failed" {
		t.Fatalf("id = %q", id)
	}
	op, getErr := lifecycle.GetOperation(context.Background(), id)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if op.State != OperationFailed {
		t.Fatalf("state = %s", op.State)
	}
	if op.Error != executeErr.Error() {
		t.Fatalf("operation error = %q", op.Error)
	}
}

func TestManagerApplyPersistsFailureAfterCallerCancellation(t *testing.T) {
	journal := newMemoryJournal()
	executeErr := errors.New("restart failed after cancellation")
	ctx, cancel := context.WithCancel(context.Background())
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			cancel()
			return executeErr
		},
		IDGen: func() string { return "op-cancelled-context" },
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}

	id, err := lifecycle.Apply(ctx, plan)
	if !errors.Is(err, executeErr) {
		t.Fatalf("error = %v, want %v", err, executeErr)
	}
	op, getErr := lifecycle.GetOperation(context.Background(), id)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if op.State != OperationFailed {
		t.Fatalf("state = %s, want %s", op.State, OperationFailed)
	}
	if op.Error != executeErr.Error() {
		t.Fatalf("operation error = %q", op.Error)
	}
}

func TestManagerApplyRejectsMutatedAndForgedPlans(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ChangePlan)
	}{
		{name: "operation ID", mutate: func(plan *ChangePlan) { plan.OperationID = "forged-id" }},
		{name: "request", mutate: func(plan *ChangePlan) { plan.Request.Action = ActionStop }},
		{name: "before snapshot", mutate: func(plan *ChangePlan) { plan.Before.State = StateFailed }},
		{name: "creation time", mutate: func(plan *ChangePlan) { plan.CreatedAt = plan.CreatedAt.Add(time.Second) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			journal := newMemoryJournal()
			executions := 0
			lifecycle := New(Dependencies{
				Journal: journal,
				Inspect: func(context.Context, Scope) (Snapshot, error) {
					return Snapshot{State: StateHealthy}, nil
				},
				Execute: func(context.Context, ChangePlan) error {
					executions++
					return nil
				},
				IDGen: func() string { return "op-sealed" },
				Now:   func() time.Time { return time.Unix(100, 0).UTC() },
			})
			plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&plan)

			if id, err := lifecycle.Apply(context.Background(), plan); err == nil || id != "" {
				t.Fatalf("Apply() = %q, %v; want empty ID and integrity error", id, err)
			}
			if executions != 0 {
				t.Fatalf("executions = %d", executions)
			}
		})
	}

	journal := newMemoryJournal()
	executions := 0
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			executions++
			return nil
		},
		IDGen: func() string { return "unused" },
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	})
	forged := ChangePlan{
		OperationID: "op-forged",
		Request:     ChangeRequest{Action: ActionRestart, Scope: CurrentScope()},
		Before:      Snapshot{Schema: SnapshotSchema, Scope: CurrentScope(), State: StateHealthy},
		CreatedAt:   time.Unix(100, 0).UTC(),
	}
	if id, err := lifecycle.Apply(context.Background(), forged); err == nil || id != "" {
		t.Fatalf("Apply(forged) = %q, %v; want empty ID and integrity error", id, err)
	}
	if executions != 0 {
		t.Fatalf("forged plan executions = %d", executions)
	}
}

func TestManagerApplyReturnsTypedValidationErrorForInvalidPlan(t *testing.T) {
	lifecycle := newTestLifecycle(newMemoryJournal(), "unused")

	_, err := lifecycle.Apply(context.Background(), ChangePlan{})
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Apply() error = %T %v, want ValidationError", err, err)
	}
	if !errors.Is(err, ErrInvalidChangePlan) {
		t.Fatalf("Apply() error = %v, want ErrInvalidChangePlan in chain", err)
	}
	if validation.ExitCode() != 2 {
		t.Fatalf("ValidationError.ExitCode() = %d, want 2", validation.ExitCode())
	}
}

func TestManagerPlanFreezesOperationIDAndRepeatApplyIsIdempotent(t *testing.T) {
	journal := newMemoryJournal()
	idCalls := 0
	executions := 0
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			executions++
			return nil
		},
		IDGen: func() string {
			idCalls++
			return "op-idempotent"
		},
		Now: func() time.Time { return time.Unix(100, 0).UTC() },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	if plan.OperationID != "op-idempotent" {
		t.Fatalf("plan operation ID = %q", plan.OperationID)
	}

	for i := 0; i < 2; i++ {
		id, err := lifecycle.Apply(context.Background(), plan)
		if err != nil {
			t.Fatalf("Apply %d: %v", i+1, err)
		}
		if id != plan.OperationID {
			t.Fatalf("Apply %d ID = %q, want %q", i+1, id, plan.OperationID)
		}
	}
	if idCalls != 1 {
		t.Fatalf("ID generator calls = %d, want 1", idCalls)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want 1", executions)
	}
}

func TestManagerConcurrentRepeatApplyExecutesOnce(t *testing.T) {
	journal := newMemoryJournal()
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			if executions.Add(1) == 1 {
				close(started)
			}
			<-release
			return nil
		},
		IDGen: func() string { return "op-concurrent" },
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		id  OperationID
		err error
	}
	first := make(chan result, 1)
	go func() {
		id, applyErr := lifecycle.Apply(context.Background(), plan)
		first <- result{id: id, err: applyErr}
	}()
	<-started
	secondID, secondErr := lifecycle.Apply(context.Background(), plan)
	close(release)
	firstResult := <-first

	if firstResult.err != nil {
		t.Fatalf("first Apply error = %v", firstResult.err)
	}
	if secondErr == nil {
		t.Fatal("second Apply error = nil, want in-progress error")
	}
	var inProgress *OperationInProgressError
	if !errors.As(secondErr, &inProgress) {
		t.Fatalf("second Apply error = %T %v, want OperationInProgressError", secondErr, secondErr)
	}
	if !errors.Is(secondErr, ErrOperationInProgress) {
		t.Fatalf("second Apply error = %v, want ErrOperationInProgress", secondErr)
	}
	if inProgress.OperationID != plan.OperationID || inProgress.State != OperationApplying {
		t.Fatalf("in-progress error = %#v", inProgress)
	}
	if firstResult.id != plan.OperationID || secondID != plan.OperationID {
		t.Fatalf("Apply IDs = %q, %q; want %q", firstResult.id, secondID, plan.OperationID)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

func TestManagerDuplicateActiveOperationReturnsInProgress(t *testing.T) {
	states := []OperationState{
		OperationPending,
		OperationDraining,
		OperationStaging,
		OperationApplying,
		OperationVerifying,
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			journal := newMemoryJournal()
			executions := 0
			lifecycle := New(Dependencies{
				Journal: journal,
				Inspect: func(context.Context, Scope) (Snapshot, error) {
					return Snapshot{State: StateHealthy}, nil
				},
				Execute: func(context.Context, ChangePlan) error {
					executions++
					return nil
				},
				IDGen: func() string { return "op-active-duplicate" },
				Now:   func() time.Time { return time.Unix(100, 0).UTC() },
			})
			plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Create(context.Background(), operationFromPlan(plan, state, "")); err != nil {
				t.Fatal(err)
			}

			id, err := lifecycle.Apply(context.Background(), plan)
			if id != plan.OperationID {
				t.Fatalf("id = %q, want %q", id, plan.OperationID)
			}
			var inProgress *OperationInProgressError
			if !errors.As(err, &inProgress) || !errors.Is(err, ErrOperationInProgress) {
				t.Fatalf("error = %T %v, want OperationInProgressError", err, err)
			}
			if inProgress.OperationID != plan.OperationID || inProgress.State != state {
				t.Fatalf("in-progress error = %#v", inProgress)
			}
			if executions != 0 {
				t.Fatalf("executions = %d", executions)
			}
		})
	}
}

func TestManagerDuplicateTerminalNotSuccessPreservesOutcome(t *testing.T) {
	tests := []struct {
		state          OperationState
		operationError string
	}{
		{state: OperationFailed, operationError: "executor failed"},
		{state: OperationRolledBack},
		{state: OperationCancelled},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			journal := newMemoryJournal()
			executions := 0
			lifecycle := New(Dependencies{
				Journal: journal,
				Inspect: func(context.Context, Scope) (Snapshot, error) {
					return Snapshot{State: StateHealthy}, nil
				},
				Execute: func(context.Context, ChangePlan) error {
					executions++
					return nil
				},
				IDGen: func() string { return "op-terminal-duplicate" },
				Now:   func() time.Time { return time.Unix(100, 0).UTC() },
			})
			plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Create(context.Background(), operationFromPlan(plan, tt.state, tt.operationError)); err != nil {
				t.Fatal(err)
			}

			id, err := lifecycle.Apply(context.Background(), plan)
			if id != plan.OperationID {
				t.Fatalf("id = %q, want %q", id, plan.OperationID)
			}
			var notSuccessful *OperationNotSuccessfulError
			if !errors.As(err, &notSuccessful) || !errors.Is(err, ErrOperationNotSuccessful) {
				t.Fatalf("error = %T %v, want OperationNotSuccessfulError", err, err)
			}
			if notSuccessful.OperationID != plan.OperationID || notSuccessful.State != tt.state {
				t.Fatalf("terminal error = %#v", notSuccessful)
			}
			if notSuccessful.OperationError != tt.operationError {
				t.Fatalf("operation error = %q, want %q", notSuccessful.OperationError, tt.operationError)
			}
			if executions != 0 {
				t.Fatalf("executions = %d", executions)
			}
		})
	}
}

func TestManagerDuplicateIDWithDifferentPlanIsNotIdempotent(t *testing.T) {
	journal := newMemoryJournal()
	first := newTestLifecycle(journal, "op-collision")
	firstPlan, err := first.Plan(context.Background(), ChangeRequest{Action: ActionStart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Apply(context.Background(), firstPlan); err != nil {
		t.Fatal(err)
	}

	executions := 0
	second := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			executions++
			return nil
		},
		IDGen: func() string { return "op-collision" },
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	})
	secondPlan, err := second.Plan(context.Background(), ChangeRequest{Action: ActionStop, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := second.Apply(context.Background(), secondPlan); err == nil || id != "" {
		t.Fatalf("Apply(collision) = %q, %v; want non-idempotent duplicate error", id, err)
	}
	if executions != 0 {
		t.Fatalf("executions = %d", executions)
	}
}

func TestManagerCancelWinsBeforeApplyingTransition(t *testing.T) {
	journal := newMemoryJournal()
	executions := 0
	var lifecycle Lifecycle
	journal.beforeTransition = func(id OperationID, state OperationState) {
		if state != OperationApplying {
			return
		}
		result, err := lifecycle.Cancel(context.Background(), id)
		if err != nil {
			t.Errorf("Cancel: %v", err)
			return
		}
		if !result.Accepted {
			t.Errorf("Cancel accepted = false")
		}
	}
	lifecycle = New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			executions++
			return nil
		},
		IDGen: func() string { return "op-cancel-race" },
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionStop, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}

	id, err := lifecycle.Apply(context.Background(), plan)
	if id != plan.OperationID {
		t.Fatalf("id = %q, want %q", id, plan.OperationID)
	}
	if !errors.Is(err, ErrInvalidOperationTransition) {
		t.Fatalf("error = %v, want ErrInvalidOperationTransition", err)
	}
	if executions != 0 {
		t.Fatalf("executions = %d, want 0", executions)
	}
	op, getErr := lifecycle.GetOperation(context.Background(), id)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if op.State != OperationCancelled {
		t.Fatalf("state = %s, want %s", op.State, OperationCancelled)
	}
}

func TestManagerApplyOrdersJournalAndExecutor(t *testing.T) {
	journal := newMemoryJournal()
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			journal.recordEvent("execute")
			return nil
		},
		IDGen: func() string { return "op-order" },
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}

	want := []string{"pending", "applying", "execute", "succeeded"}
	if got := journal.recordedEvents(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestOperationStateValuesAndJSON(t *testing.T) {
	tests := []struct {
		state OperationState
		want  string
	}{
		{OperationPending, "pending"},
		{OperationDraining, "draining"},
		{OperationStaging, "staging"},
		{OperationApplying, "applying"},
		{OperationVerifying, "verifying"},
		{OperationSucceeded, "succeeded"},
		{OperationRolledBack, "rolled_back"},
		{OperationFailed, "failed"},
		{OperationCancelled, "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.state) != tt.want {
				t.Fatalf("state = %q, want %q", tt.state, tt.want)
			}
			encoded, err := json.Marshal(tt.state)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := string(encoded), `"`+tt.want+`"`; got != want {
				t.Fatalf("JSON = %s, want %s", got, want)
			}
		})
	}

	plan := ChangePlan{OperationID: "op-json"}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"operation_id":"op-json"`) {
		t.Fatalf("plan JSON = %s", encoded)
	}
	if strings.Contains(string(encoded), "seal") {
		t.Fatalf("plan JSON exposes integrity seal: %s", encoded)
	}
}

func TestManagerApplyReturnsActiveConflict(t *testing.T) {
	journal := newMemoryJournal()
	if err := journal.Create(context.Background(), Operation{ID: "op-active", State: OperationPending}); err != nil {
		t.Fatal(err)
	}
	lifecycle := newTestLifecycle(journal, "op-new")
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionStop, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}

	id, err := lifecycle.Apply(context.Background(), plan)
	if id != "" {
		t.Fatalf("id = %q", id)
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %T %v, want ConflictError", err, err)
	}
	if conflict.ExistingOperationID != "op-active" {
		t.Fatalf("existing operation = %q", conflict.ExistingOperationID)
	}
	if conflict.ExitCode() != 3 {
		t.Fatalf("ConflictError.ExitCode() = %d, want 3", conflict.ExitCode())
	}
}

func TestManagerCancelPendingAndRejectApplying(t *testing.T) {
	tests := []struct {
		name      string
		state     OperationState
		accepted  bool
		wantState OperationState
	}{
		{name: "pending", state: OperationPending, accepted: true, wantState: OperationCancelled},
		{name: "applying", state: OperationApplying, accepted: false, wantState: OperationApplying},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			journal := newMemoryJournal()
			if err := journal.Create(context.Background(), Operation{ID: "op-1", State: tt.state}); err != nil {
				t.Fatal(err)
			}
			result, err := newTestLifecycle(journal, "unused").Cancel(context.Background(), "op-1")
			if err != nil {
				t.Fatal(err)
			}
			if result.Accepted != tt.accepted {
				t.Fatalf("accepted = %v", result.Accepted)
			}
			op, err := journal.Get(context.Background(), "op-1")
			if err != nil {
				t.Fatal(err)
			}
			if op.State != tt.wantState {
				t.Fatalf("state = %s", op.State)
			}
		})
	}
}

func TestManagerInspectOperationsUsesJournalOnly(t *testing.T) {
	journal := newMemoryJournal()
	if err := journal.Create(context.Background(), Operation{ID: "op-1", State: OperationSucceeded}); err != nil {
		t.Fatal(err)
	}
	inspectorCalled := false
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			inspectorCalled = true
			return Snapshot{}, nil
		},
		Execute: func(context.Context, ChangePlan) error { return nil },
		IDGen:   func() string { return "unused" },
		Now:     func() time.Time { return time.Unix(100, 0).UTC() },
	})

	snapshot, err := lifecycle.Inspect(context.Background(), Scope{Kind: ScopeOperations})
	if err != nil {
		t.Fatal(err)
	}
	if inspectorCalled {
		t.Fatal("host inspector was called for operations scope")
	}
	if snapshot.Schema != SnapshotSchema || snapshot.Scope.Kind != ScopeOperations {
		t.Fatalf("snapshot metadata = %q %#v", snapshot.Schema, snapshot.Scope)
	}
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-1" {
		t.Fatalf("operations = %#v", snapshot.Operations)
	}
}

func TestManagerInspectCurrentSnapshotsJournalBeforeInspectorConsumesDeadline(t *testing.T) {
	journal := newMemoryJournal()
	if err := journal.Create(context.Background(), Operation{
		ID:        "op-active",
		Action:    ActionStart,
		Scope:     CurrentScope(),
		State:     OperationApplying,
		CreatedAt: time.Unix(100, 0).UTC(),
		UpdatedAt: time.Unix(100, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(ctx context.Context, _ Scope) (Snapshot, error) {
			<-ctx.Done()
			return Snapshot{State: StateDegraded}, nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	snapshot, err := lifecycle.Inspect(ctx, CurrentScope())
	if err != nil {
		t.Fatalf("Inspect() after host inspector consumed deadline: %v", err)
	}
	if snapshot.Schema != SnapshotSchema || snapshot.Scope != CurrentScope() || snapshot.State != StateDegraded {
		t.Fatalf("snapshot metadata = schema %q scope %#v state %q", snapshot.Schema, snapshot.Scope, snapshot.State)
	}
	if snapshot.ActiveOperation == nil || snapshot.ActiveOperation.ID != "op-active" {
		t.Fatalf("active operation = %#v, want pre-inspection journal snapshot", snapshot.ActiveOperation)
	}
}

func newTestLifecycle(journal JournalStore, id string) Lifecycle {
	return New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error { return nil },
		IDGen:   func() string { return id },
		Now:     func() time.Time { return time.Unix(100, 0).UTC() },
	})
}

func operationFromPlan(plan ChangePlan, state OperationState, operationError string) Operation {
	return Operation{
		ID:        plan.OperationID,
		Action:    plan.Request.Action,
		Scope:     plan.Request.Scope,
		State:     state,
		Error:     operationError,
		CreatedAt: plan.CreatedAt,
		UpdatedAt: plan.CreatedAt,
		Plan:      clonePlan(plan),
	}
}

type memoryJournal struct {
	mu               sync.Mutex
	operations       map[OperationID]Operation
	beforeTransition func(OperationID, OperationState)
	events           []string
}

func newMemoryJournal() *memoryJournal {
	return &memoryJournal{operations: make(map[OperationID]Operation)}
}

func (j *memoryJournal) Create(ctx context.Context, operation Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, exists := j.operations[operation.ID]; exists {
		return &DuplicateOperationError{OperationID: operation.ID}
	}
	for _, existing := range j.operations {
		if isActiveOperation(existing.State) {
			return &ConflictError{ExistingOperationID: existing.ID}
		}
	}
	j.operations[operation.ID] = cloneOperation(operation)
	j.events = append(j.events, string(operation.State))
	return nil
}

func (j *memoryJournal) Get(ctx context.Context, id OperationID) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	operation, ok := j.operations[id]
	if !ok {
		return Operation{}, errors.New("operation not found")
	}
	return cloneOperation(operation), nil
}

func (j *memoryJournal) List(ctx context.Context, limit int) ([]Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	operations := make([]Operation, 0, len(j.operations))
	for _, operation := range j.operations {
		operations = append(operations, cloneOperation(operation))
	}
	sort.Slice(operations, func(i, k int) bool {
		return operations[i].CreatedAt.After(operations[k].CreatedAt)
	})
	if limit > 0 && len(operations) > limit {
		operations = operations[:limit]
	}
	return operations, nil
}

func (j *memoryJournal) Transition(ctx context.Context, id OperationID, state OperationState, message string) error {
	if j.beforeTransition != nil {
		j.beforeTransition(id, state)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	operation, ok := j.operations[id]
	if !ok {
		return errors.New("operation not found")
	}
	if !validOperationTransition(operation.State, state) {
		return &InvalidTransitionError{OperationID: id, From: operation.State, To: state}
	}
	operation.State = state
	operation.Error = message
	j.operations[id] = operation
	j.events = append(j.events, string(state))
	return nil
}

func (j *memoryJournal) CancelPending(ctx context.Context, id OperationID) (CancelResult, error) {
	if err := ctx.Err(); err != nil {
		return CancelResult{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	operation, ok := j.operations[id]
	if !ok {
		return CancelResult{}, errors.New("operation not found")
	}
	result := CancelResult{OperationID: id, State: operation.State}
	if operation.State != OperationPending {
		result.Reason = "operation is no longer pending"
		return result, nil
	}
	operation.State = OperationCancelled
	j.operations[id] = operation
	j.events = append(j.events, string(OperationCancelled))
	result.Accepted = true
	result.State = OperationCancelled
	return result, nil
}

func (j *memoryJournal) recordEvent(event string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, event)
}

func (j *memoryJournal) recordedEvents() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.events...)
}

func isActiveOperation(state OperationState) bool {
	switch state {
	case OperationPending, OperationDraining, OperationStaging, OperationApplying, OperationVerifying:
		return true
	default:
		return false
	}
}

func cloneOperation(operation Operation) Operation {
	operation.Plan = clonePlan(operation.Plan)
	return operation
}
