package bridge

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/bridge/threadcontext"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestGroupWatchEventAutomaticallyCreatesThreadTask(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{
		AppId:     "cli_amux",
		OpenId:    "owner-open",
		BotOpenId: "ou_amux",
		BotName:   "GroupWatch 助手",
	}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:             true,
		ChatIDs:             []string{"oc_alarm"},
		SenderTypes:         []string{"user", "app"},
		PollIntervalSeconds: 15,
		LookbackSeconds:     60,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	if err := b.db.SetWorkspaceMode(b.currentBridgeId(), "oc_alarm", WorkspaceModeShared); err != nil {
		t.Fatalf("SetWorkspaceMode: %v", err)
	}
	b.threadNotice = func(context.Context, string, string) error { return nil }

	event := feishuMessageEvent("group", "oc_alarm", "om_alarm", "ou_reporter", "支付成功率跌到 91%，trace_id=abc")
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	if len(hub.routes) != 2 || hub.routes[0].method != "Run" || hub.routes[1].method != "Submit" {
		t.Fatalf("routes = %+v, want Run then Submit", hub.routes)
	}
	runReq := hub.routes[0].payload.(*pb.RunRequest)
	submitReq := hub.routes[1].payload.(*pb.SubmitRequest)
	if runReq.AgentProfile != "builtin/codex" || submitReq.Text != "支付成功率跌到 91%，trace_id=abc" {
		t.Fatalf("run/submit = %+v / %+v, want current profile and original alert", runReq, submitReq)
	}
	if runReq.IdleTimeoutSeconds != 10800 || !runReq.IdleTimeoutSecondsSet {
		t.Fatalf("group watch idle timeout = %d/%v, want explicit 10800", runReq.IdleTimeoutSeconds, runReq.IdleTimeoutSecondsSet)
	}
	chat, err := b.db.FindByRootMsg(b.currentBridgeId(), "om_alarm")
	if err != nil {
		t.Fatalf("FindByRootMsg: %v", err)
	}
	if chat == nil || chat.ChatID != "oc_alarm" || chat.OpenId != "ou_reporter" {
		t.Fatalf("chat = %+v, want alert-root binding attributed to real sender", chat)
	}
	watchBindings, err := b.db.ListGroupWatchCapacityBindingsByChatID(b.currentBridgeId(), "oc_alarm")
	if err != nil {
		t.Fatalf("ListGroupWatchCapacityBindingsByChatID: %v", err)
	}
	if len(watchBindings) != 1 || watchBindings[0].RootMsgID != "om_alarm" {
		t.Fatalf("group watch bindings = %+v, want persisted om_alarm provenance", watchBindings)
	}

	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: runReq.Name, State: "running"}}}
	b.setTurnActive("om_alarm", false)
	reply := feishuThreadReplyEvent("group", "oc_alarm", "om_followup", "om_alarm", "ou_groupWatch", "重启")
	reply.Event.Message.RootId = nil
	reply.Event.Message.ParentId = testStringPtr("om_alarm")
	if err := b.handleMessage(context.Background(), reply); err != nil {
		t.Fatalf("handleMessage unmentioned follow-up: %v", err)
	}
	if err := b.handleMessage(context.Background(), reply); err != nil {
		t.Fatalf("handleMessage duplicate unmentioned follow-up: %v", err)
	}
	if len(hub.routes) != 2 {
		t.Fatalf("routes after unmentioned follow-up = %+v, want no additional Submit", hub.routes)
	}
	pkg, err := b.threadContext.BuildPackage(context.Background(), threadcontext.BuildRequest{
		ChatID:             "oc_alarm",
		RootMsgID:          "om_alarm",
		MessageID:          "om_context_request",
		BridgeId:           b.currentBridgeId(),
		BotProfile:         b.profile,
		WorkspaceMode:      WorkspaceModeShared,
		RequestText:        "继续分析",
		ThreadContextLimit: defaultThreadContextLimit,
	})
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if count := strings.Count(pkg.ThreadContext, "重启"); count != 1 {
		t.Fatalf("thread context = %q, want unmentioned shared discussion retained once, got %d", pkg.ThreadContext, count)
	}

	mentionedReply := feishuThreadReplyEvent(
		"group",
		"oc_alarm",
		"om_mentioned_followup",
		"om_alarm",
		"ou_groupWatch",
		"@_user_1 请结合最新日志继续分析",
	)
	mentionedType := "app"
	mentionedReply.Event.Message.Mentions = []*larkim.MentionEvent{{
		Key:           testStringPtr("@_user_1"),
		MentionedType: &mentionedType,
		Name:          testStringPtr("GroupWatch 助手"),
	}}
	if err := b.handleMessage(context.Background(), mentionedReply); err != nil {
		t.Fatalf("handleMessage mentioned follow-up: %v", err)
	}
	if len(hub.routes) != 3 || hub.routes[2].method != "Submit" {
		t.Fatalf("routes after mentioned follow-up = %+v, want one additional Submit", hub.routes)
	}
	if got := hub.routes[2].payload.(*pb.SubmitRequest).Text; got != "请结合最新日志继续分析" {
		t.Fatalf("mentioned follow-up submit = %q, want mention stripped", got)
	}
}

