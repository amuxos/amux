package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

type steeringProc struct {
	recordingProc
	steerMu  sync.Mutex
	steers   []TurnSteerRequest
	steerErr error
}

func (p *steeringProc) SteerTurn(_ context.Context, req TurnSteerRequest) error {
	p.steerMu.Lock()
	defer p.steerMu.Unlock()
	if p.steerErr != nil {
		return p.steerErr
	}
	p.steers = append(p.steers, req)
	return nil
}

func (p *steeringProc) steerSnapshot() []TurnSteerRequest {
	p.steerMu.Lock()
	defer p.steerMu.Unlock()
	return append([]TurnSteerRequest(nil), p.steers...)
}

func waitActiveTurn(t *testing.T, s *Session) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if id, _, ok := s.ActiveTurnAuthorization(); ok {
			return id
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("turn never became active")
	return ""
}

func TestSteerInputInjectsIntoActiveTurn(t *testing.T) {
	s := &Session{Name: "s", Replay: replayBufferForTest()}
	proc := &steeringProc{}
	s.SetProc(proc)
	if _, err := s.SteerInput(context.Background(), "lark", TextUserInput("x"), "", TurnAuthorization{}); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("idle steer err = %v, want ErrSteerUnavailable", err)
	}
	if err := s.SubmitAsync("lark", "first"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	turnID := waitActiveTurn(t, s)
	got, err := s.SteerInput(context.Background(), "lark", FrameSteerInput(SteerNoteSourceUser, TextUserInput("改成 Python")), turnID, TurnAuthorization{})
	if err != nil {
		t.Fatalf("SteerInput: %v", err)
	}
	if got != turnID {
		t.Fatalf("steered turn = %q, want %q", got, turnID)
	}
	steers := proc.steerSnapshot()
	if len(steers) != 1 || steers[0].TurnToken != turnID {
		t.Fatalf("steers = %+v", steers)
	}
	text := steers[0].Input.Text()
	if !strings.Contains(text, "改成 Python") || !strings.Contains(text, "执行当前任务的过程中") {
		t.Fatalf("steer text = %q, want framed user text", text)
	}
	if items := s.QueueSnapshot(); len(items) != 0 {
		t.Fatalf("steer must not enqueue: %+v", items)
	}
	if _, err := s.SteerInput(context.Background(), "lark", TextUserInput("x"), "other-turn", TurnAuthorization{}); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("mismatched turn err = %v", err)
	}
	proc.finishTurn()
}

func TestSteerInputRejectsAuthorizationMismatchAndInterruptedTurn(t *testing.T) {
	s := &Session{Name: "s", Replay: replayBufferForTest()}
	proc := &steeringProc{}
	s.SetProc(proc)
	if _, err := s.SubmitInputAsyncWithAuthorization("lark", TextUserInput("owner work"), TurnAuthorization{LocalExpertAuthoring: true}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitActiveTurn(t, s)
	if _, err := s.SteerInput(context.Background(), "lark", TextUserInput("non-owner"), "", TurnAuthorization{}); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("non-owner steer into owner turn err = %v, want unavailable", err)
	}
	if _, err := s.SteerInput(context.Background(), "lark", TextUserInput("owner"), "", TurnAuthorization{LocalExpertAuthoring: true}); err != nil {
		t.Fatalf("owner steer: %v", err)
	}
	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if _, err := s.SteerInput(context.Background(), "lark", TextUserInput("after interrupt"), "", TurnAuthorization{}); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("steer into interrupted turn err = %v, want unavailable", err)
	}
	if n := len(proc.steerSnapshot()); n != 1 {
		t.Fatalf("provider steers = %d, want 1", n)
	}
	proc.finishTurn()
}

func TestSteerInputProviderFailureIsUnavailable(t *testing.T) {
	s := &Session{Name: "s", Replay: replayBufferForTest()}
	proc := &steeringProc{steerErr: errors.New("no claude turn in flight")}
	s.SetProc(proc)
	if err := s.SubmitAsync("lark", "first"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	waitActiveTurn(t, s)
	if _, err := s.SteerInput(context.Background(), "lark", TextUserInput("x"), "", TurnAuthorization{}); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("err = %v, want ErrSteerUnavailable", err)
	}
	proc.finishTurn()
}

