package agentprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/task"
)

const (
	amuxMCPServerName         = "amux"
	amuxMCPToolTimeoutSeconds = 2 * 60 * 60
)

type amuxMCPConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type standardMCPConfig struct {
	MCPServers map[string]amuxMCPConfig `json:"mcpServers"`
}

// MaterializeAmuxMCP writes the built-in amux MCP server configuration for the
// agent into the supplied sandbox/workspace and returns any args needed to make
// the agent load it for this launch.
//
// actorToken is the launch's agent credential (AMUX_ACTOR_TOKEN). Agents do not
// pass their own environment down to MCP subprocesses — some agents hand the
// MCP server a curated whitelist, and the config-file formats only carry what
// we write — so the credential must be written into the MCP server config
// itself. Without it the MCP server reaches the hub anonymously and, with
// owner-scoped schedules and tasks, sees nothing. The token is already visible
// to the agent's own process env (and to codex broker args in plaintext), so
// writing it here does not widen its exposure; it is omitted entirely when the
// launch has no credential.
//
// actorTokenFile, when non-empty, is the path the runtime persists the session
// credential to; it is written as AMUX_ACTOR_TOKEN_FILE alongside the token
// value. The MCP server re-reads that file on every control-plane call, so a
// credential the hub re-mints mid-session (restart with a failed reconfigure
// never relaunches the process) still reaches it; the inline AMUX_ACTOR_TOKEN
// value stays for MCP servers older than that. Callers without such a file
// (e.g. task launches, whose credential is never persisted) pass "".
// memoryBinding optionally records the frozen, non-secret memory scope
// precondition. An explicit empty value masks a Runtime's inherited binding;
// omission retains compatibility for callers not managing memory.
func MaterializeAmuxMCP(agentType string, sb *task.Sandbox, layout LayoutFunc, amuxHome, actorToken, actorTokenFile string, memoryBinding ...string) ([]string, error) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" || sb == nil {
		return nil, nil
	}
	if err := validateDistributionProfile(Profile{Type: agentType}); err != nil {
		return nil, err
	}
	amuxHome = strings.TrimSpace(amuxHome)
	sessionID := strings.TrimSpace(sb.TaskID)
	if layout == nil {
		layout = adapter.Layout
	}
	placements, err := layout(agentType)
	if err != nil {
		return nil, err
	}
	p := placements[adapter.SurfaceMCP]
	if !p.Supported {
		return nil, nil
	}
	if p.Kind == "" || p.Path == "" {
		return nil, fmt.Errorf("agent %s mcp placement is incomplete", agentType)
	}

	switch profileConfigProtocol(agentType) {
	case "claude":
		return materializeClaudeAmuxMCP(sb, p, amuxHome, sessionID, actorToken, actorTokenFile, memoryBinding...)
	case "codex":
		return materializeCodexAmuxMCP(sb, p, amuxHome, sessionID, actorToken, actorTokenFile, memoryBinding...)
	default:
		return materializeDistributionMCP(agentType, sb, p, amuxHome, sessionID, actorToken, actorTokenFile)
	}
}

