package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/pkg/acptui"
	"google.golang.org/grpc/metadata"
)

func TestAgentTypeForAttachMatchesSandboxWhenProvided(t *testing.T) {
	sessions := []*sessionEntry{
		{Session: pbSession("same", "claude"), TargetID: "sb1"},
		{Session: pbSession("same", "codex"), TargetID: "sb2"},
	}

	sess, ok := sessionEntryForAttach(sessions, "sb2", "same")
	if !ok {
		t.Fatal("sessionEntryForAttach() ok = false, want true")
	}
	if sess.AgentType != "codex" {
		t.Fatalf("sessionEntryForAttach().AgentType = %q, want %q", sess.AgentType, "codex")
	}
}

func TestAgentTypeForAttachMatchesSessionID(t *testing.T) {
	sessions := []*sessionEntry{
		{Session: pb.Session{Id: "session-id", Name: "friendly", AgentType: "codex"}},
	}

	sess, ok := sessionEntryForAttach(sessions, "", "session-id")
	if !ok {
		t.Fatal("sessionEntryForAttach() ok = false, want true")
	}
	if sess.AgentType != "codex" {
		t.Fatalf("sessionEntryForAttach().AgentType = %q, want %q", sess.AgentType, "codex")
	}
}

func TestSessionEntryForAttachSupportsTargetSlashName(t *testing.T) {
	sessions := []*sessionEntry{
		{Session: pb.Session{Name: "work", AgentType: "codex"}, TargetID: "sb1", TargetName: "devbox"},
	}
	got, ok := sessionEntryForAttach(sessions, "", "devbox/work")
	if !ok {
		t.Fatal("sessionEntryForAttach() ok = false")
	}
	if got.Name != "work" || got.TargetName != "devbox" {
		t.Fatalf("got %+v", got)
	}
}

func TestAttachOutputEventFieldsNormalizesLegacySourceFrames(t *testing.T) {
	tests := []struct {
		name       string
		ev         *pb.AttachEvent
		wantData   string
		wantSource string
		wantInput  bool
	}{
		{
			name:       "legacy lark frame",
			ev:         &pb.AttachEvent{Data: []byte("\r\n📱 来自飞书: hello\r\n")},
			wantData:   "hello",
			wantSource: "lark",
			wantInput:  true,
		},
		{
			name:       "legacy desktop frame",
			ev:         &pb.AttachEvent{Data: []byte("\r\n🖥️ 来自桌面: hello\r\n")},
			wantData:   "hello",
			wantSource: "desktop",
			wantInput:  true,
		},
		{
			name:       "structured event passes through",
			ev:         &pb.AttachEvent{Source: "agent", Data: []byte("hello\n")},
			wantData:   "hello\n",
			wantSource: "agent",
			wantInput:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, source, input := attachOutputEventFields(tt.ev)
			if string(data) != tt.wantData || source != tt.wantSource || input != tt.wantInput {
				t.Fatalf("attachOutputEventFields() = (%q, %q, %v), want (%q, %q, %v)",
					data, source, input, tt.wantData, tt.wantSource, tt.wantInput)
			}
		})
	}
}

type fakeAttachClient struct {
	events []*pb.AttachEvent
}

func (f *fakeAttachClient) Recv() (*pb.AttachEvent, error) {
	if len(f.events) == 0 {
		return nil, io.EOF
	}
	ev := f.events[0]
	f.events = f.events[1:]
	return ev, nil
}
func (f *fakeAttachClient) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeAttachClient) Trailer() metadata.MD         { return nil }
func (f *fakeAttachClient) CloseSend() error             { return nil }
func (f *fakeAttachClient) Context() context.Context     { return context.Background() }
func (f *fakeAttachClient) SendMsg(interface{}) error    { return nil }
func (f *fakeAttachClient) RecvMsg(interface{}) error    { return nil }

func TestAmuxAgentStreamMapsTurnDoneStateChange(t *testing.T) {
	stream := &amuxAgentStream{stream: &fakeAttachClient{events: []*pb.AttachEvent{{
		Kind:       pb.AttachEvent_STATE_CHANGE,
		State:      "turn_done",
		StopReason: "cancelled",
	}}}}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != acptui.OutputKindTurnDone || ev.StopReason != "cancelled" {
		t.Fatalf("event = %+v, want turn done cancelled", ev)
	}
}

// TestAmuxAgentStreamMapsStructuredToolEvent asserts the v0.1.32 fix: a
// structured tool AttachEvent (Kind=OUTPUT, EventType=tool_started, empty
// Data) is dispatched to OutputKindToolEvent instead of being dropped by the
// old len(Data)==0 guard.
func TestAmuxAgentStreamMapsStructuredToolEvent(t *testing.T) {
	stream := &amuxAgentStream{stream: &fakeAttachClient{events: []*pb.AttachEvent{{
		Kind:          pb.AttachEvent_OUTPUT,
		EventType:     "tool_started",
		ToolName:      "command",
		ToolTitle:     "ls -la",
		ToolStatus:    "running",
		ToolInputJson: []byte(`{"command":"ls -la"}`),
	}}}}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != acptui.OutputKindToolEvent || ev.ToolEvent == nil {
		t.Fatalf("event = %+v, want OutputKindToolEvent", ev)
	}
	if ev.ToolEvent.Name != "command" || ev.ToolEvent.Title != "ls -la" || ev.ToolEvent.Status != "running" {
		t.Fatalf("tool event = %+v, want command/ls -la/running", ev.ToolEvent)
	}
	if string(ev.ToolEvent.Input) != `{"command":"ls -la"}` {
		t.Fatalf("tool input = %q, want command json", string(ev.ToolEvent.Input))
	}
}

