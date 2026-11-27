package bridge

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/amuxos/amux/internal/agentui"
)

// turnStreamState consolidates the mutable per-stream and per-turn presentation
// state for a live Feishu attach stream so turn start/finish lifecycle resets
// happen in a single place rather than across scattered closure variables.
type turnStreamState struct {
	reply *agentui.ReplyProjection

	activityViewMu  sync.RWMutex
	currentActivity *agentui.ActivityView

	finalAnswerMu sync.Mutex
	finalAnswer   string

	subtaskReportTurn atomic.Bool
	turnSource        agentui.Source
	turnStartedAt     int64

	providerFailureNoticePending bool
	failureNoticeDelivered       bool

	lastGoalStatus string
	lastGoalPatch  time.Time
}

func newTurnStreamState() *turnStreamState {
	return &turnStreamState{
		reply:      agentui.NewReplyProjection(),
		turnSource: agentui.SourceFeishu,
	}
}

func (s *turnStreamState) ActivityView() *agentui.ActivityView {
	s.activityViewMu.RLock()
	defer s.activityViewMu.RUnlock()
	return s.currentActivity
}

func (s *turnStreamState) SetActivityView(view *agentui.ActivityView) {
	s.activityViewMu.Lock()
	s.currentActivity = view
	s.activityViewMu.Unlock()
}

func (s *turnStreamState) FinalAnswer() string {
	s.finalAnswerMu.Lock()
	defer s.finalAnswerMu.Unlock()
	return s.finalAnswer
}

func (s *turnStreamState) SetFinalAnswer(text string) {
	s.finalAnswerMu.Lock()
	s.finalAnswer = text
	s.finalAnswerMu.Unlock()
}

func (s *turnStreamState) CurrentTurnID() string {
	return s.reply.TurnID()
}

func (s *turnStreamState) BeginTurnMetadata(source agentui.Source, startedAt int64) {
	s.subtaskReportTurn.Store(source == agentui.SourceSubtask)
	s.turnSource = source
	s.turnStartedAt = startedAt
	s.providerFailureNoticePending = false
	s.failureNoticeDelivered = false
}

func (s *turnStreamState) ResetForNewTurn(turnID string) {
	s.SetFinalAnswer("")
	s.reply.BeginTurn(turnID)
	s.SetActivityView(nil)
}

func (s *turnStreamState) ResetAfterTurnFinished() {
	s.reply.Reset()
	s.SetActivityView(nil)
}

func (s *turnStreamState) ShouldRefreshGoalCard(status string, now time.Time) bool {
	if status != s.lastGoalStatus || now.Sub(s.lastGoalPatch) >= 2*time.Second {
		s.lastGoalStatus = status
		s.lastGoalPatch = now
		return true
	}
	return false
}
