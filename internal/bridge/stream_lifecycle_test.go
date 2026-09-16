package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/agentui"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

type lifecycleAttach struct {
	ctx    context.Context
	events chan *pb.AttachEvent
	end    chan struct{}
}

type lifecycleStreamHub struct {
	mu      sync.Mutex
	targets []*pb.Target // Snapshots are immutable once published, as in Hub.
	started chan *lifecycleAttach
	release chan struct{}
}

func (h *lifecycleStreamHub) ListTargets() []*pb.Target {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.targets
}

func (h *lifecycleStreamHub) setSession(chat *LarkChat, id, launchID string) *pb.Session {
	session := &pb.Session{Id: id, LaunchId: launchID, Name: chat.SessionName,
		State: "running", AgentType: chat.AgentType, AgentProfile: chat.AgentProfile}
	h.mu.Lock()
	h.targets = []*pb.Target{{Id: chat.TargetID, Sessions: []*pb.Session{session}}}
	h.mu.Unlock()
	return session
}

func (h *lifecycleStreamHub) Route(context.Context, string, string, interface{}) (*pb.TunnelMsg, error) {
	return &pb.TunnelMsg{}, nil
}

func (h *lifecycleStreamHub) RouteStream(ctx context.Context, _ string, _ string, _ interface{}, emit func(*pb.TunnelMsg) error) error {
	s := &lifecycleAttach{ctx: ctx, events: make(chan *pb.AttachEvent, 16), end: make(chan struct{})}
	h.started <- s
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.end:
			return nil
		case <-h.release:
			return nil
		case ev := <-s.events:
			payload, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if err := emit(&pb.TunnelMsg{Payload: payload}); err != nil {
				return err
			}
		}
	}
}

func newStreamLifecycleBridge(t *testing.T) (*Bridge, *lifecycleStreamHub, *LarkChat) {
	t.Helper()
	loadBridgeTestConfig(t, "")
	b, _ := newBridgeForInputTest(t)
	hub := &lifecycleStreamHub{started: make(chan *lifecycleAttach, 32), release: make(chan struct{})}
	b.hub = hub
	chat := &LarkChat{BridgeId: b.currentBridgeId(), ChatID: "chat-1", RootMsgID: "root-1",
		TargetID: "target-1", SessionName: "session-1", AgentType: "codex", Status: ChatStatusActive}
	if err := b.db.UpsertChat(*chat); err != nil {
		t.Fatal(err)
	}
	hub.setSession(chat, "session-id-1", "launch-1")
	t.Cleanup(func() {
		// End Attach normally so the Bridge drains its event queue and closes
		// senders before the shared DB/config/mock server are torn down.
		b.streamMu.Lock()
		var streams []context.Context
		for _, stream := range b.streamSessions {
			streams = append(streams, stream.ctx)
		}
		b.streamMu.Unlock()
		close(hub.release)
		for _, ctx := range streams {
			waitLifecycleSignal(t, ctx.Done())
		}
	})
	return b, hub, chat
}

func waitLifecycleSignal[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream/card operation")
		var zero T
		return zero
	}
}

func TestStreamStartupReusesConcurrentBindingAndSessionAdded(t *testing.T) {
	for _, singleton := range []bool{false, true} {
		t.Run(fmt.Sprintf("singleton=%t", singleton), func(t *testing.T) {
			b, hub, chat := newStreamLifecycleBridge(t)
			session := hub.ListTargets()[0].Sessions[0]
			start := func() { b.ensureThreadStream(chat) }
			root := chat.RootMsgID
			if singleton {
				seedSingletonBotSettings(t, b, "builtin/codex", chat.SessionName)
				bs, err := b.db.GetBotSession(b.currentBridgeId())
				if err != nil {
					t.Fatal(err)
				}
				start = func() { b.startSingletonStream(bs, "") }
				root = singletonStreamKey(b.currentBridgeId())
			}
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					start()
					b.OnSessionAdded(chat.TargetID, "target", session)
				}()
			}
			wg.Wait()
			stream := waitLifecycleSignal(t, hub.started)
			b.streamMu.Lock()
			token, ready := b.streamTokens[root], b.streamReady[root]
			b.streamMu.Unlock()
			if token != 1 || stream.ctx.Err() != nil {
				t.Fatalf("concurrent startup replaced the stream: token=%d err=%v", token, stream.ctx.Err())
			}
			// Reusing a stream also preserves its handshake and discovered skills.
			b.observeAvailableCommands(chat.TargetID, chat.SessionName, json.RawMessage(`[{"name":"review","kind":"skill"}]`))
			start()
			b.OnSessionAdded(chat.TargetID, "target", session)
			b.streamMu.Lock()
			sameReady := b.streamReady[root] == ready
			b.streamMu.Unlock()
			if !sameReady || len(b.advertisedSkills(chat.TargetID, chat.SessionName)) != 1 {
				t.Fatal("duplicate startup discarded readiness or available commands")
			}
			stream.events <- &pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT}
			waitLifecycleSignal(t, ready)
			select {
			case <-hub.started:
				t.Fatal("duplicate Attach was opened")
			default:
			}
		})
	}
}

