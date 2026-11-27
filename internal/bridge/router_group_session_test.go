package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/spf13/viper"
)

func groupRouteTestBridge(t *testing.T) (*Bridge, *bridgeTestHub, *[]string) {
	t.Helper()
	enableGroupSettingsExperiment(t)
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	hub.targets = []*pb.Target{{Id: "local", Name: "local"}}
	var notices []string
	var mu sync.Mutex
	b.threadNotice = func(_ context.Context, _ string, text string) error {
		mu.Lock()
		notices = append(notices, text)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		b.coalesceMu.Lock()
		batches := make(map[string]*coalescedInput, len(b.coalesced))
		for root, batch := range b.coalesced {
			batches[root] = batch
		}
		b.coalesceMu.Unlock()
		for root, batch := range batches {
			b.flushCoalescedInput(root, batch)
		}
	})
	return b, hub, &notices
}

type groupLiveTestHub struct{ *bridgeTestHub }

func (h groupLiveTestHub) Route(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
	if method != "List" {
		return h.bridgeTestHub.Route(ctx, targetID, method, payload)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	response := &pb.ListResponse{}
	for _, call := range h.routes {
		if call.method == "Run" {
			request := call.payload.(*pb.RunRequest)
			response.Sessions = append(response.Sessions, &pb.Session{Name: request.Name, State: "running"})
		}
	}
	data, err := json.Marshal(response)
	return &pb.TunnelMsg{Payload: data}, err
}

func TestGroupSingletonConcurrentFirstInputsCreateOneSession(t *testing.T) {
	b, hub, _ := groupRouteTestBridge(t)
	live := groupLiveTestHub{hub}
	b.hub, b.sessionStarter = live, sessionlaunch.New(live)
	saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionSingleton)
	const count = 8
	start := make(chan struct{})
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		go func(i int) {
			<-start
			root := fmt.Sprintf("root-%d", i)
			errs <- b.routeToSessionInChatWithMention(context.Background(), "a", root, root, "owner", "task", botMentionInfo{Mentioned: true})
		}(i)
	}
	close(start)
	for i := 0; i < count; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if runs := groupTestRuns(hub); len(runs) != 1 {
		t.Fatalf("concurrent first inputs launched %d sessions", len(runs))
	}
	root, err := b.db.GetGroupSessionRoot(b.currentBridgeId(), "a")
	if err != nil || root == "" {
		t.Fatalf("root=%q err=%v", root, err)
	}
	for i := 0; i < count; i++ {
		origin, err := b.db.FindGroupSessionOrigin(b.currentBridgeId(), "a", fmt.Sprintf("root-%d", i))
		if err != nil || origin != root {
			t.Fatalf("origin=%q root=%q err=%v", origin, root, err)
		}
	}
}

