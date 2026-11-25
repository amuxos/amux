package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/sourceframe"
)

// AgentTimeline returns the Session-owned durable canonical event stream.
func (s *Session) AgentTimeline() *agentstream.Timeline {
	s.timelineMu.Lock()
	defer s.timelineMu.Unlock()
	if s.timeline == nil {
		s.timeline = agentstream.New(agentstream.Config{SessionID: s.ID, Epoch: s.TimelineEpoch})
	}
	return s.timeline
}

// RebindRecoveredTurn reconnects a durable active Turn to a surviving broker.
// It installs only the producer capability; provider input is never replayed.
func (s *Session) RebindRecoveredTurn() error {
	active := s.AgentTimeline().ActiveTurn()
	if active == nil {
		return nil
	}
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()

	s.queueMu.Lock()
	s.turnMu.Lock()
	if s.canonicalTurn != nil {
		s.turnMu.Unlock()
		s.queueMu.Unlock()
		return ErrTurnSinkActive
	}
	s.canonicalTurn = active
	s.canonicalAuth = TurnAuthorization{}
	s.turnMu.Unlock()
	s.queueBusy = true
	s.queueGoal = active.StartedEvent().Origin.Channel == sourceframe.SourceGoal
	s.queueIdle = make(chan struct{})
	s.queueMu.Unlock()

	done := make(chan struct{})
	s.waitForCanonicalCompletion(active, done)
	go func() {
		<-done
		s.advanceInputQueue()
		s.resumeGoalAfterInput()
	}()

	rebinder, ok := proc.(CorrelatedTurnRebinder)
	if !ok {
		cause := errors.New("surviving provider cannot rebind the recovered turn")
		return errors.Join(cause, s.finishRecoveredTurn(active, "runtime_rebind_unsupported", cause.Error()))
	}
	if err := rebinder.RebindTurn(active.Ref().ID, active.LastTransportSeq(), func(emission agentstream.ProducerEmission) bool {
		return s.acceptProducerEmission(active, emission)
	}); err != nil {
		finishErr := s.finishRecoveredTurn(active, "runtime_rebind_failed", err.Error())
		return errors.Join(err, finishErr)
	}
	s.markTurnDispatched(active)
	if ready, ok := proc.(GoalTurnReadyProvider); ok {
		ready.EnableGoalTurns()
	}
	return nil
}

// FinishRecoveredTurn records why an already-dispatched durable Turn cannot
// continue. It never invokes a provider or replays the original input.
func (s *Session) FinishRecoveredTurn(code, message string) error {
	active := s.AgentTimeline().ActiveTurn()
	if active == nil {
		return nil
	}
	return s.finishRecoveredTurn(active, code, message)
}

func (s *Session) finishRecoveredTurn(turn *agentstream.Turn, code, message string) error {
	_, err := turn.Finish(context.Background(), agentstream.Terminal{
		Outcome: agentstream.OutcomeInterrupted,
		Code:    code,
		Message: message,
	}, agentstream.Origin{
		Actor:      agentstream.ActorSystem,
		Channel:    sourceframe.SourceSystem,
		NativeType: code,
		LaunchID:   s.currentLaunchID(),
	})
	return err
}

func (s *Session) correlatedDispatcher() (Proc, bool) {
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	return proc, proc != nil
}

