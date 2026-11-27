package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type fakeSubmitHub struct {
	mu            sync.Mutex
	submits       []*pb.SubmitRequest
	interrupts    int
	interruptGate chan struct{}
	interruptErr  error
}

func (h *fakeSubmitHub) ListTargets() []*pb.Target { return nil }

func (h *fakeSubmitHub) Route(ctx context.Context, _, method string, payload interface{}) (*pb.TunnelMsg, error) {
	switch method {
	case "Submit":
		h.mu.Lock()
		h.submits = append(h.submits, payload.(*pb.SubmitRequest))
		h.mu.Unlock()
		raw, _ := json.Marshal(&pb.SubmitResponse{Ok: true, TurnId: "turn-1", SessionId: "session-1", LaunchId: "launch-1"})
		return &pb.TunnelMsg{Payload: raw}, nil
	case "Interrupt":
		h.mu.Lock()
		h.interrupts++
		gate := h.interruptGate
		err := h.interruptErr
		h.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return &pb.TunnelMsg{}, nil
}

func TestCoalescedInputPersistsEveryMessageToOneTurn(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatalf("open bridge db: %v", err)
	}
	defer db.Close()
	hub := &fakeSubmitHub{}
	b := &Bridge{
		hub:           hub,
		db:            db,
		turnCoalescer: turnCoalescer{coalesceDelay: 10 * time.Millisecond},
	}
	chat := &LarkChat{BridgeId: "feishu:test", RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	b.setTurnActive("root1", true)
	b.enqueueCoalescedInputForMessage(context.Background(), chat, "first", "message-1", "ou_first", nil)
	b.enqueueCoalescedInputForMessage(context.Background(), chat, "second", "message-2", "ou_second", nil)

	waitForBridgeTest(t, time.Second, func() bool {
		links, lookupErr := LookupTurnLinks(path, TurnLinkQuery{TurnID: "turn-1"})
		return lookupErr == nil && len(links) == 2
	})
	links, err := LookupTurnLinks(path, TurnLinkQuery{TurnID: "turn-1"})
	if err != nil {
		t.Fatalf("lookup links: %v", err)
	}
	if links[0].SessionID != "session-1" || links[0].LaunchID != "launch-1" || links[0].TurnID != "turn-1" ||
		links[0].MessageID != "message-1" || links[0].OpenID != "ou_first" ||
		links[1].MessageID != "message-2" || links[1].OpenID != "ou_second" {
		t.Fatalf("turn links = %+v", links)
	}
}

func TestCoalescedInputWaitsForInterruptBeforeSubmitting(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	hub := &fakeSubmitHub{interruptGate: gate}
	b := &Bridge{
		hub:           hub,
		turnCoalescer: turnCoalescer{coalesceDelay: 10 * time.Millisecond},
	}
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	b.setTurnActive("root1", true)

	b.enqueueCoalescedInput(chat, "next")
	time.Sleep(40 * time.Millisecond)
	if got := hub.snapshot(); len(got) != 0 {
		t.Fatalf("submits before interrupt completes = %+v, want none", got)
	}

	close(gate)
	waitForBridgeTest(t, time.Second, func() bool { return len(hub.snapshot()) == 1 })
	if got := hub.snapshot()[0]; !got.PreemptAlreadyInterrupted {
		t.Fatalf("submit after successful interrupt = %+v, want completed-interrupt marker", got)
	}
}

func TestCoalescedInputRetriesPreemptWhenImmediateInterruptFails(t *testing.T) {
	t.Parallel()
	hub := &fakeSubmitHub{interruptErr: context.DeadlineExceeded}
	b := &Bridge{
		hub:           hub,
		turnCoalescer: turnCoalescer{coalesceDelay: 10 * time.Millisecond},
	}
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	b.setTurnActive("root1", true)

	b.enqueueCoalescedInput(chat, "next")
	waitForBridgeTest(t, time.Second, func() bool { return len(hub.snapshot()) == 1 })
	if got := hub.snapshot()[0]; got.PreemptAlreadyInterrupted {
		t.Fatalf("submit after failed interrupt = %+v, must let runtime retry preempt", got)
	}
}

func waitForBridgeTest(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !ok() {
		t.Fatal("condition not met before timeout")
	}
}

func (h *fakeSubmitHub) RouteStream(context.Context, string, string, interface{}, func(*pb.TunnelMsg) error) error {
	return nil
}

func (h *fakeSubmitHub) snapshot() []*pb.SubmitRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*pb.SubmitRequest, len(h.submits))
	copy(out, h.submits)
	return out
}

