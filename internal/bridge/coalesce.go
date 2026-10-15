package bridge

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	// steerForeground is the experimental switch
	// bridge.feishu.experimental.steer_foreground (default off). Off keeps the
	// legacy busy-input contract (a follow-up interrupts the running turn and
	// preempts it); on steers the follow-up into the running turn (falling
	// back to the queue) and enables the foreground coordination prompt.
	// It is reloadable: the experiments card flips it live (atomic).
	steerForeground atomic.Bool
	// steerMu guards the per-root view used to decide whether a follow-up may
	// be steered: the turn the attach stream reports as running, and who
	// sent the input of each submitted turn.
	steerMu       sync.Mutex
	activeTurnIDs map[string]string
	turnSenders   map[string]map[string][]string
	// latestTurnIDs is the newest turn a submit receipt reported per root,
	// the steer target while the attach stream has not (yet, or again)
	// reported the running turn.
	latestTurnIDs map[string]string
}

// busyInputEnv lets operators switch the busy-input contract without a
// rebuild: AMUX_FEISHU_BUSY_INPUT=interrupt forces interrupt+preempt even
// when the experimental switch is on. It never enables steering by itself.
const busyInputEnv = "AMUX_FEISHU_BUSY_INPUT"

func (c *turnCoalescer) busyInputSteers() bool {
	if !c.steerForeground.Load() {
		return false
	}
	return strings.TrimSpace(strings.ToLower(os.Getenv(busyInputEnv))) != "interrupt"
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
	steer                bool
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
		steer:                b.busyInputSteers(),
	}
	b.coalesced[chat.RootMsgID] = pending
	delay := b.coalesceDelay
	if delay <= 0 {
		delay = defaultCoalesceDelay
	}
	rootMsgID := chat.RootMsgID
	b.coalesceMu.Unlock()

	if pending.steer {
		// Steer mode keeps the running turn (and its card) alive: the burst is
		// injected into it after the merge window, or queued when the turn
		// cannot accept it. Explicit 中断/停止 commands still interrupt.
		pending.interruptDone <- nil
		time.AfterFunc(delay, func() { b.flushCoalescedInput(rootMsgID) })
		return
	}

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
		_, err := b.hub.Route(interruptCtx, chat.TargetID, "Interrupt", &pb.InterruptRequest{Name: chat.SessionName, PreserveGoal: true})
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
	var err error
	if pending.steer {
		var receipt submitReceipt
		receipt, err = b.submitToSessionMode(ctx, pending.chat, strings.Join(pending.inputs, "\n"), b.busyInputMode(pending.chat.RootMsgID, pending.turnInputs), pending.turnInputs)
		if err == nil {
			msg := "bridge: busy input steered"
			if receipt.Delivery != "steered" {
				msg = "bridge: busy input queued behind the running turn"
			}
			log.Info().Str("session", pending.chat.SessionName).Str("turn_id", receipt.TurnID).
				Str("delivery", receipt.Delivery).Str("fallback", receipt.SteerFallbackReason).Msg(msg)
		}
	} else {
		_, err = b.submitToSessionWithOptions(ctx, pending.chat, strings.Join(pending.inputs, "\n"), true, interruptErr == nil, pending.turnInputs)
	}
	if err != nil {
		log.Warn().Err(err).Str("session", pending.chat.SessionName).Msg("bridge: flush coalesced input failed")
		if isSteerOutcomeUnknown(err) && pending.notifyFailure == nil {
			// The running turn may already have this input; tell the user
			// so they do not send it again.
			notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
			notifyErr := b.notifyThread(notifyCtx, pending.chat.RootMsgID, steerOutcomeUnknownNotice)
			notifyCancel()
			if notifyErr != nil {
				log.Warn().Err(notifyErr).Str("session", pending.chat.SessionName).
					Msg("bridge: notify steer outcome unknown")
			}
		} else if pending.notifyFailure != nil {
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
