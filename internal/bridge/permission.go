package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/agentui"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// permissionGateway owns pending/resolved Feishu permission requests. It is
// embedded in Bridge so field access stays uniform while the mutex remains
// co-located with the state it protects.
type permissionGateway struct {
	permissionMu        sync.Mutex
	permissions         map[string]*pendingPermission
	resolvedPermissions map[string]time.Time
}

type pendingPermission struct {
	PermissionId string
	MessageID    string
	RootMsgID    string
	TargetID     string
	SessionName  string
	OpenId       string
	CreatedAt    time.Time
	Resolving    bool
	TurnID       string
	ChatID       string
	Question     *permissionPayload
	Chat         *LarkChat
	SessionID    string
	LaunchID     string
	Expiry       *time.Timer
}

const resolvedPermissionRetention = 10 * time.Minute

type permissionPayload struct {
	Options []permissionOption `json:"options"`
	Tool    permissionTool     `json:"toolCall"`
	TurnID  string             `json:"-"`
}

type permissionOption struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	OptionId    string `json:"optionId"`
	Description string `json:"description,omitempty"`
}

type permissionTool struct {
	Title          string          `json:"title"`
	RawInput       json.RawMessage `json:"rawInput,omitempty"`
	Question       string          `json:"question,omitempty"`
	MultiSelect    bool            `json:"multiSelect,omitempty"`
	AllowOther     bool            `json:"allowOther,omitempty"`
	Secret         bool            `json:"secret,omitempty"`
	Blocking       *bool           `json:"blocking,omitempty"`
	Delivery       string          `json:"delivery,omitempty"`
	AgentSessionID string          `json:"agentSessionId,omitempty"`
}

func permissionMapKey(targetID, sessionName, permissionID string) string {
	return strings.TrimSpace(targetID) + "\x00" + strings.TrimSpace(sessionName) + "\x00" + strings.TrimSpace(permissionID)
}

func (p *pendingPermission) key() string {
	if p == nil {
		return ""
	}
	key := permissionMapKey(p.TargetID, p.SessionName, p.PermissionId)
	if p.TurnID != "" {
		key += "\x00" + p.TurnID
	}
	return key
}

func (c *permissionGateway) beginPermissionRequest(req *pendingPermission) (string, bool) {
	if req == nil || req.PermissionId == "" {
		return "", false
	}
	key := req.key()
	if key == "" {
		return "", false
	}
	now := time.Now()
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	c.pruneResolvedPermissionsLocked(now)
	if until, ok := c.resolvedPermissions[key]; ok && now.Before(until) {
		return key, false
	}
	if c.permissions == nil {
		c.permissions = make(map[string]*pendingPermission)
	}
	if _, ok := c.permissions[key]; ok {
		return key, false
	}
	req.CreatedAt = now
	c.permissions[key] = req
	return key, true
}

func (c *permissionGateway) completePermissionRequest(key, messageID string) {
	if key == "" {
		return
	}
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	if req := c.permissions[key]; req != nil {
		req.MessageID = messageID
	}
}

func (c *permissionGateway) forgetPermissionRequest(key string) {
	if key == "" {
		return
	}
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	if pending := c.permissions[key]; pending != nil && pending.Expiry != nil {
		pending.Expiry.Stop()
	}
	delete(c.permissions, key)
}

func (c *permissionGateway) getPermission(permissionID, targetID, sessionName string, turnIDs ...string) (*pendingPermission, string) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	if c.permissions == nil {
		return nil, ""
	}
	if targetID != "" || sessionName != "" {
		key := permissionMapKey(targetID, sessionName, permissionID)
		if len(turnIDs) > 0 && turnIDs[0] != "" {
			key += "\x00" + turnIDs[0]
		}
		return clonePendingPermission(c.permissions[key]), key
	}
	var found *pendingPermission
	foundKey := ""
	for key, req := range c.permissions {
		if req == nil || req.PermissionId != permissionID {
			continue
		}
		if found != nil {
			return nil, ""
		}
		found = req
		foundKey = key
	}
	return clonePendingPermission(found), foundKey
}

func clonePendingPermission(permission *pendingPermission) *pendingPermission {
	if permission == nil {
		return nil
	}
	cloned := *permission
	return &cloned
}

