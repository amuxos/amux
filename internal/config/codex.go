package config

import (
	"github.com/spf13/viper"
	"strings"
)

const CodexExecutableKey = "adapter.codex.executable"

// GetCodexExecutable returns the installation's program, defaulting to codex. A launcher owns its native configuration and auth.
func GetCodexExecutable() string {
	value := strings.TrimSpace(viper.GetString(CodexExecutableKey))
	if value == "" {
		return "codex"
	}
	return value
}

func ValidCodexExecutable(value string) bool {
	return !strings.ContainsAny(value, "\x00\r\n") && len(value) <= 4096
}
