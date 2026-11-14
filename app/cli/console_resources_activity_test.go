package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/gateway"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type consoleActivityStream struct {
	grpc.ClientStream
	ctx    context.Context
	events []*pb.AttachEvent
	index  int
	wait   bool
	onDone func()
}

func (s *consoleActivityStream) Recv() (*pb.AttachEvent, error) {
	if s.wait {
		<-s.ctx.Done()
		return nil, s.ctx.Err()
	}
	if s.index >= len(s.events) {
		return nil, io.EOF
	}
	event := s.events[s.index]
	s.index++
	if s.index == len(s.events) && s.onDone != nil {
		s.onDone()
	}
	return event, nil
}
func consoleActivityCaughtUp(epoch string, seq uint64) *pb.AttachEvent {
	cursor, _ := agentstream.EncodeCursor(agentstream.Cursor{Epoch: epoch, Seq: seq})
	return &pb.AttachEvent{CaughtUp: true, Cursor: cursor}
}

// Existing resource service fixtures now offer an empty canonical journal;
// production fixtures below exercise nonempty, failed and interrupted replay.
func (f *consoleResourceHub) Attach(ctx context.Context, _ *pb.AmuxdAttachRequest, _ ...grpc.CallOption) (pb.Amuxd_AttachClient, error) {
	return &consoleActivityStream{ctx: ctx, events: []*pb.AttachEvent{consoleActivityCaughtUp("empty-epoch", 0)}}, nil
}
func consoleActivityEvent(t *testing.T, seq uint64, turn *agentstream.TurnRef, body agentstream.Body) *pb.AttachEvent {
	t.Helper()
	event := agentstream.Event{SessionID: "session-id", Cursor: agentstream.Cursor{Epoch: "epoch-1", Seq: seq}, Turn: turn, At: time.Unix(10, 0), Body: body}
	raw, err := agentstream.MarshalEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	return &pb.AttachEvent{CanonicalJson: raw, Historical: true}
}

type consoleActivityHub struct {
	pb.AmuxdClient
	events    []*pb.AttachEvent
	attachErr error
	wait      bool
	delay     time.Duration
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	requests  []*pb.AmuxdAttachRequest
}

func (h *consoleActivityHub) Attach(ctx context.Context, req *pb.AmuxdAttachRequest, _ ...grpc.CallOption) (pb.Amuxd_AttachClient, error) {
	h.mu.Lock()
	h.calls++
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	done := func() { h.mu.Lock(); h.active--; h.mu.Unlock() }
	if h.attachErr != nil {
		done()
		return nil, h.attachErr
	}
	if h.delay > 0 {
		select {
		case <-ctx.Done():
			done()
			return nil, ctx.Err()
		case <-time.After(h.delay):
		}
	}
	if h.wait {
		go func() { <-ctx.Done(); done() }()
	}
	return &consoleActivityStream{ctx: ctx, events: h.events, wait: h.wait, onDone: done}, nil
}
func consoleActivityEntry() *gateway.SessionEntry {
	return &gateway.SessionEntry{Session: &pb.Session{Id: "session-id", Name: "chat", State: "running"}, TargetID: "runtime-a"}
}

