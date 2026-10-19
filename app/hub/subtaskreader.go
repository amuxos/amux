package hub

import (
	"context"
	"errors"
	"fmt"
	"io"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/metadata"
)

// bridgeSubtaskReader adapts the taskRouter/taskIndex pair to the bridge's
// SubtaskReader interface so the bridge never depends on amuxd types.
type bridgeSubtaskReader struct {
	router *taskRouter
}

func (r *bridgeSubtaskReader) SubtaskIDsByParent(parentSessionID string) []string {
	if r == nil || r.router == nil || r.router.index == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), reconcileRouteTimeout)
	defer cancel()
	_, _ = r.router.ListSubtasks(ctx, &pb.ListSubtasksRequest{})
	return r.router.index.byParent(parentSessionID)
}

func (r *bridgeSubtaskReader) LiveSubtaskIDsByParent(parentSessionID string) []string {
	if r == nil || r.router == nil || r.router.index == nil {
		return nil
	}
	return r.router.index.byParent(parentSessionID)
}

func (r *bridgeSubtaskReader) GetSubtaskJSON(ctx context.Context, id string) ([]byte, error) {
	resp, err := r.router.GetSubtask(ctx, &pb.GetSubtaskRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return resp.TaskJson, nil
}

func (r *bridgeSubtaskReader) StreamSubtaskEvents(
	ctx context.Context,
	id string,
	fromSeq uint64,
	handle func([]byte) error,
) error {
	if r == nil || r.router == nil {
		return errors.New("subtask router is unavailable")
	}
	if handle == nil {
		return errors.New("subtask event handler is required")
	}
	return r.router.StreamSubtask(
		&pb.StreamSubtaskRequest{Id: id, FromSeq: fromSeq},
		&bridgeSubtaskEventStream{ctx: ctx, handle: handle},
	)
}

type bridgeSubtaskEventStream struct {
	ctx    context.Context
	handle func([]byte) error
}

func (s *bridgeSubtaskEventStream) Send(event *pb.SubtaskEvent) error {
	if event == nil || len(event.EventJson) == 0 {
		return errors.New("subtask stream received an empty event")
	}
	return s.handle(append([]byte(nil), event.EventJson...))
}

func (s *bridgeSubtaskEventStream) Context() context.Context { return s.ctx }

func (s *bridgeSubtaskEventStream) SetHeader(metadata.MD) error  { return nil }
func (s *bridgeSubtaskEventStream) SendHeader(metadata.MD) error { return nil }
func (s *bridgeSubtaskEventStream) SetTrailer(metadata.MD)       {}

func (s *bridgeSubtaskEventStream) SendMsg(message any) error {
	event, ok := message.(*pb.SubtaskEvent)
	if !ok {
		return fmt.Errorf("unexpected subtask stream message %T", message)
	}
	return s.Send(event)
}

func (s *bridgeSubtaskEventStream) RecvMsg(any) error { return io.EOF }

func (r *bridgeSubtaskReader) SubmitScheduledTask(ctx context.Context, spec []byte) (string, error) {
	return r.router.SubmitScheduledTask(ctx, spec)
}
