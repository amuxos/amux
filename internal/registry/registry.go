package registry

import (
	"sort"
	"sync"
	"time"
)

// Registry is amuxd's in-memory view of all connected runtimes. Phase 2 keeps it
// in memory; HA/external storage is Phase 5. It is the registry source described
// in design "组件职责 / amuxd registry".
//
// Concurrency: all methods are safe for concurrent use. Returned AgentRuntime
// pointers are deep-ish copies (snapshots) so callers cannot mutate registry
// state through them.
type Registry struct {
	mu       sync.RWMutex
	runtimes map[string]*AgentRuntime
	clock    func() time.Time
	// offlineAfter marks a runtime offline if its heartbeat is older than this.
	// Zero disables staleness (used in tests with a frozen clock).
	offlineAfter time.Duration
}

// Options configures a Registry.
type Options struct {
	Clock        func() time.Time
	OfflineAfter time.Duration
}

// New constructs a Registry.
func New(opts Options) *Registry {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	return &Registry{
		runtimes:     map[string]*AgentRuntime{},
		clock:        opts.Clock,
		offlineAfter: opts.OfflineAfter,
	}
}

// Register adds or replaces a runtime's static capabilities (on tunnel connect /
// re-register). It resets the runtime to online and stamps the heartbeat time.
// Existing load is preserved if the runtime was already known, so a reconnect
// without an immediate heartbeat does not appear to drop to zero load.
func (r *Registry) Register(targetID, targetName string, caps Capabilities) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	rt, ok := r.runtimes[targetID]
	if !ok {
		rt = &AgentRuntime{TargetID: targetID}
		r.runtimes[targetID] = rt
	}
	rt.TargetName = targetName
	rt.Caps = caps
	rt.Status = StatusOnline
	rt.HeartbeatAt = now
}

// Heartbeat updates a runtime's dynamic load. It is a no-op if the runtime is
// unknown (a heartbeat must follow a register). Returns whether it was applied.
func (r *Registry) Heartbeat(targetID string, load Load) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.runtimes[targetID]
	if !ok {
		return false
	}
	rt.Load = load
	rt.HeartbeatAt = r.clock()
	if rt.Status == StatusOffline {
		rt.Status = StatusOnline
	}
	return true
}

// SetStatus transitions a runtime's status (e.g. to draining). No-op if unknown.
func (r *Registry) SetStatus(targetID string, status RuntimeStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rt, ok := r.runtimes[targetID]; ok {
		rt.Status = status
	}
}

// Remove drops a runtime entirely (tunnel disconnect).
func (r *Registry) Remove(targetID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runtimes, targetID)
}

// Get returns a snapshot of one runtime, applying staleness, or false.
func (r *Registry) Get(targetID string) (*AgentRuntime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.runtimes[targetID]
	if !ok {
		return nil, false
	}
	return r.snapshot(rt), true
}

// List returns snapshots of all runtimes (staleness applied), sorted by id.
func (r *Registry) List() []*AgentRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*AgentRuntime, 0, len(r.runtimes))
	for _, rt := range r.runtimes {
		out = append(out, r.snapshot(rt))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TargetID < out[j].TargetID })
	return out
}

// ScheduleRequest is the subset of a task spec that drives candidate selection
// (design "调度策略 / 输入").
type ScheduleRequest struct {
	AgentType          string
	Isolation          string
	WorkspacePolicy    string
	CredentialProfile  string
	CredentialProfiles []string
	Selector           map[string]string
	// RequiredTargetID is a hard placement constraint. Unlike
	// StickyTargetID, it never falls back to another runtime. Identified
	// principals with a durable environment_target_id use this field so their
	// HOME/workspace cannot silently move to a different host.
	RequiredTargetID string
	// RepoCacheKey, when set, biases ordering toward runtimes whose
	// "repo_cache" label matches (affinity to a warm repo cache).
	RepoCacheKey string
	// AgentInstanceID identifies the persistent agent instance this request
	// belongs to (Phase 3). It is logical addressing only and does NOT affect
	// capacity admission, which still goes through Reserve -> Commit -> Release.
	AgentInstanceID string
	// StickyTargetID, when set, asks the scheduler to prefer reserving this
	// target for an existing agent instance (so a follow-up task reuses the
	// same durable environment). The sticky target must still pass the request
	// filters and admit the reservation; otherwise the scheduler falls back to
	// the ranked candidate walk.
	StickyTargetID string
}

