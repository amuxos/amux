package hub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

func TestCoordinationTaskControlTranslatesTaskRouterWire(t *testing.T) {
	t.Parallel()

	router := &fakeCoordinationTaskRouter{
		run:    task.TaskRun{ID: "task-1", Status: task.StatusRunning},
		events: []task.TaskEvent{{Seq: 2, Kind: task.TaskEventOutput, Output: []byte("working")}},
	}
	control, err := newCoordinationTaskControl(router)
	if err != nil {
		t.Fatalf("newCoordinationTaskControl: %v", err)
	}
	submitted, err := control.Submit(context.Background(), task.Spec{Agent: "builtin/reviewer", Task: "review"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if submitted.ID != "task-1" || router.submitted.Agent != "builtin/reviewer" {
		t.Fatalf("submitted = %#v router spec=%#v", submitted, router.submitted)
	}

	var streamed []task.TaskEvent
	if err := control.Stream(context.Background(), "task-1", 1, func(event task.TaskEvent) error {
		streamed = append(streamed, event)
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if router.fromSeq != 1 || len(streamed) != 1 || streamed[0].Seq != 2 || string(streamed[0].Output) != "working" {
		t.Fatalf("streamed = %#v from_seq=%d", streamed, router.fromSeq)
	}

	got, err := control.Get(context.Background(), "task-1")
	if err != nil || got.Status != task.StatusRunning {
		t.Fatalf("Get = %#v err=%v", got, err)
	}
	if err := control.RespondPermission(context.Background(), "task-1", task.PermissionResponse{
		PermissionId: "permission-1",
		Allow:        true,
		OptionId:     "allow-once",
		Reason:       "needed",
	}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if router.permission.PermissionId != "permission-1" || !router.permission.Allow || router.permission.OptionId != "allow-once" {
		t.Fatalf("permission = %#v", router.permission)
	}
	if err := control.Cancel(context.Background(), "task-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if router.cancelledID != "task-1" {
		t.Fatalf("cancelled id = %q", router.cancelledID)
	}
}

type fakeCoordinationTaskRouter struct {
	run         task.TaskRun
	submitted   task.Spec
	events      []task.TaskEvent
	fromSeq     uint64
	permission  task.PermissionResponse
	cancelledID string
}

func (f *fakeCoordinationTaskRouter) SubmitSubtask(_ context.Context, request *pb.SubmitSubtaskRequest) (*pb.SubmitSubtaskResponse, error) {
	if err := json.Unmarshal(request.SpecJson, &f.submitted); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(f.run)
	return &pb.SubmitSubtaskResponse{TaskJson: encoded}, err
}

func (f *fakeCoordinationTaskRouter) GetSubtask(context.Context, *pb.GetSubtaskRequest) (*pb.GetSubtaskResponse, error) {
	encoded, err := json.Marshal(f.run)
	return &pb.GetSubtaskResponse{TaskJson: encoded}, err
}

func (f *fakeCoordinationTaskRouter) StreamSubtask(request *pb.StreamSubtaskRequest, stream grpc.ServerStreamingServer[pb.SubtaskEvent]) error {
	f.fromSeq = request.FromSeq
	for _, event := range f.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err := stream.Send(&pb.SubtaskEvent{EventJson: encoded}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeCoordinationTaskRouter) CancelSubtask(_ context.Context, request *pb.CancelSubtaskRequest) (*pb.CancelSubtaskResponse, error) {
	f.cancelledID = request.Id
	return &pb.CancelSubtaskResponse{Ok: true}, nil
}

func (f *fakeCoordinationTaskRouter) RespondSubtaskPermission(_ context.Context, request *pb.RespondSubtaskPermissionRequest) (*pb.RespondSubtaskPermissionResponse, error) {
	f.permission = task.PermissionResponse{
		PermissionId: request.PermissionId,
		Allow:        request.Allow,
		OptionId:     request.OptionId,
		Reason:       request.Reason,
	}
	return &pb.RespondSubtaskPermissionResponse{Ok: true}, nil
}
