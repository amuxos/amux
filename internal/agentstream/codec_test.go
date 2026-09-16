package agentstream_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestCanonicalEventCodecRoundTripsEveryPayloadKind(t *testing.T) {
	t.Parallel()

	turn := &agentstream.TurnRef{ID: "turn-1", Seq: 7}
	bodies := []agentstream.Body{
		agentstream.TurnStarted{InputSummary: "fix the bug"},
		agentstream.AssistantDelta{Text: "hel"},
		agentstream.AssistantMessage{Text: "hello"},
		agentstream.ThinkingDelta{Text: "inspect"},
		agentstream.PlanReplaced{Entries: []agentstream.PlanEntry{{Content: "test", Priority: "high", Status: "in_progress"}}},
		agentstream.ToolChanged{Phase: agentstream.ToolPhaseCompleted, Tool: agentstream.Tool{ID: "tool-1", Name: "shell", Input: json.RawMessage(`{"command":"pwd"}`), Content: json.RawMessage(`{"text":"ok"}`)}},
		agentstream.PermissionRequested{Request: agentstream.PermissionRequest{ID: "permission-1", Provider: "codex", Options: []agentstream.PermissionOption{{ID: "allow", Name: "Allow", Kind: "allow_once"}}}},
		agentstream.PermissionResolved{RequestID: "permission-1", OptionIDs: []string{"allow"}, Message: "accepted"},
		agentstream.UsageObserved{Usage: json.RawMessage(`{"input_tokens":12}`)},
		agentstream.ResultObserved{Text: "done", Usage: json.RawMessage(`{"output_tokens":3}`), CostUSD: 0.01},
		agentstream.Diagnostic{Severity: agentstream.SeverityWarning, Code: "retry", Message: "retrying", Retryable: true},
		agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}},
		agentstream.SessionMetadataChanged{AgentSessionID: "native-1", EffectiveModel: "gpt-5"},
		agentstream.CommandsReplaced{Commands: []agentstream.Command{{Name: "review", Description: "Review code"}}},
		agentstream.SessionDiagnostic{Severity: agentstream.SeverityError, Code: "process_exit", Message: "exited"},
		agentstream.ExtensionObserved{Namespace: "codex", Schema: "v1", Payload: json.RawMessage(`{"future":true}`)},
	}

	for i, body := range bodies {
		event := agentstream.Event{
			SessionID: "session-1",
			Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: uint64(i + 1)},
			At:        time.Date(2026, time.August, 31, 10, i, 0, 0, time.UTC),
			Origin:    agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "cli", Provider: "codex", NativeType: "test"},
			Body:      body,
		}
		switch body.(type) {
		case agentstream.SessionMetadataChanged, agentstream.CommandsReplaced, agentstream.SessionDiagnostic, agentstream.ExtensionObserved:
		default:
			event.Turn = turn
		}
		raw, err := agentstream.MarshalEvent(event)
		if err != nil {
			t.Fatalf("MarshalEvent(%s) error = %v", body.Kind(), err)
		}
		got, err := agentstream.UnmarshalEvent(raw)
		if err != nil {
			t.Fatalf("UnmarshalEvent(%s) error = %v", body.Kind(), err)
		}
		if !reflect.DeepEqual(got, event) {
			t.Fatalf("round trip %s mismatch\n got: %#v\nwant: %#v", body.Kind(), got, event)
		}
	}
}

