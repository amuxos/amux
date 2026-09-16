package session

import "time"

// ReclaimPolicy holds the three staged idle thresholds. A zero duration in any
// field disables that stage. All thresholds are measured cumulatively from the
// session's LastInteractionAt.
type ReclaimPolicy struct {
	SleepAfter   time.Duration
	ArchiveAfter time.Duration
	PruneAfter   time.Duration
}

// ReclaimAction is the single transition the reclaim watcher should apply this tick.
type ReclaimAction int

const (
	ReclaimNone ReclaimAction = iota
	ReclaimSleep
	ReclaimArchive
	ReclaimPrune
)

// ResolveReclaimPolicy picks the policy for a session by its workspace source.
// "feishu" → feishu policy; everything else (including "") → local policy.
func ResolveReclaimPolicy(workspaceSource string, local, feishu ReclaimPolicy) ReclaimPolicy {
	if workspaceSource == "feishu" {
		return feishu
	}
	return local
}

// ReclaimAction reports the staged reclaim transition due for this session, or
// ReclaimNone. It only advances one stage per call. Mirrors AutoSleepEligible's
// guards (busy/pending/permission/task-owned) and reads LastInteractionAt as the
// idle clock.
//
// If the session explicitly sets IdleTimeoutSeconds, that value overrides the
// policy's SleepAfter as the per-session sleep threshold. A value of 0 disables
// the sleep stage for that session. This is how the Feishu bridge's per-workspace
// idle_timeout_seconds, passed through RunRequest, is honored. The override only
// affects the resumable SLEEP stage; archive and prune thresholds always come
// from the resolved policy.
func (s *Session) ReclaimAction(now time.Time, p ReclaimPolicy) ReclaimAction {
	if now.IsZero() {
		now = time.Now()
	}
	s.runtimeMu.RLock()
	state := s.State
	ownerTask := s.OwnerTaskId
	instanceID := s.AgentInstanceID
	resumable := s.AgentSessionID != ""
	idleTimeoutSeconds := s.IdleTimeoutSeconds
	idleTimeoutSet := s.IdleTimeoutSet
	s.runtimeMu.RUnlock()

	if ownerTask != "" {
		return ReclaimNone // task-owned sessions are reaped by the task lifecycle only
	}
	if instanceID != "" {
		// Persistent-agent sessions are reused across tasks (sleeping <-> running)
		// and managed by the agent-level lifecycle; idle reclaim must not reap them
		// or the durable environment and provider session handle would be lost.
		return ReclaimNone
	}
	if state == StateCreated {
		return ReclaimNone
	}
	if busy, pending := s.QueueState(); busy || pending > 0 {
		return ReclaimNone
	}
	if s.PendingPermissionCount() > 0 {
		return ReclaimNone
	}

	last := s.InteractionAt()
	if last.IsZero() {
		return ReclaimNone
	}
	idle := now.Sub(last)
	reached := func(th time.Duration) bool { return th > 0 && idle >= th }

	sleepAfter := p.SleepAfter
	if idleTimeoutSet {
		sleepAfter = time.Duration(idleTimeoutSeconds) * time.Second
	}

	switch state {
	case StateArchived:
		if reached(p.PruneAfter) {
			return ReclaimPrune
		}
	case StateSleeping:
		if reached(p.ArchiveAfter) {
			return ReclaimArchive
		}
	case StateRunning:
		if resumable && reached(sleepAfter) {
			return ReclaimSleep
		}
		// non-resumable, or sleep stage disabled: fall through to archive
		if reached(p.ArchiveAfter) {
			return ReclaimArchive
		}
	}
	return ReclaimNone
}
