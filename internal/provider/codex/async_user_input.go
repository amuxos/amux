package codex

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/provider"
)

// Async questions are completed messages, not server requests. In particular,
// there is no numeric RPC ID to answer and no provider wait to unblock.
func (p *Proc) emitAsyncUserInput(n itemLifecycleNotification) {
	if n.Item.ID == "" || n.ThreadID == "" || n.TurnID == "" {
		p.emitTurnFailure("async user-input message has no identity", "invalid_user_input")
		return
	}
	for _, q := range n.Item.Questions {
		if strings.TrimSpace(q.Title) == "" {
			p.emitTurnFailure("async user-input question is empty", "invalid_user_input")
			return
		}
		seen := make(map[string]bool)
		for _, option := range q.Options {
			if strings.TrimSpace(option) == "" || seen[option] {
				p.emitTurnFailure("async user-input option is empty or duplicated", "invalid_user_input")
				return
			}
			seen[option] = true
		}
	}
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	if p.threadID != n.ThreadID || p.turnID != n.TurnID {
		return
	}
	if p.asyncInputTurnID != n.TurnID {
		p.asyncInputTurnID = n.TurnID
		p.asyncInputItems = make(map[string]bool)
	}
	if p.asyncInputItems[n.Item.ID] {
		return
	}
	p.asyncInputItems[n.Item.ID] = true
	p.resetTurnActivityLocked()
	// A question-only turn has useful output even without assistant text.
	p.curTurnHadTool = true
	for i, q := range n.Item.Questions {
		blocking := false
		permission := provider.Permission{
			ID: fmt.Sprintf("async:%s:%d", n.Item.ID, i), Provider: p.provider(),
			ToolCall: &provider.PermissionToolCall{
				ID: n.Item.ID, Name: "request_user_input_async", Question: q.Title,
				AllowOther: true, Blocking: &blocking, Delivery: "async", AgentSessionID: n.ThreadID,
			},
		}
		for _, option := range q.Options {
			permission.Options = append(permission.Options, provider.PermissionOption{OptionId: option, Name: option})
		}
		p.emitPermission(permission)
	}
}
