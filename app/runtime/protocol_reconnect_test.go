package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/session"
)

func TestReconnectProcUsesStructuredBrokerForPublicAgents(t *testing.T) {
	for _, agentType := range []string{"claude", "codex", "pi"} {
		sess := &session.Session{AgentType: agentType, BrokerSocket: filepath.Join(t.TempDir(), "missing.sock")}
		_, _, err := reconnectProc(sess)
		if err == nil || !strings.Contains(err.Error(), "dial structured broker") {
			t.Fatalf("reconnect %s did not attempt its structured broker: %v", agentType, err)
		}
	}
}
