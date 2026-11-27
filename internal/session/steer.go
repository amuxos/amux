package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// ErrSteerUnavailable reports that input could not be injected into the
// active turn. Callers fall back to ordinary queued input.
var ErrSteerUnavailable = errors.New("steer unavailable")

// ErrQueuedSteerMissing reports that the requested input is no longer pending.
var ErrQueuedSteerMissing = errors.New("steer target is not pending")

// ErrQueuedSteerOrder reports that an earlier queue entry is still waiting.
var ErrQueuedSteerOrder = fmt.Errorf("%w: earlier input is queued", ErrSteerUnavailable)

// ErrSteerOutcomeUnknown reports that the provider may or may not have
// accepted the input (a timeout or broken transport after the request was
// sent). Callers must not queue the input again: it may already be running.
var ErrSteerOutcomeUnknown = errors.New("steer outcome unknown")

// SteerCallError classifies a failed steer request to a provider. A protocol
// rejection (rejected) is unavailable and safe to queue; anything else
// happened after the request was written and has an unknown outcome.
func SteerCallError(err error, rejected bool) error {
	if rejected {
		return fmt.Errorf("%w: %v", ErrSteerUnavailable, err)
	}
	return fmt.Errorf("%w: %v", ErrSteerOutcomeUnknown, err)
}

// TurnSteerRequest carries extra user input for the turn identified by
// TurnToken. Providers must reject the request when that turn is no longer the
// one executing, so a racing terminal cannot redirect input into a new turn.
type TurnSteerRequest struct {
	TurnToken string    `json:"turn_token"`
	Input     UserInput `json:"input"`
}

// TurnSteerer is implemented by providers (and broker proxies) that can
// inject input into a running turn without interrupting it.
type TurnSteerer interface {
	SteerTurn(context.Context, TurnSteerRequest) error
}

// SteerNoteSourceUser frames input sent by the human user while the agent was
// still executing the current turn.
const SteerNoteSourceUser = "user"

// SteerNoteSourceCoordinator frames a correction relayed by a foreground
// coordinating agent into a background agent instance.
const SteerNoteSourceCoordinator = "coordinator"

// FrameSteerText wraps text that is injected mid-turn with an explicit note,
// so the agent knows the message arrived while it was working and decides by
// itself whether it corrects the current work or appends a new task.
func FrameSteerText(source, text string) string {
	text = strings.TrimSpace(text)
	switch source {
	case SteerNoteSourceCoordinator:
		return "[amux 提示：以下是前台协调 Agent 在你执行当前任务过程中转达的用户纠正/补充（经 amux 注入当前回合）。" +
			"请判断它是在纠正/调整正在进行的工作，还是追加新的要求：若为纠正，请在保留已完成进度的基础上调整，不要从头重做；" +
			"若为追加，请在合适的时机一并完成。完成后在最终结果中说明你如何处理了这条消息。]\n\n" + text
	default:
		return "[amux 提示：以下消息是用户在你执行当前任务的过程中追加发送的（经 amux 注入当前回合，不是工具输出）。" +
			"请自行判断它是在纠正/调整正在进行的工作，还是追加一个新任务：若为纠正，请在保留已完成进度的基础上调整，不要从头重做；" +
			"若为追加，请在完成或合理安排当前工作后处理它；若只是询问进度，请简要回答后继续。" +
			"如果它针对的是某个后台 Agent Instance 正在执行的任务，请用 amux.instance_steer 转达给该实例。]\n\n" + text
	}
}

// FrameSteerInput prefixes the steer note to the first text part, leaving
// the original input untouched for a queue fallback.
func FrameSteerInput(source string, input UserInput) UserInput {
	out := UserInput{Parts: append([]InputPart(nil), input.Parts...)}
	for i := range out.Parts {
		if out.Parts[i].Image == nil {
			out.Parts[i].Text = FrameSteerText(source, out.Parts[i].Text)
			return out
		}
	}
	out.Parts = append([]InputPart{{Text: FrameSteerText(source, "")}}, out.Parts...)
	return out
}

