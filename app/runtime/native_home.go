package runtime

import (
	"slices"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

// Isolated agents that declare SkipHostTrustUnderDAC must never pre-trust or
// clean up a workspace using the Runtime service user's native configuration.
// The process backend owns their native HOME; the service cannot mutate it.
func hostWorkspaceTrust(snap session.Snapshot) bool {
	if snap.ExecutionIdentity.IsolationBackend != principal.BackendLinuxDAC {
		return true
	}
	return !adapter.ExecutionCapabilitiesFor(snap.AgentType).IsolatedTrust.SkipHostTrustUnderDAC
}

func nativeWorkspaceTrustArgs(snap session.Snapshot, args []string) []string {
	if hostWorkspaceTrust(snap) {
		return args
	}
	trust := adapter.ExecutionCapabilitiesFor(snap.AgentType).IsolatedTrust
	if trust.LaunchApproveArg == "" {
		return args
	}
	for _, arg := range args {
		if slices.Contains(trust.ExplicitApproveFlags, arg) {
			return args
		}
	}
	return append(append([]string(nil), args...), trust.LaunchApproveArg)
}
