package registry

import (
	"errors"
	"testing"
	"time"
)

// fakeReserver is a Reserver backed by per-target Admissions, so scheduler tests
// exercise the real two-level quota logic. failTargets force Reserve errors.
type fakeReserver struct {
	adm         map[string]*Admission
	failTargets map[string]bool
	reserveLog  []string
}

func newFakeReserver() *fakeReserver {
	return &fakeReserver{adm: map[string]*Admission{}, failTargets: map[string]bool{}}
}

func (f *fakeReserver) set(targetID string, caps Capabilities) {
	f.adm[targetID] = NewAdmissionFromCaps(caps, 0)
}

func (f *fakeReserver) Reserve(targetID, agentType string) (string, error) {
	f.reserveLog = append(f.reserveLog, targetID)
	if f.failTargets[targetID] {
		return "", errors.New("transient")
	}
	a, ok := f.adm[targetID]
	if !ok {
		return "", errors.New("unknown target")
	}
	return a.Reserve(agentType)
}

func (f *fakeReserver) Commit(targetID, reservationID string) error {
	return f.adm[targetID].Commit(reservationID)
}

func (f *fakeReserver) Release(targetID, reservationID string) error {
	f.adm[targetID].Release(reservationID)
	return nil
}

func TestSchedulerAcquireNoMatch(t *testing.T) {
	reg := New(Options{Clock: func() time.Time { return time.Unix(1, 0) }})
	s := NewScheduler(reg, newFakeReserver())
	if _, err := s.Acquire(ScheduleRequest{AgentType: "claude"}); err != ErrNoMatchingRuntime {
		t.Fatalf("err = %v, want ErrNoMatchingRuntime", err)
	}
}