// SteerInput injects input into the turn that is executing now. It never
// starts a new turn: when no compatible active turn exists it returns an error
// wrapping ErrSteerUnavailable and the caller decides how to fall back. An
// error wrapping ErrSteerOutcomeUnknown means the input may already be in the
// turn and must not be queued again.
//
// expectedTurnID, when set, pins the steer to that turn. The authorization of
// the steering input must equal the active turn's authorization: a non-owner
// message is never injected into an owner-privileged turn, and privileged
// owner input is never downgraded into an unprivileged turn.
func (s *Session) SteerInput(ctx context.Context, source string, input UserInput, expectedTurnID string, authorization TurnAuthorization) (string, error) {
	return s.steerInput(ctx, source, input, expectedTurnID, authorization, false)
}

// SteerQueuedInput promotes the original queue head. While it is reserved,
// even a concurrent turn completion cannot dispatch it as a separate turn.
func (s *Session) SteerQueuedInput(ctx context.Context, queuedTurnID, activeTurnID string, authorization TurnAuthorization) (string, error) {
	s.queueSteerMu.Lock()
	defer s.queueSteerMu.Unlock()

	s.queueMu.Lock()
	var item *queuedInput
	for _, pending := range s.queuePending {
		if pending.TurnID == queuedTurnID {
			item = pending
			break
		}
	}
	if item == nil {
		s.queueMu.Unlock()
		return "", ErrQueuedSteerMissing
	}
	head := s.queuePending[0] == item
	s.queueMu.Unlock()
	if !head {
		return "", ErrQueuedSteerOrder
	}
	if item.Source != "lark" || item.preempt || item.Control != "" || item.lifecycle != nil ||
		item.expectedAgentSessionID != "" || item.auth != authorization || activeTurnID == "" {
		return "", ErrSteerUnavailable
	}

	turnID, err := s.steerInput(ctx, item.Source, FrameSteerInput(SteerNoteSourceUser, item.input), activeTurnID, authorization, true)
	if err == nil || errors.Is(err, ErrSteerOutcomeUnknown) {
		s.cancelPendingItem(item.ID, err)
	}
	return turnID, err
}

func (s *Session) steerInput(ctx context.Context, source string, input UserInput, expectedTurnID string, authorization TurnAuthorization, queueHead bool) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(input.DisplayText()) == "" && len(input.Parts) == 0 {
		return "", fmt.Errorf("%w: empty input", ErrSteerUnavailable)
	}
	if input.ImageCount() > 0 {
		return "", fmt.Errorf("%w: image input cannot be steered", ErrSteerUnavailable)
	}
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	if proc == nil {
		return "", fmt.Errorf("%w: no agent process", ErrSteerUnavailable)
	}
	steerer, ok := proc.(TurnSteerer)
	if !ok {
		return "", fmt.Errorf("%w: provider does not support steering", ErrSteerUnavailable)
	}
	s.turnMu.Lock()
	var turnID string
	if s.canonicalTurn != nil {
		turnID = s.canonicalTurn.Ref().ID
	}
	activeAuth := s.canonicalAuth
	interrupted := turnID != "" && s.canonicalInterruptedTurn == turnID
	s.turnMu.Unlock()
	switch {
	case turnID == "":
		return "", fmt.Errorf("%w: no active turn", ErrSteerUnavailable)
	case expectedTurnID != "" && expectedTurnID != turnID:
		return "", fmt.Errorf("%w: active turn changed", ErrSteerUnavailable)
	case interrupted:
		return "", fmt.Errorf("%w: active turn is being interrupted", ErrSteerUnavailable)
	case activeAuth != authorization:
		return "", fmt.Errorf("%w: authorization differs from the active turn", ErrSteerUnavailable)
	}
	// Input that is already waiting runs first: steering past it would let a
	// later message overtake an earlier one (and a pending preempt replaces
	// the active turn anyway).
	s.queueMu.Lock()
	pending := len(s.queuePending)
	s.queueMu.Unlock()
	if pending > 0 && !queueHead {
		return "", fmt.Errorf("%w: earlier input is queued", ErrSteerUnavailable)
	}
	// The provider checks TurnToken itself, so a turn that ends after the
	// checks above cannot receive this input.
	if err := steerer.SteerTurn(ctx, TurnSteerRequest{TurnToken: turnID, Input: input}); err != nil {
		if errors.Is(err, ErrSteerUnavailable) || errors.Is(err, ErrSteerOutcomeUnknown) {
			return "", err
		}
		return "", fmt.Errorf("%w: %v", ErrSteerUnavailable, err)
	}
	log.Info().Str("session", s.Name).Str("turn_id", turnID).Str("source", source).Msg("session.steer.accepted")
	return turnID, nil
}