func TestAmuxAgentStreamMapsThinkingDelta(t *testing.T) {
	stream := &amuxAgentStream{stream: &fakeAttachClient{events: []*pb.AttachEvent{{
		Kind:      pb.AttachEvent_OUTPUT,
		EventType: "thinking_delta",
		Text:      "thinking about the plan",
	}}}}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != acptui.OutputKindThinkingDelta || ev.ThinkingDelta != "thinking about the plan" {
		t.Fatalf("event = %+v, want thinking_delta", ev)
	}
}

// TestAmuxAgentStreamMapsUsageEvent asserts a structured usage AttachEvent
// (event_type=usage, UsageJson from the codex tokenUsage mapping) is
// dispatched to OutputKindUsage instead of being dropped by the data guard.
func TestAmuxAgentStreamMapsUsageEvent(t *testing.T) {
	stream := &amuxAgentStream{stream: &fakeAttachClient{events: []*pb.AttachEvent{{
		Kind:      pb.AttachEvent_OUTPUT,
		EventType: "usage",
		UsageJson: []byte(`{"input_tokens":100,"output_tokens":50,"total_tokens":150,"cached_input_tokens":80,"cache_creation_input_tokens":10,"reasoning_output_tokens":20,"context_window":200000}`),
	}}}}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != acptui.OutputKindUsage || ev.Usage == nil {
		t.Fatalf("event = %+v, want OutputKindUsage", ev)
	}
	u := ev.Usage
	if u.InputTokens != 100 || u.OutputTokens != 50 || u.TotalTokens != 150 ||
		u.CachedInputTokens != 80 || u.CacheCreationInputTokens != 10 ||
		u.ReasoningOutputTokens != 20 || u.ContextWindow != 200000 {
		t.Fatalf("usage = %+v, want all fields parsed", u)
	}
}

// TestAmuxAgentStreamDropsUnparseableUsage asserts a usage event without a
// parseable payload is dropped (not surfaced as an empty usage update).
func TestAmuxAgentStreamDropsUnparseableUsage(t *testing.T) {
	stream := &amuxAgentStream{stream: &fakeAttachClient{events: []*pb.AttachEvent{
		{Kind: pb.AttachEvent_OUTPUT, EventType: "usage", UsageJson: []byte(`not-json`)},
		{Kind: pb.AttachEvent_OUTPUT, EventType: "usage"},
	}}}

	for i := 0; i < 2; i++ {
		if _, err := stream.Recv(); err != io.EOF {
			t.Fatalf("event %d: expected EOF (dropped), got err=%v", i, err)
		}
	}
}

// TestAmuxAgentStreamMapsTopLevelError asserts the v0.1.38 fix: a top-level
// provider error (event_type=error, empty Data, ErrorMessage set) is rendered
// as a visible line-mode data frame instead of being dropped.
func TestAmuxAgentStreamMapsTopLevelError(t *testing.T) {
	stream := &amuxAgentStream{stream: &fakeAttachClient{events: []*pb.AttachEvent{{
		Kind:         pb.AttachEvent_OUTPUT,
		EventType:    "error",
		ErrorMessage: "model unavailable",
	}}}}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != acptui.OutputKindData {
		t.Fatalf("kind = %v, want OutputKindData", ev.Kind)
	}
	if !strings.Contains(string(ev.Data), "model unavailable") || !strings.Contains(string(ev.Data), "[error]") {
		t.Fatalf("data = %q, want [error] frame containing the message", string(ev.Data))
	}
}

// TestAmuxAgentStreamMapsAttachEventError asserts an AttachEvent.Kind=ERROR
// event is rendered as a visible data frame (v0.1.38).
func TestAmuxAgentStreamMapsAttachEventError(t *testing.T) {
	stream := &amuxAgentStream{stream: &fakeAttachClient{events: []*pb.AttachEvent{{
		Kind:         pb.AttachEvent_ERROR,
		ErrorMessage: "turn failed",
	}}}}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != acptui.OutputKindData {
		t.Fatalf("kind = %v, want OutputKindData", ev.Kind)
	}
	if !strings.Contains(string(ev.Data), "turn failed") {
		t.Fatalf("data = %q, want the error message", string(ev.Data))
	}
}

func pbSession(name, agentType string) pb.Session {
	return pb.Session{Name: name, AgentType: agentType}
}

// TestAmuxAgentStreamMapsUsageSpeedFields asserts ttft_ms/tokens_per_sec from
// the codex usage payload survive the acptui mapping.
func TestAmuxAgentStreamMapsUsageSpeedFields(t *testing.T) {
	stream := &amuxAgentStream{stream: &fakeAttachClient{events: []*pb.AttachEvent{{
		Kind:      pb.AttachEvent_OUTPUT,
		EventType: "usage",
		UsageJson: []byte(`{"total_tokens":150,"ttft_ms":1230,"tokens_per_sec":45}`),
	}}}}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Usage == nil {
		t.Fatalf("usage event = %+v, want usage payload", ev)
	}
	if ev.Usage.TTFTMs != 1230 || ev.Usage.TokensPerSec != 45 {
		t.Fatalf("usage speed fields = %+v, want ttft=1230 tps=45", ev.Usage)
	}
}