func (c *permissionGateway) claimPermission(key string) *pendingPermission {
	if key == "" {
		return nil
	}
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	pending := c.permissions[key]
	if pending == nil || pending.Resolving {
		return nil
	}
	pending.Resolving = true
	return clonePendingPermission(pending)
}

func (c *permissionGateway) releasePermissionClaim(key string) {
	if key == "" {
		return
	}
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	if pending := c.permissions[key]; pending != nil {
		pending.Resolving = false
	}
}

func (c *permissionGateway) resolvePermissionRequest(key string) {
	if key == "" {
		return
	}
	now := time.Now()
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	if pending := c.permissions[key]; pending != nil && pending.Expiry != nil {
		pending.Expiry.Stop()
	}
	delete(c.permissions, key)
	if c.resolvedPermissions == nil {
		c.resolvedPermissions = make(map[string]time.Time)
	}
	c.pruneResolvedPermissionsLocked(now)
	c.resolvedPermissions[key] = now.Add(resolvedPermissionRetention)
}

func (c *permissionGateway) pruneResolvedPermissionsLocked(now time.Time) {
	for key, until := range c.resolvedPermissions {
		if now.After(until) {
			delete(c.resolvedPermissions, key)
		}
	}
}

func (b *Bridge) handlePermissionEvent(ctx context.Context, chat *LarkChat, permission *agentui.PermissionEvent, turnIDs ...string) error {
	if permission == nil || permission.RequestID == "" {
		return nil
	}
	permissionID := permission.RequestID
	payload := permissionPayloadFromEvent(permission)
	var question *permissionPayload
	if strings.TrimSpace(payload.Tool.Question) != "" {
		question = &payload
	}
	turnID := ""
	if question != nil && len(turnIDs) > 0 {
		turnID = turnIDs[0]
		payload.TurnID = turnID
	}
	if question != nil {
		bound := *chat
		b.steerMu.Lock()
		if senders := b.turnSenders[chat.RootMsgID][turnID]; len(senders) > 0 {
			bound.OpenId = senders[0]
		}
		b.steerMu.Unlock()
		chat = &bound
	}
	pending := &pendingPermission{
		PermissionId: permissionID,
		RootMsgID:    chat.RootMsgID,
		TargetID:     chat.TargetID,
		SessionName:  chat.SessionName,
		OpenId:       chat.OpenId,
		TurnID:       turnID, Question: question, ChatID: chat.ChatID,
		SessionID: permission.SessionID, LaunchID: permission.LaunchID,
	}
	bound := *chat
	pending.Chat = &bound
	key, shouldSend := b.beginPermissionRequest(pending)
	if !shouldSend {
		return nil
	}
	if question != nil && (payload.Tool.Secret || chat.OpenId == "" || !questionCardFits(payload) ||
		(payload.Tool.Delivery == "async" && (pending.SessionID == "" || pending.LaunchID == "" || payload.Tool.AgentSessionID == ""))) {
		b.cancelQuestion(pending)
		b.resolvePermissionRequest(key)
		return b.sendQuestionUnavailable(ctx, chat, payload.Tool.Secret)
	}
	msgID, _, err := b.sendPermissionRequestCard(ctx, chat, permissionID, payload)
	if err != nil {
		b.forgetPermissionRequest(key)
		if question != nil {
			b.cancelQuestion(pending)
		}
		return err
	}
	b.completePermissionRequest(key, msgID)
	if question != nil {
		b.armQuestionExpiry(key)
	}
	return nil
}

func permissionPayloadFromEvent(permission *agentui.PermissionEvent) permissionPayload {
	if permission == nil {
		return permissionPayload{}
	}
	out := permissionPayload{}
	if permission.Tool != nil {
		out.Tool = permissionTool{
			Title:       permission.Tool.Title,
			RawInput:    append(json.RawMessage(nil), permission.Tool.RawInput...),
			Question:    permission.Tool.Question,
			MultiSelect: permission.Tool.MultiSelect,
			AllowOther:  permission.Tool.AllowOther, Secret: permission.Tool.Secret, Blocking: permission.Tool.Blocking,
			Delivery: permission.Tool.Delivery, AgentSessionID: permission.Tool.AgentSessionID,
		}
	}
	for _, option := range permission.Options {
		out.Options = append(out.Options, permissionOption{Kind: option.Kind, Name: option.Label, OptionId: option.ID, Description: option.Description})
	}
	return out
}