func TestGroupWatchDedupSuppressesRepeatedAppAlertBeforeSessionCreation(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	setGroupWatchDedupTestCatalog(b)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux", BotName: "GroupWatch 助手"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"},
		DedupEnabled: true, DedupWindowSeconds: 1800,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }

	first := groupWatchBotEvent("oc_alarm", "om_alarm_1", "ou_alarm_bot", "[service][warn]特征监控任务 lag\nRule ID: 2071\nlag: 100K")
	second := groupWatchBotEvent("oc_alarm", "om_alarm_2", "ou_alarm_bot", "[service][warn]特征监控任务 lag\nRule ID: 2071\nlag: 200K")
	if err := b.handleMessage(context.Background(), first); err != nil {
		t.Fatalf("handle first alert: %v", err)
	}
	if err := b.handleMessage(context.Background(), second); err != nil {
		t.Fatalf("handle duplicate alert: %v", err)
	}
	if len(hub.routes) != 2 || hub.routes[0].method != "Run" || hub.routes[1].method != "Submit" {
		t.Fatalf("routes = %+v, want only first alert Run and Submit", hub.routes)
	}
	if chat, err := b.db.FindByRootMsg(b.currentBridgeId(), "om_alarm_2"); err != nil || chat != nil {
		t.Fatalf("duplicate binding = %+v, err=%v; want none", chat, err)
	}
}

func TestGroupWatchDedupDisabledKeepsRepeatedAppAlerts(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"},
		DedupEnabled: false, DedupWindowSeconds: 1800,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }

	for _, id := range []string{"om_alarm_1", "om_alarm_2"} {
		if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{
			ChatID: "oc_alarm", MessageID: id, SenderID: "ou_alarm_bot", SenderType: "app",
			Text: "[service][warn]特征监控任务 lag\nRule ID: 2071",
		}); err != nil {
			t.Fatalf("route app alert %s: %v", id, err)
		}
	}
	if len(hub.routes) != 4 {
		t.Fatalf("routes = %+v, want two alerts when dedup is disabled", hub.routes)
	}
}

func TestGroupWatchDedupDoesNotSuppressUserMessages(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	setGroupWatchDedupTestCatalog(b)
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"user"},
		DedupEnabled: true, DedupWindowSeconds: 1800,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }

	for _, id := range []string{"om_user_1", "om_user_2"} {
		if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{
			ChatID: "oc_alarm", MessageID: id, SenderID: "ou_user", SenderType: "user", Text: "同一句话",
		}); err != nil {
			t.Fatalf("route user message %s: %v", id, err)
		}
	}
	if len(hub.routes) != 4 {
		t.Fatalf("routes = %+v, want two independent user sessions", hub.routes)
	}
}

