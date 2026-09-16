package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// fakeTunnelStream is a minimal pb.Amuxd_TunnelServer backed by channels. Only
// Recv/Send/Context are exercised by Hub.Serve; the embedded nil ServerStream
// satisfies the rest of the interface and is never called.
type fakeTunnelStream struct {
	pb.Amuxd_TunnelServer
	ctx context.Context
	in  chan *pb.TunnelMsg
	out chan *pb.TunnelMsg
}

func (f *fakeTunnelStream) Recv() (*pb.TunnelMsg, error) {
	msg, ok := <-f.in
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

func (f *fakeTunnelStream) Send(m *pb.TunnelMsg) error {
	select {
	case f.out <- m:
	default:
	}
	return nil
}

func (f *fakeTunnelStream) Context() context.Context { return f.ctx }

func mustMarshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 1s")
}

func TestServeRegistersCapabilitiesAndHeartbeat(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeTunnelStream{ctx: ctx, in: make(chan *pb.TunnelMsg, 8), out: make(chan *pb.TunnelMsg, 8)}

	caps := registry.Capabilities{
		Labels:            map[string]string{"pool": "local-dev"},
		Agents:            []registry.AgentCapacity{{Type: "claude", MaxConcurrency: 2}},
		IsolationBackends: []string{"private-home"},
		MaxSessions:       6,
	}
	go func() { _ = h.Serve(st) }()

	st.in <- &pb.TunnelMsg{Kind: "register", Payload: mustMarshal(t, &pb.RegisterPayload{
		TargetId: "t1", TargetName: "box", Version: "v0.1.5", Capabilities: registryCapabilitiesToPB(&caps),
	})}

	waitFor(t, func() bool {
		rt, ok := h.Registry().Get("t1")
		return ok && rt.SupportsAgent("claude")
	})

	st.in <- &pb.TunnelMsg{Kind: "heartbeat", Payload: mustMarshal(t, &pb.HeartbeatPayload{
		Load: registryLoadToPB(registry.Load{CurrentSessions: 3, PerAgentRunning: map[string]int{"claude": 3}}),
	})}
	waitFor(t, func() bool {
		rt, ok := h.Registry().Get("t1")
		return ok && rt.Load.CurrentSessions == 3
	})

	// ListTargets joins the registry capabilities/load/status.
	targets := h.ListTargets()
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	tg := targets[0]
	if tg.Capabilities == nil || tg.Capabilities.MaxSessions != 6 {
		t.Fatalf("target capabilities not surfaced: %+v", tg.Capabilities)
	}
	if tg.Version != "v0.1.5" {
		t.Fatalf("target version = %q, want v0.1.5", tg.Version)
	}
	if tg.Load == nil || tg.Load.CurrentSessions != 3 {
		t.Fatalf("target load not surfaced: %+v", tg.Load)
	}
	if tg.Status != string(registry.StatusOnline) {
		t.Fatalf("status = %q, want online", tg.Status)
	}

	// Disconnect removes the runtime from the registry.
	close(st.in)
	waitFor(t, func() bool {
		_, ok := h.Registry().Get("t1")
		return !ok
	})
}

func TestRouteStreamReportsTargetDisconnect(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeTunnelStream{ctx: ctx, in: make(chan *pb.TunnelMsg, 8), out: make(chan *pb.TunnelMsg, 8)}
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(st) }()
	st.in <- &pb.TunnelMsg{Kind: "register", Payload: mustMarshal(t, &pb.RegisterPayload{TargetId: "t1", TargetName: "box"})}
	waitFor(t, func() bool { _, ok := h.Registry().Get("t1"); return ok })

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- h.RouteStream(context.Background(), "t1", "StreamSubtask", map[string]string{"id": "task-1"}, func(*pb.TunnelMsg) error { return nil })
	}()
	select {
	case <-st.out:
	case <-time.After(time.Second):
		t.Fatal("stream request was not sent")
	}
	close(st.in)
	select {
	case err := <-streamDone:
		if err == nil || !strings.Contains(err.Error(), "disconnected") {
			t.Fatalf("RouteStream error = %v, want target disconnected", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RouteStream did not return after target disconnect")
	}
	<-serveDone
}

func TestSlowRouteStreamDoesNotBlockUnrelatedUnaryResponse(t *testing.T) {
	defer func(prev int) { streamBufferSize = prev }(streamBufferSize)
	streamBufferSize = 8

	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeTunnelStream{ctx: ctx, in: make(chan *pb.TunnelMsg, 128), out: make(chan *pb.TunnelMsg, 128)}
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(st) }()
	st.in <- &pb.TunnelMsg{Kind: "register", Payload: mustMarshal(t, &pb.RegisterPayload{TargetId: "t1", TargetName: "box"})}
	waitFor(t, func() bool { _, ok := h.Registry().Get("t1"); return ok })

	release := make(chan struct{})
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- h.RouteStream(context.Background(), "t1", "Attach", nil, func(*pb.TunnelMsg) error {
			<-release
			return nil
		})
	}()
	streamReq := <-st.out
	for i := 0; i < 40; i++ {
		st.in <- &pb.TunnelMsg{Kind: "data", RequestId: streamReq.RequestId}
	}

	unaryDone := make(chan error, 1)
	go func() {
		_, err := h.Route(context.Background(), "t1", "Info", nil)
		unaryDone <- err
	}()
	unaryReq := <-st.out
	st.in <- &pb.TunnelMsg{Kind: "resp", RequestId: unaryReq.RequestId}
	select {
	case err := <-unaryDone:
		if err != nil {
			t.Fatalf("unrelated unary route: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow stream blocked unrelated unary response")
	}

	close(release)
	select {
	case err := <-streamDone:
		if err == nil || !strings.Contains(err.Error(), "buffer") {
			t.Fatalf("slow stream error = %v, want explicit buffer overflow", err)
		}
	case <-time.After(time.Second):
		t.Fatal("overflowed stream did not terminate")
	}
	close(st.in)
	<-serveDone
}

