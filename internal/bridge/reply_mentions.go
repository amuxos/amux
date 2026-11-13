package bridge

import (
	"html"
	"strings"
	"sync"
)

const legacyTurnInitiatorMentionElementID = "turn_mentions"

// replyMentionState tracks which Turn the standalone-post path is presenting
// and whether that Turn's initiators were already mentioned. Aggregated-post
// timers may read it outside the attach processor.
type replyMentionState struct {
	mu        sync.Mutex
	turnID    string
	delivered bool
}

// observe records the Turn that later standalone posts belong to. It is cheap
// enough to call for every visible action; initiators are resolved only when a
// post is actually sent.
func (s *replyMentionState) observe(turnID string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnID != s.turnID {
		s.turnID = turnID
		s.delivered = false
	}
}

// claim returns mentions only for the first standalone post sent for a Turn.
// Initiators are resolved at send time, so a Submit receipt recorded after the
// provider's turn_started is still observed; an empty result leaves the Turn
// unclaimed so a later post can resolve it again. Streamed card replies publish
// their mentions separately when the Turn completes.
func (s *replyMentionState) claim(resolve func(turnID string) []string) (string, []string) {
	if resolve == nil {
		return "", nil
	}
	s.mu.Lock()
	turnID, delivered := s.turnID, s.delivered
	s.mu.Unlock()
	if delivered || turnID == "" {
		return "", nil
	}
	openIDs := uniqueOpenIDs(resolve(turnID))
	if len(openIDs) == 0 {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnID != turnID || s.delivered {
		return "", nil
	}
	s.delivered = true
	return turnID, openIDs
}

func (s *replyMentionState) release(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnID != "" && s.turnID == turnID {
		s.delivered = false
	}
}

func uniqueOpenIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (c *turnCoalescer) turnSenderOpenIDs(rootMsgID, turnID string) []string {
	if c == nil || rootMsgID == "" || turnID == "" {
		return nil
	}
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	return append([]string(nil), c.turnSenders[rootMsgID][turnID]...)
}

// turnReplyMentionOpenIDs returns the distinct human initiators eligible for
// final-reply presentation. Input routing records an empty Open ID for
// automation and App/Bot-authored inputs, excluding them without adding
// sender-type policy to the rendering layer. Background completion Turns use
// the durable receipts of the exact parent Turn instead of guessing from the
// most recent speaker in the conversation.
func (b *Bridge) turnReplyMentionOpenIDs(chat *LarkChat, rootMsgID, turnID string) []string {
	if b == nil || !b.mentionTurnInitiator.Load() || b.db == nil || chat == nil ||
		strings.TrimSpace(rootMsgID) == "" || strings.TrimSpace(turnID) == "" {
		return nil
	}
	openIDs := b.turnSenderOpenIDs(rootMsgID, turnID)
	subtaskTurn := false
	if parentTurnID, subtask := b.subtaskParentTurn(rootMsgID, turnID); subtask {
		subtaskTurn = true
		if parentTurnID == "" {
			return nil
		}
		bridgeID := strings.TrimSpace(chat.BridgeId)
		if bridgeID == "" {
			bridgeID = b.currentBridgeId()
		}
		var err error
		openIDs, err = b.db.TurnNotificationRecipients(bridgeID, parentTurnID)
		if err != nil {
			return nil
		}
	}
	openIDs = uniqueOpenIDs(openIDs)
	target, direct, err := b.directReplyTargetForBinding(chat)
	if err != nil {
		return nil
	}
	if direct {
		// A newly-created DM message is itself a notification; keep the native
		// mention as well, but never mention somebody other than its recipient.
		if target == nil || strings.TrimSpace(target.Kind) != botSessionReplyDM {
			return nil
		}
		recipient := strings.TrimSpace(target.OpenID)
		for _, openID := range openIDs {
			if openID == recipient {
				return []string{recipient}
			}
		}
		if !subtaskTurn && len(openIDs) == 0 && strings.TrimSpace(chat.OpenId) == recipient && recipient != "" {
			return []string{recipient}
		}
		return nil
	}
	if strings.TrimSpace(chat.ChatID) == "" {
		return nil
	}
	kind, err := b.db.WorkspaceKind(b.currentBridgeId(), chat.ChatID)
	if err != nil || (kind != WorkspaceKindGroup && kind != WorkspaceKindTopicGroup && kind != WorkspaceKindPrivate) {
		return nil
	}
	return openIDs
}

func renderTurnInitiatorMentionsMarkdown(openIDs []string) string {
	openIDs = uniqueOpenIDs(openIDs)
	if len(openIDs) == 0 {
		return ""
	}
	mentions := make([]string, 0, len(openIDs))
	for _, openID := range openIDs {
		mentions = append(mentions, `<at id="`+html.EscapeString(openID)+`"></at>`)
	}
	return strings.Join(mentions, " ")
}
