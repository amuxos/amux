package agentprofile

import (
	"strings"

	"github.com/amuxos/amux/internal/adapter"
)

func profileConfigProtocol(agentType string) string {
	if adapter.IsCodexCompatible(agentType) {
		return "codex"
	}
	return strings.TrimSpace(agentType)
}

// AgentArgsBackendVariant returns the last backend variant selected in the
// launch arguments of a Codex-compatible agent.
func AgentArgsBackendVariant(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 == len(args) {
				continue
			}
			arg = strings.TrimSpace(args[i+1])
		case strings.HasPrefix(arg, "-c="):
			arg = strings.TrimPrefix(arg, "-c=")
		case strings.HasPrefix(arg, "--config="):
			arg = strings.TrimPrefix(arg, "--config=")
		default:
			continue
		}
		if strings.HasPrefix(arg, "model_backend_variant=") {
			variant, _ := codexConfigValue(arg, "model_backend_variant")
			return variant
		}
	}
	return ""
}

func codexConfigValue(arg, key string) (string, bool) {
	prefix := key + "="
	val, ok := strings.CutPrefix(arg, prefix)
	if !ok {
		return "", false
	}
	val = strings.TrimSpace(val)
	val = strings.Trim(val, `"`)
	return strings.TrimSpace(val), val != ""
}