func TestGroupWatchChatSenderTypesOverrideDefaultPerChat(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:     true,
		ChatIDs:     []string{"oc_alarm", "oc_qa", "oc_default"},
		SenderTypes: []string{"user"},
		ChatSenderTypes: map[string][]string{
			"oc_alarm": {"app"},
			"oc_qa":    {"user", "app"},
		},
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }

	inputs := []groupWatchMessage{
		{ChatID: "oc_alarm", MessageID: "om_alarm_app", SenderID: "ou_alarm_bot", SenderType: "app", Text: "报警 Bot"},
		{ChatID: "oc_alarm", MessageID: "om_alarm_user", SenderID: "ou_user", SenderType: "user", Text: "报警群用户"},
		{ChatID: "oc_qa", MessageID: "om_qa_user", SenderID: "ou_user", SenderType: "user", Text: "答疑群用户"},
		{ChatID: "oc_qa", MessageID: "om_qa_app", SenderID: "ou_qa_bot", SenderType: "app", Text: "答疑群 Bot"},
		{ChatID: "oc_default", MessageID: "om_default_user", SenderID: "ou_user", SenderType: "user", Text: "默认群用户"},
		{ChatID: "oc_default", MessageID: "om_default_app", SenderID: "ou_default_bot", SenderType: "app", Text: "默认群 Bot"},
	}
	for _, input := range inputs {
		if err := b.routeGroupWatchMessage(context.Background(), input); err != nil {
			t.Fatalf("route %s: %v", input.MessageID, err)
		}
	}
	if len(hub.routes) != 8 {
		t.Fatalf("routes = %+v, want four accepted messages", hub.routes)
	}
	var accepted []string
	for _, call := range hub.routes {
		if call.method == "Submit" {
			accepted = append(accepted, call.payload.(*pb.SubmitRequest).Text)
		}
	}
	got := strings.Join(accepted, "\n")
	for _, want := range []string{"报警 Bot", "答疑群用户", "答疑群 Bot", "默认群用户"} {
		if !strings.Contains(got, want) {
			t.Fatalf("accepted = %q, want %q", got, want)
		}
	}
	for _, notWant := range []string{"报警群用户", "默认群 Bot"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("accepted = %q, should not contain %q", got, notWant)
		}
	}
}

func TestGroupWatchAlertFingerprintUsesRuleIDAndTitle(t *testing.T) {
	first := groupWatchAlertFingerprint(groupWatchMessage{Text: "[service][warn]特征监控任务 lag\nRule ID: 2071\nlag: 100K"})
	second := groupWatchAlertFingerprint(groupWatchMessage{Text: "  [SERVICE][WARN]特征监控任务   lag  \n**规则ID**：2071\nlag: 200K"})
	different := groupWatchAlertFingerprint(groupWatchMessage{Text: "[service][warn]另一个报警\nRule ID: 2071\nlag: 200K"})
	if first == "" || first != second {
		t.Fatalf("fingerprints = %q/%q, want same non-empty fingerprint", first, second)
	}
	if first == different {
		t.Fatalf("different titles share fingerprint %q", first)
	}
}

func TestGroupWatchActiveSessionLimitExcludesSleepingSessions(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"},
		MaxActiveSessionsPerChat: 1, IdleTimeoutSeconds: 10800,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	b.threadNotice = func(context.Context, string, string) error { return nil }
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: "oc_alarm", RootMsgID: "old-root", ThreadID: "old-root",
		OpenId: "app-1", TargetID: "target-1", TargetName: "local", SessionName: "old-watch",
		EntrySource: ChatEntrySourceGroupWatch,
	}); err != nil {
		t.Fatalf("UpsertChat old watch: %v", err)
	}

	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "old-watch", State: "sleeping"}}}
	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{
		ChatID: "oc_alarm", MessageID: "new-root", RootID: "new-root",
		SenderType: "app", SenderID: "app-2", Text: "新告警",
	}); err != nil {
		t.Fatalf("routeGroupWatchMessage: %v", err)
	}
	if len(hub.routes) != 2 || hub.routes[0].method != "Run" || hub.routes[1].method != "Submit" {
		t.Fatalf("routes = %+v, sleeping session should free active capacity", hub.routes)
	}
	if got := hub.routes[0].payload.(*pb.RunRequest).IdleTimeoutSeconds; got != 10800 {
		t.Fatalf("new group watch idle timeout = %d, want 10800", got)
	}
}

func TestGroupWatchActiveSessionLimitBlocksNewRoot(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"},
		MaxActiveSessionsPerChat: 1, IdleTimeoutSeconds: 10800,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: "oc_alarm", RootMsgID: "old-root", ThreadID: "old-root",
		OpenId: "app-1", TargetID: "target-1", TargetName: "local", SessionName: "old-watch",
		EntrySource: ChatEntrySourceGroupWatch,
	}); err != nil {
		t.Fatalf("UpsertChat old watch: %v", err)
	}
	if err := b.db.DeactivateChat(b.currentBridgeId(), "old-root"); err != nil {
		t.Fatalf("DeactivateChat old watch: %v", err)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "old-watch", State: "running"}}}
	var notice string
	b.threadNotice = func(_ context.Context, _ string, text string) error {
		notice = text
		return nil
	}

	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{
		ChatID: "oc_alarm", MessageID: "blocked-root", RootID: "blocked-root",
		SenderType: "app", SenderID: "app-2", Text: "容量外告警",
	}); err != nil {
		t.Fatalf("routeGroupWatchMessage: %v", err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("routes = %+v, active group watch limit must block Run", hub.routes)
	}
	if !strings.Contains(notice, "活跃 Session 已达上限（1）") {
		t.Fatalf("notice = %q, want active capacity explanation", notice)
	}
}

