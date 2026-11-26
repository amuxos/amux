package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCompletionNotificationDispatchDoesNotWaitForNetwork(t *testing.T) {
	loadBridgeTestConfig(t, "[bridge.feishu.presentation]\ncompletion_notification_enabled = true\ncompletion_notification_after_seconds = 1\n")
	b, _ := newBridgeForInputTest(t)
	b.bridgeID = "feishu:test"
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: "feishu:test", RootMsgID: "root", MessageID: "msg", TurnID: "turn", OpenID: "ou_user"}}); err != nil {
		t.Fatal(err)
	}
	started, finished, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("notification worker did not exit")
		}
	})
	b.dmInteractive = func(ctx context.Context, _, _ string) (string, string, error) {
		context.AfterFunc(ctx, func() { close(finished) })
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "", "", errors.New("simulated private message failure")
	}
	chat := &LarkChat{BridgeId: "feishu:test", ChatID: "oc_group", RootMsgID: "root", ThreadID: "omt_thread"}
	go func() {
		b.dispatchTurnCompletionNotification(chat, "turn", "done", "", time.Minute)
		close(returned)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("notification send did not start")
	}
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("notification dispatch blocked the caller on private message network")
	}
	unblock()
}

func TestCompletionNotificationPartialDMFailureRetriesOnlyFailedRecipient(t *testing.T) {
	loadBridgeTestConfig(t, "[bridge.feishu.presentation]\ncompletion_notification_enabled = true\ncompletion_notification_after_seconds = 1\n")
	b, _ := newBridgeForInputTest(t)
	b.bridgeID = "feishu:test"
	for _, id := range []string{"ou_a", "ou_b"} {
		if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: "feishu:test", RootMsgID: "root", MessageID: id, TurnID: "turn", OpenID: id}}); err != nil {
			t.Fatal(err)
		}
	}
	calls := map[string]int{}
	b.threadNotice = func(context.Context, string, string) error { t.Fatal("must not notify the thread"); return nil }
	b.dmInteractive = func(_ context.Context, id, card string) (string, string, error) {
		calls[id]++
		var doc cardDoc
		if err := json.Unmarshal([]byte(card), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Config.WidthMode != "compact" || len(doc.Body.Elements) != 3 {
			t.Fatal("expected compact card with title, action and attribution")
		}
		requireCardMentions(t, card, id)
		if id == "ou_b" && calls[id] == 1 {
			return "", "", errors.New("temporary send failure")
		}
		return "sent", "", nil
	}
	chat := &LarkChat{BridgeId: "feishu:test", ChatID: "oc_group", ThreadID: "omt_thread", RootMsgID: "root"}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn", "done", "", time.Minute); err == nil {
		t.Fatal("expected send error")
	}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn", "done", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if calls["ou_a"] != 1 || calls["ou_b"] != 2 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestCompletionNotificationDispatchFreezesBindingAndHasOwnDeadline(t *testing.T) {
	loadBridgeTestConfig(t, "[bridge.feishu.presentation]\ncompletion_notification_enabled = true\ncompletion_notification_after_seconds = 1\n")
	b, _ := newBridgeForInputTest(t)
	b.bridgeID = "feishu:test"
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: "feishu:test", RootMsgID: "root", MessageID: "msg", TurnID: "turn", OpenID: "ou_user"}}); err != nil {
		t.Fatal(err)
	}
	lookupStarted, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("notification did not finish")
		}
	})
	b.topicRootLookup = func(ctx context.Context, root string) (topicRootMessage, error) {
		context.AfterFunc(ctx, func() { close(finished) })
		close(lookupStarted)
		<-release
		return topicRootMessage{ChatID: "oc_original", ThreadID: "omt_original"}, nil
	}
	type delivery struct {
		id, card  string
		remaining time.Duration
	}
	delivered := make(chan delivery, 1)
	b.dmInteractive = func(ctx context.Context, id, card string) (string, string, error) {
		deadline, _ := ctx.Deadline()
		delivered <- delivery{id, card, time.Until(deadline)}
		return "sent", "", nil
	}
	chat := &LarkChat{BridgeId: "feishu:test", ChatID: "oc_original", RootMsgID: "root", DisplayTitle: "original task"}
	b.dispatchTurnCompletionNotification(chat, "turn", "done", "", time.Minute)
	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatal("lookup did not start")
	}
	chat.ChatID, chat.ThreadID, chat.DisplayTitle = "oc_next", "omt_next", "next task"
	unblock()
	select {
	case got := <-delivered:
		if got.id != "ou_user" || !strings.Contains(got.card, "original task") || !strings.Contains(got.card, "omt_original") || strings.Contains(got.card, "next task") {
			t.Fatalf("delivery changed with next turn: %+v", got)
		}
		if got.remaining <= 0 || got.remaining > 15*time.Second {
			t.Fatalf("unexpected independent deadline: %s", got.remaining)
		}
	case <-time.After(time.Second):
		t.Fatal("notification not delivered")
	}
}

