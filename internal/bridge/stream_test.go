package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type orderedReplyBackend struct {
	create         func(string) (*replyCardHandle, error)
	createActivity func(turnCardActivity) (*replyCardHandle, error)
	update         func(*replyCardHandle, string) error
	updateActivity func(*replyCardHandle, turnCardActivity) error
	finish         func(*replyCardHandle, string) error
}

func (b *orderedReplyBackend) Create(_ context.Context, text string) (*replyCardHandle, error) {
	return b.create(text)
}

func (b *orderedReplyBackend) Update(_ context.Context, handle *replyCardHandle, text string) error {
	return b.update(handle, text)
}

func (b *orderedReplyBackend) Calibrate(_ context.Context, handle *replyCardHandle, text string) error {
	return b.update(handle, text)
}

func (b *orderedReplyBackend) Finish(_ context.Context, handle *replyCardHandle, text string) error {
	return b.finish(handle, text)
}

func (b *orderedReplyBackend) CreateActivity(_ context.Context, activity turnCardActivity) (*replyCardHandle, error) {
	return b.createActivity(activity)
}

func (b *orderedReplyBackend) UpdateActivity(_ context.Context, handle *replyCardHandle, activity turnCardActivity) error {
	return b.updateActivity(handle, activity)
}

func TestFeishuStreamVisibilityRequiresInputStartedTurn(t *testing.T) {
	gate := newFeishuStreamVisibilityGate()

	systemTurn := agentui.Action{Kind: agentui.ActionTurnStarted, Turn: agentui.TurnEvent{ID: "restart", Seq: 1}}
	gate.Observe(systemTurn)
	if gate.Allow(agentui.Action{Kind: agentui.ActionReplyAppend, Turn: systemTurn.Turn, Text: "startup greeting"}) {
		t.Fatal("system-started output was visible; want suppressed")
	}
	if gate.Allow(agentui.Action{Kind: agentui.ActionActivityChanged, Turn: systemTurn.Turn, Activity: &agentui.ActivityView{Status: "running"}}) {
		t.Fatal("system-started activity was visible; want suppressed")
	}

	feishuTurn := agentui.Action{Kind: agentui.ActionTurnStarted, Source: agentui.SourceFeishu, Text: "fix it", Turn: agentui.TurnEvent{ID: "user", Seq: 2}}
	gate.Observe(feishuTurn)
	if !gate.Allow(agentui.Action{Kind: agentui.ActionReplyAppend, Turn: feishuTurn.Turn, Text: "working"}) {
		t.Fatal("Feishu input-started reply was suppressed; want visible")
	}
	if !gate.Allow(agentui.Action{Kind: agentui.ActionActivityChanged, Turn: feishuTurn.Turn, Activity: &agentui.ActivityView{Status: "running"}}) {
		t.Fatal("Feishu input-started activity was suppressed; want visible")
	}

	cliTurn := agentui.Action{Kind: agentui.ActionTurnStarted, Source: agentui.SourceCLI, Text: "continue", Turn: agentui.TurnEvent{ID: "cli", Seq: 3}}
	gate.Observe(cliTurn)
	if !gate.Allow(agentui.Action{Kind: agentui.ActionReplyAppend, Turn: cliTurn.Turn, Text: "cli-triggered output"}) {
		t.Fatal("CLI input-started reply was suppressed; want visible")
	}

	gate.Observe(agentui.Action{Kind: agentui.ActionTurnFinished, Turn: feishuTurn.Turn})
	if gate.Allow(agentui.Action{Kind: agentui.ActionReplyAppend, Turn: feishuTurn.Turn, Text: "late text"}) {
		t.Fatal("late reply after turn finished was visible; want suppressed")
	}
}

func TestFeishuStreamVisibilityAcceptsBridgeAuthorizedTurnWithoutInputEcho(t *testing.T) {
	gate := newFeishuStreamVisibilityGate()
	gate.authorizeInput()

	turn := agentui.Action{Kind: agentui.ActionTurnStarted, Turn: agentui.TurnEvent{ID: "submitted", Seq: 1}}
	gate.Observe(turn)
	if !gate.Allow(agentui.Action{Kind: agentui.ActionReplyAppend, Turn: turn.Turn, Text: "working"}) {
		t.Fatal("Bridge-authorized Feishu turn was suppressed when runtime input echo was absent")
	}

	gate.Observe(agentui.Action{Kind: agentui.ActionTurnFinished, Turn: turn.Turn})
	next := agentui.Action{Kind: agentui.ActionTurnStarted, Turn: agentui.TurnEvent{ID: "startup", Seq: 2}}
	gate.Observe(next)
	if gate.Allow(agentui.Action{Kind: agentui.ActionReplyAppend, Turn: next.Turn, Text: "startup noise"}) {
		t.Fatal("one input authorization leaked into the next provider-started turn")
	}
}

