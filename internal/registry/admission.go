package registry

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Admission errors returned by Reserve/Commit.
var (
	// ErrAgentNotSupported is returned when the runtime has no concurrency limit
	// configured for the requested agent type.
	ErrAgentNotSupported = errors.New("admission: agent type not supported")
	// ErrNoCapacity is returned when either the whole-machine max_sessions or the
	// per-agent max_concurrency quota is exhausted.
	ErrNoCapacity = errors.New("admission: no capacity")
	// ErrReservationNotFound is returned when committing an unknown/expired
	// reservation; the scheduler must re-reserve.
	ErrReservationNotFound = errors.New("admission: reservation not found")
)

// slot is one occupied admission slot — reserved (committed=false) or running
// (committed=true). Both occupy whole-machine and per-agent quota.
type slot struct {
	agentType string
	createdAt time.Time
	committed bool
}

// Admission is the amuxlet-side real-time two-level quota and reserve -> commit
// -> release two-phase admission (design "amuxlet admission" + "调度策略 /
// admission"). It is the authoritative承载力 check; the amuxd registry's
// heartbeat load is only advisory ranking input.
//
// Both reserved and committed slots count toward quota, so concurrent fan-out
// Reserves cannot oversubscribe between a Reserve and its Commit.
type Admission struct {
	mu          sync.Mutex
	maxSessions int            // whole-machine ceiling; <=0 means unbounded
	perAgentMax map[string]int // agent type -> max_concurrency
	slots       map[string]*slot
	clock       func() time.Time
	idGen       func() string
	ttl         time.Duration // uncommitted reservation lifetime
}

// AdmissionConfig configures an Admission.
type AdmissionConfig struct {
	MaxSessions    int
	PerAgentMax    map[string]int
	ReservationTTL time.Duration // 0 => default 30s
	Clock          func() time.Time
	IDGen          func() string
}

// NewAdmission constructs an Admission from a config.
func NewAdmission(cfg AdmissionConfig) *Admission {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.IDGen == nil {
		cfg.IDGen = func() string { return "rsv_" + uuid.NewString() }
	}
	if cfg.ReservationTTL <= 0 {
		cfg.ReservationTTL = 30 * time.Second
	}
	perAgent := make(map[string]int, len(cfg.PerAgentMax))
	for k, v := range cfg.PerAgentMax {
		perAgent[k] = v
	}
	return &Admission{
		maxSessions: cfg.MaxSessions,
		perAgentMax: perAgent,
		slots:       map[string]*slot{},
		clock:       cfg.Clock,
		idGen:       cfg.IDGen,
		ttl:         cfg.ReservationTTL,
	}
}

// NewAdmissionFromCaps builds an Admission whose limits come from the runtime's
// declared Capabilities (max_sessions + per-agent max_concurrency).
func NewAdmissionFromCaps(caps Capabilities, ttl time.Duration) *Admission {
	perAgent := make(map[string]int, len(caps.Agents))
	for _, a := range caps.Agents {
		perAgent[a.Type] = a.MaxConcurrency
	}
	return NewAdmission(AdmissionConfig{
		MaxSessions:    caps.MaxSessions,
		PerAgentMax:    perAgent,
		ReservationTTL: ttl,
	})
}

// Reserve attempts the first phase of admission for an agent type. On success it
// returns a reservation id and decrements available quota (whole-machine and
// per-agent). The reservation must be committed (Commit) or released (Release);
// uncommitted reservations are reclaimed by ExpireReservations after the TTL.
func (a *Admission) Reserve(agentType string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	max, ok := a.perAgentMax[agentType]
	if !ok || max <= 0 {
		return "", ErrAgentNotSupported
	}
	if a.maxSessions > 0 && len(a.slots) >= a.maxSessions {
		return "", ErrNoCapacity
	}
	if a.perAgentUsedLocked(agentType) >= max {
		return "", ErrNoCapacity
	}
	id := a.idGen()
	a.slots[id] = &slot{agentType: agentType, createdAt: a.clock(), committed: false}
	return id, nil
}

// Commit turns a reservation into a running slot. It is idempotent: committing an
// already-committed reservation succeeds without double-counting, which tolerates
// retries under tunnel jitter. Committing an unknown/expired reservation returns
// ErrReservationNotFound.
func (a *Admission) Commit(reservationID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.slots[reservationID]
	if !ok {
		return ErrReservationNotFound
	}
	s.committed = true
	return nil
}

// Release frees a slot regardless of whether it was reserved or committed. It is
// idempotent (unknown id is a no-op). Use it on reservation timeout, task
// completion, or session teardown to return both quota levels.
func (a *Admission) Release(reservationID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.slots, reservationID)
}

// ExpireReservations releases every uncommitted reservation older than the TTL
// and returns their ids. Committed slots are never expired here — they are freed
// only by an explicit Release. This is the design's "预留超时未 commit 由
// amuxlet 自动释放" leak backstop.
func (a *Admission) ExpireReservations() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock()
	var expired []string
	for id, s := range a.slots {
		if !s.committed && now.Sub(s.createdAt) > a.ttl {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		delete(a.slots, id)
	}
	return expired
}

// Snapshot returns the current load for heartbeat reporting (design Load).
func (a *Admission) Snapshot() Load {
	a.mu.Lock()
	defer a.mu.Unlock()
	load := Load{
		PerAgentRunning:  map[string]int{},
		PerAgentReserved: map[string]int{},
	}
	for _, s := range a.slots {
		if s.committed {
			load.CurrentSessions++
			load.PerAgentRunning[s.agentType]++
		} else {
			load.ReservedSlots++
			load.PerAgentReserved[s.agentType]++
		}
	}
	return load
}

// perAgentUsedLocked counts reserved+committed slots for an agent type. Caller
// must hold the mutex.
func (a *Admission) perAgentUsedLocked(agentType string) int {
	n := 0
	for _, s := range a.slots {
		if s.agentType == agentType {
			n++
		}
	}
	return n
}
