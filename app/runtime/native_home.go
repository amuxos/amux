package runtime

import (
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

// Built-in isolated agents must never pre-trust or clean up a workspace using
// the Runtime service user's native configuration. The process backend owns
// their native HOME; the service cannot mutate that home directly.
func hostWorkspaceTrust(snap session.Snapshot) bool {
	if snap.ExecutionIdentity.IsolationBackend != principal.BackendLinuxDAC {
		return true
	}
	switch snap.AgentType {
	case "codex", "claude", "pi":
		return false
	default:
		return true
	}
}

func nativeWorkspaceTrustArgs(snap session.Snapshot, args []string) []string {
	if snap.AgentType != "pi" || hostWorkspaceTrust(snap) {
		return args
	}
	// Pi RPC mode does not prompt and otherwise ignores untrusted project
	// resources. Use its launch-local override rather than writing host trust.
	// An explicit user decision wins over amux's ordinary workspace pre-trust.
	for _, arg := range args {
		switch arg {
		case "--approve", "-a", "--no-approve", "-na":
			return args
		}
	}
	return append(append([]string(nil), args...), "--approve")
}
