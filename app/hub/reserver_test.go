package hub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// compile-time check: tunnelReserver satisfies registry.Reserver.
var _ registry.Reserver = (*tunnelReserver)(nil)

// fakeRouter records routed calls and returns canned responses keyed by method.
type fakeRouter struct {
	calls   []routedCall
	reserve *pb.ReserveResponse
	err     error
}

type routedCall struct {
	targetID string
	method   string
	payload  interface{}
}

func (f *fakeRouter) Route(_ context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
	f.calls = append(f.calls, routedCall{targetID, method, payload})
	if f.err != nil {
		return nil, f.err
	}
	switch method {
	case "Reserve":
		raw, _ := json.Marshal(f.reserve)
		return &pb.TunnelMsg{Kind: "resp", Payload: raw}, nil
	default:
		return &pb.TunnelMsg{Kind: "resp"}, nil
	}
}

func TestTunnelReserverReserve(t *testing.T) {
	fr := &fakeRouter{reserve: &pb.ReserveResponse{ReservationId: "rsv-1"}}
	r := newTunnelReserver(fr)
	id, err := r.Reserve("mac-01", "claude")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if id != "rsv-1" {
		t.Fatalf("id = %q, want rsv-1", id)
	}
	if len(fr.calls) != 1 || fr.calls[0].method != "Reserve" || fr.calls[0].targetID != "mac-01" {
		t.Fatalf("unexpected calls: %+v", fr.calls)
	}
	req, ok := fr.calls[0].payload.(*pb.ReserveRequest)
	if !ok || req.AgentType != "claude" {
		t.Fatalf("reserve payload = %+v", fr.calls[0].payload)
	}
}

func TestTunnelReserverReserveError(t *testing.T) {
	fr := &fakeRouter{err: errors.New("target offline")}
	r := newTunnelReserver(fr)
	if _, err := r.Reserve("mac-01", "claude"); err == nil {
		t.Fatal("expected error when route fails")
	}
}

func TestTunnelReserverCommitAndRelease(t *testing.T) {
	fr := &fakeRouter{}
	r := newTunnelReserver(fr)
	if err := r.Commit("mac-01", "rsv-1"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := r.Release("mac-01", "rsv-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(fr.calls) != 2 || fr.calls[0].method != "Commit" || fr.calls[1].method != "Release" {
		t.Fatalf("unexpected calls: %+v", fr.calls)
	}
}
