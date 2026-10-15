package bridge

import (
	"strings"
	"sync"
)

// turnSourceChats remembers, per turn, the Feishu chat of the first message
// that fed it. A singleton stream's binding is not tied to one chat, and
// steered, coalesced or queued input joins a turn started elsewhere, so the
// completion notification resolves its jump link from here.
type turnSourceChats struct {
	mu      sync.Mutex
	chats   map[string]turnSourceChat
	subtask map[string]bool
	order   []string
}

type turnSourceChat struct {
	ChatID   string
	ThreadID string
}

const maxTurnSourceChats = 2048

func turnSourceKey(rootMsgID, turnID string) string {
	return strings.TrimSpace(rootMsgID) + "\x00" + strings.TrimSpace(turnID)
}

func (t *turnSourceChats) remember(key string) {
	t.order = append(t.order, key)
	if len(t.order) > maxTurnSourceChats {
		drop := t.order[0]
		t.order = t.order[1:]
		delete(t.chats, drop)
		delete(t.subtask, drop)
	}
}

// recordTurnSourceChat keeps the first chat seen for a turn (first wins, so a
// steered message from another chat never overrides the turn's origin).
func (b *Bridge) recordTurnSourceChat(rootMsgID, turnID, chatID, threadID string) {
	if b == nil || strings.TrimSpace(turnID) == "" || strings.TrimSpace(chatID) == "" {
		return
	}
	t := &b.turnSources
	key := turnSourceKey(rootMsgID, turnID)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chats == nil {
		t.chats = map[string]turnSourceChat{}
	}
	if _, ok := t.chats[key]; ok {
		return
	}
	t.chats[key] = turnSourceChat{ChatID: strings.TrimSpace(chatID), ThreadID: strings.TrimSpace(threadID)}
	t.remember(key)
}

func (b *Bridge) turnSourceChat(rootMsgID, turnID string) (turnSourceChat, bool) {
	if b == nil {
		return turnSourceChat{}, false
	}
	t := &b.turnSources
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.chats[turnSourceKey(rootMsgID, turnID)]
	return c, ok
}

// markSubtaskTurn records a turn started by a background-task completion
// notice: it has no Feishu source chat by design.
func (b *Bridge) markSubtaskTurn(rootMsgID, turnID string) {
	if b == nil || strings.TrimSpace(turnID) == "" {
		return
	}
	t := &b.turnSources
	key := turnSourceKey(rootMsgID, turnID)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.subtask == nil {
		t.subtask = map[string]bool{}
	}
	if !t.subtask[key] {
		t.subtask[key] = true
		t.remember(key)
	}
}

func (b *Bridge) isSubtaskTurn(rootMsgID, turnID string) bool {
	if b == nil {
		return false
	}
	t := &b.turnSources
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.subtask[turnSourceKey(rootMsgID, turnID)]
}