func TestFeishuStreamVisibilityAcceptsFirstReplyWhenProviderOmitsTurnStarted(t *testing.T) {
	gate := newFeishuStreamVisibilityGate()
	reply := agentui.Action{
		Kind: agentui.ActionReplyAppend,
		Turn: agentui.TurnEvent{ID: "turn-without-start", Seq: 1},
		Text: "answer",
	}
	if gate.Allow(reply) {
		t.Fatal("provider reply was visible before authenticated Submit authorization")
	}
	gate.authorizeTurn(reply.Turn)
	if !gate.Allow(reply) {
		t.Fatal("first provider reply remained suppressed after topic-level Submit authorization")
	}
}

func TestExternalBotTurnPresentationDefersCoordinationAndCreatesOnlyFinalCard(t *testing.T) {
	gate := newTurnActivityPresentationGate(true)
	started := time.Unix(100, 0)
	reasoning := &agentui.ActivityView{StartedAt: started, CurrentStep: "choosing collaborator"}
	if gate.ShouldPublishActivity(reasoning, false) {
		t.Fatal("provider reasoning created a card before external workflow classification")
	}
	list := &agentui.ActivityView{StartedAt: started, Tools: []agentui.ToolEvent{{
		ID: "list", Name: "amux.collaborator_list", Title: "amux/amux.collaborator_list", Status: "completed",
		InputJSON: json.RawMessage(`{"kind":"external_bot"}`),
	}}}
	if gate.ShouldPublishActivity(list, false) {
		t.Fatal("collaborator discovery created a coordination card before the target kind was known")
	}
	external := &agentui.ActivityView{StartedAt: started, Tools: append(list.Tools, agentui.ToolEvent{
		ID: "create", Name: "amux.delegation_create", Title: "amux/amux.delegation_create", Status: "running",
		InputJSON: json.RawMessage(`{"collaborator_id":"external-bot:docman","task":"review"}`),
	})}
	if gate.ShouldPublishActivity(external, false) || !gate.ExternalDelegation() {
		t.Fatal("external Bot delegation was not classified as a suppressed coordination turn")
	}
	gate.ObserveCompletedAssistantMessage()
	if gate.ShouldPublishActivity(external, false) || !gate.ExternalDelegation() {
		t.Fatal("assistant boundary changed an already classified external Bot turn")
	}

	var creates []string
	backend := &orderedReplyBackend{
		create: func(text string) (*replyCardHandle, error) {
			creates = append(creates, "answer:"+text)
			return &replyCardHandle{mode: replyCardModeCardKit, messageID: "answer-msg", hasAnswer: true}, nil
		},
		createActivity: func(turnCardActivity) (*replyCardHandle, error) {
			creates = append(creates, "activity")
			return &replyCardHandle{mode: replyCardModeCardKit, messageID: "activity-msg", hasActivity: true}, nil
		},
		update:         func(*replyCardHandle, string) error { return nil },
		updateActivity: func(*replyCardHandle, turnCardActivity) error { return nil },
		finish: func(handle *replyCardHandle, _ string) error {
			handle.finished = true
			return nil
		},
	}
	reply := newStreamingReplySender(0, backend, nil)
	reply.ReplaceCurrent("复核后的最终结果")
	reply.Flush()
	reply.Close()
	if got := strings.Join(creates, "|"); got != "answer:复核后的最终结果" {
		t.Fatalf("created cards = %q, want one final answer card", got)
	}
}

func TestExternalBotTurnPresentationToleratesProfileDiscoveryBeforeExternalSignal(t *testing.T) {
	gate := newTurnActivityPresentationGate(true)
	started := time.Unix(100, 0)
	profileList := agentui.ToolEvent{
		ID: "profiles", Name: "amux.profile_list", Title: "amux/amux.profile_list", Status: "completed",
	}
	if gate.ShouldPublishActivity(&agentui.ActivityView{StartedAt: started, Tools: []agentui.ToolEvent{profileList}}, false) {
		t.Fatal("read-only Profile discovery prematurely created a coordination activity card")
	}
	externalList := agentui.ToolEvent{
		ID: "external", Name: "amux.collaborator_list", Title: "amux/amux.collaborator_list", Status: "completed",
		InputJSON: json.RawMessage(`{"kind":"external_bot"}`),
	}
	view := &agentui.ActivityView{StartedAt: started, Tools: []agentui.ToolEvent{profileList, externalList}}
	if gate.ShouldPublishActivity(view, false) || !gate.ExternalDelegation() {
		t.Fatal("external Bot signal after Profile discovery did not retain the single-card timeline")
	}
}

func TestFeishuStreamActiveAuthorizationRejectsTerminalOnlyReplay(t *testing.T) {
	if feishuStreamActionCanAuthorizeFromActive(agentui.ActionTurnStarted) {
		t.Fatal("explicit turn_started must use the dedicated authorization path")
	}
	if feishuStreamActionCanAuthorizeFromActive(agentui.ActionTurnFinished) {
		t.Fatal("terminal-only replay must not authorize a pending Feishu turn")
	}
	for _, kind := range []agentui.ActionKind{
		agentui.ActionReplyAppend,
		agentui.ActionReplyReplace,
		agentui.ActionReplyReconcileFinalMessage,
		agentui.ActionActivityChanged,
		agentui.ActionPermissionRequest,
		agentui.ActionError,
	} {
		if !feishuStreamActionCanAuthorizeFromActive(kind) {
			t.Fatalf("first non-terminal provider action %q should authorize an active Feishu submit", kind)
		}
	}
}

