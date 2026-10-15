package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestSubtaskNoticeRequeuedAfterSessionClosed(t *testing.T) {
	s := &Session{ID: "session-1", TimelineEpoch: "epoch-1", Replay: replayBufferForTest()}
	var mu sync.Mutex
	var firstTexts, secondTexts []string
	first := &correlatedRecordingProc{}
	first.dispatch = func(_ context.Context, req TurnDispatchRequest, _ func(agentstream.ProducerEmission) bool) error {
		mu.Lock()
		firstTexts = append(firstTexts, req.Input.Text())
		mu.Unlock()
		return nil
	}
	s.SetProc(first)
	if _, err := s.SubmitInputAsyncWithAuthorization(SubtaskNoticeSource, TextUserInput("[后台任务完成] task_id=t1"), TurnAuthorization{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(firstTexts) == 1 })
	if err := s.Kill(); err != nil {
		t.Fatal(err)
	}
	// Parked until a process is installed again.
	waitFor(t, func() bool { s.queueMu.Lock(); defer s.queueMu.Unlock(); return len(s.parkedNotices) == 1 })
	second := &correlatedRecordingProc{}
	second.dispatch = func(_ context.Context, req TurnDispatchRequest, _ func(agentstream.ProducerEmission) bool) error {
		mu.Lock()
		secondTexts = append(secondTexts, req.Input.Text())
		mu.Unlock()
		return nil
	}
	s.SetProc(second)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(secondTexts) == 1 })
	if secondTexts[0] != "[后台任务完成] task_id=t1" {
		t.Fatalf("requeued text = %q", secondTexts[0])
	}
}

func TestShouldRequeueSubtaskNoticeBounds(t *testing.T) {
	item := &queuedInput{QueueItem: QueueItem{Source: SubtaskNoticeSource}}
	closed := &agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "session_closed"}
	if !shouldRequeueSubtaskNotice(item, nil, closed) {
		t.Fatal("session_closed notice not requeued")
	}
	if shouldRequeueSubtaskNotice(item, nil, &agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}) {
		t.Fatal("processed notice requeued")
	}
	if shouldRequeueSubtaskNotice(item, nil, &agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "user_interrupt"}) {
		t.Fatal("user interrupt requeued")
	}
	if !shouldRequeueSubtaskNotice(item, ErrProcessUnavailable, nil) || shouldRequeueSubtaskNotice(item, errors.New("x"), nil) {
		t.Fatal("dispatch error classification")
	}
	user := &queuedInput{QueueItem: QueueItem{Source: "lark"}}
	if shouldRequeueSubtaskNotice(user, nil, closed) {
		t.Fatal("user input requeued")
	}
	item.requeues = maxSubtaskNoticeRequeues
	if shouldRequeueSubtaskNotice(item, nil, closed) {
		t.Fatal("unbounded requeue")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
