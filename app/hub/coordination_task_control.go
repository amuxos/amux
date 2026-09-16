package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/amuxos/amux/internal/coordination/amuxadapter"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// coordinationTaskRouter is the existing taskRouter surface used by the
// Coordination Adapter's amuxd-local protobuf translation.
type coordinationTaskRouter interface {
	SubmitSubtask(context.Context, *pb.SubmitSubtaskRequest) (*pb.SubmitSubtaskResponse, error)
	GetSubtask(context.Context, *pb.GetSubtaskRequest) (*pb.GetSubtaskResponse, error)
	StreamSubtask(*pb.StreamSubtaskRequest, grpc.ServerStreamingServer[pb.SubtaskEvent]) error
	CancelSubtask(context.Context, *pb.CancelSubtaskRequest) (*pb.CancelSubtaskResponse, error)
	RespondSubtaskPermission(context.Context, *pb.RespondSubtaskPermissionRequest) (*pb.RespondSubtaskPermissionResponse, error)
}

// coordinationTaskControl keeps protobuf and tunnel concerns in cmd/amuxd;
// internal/coordination/amuxadapter consumes only task domain types.
type coordinationTaskControl struct {
	router coordinationTaskRouter
}

var _ amuxadapter.TaskControl = (*coordinationTaskControl)(nil)

func newCoordinationTaskControl(router coordinationTaskRouter) (*coordinationTaskControl, error) {
	if router == nil {
		return nil, errors.New("coordination task router is required")
	}
	return &coordinationTaskControl{router: router}, nil
}

func (c *coordinationTaskControl) Submit(ctx context.Context, spec task.Spec) (*task.TaskRun, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode coordination task spec: %w", err)
	}
	response, err := c.router.SubmitSubtask(ctx, &pb.SubmitSubtaskRequest{SpecJson: encoded})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("coordination task submit returned no response")
	}
	return decodeCoordinationTaskRun(response.TaskJson)
}

func (c *coordinationTaskControl) Get(ctx context.Context, id string) (*task.TaskRun, error) {
	response, err := c.router.GetSubtask(ctx, &pb.GetSubtaskRequest{Id: id})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("coordination task get returned no response")
	}
	return decodeCoordinationTaskRun(response.TaskJson)
}

func (c *coordinationTaskControl) Stream(
	ctx context.Context,
	id string,
	fromSeq uint64,
	handle func(task.TaskEvent) error,
) error {
	if handle == nil {
		return errors.New("coordination task stream handler is required")
	}
	stream := &coordinationTaskEventStream{ctx: ctx, handle: handle}
	return c.router.StreamSubtask(&pb.StreamSubtaskRequest{Id: id, FromSeq: fromSeq}, stream)
}

func (c *coordinationTaskControl) Cancel(ctx context.Context, id string) error {
	response, err := c.router.CancelSubtask(ctx, &pb.CancelSubtaskRequest{Id: id})
	if err != nil {
		return err
	}
	if response == nil || !response.Ok {
		return errors.New("coordination task cancellation was not accepted")
	}
	return nil
}

func (c *coordinationTaskControl) RespondPermission(ctx context.Context, id string, response task.PermissionResponse) error {
	reply, err := c.router.RespondSubtaskPermission(ctx, &pb.RespondSubtaskPermissionRequest{
		Id:           id,
		PermissionId: response.PermissionId,
		Allow:        response.Allow,
		OptionId:     response.OptionId,
		Reason:       response.Reason,
	})
	if err != nil {
		return err
	}
	if reply == nil || !reply.Ok {
		return errors.New("coordination task permission response was not accepted")
	}
	return nil
}

func decodeCoordinationTaskRun(encoded []byte) (*task.TaskRun, error) {
	if len(encoded) == 0 {
		return nil, errors.New("coordination task response has no task")
	}
	var run task.TaskRun
	if err := json.Unmarshal(encoded, &run); err != nil {
		return nil, fmt.Errorf("decode coordination task: %w", err)
	}
	return &run, nil
}

type coordinationTaskEventStream struct {
	ctx    context.Context
	handle func(task.TaskEvent) error
}

func (s *coordinationTaskEventStream) Send(event *pb.SubtaskEvent) error {
	if event == nil || len(event.EventJson) == 0 {
		return errors.New("coordination task stream received an empty event")
	}
	var decoded task.TaskEvent
	if err := json.Unmarshal(event.EventJson, &decoded); err != nil {
		return fmt.Errorf("decode coordination task event: %w", err)
	}
	return s.handle(decoded)
}

func (s *coordinationTaskEventStream) Context() context.Context { return s.ctx }

func (s *coordinationTaskEventStream) SetHeader(metadata.MD) error  { return nil }
func (s *coordinationTaskEventStream) SendHeader(metadata.MD) error { return nil }
func (s *coordinationTaskEventStream) SetTrailer(metadata.MD)       {}

func (s *coordinationTaskEventStream) SendMsg(message any) error {
	event, ok := message.(*pb.SubtaskEvent)
	if !ok {
		return fmt.Errorf("unexpected coordination task stream message %T", message)
	}
	return s.Send(event)
}

func (s *coordinationTaskEventStream) RecvMsg(any) error { return io.EOF }
