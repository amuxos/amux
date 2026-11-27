package bridge

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/bridge/threadcontext"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestOrdinaryGroupQuotedReplyUsesParentAsContextAndCurrentMessageAsRoot(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", BotName: "Helper", BotOpenId: "ou-helper"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		if messageID != "om-parent" {
			t.Fatalf("quoted message id = %q, want om-parent", messageID)
		}
		return feishuQuotedMessage{MessageID: messageID, ChatID: "oc-group", MsgType: "text", Text: "登录回调偶发失败"}, nil
	}
	event := feishuMessageEvent("group", "oc-group", "om-trigger", "owner-open", "@_helper 帮我定位根因")
	event.Event.Message.ParentId = testStringPtr("om-parent")
	addStructuredBotMention(event, "@_helper", "Helper", "ou-helper")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 2 || hub.routes[0].method != "Run" || hub.routes[1].method != "Submit" {
		t.Fatalf("routes = %+v, want Run then Submit", hub.routes)
	}
	submit := hub.routes[1].payload.(*pb.SubmitRequest)
	assertQuotedPrompt(t, submit.Text, "登录回调偶发失败", "帮我定位根因")
	chat, err := b.db.FindByRootMsg(b.currentBridgeId(), "om-trigger")
	if err != nil {
		t.Fatalf("FindByRootMsg: %v", err)
	}
	if chat == nil || chat.RootMsgID != "om-trigger" {
		t.Fatalf("chat = %+v, ordinary group quote must use trigger as new root", chat)
	}
	if parentChat, err := b.db.FindByRootMsg(b.currentBridgeId(), "om-parent"); err != nil || parentChat != nil {
		t.Fatalf("parent binding = %+v, err = %v; quote must not take over parent", parentChat, err)
	}
}

func TestOrdinaryGroupQuoteOnlyMentionCreatesRequestFromExplicitReply(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", BotName: "Helper", BotOpenId: "ou-helper"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		return feishuQuotedMessage{MessageID: messageID, ChatID: "oc-group", MsgType: "text", Text: "只回复并 @bot 的引用正文"}, nil
	}
	event := feishuMessageEvent("group", "oc-group", "om-trigger", "owner-open", "@_helper")
	event.Event.Message.ParentId = testStringPtr("om-parent")
	addStructuredBotMention(event, "@_helper", "Helper", "ou-helper")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 2 || hub.routes[0].method != "Run" || hub.routes[1].method != "Submit" {
		t.Fatalf("routes = %+v, quote-only mention must Run then Submit", hub.routes)
	}
	assertQuotedPrompt(t, hub.routes[1].payload.(*pb.SubmitRequest).Text,
		"只回复并 @bot 的引用正文", defaultQuotedMessageRequest())
}

func TestFreshTopicQuoteOfRootDoesNotDuplicateRootPrompt(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", NotifyChatId: "oc-topic", BotName: "Helper"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	b.topicRootLookup = func(context.Context, string) (topicRootMessage, error) {
		t.Fatal("quote of the root must not perform a second topic-root lookup")
		return topicRootMessage{}, nil
	}
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		return feishuQuotedMessage{MessageID: messageID, ChatID: "oc-topic", MsgType: "text", Text: "话题根消息正文"}, nil
	}
	event := feishuThreadReplyEvent("group", "oc-topic", "om-reply", "om-root", "owner-open", "@Helper")
	event.Event.Message.ParentId = testStringPtr("om-root")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 2 || hub.routes[1].method != "Submit" {
		t.Fatalf("routes = %+v, want Run then Submit", hub.routes)
	}
	text := hub.routes[1].payload.(*pb.SubmitRequest).Text
	assertQuotedPrompt(t, text, "话题根消息正文", defaultQuotedMessageRequest())
	if count := strings.Count(text, "话题根消息正文"); count != 1 {
		t.Fatalf("quoted root count = %d in %q, want exactly one", count, text)
	}
	delivered, err := b.db.TopicRootPromptDelivered(b.currentBridgeId(), "om-root")
	if err != nil {
		t.Fatalf("TopicRootPromptDelivered: %v", err)
	}
	if !delivered {
		t.Fatal("quoted root must mark topic root delivered after successful Submit")
	}
}

