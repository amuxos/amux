package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/provider"
	"github.com/amuxos/amux/internal/session"
)

// prepareAmuxTools keeps the CLI and MCP client reachable without granting an
// isolated principal access to the service account's installation/config/home.
// The existing target-UID materializer owns all writes inside the principal.
func (s *server) prepareAmuxTools(ctx context.Context, snap session.Snapshot, args, env []string, cli bool) ([]string, []string, error) {
	command, err := memoryExecutable(s.amuxDir, env)
	if err != nil {
		return nil, nil, err
	}
	hub := config.GetHub()
	toolHome := s.amuxDir
	configPath := filepath.Join(profileSessionDir(s.amuxDir, snap.ID), "agent-config", "memory", "claude-mcp.json")
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		if s.isolationRuntime == nil {
			return nil, nil, fmt.Errorf("memory tools require the selected linux-dac materializer")
		}
		paths, _, err := s.isolationRuntime.PrepareExecution(ctx, snap.ExecutionIdentity, snap.LaunchID, snap.Cwd)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare isolated memory tools: %w", err)
		}
		// Run-owned binaries are never overwritten under another live process,
		// and are reclaimed by the ordinary execution cleanup lifecycle.
		toolHome = filepath.Join(paths.Run, "agent-config", "memory")
		projectedCommand := filepath.Join(toolHome, "bin", "amux")
		stage, err := stageMemoryTools(command, hub.Addr)
		if err != nil {
			return nil, nil, err
		}
		defer os.RemoveAll(stage)
		env = memoryToolEnvironment(env, toolHome, hub)
		env = memoryprotocol.SetEnv(env, "AMUX_ACTOR_TOKEN_FILE", "")
		if !cli && snap.AgentType == "claude" {
			if err := writeMemoryMCPConfig(filepath.Join(stage, "memory", "claude-mcp.json"), projectedCommand, snap.ID, env); err != nil {
				return nil, nil, err
			}
		}
		sources := []isolation.BundleSource{{Source: stage, Prefix: "agent-config"}}
		if err := s.isolationRuntime.MaterializeExecution(ctx, snap.ExecutionIdentity, snap.LaunchID, snap.Cwd, sources); err != nil {
			return nil, nil, fmt.Errorf("materialize isolated memory tools: %w", err)
		}
		command = projectedCommand
		configPath = filepath.Join(toolHome, "claude-mcp.json")
	} else {
		env = memoryToolEnvironment(env, toolHome, hub)
		if !cli && snap.AgentType == "claude" {
			if err := writeMemoryMCPConfig(configPath, command, snap.ID, env); err != nil {
				return nil, nil, err
			}
		}
	}
	// The protocol can always use `amux`, including private-home task children
	// whose inherited PATH was intentionally restricted or omitted.
	path := filepath.Dir(command)
	if existing := launchEnvValue(env, "PATH"); existing != "" {
		path += string(os.PathListSeparator) + existing
	}
	env = memoryprotocol.SetEnv(env, "PATH", path)
	if !cli {
		args = stripAmuxMCPArgs(snap.AgentType, args)
		if snap.AgentType == "claude" {
			args = append(args, "--mcp-config", configPath)
		} else if adapter.IsCodexCompatible(snap.AgentType) {
			args = append(args, "-c", "mcp_servers.amux.command="+strconv.Quote(command), "-c", `mcp_servers.amux.args=["mcp"]`, "-c", "mcp_servers.amux.tool_timeout_sec=7200")
			// Explicit values survive Codex's curated MCP subprocess env.
			for _, key := range memoryToolEnvKeys {
				value := launchEnvValue(env, key)
				if key == "AMUX_SESSION_ID" {
					value = snap.ID
				}
				args = append(args, "-c", "mcp_servers.amux.env."+key+"="+strconv.Quote(value))
			}
		}
	}
	return args, env, nil
}

var memoryToolEnvKeys = []string{"AMUX_HOME", "AMUX_HUB_ADDR", "AMUX_HUB_TOKEN", "AMUX_SESSION_ID", "AMUX_ACTOR_TOKEN", "AMUX_ACTOR_TOKEN_FILE", memoryprotocol.BindingEnv}

func memoryToolEnvironment(env []string, home string, hub config.HubConfig) []string {
	env = memoryprotocol.SetEnv(env, "AMUX_HOME", home)
	if hub.Addr != "" {
		env = memoryprotocol.SetEnv(env, "AMUX_HUB_ADDR", hub.Addr)
	}
	if hub.Token != "" {
		env = memoryprotocol.SetEnv(env, "AMUX_HUB_TOKEN", hub.Token)
	}
	return env
}

func memoryExecutable(amuxDir string, env []string) (string, error) {
	var candidates []string
	if amuxDir != "" {
		candidates = append(candidates, filepath.Join(amuxDir, "bin", "amux"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "amux"))
	}
	if executable, err := provider.ResolveExecutableFromEnvPATH("amux", env); err == nil {
		candidates = append(candidates, executable)
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("amux memory tools unavailable: install the amux CLI alongside this Runtime or include it in the launch PATH")
}

func stageMemoryTools(command, hubAddr string) (string, error) {
	stage, err := os.MkdirTemp("", "amux-memory-tools-")
	if err != nil {
		return "", err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(stage)
		}
	}()
	dir := filepath.Join(stage, "memory", "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	source, err := os.Open(command)
	if err != nil {
		return "", err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Size() > 256<<20 {
		return "", fmt.Errorf("memory CLI must be a regular executable of at most 256 MiB")
	}
	destination, err := os.OpenFile(filepath.Join(dir, "amux"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	// The CLI config contains only public connection metadata. Actor/transport
	// credentials are passed via launch env (or the ephemeral MCP config);
	// service configuration and native homes are not copied.
	configuration := ""
	if strings.TrimSpace(hubAddr) != "" {
		configuration += "[hub]\naddr = " + strconv.Quote(hubAddr) + "\n"
	}
	if err := os.WriteFile(filepath.Join(stage, "memory", "config.toml"), []byte(configuration), 0o600); err != nil {
		return "", err
	}
	failed = false
	return stage, nil
}

func writeMemoryMCPConfig(path, command, sessionID string, env []string) error {
	values := make(map[string]string, len(memoryToolEnvKeys))
	for _, key := range memoryToolEnvKeys {
		values[key] = launchEnvValue(env, key)
	}
	values["AMUX_SESSION_ID"] = sessionID
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"amux": map[string]any{"command": command, "args": []string{"mcp"}, "env": values}}})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
