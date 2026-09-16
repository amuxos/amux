package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/amuxos/amux/internal/config"
)

func watchdogDurationWithUnit(seconds int, unit time.Duration) time.Duration {
	if unit <= 0 || unit > time.Second {
		unit = time.Second
	}
	if seconds < 0 || seconds > maxWatchdogSeconds {
		return time.Duration(maxWatchdogSeconds) * unit
	}
	return time.Duration(seconds) * unit
}
func (p *Proc) watchdogDuration(seconds int) time.Duration {
	return watchdogDurationWithUnit(seconds, p.watchdogTimeoutUnit)
}
func isToolItemType(typ string) bool {
	return toolItemTypes[typ]
}
func normalizeWatchdogConfig(providerName string, cfg config.StructuredProviderConfig) config.StructuredProviderConfig {
	defaults := config.DefaultStructuredProviderTimeoutsForProvider(providerName)
	if cfg.TurnIdleTimeoutSeconds < 0 {
		cfg.TurnIdleTimeoutSeconds = defaults.TurnIdleTimeoutSeconds
	}
	if cfg.TurnHardCapSeconds < 0 {
		cfg.TurnHardCapSeconds = defaults.TurnHardCapSeconds
	}
	if cfg.ToolIdleTimeoutSeconds < 0 {
		cfg.ToolIdleTimeoutSeconds = defaults.ToolIdleTimeoutSeconds
	}
	if cfg.RepeatToolCallLimit < 0 {
		cfg.RepeatToolCallLimit = defaults.RepeatToolCallLimit
	}
	if cfg.RepeatToolCallLimit > 1000 {
		cfg.RepeatToolCallLimit = 1000
	}
	return cfg
}

func (p *Proc) startTurnWatchdogLocked(turnID string) {
	p.stopTurnWatchdogLocked()
	p.watchdogGeneration++
	p.activeTools = make(map[ // watchdogDurationWithUnit converts configured timeout units to a clamped
	// time.Duration. The clamp is applied first: multiplying an out-of-range int
	// by the unit could overflow int64 and escape a post-multiplication check.
	// startTurnWatchdogLocked arms the turn idle and hard-cap timers for a new
	// turn. The caller must hold p.threadMu. A timeout of 0 means disabled.
	string]bool)
	p.approvals = make(map[string]pendingApproval)
	p.userInputs = make(map[int64]*pendingUserInput)
	p.curTurnProduced = false
	p.curTurnHadTool = false
	p.lastToolCallKey = ""
	p.repeatToolCallCount = 0
	p.seenToolItemIDs = make(map[string]struct{})
	now := time.Now()
	p.lastTurnActivity = now
	p.lastToolActivity = now
	gen := p.watchdogGeneration
	if p.watchdogConfig.TurnIdleTimeoutSeconds > 0 {
		p.turnIdleTimer = time.AfterFunc(p.watchdogDuration(p.watchdogConfig.TurnIdleTimeoutSeconds), func() {
			p.turnIdleTimeoutCallback(gen, turnID)
		})
	}
	if p.watchdogConfig.TurnHardCapSeconds > 0 {
		p.turnHardCapTimer = time.AfterFunc(p.watchdogDuration(p.watchdogConfig.TurnHardCapSeconds), func() {
			p.turnHardCapTimeoutCallback(gen, turnID)
		})
	}
}

func (p *Proc) stopTurnWatchdogLocked() {
	if p.turnIdleTimer != nil {
		p.turnIdleTimer.Stop()
		p.turnIdleTimer = nil
	}
	if p.turnHardCapTimer != nil {
		p.turnHardCapTimer.Stop()
		p.turnHardCapTimer = nil
	}
	if p.toolIdleTimer != nil {
		p.toolIdleTimer.Stop()
		p.toolIdleTimer = nil
	}
}

func (p *Proc) stopTurnWatchdog() {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	p.stopTurnWatchdogLocked()
}

func (p *Proc) hasProgressToolActiveLocked() bool {
	for _, hasProgress := range // stopTurnWatchdogLocked stops all watchdog timers. The caller must hold
	// p.threadMu.
	// hasProgressToolActiveLocked reports whether any in-flight tool item can
	// produce interim progress events. The caller must hold p.threadMu.
	p.activeTools {
		if hasProgress {
			return true
		}
	}
	return false
}

func (p *Proc) hasPendingUserWaitLocked() bool {
	return len(p.approvals) > 0 || len(p.userInputs) > 0
}

