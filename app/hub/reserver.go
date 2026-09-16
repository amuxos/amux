package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// reserveRouter is the subset of *tunnel.Hub the reserver needs: a unary RPC
// routed to a target over the reverse tunnel.
type reserveRouter interface {
	Route(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error)
}

// tunnelReserver implements registry.Reserver by routing Reserve/Commit/Release
// to a target amuxlet's local Admission over the reverse tunnel. It is the
// amuxd half of the two-phase admission used by registry.Scheduler.
type tunnelReserver struct {
	router  reserveRouter
	timeout time.Duration
}

func newTunnelReserver(r reserveRouter) *tunnelReserver {
	return &tunnelReserver{router: r, timeout: 5 * time.Second}
}

func (t *tunnelReserver) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), t.timeout)
}

func (t *tunnelReserver) Reserve(targetID, agentType string) (string, error) {
	ctx, cancel := t.ctx()
	defer cancel()
	resp, err := t.router.Route(ctx, targetID, "Reserve", &pb.ReserveRequest{AgentType: agentType})
	if err != nil {
		return "", err
	}
	var out pb.ReserveResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return "", fmt.Errorf("decode reserve response: %w", err)
	}
	return out.ReservationId, nil
}

func (t *tunnelReserver) Commit(targetID, reservationID string) error {
	ctx, cancel := t.ctx()
	defer cancel()
	_, err := t.router.Route(ctx, targetID, "Commit", &pb.CommitRequest{ReservationId: reservationID})
	return err
}

func (t *tunnelReserver) Release(targetID, reservationID string) error {
	ctx, cancel := t.ctx()
	defer cancel()
	_, err := t.router.Route(ctx, targetID, "Release", &pb.ReleaseRequest{ReservationId: reservationID})
	return err
}