func TestGroupWatchWorkspaceTotalQuotaAppliesInOwnerOnlyMode(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"},
		MaxActiveSessionsPerChat: 50, IdleTimeoutSeconds: 10800,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	if err := b.db.SetWorkspaceMode(b.currentBridgeId(), "oc_alarm", WorkspaceModeOwnerOnly); err != nil {
		t.Fatalf("SetWorkspaceMode: %v", err)
	}
	setWorkspaceLimits(t, b.db, "oc_alarm", 1, 0)
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: "oc_alarm", RootMsgID: "manual-root", ThreadID: "manual-root",
		OpenId: "owner-open", TargetID: "target-1", TargetName: "local", SessionName: "manual-session",
	}); err != nil {
		t.Fatalf("UpsertChat manual: %v", err)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "manual-session", State: "sleeping"}}}
	var notice string
	b.threadNotice = func(_ context.Context, _ string, text string) error {
		notice = text
		return nil
	}

	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{
		ChatID: "oc_alarm", MessageID: "quota-root", RootID: "quota-root",
		SenderType: "app", SenderID: "app-2", Text: "超过 workspace 总量",
	}); err != nil {
		t.Fatalf("routeGroupWatchMessage: %v", err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("routes = %+v, owner_only workspace total quota must block Run", hub.routes)
	}
	if !strings.Contains(notice, "workspace session 数量已达上限（1）") {
		t.Fatalf("notice = %q, want workspace total quota explanation", notice)
	}
}

func TestArchivedManualSessionReleasesGroupWatchWorkspaceTotalQuota(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"},
		MaxActiveSessionsPerChat: 50, IdleTimeoutSeconds: 10800,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	if err := b.db.SetWorkspaceMode(b.currentBridgeId(), "oc_alarm", WorkspaceModeOwnerOnly); err != nil {
		t.Fatalf("SetWorkspaceMode: %v", err)
	}
	setWorkspaceLimits(t, b.db, "oc_alarm", 1, 0)
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: b.currentBridgeId(), ChatID: "oc_alarm", RootMsgID: "archived-root", ThreadID: "archived-root",
		OpenId: "owner-open", TargetID: "target-1", TargetName: "local", SessionName: "archived-manual",
	}); err != nil {
		t.Fatalf("UpsertChat archived manual: %v", err)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "archived-manual", State: "archived"}}}
	b.threadNotice = func(context.Context, string, string) error { return nil }

	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{
		ChatID: "oc_alarm", MessageID: "replacement-root", RootID: "replacement-root",
		SenderType: "app", SenderID: "app-2", Text: "归档后的新告警",
	}); err != nil {
		t.Fatalf("routeGroupWatchMessage: %v", err)
	}
	if len(hub.routes) != 2 || hub.routes[0].method != "Run" || hub.routes[1].method != "Submit" {
		t.Fatalf("routes = %+v, archived manual session should release workspace quota", hub.routes)
	}
	old, err := b.db.FindByRootMsg(b.currentBridgeId(), "archived-root")
	if err != nil {
		t.Fatalf("FindByRootMsg archived: %v", err)
	}
	if old == nil || old.Status != ChatStatusTerminal {
		t.Fatalf("archived binding = %+v, want terminal", old)
	}
}

func TestGroupWatchNewRootRejectsModelWithoutExpert(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"app"},
		PollIntervalSeconds: 15, LookbackSeconds: 60, ModelRef: "GPT-5.5",
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	var notice string
	b.threadNotice = func(_ context.Context, _ string, text string) error {
		notice = text
		return nil
	}

	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{
		ChatID: "oc_alarm", MessageID: "om_invalid", RootID: "om_invalid",
		SenderType: "app", SenderID: "cli_alert", Text: "支付错误率升高",
	}); err != nil {
		t.Fatalf("routeGroupWatchMessage: %v", err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("routes = %+v, invalid execution config must not create a session", hub.routes)
	}
	if !strings.Contains(notice, "execution override requires an Agent Profile override") {
		t.Fatalf("notice = %q, want actionable invalid execution error", notice)
	}
}

