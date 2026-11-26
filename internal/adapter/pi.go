package adapter

import "os/exec"

// PiAdapter runs the `pi` coding agent in RPC mode (`pi --mode rpc`), a strict
// JSONL protocol over stdio: commands are JSON objects on stdin, responses and
// agent events are JSON objects streamed on stdout. pi has no ACP mode, so the
// adapter drives it through the structured provider path.
type PiAdapter struct{}

func (a *PiAdapter) Cmd(extraArgs []string) *exec.Cmd {
	args := append([]string{"--mode", "rpc"}, extraArgs...)
	return exec.Command("pi", args...)
}

func (a *PiAdapter) Capabilities() Capabilities {
	return normalizeCapabilities(Capabilities{
		Protocol:          ProtocolPiRPC,
		Interrupt:         true,
		InterruptStrategy: InterruptCtrlC,
		// pi's `--session-id` resumes a persisted pi session file best-effort
		// on restart, so native conversation survives amux session restarts.
		// Workspace switching (cwd_switch=resume) is deferred: a pi session
		// file is bound to the cwd it was created in, so switching cwd without
		// reloading the session is not yet validated.
		CwdSwitch:    CwdSwitchUnsupported,
		NativeResult: true,
		ConfigLayout: map[Surface]Placement{
			// pi auto-discovers AGENTS.md from its working directory, project
			// skills from <cwd>/.pi/skills and user skills from
			// ~/.pi/agent/skills (agent-pack-contract >= 0.4 renders pi skills
			// into .pi/skills). The system prompt is injectable via
			// --append-system-prompt. pi has no MCP support (its tools come from
			// built-ins and TypeScript extensions), so the built-in amux MCP is
			// not injected.
			SurfaceRules:        {Supported: true, Kind: "cwd-rel", Path: "AGENTS.md"},
			SurfaceSkills:       {Supported: true, Kind: "cwd-rel", Path: ".pi/skills"},
			SurfaceSystemPrompt: {Supported: true, Kind: "extra-arg", Path: "--append-system-prompt"},
		},
		Execution: ExecutionCapabilities{
			IsolatedHomeEnv: []IsolationEnvBinding{
				{Key: "PI_CODING_AGENT_DIR", HomeRel: ".pi/agent"},
			},
			PermissionSurface:           PermissionSurfaceNone,
			MCPArgSurface:               MCPArgSurfaceNone,
			AlwaysUseCLIForCapabilities: true,
			Memory: MemoryContract{
				Supported:               true,
				PromptInjection:         true,
				AlwaysUseCLIInstruction: true,
			},
			SessionInstructions: SessionInstructionsSystemPrompt,
			IsolatedTrust: IsolatedTrustPolicy{
				SkipHostTrustUnderDAC: true,
				// Pi RPC mode does not prompt and otherwise ignores untrusted
				// project resources. Use its launch-local override rather than
				// writing host trust; an explicit user decision wins.
				LaunchApproveArg:     "--approve",
				ExplicitApproveFlags: []string{"--approve", "-a", "--no-approve", "-na"},
			},
		},
	})
}
