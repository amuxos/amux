package hub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func isolatingFleet() func() []*pb.Target {
	return func() []*pb.Target {
		return []*pb.Target{{
			Id:           "t1",
			Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"},
		}}
	}
}

func plainFleet() func() []*pb.Target {
	return func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Capabilities: &pb.TargetCapabilities{}}}
	}
}

func subtaskScheduleRequest(t *testing.T) *pb.CreateScheduleRequest {
	t.Helper()
	return &pb.CreateScheduleRequest{
		Name:         "joke",
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "every 3m",
		TaskSpecJson: mustTaskSpecJson(t, task.Spec{Agent: "builtin/codex", Task: "tell a joke"}),
		CreatedBy:    "mcp",
	}
}

// An unauthenticated caller used to get a schedule stored with no identity at
// all. Nothing complained until the schedule fired, at which point the runtime
// refused the unidentified execution — every three minutes, in a place that
// names neither the schedule nor its creator. Where the fleet isolates, that
// outcome is certain at creation time, so refuse there instead.
func TestCreateScheduleRejectsIdentitylessSubtaskWhenTheFleetIsolates(t *testing.T) {
	srv := &hubServer{
		schedule:    newScheduleTestStore(t),
		principals:  agentTokenTestStore(t),
		listTargets: isolatingFleet(),
	}
	_, err := srv.CreateSchedule(context.Background(), subtaskScheduleRequest(t))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("CreateSchedule error = %v (code %v), want Unauthenticated", err, status.Code(err))
	}
}

// The same request stays legal where nothing isolates: those runtimes still run
// unidentified executions, so refusing would break working setups to prevent a
// failure that cannot happen there.
func TestCreateScheduleKeepsIdentitylessSubtaskWithoutIsolation(t *testing.T) {
	srv := &hubServer{
		schedule:    newScheduleTestStore(t),
		principals:  agentTokenTestStore(t),
		listTargets: plainFleet(),
	}
	created, err := srv.CreateSchedule(context.Background(), subtaskScheduleRequest(t))
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	got, err := srv.GetSchedule(context.Background(), &pb.GetScheduleRequest{Id: created.Id})
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	var stored task.Spec
	if err := json.Unmarshal(got.Schedule.TaskSpecJson, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ExecutionIdentity != nil {
		t.Fatalf("stored identity = %+v, want none on a non-isolating fleet", stored.ExecutionIdentity)
	}
}
