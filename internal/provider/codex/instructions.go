package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// sessionInstructionsEnv mirrors runtime.SessionInstructionsEnv: amux
// session-level instructions (e.g. the Feishu foreground protocol) that must
// reach the model out of band, as thread developer instructions.
const sessionInstructionsEnv = "AMUX_SESSION_INSTRUCTIONS"

// sessionInstructionThreadParams merges amux session instructions into the
// thread/start or thread/resume developerInstructions, preserving the
// configured developer_instructions (or the value memory already resolved).
func (p *Proc) sessionInstructionThreadParams(ctx context.Context, opts StartOptions, params map[string]any) {
	extra := strings.TrimSpace(environmentValue(opts.Env, sessionInstructionsEnv))
	if extra == "" {
		return
	}
	existing, _ := params["developerInstructions"].(string)
	if _, set := params["developerInstructions"]; !set && p.client != nil {
		resp, err := p.client.Call(ctx, "config/read", map[string]any{"cwd": opts.Cwd, "includeLayers": false})
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: config/read for session instructions: %v\n", p.provider(), err)
		} else {
			var result struct {
				Config struct {
					DeveloperInstructions string `json:"developer_instructions"`
				} `json:"config"`
			}
			if json.Unmarshal(resp.Result, &result) == nil {
				existing = result.Config.DeveloperInstructions
			}
		}
	}
	params["developerInstructions"] = mergeDeveloperInstructions(existing, extra)
}

func mergeDeveloperInstructions(existing, extra string) string {
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return extra
	}
	if strings.Contains(existing, extra) {
		return existing
	}
	return existing + "\n\n" + extra
}
