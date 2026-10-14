package agentstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultMailboxSize = 256

type Timeline struct {
	mu sync.Mutex

	sessionID   string
	epoch       string
	mailboxSize int
	now         func() time.Time
	newTurnID   func() string
	journal     Journal
	eventSeq    uint64
	turnSeq     uint64
	history     []Event
	fault       error
	active      *turnState
	usedTurnIDs map[string]struct{}
	subscribers map[*Subscription]struct{}
	metadata    *SessionMetadataChanged
	commands    *CommandsReplaced
	goal        *GoalChanged
}

type turnState struct {
	ref              TurnRef
	started          Event
	finished         *Event
	lastTransportSeq uint64
	waiters          []chan Completion
	failure          error
}

type Turn struct {
	timeline *Timeline
	state    *turnState
}

type Subscription struct {
	timeline *Timeline
	events   chan Event
	done     chan struct{}
	mu       sync.Mutex
	history  []Event
	follow   bool
	terminal error
	cursor   bool
	nextSeq  uint64
	err      error
	closed   bool
}

func New(cfg Config) *Timeline {
	return newTimeline(cfg)
}

// Open restores a Timeline from its journal. Callers that configure a Journal
// must use Open rather than New so sequence and turn identity continue from
// durable state.
func Open(ctx context.Context, cfg Config) (*Timeline, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeline := newTimeline(cfg)
	if cfg.Journal == nil {
		return timeline, nil
	}
	events, err := cfg.Journal.LoadAgentEvents(ctx, cfg.SessionID)
	if err != nil {
		return nil, err
	}
	if err := timeline.restore(events, cfg.Epoch); err != nil {
		return nil, err
	}
	return timeline, nil
}

func newTimeline(cfg Config) *Timeline {
	epoch := cfg.Epoch
	if epoch == "" {
		epoch = uuid.NewString()
	}
	mailboxSize := cfg.MailboxSize
	if mailboxSize <= 0 {
		mailboxSize = defaultMailboxSize
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	newTurnID := cfg.NewTurnID
	if newTurnID == nil {
		newTurnID = uuid.NewString
	}
	return &Timeline{
		sessionID:   cfg.SessionID,
		epoch:       epoch,
		mailboxSize: mailboxSize,
		now:         now,
		newTurnID:   newTurnID,
		journal:     cfg.Journal,
		usedTurnIDs: make(map[string]struct{}),
		subscribers: make(map[*Subscription]struct{}),
	}
}

func (t *Timeline) restore(events []Event, configuredEpoch string) error {
	if len(events) == 0 {
		return nil
	}
	epoch := events[0].Cursor.Epoch
	if epoch == "" || (configuredEpoch != "" && configuredEpoch != epoch) {
		return fmt.Errorf("%w: epoch mismatch", ErrCorruptJournal)
	}
	var active *turnState
	for i, event := range events {
		wantSeq := uint64(i + 1)
		if event.SessionID != t.sessionID || event.Cursor.Epoch != epoch || event.Cursor.Seq != wantSeq || event.Body == nil {
			return fmt.Errorf("%w: invalid event at position %d", ErrCorruptJournal, i)
		}
		if _, err := validatedBody(event.Body, event.Turn != nil); err != nil {
			return fmt.Errorf("%w: invalid body at position %d", ErrCorruptJournal, i)
		}
		switch body := event.Body.(type) {
		case TurnStarted:
			if event.Turn == nil || event.Turn.ID == "" || event.Turn.Seq != t.turnSeq+1 || active != nil {
				return fmt.Errorf("%w: invalid turn start at position %d", ErrCorruptJournal, i)
			}
			if _, reused := t.usedTurnIDs[event.Turn.ID]; reused {
				return fmt.Errorf("%w: reused turn id at position %d", ErrCorruptJournal, i)
			}
			t.usedTurnIDs[event.Turn.ID] = struct{}{}
			t.turnSeq = event.Turn.Seq
			active = &turnState{
				ref:              *event.Turn,
				started:          cloneEvent(event),
				lastTransportSeq: event.Origin.TransportSeq,
			}
		case TurnFinished:
			if active == nil || event.Turn == nil || *event.Turn != active.ref {
				return fmt.Errorf("%w: unmatched turn finish at position %d", ErrCorruptJournal, i)
			}
			finished := cloneEvent(event)
			active.finished = &finished
			active = nil
		case SessionMetadataChanged:
			copy := body
			t.metadata = &copy
		case CommandsReplaced:
			copy := body.cloneBody().(CommandsReplaced)
			t.commands = &copy
		case GoalChanged:
			copy := body.cloneBody().(GoalChanged)
			t.goal = &copy
		default:
			if event.Turn != nil && (active == nil || *event.Turn != active.ref) {
				return fmt.Errorf("%w: event outside active turn at position %d", ErrCorruptJournal, i)
			}
		}
		if active != nil && event.Turn != nil && *event.Turn == active.ref && event.Origin.TransportSeq > active.lastTransportSeq {
			active.lastTransportSeq = event.Origin.TransportSeq
		}
		t.history = append(t.history, cloneEvent(event))
	}
	t.epoch = epoch
	t.eventSeq = uint64(len(events))
	t.active = active
	return nil
}

func (t *Timeline) StartTurn(
	ctx context.Context,
	req StartRequest,
	dispatch func(context.Context, *Turn) error,
) (*Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	t.mu.Lock()
	if t.active != nil {
		t.mu.Unlock()
		return nil, ErrTurnActive
	}
	turnID := req.TurnID
	if turnID == "" {
		turnID = t.newTurnID()
	}
	if turnID == "" {
		t.mu.Unlock()
		return nil, ErrInvalidPayload
	}
	if _, exists := t.usedTurnIDs[turnID]; exists {
		t.mu.Unlock()
		return nil, ErrDuplicateTurnID
	}
	nextTurnSeq := t.turnSeq + 1
	state := &turnState{ref: TurnRef{ID: turnID, Seq: nextTurnSeq}}
	turn := &Turn{timeline: t, state: state}
	started, err := t.acceptLocked(ctx, &state.ref, req.Origin, TurnStarted{InputSummary: req.InputSummary})
	if err != nil {
		t.mu.Unlock()
		return nil, err
	}
	state.started = started
	t.usedTurnIDs[turnID] = struct{}{}
	t.turnSeq = nextTurnSeq
	t.active = state
	t.mu.Unlock()

	if dispatch == nil {
		dispatch = func(context.Context, *Turn) error { return ErrInvalidPayload }
	}
	if err := dispatch(ctx, turn); err != nil {
		_, recordErr := turn.Finish(context.Background(), Terminal{
			Outcome: OutcomeError,
			Code:    "submit_failed",
			Message: err.Error(),
		}, Origin{
			Actor:      ActorSystem,
			Channel:    req.Origin.Channel,
			Provider:   req.Origin.Provider,
			NativeType: "dispatch_error",
			LaunchID:   req.Origin.LaunchID,
		})
		return turn, &DispatchError{Turn: state.ref, Err: err, RecordErr: recordErr}
	}
	return turn, nil
}

func (t *Turn) Ref() TurnRef { return t.state.ref }

// LastTransportSeq returns the greatest broker sequence durably represented
// by this Turn. A recovered runtime uses it as the exclusive replay cursor.
func (t *Turn) LastTransportSeq() uint64 {
	t.timeline.mu.Lock()
	defer t.timeline.mu.Unlock()
	return t.state.lastTransportSeq
}

// ActiveTurn returns the exact recovered/live turn capability without
// dispatching any provider action. Callers may finish or rebind it explicitly.
func (t *Timeline) ActiveTurn() *Turn {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		return nil
	}
	return &Turn{timeline: t, state: t.active}
}

