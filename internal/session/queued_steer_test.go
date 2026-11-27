package session

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSteerQueuedInputPreservesOrderAndAuthorization(t *testing.T) {
	ctx := context.Background()
	s := &Session{Name: "queue-promotion", Replay: replayBufferForTest()}
	proc := &steeringProc{}
	s.SetProc(proc)
	first, err := s.SubmitInputAsync("lark", TextUserInput("active owner turn"))
	if err != nil || first.Waiting {
		t.Fatalf("initial submit: %+v, %v", first, err)
	}
	active := waitActiveTurn(t, s)
	head, err := s.SubmitInputAsync("lark", TextUserInput("original queued message"))
	if err != nil || !head.Waiting {
		t.Fatalf("queued submit: %+v, %v", head, err)
	}
	tail, err := s.SubmitInputAsync("lark", TextUserInput("later queued message"))
	if err != nil || !tail.Waiting {
		t.Fatalf("tail submit: %+v, %v", tail, err)
	}
	for _, attempt := range []struct {
		queued, target string
		auth           TurnAuthorization
	}{
		{tail.TurnID, active, TurnAuthorization{}},
		{head.TurnID, "replaced-turn", TurnAuthorization{}},
		{head.TurnID, active, TurnAuthorization{LocalExpertAuthoring: true}},
	} {
		_, err := s.SteerQueuedInput(ctx, attempt.queued, attempt.target, attempt.auth)
		if !errors.Is(err, ErrSteerUnavailable) {
			t.Fatalf("invalid promotion err = %v", err)
		}
	}
	if items := s.QueueSnapshot(); len(items) != 2 || items[0].TurnID != head.TurnID || items[1].TurnID != tail.TurnID {
		t.Fatalf("queue order changed after rejected promotions: %+v", items)
	}
	proc.steerErr = ErrSteerUnavailable
	if _, err := s.SteerQueuedInput(ctx, head.TurnID, active, TurnAuthorization{}); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("provider rejection = %v", err)
	}
	if items := s.QueueSnapshot(); len(items) != 2 || items[0].TurnID != head.TurnID {
		t.Fatalf("queue changed on definite rejection: %+v", items)
	}
	proc.steerErr = nil
	got, err := s.SteerQueuedInput(ctx, head.TurnID, active, TurnAuthorization{})
	if err != nil || got != active {
		t.Fatalf("promotion delivered to %q, error %v", got, err)
	}
	if got := proc.steerSnapshot(); len(got) != 1 || !strings.HasSuffix(got[0].Input.Text(), "original queued message") {
		t.Fatalf("provider received %+v", got)
	}
	if _, err := s.SteerQueuedInput(ctx, head.TurnID, active, TurnAuthorization{}); !errors.Is(err, ErrQueuedSteerMissing) {
		t.Fatalf("duplicate promotion = %v", err)
	}
	if items := s.QueueSnapshot(); len(items) != 1 || items[0].TurnID != tail.TurnID {
		t.Fatalf("queue after promotion: %+v", items)
	}
	s.ClearQueue()
	proc.finishTurn()
}

func TestSteerQueuedInputConsumesUncertainDelivery(t *testing.T) {
	s := &Session{Name: "queue-promotion", Replay: replayBufferForTest()}
	proc := &steeringProc{steerErr: ErrSteerOutcomeUnknown}
	s.SetProc(proc)
	if err := s.SubmitAsync("lark", "initial"); err != nil {
		t.Fatal(err)
	}
	active := waitActiveTurn(t, s)
	item, err := s.SubmitInputAsync("lark", TextUserInput("queued"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SteerQueuedInput(context.Background(), item.TurnID, active, TurnAuthorization{}); !errors.Is(err, ErrSteerOutcomeUnknown) {
		t.Fatalf("promotion err = %v", err)
	}
	if items := s.QueueSnapshot(); len(items) != 0 {
		t.Fatalf("uncertain input is still queued: %+v", items)
	}
	proc.finishTurn()
}