func materializeClaudeAmuxMCP(sb *task.Sandbox, p adapter.Placement, amuxHome, sessionID, actorToken, actorTokenFile string, memoryBinding ...string) ([]string, error) {
	root, err := rootFor(p.Kind, sb)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	path := filepath.Join(root, p.Path)
	cfg := standardMCPConfig{
		MCPServers: map[string]amuxMCPConfig{
			amuxMCPServerName: amuxServerConfig(amuxHome, sessionID, actorToken, actorTokenFile, memoryBinding...),
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := writeProfileFile(path, string(data)+"\n"); err != nil {
		return nil, err
	}
	return []string{"--mcp-config", path}, nil
}

func materializeCodexAmuxMCP(sb *task.Sandbox, p adapter.Placement, amuxHome, sessionID, actorToken, actorTokenFile string, memoryBinding ...string) ([]string, error) {
	if p.Kind != "extra-arg" || p.Path == "" {
		return nil, fmt.Errorf("agent codex mcp placement must be extra-arg")
	}
	args := []string{
		p.Path, fmt.Sprintf("mcp_servers.amux.command=%q", amuxCommand(amuxHome)),
		p.Path, "mcp_servers.amux.args=[\"mcp\"]",
		// App servers otherwise default each MCP call to five minutes. Keep
		// their host timeout out of the way of the default 35-minute
		// Delegation while retaining the provider turn hard cap as the bound.
		p.Path, fmt.Sprintf("mcp_servers.amux.tool_timeout_sec=%d", amuxMCPToolTimeoutSeconds),
	}
	if env := inlineMCPEnv(amuxHome, sessionID, actorToken, actorTokenFile, memoryBinding...); env != "" {
		args = append(args, p.Path, "mcp_servers.amux.env={"+env+"}")
	}
	return args, nil
}

func amuxServerConfig(amuxHome, sessionID, actorToken, actorTokenFile string, memoryBinding ...string) amuxMCPConfig {
	cfg := amuxMCPConfig{Command: amuxCommand(amuxHome), Args: []string{"mcp"}}
	cfg.Env = mcpEnv(amuxHome, sessionID, actorToken, actorTokenFile, memoryBinding...)
	if len(cfg.Env) == 0 {
		cfg.Env = nil
	}
	return cfg
}

func mcpEnv(amuxHome, sessionID, actorToken, actorTokenFile string, memoryBinding ...string) map[string]string {
	env := make(map[string]string, 4)
	if amuxHome != "" {
		env["AMUX_HOME"] = amuxHome
	}
	if sessionID != "" {
		env["AMUX_SESSION_ID"] = sessionID
	}
	if actorToken != "" {
		env["AMUX_ACTOR_TOKEN"] = actorToken
	}
	if actorTokenFile != "" {
		env["AMUX_ACTOR_TOKEN_FILE"] = actorTokenFile
	}
	if len(memoryBinding) > 0 {
		env[memoryprotocol.BindingEnv] = memoryBinding[0]
	}
	return env
}

func inlineMCPEnv(amuxHome, sessionID, actorToken, actorTokenFile string, memoryBinding ...string) string {
	var fields []string
	if amuxHome != "" {
		fields = append(fields, fmt.Sprintf("AMUX_HOME=%q", amuxHome))
	}
	if sessionID != "" {
		fields = append(fields, fmt.Sprintf("AMUX_SESSION_ID=%q", sessionID))
	}
	if actorToken != "" {
		fields = append(fields, fmt.Sprintf("AMUX_ACTOR_TOKEN=%q", actorToken))
	}
	if actorTokenFile != "" {
		fields = append(fields, fmt.Sprintf("AMUX_ACTOR_TOKEN_FILE=%q", actorTokenFile))
	}
	if len(memoryBinding) > 0 {
		fields = append(fields, fmt.Sprintf("%s=%q", memoryprotocol.BindingEnv, memoryBinding[0]))
	}
	return strings.Join(fields, ", ")
}

func upsertStandardMCPConfig(path, name string, server amuxMCPConfig) error {
	cfg := standardMCPConfig{MCPServers: map[string]amuxMCPConfig{}}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parse existing mcp config %s: %w", path, err)
		}
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]amuxMCPConfig{}
	}
	cfg.MCPServers[name] = server
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return writeProfileFile(path, string(data)+"\n")
}

func amuxCommand(amuxHome string) string {
	// A running amuxlet can outlive an atomic three-binary upgrade. On Linux,
	// os.Executable then resolves to the renamed backup inode; that directory
	// may contain only amuxlet, so falling back to the daemon itself would
	// project the invalid command `amuxlet mcp`. The installation path remains
	// stable across the rename and is therefore authoritative when available.
	if home := strings.TrimSpace(amuxHome); home != "" {
		installed := filepath.Join(home, "bin", "amux")
		if st, err := os.Stat(installed); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			return installed
		}
	}
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		if filepath.Base(exe) == "amux" {
			return exe
		}
		sibling := filepath.Join(filepath.Dir(exe), "amux")
		if st, statErr := os.Stat(sibling); statErr == nil && !st.IsDir() {
			return sibling
		}
		return exe
	}
	return "amux"
}
