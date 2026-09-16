package registry

import (
	"testing"
	"time"
)

func testCaps(agentType string, maxConc, maxSess int) Capabilities {
	return Capabilities{
		Labels:            map[string]string{"pool": "local-dev"},
		Agents:            []AgentCapacity{{Type: agentType, Protocol: "claude_sdk_sidecar", MaxConcurrency: maxConc}},
		IsolationBackends: []string{"process", "private-home"},
		WorkspacePolicies: []string{"temp-dir", "git-worktree"},
		CredentialProfiles: map[string][]string{
			agentType: {agentType + "-subagent"},
		},
		MaxSessions: maxSess,
	}
}

func TestRegistryRegisterHeartbeatList(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(Options{Clock: func() time.Time { return now }})

	r.Register("t1", "host-1", testCaps("claude", 2, 6))
	rt, ok := r.Get("t1")
	if !ok {
		t.Fatal("expected t1 registered")
	}
	if rt.Status != StatusOnline {
		t.Fatalf("status = %q, want online", rt.Status)
	}
	if !rt.SupportsAgent("claude") || rt.SupportsAgent("codex") {
		t.Fatal("agent support wrong")
	}

	if !r.Heartbeat("t1", Load{CurrentSessions: 3}) {
		t.Fatal("heartbeat not applied")
	}
	if got := r.Heartbeat("missing", Load{}); got {
		t.Fatal("heartbeat on unknown runtime should be no-op")
	}
	rt, _ = r.Get("t1")
	if rt.Load.CurrentSessions != 3 {
		t.Fatalf("load = %d, want 3", rt.Load.CurrentSessions)
	}

	if list := r.List(); len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
}

func TestRegistrySnapshotIsolation(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(Options{Clock: func() time.Time { return now }})
	r.Register("t1", "host-1", testCaps("claude", 2, 6))

	rt, _ := r.Get("t1")
	rt.Caps.Labels["pool"] = "mutated"
	rt.Caps.Agents[0].MaxConcurrency = 99

	fresh, _ := r.Get("t1")
	if fresh.Caps.Labels["pool"] != "local-dev" {
		t.Fatal("registry label mutated through snapshot")
	}
	if fresh.Caps.Agents[0].MaxConcurrency != 2 {
		t.Fatal("registry agent cap mutated through snapshot")
	}
}

func TestRegistryStaleness(t *testing.T) {
	cur := time.Unix(1000, 0)
	r := New(Options{Clock: func() time.Time { return cur }, OfflineAfter: 30 * time.Second})
	r.Register("t1", "host-1", testCaps("claude", 2, 6))

	cur = cur.Add(10 * time.Second)
	if rt, _ := r.Get("t1"); rt.Status != StatusOnline {
		t.Fatal("should still be online within window")
	}
	cur = cur.Add(40 * time.Second)
	if rt, _ := r.Get("t1"); rt.Status != StatusOffline {
		t.Fatalf("should be offline after window, got %q", rt.Status)
	}
	// A fresh heartbeat revives it.
	r.Heartbeat("t1", Load{})
	if rt, _ := r.Get("t1"); rt.Status != StatusOnline {
		t.Fatal("heartbeat should revive offline runtime")
	}
}