func TestGroupWatchRouteIgnoresManagementCommand(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_alarm"}, SenderTypes: []string{"user", "app"},
		PollIntervalSeconds: 15, LookbackSeconds: 60,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{
		ChatID: "oc_alarm", MessageID: "om_command", SenderType: "user",
		SenderID: "owner-open", Text: "关闭群值守",
	}); err != nil {
		t.Fatalf("routeGroupWatchMessage: %v", err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("routes = %+v, management command must not reach Agent", hub.routes)
	}
}

func TestGroupWatchThreadFollowupUsesExistingOwnerOnlySingleBotRouting(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{
		AppId:     "cli_amux",
		OpenId:    "ou_owner",
		BotOpenId: "ou_amux",
		BotName:   "GroupWatch 助手",
	}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:     true,
		ChatIDs:     []string{"oc_alarm"},
		SenderTypes: []string{"app"},
	}
	b.chatBotCountLookup = func(context.Context, string) (int, error) { return 1, nil }
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	b.threadNotice = func(context.Context, string, string) error { return nil }

	if err := b.handleMessage(
		context.Background(),
		groupWatchBotEvent("oc_alarm", "om_owner_alarm", "ou_alarm_bot", "支付错误率升高"),
	); err != nil {
		t.Fatalf("handleMessage root alert: %v", err)
	}
	if len(hub.routes) != 2 {
		t.Fatalf("root routes = %+v, want Run then Submit", hub.routes)
	}
	runReq := hub.routes[0].payload.(*pb.RunRequest)
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: runReq.Name, State: "running"}}}
	b.setTurnActive("om_owner_alarm", false)

	reply := feishuThreadReplyEvent(
		"group",
		"oc_alarm",
		"om_owner_followup",
		"om_owner_alarm",
		"ou_owner",
		"我先看一下数据库",
	)
	if err := b.handleMessage(context.Background(), reply); err != nil {
		t.Fatalf("handleMessage unmentioned owner follow-up: %v", err)
	}
	if len(hub.routes) != 3 || hub.routes[2].method != "Submit" {
		t.Fatalf("routes after unmentioned owner follow-up = %+v, want existing routing to Submit", hub.routes)
	}
	if got := hub.routes[2].payload.(*pb.SubmitRequest).Text; got != "我先看一下数据库" {
		t.Fatalf("follow-up submit = %q, want original existing-route input", got)
	}
}

func TestGroupWatchAllowlistDoesNotChangeOrdinaryThreadRouting(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{
		AppId:     "cli_amux",
		OpenId:    "ou_owner",
		BotOpenId: "ou_amux",
		BotName:   "GroupWatch 助手",
	}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:     true,
		ChatIDs:     []string{"oc_alarm"},
		SenderTypes: []string{"user"},
	}
	b.chatBotCountLookup = func(context.Context, string) (int, error) { return 1, nil }
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), "oc_alarm", WorkspaceKindGroup); err != nil {
		t.Fatalf("EnsureWorkspaceKind: %v", err)
	}
	if err := b.db.UpsertChat(LarkChat{
		BridgeId:    b.currentBridgeId(),
		ChatID:      "oc_alarm",
		RootMsgID:   "om_manual_root",
		ThreadID:    "om_manual_root",
		OpenId:      "ou_owner",
		TargetID:    "target-1",
		TargetName:  "local",
		SessionName: "manual-session",
		AgentType:   "codex",
		Status:      ChatStatusActive,
	}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: "manual-session", State: "running"}}}
	b.threadNotice = func(context.Context, string, string) error { return nil }

	reply := feishuThreadReplyEvent(
		"group",
		"oc_alarm",
		"om_manual_followup",
		"om_manual_root",
		"ou_owner",
		"普通 thread 继续讨论",
	)
	if err := b.handleMessage(context.Background(), reply); err != nil {
		t.Fatalf("handleMessage ordinary thread follow-up: %v", err)
	}
	if len(hub.routes) != 1 || hub.routes[0].method != "Submit" {
		t.Fatalf("routes = %+v, want ordinary single-bot owner routing to remain one Submit", hub.routes)
	}
}

