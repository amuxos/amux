package bridge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type goalDestination struct {
	Generation string             `json:"generation"`
	SessionID  string             `json:"session_id"`
	ThreadID   string             `json:"thread_id,omitempty"`
	Objective  string             `json:"objective"`
	Chat       LarkChat           `json:"chat"`
	Direct     *directReplyTarget `json:"direct,omitempty"`
}

func (b *bridgeDB) migrateGoals() error {
	_, err := b.db.Exec(`CREATE TABLE IF NOT EXISTS feishu_goal_destinations (
	 bridge_id TEXT NOT NULL, target_id TEXT NOT NULL, session_name TEXT NOT NULL,
	 destination_json TEXT NOT NULL, card_message_id TEXT NOT NULL DEFAULT '',
	 PRIMARY KEY(bridge_id,target_id,session_name))`)
	return err
}

func (b *bridgeDB) saveGoalDestination(bridgeID, targetID, sessionName string, destination *goalDestination) error {
	if destination == nil {
		_, err := b.db.Exec(`DELETE FROM feishu_goal_destinations WHERE bridge_id=? AND target_id=? AND session_name=?`, bridgeID, targetID, sessionName)
		return err
	}
	encoded, err := json.Marshal(destination)
	if err != nil {
		return err
	}
	_, err = b.db.Exec(`INSERT INTO feishu_goal_destinations(bridge_id,target_id,session_name,destination_json) VALUES(?,?,?,?) ON CONFLICT(bridge_id,target_id,session_name) DO UPDATE SET destination_json=excluded.destination_json,card_message_id=''`, bridgeID, targetID, sessionName, string(encoded))
	return err
}

func (b *bridgeDB) goalDestination(bridgeID, targetID, sessionName string) (*goalDestination, error) {
	var encoded string
	err := b.db.QueryRow(`SELECT destination_json FROM feishu_goal_destinations WHERE bridge_id=? AND target_id=? AND session_name=?`, bridgeID, targetID, sessionName).Scan(&encoded)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var destination goalDestination
	if err := json.Unmarshal([]byte(encoded), &destination); err != nil {
		return nil, err
	}
	return &destination, nil
}

func (b *Bridge) goalRPC(ctx context.Context, chat *LarkChat, request command.GoalCommand) (*agentstream.GoalState, bool, error) {
	response, err := b.hub.Route(ctx, chat.TargetID, "Goal", &pb.GoalRequest{Name: chat.SessionName, Action: request.Action, Objective: request.Objective, TokenBudget: request.TokenBudget, ExpectedSessionId: chat.goalSessionID})
	if err != nil {
		return nil, false, err
	}
	var result pb.GoalResponse
	if err := json.Unmarshal(response.Payload, &result); err != nil {
		return nil, false, err
	}
	var goal *agentstream.GoalState
	if len(result.GoalJson) > 0 {
		if err := json.Unmarshal(result.GoalJson, &goal); err != nil {
			return nil, result.Supported, err
		}
	}
	return goal, result.Supported, nil
}