func TestCompletionNotificationThresholdUsesCompletionTimeNotDispatchDelay(t *testing.T) {
	loadBridgeTestConfig(t, "[bridge.feishu.presentation]\ncompletion_notification_enabled = true\ncompletion_notification_after_seconds = 60\n")
	b, _ := newBridgeForInputTest(t)
	b.bridgeID = "feishu:test"
	finishedAt := time.Now().Add(-time.Hour)
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: "feishu:test", RootMsgID: "root", MessageID: "msg", TurnID: "turn", OpenID: "ou_user", SubmittedAt: finishedAt.Add(-59 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	b.dmInteractive = func(context.Context, string, string) (string, string, error) {
		t.Fatal("dispatch delay must not qualify a short task for a notification")
		return "", "", nil
	}
	chat := &LarkChat{BridgeId: "feishu:test", ChatID: "oc_original", RootMsgID: "root", ThreadID: "omt_original"}
	if err := b.notifyTurnCompletionAt(context.Background(), chat, "turn", "done", "", 59*time.Second, finishedAt); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionNotificationCardQuietVisualHierarchy(t *testing.T) {
	for _, kind := range []string{turnNotificationCompleted, turnNotificationWaiting, turnNotificationFailed} {
		t.Run(kind, func(t *testing.T) {
			var doc cardDoc
			if err := json.Unmarshal([]byte(renderTurnCompletionNotificationCard(kind, strings.Repeat("长任务", 50), "https://example.com/result", "ou_user")), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Header.Icon != nil || len(doc.Body.Elements) != 3 {
				t.Fatal("expected quiet header and three compact body elements")
			}
			title, action, footer := doc.Body.Elements[0], doc.Body.Elements[1], doc.Body.Elements[2]
			if title.Text.Tag != "plain_text" || title.Text.Lines != 2 {
				t.Fatal("task title must be plain text limited to two lines")
			}
			if action.Type != "primary" || action.Width != "" || action.Size != "small" {
				t.Fatal("expected small outline button at natural width")
			}
			wantChinese := `<font color='grey'>来自 amux | 发送给:</font> <at id="ou_user"></at>`
			wantEnglish := `<font color='grey'>From amux | Sent to:</font> <at id="ou_user"></at>`
			if footer.Tag != "markdown" || footer.TextSize != "notation" || footer.Content != wantChinese ||
				footer.I18nContent["zh_cn"] != wantChinese || footer.I18nContent["en_us"] != wantEnglish {
				t.Fatalf("expected localized attribution with recipient mention: %+v", footer)
			}
		})
	}
}

func TestCompletionNotificationResolvesMissingThreadWithoutWrongChatFallback(t *testing.T) {
	b := &Bridge{bridgeID: "feishu:test"}
	b.topicRootLookup = func(context.Context, string) (topicRootMessage, error) {
		return topicRootMessage{ChatID: "oc_source", ThreadID: "omt_source"}, nil
	}
	chat := &LarkChat{ChatID: "oc_source", RootMsgID: "om_root"}
	link, err := b.completionNotificationURL(context.Background(), chat)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	if u.Path != "/client/thread/open" || u.Query().Get("open_thread_id") != "omt_source" {
		t.Fatalf("link = %s", link)
	}
	b.topicRootLookup = func(context.Context, string) (topicRootMessage, error) {
		return topicRootMessage{ChatID: "oc_other", ThreadID: "omt_other"}, nil
	}
	if _, err := b.completionNotificationURL(context.Background(), chat); err == nil {
		t.Fatal("must reject mismatched source")
	}
}

func TestCompletionNotificationUsesThresholdDMsInitiatorsAndDeduplicates(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.presentation]
completion_notification_enabled = true
completion_notification_after_seconds = 60
`)
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	if err := db.RecordTurnLinks([]TurnLink{{
		BridgeID: "feishu:test", RootMsgID: "root-1", MessageID: "message-1",
		TargetID: "target-1", SessionID: "session-id-1", SessionName: "session-1",
		LaunchID: "launch-1", TurnID: "turn-1", OpenID: "ou_initiator",
	}, {
		BridgeID: "feishu:test", RootMsgID: "root-1", MessageID: "message-2",
		TargetID: "target-1", SessionID: "session-id-1", SessionName: "session-1",
		LaunchID: "launch-1", TurnID: "turn-1", OpenID: "ou_collaborator",
	}}); err != nil {
		t.Fatalf("RecordTurnLinks: %v", err)
	}

	var notices []string
	b := &Bridge{
		bridgeID: "feishu:test",
		db:       db,
		messagingPort: messagingPort{dmInteractive: func(_ context.Context, openID, card string) (string, string, error) {
			notices = append(notices, openID+card)
			return "sent", "", nil
		}},
	}
	chat := &LarkChat{BridgeId: "feishu:test", ChatID: "oc-group", RootMsgID: "root-1", ThreadID: "omt_thread", DisplayTitle: "飞书通知体验"}

	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-1", "done", "end_turn", 59*time.Second); err != nil {
		t.Fatalf("notify below threshold: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("notices below threshold = %q, want none", notices)
	}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-1", "done", "end_turn", 61*time.Second); err != nil {
		t.Fatalf("notify completion: %v", err)
	}
	if len(notices) != 2 || !strings.Contains(strings.Join(notices, ""), "ou_initiator") || !strings.Contains(strings.Join(notices, ""), "ou_collaborator") {
		t.Fatalf("completion notices = %q, want a DM for each initiator", notices)
	}
	for _, card := range notices {
		for _, want := range []string{"已完成", "飞书通知体验", "查看结果 →", "来自 amux", "client/thread/open", "omt_thread"} {
			if !strings.Contains(card, want) {
				t.Fatalf("card missing %q: %s", want, card)
			}
		}
		openID := "ou_initiator"
		if strings.Contains(card, "ou_collaborator") {
			openID = "ou_collaborator"
		}
		jsonStart := strings.Index(card, "{")
		if jsonStart < 0 {
			t.Fatalf("DM for %q has no card payload: %s", openID, card)
		}
		raw := card[jsonStart:]
		requireCardMentions(t, raw, openID)
		otherOpenID := "ou_collaborator"
		if openID == otherOpenID {
			otherOpenID = "ou_initiator"
		}
		if strings.Contains(raw, otherOpenID) {
			t.Fatalf("DM for %q unexpectedly mentions %q: %s", openID, otherOpenID, raw)
		}
	}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-1", "done", "end_turn", 62*time.Second); err != nil {
		t.Fatalf("notify duplicate completion: %v", err)
	}
	if len(notices) != 2 {
		t.Fatalf("duplicate completion notices = %q, want one", notices)
	}
}

func TestCompletionNotificationThresholdAppliesToWaitingAndFailure(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.presentation]
completion_notification_enabled = true
completion_notification_after_seconds = 60
`)
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	for _, turnID := range []string{"turn-waiting", "turn-failed", "turn-cancelled"} {
		if err := db.RecordTurnLinks([]TurnLink{{
			BridgeID: "feishu:test", RootMsgID: "root-1", MessageID: "message-" + turnID,
			TurnID: turnID, OpenID: "ou_initiator",
		}}); err != nil {
			t.Fatalf("RecordTurnLinks(%s): %v", turnID, err)
		}
	}

	var notices []string
	b := &Bridge{
		bridgeID: "feishu:test",
		db:       db,
		messagingPort: messagingPort{dmInteractive: func(_ context.Context, _, text string) (string, string, error) {
			notices = append(notices, text)
			return "sent", "", nil
		}},
	}
	chat := &LarkChat{BridgeId: "feishu:test", ChatID: "oc-group", RootMsgID: "root-1", ThreadID: "omt_thread"}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-waiting", "waiting", "end_turn", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-failed", "failed", "provider_error", time.Second); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 {
		t.Fatalf("below threshold notices = %q, want none", notices)
	}
	for _, status := range []string{"waiting", "failed"} {
		if err := b.notifyTurnCompletion(context.Background(), chat, "turn-"+status, status, "", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-cancelled", "done", "cancelled", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-session-closed", "done", "session_closed", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-stopped", "stopped", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 || !strings.Contains(notices[0], "需要你补充") || !strings.Contains(notices[1], "处理失败") {
		t.Fatalf("notices = %q, want waiting and failure only", notices)
	}
}

func TestCompletionNotificationZeroThresholdDisablesAllKinds(t *testing.T) {
	loadBridgeTestConfig(t, "[bridge.feishu.presentation]\ncompletion_notification_enabled = true\ncompletion_notification_after_seconds = 0\n")
	b, _ := newBridgeForInputTest(t)
	b.bridgeID = "feishu:test"
	b.dmInteractive = func(context.Context, string, string) (string, string, error) {
		t.Fatal("zero threshold must disable every completion DM")
		return "", "", nil
	}
	chat := &LarkChat{BridgeId: "feishu:test", ChatID: "oc_group", RootMsgID: "root", ThreadID: "omt_thread"}
	for _, status := range []string{"done", "waiting", "failed"} {
		if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: "feishu:test", RootMsgID: "root", MessageID: status, TurnID: status, OpenID: "ou_user"}}); err != nil {
			t.Fatal(err)
		}
		if err := b.notifyTurnCompletion(context.Background(), chat, status, status, "", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompletionNotificationMentionsRecipientInPrivateWorkspace(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.presentation]
completion_notification_enabled = true
completion_notification_after_seconds = 1
`)
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	if err := db.EnsureWorkspaceKind("feishu:test", "oc-private", WorkspaceKindPrivate); err != nil {
		t.Fatalf("EnsureWorkspaceKind: %v", err)
	}
	if err := db.RecordTurnLinks([]TurnLink{{
		BridgeID: "feishu:test", RootMsgID: "root-private", MessageID: "message-private",
		TurnID: "turn-private", OpenID: "ou_initiator",
	}}); err != nil {
		t.Fatalf("RecordTurnLinks: %v", err)
	}
	var notice string
	b := &Bridge{
		bridgeID: "feishu:test",
		db:       db,
		messagingPort: messagingPort{dmInteractive: func(_ context.Context, _, text string) (string, string, error) {
			notice = text
			return "sent", "", nil
		}},
	}
	chat := &LarkChat{BridgeId: "feishu:test", ChatID: "oc-private", RootMsgID: "root-private", ThreadID: "omt_private"}
	if err := b.notifyTurnCompletion(context.Background(), chat, "turn-private", "done", "end_turn", time.Second); err != nil {
		t.Fatal(err)
	}
	if notice == "" {
		t.Fatal("private notice is empty")
	}
	requireCardMentions(t, notice, "ou_initiator")
}

func TestCompletionNotificationInheritsSourceChatOfFirstMessage(t *testing.T) {
	loadBridgeTestConfig(t, "[bridge.feishu.presentation]\ncompletion_notification_enabled = true\ncompletion_notification_after_seconds = 1\n")
	b, _ := newBridgeForInputTest(t)
	b.bridgeID = "feishu:test"
	root := singletonStreamKey("feishu:test")
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: "feishu:test", RootMsgID: root, MessageID: "m1", TurnID: "turn", OpenID: "ou_user"}}); err != nil {
		t.Fatal(err)
	}
	// First message's chat wins; a later steered message from another chat
	// does not override it.
	b.recordTurnSourceChat(root, "turn", "oc_first", "")
	b.recordTurnSourceChat(root, "turn", "oc_other", "")
	var sent string
	b.dmInteractive = func(_ context.Context, _, card string) (string, string, error) {
		sent = card
		return "ok", "", nil
	}
	streamChat := &LarkChat{BridgeId: "feishu:test", RootMsgID: root} // singleton stream: no chat
	if err := b.notifyTurnCompletion(context.Background(), streamChat, "turn", "done", "", time.Minute); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !strings.Contains(sent, url.Values{"openChatId": {"oc_first"}}.Encode()) {
		t.Fatalf("card does not link the first message's chat: %s", sent)
	}
}

func TestCompletionNotificationSkipsSubtaskTurnSilently(t *testing.T) {
	loadBridgeTestConfig(t, "[bridge.feishu.presentation]\ncompletion_notification_enabled = true\ncompletion_notification_after_seconds = 1\n")
	b, _ := newBridgeForInputTest(t)
	b.bridgeID = "feishu:test"
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: "feishu:test", RootMsgID: "root", MessageID: "m1", TurnID: "turn", OpenID: "ou_user"}}); err != nil {
		t.Fatal(err)
	}
	b.markSubtaskTurn("root", "turn", "")
	b.dmInteractive = func(context.Context, string, string) (string, string, error) {
		t.Fatal("subtask turn must not notify")
		return "", "", nil
	}
	if err := b.notifyTurnCompletion(context.Background(), &LarkChat{BridgeId: "feishu:test", RootMsgID: "root"}, "turn", "done", "", time.Minute); err != nil {
		t.Fatalf("subtask turn returned error (would warn): %v", err)
	}
}