func (b *Bridge) handleCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return cardToast("warning", managementCopy("permission.invalid")), nil
	}
	if resp, handled, err := b.handleSkillPickerCardAction(ctx, event); handled || err != nil {
		return resp, err
	}
	if resp, handled, err := b.handleGoalCardAction(ctx, event); handled || err != nil {
		return resp, err
	}
	if observer := b.getConversationObserver(); observer != nil {
		if resp, handled, err := observer.CardAction(ctx, event); handled || err != nil {
			return resp, err
		}
	}
	if resp, handled, err := b.handleManagementCardAction(ctx, event); handled || err != nil {
		return resp, err
	}
	value := event.Event.Action.Value
	permissionID, _ := value["permission_id"].(string)
	targetID, _ := value["target_id"].(string)
	sessionName, _ := value["session_name"].(string)
	turnID, _ := value["turn_id"].(string)
	cancelQuestion, _ := value["cancel"].(string)
	if permissionID == "" {
		return cardToast("warning", managementCopy("permission.invalid")), nil
	}
	pending, permissionKey := b.getPermission(permissionID, targetID, sessionName, turnID)
	if pending == nil {
		return cardToast("warning", managementCopy("permission.expired")), nil
	}
	operator := ""
	if event.Event.Operator != nil {
		operator = event.Event.Operator.OpenID
	}
	if pending.Question != nil {
		if operator == "" || operator != pending.OpenId {
			return cardToast("error", managementCopy("question.unauthorized")), nil
		}
		if time.Since(pending.CreatedAt) >= questionLifetime || turnID != pending.TurnID {
			return cardToast("warning", managementCopy("permission.expired")), nil
		}
		if c := event.Event.Context; c != nil {
			if (c.OpenMessageID != "" && c.OpenMessageID != pending.MessageID) ||
				(c.OpenChatID != "" && c.OpenChatID != pending.ChatID) {
				return cardToast("error", managementCopy("permission.invalid")), nil
			}
		}
	} else if !b.canUseBridge(operator) && pending.OpenId != "" && pending.OpenId != operator {
		return cardToast("error", managementCopy("permission.unauthorized")), nil
	}
	isCancel := cancelQuestion == "true" && pending.Question != nil
	optionIDs := permissionOptionIDs(event.Event.Action)
	if pending.Question != nil && !isCancel {
		var valid bool
		optionIDs, valid = questionAnswers(*pending.Question, event.Event.Action)
		if !valid {
			return cardToast("warning", managementCopy("question.invalid_answer")), nil
		}
	}
	if !isCancel && len(optionIDs) == 0 {
		return cardToast("warning", managementCopy("question.invalid_answer")), nil
	}
	pending = b.claimPermission(permissionKey)
	if pending == nil {
		return cardToast("warning", managementCopy("permission.processing")), nil
	}
	request := &pb.RespondPermissionRequest{
		Name: pending.SessionName, PermissionId: permissionID, TurnId: pending.TurnID, Cancel: isCancel,
	}
	if !isCancel {
		request.OptionId = optionIDs[0]
		if (pending.Question != nil && pending.Question.Tool.MultiSelect) || permissionUsesMultipleOptions(event.Event.Action) {
			request.OptionIds = optionIDs
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	var err error
	if pending.Question != nil && pending.Question.Tool.Delivery == "async" {
		if !isCancel {
			err = b.submitAsyncQuestionAnswer(sendCtx, pending, optionIDs)
		}
	} else {
		_, err = b.hub.Route(sendCtx, pending.TargetID, "RespondPermission", request)
	}
	if err != nil {
		if pending.Question != nil && pending.Question.Tool.Delivery == "async" {
			// A broken receipt cannot prove the queued input was rejected. Never
			// permit a second submit from this card after an ambiguous outcome.
			b.resolvePermissionRequest(permissionKey)
			text := managementCopy("question.delivery_unknown")
			b.patchQuestionAsync(pending.MessageID, text)
			log.Warn().Err(err).Str("permission_id", permissionID).Msg("bridge: async question answer delivery unconfirmed")
			return cardToastWithCard("warning", text, renderPermissionResolvedCard(text)), nil
		}
		b.releasePermissionClaim(permissionKey)
		log.Warn().Err(err).Str("permission_id", permissionID).Msg("bridge: respond permission failed")
		return cardToast("error", managementCopy("permission.failed")), nil
	}
	b.resolvePermissionRequest(permissionKey)
	resolved := managementCopy("permission.submitted", truncateCardText(strings.Join(optionIDs, "、"), 1000))
	if isCancel {
		resolved = managementCopy("question.cancelled")
	}
	b.patchQuestionAsync(pending.MessageID, resolved)
	return cardToastWithCard("success", resolved, renderPermissionResolvedCard(resolved)), nil
}

func permissionOperatorName(operator string) string {
	if operator == "" {
		return "审批人"
	}
	return operator
}

func permissionUsesMultipleOptions(action *callback.CallBackAction) bool {
	if action == nil {
		return false
	}
	if _, ok := action.FormValue["option_ids"]; ok {
		return true
	}
	return len(action.Options) > 1
}

func permissionOptionIDs(action *callback.CallBackAction) []string {
	if action == nil {
		return nil
	}
	for _, key := range []string{"option_ids", "option_id", "answer"} {
		if values := stringValues(action.FormValue[key]); len(values) > 0 {
			return values
		}
	}
	if len(action.Options) > 0 {
		return append([]string(nil), action.Options...)
	}
	if action.Option != "" {
		return []string{action.Option}
	}
	if action.InputValue != "" {
		return []string{action.InputValue}
	}
	if optionID, _ := action.Value["option_id"].(string); optionID != "" {
		return []string{optionID}
	}
	return nil
}

func stringValues(value any) []string {
	switch value := value.(type) {
	case string:
		if value != "" {
			return []string{value}
		}
	case []string:
		return append([]string(nil), value...)
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

func cardToast(kind string, content any) *callback.CardActionTriggerResponse {
	text := managementPlainText(content)
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: kind, Content: text.Content, I18nContent: text.I18nContent}}
}

func cardToastWithCard(kind string, content any, cardJSON string) *callback.CardActionTriggerResponse {
	response := cardToast(kind, content)
	var data any
	if json.Unmarshal([]byte(cardJSON), &data) == nil {
		// card.action.trigger uses the callback response protocol, where an
		// inline JSON replacement is type=raw. card_json belongs to the CardKit
		// create/update API and makes Feishu report an operation failure even
		// though the business action already succeeded.
		response.Card = &callback.Card{Type: "raw", Data: data}
	}
	return response
}

func permissionSummary(payload permissionPayload) string {
	var lines []string
	title := strings.TrimSpace(payload.Tool.Title)
	// User-input permissions (item/tool/requestUserInput) carry the question
	// prompt and present as a question card, not a tool-approval card.
	if strings.TrimSpace(payload.Tool.Question) != "" {
		if title == "" {
			title = "Agent 需要你回答"
		}
		lines = append(lines, "**"+escapeCardMarkdown(title)+"**")
		lines = append(lines, escapeCardMarkdown(payload.Tool.Question))
		return strings.Join(lines, "\n")
	}
	if title == "" {
		title = "工具调用"
	}
	lines = append(lines, "**操作**: "+escapeCardMarkdown(title))
	if scope := permissionImpactScope(payload.Tool.RawInput); scope != "" {
		lines = append(lines, "**影响范围**: "+escapeCardMarkdown(scope))
	}
	if len(payload.Tool.RawInput) > 0 {
		bs, _ := json.MarshalIndent(payload.Tool.RawInput, "", "  ")
		lines = append(lines, "**输入**:\n```json\n"+truncateCardText(string(bs), maxPermissionInputRunes)+"\n```")
	}
	return strings.Join(lines, "\n")
}

func permissionImpactScope(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "filePath", "FilePath", "cwd", "url", "URL"} {
		var value string
		if field, ok := fields[key]; ok && json.Unmarshal(field, &value) == nil && strings.TrimSpace(value) != "" {
			return truncateCardText(strings.TrimSpace(value), 300)
		}
	}
	for _, key := range []string{"command", "Command", "cmd", "Cmd"} {
		if _, ok := fields[key]; ok {
			return "命令执行"
		}
	}
	return ""
}

func escapeCardMarkdown(s string) string {
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}
