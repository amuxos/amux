package adapter

import "os/exec"

// ClaudeAdapter runs the `claude` CLI as a subprocess.
type ClaudeAdapter struct{}

func (a *ClaudeAdapter) Cmd(extraArgs []string) *exec.Cmd {
	args := append([]string{}, extraArgs...)
	return exec.Command("claude", args...)
}

func (a *ClaudeAdapter) Capabilities() Capabilities {
	return normalizeCapabilities(Capabilities{
		Protocol:          ProtocolClaudeSDKSidecar,
		Interrupt:         true,
		InterruptStrategy: InterruptCtrlC,
		NativeResult:      true,
		ConfigLayout: map[Surface]Placement{
			SurfaceSkills:       {Supported: true, Kind: "home-rel", Path: ".claude/skills"},
			SurfaceRules:        {Supported: true, Kind: "cwd-rel", Path: "AGENTS.md"},
			SurfaceSystemPrompt: {Supported: true, Kind: "extra-arg", Path: "--append-system-prompt"},
			SurfaceMCP:          {Supported: true, Kind: "agent-config-rel", Path: "claude-mcp.json"},
		},
		Execution: ExecutionCapabilities{
			IsolatedHomeEnv: []IsolationEnvBinding{
				// Empty masks any shared service-user override so Claude uses
				// the isolated HOME's native default layout (including HOME/.claude.json).
				{Key: "CLAUDE_CONFIG_DIR", HomeRel: ""},
			},
			PermissionSurface: PermissionSurfaceClaudeMode,
			MCPArgSurface:     MCPArgSurfaceClaudeConfig,
			Memory: MemoryContract{
				Supported:                  true,
				PromptInjection:            true,
				DisableNativeAutoMemoryEnv: "CLAUDE_CODE_DISABLE_AUTO_MEMORY",
			},
			SessionInstructions: SessionInstructionsSystemPrompt,
			IsolatedTrust:       IsolatedTrustPolicy{SkipHostTrustUnderDAC: true},
		},
	})
}
