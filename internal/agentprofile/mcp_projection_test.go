package agentprofile

import (
	"github.com/amuxos/amux/internal/task"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAmuxCommandPrefersStableInstallationBinary(t *testing.T) {
	home := t.TempDir()
	installed := filepath.Join(home, "bin", "amux")
	if err := os.MkdirAll(filepath.Dir(installed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := amuxCommand(home); got != installed {
		t.Fatalf("amuxCommand(%q) = %q, want stable installation binary %q", home, got, installed)
	}
}

func TestAmuxCommandIgnoresNonExecutableInstallationFile(t *testing.T) {
	home := t.TempDir()
	installed := filepath.Join(home, "bin", "amux")
	if err := os.MkdirAll(filepath.Dir(installed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("not executable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := amuxCommand(home); got == installed {
		t.Fatalf("amuxCommand(%q) selected non-executable installation file", home)
	}
}

// The amux MCP server launched by an agent is deliberately given only a small
// whitelist of variables plus whatever this projection writes — for
// Codex the agent passes a curated default set (HOME, PATH, ...) and
// never inherits the agent's own environment. Anything the MCP server needs
// must therefore be written into its config explicitly. AMUX_ACTOR_TOKEN is
// one of those things: without it the MCP server reaches the hub anonymously
// and, since schedules and tasks are owner-scoped, sees nothing.
func TestInlineMCPEnvIncludesActorToken(t *testing.T) {
	got := inlineMCPEnv("/home/u/.amux", "sess-1", "tok-secret", "/home/u/.amux/sessions/sess-1.actor-token")
	if !strings.Contains(got, `AMUX_ACTOR_TOKEN="tok-secret"`) {
		t.Fatalf("inlineMCPEnv = %q, want it to carry AMUX_ACTOR_TOKEN", got)
	}
	if !strings.Contains(got, `AMUX_ACTOR_TOKEN_FILE="/home/u/.amux/sessions/sess-1.actor-token"`) {
		t.Fatalf("inlineMCPEnv = %q, want it to carry AMUX_ACTOR_TOKEN_FILE", got)
	}
	if !strings.Contains(got, `AMUX_HOME="/home/u/.amux"`) || !strings.Contains(got, `AMUX_SESSION_ID="sess-1"`) {
		t.Fatalf("inlineMCPEnv = %q, want AMUX_HOME and AMUX_SESSION_ID preserved", got)
	}
}

func TestInlineMCPEnvOmitsActorTokenWhenEmpty(t *testing.T) {
	got := inlineMCPEnv("/home/u/.amux", "sess-1", "", "")
	if strings.Contains(got, "AMUX_ACTOR_TOKEN") {
		t.Fatalf("inlineMCPEnv = %q, want no AMUX_ACTOR_TOKEN when the launch has none", got)
	}
}

// The token file is optional: callers without a persisted credential (task
// launches) pass "" and get exactly the old env.
func TestInlineMCPEnvOmitsActorTokenFileWhenEmpty(t *testing.T) {
	got := inlineMCPEnv("/home/u/.amux", "sess-1", "tok-secret", "")
	if strings.Contains(got, "AMUX_ACTOR_TOKEN_FILE") {
		t.Fatalf("inlineMCPEnv = %q, want no AMUX_ACTOR_TOKEN_FILE without a path", got)
	}
	if !strings.Contains(got, `AMUX_ACTOR_TOKEN="tok-secret"`) {
		t.Fatalf("inlineMCPEnv = %q, want AMUX_ACTOR_TOKEN preserved", got)
	}
}

func TestMCPEnvIncludesActorToken(t *testing.T) {
	got := mcpEnv("/home/u/.amux", "sess-1", "tok-secret", "/home/u/.amux/sessions/sess-1.actor-token")
	if got["AMUX_ACTOR_TOKEN"] != "tok-secret" {
		t.Fatalf("mcpEnv = %v, want AMUX_ACTOR_TOKEN=tok-secret", got)
	}
	if got["AMUX_ACTOR_TOKEN_FILE"] != "/home/u/.amux/sessions/sess-1.actor-token" {
		t.Fatalf("mcpEnv = %v, want AMUX_ACTOR_TOKEN_FILE", got)
	}
}

func TestMCPEnvOmitsActorTokenWhenEmpty(t *testing.T) {
	if got := mcpEnv("/home/u/.amux", "sess-1", "", ""); len(got) != 0 {
		if _, ok := got["AMUX_ACTOR_TOKEN"]; ok {
			t.Fatalf("mcpEnv = %v, want no AMUX_ACTOR_TOKEN when the launch has none", got)
		}
	}
}

func TestMCPEnvOmitsActorTokenFileWhenEmpty(t *testing.T) {
	got := mcpEnv("/home/u/.amux", "sess-1", "tok-secret", "")
	if _, ok := got["AMUX_ACTOR_TOKEN_FILE"]; ok {
		t.Fatalf("mcpEnv = %v, want no AMUX_ACTOR_TOKEN_FILE without a path", got)
	}
}

func TestMaterializeCodexAmuxMCPCarriesActorToken(t *testing.T) {
	sb, err := task.PrepareSandbox(t.TempDir(), "sess-1", "", task.IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	args, err := MaterializeAmuxMCP("codex", sb, nil, "/home/u/.amux", "tok-secret", "/home/u/.amux/sessions/sess-1.actor-token")
	if err != nil {
		t.Fatalf("MaterializeAmuxMCP: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `AMUX_ACTOR_TOKEN="tok-secret"`) {
		t.Fatalf("codex mcp args = %q, want AMUX_ACTOR_TOKEN in the inline env", joined)
	}
	if !strings.Contains(joined, `AMUX_ACTOR_TOKEN_FILE="/home/u/.amux/sessions/sess-1.actor-token"`) {
		t.Fatalf("codex mcp args = %q, want AMUX_ACTOR_TOKEN_FILE in the inline env", joined)
	}
}

func TestMaterializeClaudeAmuxMCPCarriesActorToken(t *testing.T) {
	sb, err := task.PrepareSandbox(t.TempDir(), "sess-1", "", task.IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	args, err := MaterializeAmuxMCP("claude", sb, nil, "/home/u/.amux", "tok-secret", "/home/u/.amux/sessions/sess-1.actor-token")
	if err != nil {
		t.Fatalf("MaterializeAmuxMCP: %v", err)
	}
	if len(args) != 2 || args[0] != "--mcp-config" {
		t.Fatalf("claude mcp args = %q, want --mcp-config <path>", args)
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatalf("read mcp config: %v", err)
	}
	if !strings.Contains(string(data), `"AMUX_ACTOR_TOKEN_FILE": "/home/u/.amux/sessions/sess-1.actor-token"`) {
		t.Fatalf("claude mcp config = %s, want AMUX_ACTOR_TOKEN_FILE", data)
	}
}