func (h *fakeSubmitHub) interruptCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.interrupts
}

func TestSubmitToSessionPreemptsAndMarksTurnActive(t *testing.T) {
	t.Parallel()
	hub := &fakeSubmitHub{}
	b := &Bridge{hub: hub, turnCoalescer: turnCoalescer{}}
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}

	if err := b.submitToSession(context.Background(), chat, "hello"); err != nil {
		t.Fatalf("submitToSession: %v", err)
	}
	subs := hub.snapshot()
	if len(subs) != 1 || !subs[0].Preempt || subs[0].PreemptAlreadyInterrupted || !subs[0].Async || subs[0].Text != "hello" {
		t.Fatalf("submits = %+v, want one preempt+async hello", subs)
	}
	if !b.isTurnActive("root1") {
		t.Fatal("turn should be active after submit")
	}
	b.setTurnActive("root1", false)
	if b.isTurnActive("root1") {
		t.Fatal("turn should be inactive after clear")
	}
}

func TestSubmitCarriesLocalExpertGrantOnlyForStrictOwnerInput(t *testing.T) {
	t.Parallel()
	hub := &fakeSubmitHub{}
	b := &Bridge{hub: hub, secrets: &Secrets{OpenId: "owner"}}
	owner := b.botSessionChat(&BotSession{TargetID: "t1", SessionName: "s1"}, botSessionInput{OpenID: "owner"})
	if err := b.submitToSession(context.Background(), owner, "edit"); err != nil {
		t.Fatal(err)
	}
	member := b.botSessionChat(&BotSession{TargetID: "t1", SessionName: "s1"}, botSessionInput{OpenID: "member"})
	if err := b.submitToSession(context.Background(), member, "edit"); err != nil {
		t.Fatal(err)
	}
	submits := hub.snapshot()
	if len(submits) != 2 || !submits[0].LocalExpertAuthoring || submits[1].LocalExpertAuthoring {
		t.Fatalf("authoring grants = %+v, want owner=true member=false", submits)
	}
}

func TestCoalescedMixedSenderInputDropsLocalExpertGrant(t *testing.T) {
	t.Parallel()
	hub := &fakeSubmitHub{}
	b := &Bridge{
		hub:           hub,
		turnCoalescer: turnCoalescer{coalesceDelay: 10 * time.Millisecond},
	}
	owner := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1", localExpertAuthoring: true}
	member := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1", localExpertAuthoring: false}
	b.setTurnActive("root1", true)
	b.enqueueCoalescedInput(owner, "owner input")
	b.enqueueCoalescedInput(member, "member input")
	waitForBridgeTest(t, time.Second, func() bool { return len(hub.snapshot()) == 1 })
	if got := hub.snapshot()[0]; got.LocalExpertAuthoring {
		t.Fatalf("mixed-sender submit retained owner grant: %+v", got)
	}
}

type failingSubmitHub struct{ fakeSubmitHub }

func (h *failingSubmitHub) Route(_ context.Context, _, _ string, _ interface{}) (*pb.TunnelMsg, error) {
	return nil, context.DeadlineExceeded
}

func TestDirectSubmitFailureClearsTurnActive(t *testing.T) {
	t.Parallel()
	hub := &failingSubmitHub{}
	b := &Bridge{hub: hub}
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}

	if err := b.submitToSession(context.Background(), chat, "hello"); err == nil {
		t.Fatal("submitToSession should fail")
	}
	if b.isTurnActive("root1") {
		t.Fatal("turn must not be active after failed submit")
	}
}

