package system

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type manager struct {
	journal JournalStore
	inspect Inspector
	execute Executor
	idGen   func() string
	now     func() time.Time
}

type executionLeaser interface {
	acquireExecutionLease(context.Context) (func(), error)
}

func New(dependencies Dependencies) Lifecycle {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	idGen := dependencies.IDGen
	if idGen == nil {
		idGen = func() string {
			return fmt.Sprintf("op-%d", now().UnixNano())
		}
	}
	return &manager{
		journal: dependencies.Journal,
		inspect: dependencies.Inspect,
		execute: dependencies.Execute,
		idGen:   idGen,
		now:     now,
	}
}

func (m *manager) Inspect(ctx context.Context, scope Scope) (Snapshot, error) {
	if err := validateScope(scope, true); err != nil {
		return Snapshot{}, &ValidationError{Err: err}
	}
	if scope.Kind == ScopeOperations {
		if m.journal == nil {
			return Snapshot{}, errors.New("system journal is required")
		}
		operations, err := m.journal.List(ctx, 0)
		if err != nil {
			return Snapshot{}, fmt.Errorf("list system operations: %w", err)
		}
		snapshot := Snapshot{
			Schema:     SnapshotSchema,
			Scope:      scope,
			State:      StateHealthy,
			Operations: cloneOperations(operations),
		}
		snapshot.ActiveOperation = activeOperation(operations)
		return snapshot, nil
	}
	if m.inspect == nil {
		return Snapshot{}, errors.New("system inspector is required")
	}
	var operations []Operation
	if m.journal != nil {
		var listErr error
		operations, listErr = m.journal.List(ctx, 0)
		if listErr != nil {
			return Snapshot{}, fmt.Errorf("list system operations: %w", listErr)
		}
	}
	snapshot, err := m.inspect(ctx, scope)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect system: %w", err)
	}
	snapshot = cloneSnapshot(snapshot)
	snapshot.Schema = SnapshotSchema
	snapshot.Scope = scope
	if m.journal != nil {
		snapshot.ActiveOperation = activeOperation(operations)
		snapshot.Summary = snapshotSummaryWithOperations(snapshot.Summary, operations)
	}
	return snapshot, nil
}

func (m *manager) Plan(ctx context.Context, request ChangeRequest) (ChangePlan, error) {
	if err := validateChangeRequest(request); err != nil {
		return ChangePlan{}, &ValidationError{Err: err}
	}
	before, err := m.Inspect(ctx, request.Scope)
	if err != nil {
		return ChangePlan{}, err
	}
	id := OperationID(m.idGen())
	if id == "" {
		return ChangePlan{}, errors.New("system operation ID must not be empty")
	}
	plan := ChangePlan{
		OperationID: id,
		Request:     cloneRequest(request),
		Before:      cloneSnapshot(before),
		CreatedAt:   m.now().UTC(),
	}
	integrity, err := changePlanIntegrity(plan)
	if err != nil {
		return ChangePlan{}, fmt.Errorf("seal system change plan: %w", err)
	}
	plan.integrity = integrity
	return plan, nil
}

func (m *manager) Apply(ctx context.Context, plan ChangePlan) (OperationID, error) {
	if err := validatePlan(plan); err != nil {
		return "", &ValidationError{Err: err}
	}
	if m.journal == nil {
		return "", errors.New("system journal is required")
	}
	if m.execute == nil {
		return "", errors.New("system executor is required")
	}

	frozen := clonePlan(plan)
	id := frozen.OperationID
	if leaser, ok := m.journal.(executionLeaser); ok {
		release, err := leaser.acquireExecutionLease(ctx)
		if err != nil {
			return "", fmt.Errorf("acquire system execution lease: %w", err)
		}
		defer release()
	}
	now := m.now().UTC()
	operation := Operation{
		ID:        id,
		Action:    frozen.Request.Action,
		Scope:     frozen.Request.Scope,
		State:     OperationPending,
		CreatedAt: now,
		UpdatedAt: now,
		Plan:      frozen,
	}
	if err := m.journal.Create(ctx, operation); err != nil {
		if !errors.Is(err, ErrDuplicateOperation) {
			return "", fmt.Errorf("create system operation: %w", err)
		}
		existing, getErr := m.journal.Get(ctx, id)
		if getErr != nil {
			return "", errors.Join(
				fmt.Errorf("create system operation: %w", err),
				fmt.Errorf("get duplicate system operation %s: %w", id, getErr),
			)
		}
		if !samePlannedOperation(existing, operation) {
			return "", fmt.Errorf("system operation ID collision: %w", err)
		}
		return duplicateOperationOutcome(existing)
	}
	if err := m.journal.Transition(ctx, id, OperationApplying, ""); err != nil {
		return id, fmt.Errorf("mark system operation applying: %w", err)
	}

	executeErr := m.execute(ctx, clonePlan(frozen))
	terminalCtx := context.WithoutCancel(ctx)
	if executeErr != nil {
		transitionErr := m.journal.Transition(terminalCtx, id, OperationFailed, executeErr.Error())
		if transitionErr != nil {
			return id, errors.Join(executeErr, fmt.Errorf("mark system operation failed: %w", transitionErr))
		}
		return id, executeErr
	}
	if err := m.journal.Transition(terminalCtx, id, OperationSucceeded, ""); err != nil {
		return id, fmt.Errorf("mark system operation succeeded: %w", err)
	}
	return id, nil
}