// StartedEvent returns the immutable event that was durably accepted before
// provider dispatch began.
func (t *Turn) StartedEvent() Event {
	t.timeline.mu.Lock()
	defer t.timeline.mu.Unlock()
	return cloneEvent(t.state.started)
}

func (t *Turn) Emit(ctx context.Context, body TurnPayload, origin Origin) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	normalized, err := normalizeTurnPayload(body)
	if err != nil {
		return Event{}, err
	}
	t.timeline.mu.Lock()
	defer t.timeline.mu.Unlock()
	if t.state.finished != nil {
		return Event{}, ErrTurnFinished
	}
	if t.state.failure != nil {
		return Event{}, t.state.failure
	}
	if t.timeline.active != t.state {
		return Event{}, ErrNoActiveTurn
	}
	event, err := t.timeline.acceptLocked(ctx, &t.state.ref, origin, normalized)
	if err == nil && origin.TransportSeq > t.state.lastTransportSeq {
		t.state.lastTransportSeq = origin.TransportSeq
	}
	return event, err
}

func (t *Turn) Finish(ctx context.Context, terminal Terminal, origin Origin) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if !validOutcome(terminal.Outcome) {
		return Event{}, ErrInvalidPayload
	}
	t.timeline.mu.Lock()
	defer t.timeline.mu.Unlock()
	if t.state.finished != nil {
		finished := *t.state.finished
		body := finished.Body.(TurnFinished)
		if body.Terminal == terminal {
			return cloneEvent(finished), nil
		}
		return Event{}, ErrTerminalConflict
	}
	if t.state.failure != nil {
		return Event{}, t.state.failure
	}
	if t.timeline.active != t.state {
		return Event{}, ErrNoActiveTurn
	}
	event, err := t.timeline.acceptLocked(ctx, &t.state.ref, origin, TurnFinished{Terminal: terminal})
	if err != nil {
		return Event{}, err
	}
	if origin.TransportSeq > t.state.lastTransportSeq {
		t.state.lastTransportSeq = origin.TransportSeq
	}
	stored := cloneEvent(event)
	t.state.finished = &stored
	t.timeline.active = nil
	completion := Completion{Turn: t.state.ref, Terminal: terminal, Event: cloneEvent(event)}
	for _, waiter := range t.state.waiters {
		waiter <- cloneCompletion(completion)
		close(waiter)
	}
	t.state.waiters = nil
	return event, nil
}

