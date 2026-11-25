package bridge

import (
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/turnresult"
	"github.com/rs/zerolog/log"
)

// TurnResultRecorder receives read-only lifecycle and delivery facts from the
// Feishu Bridge when hub-level turn_results.enabled is true. Nil disables
// recording with zero overhead.
type TurnResultRecorder interface {
	RegisterSubmission(turnresult.SubmissionInput) (turnresult.Record, error)
	RecordExecutionOutput(turnresult.ExecutionInput) (turnresult.Record, error)
	RecordDelivery(turnresult.DeliveryInput) (turnresult.Record, error)
	RecordArtifactDelivery(turnresult.ArtifactDeliveryInput) (turnresult.Record, error)
	MarkUnsupported(turnresult.Ref, turnresult.Source, time.Time) error
}

// SetTurnResultRecorder installs the optional Hub-owned single-turn result recorder.
func (b *Bridge) SetTurnResultRecorder(r TurnResultRecorder) {
	if b == nil {
		return
	}
	b.turnResults = r
}

// HasTurnBinding reports whether this Bridge currently attaches to or has an
// active Feishu binding for (targetID, sessionName), so Hub knows whether a
// Feishu delivery receipt is expected for turns in that session.
func (b *Bridge) HasTurnBinding(targetID, sessionName string) bool {
	if b == nil {
		return false
	}
	targetID = strings.TrimSpace(targetID)
	sessionName = strings.TrimSpace(sessionName)
	if targetID == "" || sessionName == "" {
		return false
	}
	b.streamMu.Lock()
	_, active := b.streamSessions[targetID+"\x00"+sessionName]
	b.streamMu.Unlock()
	if active {
		return true
	}
	if b.db == nil {
		return false
	}
	bridgeID := b.currentBridgeId()
	if chat, err := b.db.FindBySession(targetID, sessionName); err == nil && chat != nil && chat.BridgeId == bridgeID && chat.Status == ChatStatusActive {
		return true
	}
	if bs, err := b.db.GetBotSession(bridgeID); err == nil && botSessionReady(bs) &&
		strings.TrimSpace(bs.TargetID) == targetID && strings.TrimSpace(bs.SessionName) == sessionName {
		return true
	}
	return false
}

func (b *Bridge) recordTurnResultSubmission(chat *LarkChat, receipt submitReceipt, inputs []turnInputRef) {
	if b == nil || b.turnResults == nil || chat == nil || strings.TrimSpace(receipt.TurnID) == "" {
		return
	}
	msgIDs := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if id := strings.TrimSpace(in.MessageID); id != "" {
			msgIDs = append(msgIDs, id)
		}
	}
	_, err := b.turnResults.RegisterSubmission(turnresult.SubmissionInput{
		Ref: turnresult.Ref{
			TargetID:    chat.TargetID,
			SessionID:   receipt.SessionID,
			SessionName: chat.SessionName,
			LaunchID:    receipt.LaunchID,
			TurnID:      receipt.TurnID,
		},
		Source: turnresult.Source{
			Kind:          turnresult.SourceFeishu,
			BridgeID:      b.currentBridgeId(),
			ChatID:        chat.ChatID,
			RootMessageID: chat.RootMsgID,
			MessageIDs:    msgIDs,
		},
		DeliveryExpected: true,
		Now:              time.Now(),
	})
	if err != nil {
		log.Warn().Err(err).Str("turn_id", receipt.TurnID).Msg("bridge: record turn result submission")
	}
}

