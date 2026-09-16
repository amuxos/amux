package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestFetchTopicRootMessageGetsAndNormalizesPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/im/v1/messages/om-root":
			if got := r.URL.Query().Get("user_id_type"); got != "open_id" {
				t.Fatalf("user_id_type = %q, want open_id", got)
			}
			content, _ := json.Marshal(map[string]any{
				"title": "任务背景",
				"content": [][]map[string]string{{
					{"tag": "text", "text": "请让 "},
					{"tag": "at", "user_id": "ou-alice", "user_name": "Alice"},
					{"tag": "text", "text": " 修复登录问题"},
				}},
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"items": []map[string]any{{
						"message_id": "om-root",
						"thread_id":  "omt-topic",
						"msg_type":   "post",
						"chat_id":    "oc-topic",
						"deleted":    false,
						"sender": map[string]string{
							"id":          "ou-author",
							"id_type":     "open_id",
							"sender_type": "user",
						},
						"body": map[string]string{"content": string(content)},
					}},
				},
			})
		default:
			t.Fatalf("unexpected lark API call: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	b := &Bridge{
		client: lark.NewClient(
			"app-id",
			"app-secret",
			lark.WithOpenBaseUrl(server.URL),
			lark.WithOAuthBaseUrl(server.URL),
		),
	}
	got, err := b.fetchTopicRootMessage(context.Background(), "om-root")
	if err != nil {
		t.Fatalf("fetchTopicRootMessage: %v", err)
	}
	if got.ChatID != "oc-topic" || got.MessageID != "om-root" || got.ThreadID != "omt-topic" ||
		got.ActorType != "user" || got.ActorID != "ou-author" {
		t.Fatalf("topic root = %+v, want normalized identity fields", got)
	}
	if got.Text != "任务背景\n请让 @Alice 修复登录问题" {
		t.Fatalf("topic root text = %q, want normalized mention", got.Text)
	}
}

func TestFetchTopicRootMessageAcceptsAppSender(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/im/v1/messages/om-agent-root":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"items": []map[string]any{{
						"message_id": "om-agent-root",
						"thread_id":  "omt-topic",
						"msg_type":   "text",
						"chat_id":    "oc-topic",
						"deleted":    false,
						"sender": map[string]string{
							"id":          "cli-agent",
							"id_type":     "app_id",
							"sender_type": "app",
						},
						"body": map[string]string{"content": `{"text":"智能体生成的任务背景"}`},
					}},
				},
			})
		default:
			t.Fatalf("unexpected lark API call: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	b := &Bridge{
		client: lark.NewClient(
			"app-id",
			"app-secret",
			lark.WithOpenBaseUrl(server.URL),
			lark.WithOAuthBaseUrl(server.URL),
		),
	}
	got, err := b.fetchTopicRootMessage(context.Background(), "om-agent-root")
	if err != nil {
		t.Fatalf("fetchTopicRootMessage: %v", err)
	}
	if got.ActorType != "app" || got.ActorID != "cli-agent" || got.Text != "智能体生成的任务背景" {
		t.Fatalf("topic root = %+v, want app-authored root content", got)
	}
}

func TestFetchQuotedMessageExtractsInteractiveCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/im/v1/messages/om-card":
			if got := r.URL.Query().Get("card_msg_content_type"); got != "user_card_content" {
				t.Fatalf("card_msg_content_type = %q, want user_card_content", got)
			}
			content := renderAgentReplyCard("引用卡片里的最终结论")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"items": []map[string]any{{
					"message_id": "om-card",
					"msg_type":   "interactive",
					"chat_id":    "oc-topic",
					"deleted":    false,
					"sender": map[string]string{
						"id": "cli-agent", "sender_type": "app",
					},
					"body": map[string]string{"content": content},
				}}},
			})
		default:
			t.Fatalf("unexpected lark API call: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	b := &Bridge{client: lark.NewClient(
		"app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL),
	)}
	got, err := b.fetchQuotedMessage(context.Background(), "om-card")
	if err != nil {
		t.Fatalf("fetchQuotedMessage: %v", err)
	}
	if got.MessageID != "om-card" || got.ChatID != "oc-topic" || got.MsgType != "interactive" ||
		got.ActorType != "app" || got.Text != "引用卡片里的最终结论" {
		t.Fatalf("quoted message = %+v, want normalized interactive card", got)
	}
	if strings.Contains(got.Text, "Amux") {
		t.Fatalf("quoted card text includes bridge footer: %q", got.Text)
	}
}

func TestFetchQuotedMessageRejectsDeletedMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/im/v1/messages/om-deleted":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"items": []map[string]any{{
					"message_id": "om-deleted", "msg_type": "text", "chat_id": "oc-topic",
					"deleted": true, "body": map[string]string{"content": `{"text":"已撤回"}`},
				}}},
			})
		default:
			t.Fatalf("unexpected lark API call: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	b := &Bridge{client: lark.NewClient(
		"app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL),
	)}
	if _, err := b.fetchQuotedMessage(context.Background(), "om-deleted"); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("fetchQuotedMessage error = %v, want deleted rejection", err)
	}
}
