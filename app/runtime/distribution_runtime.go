package runtime

import (
	"context"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

func distributionAutoModelConfig(agentType string) (adapter.AutoModelSelectionConfig, bool) {
	if configure := distributionHooks.AutoModelConfig; configure != nil {
		return configure(agentType)
	}
	return adapter.AutoModelSelectionConfig{}, false
}

func startDistributionStateWatchers(ctx context.Context) {
	if start := distributionHooks.StartStateWatchers; start != nil {
		start(ctx)
	}
}

func distributionCredentialLoginCommand(path string) string {
	if command := distributionHooks.CredentialLoginCommand; command != nil {
		return command(path)
	}
	return "the provider login command"
}

func appendDistributionUserBinPaths(home string, paths []string) []string {
	if appendPaths := distributionHooks.AppendUserBinPaths; appendPaths != nil {
		return appendPaths(home, paths)
	}
	return paths
}

func (s *server) cleanupDistributionSessionState(snap session.Snapshot) {
	if cleanup := distributionHooks.SessionArchived; cleanup != nil {
		cleanup(ArchivedSession{
			AgentType: snap.AgentType, Env: snap.Env,
			HasRunningAgent: s.hasRunningAgent(snap.AgentType),
			Isolated:        snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC,
			ReclaimHome: func(ctx context.Context) error {
				return s.isolationRuntime.ReclaimHome(ctx, snap.ExecutionIdentity, isolatedHomeCachesFor(snap.AgentType))
			},
		})
	}
}