func TestRouteStreamDrainsBufferedFramesBeforeOverflowError(t *testing.T) {
	defer func(prev int) { streamBufferSize = prev }(streamBufferSize)
	streamBufferSize = 4

	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeTunnelStream{ctx: ctx, in: make(chan *pb.TunnelMsg, 128), out: make(chan *pb.TunnelMsg, 128)}
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(st) }()
	st.in <- &pb.TunnelMsg{Kind: "register", Payload: mustMarshal(t, &pb.RegisterPayload{TargetId: "t1", TargetName: "box"})}
	waitFor(t, func() bool { _, ok := h.Registry().Get("t1"); return ok })

	// The consumer blocks on the first frame until release, so the buffer fills
	// and later frames overflow while a backlog is retained.
	release := make(chan struct{})
	var got []int
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- h.RouteStream(context.Background(), "t1", "Attach", nil, func(m *pb.TunnelMsg) error {
			<-release
			var n int
			_ = json.Unmarshal(m.Payload, &n)
			got = append(got, n)
			return nil
		})
	}()
	streamReq := <-st.out
	for i := 0; i < 40; i++ {
		st.in <- &pb.TunnelMsg{Kind: "data", RequestId: streamReq.RequestId, Payload: mustMarshal(t, i)}
	}

	// Wait until the overflow has been signaled (the stream has been dropped from
	// the target's pending map), then let the consumer proceed and drain.
	waitFor(t, func() bool {
		h.mu.RLock()
		e, ok := h.targets["t1"]
		h.mu.RUnlock()
		if !ok {
			return false
		}
		e.mu.Lock()
		_, pending := e.pending[streamReq.RequestId]
		e.mu.Unlock()
		return !pending
	})
	close(release)

	select {
	case err := <-streamDone:
		if err == nil || !strings.Contains(err.Error(), "buffer") {
			t.Fatalf("stream error = %v, want buffer overflow", err)
		}
	case <-time.After(time.Second):
		t.Fatal("overflowed stream did not terminate")
	}

	// Drained frames must be a gap-free in-order prefix, and at least a full
	// buffer's worth must survive — the pre-drain behavior delivered ~1 frame.
	if len(got) < streamBufferSize {
		t.Fatalf("delivered %d frames, want >= %d (buffered backlog must be drained)", len(got), streamBufferSize)
	}
	for i, n := range got {
		if n != i {
			t.Fatalf("frame %d out of order: got payload %d, want %d (delivery must stay ordered)", i, n, i)
		}
	}
}

func TestServeRejectsUnauthorizedRegister(t *testing.T) {
	h := NewHub()
	h.SetAuthorizer(registry.NewAuthorizer(registry.AuthorizerConfig{SharedToken: "s3cret"}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeTunnelStream{ctx: ctx, in: make(chan *pb.TunnelMsg, 4), out: make(chan *pb.TunnelMsg, 4)}

	errCh := make(chan error, 1)
	go func() { errCh <- h.Serve(st) }()

	st.in <- &pb.TunnelMsg{Kind: "register", Payload: mustMarshal(t, &pb.RegisterPayload{
		TargetId: "t1", TargetName: "box", Token: "wrong",
	})}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected register to be denied")
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return on denied register")
	}
	if _, ok := h.Registry().Get("t1"); ok {
		t.Fatal("denied runtime should not be registered")
	}
}