func TestGroupSingletonSettingsChangePreservesCurrentAndExistingTopic(t *testing.T) {
	b, hub, _ := groupRouteTestBridge(t)
	live := groupLiveTestHub{hub}
	b.hub, b.sessionStarter = live, sessionlaunch.New(live)
	settings := saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionThread)
	routeGroupTestInput(t, b, "a", "topic", "topic")
	settings.InteractionMode = config.FeishuInteractionSingleton
	settings, err := b.applyGroupSettings(context.Background(), settings, settings.Version, "owner")
	if err != nil {
		t.Fatal(err)
	}
	routeGroupTestInput(t, b, "a", "anchor", "anchor")
	settings.DefaultModelRef = "new-model"
	if _, err := b.applyGroupSettings(context.Background(), settings, settings.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	b.setTurnActive("topic", false)
	b.setTurnActive("anchor", false)
	routeGroupTestInput(t, b, "a", "other", "other")
	routeGroupTestInput(t, b, "a", "topic", "old-topic-reply")
	if runs := groupTestRuns(hub); len(runs) != 2 {
		t.Fatalf("default update replaced existing sessions: %d", len(runs))
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var names []string
	for _, call := range hub.routes {
		if call.method == "Submit" {
			names = append(names, call.payload.(*pb.SubmitRequest).Name)
		}
	}
	if len(names) != 4 || names[0] != names[3] || names[1] != names[2] || names[0] == names[1] {
		t.Fatalf("topic/singleton routes = %v", names)
	}
}

func saveGroupRouteTestSettings(t *testing.T, b *Bridge, chatID, mode string) *GroupSettings {
	t.Helper()
	draft, err := b.groupSettingsDraft(chatID)
	if err != nil {
		t.Fatal(err)
	}
	draft.InteractionMode, draft.DefaultAgent = mode, "builtin/codex"
	draft.DefaultRuntimeRef, draft.DefaultModelRef, draft.DefaultReasoningEffort = "codex", "group-model", "high"
	draft.Mode = WorkspaceModeShared
	draft.DefaultCwd, draft.IdleTimeoutSeconds = "/group/"+chatID, 180
	saved, err := b.applyGroupSettings(context.Background(), draft, draft.Version, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func routeGroupTestInput(t *testing.T, b *Bridge, chatID, rootID, messageID string) {
	t.Helper()
	if err := b.routeToSessionInChatWithMention(context.Background(), chatID, rootID, messageID, "owner", "do this task", botMentionInfo{Mentioned: true}); err != nil {
		t.Fatal(err)
	}
}

func groupTestRuns(hub *bridgeTestHub) []*pb.RunRequest {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var runs []*pb.RunRequest
	for _, call := range hub.routes {
		if call.method == "Run" {
			runs = append(runs, call.payload.(*pb.RunRequest))
		}
	}
	return runs
}

func TestGroupTopicLaunchUsesFullExecutionAndWorkspaceSettings(t *testing.T) {
	b, hub, notices := groupRouteTestBridge(t)
	saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionThread)
	routeGroupTestInput(t, b, "a", "root-a", "root-a")
	runs := groupTestRuns(hub)
	if len(runs) != 1 {
		t.Fatalf("runs=%d notices=%v", len(runs), *notices)
	}
	run := runs[0]
	if run.AgentProfile != "builtin/codex" || run.AgentType != "codex" || run.ProfileSpec.GetRuntimeRef() != "codex" || run.ProfileSpec.GetModel() != "group-model" || run.ProfileSpec.GetReasoningEffort() != "high" || run.Cwd != "/group/a" || run.IdleTimeoutSeconds != 180 {
		t.Fatalf("wrong group launch: %+v", run)
	}
	if err := b.runAndBindThread(context.Background(), "a", "explicit", "owner", "/run builtin/codex -n explicit --model explicit-model"); err != nil {
		t.Fatal(err)
	}
	runs = groupTestRuns(hub)
	if len(runs) != 2 || runs[1].ProfileSpec.GetModel() != "explicit-model" || runs[1].ProfileSpec.GetReasoningEffort() != "high" || runs[1].Cwd != "/group/a" {
		t.Fatalf("explicit launch ignored group defaults/override: %+v notices=%v", runs, *notices)
	}
}

func TestGroupSingletonReusesOnlyWithinGroupAndRetiresOldOrigins(t *testing.T) {
	b, hub, notices := groupRouteTestBridge(t)
	settings := saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionSingleton)
	saveGroupRouteTestSettings(t, b, "b", config.FeishuInteractionSingleton)
	var sharedLaunches int
	b.SetExecutionIdentityResolver(func(_, _, _ string, shared bool) (LaunchIdentity, error) {
		if shared {
			sharedLaunches++
		}
		return LaunchIdentity{}, nil
	})
	routeGroupTestInput(t, b, "a", "a1", "a1")
	first, err := b.db.FindByRootMsg(b.currentBridgeId(), "a1")
	if err != nil || first == nil {
		t.Fatalf("binding=%+v err=%v notices=%v", first, err, *notices)
	}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Name: first.SessionName, State: "running"}}}
	routeGroupTestInput(t, b, "a", "a2", "a2")
	routeGroupTestInput(t, b, "b", "b1", "b1")
	if runs := groupTestRuns(hub); len(runs) != 2 || runs[0].Name == runs[1].Name || sharedLaunches != 2 {
		t.Fatalf("runs=%v shared=%d notices=%v", runs, sharedLaunches, *notices)
	}
	if extra, _ := b.db.FindByRootMsg(b.currentBridgeId(), "a2"); extra != nil {
		t.Fatalf("second origin acquired its own topic binding: %+v", extra)
	}
	if err := b.newGroupConversation("a", settings.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	routeGroupTestInput(t, b, "a", "a2", "old-reply")
	if !strings.Contains(strings.Join(*notices, "\n"), "已结束接收新消息") {
		t.Fatalf("no retired-origin notice: %v", *notices)
	}
	routeGroupTestInput(t, b, "a", "a3", "a3")
	if runs := groupTestRuns(hub); len(runs) != 3 {
		t.Fatalf("new conversation did not create one session: %d, notices=%v", len(runs), *notices)
	}
}

