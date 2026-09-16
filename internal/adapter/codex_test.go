package adapter

import (
	"reflect"
	"testing"
)

func TestCodexAdapterUsesAppServerMode(t *testing.T) {
	a := &CodexAdapter{}
	cmd := a.Cmd([]string{"--model", "gpt-5"})
	if got, want := cmd.Args, []string{"codex", "--model", "gpt-5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cmd args = %#v, want %#v", got, want)
	}
	caps := a.Capabilities()
	if caps.Protocol != ProtocolCodexAppServer {
		t.Fatalf("Capabilities().Protocol = %q, want %q", caps.Protocol, ProtocolCodexAppServer)
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

func TestAdapterRegistryIncludesCodex(t *testing.T) {
	a, err := Get("codex")
	if err != nil {
		t.Fatalf("Get(codex): %v", err)
	}
	if _, ok := a.(*CodexAdapter); !ok {
		t.Fatalf("adapter type = %T, want *CodexAdapter", a)
	}
}
