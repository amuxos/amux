package adapter

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestExecutionMatrixPublicEngines(t *testing.T) {
	matrix := ExecutionMatrix()
	byName := make(map[string]ExecutionMatrixEntry, len(matrix))
	for _, entry := range matrix {
		byName[entry.AgentType] = entry
	}
	for _, tc := range []struct {
		agent          string
		permSurface    PermissionSurface
		mcpSurface     MCPArgSurface
		alwaysCLI      bool
		memorySupport  bool
		memoryPrompt   bool
		memoryCLIOnly  bool
		disableMemEnv  string
		instructions   SessionInstructionsMode
		skipHostTrust  bool
		launchApprove  string
		allowDenyAll   bool
		allowEscalate  bool
		isolatedEnvKey string
		isolatedRel    string
		ensureDir      bool
	}{
		{
			agent:          "codex",
			permSurface:    PermissionSurfaceCodexConfig,
			mcpSurface:     MCPArgSurfaceCodexConfig,
			alwaysCLI:      false,
			memorySupport:  true,
			instructions:   SessionInstructionsEnv,
			skipHostTrust:  true,
			allowDenyAll:   true,
			allowEscalate:  true,
			isolatedEnvKey: "CODEX_HOME",
			isolatedRel:    ".codex",
			ensureDir:      true,
		},
		{
			agent:          "claude",
			permSurface:    PermissionSurfaceClaudeMode,
			mcpSurface:     MCPArgSurfaceClaudeConfig,
			alwaysCLI:      false,
			memorySupport:  true,
			memoryPrompt:   true,
			disableMemEnv:  "CLAUDE_CODE_DISABLE_AUTO_MEMORY",
			instructions:   SessionInstructionsSystemPrompt,
			skipHostTrust:  true,
			allowDenyAll:   true,
			allowEscalate:  true,
			isolatedEnvKey: "CLAUDE_CONFIG_DIR",
			isolatedRel:    "",
			ensureDir:      false,
		},
		{
			agent:          "pi",
			permSurface:    PermissionSurfaceNone,
			mcpSurface:     MCPArgSurfaceNone,
			alwaysCLI:      true,
			memorySupport:  true,
			memoryPrompt:   true,
			memoryCLIOnly:  true,
			instructions:   SessionInstructionsSystemPrompt,
			skipHostTrust:  true,
			launchApprove:  "--approve",
			allowDenyAll:   false,
			allowEscalate:  false,
			isolatedEnvKey: "PI_CODING_AGENT_DIR",
			isolatedRel:    ".pi/agent",
			ensureDir:      false,
		},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			entry, ok := byName[tc.agent]
			if !ok {
				t.Fatalf("agent %q missing from ExecutionMatrix()", tc.agent)
			}
			exec := entry.Execution
			if exec.PermissionSurface != tc.permSurface {
				t.Fatalf("PermissionSurface = %q, want %q", exec.PermissionSurface, tc.permSurface)
			}
			if exec.MCPArgSurface != tc.mcpSurface {
				t.Fatalf("MCPArgSurface = %q, want %q", exec.MCPArgSurface, tc.mcpSurface)
			}
			if exec.AlwaysUseCLIForCapabilities != tc.alwaysCLI {
				t.Fatalf("AlwaysUseCLIForCapabilities = %v, want %v", exec.AlwaysUseCLIForCapabilities, tc.alwaysCLI)
			}
			if exec.Memory.Supported != tc.memorySupport ||
				exec.Memory.PromptInjection != tc.memoryPrompt ||
				exec.Memory.AlwaysUseCLIInstruction != tc.memoryCLIOnly ||
				exec.Memory.DisableNativeAutoMemoryEnv != tc.disableMemEnv {
				t.Fatalf("Memory = %+v, unexpected", exec.Memory)
			}
			if exec.SessionInstructions != tc.instructions {
				t.Fatalf("SessionInstructions = %q, want %q", exec.SessionInstructions, tc.instructions)
			}
			if exec.IsolatedTrust.SkipHostTrustUnderDAC != tc.skipHostTrust || exec.IsolatedTrust.LaunchApproveArg != tc.launchApprove {
				t.Fatalf("IsolatedTrust = %+v, unexpected", exec.IsolatedTrust)
			}
			if !exec.SupportsTaskPermissionPolicy("") || !exec.SupportsTaskPermissionPolicy("policy-allow") {
				t.Fatal("all engines must support default / policy-allow")
			}
			if exec.SupportsTaskPermissionPolicy("deny-all") != tc.allowDenyAll {
				t.Fatalf("SupportsTaskPermissionPolicy(deny-all) = %v, want %v", !tc.allowDenyAll, tc.allowDenyAll)
			}
			if exec.SupportsTaskPermissionPolicy("escalate-to-main") != tc.allowEscalate {
				t.Fatalf("SupportsTaskPermissionPolicy(escalate-to-main) = %v, want %v", !tc.allowEscalate, tc.allowEscalate)
			}
			if exec.SupportsTaskPermissionPolicy("unknown") {
				t.Fatal("unknown policy must return false")
			}
			if len(exec.IsolatedHomeEnv) != 1 ||
				exec.IsolatedHomeEnv[0].Key != tc.isolatedEnvKey ||
				exec.IsolatedHomeEnv[0].HomeRel != tc.isolatedRel ||
				exec.IsolatedHomeEnv[0].EnsureDir != tc.ensureDir {
				t.Fatalf("IsolatedHomeEnv = %+v", exec.IsolatedHomeEnv)
			}
		})
	}
}

