package agentui

import (
	"strings"

	"github.com/amuxos/amux/internal/agentstream"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// FromAttachEvent decodes the canonical transport envelope used by first-party
// presentation consumers. Flat legacy payloads belong to the unspecified
// Attach compatibility contract and are intentionally not reinterpreted here.
func FromAttachEvent(event *pb.AttachEvent) []Event {
	if event == nil {
		return nil
	}
	if len(event.CanonicalJson) > 0 {
		canonical, err := agentstream.UnmarshalEvent(event.CanonicalJson)
		if err != nil {
			return []Event{{Kind: KindError, Source: SourceSystem, Text: err.Error(), Error: &ErrorEvent{Message: err.Error()}}}
		}
		return FromAgentEvent(canonical)
	}
	if event.Kind == pb.AttachEvent_HEARTBEAT {
		return []Event{{Kind: KindHeartbeat, Source: SourceSystem}}
	}
	return nil
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
