package session

import (
	"errors"
	"strconv"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/google/uuid"
)

// SubtaskNoticeSource is the submit source of background-task completion
// notices delivered into a parent session.
const SubtaskNoticeSource = "subtask"

// maxSubtaskNoticeRequeues bounds how often one completion notice is
// re-queued after its turn ended before the model processed it.
const maxSubtaskNoticeRequeues = 3

// requeueableNoticeCodes are terminal codes meaning the turn was torn down by
// the runtime (session closed, provider stream lost, restart) rather than
// handled or interrupted by a user.
var requeueableNoticeCodes = map[string]bool{
	"session_closed":           true,
	"provider_stream_closed":   true,
	"runtime_reconnect_failed": true,
	"runtime_restart":          true,
	"aborted":                  true,
}

func shouldRequeueSubtaskNotice(item *queuedInput, dispatchErr error, terminal *agentstream.Terminal) bool {
	if item == nil || item.Source != SubtaskNoticeSource || item.Control != "" || item.lifecycle != nil || item.requeues >= maxSubtaskNoticeRequeues {
		return false
	}
	if dispatchErr != nil {
		return errors.Is(dispatchErr, ErrProcessUnavailable)
	}
	if terminal == nil {
		return false
	}
	if terminal.Outcome != agentstream.OutcomeInterrupted && terminal.Outcome != agentstream.OutcomeError {
		return false
	}
	return requeueableNoticeCodes[terminal.Code]
}

func (s *Session) watchTurnTerminal(turnID string) {
	s.queueMu.Lock()
	if s.turnTerminals == nil {
		s.turnTerminals = map[string]*agentstream.Terminal{}
	}
	s.turnTerminals[turnID] = nil
	s.queueMu.Unlock()
}

func (s *Session) recordTurnTerminal(turnID string, terminal agentstream.Terminal) {
	s.queueMu.Lock()
	if _, ok := s.turnTerminals[turnID]; ok {
		t := terminal
		s.turnTerminals[turnID] = &t
	}
	s.queueMu.Unlock()
}

func (s *Session) takeTurnTerminal(turnID string) *agentstream.Terminal {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	t := s.turnTerminals[turnID]
	delete(s.turnTerminals, turnID)
	return t
}

// requeueSubtaskNoticeLocked puts a fresh copy of the notice back in front of
// the queue, or parks it until a process is installed when none is running.
// Caller holds queueMu.
func (s *Session) requeueSubtaskNoticeLocked(item *queuedInput, hasProcess bool) {
	s.queueNextID++
	retry := &queuedInput{
		QueueItem: QueueItem{
			ID:        strconv.FormatInt(s.queueNextID, 10),
			TurnID:    uuid.NewString(),
			Source:    item.Source,
			Text:      item.Text,
			CreatedAt: time.Now(),
		},
		input:    item.input,
		auth:     item.auth,
		requeues: item.requeues + 1,
		done:     make(chan error, 1),
	}
	if hasProcess {
		s.queuePending = append([]*queuedInput{retry}, s.queuePending...)
		return
	}
	s.parkedNotices = append(s.parkedNotices, retry)
}

// releaseParkedNotices queues notices parked while no process was running.
func (s *Session) releaseParkedNotices() {
	s.queueMu.Lock()
	parked := s.parkedNotices
	s.parkedNotices = nil
	if len(parked) == 0 {
		s.queueMu.Unlock()
		return
	}
	if s.queueBusy {
		s.queuePending = append(parked, s.queuePending...)
		s.queueMu.Unlock()
		return
	}
	s.queueBusy = true
	first := parked[0]
	s.queuePending = append(parked[1:], s.queuePending...)
	s.queueMu.Unlock()
	go s.runQueuedInput(first)
}
