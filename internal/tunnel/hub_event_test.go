package tunnel

import (
	"testing"
	"time"

	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// waitEvent waits up to 1s for a single SessionEvent to arrive on ch.
func waitEvent(t *testing.T, ch <-chan SessionEvent) SessionEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for SessionEvent")
		return SessionEvent{}
	}
}

func TestFireSessionEvent_Added(t *testing.T) {
	h := NewHub()

	ch := make(chan SessionEvent, 4)
	h.SetSessionEventHandler(func(ev SessionEvent) { ch <- ev })

	prev := []*pb.Session{}
	next := []*pb.Session{
		{Id: "s1", Name: "impl", AgentType: "claude"},
	}
	h.fireSessionEvent("sb-1", "mybox", prev, next)

	ev := waitEvent(t, ch)
	if len(ev.Added) != 1 || ev.Added[0].Name != "impl" {
		t.Errorf("unexpected added sessions: %v", ev.Added)
	}
	if len(ev.Removed) != 0 {
		t.Errorf("unexpected removed sessions: %v", ev.Removed)
	}
}

func TestFireSessionEvent_RunningTransitionIsAdded(t *testing.T) {
	h := NewHub()

	ch := make(chan SessionEvent, 4)
	h.SetSessionEventHandler(func(ev SessionEvent) { ch <- ev })

	prev := []*pb.Session{
		{Id: "s1", Name: "impl", AgentType: "demo-agent", State: "created"},
	}
	next := []*pb.Session{
		{Id: "s1", Name: "impl", AgentType: "demo-agent", State: "running"},
	}
	h.fireSessionEvent("sb-1", "mybox", prev, next)

	ev := waitEvent(t, ch)
	if len(ev.Added) != 1 || ev.Added[0].Name != "impl" || ev.Added[0].State != "running" {
		t.Errorf("unexpected added sessions: %v", ev.Added)
	}
	if len(ev.Removed) != 0 {
		t.Errorf("unexpected removed sessions: %v", ev.Removed)
	}
}

func TestFireSessionEvent_Removed(t *testing.T) {
	h := NewHub()

	ch := make(chan SessionEvent, 4)
	h.SetSessionEventHandler(func(ev SessionEvent) { ch <- ev })

	prev := []*pb.Session{{Id: "s1", Name: "impl"}}
	next := []*pb.Session{}
	h.fireSessionEvent("sb-1", "mybox", prev, next)

	ev := waitEvent(t, ch)
	if len(ev.Removed) != 1 || ev.Removed[0].Name != "impl" {
		t.Errorf("unexpected removed sessions: %v", ev.Removed)
	}
}

func TestFireSessionEvent_NoDiff(t *testing.T) {
	h := NewHub()

	fired := make(chan struct{}, 1)
	h.SetSessionEventHandler(func(ev SessionEvent) { fired <- struct{}{} })

	same := []*pb.Session{{Id: "s1", Name: "impl"}}
	h.fireSessionEvent("sb-1", "mybox", same, same)

	// handler should NOT be called — give it a brief window then confirm silence
	select {
	case <-fired:
		t.Error("expected no event when sessions are unchanged")
	case <-time.After(100 * time.Millisecond):
		// ok — no event fired
	}
}

// TestFireSessionEvent_ArchivedTransition reports a session that transitioned
// into the archived state in ev.Archived, so amuxd can reclaim persistent agent
// instances whose owner session has ended (v0.39).
func TestFireSessionEvent_ArchivedTransition(t *testing.T) {
	h := NewHub()

	ch := make(chan SessionEvent, 4)
	h.SetSessionEventHandler(func(ev SessionEvent) { ch <- ev })

	prev := []*pb.Session{
		{Id: "owner-1", Name: "feishu-x", State: "running"},
	}
	next := []*pb.Session{
		{Id: "owner-1", Name: "feishu-x", State: "archived"},
	}
	h.fireSessionEvent("sb-1", "mybox", prev, next)

	ev := waitEvent(t, ch)
	if len(ev.Archived) != 1 || ev.Archived[0].Id != "owner-1" || ev.Archived[0].State != "archived" {
		t.Errorf("unexpected archived sessions: %v", ev.Archived)
	}
	// A running->archived transition is not an "added" (becameRunning) event.
	if len(ev.Added) != 0 {
		t.Errorf("unexpected added sessions: %v", ev.Added)
	}
	if len(ev.Removed) != 0 {
		t.Errorf("unexpected removed sessions: %v", ev.Removed)
	}
}

// TestFireSessionEvent_NewlyArchivedNotReported ensures a session that appears
// for the first time already archived (no prev) is not treated as an archived
// transition — there is no owner-lifecycle event to act on.
func TestFireSessionEvent_NewlyArchivedNotReported(t *testing.T) {
	h := NewHub()

	ch := make(chan SessionEvent, 4)
	h.SetSessionEventHandler(func(ev SessionEvent) { ch <- ev })

	prev := []*pb.Session{}
	next := []*pb.Session{
		{Id: "s1", Name: "old", State: "archived"},
	}
	h.fireSessionEvent("sb-1", "mybox", prev, next)

	ev := waitEvent(t, ch)
	if len(ev.Archived) != 0 {
		t.Errorf("unexpected archived sessions for first-appearance: %v", ev.Archived)
	}
	if len(ev.Added) != 1 {
		t.Errorf("expected the new session in added, got: %v", ev.Added)
	}
}

func TestRemoveSessionsUpdatesTargetCache(t *testing.T) {
	h := NewHub()
	entry := &targetEntry{
		id:           "target-1",
		name:         "devbox",
		capabilities: &registry.Capabilities{AgentTypes: []string{"codex"}, RuntimeTools: []string{"git"}},
		sessions: []*pb.Session{
			{Id: "s1", Name: "dead"},
			{Id: "s2", Name: "live"},
		},
		sendCh:  make(chan *pb.TunnelMsg, 1),
		pending: make(map[string]*pendingCall),
	}
	h.targets[entry.id] = entry

	ch := make(chan SessionEvent, 1)
	h.SetSessionEventHandler(func(ev SessionEvent) { ch <- ev })

	h.RemoveSessions("target-1", []string{"dead"})

	targets := h.ListTargets()
	if len(targets) != 1 || len(targets[0].Sessions) != 1 || targets[0].Sessions[0].Name != "live" {
		t.Fatalf("targets = %+v, want only live session", targets)
	}
	if targets[0].Capabilities == nil || targets[0].Capabilities.AgentTypes[0] != "codex" || targets[0].Capabilities.RuntimeTools[0] != "git" {
		t.Fatalf("target capabilities = %+v, want structured capability copy", targets[0].Capabilities)
	}
	ev := waitEvent(t, ch)
	if len(ev.Removed) != 1 || ev.Removed[0].Name != "dead" {
		t.Fatalf("removed event = %+v, want dead", ev.Removed)
	}
}
