package agentui

import (
	"encoding/json"

	"github.com/amuxos/amux/internal/agentstream"
)

// FromAgentEvent is the presentation boundary for the canonical Agent Event
// Timeline. It contains no provider-specific lifecycle inference.
func FromAgentEvent(event agentstream.Event) []Event {
	raw, _ := json.Marshal(event.Body)
	out := Event{
		ID:       event.Origin.NativeID,
		Seq:      int64(event.Cursor.Seq),
		Source:   normalizeSource(event.Origin.Channel),
		Provider: event.Origin.Provider,
		RawJSON:  raw,
	}
	if event.Turn != nil {
		out.Turn = &TurnEvent{ID: event.Turn.ID, Seq: int64(event.Turn.Seq)}
	}
	switch body := event.Body.(type) {
	case agentstream.TurnStarted:
		out.Kind = KindInputSubmitted
		out.Text = body.InputSummary
		out.Input = &InputEvent{Source: out.Source, Text: body.InputSummary}
	case agentstream.AssistantDelta:
		out.Kind = KindAssistantTextDelta
		out.Source = SourceAgent
		out.Text = body.Text
	case agentstream.AssistantMessage:
		out.Kind = KindAssistantText
		out.Source = SourceAgent
		out.Text = body.Text
	case agentstream.ThinkingDelta:
		out.Kind = KindThinkingDelta
		out.Source = SourceAgent
		out.Text = body.Text
	case agentstream.PlanReplaced:
		out.Kind = KindPlan
		out.Source = SourceAgent
		out.Plan = &PlanEvent{Entries: make([]PlanEntry, 0, len(body.Entries))}
		for _, entry := range body.Entries {
			out.Plan.Entries = append(out.Plan.Entries, PlanEntry{Content: entry.Content, Priority: entry.Priority, Status: entry.Status})
		}
	case agentstream.ToolChanged:
		out.Source = SourceTool
		out.Kind = toolKind(body.Phase)
		out.Tool = &ToolEvent{
			ID:            body.Tool.ID,
			Name:          body.Tool.Name,
			Title:         body.Tool.Title,
			Status:        string(body.Phase),
			InputJSON:     cloneBytes(body.Tool.Input),
			OutputPreview: body.Tool.OutputPreview,
			Error:         body.Tool.Error,
			ContentJSON:   cloneBytes(body.Tool.Content),
			LocationsJSON: cloneBytes(body.Tool.Locations),
			RawOutputJSON: cloneBytes(body.Tool.RawOutput),
		}
	case agentstream.PermissionRequested:
		out.Kind = KindPermissionRequested
		out.Source = SourceSystem
		out.Permission = permissionFromAgent(body.Request)
		out.Permission.SessionID = event.SessionID
		out.Permission.LaunchID = event.Origin.LaunchID
	case agentstream.PermissionResolved:
		out.Kind = KindPermissionResolved
		out.Source = SourceSystem
		out.Permission = &PermissionEvent{RequestID: body.RequestID}
	case agentstream.UsageObserved:
		out.Kind = KindUsage
		out.Source = SourceAgent
		out.UsageJSON = cloneBytes(body.Usage)
	case agentstream.ResultObserved:
		out.Kind = KindResult
		out.Source = SourceResult
		out.Text = body.Text
		out.UsageJSON = cloneBytes(body.Usage)
		out.ResultJSON = raw
	case agentstream.Diagnostic:
		out.Kind = KindUnknown
		out.Source = SourceSystem
		out.Text = body.Message
		if body.Severity == agentstream.SeverityError {
			out.Kind = KindError
			out.Error = &ErrorEvent{Message: body.Message, Code: body.Code}
		}
	case agentstream.TurnFinished:
		out.Kind = KindTurnDone
		out.Source = SourceSystem
		state := "turn_done"
		if body.Terminal.Outcome == agentstream.OutcomeCancelled || body.Terminal.Outcome == agentstream.OutcomeInterrupted {
			out.Kind = KindInterrupted
			state = "interrupted"
		} else if body.Terminal.Outcome == agentstream.OutcomeError {
			state = "failed"
		}
		out.Turn = turnWithState(out.Turn, state, body.Terminal.Message)
	case agentstream.SessionMetadataChanged:
		out.Kind = KindHeartbeat
		out.Source = SourceSystem
	case agentstream.SessionDiagnostic:
		out.Kind = KindUnknown
		out.Source = SourceSystem
		out.Text = body.Message
		if body.Severity == agentstream.SeverityError {
			out.Kind = KindError
			out.Error = &ErrorEvent{Message: body.Message, Code: body.Code}
		}
	case agentstream.CommandsReplaced, agentstream.ExtensionObserved:
		out.Kind = KindUnknown
		out.Source = SourceSystem
	default:
		return nil
	}
	return []Event{out}
}

func turnWithState(turn *TurnEvent, state, reason string) *TurnEvent {
	if turn == nil {
		turn = &TurnEvent{}
	}
	turn.State = state
	turn.StopReason = reason
	return turn
}

func toolKind(phase agentstream.ToolPhase) EventKind {
	switch phase {
	case agentstream.ToolPhaseStarted:
		return KindToolStarted
	case agentstream.ToolPhaseCompleted:
		return KindToolCompleted
	case agentstream.ToolPhaseFailed:
		return KindToolFailed
	default:
		return KindToolUpdated
	}
}

func permissionFromAgent(request agentstream.PermissionRequest) *PermissionEvent {
	out := &PermissionEvent{RequestID: request.ID, Provider: request.Provider, RawPayload: cloneBytes(request.Details)}
	if request.ToolCall != nil {
		out.Tool = &PermissionTool{
			ID:             request.ToolCall.ID,
			Name:           request.ToolCall.Name,
			Title:          request.ToolCall.Title,
			RawInput:       cloneBytes(request.ToolCall.RawInput),
			Question:       request.ToolCall.Question,
			MultiSelect:    request.ToolCall.MultiSelect,
			AllowOther:     request.ToolCall.AllowOther,
			Secret:         request.ToolCall.Secret,
			Blocking:       request.ToolCall.Blocking,
			Delivery:       request.ToolCall.Delivery,
			AgentSessionID: request.ToolCall.AgentSessionID,
		}
	}
	for _, option := range request.Options {
		out.Options = append(out.Options, PermissionOption{ID: option.ID, Label: firstNonEmpty(option.Name, option.ID), Kind: option.Kind, Description: option.Description})
	}
	return out
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