func TestGroupIsolationRejectsMissingIdentityEvenWhenBotIsNotIsolated(t *testing.T) {
	b, hub, notices := groupRouteTestBridge(t)
	b.SetIsolationSupportChecker(func() (bool, string) { return true, "" })
	settings := saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionThread)
	settings.IsolationRequired = true
	if _, err := b.applyGroupSettings(context.Background(), settings, settings.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	routeGroupTestInput(t, b, "a", "isolated-root", "isolated-root")
	if len(groupTestRuns(hub)) != 0 {
		t.Fatal("isolation requirement fell back to anonymous launch")
	}
	if !strings.Contains(strings.Join(*notices, "\n"), "无法以隔离用户身份") {
		t.Fatalf("notices=%v", *notices)
	}
}

func TestGroupIsolationRejectsPassthroughBinding(t *testing.T) {
	b, hub, _ := groupRouteTestBridge(t)
	b.SetIsolationSupportChecker(func() (bool, string) { return true, "" })
	settings := saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionThread)
	settings.IsolationRequired = true
	if _, err := b.applyGroupSettings(context.Background(), settings, settings.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	hub.targets[0].Sessions = []*pb.Session{{Name: "existing", ExecutionIdentity: &pb.ExecutionIdentity{IdentityMode: "identified", EffectivePrincipalId: "principal-1", IsolationBackend: "passthrough"}}}
	err := b.checkGroupBindingIsolation(context.Background(), "a", resolvedBridgeSession{TargetID: "local", SessionName: "existing"})
	if err == nil {
		t.Fatal("required-isolation group accepted passthrough binding")
	}
}

func TestGroupWatchSkipsRetiredSingletonOriginAfterThreadTransition(t *testing.T) {
	b, hub, notices := groupRouteTestBridge(t)
	settings := saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionSingleton)
	routeGroupTestInput(t, b, "a", "anchor", "anchor")
	b.setTurnActive("anchor", false)
	routeGroupTestInput(t, b, "a", "later-root", "later-root")
	beforeRoutes := len(hub.routes)
	settings.InteractionMode = config.FeishuInteractionThread
	if _, err := b.applyGroupSettings(context.Background(), settings, settings.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	b.setGroupWatchConfig(config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"a"}, SenderTypes: []string{"user"}})
	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{ChatID: "a", RootID: "later-root", MessageID: "watch-replay", SenderID: "member", SenderType: "user", Text: "replayed root"}); err != nil {
		t.Fatal(err)
	}
	if len(hub.routes) != beforeRoutes {
		t.Fatalf("retired origin was submitted by watch: before=%d after=%d", beforeRoutes, len(hub.routes))
	}
	if strings.Contains(strings.Join(*notices, "\n"), "已结束接收新消息") {
		t.Fatalf("automated replay emitted user notice: %v", *notices)
	}
}

func TestGroupSingletonRootSlashCommandReachesCurrentSession(t *testing.T) {
	b, hub, _ := groupRouteTestBridge(t)
	live := groupLiveTestHub{hub}
	b.hub, b.sessionStarter = live, sessionlaunch.New(live)
	b.secrets.BotOpenId = "ou_bot"
	saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionSingleton)
	routeGroupTestInput(t, b, "a", "anchor", "anchor")
	event := feishuMessageEvent("group", "a", "control", "owner", "@_bot /interrupt")
	key, kind, id := "@_bot", "bot", "ou_bot"
	event.Event.Message.Mentions = []*larkim.MentionEvent{{Key: &key, MentionedType: &kind, Id: &larkim.UserId{OpenId: &id}}}
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for _, call := range hub.routes {
		if call.method == "Interrupt" {
			return
		}
	}
	t.Fatalf("group root control did not reach Session: %+v", hub.routes)
}