func TestConsoleResourceActivityCanonicalTurnAndPermissions(t *testing.T) {
	turn := &agentstream.TurnRef{ID: "turn-1", Seq: 1}
	start := consoleActivityEvent(t, 1, turn, agentstream.TurnStarted{InputSummary: "not an activity heuristic"})
	request := consoleActivityEvent(t, 2, turn, agentstream.PermissionRequested{Request: agentstream.PermissionRequest{ID: "permission-1", ToolCall: &agentstream.PermissionToolCall{Title: "TOKEN=private", RawInput: json.RawMessage(`{"secret":"private"}`)}}})
	result := consoleActivityEvent(t, 3, turn, agentstream.ResultObserved{Text: "done"})
	hub := &consoleActivityHub{events: []*pb.AttachEvent{start, request, result, consoleActivityCaughtUp("epoch-1", 3)}}
	backend := &consoleBackend{hub: hub}
	views := backend.consoleEnrichActivity(context.Background(), []*gateway.SessionEntry{consoleActivityEntry()})
	view := views[0]
	if view.ActivityStatus != "known" || view.TurnState != "waiting_permission" || !view.ActiveTurn || view.PendingPermissions != 1 || view.PendingPermissionIDs[0] != "permission-1" {
		t.Fatalf("activity=%+v", view)
	}
	if hub.requests[0].TimelineMode != pb.AttachRequest_HISTORY || hub.requests[0].AfterCursor != "" || hub.requests[0].TargetId != "runtime-a" || hub.requests[0].Name != "chat" {
		t.Fatalf("routing=%v", hub.requests[0])
	}
	raw := consoleTestJSON(t, view)
	if strings.Contains(raw, "private") || !strings.Contains(raw, `"name":"chat"`) || !strings.Contains(raw, `"active_turn":true`) {
		t.Fatalf("unsafe/incorrect JSON=%s", raw)
	}
	// ResultObserved did not finish the turn. Permission resolution changes only
	// the wait status; the canonical TurnFinished is the terminal authority.
	resolved := consoleActivityEvent(t, 4, turn, agentstream.PermissionResolved{RequestID: "permission-1"})
	hub.events = []*pb.AttachEvent{start, request, result, resolved, consoleActivityCaughtUp("epoch-1", 4)}
	facts := backend.consoleReadActivity(context.Background(), consoleActivityEntry())
	if facts == nil || facts.TurnState != "running" || !facts.ActiveTurn || facts.PendingPermissions != 0 {
		t.Fatalf("resolved facts=%+v", facts)
	}
	finish := consoleActivityEvent(t, 5, turn, agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeError, Message: "private-diagnostic"}})
	hub.events = []*pb.AttachEvent{start, request, result, resolved, finish, consoleActivityCaughtUp("epoch-1", 5)}
	facts = backend.consoleReadActivity(context.Background(), consoleActivityEntry())
	if facts == nil || facts.TurnState != "failed" || facts.ActiveTurn || facts.PendingPermissions != 0 {
		t.Fatalf("terminal facts=%+v", facts)
	}
	if strings.Contains(consoleTestJSON(t, facts), "private-diagnostic") {
		t.Fatal("terminal diagnostic leaked")
	}
}
func TestConsoleResourceActivityFailClosedWithoutCompleteCanonicalReplay(t *testing.T) {
	turn := &agentstream.TurnRef{ID: "turn-1", Seq: 1}
	start := consoleActivityEvent(t, 1, turn, agentstream.TurnStarted{})
	tests := []struct {
		name   string
		events []*pb.AttachEvent
	}{
		{"no caught up", []*pb.AttachEvent{start}},
		{"raw text is not fact", []*pb.AttachEvent{{Text: "permission requested and done", Kind: pb.AttachEvent_OUTPUT}, consoleActivityCaughtUp("epoch-1", 0)}},
		{"corrupt canonical", []*pb.AttachEvent{{CanonicalJson: []byte(`{"kind":"turn_finished"}`)}, consoleActivityCaughtUp("epoch-1", 1)}},
		{"missing event", []*pb.AttachEvent{start, consoleActivityCaughtUp("epoch-1", 2)}},
		{"wrong epoch", []*pb.AttachEvent{start, consoleActivityCaughtUp("other", 1)}},
		{"duplicate cursor", []*pb.AttachEvent{start, start, consoleActivityCaughtUp("epoch-1", 1)}},
		{"terminal without start", []*pb.AttachEvent{consoleActivityEvent(t, 1, turn, agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}}), consoleActivityCaughtUp("epoch-1", 1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &consoleBackend{hub: &consoleActivityHub{events: tt.events}}
			view := backend.consoleEnrichActivity(context.Background(), []*gateway.SessionEntry{consoleActivityEntry()})[0]
			if view.ActivityStatus != "unknown" || view.consoleActivityFacts != nil {
				t.Fatalf("partial replay became fact: %+v", view)
			}
			raw := consoleTestJSON(t, view)
			for _, key := range []string{`"turn_state"`, `"active_turn"`, `"pending_permissions"`} {
				if strings.Contains(raw, key) {
					t.Fatalf("unknown field fabricated in %s", raw)
				}
			}
		})
	}
}
func TestConsoleResourceActivityBoundedConcurrencyFleetAndTimeout(t *testing.T) {
	hub := &consoleActivityHub{events: []*pb.AttachEvent{consoleActivityCaughtUp("epoch-1", 0)}, delay: time.Millisecond}
	backend := &consoleBackend{hub: hub}
	entries := make([]*gateway.SessionEntry, 25)
	for index := range entries {
		entry := consoleActivityEntry()
		entry.Session.UpdatedAt = int64(index)
		entries[index] = entry
	}
	entries[24].Session.State = "archived"
	views := backend.consoleEnrichActivity(context.Background(), entries)
	if hub.calls != consoleActivityMaxSessions || hub.maxActive > consoleActivityConcurrency {
		t.Fatalf("unbounded calls=%d active=%d", hub.calls, hub.maxActive)
	}
	known := 0
	for _, view := range views {
		if view.ActivityStatus == "known" {
			known++
		}
	}
	if known != consoleActivityMaxSessions || views[0].ActivityStatus != "unknown" || views[24].ActivityStatus != "unknown" {
		t.Fatal("fleet bound or archived exclusion failed")
	}
	hub = &consoleActivityHub{wait: true}
	backend.hub = hub
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	views = backend.consoleEnrichActivity(ctx, entries)
	if time.Since(started) > time.Second {
		t.Fatal("activity did not respect caller deadline")
	}
	for _, view := range views {
		if view.consoleActivityFacts != nil {
			t.Fatal("timeout published partial facts")
		}
	}
	if hub.calls > consoleActivityConcurrency {
		t.Fatalf("started requests after deadline: %d", hub.calls)
	}
}
func TestConsoleResourceActivityInspectsLiveSessionsBeforeRecentSleepingOnes(t *testing.T) {
	hub := &consoleActivityHub{events: []*pb.AttachEvent{consoleActivityCaughtUp("epoch-1", 0)}}
	backend := &consoleBackend{hub: hub}
	entries := make([]*gateway.SessionEntry, 2*consoleActivityMaxSessions)
	for index := range entries {
		entry := consoleActivityEntry()
		entry.Session.State = "sleeping"
		entry.Session.UpdatedAt = int64(1000 + index) // newer sleeping Sessions outrank by recency alone
		entries[index] = entry
	}
	// The only Sessions that can be mid-Turn are also the least recently active.
	entries[0].Session.State = "running"
	entries[1].Session.State = "created"
	views := backend.consoleEnrichActivity(context.Background(), entries)
	if views[0].ActivityStatus != "known" || views[1].ActivityStatus != "known" {
		t.Fatal("a live Session was starved by newer sleeping Sessions")
	}
	known := 0
	for _, view := range views {
		if view.ActivityStatus == "known" {
			known++
		}
	}
	if known != consoleActivityMaxSessions || hub.calls != consoleActivityMaxSessions {
		t.Fatalf("budget not spent on recent sleeping Sessions: known=%d calls=%d", known, hub.calls)
	}
}
func TestConsoleResourceActivityEmptyJournalAndError(t *testing.T) {
	hub := &consoleActivityHub{events: []*pb.AttachEvent{consoleActivityCaughtUp("epoch-1", 0)}}
	backend := &consoleBackend{hub: hub}
	facts := backend.consoleReadActivity(context.Background(), consoleActivityEntry())
	if facts == nil || facts.TurnState != "idle" || facts.ActiveTurn || facts.PendingPermissions != 0 {
		t.Fatalf("empty journal=%+v", facts)
	}
	hub.attachErr = errors.New("private transport error")
	if backend.consoleReadActivity(context.Background(), consoleActivityEntry()) != nil {
		t.Fatal("transport error became activity")
	}
}
func TestConsoleResourceActivitySnapshotsKeepGatewayShape(t *testing.T) {
	consoleTestOperator(t)
	backend := &consoleBackend{hub: consoleTestHub()}
	for _, resource := range []string{"overview", "sessions"} {
		value, err := backend.snapshotResource(context.Background(), resource, url.Values{})
		if err != nil {
			t.Fatal(err)
		}
		raw := consoleTestJSON(t, value)
		for _, expected := range []string{`"activity_status":"known"`, `"turn_state":"idle"`, `"active_turn":false`, `"pending_permissions":0`, `"name":"chat"`, `"target_id":"runtime-a"`, `"activity_coverage":{"known":1,"unknown":0}`} {
			if !strings.Contains(raw, expected) {
				t.Fatalf("missing %s in %s", expected, raw)
			}
		}
	}
}

func TestConsoleBackendAttachResumesAfterTheBrowserCursor(t *testing.T) {
	hub := &consoleActivityHub{events: []*pb.AttachEvent{consoleActivityCaughtUp("epoch-1", 0)}}
	backend := &consoleBackend{hub: hub}
	if _, err := backend.Attach(context.Background(), "runtime-a", "chat", "epoch-1:7"); err != nil {
		t.Fatal(err)
	}
	if len(hub.requests) != 1 {
		t.Fatalf("attach requests = %d", len(hub.requests))
	}
	request := hub.requests[0]
	if request.GetAfterCursor() != "epoch-1:7" || request.GetTimelineMode() != pb.AttachRequest_FOLLOW || request.GetTargetId() != "runtime-a" || request.GetName() != "chat" {
		t.Fatalf("attach request = %+v", request)
	}
}
