package clientop

import (
	"context"
	"errors"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type scheduleFakeAmuxd struct {
	pb.AmuxdClient
	createReq  *pb.CreateScheduleRequest
	createResp *pb.CreateScheduleResponse
	listResp   *pb.ListSchedulesResponse
	getReq     *pb.GetScheduleRequest
	getResp    *pb.GetScheduleResponse
	deleteResp *pb.DeleteScheduleResponse
	setReq     *pb.SetScheduleEnabledRequest
	setResp    *pb.SetScheduleEnabledResponse
	updateReq  *pb.UpdateScheduleRequest
	updateResp *pb.UpdateScheduleResponse
}

func (f *scheduleFakeAmuxd) CreateSchedule(_ context.Context, in *pb.CreateScheduleRequest, _ ...grpc.CallOption) (*pb.CreateScheduleResponse, error) {
	f.createReq = in
	return f.createResp, nil
}
func (f *scheduleFakeAmuxd) ListSchedules(context.Context, *pb.ListSchedulesRequest, ...grpc.CallOption) (*pb.ListSchedulesResponse, error) {
	return f.listResp, nil
}
func (f *scheduleFakeAmuxd) GetSchedule(_ context.Context, in *pb.GetScheduleRequest, _ ...grpc.CallOption) (*pb.GetScheduleResponse, error) {
	f.getReq = in
	return f.getResp, nil
}
func (f *scheduleFakeAmuxd) DeleteSchedule(context.Context, *pb.DeleteScheduleRequest, ...grpc.CallOption) (*pb.DeleteScheduleResponse, error) {
	return f.deleteResp, nil
}
func (f *scheduleFakeAmuxd) SetScheduleEnabled(_ context.Context, in *pb.SetScheduleEnabledRequest, _ ...grpc.CallOption) (*pb.SetScheduleEnabledResponse, error) {
	f.setReq = in
	return f.setResp, nil
}
func (f *scheduleFakeAmuxd) UpdateSchedule(_ context.Context, in *pb.UpdateScheduleRequest, _ ...grpc.CallOption) (*pb.UpdateScheduleResponse, error) {
	f.updateReq = in
	return f.updateResp, nil
}

func validScheduleTaskSpec() ScheduledTaskSpec {
	return ScheduledTaskSpec{Agent: "builtin/codex", Task: "review"}
}

func TestScheduleCreateTaskSendsNormalizedSpec(t *testing.T) {
	fake := &scheduleFakeAmuxd{createResp: &pb.CreateScheduleResponse{Id: 7, NextRunAt: 1700000000}}
	out, err := ScheduleCreateTask(context.Background(), fake, ScheduleCreateTaskRequest{
		Spec: " every 1h ", Name: "nightly", CreatedBy: "cli", Task: validScheduleTaskSpec(),
	})
	if err != nil {
		t.Fatalf("ScheduleCreateTask: %v", err)
	}
	if out.ID != 7 || out.NextRunAt != 1700000000 {
		t.Fatalf("result = %+v", out)
	}
	if fake.createReq.ScheduleType != "subtask" || fake.createReq.Spec != "every 1h" || fake.createReq.CreatedBy != "cli" || len(fake.createReq.TaskSpecJson) == 0 {
		t.Fatalf("request = %+v", fake.createReq)
	}
}

func TestScheduleCreateTaskRejectsEmptySpec(t *testing.T) {
	_, err := ScheduleCreateTask(context.Background(), &scheduleFakeAmuxd{}, ScheduleCreateTaskRequest{Task: validScheduleTaskSpec()})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != FieldSpec {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduleDeleteReportsNotFound(t *testing.T) {
	_, err := ScheduleDelete(context.Background(), &scheduleFakeAmuxd{deleteResp: &pb.DeleteScheduleResponse{Ok: false}}, ScheduleIDRequest{ID: 9})
	if err == nil {
		t.Fatal("missing schedule returned nil error")
	}
}

func TestScheduleGetReportsNilSchedule(t *testing.T) {
	_, err := ScheduleGet(context.Background(), &scheduleFakeAmuxd{getResp: &pb.GetScheduleResponse{}}, ScheduleIDRequest{ID: 9})
	if err == nil {
		t.Fatal("nil schedule returned nil error")
	}
}

func TestScheduleGetPassesIncludeRuns(t *testing.T) {
	want := &pb.ScheduleItem{Id: 9}
	fake := &scheduleFakeAmuxd{getResp: &pb.GetScheduleResponse{Schedule: want}}
	got, err := ScheduleGet(context.Background(), fake, ScheduleIDRequest{ID: 9, IncludeRuns: true})
	if err != nil || got != want || fake.getReq == nil || !fake.getReq.IncludeRuns {
		t.Fatalf("got=%v err=%v req=%v", got, err, fake.getReq)
	}
}

func TestScheduleListReturnsSchedules(t *testing.T) {
	want := []*pb.ScheduleItem{{Id: 1}}
	got, err := ScheduleList(context.Background(), &scheduleFakeAmuxd{listResp: &pb.ListSchedulesResponse{Schedules: want}})
	if err != nil || len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestScheduleSetEnabledFalseIsNotFound(t *testing.T) {
	ok, err := ScheduleSetEnabled(context.Background(), &scheduleFakeAmuxd{setResp: &pb.SetScheduleEnabledResponse{Ok: false}}, 9, true)
	if err == nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestScheduleSetEnabledSendsRequest(t *testing.T) {
	fake := &scheduleFakeAmuxd{setResp: &pb.SetScheduleEnabledResponse{Ok: true}}
	ok, err := ScheduleSetEnabled(context.Background(), fake, 9, false)
	if err != nil || !ok || fake.setReq.Id != 9 || fake.setReq.Enabled {
		t.Fatalf("ok=%v err=%v req=%v", ok, err, fake.setReq)
	}
}

func strPtr(s string) *string { return &s }

func TestScheduleUpdateSendsPartialFields(t *testing.T) {
	fake := &scheduleFakeAmuxd{updateResp: &pb.UpdateScheduleResponse{
		Schedule: &pb.ScheduleItem{Id: 3, Name: "new-name", Spec: "every 2h", NextRunAt: 1700000000},
	}}
	out, err := ScheduleUpdate(context.Background(), fake, ScheduleUpdateRequest{
		ID:   3,
		Name: strPtr("new-name"),
		Spec: strPtr("every 2h"),
	})
	if err != nil {
		t.Fatalf("ScheduleUpdate: %v", err)
	}
	if got := fake.updateReq; got.Id != 3 || got.Name == nil || *got.Name != "new-name" || got.Spec == nil || *got.Spec != "every 2h" {
		t.Fatalf("update request = %+v, want id 3 name new-name spec every 2h", got)
	}
	if len(fake.updateReq.TaskSpecJson) != 0 {
		t.Fatalf("task_spec_json sent without a task; want empty")
	}
	if out.ID != 3 || out.Name != "new-name" {
		t.Fatalf("result = %+v, want id 3 name new-name", out)
	}
}

func TestScheduleUpdateValidatesTaskBeforeSending(t *testing.T) {
	fake := &scheduleFakeAmuxd{}
	bad := validScheduledTaskSpec()
	bad.Agent = "  "
	_, err := ScheduleUpdate(context.Background(), fake, ScheduleUpdateRequest{
		ID:   3,
		Task: &bad,
	})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != FieldAgent {
		t.Fatalf("err = %v, want FieldError on agent", err)
	}
	if fake.updateReq != nil {
		t.Fatal("invalid task was sent to the hub")
	}
}

func TestScheduleUpdateRequiresAField(t *testing.T) {
	_, err := ScheduleUpdate(context.Background(), &scheduleFakeAmuxd{}, ScheduleUpdateRequest{ID: 3})
	if err == nil {
		t.Fatal("ScheduleUpdate with no fields = nil, want an error")
	}
}
