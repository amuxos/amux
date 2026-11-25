package bridge

import (
	"context"
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

func TestGoalPermissionRequestKeepsPrivateDestinationAfterGroupQuery(t *testing.T) {
	b, h, _, _ := goalBridgeFixture(t)
	bs := BotSession{BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "t1", SessionName: "s1", AgentType: "codex", LastReplyKind: botSessionReplyDM, LastReplyOpenID: "owner", LastReplyChatID: "dm-original"}
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	origin := b.botSessionChat(&bs, botSessionInput{OpenID: "owner", ChatID: "dm-original", ReplyKind: botSessionReplyDM})
	if err := b.handleGoalCommand(context.Background(), origin, "create", "owner", "goal private task"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.SetBotSessionLastReply(b.currentBridgeId(), botSessionReplyGroup, "group-later", "owner", "query"); err != nil {
		t.Fatal(err)
	}
	queried := b.botSessionChat(&bs, botSessionInput{OpenID: "owner", ChatID: "group-later", ReplyKind: botSessionReplyGroup})
	if err := b.handleGoalCommand(context.Background(), queried, "query", "owner", "goal"); err != nil {
		t.Fatal(err)
	}
	pinned, err := b.goalChatForTurn(queried, agentstream.Event{SessionID: "session-1", Origin: agentstream.Origin{Channel: "goal", NativeType: "goal_continuation"}, Body: agentstream.TurnStarted{InputSummary: h.goal.Objective}})
	if err != nil || pinned == nil {
		t.Fatalf("missing pinned turn: %v", err)
	}
	var messages int
	var messageMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Query().Get("receive_id_type") != "open_id" || body["receive_id"] != "owner" {
			t.Errorf("private permission leaked: URL=%s body=%v", r.URL, body)
		}
		messageMu.Lock()
		messages++
		messageMu.Unlock()
		_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"permission-message"}}`))
	}))
	defer server.Close()
	b.client = lark.NewClient(t.Name(), "secret", lark.WithOpenBaseUrl(server.URL))
	if err := b.handlePermissionEvent(context.Background(), pinned, &agentui.PermissionEvent{RequestID: "permission-1"}); err != nil {
		t.Fatal(err)
	}
	messageMu.Lock()
	defer messageMu.Unlock()
	if messages != 1 {
		t.Fatalf("sent %d permission cards", messages)
	}
}

func TestTailAttachRestoresRepliesAndPermissions(t *testing.T) {
	for _, scenario := range []string{"topic", "singleton", "wrong-session", "wrong-origin", "wrong-objective", "feishu-turn", "missing-receipt", "wrong-receipt-binding"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			var recipients []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				raw, _ := json.Marshal(body)
				mu.Lock()
				bodies = append(bodies, string(raw))
				if recipient, ok := body["receive_id"].(string); ok {
					recipients = append(recipients, recipient)
				}
				mu.Unlock()
				_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"reply","card_id":"card"}}`))
			}))
			t.Cleanup(server.Close)
			b, hub, chat := newStreamLifecycleBridge(t)
			b.client = lark.NewClient(t.Name(), "secret", lark.WithOpenBaseUrl(server.URL))
			b.secrets = &Secrets{OpenId: "owner"}
			chat.OpenId, chat.AgentType = "owner", "codex"
			if err := b.db.UpsertChat(*chat); err != nil {
				t.Fatal(err)
			}
			destination := &goalDestination{Generation: "generation", SessionID: "session-id-1", Objective: "private goal", Chat: *chat}
			var bs BotSession
			if scenario == "singleton" {
				bs = BotSession{BridgeId: b.currentBridgeId(), Enabled: true, TargetID: chat.TargetID, SessionName: chat.SessionName, AgentType: chat.AgentType, LastReplyKind: botSessionReplyGroup, LastReplyChatID: "group-later", LastReplyOpenID: "owner"}
				if err := b.db.SetBotSession(bs); err != nil {
					t.Fatal(err)
				}
				destination.Chat = *b.botSessionChat(&bs, botSessionInput{OpenID: "owner", ChatID: "dm-original", ReplyKind: botSessionReplyDM})
				destination.Direct = &directReplyTarget{Kind: botSessionReplyDM, ChatID: "dm-original", OpenID: "owner"}
			}
			if err := b.db.saveGoalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName, destination); err != nil {
				t.Fatal(err)
			}
			// The native turn already started before this fresh Bridge attaches.
			started := agentstream.Event{SessionID: "session-id-1", Cursor: agentstream.Cursor{Epoch: "epoch", Seq: 1}, At: time.Now(), Turn: &agentstream.TurnRef{ID: "active-goal", Seq: 4}, Origin: agentstream.Origin{Channel: "goal", NativeType: "goal_continuation"}, Body: agentstream.TurnStarted{InputSummary: destination.Objective}}
			switch scenario {
			case "wrong-session":
				started.SessionID = "replacement-session"
			case "wrong-origin":
				started.Origin.NativeType = "unrelated-native-turn"
			case "wrong-objective":
				started.Body = agentstream.TurnStarted{InputSummary: "another goal"}
			case "feishu-turn", "missing-receipt", "wrong-receipt-binding":
				started.Origin = agentstream.Origin{Actor: agentstream.ActorUser, Channel: "lark", LaunchID: "launch-1"}
				started.Turn.ID = "feishu-turn"
				started.Body = agentstream.TurnStarted{InputSummary: "original Feishu question"}
				if scenario != "missing-receipt" {
					link := TurnLink{BridgeID: chat.BridgeId, RootMsgID: chat.RootMsgID, MessageID: "input-1", OpenID: "owner",
						TargetID: chat.TargetID, SessionName: chat.SessionName, SessionID: started.SessionID, LaunchID: "launch-1", TurnID: started.Turn.ID}
					if scenario == "wrong-receipt-binding" {
						link.RootMsgID = "other-topic"
					}
					if err := b.db.RecordTurnLinks([]TurnLink{link}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario == "singleton" {
				b.startSingletonStream(&bs, "owner")
			} else {
				b.startThreadStream(chat, "")
			}
			stream := waitLifecycleSignal(t, hub.started)
			raw, err := agentstream.MarshalEvent(started)
			if err != nil {
				t.Fatal(err)
			}
			stream.events <- &pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT, CaughtUp: true, ActiveTurnStartJson: raw}
			for index, body := range []agentstream.Body{
				agentstream.PermissionRequested{Request: agentstream.PermissionRequest{ID: "restored-permission", Options: []agentstream.PermissionOption{{ID: "allow", Name: "Allow", Kind: "allow_once"}}}},
				agentstream.ResultObserved{Text: "Recovered turn result"},
				agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}},
			} {
				event := started
				event.Cursor.Seq = uint64(index + 2)
				event.Body, event.Origin = body, agentstream.Origin{Channel: "agent"}
				raw, err := agentstream.MarshalEvent(event)
				if err != nil {
					t.Fatal(err)
				}
				stream.events <- &pb.AttachEvent{CanonicalJson: raw}
			}
			deadline := time.Now().Add(3 * time.Second)
			for len(stream.events) > 0 {
				if time.Now().After(deadline) {
					t.Fatal("mock Attach did not deliver all events")
				}
				time.Sleep(time.Millisecond)
			}
			close(stream.end)
			waitLifecycleSignal(t, stream.ctx.Done())
			mu.Lock()
			defer mu.Unlock()
			all := strings.Join(bodies, "\n")
			allowed := scenario == "topic" || scenario == "singleton" || scenario == "feishu-turn"
			if !allowed {
				if len(bodies) != 0 {
					t.Fatalf("invalid recovery authorized output: %v", bodies)
				}
				return
			}
			if !strings.Contains(all, "Recovered turn result") || !strings.Contains(all, "restored-permission") {
				t.Fatalf("recovered turn output/permission suppressed: %s", all)
			}
			if scenario == "singleton" {
				for _, recipient := range recipients {
					if recipient != "owner" {
						t.Error(fmt.Sprintf("recovered goal leaked to %s", recipient))
					}
				}
			}
		})
	}
}
