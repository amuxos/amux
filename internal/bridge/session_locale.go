package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/rs/zerolog/log"
)

// Every CardKit message keeps the destination that its language was selected
// for. In particular, retries must not follow mutable singleton reply fields.
type replyCardDelivery struct {
	locale i18n.Locale
	send   func(context.Context, string, string) (string, error)
}

func (b *Bridge) prepareSessionCardDelivery(chat *LarkChat) (replyCardDelivery, error) {
	if target, ok, err := b.directReplyTargetForBinding(chat); ok || err != nil {
		if err != nil {
			return replyCardDelivery{}, err
		}
		if target == nil {
			return replyCardDelivery{}, fmt.Errorf("missing direct reply target")
		}
		delivery := replyCardDelivery{send: func(ctx context.Context, content, id string) (string, error) {
			messageID, _, err := b.sendCardKitReplyToDirectTarget(ctx, target, content, id)
			return messageID, err
		}}
		if target.Kind == botSessionReplyDM {
			delivery.locale = b.managementLocale(target.OpenID)
		}
		return delivery, nil
	}
	if chat == nil {
		return replyCardDelivery{}, fmt.Errorf("nil lark chat")
	}
	root := chat.RootMsgID
	return replyCardDelivery{
		locale: b.managementLocale(b.privateThreadRecipient(chat)),
		send: func(ctx context.Context, content, id string) (string, error) {
			messageID, _, err := b.sendCardKitReplyToThread(ctx, root, content, id)
			return messageID, err
		},
	}, nil
}

// A thread author's OpenId alone does not prove a DM. Use the persisted private
// workspace kind established from an authenticated p2p message event.
func (b *Bridge) privateThreadRecipient(chat *LarkChat) string {
	if b == nil || b.db == nil || chat == nil {
		return ""
	}
	bridgeID := strings.TrimSpace(chat.BridgeId)
	if bridgeID == "" {
		bridgeID = b.currentBridgeId()
	}
	if bridgeID != b.currentBridgeId() {
		return ""
	}
	bound, err := b.db.FindByRootMsg(bridgeID, chat.RootMsgID)
	if err != nil || bound == nil || bound.ChatID == "" {
		return ""
	}
	workspace, err := b.db.GetWorkspace(bridgeID, bound.ChatID)
	if err != nil || workspace == nil || workspace.Kind != WorkspaceKindPrivate {
		return ""
	}
	return bound.OpenId
}

func (b *Bridge) rememberPrivateSessionCard(openID, messageID string) {
	if b.db == nil || openID == "" || messageID == "" {
		return
	}
	_, err := b.db.db.Exec(`INSERT INTO feishu_private_session_cards(bridge_id,message_id,open_id) VALUES(?,?,?) ON CONFLICT(bridge_id,message_id) DO NOTHING`, b.currentBridgeId(), messageID, openID)
	if err != nil {
		log.Warn().Err(err).Msg("bridge: could not record private session card")
	}
}

func (b *Bridge) sessionCardForPatch(messageID, content string) string {
	if b.db == nil || messageID == "" {
		return content
	}
	var openID string
	if b.db.db.QueryRow(`SELECT open_id FROM feishu_private_session_cards WHERE bridge_id=? AND message_id=?`, b.currentBridgeId(), messageID).Scan(&openID) != nil {
		return content
	}
	return b.managementCardForRecipient(openID, content)
}