func (t *Turn) Done() <-chan Completion {
	waiter := make(chan Completion, 1)
	t.timeline.mu.Lock()
	defer t.timeline.mu.Unlock()
	if t.state.finished != nil {
		finished := cloneEvent(*t.state.finished)
		body := finished.Body.(TurnFinished)
		waiter <- Completion{Turn: t.state.ref, Terminal: body.Terminal, Event: finished}
		close(waiter)
		return waiter
	}
	if t.state.failure != nil {
		waiter <- Completion{Turn: t.state.ref, Err: t.state.failure}
		close(waiter)
		return waiter
	}
	t.state.waiters = append(t.state.waiters, waiter)
	return waiter
}

func (t *Timeline) Publish(ctx context.Context, body SessionPayload, origin Origin) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	normalized, err := normalizeSessionPayload(body)
	if err != nil {
		return Event{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	event, err := t.acceptLocked(ctx, nil, origin, normalized)
	if err != nil {
		return Event{}, err
	}
	switch value := event.Body.(type) {
	case SessionMetadataChanged:
		copy := value
		t.metadata = &copy
	case CommandsReplaced:
		copy := value.cloneBody().(CommandsReplaced)
		t.commands = &copy
	case GoalChanged:
		copy := value.cloneBody().(GoalChanged)
		t.goal = &copy
	}
	return event, nil
}

func (t *Timeline) Subscribe(ctx context.Context) (Head, *Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Head{}, nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fault != nil {
		return t.headLocked(), nil, t.fault
	}
	sub := &Subscription{
		timeline: t,
		events:   make(chan Event, t.mailboxSize),
		done:     make(chan struct{}),
		follow:   true,
	}
	t.subscribers[sub] = struct{}{}
	return t.headLocked(), sub, nil
}

// Read captures history and, for Follow/Tail, installs the live subscriber
// under the same Timeline lock. This is the single no-gap read seam.
func (t *Timeline) Read(ctx context.Context, req ReadRequest) (Head, *Subscription, error) {
	if err := ctx.Err(); err != nil {
		return Head{}, nil, err
	}
	if req.Mode != ReadHistory && req.Mode != ReadFollow && req.Mode != ReadTail {
		return Head{}, nil, ErrInvalidPayload
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if req.Mode != ReadTail {
		if err := t.validateCursorLocked(req.After); err != nil {
			return Head{}, nil, err
		}
	}
	if t.fault != nil && req.Mode != ReadHistory {
		return t.headLocked(), nil, t.fault
	}
	mailboxSize := t.mailboxSize
	if req.Mode != ReadHistory {
		mailboxSize = 1 // cursor readers use this channel only as a coalesced wakeup
	}
	sub := &Subscription{
		timeline: t,
		events:   make(chan Event, mailboxSize),
		done:     make(chan struct{}),
		follow:   req.Mode != ReadHistory,
		terminal: t.fault,
		cursor:   req.Mode != ReadHistory,
	}
	if req.Mode == ReadHistory {
		start := int(req.After.Seq)
		sub.history = make([]Event, 0, len(t.history)-start)
		for _, event := range t.history[start:] {
			sub.history = append(sub.history, cloneEvent(event))
		}
	} else if req.Mode == ReadFollow {
		sub.nextSeq = req.After.Seq + 1
	} else {
		sub.nextSeq = t.eventSeq + 1
	}
	if sub.follow {
		t.subscribers[sub] = struct{}{}
	}
	return t.headLocked(), sub, nil
}

func (t *Timeline) validateCursorLocked(cursor Cursor) error {
	if cursor == (Cursor{}) {
		return nil
	}
	if cursor.Epoch == "" || cursor.Epoch != t.epoch || cursor.Seq > t.eventSeq {
		return ErrInvalidCursor
	}
	return nil
}

func (t *Timeline) headLocked() Head {
	head := Head{Cursor: Cursor{Epoch: t.epoch, Seq: t.eventSeq}}
	if t.active != nil {
		started := cloneEvent(t.active.started)
		head.ActiveTurnStart = &started
	}
	if t.metadata != nil {
		copy := *t.metadata
		head.Metadata = &copy
	}
	if t.commands != nil {
		copy := t.commands.cloneBody().(CommandsReplaced)
		head.Commands = &copy
	}
	if t.goal != nil {
		copy := t.goal.cloneBody().(GoalChanged)
		head.Goal = &copy
	}
	return head
}

// Head returns the current durable high-watermark and sticky session state.
func (t *Timeline) Head() Head {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.headLocked()
}

func (s *Subscription) Recv(ctx context.Context) (Event, error) {
	if s.cursor {
		return s.recvCursor(ctx)
	}
	s.mu.Lock()
	if s.closed {
		err := s.err
		s.mu.Unlock()
		return Event{}, err
	}
	if len(s.history) > 0 {
		event := cloneEvent(s.history[0])
		s.history = s.history[1:]
		s.mu.Unlock()
		return event, nil
	}
	if !s.follow {
		terminal := s.terminal
		s.mu.Unlock()
		if terminal != nil {
			return Event{}, terminal
		}
		return Event{}, io.EOF
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return Event{}, ctx.Err()
	case <-s.done:
		return Event{}, s.terminalError()
	case event := <-s.events:
		if err := s.terminalError(); err != nil {
			return Event{}, err
		}
		return cloneEvent(event), nil
	}
}

func (s *Subscription) recvCursor(ctx context.Context) (Event, error) {
	for {
		s.timeline.mu.Lock()
		s.mu.Lock()
		if s.closed {
			err := s.err
			s.mu.Unlock()
			s.timeline.mu.Unlock()
			return Event{}, err
		}
		if s.nextSeq > 0 && s.nextSeq <= s.timeline.eventSeq {
			event := cloneEvent(s.timeline.history[s.nextSeq-1])
			s.nextSeq++
			s.mu.Unlock()
			s.timeline.mu.Unlock()
			return event, nil
		}
		s.mu.Unlock()
		s.timeline.mu.Unlock()

		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-s.done:
			return Event{}, s.terminalError()
		case <-s.events:
		}
	}
}

func (s *Subscription) Close() {
	s.timeline.mu.Lock()
	defer s.timeline.mu.Unlock()
	delete(s.timeline.subscribers, s)
	s.fail(ErrClosed)
}

func (s *Subscription) terminalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		return nil
	}
	return s.err
}

