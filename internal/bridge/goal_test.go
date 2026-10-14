package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type goalTestHub struct {
	*bridgeTestHub
	supported bool
	goal      *agentstream.GoalState
	actions   []string
}

func (h *goalTestHub) Route(ctx context.Context, target, method string, payload interface{}) (*pb.TunnelMsg, error) {
	if method == "Run" {
		response, err := h.bridgeTestHub.Route(ctx, target, method, payload)
		if err != nil {
			return nil, err
		}
		var result pb.RunResponse
		if err := json.Unmarshal(response.Payload, &result); err != nil {
			return nil, err
		}
		result.Session.Id = "session-fresh"
		result.Session.GoalSupported = h.supported
		h.listResp.Sessions = append(h.listResp.Sessions, result.Session)
		response.Payload, _ = json.Marshal(&result)
		return response, nil
	}
	if method != "Goal" {
		return h.bridgeTestHub.Route(ctx, target, method, payload)
	}
	req := payload.(*pb.GoalRequest)
	h.actions = append(h.actions, req.Action)
	if h.supported {
		switch req.Action {
		case "create", "set":
			h.goal = &agentstream.GoalState{ThreadID: "native-1", Objective: req.Objective, Status: "active", CreatedAt: 1}
		case "pause":
			if h.goal != nil {
				h.goal.Status = "paused"
			}
		case "resume":
			if h.goal != nil {
				h.goal.Status = "active"
			}
		case "clear":
			h.goal = nil
		case "budget":
			if h.goal != nil {
				h.goal.TokenBudget = req.TokenBudget
			}
		}
	}
	goalJSON, _ := json.Marshal(h.goal)
	raw, _ := json.Marshal(&pb.GoalResponse{Supported: h.supported, GoalJson: goalJSON})
	return &pb.TunnelMsg{Payload: raw}, nil
}

func goalBridgeFixture(t *testing.T) (*Bridge, *goalTestHub, *LarkChat, *[]string) {
	t.Helper()
	b, base := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	base.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Id: "session-1", Name: "s1", State: "running", AgentType: "codex", GoalSupported: true}}}
	h := &goalTestHub{bridgeTestHub: base, supported: true}
	b.hub = h
	b.sessionStarter = sessionlaunch.New(h)
	base.targets = []*pb.Target{{Id: "t1", Name: "local"}}
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root-1", ChatID: "group-1", OpenId: "owner", TargetID: "t1", SessionName: "s1", AgentType: "codex", Status: ChatStatusActive}
	if err := b.db.UpsertChat(*chat); err != nil {
		t.Fatal(err)
	}
	cards := []string{}
	b.threadNotice = func(_ context.Context, _ string, text string) error { cards = append(cards, text); return nil }
	return b, h, chat, &cards
}

func TestGoalBoundTopicControlsUseRPCAndPinOutput(t *testing.T) {
	b, h, chat, cards := goalBridgeFixture(t)
	ctx := context.Background()
	if err := b.routeToSessionInChatWithMention(ctx, chat.ChatID, chat.RootMsgID, "msg-1", "owner", "goal finish this task", botMentionInfo{Mentioned: true}); err != nil {
		t.Fatal(err)
	}
	if h.goal == nil || h.goal.Objective != "finish this task" {
		t.Fatalf("goal=%+v actions=%v", h.goal, h.actions)
	}
	for _, call := range h.routes {
		if call.method == "Submit" {
			t.Fatal("goal became model input")
		}
	}
	origin, err := b.db.goalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName)
	if err != nil || origin == nil || origin.SessionID != "session-1" || origin.Generation == "" {
		t.Fatalf("origin=%+v err=%v", origin, err)
	}
	event := agentstream.Event{SessionID: "session-1", Origin: agentstream.Origin{Channel: "goal", NativeType: "goal_continuation"}, Body: agentstream.TurnStarted{InputSummary: h.goal.Objective}}
	pinned, err := b.goalChatForTurn(chat, event)
	if err != nil || pinned == nil || pinned.RootMsgID != "root-1" {
		t.Fatalf("pinned=%+v err=%v", pinned, err)
	}
	event.SessionID = "replacement-session"
	if pinned, _ := b.goalChatForTurn(chat, event); pinned != nil {
		t.Fatal("replacement session inherited output grant")
	}
	for _, text := range []string{"goal", "goal pause", "goal budget 9000", "goal resume", "goal clear"} {
		if err := b.handleGoalCommand(ctx, chat, "control", "owner", text); err != nil {
			t.Fatal(err)
		}
	}
	if h.goal != nil {
		t.Fatal("clear left native goal")
	}
	if origin, _ := b.db.goalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName); origin != nil {
		t.Fatal("clear left output destination")
	}
	if len(*cards) < 6 {
		t.Fatal("missing state responses")
	}
}

