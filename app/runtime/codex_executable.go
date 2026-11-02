package runtime

import (
	"github.com/amuxos/amux/internal/provider"
)

func resolveCodexExecutable(value string, env []string) (string, error) {
	return provider.ResolveAgentExecutable("codex", value, env)
}
func resolveAgentExecutable(agent, value string, env []string) (string, error) {
	return provider.ResolveAgentExecutable(agent, value, env)
}

func launchSpecExecutable(spec string, agents ...string) string {
	var saved struct {
		AgentType       string `json:"agent_type"`
		AgentExecutable string `json:"agent_executable"`
	}
	_ = decodeLaunchSpec(spec, &saved)
	if saved.AgentExecutable == "" {
		if len(agents) > 0 && agents[0] != "" {
			return agents[0]
		}
		if saved.AgentType != "" {
			return saved.AgentType
		}
		return "codex"
	}
	return saved.AgentExecutable
}
