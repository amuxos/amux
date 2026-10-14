package codex

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/amuxos/amux/internal/sessionprotocol"
)

// Configure the native conversation, never an individual user turn. The shared
// app-server driver applies this to compatible extension providers as well.
func (p *Proc) runtimeThreadParams(ctx context.Context, opts StartOptions, params map[string]any) error {
	instruction := sessionprotocol.FromEnv(opts.Env)
	if instruction == "" {
		return nil
	}
	existing, prepared := params["developerInstructions"].(string)
	if !prepared {
		// Read resolved configuration before overriding developerInstructions,
		// preserving project, user and launch configuration layers.
		resp, err := p.client.Call(ctx, "config/read", map[string]any{"cwd": opts.Cwd, "includeLayers": false})
		if err != nil {
			return fmt.Errorf("%s config/read: %w", p.provider(), err)
		}
		var result struct {
			Config struct {
				DeveloperInstructions string `json:"developer_instructions"`
			} `json:"config"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return err
		}
		existing = result.Config.DeveloperInstructions
	}
	params["developerInstructions"] = sessionprotocol.MergeInstruction(existing, instruction)
	return nil
}
