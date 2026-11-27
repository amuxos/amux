package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func setReplyEndNotificationForTest(t *testing.T, b *Bridge, on bool) {
	t.Helper()
	saved, err := config.ReadExperimentsSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{
		CompletionNotification: saved.CompletionNotification, ReplyEndNotification: &on,
	}); err != nil {
		t.Fatal(err)
	}
	b.replyEndNotification.Store(on)
}

func TestReplyEndExcerptUsesProseAndRedactsSecrets(t *testing.T) {
	t.Parallel()
	answer := "# 交付结果\n\n```sh\nsecret=do-not-forward\nhttps://example.com/demo/pull/99\n```\n" +
		"- **已修复缓存失效问题。** 通过校验。\n" +
		"[MR](https://example.com/team/cache/-/merge_requests/42?token=private-value#diff)"
	if got := replyEndExcerpt(answer); got != "已修复缓存失效问题。 通过校验。" {
		t.Fatalf("excerpt = %q", got)
	}
	for _, text := range []string{
		"已处理，token=private-token password=private-password Authorization: Bearer private-bearer",
		"已处理，API_KEY=sk-123456789secret /Users/user/project/config.json",
		"请在 [授权入口](https://auth.example/login?secret=private-url) 完成授权。",
		"已处理，access_key=private-access AppSecret=private-app client_secret=private-client private_key=private-pem",
		"请使用 custom://auth.example/login?token=private-url 完成授权。",
	} {
		excerpt := replyEndExcerpt(text)
		for _, secret := range []string{"private-token", "private-password", "private-bearer", "sk-123456789secret", "/Users/user", "auth.example", "private-url", "private-access", "private-app", "private-client", "private-pem"} {
			if strings.Contains(excerpt, secret) {
				t.Fatalf("excerpt leaked %q: %s", secret, excerpt)
			}
		}
	}
	if got := replyEndExcerpt(strings.Repeat("已完成修复", 30)); utf8.RuneCountInString(got) > replyEndExcerptMaxRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("unbounded excerpt: %q", got)
	}
	if got := replyEndExcerpt("Updated version 1.2.3 successfully. All checks passed."); got != "Updated version 1.2.3 successfully." {
		t.Fatalf("version split excerpt: %q", got)
	}
	if got := replyEndExcerpt("# Result\n```text\nonly code\n```"); got != "" {
		t.Fatalf("code/header-only answer must use status fallback: %q", got)
	}
	if got := replyEndExcerpt("    access_key=private-indented\nThe fix is **ready**."); got != "The fix is ready." {
		t.Fatalf("indented code or emphasis leaked into excerpt: %s", got)
	}
}

func TestReplyEndExcerptPrefersExplicitConclusion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ answer, want string }{
		{
			"我先检查了配置。\n" + strings.Repeat("检查了各项指标。\n", 80) +
				"## 最终结论\n修复已完成，但尚未测试，需用户复测。",
			"修复已完成，但尚未测试，需用户复测。",
		},
		{
			"I checked the configuration.\n**Conclusion:** The fix is ready. Tests are pending.",
			"The fix is ready. Tests are pending.",
		},
		{
			"First usable sentence. More details.\n## Result\n[Details](https://example.com)\n## Appendix",
			"Details",
		},
		{
			"First usable sentence. More details.\n## Result\nhttps://example.com\n## Appendix\nAppendix text.",
			"First usable sentence.",
		},
	} {
		if got := replyEndExcerpt(tc.answer); got != tc.want {
			t.Fatalf("excerpt = %q, want %q", got, tc.want)
		}
	}
}

type replyEndTestHub struct {
	Hub
	send func(context.Context, string, feishumsg.PersonalRequest) error
}

func (h replyEndTestHub) Route(ctx context.Context, targetID, method string, payload any) (*pb.TunnelMsg, error) {
	if method != feishumsg.PersonalRoute {
		return h.Hub.Route(ctx, targetID, method, payload)
	}
	if err := h.send(ctx, targetID, payload.(feishumsg.PersonalRequest)); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(feishumsg.PersonalResult{Result: feishumsg.Result{MessageID: "om_delivery"}})
	return &pb.TunnelMsg{Payload: raw}, err
}