func TestCandidatesFiltering(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(Options{Clock: func() time.Time { return now }})

	r.Register("claude-box", "c", testCaps("claude", 2, 6))
	r.Register("codex-box", "x", testCaps("codex", 2, 6))
	r.Register("draining-box", "d", testCaps("claude", 2, 6))
	r.SetStatus("draining-box", StatusDraining)

	got := r.Candidates(ScheduleRequest{
		AgentType:         "claude",
		Isolation:         "private-home",
		WorkspacePolicy:   "git-worktree",
		CredentialProfile: "claude-subagent",
		Selector:          map[string]string{"pool": "local-dev"},
	})
	if len(got) != 1 || got[0].TargetID != "claude-box" {
		t.Fatalf("candidates = %v, want [claude-box] (codex filtered by agent, draining excluded)", ids(got))
	}

	// Unsupported isolation filters everything out.
	if c := r.Candidates(ScheduleRequest{AgentType: "claude", Isolation: "container"}); len(c) != 0 {
		t.Fatalf("container isolation should yield no candidates, got %v", ids(c))
	}
	// Disallowed credential profile filters out.
	if c := r.Candidates(ScheduleRequest{AgentType: "claude", CredentialProfile: "claude-main"}); len(c) != 0 {
		t.Fatalf("disallowed profile should yield no candidates, got %v", ids(c))
	}
	// Multiple credential profiles require a runtime that allows every profile.
	multiCaps := testCaps("claude", 2, 6)
	multiCaps.CredentialProfiles["claude"] = []string{"github", "slack"}
	r.Register("multi-profile-box", "m", multiCaps)
	if c := r.Candidates(ScheduleRequest{AgentType: "claude", CredentialProfiles: []string{"github", "slack"}}); len(c) != 1 || c[0].TargetID != "multi-profile-box" {
		t.Fatalf("multi profile candidates = %v, want [multi-profile-box]", ids(c))
	}
	if c := r.Candidates(ScheduleRequest{AgentType: "claude", CredentialProfiles: []string{"github", "jira"}}); len(c) != 0 {
		t.Fatalf("missing profile should yield no candidates, got %v", ids(c))
	}
	// Selector mismatch filters out.
	if c := r.Candidates(ScheduleRequest{AgentType: "claude", Selector: map[string]string{"pool": "prod"}}); len(c) != 0 {
		t.Fatalf("selector mismatch should yield no candidates, got %v", ids(c))
	}
	// A principal's environment target is a hard placement constraint.
	r.Register("other-claude-box", "o", testCaps("claude", 2, 6))
	if c := r.Candidates(ScheduleRequest{AgentType: "claude", RequiredTargetID: "other-claude-box"}); len(c) != 1 || c[0].TargetID != "other-claude-box" {
		t.Fatalf("hard-placement candidates = %v, want [other-claude-box]", ids(c))
	}
	if c := r.Candidates(ScheduleRequest{AgentType: "claude", RequiredTargetID: "missing"}); len(c) != 0 {
		t.Fatalf("missing hard-placement target should not fall back, got %v", ids(c))
	}
}

func TestCandidatesOrderingByLoadAndAffinity(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(Options{Clock: func() time.Time { return now }})

	for _, id := range []string{"a", "b", "c"} {
		caps := testCaps("claude", 4, 10)
		if id == "c" {
			caps.Labels["repo_cache"] = "amux"
		}
		r.Register(id, id, caps)
	}
	// a is busiest, b is idle, c has repo affinity but moderate load.
	r.Heartbeat("a", Load{CurrentSessions: 8})
	r.Heartbeat("b", Load{CurrentSessions: 1})
	r.Heartbeat("c", Load{CurrentSessions: 5})

	// Without affinity: least-loaded first => b, c, a.
	got := r.Candidates(ScheduleRequest{AgentType: "claude"})
	if order := ids(got); order[0] != "b" || order[2] != "a" {
		t.Fatalf("load ordering = %v, want b first, a last", order)
	}

	// With repo affinity: c wins despite higher load than b.
	got = r.Candidates(ScheduleRequest{AgentType: "claude", RepoCacheKey: "amux"})
	if order := ids(got); order[0] != "c" {
		t.Fatalf("affinity ordering = %v, want c first", order)
	}
}

func TestCredentialProfilesAreUnrestrictedWhenRuntimeDeclaresNoAllowlist(t *testing.T) {
	r := New(Options{})
	caps := testCaps("claude", 2, 6)
	caps.CredentialProfiles = nil
	r.Register("hub-central-box", "h", caps)

	got := r.Candidates(ScheduleRequest{AgentType: "claude", CredentialProfiles: []string{"github", "slack"}})
	if len(got) != 1 || got[0].TargetID != "hub-central-box" {
		t.Fatalf("candidates = %v, want [hub-central-box]", ids(got))
	}
}

func TestRegistryRegisterPreservesLoadOnReconnect(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(Options{Clock: func() time.Time { return now }})
	r.Register("t1", "host-1", testCaps("claude", 2, 6))
	r.Heartbeat("t1", Load{CurrentSessions: 4})

	// Re-register (reconnect) without an immediate heartbeat.
	r.Register("t1", "host-1", testCaps("claude", 2, 6))
	rt, _ := r.Get("t1")
	if rt.Load.CurrentSessions != 4 {
		t.Fatalf("reconnect dropped load to %d, want 4 preserved", rt.Load.CurrentSessions)
	}
}

func TestRegistryRemove(t *testing.T) {
	r := New(Options{})
	r.Register("t1", "host-1", testCaps("claude", 2, 6))
	r.Remove("t1")
	if _, ok := r.Get("t1"); ok {
		t.Fatal("expected t1 removed")
	}
}

func ids(rts []*AgentRuntime) []string {
	out := make([]string, len(rts))
	for i, rt := range rts {
		out[i] = rt.TargetID
	}
	return out
}