func (p *Proc) markTerminalTurnLocked(turnID string) {
	if turnID == "" {
		return
	}
	if p.terminalTurnIDs == nil {
		p.terminalTurnIDs = make(map[ // hasPendingUserWaitLocked reports whether the provider is blocked on any
		// unanswered approval or requestUserInput. Derived from the maps so it cannot
		// drift from them. The caller must hold p.threadMu.
		// markTerminalTurnLocked records that amux has already finalized turnID. The
		// caller must hold p.threadMu.
		string]struct{})
	}
	p.terminalTurnIDs[turnID] = struct{}{}
}

func (p *Proc) isTerminalTurnLocked(turnID string) bool {
	if turnID == "" {
		return false
	}
	_, ok := p.terminalTurnIDs[turnID]
	return ok
}

func (p *Proc) markZombieTurnLocked(turnID string) {
	if turnID == "" {
		return
	}
	if p.zombieTurnIDs == nil {
		p.zombieTurnIDs = make(map[ // isTerminalTurnLocked reports whether amux has already finalized turnID. The
		// caller must hold p.threadMu.
		// markZombieTurnLocked records a watchdog-failed turn whose server-side death
		// has not yet been confirmed. zombieTurnID remains the preferred manual
		// Interrupt fallback; zombieTurnIDs retains every outstanding tombstone so an
		// older zombie cannot become invisible when a newer turn also times out. The
		// caller must hold p.threadMu.
		string]struct{})
	}
	p.zombieTurnIDs[turnID] = struct{}{}
	p.zombieTurnID = turnID
}

func (p *Proc) clearZombieTurnLocked(turnID string) {
	delete(p.zombieTurnIDs, turnID)
	if p.zombieTurnID != turnID {
		return
	}
	p.zombieTurnID = ""
	for candidate := range // clearZombieTurnLocked consumes the server-side terminal proof for turnID.
	// If it was the preferred Interrupt fallback, choose another still-outstanding
	// zombie (if any). The caller must hold p.threadMu.
	p.zombieTurnIDs {
		p.zombieTurnID = candidate
		break
	}
}
func (p *Proc) hasOutstandingZombieLocked() bool {
	return len(p.zombieTurnIDs) > 0
}

func (p *Proc) quarantineUnexpectedTurnLocked(turnID string) {
	if turnID == "" || p.isTerminalTurnLocked(turnID) {
		return
	}
	p.markTerminalTurnLocked(turnID)
	p.markZombieTurnLocked(turnID)
	p.interruptTurnBestEffort(p.threadID, turnID)
}

func (p *Proc) canAcceptUnscopedIdleLocked() bool {
	return p.turnID != "" && (!p.hasOutstandingZombieLocked() || p.curTurnProduced)
}

func (p *Proc) toolIdleSuspendedForWaitLocked() bool {
	return p.hasPendingUserWaitLocked() && p.watchdogConfig.TurnHardCapSeconds > 0
}

func (p *Proc) turnIdleSuspendedLocked() bool {
	if p.hasPendingUserWaitLocked() {
		return true
	}
	if len(p.activeTools) == 0 {
		return false
	}
	if !p.hasProgressToolActiveLocked() {
		return true
	}
	return p.watchdogConfig.ToolIdleTimeoutSeconds > 0
}

func (p *Proc) reconcileWatchdogTimersLocked() {
	wantToolIdle := p.watchdogConfig.ToolIdleTimeoutSeconds > 0 && p.hasProgressToolActiveLocked() && !p.toolIdleSuspendedForWaitLocked() && p.turnID != ""
	if wantToolIdle && p.toolIdleTimer == nil {
		gen := p.watchdogGeneration
		turnID := p.turnID
		p.lastToolActivity = time.Now()
		p.toolIdleTimer = time.AfterFunc(p.watchdogDuration(p.watchdogConfig.ToolIdleTimeoutSeconds), func() {
			p.toolIdleTimeoutCallback(gen, turnID)
		})
	} else if !wantToolIdle && p.toolIdleTimer != nil {
		p.toolIdleTimer.Stop()
		p.toolIdleTimer = nil
	}
	if p.turnIdleTimer == nil {
		return
	}
	if p.turnIdleSuspendedLocked() {
		p.turnIdleTimer.Stop()
	} else {
		p.turnIdleTimer.Reset(p.watchdogDuration(p.watchdogConfig.TurnIdleTimeoutSeconds))
	}
}

