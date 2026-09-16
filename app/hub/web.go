package hub

import (
	"context"
	"encoding/json"

	"github.com/amuxos/amux/internal/hubweb"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/tunnel"
)

type hubWebBackend struct {
	hub      *tunnel.Hub
	subtasks hubweb.SubtaskReader
}

func (b hubWebBackend) ListTargets() []*pb.Target {
	if b.hub == nil {
		return nil
	}
	return b.hub.ListTargets()
}

func (b hubWebBackend) Attach(ctx context.Context, targetID, name string, fn func(*pb.AttachEvent) error) error {
	if b.hub == nil {
		return nil
	}
	return b.hub.RouteStream(ctx, targetID, "Attach", &pb.AttachRequest{Name: name, TimelineMode: pb.AttachRequest_FOLLOW}, func(msg *pb.TunnelMsg) error {
		var ev pb.AttachEvent
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			return err
		}
		return fn(&ev)
	})
}

func (b hubWebBackend) SubtaskIDsByParent(parentSessionID string) []string {
	if b.subtasks == nil {
		return nil
	}
	return b.subtasks.SubtaskIDsByParent(parentSessionID)
}

func (b hubWebBackend) GetSubtaskJSON(ctx context.Context, id string) ([]byte, error) {
	if b.subtasks == nil {
		return nil, nil
	}
	return b.subtasks.GetSubtaskJSON(ctx, id)
}
