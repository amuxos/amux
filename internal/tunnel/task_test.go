package tunnel

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// fakeTaskSrv is a minimal pb.AmuxTaskServer recording what it was asked and
// returning canned responses, to assert the tunnel client's delegation.
type fakeTaskSrv struct {
	pb.UnimplementedAmuxTaskServer
	gotSpec      []byte
	gotCancelID  string
	gotPrune     *pb.PruneSubtasksRequest
	streamEvents [][]byte
}

func (f *fakeTaskSrv) SubmitSubtask(_ context.Context, req *pb.SubmitSubtaskRequest) (*pb.SubmitSubtaskResponse, error) {
	f.gotSpec = req.SpecJson
	return &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"pending"}`)}, nil
}
func (f *fakeTaskSrv) GetSubtask(_ context.Context, req *pb.GetSubtaskRequest) (*pb.GetSubtaskResponse, error) {
	return &pb.GetSubtaskResponse{TaskJson: []byte(`{"id":"` + req.Id + `","status":"running"}`)}, nil
}
func (f *fakeTaskSrv) ListSubtasks(context.Context, *pb.ListSubtasksRequest) (*pb.ListSubtasksResponse, error) {
	return &pb.ListSubtasksResponse{TasksJson: [][]byte{[]byte(`{"id":"task-1"}`)}}, nil
}
func (f *fakeTaskSrv) PruneSubtasks(_ context.Context, req *pb.PruneSubtasksRequest) (*pb.PruneSubtasksResponse, error) {
	cp := *req
	f.gotPrune = &cp
	return &pb.PruneSubtasksResponse{DeletedCount: 1, Tasks: []*pb.PrunedSubtask{{Id: "task-1"}}}, nil
}
func (f *fakeTaskSrv) CancelSubtask(_ context.Context, req *pb.CancelSubtaskRequest) (*pb.CancelSubtaskResponse, error) {
	f.gotCancelID = req.Id
	return &pb.CancelSubtaskResponse{Ok: true}, nil
}
func (f *fakeTaskSrv) StreamSubtask(_ *pb.StreamSubtaskRequest, stream pb.AmuxTask_StreamSubtaskServer) error {
	for _, e := range f.streamEvents {
		if err := stream.Send(&pb.SubtaskEvent{EventJson: e}); err != nil {
			return err
		}
	}
	return nil
}

// runReq drives handleRequest synchronously and returns the frames it emitted.
func runReq(c *Client, method string, payload interface{}) []*pb.TunnelMsg {
	var mu sync.Mutex
	var sent []*pb.TunnelMsg
	sendFn := func(m *pb.TunnelMsg) error {
		mu.Lock()
		sent = append(sent, m)
		mu.Unlock()
		return nil
	}
	raw, _ := json.Marshal(payload)
	c.handleRequest(context.Background(), sendFn, &pb.TunnelMsg{Kind: "req", RequestId: "r1", Method: method, Payload: raw})
	return sent
}

func TestHandleRequestTaskUnaryDelegation(t *testing.T) {
	f := &fakeTaskSrv{}
	c := NewClient("addr", "t1", "t1", nil, nil)
	c.SetTaskServer(f)

	// Submit: spec is forwarded; the task JSON comes back in a single resp frame.
	sent := runReq(c, pb.TaskMethodSubmit, &pb.SubmitSubtaskRequest{SpecJson: []byte(`{"agent":"claude","task":"x"}`)})
	if len(sent) != 1 || sent[0].Kind != "resp" || sent[0].Error != "" {
		t.Fatalf("submit frames = %+v", sent)
	}
	var sub pb.SubmitSubtaskResponse
	if err := json.Unmarshal(sent[0].Payload, &sub); err != nil {
		t.Fatalf("decode submit resp: %v", err)
	}
	if string(sub.TaskJson) != `{"id":"task-1","status":"pending"}` {
		t.Fatalf("submit task json = %s", sub.TaskJson)
	}
	if string(f.gotSpec) != `{"agent":"claude","task":"x"}` {
		t.Fatalf("spec not forwarded: %s", f.gotSpec)
	}

	// Prune forwards the request.
	sent = runReq(c, pb.TaskMethodPrune, &pb.PruneSubtasksRequest{DryRun: true, OlderThanSeconds: 60})
	if len(sent) != 1 || sent[0].Error != "" || f.gotPrune == nil || !f.gotPrune.DryRun || f.gotPrune.OlderThanSeconds != 60 {
		t.Fatalf("prune frames = %+v gotPrune=%+v", sent, f.gotPrune)
	}

	// Cancel forwards the id.
	sent = runReq(c, pb.TaskMethodCancel, &pb.CancelSubtaskRequest{Id: "task-1"})
	if len(sent) != 1 || sent[0].Error != "" || f.gotCancelID != "task-1" {
		t.Fatalf("cancel frames = %+v gotCancelID=%q", sent, f.gotCancelID)
	}
}

func TestHandleRequestTaskStreamDelegation(t *testing.T) {
	f := &fakeTaskSrv{streamEvents: [][]byte{[]byte(`{"kind":"status"}`), []byte(`{"kind":"result"}`)}}
	c := NewClient("addr", "t1", "t1", nil, nil)
	c.SetTaskServer(f)

	sent := runReq(c, pb.TaskMethodStream, &pb.StreamSubtaskRequest{Id: "task-1"})
	// Two data frames then a terminal end frame with no error.
	if len(sent) != 3 {
		t.Fatalf("stream frames = %d (%+v)", len(sent), sent)
	}
	for i, want := range []string{"data", "data", "end"} {
		if sent[i].Kind != want {
			t.Fatalf("frame %d kind = %q, want %q", i, sent[i].Kind, want)
		}
	}
	if sent[2].Error != "" {
		t.Fatalf("end frame carried error: %q", sent[2].Error)
	}
	var ev pb.SubtaskEvent
	if err := json.Unmarshal(sent[0].Payload, &ev); err != nil || string(ev.EventJson) != `{"kind":"status"}` {
		t.Fatalf("first data frame = %s err=%v", ev.EventJson, err)
	}
}

func TestHandleRequestTaskUnavailableWithoutTaskServer(t *testing.T) {
	c := NewClient("addr", "t1", "t1", nil, nil) // no SetTaskServer

	sent := runReq(c, pb.TaskMethodGet, &pb.GetSubtaskRequest{Id: "task-1"})
	if len(sent) != 1 || sent[0].Error != errTaskUnavailable {
		t.Fatalf("get-without-taskserver frames = %+v", sent)
	}

	// Stream signals unavailability via a terminal end frame, not a resp.
	sent = runReq(c, pb.TaskMethodStream, &pb.StreamSubtaskRequest{Id: "task-1"})
	if len(sent) != 1 || sent[0].Kind != "end" || sent[0].Error != errTaskUnavailable {
		t.Fatalf("stream-without-taskserver frames = %+v", sent)
	}
}