func TestFeishuStreamProviderStartFallbackWaitsForPendingBotInput(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	if !b.canAuthorizeProviderTurnFromActive("root-1") {
		t.Fatal("ordinary Feishu submit should keep provider turn_started fallback")
	}
	claim, err := b.db.ClaimBotTurn(b.currentBridgeId(), "oc-workspace", "root-1", "om-bot", "ou-caller", 10)
	if err != nil || !claim.Accepted {
		t.Fatalf("ClaimBotTurn = %+v err=%v", claim, err)
	}
	if b.canAuthorizeProviderTurnFromActive("root-1") {
		t.Fatal("provider-owned turn_started must not consume a pending Bot-to-Bot callback")
	}
	if _, err := b.db.ClearPendingBotHandoff(b.currentBridgeId(), "root-1", "om-bot"); err != nil {
		t.Fatalf("ClearPendingBotHandoff: %v", err)
	}
	if !b.canAuthorizeProviderTurnFromActive("root-1") {
		t.Fatal("provider turn_started fallback should resume after Bot handoff clears")
	}
}

func TestPrivateWorkspaceBindingSendsThreadReplies(t *testing.T) {
	var paths []string
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages/om-private-root/reply":
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatalf("decode reply body: %v", err)
			}
			encoded, _ := json.Marshal(raw)
			paths = append(paths, r.URL.Path)
			bodies = append(bodies, string(encoded))
			msgID := "reply-msg-" + string(rune('0'+len(paths)))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]string{"message_id": msgID, "thread_id": "thread-private"},
			})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/reply-msg-"):
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			encoded, _ := json.Marshal(raw)
			paths = append(paths, r.URL.Path)
			bodies = append(bodies, string(encoded))
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
		default:
			t.Fatalf("unexpected lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	if err := db.EnsureWorkspaceKind("feishu:test", "oc-private", WorkspaceKindPrivate); err != nil {
		t.Fatalf("EnsureWorkspaceKind: %v", err)
	}
	b := &Bridge{
		bridgeID: "feishu:test",
		db:       db,
		client:   lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL)),
	}
	chat := &LarkChat{
		ChatID:    "oc-private",
		RootMsgID: "om-private-root",
		OpenId:    "owner-open",
	}

	if _, _, err := b.sendTextToBinding(context.Background(), chat, "测试通知"); err != nil {
		t.Fatalf("sendTextToBinding: %v", err)
	}
	replyID, _, err := b.sendAgentReplyCardToBinding(context.Background(), chat, "完成了")
	if err != nil {
		t.Fatalf("sendAgentReplyCardToBinding: %v", err)
	}
	if err := b.patchAgentReplyBinding(context.Background(), chat, replyID, "完成了\n\n补充说明"); err != nil {
		t.Fatalf("patchAgentReplyBinding: %v", err)
	}
	activityID, _, err := b.sendToolActivityToBinding(context.Background(), chat, toolActivityCardMeta{Status: "running"}, "Agent 正在工作")
	if err != nil {
		t.Fatalf("sendToolActivityToBinding: %v", err)
	}
	if err := b.patchToolActivityBinding(context.Background(), chat, activityID, toolActivityCardMeta{Status: "done"}, "Agent 已完成"); err != nil {
		t.Fatalf("patchToolActivityBinding: %v", err)
	}

	if len(paths) != 5 {
		t.Fatalf("lark calls = %d, want 5; paths=%v bodies=%v", len(paths), paths, bodies)
	}
	for i, body := range bodies {
		if strings.HasSuffix(paths[i], "/reply") && !strings.Contains(body, `"reply_in_thread":true`) {
			t.Fatalf("body #%d = %s, want reply_in_thread=true", i+1, body)
		}
	}
	if !strings.Contains(bodies[0], `"msg_type":"text"`) {
		t.Fatalf("body #1 = %s, want text notice", bodies[0])
	}
	for i, body := range bodies[1:] {
		if strings.Contains(body, `"msg_type"`) && !strings.Contains(body, `"msg_type":"interactive"`) {
			t.Fatalf("body #%d = %s, want interactive card", i+2, body)
		}
		assertInteractiveCardJSON2(t, body)
	}
}