func TestGroupSingletonReattachesAfterLongIdle(t *testing.T) {
	b, hub, chat := newStreamLifecycleBridge(t)
	enableGroupSettingsExperiment(t)
	b.secrets = &Secrets{OpenId: "owner"}
	settings, err := b.groupSettingsDraft(chat.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	settings.InteractionMode = config.FeishuInteractionSingleton
	saved, err := b.applyGroupSettings(context.Background(), settings, 0, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.SetGroupSessionRoot(b.currentBridgeId(), chat.ChatID, saved.Version, chat.RootMsgID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.db.Exec("UPDATE lark_chats SET last_activity_at=?", time.Now().Add(-30*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	session := hub.setSession(chat, "session-id-1", "launch-1")
	b.OnSessionAdded(chat.TargetID, "target", session)
	attachment := waitLifecycleSignal(t, hub.started)
	if attachment == nil {
		t.Fatal("no restored group attachment")
	}
}

func TestGroupThreadWatchWorksWithSingletonBotDefault(t *testing.T) {
	b, hub, _ := groupRouteTestBridge(t)
	saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionThread)
	state, err := b.currentBotProfileState()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.CommitSingletonBotSettings(b.currentBridgeId(), state.Version, "builtin/codex", BotSession{BridgeId: b.currentBridgeId(), TargetID: "local", SessionName: "bot-global", AgentProfile: "builtin/codex"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"a"}, SenderTypes: []string{"user"}}
	b.setGroupWatchConfig(cfg)
	if !b.groupWatchActive() || !b.feishuThreadCapabilitiesActive() {
		t.Fatal("independent group thread capability disabled by Bot default")
	}
	if err := b.routeGroupWatchMessage(context.Background(), groupWatchMessage{ChatID: "a", MessageID: "alert", SenderID: "member", SenderType: "user", Text: "process alert"}); err != nil {
		t.Fatal(err)
	}
	runs := groupTestRuns(hub)
	if len(runs) != 1 || runs[0].ProfileSpec.GetModel() != "group-model" || runs[0].ProfileSpec.GetReasoningEffort() != "high" {
		t.Fatalf("group watch ignored saved execution settings: %+v", runs)
	}
}

func TestGroupSingletonAliasRootParentFollowup(t *testing.T) {
	b, hub, notices := groupRouteTestBridge(t)
	live := groupLiveTestHub{hub}
	b.hub, b.sessionStarter = live, sessionlaunch.New(live)
	b.secrets.BotName, b.secrets.BotOpenId = "Helper", "ou-helper"
	saveGroupRouteTestSettings(t, b, "a", config.FeishuInteractionSingleton)
	routeGroupTestInput(t, b, "a", "anchor", "anchor")
	b.setTurnActive("anchor", false)
	routeGroupTestInput(t, b, "a", "alias", "alias")
	b.setTurnActive("anchor", false)
	quoteLookups := 0
	b.quotedMessageLookup = func(_ context.Context, messageID string) (feishuQuotedMessage, error) {
		quoteLookups++
		return feishuQuotedMessage{}, fmt.Errorf("root is no longer readable")
	}
	event := feishuThreadReplyEvent("group", "a", "follow-up", "alias", "owner", "@_helper continue")
	event.Event.Message.ParentId = testStringPtr("alias")
	addStructuredBotMention(event, "@_helper", "Helper", "ou-helper")
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var sourceRoot string
	if err := b.db.db.QueryRow("SELECT root_msg_id FROM feishu_seen_messages WHERE bridge_id=? AND message_id=?", b.currentBridgeId(), "follow-up").Scan(&sourceRoot); err != nil {
		t.Fatal(err)
	}
	if sourceRoot != "alias" {
		t.Fatalf("follow-up source root = %q, want alias", sourceRoot)
	}
	if quoteLookups != 0 {
		t.Fatalf("ordinary alias-topic follow-up made %d quote lookup(s); notices=%v", quoteLookups, *notices)
	}
}

func TestGroupSingletonRetiredAnchorReattachesAcceptedLongTurn(t *testing.T) {
	b, hub, chat := newStreamLifecycleBridge(t)
	enableGroupSettingsExperiment(t)
	b.secrets = &Secrets{OpenId: "owner"}
	settings, err := b.groupSettingsDraft(chat.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	settings.InteractionMode = config.FeishuInteractionSingleton
	saved, err := b.applyGroupSettings(context.Background(), settings, 0, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.SetGroupSessionRoot(b.currentBridgeId(), chat.ChatID, saved.Version, chat.RootMsgID); err != nil {
		t.Fatal(err)
	}
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: b.currentBridgeId(), RootMsgID: chat.RootMsgID, MessageID: "pending-request", OpenID: "owner", TargetID: chat.TargetID, SessionName: chat.SessionName, SessionID: "session-id-1", LaunchID: "launch-1", TurnID: "pending-turn", SubmittedAt: time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if err := b.newGroupConversation(chat.ChatID, saved.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.db.Exec("UPDATE lark_chats SET last_activity_at=?", time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	session := hub.setSession(chat, "session-id-1", "launch-1")
	viper.Set("bridge.feishu.group_settings.enabled", false)
	b.OnSessionAdded(chat.TargetID, "target", session)
	b.streamMu.Lock()
	_, attached := b.streams[chat.RootMsgID]
	b.streamMu.Unlock()
	if !attached {
		t.Fatal("retired singleton with accepted unfinished Turn not reattached after restart")
	}
}
