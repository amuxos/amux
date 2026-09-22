package bridge

import (
	"context"
	"encoding/json"
	"fmt"
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
}

const resolvedPermissionRetention = 10 * time.Minute

type permissionPayload struct {
	Options []permissionOption `json:"options"`
	Tool    permissionTool     `json:"toolCall"`
}

type permissionOption struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	OptionId string `json:"optionId"`
}

type permissionTool struct {
	Title       string          `json:"title"`
	RawInput    json.RawMessage `json:"rawInput,omitempty"`
	Question    string          `json:"question,omitempty"`
	MultiSelect bool            `json:"multiSelect,omitempty"`
}

func permissionMapKey(targetID, sessionName, permissionID string) string {
	return strings.TrimSpace(targetID) + "\x00" + strings.TrimSpace(sessionName) + "\x00" + strings.TrimSpace(permissionID)
}

func (p *pendingPermission) key() string {
	if p == nil {
		return ""
	}
	return permissionMapKey(p.TargetID, p.SessionName, p.PermissionId)
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
	delete(c.permissions, key)
}

func (c *permissionGateway) getPermission(permissionID, targetID, sessionName string) (*pendingPermission, string) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	if c.permissions == nil {
		return nil, ""
	}
	if targetID != "" || sessionName != "" {
		key := permissionMapKey(targetID, sessionName, permissionID)
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

func (b *Bridge) handlePermissionEvent(ctx context.Context, rootMsgID, targetID, sessionName, openID string, permission *agentui.PermissionEvent) error {
	if permission == nil || permission.RequestID == "" {
		return nil
	}
	permissionID := permission.RequestID
	key, shouldSend := b.beginPermissionRequest(&pendingPermission{
		PermissionId: permissionID,
		RootMsgID:    rootMsgID,
		TargetID:     targetID,
		SessionName:  sessionName,
		OpenId:       openID,
	})
	if !shouldSend {
		return nil
	}
	payload := permissionPayloadFromEvent(permission)
	msgID, _, err := b.sendPermissionRequestCard(ctx, rootMsgID, targetID, sessionName, permissionID, payload)
	if err != nil {
		b.forgetPermissionRequest(key)
		return err
	}
	b.completePermissionRequest(key, msgID)
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
		}
	}
	for _, option := range permission.Options {
		out.Options = append(out.Options, permissionOption{Kind: option.Kind, Name: option.Label, OptionId: option.ID})
	}
	return out
}

func (b *Bridge) handleCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return cardToast("warning", "空的卡片操作"), nil
	}
	if resp, handled, err := b.handleSkillPickerCardAction(ctx, event); handled || err != nil {
		return resp, err
	}
	if resp, handled, err := b.handleManagementCardAction(ctx, event); handled || err != nil {
		return resp, err
	}
	value := event.Event.Action.Value
	permissionID, _ := value["permission_id"].(string)
	optionID, _ := value["option_id"].(string)
	optionIDs := permissionOptionIDs(event.Event.Action)
	if optionID == "" && len(optionIDs) > 0 {
		optionID = optionIDs[0]
	}
	wireOptionIDs := optionIDs
	if !permissionUsesMultipleOptions(event.Event.Action) {
		wireOptionIDs = nil
	}
	targetID, _ := value["target_id"].(string)
	sessionName, _ := value["session_name"].(string)
	if permissionID == "" || optionID == "" {
		return cardToast("warning", "不是 amux 授权操作"), nil
	}
	pending, permissionKey := b.getPermission(permissionID, targetID, sessionName)
	if pending == nil {
		return cardToast("warning", "授权请求已处理或已过期"), nil
	}
	operator := ""
	if event.Event.Operator != nil {
		operator = event.Event.Operator.OpenID
	}
	if !b.canUseBridge(operator) && pending.OpenId != "" && pending.OpenId != operator {
		return cardToast("error", "你没有权限审批这个请求"), nil
	}
	pending = b.claimPermission(permissionKey)
	if pending == nil {
		return cardToast("warning", "授权请求正在处理或已处理"), nil
	}
	selection := optionID
	if len(optionIDs) > 0 {
		selection = strings.Join(optionIDs, "、")
	}
	displaySelection := truncateCardText(selection, 1000)

	sendCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	_, err := b.hub.Route(sendCtx, pending.TargetID, "RespondPermission", &pb.RespondPermissionRequest{
		Name:         pending.SessionName,
		PermissionId: permissionID,
		OptionId:     optionID,
		OptionIds:    wireOptionIDs,
	})
	if err != nil {
		b.releasePermissionClaim(permissionKey)
		log.Warn().Err(err).Str("permission_id", permissionID).Msg("bridge: respond permission failed")
		return cardToast("error", "授权回传失败: "+err.Error()), nil
	}
	b.resolvePermissionRequest(permissionKey)
	resolved := fmt.Sprintf("%s 于 %s 选择：%s", permissionOperatorName(operator), time.Now().Format("2006-01-02 15:04:05"), displaySelection)
	if pending.MessageID != "" {
		messageID := pending.MessageID
		go func() {
			patchCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = b.patchPermissionMessage(patchCtx, messageID, resolved)
		}()
	}
	return cardToastWithCard("success", "已提交选择: "+displaySelection, renderPermissionResolvedCard(resolved)), nil
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