// passes reports whether a runtime snapshot satisfies the request's
// non-capacity filters (online + agent/isolation/workspace/credential/selector).
// It is the screening predicate shared by Candidates and the sticky path.
func (r *Registry) passes(req ScheduleRequest, snap *AgentRuntime) bool {
	if snap.Status != StatusOnline { // excludes offline + draining for ephemeral
		return false
	}
	if req.RequiredTargetID != "" && snap.TargetID != req.RequiredTargetID {
		return false
	}
	if !snap.SupportsAgent(req.AgentType) {
		return false
	}
	if req.Isolation != "" && !snap.SupportsIsolation(req.Isolation) {
		return false
	}
	if req.WorkspacePolicy != "" && !snap.SupportsWorkspace(req.WorkspacePolicy) {
		return false
	}
	if !snap.AllowsCredentialProfiles(req.AgentType, scheduleCredentialProfiles(req)) {
		return false
	}
	if !snap.MatchesSelector(req.Selector) {
		return false
	}
	return true
}

func scheduleCredentialProfiles(req ScheduleRequest) []string {
	out := make([]string, 0, 1+len(req.CredentialProfiles))
	if req.CredentialProfile != "" {
		out = append(out, req.CredentialProfile)
	}
	for _, profile := range req.CredentialProfiles {
		if profile == "" || containsString(out, profile) {
			continue
		}
		out = append(out, profile)
	}
	return out
}

// StickyCandidate returns the snapshot of the sticky target if it is registered,
// online, and passes the request's filters; otherwise nil. Used by the scheduler
// to attempt a sticky reservation before falling back to the ranked walk.
func (r *Registry) StickyCandidate(req ScheduleRequest) *AgentRuntime {
	if req.StickyTargetID == "" {
		return nil
	}
	snap, ok := r.Get(req.StickyTargetID)
	if !ok {
		return nil
	}
	if !r.passes(req, snap) {
		return nil
	}
	return snap
}

// Candidates returns the runtimes that can host the request, ordered by
// preference (least loaded first, repo-cache affinity as a tiebreak, then id for
// determinism). This is design "ephemeral 调度" steps 1-6 — initial screening
// only. The caller must still run reserve -> commit admission against the chosen
// target before creating a child session, because heartbeat load is stale.
func (r *Registry) Candidates(req ScheduleRequest) []*AgentRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var cand []*AgentRuntime
	for _, rt := range r.runtimes {
		snap := r.snapshot(rt)
		if !r.passes(req, snap) {
			continue
		}
		cand = append(cand, snap)
	}

	sort.Slice(cand, func(i, j int) bool {
		a, b := cand[i], cand[j]
		// Repo-cache affinity wins first: a warm cache avoids a fresh clone.
		if req.RepoCacheKey != "" {
			am := a.Caps.Labels["repo_cache"] == req.RepoCacheKey
			bm := b.Caps.Labels["repo_cache"] == req.RepoCacheKey
			if am != bm {
				return am
			}
		}
		if la, lb := a.loadFraction(), b.loadFraction(); la != lb {
			return la < lb
		}
		return a.TargetID < b.TargetID
	})
	return cand
}

// snapshot returns a copy of rt with staleness-derived status applied. Caller
// must hold at least the read lock.
func (r *Registry) snapshot(rt *AgentRuntime) *AgentRuntime {
	cp := *rt
	// Deep-copy maps/slices that callers might read concurrently with a writer.
	cp.Caps.Labels = copyStringMap(rt.Caps.Labels)
	cp.Caps.Agents = append([]AgentCapacity(nil), rt.Caps.Agents...)
	cp.Caps.IsolationBackends = append([]string(nil), rt.Caps.IsolationBackends...)
	cp.Caps.WorkspacePolicies = append([]string(nil), rt.Caps.WorkspacePolicies...)
	cp.Caps.CredentialProfiles = copyStringSliceMap(rt.Caps.CredentialProfiles)
	cp.Load.PerAgentRunning = copyIntMap(rt.Load.PerAgentRunning)
	cp.Load.PerAgentReserved = copyIntMap(rt.Load.PerAgentReserved)

	if cp.Status == StatusOnline && r.offlineAfter > 0 {
		if r.clock().Sub(rt.HeartbeatAt) > r.offlineAfter {
			cp.Status = StatusOffline
		}
	}
	return &cp
}

func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyIntMap(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyStringSliceMap(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = append([]string(nil), v...)
	}
	return out
}
