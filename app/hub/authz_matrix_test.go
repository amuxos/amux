package hub

import "testing"

// taskRouterAuthzRule names how one agent-operable RPC is authorized. Every
// such RPC must appear exactly once here; adding an RPC without a declared rule
// fails this test, which is the only thing standing between a new RPC and one
// that ships with no authorization.
var taskRouterAuthzRules = map[string]string{
	"SubmitSubtask":            "mutate", // rule five: parent_session_id claim authorized at submit
	"CancelSubtask":            "mutate",
	"RespondSubtaskPermission": "mutate",
	"GetSubtask":               "read",
	"StreamSubtask":            "read",
	"ListSubtasks":             "list",
	"PruneSubtasks":            "prune",
	"SendAgentMessage":         "mutate",
	"InterruptAgentInstance":   "mutate",
	"GetAgentInstance":         "read",
	"WaitAgentInstance":        "read",
	"DescribeAgentInstance":    "read",
	"ListAgentInstances":       "list",
}

// TestEveryAgentOperableRPCDeclaresAnAuthzRule pins that the set of RPCs an
// agent can reach is exactly the set with a declared authorization rule. The
// method list is the contract; an RPC implemented on taskRouter but missing
// here, or an entry here with no implementation, is a drift this test catches.
//
// taskRouter has two more exported methods, deliberately absent from both maps:
// SubmitScheduledTask (an in-process helper the schedule surface uses to wrap
// SubmitSubtask, not a wire RPC) and ReclaimInstancesForOwner (an internal
// reconciliation entry point driven by owner-session archival, not callable
// over gRPC). Neither is agent-operable, so neither carries an authz rule.
func TestEveryAgentOperableRPCDeclaresAnAuthzRule(t *testing.T) {
	implemented := map[string]bool{
		"SubmitSubtask":            true,
		"CancelSubtask":            true,
		"RespondSubtaskPermission": true,
		"GetSubtask":               true,
		"StreamSubtask":            true,
		"ListSubtasks":             true,
		"PruneSubtasks":            true,
		"SendAgentMessage":         true,
		"InterruptAgentInstance":   true,
		"GetAgentInstance":         true,
		"WaitAgentInstance":        true,
		"DescribeAgentInstance":    true,
		"ListAgentInstances":       true,
	}
	for name := range taskRouterAuthzRules {
		if !implemented[name] {
			t.Errorf("authz rule declared for %q but no such taskRouter RPC exists", name)
		}
	}
	for name := range implemented {
		if _, ok := taskRouterAuthzRules[name]; !ok {
			t.Errorf("taskRouter RPC %q has no declared authz rule", name)
		}
	}
}

// hubServerSessionAuthzRule names how one hub-side session RPC is authorized.
// The session control plane lives on hubServer (not taskRouter), so it gets a
// parallel matrix: same drift guarantee, one declared rule per RPC.
//
// Rule labels:
//   - create: Run authorizes at creation through executionauth.Policy before
//     the session exists.
//   - mutate: authorizeSessionMutation (shared without fleet isolation;
//     same-principal access with isolation, including delegated siblings).
//   - read: authorizeSessionRead — the same decision core as mutate; a caller
//     may not read a session it may not mutate.
//   - prune: authorizeSessionPrune — a fleet-wide delete of archived sessions
//     restricted to enrollment actors while fleet isolation is active.
var hubServerSessionAuthzRules = map[string]string{
	"Run":               "create",
	"Kill":              "mutate",
	"Restart":           "mutate",
	"ChangeCwd":         "mutate",
	"Sleep":             "mutate",
	"Resume":            "mutate",
	"Interrupt":         "mutate",
	"Submit":            "mutate",
	"QueueClear":        "mutate",
	"QueueCancel":       "mutate",
	"RespondPermission": "mutate",
	"Attach":            "read",
	"QueueList":         "read",
	"Prune":             "prune",
}

// TestEverySessionRPCDeclaresAnAuthzRule pins that the set of session RPCs on
// hubServer is exactly the set with a declared authorization rule. A session
// RPC added to hubServer without a rule here — or a rule here naming no
// implemented RPC — is the drift this test catches.
//
// ListTargets is deliberately absent from both maps: it is the target list,
// not a session action, and its principal filtering is the visibility tier
// (executionauth.Policy.SessionListAccess) these per-session rules sit behind.
func TestEverySessionRPCDeclaresAnAuthzRule(t *testing.T) {
	implemented := map[string]bool{
		"Run":               true,
		"Kill":              true,
		"Restart":           true,
		"ChangeCwd":         true,
		"Sleep":             true,
		"Resume":            true,
		"Interrupt":         true,
		"Submit":            true,
		"QueueClear":        true,
		"QueueCancel":       true,
		"RespondPermission": true,
		"Attach":            true,
		"QueueList":         true,
		"Prune":             true,
	}
	for name := range hubServerSessionAuthzRules {
		if !implemented[name] {
			t.Errorf("authz rule declared for %q but no such hubServer session RPC exists", name)
		}
	}
	for name := range implemented {
		if _, ok := hubServerSessionAuthzRules[name]; !ok {
			t.Errorf("hubServer session RPC %q has no declared authz rule", name)
		}
	}
}
