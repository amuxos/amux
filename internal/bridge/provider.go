package bridge

import (
	"context"
	"strings"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/modelservice"
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

func (b *Bridge) handleDistributionTextCommand(ctx context.Context, openID, text string, inGroup bool) (string, bool, error) {
	hook := bridgeExtensions.snapshot().HandleTextCommand
	if hook == nil {
		return "", false, nil
	}
	principalID := ""
	if b != nil && b.resolveFeishuPrincipal != nil && strings.TrimSpace(openID) != "" {
		if id, err := b.resolveFeishuPrincipal(openID); err == nil {
			principalID = strings.TrimSpace(id)
		}
	}
	// Pass true owner identity alongside InGroup so command handlers can allow
	// owner self-service (e.g. local auth reuse) while still suppressing
	// owner-only disclosures inside group chats.
	isOwner := b != nil && b.canUseBridge(openID)
	locale := ""
	if b != nil {
		locale = string(b.managementLocale(openID))
	}
	return hook(ctx, TextCommandRequest{
		OpenID:      strings.TrimSpace(openID),
		PrincipalID: principalID,
		Text:        strings.TrimSpace(text),
		IsOwner:     isOwner,
		InGroup:     inGroup,
		Locale:      locale,
	})
}

func (b *Bridge) sessionQuotaBorrowed(targetID, sessionName string) bool {
	if b == nil || b.hub == nil || strings.TrimSpace(sessionName) == "" {
		return false
	}
	for _, target := range b.hub.ListTargets() {
		if targetID != "" && target.GetId() != targetID {
			continue
		}
		for _, sess := range target.GetSessions() {
			if sess.GetName() == sessionName {
				if id := strings.TrimSpace(sess.GetId()); id != "" && modelservice.IsSessionQuotaBorrowed(id) {
					return true
				}
				return modelservice.IsSessionQuotaBorrowed(sessionName)
			}
		}
	}
	return modelservice.IsSessionQuotaBorrowed(sessionName)
}