func TestStreamStartupReattachesChangedOrEndedSession(t *testing.T) {
	for _, reason := range []string{"launch", "session", "root", "target", "ended", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			b, hub, chat := newStreamLifecycleBridge(t)
			b.startThreadStream(chat, "")
			first := waitLifecycleSignal(t, hub.started)
			switch reason {
			case "launch":
				session := hub.setSession(chat, "session-id-1", "launch-2")
				b.OnSessionAdded(chat.TargetID, "target", session)
			case "session":
				session := hub.setSession(chat, "session-id-2", "launch-2")
				b.OnSessionAdded(chat.TargetID, "target", session)
			case "root":
				chat.RootMsgID = "root-2"
				if err := b.db.UpsertChat(*chat); err != nil {
					t.Fatal(err)
				}
			case "target":
				chat.TargetID = "target-2"
				if err := b.db.UpsertChat(*chat); err != nil {
					t.Fatal(err)
				}
				hub.setSession(chat, "session-id-2", "launch-2")
			case "ended":
				close(first.end)
				waitLifecycleSignal(t, first.ctx.Done())
			case "cancelled":
				// Leave a cancelled registration in place while its worker exits.
				b.streamMu.Lock()
				b.streams[chat.RootMsgID]()
				b.streamMu.Unlock()
			}
			b.ensureThreadStream(chat)
			second := waitLifecycleSignal(t, hub.started)
			waitLifecycleSignal(t, first.ctx.Done())
			if second.ctx.Err() != nil {
				t.Fatal("replacement stream was cancelled by old stream cleanup")
			}
			b.streamMu.Lock()
			count := len(b.streamSessions)
			b.streamMu.Unlock()
			if count != 1 {
				t.Fatalf("registered session streams = %d, want one", count)
			}
		})
	}
}

func TestStreamStartupLearnsInitialIdentityWithoutReplacing(t *testing.T) {
	b, hub, chat := newStreamLifecycleBridge(t)
	hub.mu.Lock()
	hub.targets = nil // Run returned before the first snapshot arrived.
	hub.mu.Unlock()
	b.startThreadStream(chat, "")
	first := waitLifecycleSignal(t, hub.started)
	session := hub.setSession(chat, "session-id-1", "launch-1")
	b.OnSessionAdded(chat.TargetID, "target", session)
	if first.ctx.Err() != nil {
		t.Fatal("initial identity discovery replaced the live stream")
	}
	// Learned identity must still detect a later real restart.
	latest := hub.setSession(chat, "session-id-1", "launch-2")
	b.OnSessionAdded(chat.TargetID, "target", latest)
	second := waitLifecycleSignal(t, hub.started)
	b.OnSessionAdded(chat.TargetID, "target", session) // Delayed old callback.
	if second.ctx.Err() != nil {
		t.Fatal("stale session-added callback replaced the current launch")
	}
}

