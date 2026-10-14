package bridge

import (
	"context"
	"fmt"
	"strings"
)

// directReplyTarget describes a direct group/DM destination without making a
// group conversation depend on the Bot singleton's mutable last-reply fields.
type directReplyTarget struct {
	Kind, ChatID, OpenID string
	bridgeID, groupRoot  string
}

func botSessionReplyTarget(bs *BotSession) *directReplyTarget {
	if bs == nil {
		return nil
	}
	return &directReplyTarget{Kind: bs.LastReplyKind, ChatID: bs.LastReplyChatID, OpenID: bs.LastReplyOpenID}
}

func (b *Bridge) directReplyTargetForBinding(chat *LarkChat) (*directReplyTarget, bool, error) {
	if chat != nil && chat.goalReplyTarget != nil {
		return chat.goalReplyTarget, true, nil
	}
	if bs, ok, err := b.botSessionForBinding(chat); ok || err != nil {
		return botSessionReplyTarget(bs), ok, err
	}
	if b == nil || b.db == nil || chat == nil || chat.RootMsgID == "" {
		return nil, false, nil
	}
	bridgeID := strings.TrimSpace(chat.BridgeId)
	if bridgeID == "" {
		bridgeID = b.currentBridgeId()
	}
	bound, err := b.db.FindByRootMsg(bridgeID, chat.RootMsgID)
	if err != nil || bound == nil || bound.ChatID == "" {
		return nil, false, err
	}
	root, err := b.db.FindGroupSessionOrigin(bridgeID, bound.ChatID, chat.RootMsgID)
	if err != nil || root == "" || root != chat.RootMsgID {
		return nil, false, err
	}
	if (chat.ChatID != "" && chat.ChatID != bound.ChatID) ||
		(chat.TargetID != "" && chat.TargetID != bound.TargetID) ||
		(chat.SessionName != "" && chat.SessionName != bound.SessionName) {
		return nil, true, fmt.Errorf("stale group singleton output binding for %s", root)
	}
	// Retired conversations still deliver accepted work to their original group.
	// Their output aliases retain that retired identity for subsequent replies.
	return &directReplyTarget{Kind: botSessionReplyGroup, ChatID: bound.ChatID, bridgeID: bridgeID, groupRoot: root}, true, nil
}

func (b *Bridge) directReplyTargetForRoot(ctx context.Context, root string) (*directReplyTarget, bool, error) {
	if target, ok, err := b.directReplyTargetForBinding(&LarkChat{RootMsgID: root}); ok || err != nil {
		return target, ok, err
	}
	// Launch errors occur before the first binding exists, but should also appear
	// in the group where the continuous conversation was requested.
	if policy, ok := ctx.Value(groupLaunchPolicyKey{}).(groupLaunchPolicy); ok && policy.shared && policy.settings != nil {
		return &directReplyTarget{Kind: botSessionReplyGroup, ChatID: policy.settings.ChatID}, true, nil
	}
	return nil, false, nil
}

func (b *Bridge) recordDirectReply(target *directReplyTarget, messageID, threadID string, err error) (string, string, error) {
	if err == nil && target != nil && target.groupRoot != "" && messageID != "" {
		err = b.db.RecordGroupSessionOutput(target.bridgeID, target.ChatID, messageID, target.groupRoot)
	}
	return messageID, threadID, err
}
