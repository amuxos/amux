package adapter

import (
	"reflect"
	"testing"
)

func TestClaudeAdapterUsesSDKSidecar(t *testing.T) {
	a := &ClaudeAdapter{}

	cmd := a.Cmd([]string{"--model", "opus"})
	if got, want := cmd.Args, []string{"claude", "--model", "opus"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Cmd() Args = %v, want %v", got, want)
	}
	caps := a.Capabilities()
	if caps.Protocol != ProtocolClaudeSDKSidecar {
		t.Fatalf("Capabilities().Protocol = %q, want %q", caps.Protocol, ProtocolClaudeSDKSidecar)
	}
	if caps.Acp {
		t.Fatal("Capabilities().Acp = true, want false")
	}
	if !caps.Interrupt {
		t.Fatal("Capabilities().Interrupt = false, want true")
	}
	if caps.InterruptStrategy != InterruptCtrlC {
		t.Fatalf("Capabilities().InterruptStrategy = %q, want %q", caps.InterruptStrategy, InterruptCtrlC)
	}
}

func TestAdapterRegistryIncludesClaude(t *testing.T) {
	a, err := Get("claude")
	if err != nil {
		t.Fatalf("Get(claude): %v", err)
	}
	if _, ok := a.(*ClaudeAdapter); !ok {
		t.Fatalf("adapter type = %T, want *ClaudeAdapter", a)
	}
}