func (s *Session) submitCorrelatedTurn(
	dispatcher Proc,
	source, displayText, control string,
	input UserInput,
	turnID string,
	authorization TurnAuthorization,
	expectedAgentSessionID string,
) (<-chan struct{}, error) {
	if source == "" {
		source = sourceframe.SourceCLI
	}
	timeline := s.AgentTimeline()
	done := make(chan struct{})
	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{
		TurnID:       turnID,
		InputSummary: displayText,
		Origin: agentstream.Origin{
			Actor:    agentstream.ActorUser,
			Channel:  source,
			LaunchID: s.currentLaunchID(),
		},
	}, func(ctx context.Context, turn *agentstream.Turn) error {
		// Install completion handling before dispatch: a producer may finish the
		// turn before the dispatch acknowledgement returns.
		s.waitForCanonicalCompletion(turn, done)

		s.turnMu.Lock()
		s.canonicalTurn = turn
		s.canonicalAuth = authorization
		s.turnMu.Unlock()
		s.observeCanonicalEvent(turn.StartedEvent())

		if expectedAgentSessionID != "" && s.Snapshot().AgentSessionID != expectedAgentSessionID {
			return errors.New("native conversation changed before queued input dispatch")
		}

		dispatchedInput := input
		handoff := ""
		if control == "" {
			var prepareErr error
			dispatchedInput, handoff, prepareErr = s.prepareCorrelatedInputLocked(input)
			if prepareErr != nil {
				return prepareErr
			}
		}
		req := TurnDispatchRequest{TurnToken: turn.Ref().ID, Input: dispatchedInput, Control: control}
		dispatchErr := dispatcher.DispatchTurn(ctx, req, func(emission agentstream.ProducerEmission) bool {
			return s.acceptProducerEmission(turn, emission)
		})
		if dispatchErr != nil && handoff != "" {
			s.restoreHandoffContext(handoff)
		}
		if dispatchErr == nil {
			s.markTurnDispatched(turn)
		}
		return dispatchErr
	})
	if turn == nil {
		return nil, err
	}
	return done, err
}

func (s *Session) markTurnDispatched(turn *agentstream.Turn) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if s.canonicalTurn == turn {
		s.canonicalDispatched = true
	}
}

func (s *Session) waitForCanonicalCompletion(turn *agentstream.Turn, done chan struct{}) {
	go func() {
		completion := <-turn.Done()
		if completion.Err == nil {
			s.recordTurnTerminal(completion.Turn.ID, completion.Terminal)
			s.observeCanonicalEvent(completion.Event)
		} else {
			s.handleTimelineError(completion.Err)
		}
		s.completeCanonicalTurn(completion.Turn)
		if done != nil {
			close(done)
		}
	}()
}

func (s *Session) prepareCorrelatedInputLocked(input UserInput) (UserInput, string, error) {
	handoff, err := s.consumeHandoffContext()
	if err != nil {
		return UserInput{}, "", fmt.Errorf("consume conversation handoff: %w", err)
	}
	if handoff != "" {
		input.Parts = append([]InputPart{{Text: handoff}}, input.Parts...)
	}
	return input, handoff, nil
}

func (s *Session) currentLaunchID() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.LaunchID
}

// acceptProducerEmission is the only turn-scoped producer boundary. It does
// not translate a compatibility event: the producer already supplied a typed
// canonical payload or terminal, while the Timeline supplies identity/order.
func (s *Session) acceptProducerEmission(turn *agentstream.Turn, emission agentstream.ProducerEmission) bool {
	origin := s.enrichProducerOrigin(emission.Origin)
	if emission.Terminal != nil {
		_, err := turn.Finish(context.Background(), *emission.Terminal, origin)
		s.handleTimelineError(err)
		return false
	}
	if emission.Turn == nil || emission.Session != nil {
		return false
	}
	accepted, err := turn.Emit(context.Background(), emission.Turn, origin)
	if err != nil {
		s.handleTimelineError(err)
		return false
	}
	return s.observeCanonicalEvent(accepted)
}

func (s *Session) enrichProducerOrigin(origin agentstream.Origin) agentstream.Origin {
	if origin.Actor == "" {
		origin.Actor = agentstream.ActorAgent
	}
	if origin.Channel == "" {
		origin.Channel = "agent"
	}
	origin.LaunchID = s.currentLaunchID()
	return origin
}

// recordProducerEmission accepts only session-scoped producer observations.
// A turn payload without an opaque turn capability is never rebound by time.
func (s *Session) recordProducerEmission(emission agentstream.ProducerEmission) bool {
	if emission.Session == nil || emission.Turn != nil || emission.Terminal != nil {
		return false
	}
	accepted, err := s.AgentTimeline().Publish(context.Background(), emission.Session, s.enrichProducerOrigin(emission.Origin))
	if err != nil {
		s.handleTimelineError(err)
		return false
	}
	if changed, ok := emission.Session.(agentstream.GoalChanged); ok && changed.Goal == nil && emission.Origin.NativeType == "thread/goal/cleared" {
		if err := s.setGoalGrant(nil); err != nil {
			s.handleTimelineError(err)
		}
	}
	if changed, ok := emission.Session.(agentstream.GoalChanged); ok && emission.Origin.NativeType == "thread/goal/updated" {
		if err := s.observeGoalCompletion(changed.Goal); err != nil {
			s.handleTimelineError(err)
		}
	}
	return s.observeCanonicalEvent(accepted)
}

