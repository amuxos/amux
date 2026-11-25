package clientop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/schedule"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type ScheduleCreateTaskRequest struct {
	Spec           string            `json:"spec"`
	Timezone       string            `json:"timezone,omitempty"`
	Name           string            `json:"name,omitempty"`
	DeliveryTarget string            `json:"delivery_target,omitempty"`
	DeliveryPolicy string            `json:"delivery_policy,omitempty"`
	Task           ScheduledTaskSpec `json:"task"`
	CreatedBy      string            `json:"-"`
}

type ScheduleCreateResult struct {
	ID             int64  `json:"schedule_id"`
	NextRunAt      int64  `json:"next_run_at,omitempty"`
	DeliveryTarget string `json:"delivery_target,omitempty"`
	DeliveryPolicy string `json:"delivery_policy,omitempty"`
}

type ScheduleIDRequest struct {
	ID          int64 `json:"schedule_id"`
	IncludeRuns bool  `json:"include_runs,omitempty"`
}

func ScheduleCreateTask(ctx context.Context, hub pb.AmuxdClient, req ScheduleCreateTaskRequest) (ScheduleCreateResult, error) {
	if strings.TrimSpace(req.Spec) == "" {
		return ScheduleCreateResult{}, RequiredError(FieldSpec)
	}
	if err := req.Task.Validate(); err != nil {
		return ScheduleCreateResult{}, err
	}
	deliveryTarget := strings.TrimSpace(req.DeliveryTarget)
	if deliveryTarget != "" {
		normalized, err := schedule.NormalizeDeliveryTarget(deliveryTarget)
		if err != nil {
			return ScheduleCreateResult{}, InvalidError(FieldDeliveryTarget, req.DeliveryTarget, "none, origin, or creator_dm")
		}
		deliveryTarget = normalized
	}
	deliveryPolicy := strings.TrimSpace(req.DeliveryPolicy)
	if deliveryPolicy != "" {
		normalized, err := schedule.NormalizeDeliveryPolicy(deliveryPolicy)
		if err != nil {
			return ScheduleCreateResult{}, InvalidError(FieldDeliveryPolicy, req.DeliveryPolicy, "always, on_failure, or on_non_empty")
		}
		deliveryPolicy = normalized
	}
	specJSON, err := json.Marshal(req.Task.ToTaskSpec())
	if err != nil {
		return ScheduleCreateResult{}, fmt.Errorf("encode task spec: %w", err)
	}
	resp, err := hub.CreateSchedule(ctx, &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask, Spec: strings.TrimSpace(req.Spec), Timezone: strings.TrimSpace(req.Timezone), Name: req.Name,
		TaskSpecJson: specJSON, CreatedBy: req.CreatedBy,
		DeliveryTarget: deliveryTarget, DeliveryPolicy: deliveryPolicy,
	})
	if err != nil {
		return ScheduleCreateResult{}, err
	}
	if resp.Error != "" {
		return ScheduleCreateResult{}, fmt.Errorf("%s", resp.Error)
	}
	return ScheduleCreateResult{ID: resp.Id, NextRunAt: resp.NextRunAt, DeliveryTarget: resp.DeliveryTarget, DeliveryPolicy: resp.DeliveryPolicy}, nil
}

// ScheduleCreateSessionPromptRequest creates a session-prompt schedule that
// Submit-s into an existing target session when due (Scheduler.deliverReuseSession
// or deliverNewSessionPerRun).
type ScheduleCreateSessionPromptRequest struct {
	Spec           string `json:"spec"`
	Timezone       string `json:"timezone,omitempty"`
	Name           string `json:"name,omitempty"`
	TargetID       string `json:"target_id"`
	TargetName     string `json:"target_name,omitempty"`
	SessionName    string `json:"session_name"`
	Prompt         string `json:"prompt"`
	RunMode        string `json:"run_mode,omitempty"`
	DeliveryTarget string `json:"delivery_target,omitempty"`
	DeliveryPolicy string `json:"delivery_policy,omitempty"`
	CreatedBy      string `json:"-"`
}

