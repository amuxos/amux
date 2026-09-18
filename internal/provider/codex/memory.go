package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/memoryprotocol"
)

func managedMemory(opts StartOptions) bool {
	return memoryprotocol.Binding(opts.Env) != ""
}

// Apply before app-server starts, so neither its worker nor thread creation
// observes the user's ordinary native memory settings. This is launch-local.
func memoryLaunchArgs(opts StartOptions) []string {
	args := append([]string(nil), opts.ExtraArgs...)
	if managedMemory(opts) {
		args = append(args, "-c", "features.memories=false", "-c", "memories.use_memories=false", "-c", "memories.generate_memories=false")
	}
	return args
}

func (p *Proc) memoryThreadParams(ctx context.Context, opts StartOptions, params map[string]any) error {
	if !managedMemory(opts) {
		return nil
	}
	if p.provider() != "codex" {
		return fmt.Errorf("amux memory is unsupported by provider %s", p.provider())
	}
	// The resolved config includes user/project and launch overrides. Supplying
	// developerInstructions blindly would replace those existing instructions.
	resp, err := p.client.Call(ctx, "config/read", map[string]any{"cwd": opts.Cwd, "includeLayers": false})
	if err != nil {
		return fmt.Errorf("amux memory requires Codex config/read support: %w", err)
	}
	var result struct {
		Config struct {
			DeveloperInstructions string `json:"developer_instructions"`
			Features              struct {
				Memories *bool `json:"memories"`
			} `json:"features"`
			Memories struct {
				Use      *bool `json:"use_memories"`
				Generate *bool `json:"generate_memories"`
			} `json:"memories"`
		} `json:"config"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("amux memory: decode Codex effective config: %w", err)
	}
	settings := result.Config
	if settings.Features.Memories == nil || *settings.Features.Memories || settings.Memories.Use == nil || *settings.Memories.Use || settings.Memories.Generate == nil || *settings.Memories.Generate {
		return fmt.Errorf("amux memory requires Codex to confirm native memory reading and generation are disabled; update Codex or disable amux memory")
	}
	protocol := memoryprotocol.Instruction(environmentValue(opts.Env, "AMUX_MEMORY_CLI") == "1")
	existing := strings.TrimSpace(settings.DeveloperInstructions)
	if strings.Contains(existing, protocol) {
		protocol = existing
	} else if existing != "" {
		protocol = existing + "\n\n" + protocol
	}
	params["developerInstructions"] = protocol
	params["config"] = map[string]any{
		"features.memories":          false,
		"memories.use_memories":      false,
		"memories.generate_memories": false,
	}
	return nil
}

// The persisted mode prevents a later ordinary Codex worker from selecting
// this managed conversation as extraction input. A mere prompt rule cannot.
func (p *Proc) disableThreadMemory(ctx context.Context, opts StartOptions, threadID string) error {
	if !managedMemory(opts) {
		return nil
	}
	_, err := p.client.Call(ctx, "thread/memoryMode/set", map[string]any{"threadId": threadID, "mode": "disabled"})
	if err != nil {
		return fmt.Errorf("amux memory requires persistent Codex thread memory-mode support: %w", err)
	}
	return nil
}