func TestActiveThreadQuotedReplyUsesImmediateParentNotRoot(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", NotifyChatId: "oc-workspace", BotName: "Helper"}
	insertActiveChat(t, b.db)
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		if messageID != "om-immediate-parent" {
			t.Fatalf("quoted message id = %q, want immediate parent", messageID)
		}
		return feishuQuotedMessage{MessageID: messageID, ChatID: "oc-workspace", Text: "Reviewer 指出需要补并发测试"}, nil
	}
	event := feishuThreadReplyEvent("group", "oc-workspace", "om-reply", "root-1", "owner-open", "@Helper 按这个意见修改")
	event.Event.Message.ParentId = testStringPtr("om-immediate-parent")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 1 || hub.routes[0].method != "Submit" {
		t.Fatalf("routes = %+v, want one Submit", hub.routes)
	}
	assertQuotedPrompt(t, hub.routes[0].payload.(*pb.SubmitRequest).Text, "Reviewer 指出需要补并发测试", "按这个意见修改")
}

func TestActiveThreadRootParentFollowupDoesNotRepeatQuotedPrompt(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", NotifyChatId: "oc-workspace", BotName: "Helper"}
	insertActiveChat(t, b.db)
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		t.Fatalf("ordinary topic follow-up must not look up root quote %q", messageID)
		return feishuQuotedMessage{}, nil
	}
	event := feishuThreadReplyEvent("group", "oc-workspace", "om-followup", "root-1", "owner-open", "@Helper 帮我验证下是否生效吧")
	event.Event.Message.ParentId = testStringPtr("root-1")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 1 || hub.routes[0].method != "Submit" {
		t.Fatalf("routes = %+v, want one Submit", hub.routes)
	}
	text := hub.routes[0].payload.(*pb.SubmitRequest).Text
	if strings.Contains(text, "[Amux Feishu Quoted Reply]") || strings.Contains(text, "[Quoted message]") {
		t.Fatalf("ordinary topic follow-up repeated quoted prompt: %q", text)
	}
	if text != "帮我验证下是否生效吧" {
		t.Fatalf("submit text = %q, want plain follow-up", text)
	}
}

func TestQuotedReplyUsesLocallyRecordedAgentCardTextBeforeAPI(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", NotifyChatId: "oc-workspace", BotName: "Helper"}
	insertActiveChat(t, b.db)
	if err := b.threadContext.RecordEvent(context.Background(), threadcontext.ThreadEvent{
		ChatID: "oc-workspace", RootMsgID: "root-1", MessageID: "om-card", EventType: threadcontext.EventTypeAgentOutput,
		ActorType: "agent", ActorID: "reviewer", BridgeId: "feishu:reviewer", Text: "这是 CardKit 中最终展示的审核结论。",
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		return feishuQuotedMessage{MessageID: messageID, ChatID: "oc-workspace", MsgType: "interactive"}, nil
	}
	event := feishuThreadReplyEvent("group", "oc-workspace", "om-reply", "root-1", "owner-open", "@Helper 继续处理")
	event.Event.Message.ParentId = testStringPtr("om-card")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	assertQuotedPrompt(t, hub.routes[0].payload.(*pb.SubmitRequest).Text, "这是 CardKit 中最终展示的审核结论。", "继续处理")
}

func TestFreshQuotedReplyFailureDoesNotCreateAgentTurn(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", BotName: "Helper", BotOpenId: "ou-helper"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	var notices []string
	b.threadNotice = func(_ context.Context, rootMsgID, text string) error {
		notices = append(notices, rootMsgID+":"+text)
		return nil
	}
	b.quotedMessageLookup = func(context.Context, string) (feishuQuotedMessage, error) {
		return feishuQuotedMessage{}, fmt.Errorf("missing im:message.group_msg")
	}
	event := feishuMessageEvent("group", "oc-group", "om-trigger", "owner-open", "@_helper 解释一下")
	event.Event.Message.ParentId = testStringPtr("om-parent")
	addStructuredBotMention(event, "@_helper", "Helper", "ou-helper")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("routes = %+v, unreadable quote must not Run or Submit", hub.routes)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "om-trigger:无法读取被引用的消息") {
		t.Fatalf("notices = %q, want quote failure at trigger", notices)
	}
}