func TestSteerInputWithoutSteererIsUnavailable(t *testing.T) {
	s := &Session{Name: "s", Replay: replayBufferForTest()}
	proc := &recordingProc{}
	s.SetProc(proc)
	if err := s.SubmitAsync("lark", "first"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	waitActiveTurn(t, s)
	if _, err := s.SteerInput(context.Background(), "lark", TextUserInput("x"), "", TurnAuthorization{}); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("err = %v", err)
	}
	proc.finishTurn()
}

func TestFrameSteerInputKeepsOriginal(t *testing.T) {
	in := TextUserInput("hello")
	out := FrameSteerInput(SteerNoteSourceCoordinator, in)
	if in.Text() != "hello" {
		t.Fatalf("original mutated: %q", in.Text())
	}
	if !strings.HasSuffix(out.Text(), "hello") || !strings.Contains(out.Text(), "前台协调 Agent") {
		t.Fatalf("framed = %q", out.Text())
	}
}

func TestSteerInputRejectsWhileEarlierInputIsQueued(t *testing.T) {
	s := &Session{Name: "s", Replay: replayBufferForTest()}
	proc := &steeringProc{}
	s.SetProc(proc)
	if err := s.SubmitAsync("lark", "first"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	turnID := waitActiveTurn(t, s)
	// "second" waits behind the active turn; a later message must not overtake it.
	if err := s.SubmitAsync("lark", "second"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	if _, err := s.SteerInput(context.Background(), "lark", TextUserInput("third"), turnID, TurnAuthorization{}); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("err = %v, want ErrSteerUnavailable", err)
	}
	if n := len(proc.steerSnapshot()); n != 0 {
		t.Fatalf("provider steers = %d, want 0", n)
	}
	proc.finishTurn()
}

func TestSteerInputKeepsUnknownOutcomeDistinct(t *testing.T) {
	s := &Session{Name: "s", Replay: replayBufferForTest()}
	proc := &steeringProc{steerErr: SteerCallError(errors.New("context deadline exceeded"), false)}
	s.SetProc(proc)
	if err := s.SubmitAsync("lark", "first"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	waitActiveTurn(t, s)
	_, err := s.SteerInput(context.Background(), "lark", TextUserInput("x"), "", TurnAuthorization{})
	if !errors.Is(err, ErrSteerOutcomeUnknown) || errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("err = %v, want ErrSteerOutcomeUnknown only", err)
	}
	proc.finishTurn()
}

func TestSteerCallErrorClassifiesRejections(t *testing.T) {
	if err := SteerCallError(errors.New("no claude turn in flight"), true); !errors.Is(err, ErrSteerUnavailable) {
		t.Fatalf("rejection = %v, want ErrSteerUnavailable", err)
	}
	if err := SteerCallError(errors.New("io: read/write on closed pipe"), false); !errors.Is(err, ErrSteerOutcomeUnknown) {
		t.Fatalf("transport failure = %v, want ErrSteerOutcomeUnknown", err)
	}
}

func TestEventSinksTurnActiveForMatchesTheTurnToken(t *testing.T) {
	var sinks EventSinks
	if sinks.TurnActiveFor("turn-1") {
		t.Fatal("no turn installed")
	}
	if err := sinks.BeginTurnFor("turn-1", func(agentstream.ProducerEmission) bool { return true }); err != nil {
		t.Fatalf("BeginTurnFor: %v", err)
	}
	if !sinks.TurnActiveFor("turn-1") || sinks.TurnActiveFor("turn-2") || sinks.TurnActiveFor("") {
		t.Fatal("only turn-1 is active")
	}
	sinks.Emit(testSuccess())
	if sinks.TurnActiveFor("turn-1") {
		t.Fatal("terminal must end turn-1")
	}
	if err := sinks.BeginTurnFor("turn-2", func(agentstream.ProducerEmission) bool { return true }); err != nil {
		t.Fatalf("BeginTurnFor: %v", err)
	}
	sinks.AbortTurn()
	if sinks.TurnActiveFor("turn-2") {
		t.Fatal("aborted turn must not stay active")
	}
}
