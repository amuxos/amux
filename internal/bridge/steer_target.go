package bridge

import "strings"

// steerOutcomeUnknownNotice tells the user a steer may have reached the
// running turn even though the submit failed, so they do not send it twice.
const steerOutcomeUnknownNotice = "⚠️ 消息可能已送达当前任务，请勿重复发送。\n如果任务结束后仍未处理，请再发一次。"

// isSteerOutcomeUnknown reports the runtime's "steer outcome unknown" submit
// error (codes.Unknown): the input was not queued because the provider may
// already have it. Routed errors lose their gRPC status, so match the text.
func isSteerOutcomeUnknown(err error) bool {
	return err != nil && strings.Contains(err.Error(), "steer outcome unknown")
}

// Steering injects a follow-up into the running turn as the same user's
// correction. That is only true when the follow-up comes from the person who
// asked for that turn: input from another group member, a group-watch alert
// or another Bot (no sender) queues behind the turn instead.

// busyInputMode returns the submit mode for input that may arrive while a
// turn runs: steer pinned to the running turn when the sender owns it.
// The Runtime is authoritative for queue order.
func (c *turnCoalescer) busyInputMode(rootMsgID string, inputs []turnInputRef) submitMode {
	if turnID, ok := c.steerTarget(rootMsgID, inputs); ok {
		return submitMode{steer: true, steerExpectedTurnID: turnID}
	}
	return submitMode{}
}

// steerTarget returns the running turn of rootMsgID when every input comes
// from the one sender who sent all of that turn's input.
func (c *turnCoalescer) steerTarget(rootMsgID string, inputs []turnInputRef) (string, bool) {
	sender := ""
	for _, input := range inputs {
		openID := strings.TrimSpace(input.OpenID)
		if openID == "" || (sender != "" && openID != sender) {
			return "", false
		}
		sender = openID
	}
	if sender == "" {
		return "", false
	}
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	turnID := c.activeTurnIDs[rootMsgID]
	if turnID == "" {
		// The stream has not reported the running turn: right after a submit
		// (before turn_started) or after a stream reconnect. Pin the steer
		// to the newest turn this bridge submitted; the runtime and provider
		// steer only when that turn is the one running, otherwise the input
		// is queued. With no record at all (e.g. after a bridge restart) the
		// input queues: the runtime does not know who sent a turn, so a steer
		// without an expected turn could let one person steer another's turn.
		turnID = c.latestTurnIDs[rootMsgID]
	}
	if turnID == "" {
		return "", false
	}
	senders := c.turnSenders[rootMsgID][turnID]
	if len(senders) == 0 {
		return "", false
	}
	for _, openID := range senders {
		if openID != sender {
			return "", false
		}
	}
	return turnID, true
}

// recordTurnSenders remembers who sent the input of a submitted (or steered)
// turn. Input without a sender makes the turn unsteerable.
func (c *turnCoalescer) recordTurnSenders(rootMsgID, turnID string, inputs []turnInputRef) {
	if rootMsgID == "" || turnID == "" {
		return
	}
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	if c.turnSenders == nil {
		c.turnSenders = make(map[string]map[string][]string)
	}
	turns := c.turnSenders[rootMsgID]
	if turns == nil {
		turns = make(map[string][]string)
		c.turnSenders[rootMsgID] = turns
	}
	if c.latestTurnIDs == nil {
		c.latestTurnIDs = make(map[string]string)
	}
	c.latestTurnIDs[rootMsgID] = turnID
	if len(inputs) == 0 {
		turns[turnID] = append(turns[turnID], "")
	}
	for _, input := range inputs {
		turns[turnID] = append(turns[turnID], strings.TrimSpace(input.OpenID))
	}
}

func (c *turnCoalescer) currentSteerCandidate(rootMsgID string) string {
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	if turnID := c.activeTurnIDs[rootMsgID]; turnID != "" {
		return turnID
	}
	return c.latestTurnIDs[rootMsgID]
}

func (c *turnCoalescer) observedActiveTurn(rootMsgID string) string {
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	return c.activeTurnIDs[rootMsgID]
}

// observeTurnStarted records the turn the session reports as running.
func (c *turnCoalescer) observeTurnStarted(rootMsgID, turnID string) {
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	if c.activeTurnIDs == nil {
		c.activeTurnIDs = make(map[string]string)
	}
	c.activeTurnIDs[rootMsgID] = turnID
}

// observeTurnFinished forgets a finished turn.
func (c *turnCoalescer) observeTurnFinished(rootMsgID, turnID string) {
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	if c.activeTurnIDs[rootMsgID] == turnID {
		delete(c.activeTurnIDs, rootMsgID)
	}
	delete(c.turnSenders[rootMsgID], turnID)
	if c.latestTurnIDs[rootMsgID] == turnID {
		delete(c.latestTurnIDs, rootMsgID)
	}
}

// forgetTurns drops the steering view of a root whose stream ended. The
// newest submitted turn and its senders are kept so a reconnected stream can
// still steer it before it reports the turn again; if that turn is no longer
// running the runtime rejects the steer and the input queues.
func (c *turnCoalescer) forgetTurns(rootMsgID string) {
	c.steerMu.Lock()
	defer c.steerMu.Unlock()
	delete(c.activeTurnIDs, rootMsgID)
	latest := c.latestTurnIDs[rootMsgID]
	senders := c.turnSenders[rootMsgID][latest]
	delete(c.turnSenders, rootMsgID)
	if latest != "" && len(senders) > 0 {
		c.turnSenders[rootMsgID] = map[string][]string{latest: senders}
	}
}