func (b *Bridge) handleGoalCommand(ctx context.Context, chat *LarkChat, messageID, openID, text string) error {
	unlock := b.lockRoute("goal:" + b.currentBridgeId() + ":" + chat.TargetID + ":" + chat.SessionName)
	defer unlock()
	request, _, parseErr := command.ParseGoal(text)
	if parseErr != nil {
		return b.notifyGoalMessage(ctx, chat, openID, sessionMessage("goal.usage"))
	}
	if request.Action != "get" && !b.canControlBinding(openID, chat) {
		return b.notifyGoalMessage(ctx, chat, openID, sessionMessage("goal.denied"))
	}
	if (request.Action == "create" || request.Action == "set") && (hasFeishuMaterials(ctx) || feishuQuotedMessageFromContext(withoutBoundTopicRootQuote(ctx, chat.RootMsgID)).messageID != "") {
		return b.notifyGoalMessage(ctx, chat, openID, sessionMessage("goal.text_only"))
	}
	goalCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	current, supported, err := b.goalRPC(goalCtx, chat, command.GoalCommand{Action: "get"})
	if err != nil {
		return b.notifyGoalMessage(ctx, chat, openID, goalFailureMessage(err))
	}
	if !supported {
		return b.notifyGoalMessage(ctx, chat, openID, sessionMessage("goal.unsupported"))
	}
	var previous *goalDestination
	needsDestination := request.Action == "create" || request.Action == "set"
	if request.Action == "resume" && current != nil {
		destination, loadErr := b.db.goalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName)
		if loadErr != nil {
			return loadErr
		}
		needsDestination = destination == nil || destination.Objective != current.Objective
	}
	if needsDestination {
		if request.Action == "create" && current != nil && !current.IsTerminal() {
			return b.notifyGoalMessage(ctx, chat, openID, sessionMessage("goal.exists"))
		}
		previous, err = b.db.goalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName)
		if err != nil {
			return err
		}
		info, infoErr := b.hub.Route(goalCtx, chat.TargetID, "List", &pb.ListRequest{})
		if infoErr != nil {
			return b.notifyGoalMessage(ctx, chat, openID, goalFailureMessage(infoErr))
		}
		var sessions pb.ListResponse
		if err := json.Unmarshal(info.Payload, &sessions); err != nil {
			return err
		}
		sessionID := ""
		for _, sess := range sessions.Sessions {
			if sess.Name == chat.SessionName {
				sessionID = sess.Id
				break
			}
		}
		if sessionID == "" {
			return b.notifyGoalMessage(ctx, chat, openID, sessionMessage("goal.stale"))
		}
		copy := *chat
		objective := request.Objective
		if request.Action == "resume" {
			objective = current.Objective
		}
		destination := &goalDestination{Generation: uuid.NewString(), SessionID: sessionID, Objective: objective, Chat: copy}
		if target, direct, targetErr := b.directReplyTargetForBinding(chat); targetErr != nil {
			return targetErr
		} else if direct {
			destination.Direct = target
		}
		if err := b.db.saveGoalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName, destination); err != nil {
			return err
		}
		copy.goalSessionID = sessionID
		chat = &copy
	}
	goal := current
	if request.Action == "create" || request.Action == "set" || request.Action == "resume" {
		b.waitForStreamReady(goalCtx, chat.RootMsgID)
	}
	if request.Action != "get" {
		goal, supported, err = b.goalRPC(goalCtx, chat, request)
		if err != nil {
			if needsDestination {
				_ = b.db.saveGoalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName, previous)
			}
			return b.notifyGoalMessage(ctx, chat, openID, goalFailureMessage(err))
		}
		if !supported {
			if needsDestination {
				_ = b.db.saveGoalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName, previous)
			}
			return b.notifyGoalMessage(ctx, chat, openID, sessionMessage("goal.unsupported"))
		}
	}
	if request.Action == "clear" {
		if err := b.db.saveGoalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName, nil); err != nil {
			return err
		}
	}
	cardChat := *chat
	if destination, err := b.db.goalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName); err != nil {
		return err
	} else if destination != nil {
		cardChat.goalGeneration, cardChat.goalSessionID = destination.Generation, destination.SessionID
	}
	return b.sendGoalCard(ctx, chat, renderGoalCard(&cardChat, goal))
}

func goalFailureMessage(err error) i18n.Text {
	if strings.Contains(err.Error(), "goal_stale") {
		return sessionMessage("goal.stale")
	}
	for _, code := range []string{"goal_exists", "goal_missing", "goal_complete", "goal_objective_invalid", "goal_budget_invalid"} {
		if strings.Contains(err.Error(), code) {
			return sessionMessage("goal.error." + code)
		}
	}
	return sessionMessage("goal.failed", err.Error())
}

func (b *Bridge) notifyGoalMessage(ctx context.Context, chat *LarkChat, openID string, message i18n.Text) error {
	text := message.For(b.managementLocale(openID))
	if target, direct, err := b.directReplyTargetForBinding(chat); direct || err != nil {
		if err != nil {
			return err
		}
		if b.chatNotice != nil {
			_, _, err = b.chatNotice(ctx, target.ChatID, text)
			return err
		}
		_, _, err = b.sendTextToDirectTarget(ctx, target, text)
		return err
	}
	return b.notifyThread(ctx, chat.RootMsgID, text)
}