func replyEndFixture(t *testing.T) (*Bridge, *LarkChat, *pb.Session) {
	t.Helper()
	loadBridgeTestConfig(t, "[bridge.feishu.presentation]\ncompletion_notification_enabled = true\ncompletion_notification_after_seconds = 1\n")
	b, hub := newBridgeForInputTest(t)
	setReplyEndNotificationForTest(t, b, true)
	b.SetFeishuPrincipalResolver(func(id string) (string, error) { return "principal-" + id, nil })
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root-a", ChatID: "oc_source", ThreadID: "omt_a",
		OpenId: "ou_a", TargetID: "target", SessionName: "topic-a", DisplayTitle: "Topic A"}
	session := &pb.Session{Id: "session-a", Name: chat.SessionName, ExecutionIdentity: principal.ToProtoIdentity(principal.ExecutionIdentity{
		IdentityMode: principal.IdentityModeIdentified, Source: principal.SourceFeishu, IsolationBackend: principal.BackendLinuxDAC,
		EffectivePrincipalID: "principal-ou_a", OriginActorPrincipalID: "principal-ou_a",
	})}
	hub.targets = []*pb.Target{{Id: chat.TargetID, Sessions: []*pb.Session{session}}}
	return b, chat, session
}

func recordReplyEndInput(t *testing.T, b *Bridge, chat *LarkChat, id, sender string) {
	t.Helper()
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: chat.BridgeId, RootMsgID: chat.RootMsgID, MessageID: id, TurnID: id, OpenID: sender}}); err != nil {
		t.Fatal(err)
	}
}

