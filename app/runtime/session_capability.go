package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessionprotocol"
)

// This runs for raw, Profile, resumed and Task-owned launches. It adds guidance,
// never a control credential or MCP server to a Task child.
func (s *server) configureSessionCapability(ctx context.Context, sess *session.Session, args, env []string) ([]string, []string, error) {
	// An unconfigured server (used by narrow process fixtures) has no resource
	// root. Never turn an empty data directory into writes under the caller cwd.
	if s.amuxDir == "" {
		return args, memoryprotocol.SetEnv(env, sessionprotocol.InstructionEnv, ""), nil
	}
	snap := sess.Snapshot()
	child := snap.OwnerTaskId != ""
	transport := "unavailable; report this if session/schedule control is requested"
	if !child {
		if sessionCapabilityUsesCLI(snap) {
			var err error
			var cliArgs, cliEnv []string
			cliArgs, cliEnv, err = s.prepareAmuxTools(ctx, snap, args, env, true)
			if err != nil {
				// Discovery must not prevent ordinary model work when the CLI is absent.
				transport = "unavailable; scoped Amux CLI could not be prepared"
			} else {
				args, env = cliArgs, cliEnv
				transport = "CLI (amux in the injected PATH)"
			}
		} else if s.sessionMCPConfigured(snap, args) && snap.ExecutionIdentity.IsolationBackend != principal.BackendLinuxDAC {
			transport = "MCP (the exposed Amux tools; do not substitute shell CLI)"
		} else if s.sessionMCPConfigured(snap, args) {
			// Configuring a server is insufficient under DAC when its command
			// still points inside the service account's private installation.
			var toolArgs, toolEnv []string
			var err error
			toolArgs, toolEnv, err = s.prepareAmuxTools(ctx, snap, args, env, false)
			if err != nil {
				transport = "unavailable; scoped Amux control tools could not be prepared"
			} else {
				args, env = toolArgs, toolEnv
				transport = "MCP (the exposed Amux tools; do not substitute shell CLI)"
			}
		}
	}
	path, err := s.materializeSessionSkill(ctx, snap)
	if err != nil {
		return nil, nil, err
	}
	instruction := sessionprotocol.Instruction(path, transport, child)
	env = memoryprotocol.SetEnv(env, sessionprotocol.InstructionEnv, instruction)
	if !adapter.IsCodexCompatible(snap.AgentType) {
		layout, err := adapter.Layout(snap.AgentType)
		if err != nil {
			return nil, nil, err
		}
		if placement := layout[adapter.SurfaceSystemPrompt]; placement.Supported && placement.Kind == "extra-arg" && placement.Path != "" {
			args = sessionprotocol.AppendPrompt(args, placement.Path, instruction)
		} else if err := s.materializeSessionDiscovery(ctx, snap, env, layout[adapter.SurfaceSkills]); err != nil {
			return nil, nil, err
		}
	}
	return args, env, nil
}

// File-based extension hosts can share their MCP file with model, permission,
// and user settings. Under DAC that file belongs to the principal: use the
// scoped CLI rather than replacing it with a newly rendered partial file.
func sessionCapabilityUsesCLI(snap session.Snapshot) bool {
	exec := adapter.ExecutionCapabilitiesFor(snap.AgentType)
	return exec.AlwaysUseCLIForCapabilities || (snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC && exec.MCPArgSurface == adapter.MCPArgSurfaceNone)
}

func (s *server) sessionMCPConfigured(snap session.Snapshot, args []string) bool {
	for _, arg := range args {
		if strings.Contains(arg, "mcp_servers.amux.enabled=false") {
			return false
		}
	}
	if adapter.ExecutionCapabilitiesFor(snap.AgentType).MCPArgSurface == adapter.MCPArgSurfaceClaudeConfig {
		return memoryMCPConfigured(snap.AgentType, args)
	}
	if runArgsContainAmuxMCP(snap.AgentType, args) {
		return true
	}
	// File-based extension hosts may need no launch arguments. Their declared
	// layout is materialized before this boundary when auto-injection is enabled.
	a, err := adapter.Get(snap.AgentType)
	if err != nil || s.disableMCPAutoInject {
		return false
	}
	placement := a.Capabilities().ConfigLayout[adapter.SurfaceMCP]
	return placement.Supported && placement.Kind != "extra-arg"
}

func (s *server) materializeSessionSkill(ctx context.Context, snap session.Snapshot) (string, error) {
	root := filepath.Join(profileSessionDir(s.amuxDir, snap.ID), "agent-config", "runtime-skills")
	if snap.ExecutionIdentity.IsolationBackend != principal.BackendLinuxDAC {
		return filepath.Join(root, sessionprotocol.SkillName, "SKILL.md"), writeSessionSkill(root)
	}
	if s.isolationRuntime == nil {
		return "", os.ErrPermission
	}
	paths, _, err := s.isolationRuntime.PrepareExecution(ctx, snap.ExecutionIdentity, snap.LaunchID, snap.Cwd)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp("", "amux-runtime-skills-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := writeSessionSkill(filepath.Join(stage, "runtime-skills")); err != nil {
		return "", err
	}
	if err := s.isolationRuntime.MaterializeExecution(ctx, snap.ExecutionIdentity, snap.LaunchID, snap.Cwd, []isolation.BundleSource{{Source: stage, Prefix: "agent-config"}}); err != nil {
		return "", err
	}
	return filepath.Join(paths.Run, "agent-config", "runtime-skills", sessionprotocol.SkillName, "SKILL.md"), nil
}

func writeSessionSkill(root string) error {
	return sessionprotocol.WriteFiles(func(path string, data []byte) error {
		path = filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o600)
	})
}
