package bridge

import (
	"slices"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
)

// appendConfiguredClaudeModels merges operator-declared Claude model refs from
// bridge.feishu.claude_models into the Claude runtime so the Feishu model picker
// offers them. Launch already accepts these refs because the Claude runtime
// permits unlisted native models; this only makes them selectable in the picker.
func appendConfiguredClaudeModels(runtimes []agentprofile.RuntimeOption) []agentprofile.RuntimeOption {
	return mergeClaudeRuntimeModels(runtimes, config.GetBridgeFeishuClaudeModels())
}

// mergeClaudeRuntimeModels is the pure merge used by appendConfiguredClaudeModels.
// Config refs already present in the catalog are skipped, so re-applying the
// merge is idempotent. Each added model inherits the Claude runtime's reasoning
// efforts so the effort picker keeps working for it.
func mergeClaudeRuntimeModels(runtimes []agentprofile.RuntimeOption, extra []string) []agentprofile.RuntimeOption {
	if len(extra) == 0 {
		return runtimes
	}
	out := slices.Clone(runtimes)
	for i := range out {
		if out[i].Ref != "claude" {
			continue
		}
		models := slices.Clone(out[i].Models)
		seen := make(map[string]bool, len(models))
		for _, model := range models {
			seen[model.Ref] = true
		}
		for _, ref := range extra {
			ref = strings.TrimSpace(ref)
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			models = append(models, agentprofile.RuntimeModelOption{
				Ref:                       ref,
				AgentModel:                ref,
				Kind:                      agentprofile.ModelKindNative,
				DisplayName:               ref,
				SupportedReasoningEfforts: slices.Clone(out[i].DefaultReasoningEfforts),
			})
		}
		out[i].Models = models
	}
	return out
}
