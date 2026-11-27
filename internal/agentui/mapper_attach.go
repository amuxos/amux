package agentui

import (
	"encoding/json"
	"strings"

	"github.com/amuxos/amux/internal/agentstream"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// FromAttachEvent decodes the canonical transport envelope used by first-party
// presentation consumers, including session-level metadata updates (effective
// model, available commands, and goal state changes). Flat legacy output
// payloads belong to the unspecified Attach compatibility contract and are
// intentionally not reinterpreted here.
func FromAttachEvent(event *pb.AttachEvent) []Event {
	if event == nil {
		return nil
	}
	var out []Event
	if model := strings.TrimSpace(event.EffectiveModel); model != "" {
		out = append(out, Event{
			Kind:   KindEffectiveModelChanged,
			Source: SourceSystem,
			Text:   model,
		})
	}
	if event.EventType == "available_commands" {
		return append(out, Event{
			Kind:    KindAvailableCommands,
			Source:  SourceSystem,
			RawJSON: cloneBytes(event.StructuredJson),
		})
	}
	if event.EventType == "goal_changed" {
		var value agentstream.GoalChanged
		if json.Unmarshal(event.StructuredJson, &value) == nil {
			return append(out, Event{
				Kind:   KindGoalChanged,
				Source: SourceGoal,
				Goal:   cloneGoalState(value.Goal),
			})
		}
		return out
	}
	if len(event.CanonicalJson) > 0 {
		canonical, err := agentstream.UnmarshalEvent(event.CanonicalJson)
		if err != nil {
			return append(out, Event{Kind: KindError, Source: SourceSystem, Text: err.Error(), Error: &ErrorEvent{Message: err.Error()}})
		}
		mapped := FromAgentEvent(canonical)
		if event.Historical {
			for i := range mapped {
				mapped[i].Historical = true
			}
		}
		return append(out, mapped...)
	}
	if event.Kind == pb.AttachEvent_HEARTBEAT {
		return append(out, Event{Kind: KindHeartbeat, Source: SourceSystem})
	}
	return out
}

func normalizeSource(source string) Source {
	switch strings.TrimSpace(source) {
	case "", "agent":
		return SourceAgent
	case "cli":
		return SourceCLI
	case "desktop":
		return SourceDesktop
	case "lark", "feishu":
		return SourceFeishu
	case "scheduler":
		return SourceScheduler
	case "goal":
		return SourceGoal
	case "subtask":
		return SourceSubtask
	case "system":
		return SourceSystem
	case "tool":
		return SourceTool
	case "result":
		return SourceResult
	default:
		return SourceUnknown
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
