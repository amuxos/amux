package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/turnresult"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestTurnResultJSONAndBilingualRendering(t *testing.T) {
	readyResp := &pb.AmuxdGetTurnResultResponse{
		Status: turnresult.StatusReady,
		Result: &pb.TurnResultPayload{
			Version: 1,
			Ref: &pb.TurnResultRef{
				TargetId: "local", SessionId: "sess-1", SessionName: "ops", TurnId: "turn-1",
			},
			Source: &pb.TurnResultSource{
				Kind: turnresult.SourceSchedule, ScheduleId: 8, ExecutionId: 42,
				ScheduleType: "session-prompt", RunMode: "reuse-session",
			},
			Execution: &pb.TurnResultExecution{Status: turnresult.ExecutionCompleted, StartedAt: 100, FinishedAt: 110},
			Output:    &pb.TurnResultOutput{Status: turnresult.OutputAvailable, Format: "markdown", Content: "All checks green."},
			Deliveries: []*pb.TurnResultDelivery{{
				Channel: "feishu", Status: turnresult.DeliveryDelivered, BridgeId: "bot-1", ChatId: "oc_1", RootMessageId: "om_root",
				Messages: []*pb.TurnResultDeliveryMessage{
					{Kind: turnresult.MessageKindPrimary, MessageId: "om_reply_1"},
					{Kind: turnresult.MessageKindAttachment, MessageId: "om_file_1", Name: "report.md"},
				},
			}},
		},
	}

	raw, err := json.Marshal(turnResultJSONEnvelope(readyResp))
	if err != nil {
		t.Fatalf("marshal json envelope: %v", err)
	}
	if !strings.Contains(string(raw), `"schema":"amux.turn.result.v1"`) ||
		!strings.Contains(string(raw), `"content":"All checks green."`) ||
		!strings.Contains(string(raw), `"message_id":"om_file_1"`) {
		t.Fatalf("unexpected json envelope: %s", raw)
	}

	// Verify both English and Simplified Chinese rendering for ready, disabled, not_found, and not_ready states.
	for _, tc := range []struct {
		locale i18n.Locale
		want   []string
	}{
		{locale: i18n.English, want: []string{"Status: ready", "Session: local/ops", "Turn: turn-1", "Deliveries: feishu=delivered", "All checks green."}},
		{locale: i18n.Chinese, want: []string{"状态: ready", "会话: local/ops", "轮次: turn-1", "投递: feishu=delivered", "All checks green."}},
	} {
		var buf bytes.Buffer
		renderTurnResultText(&buf, tc.locale, readyResp)
		out := buf.String()
		for _, sub := range tc.want {
			if !strings.Contains(out, sub) {
				t.Fatalf("locale %s ready output missing %q:\n%s", tc.locale, sub, out)
			}
		}
	}

	disabledResp := &pb.AmuxdGetTurnResultResponse{Status: turnresult.StatusUnavailable, Reason: turnresult.ReasonDisabled}
	for _, tc := range []struct {
		locale i18n.Locale
		want   string
	}{
		{locale: i18n.English, want: "turn_results.enabled true"},
		{locale: i18n.Chinese, want: "单轮结果功能未开启"},
	} {
		var buf bytes.Buffer
		renderTurnResultText(&buf, tc.locale, disabledResp)
		if !strings.Contains(buf.String(), tc.want) {
			t.Fatalf("locale %s disabled output = %q, want %q", tc.locale, buf.String(), tc.want)
		}
	}

	for _, resp := range []*pb.AmuxdGetTurnResultResponse{
		{Status: turnresult.StatusNotFound},
		{Status: turnresult.StatusNotReady},
		{Status: turnresult.StatusUnavailable, Reason: turnresult.ReasonResultNotSettled},
	} {
		for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
			var buf bytes.Buffer
			renderTurnResultText(&buf, locale, resp)
			if strings.TrimSpace(buf.String()) == "" || strings.Contains(buf.String(), "turn.result.") {
				t.Fatalf("locale %s status %s rendered raw key or empty: %q", locale, resp.GetStatus(), buf.String())
			}
		}
	}
}

func TestMCPToolsIncludeTurnResultTools(t *testing.T) {
	tools := amuxTools()
	byName := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = struct{}{}
	}
	for _, name := range []string{"amux.turn_result_get", "amux.schedule_result_get"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing MCP tool %s", name)
		}
	}
}