func (p *Proc) resetTurnActivity() {
	p.threadMu.Lock()
	p.resetTurnActivityLocked()
	p.threadMu.Unlock()
}

func (p *Proc) resetTurnActivityLocked() {
	if p.turnID == "" {
		return
	}
	p.lastTurnActivity = time.Now()
	p.curTurnProduced = true
	p.reconcileWatchdogTimersLocked()
}

func (p *Proc) resetToolActivityLocked() {
	p.lastToolActivity = time.Now()
	if p.toolIdleTimer != nil {
		p.toolIdleTimer.Reset(p.watchdogDuration(p.watchdogConfig.ToolIdleTimeoutSeconds))
	}
}

func (p *Proc) resetToolProgress() {
	p.threadMu.Lock()
	if p.turnID != "" {
		p.lastTurnActivity = time.Now()
		p.curTurnProduced = true
		p.reconcileWatchdogTimersLocked()
	}
	p.resetToolActivityLocked()
	p.threadMu.Unlock()
}

func (p *Proc) noteToolStarted(item threadItem, hasProgress bool) {
	p.threadMu.Lock()
	if item.ID != "" {
		if _, duplicate := p.seenToolItemIDs[item.ID]; duplicate {
			p.threadMu.Unlock()
			return
		}
	}
	first, added := p.noteToolStartedLocked(item.ID, hasProgress)
	turnID := p.turnID
	reason := ""
	if added {
		if item.ID != "" {
			p.seenToolItemIDs[item.ID] = struct{}{}
		}
		reason = p.noteToolRepeatLocked(item)
	}
	p.threadMu.Unlock()
	if first {
		p.pauseGenerationAt(time.Now())
	}
	if reason != "" {
		p.failTurnByWatchdog(turnID, reason)
	}
}

// noteToolStartedLocked records the tool as active and reports whether this
// call transitioned the turn from no active tools to at least one, which is
// the generation-window pause edge. The second result reports whether itemID
// was newly registered, allowing other item/started consumers to ignore a
// duplicate lifecycle frame too.
func (p *Proc) noteToolStartedLocked(itemID string, hasProgress bool) (first, added bool) {
	if p.turnID == "" {
		return false, false
	}
	if _, dup := p.activeTools[itemID]; dup {
		return false, false
	}
	first = len(p.activeTools) == 0
	p.activeTools[itemID] = hasProgress
	now := time.Now()
	p.lastTurnActivity = now
	p.lastToolActivity = now
	p.curTurnProduced = true
	p.curTurnHadTool = true
	p.reconcileWatchdogTimersLocked()
	return first, true
}

func (p *Proc) noteToolCompleted(itemID string) {
	p.threadMu.Lock()
	p.noteToolCompletedLocked(itemID)
	p.threadMu.Unlock()
}

func (p *Proc) noteToolCompletedLocked(itemID string) {
	if p.turnID == "" {
		return
	}
	p.curTurnHadTool = true
	p.lastTurnActivity = time.Now()
	p.curTurnProduced = true
	if _, ok := p.activeTools[itemID]; !ok {
		p.reconcileWatchdogTimersLocked()
		return
	}
	delete(p.activeTools, itemID)
	p.reconcileWatchdogTimersLocked()
}

// noteToolRepeatLocked counts consecutive identical tool calls within the
// active turn and returns a failure reason once the configured limit is
// reached. Models can
// loop a no-progress call forever after their plan is already complete (a
// production glm-5.3-flash turn issued 1101 identical update_plan calls),
// which no timeout layer catches because every call is "activity". The key is
// the provider item's call-defining payload, preserving raw arguments for MCP
// and dynamic tools. Any different key resets the streak. The caller must hold
// threadMu and invoke this only for a newly accepted item ID.
func (p *Proc) noteToolRepeatLocked(item threadItem) string {
	limit := p.watchdogConfig.RepeatToolCallLimit
	if p.turnID == "" || limit <= 0 {
		return ""
	}
	key := repeatToolCallKey(item)
	if key != p.lastToolCallKey {
		p.lastToolCallKey = key
		p.repeatToolCallCount = 1
	} else {
		p.repeatToolCallCount++
	}
	if p.repeatToolCallCount < limit {
		return ""
	}
	return fmt.Sprintf("repeated identical tool calls: %s called %d times (code=repeat_tool_loop)", repeatToolCallLabel(item), p.repeatToolCallCount)
}

