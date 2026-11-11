package bridge

import (
	"html"
	"strings"
	"sync"
)

const turnInitiatorMentionElementID = "turn_initiator_mentions"

// replyMentionState keeps the presentation-only initiators for the current
// stream Turn. Aggregated-post timers may read it outside the attach processor.
type replyMentionState struct {
	mu        sync.RWMutex
	turnID    string
	openIDs   []string
	delivered bool
}

func (s *replyMentionState) set(turnID string, openIDs []string) bool {
	turnID = strings.TrimSpace(turnID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnID == s.turnID {
		return false
	}
	s.turnID = turnID
	s.openIDs = uniqueOpenIDs(openIDs)
	s.delivered = false
	return true
}

func (s *replyMentionState) current() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.openIDs...)
}

// claim returns mentions only for the first standalone reply sent for a Turn.
// CardKit has its own backend-local claim because it updates one card in place;
// this claim covers the post aggregator, which may split a long reply.
func (s *replyMentionState) claim() (string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivered || len(s.openIDs) == 0 {
		return "", nil
	}
	s.delivered = true
	return s.turnID, append([]string(nil), s.openIDs...)
}

func (s *replyMentionState) release(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnID != "" && s.turnID == turnID {
		s.delivered = false
	}
}

func uniqueOpenIDs(openIDs []string) []string {
	seen := make(map[string]struct{}, len(openIDs))
	out := make([]string, 0, len(openIDs))
	for _, openID := range openIDs {
		openID = strings.TrimSpace(openID)
		if openID == "" {
			continue
		}
		if _, ok := seen[openID]; ok {
			continue
		}
		seen[openID] = struct{}{}
		out = append(out, openID)
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
// presentation in an ordinary group topic. Input routing records an empty
// Open ID for group-watch automation and App/Bot-authored inputs, excluding
// them without adding sender-type policy to the rendering layer.
func (b *Bridge) turnReplyMentionOpenIDs(chat *LarkChat, rootMsgID, turnID string) []string {
	if b == nil || !b.mentionTurnInitiator.Load() || b.db == nil || chat == nil ||
		strings.TrimSpace(chat.ChatID) == "" || strings.TrimSpace(rootMsgID) == "" || strings.TrimSpace(turnID) == "" {
		return nil
	}
	if _, direct, err := b.directReplyTargetForBinding(chat); err != nil || direct {
		// Direct destinations include DMs and Bot/group singleton output. The
		// experiment is deliberately limited to ordinary topic replies.
		return nil
	}
	kind, err := b.db.WorkspaceKind(b.currentBridgeId(), chat.ChatID)
	if err != nil || (kind != WorkspaceKindGroup && kind != WorkspaceKindTopicGroup) {
		return nil
	}
	return uniqueOpenIDs(b.turnSenderOpenIDs(rootMsgID, turnID))
}

func renderTurnInitiatorMentionElements(openIDs []string) []cardElement {
	openIDs = uniqueOpenIDs(openIDs)
	if len(openIDs) == 0 {
		return nil
	}
	mentions := make([]string, 0, len(openIDs))
	for _, openID := range openIDs {
		mentions = append(mentions, `<at id="`+html.EscapeString(openID)+`"></at>`)
	}
	return []cardElement{{
		Tag:       "markdown",
		ElementID: turnInitiatorMentionElementID,
		Content:   strings.Join(mentions, " "),
	}}
}

func prependTurnInitiatorMentions(elements []cardElement, openIDs []string) []cardElement {
	mentions := renderTurnInitiatorMentionElements(openIDs)
	if len(mentions) == 0 {
		return elements
	}
	out := make([]cardElement, 0, len(mentions)+len(elements))
	out = append(out, mentions...)
	return append(out, elements...)
}
