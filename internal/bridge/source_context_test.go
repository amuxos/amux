package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const sourceTestURL = "https://applink.feishu.cn/client/thread/open?open_chat_id=oc-source&open_thread_id=omt_source&openchatid=oc-source&openthreadid=omt_source&thread_position=-1"

func readSourceContext(t *testing.T, text string) feishuSourceContext {
	t.Helper()
	start := strings.Index(text, sourceContextStart)
	end := strings.Index(text, sourceContextEnd)
	if start < 0 || end < start {
		t.Fatalf("missing source context in %q", text)
	}
	body := text[start+len(sourceContextStart) : end]
	jsonStart := strings.IndexByte(body, '{')
	if jsonStart < 0 {
		t.Fatalf("missing JSON in %q", body)
	}
	var data feishuSourceContext
	if err := json.Unmarshal([]byte(body[jsonStart:]), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func sourceSubmittedRequest(t *testing.T, hub *bridgeTestHub) *pb.SubmitRequest {
	t.Helper()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for i := len(hub.routes) - 1; i >= 0; i-- {
		if req, ok := hub.routes[i].payload.(*pb.SubmitRequest); ok {
			return req
		}
	}
	t.Fatal("no submission recorded")
	return nil
}

func sourceTestMessage(id string) *larkim.Message {
	return larkim.NewMessageBuilder().MessageId(id).ChatId("oc-source").
		RootId("om-root").ThreadId("omt_source").Build()
}

func TestSourceContextValidatesExactMessage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*larkim.Message)
		err  string
	}{
		{name: "valid"},
		{name: "wrong message", edit: func(m *larkim.Message) { *m.MessageId = "om-other" }, err: "source_message_mismatch"},
		{name: "wrong chat", edit: func(m *larkim.Message) { *m.ChatId = "oc-other" }, err: "source_chat_mismatch"},
		{name: "wrong root", edit: func(m *larkim.Message) { *m.RootId = "om-other" }, err: "source_root_mismatch"},
		{name: "deleted", edit: func(m *larkim.Message) { v := true; m.Deleted = &v }, err: "source_message_deleted"},
		{name: "missing link"},
		{name: "missing thread", edit: func(m *larkim.Message) { m.ThreadId = nil }, err: "source_thread_unavailable"},
		{name: "invalid thread", edit: func(m *larkim.Message) { v := "om-not-thread"; m.ThreadId = &v }, err: "source_thread_unavailable"},
		{name: "invalid link", edit: func(m *larkim.Message) { v := "javascript:alert(1)"; m.MessageAppLink = &v }, err: "source_link_invalid"},
		{name: "relative link", edit: func(m *larkim.Message) { v := "/thread/open"; m.MessageAppLink = &v }, err: "source_link_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bridge{bridgeID: "feishu:test"}
			b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) {
				if id != "om-input" {
					t.Fatalf("looked up %q", id)
				}
				m := sourceTestMessage(id)
				if tc.edit != nil {
					tc.edit(m)
				}
				return m, nil
			}
			chat := &LarkChat{BridgeId: b.bridgeID, ChatID: "oc-source", RootMsgID: "om-root", TargetID: "target", SessionName: "session"}
			text := b.sourceContextInstructions(context.Background(), chat, compactTurnInputRefs("user", "om-input"))
			got := readSourceContext(t, text)
			if got.ConversationURLError != tc.err || (tc.err == "" && got.ConversationURL != sourceTestURL) || (tc.err != "" && got.ConversationURL != "") {
				t.Fatalf("source context = %+v", got)
			}
			if got.ChatID != "oc-source" || got.MessageID != "om-input" || got.RootMessageID != "om-root" || got.BridgeID != b.bridgeID || got.SessionName != "session" || got.TargetID != "target" {
				t.Fatalf("lost source identity: %+v", got)
			}
		})
	}
}

