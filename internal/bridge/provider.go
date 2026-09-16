package bridge

import (
	"strings"

	"github.com/amuxos/amux/internal/agentui"
)

func distributionActivityPossiblyQueued(agentType string, activity *agentui.ActivityView) bool {
	if hook := bridgeExtensions.snapshot().ActivityPossiblyQueued; hook != nil {
		return hook(agentType, activity)
	}
	return false
}

func distributionProviderErrorNotice(text string) string {
	if hook := bridgeExtensions.snapshot().ProviderErrorNotice; hook != nil {
		return hook(text)
	}
	return ""
}

func distributionReplyCardAgent(agentType string) bool {
	if hook := bridgeExtensions.snapshot().ReplyCardAgent; hook != nil {
		return hook(agentType)
	}
	return false
}

func distributionAgentTypeName(agentType string) string {
	if hook := bridgeExtensions.snapshot().AgentTypeName; hook != nil {
		return hook(agentType)
	}
	return strings.TrimSpace(agentType)
}

func distributionCoordinationProfile(ref string) bool {
	if hook := bridgeExtensions.snapshot().CoordinationProfile; hook != nil {
		return hook(ref)
	}
	return false
}
