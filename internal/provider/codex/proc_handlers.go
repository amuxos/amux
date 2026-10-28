package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/provider"
	providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"
)

func (p *Proc) snapshotThreadID() string {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	return p.threadID
}
func (p *Proc) snapshotThreadMetadata() (string, string) {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	return p.threadID, p.effectiveModel
}
func (p *Proc) snapshotTurnID() string {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	return p.turnID
}
func decodeEventScope(raw json.RawMessage) eventScope {
	var scope eventScope
	_ = json.Unmarshal(raw, &scope)
	if scope.TurnID == "" {
		scope.TurnID = scope.Turn.ID
	}
	return scope
}
func (p *Proc) registerHandlers(out chan<- []byte) {
	p.registerGoalHandlers()
	if p.discoverSkills {
		p.onNotification("skills/changed", func(providerjsonrpc.Message) {
			p.scheduleSkillRefresh(true)
		})
	}
	p.onNotification("item/agentMessage/delta", func(msg providerjsonrpc.Message) {
		p.deferPendingIdleCompletion()
		var n struct {
			Delta    string `json:"delta"`
			ItemID   string `json:"itemId"`
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		if err := json.Unmarshal(msg.Params, &n); err != nil || n.Delta == "" {
			return
		}
		if !p.acceptsActiveTurn(n.ThreadID, n.TurnID) {
			return
		}
		p.markFirstToken()
		p.resumeGenerationAt(time.Now())
		p.resetTurnActivity()
		var newItem bool
		p.msgMu.Lock()
		if n.ItemID != p.curItemID {
			p.curItemID = n.ItemID
			p.curMsg.Reset()
			newItem = n.ItemID != ""
		}
		p.curMsg.WriteString(n.Delta)
		p.msgMu.Unlock()
		if newItem {
			p.emitTurn(agentstream.AssistantMessage{}, agentstream.ActorAgent, "agent", "assistant_text", n.ItemID)
		}
		p.emitTurn(agentstream.AssistantDelta{Text: n.Delta}, agentstream.ActorAgent, "agent", "assistant_text_delta", n.ItemID)
		select {
		case out <- []byte(n.Delta):
		default:
		}
	})
	p.onNotification("item/reasoning/textDelta", func(msg providerjsonrpc.Message) {
		p.deferPendingIdleCompletion()
		var n struct {
			Delta    string `json:"delta"`
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		if err := json.Unmarshal(msg.Params, &n); err != nil || n.Delta == "" {
			return
		}
		if !p.acceptsActiveTurn(n.ThreadID, n.TurnID) {
			return
		}
		p.markFirstToken()
		p.resumeGenerationAt(time.Now())
		p.resetTurnActivity()
		p.emitTurn(agentstream.ThinkingDelta{Text: n.Delta}, agentstream.ActorAgent, "agent", "thinking_delta", "")
	})
	p.onNotification("thread/tokenUsage/updated", func(msg providerjsonrpc.Message) {
		p.deferPendingIdleCompletion()
		var n threadTokenUsageUpdatedNotification
		if err := json.Unmarshal(msg.Params, &n); err != nil {
			return
		}
		if !p.acceptsActiveTurn(n.ThreadID, n.TurnID) {
			return
		}
		p.resetTurnActivity()
		usage := map[string]int64{"input_tokens": n.TokenUsage.Last.InputTokens, "output_tokens": n.TokenUsage.Last.OutputTokens, "total_tokens": n.TokenUsage.Last.TotalTokens, "cached_input_tokens": n.TokenUsage.Last.CachedInputTokens, "cache_creation_input_tokens": n.TokenUsage.Last.CacheCreationInputTokens, "reasoning_output_tokens": n.TokenUsage.Last.ReasoningOutputTokens}
		if total := n.TokenUsage.Total; total != nil {
			usage["cumulative_input_tokens"] = total.InputTokens
			usage["cumulative_output_tokens"] = total.OutputTokens
			usage["cumulative_total_tokens"] = total.TotalTokens
			usage["cumulative_cached_input_tokens"] = total.CachedInputTokens
			usage["cumulative_cache_creation_input_tokens"] = total.CacheCreationInputTokens
			usage["cumulative_reasoning_output_tokens"] = total.ReasoningOutputTokens
		}
		if n.TokenUsage.ModelContextWindow != nil {
			usage["context_window"] = *n.TokenUsage.ModelContextWindow
		}
		if ttftMs, ok := p.turnTTFTMillis(); ok {
			usage["ttft_ms"] = ttftMs
		}
		if tps, ok := p.outputTokensPerSec(n.TokenUsage.Last.OutputTokens); ok {
			usage["tokens_per_sec"] = tps
		}
		raw, err := json.Marshal(usage)
		if err != nil {
			return
		}
		p.usageMu.Lock()
		p.turnUsage = append(p.turnUsage[:0], raw...)
		p.usageMu.Unlock()
		p.emitTurn(agentstream.UsageObserved{Usage: raw}, agentstream.ActorAgent, "agent", "usage", "")
	})
	p.onNotification("turn/started", func(msg providerjsonrpc.Message) {
		scope := decodeEventScope(msg.Params)
		if !p.acceptsThread(scope.ThreadID) {
			p.deferPendingIdleCompletion()
			return
		}
		turnID := scope.TurnID
		if turnID == "" {
			return
		}
		p.threadMu.Lock()
		nativeGoalStarted := false
		if p.turnID == "" && p.pendingTurnStart == nil && !p.isTerminalTurnLocked(turnID) {
			p.threadMu.Unlock()
			nativeGoalStarted = p.admitGoalTurn(turnID)
			p.threadMu.Lock()
		}
		fresh, dropped := true, false
		switch {
		case p.isTerminalTurnLocked(turnID):
			dropped = true
		case p.turnID == turnID:
			p.lastFailureTurnID = ""
			fresh = nativeGoalStarted
		case p.turnID != "":
			p.quarantineUnexpectedTurnLocked(turnID)
			dropped = true
		default:
			accepted, wasFresh := p.adoptPendingTurnCandidateLocked(turnID)
			fresh, dropped = wasFresh, !accepted
		}
		p.threadMu.Unlock()
		if dropped {
			return
		}
		if fresh {
			p.usageMu.Lock()
			p.turnUsage = nil
			p.usageMu.Unlock()
			p.msgMu.Lock()
			p.curMsg.Reset()
			p.curItemID = ""
			p.msgMu.Unlock()
		}
		// Timeline owns TurnStarted; the native notification only arms provider
		// correlation and must not create a second lifecycle event.
	})
	p.onNotification("turn/completed", func(msg providerjsonrpc.Message) {
		scope := decodeEventScope(msg.Params)
		if !p.acceptsActiveTurn(scope.ThreadID, scope.TurnID) {
			p.notePossibleZombieDeath(scope.TurnID)
			p.deferPendingIdleCompletion()
			return
		}
		completed, hadTool := p.claimTurnCompletion()
		if !completed {
			return
		}
		p.emitTurnDone(hadTool)
	})
	p.onNotification("thread/status/changed", func(msg providerjsonrpc.Message) {
		var n struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(msg.Params, &n); err != nil {
			return
		}
		if !p.acceptsThread(n.ThreadID) {
			if n.Status.Type == "active" {
				p.deferPendingIdleCompletion()
			}
			return
		}
		switch n.Status.Type {
		case "idle":
			p.threadMu.Lock()
			schedule := p.canAcceptUnscopedIdleLocked()
			if schedule {
				p.scheduleIdleCompletionLocked()
			}
			p.threadMu.Unlock()
			return
		case "active":
			p.threadMu.Lock()
			p.cancelIdleCompletionLocked()
			if p.turnID != "" {
				p.lastTurnActivity = time.Now()
				p.reconcileWatchdogTimersLocked()
			}
			p.threadMu.Unlock()
			return
		case "systemError":
			p.emitTurnFailure("thread status: systemError", "")
			return
		default:
			return
		}
	})
	p.onNotification("turn/failed", func(msg providerjsonrpc.Message) {
		scope := decodeEventScope(msg.Params)
		if !p.acceptsActiveTurn(scope.ThreadID, scope.TurnID) {
			p.notePossibleZombieDeath(scope.TurnID)
			p.deferPendingIdleCompletion()
			return
		}
		var n struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(msg.Params, &n)
		reason := n.Error.Message
		if reason == "" {
			reason = "turn failed"
		}
		p.emitTurnFailure(reason, n.Error.Code)
	})
	p.onNotification("error", func(msg providerjsonrpc.Message) {
		scope := decodeEventScope(msg.Params)
		if !p.acceptsActiveTurn(scope.ThreadID, scope.TurnID) && (scope.ThreadID != "" || scope.TurnID != "") {
			p.deferPendingIdleCompletion()
			return
		}
		var n struct {
			Message   string `json:"message"`
			Code      string `json:"code"`
			WillRetry bool   `json:"willRetry"`
			Error     struct {
				Message        string          `json:"message"`
				CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		}
		_ = json.Unmarshal(msg.Params, &n)
		if n.WillRetry && p.noteRetryActivity() {
			return
		}
		reason := n.Error.Message
		if strings.TrimSpace(reason) == "" {
			reason = n.Message
		}
		if strings.TrimSpace(reason) == "" {
			reason = "provider error"
		}
		errorCode := n.Code
		if strings.TrimSpace(errorCode) == "" {
			errorCode = codexErrorInfoCode(n.Error.CodexErrorInfo)
		}
		if p.snapshotTurnID() != "" {
			p.emitTurnFailure(reason, errorCode)
			return
		}
		p.emitProviderError(reason, errorCode)
	})
	p.onNotification("item/started", func(msg providerjsonrpc.Message) {
		p.deferPendingIdleCompletion()
		var n itemLifecycleNotification
		if err := json.Unmarshal(msg.Params, &n); err != nil {
			return
		}
		if !p.acceptsActiveTurn(n.ThreadID, n.TurnID) {
			return
		}
		p.emitItemToolEvent(n.Item, "", true)
		if isToolItemType(n.Item.Type) {
			p.noteToolStarted(n.Item, progressToolItemTypes[n.Item.Type])
		}
	})
	p.onNotification("item/completed", func(msg providerjsonrpc.Message) {
		p.deferPendingIdleCompletion()
		var n itemLifecycleNotification
		if err := json.Unmarshal(msg.Params, &n); err != nil {
			return
		}
		if !p.acceptsActiveTurn(n.ThreadID, n.TurnID) {
			return
		}
		if n.Item.Type == "agentMessage" && n.Item.Delivery == "async" && len(n.Item.Questions) > 0 {
			p.emitAsyncUserInput(n)
			return
		}
		p.emitItemToolEvent(n.Item, "", false)
		if isToolItemType(n.Item.Type) {
			p.noteToolCompleted(n.Item.ID)
		} else {
			p.resetTurnActivity()
		}
		if n.Item.Type != "agentMessage" {
			return
		}
		p.threadMu.Lock()
		activeTurnID := p.turnID
		if activeTurnID != "" && (n.TurnID == "" || n.TurnID == activeTurnID) {
			// A completed message may be progress before a long silent model step.
			// Keep its full text, but wait for provider turn completion or watchdog failure.
			p.reconcileCompletedAgentMessage(n.Item)
		}
		p.threadMu.Unlock()
	})
	p.onNotification("item/commandExecution/outputDelta", func(msg providerjsonrpc.Message) {
		p.deferPendingIdleCompletion()
		var n struct {
			Delta    string `json:"delta"`
			ItemID   string `json:"itemId"`
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		if err := json.Unmarshal(msg.Params, &n); err != nil || n.Delta == "" {
			return
		}
		if !p.acceptsActiveTurn(n.ThreadID, n.TurnID) {
			return
		}
		p.resetToolProgress()
		p.emitTool(agentstream.ToolPhaseUpdated, agentstream.Tool{ID: n.ItemID, Name: "command", OutputPreview: truncateOutput(n.Delta)}, "tool_updated")
	})
	p.onNotification("item/fileChange/patchUpdated", func(msg providerjsonrpc.Message) {
		p.deferPendingIdleCompletion()
		var n fileChangePatchUpdatedNotification
		if err := json.Unmarshal(msg.Params, &n); err != nil {
			return
		}
		if !p.acceptsActiveTurn(n.ThreadID, n.TurnID) {
			return
		}
		p.resetToolProgress()
		title, input := summarizeFileChanges(n.Changes)
		p.emitTool(agentstream.ToolPhaseUpdated, agentstream.Tool{ID: n.ItemID, Name: "file_change", Title: title, Input: input}, "tool_updated")
	})
	p.onNotification("item/mcpToolCall/progress", func(msg providerjsonrpc.Message) {
		p.deferPendingIdleCompletion()
		var n struct {
			ItemID   string `json:"itemId"`
			Message  string `json:"message"`
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		if err := json.Unmarshal(msg.Params, &n); err != nil || n.Message == "" {
			return
		}
		if !p.acceptsActiveTurn(n.ThreadID, n.TurnID) {
			return
		}
		p.resetToolProgress()
		p.emitTool(agentstream.ToolPhaseUpdated, agentstream.Tool{ID: n.ItemID, OutputPreview: truncateOutput(n.Message)}, "tool_updated")
	})
	approvalHandler := func(method string) providerjsonrpc.Handler {
		return func(msg providerjsonrpc.Message) {
			p.handleApprovalRequest(method, msg)
		}
	}
	p.onRequest(methodCommandExecApproval, approvalHandler(methodCommandExecApproval))
	p.onRequest(methodFileChangeApproval, approvalHandler(methodFileChangeApproval))
	p.onRequest(methodApplyPatchApproval, approvalHandler(methodApplyPatchApproval))
	p.onRequest(methodExecCommandApprovalRoot, approvalHandler(methodExecCommandApprovalRoot))
	p.onRequest(methodPermissionsApproval, approvalHandler(methodPermissionsApproval))
	p.onRequest(methodToolRequestUserInput, providerjsonrpc.Handler(p.handleUserInputRequest))
	p.client.OnUnknownRequest(func(method string, _ int64, params json.RawMessage) {
		scope := decodeEventScope(params)
		if !p.acceptsActiveTurn(scope.ThreadID, scope.TurnID) && (scope.ThreadID != "" || scope.TurnID != "") {
			p.deferPendingIdleCompletion()
			return
		}
		reason := fmt.Sprintf("unhandled provider request: %s", method)
		if p.snapshotTurnID() != "" {
			p.emitTurnFailure(reason, "")
			return
		}
		p.emitProviderError(reason, "")
	})
}

func (p *Proc) isStaleTurnScopeLocked(scope eventScope) bool {
	if scope.ThreadID == "" && scope.TurnID == "" {
		return false
	}
	if scope.ThreadID != "" && p.threadID != "" && scope.ThreadID != p.threadID {
		return true
	}
	if scope.TurnID != "" {
		if p.turnID != "" {
			if scope.TurnID == p.turnID {
				return false
			}
			p.quarantineUnexpectedTurnLocked(scope.TurnID)
			return true
		}
		if p.isTerminalTurnLocked(scope.TurnID) {
			return true
		}
		accepted, _ := p.adoptPendingTurnCandidateLocked(scope.TurnID)
		return !accepted
	}
	return false
}
func (p *Proc) handleApprovalRequest(method string, msg providerjsonrpc.Message) {
	p.deferPendingIdleCompletion()
	scope := decodeEventScope(msg.Params)
	permID := strconv.FormatInt(msg.ID, 10)
	p.threadMu.Lock()
	if p.isStaleTurnScopeLocked(scope) {
		p.threadMu.Unlock()
		return
	}
	fresh := p.registerApprovalWaitLocked(permID, pendingApproval{reqID: msg.ID, method: method})
	if fresh && scope.TurnID != "" {
		p.curTurnProduced = true
	}
	p.threadMu.Unlock()
	if !fresh {
		return
	}
	perm := provider.Permission{ID: permID, Provider: p.provider(), Options: approvalOptions(method), Raw: msg.Params}
	p.threadMu.Lock()
	if p.turnID != "" {
		p.emitPermission(perm)
	}
	p.threadMu.Unlock()
}

func (p *Proc) handleUserInputRequest(msg providerjsonrpc.Message) {
	p.deferPendingIdleCompletion()
	scope := decodeEventScope(msg.Params)
	var params struct {
		Items []inputQuestionParams `json:"questions"` // isStaleTurnScopeLocked reports whether a server->client request carries a
		// thread/turn scope that does not match the active turn and cannot be adopted
		// (a zombie turn the watchdog failed client-side, or a cross-turn straggler).
		// Such requests are dropped WITHOUT a response: responding could unblock
		// server-side execution for a turn whose events amux no longer surfaces, while
		// leaving the request pending keeps it blocked until the watchdog's
		// turn/interrupt cancels the zombie. Unscoped requests cannot be validated and
		// are accepted, matching the pre-gate behavior. The caller must hold p.threadMu.
		//
		// A scoped request proves the provider created a turn, so when no turn is
		// currently armed (the response-to-arm window, with turn/started dropped) and
		// the scope matches our thread and is NOT a turn we already declared dead, we
		// arm from the scope instead of dropping the request — otherwise a valid
		// approval would be dropped without a response and the provider blocks forever.
		// handleUserInputRequest handles item/tool/requestUserInput (user questions).
		// One request carries N questions; amux emits one permission_requested event per
		// question (permission_id = "<reqID>:<questionId>") and aggregates the answers,
		// responding to the server once every question has been answered.

		ItemID   string `json:"itemId"`
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Blocking *bool  `json:"isBlocking"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		_ = p.client.RespondError(msg.ID, -32602, "invalid requestUserInput params: "+err.Error())
		return
	}
	if len(params.Items) == 0 {
		_ = p.client.Respond(msg.ID, map[string]any{"answers": map[string]any{}})
		return
	}
	pending := &pendingUserInput{reqID: msg.ID, answered: make(map[string][]string), permToQ: make(map[string]string), multiSel: make(map[string]bool)}
	seenQuestions := make(map[string]bool)
	for _, q := range params.Items {
		qid := strings.TrimSpace(q.ID)
		if qid == "" || strings.TrimSpace(q.Question) == "" || seenQuestions[qid] {
			_ = p.client.RespondError(msg.ID, -32602, "invalid or duplicate user-input question")
			return
		}
		seenQuestions[qid] = true
		iq := inputQuestion{id: qid, header: q.Header, question: q.Question, multiSelect: q.MultiSelect, allowOther: q.AllowOther, secret: q.Secret, blocking: params.Blocking}
		seenOptions := make(map[string]bool)
		for _, o := range q.Options {
			label := strings.TrimSpace(o.Label)
			if label == "" || seenOptions[label] {
				_ = p.client.RespondError(msg.ID, -32602, "invalid or duplicate user-input option")
				return
			}
			seenOptions[label] = true
			iq.options = append(iq.options, inputOption{label: label, description: o.Description})
		}
		pending.questions = append(pending.questions, iq)
		pending.multiSel[qid] = q.MultiSelect
	}
	if len(pending.questions) == 0 {
		_ = p.client.Respond(msg.ID, map[string]any{"answers": map[string]any{}})
		return
	}
	p.threadMu.Lock()
	if p.isStaleTurnScopeLocked(scope) {
		p.threadMu.Unlock()
		return
	}
	fresh := p.registerUserInputWaitLocked(msg.ID, pending)
	if fresh && scope.TurnID != "" {
		p.curTurnProduced = true
	}
	p.threadMu.Unlock()
	if !fresh {
		return
	}
	p.threadMu.Lock()
	if p.turnID == "" {
		p.threadMu.Unlock()
		return
	}
	defer p.threadMu.Unlock()
	for _, iq := range pending.questions {
		permID := userInputPermissionID(msg.ID, iq.id)
		pending.permToQ[permID] = iq.id
		opts := make([]provider.PermissionOption, 0, len(iq.options))
		for _, o := range iq.options {
			opts = append(opts, provider.PermissionOption{OptionId: o.label, Name: o.label, Description: o.description})
		}
		perm := provider.Permission{ID: permID, Provider: p.provider(), Options: opts, ToolCall: &provider.PermissionToolCall{Name: "AskUserQuestion", Title: iq.header, Question: iq.question, MultiSelect: iq.multiSelect, AllowOther: iq.allowOther, Secret: iq.secret, Blocking: iq.blocking}, Raw: msg.Params}
		p.emitPermission(perm)
	}
}

func (p *Proc) respondUserInput(permID string, optionIDs []string) error { // respondUserInput records an answer for one question of a requestUserInput
	// request. When every question has been answered, it responds to the server.
	// optionIDs carries the selected labels (1 for single-select, 1+ for multiSelect).
	// Returns an error if the permission id is unknown.

	reqID, qid, ok := parseUserInputPermissionID(permID)
	if !ok {
		return errNotUserInputPermission
	}
	p.threadMu.Lock()
	pending, exists := p.userInputs[reqID]
	if !exists {
		p.threadMu.Unlock()
		return fmt.Errorf("%s: unknown user-input request id %d", p.provider(), reqID)
	}
	var question *inputQuestion
	for i := range pending.questions {
		if pending.questions[i].id == qid {
			question = &pending.questions[i]
			break
		}
	}
	if question == nil || pending.permToQ[permID] != qid {
		p.threadMu.Unlock()
		return fmt.Errorf("%s: unknown user-input question %q", p.provider(), qid)
	}
	if _, answered := pending.answered[qid]; answered {
		p.threadMu.Unlock()
		return fmt.Errorf("%s: user-input question %q was already answered", p.provider(), qid)
	}
	chosen := make([]string, 0, len(optionIDs))
	seen := make(map[string]bool, len(optionIDs))
	for _, o := range optionIDs {
		o = strings.TrimSpace(o)
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		chosen = append(chosen, o)
	}
	if len(chosen) == 0 {
		p.threadMu.Unlock()
		return fmt.Errorf("%s: no option selected for question %q", p.provider(), qid)
	}
	if !question.multiSelect && len(chosen) != 1 {
		p.threadMu.Unlock()
		return fmt.Errorf("%s: question %q accepts one answer", p.provider(), qid)
	}
	if len(question.options) > 0 && !question.allowOther {
		for _, answer := range chosen {
			valid := false
			for _, option := range question.options {
				if option.label == answer {
					valid = true
					break
				}
			}
			if !valid {
				p.threadMu.Unlock()
				return fmt.Errorf("%s: invalid answer for question %q", p.provider(), qid)
			}
		}
	}
	pending.answered[qid] = chosen
	defer p.threadMu.Unlock()
	if len(pending.answered) == len(pending.questions) {
		answers := make(map[string]any, len(pending.questions))
		for _, q := range pending.questions {
			answers[q.id] = map[string]any{"answers": pending.answered[q.id]}
		}
		if err := p.client.Respond(reqID, map[string]any{"answers": answers}); err != nil {
			delete(pending.answered, qid) // permit a retry after a failed write
			return err
		}
		delete(p.userInputs, reqID)
		p.reconcileWatchdogTimersLocked()
	}
	p.emitTurn(agentstream.PermissionResolved{RequestID: permID}, agentstream.ActorUser, "user", "user_input_answered", permID)
	return nil
}

func userInputPermissionID(reqID int64, questionID string) string {
	return strconv.FormatInt(reqID, 10) + ":" + questionID
}

func parseUserInputPermissionID(permID string) (int64, string, bool) {
	idx := strings.IndexByte(permID, ':')
	if idx <= 0 || idx == len(permID)-1 {
		return 0, "", false
	}
	reqID, err := strconv.ParseInt(permID[: // userInputPermissionID composes "<reqID>:<questionId>".
	// parseUserInputPermissionID splits "<reqID>:<questionId>" back out.
	idx], 10, 64)
	if err != nil || reqID < 0 {
		return 0, "", false
	}
	return reqID, permID[idx+1:], true
}
func approvalOptions(method string) []provider.PermissionOption {
	switch method {
	case methodCommandExecApproval, methodExecCommandApprovalRoot:
		return []provider.PermissionOption{{OptionId: optAccept, Name: "Accept"}, {OptionId: optAcceptForSession, Name: "Accept for session"}, {OptionId: optDecline, Name: "Decline"}, {OptionId: optCancel, Name: "Cancel turn", Kind: "reject"}}
	case methodFileChangeApproval, methodApplyPatchApproval:
		return []provider.PermissionOption{{OptionId: optAccept, Name: "Accept"}, {OptionId: optAcceptForSession, Name: "Accept for session"}, {OptionId: optDecline, Name: "Decline"}, {OptionId: optCancel, Name: "Cancel turn", Kind: "reject"}}
	case methodPermissionsApproval:
		return []provider.PermissionOption{{OptionId: optDecline, Name: "Decline"}, {OptionId: optCancel, Name: "Cancel turn", Kind: "reject"}}
	default:
		return []provider.PermissionOption{{OptionId: optDecline, Name: "Decline"}}
	}
}
func approvalResponsePayload(method, optionID string) any {
	switch method {
	case methodPermissionsApproval:
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	default:
		decision := normalizeDecision(optionID)
		return map[string]any{"decision": decision}
	}
}
func normalizeDecision(optionID string) string {
	switch optionID {
	case optAccept, optAcceptForSession, optDecline, optCancel:
		return optionID
	default:
		return optDecline
	}
}
func (p *Proc) emit(ev agentstream.ProducerEmission) {
	p.eventSinks.Emit(ev)
}

func (p *Proc) emitTurn(payload agentstream.TurnPayload, actor agentstream.ActorClass, channel, nativeType, nativeID string) {
	p.emit(agentstream.ProducerEmission{
		Origin: agentstream.Origin{Actor: actor, Channel: channel, Provider: p.provider(), NativeType: nativeType, NativeID: nativeID},
		Turn:   payload,
	})
}

func (p *Proc) emitTool(phase agentstream.ToolPhase, tool agentstream.Tool, nativeType string) {
	p.emitTurn(agentstream.ToolChanged{Phase: phase, Tool: tool}, agentstream.ActorTool, "tool", nativeType, tool.ID)
}

func (p *Proc) emitPermission(permission provider.Permission) {
	request := agentstream.PermissionRequest{ID: permission.ID, Provider: permission.Provider, Details: permission.Raw}
	if permission.ToolCall != nil {
		request.ToolCall = &agentstream.PermissionToolCall{
			ID: permission.ToolCall.ID, Name: permission.ToolCall.Name, Title: permission.ToolCall.Title,
			RawInput: permission.ToolCall.RawInput, Question: permission.ToolCall.Question, MultiSelect: permission.ToolCall.MultiSelect,
			AllowOther: permission.ToolCall.AllowOther, Secret: permission.ToolCall.Secret, Blocking: permission.ToolCall.Blocking,
			Delivery: permission.ToolCall.Delivery, AgentSessionID: permission.ToolCall.AgentSessionID,
		}
	}
	for _, option := range permission.Options {
		request.Options = append(request.Options, agentstream.PermissionOption{ID: option.OptionId, Name: option.Name, Kind: option.Kind, Description: option.Description})
	}
	p.emitTurn(agentstream.PermissionRequested{Request: request}, agentstream.ActorAgent, "agent", "permission_requested", permission.ID)
}

func (p *Proc) emitCommands(commands []provider.AvailableCommand) {
	canonical := make([]agentstream.Command, 0, len(commands))
	for _, command := range commands {
		canonical = append(canonical, agentstream.Command{Name: command.Name, Description: command.Description, InputHint: command.InputHint, Kind: command.Kind})
	}
	p.emit(agentstream.ProducerEmission{
		Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.provider(), NativeType: "available_commands"},
		Session: agentstream.CommandsReplaced{Commands: canonical},
	})
}

func (p *Proc) emitSessionMetadata(agentSessionID, effectiveModel string) {
	p.emit(agentstream.ProducerEmission{
		Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.provider(), NativeType: "session_metadata"},
		Session: agentstream.SessionMetadataChanged{
			AgentSessionID: agentSessionID,
			EffectiveModel: effectiveModel,
		},
	})
}

func (p *Proc) reconcileCompletedAgentMessage(item threadItem) {
	p.msgMu.Lock()
	defer p.msgMu.Unlock()
	if item.ID != "" && p.curItemID != "" && item.ID != p.curItemID {
		return
	}
	if item.ID != "" {
		p.curItemID = item.ID
	}
	if strings.TrimSpace(item.Text) != "" {
		p.curMsg.Reset()
		p.curMsg.WriteString(item.Text)
	}
}

func codexErrorInfoCode(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var code string
	if err := json.Unmarshal(raw, &code); err == nil {
		return strings.TrimSpace(code)
	}
	var variant map[ // codexErrorInfoCode extracts the stable diagnostic category from the
	// Codex-compatible TurnError.codexErrorInfo union. Current schemas encode simple
	// categories as strings and categories with metadata as a single-key object.
	string]json.RawMessage
	if err := json.Unmarshal(raw, &variant); err != nil || len(variant) != 1 {
		return ""
	}
	for name := range variant {
		return strings.TrimSpace(name)
	}
	return ""
}
func formatProviderErrorReason(reason, errorCode string) string {
	errorCode = strings.TrimSpace(errorCode)
	if errorCode == "" {
		return reason
	}
	return fmt.Sprintf("%s (code=%s)", reason, errorCode)
}

func (p *Proc) emitTurnDone(hadTool bool) {
	p.cleanupInputStaging()
	p.msgMu.Lock()
	text := p.curMsg.String()
	p.curMsg.Reset()
	p.curItemID = ""
	p.msgMu.Unlock()
	p.usageMu.Lock()
	usage := append(json.RawMessage(nil), p.turnUsage...)
	p.turnUsage = nil
	p.usageMu.Unlock()
	usage = p.finalizeTurnSpeedMetrics(usage)
	if strings.TrimSpace(text) == "" && len(usage) > 0 && !hadTool {
		p.emitTurnFailureEvents("provider completed without assistant output", "empty_response")
		return
	}
	if text != "" {
		p.emitTurn(agentstream.ResultObserved{Text: text, Usage: usage}, agentstream.ActorAgent, "result", "result", "")
	}
	terminal := agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}
	p.emit(agentstream.ProducerEmission{Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.provider(), NativeType: "turn_done"}, Terminal: &terminal})
}

func (p *Proc) emitTurnFailure(reason, errorCode string) {
	p.threadMu.Lock()
	p.emitTurnFailureLocked(reason, errorCode)
}

func (p *Proc) emitTurnFailureLocked(reason, errorCode string) {
	p.cancelIdleCompletionLocked()
	p.stopTurnWatchdogLocked()
	turnID := p.turnID
	if turnID != "" && turnID == p.lastFailureTurnID {
		p.threadMu.Unlock()
		return
	}
	if turnID == "" && p.lastFailureTurnID != "" {
		p.threadMu.Unlock()
		return
	}
	if turnID != "" {
		p.markTerminalTurnLocked(turnID)
		p.turnID = ""
	}
	p.lastFailureTurnID = turnID
	p.curTurnHadTool = false
	p.clearPendingUserWaitsLocked()
	p.threadMu.Unlock()
	p.msgMu.Lock()
	p.curMsg.Reset()
	p.curItemID = ""
	p.msgMu.Unlock()
	p.usageMu.Lock()
	p.turnUsage = nil
	p.usageMu.Unlock()
	p.emitTurnFailureEvents(reason, errorCode)
}
func (p *Proc) emitTurnFailureEvents(reason, errorCode string) {
	p.cleanupInputStaging()
	if p.turnFailureHint != nil {
		if hint, code := p.turnFailureHint(reason, errorCode); hint != "" {
			if errorCode == "" {
				errorCode = code
			}
			reason = reason + "; " + hint
		}
	}
	message := p.provider() + ": " + formatProviderErrorReason(reason, errorCode)
	p.emit(agentstream.ProducerEmission{
		Origin: agentstream.Origin{Actor: agentstream.ActorSystem, Channel: "system", Provider: p.provider(), NativeType: "provider_failure"},
		Turn:   agentstream.Diagnostic{Severity: agentstream.SeverityError, Code: errorCode, Message: message},
	})
	terminal := agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: errorCode, Message: reason}
	if terminal.Code == "" {
		terminal.Code = "provider_error"
	}
	p.emit(agentstream.ProducerEmission{Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.provider(), NativeType: "turn_failed"}, Terminal: &terminal})
}

func (p *Proc) emitProviderError(reason, errorCode string) {
	message := p.provider() + ": " + formatProviderErrorReason(reason, errorCode)
	p.emit(agentstream.ProducerEmission{
		Origin:  agentstream.Origin{Actor: agentstream.ActorSystem, Channel: "system", Provider: p.provider(), NativeType: "provider_error"},
		Session: agentstream.SessionDiagnostic{Severity: agentstream.SeverityError, Code: errorCode, Message: message},
	})
}

func (p *Proc) emitItemToolEvent(item threadItem, _ string, started bool) {
	switch item.Type {
	case "commandExecution":
		p.emitCommandExecToolEvent(item, started)
	case "fileChange":
		p.emitFileChangeToolEvent(item, started)
	case "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch":
		p.emitGenericToolCallEvent(item, started)
	default:
	}
}

// emitCommandExecToolEvent maps one command item directly to ToolChanged.
func (p *Proc) emitCommandExecToolEvent(item threadItem, started bool) {
	input, _ := json.Marshal(map[string]string{"command": item.Command, "cwd": item.Cwd})
	phase := commandExecToolPhase(item.Status, started)
	tool := agentstream.Tool{ID: item.ID, Name: "command", Title: item.Command, Input: input}
	if !started {
		tool.OutputPreview = truncateOutput(item.AggregatedOutput)
		tool.RawOutput, _ = json.Marshal(map[string]any{"output": item.AggregatedOutput, "exit_code": item.ExitCode})
		if phase == agentstream.ToolPhaseFailed {
			if item.ExitCode != nil && *item.ExitCode != 0 {
				tool.Error = fmt.Sprintf("exit code %d", *item.ExitCode)
			} else if item.AggregatedOutput != "" {
				tool.Error = truncateOutput(item.AggregatedOutput)
			}
		}
	}
	p.emitTool(phase, tool, toolNativeType(phase))
}
func (p *Proc) emitFileChangeToolEvent(item threadItem, started bool) {
	title, input := summarizeFileChanges(item.Changes)
	content := make([]map[string]any, 0, len(item.Changes))
	locations := make([]map[string]any, 0, len(item.Changes))
	for _, change := range item.Changes {
		content = append(content, map[string]any{"type": "diff", "path": change.Path, "diff": change.Diff, "kind": change.Kind})
		locations = append(locations, map[string]any{"path": change.Path})
	}
	contentJSON, _ := json.Marshal(content)
	locationsJSON, _ := json.Marshal(locations)
	phase := fileChangeToolPhase(item.Status, started)
	tool := agentstream.Tool{ID: item.ID, Name: "file_change", Title: title, Input: input, Content: contentJSON, Locations: locationsJSON}
	if !started {
		if phase == agentstream.ToolPhaseFailed && item.Changes != nil {
			tool.Error = "patch failed"
		}
	}
	p.emitTool(phase, tool, toolNativeType(phase))
}

func (p *Proc) emitGenericToolCallEvent(item threadItem, started bool) {
	name, title := genericToolNameAndTitle(item)
	phase := genericToolCallPhase(item.Status, started)
	tool := agentstream.Tool{ID: item.ID, Name: name, Title: title, Input: item.Arguments}
	if !started {
		if phase == agentstream.ToolPhaseFailed {
			tool.Error = item.Status
		}
	}
	p.emitTool(phase, tool, toolNativeType(phase))
}

func toolNativeType(phase agentstream.ToolPhase) string {
	return "tool_" + string(phase)
}

func genericToolNameAndTitle(item threadItem) (name, title string) {
	switch item.Type {
	case "mcpToolCall":
		name = item.Tool
		if item.Server != "" && item.Tool != "" {
			title = item.Server + "/" + item.Tool
		} else if item.Tool != "" {
			title = item.Tool
		}
	case "dynamicToolCall":
		name = "dynamic_tool"
		title = item.Tool
		if item.Namespace != "" && title != "" {
			title = item.Namespace + "/" + title
		}
	case "collabAgentToolCall":
		name = "collab_agent"
		if item.AgentRole != "" {
			title = item.AgentRole
		} else {
			title = item.AgentNickname
		}
	case "webSearch":
		name = "web_search"
		title = item.Query
	}
	if name == "" {
		name = "tool"
	}
	return name, title
}

func genericToolCallPhase(status string, started bool) agentstream.ToolPhase {
	if started {
		return agentstream.ToolPhaseStarted
	}
	if status == "failed" {
		return agentstream.ToolPhaseFailed
	}
	return agentstream.ToolPhaseCompleted
}

func commandExecToolPhase(status string, started bool) agentstream.ToolPhase {
	if started {
		return agentstream.ToolPhaseStarted
	}
	switch status {
	case "failed", "declined":
		return agentstream.ToolPhaseFailed
	default:
		return agentstream.ToolPhaseCompleted
	}
}
func fileChangeToolPhase(status string, started bool) agentstream.ToolPhase {
	if started {
		return agentstream.ToolPhaseStarted
	}
	switch status {
	case "failed", "declined":
		return agentstream.ToolPhaseFailed
	default:
		return agentstream.ToolPhaseCompleted
	}
}

// summarizeFileChanges builds a human-readable title and a canonical tool
// input payload from a set of file changes.
func summarizeFileChanges(changes []fileUpdateChange) (string, json.RawMessage) {
	if len(changes) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(changes))
	type fileChangeInput struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	inputs := make([]fileChangeInput, 0, len(changes))
	for _, c := range changes {
		kind := patchChangeKindType(c.Kind)
		parts = append(parts, fmt.Sprintf("%s (%s)", c.Path, kind))
		inputs = append(inputs, fileChangeInput{Path: c.Path, Kind: kind})
	}
	input, _ := json.Marshal(inputs)
	return strings.Join(parts, " "), input
}

func patchChangeKindType(kind json.RawMessage) string {
	var k struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(kind, &k); err != nil || k.Type == "" {
		return "update"
	}
	return k.Type
}

// truncateOutput caps a tool output preview to keep attach frames and activity
// cards readable.
func truncateOutput(s string) string {
	const max = 2000
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// abortStart tears down a handle whose start never completed.
func abortStart(handle isolation.ProcessHandle) {
	_ = handle.Kill(context.Background())
	_ = handle.Close()
}
