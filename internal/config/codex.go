package config

import (
	"sort"
	"strings"

	"github.com/spf13/viper"
)

const CodexExecutableKey = "adapter.codex.executable"

var agentExecutables = map[string]string{"codex": "Codex"}

func AgentExecutableKey(agent string) string    { return "adapter." + agent + ".executable" }
func SupportsAgentExecutable(agent string) bool { _, ok := agentExecutables[agent]; return ok }
func AgentExecutableNames() []string {
	names := make([]string, 0, len(agentExecutables))
	for name := range agentExecutables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func AgentExecutableLabel(agent string) string { return agentExecutables[agent] }
func GetAgentExecutable(agent string) string {
	value := strings.TrimSpace(viper.GetString(AgentExecutableKey(agent)))
	if value == "" {
		return agent
	}
	return value
}
func GetCodexExecutable() string             { return GetAgentExecutable("codex") }
func ValidCodexExecutable(value string) bool { return ValidAgentExecutable(value) }
func ValidAgentExecutable(value string) bool {
	return !strings.ContainsAny(value, "\x00\r\n") && len(value) <= 4096
}
func IsAgentExecutableKey(key string) bool {
	for _, agent := range AgentExecutableNames() {
		if key == AgentExecutableKey(agent) {
			return true
		}
	}
	return false
}