func TestSessionAddedKeepsWaitingCardThroughResult(t *testing.T) {
	// Provider presentation hooks are immutable. Isolate a generic extension
	// to exercise the real waiting-card path without a distribution dependency.
	const childEnv = "AMUX_TEST_WAITING_CARD_CHILD"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSessionAddedKeepsWaitingCardThroughResult$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated waiting-card test: %v\n%s", err, output)
		}
		return
	}
	if err := RegisterExtension(Extension{
		ActivityPossiblyQueued: func(agent string, activity *agentui.ActivityView) bool {
			return agent == "custom" && activity != nil && activity.CurrentStep == "" && len(activity.Tools) == 0
		},
		ReplyCardAgent: func(agent string) bool { return agent == "custom" },
	}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var creates, operations []string
	created, finished := make(chan string, 8), make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode API request: %v", err)
		}
		raw, _ := json.Marshal(body)
		mu.Lock()
		operations = append(operations, r.URL.Path+" "+string(raw))
		mu.Unlock()
		switch {
		case r.URL.Path == "/open-apis/cardkit/v1/cards":
			mu.Lock()
			creates = append(creates, string(raw))
			id := fmt.Sprintf("card-%d", len(creates))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"card_id": id}})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": "reply-1"}})
			created <- body["content"].(string)
		case strings.HasPrefix(r.URL.Path, "/open-apis/cardkit/v1/cards/"):
			_, _ = w.Write([]byte(`{"code":0}`))
			if strings.HasSuffix(r.URL.Path, "/settings") {
				finished <- r.URL.Path
			}
		default:
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	b, hub, chat := newStreamLifecycleBridge(t)
	b.client = lark.NewClient("test-waiting-app", "test-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	chat.AgentType = "custom"
	if err := b.db.UpsertChat(*chat); err != nil {
		t.Fatal(err)
	}
	session := hub.setSession(chat, "session-id-1", "launch-1")
	b.startThreadStream(chat, "")
	stream := waitLifecycleSignal(t, hub.started)
	var seq uint64
	emit := func(channel string, body agentstream.Body) {
		t.Helper()
		seq++
		raw, err := agentstream.MarshalEvent(agentstream.Event{
			SessionID: session.Id, Cursor: agentstream.Cursor{Epoch: "epoch-1", Seq: seq},
			Turn: &agentstream.TurnRef{ID: "turn-1", Seq: 1}, At: time.Now(),
			Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: channel, Provider: "custom", LaunchID: session.LaunchId},
			Body:   body,
		})
		if err != nil {
			t.Fatal(err)
		}
		stream.events <- &pb.AttachEvent{CanonicalJson: raw}
	}
	emit("lark", agentstream.TurnStarted{InputSummary: "Investigate this alert"})
	if got := waitLifecycleSignal(t, created); !strings.Contains(got, "card-1") {
		t.Fatalf("waiting message = %s", got)
	}
	// The exact incident order: input opens a waiting card, then the Hub's
	// asynchronous Added callback arrives before the first provider response.
	b.OnSessionAdded(chat.TargetID, "target", session)
	b.startThreadStream(chat, "")
	if stream.ctx.Err() != nil {
		t.Fatal("session-added callback cancelled the stream owning the waiting card")
	}
	emit("agent", agentstream.ToolChanged{Phase: agentstream.ToolPhaseStarted,
		Tool: agentstream.Tool{ID: "tool-1", Name: "inspect", Title: "Inspect alert"}})
	emit("agent", agentstream.AssistantMessage{Text: "Inspecting the alert"})
	emit("agent", agentstream.ResultObserved{Text: "The alert is resolved."})
	emit("agent", agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}})
	if got := waitLifecycleSignal(t, finished); got != "/open-apis/cardkit/v1/cards/card-1/settings" {
		t.Fatalf("finished card = %s, want original waiting card", got)
	}
	close(stream.end)
	waitLifecycleSignal(t, stream.ctx.Done())
	mu.Lock()
	defer mu.Unlock()
	if len(creates) != 1 || len(created) != 0 {
		t.Fatalf("cards=%d extra messages=%d, want one card and one message", len(creates), len(created))
	}
	if !strings.Contains(creates[0], "等待模型响应") || !strings.Contains(creates[0], "Investigate this alert") {
		t.Fatalf("initial card lost its question/waiting state: %s", creates[0])
	}
	var resultOnOriginalCard bool
	for _, operation := range operations {
		if strings.Contains(operation, "/card-1/") && strings.Contains(operation, "The alert is resolved.") {
			resultOnOriginalCard = true
		}
	}
	if !resultOnOriginalCard {
		t.Fatal("final result did not update the original waiting card")
	}
}