func TestGroupWatchEventSkipsCommandsAndUnconfiguredChats(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:     true,
		ChatIDs:     []string{"oc_alarm"},
		SenderTypes: []string{"user", "app"},
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	b.threadNotice = func(context.Context, string, string) error { return nil }

	if err := b.handleMessage(context.Background(), feishuMessageEvent("group", "oc_alarm", "om_help", "ou_reporter", "/help")); err != nil {
		t.Fatalf("handleMessage command: %v", err)
	}
	if err := b.handleMessage(context.Background(), feishuMessageEvent("group", "oc_other", "om_other", "ou_reporter", "普通消息")); err != nil {
		t.Fatalf("handleMessage other chat: %v", err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("routes = %+v, want no automatic task", hub.routes)
	}
}

func TestGroupWatchAppEventSkipsSelfAndDeduplicatesOtherBot(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux", BotName: "GroupWatch 助手"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:     true,
		ChatIDs:     []string{"oc_alarm"},
		SenderTypes: []string{"app"},
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	b.threadNotice = func(context.Context, string, string) error { return nil }

	self := groupWatchBotEvent("oc_alarm", "om_self", "ou_amux", "amux 自己的回复")
	if err := b.handleMessage(context.Background(), self); err != nil {
		t.Fatalf("handleMessage self: %v", err)
	}
	other := groupWatchBotEvent("oc_alarm", "om_bot_alarm", "ou_alarm_bot", "P0 数据库连接耗尽")
	if err := b.handleMessage(context.Background(), other); err != nil {
		t.Fatalf("handleMessage other bot: %v", err)
	}
	if err := b.handleMessage(context.Background(), other); err != nil {
		t.Fatalf("handleMessage duplicate other bot: %v", err)
	}
	appReply := groupWatchBotEvent("oc_alarm", "om_bot_reply", "ou_other_assistant", "自动回复，不应再次触发")
	appReply.Event.Message.RootId = testStringPtr("om_bot_alarm")
	if err := b.handleMessage(context.Background(), appReply); err != nil {
		t.Fatalf("handleMessage app reply: %v", err)
	}
	parentOnlyAppReply := groupWatchBotEvent("oc_alarm", "om_bot_parent_reply", "ou_other_assistant", "parent_id 回复，也不应触发")
	parentOnlyAppReply.Event.Message.ParentId = testStringPtr("om_bot_alarm")
	if err := b.handleMessage(context.Background(), parentOnlyAppReply); err != nil {
		t.Fatalf("handleMessage parent-only app reply: %v", err)
	}

	if len(hub.routes) != 2 {
		t.Fatalf("routes = %+v, want one Run and one Submit for other bot only", hub.routes)
	}
}

func TestGroupWatchPollProcessesBotCardAtMostOnceWhenSubmissionResultIsUnknown(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux", BotName: "GroupWatch 助手"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:             true,
		ChatIDs:             []string{"oc_alarm"},
		SenderTypes:         []string{"app"},
		PollIntervalSeconds: 15,
		LookbackSeconds:     60,
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	b.groupWatchListMessages = func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error) {
		return []*larkim.Message{{
			MessageId:  testStringPtr("om_card_alarm"),
			ChatId:     testStringPtr("oc_alarm"),
			MsgType:    testStringPtr("interactive"),
			CreateTime: testStringPtr(strconv.FormatInt(time.Now().Add(-time.Second).UnixMilli(), 10)),
			Sender: &larkim.Sender{
				Id:         testStringPtr("cli_alarm_bot"),
				IdType:     testStringPtr("app_id"),
				SenderType: testStringPtr("app"),
			},
			Body: &larkim.MessageBody{Content: testStringPtr(`{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"P0 告警"}},"body":{"elements":[{"tag":"markdown","content":"订单错误率 **35%**"}]}}`)},
		}}, nil
	}
	hub.routeErrs = map[string]error{"Submit": errors.New("runtime unavailable")}
	if err := b.pollGroupWatchOnce(context.Background(), time.Now()); err == nil {
		t.Fatal("pollGroupWatchOnce error = nil, want failed submit")
	}
	hub.routeErrs = nil
	if err := b.pollGroupWatchOnce(context.Background(), time.Now()); err != nil {
		t.Fatalf("pollGroupWatchOnce duplicate window: %v", err)
	}

	var submits []*pb.SubmitRequest
	for _, call := range hub.routes {
		if call.method == "Submit" {
			submits = append(submits, call.payload.(*pb.SubmitRequest))
		}
	}
	if len(submits) != 1 {
		t.Fatalf("submit attempts = %d, want one attempt despite an unknown result", len(submits))
	}
	if !strings.Contains(submits[0].Text, "P0 告警") || !strings.Contains(submits[0].Text, "订单错误率 **35%**") {
		t.Fatalf("submit text = %q, want extracted card text", submits[0].Text)
	}
}