func TestSourceContextFirstMessageAndSingleton(t *testing.T) {
	t.Parallel()
	b := &Bridge{bridgeID: "feishu:test"}
	b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) {
		m := sourceTestMessage(id)
		m.RootId, m.ThreadId = nil, nil
		return m, nil
	}
	for _, root := range []string{"om-first", singletonStreamKey(b.bridgeID)} {
		chat := &LarkChat{BridgeId: b.bridgeID, ChatID: "oc-source", RootMsgID: root}
		got := readSourceContext(t, b.sourceContextInstructions(context.Background(), chat, compactTurnInputRefs("user", "om-first")))
		if got.RootMessageID != "om-first" || got.ConversationURL != "https://applink.feishu.cn/client/chat/open?openChatId=oc-source" || got.ThreadID != "" {
			t.Fatalf("first message context = %+v", got)
		}
	}
}

func TestSourceContextDoesNotBorrowBinding(t *testing.T) {
	t.Parallel()
	b := &Bridge{bridgeID: "feishu:test"}
	chat := &LarkChat{BridgeId: b.bridgeID, ChatID: "oc-source", RootMsgID: "om-old", ThreadID: "omt-old"}
	ctx := withFeishuSource(context.Background(), feishuSource{ChatID: "oc-source", RootMessageID: "om-root"})
	b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) { return sourceTestMessage(id), nil }
	got := readSourceContext(t, b.sourceContextInstructions(ctx, chat, compactTurnInputRefs("user", "om-input")))
	if got.RootMessageID != "om-root" || got.ThreadID != "omt_source" || got.ConversationURL != sourceTestURL {
		t.Fatalf("used output binding instead of input: %+v", got)
	}
	b.sourceMessageLookup = func(context.Context, string) (*larkim.Message, error) { return nil, errors.New("unavailable") }
	got = readSourceContext(t, b.sourceContextInstructions(ctx, chat, compactTurnInputRefs("user", "om-next")))
	if got.ConversationURL != "" || got.ThreadID != "" || got.RootMessageID != "om-root" || got.ConversationURLError != "source_lookup_failed" {
		t.Fatalf("reused a prior source: %+v", got)
	}
	chat.BridgeId = "feishu:other"
	got = readSourceContext(t, b.sourceContextInstructions(ctx, chat, compactTurnInputRefs("user", "om-next")))
	if got.ConversationURLError != "source_binding_mismatch" || got.ConversationURL != "" {
		t.Fatalf("accepted another Bot binding: %+v", got)
	}
}

func TestSourceContextLookupHasBoundedDeadline(t *testing.T) {
	t.Parallel()
	b := &Bridge{bridgeID: "feishu:test"}
	b.sourceMessageLookup = func(ctx context.Context, _ string) (*larkim.Message, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("source lookup has no bounded deadline")
		}
		return nil, context.DeadlineExceeded
	}
	got := readSourceContext(t, b.sourceContextInstructions(context.Background(), &LarkChat{ChatID: "oc-source", RootMsgID: "om-root"}, compactTurnInputRefs("user", "om-input")))
	if got.ConversationURLError != "source_lookup_failed" {
		t.Fatalf("context = %+v", got)
	}
}

func TestSourceContextHiddenAndLocalized(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	chat := &LarkChat{BridgeId: b.bridgeID, ChatID: "oc-source", RootMsgID: "om-root", OpenId: "user"}
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		if err := b.db.setManagementLocale(b.bridgeID, chat.OpenId, string(locale)); err != nil {
			t.Fatal(err)
		}
		block := b.sourceContextInstructions(context.Background(), chat, compactTurnInputRefs("user", "om-input"))
		readSourceContext(t, block)
		if strings.Contains(block, "session.source.context") {
			t.Fatal("raw translation key")
		}
		if locale == i18n.English && !strings.Contains(block, "source of this input") {
			t.Fatalf("missing English copy: %q", block)
		}
		if locale == i18n.Chinese && !strings.Contains(block, "本次输入来源") {
			t.Fatalf("missing Chinese copy: %q", block)
		}
		text := "hello" + block + artifactInstructions
		if got := stripAmuxGuidance(text); got != "hello" {
			t.Fatalf("guidance leaked: %q", got)
		}
		if got := queueListLine("1", "lark", text); strings.Contains(got, "source") || !strings.Contains(got, "hello") {
			t.Fatalf("queue leaked context: %q", got)
		}
	}
	if got := b.sourceContextInstructions(context.Background(), chat, nil); got != "" {
		t.Fatalf("non-message input has context: %q", got)
	}
}

