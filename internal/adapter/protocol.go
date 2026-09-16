package adapter

import "strings"

// IsCodexCompatible reports whether a registered agent uses the Codex-compatible
// app-server interface, including config overrides and session controls.
func IsCodexCompatible(agentType string) bool {
	a, err := Get(strings.TrimSpace(agentType))
	if err != nil {
		return false
	}
	p := a.Capabilities().Protocol
	return p == ProtocolCodexAppServer || distributionCodexCompatible(p)
}

// StructuredProvider returns the broker provider ID and optional watchdog
// configuration key. Empty ID means this is not a supported structured protocol.
func (p Protocol) StructuredProvider() (id, watchdogKey string) {
	switch p {
	case ProtocolClaudeSDKSidecar:
		return "claude-sdk", ""
	case ProtocolCodexAppServer:
		return "codex-app-server", "codex"
	case ProtocolPiRPC:
		return "pi-rpc", ""
	default:
		return distributionStructuredProvider(p)
	}
}