func (b *Bridge) sendGoalCard(ctx context.Context, chat *LarkChat, card string) error {
	if b.threadNotice != nil {
		return b.threadNotice(ctx, chat.RootMsgID, card)
	}
	var messageID string
	var err error
	if target, direct, targetErr := b.directReplyTargetForBinding(chat); direct || targetErr != nil {
		if targetErr != nil {
			return targetErr
		}
		messageID, _, err = b.sendInteractiveToDirectTarget(ctx, target, card, "goal state card")
	} else {
		messageID, _, err = b.sendInteractiveToThread(ctx, chat.RootMsgID, card, "goal state card")
	}
	if err == nil && messageID != "" {
		destination, loadErr := b.db.goalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName)
		if loadErr != nil {
			return loadErr
		}
		if destination != nil && destination.Chat.RootMsgID == chat.RootMsgID && destination.Chat.ChatID == chat.ChatID && destination.Chat.OpenId == chat.OpenId {
			_, err = b.db.db.Exec(`UPDATE feishu_goal_destinations SET card_message_id=? WHERE bridge_id=? AND target_id=? AND session_name=?`, messageID, b.currentBridgeId(), chat.TargetID, chat.SessionName)
		}
	}
	return err
}

// Native source alone is insufficient for Feishu visibility. Match the
// durable canonical Session and explicit objective to its frozen destination.
func (b *Bridge) goalChatForTurn(base *LarkChat, event agentstream.Event) (*LarkChat, error) {
	started, ok := event.Body.(agentstream.TurnStarted)
	if !ok || event.Origin.Channel != "goal" || event.Origin.NativeType != "goal_continuation" {
		return nil, nil
	}
	destination, err := b.db.goalDestination(b.currentBridgeId(), base.TargetID, base.SessionName)
	if err != nil || destination == nil {
		return nil, err
	}
	if destination.SessionID != event.SessionID || destination.Objective != started.InputSummary {
		return nil, nil
	}
	allowed, err := b.canUseBotInGroup(destination.Chat.ChatID, destination.Chat.OpenId)
	if err != nil || !allowed {
		return nil, err
	}
	if strings.HasPrefix(base.RootMsgID, "singleton:") {
		if _, matched, err := b.botSessionForBinding(base); err != nil || !matched {
			return nil, err
		}
	} else {
		binding, err := b.db.FindByRootMsg(b.currentBridgeId(), destination.Chat.RootMsgID)
		if err != nil || binding == nil || binding.Status != ChatStatusActive || binding.TargetID != base.TargetID || binding.SessionName != base.SessionName {
			return nil, err
		}
	}
	copy := destination.Chat
	copy.EffectiveModel = base.EffectiveModel
	copy.goalReplyTarget = destination.Direct
	if copy.goalReplyTarget != nil {
		copy.goalReplyTarget.bridgeID = b.currentBridgeId()
		if copy.goalReplyTarget.Kind == botSessionReplyGroup && !strings.HasPrefix(copy.RootMsgID, "singleton:") {
			copy.goalReplyTarget.groupRoot = copy.RootMsgID
		}
	}
	return &copy, nil
}

func (b *Bridge) refreshGoalCard(ctx context.Context, base *LarkChat, goal *agentstream.GoalState) error {
	destination, err := b.db.goalDestination(b.currentBridgeId(), base.TargetID, base.SessionName)
	if err != nil || destination == nil {
		return err
	}
	if goal != nil && goal.Objective != destination.Objective {
		return nil
	}
	allowed, err := b.canUseBotInGroup(destination.Chat.ChatID, destination.Chat.OpenId)
	if err != nil || !allowed {
		return err
	}
	destination.Chat.goalGeneration, destination.Chat.goalSessionID = destination.Generation, destination.SessionID
	var messageID string
	if err := b.db.db.QueryRow(`SELECT card_message_id FROM feishu_goal_destinations WHERE bridge_id=? AND target_id=? AND session_name=?`, b.currentBridgeId(), base.TargetID, base.SessionName).Scan(&messageID); err != nil {
		return err
	}
	if messageID == "" {
		return nil
	}
	return b.patchInteractiveMessage(ctx, messageID, renderGoalCard(&destination.Chat, goal), "goal state card")
}

