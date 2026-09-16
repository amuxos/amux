package bridge

import (
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const botE2EEnvelope = "/e2e"

// botE2ERoleOwner and botE2ERoleShared are the identity roles a sender_roles
// mapping may project a test bot's /e2e request onto.
const (
	botE2ERoleOwner  = "owner"
	botE2ERoleShared = "shared"
)

func (b *Bridge) isBotE2ECandidate(isGroup bool, senderType, senderOpenID string) bool {
	if b == nil || !isGroup || normalizeGroupWatchSenderType(senderType) != "app" {
		return false
	}
	senderOpenID = strings.TrimSpace(senderOpenID)
	if senderOpenID == "" || b.isOwnGroupWatchSender(senderType, senderOpenID) {
		return false
	}
	for _, allowed := range b.botE2EConfig.SenderOpenIDs {
		if senderOpenID == strings.TrimSpace(allowed) {
			return true
		}
	}
	return false
}

// botE2ERoleForSender resolves the identity role a trusted test bot's /e2e
// request should be projected as. It returns "" when the sender is not in the
// allowlist or has no explicit role (callers then fall back to owner). The
// map retains unknown roles (without an entry in ValidBotE2ESenderRoles) so the
// bridge can reject them loudly at routing time instead of silently treating
// them as owner; that rejection happens in the caller via sceneIdentityError.
func (b *Bridge) botE2ERoleForSender(senderOpenID string) string {
	if b == nil || len(b.botE2EConfig.SenderRoles) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(b.botE2EConfig.SenderRoles[senderOpenID]))
}

func (b *Bridge) botE2ERequest(msg *larkim.EventMessage, text, senderOpenID string, candidate bool) (string, bool) {
	if !candidate || msg == nil || len(msg.Mentions) == 0 {
		return "", false
	}
	mention := b.messageBotMentionInfo(msg)
	if !mention.Mentioned {
		return "", false
	}
	envelope := strings.TrimSpace(b.stripCurrentBotMention(text, mention))
	fields := strings.Fields(envelope)
	if len(fields) < 2 || fields[0] != botE2EEnvelope {
		return "", false
	}
	request := strings.TrimSpace(strings.TrimPrefix(envelope, botE2EEnvelope))
	if request == "" || strings.HasPrefix(request, "/") {
		return "", false
	}
	return request, true
}