func TestGoalSingletonQueryDoesNotMoveContinuation(t *testing.T) {
	b, h, _, _ := goalBridgeFixture(t)
	bs := BotSession{BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "t1", SessionName: "s1", AgentType: "codex", LastReplyKind: botSessionReplyDM, LastReplyOpenID: "owner", LastReplyChatID: "dm-original"}
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	origin := b.botSessionChat(&bs, botSessionInput{OpenID: "owner", ChatID: "dm-original", ReplyKind: botSessionReplyDM})
	if err := b.handleGoalCommand(context.Background(), origin, "create", "owner", "goal finish shared task"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.SetBotSessionLastReply(b.currentBridgeId(), botSessionReplyGroup, "group-later", "owner", "query"); err != nil {
		t.Fatal(err)
	}
	queried := b.botSessionChat(&bs, botSessionInput{OpenID: "owner", ChatID: "group-later", ReplyKind: botSessionReplyGroup})
	if err := b.handleGoalCommand(context.Background(), queried, "query", "owner", "goal"); err != nil {
		t.Fatal(err)
	}
	event := agentstream.Event{SessionID: "session-1", Origin: agentstream.Origin{Channel: "goal", NativeType: "goal_continuation"}, Body: agentstream.TurnStarted{InputSummary: h.goal.Objective}}
	pinned, err := b.goalChatForTurn(queried, event)
	if err != nil || pinned == nil {
		t.Fatalf("pinned=%+v %v", pinned, err)
	}
	target, direct, err := b.directReplyTargetForBinding(pinned)
	if err != nil || !direct || target.Kind != botSessionReplyDM || target.OpenID != "owner" || target.ChatID != "dm-original" {
		t.Fatalf("destination=%+v direct=%v err=%v", target, direct, err)
	}
}

func TestGoalSingletonCommandEntryUsesRPC(t *testing.T) {
	b, h, _, _ := goalBridgeFixture(t)
	bs := BotSession{BridgeId: b.currentBridgeId(), Enabled: true, TargetID: "t1", SessionName: "s1", AgentType: "codex", LastReplyKind: botSessionReplyDM, LastReplyOpenID: "owner", LastReplyChatID: "dm-original"}
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	in := botSessionInput{OpenID: "owner", ChatID: "dm-original", ReplyKind: botSessionReplyDM, MessageID: "goal-entry"}
	for _, text := range []string{"goal finish the shared task", "goal", "goal pause", "goal budget 9000", "goal resume", "goal clear"} {
		handled, err := b.handleBotSessionCommand(context.Background(), &bs, in, text)
		if err != nil || !handled {
			t.Fatalf("singleton command %q bypassed native control: handled=%v err=%v", text, handled, err)
		}
	}
	if len(h.actions) == 0 || h.goal != nil {
		t.Fatalf("native goal controls were not applied: actions=%v goal=%+v", h.actions, h.goal)
	}
	if handled, err := b.handleBotSessionCommand(context.Background(), &bs, in, "/goal finish the shared task"); err != nil || handled {
		t.Fatalf("slash goal unexpectedly became a native goal command: handled=%v err=%v", handled, err)
	}
}

func TestGoalUnsupportedAndPermissionFailClosed(t *testing.T) {
	b, h, chat, cards := goalBridgeFixture(t)
	h.supported = false
	if err := b.handleGoalCommand(context.Background(), chat, "m", "owner", "goal task"); err != nil {
		t.Fatal(err)
	}
	if h.goal != nil || len(h.actions) != 1 || h.actions[0] != "get" {
		t.Fatal("unsupported runtime mutated")
	}
	if !strings.Contains(strings.Join(*cards, ""), sessionMessage("goal.unsupported").For(b.managementLocale("owner"))) {
		t.Fatal("missing unsupported copy")
	}
	h.actions = nil
	h.supported = true
	if err := b.handleGoalCommand(context.Background(), chat, "m2", "member", "goal task"); err != nil {
		t.Fatal(err)
	}
	if len(h.actions) != 0 {
		t.Fatal("unauthorized operator reached native control")
	}
}

func TestGoalCardLanguagesAndStaleGeneration(t *testing.T) {
	b, h, chat, cards := goalBridgeFixture(t)
	if err := b.handleGoalCommand(context.Background(), chat, "m", "owner", "goal task"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"active", "paused", "blocked", "usageLimited", "budgetLimited", "complete"} {
		state := *h.goal
		state.Status = status
		cp := *chat
		cp.goalGeneration = "generation"
		cp.goalSessionID = "session-1"
		assertSessionCardLanguages(t, renderGoalCard(&cp, &state))
	}
	assertSessionCardLanguages(t, renderGoalCard(chat, nil))
	for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
		for _, key := range []string{"usage", "unsupported", "denied", "stale", "error.goal_missing", "error.goal_complete", "error.goal_budget_invalid", "error.goal_objective_invalid"} {
			if text := sessionMessage("goal." + key).For(locale); strings.Contains(text, "session.goal.") || strings.Contains(text, "%!") {
				t.Fatalf("invalid copy %q", text)
			}
		}
	}
	var values map[string]interface{}
	destination, _ := b.db.goalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName)
	cp := *chat
	cp.goalGeneration = destination.Generation
	cp.goalSessionID = destination.SessionID
	if err := json.Unmarshal(goalButton(&cp, h.goal, "pause").Value, &values); err != nil {
		t.Fatal(err)
	}
	if err := b.handleGoalCommand(context.Background(), chat, "m2", "owner", "goal set task"); err != nil {
		t.Fatal(err)
	}
	before := len(h.actions)
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "owner"}, Action: &callback.CallBackAction{Value: values}}}
	response, handled, err := b.handleGoalCardAction(context.Background(), event)
	if err != nil || !handled || response == nil || response.Toast.Type != "error" || len(h.actions) != before {
		t.Fatalf("stale button controlled replacement: %+v %v", response, err)
	}
	if len(*cards) == 0 {
		t.Fatal("no cards")
	}
	if strings.Contains(renderBoundTopicHelpCardWithGoal(false), "goal <objective>") {
		t.Fatal("unsupported goal advertised")
	}
	if !strings.Contains(assertSessionCardLanguages(t, renderBoundTopicHelpCardWithGoal(true)), "goal <objective>") {
		t.Fatal("supported goal hidden")
	}
}

