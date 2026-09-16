package session

import (
	"testing"
	"time"
)

func mkSess(state State, agentSessionID, ownerTask string, lastInteraction time.Time) *Session {
	s := &Session{State: state, AgentSessionID: agentSessionID, OwnerTaskId: ownerTask}
	s.LastInteractionAt = lastInteraction
	return s
}

func TestReclaimAction(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	pol := ReclaimPolicy{SleepAfter: 48 * time.Hour, ArchiveAfter: 60 * time.Hour, PruneAfter: 120 * time.Hour}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	cases := []struct {
		name string
		sess *Session
		pol  ReclaimPolicy
		want ReclaimAction
	}{
		{"running fresh", mkSess(StateRunning, "agent-1", "", ago(time.Hour)), pol, ReclaimNone},
		{"running resumable idle→sleep", mkSess(StateRunning, "agent-1", "", ago(49*time.Hour)), pol, ReclaimSleep},
		{"running non-resumable idle→archive", mkSess(StateRunning, "", "", ago(61*time.Hour)), pol, ReclaimArchive},
		{"running non-resumable below archive", mkSess(StateRunning, "", "", ago(49*time.Hour)), pol, ReclaimNone},
		{"sleeping idle→archive", mkSess(StateSleeping, "agent-1", "", ago(61*time.Hour)), pol, ReclaimArchive},
		{"sleeping below archive", mkSess(StateSleeping, "agent-1", "", ago(49*time.Hour)), pol, ReclaimNone},
		{"archived idle→prune", mkSess(StateArchived, "agent-1", "", ago(121*time.Hour)), pol, ReclaimPrune},
		{"archived below prune", mkSess(StateArchived, "agent-1", "", ago(61*time.Hour)), pol, ReclaimNone},
		{"task-owned never", mkSess(StateRunning, "agent-1", "task-7", ago(200*time.Hour)), pol, ReclaimNone},
		{"created never", mkSess(StateCreated, "agent-1", "", ago(200*time.Hour)), pol, ReclaimNone},
		{"disabled policy (all zero)", mkSess(StateRunning, "agent-1", "", ago(200*time.Hour)), ReclaimPolicy{}, ReclaimNone},
		{"sleep disabled, archive enabled, resumable running", mkSess(StateRunning, "agent-1", "", ago(61*time.Hour)), ReclaimPolicy{ArchiveAfter: 60 * time.Hour, PruneAfter: 120 * time.Hour}, ReclaimArchive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.sess.ReclaimAction(now, c.pol); got != c.want {
				t.Fatalf("ReclaimAction = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReclaimActionPerSessionIdleOverride(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	pol := ReclaimPolicy{SleepAfter: 48 * time.Hour, ArchiveAfter: 60 * time.Hour, PruneAfter: 120 * time.Hour}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	mk := func(state State, agentSessionID string, idle time.Duration, idleTimeout int, idleTimeoutSet bool) *Session {
		s := mkSess(state, agentSessionID, "", ago(idle))
		s.IdleTimeoutSeconds = idleTimeout
		s.IdleTimeoutSet = idleTimeoutSet
		return s
	}

	cases := []struct {
		name string
		sess *Session
		want ReclaimAction
	}{
		{"override fires below policy sleep", mk(StateRunning, "agent-1", 2*time.Hour, 3600, true), ReclaimSleep},
		{"below per-session sleep", mk(StateRunning, "agent-1", 30*time.Minute, 3600, true), ReclaimNone},
		{"unset uses policy", mk(StateRunning, "agent-1", 2*time.Hour, 0, false), ReclaimNone},
		{"explicit zero disables sleep despite policy", mk(StateRunning, "agent-1", 49*time.Hour, 0, true), ReclaimNone},
		{"explicit zero does not disable archive", mk(StateRunning, "agent-1", 61*time.Hour, 0, true), ReclaimArchive},
		{"override ignored for non-resumable", mk(StateRunning, "", 2*time.Hour, 3600, true), ReclaimNone},
		{"override does not affect archive", mk(StateSleeping, "agent-1", 2*time.Hour, 3600, true), ReclaimNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.sess.ReclaimAction(now, pol); got != c.want {
				t.Fatalf("ReclaimAction = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReclaimActionGuards(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	pol := ReclaimPolicy{SleepAfter: 48 * time.Hour, ArchiveAfter: 60 * time.Hour, PruneAfter: 120 * time.Hour}
	s := mkSess(StateRunning, "agent-1", "", now.Add(-49*time.Hour))
	s.queueBusy = true
	if got := s.ReclaimAction(now, pol); got != ReclaimNone {
		t.Fatalf("busy session must not reclaim, got %v", got)
	}
}

func TestResolveReclaimPolicy(t *testing.T) {
	local := ReclaimPolicy{}
	feishu := ReclaimPolicy{SleepAfter: 48 * time.Hour, ArchiveAfter: 60 * time.Hour, PruneAfter: 120 * time.Hour}
	if got := ResolveReclaimPolicy("feishu", local, feishu); got != feishu {
		t.Fatalf("feishu source = %+v, want %+v", got, feishu)
	}
	if got := ResolveReclaimPolicy("", local, feishu); got != local {
		t.Fatalf("empty source = %+v, want local %+v", got, local)
	}
	if got := ResolveReclaimPolicy("cli", local, feishu); got != local {
		t.Fatalf("cli source = %+v, want local %+v", got, local)
	}
}
