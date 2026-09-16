package bridge

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// turnCoalescer owns queued input coalescing and per-root turn activity.
type turnCoalescer struct {
	turnMu        sync.Mutex
	turnActive    map[string]bool
	coalesceMu    sync.Mutex
	coalesced     map[string]*coalescedInput
	coalesceDelay time.Duration
}

// defaultCoalesceDelay is the burst-merge window: messages arriving while a
// turn is executing are buffered for this long and submitted as ONE preempt,
// so a burst interrupts the running turn only once.
const defaultCoalesceDelay = 2 * time.Second

type coalescedInput struct {
	chat                 *LarkChat
	inputs               []string
	turnInputs           []turnInputRef
	interruptDone        chan error
	notifyFailure        func(context.Context, error) error
	localExpertAuthoring bool
}

// setTurnActive records whether the session bound to rootMsgID is currently
// executing a turn. Marked on submit and on observed Lark input echo; cleared
// on turn_done and when the attach stream ends.
func (c *turnCoalescer) setTurnActive(rootMsgID string, active bool) {
	c.turnMu.Lock()
	defer c.turnMu.Unlock()
	if c.turnActive == nil {
		c.turnActive = make(map[string]bool)
	}
	if active {
		c.turnActive[rootMsgID] = true
	} else {
		delete(c.turnActive, rootMsgID)
	}
}

func (c *turnCoalescer) isTurnActive(rootMsgID string) bool {
	c.turnMu.Lock()
	defer c.turnMu.Unlock()
	return c.turnActive[rootMsgID]
}

func (c *turnCoalescer) shouldCoalesceInput(rootMsgID string) bool {
	if c.isTurnActive(rootMsgID) {
		return true
	}
	c.coalesceMu.Lock()
	defer c.coalesceMu.Unlock()
	return c.coalesced[rootMsgID] != nil
}

// enqueueCoalescedInput buffers an input for rootMsgID and arms the flush
// timer on the first buffered input of a window.
func (b *Bridge) enqueueCoalescedInput(chat *LarkChat, input string) {
	b.enqueueCoalescedInputForMessage(chat, input, "", "", nil)
}

func (b *Bridge) enqueueCoalescedInputWithFailure(chat *LarkChat, input string, notifyFailure func(context.Context, error) error) {
	b.enqueueCoalescedInputForMessage(chat, input, "", "", notifyFailure)
}

func (b *Bridge) enqueueCoalescedInputForMessage(chat *LarkChat, input, messageID, openID string, notifyFailure func(context.Context, error) error) {
	b.coalesceMu.Lock()
	if b.coalesced == nil {
		b.coalesced = make(map[string]*coalescedInput)
	}
	if pending, ok := b.coalesced[chat.RootMsgID]; ok {
		pending.inputs = append(pending.inputs, input)
		pending.turnInputs = append(pending.turnInputs, compactTurnInputRefs(openID, messageID)...)
		pending.localExpertAuthoring = pending.localExpertAuthoring && chat.localExpertAuthoring
		if pending.notifyFailure == nil {
			pending.notifyFailure = notifyFailure
		}
		b.coalesceMu.Unlock()
		return
	}
	pending := &coalescedInput{
		chat:                 chat,
		inputs:               []string{input},
		turnInputs:           compactTurnInputRefs(openID, messageID),
		interruptDone:        make(chan error, 1),
		notifyFailure:        notifyFailure,
		localExpertAuthoring: chat.localExpertAuthoring,
	}
	b.coalesced[chat.RootMsgID] = pending
	delay := b.coalesceDelay
	if delay <= 0 {
		delay = defaultCoalesceDelay
	}
	rootMsgID := chat.RootMsgID
	b.coalesceMu.Unlock()

	// Mark the old turn superseded synchronously so attach processing rejects
	// late deltas immediately. Card finalization and the interrupt RPC can both
	// perform network I/O, so keep them off the message-routing lock.
	if freeze := b.markTurnSuperseded(rootMsgID); freeze != nil {
		go freeze()
	}
	time.AfterFunc(delay, func() { b.flushCoalescedInput(rootMsgID) })
	go func() {
		interruptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := b.hub.Route(interruptCtx, chat.TargetID, "Interrupt", &pb.InterruptRequest{Name: chat.SessionName})
		if err != nil {
			log.Warn().Err(err).Str("session", chat.SessionName).Msg("bridge: immediate coalesced turn interrupt failed")
		}
		pending.interruptDone <- err
	}()
}

func (b *Bridge) flushCoalescedInput(rootMsgID string) {
	b.coalesceMu.Lock()
	pending := b.coalesced[rootMsgID]
	delete(b.coalesced, rootMsgID)
	b.coalesceMu.Unlock()
	if pending == nil || len(pending.inputs) == 0 {
		return
	}
	interruptErr := <-pending.interruptDone
	pending.chat.localExpertAuthoring = pending.localExpertAuthoring
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := b.submitToSessionWithOptions(ctx, pending.chat, strings.Join(pending.inputs, "\n"), true, interruptErr == nil, pending.turnInputs); err != nil {
		log.Warn().Err(err).Str("session", pending.chat.SessionName).Msg("bridge: flush coalesced input failed")
		if pending.notifyFailure != nil {
			notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
			notifyErr := pending.notifyFailure(notifyCtx, err)
			notifyCancel()
			if notifyErr != nil {
				log.Warn().Err(notifyErr).Str("session", pending.chat.SessionName).
					Msg("bridge: notify coalesced input failure")
			}
		}
	}
}

type turnInputRef struct {
	MessageID string
	OpenID    string
}

func compactTurnInputRefs(openID string, ids ...string) []turnInputRef {
	out := make([]turnInputRef, 0, len(ids))
	openID = strings.TrimSpace(openID)
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, turnInputRef{MessageID: id, OpenID: openID})
		}
	}
	return out
}
