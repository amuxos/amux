package hubweb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/gorilla/websocket"
)

func TestTurnProcessLinkRejectsSessionAndExpiredTokens(t *testing.T) {
	const secret = "viewer-secret"
	server := httptest.NewServer(New(&fakeBackend{}, secret, "test").Handler())
	defer server.Close()
	now := time.Now()
	session := SignSessionToken(secret, "target", "session", now.Add(time.Hour))
	valid := SignTurnProcessToken(secret, "target", "session", "turn-2", false, false, now.Add(time.Hour))
	expired := SignTurnProcessToken(secret, "target", "session", "turn-2", false, false, now.Add(-time.Hour))
	for _, path := range []string{
		"/t/" + session, "/t/" + expired, "/t/" + valid + "x",
		"/api/turn/stream?token=" + session, "/api/session?token=" + valid,
		"/api/session/stream?token=" + valid,
		"/api/session?target_id=target&name=another&token=" + valid,
		"/s/" + valid,
	} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s got status %d, want unauthorized", strings.Split(path, "?")[0], resp.StatusCode)
		}
	}
	for _, tt := range []struct {
		lang, title, progress string
		hidden, expanded      bool
	}{
		{"zh-CN", "处理过程", "思考进展", false, false},
		{"en-US", "Processing details", "Thinking progress", false, true},
		{"de-DE", "Processing details", "", true, false},
	} {
		token := SignTurnProcessToken(secret, "target", "session", "turn-2", tt.hidden, tt.expanded, now.Add(time.Hour))
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/t/"+token, nil)
		req.Header.Set("Accept-Language", tt.lang)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<h1>"+tt.title+"</h1>") {
			t.Fatalf("failed to render %s: status %d, err %v", tt.lang, resp.StatusCode, err)
		}
		details := `<details id="progress"`
		if strings.Contains(string(body), details) == tt.hidden ||
			strings.Contains(string(body), details+" open>") != tt.expanded ||
			(!tt.hidden && !strings.Contains(string(body), "<summary>"+tt.progress)) {
			t.Fatalf("incorrect thinking section for %s", tt.lang)
		}
		if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("capability page must not be cached or forwarded as a referrer")
		}
	}
}

func TestTurnProcessReplaysAllCallsAndStopsAtExactTurn(t *testing.T) {
	const turnID = "turn-2"
	events := []*pb.AttachEvent{
		{TurnId: "turn-1", EventType: "tool_started", ToolId: "previous", ToolTitle: "previous secret"},
		{TurnId: "turn-1", EventType: "thinking_delta", Text: "previous thinking secret"},
		{TurnId: turnID, EventType: "turn_started", Text: "prompt secret"},
		{TurnId: turnID, EventType: "thinking_delta", Text: "Verifying the queue"},
		{TurnId: turnID, EventType: "thinking_delta", Text: " and rollout"},
		{TurnId: turnID, EventType: "permission_requested", PermissionPayload: []byte("permission secret")},
	}
	for i := range 128 {
		id := fmt.Sprintf("check-%03d", i)
		events = append(events, &pb.AttachEvent{
			TurnId: turnID, EventType: "tool_started", ToolId: id, ToolTitle: "Query queue",
			ToolInputJson: []byte("tool input secret"), ToolOutputPreview: "tool output secret",
			CanonicalJson: []byte(`{"at":"2026-11-27T08:00:00Z"}`),
		}, &pb.AttachEvent{TurnId: turnID, EventType: "tool_completed", ToolId: id})
	}
	events = append(events,
		&pb.AttachEvent{TurnId: turnID, EventType: "thinking_delta", Text: "Checking completion"},
		&pb.AttachEvent{TurnId: turnID, EventType: "assistant_text_delta", Text: "answer secret"},
		&pb.AttachEvent{TurnId: turnID, EventType: "turn_finished", State: "turn_done"},
		&pb.AttachEvent{TurnId: "turn-3", EventType: "tool_started", ToolId: "later", ToolTitle: "later secret"})
	for _, toolsOnly := range []bool{false, true} {
		backend := &fakeBackend{events: events}
		server := httptest.NewServer(New(backend, "viewer-secret", "test").Handler())
		token := SignTurnProcessToken("viewer-secret", "one-target", "one-session", turnID, toolsOnly, false, time.Now().Add(time.Hour))
		endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/turn/stream?" + url.Values{
			"token": {token}, "target_id": {"other-target"}, "name": {"other-session"}, "turn_id": {"turn-3"},
		}.Encode()
		conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var calls, updates int
		var terminal string
		var progress []turnProcessProgress
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "another") {
				t.Errorf("sensitive data in Turn frame: %s", raw)
			}
			var update turnProcessUpdate
			if err := json.Unmarshal(raw, &update); err != nil {
				t.Fatal(err)
			}
			if update.Progress != nil {
				progress = append(progress, *update.Progress)
			}
			if update.Status != "" {
				terminal = update.Status
			}
			if tool := update.Tool; tool != nil {
				updates++
				if tool.Phase == "started" {
					if tool.ID != fmt.Sprintf("check-%03d", calls) || tool.At == 0 {
						t.Errorf("unexpected call at %d: %+v", calls, tool)
					}
					calls++
				}
			}
		}
		conn.Close()
		server.Close()
		if calls != 128 || updates != 256 || terminal != "done" {
			t.Fatalf("toolsOnly=%v calls=%d updates=%d status=%q", toolsOnly, calls, updates, terminal)
		}
		wantProgress := []turnProcessProgress{
			{Index: 1, Text: "Verifying the queue"},
			{Index: 1, Text: "Verifying the queue and rollout"},
			{Index: 2, Text: "Checking completion"},
		}
		if toolsOnly {
			wantProgress = nil
		}
		if !slices.Equal(progress, wantProgress) {
			t.Fatalf("toolsOnly=%v progress=%+v, want %+v", toolsOnly, progress, wantProgress)
		}
	}
}
