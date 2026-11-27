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

// defaultCoalesceDelay is the initial burst-merge window: messages arriving
// while a turn is executing are buffered briefly and submitted as ONE preempt
// or steer. When additional messages arrive within the window, the timer slides
// up to defaultCoalesceMaxDelay so multi-message bursts still merge cleanly
// without penalizing single follow-ups with a multi-second wait.
const (
	defaultCoalesceDelay    = 400 * time.Millisecond
	defaultCoalesceMaxDelay = 2 * time.Second
)

type coalescedInput struct {
	batches       []coalescedBatch
	interruptDone chan error
	steer         bool
	timer         *time.Timer
	windowDelay   time.Duration
	maxDeadline   time.Time
}

type coalescedBatch struct {
	source        feishuSource
	chat          LarkChat
	inputs        []string
	turnInputs    []turnInputRef
	sender        string
	targetTurnID  string
	notifyFailure func(context.Context, error) error
}

func (p *coalescedInput) appendBatch(batch coalescedBatch) {
	last := &p.batches[len(p.batches)-1]
	if p.steer && (batch.sender == "" || last.sender != batch.sender || last.source != batch.source ||
		last.targetTurnID != batch.targetTurnID || last.chat.localExpertAuthoring != batch.chat.localExpertAuthoring) {
		p.batches = append(p.batches, batch)
		return
	}
	last.inputs = append(last.inputs, batch.inputs...)
	last.turnInputs = append(last.turnInputs, batch.turnInputs...)
	last.chat.localExpertAuthoring = last.chat.localExpertAuthoring && batch.chat.localExpertAuthoring
	if last.notifyFailure == nil {
		last.notifyFailure = batch.notifyFailure
	}
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
	b.enqueueCoalescedInputForMessage(context.Background(), chat, input, "", "", nil)
}

func (b *Bridge) enqueueCoalescedInputWithFailure(chat *LarkChat, input string, notifyFailure func(context.Context, error) error) {
	b.enqueueCoalescedInputForMessage(context.Background(), chat, input, "", "", notifyFailure)
}

func (b *Bridge) enqueueCoalescedInputForMessage(ctx context.Context, chat *LarkChat, input, messageID, openID string, notifyFailure func(context.Context, error) error) {
	batch := coalescedBatch{
		source: feishuSourceForInput(ctx, chat), chat: *chat,
		inputs: []string{input}, turnInputs: compactTurnInputRefs(openID, messageID),
		targetTurnID: b.currentSteerCandidate(chat.RootMsgID), notifyFailure: notifyFailure,
	}
	if len(batch.turnInputs) != 0 {
		batch.sender = batch.turnInputs[0].OpenID
	}
	b.coalesceMu.Lock()
	if b.coalesced == nil {
		b.coalesced = make(map[string]*coalescedInput)
	}
	if pending, ok := b.coalesced[chat.RootMsgID]; ok {
		pending.appendBatch(batch)
		if pending.timer != nil && !pending.maxDeadline.IsZero() {
			if remaining := time.Until(pending.maxDeadline); remaining > 0 {
				extend := pending.windowDelay
				if extend > remaining {
					extend = remaining
				}
				pending.timer.Reset(extend)
			}
		}
		b.coalesceMu.Unlock()
		return
	}
	delay := b.coalesceDelay
	maxDelay := delay
	if delay <= 0 {
		delay = defaultCoalesceDelay
		maxDelay = defaultCoalesceMaxDelay
	}
	rootMsgID := chat.RootMsgID
	pending := &coalescedInput{
		batches:       []coalescedBatch{batch},
		interruptDone: make(chan error, 1),
		steer:         b.busyInputSteers(),
		windowDelay:   delay,
	}
	if maxDelay > delay {
		pending.maxDeadline = time.Now().Add(maxDelay)
	}
	pending.timer = time.AfterFunc(delay, func() { b.flushCoalescedInput(rootMsgID, pending) })
	b.coalesced[rootMsgID] = pending
	b.coalesceMu.Unlock()

	if pending.steer {
		// Steer mode keeps the running turn (and its card) alive: the burst is
		// injected into it after the merge window, or queued when the turn
		// cannot accept it. Explicit 中断/停止 commands still interrupt.
		pending.interruptDone <- nil
		return
	}

	// Mark the old turn superseded synchronously so attach processing rejects
	// late deltas immediately. Card finalization and the interrupt RPC can both
	// perform network I/O, so keep them off the message-routing lock.
	if freeze := b.markTurnSuperseded(rootMsgID); freeze != nil {
		go freeze()
	}
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

func (b *Bridge) flushCoalescedInput(rootMsgID string, pending *coalescedInput) {
	b.coalesceMu.Lock()
	if b.coalesced[rootMsgID] != pending {
		b.coalesceMu.Unlock()
		return
	}
	delete(b.coalesced, rootMsgID)
	b.coalesceMu.Unlock()
	interruptErr := <-pending.interruptDone
	for _, batch := range pending.batches {
		b.flushCoalescedBatch(batch, pending.steer, interruptErr)
	}
}

func (b *Bridge) flushCoalescedBatch(batch coalescedBatch, steer bool, interruptErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = withFeishuSource(ctx, batch.source)
	var err error
	text := strings.Join(batch.inputs, "\n")
	if steer {
		mode := b.busyInputMode(batch.chat.RootMsgID, batch.turnInputs)
		if mode.steerExpectedTurnID != batch.targetTurnID {
			mode = submitMode{}
		}
		mode.promotionTurnID = batch.targetTurnID
		mode.hasPromotionPin = true
		var receipt submitReceipt
		receipt, err = b.submitToSessionMode(ctx, &batch.chat, text, mode, batch.turnInputs)
		if err == nil {
			log.Info().Str("session", batch.chat.SessionName).Str("turn_id", receipt.TurnID).
				Str("delivery", receipt.Delivery).Bool("waiting", receipt.Waiting).Str("fallback", receipt.SteerFallbackReason).Msg("bridge: busy input submitted")
		}
	} else {
		_, err = b.submitToSessionWithOptions(ctx, &batch.chat, text, true, interruptErr == nil, batch.turnInputs)
	}
	if err != nil {
		log.Warn().Err(err).Str("session", batch.chat.SessionName).Msg("bridge: flush coalesced input failed")
		if isSteerOutcomeUnknown(err) && batch.notifyFailure == nil {
			// The running turn may already have this input; tell the user
			// so they do not send it again.
			notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
			notifyErr := b.notifyThread(notifyCtx, batch.chat.RootMsgID, steerOutcomeUnknownNotice)
			notifyCancel()
			if notifyErr != nil {
				log.Warn().Err(notifyErr).Str("session", batch.chat.SessionName).
					Msg("bridge: notify steer outcome unknown")
			}
		} else if batch.notifyFailure != nil {
			notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
			notifyErr := batch.notifyFailure(notifyCtx, err)
			notifyCancel()
			if notifyErr != nil {
				log.Warn().Err(notifyErr).Str("session", batch.chat.SessionName).
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