func TestSingletonBindingSendsLatestDMOrChatNotice(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	b := &Bridge{
		bridgeID: "feishu:test",
		db:       db,
	}
	var dmNotices []string
	var chatNotices []string
	b.dmNotice = func(_ context.Context, openID, text string) (string, string, error) {
		dmNotices = append(dmNotices, openID+":"+text)
		return "om-dm", "", nil
	}
	b.chatNotice = func(_ context.Context, chatID, text string) (string, string, error) {
		chatNotices = append(chatNotices, chatID+":"+text)
		return "om-chat", "", nil
	}
	if err := db.SetBotSession(BotSession{
		BridgeId:        "feishu:test",
		Enabled:         true,
		TargetID:        "runtime-1",
		TargetName:      "local",
		SessionName:     "bot-a-main",
		AgentType:       "codex",
		LastReplyKind:   botSessionReplyDM,
		LastReplyOpenID: "owner-open",
	}); err != nil {
		t.Fatalf("SetBotSession dm: %v", err)
	}
	chat := &LarkChat{BridgeId: "feishu:test", RootMsgID: singletonStreamKey("feishu:test")}

	if _, _, err := b.sendTextToBinding(context.Background(), chat, "已中断"); err != nil {
		t.Fatalf("sendTextToBinding dm: %v", err)
	}
	if len(dmNotices) != 1 || dmNotices[0] != "owner-open:已中断" {
		t.Fatalf("dmNotices = %q, want owner-open text", dmNotices)
	}
	if len(chatNotices) != 0 {
		t.Fatalf("chatNotices = %q, want none for dm target", chatNotices)
	}

	if err := db.SetBotSession(BotSession{
		BridgeId:        "feishu:test",
		Enabled:         true,
		TargetID:        "runtime-1",
		TargetName:      "local",
		SessionName:     "bot-a-main",
		AgentType:       "codex",
		LastReplyKind:   botSessionReplyGroup,
		LastReplyChatID: "oc-workspace",
	}); err != nil {
		t.Fatalf("SetBotSession group: %v", err)
	}
	if _, _, err := b.sendTextToBinding(context.Background(), chat, "已中断"); err != nil {
		t.Fatalf("sendTextToBinding group: %v", err)
	}
	if len(chatNotices) != 1 || chatNotices[0] != "oc-workspace:已中断" {
		t.Fatalf("chatNotices = %q, want group text", chatNotices)
	}
}

func TestBotSessionForBindingRejectsStaleSingletonStream(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	if err := db.SetBotSession(BotSession{
		BridgeId:    "feishu:test",
		Enabled:     true,
		TargetID:    "runtime-2",
		SessionName: "new-session",
		AgentType:   "codex",
	}); err != nil {
		t.Fatalf("SetBotSession: %v", err)
	}
	b := &Bridge{bridgeID: "feishu:test", db: db}
	chat := &LarkChat{
		BridgeId:    "feishu:test",
		RootMsgID:   singletonStreamKey("feishu:test"),
		TargetID:    "runtime-1",
		SessionName: "old-session",
	}

	if _, ok, err := b.botSessionForBinding(chat); !ok || err == nil || !strings.Contains(err.Error(), "stale singleton") {
		t.Fatalf("botSessionForBinding() = ok %v, err %v; want stale singleton error", ok, err)
	}
}

func TestSingletonBindingSendsDirectCardsWithoutThread(t *testing.T) {
	var mu sync.Mutex
	var calls []struct {
		Method string
		Path   string
		Query  string
		Body   string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			encoded, _ := json.Marshal(raw)
			mu.Lock()
			calls = append(calls, struct {
				Method string
				Path   string
				Query  string
				Body   string
			}{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(encoded)})
			id := "direct-card-" + string(rune('0'+len(calls)))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]string{"message_id": id},
			})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			encoded, _ := json.Marshal(raw)
			mu.Lock()
			calls = append(calls, struct {
				Method string
				Path   string
				Query  string
				Body   string
			}{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(encoded)})
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})
		default:
			t.Fatalf("unexpected lark API call: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	if err := db.SetBotSession(BotSession{
		BridgeId:        "feishu:test",
		Enabled:         true,
		TargetID:        "runtime-1",
		TargetName:      "local",
		SessionName:     "bot-a-main",
		AgentType:       "codex",
		LastReplyKind:   botSessionReplyGroup,
		LastReplyChatID: "oc-workspace",
	}); err != nil {
		t.Fatalf("SetBotSession group: %v", err)
	}
	b := &Bridge{
		bridgeID: "feishu:test",
		db:       db,
		client:   lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL)),
	}
	chat := &LarkChat{BridgeId: "feishu:test", RootMsgID: singletonStreamKey("feishu:test")}

	replyID, _, err := b.sendAgentReplyCardToBinding(context.Background(), chat, "完成了")
	if err != nil {
		t.Fatalf("sendAgentReplyCardToBinding: %v", err)
	}
	if err := b.patchAgentReplyBinding(context.Background(), chat, replyID, "完成了\n\n补充说明"); err != nil {
		t.Fatalf("patchAgentReplyBinding: %v", err)
	}
	activityID, _, err := b.sendToolActivityToBinding(context.Background(), chat, toolActivityCardMeta{Status: "running"}, "Agent 正在工作")
	if err != nil {
		t.Fatalf("sendToolActivityToBinding: %v", err)
	}
	if err := b.patchToolActivityBinding(context.Background(), chat, activityID, toolActivityCardMeta{Status: "done"}, "Agent 已完成"); err != nil {
		t.Fatalf("patchToolActivityBinding: %v", err)
	}

	mu.Lock()
	if len(calls) != 4 {
		t.Fatalf("lark calls = %d, want 4: %+v", len(calls), calls)
	}
	for i, call := range calls {
		if strings.Contains(call.Path, "/reply") {
			t.Fatalf("call #%d used thread reply path: %+v", i+1, call)
		}
		if call.Method == http.MethodPost {
			if call.Query != "receive_id_type=chat_id" {
				t.Fatalf("call #%d query = %q, want receive_id_type=chat_id", i+1, call.Query)
			}
			if !strings.Contains(call.Body, `"msg_type":"interactive"`) {
				t.Fatalf("call #%d body = %s, want interactive card", i+1, call.Body)
			}
			assertInteractiveCardJSON2(t, call.Body)
			if strings.Contains(call.Body, `"reply_in_thread"`) {
				t.Fatalf("call #%d body = %s, want no reply_in_thread", i+1, call.Body)
			}
		}
	}
	mu.Unlock()

	mu.Lock()
	calls = nil
	mu.Unlock()
	if err := db.SetBotSession(BotSession{
		BridgeId:        "feishu:test",
		Enabled:         true,
		TargetID:        "runtime-1",
		TargetName:      "local",
		SessionName:     "bot-a-main",
		AgentType:       "codex",
		LastReplyKind:   botSessionReplyDM,
		LastReplyOpenID: "owner-open",
	}); err != nil {
		t.Fatalf("SetBotSession dm: %v", err)
	}
	if _, _, err := b.sendAgentReplyCardToBinding(context.Background(), chat, "私聊输出"); err != nil {
		t.Fatalf("sendAgentReplyCardToBinding dm: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("dm lark calls = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].Query != "receive_id_type=open_id" {
		t.Fatalf("dm query = %q, want receive_id_type=open_id", calls[0].Query)
	}
	if !strings.Contains(calls[0].Body, `"msg_type":"interactive"`) {
		t.Fatalf("dm body = %s, want interactive card", calls[0].Body)
	}
	assertInteractiveCardJSON2(t, calls[0].Body)
}

