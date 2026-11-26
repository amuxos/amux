package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/agentui"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

type placeholderHarness struct {
	t          *testing.T
	mu         sync.Mutex
	creates    []string
	operations []string
	messages   []string // non-card messages (text notices)
	created    chan string
	finished   chan string
	stream     *lifecycleAttach
	session    *pb.Session
	seq        uint64
	bridge     *Bridge
	chat       *LarkChat
}

func newPlaceholderHarness(t *testing.T) *placeholderHarness {
	t.Helper()
	h := &placeholderHarness{t: t, created: make(chan string, 16), finished: make(chan string, 16)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		h.mu.Lock()
		h.operations = append(h.operations, r.URL.Path+" "+string(raw))
		h.mu.Unlock()
		switch {
		case r.URL.Path == "/open-apis/cardkit/v1/cards":
			h.mu.Lock()
			h.creates = append(h.creates, string(raw))
			id := fmt.Sprintf("card-%d", len(h.creates))
			h.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"card_id": id}})
		case strings.HasSuffix(r.URL.Path, "/reply") || strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages"):
			content, _ := body["content"].(string)
			if !strings.Contains(content, "card_id") {
				h.mu.Lock()
				h.messages = append(h.messages, content)
				h.mu.Unlock()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": "reply-1"}})
			h.created <- content
		case strings.HasPrefix(r.URL.Path, "/open-apis/cardkit/v1/cards/"):
			_, _ = w.Write([]byte(`{"code":0}`))
			if strings.HasSuffix(r.URL.Path, "/settings") {
				h.finished <- r.URL.Path
			}
		default:
			_, _ = w.Write([]byte(`{"code":0}`))
		}
	}))
	t.Cleanup(server.Close)
	b, hub, chat := newStreamLifecycleBridge(t)
	h.bridge, h.chat = b, chat
	b.client = lark.NewClient("test-placeholder-app", "test-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	h.session = hub.setSession(chat, "session-id-1", "launch-1")
	b.startThreadStream(chat, "")
	h.stream = waitLifecycleSignal(t, hub.started)
	return h
}

func (h *placeholderHarness) emit(turnID string, turnSeq uint64, channel string, body agentstream.Body) {
	h.t.Helper()
	h.seq++
	raw, err := agentstream.MarshalEvent(agentstream.Event{
		SessionID: h.session.Id, Cursor: agentstream.Cursor{Epoch: "epoch-1", Seq: h.seq},
		Turn: &agentstream.TurnRef{ID: turnID, Seq: turnSeq}, At: time.Now(),
		Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: channel, Provider: "codex", LaunchID: h.session.LaunchId},
		Body:   body,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.stream.events <- &pb.AttachEvent{CanonicalJson: raw}
}

func (h *placeholderHarness) cardOps(cardID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, op := range h.operations {
		if strings.Contains(op, "/"+cardID+"/") {
			out = append(out, op)
		}
	}
	return out
}

func (h *placeholderHarness) cardCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.creates)
}

func (h *placeholderHarness) waitOp(t *testing.T, cardID, needle string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, op := range h.cardOps(cardID) {
			if strings.Contains(op, needle) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.mu.Lock()
	msgs := h.messages
	all := h.operations
	h.mu.Unlock()
	t.Fatalf("no %s update containing %q; ops=%v msgs=%v all=%d", cardID, needle, h.cardOps(cardID), msgs, len(all))
}

func TestPlaceholderCardPostedAtTurnStartAndReusedForResult(t *testing.T) {
	h := newPlaceholderHarness(t)
	h.emit("turn-1", 1, "lark", agentstream.TurnStarted{InputSummary: "帮我看看这个告警"})
	// The card appears before any model output.
	if got := waitLifecycleSignal(t, h.created); !strings.Contains(got, "card-1") {
		t.Fatalf("placeholder message = %s", got)
	}
	h.mu.Lock()
	first := h.creates[0]
	h.mu.Unlock()
	if !strings.Contains(first, "处理中") || !strings.Contains(first, "帮我看看这个告警") || !strings.Contains(first, "placeholder_status") {
		t.Fatalf("placeholder card lacks waiting line/question: %s", first)
	}
	if strings.Contains(first, "amux") && strings.Contains(first, "协调说明") {
		t.Fatalf("question leaks protocol: %s", first)
	}
	h.emit("turn-1", 1, "agent", agentstream.AssistantMessage{Text: "正在检查"})
	h.emit("turn-1", 1, "agent", agentstream.ResultObserved{Text: "告警已恢复。"})
	h.emit("turn-1", 1, "agent", agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}})
	if got := waitLifecycleSignal(t, h.finished); got != "/open-apis/cardkit/v1/cards/card-1/settings" {
		t.Fatalf("finished card = %s", got)
	}
	h.waitOp(t, "card-1", "告警已恢复")
	if n := h.cardCount(); n != 1 {
		t.Fatalf("cards = %d, want exactly one for the turn", n)
	}
	ops := h.cardOps("card-1")
	last := ""
	for _, op := range ops {
		if strings.Contains(op, cardKitActivityContentID) {
			last = op
		}
	}
	if strings.Contains(last, "placeholder_status") {
		t.Fatalf("final activity still shows the waiting line: %s", last)
	}
}