func repeatToolCallKey(item threadItem) string {
	const separator = "\x00"
	switch item.Type {
	case "commandExecution":
		return item.Type + separator + item.Command + separator + item.Cwd
	case "fileChange":
		changes, _ := json.Marshal(item.Changes)
		return item.Type + separator + string(changes)
	case "mcpToolCall":
		return item.Type + separator + item.Server + separator + item.Tool + separator + string(item.Arguments)
	case "dynamicToolCall":
		return item.Type + separator + item.Namespace + separator + item.Tool + separator + string(item.Arguments)
	case "collabAgentToolCall":
		return item.Type + separator + item.AgentNickname + separator + item.AgentRole + separator + item.Tool + separator + string(item.Arguments)
	case "webSearch":
		return item.Type + separator + item.Query
	default:
		return item.Type + separator + item.Server + separator + item.Tool + separator + string(item.Arguments)
	}
}

func repeatToolCallLabel(item threadItem) string {
	if item.Tool != "" {
		return item.Tool
	}
	name, _ := genericToolNameAndTitle(item)
	return name
}

func (p *Proc) registerApprovalWaitLocked(permID string, pending pendingApproval) bool {
	if _, dup := p.approvals[permID]; dup {
		return false
	}
	p.approvals[permID] = pending
	p.reconcileWatchdogTimersLocked()
	return true
}

func (p *Proc) registerUserInputWaitLocked(reqID int64, pending *pendingUserInput) bool {
	if _, dup := p.userInputs[reqID]; dup {
		return false
	}
	p.userInputs[reqID] = pending
	p.curTurnProduced = true
	p.reconcileWatchdogTimersLocked()
	return true
}

func (p *Proc) interruptTurnBestEffort(threadID, turnID string) {
	if p.client == nil || threadID == "" || turnID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = p.client.Call(ctx, "turn/interrupt", map[ // quarantineUnexpectedTurnLocked retires a provider turn that cannot belong
		// to the current amux submission. Remember it as terminal immediately so
		// duplicate late frames cannot adopt it, retain a zombie tombstone until the
		// provider confirms death, and cancel it asynchronously (notification
		// handlers run on the JSON-RPC read loop and therefore must not wait for a
		// response on that same loop). The caller must hold p.threadMu.
		// interruptTurnBestEffort asks the provider to cancel a turn the client-side
		// watchdog has declared dead. Without this the server-side turn keeps running
		// as a zombie: the next turn/start is rejected or hangs (single active turn
		// per thread) and the session re-wedges right after the watchdog freed it.
		// Fire-and-forget; the provider may already have finished the turn.
		string]string{"threadId": threadID, "turnId": turnID})
	}()
}

func (p *Proc) interruptTurnSyncOrdered(threadID, turnID string) {
	if threadID == "" || turnID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.watchdogInterruptTimeout)
	defer cancel()
	_, _ = p.client.Call(ctx, "turn/interrupt", map[ // interruptTurnSyncOrdered sends turn/interrupt synchronously so the request is
	// written to the pipe BEFORE the caller emits the queue-freeing failure — an
	// async (goroutine) dispatch gives no such ordering, letting a queued
	// resubmission's turn/start reach the provider first and be rejected by the
	// still-active zombie turn. The write is bounded by the client's OS write
	// deadline, and the response is matched by the jsonrpc read loop without
	// needing threadMu, so a healthy provider ACKs immediately; a wedged provider
	// costs at most the short timeout. The response is irrelevant (the turn is
	// being declared dead regardless) and any error is swallowed.
	string]string{"threadId": threadID, "turnId": turnID})
}

func (p *Proc) failTurnByWatchdogLocked(reason string) {
	threadID := p.threadID
	turnID := p.turnID
	p.markZombieTurnLocked(turnID)
	p.interruptTurnSyncOrdered(threadID, turnID)
	p.emitTurnFailureLocked(reason, "")
}

