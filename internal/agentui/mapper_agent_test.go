package agentui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestFromAgentEventMapsCanonicalPresentationEvents(t *testing.T) {
	ref := &agentstream.TurnRef{ID: "turn-1", Seq: 7}
	base := agentstream.Event{
		Cursor: agentstream.Cursor{Epoch: "epoch-1", Seq: 9},
		Turn:   ref,
		Origin: agentstream.Origin{Channel: "lark", Provider: "codex", NativeID: "native-1"},
	}
	tests := []struct {
		name  string
		body  agentstream.Body
		kind  EventKind
		check func(t *testing.T, event Event)
	}{
		{name: "assistant", body: agentstream.AssistantDelta{Text: "hello"}, kind: KindAssistantTextDelta, check: func(t *testing.T, event Event) {
			if event.Text != "hello" || event.Source != SourceAgent {
				t.Fatalf("event = %+v", event)
			}
		}},
		{name: "tool", body: agentstream.ToolChanged{Phase: agentstream.ToolPhaseCompleted, Tool: agentstream.Tool{ID: "tool-1", Name: "exec", Input: json.RawMessage(`{"cmd":"pwd"}`)}}, kind: KindToolCompleted, check: func(t *testing.T, event Event) {
			if event.Tool == nil || event.Tool.ID != "tool-1" || event.Source != SourceTool {
				t.Fatalf("event = %+v", event)
			}
		}},
		{name: "permission", body: agentstream.PermissionRequested{Request: agentstream.PermissionRequest{ID: "perm-1", ToolCall: &agentstream.PermissionToolCall{Name: "Bash"}, Options: []agentstream.PermissionOption{{ID: "allow", Name: "Allow"}}}}, kind: KindPermissionRequested, check: func(t *testing.T, event Event) {
			if event.Permission == nil || event.Permission.RequestID != "perm-1" || len(event.Permission.Options) != 1 {
				t.Fatalf("event = %+v", event)
			}
		}},
		{name: "result", body: agentstream.ResultObserved{Text: "done", Usage: json.RawMessage(`{"input_tokens":3}`)}, kind: KindResult, check: func(t *testing.T, event Event) {
			if event.Text != "done" || event.Source != SourceResult || len(event.UsageJSON) == 0 {
				t.Fatalf("event = %+v", event)
			}
		}},
		{name: "terminal", body: agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "failed", Message: "boom"}}, kind: KindTurnDone, check: func(t *testing.T, event Event) {
			if event.Turn == nil || event.Turn.State != "failed" || event.Turn.StopReason != "boom" {
				t.Fatalf("event = %+v", event)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := FromAgentEvent(agentstream.Event{Cursor: base.Cursor, Turn: base.Turn, Origin: base.Origin, Body: tt.body})
			if len(events) != 1 || events[0].Kind != tt.kind {
				t.Fatalf("events = %+v, want kind %q", events, tt.kind)
			}
			if events[0].Seq != 9 || events[0].Turn == nil || events[0].Turn.ID != "turn-1" {
				t.Fatalf("identity = %+v", events[0])
			}
			tt.check(t, events[0])
		})
	}
}

func TestFromAttachEventUsesCanonicalPayloadAsSourceOfTruth(t *testing.T) {
	canonical := agentstream.Event{
		SessionID: "session-1",
		Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: 1},
		Turn:      &agentstream.TurnRef{ID: "turn-1", Seq: 1},
		At:        time.Now().UTC(),
		Origin:    agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "codex"},
		Body:      agentstream.ResultObserved{Text: "canonical result"},
	}
	raw, err := agentstream.MarshalEvent(canonical)
	if err != nil {
		t.Fatal(err)
	}
	events := FromAttachEvent(&pb.AttachEvent{
		CanonicalJson: raw,
		EventType:     "assistant_text_delta",
		Text:          "stale flat projection",
	})
	if len(events) != 1 || events[0].Kind != KindResult || events[0].Text != "canonical result" {
		t.Fatalf("events = %+v, want canonical result", events)
	}
}

func TestFromAttachEventKeepsOnlyCanonicalAndHeartbeatContracts(t *testing.T) {
	if events := FromAttachEvent(&pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Text: "legacy"}); len(events) != 0 {
		t.Fatalf("legacy flat event = %+v, want ignored", events)
	}
	if events := FromAttachEvent(&pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT}); len(events) != 1 || events[0].Kind != KindHeartbeat {
		t.Fatalf("heartbeat = %+v", events)
	}
	if events := FromAttachEvent(&pb.AttachEvent{CanonicalJson: []byte(`{"bad":true}`)}); len(events) != 1 || events[0].Kind != KindError {
		t.Fatalf("invalid canonical event = %+v, want error", events)
	}
}