func (s *Subscription) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.err = err
	close(s.done)
}

func (t *Timeline) acceptLocked(ctx context.Context, turn *TurnRef, origin Origin, body Body) (Event, error) {
	if t.fault != nil {
		return Event{}, t.fault
	}
	event := Event{
		SessionID: t.sessionID,
		Cursor:    Cursor{Epoch: t.epoch, Seq: t.eventSeq + 1},
		At:        t.now(),
		Origin:    origin,
		Body:      body.cloneBody(),
	}
	durable := cloneEvent(event)
	durable.Body = durableBody(durable.Body)
	if turn != nil {
		ref := *turn
		event.Turn = &ref
		durable.Turn = &ref
	}
	if t.journal != nil {
		if err := t.journal.AppendAgentEvent(ctx, cloneEvent(durable)); err != nil {
			fault := errors.Join(ErrJournalFault, err)
			t.faultLocked(fault)
			return Event{}, fault
		}
	}
	t.eventSeq++
	t.history = append(t.history, cloneEvent(durable))
	for sub := range t.subscribers {
		if sub.cursor {
			select {
			case sub.events <- Event{}:
			default:
			}
			continue
		}
		select {
		case sub.events <- cloneEvent(event):
		default:
			delete(t.subscribers, sub)
			sub.fail(ErrLagged)
		}
	}
	return cloneEvent(event), nil
}

func (t *Timeline) faultLocked(fault error) {
	if t.fault != nil {
		return
	}
	t.fault = fault
	if t.active != nil {
		state := t.active
		state.failure = fault
		completion := Completion{Turn: state.ref, Err: fault}
		for _, waiter := range state.waiters {
			waiter <- completion
			close(waiter)
		}
		state.waiters = nil
		t.active = nil
	}
	for sub := range t.subscribers {
		delete(t.subscribers, sub)
		sub.fail(fault)
	}
}

func cloneEvent(event Event) Event {
	out := event
	if event.Turn != nil {
		turn := *event.Turn
		out.Turn = &turn
	}
	if event.Body != nil {
		out.Body = event.Body.cloneBody()
	}
	return out
}

// CloneEvent returns a defensive copy for persistence and transport adapters.
func CloneEvent(event Event) Event { return cloneEvent(event) }

func cloneCompletion(completion Completion) Completion {
	completion.Event = cloneEvent(completion.Event)
	return completion
}

func validOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeSuccess, OutcomeError, OutcomeCancelled, OutcomeInterrupted:
		return true
	default:
		return false
	}
}
