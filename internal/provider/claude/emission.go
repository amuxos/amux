package claude

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/provider"
)

// emissionFromSidecarEvent decodes the Claude sidecar's native wire event into
// the canonical producer vocabulary at the provider boundary. The native DTO
// never crosses Proc or broker APIs.
func emissionFromSidecarEvent(ev provider.Event) (agentstream.ProducerEmission, bool, error) {
	origin := agentstream.Origin{
		Actor: agentstream.ActorAgent, Channel: "agent", Provider: ev.Provider,
		NativeType: string(ev.Type), NativeID: ev.ID, NativeSeq: ev.Seq,
	}
	emission := agentstream.ProducerEmission{Origin: origin}
	switch ev.Type {
	case provider.EventTurnStarted, provider.EventUnknown:
		return agentstream.ProducerEmission{}, true, nil
	case provider.EventAssistantTextDelta:
		emission.Turn = agentstream.AssistantDelta{Text: ev.Text}
	case provider.EventAssistantText:
		emission.Turn = agentstream.AssistantMessage{Text: ev.Text}
	case provider.EventThinkingDelta:
		emission.Turn = agentstream.ThinkingDelta{Text: ev.Text}
	case provider.EventPlan:
		var entries []agentstream.PlanEntry
		if ev.Plan != nil {
			entries = make([]agentstream.PlanEntry, 0, len(ev.Plan.Entries))
			for _, entry := range ev.Plan.Entries {
				entries = append(entries, agentstream.PlanEntry{Content: entry.Content, Priority: entry.Priority, Status: entry.Status})
			}
		}
		emission.Turn = agentstream.PlanReplaced{Entries: entries}
	case provider.EventToolStarted, provider.EventToolUpdated, provider.EventToolCompleted, provider.EventToolFailed:
		emission.Origin.Actor, emission.Origin.Channel = agentstream.ActorTool, "tool"
		phase := agentstream.ToolPhaseUpdated
		switch ev.Type {
		case provider.EventToolStarted:
			phase = agentstream.ToolPhaseStarted
		case provider.EventToolCompleted:
			phase = agentstream.ToolPhaseCompleted
		case provider.EventToolFailed:
			phase = agentstream.ToolPhaseFailed
		}
		tool := agentstream.Tool{}
		if ev.Tool != nil {
			tool = agentstream.Tool{ID: ev.Tool.ID, Name: ev.Tool.Name, Title: ev.Tool.Title, Input: ev.Tool.Input,
				OutputPreview: ev.Tool.OutputPreview, Error: ev.Tool.Error, Content: ev.Tool.Content,
				Locations: ev.Tool.Locations, RawOutput: ev.Tool.RawOutput}
		}
		emission.Turn = agentstream.ToolChanged{Phase: phase, Tool: tool}
	case provider.EventPermissionRequest:
		if ev.Permission == nil {
			return agentstream.ProducerEmission{}, false, agentstream.ErrInvalidPayload
		}
		emission.Turn = canonicalClaudePermission(*ev.Permission)
	case provider.EventPermissionResolved:
		if ev.Permission == nil || ev.Permission.ID == "" {
			return agentstream.ProducerEmission{}, false, agentstream.ErrInvalidPayload
		}
		emission.Turn = agentstream.PermissionResolved{RequestID: ev.Permission.ID}
	case provider.EventUsage:
		emission.Turn = agentstream.UsageObserved{Usage: ev.Usage}
	case provider.EventResult:
		if ev.Result == nil {
			return agentstream.ProducerEmission{}, false, agentstream.ErrInvalidPayload
		}
		emission.Turn = agentstream.ResultObserved{Text: ev.Result.Text, Usage: ev.Result.Usage, CostUSD: ev.Result.CostUSD}
	case provider.EventError:
		message, code := "provider error", "provider_error"
		if ev.Error != nil {
			if ev.Error.Message != "" {
				message = ev.Error.Message
			}
			if ev.Error.Code != "" {
				code = ev.Error.Code
			}
		}
		emission.Turn = agentstream.Diagnostic{Severity: agentstream.SeverityError, Code: code, Message: message}
	case provider.EventAvailableCommands:
		commands := make([]agentstream.Command, 0, len(ev.AvailableCommands))
		for _, command := range ev.AvailableCommands {
			commands = append(commands, agentstream.Command{Name: command.Name, Description: command.Description, InputHint: command.InputHint, Kind: command.Kind})
		}
		emission.Session = agentstream.CommandsReplaced{Commands: commands}
	case provider.EventInterrupted:
		message := ""
		if ev.State != nil {
			message = ev.State.StopReason
		}
		terminal := agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "interrupted", Message: message}
		emission.Terminal = &terminal
	case provider.EventTurnDone:
		terminal := claudeTerminal(ev)
		emission.Terminal = &terminal
	default:
		return agentstream.ProducerEmission{}, false, fmt.Errorf("%w: unknown Claude sidecar event %q", agentstream.ErrInvalidPayload, ev.Type)
	}
	return emission, false, nil
}

func canonicalClaudePermission(permission provider.Permission) agentstream.PermissionRequested {
	request := agentstream.PermissionRequest{ID: permission.ID, Provider: permission.Provider, Details: permission.Raw}
	if permission.ToolCall != nil {
		request.ToolCall = &agentstream.PermissionToolCall{ID: permission.ToolCall.ID, Name: permission.ToolCall.Name,
			Title: permission.ToolCall.Title, RawInput: permission.ToolCall.RawInput, Question: permission.ToolCall.Question,
			MultiSelect: permission.ToolCall.MultiSelect}
	}
	for _, option := range permission.Options {
		request.Options = append(request.Options, agentstream.PermissionOption{ID: option.OptionId, Name: option.Name, Kind: option.Kind})
	}
	return agentstream.PermissionRequested{Request: request}
}

func claudeTerminal(ev provider.Event) agentstream.Terminal {
	state, reason := "", ""
	if ev.State != nil {
		state, reason = strings.TrimSpace(ev.State.State), strings.TrimSpace(ev.State.StopReason)
	}
	message, code := reason, "provider_error"
	if ev.Error != nil {
		if ev.Error.Message != "" {
			message = ev.Error.Message
		}
		if ev.Error.Code != "" {
			code = ev.Error.Code
		}
	}
	switch {
	case reason == "cancelled":
		return agentstream.Terminal{Outcome: agentstream.OutcomeCancelled, Code: "cancelled", Message: message}
	case state == "interrupted":
		return agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "interrupted", Message: message}
	case state == "failed" || reason == "error" || reason == "process_exit" || ev.Error != nil:
		if reason == "process_exit" && (ev.Error == nil || ev.Error.Code == "") {
			code = "provider_stream_closed"
		}
		if reason == "turn_idle_timeout" || reason == "turn idle timeout" {
			code = "turn_idle_timeout"
		}
		return agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: code, Message: message}
	default:
		return agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed", Message: message}
	}
}
