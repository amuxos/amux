// Package amuxadapter maps Coordination routes onto the existing Task control
// plane without owning or copying Task lifecycle state.
package amuxadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/coordination"
	"github.com/amuxos/amux/internal/task"
)

const (
	internalTaskPrefix      = "amux-task:"
	waitSnapshotReadTimeout = 2 * time.Second
)

type TaskControl interface {
	Submit(context.Context, task.Spec) (*task.TaskRun, error)
	Stream(context.Context, string, uint64, func(task.TaskEvent) error) error
	Get(context.Context, string) (*task.TaskRun, error)
	Cancel(context.Context, string) error
	RespondPermission(context.Context, string, task.PermissionResponse) error
}

type SpecBuilder interface {
	BuildTaskSpec(context.Context, *coordination.Route) (task.Spec, error)
}

type SpecBuilderFunc func(context.Context, *coordination.Route) (task.Spec, error)

func (f SpecBuilderFunc) BuildTaskSpec(ctx context.Context, route *coordination.Route) (task.Spec, error) {
	return f(ctx, route)
}

type Options struct {
	Tasks     TaskControl
	BuildSpec SpecBuilder
	Now       func() time.Time
}

// Backend is a stateless projection over TaskControl.
type Backend struct {
	tasks     TaskControl
	buildSpec SpecBuilder
	now       func() time.Time
}

func New(options Options) (*Backend, error) {
	if options.Tasks == nil {
		return nil, errors.New("task control is required")
	}
	if options.BuildSpec == nil {
		return nil, errors.New("task spec builder is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Backend{tasks: options.Tasks, buildSpec: options.BuildSpec, now: options.Now}, nil
}

// Create submits the backend Task and returns its opaque route reference.
func (b *Backend) Create(ctx context.Context, route *coordination.Route) (string, error) {
	if route == nil {
		return "", errors.New("delegation route is required")
	}
	spec, err := b.buildSpec.BuildTaskSpec(ctx, route)
	if err != nil {
		return "", fmt.Errorf("resolve internal collaborator: %w", err)
	}
	// Preserve the initiating session through the existing Task admission path.
	// TaskRouter validates that parent and resolves its active turn; the adapter
	// does not invent identity/turn metadata or bypass execution authorization.
	if sessionID, ok := strings.CutPrefix(strings.TrimSpace(route.InitiatorRef), "amux-session:"); ok {
		spec.ParentSessionID = strings.TrimSpace(sessionID)
	}
	spec.Task = route.Input
	spec.Mode = task.ModeEphemeral
	spec.PermissionPolicy = task.PolicyEscalateToMain
	spec.ResultGranularity = task.GranularityStructured
	if !route.Deadline.IsZero() {
		remaining := route.Deadline.Sub(b.now().UTC())
		if remaining < time.Second {
			remaining = time.Second
		}
		spec.TimeoutSeconds = int(math.Ceil(remaining.Seconds()))
	}
	run, err := b.tasks.Submit(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("submit internal task: %w", err)
	}
	if run == nil || strings.TrimSpace(run.ID) == "" {
		return "", errors.New("submit internal task returned no id")
	}
	return internalTaskPrefix + run.ID, nil
}

// Get reads current TaskRun truth. Event replay is provided by Wait.
func (b *Backend) Get(ctx context.Context, route *coordination.Route) (*coordination.Snapshot, error) {
	taskID, err := taskID(route)
	if err != nil {
		return nil, err
	}
	run, err := b.tasks.Get(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get internal task: %w", err)
	}
	return snapshotFromRun(route, run, nil, 0)
}

var errWaitEventReady = errors.New("coordination wait event ready")

// Wait blocks on the Task event stream until one event arrives, the Task
// stream closes, or ctx ends. TaskEvent.Seq remains the public resume cursor.
func (b *Backend) Wait(ctx context.Context, route *coordination.Route, fromSeq uint64) (*coordination.Snapshot, error) {
	taskID, err := taskID(route)
	if err != nil {
		return nil, err
	}
	events := make([]coordination.Event, 0, 1)
	lastSeq := fromSeq
	err = b.tasks.Stream(ctx, taskID, fromSeq, func(event task.TaskEvent) error {
		lastSeq = event.Seq
		if event.Kind == task.TaskEventTool || event.Kind == task.TaskEventUsage {
			return nil
		}
		events = append(events, projectEvent(route, event))
		return errWaitEventReady
	})
	if err != nil && !errors.Is(err, errWaitEventReady) && ctx.Err() == nil {
		return nil, fmt.Errorf("stream internal task: %w", err)
	}
	readCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), waitSnapshotReadTimeout)
		defer cancel()
	}
	run, getErr := b.tasks.Get(readCtx, taskID)
	if getErr != nil {
		return nil, fmt.Errorf("get internal task after wait: %w", getErr)
	}
	return snapshotFromRun(route, run, events, lastSeq)
}

func (b *Backend) RespondPermission(ctx context.Context, route *coordination.Route, response coordination.PermissionResponse) error {
	if strings.TrimSpace(response.RequestID) == "" {
		return errors.New("permission request id is required")
	}
	if response.Decision != coordination.PermissionAllow && response.Decision != coordination.PermissionDeny {
		return fmt.Errorf("unsupported permission decision %q", response.Decision)
	}
	id, err := taskID(route)
	if err != nil {
		return err
	}
	return b.tasks.RespondPermission(ctx, id, task.PermissionResponse{
		PermissionId: response.RequestID,
		Allow:        response.Decision == coordination.PermissionAllow,
		OptionId:     response.OptionID,
		Reason:       response.Reason,
	})
}

