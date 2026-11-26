package adapter

import "os/exec"

// CodexAdapter runs the `codex` CLI as a subprocess.
type CodexAdapter struct{}

func (a *CodexAdapter) Cmd(extraArgs []string) *exec.Cmd {
	args := append([]string{}, extraArgs...)
	return exec.Command("codex", args...)
}

func (a *CodexAdapter) Capabilities() Capabilities {
	return normalizeCapabilities(Capabilities{
		Protocol:          ProtocolCodexAppServer,
		Interrupt:         true,
		InterruptStrategy: InterruptCtrlC,
		CwdSwitch:         CwdSwitchResume,
		NativeResult:      true,
		ConfigLayout: map[Surface]Placement{
			SurfaceRules:  {Supported: true, Kind: "cwd-rel", Path: "AGENTS.md"},
			SurfaceSkills: {Supported: true, Kind: "cwd-rel", Path: ".agents/skills"},
			SurfaceMCP:    {Supported: true, Kind: "extra-arg", Path: "-c"},
		},
		Execution: ExecutionCapabilities{
			IsolatedHomeEnv: []IsolationEnvBinding{
				{Key: "CODEX_HOME", HomeRel: ".codex", EnsureDir: true},
			},
			PermissionSurface:   PermissionSurfaceCodexConfig,
			MCPArgSurface:       MCPArgSurfaceCodexConfig,
			Memory:              MemoryContract{Supported: true},
			SessionInstructions: SessionInstructionsEnv,
			IsolatedTrust:       IsolatedTrustPolicy{SkipHostTrustUnderDAC: true},
		},
	})
}
