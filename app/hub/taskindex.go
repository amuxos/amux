package hub

import (
	"sort"
	"sync"

	"github.com/amuxos/amux/internal/registry"
	"github.com/rs/zerolog/log"
)

// taskPlacement records where a subtask was scheduled. It is amuxd's task-truth
// for routing: which runtime owns the task, and the admission reservation held on
// that runtime so the reconciler can release it when the task ends.
//
// ReservationId may be empty for entries learned by fan-out discovery (List, or a
// post-restart resolve) rather than by Submit — amuxd lost the original
// reservation id, so the committed admission slot is reclaimed only when that
// amuxlet restarts (a known Phase-2 in-memory limitation; Phase 5 externalizes
// this state). Release of an empty/unknown reservation id is a harmless no-op.
type taskPlacement struct {
	TargetID      string
	ReservationId string
	// ParentSessionID records which parent session spawned this subtask, so the
	// Feishu bridge can surface the task's progress on that session's thread. It
	// is provenance only — never used for routing.
	ParentSessionID string
	// AgentInstanceID records the persistent agent instance this task belongs to
	// (Phase 3). Empty for ephemeral tasks. Used to recover sticky target mapping
	// and to route instance-level RPCs (list/resume/interrupt) after an amuxd
	// restart.
	AgentInstanceID string
}

// taskIndex is amuxd's in-memory task_id -> placement map. Phase 2 keeps it in
// memory (rebuilt by List fan-out / the reconciler after an amuxd restart); Phase
// 5 moves it to durable storage for HA.
type taskIndex struct {
	mu sync.Mutex
	m  map[string]taskPlacement
}

func newTaskIndex() *taskIndex { return &taskIndex{m: map[string]taskPlacement{}} }

func (i *taskIndex) put(id string, p taskPlacement) {
	i.mu.Lock()
	// Discovery has no admission identity. Merge it under the same lock as
	// publication so a late List/Get cannot erase a Submit's reservation or
	// its provenance, including while a failed release is awaiting retry.
	if existing, ok := i.m[id]; ok && existing.TargetID == p.TargetID {
		if p.ReservationId == "" {
			p.ReservationId = existing.ReservationId
		}
		if p.ParentSessionID == "" {
			p.ParentSessionID = existing.ParentSessionID
		}
		if p.AgentInstanceID == "" {
			p.AgentInstanceID = existing.AgentInstanceID
		}
	}
	i.m[id] = p
	i.mu.Unlock()
}

func (i *taskIndex) get(id string) (taskPlacement, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	p, ok := i.m[id]
	return p, ok
}

func (i *taskIndex) delete(id string) {
	i.mu.Lock()
	delete(i.m, id)
	i.mu.Unlock()
}

// reclaim owns the release-before-forget rule shared by reconciliation and
// history prune. A failed or unacknowledged release keeps the placement for a
// later pass; the runtime's Release is idempotent even if its response was lost.
func (i *taskIndex) reclaim(id string, p taskPlacement, release func(*registry.Placement) error) {
	if p.ReservationId != "" {
		if err := release(&registry.Placement{TargetID: p.TargetID, ReservationId: p.ReservationId}); err != nil {
			log.Warn().Err(err).Str("task_id", id).Str("target_id", p.TargetID).
				Str("reservation_id", p.ReservationId).Msg("task admission release unconfirmed; retaining placement for retry")
			return
		}
	}
	// Do not erase a replacement placement installed while the RPC was in flight.
	i.mu.Lock()
	if current, ok := i.m[id]; ok && current == p {
		delete(i.m, id)
	}
	i.mu.Unlock()
}

// snapshot returns a copy of the index for safe iteration without holding the lock.
func (i *taskIndex) snapshot() map[string]taskPlacement {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make(map[string]taskPlacement, len(i.m))
	for k, v := range i.m {
		out[k] = v
	}
	return out
}

// byParent returns task ids whose recorded parent session matches, sorted for
// stable rendering. Empty parent never matches.
func (i *taskIndex) byParent(parentSessionID string) []string {
	if parentSessionID == "" {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	var out []string
	for id, p := range i.m {
		if p.ParentSessionID == parentSessionID {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// getInstance returns the placement of the most recent task belonging to the
// given agent instance, or (zero, false) if none is recorded. It is the in-memory
// fallback for sticky routing when the durable instance store has no target yet
// (e.g. between instance record creation and the first index.put).
func (i *taskIndex) getInstance(agentInstanceID string) (taskPlacement, bool) {
	if agentInstanceID == "" {
		return taskPlacement{}, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	var (
		best    taskPlacement
		found   bool
		bestKey string
	)
	for id, p := range i.m {
		if p.AgentInstanceID != agentInstanceID {
			continue
		}
		if !found || id > bestKey {
			best, bestKey, found = p, id, true
		}
	}
	return best, found
}
