package cli

import "context"

func runAgentAuthenticationPreflight(ctx context.Context, agentType, executable string) error {
	if probe := distributionHooks.AuthenticationPreflight; probe != nil {
		return probe(ctx, agentType, executable)
	}
	return nil
}
func distributionAuthenticationProbeLabel(agentType string) string {
	if label := distributionHooks.AuthenticationProbeLabel; label != nil {
		return label(agentType)
	}
	return ""
}
