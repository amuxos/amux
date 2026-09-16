package registry

import "errors"

// Scheduling errors.
var (
	// ErrNoMatchingRuntime is returned when no online runtime satisfies the
	// request's agent/isolation/workspace/credential/selector filters.
	ErrNoMatchingRuntime = errors.New("scheduler: no matching runtime")
	// ErrNoCapacityAnywhere is returned when candidates exist but every one
	// rejected the reservation (all at quota).
	ErrNoCapacityAnywhere = errors.New("scheduler: no capacity on any candidate")
)

// Reserver is the amuxd-side view of remote amuxlet admissions. The scheduler
// reserves a slot on a candidate, the caller creates the child session, then the
// caller Commits (turning the reservation into a running slot) or Releases it on
// failure. Implementations route Reserve/Commit/Release to the target runtime's
// Admission over the tunnel.
type Reserver interface {
	Reserve(targetID, agentType string) (reservationID string, err error)
	Commit(targetID, reservationID string) error
	Release(targetID, reservationID string) error
}

// Placement is a committed-pending reservation on a chosen target. The caller
// must Commit it (after the child session is created) or Release it (on failure)
// via the same Reserver, so the runtime's two-level quota stays consistent.
type Placement struct {
	TargetID      string
	ReservationId string
}

// Scheduler turns a ScheduleRequest into a concrete Placement using the design's
// "ephemeral 调度（含 admission control）": filter+rank candidates from the
// registry (initial screening on stale heartbeat load), then walk the ranked
// queue issuing Reserve until one runtime accepts (authoritative real-time
// admission). Heartbeat load alone is never trusted to place work.
type Scheduler struct {
	reg      *Registry
	reserver Reserver
}

// NewScheduler builds a Scheduler over a registry and a reserver.
func NewScheduler(reg *Registry, reserver Reserver) *Scheduler {
	return &Scheduler{reg: reg, reserver: reserver}
}

// Acquire selects a target and reserves a slot for the request. It returns a
// Placement whose reservation the caller MUST either Commit (after creating the
// child session) or Release (on failure). On no candidate it returns
// ErrNoMatchingRuntime; when candidates exist but all reject the reservation it
// returns ErrNoCapacityAnywhere.
//
// A candidate that errors on Reserve for a transient reason (e.g. tunnel
// hiccup) is skipped just like a no-capacity rejection, so one flaky runtime
// does not fail an otherwise-placeable task.
func (s *Scheduler) Acquire(req ScheduleRequest) (*Placement, error) {
	// Sticky path: for an existing persistent agent instance, prefer the target
	// that already hosts its durable environment. The sticky target must still
	// pass the request filters and admit the reservation; on any failure the
	// scheduler falls back to the ranked candidate walk so capacity semantics
	// (Reserve -> Commit -> Release) stay unchanged.
	if sticky := s.reg.StickyCandidate(req); sticky != nil {
		if id, err := s.reserver.Reserve(sticky.TargetID, req.AgentType); err == nil {
			return &Placement{TargetID: sticky.TargetID, ReservationId: id}, nil
		}
		// Sticky target rejected the reservation (at quota / transient): fall
		// through to the ranked walk rather than failing the whole acquire.
	}

	candidates := s.reg.Candidates(req)
	if len(candidates) == 0 {
		return nil, ErrNoMatchingRuntime
	}
	for _, rt := range candidates {
		id, err := s.reserver.Reserve(rt.TargetID, req.AgentType)
		if err != nil {
			continue // at quota or transient failure: try the next candidate
		}
		return &Placement{TargetID: rt.TargetID, ReservationId: id}, nil
	}
	return nil, ErrNoCapacityAnywhere
}

// Commit confirms a placement's reservation (the child session now exists).
func (s *Scheduler) Commit(p *Placement) error {
	return s.reserver.Commit(p.TargetID, p.ReservationId)
}

// Release returns a placement's reserved or committed slot (creation failed,
// the placement was abandoned, or the task finished).
// Callers must retain committed placements until release is confirmed; their
// slots do not expire with the uncommitted reservation TTL.
func (s *Scheduler) Release(p *Placement) error {
	if p.ReservationId == "" {
		return nil
	}
	return s.reserver.Release(p.TargetID, p.ReservationId)
}
