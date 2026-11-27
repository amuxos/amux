package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/feishumsg"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func agentMessageBridge(t *testing.T) *Bridge {
	t.Helper()
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Bridge{db: db, bridgeID: "feishu:app-a", profile: "bot-a"}
}

func TestAgentMessageBindings(t *testing.T) {
	t.Parallel()
	b := agentMessageBridge(t)
	if err := b.db.UpsertChat(LarkChat{BridgeId: b.bridgeID, ChatID: "oc-a", RootMsgID: "om-a", TargetID: "target", SessionName: "topic"}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.SetBotSession(BotSession{BridgeId: b.bridgeID, Enabled: true, TargetID: "target", SessionName: "singleton"}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertDocumentCommentSession(DocumentCommentSession{BridgeID: b.bridgeID, FileToken: "doc", CommentID: "comment", TargetID: "target", SessionName: "document"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"topic", "singleton", "document"} {
		if bound, err := b.HasAgentMessageBinding("target", name); err != nil || !bound {
			t.Fatalf("%s binding = %v, %v", name, bound, err)
		}
		if bound, err := b.HasAgentMessageBinding("other-target", name); err != nil || bound {
			t.Fatalf("foreign target: %v, %v", bound, err)
		}
	}
	b.bridgeID = "feishu:other"
	if bound, err := b.HasAgentMessageBinding("target", "topic"); err != nil || bound {
		t.Fatalf("foreign bot: %v, %v", bound, err)
	}
	b.bridgeID = "feishu:app-a"
	if err := b.db.DeactivateChat(b.bridgeID, "om-a"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.DisableBotSession(b.bridgeID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"topic", "singleton", "missing"} {
		if bound, err := b.HasAgentMessageBinding("target", name); err != nil || bound {
			t.Fatalf("inactive %s: %v, %v", name, bound, err)
		}
	}
}

func TestSendAgentMessageUsesBoundTenant(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"", "text", "markdown"} {
		t.Run("format/"+format, func(t *testing.T) {
			testSendAgentMessageUsesBoundTenant(t, format)
		})
	}
}

func testSendAgentMessageUsesBoundTenant(t *testing.T, format string) {
	b := agentMessageBridge(t)
	if err := b.db.UpsertChat(LarkChat{BridgeId: b.bridgeID, ChatID: "oc-origin", RootMsgID: "om-origin", TargetID: "target", SessionName: "topic"}); err != nil {
		t.Fatal(err)
	}
	type sent struct {
		path, kind string
		body       map[string]any
	}
	var sends []sent
	apiResponse := `{"code":0,"data":{"message_id":"om-sent","chat_id":"oc-destination","thread_id":"omt-a"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["app_id"] != "agent-message-app" || body["app_secret"] != "bot-secret" {
				t.Errorf("wrong tenant: %v", body)
			}
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"bound-tenant-token","expire":7200}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer bound-tenant-token" {
			t.Errorf("wrong lookup/send credentials: %s %s", r.Method, r.URL)
		}
		if r.Method == "GET" {
			switch r.URL.Path {
			case "/open-apis/im/v1/chats/oc-destination":
				_, _ = w.Write([]byte(`{"code":0,"data":{"chat_mode":"group"}}`))
			case "/open-apis/im/v1/messages/om-original":
				_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"message_id":"om-original","chat_id":"oc-destination"}]}}`))
			default:
				t.Errorf("unexpected lookup: %s", r.URL)
			}
			return
		}
		if r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		sends = append(sends, sent{r.URL.Path, r.URL.Query().Get("receive_id_type"), body})
		_, _ = w.Write([]byte(apiResponse))
	}))
	defer server.Close()
	b.client = lark.NewClient("agent-message-app", "bot-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	msg := feishumsg.Message{ReceiveID: "oc-destination", ReceiveIDType: "chat_id", Text: "# 中文\n\n**bold** [link](https://example.com)\n\n- item\n\n```go\nfmt.Println(\"test\")\n```\n", Format: format, UUID: "retry-key"}
	for i := 0; i < 2; i++ {
		result, err := b.SendAgentMessage(context.Background(), "target", "topic", msg)
		if err != nil || result.MessageID != "om-sent" {
			t.Fatalf("send: %+v %v", result, err)
		}
	}
	if len(sends) != 2 || sends[0].kind != "chat_id" || sends[0].body["receive_id"] != "oc-destination" || sends[0].body["content"] != msg.Content() || sends[0].body["uuid"] != sends[1].body["uuid"] || sends[0].body["uuid"] == "" {
		t.Fatalf("requests: %+v", sends)
	}
	msg.ReceiveID = ""
	msg.ReceiveIDType = ""
	msg.ReplyToMessageID = "om-original"
	msg.ReplyInThread = true
	if _, err := b.SendAgentMessage(context.Background(), "target", "topic", msg); err != nil {
		t.Fatal(err)
	}
	if sends[2].path != "/open-apis/im/v1/messages/om-original/reply" || sends[2].body["reply_in_thread"] != true {
		t.Fatalf("reply: %+v", sends[2])
	}
	// Check the actual HTTP payload for both create and reply, independently of
	// Message.Content, so a missing format hop cannot silently send raw Markdown.
	for _, sent := range sends {
		content, ok := sent.body["content"].(string)
		if !ok {
			t.Fatalf("content is not a JSON string: %+v", sent)
		}
		if format == "markdown" {
			var post map[string]struct {
				Content [][]map[string]string `json:"content"`
			}
			if err := json.Unmarshal([]byte(content), &post); err != nil {
				t.Fatal(err)
			}
			blocks := post["zh_cn"].Content
			if sent.body["msg_type"] != "post" || len(blocks) != 1 || len(blocks[0]) != 1 || blocks[0][0]["tag"] != "md" || blocks[0][0]["text"] != msg.Text {
				t.Fatalf("invalid rich-text payload: %+v", sent)
			}
		} else {
			var text map[string]string
			if err := json.Unmarshal([]byte(content), &text); err != nil {
				t.Fatal(err)
			}
			if sent.body["msg_type"] != "text" || text["text"] != msg.Text {
				t.Fatalf("plain text changed: %+v", sent)
			}
		}
	}
	apiResponse = `{"code":230002,"msg":"bot is not in chat"}`
	if _, err := b.SendAgentMessage(context.Background(), "target", "topic", msg); err == nil || !strings.Contains(err.Error(), "230002") {
		t.Fatalf("API failure = %v", err)
	}
	apiResponse = `{"code":0,"data":{}}`
	if _, err := b.SendAgentMessage(context.Background(), "target", "topic", msg); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("empty response = %v", err)
	}
	count := len(sends)
	if err := b.db.DeactivateChat(b.bridgeID, "om-origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SendAgentMessage(context.Background(), "target", "topic", msg); err == nil {
		t.Fatal("detached binding sent")
	}
	if len(sends) != count {
		t.Fatal("API called after detach")
	}
}

