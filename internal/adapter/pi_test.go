package adapter

import (
	"reflect"
	"testing"
)

func TestPiAdapterUsesRPCMode(t *testing.T) {
	a := &PiAdapter{}
	cmd := a.Cmd([]string{"--provider", "deepseek", "--model", "deepseek-v4-flash"})
	if got, want := cmd.Args, []string{"pi", "--mode", "rpc", "--provider", "deepseek", "--model", "deepseek-v4-flash"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cmd args = %#v, want %#v", got, want)
	}
	caps := a.Capabilities()
	if caps.Protocol != ProtocolPiRPC {
		t.Fatalf("Capabilities().Protocol = %q, want %q", caps.Protocol, ProtocolPiRPC)
	}
	if caps.Protocol == ProtocolCodexAppServer || caps.Protocol == ProtocolACP {
		t.Fatalf("Capabilities().Protocol = %q, must be a distinct pi protocol", caps.Protocol)
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
	if !caps.NativeResult {
		t.Fatal("Capabilities().NativeResult = false, want true")
	}
	if caps.CwdSwitch != CwdSwitchUnsupported {
		t.Fatalf("Capabilities().CwdSwitch = %q, want unsupported (empty)", caps.CwdSwitch)
	}
}

// TestPiAdapterConfigLayout locks the Agent Profile surface placements so pi
// projection (rules/skills/system prompt) cannot silently drift from the
// adapter capability contract. MCP stays unsupported: pi has no MCP host.
func TestPiAdapterConfigLayout(t *testing.T) {
	a := &PiAdapter{}
	layout := a.Capabilities().ConfigLayout
	if layout == nil {
		t.Fatal("ConfigLayout = nil, want surface placements")
	}
	rules := layout[SurfaceRules]
	if !rules.Supported || rules.Kind != "cwd-rel" || rules.Path != "AGENTS.md" {
		t.Fatalf("rules placement = %+v, want cwd-rel AGENTS.md", rules)
	}
	skills := layout[SurfaceSkills]
	if !skills.Supported || skills.Kind != "cwd-rel" || skills.Path != ".pi/skills" {
		t.Fatalf("skills placement = %+v, want cwd-rel .pi/skills", skills)
	}
	sp := layout[SurfaceSystemPrompt]
	if !sp.Supported || sp.Kind != "extra-arg" || sp.Path != "--append-system-prompt" {
		t.Fatalf("system prompt placement = %+v, want extra-arg --append-system-prompt", sp)
	}
	if mcp := layout[SurfaceMCP]; mcp.Supported {
		t.Fatalf("mcp placement = %+v, want unsupported", mcp)
	}
}

func TestAdapterRegistryIncludesPi(t *testing.T) {
	a, err := Get("pi")
	if err != nil {
		t.Fatalf("Get(pi): %v", err)
	}
	if _, ok := a.(*PiAdapter); !ok {
		t.Fatalf("adapter type = %T, want *PiAdapter", a)
	}
	names := Names()
	found := false
	for _, n := range names {
		if n == "pi" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Names() = %v, want it to include pi", names)
	}
}