func duplicateOperationOutcome(operation Operation) (OperationID, error) {
	switch {
	case operation.State == OperationSucceeded:
		return operation.ID, nil
	case isActiveState(operation.State):
		return operation.ID, &OperationInProgressError{
			OperationID: operation.ID,
			State:       operation.State,
		}
	default:
		return operation.ID, &OperationNotSuccessfulError{
			OperationID:    operation.ID,
			State:          operation.State,
			OperationError: operation.Error,
		}
	}
}

func (m *manager) GetOperation(ctx context.Context, id OperationID) (Operation, error) {
	if id == "" {
		return Operation{}, &ValidationError{Err: errors.New("system operation ID must not be empty")}
	}
	if m.journal == nil {
		return Operation{}, errors.New("system journal is required")
	}
	operation, err := m.journal.Get(ctx, id)
	if err != nil {
		return Operation{}, fmt.Errorf("get system operation %s: %w", id, err)
	}
	return cloneOperationValue(operation), nil
}

func (m *manager) Cancel(ctx context.Context, id OperationID) (CancelResult, error) {
	if id == "" {
		return CancelResult{}, &ValidationError{Err: errors.New("system operation ID must not be empty")}
	}
	if m.journal == nil {
		return CancelResult{}, errors.New("system journal is required")
	}
	result, err := m.journal.CancelPending(ctx, id)
	if err != nil {
		return CancelResult{}, fmt.Errorf("cancel system operation %s: %w", id, err)
	}
	return result, nil
}

func validateChangeRequest(request ChangeRequest) error {
	if err := validateScope(request.Scope, false); err != nil {
		return err
	}
	switch request.Action {
	case ActionSetup:
		if request.Setup == nil {
			return errors.New("setup action requires a setup specification")
		}
	case ActionStart, ActionStop, ActionRestart:
		if request.Setup != nil {
			return fmt.Errorf("%s action does not accept a setup specification", request.Action)
		}
	default:
		return fmt.Errorf("unsupported system action %q", request.Action)
	}
	return nil
}

func validateScope(scope Scope, allowOperations bool) error {
	switch scope.Kind {
	case ScopeCurrent:
		return nil
	case ScopeOperations:
		if allowOperations && scope.Target == "" {
			return nil
		}
	}
	return fmt.Errorf("unsupported system scope %q", scope.Kind)
}

func validatePlan(plan ChangePlan) error {
	if plan.OperationID == "" {
		return fmt.Errorf("%w: operation ID is empty", ErrInvalidChangePlan)
	}
	if plan.CreatedAt.IsZero() {
		return fmt.Errorf("%w: creation time is empty", ErrInvalidChangePlan)
	}
	if err := validateChangeRequest(plan.Request); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidChangePlan, err)
	}
	if plan.Before.Schema != SnapshotSchema {
		return fmt.Errorf("%w: unsupported snapshot schema %q", ErrInvalidChangePlan, plan.Before.Schema)
	}
	if plan.Before.Scope != plan.Request.Scope {
		return fmt.Errorf("%w: scope does not match before snapshot", ErrInvalidChangePlan)
	}
	want, err := changePlanIntegrity(plan)
	if err != nil {
		return fmt.Errorf("%w: calculate integrity: %v", ErrInvalidChangePlan, err)
	}
	if subtle.ConstantTimeCompare(plan.integrity[:], want[:]) != 1 {
		return fmt.Errorf("%w: integrity check failed", ErrInvalidChangePlan)
	}
	return nil
}