func TestSourceContextIncludedInEverySubmit(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	chat := &LarkChat{BridgeId: b.bridgeID, ChatID: "oc-source", RootMsgID: "om-root", TargetID: "target", SessionName: "session"}
	b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) { return sourceTestMessage(id), nil }
	for _, foreground := range []bool{false, true} {
		b.steerForeground.Store(foreground)
		for _, mode := range []submitMode{{}, {steer: true}} {
			if _, err := b.submitToSessionMode(context.Background(), chat, "hello", mode, compactTurnInputRefs("user", "om-input")); err != nil {
				t.Fatal(err)
			}
			req := sourceSubmittedRequest(t, hub)
			if req.Text != "hello" || len(req.Parts) != 1 || req.Steer != mode.steer {
				t.Fatalf("submit changed: %+v", req)
			}
			got := readSourceContext(t, req.Parts[0].GetText())
			if got.ConversationURL != sourceTestURL || strings.Contains(req.SystemInstructions, sourceContextStart) {
				t.Fatalf("source lost or persisted in system instructions: %+v", req)
			}
		}
	}
}

func TestLegacySourceContextCoalescingKeepsFirstSource(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.coalesceDelay = time.Hour
	chat := &LarkChat{BridgeId: b.bridgeID, ChatID: "oc-source", RootMsgID: "om-anchor", TargetID: "target", SessionName: "session"}
	b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) {
		if id != "om-first" {
			t.Fatalf("coalescing looked up %q", id)
		}
		return sourceTestMessage(id), nil
	}
	first := withFeishuSource(context.Background(), feishuSource{ChatID: "oc-source", RootMessageID: "om-root"})
	second := withFeishuSource(context.Background(), feishuSource{ChatID: "oc-other", RootMessageID: "om-other"})
	b.enqueueCoalescedInputForMessage(first, chat, "first", "om-first", "user", nil)
	b.enqueueCoalescedInputForMessage(second, chat, "second", "om-second", "user", nil)
	b.flushCoalescedInput(chat.RootMsgID, b.coalesced[chat.RootMsgID])
	req := sourceSubmittedRequest(t, hub)
	got := readSourceContext(t, req.Parts[0].GetText())
	if req.Text != "first\nsecond" || got.ChatID != "oc-source" || got.RootMessageID != "om-root" || got.MessageID != "om-first" {
		t.Fatalf("coalesced source = %+v, request = %+v", got, req)
	}
}

func TestSourceContextThreadRootWithoutAPILink(t *testing.T) {
	t.Parallel()
	b := &Bridge{bridgeID: "feishu:test"}
	b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) {
		return larkim.NewMessageBuilder().MessageId(id).ChatId("oc-source").ThreadId("omt_source").Build(), nil
	}
	for _, root := range []string{"om-root", singletonStreamKey(b.bridgeID)} {
		chat := &LarkChat{BridgeId: b.bridgeID, ChatID: "oc-source", RootMsgID: root}
		got := readSourceContext(t, b.sourceContextInstructions(context.Background(), chat, compactTurnInputRefs("user", "om-root")))
		if got.RootMessageID != "om-root" || got.ThreadID != "omt_source" || got.ConversationURL != sourceTestURL || got.ConversationURLError != "" {
			t.Fatalf("thread root without API link = %+v", got)
		}
	}
}

