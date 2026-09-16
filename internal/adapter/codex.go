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
			// codex reads AGENTS.md from its working directory. Skills (HOME-based)
			// and a system-prompt injection (codex -c instructions) are deferred to
			// Phase B (the codex config key is unverified).
			SurfaceRules: {Supported: true, Kind: "cwd-rel", Path: "AGENTS.md"},
			SurfaceMCP:   {Supported: true, Kind: "extra-arg", Path: "-c"},
		},
	})
}
