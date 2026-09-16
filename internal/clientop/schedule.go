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
	Spec      string            `json:"spec"`
	Name      string            `json:"name,omitempty"`
	Task      ScheduledTaskSpec `json:"task"`
	CreatedBy string            `json:"-"`
}

type ScheduleCreateResult struct {
	ID        int64 `json:"schedule_id"`
	NextRunAt int64 `json:"next_run_at,omitempty"`
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
	specJSON, err := json.Marshal(req.Task.ToTaskSpec())
	if err != nil {
		return ScheduleCreateResult{}, fmt.Errorf("encode task spec: %w", err)
	}
	resp, err := hub.CreateSchedule(ctx, &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask, Spec: strings.TrimSpace(req.Spec), Name: req.Name,
		TaskSpecJson: specJSON, CreatedBy: req.CreatedBy,
	})
	if err != nil {
		return ScheduleCreateResult{}, err
	}
	if resp.Error != "" {
		return ScheduleCreateResult{}, fmt.Errorf("%s", resp.Error)
	}
	return ScheduleCreateResult{ID: resp.Id, NextRunAt: resp.NextRunAt}, nil
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

// ScheduleUpdateRequest updates one schedule in place. Any of Name, Spec, or
// Task may be set; omitted fields keep their current value. Setting Spec
// recomputes next_run_at on the hub. Setting Task requires a complete spec and
// reuses the same validation as create.
type ScheduleUpdateRequest struct {
	ID   int64
	Name *string
	Spec *string
	Task *ScheduledTaskSpec
}

// ScheduleUpdateResult is what an update returns.
type ScheduleUpdateResult struct {
	ID        int64  `json:"schedule_id"`
	Name      string `json:"name,omitempty"`
	Spec      string `json:"spec,omitempty"`
	NextRunAt int64  `json:"next_run_at,omitempty"`
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
		return ScheduleUpdateResult{}, fmt.Errorf("at least one of name, spec, or task is required")
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
		out.NextRunAt = resp.Schedule.NextRunAt
	}
	return out, nil
}
