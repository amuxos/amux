package agentprofile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type LayoutFunc func(agentType string) (map[adapter.Surface]adapter.Placement, error)

// MaterializeIntoSandbox projects a resolved Agent Profile spec into the task
// sandbox workspace and returns extra launch args. Agent-facing content follows
// the AGENTS.md projection model (claude additionally gets a CLAUDE.md import);
// skill directories only project when the profile pack is materialized on this
// host. Task child sessions intentionally do not receive the built-in amux MCP
// server by default; only their parent/main agent should schedule subtasks.
func MaterializeIntoSandbox(spec *pb.AgentProfileSpec, sb *task.Sandbox, layout LayoutFunc, amuxHome string) ([]string, error) {
	if spec == nil {
		return nil, nil
	}
	if sb == nil || sb.Workspace == "" {
		return nil, fmt.Errorf("sandbox workspace is required")
	}
	profile, err := FromPBProfileSpec(spec)
	if err != nil {
		return nil, err
	}
	registryPath := ""
	if strings.TrimSpace(amuxHome) != "" {
		registryPath = filepath.Join(amuxHome, "agent-packs", "registry.json")
	}
	profile, err = AttachLocalPackPath(profile, registryPath)
	if err != nil {
		return nil, err
	}
	if _, err := RenderProfileWorkspace(profile, sb.Workspace); err != nil {
		return nil, err
	}
	return nil, nil
}

func rootFor(kind string, sb *task.Sandbox) (string, error) {
	switch kind {
	case "cwd-rel":
		return sb.Workspace, nil
	case "home-rel":
		if sb.Home == "" {
			return "", fmt.Errorf("home-rel placement requires private-home isolation")
		}
		return sb.Home, nil
	case "agent-config-rel":
		return sb.AgentConfig, nil
	default:
		return "", fmt.Errorf("unsupported placement kind %q", kind)
	}
}

func writeProfileFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0600)
}