// ScheduleCreateSessionPrompt persists a session-prompt schedule via the hub.
// Authorization (SessionPromptAuthorizer / authorizeScheduleSessionTarget) stays
// on the hub; this helper only validates and forwards the request.
func ScheduleCreateSessionPrompt(ctx context.Context, hub pb.AmuxdClient, req ScheduleCreateSessionPromptRequest) (ScheduleCreateResult, error) {
	if strings.TrimSpace(req.Spec) == "" {
		return ScheduleCreateResult{}, RequiredError(FieldSpec)
	}
	if strings.TrimSpace(req.TargetID) == "" {
		return ScheduleCreateResult{}, RequiredError(FieldTargetID)
	}
	if strings.TrimSpace(req.SessionName) == "" {
		return ScheduleCreateResult{}, RequiredError(FieldScheduledSession)
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return ScheduleCreateResult{}, RequiredError(FieldPrompt)
	}
	runMode, err := schedule.NormalizeRunMode(req.RunMode)
	if err != nil {
		return ScheduleCreateResult{}, InvalidError(FieldRunMode, req.RunMode, "reuse-session or new-session-per-run")
	}
	deliveryTarget := strings.TrimSpace(req.DeliveryTarget)
	if deliveryTarget != "" {
		normalized, err := schedule.NormalizeDeliveryTarget(deliveryTarget)
		if err != nil {
			return ScheduleCreateResult{}, InvalidError(FieldDeliveryTarget, req.DeliveryTarget, "none, origin, or creator_dm")
		}
		deliveryTarget = normalized
	}
	deliveryPolicy := strings.TrimSpace(req.DeliveryPolicy)
	if deliveryPolicy != "" {
		normalized, err := schedule.NormalizeDeliveryPolicy(deliveryPolicy)
		if err != nil {
			return ScheduleCreateResult{}, InvalidError(FieldDeliveryPolicy, req.DeliveryPolicy, "always, on_failure, or on_non_empty")
		}
		deliveryPolicy = normalized
	}
	resp, err := hub.CreateSchedule(ctx, &pb.CreateScheduleRequest{
		ScheduleType:   schedule.ScheduleTypeSessionPrompt,
		TargetId:       strings.TrimSpace(req.TargetID),
		TargetName:     strings.TrimSpace(req.TargetName),
		SessionName:    strings.TrimSpace(req.SessionName),
		Spec:           strings.TrimSpace(req.Spec),
		Timezone:       strings.TrimSpace(req.Timezone),
		RunMode:        runMode,
		Prompt:         prompt,
		Name:           req.Name,
		CreatedBy:      req.CreatedBy,
		DeliveryTarget: deliveryTarget,
		DeliveryPolicy: deliveryPolicy,
	})
	if err != nil {
		return ScheduleCreateResult{}, err
	}
	if resp.Error != "" {
		return ScheduleCreateResult{}, fmt.Errorf("%s", resp.Error)
	}
	return ScheduleCreateResult{ID: resp.Id, NextRunAt: resp.NextRunAt, DeliveryTarget: resp.DeliveryTarget, DeliveryPolicy: resp.DeliveryPolicy}, nil
}

func ScheduleDelete(ctx context.Context, hub pb.AmuxdClient, req ScheduleIDRequest) (bool, error) {
	resp, err := hub.DeleteSchedule(ctx, &pb.DeleteScheduleRequest{Id: req.ID})
	if err != nil {
		return false, err
	}
	if !resp.Ok {
		return false, fmt.Errorf("schedule #%d not found", req.ID)
	}
	return true, nil
}

func ScheduleGet(ctx context.Context, hub pb.AmuxdClient, req ScheduleIDRequest) (*pb.ScheduleItem, error) {
	resp, err := hub.GetSchedule(ctx, &pb.GetScheduleRequest{Id: req.ID, IncludeRuns: req.IncludeRuns})
	if err != nil {
		return nil, err
	}
	if resp.Schedule == nil {
		return nil, fmt.Errorf("schedule #%d not found", req.ID)
	}
	return resp.Schedule, nil
}