func changePlanIntegrity(plan ChangePlan) ([32]byte, error) {
	payload := struct {
		OperationID OperationID   `json:"operation_id"`
		Request     ChangeRequest `json:"request"`
		Before      Snapshot      `json:"before"`
		CreatedAt   time.Time     `json:"created_at"`
	}{
		OperationID: plan.OperationID,
		Request:     plan.Request,
		Before:      plan.Before,
		CreatedAt:   plan.CreatedAt,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func samePlannedOperation(existing, candidate Operation) bool {
	if existing.ID != candidate.ID || existing.Action != candidate.Action || existing.Scope != candidate.Scope {
		return false
	}
	existingIntegrity, err := changePlanIntegrity(existing.Plan)
	if err != nil {
		return false
	}
	candidateIntegrity, err := changePlanIntegrity(candidate.Plan)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(existingIntegrity[:], candidateIntegrity[:]) == 1
}

func validOperationTransition(from, to OperationState) bool {
	switch from {
	case OperationPending:
		return to == OperationDraining || to == OperationApplying || to == OperationCancelled || to == OperationFailed
	case OperationDraining:
		return to == OperationStaging || to == OperationCancelled || to == OperationFailed
	case OperationStaging:
		return to == OperationApplying || to == OperationCancelled || to == OperationFailed
	case OperationApplying:
		return to == OperationVerifying || to == OperationSucceeded || to == OperationRolledBack || to == OperationFailed
	case OperationVerifying:
		return to == OperationSucceeded || to == OperationRolledBack || to == OperationFailed
	default:
		return false
	}
}

func activeOperation(operations []Operation) *Operation {
	for _, operation := range operations {
		if isActiveState(operation.State) {
			cloned := cloneOperationValue(operation)
			return &cloned
		}
	}
	return nil
}

func snapshotSummaryWithOperations(summary *SnapshotSummary, operations []Operation) *SnapshotSummary {
	if len(operations) == 0 {
		return summary
	}
	if summary == nil {
		summary = &SnapshotSummary{}
	}
	operationSummary := OperationSummary{}
	if active := activeOperation(operations); active != nil {
		operationSummary.Active = operationBrief(*active)
	}
	for _, operation := range operations {
		if operation.State == OperationFailed || operation.State == OperationRolledBack {
			operationSummary.LastFailed = operationBrief(operation)
			break
		}
	}
	if operationSummary.Active != nil || operationSummary.LastFailed != nil {
		summary.Operations = &operationSummary
	}
	return summary
}

func operationBrief(operation Operation) *OperationBrief {
	return &OperationBrief{
		ID:     operation.ID,
		Action: operation.Action,
		State:  operation.State,
		Error:  operation.Error,
	}
}

func isActiveState(state OperationState) bool {
	switch state {
	case OperationPending, OperationDraining, OperationStaging, OperationApplying, OperationVerifying:
		return true
	default:
		return false
	}
}

func cloneRequest(request ChangeRequest) ChangeRequest {
	if request.Setup != nil {
		setup := *request.Setup
		request.Setup = &setup
	}
	return request
}

func clonePlan(plan ChangePlan) ChangePlan {
	plan.Request = cloneRequest(plan.Request)
	plan.Before = cloneSnapshot(plan.Before)
	return plan
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Summary = cloneSnapshotSummary(snapshot.Summary)
	snapshot.Components = append([]Component(nil), snapshot.Components...)
	snapshot.Checks = append([]Check(nil), snapshot.Checks...)
	snapshot.Operations = cloneOperations(snapshot.Operations)
	snapshot.NextActions = append([]SuggestedAction(nil), snapshot.NextActions...)
	if snapshot.ActiveOperation != nil {
		operation := cloneOperationValue(*snapshot.ActiveOperation)
		snapshot.ActiveOperation = &operation
	}
	return snapshot
}

func cloneSnapshotSummary(summary *SnapshotSummary) *SnapshotSummary {
	if summary == nil {
		return nil
	}
	cloned := *summary
	if summary.Installation != nil {
		installation := *summary.Installation
		cloned.Installation = &installation
	}
	if summary.Workloads != nil {
		workloads := *summary.Workloads
		workloads.LocalSessions.Agents = cloneStringIntMap(workloads.LocalSessions.Agents)
		workloads.TargetSessions.Agents = cloneStringIntMap(workloads.TargetSessions.Agents)
		cloned.Workloads = &workloads
	}
	if summary.Integrations != nil {
		integrations := *summary.Integrations
		cloned.Integrations = &integrations
	}
	if summary.Operations != nil {
		operations := *summary.Operations
		if summary.Operations.Active != nil {
			active := *summary.Operations.Active
			operations.Active = &active
		}
		if summary.Operations.LastFailed != nil {
			lastFailed := *summary.Operations.LastFailed
			operations.LastFailed = &lastFailed
		}
		cloned.Operations = &operations
	}
	return &cloned
}

func cloneStringIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneOperations(operations []Operation) []Operation {
	if operations == nil {
		return nil
	}
	cloned := make([]Operation, len(operations))
	for i, operation := range operations {
		cloned[i] = cloneOperationValue(operation)
	}
	return cloned
}

func cloneOperationValue(operation Operation) Operation {
	operation.Plan = clonePlan(operation.Plan)
	operation.NextActions = append([]SuggestedAction(nil), operation.NextActions...)
	if operation.FinishedAt != nil {
		finishedAt := *operation.FinishedAt
		operation.FinishedAt = &finishedAt
	}
	return operation
}
