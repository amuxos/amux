package agentprofile

import (
	"fmt"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/task"
)

func materializeDistributionMCP(agentType string, sandbox *task.Sandbox, placement adapter.Placement, home, session, token, tokenFile string) ([]string, error) {
	if customization.MaterializeMCP != nil {
		return customization.MaterializeMCP(agentType, sandbox, placement, home, session, token, tokenFile)
	}
	return nil, fmt.Errorf("agent %s declares unsupported mcp projection", agentType)
}