func TestCoalescedSubmitFailureCallsNotifier(t *testing.T) {
	t.Parallel()
	hub := &failingSubmitHub{}
	b := &Bridge{
		hub:           hub,
		turnCoalescer: turnCoalescer{coalesceDelay: 10 * time.Millisecond},
	}
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	notified := make(chan error, 1)
	b.enqueueCoalescedInputWithFailure(chat, "hello", func(_ context.Context, err error) error {
		notified <- err
		return nil
	})

	select {
	case err := <-notified:
		if err == nil {
			t.Fatal("notifier received nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced submit failure notice")
	}
}

func TestCoalescedInputsFlushAsSinglePreempt(t *testing.T) {
	t.Parallel()
	hub := &fakeSubmitHub{}
	b := &Bridge{
		hub:           hub,
		turnCoalescer: turnCoalescer{coalesceDelay: 100 * time.Millisecond},
	}
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	b.setTurnActive("root1", true)

	b.enqueueCoalescedInput(chat, "msg1")
	deadline := time.Now().Add(time.Second)
	for hub.interruptCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := hub.interruptCount(); got != 1 {
		t.Fatalf("interrupts after first input = %d, want 1 without waiting for coalesce flush", got)
	}
	if subs := hub.snapshot(); len(subs) != 0 {
		t.Fatalf("submits before coalesce flush = %+v, want none", subs)
	}
	b.enqueueCoalescedInput(chat, "msg2")
	b.enqueueCoalescedInput(chat, "msg3")
	if got := hub.interruptCount(); got != 1 {
		t.Fatalf("interrupts after burst = %d, want exactly 1", got)
	}

	flushDeadline := time.After(2 * time.Second)
	for {
		if subs := hub.snapshot(); len(subs) == 1 {
			if subs[0].Text != "msg1\nmsg2\nmsg3" || !subs[0].Preempt || !subs[0].PreemptAlreadyInterrupted {
				t.Fatalf("submit = %+v, want joined preempt", subs[0])
			}
			// Confirm no second submit fires after a brief pause
			time.Sleep(50 * time.Millisecond)
			if subs2 := hub.snapshot(); len(subs2) != 1 {
				t.Fatalf("submits = %+v after pause, want exactly 1 flush", subs2)
			}
			return
		}
		select {
		case <-flushDeadline:
			t.Fatalf("submits = %+v, want exactly 1 flush", hub.snapshot())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestPendingCoalesceWindowRemainsActiveAfterOldTurnFinishes(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	b.coalesced = map[string]*coalescedInput{"root1": {}}

	if !b.shouldCoalesceInput("root1") {
		t.Fatal("pending coalesce window should accept later burst input even after old turn is inactive")
	}
}

// ownTurn records that the session runs turnID for input sent by openID, as
// the submit receipt and the attach stream report it.
func ownTurn(b *Bridge, rootMsgID, turnID, openID string) {
	b.recordTurnSenders(rootMsgID, turnID, compactTurnInputRefs(openID, "om-start"))
	b.observeTurnStarted(rootMsgID, turnID)
}

func TestBusyInputSteersByDefaultWithoutInterrupt(t *testing.T) {
	t.Parallel()
	hub := &fakeSubmitHub{}
	b := &Bridge{
		hub:           hub,
		turnCoalescer: turnCoalescer{coalesceDelay: 30 * time.Millisecond},
	}
	b.steerForeground.Store(true)
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	b.setTurnActive("root1", true)
	ownTurn(b, "root1", "turn-1", "ou-a")

	b.enqueueCoalescedInputForMessage(context.Background(), chat, "改成用 Python", "om-1", "ou-a", nil)
	b.enqueueCoalescedInputForMessage(context.Background(), chat, "并且加上测试", "om-2", "ou-a", nil)
	waitForBridgeTest(t, time.Second, func() bool { return len(hub.snapshot()) == 1 })
	time.Sleep(20 * time.Millisecond)
	if n := hub.interruptCount(); n != 0 {
		t.Fatalf("interrupts = %d, steer mode must not interrupt the running turn", n)
	}
	subs := hub.snapshot()
	if len(subs) != 1 {
		t.Fatalf("submits = %d, want one merged steer", len(subs))
	}
	got := subs[0]
	if !got.Steer || got.SteerExpectedTurnId != "turn-1" || got.Preempt || got.PreemptAlreadyInterrupted || !got.Async {
		t.Fatalf("submit = %+v, want async steer pinned to turn-1 without preempt", got)
	}
	if got.Text != "改成用 Python\n并且加上测试" {
		t.Fatalf("text = %q, want merged burst (runtime adds the steer note)", got.Text)
	}
}

func TestBusyInputFromAnotherSenderQueuesInsteadOfSteering(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		inputs []turnInputRef
	}{
		{"another member", compactTurnInputRefs("ou-b", "om-1")},
		{"mixed burst", append(compactTurnInputRefs("ou-a", "om-1"), compactTurnInputRefs("ou-b", "om-2")...)},
		{"no sender (group watch / Bot)", compactTurnInputRefs("", "om-1")},
		{"no message", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := &fakeSubmitHub{}
			b := &Bridge{hub: hub, turnCoalescer: turnCoalescer{coalesceDelay: 10 * time.Millisecond}}
			b.steerForeground.Store(true)
			chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
			b.setTurnActive("root1", true)
			ownTurn(b, "root1", "turn-1", "ou-a")
			if _, err := b.submitToSessionForMessages(context.Background(), chat, "hello", tc.inputs); err != nil {
				t.Fatalf("submit: %v", err)
			}
			got := hub.snapshot()[0]
			if got.Steer || got.SteerExpectedTurnId != "" || got.Preempt || !got.Async {
				t.Fatalf("submit = %+v, want an ordinary queued submit", got)
			}
			if hub.interruptCount() != 0 {
				t.Fatalf("interrupts = %d, want none", hub.interruptCount())
			}
		})
	}
}

func TestSteerTargetFollowsTheRunningTurn(t *testing.T) {
	t.Parallel()
	var c turnCoalescer
	own := compactTurnInputRefs("ou-a", "om-1")
	if _, ok := c.steerTarget("root1", own); ok {
		t.Fatal("no running turn is known: nothing to steer")
	}
	c.recordTurnSenders("root1", "turn-1", own)
	c.observeTurnStarted("root1", "turn-1")
	if turnID, ok := c.steerTarget("root1", own); !ok || turnID != "turn-1" {
		t.Fatalf("steerTarget = %q, %v; want turn-1", turnID, ok)
	}
	// Queued input from someone else becomes the next turn; it is not theirs
	// to steer into turn-1 and turn-2 is not ou-a's.
	c.recordTurnSenders("root1", "turn-2", compactTurnInputRefs("ou-b", "om-2"))
	c.observeTurnFinished("root1", "turn-1")
	if _, ok := c.steerTarget("root1", own); ok {
		t.Fatal("finished turn must not be steered")
	}
	c.observeTurnStarted("root1", "turn-2")
	if _, ok := c.steerTarget("root1", own); ok {
		t.Fatal("ou-a must not steer ou-b's turn")
	}
	if turnID, ok := c.steerTarget("root1", compactTurnInputRefs("ou-b", "om-3")); !ok || turnID != "turn-2" {
		t.Fatalf("steerTarget = %q, %v; want turn-2 for its own sender", turnID, ok)
	}
	c.observeTurnFinished("root1", "turn-2")
	if _, ok := c.steerTarget("root1", compactTurnInputRefs("ou-b", "om-3")); ok {
		t.Fatal("finished turn must be forgotten")
	}
}

func TestSteerTargetBeforeTurnStartedUsesSubmitReceipt(t *testing.T) {
	t.Parallel()
	var c turnCoalescer
	own := compactTurnInputRefs("ou-a", "om-1")
	// Rapid second message: submitted, turn_started not yet seen.
	c.recordTurnSenders("root1", "turn-1", own)
	if turnID, ok := c.steerTarget("root1", compactTurnInputRefs("ou-a", "om-2")); !ok || turnID != "turn-1" {
		t.Fatalf("steerTarget = %q, %v; want turn-1 from the submit receipt", turnID, ok)
	}
	if _, ok := c.steerTarget("root1", compactTurnInputRefs("ou-b", "om-3")); ok {
		t.Fatal("another sender must not steer before turn_started either")
	}
	if _, ok := c.steerTarget("root1", compactTurnInputRefs("", "om-4")); ok {
		t.Fatal("input without a sender must not steer")
	}
}

func TestSteerTargetSurvivesStreamReconnect(t *testing.T) {
	t.Parallel()
	var c turnCoalescer
	own := compactTurnInputRefs("ou-a", "om-1")
	c.recordTurnSenders("root1", "turn-0", compactTurnInputRefs("ou-b", "om-0"))
	c.recordTurnSenders("root1", "turn-1", own)
	c.observeTurnStarted("root1", "turn-1")
	c.forgetTurns("root1") // stream ended; reconnect has not reported the turn
	if turnID, ok := c.steerTarget("root1", own); !ok || turnID != "turn-1" {
		t.Fatalf("steerTarget = %q, %v; want turn-1 after reconnect", turnID, ok)
	}
	if _, ok := c.steerTarget("root1", compactTurnInputRefs("ou-b", "om-2")); ok {
		t.Fatal("reconnect must not let another sender steer")
	}
	if len(c.turnSenders["root1"]) != 1 {
		t.Fatalf("forgetTurns kept %d turns, want only the newest", len(c.turnSenders["root1"]))
	}
}

func TestSteerTargetWithoutAnyRecordQueues(t *testing.T) {
	t.Parallel()
	var c turnCoalescer
	// After a bridge restart nothing is known about the running turn: queue.
	if _, ok := c.steerTarget("root1", compactTurnInputRefs("ou-a", "om-1")); ok {
		t.Fatal("unknown turn ownership must queue")
	}
	if mode := c.busyInputMode("root1", compactTurnInputRefs("ou-a", "om-1")); mode.steer {
		t.Fatal("busyInputMode must queue without a known turn")
	}
}

func TestBusyInputEnvRestoresInterrupt(t *testing.T) {
	t.Setenv(busyInputEnv, "interrupt")
	hub := &fakeSubmitHub{}
	b := &Bridge{
		hub:           hub,
		turnCoalescer: turnCoalescer{coalesceDelay: 10 * time.Millisecond},
	}
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	b.setTurnActive("root1", true)
	b.enqueueCoalescedInput(chat, "stop that")
	waitForBridgeTest(t, time.Second, func() bool { return len(hub.snapshot()) == 1 })
	if got := hub.snapshot()[0]; !got.Preempt || got.Steer {
		t.Fatalf("submit = %+v, want legacy preempt", got)
	}
	if hub.interruptCount() != 1 {
		t.Fatalf("interrupts = %d, want 1", hub.interruptCount())
	}
}

func TestIdleSubmitQueuesWithoutPreempt(t *testing.T) {
	t.Parallel()
	hub := &fakeSubmitHub{}
	b := &Bridge{hub: hub}
	b.steerForeground.Store(true)
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	if err := b.submitToSession(context.Background(), chat, "hello"); err != nil {
		t.Fatalf("submitToSession: %v", err)
	}
	subs := hub.snapshot()
	if len(subs) != 1 || subs[0].Steer || subs[0].Preempt || !subs[0].Async {
		t.Fatalf("submits = %+v, want an ordinary queued submit (never preempt)", subs)
	}
}

func TestIdleSubmitSteersAnUnexpectedlyRunningOwnTurn(t *testing.T) {
	t.Parallel()
	hub := &fakeSubmitHub{}
	b := &Bridge{hub: hub}
	b.steerForeground.Store(true)
	chat := &LarkChat{RootMsgID: "root1", TargetID: "t1", SessionName: "s1"}
	ownTurn(b, "root1", "turn-1", "ou-a")
	if _, err := b.submitToSessionForMessages(context.Background(), chat, "hello", compactTurnInputRefs("ou-a", "om-1")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := hub.snapshot()[0]; !got.Steer || got.SteerExpectedTurnId != "turn-1" || got.Preempt {
		t.Fatalf("submit = %+v, want steer pinned to turn-1", got)
	}
}

func TestSteerOutcomeUnknownGetsDuplicateWarning(t *testing.T) {
	t.Parallel()
	err := errors.New("route Submit: rpc error: code = Unknown desc = steer outcome unknown; the input was not queued again to avoid running it twice: closed pipe")
	if !isSteerOutcomeUnknown(err) {
		t.Fatal("routed outcome-unknown error not recognized")
	}
	if got := botSessionSubmitFailureText(err); got != steerOutcomeUnknownNotice {
		t.Fatalf("bot session notice = %q", got)
	}
	if isSteerOutcomeUnknown(errors.New("session busy")) {
		t.Fatal("ordinary failure misclassified")
	}
}