func TestSingletonQuotedReplyInjectsContext(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.default.interaction]
mode = "singleton"
`)
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", BotName: "Helper", BotOpenId: "ou-helper"}
	if err := b.db.SetBotSession(BotSession{
		BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "runtime-1", TargetName: "local",
		SessionName: "bot-a-main", AgentType: "codex",
	}); err != nil {
		t.Fatalf("SetBotSession: %v", err)
	}
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		return feishuQuotedMessage{MessageID: messageID, ChatID: "oc-group", Text: "发布流水线失败在测试阶段"}, nil
	}
	event := feishuMessageEvent("group", "oc-group", "om-trigger", "owner-open", "@_helper")
	event.Event.Message.ParentId = testStringPtr("om-parent")
	addStructuredBotMention(event, "@_helper", "Helper", "ou-helper")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 1 || hub.routes[0].method != "Submit" {
		t.Fatalf("routes = %+v, want singleton Submit", hub.routes)
	}
	assertQuotedPrompt(t, hub.routes[0].payload.(*pb.SubmitRequest).Text,
		"发布流水线失败在测试阶段", defaultQuotedMessageRequest())
}

func TestPrivateQuotedReplyInjectsContext(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", BotName: "Helper"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	b.threadNotice = func(context.Context, string, string) error { return nil }
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		return feishuQuotedMessage{MessageID: messageID, ChatID: "oc-private", Text: "私聊里已有的需求说明"}, nil
	}
	event := feishuMessageEvent("p2p", "oc-private", "om-reply", "owner-open", "继续实现")
	event.Event.Message.ParentId = testStringPtr("om-parent")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 2 || hub.routes[0].method != "Run" || hub.routes[1].method != "Submit" {
		t.Fatalf("routes = %+v, want private Run then Submit", hub.routes)
	}
	assertQuotedPrompt(t, hub.routes[1].payload.(*pb.SubmitRequest).Text, "私聊里已有的需求说明", "继续实现")
}

func TestResolveQuotedMessageRejectsCrossChatResult(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		return feishuQuotedMessage{MessageID: messageID, ChatID: "oc-other", Text: "不应泄漏"}, nil
	}
	ctx := withFeishuQuotedMessage(context.Background(), feishuQuotedMessageRef{messageID: "om-parent", chatID: "oc-current"})
	if _, present, err := b.resolveFeishuQuotedMessage(ctx); !present || err == nil {
		t.Fatalf("present = %v, err = %v; want cross-chat rejection", present, err)
	}
}

func TestTruncateFeishuQuotedMessageHonorsRuneLimit(t *testing.T) {
	t.Parallel()
	got := truncateFeishuQuotedMessage(strings.Repeat("引", maxFeishuQuotedMessageRunes+50))
	if len([]rune(got)) != maxFeishuQuotedMessageRunes || !strings.HasSuffix(got, "[Quoted message truncated]") {
		t.Fatalf("quoted text rune count=%d suffix=%q", len([]rune(got)), got[len(got)-30:])
	}
}

func assertQuotedPrompt(t *testing.T, text, quoted, request string) {
	t.Helper()
	for _, want := range []string{
		"[Amux Feishu Quoted Reply]",
		"[Quoted message]\n" + quoted,
		"[Conversation input]",
		request,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("submit text = %q, want %q", text, want)
		}
	}
}