func TestGroupWatchHistorySkipsCurrentBotMentionAndAppParentReply(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", BotOpenId: "ou_amux", BotName: "GroupWatch 助手"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:     true,
		ChatIDs:     []string{"oc_alarm"},
		SenderTypes: []string{"user", "app"},
	}
	now := time.Now()
	base := &larkim.Message{
		MessageId:  testStringPtr("om_mention"),
		ChatId:     testStringPtr("oc_alarm"),
		MsgType:    testStringPtr("text"),
		CreateTime: testStringPtr(strconv.FormatInt(now.UnixMilli(), 10)),
		Sender: &larkim.Sender{
			Id:         testStringPtr("ou_reporter"),
			SenderType: testStringPtr("user"),
		},
		Body:     &larkim.MessageBody{Content: testStringPtr(`{"text":"@GroupWatch 助手 请处理"}`)},
		Mentions: []*larkim.Mention{{Id: testStringPtr("ou_amux"), Name: testStringPtr("GroupWatch 助手")}},
	}
	if _, ok := b.groupWatchMessageFromHistory(base, now.Add(-time.Minute), now.Add(time.Minute)); ok {
		t.Fatal("history message explicitly mentioning current bot should use normal routing")
	}

	appReply := *base
	appReply.MessageId = testStringPtr("om_app_parent_reply")
	appReply.ParentId = testStringPtr("om_alarm")
	appReply.Mentions = nil
	appReply.Sender = &larkim.Sender{Id: testStringPtr("cli_other_bot"), SenderType: testStringPtr("app")}
	if _, ok := b.groupWatchMessageFromHistory(&appReply, now.Add(-time.Minute), now.Add(time.Minute)); ok {
		t.Fatal("history app reply with parent_id should not trigger groupWatch routing")
	}

	userReply := appReply
	userReply.MessageId = testStringPtr("om_user_parent_reply")
	userReply.Sender = &larkim.Sender{Id: testStringPtr("ou_groupWatch"), SenderType: testStringPtr("user")}
	if _, ok := b.groupWatchMessageFromHistory(&userReply, now.Add(-time.Minute), now.Add(time.Minute)); ok {
		t.Fatal("history user reply without current bot mention should not trigger groupWatch routing")
	}
}

func TestGroupWatchSkipsAppRootDirectedAtAnotherBot(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{BotOpenId: "ou-watcher", BotName: "watcher"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"oc-shared"}, SenderTypes: []string{"app"}}
	event := feishuMessageEvent("group", "oc-shared", "om-delegation", "ou-sender", "@_target review")
	event.Event.Sender.SenderType = testStringPtr("app")
	addStructuredBotMention(event, "@_target", "target", "ou-target")
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("routes = %+v, want directed app message ignored by group watch", hub.routes)
	}
}

func TestGroupWatchPollerWakesAndUsesUpdatedConfig(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled:             true,
		ChatIDs:             []string{"oc_alert"},
		SenderTypes:         []string{"user", "app"},
		PollIntervalSeconds: 300,
		LookbackSeconds:     300,
	}
	calls := make(chan string, 2)
	b.groupWatchListMessages = func(_ context.Context, chatID string, _, _ time.Time) ([]*larkim.Message, error) {
		calls <- chatID
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.runGroupWatchPoller(ctx)

	select {
	case got := <-calls:
		if got != "oc_alert" {
			t.Fatalf("initial poll chat = %q, want oc_alert", got)
		}
	case <-time.After(time.Second):
		t.Fatal("initial group watch poll did not run")
	}
	next := b.groupWatchSnapshot()
	next.PollIntervalSeconds = 15
	b.setGroupWatchConfig(next)
	select {
	case got := <-calls:
		if got != "oc_alert" {
			t.Fatalf("updated poll chat = %q, want oc_alert", got)
		}
	case <-time.After(time.Second):
		t.Fatal("group watch config update did not wake poller")
	}
}

func groupWatchBotEvent(chatID, messageID, botOpenID, text string) *larkim.P2MessageReceiveV1 {
	event := feishuMessageEvent("group", chatID, messageID, botOpenID, text)
	event.Event.Sender.SenderType = testStringPtr("bot")
	return event
}