func TestSchedulerAcquirePicksLeastLoadedAndCommits(t *testing.T) {
	now := time.Unix(1, 0)
	reg := New(Options{Clock: func() time.Time { return now }})
	fr := newFakeReserver()
	for _, id := range []string{"busy", "idle"} {
		caps := testCaps("claude", 4, 10)
		reg.Register(id, id, caps)
		fr.set(id, caps)
	}
	reg.Heartbeat("busy", Load{CurrentSessions: 9})
	reg.Heartbeat("idle", Load{CurrentSessions: 0})

	s := NewScheduler(reg, fr)
	p, err := s.Acquire(ScheduleRequest{AgentType: "claude"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if p.TargetID != "idle" {
		t.Fatalf("placed on %q, want idle (least loaded)", p.TargetID)
	}
	if err := s.Commit(p); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := fr.adm["idle"].Snapshot(); got.CurrentSessions != 1 {
		t.Fatalf("idle running = %d, want 1 after commit", got.CurrentSessions)
	}
}

func TestSchedulerAcquireFallsBackWhenFirstAtCapacity(t *testing.T) {
	now := time.Unix(1, 0)
	reg := New(Options{Clock: func() time.Time { return now }})
	fr := newFakeReserver()
	// "full" ranks first by load but its admission is already exhausted.
	full := testCaps("claude", 1, 1)
	reg.Register("full", "full", full)
	fr.set("full", full)
	if _, err := fr.adm["full"].Reserve("claude"); err != nil { // occupy the only slot
		t.Fatalf("pre-occupy: %v", err)
	}
	spare := testCaps("claude", 2, 4)
	reg.Register("spare", "spare", spare)
	fr.set("spare", spare)
	reg.Heartbeat("full", Load{CurrentSessions: 0})  // looks idle by stale heartbeat
	reg.Heartbeat("spare", Load{CurrentSessions: 1}) // looks busier

	s := NewScheduler(reg, fr)
	p, err := s.Acquire(ScheduleRequest{AgentType: "claude"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if p.TargetID != "spare" {
		t.Fatalf("placed on %q, want spare (full rejected reservation)", p.TargetID)
	}
	// It must have tried "full" first (stale heartbeat ranked it ahead).
	if len(fr.reserveLog) < 2 || fr.reserveLog[0] != "full" {
		t.Fatalf("reserve order = %v, want full tried first then spare", fr.reserveLog)
	}
}

func TestSchedulerAcquireNoCapacityAnywhere(t *testing.T) {
	now := time.Unix(1, 0)
	reg := New(Options{Clock: func() time.Time { return now }})
	fr := newFakeReserver()
	caps := testCaps("claude", 1, 1)
	reg.Register("a", "a", caps)
	fr.set("a", caps)
	if _, err := fr.adm["a"].Reserve("claude"); err != nil {
		t.Fatalf("pre-occupy: %v", err)
	}

	s := NewScheduler(reg, fr)
	if _, err := s.Acquire(ScheduleRequest{AgentType: "claude"}); err != ErrNoCapacityAnywhere {
		t.Fatalf("err = %v, want ErrNoCapacityAnywhere", err)
	}
}

func TestSchedulerSkipsTransientReserveFailure(t *testing.T) {
	now := time.Unix(1, 0)
	reg := New(Options{Clock: func() time.Time { return now }})
	fr := newFakeReserver()
	for _, id := range []string{"flaky", "good"} {
		caps := testCaps("claude", 2, 4)
		reg.Register(id, id, caps)
		fr.set(id, caps)
	}
	reg.Heartbeat("flaky", Load{CurrentSessions: 0}) // ranked first
	reg.Heartbeat("good", Load{CurrentSessions: 1})
	fr.failTargets["flaky"] = true

	s := NewScheduler(reg, fr)
	p, err := s.Acquire(ScheduleRequest{AgentType: "claude"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if p.TargetID != "good" {
		t.Fatalf("placed on %q, want good (flaky errored)", p.TargetID)
	}
}

func TestSchedulerStickyHit(t *testing.T) {
	now := time.Unix(1, 0)
	reg := New(Options{Clock: func() time.Time { return now }})
	fr := newFakeReserver()
	for _, id := range []string{"a", "b"} {
		caps := testCaps("claude", 2, 4)
		reg.Register(id, id, caps)
		fr.set(id, caps)
	}
	// "a" is idle (would rank first by load), "b" is busier. Sticky asks for b.
	reg.Heartbeat("a", Load{CurrentSessions: 0})
	reg.Heartbeat("b", Load{CurrentSessions: 2})

	s := NewScheduler(reg, fr)
	p, err := s.Acquire(ScheduleRequest{AgentType: "claude", AgentInstanceID: "inst-1", StickyTargetID: "b"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if p.TargetID != "b" {
		t.Fatalf("placed on %q, want b (sticky)", p.TargetID)
	}
	if len(fr.reserveLog) != 1 || fr.reserveLog[0] != "b" {
		t.Fatalf("reserve order = %v, want only b (sticky short-circuit)", fr.reserveLog)
	}
}

func TestSchedulerStickyRejectsFallsBack(t *testing.T) {
	now := time.Unix(1, 0)
	reg := New(Options{Clock: func() time.Time { return now }})
	fr := newFakeReserver()
	for _, id := range []string{"sticky", "spare"} {
		caps := testCaps("claude", 2, 4)
		reg.Register(id, id, caps)
		fr.set(id, caps)
	}
	reg.Heartbeat("sticky", Load{CurrentSessions: 0})
	reg.Heartbeat("spare", Load{CurrentSessions: 1})
	// Sticky target rejects the reservation (at quota / transient).
	fr.failTargets["sticky"] = true

	s := NewScheduler(reg, fr)
	p, err := s.Acquire(ScheduleRequest{AgentType: "claude", AgentInstanceID: "inst-1", StickyTargetID: "sticky"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if p.TargetID != "spare" {
		t.Fatalf("placed on %q, want spare (sticky rejected -> fallback)", p.TargetID)
	}
	// Must have tried sticky first, then fallen back to the ranked walk.
	if len(fr.reserveLog) < 2 || fr.reserveLog[0] != "sticky" {
		t.Fatalf("reserve order = %v, want sticky tried first", fr.reserveLog)
	}
}

func TestSchedulerStickyOfflineFallsBack(t *testing.T) {
	now := time.Unix(1, 0)
	reg := New(Options{Clock: func() time.Time { return now }})
	fr := newFakeReserver()
	for _, id := range []string{"dead", "live"} {
		caps := testCaps("claude", 2, 4)
		reg.Register(id, id, caps)
		fr.set(id, caps)
	}
	reg.Heartbeat("live", Load{CurrentSessions: 0})
	reg.SetStatus("dead", StatusOffline) // sticky target is offline

	s := NewScheduler(reg, fr)
	p, err := s.Acquire(ScheduleRequest{AgentType: "claude", AgentInstanceID: "inst-1", StickyTargetID: "dead"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if p.TargetID != "live" {
		t.Fatalf("placed on %q, want live (sticky offline -> fallback)", p.TargetID)
	}
	if len(fr.reserveLog) != 1 || fr.reserveLog[0] != "live" {
		t.Fatalf("reserve order = %v, want only live (sticky skipped, not reserved)", fr.reserveLog)
	}
}

func TestSchedulerNoStickyIsOriginalLogic(t *testing.T) {
	now := time.Unix(1, 0)
	reg := New(Options{Clock: func() time.Time { return now }})
	fr := newFakeReserver()
	for _, id := range []string{"a", "b"} {
		caps := testCaps("claude", 2, 4)
		reg.Register(id, id, caps)
		fr.set(id, caps)
	}
	reg.Heartbeat("a", Load{CurrentSessions: 0})
	reg.Heartbeat("b", Load{CurrentSessions: 2})

	s := NewScheduler(reg, fr)
	p, err := s.Acquire(ScheduleRequest{AgentType: "claude"}) // no sticky fields
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if p.TargetID != "a" {
		t.Fatalf("placed on %q, want a (least loaded, no sticky)", p.TargetID)
	}
}