func TestSourceContextPreservesOfficialLink(t *testing.T) {
	t.Parallel()
	const link = "https://applink.feishu.cn/client/thread/open?open_chat_id=oc-source&open_thread_id=omt_source&thread_position=1"
	b := &Bridge{bridgeID: "feishu:test"}
	b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) {
		m := sourceTestMessage(id)
		v := link
		m.MessageAppLink = &v
		return m, nil
	}
	got := readSourceContext(t, b.sourceContextInstructions(context.Background(), &LarkChat{ChatID: "oc-source", RootMsgID: "om-root"}, compactTurnInputRefs("user", "om-input")))
	if got.ConversationURL != link || got.ConversationURLError != "" {
		t.Fatalf("official link changed: %+v", got)
	}
}

func TestSourceContextKnownTopicDoesNotFallBackToChat(t *testing.T) {
	t.Parallel()
	b := &Bridge{bridgeID: "feishu:test"}
	b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) {
		return larkim.NewMessageBuilder().MessageId(id).ChatId("oc-source").Build(), nil
	}
	chat := &LarkChat{ChatID: "oc-source", RootMsgID: "om-first", ThreadID: "omt_known"}
	got := readSourceContext(t, b.sourceContextInstructions(context.Background(), chat, compactTurnInputRefs("user", "om-first")))
	if got.ConversationURL != "" || got.ThreadID != "" || got.ConversationURLError != "source_thread_unavailable" {
		t.Fatalf("known topic fell back to chat: %+v", got)
	}
}

func TestSourceContextDoesNotBorrowSingletonThread(t *testing.T) {
	t.Parallel()
	b := &Bridge{bridgeID: "feishu:test"}
	b.sourceMessageLookup = func(_ context.Context, id string) (*larkim.Message, error) {
		return larkim.NewMessageBuilder().MessageId(id).ChatId("oc-input").Build(), nil
	}
	chat := &LarkChat{ChatID: "oc-binding", RootMsgID: singletonStreamKey(b.bridgeID), ThreadID: "omt-binding"}
	ctx := withFeishuSource(context.Background(), feishuSource{ChatID: "oc-input"})
	got := readSourceContext(t, b.sourceContextInstructions(ctx, chat, compactTurnInputRefs("user", "om-input")))
	if got.ChatID != "oc-input" || got.ThreadID != "" || got.ConversationURLError != "" || got.ConversationURL != "https://applink.feishu.cn/client/chat/open?openChatId=oc-input" {
		t.Fatalf("singleton borrowed output binding: %+v", got)
	}
}

func TestFeishuConversationURLEncodesIdentifiers(t *testing.T) {
	t.Parallel()
	chatID, threadID := "oc_source&extra=chat", "omt_source&extra=thread"
	for _, thread := range []string{"", threadID} {
		u, err := url.Parse(feishuConversationURL(chatID, thread))
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if u.Scheme != "https" || u.Host != "applink.feishu.cn" || q.Has("extra") {
			t.Fatalf("unsafe entry URL: %s", u)
		}
		if thread == "" {
			if u.Path != "/client/chat/open" || q.Get("openChatId") != chatID {
				t.Fatalf("chat URL = %s", u)
			}
		} else if u.Path != "/client/thread/open" || q.Get("open_chat_id") != chatID || q.Get("openchatid") != chatID || q.Get("open_thread_id") != thread || q.Get("openthreadid") != thread || q.Get("thread_position") != "-1" {
			t.Fatalf("thread URL = %s", u)
		}
	}
}

func TestFetchSourceMessageBuildsEntryWithoutAPILink(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
		case "/open-apis/im/v1/messages/om-input":
			if r.Method != http.MethodGet || r.URL.Query().Get("user_id_type") != "open_id" {
				t.Errorf("unexpected lookup: %s", r.URL)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []*larkim.Message{sourceTestMessage("om-input")}}})
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	b := &Bridge{bridgeID: "feishu:test", client: lark.NewClient("test-app", "test-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))}
	got := readSourceContext(t, b.sourceContextInstructions(context.Background(), &LarkChat{ChatID: "oc-source", RootMsgID: "om-root"}, compactTurnInputRefs("user", "om-input")))
	if got.ConversationURL != sourceTestURL || got.ConversationURLError != "" {
		t.Fatalf("Bot source context without API link = %+v", got)
	}
}