func (p *Proc) failTurnByWatchdog(expectedTurnID, reason string) {
	p.threadMu.Lock()
	if expectedTurnID == "" || p.turnID != expectedTurnID {
		p.threadMu.Unlock()
		return
	}
	p.failTurnByWatchdogLocked(reason)
}
func (p *Proc) turnIdleTimeoutCallback(expectedGen uint64, expectedTurnID string) {
	p.threadMu.Lock()
	if p.watchdogGeneration != expectedGen || p.turnID == "" || p.turnID != expectedTurnID {
		p.threadMu.Unlock()
		return
	}
	if p.turnIdleSuspendedLocked() {
		p.threadMu.Unlock()
		return
	}
	if remaining := time.Until(p.lastTurnActivity.Add(p.watchdogDuration(p.watchdogConfig.TurnIdleTimeoutSeconds))); remaining > 0 {
		if p.turnIdleTimer != nil {
			p.turnIdleTimer.Reset(remaining)
		}
		p.threadMu.Unlock()
		return
	}
	p.failTurnByWatchdogLocked("turn idle timeout")
}
func (p *Proc) turnHardCapTimeoutCallback(expectedGen uint64, expectedTurnID string) {
	p.threadMu.Lock()
	if p.watchdogGeneration != expectedGen || p.turnID == "" || p.turnID != expectedTurnID {
		p.threadMu.Unlock()
		return
	}
	p.failTurnByWatchdogLocked("turn hard cap exceeded")
}
func (p *Proc) toolIdleTimeoutCallback(expectedGen uint64, expectedTurnID string) {
	p.threadMu.Lock()
	if p.watchdogGeneration != expectedGen || p.turnID == "" || p.turnID != expectedTurnID {
		p.threadMu.Unlock()
		return
	}
	if p.watchdogConfig.ToolIdleTimeoutSeconds <= 0 || !p.hasProgressToolActiveLocked() {
		p.threadMu.Unlock()
		return
	}
	if p.toolIdleSuspendedForWaitLocked() {
		p.threadMu.Unlock()
		return
	}
	if remaining := time.Until(p.lastToolActivity.Add(p.watchdogDuration(p.watchdogConfig.ToolIdleTimeoutSeconds))); remaining > 0 {
		if p.toolIdleTimer != nil {
			p.toolIdleTimer.Reset(remaining)
		}
		p.threadMu.Unlock()
		return
	}
	p.failTurnByWatchdogLocked("tool idle timeout")
}

func (p *Proc) freshArmLocked(turnID string) {
	p.cancelIdleCompletionLocked()
	p.turnID = turnID
	p.lastFailureTurnID = ""
	p.startTurnWatchdogLocked(turnID)
}

func (p *Proc) adoptPendingTurnCandidateLocked(turnID string) (accepted, fresh bool) {
	attempt := p.pendingTurnStart
	if attempt == nil {
		p.quarantineUnexpectedTurnLocked(turnID)
		return false, false
	}
	if attempt.candidateTurnID == "" {
		attempt.candidateTurnID = turnID
		p.freshArmLocked(turnID)
		if attempt.started != nil {
			close(attempt.started)
		}
		if attempt.responseLess && p.pendingTurnStart == attempt {
			p.pendingTurnStart = nil
		}
		return true, true
	}
	if attempt.candidateTurnID != turnID {
		p.quarantineUnexpectedTurnLocked(turnID)
		return false, false
	}
	if p.turnID == "" && !p.isTerminalTurnLocked(turnID) {
		p.freshArmLocked(turnID)
		return true, true
	}
	return true, false
}
func (p *Proc) acceptsThread(threadID string) bool {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	return threadID == "" || p.threadID == "" || threadID == p.threadID
}
func (p *Proc) acceptsActiveTurn(threadID, turnID string) bool {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	if threadID != "" && p.threadID != "" && threadID != p.threadID {
		return false
	}
	if turnID != "" {
		return p.turnID != "" && turnID == p.turnID
	}
	return true
}

func (p *Proc) claimTurnCompletion() (bool, bool) {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	if p.turnID == "" {
		return false, false
	}
	p.cancelIdleCompletionLocked()
	p.stopTurnWatchdogLocked()
	p.markTerminalTurnLocked(p.turnID)
	p.turnID = ""
	p.clearPendingUserWaitsLocked()
	hadTool := p.curTurnHadTool
	p.curTurnHadTool = false
	return true, hadTool
}