func TestReplyEndRoutesEachTopicToItsOwnPrincipal(t *testing.T) {
	b, chat, session := replyEndFixture(t)
	hub := b.hub.(*bridgeTestHub)
	otherChat := *chat
	otherChat.RootMsgID, otherChat.ThreadID, otherChat.SessionName, otherChat.OpenId = "root-b", "omt_b", "topic-b", "ou_b"
	otherSession := &pb.Session{Id: "session-b", Name: otherChat.SessionName, ExecutionIdentity: principal.ToProtoIdentity(principal.ExecutionIdentity{
		IdentityMode: principal.IdentityModeIdentified, Source: principal.SourceFeishu, IsolationBackend: principal.BackendLinuxDAC,
		EffectivePrincipalID: "principal-ou_b", OriginActorPrincipalID: "principal-ou_b",
	})}
	hub.targets[0].Sessions = append(hub.targets[0].Sessions, otherSession)
	var sent []feishumsg.PersonalRequest
	b.hub = replyEndTestHub{hub, func(_ context.Context, target string, req feishumsg.PersonalRequest) error {
		if target != chat.TargetID {
			t.Fatalf("wrong target %q", target)
		}
		sent = append(sent, req)
		return nil
	}}
	b.dmInteractive = func(context.Context, string, string) (string, string, error) {
		t.Fatal("legacy notification sent while reply-end is enabled")
		return "", "", nil
	}

	for _, tc := range []struct {
		chat              *LarkChat
		sessionID, turnID string
		sender, status    string
		elapsed           time.Duration
	}{
		{chat, session.Id, "turn-a", "ou_b", "done", time.Minute},
		{&otherChat, otherSession.Id, "turn-b", "ou_b", "waiting", time.Millisecond},
	} {
		recordReplyEndInput(t, b, tc.chat, tc.turnID, tc.sender)
		if err := b.db.setManagementLocale(chat.BridgeId, tc.chat.OpenId, "zh-CN"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := b.notifyTurnCompletionAt(context.Background(), tc.chat, tc.turnID, tc.status, "", tc.elapsed, time.Now(),
				"检查配置。\n**结论：** 完成。\n[MR](https://example.com/repo/pull/12)", tc.sessionID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(sent) != 2 {
		t.Fatalf("expected one delivery per topic, got %d", len(sent))
	}
	for i, topic := range []*LarkChat{chat, &otherChat} {
		req := sent[i]
		link := feishuConversationURL(topic.ChatID, topic.ThreadID)
		if req.SessionID != hub.targets[0].Sessions[i].Id || req.PrincipalID != "principal-"+topic.OpenId ||
			req.Message.ReceiveIDType != feishumsg.Self || req.Message.Validate() != nil ||
			!strings.Contains(req.Message.Text, "完成。\n[打开源话题]("+link+")") ||
			strings.Contains(req.Message.Text, "example.com") || strings.Count(req.Message.Text, "](") != 1 {
			t.Fatalf("wrong destination or content: %+v", req)
		}
	}
}

func TestReplyEndSkipsIneligibleSessionsAndTurns(t *testing.T) {
	for _, name := range []string{"stale session", "ordinary topic", "singleton", "group singleton", "shared principal", "wrong topic principal", "unattributed", "cancelled", "superseded", "subtask"} {
		t.Run(name, func(t *testing.T) {
			b, chat, session := replyEndFixture(t)
			b.hub = replyEndTestHub{b.hub, func(context.Context, string, feishumsg.PersonalRequest) error {
				t.Fatal("ineligible turn requested personal delivery")
				return nil
			}}
			id, reason := session.Id, ""
			switch name {
			case "stale session":
				id = "old-session-id"
			case "ordinary topic":
				session.ExecutionIdentity.IsolationBackend = principal.BackendPassthrough
			case "singleton":
				chat.RootMsgID = singletonStreamKey(chat.BridgeId)
			case "group singleton":
				if _, err := b.db.db.Exec(`INSERT INTO feishu_group_session_origins (bridge_id, chat_id, origin_root_msg_id, session_root_msg_id) VALUES (?, ?, ?, ?)`, chat.BridgeId, chat.ChatID, chat.RootMsgID, chat.RootMsgID); err != nil {
					t.Fatal(err)
				}
			case "shared principal":
				session.ExecutionIdentity.EffectivePrincipalId = "service"
			case "wrong topic principal":
				chat.OpenId = "ou_b"
			case "cancelled", "superseded":
				reason = name
			case "subtask":
				b.markSubtaskTurn(chat.RootMsgID, "turn", "")
			}
			if name != "unattributed" {
				recordReplyEndInput(t, b, chat, "turn", "ou_a")
			}
			if err := b.notifyTurnCompletionAt(context.Background(), chat, "turn", "done", reason, time.Minute, time.Now(), "Done.", id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplyEndPreservesUnknownDeliveryClaim(t *testing.T) {
	b, chat, session := replyEndFixture(t)
	recordReplyEndInput(t, b, chat, "turn", "ou_a")
	chat.ThreadID = ""
	b.topicRootLookup = func(ctx context.Context, _ string) (topicRootMessage, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Error("source lookup must leave time for fallback delivery")
		}
		return topicRootMessage{ChatID: "oc_wrong", ThreadID: "omt_wrong"}, nil
	}
	calls := 0
	b.hub = replyEndTestHub{b.hub, func(_ context.Context, _ string, req feishumsg.PersonalRequest) error {
		calls++
		want := "This turn failed. Check the original conversation for details.\nSource topic link unavailable."
		if !strings.Contains(req.Message.Text, want) || strings.Contains(req.Message.Text, "applink") {
			t.Errorf("wrong fallback content: %s", req.Message.Text)
		}
		return errors.New("lost delivery response")
	}}
	notify := func() error {
		return b.notifyTurnCompletionAt(context.Background(), chat, "turn", "failed", "", time.Minute, time.Now(), "", session.Id)
	}
	if err := notify(); err == nil {
		t.Fatal("expected uncertain delivery to be reported")
	}
	if err := notify(); err != nil || calls != 1 {
		t.Fatalf("retried an uncertain delivery: calls=%d err=%v", calls, err)
	}
}

func TestReplyEndOffRestoresLegacyDuration(t *testing.T) {
	b, chat, session := replyEndFixture(t)
	setReplyEndNotificationForTest(t, b, false)
	recordReplyEndInput(t, b, chat, "turn", "ou_a")
	count := 0
	b.dmInteractive = func(_ context.Context, to, _ string) (string, string, error) {
		if to != chat.OpenId {
			t.Errorf("legacy destination = %q", to)
		}
		count++
		return "om_sent", "", nil
	}
	for _, elapsed := range []time.Duration{time.Millisecond, time.Minute} {
		if err := b.notifyTurnCompletionAt(context.Background(), chat, "turn", "done", "", elapsed, time.Now(), "", session.Id); err != nil {
			t.Fatal(err)
		}
		if elapsed < time.Second && count != 0 {
			t.Fatal("short turn bypassed legacy threshold")
		}
	}
	if count != 1 {
		t.Fatalf("legacy notifications sent = %d", count)
	}
}
