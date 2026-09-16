package tunnel

import (
	"encoding/json"
	"testing"

	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// admReq builds a tunnel req message for an admission method.
func admReq(t *testing.T, method string, payload interface{}) *pb.TunnelMsg {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &pb.TunnelMsg{Kind: "req", RequestId: "r1", Method: method, Payload: raw}
}

func newAdmissionClient(maxSessions, maxConc int) *Client {
	c := NewClient("addr", "t1", "t1", nil, nil)
	c.SetCapabilities(&registry.Capabilities{
		Agents:      []registry.AgentCapacity{{Type: "claude", MaxConcurrency: maxConc}},
		MaxSessions: maxSessions,
	}, "")
	return c
}

func TestHandleAdmissionReserveCommitRelease(t *testing.T) {
	c := newAdmissionClient(2, 2)

	var lastPayload interface{}
	var lastErr string
	sendResp := func(p interface{}, e string) { lastPayload, lastErr = p, e }

	// Reserve.
	if !c.handleAdmission(admReq(t, "Reserve", &pb.ReserveRequest{AgentType: "claude"}), sendResp) {
		t.Fatal("Reserve not handled")
	}
	if lastErr != "" {
		t.Fatalf("reserve err: %s", lastErr)
	}
	resID := lastPayload.(*pb.ReserveResponse).ReservationId
	if resID == "" {
		t.Fatal("empty reservation id")
	}
	if snap := c.adm.Snapshot(); snap.ReservedSlots != 1 {
		t.Fatalf("reserved = %d, want 1", snap.ReservedSlots)
	}

	// Commit.
	if !c.handleAdmission(admReq(t, "Commit", &pb.CommitRequest{ReservationId: resID}), sendResp) {
		t.Fatal("Commit not handled")
	}
	if lastErr != "" {
		t.Fatalf("commit err: %s", lastErr)
	}
	if snap := c.adm.Snapshot(); snap.CurrentSessions != 1 || snap.ReservedSlots != 0 {
		t.Fatalf("after commit snap = %+v, want 1 running 0 reserved", snap)
	}

	// Release.
	if !c.handleAdmission(admReq(t, "Release", &pb.ReleaseRequest{ReservationId: resID}), sendResp) {
		t.Fatal("Release not handled")
	}
	if snap := c.adm.Snapshot(); snap.CurrentSessions != 0 {
		t.Fatalf("after release snap = %+v, want empty", snap)
	}
}

func TestHandleAdmissionRejectsWhenFull(t *testing.T) {
	c := newAdmissionClient(1, 1)
	sink := func(interface{}, string) {}
	// Occupy the only slot.
	c.handleAdmission(admReq(t, "Reserve", &pb.ReserveRequest{AgentType: "claude"}), sink)

	var lastErr string
	c.handleAdmission(admReq(t, "Reserve", &pb.ReserveRequest{AgentType: "claude"}), func(_ interface{}, e string) { lastErr = e })
	if lastErr == "" {
		t.Fatal("expected no-capacity error on second reserve")
	}
}

func TestHandleAdmissionUnavailableWithoutCaps(t *testing.T) {
	c := NewClient("addr", "t1", "t1", nil, nil) // no SetCapabilities => no admission
	var lastErr string
	if !c.handleAdmission(admReq(t, "Reserve", &pb.ReserveRequest{AgentType: "claude"}), func(_ interface{}, e string) { lastErr = e }) {
		t.Fatal("Reserve should be handled (with error) even without admission")
	}
	if lastErr == "" {
		t.Fatal("expected admission-not-available error")
	}
}

func TestHandleAdmissionIgnoresNonAdmissionMethod(t *testing.T) {
	c := newAdmissionClient(1, 1)
	if c.handleAdmission(&pb.TunnelMsg{Method: "Run"}, func(interface{}, string) {}) {
		t.Fatal("non-admission method should not be handled by handleAdmission")
	}
}