func TestAllIsolatedHomeEnvBindingsAndEnsureDirs(t *testing.T) {
	home := filepath.Join("/srv", "principals", "u1")
	bindings := AllIsolatedHomeEnvBindings()
	got := make(map[string]string, len(bindings))
	for _, b := range bindings {
		got[b.Key] = b.ValueForHome(home)
	}
	want := map[string]string{
		"CODEX_HOME":          filepath.Join(home, ".codex"),
		"CLAUDE_CONFIG_DIR":   "",
		"PI_CODING_AGENT_DIR": filepath.Join(home, ".pi", "agent"),
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("binding %s = %q, want %q", k, got[k], v)
		}
	}
	ensureDirs := IsolatedHomeEnsureDirs()
	if !slices.Contains(ensureDirs, ".codex") {
		t.Fatalf("IsolatedHomeEnsureDirs() = %v, want .codex", ensureDirs)
	}
}

func TestArgsContainAndStripAmuxMCP(t *testing.T) {
	codexArgs := []string{"--model", "m1", "-c", `mcp_servers.amux.command="amux"`, "-c", `approval_policy="never"`}
	if !ArgsContainAmuxMCP("codex", codexArgs) {
		t.Fatal("expected codex args to contain amux MCP")
	}
	if got := StripAmuxMCPArgs("codex", codexArgs); !slices.Equal(got, []string{"--model", "m1", "-c", `approval_policy="never"`}) {
		t.Fatalf("StripAmuxMCPArgs(codex) = %q", got)
	}

	claudeArgs := []string{"--model", "sonnet", "--mcp-config", "/tmp/claude-mcp.json", "--debug"}
	if !ArgsContainAmuxMCP("claude", claudeArgs) {
		t.Fatal("expected claude args to contain amux MCP")
	}
	if got := StripAmuxMCPArgs("claude", claudeArgs); !slices.Equal(got, []string{"--model", "sonnet", "--debug"}) {
		t.Fatalf("StripAmuxMCPArgs(claude) = %q", got)
	}

	piArgs := []string{"--model", "m1", "--mcp-config", "/tmp/ignored.json"}
	if ArgsContainAmuxMCP("pi", piArgs) {
		t.Fatal("pi has no MCP arg surface")
	}
	if got := StripAmuxMCPArgs("pi", piArgs); !slices.Equal(got, piArgs) {
		t.Fatalf("StripAmuxMCPArgs(pi) modified args: %q", got)
	}
}