func TestCanonicalEventCodecUsesStableSnakeCaseSchema(t *testing.T) {
	t.Parallel()

	raw, err := agentstream.MarshalEvent(agentstream.Event{
		SessionID: "session-1",
		Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: 1},
		Turn:      &agentstream.TurnRef{ID: "turn-1", Seq: 1},
		At:        time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC),
		Origin:    agentstream.Origin{Actor: agentstream.ActorUser, NativeType: "prompt", TransportSeq: 9, LaunchID: "launch-1"},
		Body:      agentstream.TurnStarted{InputSummary: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range [][]byte{[]byte(`"input_summary"`), []byte(`"native_type"`), []byte(`"transport_seq":9`), []byte(`"launch_id"`), []byte(`"epoch"`)} {
		if !bytes.Contains(raw, field) {
			t.Fatalf("canonical JSON %s does not contain stable field %s", raw, field)
		}
	}
	for _, goField := range [][]byte{[]byte(`"InputSummary"`), []byte(`"NativeType"`), []byte(`"LaunchID"`), []byte(`"Epoch"`)} {
		if bytes.Contains(raw, goField) {
			t.Fatalf("canonical JSON %s leaked Go field %s", raw, goField)
		}
	}
}

func TestCanonicalEventCodecRejectsUnknownOrMismatchedPayload(t *testing.T) {
	t.Parallel()

	for _, raw := range [][]byte{
		[]byte(`{"version":1,"session_id":"s","cursor":{"Epoch":"e","Seq":1},"kind":"future","body":{}}`),
		[]byte(`{"version":1,"session_id":"s","cursor":{"Epoch":"e","Seq":1},"kind":"assistant_delta","body":{"Text":"x"}}`),
		[]byte(`{"version":2,"session_id":"s","cursor":{"Epoch":"e","Seq":1},"kind":"commands_replaced","body":{"Commands":[]}}`),
		[]byte(`not-json`),
	} {
		if _, err := agentstream.UnmarshalEvent(raw); err == nil {
			t.Fatalf("UnmarshalEvent(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestCanonicalEventCodecRejectsUnknownEnvelopeAndBodyFields(t *testing.T) {
	t.Parallel()

	for _, raw := range [][]byte{
		[]byte(`{"version":1,"session_id":"s","cursor":{"epoch":"e","seq":1},"at":"2026-08-31T00:00:00Z","origin":{},"kind":"commands_replaced","body":{"commands":[]},"future":true}`),
		[]byte(`{"version":1,"session_id":"s","cursor":{"epoch":"e","seq":1},"at":"2026-08-31T00:00:00Z","origin":{},"kind":"commands_replaced","body":{"commands":[],"future":true}}`),
	} {
		if _, err := agentstream.UnmarshalEvent(raw); err == nil {
			t.Fatalf("UnmarshalEvent(%q) unexpectedly accepted an unknown field", raw)
		}
	}
}

func TestCanonicalEventCodecExcludesOpaqueProviderDumps(t *testing.T) {
	t.Parallel()

	base := agentstream.Event{
		SessionID: "session-1",
		Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: 1},
		Turn:      &agentstream.TurnRef{ID: "turn-1", Seq: 1},
		At:        time.Now().UTC(),
	}
	tool := base
	tool.Body = agentstream.ToolChanged{
		Phase: agentstream.ToolPhaseCompleted,
		Tool: agentstream.Tool{
			ID:        "tool-1",
			Content:   json.RawMessage(`{"text":"visible"}`),
			RawOutput: json.RawMessage(`{"token":"secret-provider-dump"}`),
		},
	}
	raw, err := agentstream.MarshalEvent(tool)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := agentstream.UnmarshalEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	gotTool := decoded.Body.(agentstream.ToolChanged)
	if len(gotTool.Tool.RawOutput) != 0 || string(gotTool.Tool.Content) != `{"text":"visible"}` {
		t.Fatalf("durable tool payload = %#v", gotTool.Tool)
	}

	permission := base
	permission.Body = agentstream.PermissionRequested{Request: agentstream.PermissionRequest{
		ID:      "permission-1",
		Details: json.RawMessage(`{"raw_prompt":"secret-provider-dump"}`),
		ToolCall: &agentstream.PermissionToolCall{
			ID:       "tool-1",
			Name:     "shell",
			RawInput: json.RawMessage(`{"token":"secret-tool-input"}`),
		},
	}}
	raw, err = agentstream.MarshalEvent(permission)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = agentstream.UnmarshalEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	gotPermission := decoded.Body.(agentstream.PermissionRequested).Request
	if details := gotPermission.Details; len(details) != 0 {
		t.Fatalf("durable permission details = %s, want omitted", details)
	}
	if gotPermission.ToolCall == nil || gotPermission.ToolCall.ID != "tool-1" || gotPermission.ToolCall.Name != "shell" {
		t.Fatalf("durable permission tool call = %#v, want stable fields preserved", gotPermission.ToolCall)
	}
	if rawInput := gotPermission.ToolCall.RawInput; len(rawInput) != 0 {
		t.Fatalf("durable permission raw input = %s, want omitted", rawInput)
	}
	original := permission.Body.(agentstream.PermissionRequested).Request.ToolCall.RawInput
	if string(original) != `{"token":"secret-tool-input"}` {
		t.Fatalf("MarshalEvent mutated live permission raw input: %s", original)
	}
}
