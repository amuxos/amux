package adapter

import "testing"

func TestStructuredProtocolBindings(t *testing.T) {
	for _, tc := range []struct {
		protocol Protocol
		provider string
		watchdog string
	}{
		{ProtocolClaudeSDKSidecar, "claude-sdk", ""},
		{ProtocolCodexAppServer, "codex-app-server", "codex"},
		{ProtocolPiRPC, "pi-rpc", ""},
		{ProtocolACP, "", ""},
		{Protocol("unknown"), "", ""},
	} {
		provider, watchdog := tc.protocol.StructuredProvider()
		if provider != tc.provider || watchdog != tc.watchdog {
			t.Fatalf("%s binding = (%q, %q), want (%q, %q)", tc.protocol, provider, watchdog, tc.provider, tc.watchdog)
		}
	}
}

func TestStructuredProtocolCodexCompatibility(t *testing.T) {
	for agentType, want := range map[string]bool{"codex": true, " codex ": true, "claude": false, "pi": false, "unknown": false} {
		if got := IsCodexCompatible(agentType); got != want {
			t.Errorf("IsCodexCompatible(%q) = %v, want %v", agentType, got, want)
		}
	}
}