func ScheduleList(ctx context.Context, hub pb.AmuxdClient) ([]*pb.ScheduleItem, error) {
	resp, err := hub.ListSchedules(ctx, &pb.ListSchedulesRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Schedules, nil
}

func ScheduleSetEnabled(ctx context.Context, hub pb.AmuxdClient, id int64, enabled bool) (bool, error) {
	resp, err := hub.SetScheduleEnabled(ctx, &pb.SetScheduleEnabledRequest{Id: id, Enabled: enabled})
	if err != nil {
		return false, err
	}
	if !resp.Ok {
		return false, fmt.Errorf("schedule #%d not found", id)
	}
	return true, nil
}

// ScheduleTriggerResult reports the admission outcome of an immediate manual run.
type ScheduleTriggerResult struct {
	ScheduleID  int64  `json:"schedule_id"`
	ExecutionID int64  `json:"execution_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	Status      string `json:"status,omitempty"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
}

// ScheduleTrigger executes one immediate manual run of a schedule without
// altering its enabled state or next_run_at.
func ScheduleTrigger(ctx context.Context, hub pb.AmuxdClient, id int64) (ScheduleTriggerResult, error) {
	resp, err := hub.TriggerSchedule(ctx, &pb.TriggerScheduleRequest{Id: id})
	if err != nil {
		return ScheduleTriggerResult{}, err
	}
	return ScheduleTriggerResult{
		ScheduleID:  id,
		ExecutionID: resp.ExecutionId,
		TaskID:      resp.TaskId,
		Status:      resp.Status,
		OK:          resp.Ok,
		Error:       resp.Error,
	}, nil
}

// ScheduleUpdateRequest updates one schedule in place. Any of Name, Spec,
// Prompt, RunMode, or Task may be set; omitted fields keep their current value.
// Setting Spec recomputes next_run_at on the hub. Prompt and RunMode apply to
// session-prompt schedules; Task applies to subtask schedules and reuses the
// same validation as create.
type ScheduleUpdateRequest struct {
	ID             int64
	Name           *string
	Spec           *string
	Timezone       *string
	Prompt         *string
	RunMode        *string
	DeliveryTarget *string
	DeliveryPolicy *string
	Task           *ScheduledTaskSpec
}

// ScheduleUpdateResult is what an update returns.
type ScheduleUpdateResult struct {
	ID             int64  `json:"schedule_id"`
	Name           string `json:"name,omitempty"`
	Spec           string `json:"spec,omitempty"`
	Timezone       string `json:"timezone,omitempty"`
	DeliveryTarget string `json:"delivery_target,omitempty"`
	DeliveryPolicy string `json:"delivery_policy,omitempty"`
	NextRunAt      int64  `json:"next_run_at,omitempty"`
}

// ScheduleUpdate applies a partial update to one schedule.
func ScheduleUpdate(ctx context.Context, hub pb.AmuxdClient, req ScheduleUpdateRequest) (ScheduleUpdateResult, error) {
	wire := &pb.UpdateScheduleRequest{Id: req.ID}
	changed := false
	if req.Name != nil {
		wire.Name = req.Name
		changed = true
	}
	if req.Spec != nil {
		spec := strings.TrimSpace(*req.Spec)
		if spec == "" {
			return ScheduleUpdateResult{}, RequiredError(FieldSpec)
		}
		wire.Spec = &spec
		changed = true
	}
	if req.Timezone != nil {
		tz := strings.TrimSpace(*req.Timezone)
		wire.Timezone = &tz
		changed = true
	}
	if req.Prompt != nil {
		prompt := strings.TrimSpace(*req.Prompt)
		if prompt == "" {
			return ScheduleUpdateResult{}, RequiredError(FieldPrompt)
		}
		wire.Prompt = &prompt
		changed = true
	}
	if req.RunMode != nil {
		runMode, err := schedule.NormalizeRunMode(*req.RunMode)
		if err != nil {
			return ScheduleUpdateResult{}, InvalidError(FieldRunMode, *req.RunMode, "reuse-session or new-session-per-run")
		}
		wire.RunMode = &runMode
		changed = true
	}
	if req.DeliveryTarget != nil {
		target, err := schedule.NormalizeDeliveryTarget(*req.DeliveryTarget)
		if err != nil {
			return ScheduleUpdateResult{}, InvalidError(FieldDeliveryTarget, *req.DeliveryTarget, "none, origin, or creator_dm")
		}
		wire.DeliveryTarget = &target
		changed = true
	}
	if req.DeliveryPolicy != nil {
		policy, err := schedule.NormalizeDeliveryPolicy(*req.DeliveryPolicy)
		if err != nil {
			return ScheduleUpdateResult{}, InvalidError(FieldDeliveryPolicy, *req.DeliveryPolicy, "always, on_failure, or on_non_empty")
		}
		wire.DeliveryPolicy = &policy
		changed = true
	}
	if req.Task != nil {
		if err := req.Task.Validate(); err != nil {
			return ScheduleUpdateResult{}, err
		}
		specJSON, err := json.Marshal(req.Task.ToTaskSpec())
		if err != nil {
			return ScheduleUpdateResult{}, fmt.Errorf("encode task spec: %w", err)
		}
		wire.TaskSpecJson = specJSON
		changed = true
	}
	if !changed {
		return ScheduleUpdateResult{}, fmt.Errorf("at least one of name, spec, timezone, prompt, run_mode, delivery_target, delivery_policy, or task is required")
	}
	resp, err := hub.UpdateSchedule(ctx, wire)
	if err != nil {
		return ScheduleUpdateResult{}, err
	}
	if resp.Error != "" {
		return ScheduleUpdateResult{}, fmt.Errorf("%s", resp.Error)
	}
	out := ScheduleUpdateResult{ID: req.ID}
	if resp.Schedule != nil {
		out.Name = resp.Schedule.Name
		out.Spec = resp.Schedule.Spec
		out.Timezone = resp.Schedule.Timezone
		out.DeliveryTarget = resp.Schedule.DeliveryTarget
		out.DeliveryPolicy = resp.Schedule.DeliveryPolicy
		out.NextRunAt = resp.Schedule.NextRunAt
	}
	return out, nil
}