// observeCanonicalEvent applies bounded Session policy directly from the
// accepted canonical event. It is not another event representation or live
// stream: the exact accepted value is synchronously projected to replay,
// activity, persistence and permission policy.
func (s *Session) observeCanonicalEvent(event agentstream.Event) bool {
	s.projectCanonicalReplay(event)
	s.noteCanonicalActivity(event, time.Now())

	s.tapMu.Lock()
	tap := s.tap
	s.tapMu.Unlock()
	if tap != nil {
		return tap(event)
	}
	return false
}

func (s *Session) projectCanonicalReplay(event agentstream.Event) {
	var data []byte
	switch body := event.Body.(type) {
	case agentstream.TurnStarted:
		if body.InputSummary != "" {
			data = sourceframe.FormatInput(event.Origin.Channel, body.InputSummary)
		}
	case agentstream.AssistantDelta:
		data = []byte(body.Text)
	case agentstream.AssistantMessage:
		data = []byte(body.Text)
	case agentstream.Diagnostic:
		if body.Severity == agentstream.SeverityError {
			data = []byte("\r\n[system] " + body.Message + "\r\n")
		}
	}
	if len(data) > 0 {
		s.AppendReplay(data)
	}
}

func (s *Session) noteCanonicalActivity(event agentstream.Event, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	s.LastActivityAt = now

	interaction := false
	switch body := event.Body.(type) {
	case agentstream.TurnStarted,
		agentstream.AssistantDelta,
		agentstream.AssistantMessage,
		agentstream.ThinkingDelta,
		agentstream.PlanReplaced,
		agentstream.ToolChanged,
		agentstream.PermissionRequested,
		agentstream.PermissionResolved,
		agentstream.UsageObserved,
		agentstream.ResultObserved:
		interaction = true
	case agentstream.Diagnostic:
		interaction = event.Origin.Actor != agentstream.ActorSystem
	case agentstream.TurnFinished:
		interaction = body.Terminal.Outcome != agentstream.OutcomeError
	}
	if interaction {
		s.LastInteractionAt = now
	}
	if requested, ok := event.Body.(agentstream.PermissionRequested); ok && requested.Request.ID != "" && !requested.Request.IsAsyncUserInput() {
		if s.pendingPermissions == nil {
			s.pendingPermissions = make(map[string]struct{})
		}
		s.pendingPermissions[requested.Request.ID] = struct{}{}
	}
	if resolved, ok := event.Body.(agentstream.PermissionResolved); ok {
		delete(s.pendingPermissions, resolved.RequestID)
	}
}

func (s *Session) completeCanonicalTurn(ref agentstream.TurnRef) {
	s.turnMu.Lock()
	if s.canonicalTurn == nil || s.canonicalTurn.Ref() != ref {
		s.turnMu.Unlock()
		return
	}
	s.canonicalTurn = nil
	s.canonicalAuth = TurnAuthorization{}
	s.canonicalDispatched = false
	s.turnMu.Unlock()
	s.activityMu.Lock()
	s.pendingPermissions = nil
	s.activityMu.Unlock()
}

func (s *Session) failCanonicalTurn(stopReason string) bool {
	s.turnMu.Lock()
	turn := s.canonicalTurn
	s.turnMu.Unlock()
	if turn == nil {
		return false
	}
	_, err := turn.Finish(context.Background(), agentstream.Terminal{
		Outcome: agentstream.OutcomeError,
		Code:    "provider_stream_closed",
		Message: stopReason,
	}, agentstream.Origin{
		Actor:      agentstream.ActorSystem,
		Channel:    sourceframe.SourceSystem,
		NativeType: "runtime_failure",
		LaunchID:   s.currentLaunchID(),
	})
	if err == nil {
		return true
	}
	return errors.Is(err, agentstream.ErrTerminalConflict) || errors.Is(err, agentstream.ErrTurnFinished) || errors.Is(err, agentstream.ErrNoActiveTurn)
}

func (s *Session) handleTimelineError(err error) {
	if !errors.Is(err, agentstream.ErrJournalFault) {
		return
	}
	s.timelineFaultOnce.Do(func() {
		go func() { _ = s.Kill() }()
	})
}
