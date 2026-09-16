package adapter

import "testing"

func TestConfigLayoutByAgent(t *testing.T) {
	// claude supports profile surfaces and per-launch amux MCP.
	cl, err := Layout("claude")
	if err != nil {
		t.Fatalf("Layout(claude): %v", err)
	}
	if !cl[SurfaceSkills].Supported || cl[SurfaceSkills].Kind != "home-rel" || cl[SurfaceSkills].Path != ".claude/skills" {
		t.Fatalf("claude skills placement wrong: %+v", cl[SurfaceSkills])
	}
	if !cl[SurfaceRules].Supported || cl[SurfaceRules].Kind != "cwd-rel" || cl[SurfaceRules].Path != "AGENTS.md" {
		t.Fatalf("claude rules placement wrong: %+v", cl[SurfaceRules])
	}
	if !cl[SurfaceSystemPrompt].Supported || cl[SurfaceSystemPrompt].Kind != "extra-arg" || cl[SurfaceSystemPrompt].Path != "--append-system-prompt" {
		t.Fatalf("claude system prompt placement wrong: %+v", cl[SurfaceSystemPrompt])
	}
	if !cl[SurfaceMCP].Supported || cl[SurfaceMCP].Kind != "agent-config-rel" || cl[SurfaceMCP].Path != "claude-mcp.json" {
		t.Fatalf("claude MCP placement wrong: %+v", cl[SurfaceMCP])
	}

	// codex supports rules and per-launch amux MCP, but not skills or the
	// --append-system-prompt flag.
	for _, name := range []string{"codex"} {
		l, err := Layout(name)
		if err != nil {
			t.Fatalf("Layout(%s): %v", name, err)
		}
		if !l[SurfaceRules].Supported || l[SurfaceRules].Kind != "cwd-rel" || l[SurfaceRules].Path != "AGENTS.md" {
			t.Fatalf("%s rules placement wrong: %+v", name, l[SurfaceRules])
		}
		if !l[SurfaceMCP].Supported || l[SurfaceMCP].Kind != "extra-arg" || l[SurfaceMCP].Path != "-c" {
			t.Fatalf("%s MCP placement wrong: %+v", name, l[SurfaceMCP])
		}
		if l[SurfaceSkills].Supported || l[SurfaceSystemPrompt].Supported {
			t.Fatalf("%s should support only rules and MCP, got %+v", name, l)
		}
	}

	if _, err := Layout("nope"); err == nil {
		t.Fatalf("Layout(unknown) should error")
	}
}

func TestCwdSwitchCapabilities(t *testing.T) {
	for _, name := range []string{"codex"} {
		a, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Capabilities().CwdSwitch; got != CwdSwitchResume {
			t.Fatalf("%s cwd switch = %q, want resume", name, got)
		}
	}
	for _, name := range []string{"claude", "pi"} {
		a, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Capabilities().CwdSwitch; got != CwdSwitchUnsupported {
			t.Fatalf("%s cwd switch = %q, want unsupported", name, got)
		}
	}
}