func TestSendAgentMessageGroupBoundary(t *testing.T) {
	t.Parallel()
	const group = `{"code":0,"data":{"chat_mode":"group","chat_type":"private"}}`
	const parent = `{"code":0,"data":{"items":[{"message_id":"om-parent","chat_id":"oc-destination"}]}}`
	for _, tc := range []struct {
		name, chat, message, wantError string
	}{
		{"group", group, parent, ""},
		{"topic", `{"code":0,"data":{"chat_mode":"topic"}}`, parent, ""},
		{"private", `{"code":0,"data":{"chat_mode":"p2p"}}`, parent, "group_only"},
		{"unknown mode", `{"code":0,"data":{"chat_mode":"new-mode"}}`, parent, "destination_metadata"},
		{"missing mode", `{"code":0,"data":{"chat_type":"private"}}`, parent, "destination_metadata"},
		{"missing chat", `{"code":0}`, parent, "destination_metadata"},
		{"chat permission denied", `{"code":99991672,"msg":"permission denied"}`, parent, "destination_lookup"},
		{"chat transport failure", `invalid-json`, parent, "destination_lookup"},
		{"message permission denied", group, `{"code":99991672,"msg":"permission denied"}`, "destination_lookup"},
		{"message transport failure", group, `invalid-json`, "destination_lookup"},
		{"missing message data", group, `{"code":0}`, "destination_metadata"},
		{"empty messages", group, `{"code":0,"data":{"items":[]}}`, "destination_metadata"},
		{"null message", group, `{"code":0,"data":{"items":[null]}}`, "destination_metadata"},
		{"wrong message", group, `{"code":0,"data":{"items":[{"message_id":"om-other","chat_id":"oc-destination"}]}}`, "destination_metadata"},
		{"deleted message", group, `{"code":0,"data":{"items":[{"message_id":"om-parent","chat_id":"oc-destination","deleted":true}]}}`, "destination_metadata"},
		{"missing parent chat", group, `{"code":0,"data":{"items":[{"message_id":"om-parent"}]}}`, "destination_metadata"},
	} {
		for _, route := range []string{"create", "reply", "thread"} {
			if route == "create" && tc.message != parent {
				continue
			}
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				b := agentMessageBridge(t)
				if err := b.db.UpsertChat(LarkChat{BridgeId: b.bridgeID, ChatID: "oc-origin", RootMsgID: "om-origin", TargetID: "target", SessionName: "topic"}); err != nil {
					t.Fatal(err)
				}
				var sends, chatReads, messageReads int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
						_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"group-check-token","expire":7200}`))
					case r.Method == "GET" && r.URL.Path == "/open-apis/im/v1/chats/oc-destination":
						chatReads++
						_, _ = w.Write([]byte(tc.chat))
					case r.Method == "GET" && r.URL.Path == "/open-apis/im/v1/messages/om-parent":
						messageReads++
						_, _ = w.Write([]byte(tc.message))
					case r.Method == "POST":
						sends++
						_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om-sent"}}`))
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL)
					}
				}))
				defer server.Close()
				b.client = lark.NewClient("group-boundary-app", "secret", lark.WithOpenBaseUrl(server.URL))
				msg := feishumsg.Message{Text: "**group only**", Format: "markdown"}
				if route == "create" {
					msg.ReceiveID, msg.ReceiveIDType = "oc-destination", "chat_id"
				} else {
					msg.ReplyToMessageID = "om-parent"
					msg.ReplyInThread = route == "thread"
				}
				_, err := b.SendAgentMessage(context.Background(), "target", "topic", msg)
				if tc.wantError != "" {
					if err == nil || sends != 0 {
						t.Fatalf("unsafe destination: sends=%d, error=%v", sends, err)
					}
					// The formatted lookup diagnostic varies by SDK; verify its
					// bilingual wrapper while keeping the raw platform detail.
					if tc.wantError == "destination_lookup" {
						if !strings.Contains(err.Error(), "no message was sent") || !strings.Contains(err.Error(), "未发送消息") {
							t.Fatalf("missing localized lookup failure: %v", err)
						}
					} else if want := feishumsg.Copy(tc.wantError); err.Error() != want {
						t.Fatalf("error=%v, want=%s", err, want)
					}
				} else if err != nil || sends != 1 || chatReads != 1 || (route != "create" && messageReads != 1) {
					t.Fatalf("group send: sends=%d chatReads=%d messageReads=%d error=%v", sends, chatReads, messageReads, err)
				}
			})
		}
	}
}