func assertInteractiveCardJSON2(t *testing.T, body string) {
	t.Helper()
	var outer struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &outer); err != nil {
		t.Fatalf("decode lark message body: %v\n%s", err, body)
	}
	var card struct {
		Schema string `json:"schema"`
		Body   struct {
			Elements []struct {
				Tag string `json:"tag"`
			} `json:"elements"`
		} `json:"body"`
		LegacyElements []any `json:"elements"`
	}
	if err := json.Unmarshal([]byte(outer.Content), &card); err != nil {
		t.Fatalf("decode interactive card content: %v\n%s", err, outer.Content)
	}
	if card.Schema != "2.0" {
		t.Fatalf("card schema = %q, want 2.0\n%s", card.Schema, outer.Content)
	}
	if len(card.Body.Elements) == 0 {
		t.Fatalf("card body.elements empty\n%s", outer.Content)
	}
	if len(card.LegacyElements) != 0 {
		t.Fatalf("card top-level elements = %#v, want none for card json 2.0\n%s", card.LegacyElements, outer.Content)
	}
}

// Assistant narration is projected into activity, while only the native result
// becomes the answer on the same CardKit turn card.
func TestMainTurnCard_ProgressThenAuthoritativeResult(t *testing.T) {
	var mu sync.Mutex
	var creates []string
	var replyText string
	var activityUpdates int

	replyBackend := &orderedReplyBackend{
		create: func(text string) (*replyCardHandle, error) {
			mu.Lock()
			defer mu.Unlock()
			creates = append(creates, "reply")
			replyText = text
			return &replyCardHandle{mode: replyCardModeCardKit, cardID: "reply-card", messageID: "reply-msg", lastText: text, hasAnswer: true}, nil
		},
		createActivity: func(turnCardActivity) (*replyCardHandle, error) {
			mu.Lock()
			defer mu.Unlock()
			creates = append(creates, "activity")
			return &replyCardHandle{mode: replyCardModeCardKit, cardID: "activity-card", messageID: "activity-msg", hasActivity: true}, nil
		},
		update: func(handle *replyCardHandle, text string) error {
			mu.Lock()
			defer mu.Unlock()
			if handle.messageID != "activity-msg" {
				t.Fatalf("answer handle = %q, want activity-msg", handle.messageID)
			}
			replyText = text
			handle.lastText = text
			return nil
		},
		updateActivity: func(handle *replyCardHandle, _ turnCardActivity) error {
			mu.Lock()
			defer mu.Unlock()
			if handle.messageID != "activity-msg" {
				t.Fatalf("activity handle = %q, want activity-msg", handle.messageID)
			}
			activityUpdates++
			return nil
		},
		finish: func(handle *replyCardHandle, text string) error {
			mu.Lock()
			defer mu.Unlock()
			replyText = text
			handle.finished = true
			return nil
		},
	}
	reply := newStreamingReplySender(0, replyBackend, nil)

	legacyCreates := 0
	activitySender := newMainTurnActivitySender(time.Hour, reply,
		func() turnCardActivity { return renderTurnActivity(toolActivityCardMeta{Status: "running"}) },
		func(_ string) (string, error) {
			legacyCreates++
			return "legacy-activity", nil
		},
		func(_, _ string) error { return nil },
		nil,
	)
	defer activitySender.Close()

	progress := &assistantProgress{}
	// 1. Pre-tool narration only creates the activity form of the card.
	if summary := progress.Append("Let me check that file."); summary == "" {
		t.Fatal("missing assistant progress summary")
	}
	activitySender.Flush(`{"progress":"checking"}`)
	progress.Boundary()
	// 2. Tool starts and updates that activity card.
	activitySender.MaybeSend(`{"tools":[{"id":"tool-1","status":"running"}]}`)
	// 3. Tool completes.
	activitySender.MaybeSend(`{"tools":[{"id":"tool-1","status":"completed"}]}`)
	activitySender.Flush(`{"tools":[{"id":"tool-1","status":"completed"}]}`)
	// 4. The provider-native result becomes the only answer text.
	reply.ReplaceCurrent("The file contains foo.")
	reply.Flush()
	activitySender.Reset()

	mu.Lock()
	defer mu.Unlock()
	if len(creates) != 1 || creates[0] != "activity" {
		t.Fatalf("created cards = %v, want one activity-first main card", creates)
	}
	if legacyCreates != 0 || activityUpdates != 1 {
		t.Fatalf("legacy creates=%d activity updates=%d", legacyCreates, activityUpdates)
	}
	if replyText != "The file contains foo." {
		t.Fatalf("reply text = %q, want authoritative result only", replyText)
	}
}

