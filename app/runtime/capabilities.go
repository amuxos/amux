package runtime

import (
	"context"
	"runtime"
	"strconv"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
)

// Default capability ceilings when not configured.
const (
	defaultMaxSessions    = 100
	defaultMaxConcurrency = 10
)

// buildCapabilities derives this runtime's static registry capabilities from the
// adapter registry (agent types + protocols) and runtime config (limits/labels).
// Isolation and workspace policy support reflect the Phase 1/2 implementation:
// process/private-home (trusted-mode weak isolation) and temp-dir/git-worktree.
func buildCapabilities(rt config.RuntimeConfig, isolationRuntimes ...*isolation.Runtime) *registry.Capabilities {
	maxConc := rt.MaxConcurrency
	if maxConc <= 0 {
		maxConc = defaultMaxConcurrency
	}
	maxSess := rt.MaxSessions
	if maxSess <= 0 {
		maxSess = defaultMaxSessions
	}

	agents := make([]registry.AgentCapacity, 0, len(adapter.Names()))
	agentTypes := make([]string, 0, len(adapter.Names()))
	for _, name := range adapter.Names() {
		protocol := ""
		if a, err := adapter.Get(name); err == nil {
			protocol = string(a.Capabilities().Protocol)
		}
		agentTypes = append(agentTypes, name)
		agents = append(agents, registry.AgentCapacity{
			Type:           name,
			Protocol:       protocol,
			MaxConcurrency: maxConc,
		})
	}

	labels := map[string]string{
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
	}
	for k, v := range rt.Labels {
		labels[k] = v
	}

	capabilities := &registry.Capabilities{
		Labels:            labels,
		Agents:            agents,
		AgentTypes:        agentTypes,
		Features:          []string{"task", "admission", "event-log"},
		IsolationBackends: []string{"process", "private-home"},
		WorkspacePolicies: []string{"temp-dir", "git-worktree"},
		MaxSessions:       maxSess,
	}
	if len(isolationRuntimes) > 0 && isolationRuntimes[0] != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		status := isolationRuntimes[0].Status(ctx)
		cancel()
		capabilities.IsolationDegradedReason = status.DegradedReason
		if status.Enabled && status.Configured != principal.BackendPassthrough {
			capabilities.Features = append(capabilities.Features, registry.FeaturePrincipalIsolation)
		}
		if status.HelperReady {
			capabilities.IsolationBackends = append(capabilities.IsolationBackends, principal.BackendLinuxDAC)
			capabilities.IsolationHelperProtocol = strconv.Itoa(status.ProtocolVersion)
		}
	}
	return capabilities
}