func renderGoalCard(chat *LarkChat, goal *agentstream.GoalState) string {
	elements := []cardElement{managementKeyValueRowElement(sessionMessage("goal.scope"), chat.SessionName)}
	if strings.HasPrefix(chat.RootMsgID, "singleton:") {
		elements = append(elements, managementHintElement(sessionMessage("goal.shared")))
	}
	if goal == nil {
		elements = append(elements, managementTextElement(sessionMessage("goal.empty"), "normal", "default", nil))
	} else {
		status := sessionMessage("goal.status." + goal.Status)
		switch goal.Status {
		case "active", "paused", "blocked", "usageLimited", "budgetLimited", "complete":
		default:
			status = i18n.Literal(goal.Status)
		}
		elements = append(elements, managementKeyValueRowElement(sessionMessage("goal.objective"), goal.Objective), managementKeyValueRowElement(sessionMessage("goal.status"), status))
		budget := any(sessionMessage("goal.no_budget"))
		if goal.TokenBudget != nil {
			budget = strconv.FormatInt(*goal.TokenBudget, 10)
		}
		elements = append(elements, managementKeyValueRowElement(sessionMessage("goal.tokens"), strconv.FormatInt(goal.TokensUsed, 10)), managementKeyValueRowElement(sessionMessage("goal.budget"), budget), managementKeyValueRowElement(sessionMessage("goal.time"), sessionMessage("goal.seconds", goal.TimeUsedSeconds)))
		buttons := []cardElement{}
		if goal.Status == "active" && chat.goalGeneration != "" {
			buttons = append(buttons, goalButton(chat, goal, "pause"))
		} else if goal.Status != "complete" && chat.goalGeneration != "" {
			buttons = append(buttons, goalButton(chat, goal, "resume"))
		}
		if chat.goalGeneration != "" {
			buttons = append(buttons, goalButton(chat, goal, "clear"))
			elements = append(elements, managementFormButtonRowElement(buttons...))
		}
	}
	elements = append(elements, managementHintElement(sessionMessage("goal.usage")))
	return marshalNativeManagementCard(managementCardView{Title: sessionMessage("goal.title"), Subtitle: chat.SessionName, Summary: sessionMessage("goal.title"), Theme: "default", Icon: managementIcon("target_outlined", "blue"), Elements: elements})
}

func goalButton(chat *LarkChat, goal *agentstream.GoalState, action string) cardElement {
	value, _ := json.Marshal(map[string]string{"amux_goal_action": action, "target_id": chat.TargetID, "session_name": chat.SessionName, "session_id": chat.goalSessionID, "generation": chat.goalGeneration, "chat_id": chat.ChatID, "root_msg_id": chat.RootMsgID, "thread_id": goal.ThreadID, "objective_hash": goalObjectiveHash(goal.Objective), "created_at": strconv.FormatInt(goal.CreatedAt, 10)})
	return cardElement{Tag: "button", Name: "goal_" + action, Type: "default", Size: "medium", Text: managementPlainTextPtr(sessionMessage("goal.button." + action)), Value: value}
}

func goalObjectiveHash(objective string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(objective)))
}