func (b *Bridge) recordTurnResultTurnStarted(chat *LarkChat, turnID string, source agentui.Source, canonicalJSON []byte) {
	if b == nil || b.turnResults == nil || chat == nil || strings.TrimSpace(turnID) == "" {
		return
	}
	sessionID, launchID := b.turnIdentityFromCanonical(chat, canonicalJSON)
	_, err := b.turnResults.RegisterSubmission(turnresult.SubmissionInput{
		Ref: turnresult.Ref{
			TargetID:    chat.TargetID,
			SessionID:   sessionID,
			SessionName: chat.SessionName,
			LaunchID:    launchID,
			TurnID:      turnID,
		},
		Source: turnresult.Source{
			Kind:          bridgeSourceKind(source),
			BridgeID:      b.currentBridgeId(),
			ChatID:        chat.ChatID,
			RootMessageID: chat.RootMsgID,
		},
		DeliveryExpected: true,
		Now:              time.Now(),
	})
	if err != nil {
		log.Warn().Err(err).Str("turn_id", turnID).Msg("bridge: record turn result start")
	}
}

func (b *Bridge) recordTurnResultCompletion(
	chat *LarkChat,
	turnID string,
	source agentui.Source,
	turnEvent agentui.TurnEvent,
	answer string,
	superseded bool,
	suppressed bool,
	receipt replyDeliveryReceipt,
	artifactsPending bool,
	startedAt int64,
	canonicalJSON []byte,
) {
	if b == nil || b.turnResults == nil || chat == nil || strings.TrimSpace(turnID) == "" {
		return
	}
	now := time.Now()
	sessionID, launchID := b.turnIdentityFromCanonical(chat, canonicalJSON)
	ref := turnresult.Ref{
		TargetID:    chat.TargetID,
		SessionID:   sessionID,
		SessionName: chat.SessionName,
		LaunchID:    launchID,
		TurnID:      turnID,
	}
	src := turnresult.Source{
		Kind:          bridgeSourceKind(source),
		BridgeID:      b.currentBridgeId(),
		ChatID:        chat.ChatID,
		RootMessageID: chat.RootMsgID,
	}

	execStatus, execCode := canonicalTurnExecution(turnEvent, canonicalJSON)
	cleanAnswer := strings.TrimSpace(stripAmuxGuidance(answer))
	if _, err := b.turnResults.RecordExecutionOutput(turnresult.ExecutionInput{
		Ref:    ref,
		Source: src,
		Execution: turnresult.Execution{
			Status:     execStatus,
			Code:       execCode,
			StartedAt:  startedAt,
			FinishedAt: now.Unix(),
		},
		Output: turnresult.Output{
			Format:  "markdown",
			Content: cleanAnswer,
		},
		Now: now,
	}); err != nil {
		log.Warn().Err(err).Str("turn_id", turnID).Msg("bridge: record turn result execution")
	}

	delivery := buildFeishuDelivery(b.currentBridgeId(), chat, execStatus, cleanAnswer, superseded, suppressed, receipt)
	if _, err := b.turnResults.RecordDelivery(turnresult.DeliveryInput{
		Ref:              ref,
		Source:           src,
		Delivery:         delivery,
		ArtifactsPending: artifactsPending,
		Now:              now,
	}); err != nil {
		log.Warn().Err(err).Str("turn_id", turnID).Msg("bridge: record turn result delivery")
	}
}

func (b *Bridge) recordTurnResultArtifacts(bridgeID, turnID string, attachments []turnresult.DeliveryMessage, failedCount, totalPlanned int) {
	if b == nil || b.turnResults == nil || strings.TrimSpace(turnID) == "" {
		return
	}
	if _, err := b.turnResults.RecordArtifactDelivery(turnresult.ArtifactDeliveryInput{
		BridgeID:     bridgeID,
		TurnID:       turnID,
		Attachments:  attachments,
		FailedCount:  failedCount,
		TotalPlanned: totalPlanned,
		Now:          time.Now(),
	}); err != nil {
		log.Warn().Err(err).Str("turn_id", turnID).Msg("bridge: record turn result artifacts")
	}
}

