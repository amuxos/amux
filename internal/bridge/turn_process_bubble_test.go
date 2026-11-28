package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestTurnProcessBubblePostsNativeEventsInOriginalTopic(t *testing.T) {
	type appendBody struct {
		turnBubbleIDs
		Events []turnBubbleEvent `json:"events"`
	}
	published := make(chan appendBody, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.URL.Path == turnBubblePath && r.Method == http.MethodPost:
			var dest turnBubbleDestination
			err := json.NewDecoder(r.Body).Decode(&dest)
			if want := (turnBubbleDestination{ReceiveID: "oc-topic", OriginMessageID: "om_topic", ReplyInThread: true}); dest != want || err != nil {
				t.Errorf("native message destination = %+v, %v", dest, err)
			}
			if r.URL.Query().Get("receive_id_type") != "chat_id" || r.Header.Get("Authorization") != "Bearer tenant-token" {
				t.Error("native message must use the same bot tenant identity")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"cot_id":"ct-one","message_id":"om-bubble"}}`))
		case r.URL.Path == turnBubblePath && r.Method == http.MethodPut:
			var body appendBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			published <- body
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Errorf("unexpected Feishu request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	b := &Bridge{client: lark.NewClient("bubble-test-app", "secret", lark.WithOpenBaseUrl(server.URL))}
	chat := &LarkChat{ChatID: "oc-topic", RootMsgID: "om_topic"}
	bubble := b.newTurnProcessBubble(chat, "session-one", "turn-one")
	if bubble == nil {
		t.Fatal("native topic bubble not configured")
	}
	bubble.locale = i18n.English
	ctx := context.Background()
	view := &agentui.ActivityView{
		TurnID: "turn-one", Status: "running", CurrentStep: "Checking the test configuration",
		Tools: []agentui.ToolEvent{{ID: "read", Name: "Read", Title: "Read config", Status: "running", InputJSON: json.RawMessage(`{"token":"private-argument"}`)}},
	}
	bubble.observe(ctx, view, true, false)
	if !bubble.confirmed.Load() {
		t.Fatal("could not activate native bubble")
	}
	view.Tools[0].Status = "failed"
	view.Tools[0].Error = "private-diagnostic"
	view.Tools[0].OutputPreview = "private-output"
	bubble.finish(ctx, view, "done", false)
	close(published)
	var kinds []string
	var payload strings.Builder
	for batch := range published {
		if batch.COTID != "ct-one" || batch.MessageID != "om-bubble" {
			t.Fatalf("append used wrong bubble: %+v", batch.turnBubbleIDs)
		}
		for _, event := range batch.Events {
			kinds = append(kinds, event.Type)
			payload.WriteString(event.Content)
		}
	}
	want := []string{"RUN_STARTED", "REASONING_START", "REASONING_MESSAGE_START", "REASONING_MESSAGE_CONTENT", "REASONING_MESSAGE_END", "TOOL_CALL_START", "TOOL_CALL_END", "TOOL_CALL_RESULT", "REASONING_END", "RUN_FINISHED"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("native events = %v, want %v", kinds, want)
	}
	got := payload.String()
	if !strings.Contains(got, "Checking the test configuration") || !strings.Contains(got, "Read config") ||
		!strings.Contains(got, "Failed") || strings.Contains(got, "private-") {
		t.Fatalf("incorrect native event payload: %s", got)
	}
}

func TestTurnProcessBubbleFallsBackOnRejectedAppend(t *testing.T) {
	closed := make(chan bool, 1)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.URL.Path == turnBubblePath && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"code":0,"data":{"cot_id":"ct-one","message_id":"om-bubble"}}`))
		case r.URL.Path == turnBubblePath && r.Method == http.MethodPut:
			requests++
			if requests == 1 {
				_, _ = w.Write([]byte(`{"code":0}`))
			} else {
				_, _ = w.Write([]byte(`{"code":230001,"msg":"rejected"}`))
			}
		case r.URL.Path == turnBubblePath+"/complete/ct-one" && r.Method == http.MethodPost:
			closed <- r.URL.Query().Get("reason") == "error" && r.URL.Query().Get("message_id") == "om-bubble"
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Errorf("unexpected Feishu request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	b := &Bridge{client: lark.NewClient("bubble-reject-test-app", "secret", lark.WithOpenBaseUrl(server.URL))}
	bubble := b.newTurnProcessBubble(&LarkChat{ChatID: "oc-topic", RootMsgID: "om_topic"}, "session", "turn")
	if bubble == nil {
		t.Fatal("native topic bubble not configured")
	}
	view := &agentui.ActivityView{TurnID: "turn", Status: "running", CurrentStep: "provider progress", Tools: []agentui.ToolEvent{{ID: "read", Name: "Read", Status: "running"}}}
	ctx := context.Background()
	bubble.observe(ctx, view, true, true)
	if !bubble.confirmed.Load() || bubble.thought != "" {
		t.Fatal("successful tool-only bubble should respect hidden thinking")
	}
	meta := toolActivityCardMeta{
		Status: "running", AgentType: "codex", EffectiveModel: "gpt", Activity: view, ProcessInBubble: bubble.confirmed.Load(),
	}
	native := renderToolActivityCard(meta, "")
	if !strings.Contains(native, cardKitExecutionSummaryID) || strings.Contains(native, "provider progress") ||
		strings.Contains(native, "Read") || strings.Contains(native, "sidebar-semi") {
		t.Fatal("native mode should keep the main card summary and move process details to the bubble")
	}

	view.Tools[0].Status = "completed"
	bubble.finish(ctx, view, "done", true)
	bubble.observe(ctx, view, true, true)
	meta.ProcessInBubble = bubble.confirmed.Load()
	if meta.ProcessInBubble || !strings.Contains(renderToolActivityCard(meta, ""), "Read") || !bubble.stopped {
		t.Fatal("a failed native update must restore inline details and stop retrying on the same turn")
	}
	select {
	case matched := <-closed:
		if !matched {
			t.Fatal("fallback did not settle the correct native message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fallback did not settle the partially published bubble")
	}
}