func (b *Backend) Cancel(ctx context.Context, route *coordination.Route) error {
	id, err := taskID(route)
	if err != nil {
		return err
	}
	return b.tasks.Cancel(ctx, id)
}

func taskID(route *coordination.Route) (string, error) {
	if route == nil {
		return "", errors.New("delegation route is required")
	}
	if !strings.HasPrefix(route.BackendRef, internalTaskPrefix) {
		return "", fmt.Errorf("invalid internal task backend ref %q", route.BackendRef)
	}
	id := strings.TrimSpace(strings.TrimPrefix(route.BackendRef, internalTaskPrefix))
	if id == "" {
		return "", errors.New("internal task backend ref has no task id")
	}
	return id, nil
}

func snapshotFromRun(route *coordination.Route, run *task.TaskRun, events []coordination.Event, fromSeq uint64) (*coordination.Snapshot, error) {
	if run == nil {
		return nil, errors.New("internal task returned no run")
	}
	delegation := &coordination.Delegation{
		ID:                 route.ID,
		Direction:          coordination.DirectionOutbound,
		InitiatorRef:       route.InitiatorRef,
		TargetRef:          route.TargetRef,
		ScopeRef:           route.ScopeRef,
		ParentDelegationID: route.ParentDelegationID,
		HopCount:           route.HopCount,
		HopBudget:          route.HopBudget,
		Input:              route.Input,
		Status:             projectStatus(run.Status),
		Deadline:           route.Deadline,
		ResultTrustLevel:   route.TrustLevel,
		IdempotencyKey:     route.IdempotencyKey,
		CreatedAt:          route.CreatedAt,
		UpdatedAt:          run.RequestedAt,
		FinishedAt:         run.FinishedAt,
	}
	if delegation.UpdatedAt.IsZero() {
		delegation.UpdatedAt = route.CreatedAt
	}
	if run.Result != nil {
		result, err := projectResult(route.ID, run.Result)
		if err != nil {
			return nil, err
		}
		delegation.Result = &result
	}
	if run.Status == task.StatusFailed {
		delegation.Failure = &coordination.Failure{Code: coordination.FailureBackendFailed, Message: run.Error}
	}
	if run.Status == task.StatusTimedOut {
		delegation.Failure = &coordination.Failure{Code: coordination.FailureDeadlineExceeded, Message: run.Error}
	}
	next := fromSeq
	for _, event := range events {
		if event.Seq > next {
			next = event.Seq
		}
	}
	return &coordination.Snapshot{Delegation: delegation, Events: events, NextSeq: next}, nil
}

func projectEvent(route *coordination.Route, event task.TaskEvent) coordination.Event {
	projected := coordination.Event{DelegationID: route.ID, Seq: event.Seq, At: event.At, Status: projectStatus(event.Status)}
	switch event.Kind {
	case task.TaskEventOutput:
		projected.Kind = coordination.EventOutput
		projected.Content = string(event.Output)
	case task.TaskEventPermission:
		projected.Kind = coordination.EventPermissionRequested
		projected.Status = coordination.StatusWaiting
		if event.Permission != nil {
			projected.Permission = &coordination.PermissionRequest{
				ID: event.Permission.PermissionId, Summary: permissionSummary(event.Permission),
				Tool: event.Permission.Tool, Command: event.Permission.Command,
				Paths: append([]string(nil), event.Permission.Paths...), Write: event.Permission.Write, Network: event.Permission.Network,
			}
		}
	case task.TaskEventResult:
		projected.Kind = coordination.EventResult
		if event.Result != nil {
			result, err := projectResult(route.ID, event.Result)
			if err == nil {
				projected.Result = &result
			}
		}
		if event.Status == task.StatusFailed {
			projected.Kind = coordination.EventError
			projected.Failure = &coordination.Failure{Code: coordination.FailureBackendFailed, Message: resultSummary(event.Result)}
		}
	default:
		projected.Kind = coordination.EventStatusChanged
	}
	return projected
}

func projectStatus(status task.Status) coordination.Status {
	switch status {
	case task.StatusPending, task.StatusScheduled, task.StatusReserving:
		return coordination.StatusPending
	case task.StatusWaitingPermission:
		return coordination.StatusWaiting
	case task.StatusSucceeded:
		return coordination.StatusSucceeded
	case task.StatusFailed:
		return coordination.StatusFailed
	case task.StatusCancelled:
		return coordination.StatusCancelled
	case task.StatusTimedOut:
		return coordination.StatusTimedOut
	default:
		return coordination.StatusRunning
	}
}

func projectResult(delegationID string, result *task.TaskResult) (coordination.ResultEnvelope, error) {
	copy := *result
	copy.DetailsRef = ""
	content, err := json.Marshal(copy)
	if err != nil {
		return coordination.ResultEnvelope{}, err
	}
	return coordination.ResultEnvelope{Summary: result.Summary, Content: string(content), ContentType: "application/vnd.amux.task-result+json", DetailsRef: "delegation:" + delegationID, TrustLevel: coordination.TrustInternal, SourceRefs: []string{"internal_task"}}, nil
}

func permissionSummary(request *task.PermissionRequest) string {
	parts := make([]string, 0, 2)
	if value := strings.TrimSpace(request.Tool); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(request.Command); value != "" {
		parts = append(parts, value)
	}
	if len(parts) == 0 {
		return "internal agent requests permission"
	}
	return strings.Join(parts, ": ")
}

func resultSummary(result *task.TaskResult) string {
	if result == nil || strings.TrimSpace(result.Summary) == "" {
		return "internal task failed"
	}
	return result.Summary
}