func (b *Bridge) handleGoalCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, false, nil
	}
	action := callbackActionString(event.Event.Action, "amux_goal_action")
	if action == "" {
		return nil, false, nil
	}
	operator := ""
	if event.Event.Operator != nil {
		operator = event.Event.Operator.OpenID
	}
	locale := b.managementLocale(operator)
	toast := func(key string) (*callback.CardActionTriggerResponse, bool, error) {
		return cardToast("error", sessionMessage("goal."+key).For(locale)), true, nil
	}
	if action != "pause" && action != "resume" && action != "clear" {
		return toast("stale")
	}
	root := callbackActionString(event.Event.Action, "root_msg_id")
	unlock := b.lockRoute("goal:" + b.currentBridgeId() + ":" + callbackActionString(event.Event.Action, "target_id") + ":" + callbackActionString(event.Event.Action, "session_name"))
	defer unlock()
	var chat *LarkChat
	if root == singletonStreamKey(b.currentBridgeId()) {
		bs, err := b.db.GetBotSession(b.currentBridgeId())
		if err != nil || !botSessionReady(bs) {
			return toast("stale")
		}
		chat = b.botSessionChat(bs, botSessionInput{OpenID: operator, ChatID: callbackActionString(event.Event.Action, "chat_id"), ReplyKind: bs.LastReplyKind})
	} else {
		var err error
		chat, err = b.db.FindByRootMsg(b.currentBridgeId(), root)
		if err != nil || chat == nil || chat.Status != ChatStatusActive {
			return toast("stale")
		}
	}
	if chat.TargetID != callbackActionString(event.Event.Action, "target_id") || chat.SessionName != callbackActionString(event.Event.Action, "session_name") {
		return toast("stale")
	}
	destination, err := b.db.goalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName)
	if err != nil {
		return nil, true, err
	}
	if destination == nil || destination.Generation == "" || destination.Generation != callbackActionString(event.Event.Action, "generation") || destination.SessionID != callbackActionString(event.Event.Action, "session_id") {
		return toast("stale")
	}
	info, err := b.hub.Route(ctx, chat.TargetID, "List", &pb.ListRequest{})
	if err != nil {
		return toast("stale")
	}
	var list pb.ListResponse
	if json.Unmarshal(info.Payload, &list) != nil {
		return toast("stale")
	}
	matched := false
	for _, candidate := range list.Sessions {
		if candidate.Name == chat.SessionName && candidate.Id == destination.SessionID {
			matched = true
			break
		}
	}
	if !matched {
		return toast("stale")
	}
	chat.goalGeneration, chat.goalSessionID = destination.Generation, destination.SessionID
	if !b.canControlBinding(operator, chat) {
		return toast("denied")
	}
	allowed, err := b.canUseBotInGroup(chat.ChatID, operator)
	if err != nil || !allowed {
		return toast("denied")
	}
	goal, supported, err := b.goalRPC(ctx, chat, command.GoalCommand{Action: "get"})
	if err != nil {
		return cardToast("error", goalFailureMessage(err).For(locale)), true, nil
	}
	if !supported {
		return toast("unsupported")
	}
	if goal == nil || goal.ThreadID != callbackActionString(event.Event.Action, "thread_id") || goalObjectiveHash(goal.Objective) != callbackActionString(event.Event.Action, "objective_hash") || strconv.FormatInt(goal.CreatedAt, 10) != callbackActionString(event.Event.Action, "created_at") {
		return toast("stale")
	}
	goal, _, err = b.goalRPC(ctx, chat, command.GoalCommand{Action: action})
	if err != nil {
		return cardToast("error", goalFailureMessage(err).For(locale)), true, nil
	}
	if action == "clear" {
		if err := b.db.saveGoalDestination(b.currentBridgeId(), chat.TargetID, chat.SessionName, nil); err != nil {
			return nil, true, err
		}
	}
	return cardToastWithCard("success", sessionMessage("goal.updated").For(locale), renderGoalCard(chat, goal)), true, nil
}

func (b *Bridge) boundTopicHelpCard(ctx context.Context, chat *LarkChat) string {
	supported := false
	if chat != nil {
		response, err := b.hub.Route(ctx, chat.TargetID, "List", &pb.ListRequest{})
		var list pb.ListResponse
		if err == nil && json.Unmarshal(response.Payload, &list) == nil {
			for _, candidate := range list.Sessions {
				if candidate.Name == chat.SessionName {
					supported = candidate.GoalSupported
					break
				}
			}
		}
	}
	return renderBoundTopicHelpCardWithGoal(supported)
}
