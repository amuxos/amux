package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/provider"
)

// Resolve on the execution node, never on the Hub that rendered the picker.
func resolveCodexExecutable(value string, env []string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "codex"
	}
	if !config.ValidCodexExecutable(value) {
		return "", fmt.Errorf("invalid %s", config.CodexExecutableKey)
	}
	if strings.HasPrefix(value, "~/") {
		home := ""
		for _, entry := range env {
			if strings.HasPrefix(entry, "HOME=") {
				home = strings.TrimPrefix(entry, "HOME=")
			}
		}
		if home == "" {
			return "", fmt.Errorf("%s: execution HOME is required", config.CodexExecutableKey)
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	if strings.ContainsAny(value, `/\`) && !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s: relative executable path", config.CodexExecutableKey)
	}
	// Default command remains compatible with installations that resolve inside
	// their isolation backend. Explicit launchers are resolved using launch PATH.
	if value == "codex" {
		return value, nil
	}
	return provider.ResolveExecutableFromEnvPATH(value, append(os.Environ(), env...))
}

func launchSpecExecutable(spec string) string {
	var saved struct {
		AgentExecutable string `json:"agent_executable"`
	}
	_ = decodeLaunchSpec(spec, &saved)
	if saved.AgentExecutable == "" {
		return "codex"
	}
	return saved.AgentExecutable
}