func TestPlaceholderShowsErrorBeforeOutput(t *testing.T) {
	h := newPlaceholderHarness(t)
	h.emit("turn-1", 1, "lark", agentstream.TurnStarted{InputSummary: "hi"})
	waitLifecycleSignal(t, h.created)
	h.emit("turn-1", 1, "agent", agentstream.Diagnostic{Severity: agentstream.SeverityError, Message: "model overloaded"})
	h.emit("turn-1", 1, "agent", agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeError, Message: "model overloaded"}})
	waitLifecycleSignal(t, h.finished)
	h.waitOp(t, "card-1", "❌")
	if n := h.cardCount(); n != 1 {
		t.Fatalf("cards = %d", n)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.messages) != 0 {
		t.Fatalf("error must update the waiting card, not post a separate notice: %v", h.messages)
	}
}

func TestPlaceholderShowsInterrupt(t *testing.T) {
	h := newPlaceholderHarness(t)
	h.emit("turn-1", 1, "lark", agentstream.TurnStarted{InputSummary: "hi"})
	waitLifecycleSignal(t, h.created)
	h.emit("turn-1", 1, "agent", agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted}})
	waitLifecycleSignal(t, h.finished)
	h.waitOp(t, "card-1", "已中断")
	t.Logf("ops=%v", h.cardOps("card-1"))
	if n := h.cardCount(); n != 1 {
		t.Fatalf("cards = %d", n)
	}
}

func TestSubtaskTurnGetsNoPlaceholder(t *testing.T) {
	h := newPlaceholderHarness(t)
	h.emit("turn-1", 1, "subtask", agentstream.TurnStarted{InputSummary: "[后台任务完成] task_id=t1"})
	time.Sleep(400 * time.Millisecond)
	if n := h.cardCount(); n != 0 {
		t.Fatalf("subtask turn opened a placeholder card")
	}
	h.emit("turn-1", 1, "agent", agentstream.ResultObserved{Text: "后台结果"})
	h.emit("turn-1", 1, "agent", agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}})
	waitLifecycleSignal(t, h.finished)
	if n := h.cardCount(); n != 1 {
		t.Fatalf("cards = %d, want 1 (agent text only)", n)
	}
	for _, op := range h.cardOps("card-1") {
		if strings.Contains(op, "placeholder_status") {
			t.Fatalf("subtask card shows waiting line: %s", op)
		}
	}
}

func TestQueuedTurnGetsPlaceholderOnlyWhenItStarts(t *testing.T) {
	h := newPlaceholderHarness(t)
	h.emit("turn-1", 1, "lark", agentstream.TurnStarted{InputSummary: "first"})
	waitLifecycleSignal(t, h.created)
	// Steered/queued input produces no turn_started: no new card while
	// turn-1 keeps streaming.
	h.emit("turn-1", 1, "agent", agentstream.AssistantMessage{Text: "working"})
	time.Sleep(300 * time.Millisecond)
	if n := h.cardCount(); n != 1 {
		t.Fatalf("cards = %d before queued turn starts", n)
	}
	h.emit("turn-1", 1, "agent", agentstream.ResultObserved{Text: "done one"})
	h.emit("turn-1", 1, "agent", agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}})
	waitLifecycleSignal(t, h.finished)
	h.emit("turn-2", 2, "lark", agentstream.TurnStarted{InputSummary: "second"})
	waitLifecycleSignal(t, h.created)
	if n := h.cardCount(); n != 2 {
		t.Fatalf("cards = %d, want one per turn", n)
	}
	h.mu.Lock()
	second := h.creates[1]
	h.mu.Unlock()
	if !strings.Contains(second, "second") || !strings.Contains(second, "处理中") {
		t.Fatalf("queued turn placeholder = %s", second)
	}
}

func TestTurnPlaceholderStateMachine(t *testing.T) {
	now := time.Unix(1000, 0)
	p := &turnPlaceholder{now: func() time.Time { return now }}
	if p.Fail("x") {
		t.Fatal("fail without placeholder")
	}
	p.Begin()
	if p.RefreshDue(now.Add(5 * time.Second)) {
		t.Fatal("refresh too early")
	}
	if !p.RefreshDue(now.Add(10 * time.Second)) {
		t.Fatal("refresh due")
	}
	if !p.End() || p.End() {
		t.Fatal("end once")
	}
	if active, _ := p.State(); active || !p.Shown() {
		t.Fatal("state after end")
	}
	if p.Fail("x") {
		t.Fatal("fail after output must use normal notice")
	}
	if shouldShowTurnPlaceholder(agentui.SourceSubtask) || !shouldShowTurnPlaceholder(agentui.SourceFeishu) {
		t.Fatal("source gating")
	}
}

func TestRenderPlaceholderStatus(t *testing.T) {
	start := time.Unix(1000, 0)
	meta := toolActivityCardMeta{Status: "running", Placeholder: true, Now: start.Add(12 * time.Second),
		Activity: &agentui.ActivityView{Status: "running", StartedAt: start}}
	raw, _ := json.Marshal(renderTurnActivity(meta).contentElements())
	if !strings.Contains(string(raw), "处理中") || !strings.Contains(string(raw), "12") {
		t.Fatalf("running placeholder = %s", raw)
	}
	meta.Status, meta.PlaceholderError = "failed", "boom"
	raw, _ = json.Marshal(renderTurnActivity(meta).contentElements())
	if !strings.Contains(string(raw), "❌ boom") {
		t.Fatalf("failed placeholder = %s", raw)
	}
	meta.Status = "done"
	raw, _ = json.Marshal(renderTurnActivity(meta).contentElements())
	if strings.Contains(string(raw), "placeholder_status") {
		t.Fatal("done card keeps waiting line")
	}
}
