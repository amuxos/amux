package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/spf13/viper"
)

func TestGroupSingletonOutputStaysInGroupAcrossRetirement(t *testing.T) {
	b, _, _ := groupRouteTestBridge(t)
	settings := saveGroupRouteTestSettings(t, b, "group-a", config.FeishuInteractionSingleton)
	chat := &LarkChat{BridgeId: b.currentBridgeId(), ChatID: "group-a", RootMsgID: "first-input", TargetID: "local", SessionName: "shared", Status: ChatStatusActive}
	if err := b.db.UpsertChat(*chat); err != nil {
		t.Fatal(err)
	}
	if err := b.db.SetGroupSessionRoot(chat.BridgeId, chat.ChatID, settings.Version, chat.RootMsgID); err != nil {
		t.Fatal(err)
	}
	ordinary := *chat
	ordinary.RootMsgID, ordinary.SessionName = "old-topic", "ordinary"
	if err := b.db.UpsertChat(ordinary); err != nil {
		t.Fatal(err)
	}
	var outputs []string
	var wantThread bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if wantThread {
			if r.URL.Path != "/open-apis/im/v1/messages/old-topic/reply" || body["reply_in_thread"] != true {
				t.Errorf("ordinary topic output moved: path=%s body=%v", r.URL.Path, body)
			}
		} else if r.Method != http.MethodPost || r.URL.Path != "/open-apis/im/v1/messages" || r.URL.Query().Get("receive_id_type") != "chat_id" || body["receive_id"] != "group-a" || body["reply_in_thread"] != nil {
			t.Errorf("group singleton output is not a direct group message: %s %s body=%v", r.Method, r.URL, body)
		}
		id := fmt.Sprintf("output-%d", len(outputs))
		outputs = append(outputs, id)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": id}})
	}))
	defer server.Close()
	b.client = lark.NewClient("test-app", "test-secret", lark.WithOpenBaseUrl(server.URL))
	b.threadNotice = nil
	ctx := context.Background()
	sends := []struct {
		name string
		send func() error
	}{
		{"text", func() error { _, _, err := b.sendTextToBinding(ctx, chat, "notice"); return err }},
		{"answer", func() error { _, _, err := b.sendAgentReplyToBinding(ctx, chat, "answer"); return err }},
		{"activity", func() error {
			_, _, err := b.sendToolActivityToBinding(ctx, chat, toolActivityCardMeta{}, "progress")
			return err
		}},
		{"cardkit", func() error {
			_, _, err := b.sendCardKitReplyToBinding(ctx, chat, renderCardKitMessageContent("card"), "uuid")
			return err
		}},
		{"child", func() error {
			_, _, err := b.sendInteractiveToBinding(ctx, chat, renderAgentReplyCard("child"), "child")
			return err
		}},
		{"permission", func() error {
			_, _, err := b.sendPermissionRequestCard(ctx, chat.RootMsgID, chat.TargetID, chat.SessionName, "permission", permissionPayload{})
			return err
		}},
		{"artifact", func() error { return b.sendArtifactToBinding(ctx, chat, "file", "file-key", "artifact-uuid") }},
		{"artifact-failure", func() error { return b.notifyArtifactFailure(ctx, chat, "failed") }},
		{"control", func() error { return b.notifyThread(ctx, chat.RootMsgID, "queue status") }},
		{"help", func() error { return b.notifyThreadHelpCard(ctx, chat.RootMsgID, renderBoundTopicHelpCard()) }},
	}
	for _, phase := range []string{"active", "retired", "experiment-disabled"} {
		if phase == "retired" {
			if err := b.newGroupConversation(chat.ChatID, settings.Version, "owner"); err != nil {
				t.Fatal(err)
			}
		}
		if phase == "experiment-disabled" {
			viper.Set("bridge.feishu.group_settings.enabled", false)
		}
		for _, send := range sends {
			t.Run(phase+"/"+send.name, func(t *testing.T) {
				before := len(outputs)
				if err := send.send(); err != nil {
					t.Fatal(err)
				}
				if len(outputs) != before+1 {
					t.Fatalf("sent %d messages", len(outputs)-before)
				}
				origin, err := b.db.FindGroupSessionOrigin(chat.BridgeId, chat.ChatID, outputs[before])
				if err != nil || origin != chat.RootMsgID {
					t.Fatalf("output reply lost conversation identity: origin=%q err=%v", origin, err)
				}
			})
		}
	}
	wantThread = true
	if _, _, err := b.sendAgentReplyToBinding(ctx, &ordinary, "old topic"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*LarkChat){
		func(c *LarkChat) { c.ChatID = "group-b" },
		func(c *LarkChat) { c.SessionName = "replacement" },
	} {
		stale := *chat
		change(&stale)
		if _, _, err := b.sendTextToBinding(ctx, &stale, "wrong destination"); err == nil || !strings.Contains(err.Error(), "stale group singleton") {
			t.Fatalf("stale binding not rejected: %v", err)
		}
	}
}

func TestGroupSingletonReplyToDirectOutputReusesSession(t *testing.T) {
	b, hub, notices := groupRouteTestBridge(t)
	settings := saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionSingleton)
	routeGroupTestInput(t, b, "a", "first", "first")
	chat, err := b.db.FindByRootMsg(b.currentBridgeId(), "first")
	if err != nil || chat == nil {
		t.Fatalf("chat=%v err=%v", chat, err)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: chat.SessionName, State: "running"}}}
	b.chatNotice = func(context.Context, string, string) (string, string, error) { return "direct-output", "", nil }
	if _, _, err := b.sendTextToBinding(context.Background(), chat, "answer"); err != nil {
		t.Fatal(err)
	}
	b.setTurnActive("first", false)
	if err := b.routeToSessionInChatWithMention(context.Background(), "a", "direct-output", "reply", "owner", "continue", botMentionInfo{Mentioned: true}); err != nil {
		t.Fatal(err)
	}
	if runs := groupTestRuns(hub); len(runs) != 1 {
		t.Fatalf("reply created %d sessions", len(runs))
	}
	var submits []*pb.SubmitRequest
	for _, call := range hub.routes {
		if call.method == "Submit" {
			submits = append(submits, call.payload.(*pb.SubmitRequest))
		}
	}
	if len(submits) != 2 || submits[0].Name != submits[1].Name {
		t.Fatalf("reply did not continue the session: %+v notices=%v", submits, *notices)
	}
	if len(*notices) != 0 {
		t.Fatalf("unexpected topic redirect notice: %v", *notices)
	}
	if err := b.newGroupConversation("a", settings.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := b.routeToSessionInChatWithMention(context.Background(), "a", "direct-output", "late-reply", "owner", "continue", botMentionInfo{}); err != nil {
		t.Fatal(err)
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0], "已结束接收新消息") {
		t.Fatalf("retired output did not retain retirement boundary: %v", *notices)
	}
}
