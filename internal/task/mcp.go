package task

import "github.com/amuxos/amux/internal/adapter"

func taskUsesCodexMCP(agentType string) bool {
	return adapter.IsCodexCompatible(agentType)
}