type blockingStreamHub struct {
	mu      sync.Mutex
	nextID  int
	started chan int
	exited  chan int
	release chan struct{}
}

type heartbeatStreamHub struct {
	exited chan struct{}
}

type effectiveModelStreamHub struct {
	exited chan struct{}
}

func (h *effectiveModelStreamHub) ListTargets() []*pb.Target { return nil }

func (h *effectiveModelStreamHub) Route(context.Context, string, string, interface{}) (*pb.TunnelMsg, error) {
	return &pb.TunnelMsg{}, nil
}

func (h *effectiveModelStreamHub) RouteStream(_ context.Context, _ string, _ string, _ interface{}, emit func(*pb.TunnelMsg) error) error {
	defer close(h.exited)
	payload, _ := json.Marshal(&pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT, EffectiveModel: "example-model"})
	return emit(&pb.TunnelMsg{Payload: payload})
}

func (h *heartbeatStreamHub) ListTargets() []*pb.Target {
	return []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{Id: "session-id-1", Name: "session-1"}}}}
}

func (h *heartbeatStreamHub) Route(context.Context, string, string, interface{}) (*pb.TunnelMsg, error) {
	return &pb.TunnelMsg{}, nil
}

func (h *heartbeatStreamHub) RouteStream(_ context.Context, _ string, _ string, _ interface{}, emit func(*pb.TunnelMsg) error) error {
	defer close(h.exited)
	for _, ev := range []*pb.AttachEvent{
		{Kind: pb.AttachEvent_STATE_CHANGE, EventType: "turn_started", State: "turn_started", TurnSeq: 1},
		{Kind: pb.AttachEvent_HEARTBEAT},
	} {
		payload, _ := json.Marshal(ev)
		if err := emit(&pb.TunnelMsg{Payload: payload}); err != nil {
			return err
		}
	}
	return nil
}

type countingSubtaskReader struct {
	mu    sync.Mutex
	calls int
}

func TestStreamThinkingProcessKeepsAssistantAndGroupWatchIndependent(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.presentation]
thinking_process = "expanded"

[bridge.feishu.group_watch]
thinking_process = "collapsed"

[bridge.feishu.profiles.coder.presentation]
thinking_process = "hidden"

