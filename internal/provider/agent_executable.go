package provider

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/config"
)

// Resolve on the execution node, never on the Hub that rendered the picker.
func ResolveAgentExecutable(agent, value string, env []string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = agent
	}
	if !config.ValidAgentExecutable(value) {
		return "", config.ExecutableError("invalid", config.AgentExecutableKey(agent))
	}
	if strings.HasPrefix(value, "~/") {
		home := ""
		for _, entry := range env {
			if strings.HasPrefix(entry, "HOME=") {
				home = strings.TrimPrefix(entry, "HOME=")
			}
		}
		if home == "" {
			return "", config.ExecutableError("home", config.AgentExecutableKey(agent))
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	if strings.ContainsAny(value, `/\`) && !filepath.IsAbs(value) {
		return "", config.ExecutableError("relative", config.AgentExecutableKey(agent))
	}
	// Default command remains compatible with installations that resolve inside
	// their isolation backend. Explicit launchers are resolved using launch PATH.
	if value == agent {
		return value, nil
	}
	return ResolveExecutableFromEnvPATH(value, append(os.Environ(), env...))
}
