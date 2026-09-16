package registry

import (
	"fmt"
	"testing"
	"time"
)

func seqIDGen() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("rsv-%d", n)
	}
}

func TestAdmissionTwoLevelQuota(t *testing.T) {
	// Whole machine allows 3 sessions; claude capped at 2, codex at 2.
	a := NewAdmission(AdmissionConfig{
		MaxSessions: 3,
		PerAgentMax: map[string]int{"claude": 2, "codex": 2},
		IDGen:       seqIDGen(),
	})

	// Two claude reservations succeed.
	r1, err := a.Reserve("claude")
	if err != nil {
		t.Fatalf("reserve 1: %v", err)
	}
	if _, err := a.Reserve("claude"); err != nil {
		t.Fatalf("reserve 2: %v", err)
	}
	// Third claude exceeds per-agent max_concurrency even though whole-machine
	// still has a slot.
	if _, err := a.Reserve("claude"); err != ErrNoCapacity {
		t.Fatalf("reserve 3 claude err = %v, want ErrNoCapacity (per-agent)", err)
	}
	// A codex reservation still fits the last whole-machine slot.
	if _, err := a.Reserve("codex"); err != nil {
		t.Fatalf("reserve codex: %v", err)
	}
	// Now whole machine is full (2 claude + 1 codex = 3).
	if _, err := a.Reserve("codex"); err != ErrNoCapacity {
		t.Fatalf("reserve 2nd codex err = %v, want ErrNoCapacity (whole machine)", err)
	}

	// Releasing one claude frees both a whole slot and a claude slot.
	a.Release(r1)
	if _, err := a.Reserve("claude"); err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
}

func TestAdmissionUnsupportedAgent(t *testing.T) {
	a := NewAdmission(AdmissionConfig{MaxSessions: 5, PerAgentMax: map[string]int{"claude": 1}})
	if _, err := a.Reserve("codex"); err != ErrAgentNotSupported {
		t.Fatalf("err = %v, want ErrAgentNotSupported", err)
	}
}

func TestAdmissionCommitIdempotent(t *testing.T) {
	a := NewAdmission(AdmissionConfig{MaxSessions: 1, PerAgentMax: map[string]int{"claude": 1}, IDGen: seqIDGen()})
	id, err := a.Reserve("claude")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := a.Commit(id); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Idempotent: second commit succeeds and does not double-count (still 1 slot).
	if err := a.Commit(id); err != nil {
		t.Fatalf("commit again: %v", err)
	}
	snap := a.Snapshot()
	if snap.CurrentSessions != 1 || snap.ReservedSlots != 0 {
		t.Fatalf("snapshot after double commit = %+v, want 1 running 0 reserved", snap)
	}
	// Whole machine is full at 1.
	if _, err := a.Reserve("claude"); err != ErrNoCapacity {
		t.Fatalf("reserve when full err = %v, want ErrNoCapacity", err)
	}
}

func TestAdmissionCommitUnknown(t *testing.T) {
	a := NewAdmission(AdmissionConfig{MaxSessions: 1, PerAgentMax: map[string]int{"claude": 1}})
	if err := a.Commit("nope"); err != ErrReservationNotFound {
		t.Fatalf("err = %v, want ErrReservationNotFound", err)
	}
}

func TestAdmissionExpireReservations(t *testing.T) {
	cur := time.Unix(1000, 0)
	a := NewAdmission(AdmissionConfig{
		MaxSessions:    2,
		PerAgentMax:    map[string]int{"claude": 2},
		ReservationTTL: 30 * time.Second,
		Clock:          func() time.Time { return cur },
		IDGen:          seqIDGen(),
	})
	stale, _ := a.Reserve("claude")
	committed, _ := a.Reserve("claude")
	if err := a.Commit(committed); err != nil {
		t.Fatalf("commit: %v", err)
	}

	cur = cur.Add(45 * time.Second)
	expired := a.ExpireReservations()
	if len(expired) != 1 || expired[0] != stale {
		t.Fatalf("expired = %v, want [%s]", expired, stale)
	}
	snap := a.Snapshot()
	if snap.ReservedSlots != 0 || snap.CurrentSessions != 1 {
		t.Fatalf("snapshot after expire = %+v, want 0 reserved 1 running (committed not expired)", snap)
	}
}

func TestAdmissionSnapshot(t *testing.T) {
	a := NewAdmission(AdmissionConfig{MaxSessions: 5, PerAgentMax: map[string]int{"claude": 3, "codex": 2}, IDGen: seqIDGen()})
	c1, _ := a.Reserve("claude")
	a.Reserve("claude")
	a.Reserve("codex")
	_ = a.Commit(c1)

	snap := a.Snapshot()
	if snap.CurrentSessions != 1 {
		t.Fatalf("current = %d, want 1", snap.CurrentSessions)
	}
	if snap.ReservedSlots != 2 {
		t.Fatalf("reserved = %d, want 2", snap.ReservedSlots)
	}
	if snap.PerAgentRunning["claude"] != 1 {
		t.Fatalf("claude running = %d, want 1", snap.PerAgentRunning["claude"])
	}
	if snap.PerAgentReserved["claude"] != 1 || snap.PerAgentReserved["codex"] != 1 {
		t.Fatalf("per-agent reserved = %+v, want claude:1 codex:1", snap.PerAgentReserved)
	}
}

func TestAdmissionFromCaps(t *testing.T) {
	caps := Capabilities{
		Agents:      []AgentCapacity{{Type: "claude", MaxConcurrency: 1}},
		MaxSessions: 4,
	}
	a := NewAdmissionFromCaps(caps, 0)
	if _, err := a.Reserve("claude"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := a.Reserve("claude"); err != ErrNoCapacity {
		t.Fatalf("second reserve err = %v, want ErrNoCapacity", err)
	}
}

func TestAdmissionUnboundedWholeMachine(t *testing.T) {
	// MaxSessions <= 0 means only per-agent limits apply.
	a := NewAdmission(AdmissionConfig{MaxSessions: 0, PerAgentMax: map[string]int{"claude": 100}, IDGen: seqIDGen()})
	for i := 0; i < 50; i++ {
		if _, err := a.Reserve("claude"); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
}