func TestGoalLongObjectiveFitsCardAndUsesDigest(t *testing.T) {
	chat := &LarkChat{SessionName: "s1", goalGeneration: "generation", goalSessionID: "session"}
	for _, objective := range []string{strings.Repeat("目标", 2000), strings.Repeat("😀", 4000)} {
		goal := &agentstream.GoalState{ThreadID: "native", Objective: objective, Status: "active"}
		raw := renderGoalCard(chat, goal)
		if len(raw) >= maxFeishuCardBytes {
			t.Fatalf("valid objective overflows card: %d", len(raw))
		}
		var value map[string]string
		if err := json.Unmarshal(goalButton(chat, goal, "pause").Value, &value); err != nil {
			t.Fatal(err)
		}
		if value["objective_hash"] != goalObjectiveHash(objective) || value["objective"] != "" {
			t.Fatal("callback did not use compact identity")
		}
	}
}

func TestGoalPrivateEntryCreatesBoundTopicThroughRPC(t *testing.T) {
	b, h, _, cards := goalBridgeFixture(t)
	installBridgeTestProfilePack(t)
	setBridgeTestBotDefaultAgent(t, b, "team/coder")
	setCurrentBotRuntime(t, b, "codex")
	chatID, msgID := "private-goal", "private-message"
	message := &larkim.EventMessage{ChatId: &chatID, MessageId: &msgID}
	if err := b.handlePrivateMessage(context.Background(), message, "owner", "goal finish private task"); err != nil {
		t.Fatal(err)
	}
	if h.goal == nil || h.goal.Objective != "finish private task" {
		t.Fatalf("private goal not created: %+v actions=%v cards=%v routes=%v", h.goal, h.actions, *cards, h.routes)
	}
	for _, call := range h.routes {
		if call.method == "Submit" {
			t.Fatal("private goal became prompt text")
		}
	}
	bound, err := b.db.FindByRootMsg(b.currentBridgeId(), msgID)
	if err != nil || bound == nil {
		t.Fatal("private goal did not bind a topic")
	}
}

func TestGoalFreshTopicCreateAndControls(t *testing.T) {
	b, h, _, cards := goalBridgeFixture(t)
	installBridgeTestProfilePack(t)
	setBridgeTestBotDefaultAgent(t, b, "team/coder")
	setCurrentBotRuntime(t, b, "codex")
	ctx := context.Background()
	if err := b.runAndBindNaturalEntry(ctx, "group-fresh", "fresh-goal", "fresh-goal", "owner", "goal finish topic task", botMentionInfo{Mentioned: true}); err != nil {
		t.Fatal(err)
	}
	if h.goal == nil || h.goal.Objective != "finish topic task" {
		t.Fatalf("fresh goal not created: %+v actions=%v cards=%v routes=%v", h.goal, h.actions, *cards, h.routes)
	}
	before := len(h.routes)
	if err := b.runAndBindNaturalEntry(ctx, "group-fresh", "unbound-control", "unbound-control", "owner", "goal pause", botMentionInfo{Mentioned: true}); err != nil {
		t.Fatal(err)
	}
	if len(h.routes) != before {
		t.Fatal("unbound control launched or submitted a session")
	}
	if len(*cards) == 0 {
		t.Fatal("missing no-binding guidance")
	}
}