[bridge.feishu.profiles.coder.group_watch]
thinking_process = "expanded"
`)
	b := &Bridge{profile: "coder"}

	if got := b.streamThinkingProcess(false); got != config.FeishuThinkingProcessHidden {
		t.Fatalf("assistant thinking process = %q, want hidden", got)
	}
	if got := b.streamThinkingProcess(true); got != config.FeishuThinkingProcessExpanded {
		t.Fatalf("group watch thinking process = %q, want expanded", got)
	}

	if err := config.SetBridgeFeishuThinkingProcess("coder", config.FeishuThinkingProcessCollapsed); err != nil {
		t.Fatalf("SetBridgeFeishuThinkingProcess: %v", err)
	}
	if err := config.SetBridgeFeishuGroupWatchExecution(
		"coder", "", "", "", "", config.FeishuThinkingProcessHidden,
	); err != nil {
		t.Fatalf("SetBridgeFeishuGroupWatchExecution: %v", err)
	}
	if got := b.streamThinkingProcess(false); got != config.FeishuThinkingProcessCollapsed {
		t.Fatalf("updated assistant thinking process = %q, want collapsed", got)
	}
	if got := b.streamThinkingProcess(true); got != config.FeishuThinkingProcessHidden {
		t.Fatalf("updated group watch thinking process = %q, want hidden", got)
	}
}

func (r *countingSubtaskReader) SubtaskIDsByParent(string) []string {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return nil
}

func (r *countingSubtaskReader) GetSubtaskJSON(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (r *countingSubtaskReader) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type readinessStreamHub struct {
	streamStarted  chan struct{}
	allowHeartbeat chan struct{}
	submitted      chan struct{}
}

func (h *readinessStreamHub) ListTargets() []*pb.Target { return nil }

func (h *readinessStreamHub) Route(_ context.Context, _ string, method string, _ interface{}) (*pb.TunnelMsg, error) {
	if method == "Submit" {
		close(h.submitted)
	}
	payload, _ := json.Marshal(&pb.SubmitResponse{Ok: true})
	return &pb.TunnelMsg{Payload: payload}, nil
}

func (h *readinessStreamHub) RouteStream(ctx context.Context, _ string, _ string, _ interface{}, emit func(*pb.TunnelMsg) error) error {
	close(h.streamStarted)
	select {
	case <-ctx.Done():
		return nil
	case <-h.allowHeartbeat:
	}
	payload, _ := json.Marshal(&pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT})
	if err := emit(&pb.TunnelMsg{Payload: payload}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func newBlockingStreamHub() *blockingStreamHub {
	return &blockingStreamHub{
		started: make(chan int, 4),
		exited:  make(chan int, 4),
		release: make(chan struct{}),
	}
}

func (h *blockingStreamHub) ListTargets() []*pb.Target { return nil }

func (h *blockingStreamHub) Route(context.Context, string, string, interface{}) (*pb.TunnelMsg, error) {
	return &pb.TunnelMsg{}, nil
}

func (h *blockingStreamHub) RouteStream(ctx context.Context, _ string, _ string, _ interface{}, _ func(*pb.TunnelMsg) error) error {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	h.mu.Unlock()

	h.started <- id
	select {
	case <-ctx.Done():
	case <-h.release:
	}
	h.exited <- id
	return nil
}

func TestStreamFreezeRegistrationIsScopedByStreamToken(t *testing.T) {
	b := &Bridge{
		streamController: streamController{streamTokens: map[string]uint64{"root-1": 2}, supersededTurns: map[string]bool{"root-1": true}},
	}
	oldCalls := 0
	newCalls := 0

	b.registerStreamFreeze("root-1", 1, func() { oldCalls++ })
	b.registerStreamFreeze("root-1", 2, func() { newCalls++ })
	if oldCalls != 0 || newCalls != 1 {
		t.Fatalf("registration calls = old:%d new:%d, want old:0 new:1", oldCalls, newCalls)
	}

	b.unregisterStreamFreeze("root-1", 1)
	if freeze := b.markTurnSuperseded("root-1"); freeze != nil {
		freeze()
	}
	if oldCalls != 0 || newCalls != 2 {
		t.Fatalf("freeze calls = old:%d new:%d, want old:0 new:2", oldCalls, newCalls)
	}
}

func TestStartSingletonStreamUsesSingletonKey(t *testing.T) {
	hub := newBlockingStreamHub()
	defer close(hub.release)
	b := &Bridge{
		hub:              hub,
		streamController: streamController{streams: make(map[string]context.CancelFunc), streamTokens: make(map[string]uint64), nextStreamToken: 0},
	}
	b.startSingletonStream(&BotSession{
		BridgeId:    "feishu:test",
		Enabled:     true,
		TargetID:    "runtime-1",
		SessionName: "bot-a-main",
		AgentType:   "codex",
	}, "owner-open")
	if got := waitForStreamTestSignal(t, hub.started); got != 1 {
		t.Fatalf("stream id = %d, want 1", got)
	}
	key := singletonStreamKey("feishu:test")
	b.streamMu.Lock()
	_, ok := b.streams[key]
	b.streamMu.Unlock()
	if !ok {
		t.Fatalf("singleton stream key %q was not registered", key)
	}
	b.StopStream(key)
	if got := waitForStreamTestSignal(t, hub.exited); got != 1 {
		t.Fatalf("exited stream id = %d, want 1", got)
	}
}

func waitForStreamTestSignal(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream signal")
		return 0
	}
}

func TestBridgeErrorNoticeSanitized(t *testing.T) {
	// No session name: generic prompt.
	if got := bridgeErrorNotice("", "provider exploded at /secret/path", false); !strings.Contains(got, "Agent 执行出错") {
		t.Fatalf("bridgeErrorNotice(empty) = %q, want generic prompt", got)
	}
	// With session name: includes the session ref but no ErrorMessage forwarding.
	got := bridgeErrorNotice("work-session", "provider exploded at /secret/path", false)
	if !strings.Contains(got, "work-session") {
		t.Fatalf("bridgeErrorNotice = %q, want session ref", got)
	}
	if !strings.Contains(got, "Agent 执行出错") {
		t.Fatalf("bridgeErrorNotice = %q, want generic prefix", got)
	}
	if strings.Contains(got, "/secret/path") {
		t.Fatalf("bridgeErrorNotice leaked provider detail: %q", got)
	}
}

func TestBridgeErrorNoticeExplainsMissingProviderConversation(t *testing.T) {
	got := bridgeErrorNotice("work-session", "Thread 01JXYZ was not found", true)
	for _, want := range []string{"历史对话上下文已失效", "新建话题", "work-session", "--refresh-profile"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bridgeErrorNotice missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "01JXYZ") {
		t.Fatalf("bridgeErrorNotice leaked provider thread id: %q", got)
	}

	ordinary := bridgeErrorNotice("work-session", "workspace file not found", true)
	if strings.Contains(ordinary, "历史对话上下文已失效") {
		t.Fatalf("ordinary not-found error misclassified as missing conversation: %q", ordinary)
	}

	notProfileBacked := bridgeErrorNotice("work-session", "Thread 01JXYZ was not found", false)
	if strings.Contains(notProfileBacked, "--refresh-profile") {
		t.Fatalf("non-profile session received inapplicable refresh command: %q", notProfileBacked)
	}
}

func TestBridgeErrorNoticeExplainsUnavailableModel(t *testing.T) {
	got := bridgeErrorNotice(
		"feishu-singleton-default-df87e62394",
		"unexpected status 404 Not Found: The model `default_model` does not exist., url: http://models.internal/v1/chat/completions",
		true,
	)
	for _, want := range []string{
		"当前模型服务不可用",
		"模型已下线或服务暂时异常",
		"`/amux`",
		"助手设置",
		"切换其他模型",
		"重新发送本条消息",
		"--refresh-profile",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("bridgeErrorNotice missing %q: %q", want, got)
		}
	}
	for _, leaked := range []string{"default_model", "models.internal", "unexpected status", "does not exist"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("bridgeErrorNotice leaked provider detail %q: %q", leaked, got)
		}
	}
	if strings.Contains(got, "`amux session restart feishu-singleton-default-df87e62394`") {
		t.Fatalf("bridgeErrorNotice suggested non-refresh restart: %q", got)
	}

	ordinarySession := bridgeErrorNotice(
		"adhoc-session",
		"code=model_not_found",
		false,
	)
	for _, want := range []string{"`/amux`", "助手设置", "重新发送本条消息"} {
		if !strings.Contains(ordinarySession, want) {
			t.Fatalf("non-profile notice missing %q: %q", want, ordinarySession)
		}
	}
	if strings.Contains(ordinarySession, "amux session restart") {
		t.Fatalf("non-profile notice suggested an inapplicable restart: %q", ordinarySession)
	}

	for _, providerError := range []string{
		"model is unavailable",
		`model "retired-model" is not available`,
		`{"code":"model_unavailable"}`,
	} {
		if !providerModelUnavailable(providerError) {
			t.Errorf("providerModelUnavailable(%q) = false, want true", providerError)
		}
	}
	for _, unrelatedError := range []string{
		"model cache file does not exist",
		"workspace model file was not found",
		"open /tmp/model: file not found",
	} {
		if providerModelUnavailable(unrelatedError) {
			t.Errorf("providerModelUnavailable(%q) = true, want false", unrelatedError)
		}
	}
}

func TestBridgeErrorNoticeRepeatedToolLoop(t *testing.T) {
	const want = "⚠️ 模型反复调用同一工具未正常结束，本轮已自动终止。实际工作可能已完成，请检查结果后直接继续对话；若连续出现请切换模型。"
	got := bridgeErrorNotice(
		"work-session",
		"repeated identical tool calls: update_plan called 5 times (code=repeat_tool_loop)",
		true,
	)
	if got != want {
		t.Fatalf("repeat tool loop notice = %q; want %q", got, want)
	}
	if strings.Contains(got, "work-session") || strings.Contains(got, "update_plan") || strings.Contains(got, "repeat_tool_loop") {
		t.Fatalf("repeat tool loop notice leaked internal detail: %q", got)
	}
	ordinary := bridgeErrorNotice("work-session", "repeated identical tool calls", true)
	if ordinary == want || !strings.Contains(ordinary, "Agent 执行出错") {
		t.Fatalf("unmarked repeat error was misclassified: %q", ordinary)
	}
}

func TestBridgeStreamFailureNoticeExplainsMissingAmuxSession(t *testing.T) {
	got, ok := bridgeStreamFailureNotice(
		"work-session",
		status.Errorf(codes.NotFound, `session "work-session" not found`),
	)
	if !ok {
		t.Fatal("missing amux session was not recognized")
	}
	for _, want := range []string{"当前话题", "会话已不存在", "新建话题"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bridgeStreamFailureNotice missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "--refresh-profile") {
		t.Fatalf("missing amux session should not suggest refreshing provider context: %q", got)
	}

	if _, ok := bridgeStreamFailureNotice("work-session", errors.New("target disconnected")); ok {
		t.Fatal("ordinary stream failure should not be presented as a missing session")
	}
	if _, ok := bridgeStreamFailureNotice(
		"work-session",
		errors.New("session attach failed: workspace file not found"),
	); ok {
		t.Fatal("stream file error should not be presented as a missing session")
	}
}
