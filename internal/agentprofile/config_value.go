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