func (p *Proc) clearPendingUserWaitsLocked() {
	p.approvals = make(map[ // failTurnByWatchdogLocked is the shared tail of the three watchdog timeout
	// callbacks. The caller must hold p.threadMu and have confirmed the deadline
	// truly elapsed for the active turn. It records the turn as a zombie (so a
	// later user Interrupt() can still cancel it), sends turn/interrupt
	// SYNCHRONOUSLY (guaranteeing it precedes any queued resubmission on the pipe),
	// and then emits the failure (which unlocks p.threadMu).
	// clearPendingUserWaitsLocked drops all unanswered approval/user-input
	// requests. Called when a turn ends (completed, failed, or idle-completed):
	// the maps are connection-scoped, so without this a late response to a dead
	// turn's permission would touch the NEW turn's wait state and prematurely
	// resume its idle watchdog. The provider side is unblocked by the turn end
	// itself (or by the watchdog's best-effort turn/interrupt for zombie turns).
	// The caller must hold p.threadMu.
	string]pendingApproval)
	p.userInputs = make(map[int64]*pendingUserInput)
}
func (p *Proc) cancelIdleCompletionLocked() {
	p.idleGeneration++
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
}
func (p *Proc) cancelIdleCompletion() {
	p.threadMu.Lock()
	p.cancelIdleCompletionLocked()
	p.threadMu.Unlock()
}
func (p *Proc) scheduleIdleCompletionLocked() {
	if p.turnID == "" {
		return
	}
	p.cancelIdleCompletionLocked()
	generation := p.idleGeneration
	turnID := p.turnID
	p.idleTimer = time.AfterFunc(p.idleCompletionDelay, func() {
		p.threadMu.Lock()
		if p.idleGeneration != generation || p.turnID != turnID {
			p.threadMu.Unlock()
			return
		}
		if p.hasPendingUserWaitLocked() {
			p.scheduleIdleCompletionLocked()
			p.threadMu.Unlock()
			return
		}
		if len(p.activeTools) > 0 {
			if deadline := p.toolBudgetDeadlineLocked(); !deadline.IsZero() && time.Now().Add(p.idleCompletionDelay).Before(deadline) {
				p.scheduleIdleCompletionLocked()
				p.threadMu.Unlock()
				return
			}
			p.activeTools = make(map[string]bool)
		}
		p.idleTimer = nil
		p.idleGeneration++
		p.markTerminalTurnLocked(p.turnID)
		p.turnID = ""
		p.stopTurnWatchdogLocked()
		p.clearPendingUserWaitsLocked()
		hadTool := p.curTurnHadTool
		p.curTurnHadTool = false
		p.threadMu.Unlock()
		p.emitTurnDone(hadTool)
	})
}

func (p *Proc) toolBudgetDeadlineLocked() time.Time {
	var budget time.Duration
	switch {
	case p.hasProgressToolActiveLocked() && p.watchdogConfig.ToolIdleTimeoutSeconds > 0:
		budget = p.watchdogDuration(p.watchdogConfig.ToolIdleTimeoutSeconds)
	case p.watchdogConfig.TurnHardCapSeconds > 0:
		budget = p.watchdogDuration(p.watchdogConfig.TurnHardCapSeconds)
	case p.watchdogConfig.TurnIdleTimeoutSeconds > 0:
		budget = p.watchdogDuration(p.watchdogConfig.TurnIdleTimeoutSeconds)
	default:
		return time.Time{}
	}
	return p.lastToolActivity.Add(budget)
}

func (p *Proc) deferPendingIdleCompletion() {
	p.threadMu.Lock()
	if p.idleTimer != nil {
		p.scheduleIdleCompletionLocked()
	}
	p.threadMu.Unlock()
}

func (p *Proc) noteRetryActivity() bool {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	if p.turnID == "" {
		return false
	}
	if p.idleTimer != nil {
		p.scheduleIdleCompletionLocked()
	}
	p.lastTurnActivity = time.Now()
	p.curTurnProduced = true
	p.reconcileWatchdogTimersLocked()
	return true
}

func (p *Proc) notePossibleZombieDeath(turnID string) {
	if turnID == "" {
		return
	}
	p.threadMu.Lock()
	p.clearZombieTurnLocked(turnID)
	p.threadMu.Unlock()
}

// toolBudgetDeadlineLocked returns the moment after which a still-"active"
// tool may be presumed lost/stuck for idle-completion purposes, derived from
// the same budget the watchdog would use to judge it: the tool idle timeout
// for progress-capable tools when that layer is enabled, else the turn hard
// cap, else the turn idle timeout. The zero Time means no budget is configured
// (all layers disabled), in which case a stable idle is taken at face value.
// The caller must hold p.threadMu.
// notePossibleZombieDeath consumes a terminal frame (turn/completed or
// turn/failed) scoped to a turn we declared a zombie: it confirms the zombie
// died server-side, so we stop targeting it with the Interrupt() fallback.
