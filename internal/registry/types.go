// Package registry implements the Phase 2 distributed registry and scheduler
// core for the agent runtime described in docs/distributed-task-model.md.
//
// It is split along the design's truth boundary:
//
//   - Registry (amuxd side) tracks online AgentRuntimes from register + heartbeat
//     and performs candidate filtering/ordering for the scheduler. Heartbeat load
//     is only used for initial screening (初筛排序); it is a stale value.
//   - Admission (amuxlet side) holds the real-time two-level quota and performs
//     the authoritative reserve -> commit -> release two-phase admission. A
//     candidate is only truly load-bearing once Reserve succeeds on the target.
//
// Both halves are pure logic with injected clock/id generators so they are
// unit-testable without any gRPC/tunnel wiring. The binaries (cmd/amuxd,
// cmd/amuxlet) wire them to the transport in later Phase 2 increments.
package registry

import (
	"strings"
	"time"
)

// RuntimeStatus is the lifecycle state of a registered runtime.
type RuntimeStatus string

const (
	// FeaturePrincipalIsolation declares that the runtime is configured to
	// enforce per-principal execution policy. It describes policy intent, not
	// helper readiness, so control-plane authorization stays closed while the
	// execution backend is degraded.
	FeaturePrincipalIsolation = "principal-isolation"

	// StatusOnline accepts new ephemeral tasks.
	StatusOnline RuntimeStatus = "online"
	// StatusDraining keeps serving existing/sticky work but takes no new
	// ephemeral tasks.
	StatusDraining RuntimeStatus = "draining"
	// StatusOffline is disconnected; never a candidate.
	StatusOffline RuntimeStatus = "offline"
)

// AgentCapacity is one supported agent type and its per-agent concurrency limit
// on a runtime (design AgentRuntime.supported_agents item).
type AgentCapacity struct {
	Type           string `json:"type"`
	Protocol       string `json:"protocol,omitempty"`
	MaxConcurrency int    `json:"max_concurrency"`
}

// Capabilities is the static portion of a runtime's registration: what it can
// run, never its current load. It is reported on register and on config change.
//
// The "v2-merged fields" block was added from v2's pb.TargetCapabilities during
// the amux v2 / distributed-runtime merge so we have a single capabilities type
// across CLI target selection (app/cli/{resolver,target}.go) and hub scheduling
// (this package). Phase 1/2 admission/scheduling code uses Agents (rich) and
// CredentialProfiles (rich); resolver / target_selector code uses AgentTypes
// (flat) / CredentialRefs (flat). Producers populate both, kept in sync.
type Capabilities struct {
	Labels             map[string]string   `json:"labels,omitempty"`
	Agents             []AgentCapacity     `json:"agents,omitempty"`
	IsolationBackends  []string            `json:"isolation,omitempty"`
	WorkspacePolicies  []string            `json:"workspace_policies,omitempty"`
	CredentialProfiles map[string][]string `json:"credential_profiles,omitempty"` // agent_type -> profile names
	MaxSessions        int                 `json:"max_sessions,omitempty"`
	// v2-merged fields (target capability surface for CLI resolver and pretty-print).
	AgentTypes              []string `json:"agent_types,omitempty"`
	Features                []string `json:"features,omitempty"`
	RuntimeTools            []string `json:"runtime_tools,omitempty"`
	WorkspaceRoots          []string `json:"workspace_roots,omitempty"`
	Shells                  []string `json:"shells,omitempty"`
	PathStyle               string   `json:"path_style,omitempty"`
	NetworkProfile          string   `json:"network_profile,omitempty"`
	CredentialRefs          []string `json:"credential_refs,omitempty"`
	IsolationHelperProtocol string   `json:"isolation_helper_protocol,omitempty"`
	IsolationDegradedReason string   `json:"isolation_degraded_reason,omitempty"`
}

// Load is the dynamic portion of a runtime's state, reported via heartbeat. It
// is advisory: the scheduler uses it only to rank candidates before admission.
type Load struct {
	CurrentSessions  int            `json:"current_sessions"`
	ReservedSlots    int            `json:"reserved_slots,omitempty"`
	PerAgentRunning  map[string]int `json:"per_agent_running,omitempty"`
	PerAgentReserved map[string]int `json:"per_agent_reserved,omitempty"`
	DiskPressure     bool           `json:"disk_pressure,omitempty"`
}

// AgentRuntime is amuxd's view of one online (or recently online) amuxlet. It is
// the registry's record; see design "数据模型 / AgentRuntime".
type AgentRuntime struct {
	TargetID    string
	TargetName  string
	Caps        Capabilities
	Load        Load
	Status      RuntimeStatus
	HeartbeatAt time.Time
}

// agentCap returns the AgentCapacity for the given type, or false.
func (rt *AgentRuntime) agentCap(agentType string) (AgentCapacity, bool) {
	for _, a := range rt.Caps.Agents {
		if a.Type == agentType {
			return a, true
		}
	}
	return AgentCapacity{}, false
}

// SupportsAgent reports whether the runtime can run the agent type.
func (rt *AgentRuntime) SupportsAgent(agentType string) bool {
	_, ok := rt.agentCap(agentType)
	return ok
}

// SupportsIsolation reports whether the runtime offers the isolation backend.
func (rt *AgentRuntime) SupportsIsolation(mode string) bool {
	return containsString(rt.Caps.IsolationBackends, mode)
}

// SupportsWorkspace reports whether the runtime offers the workspace policy.
func (rt *AgentRuntime) SupportsWorkspace(policy string) bool {
	return containsString(rt.Caps.WorkspacePolicies, policy)
}

// AllowsCredentialProfile reports whether the named credential profile is
// allowed for the agent type on this runtime. An empty profile is always
// allowed (the task uses the runtime/agent default identity).
func (rt *AgentRuntime) AllowsCredentialProfile(agentType, profile string) bool {
	return rt.AllowsCredentialProfiles(agentType, []string{profile})
}

// AllowsCredentialProfiles reports whether all named credential profiles are
// allowed for the agent type on this runtime. Empty profiles are ignored.
func (rt *AgentRuntime) AllowsCredentialProfiles(agentType string, profiles []string) bool {
	allowed, restricted := rt.Caps.CredentialProfiles[agentType]
	if !restricted {
		return true
	}
	for _, profile := range profiles {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			continue
		}
		if !containsString(allowed, profile) {
			return false
		}
	}
	return true
}

// MatchesSelector reports whether the runtime's labels are a superset of the
// selector (every selector key must match the runtime label exactly).
func (rt *AgentRuntime) MatchesSelector(selector map[string]string) bool {
	for k, v := range selector {
		if rt.Caps.Labels[k] != v {
			return false
		}
	}
	return true
}

// loadFraction is the whole-machine load ratio used for ranking. Runtimes with
// no declared MaxSessions sort last (treated as fully loaded) so a misconfigured
// runtime is not preferred.
func (rt *AgentRuntime) loadFraction() float64 {
	if rt.Caps.MaxSessions <= 0 {
		return 1
	}
	used := rt.Load.CurrentSessions + rt.Load.ReservedSlots
	return float64(used) / float64(rt.Caps.MaxSessions)
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