func (b *Bridge) turnIdentityFromCanonical(chat *LarkChat, canonicalJSON []byte) (string, string) {
	if len(canonicalJSON) > 0 {
		if ev, err := agentstream.UnmarshalEvent(canonicalJSON); err == nil {
			if ev.SessionID != "" || ev.Origin.LaunchID != "" {
				return ev.SessionID, ev.Origin.LaunchID
			}
		}
	}
	if b != nil && b.hub != nil && chat != nil {
		if sess := b.boundSession(chat.TargetID, chat.SessionName); sess != nil {
			return sess.GetId(), sess.GetLaunchId()
		}
	}
	return "", ""
}

func bridgeSourceKind(source agentui.Source) string {
	switch source {
	case agentui.SourceScheduler:
		return turnresult.SourceSchedule
	case agentui.SourceCLI:
		return turnresult.SourceCLI
	case agentui.SourceDesktop:
		return turnresult.SourceDesktop
	case agentui.SourceSubtask:
		return turnresult.SourceSubtask
	case agentui.SourceGoal:
		return turnresult.SourceGoal
	default:
		return turnresult.SourceFeishu
	}
}

func canonicalTurnExecution(turnEvent agentui.TurnEvent, canonicalJSON []byte) (string, string) {
	if len(canonicalJSON) > 0 {
		if ev, err := agentstream.UnmarshalEvent(canonicalJSON); err == nil {
			if finished, ok := ev.Body.(agentstream.TurnFinished); ok {
				switch finished.Terminal.Outcome {
				case agentstream.OutcomeSuccess:
					return turnresult.ExecutionCompleted, finished.Terminal.Code
				case agentstream.OutcomeCancelled:
					return turnresult.ExecutionCancelled, finished.Terminal.Code
				case agentstream.OutcomeInterrupted:
					return turnresult.ExecutionInterrupted, finished.Terminal.Code
				default:
					return turnresult.ExecutionFailed, finished.Terminal.Code
				}
			}
		}
	}
	switch turnEvent.State {
	case "failed":
		return turnresult.ExecutionFailed, turnEvent.StopReason
	case "cancelled":
		return turnresult.ExecutionCancelled, turnEvent.StopReason
	case "interrupted":
		return turnresult.ExecutionInterrupted, turnEvent.StopReason
	default:
		return turnresult.ExecutionCompleted, turnEvent.StopReason
	}
}

func buildFeishuDelivery(bridgeID string, chat *LarkChat, execStatus, cleanAnswer string, superseded, suppressed bool, receipt replyDeliveryReceipt) turnresult.Delivery {
	d := turnresult.Delivery{
		Channel:       "feishu",
		BridgeID:      bridgeID,
		ChatID:        chat.ChatID,
		RootMessageID: chat.RootMsgID,
	}
	if suppressed || superseded {
		d.Status = turnresult.DeliverySuppressed
		if superseded {
			d.Reason = "superseded"
		} else {
			d.Reason = "visibility_suppressed"
		}
		return d
	}
	var confirmed []turnresult.DeliveryMessage
	unfinished := 0
	for _, card := range receipt.Cards {
		if card.Finished && strings.TrimSpace(card.MessageID) != "" {
			kind := card.Kind
			if kind == "" {
				kind = turnresult.MessageKindPrimary
			}
			confirmed = append(confirmed, turnresult.DeliveryMessage{
				Kind:      kind,
				MessageID: strings.TrimSpace(card.MessageID),
			})
		} else if !card.Finished {
			unfinished++
		}
	}
	d.Messages = confirmed
	switch {
	case len(confirmed) > 0 && unfinished == 0 && receipt.FailedAttempts == 0:
		d.Status = turnresult.DeliveryDelivered
	case len(confirmed) > 0 && (unfinished > 0 || receipt.FailedAttempts > 0):
		d.Status = turnresult.DeliveryPartial
	case receipt.FailedAttempts > 0 || unfinished > 0:
		d.Status = turnresult.DeliveryFailed
	case execStatus != turnresult.ExecutionCompleted || cleanAnswer == "":
		d.Status = turnresult.DeliverySuppressed
	default:
		d.Status = turnresult.DeliveryUnknown
	}
	return d
}
