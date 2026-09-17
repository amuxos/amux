package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/distribution"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

// skillPickerStore owns skill-picker submission dedupe state.
type skillPickerStore struct {
	skillPickerMu     sync.Mutex
	skillPickerClaims map[string]time.Time
}

const (
	skillPickerActionKey     = "amux_skill_action"
	skillPickerActionRun     = "run"
	skillPickerActionPage    = "page"
	skillPickerSurfaceThread = "thread"
	skillPickerSurfaceBot    = "bot_session"
	skillPickerPageSize      = 50
	skillPickerClaimTTL      = 30 * 24 * time.Hour
)

type skillPickerTarget struct {
	Surface     string
	TargetID    string
	SessionName string
	RootMsgID   string
	ReplyKind   string
	ChatID      string
}

func parseSkillCommand(text string) (name, task string, ok bool) {
	command, ok := slashCommandText(text)
	if !ok {
		return "", "", false
	}
	parts := strings.Fields(command)
	if len(parts) == 0 || !strings.EqualFold(parts[0], "skill") {
		return "", "", false
	}
	if len(parts) == 1 {
		return "", "", true
	}
	name = parts[1]
	rest := strings.TrimSpace(strings.TrimPrefix(command, parts[0]))
	rest = strings.TrimSpace(strings.TrimPrefix(rest, name))
	return name, rest, true
}

func isSkillSlashCommand(text string) bool {
	_, _, ok := parseSkillCommand(text)
	return ok
}

func skillPrompt(name, task string) string {
	prompt := "$" + strings.TrimSpace(name)
	if task = strings.TrimSpace(task); task != "" {
		prompt += " " + task
	}
	return prompt
}

func renderSkillPickerCard(target skillPickerTarget, skills []advertisedAgentCommand, page int) string {
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	pages := (len(skills) + skillPickerPageSize - 1) / skillPickerPageSize
	if pages == 0 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}

	elements := []cardElement{
		managementTextElement("选择一个当前 Session 已加载的 Skill；也可以填写本次希望它完成的任务。", "normal", "default", nil),
	}
	if len(skills) == 0 {
		elements = append(elements, managementTextElement("当前 Session 尚未提供可用 Skill。请稍后重试，或确认该 Session 使用 "+distribution.CodexCompatibleChoice+"。", "normal", "grey", nil))
	} else {
		start := page * skillPickerPageSize
		end := start + skillPickerPageSize
		if end > len(skills) {
			end = len(skills)
		}
		options := make([]cardOption, 0, end-start)
		for _, skill := range skills[start:end] {
			label := skill.Name
			if description := strings.TrimSpace(skill.Description); description != "" {
				label += " · " + truncateRunes(strings.Join(strings.Fields(description), " "), 56)
			}
			options = append(options, cardOption{
				Text:  cardTitle{Tag: "plain_text", Content: label},
				Value: skill.Name,
			})
		}
		form := cardElement{Tag: "form", Name: "skill_picker_form"}
		form.Elements = append(form.Elements,
			cardElement{
				Tag: "select_static", Name: "skill_name", Options: options, Required: true, Width: "fill",
				Placeholder: &cardTitle{Tag: "plain_text", Content: "选择 Skill"},
			},
			cardElement{
				Tag: "input", Name: "skill_task", Required: false,
				Placeholder: &cardTitle{Tag: "plain_text", Content: "输入本次任务（可选）"},
			},
			skillPickerButton(skillPickerActionRun, "执行 Skill", "primary", target, page, true),
		)
		elements = append(elements, form)
		if pages > 1 {
			buttons := make([]cardElement, 0, 2)
			if page > 0 {
				buttons = append(buttons, skillPickerButton(skillPickerActionPage, "上一页", "default", target, page-1, false))
			}
			if page+1 < pages {
				buttons = append(buttons, skillPickerButton(skillPickerActionPage, "下一页", "default", target, page+1, false))
			}
			elements = append(elements, managementFormButtonRowElement(buttons...))
		}
		elements = append(elements, managementTextElement(
			fmt.Sprintf("共 %d 个 Skill · 第 %d/%d 页；也可发送 /skill <name> [任务]。", len(skills), page+1, pages),
			"notation", "grey", nil,
		))
	}
	return marshalNativeManagementCard(managementCardView{
		Title:    "选择 Skill",
		Subtitle: strings.TrimSpace(target.SessionName),
		Summary:  "amux Skill 选择器",
		Theme:    "default",
		Icon:     managementIcon("robot_outlined", "blue"),
		Elements: elements,
	})
}

func skillPickerButton(action, text, buttonType string, target skillPickerTarget, page int, formSubmit bool) cardElement {
	value := map[string]any{
		skillPickerActionKey: action,
		"surface":            target.Surface,
		"target_id":          target.TargetID,
		"session_name":       target.SessionName,
		"root_msg_id":        target.RootMsgID,
		"reply_kind":         target.ReplyKind,
		"chat_id":            target.ChatID,
		"page":               strconv.Itoa(page),
	}
	raw, _ := json.Marshal(value)
	button := cardElement{
		Tag: "button", Name: "skill_picker_" + action, Type: buttonType, Size: "medium",
		Text: &cardTitle{Tag: "plain_text", Content: text}, Value: raw,
	}
	if formSubmit {
		button.ActionType = "form_submit"
	}
	return button
}

func (b *Bridge) handleThreadSkillCommand(ctx context.Context, chat *LarkChat, messageID, openID, text string) error {
	if chat == nil {
		return nil
	}
	if !supportsNativeSkillDiscovery(chat.AgentType) {
		return b.notifyThread(ctx, chat.RootMsgID, "/skill 当前仅支持 "+distribution.CodexCompatibleChoice+" session。")
	}
	name, task, _ := parseSkillCommand(text)
	if name == "" {
		target := skillPickerTarget{
			Surface: skillPickerSurfaceThread, TargetID: chat.TargetID, SessionName: chat.SessionName,
			RootMsgID: chat.RootMsgID, ChatID: chat.ChatID,
		}
		return b.notifyThreadHelpCard(ctx, chat.RootMsgID, renderSkillPickerCard(target, b.advertisedSkills(chat.TargetID, chat.SessionName), 0))
	}
	if _, ok := b.advertisedSkill(chat.TargetID, chat.SessionName, name); !ok {
		return b.notifyThread(ctx, chat.RootMsgID, fmt.Sprintf("当前 Session 未加载 Skill：%s。请发送 /skill 重新选择。", name))
	}
	if _, err := b.submitQueuedToSessionForMessages(ctx, chat, skillPrompt(name, task), compactTurnInputRefs(openID, messageID)); err != nil {
		return b.notifyThread(ctx, chat.RootMsgID, fmt.Sprintf("⚠️ Skill 提交失败：%v", err))
	}
	return b.notifyThread(ctx, chat.RootMsgID, fmt.Sprintf("已将 $%s 加入队列。", name))
}

func (b *Bridge) handleBotSessionSkillCommand(ctx context.Context, bs *BotSession, in botSessionInput, text string) error {
	if bs == nil {
		return nil
	}
	if !supportsNativeSkillDiscovery(bs.AgentType) {
		return b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, "/skill 当前仅支持 "+distribution.CodexCompatibleChoice+" session。")
	}
	name, task, _ := parseSkillCommand(text)
	if name == "" {
		target := skillPickerTarget{
			Surface: skillPickerSurfaceBot, TargetID: bs.TargetID, SessionName: bs.SessionName,
			ReplyKind: in.ReplyKind, ChatID: in.ChatID,
		}
		cardTarget := *bs
		cardTarget.LastReplyKind = in.ReplyKind
		cardTarget.LastReplyChatID = in.ChatID
		cardTarget.LastReplyOpenID = in.OpenID
		_, _, err := b.sendInteractiveToDirectTarget(ctx, botSessionReplyTarget(&cardTarget),
			renderSkillPickerCard(target, b.advertisedSkills(bs.TargetID, bs.SessionName), 0), "skill picker card")
		return err
	}
	if _, ok := b.advertisedSkill(bs.TargetID, bs.SessionName, name); !ok {
		return b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID,
			fmt.Sprintf("当前 Session 未加载 Skill：%s。请发送 /skill 重新选择。", name))
	}
	chat := b.botSessionChat(bs, in)
	if _, err := b.submitQueuedToSessionForMessages(ctx, chat, skillPrompt(name, task), compactTurnInputRefs(in.OpenID, in.MessageID)); err != nil {
		return b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, botSessionSubmitFailureText(err))
	}
	return b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, fmt.Sprintf("已将 $%s 加入队列。", name))
}

func (b *Bridge) handleSkillPickerCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, false, nil
	}
	action := strings.TrimSpace(callbackActionString(event.Event.Action, skillPickerActionKey))
	if action == "" {
		return nil, false, nil
	}
	operator := ""
	if event.Event.Operator != nil {
		operator = strings.TrimSpace(event.Event.Operator.OpenID)
	}
	target := skillPickerTarget{
		Surface:     callbackActionString(event.Event.Action, "surface"),
		TargetID:    callbackActionString(event.Event.Action, "target_id"),
		SessionName: callbackActionString(event.Event.Action, "session_name"),
		RootMsgID:   callbackActionString(event.Event.Action, "root_msg_id"),
		ReplyKind:   callbackActionString(event.Event.Action, "reply_kind"),
		ChatID:      callbackActionString(event.Event.Action, "chat_id"),
	}
	page, _ := strconv.Atoi(callbackActionString(event.Event.Action, "page"))

	chat, bs, authErr := b.resolveSkillPickerTarget(target, operator)
	if authErr != nil {
		return cardToast("error", authErr.Error()), true, nil
	}
	skills := b.advertisedSkills(target.TargetID, target.SessionName)
	if action == skillPickerActionPage {
		return cardToastWithCard("success", "已切换 Skill 列表", renderSkillPickerCard(target, skills, page)), true, nil
	}
	if action != skillPickerActionRun {
		return cardToast("warning", "未知的 Skill 操作"), true, nil
	}
	name := callbackActionString(event.Event.Action, "skill_name")
	task := callbackActionString(event.Event.Action, "skill_task")
	if name == "" {
		return cardToast("warning", "请选择一个 Skill"), true, nil
	}
	if _, ok := b.advertisedSkill(target.TargetID, target.SessionName, name); !ok {
		return cardToastWithCard("warning", "该 Skill 已不可用，请重新选择", renderSkillPickerCard(target, skills, page)), true, nil
	}
	claimKey := b.skillPickerSubmissionKey(event)
	actionMessageID := ""
	if event.Event.Context != nil {
		actionMessageID = strings.TrimSpace(event.Event.Context.OpenMessageID)
	}
	if !b.claimSkillPickerSubmission(claimKey) {
		return cardToastWithCard("warning", "该 Skill 已提交，请勿重复点击", renderSkillPickerSubmittedCard(name, task)), true, nil
	}
	claimed := claimKey != ""
	releaseClaim := func() {
		if claimed {
			b.releaseSkillPickerSubmission(claimKey)
			claimed = false
		}
	}

	submitCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	if bs != nil {
		if err := b.db.SetBotSessionLastReply(b.currentBridgeId(), target.ReplyKind, target.ChatID, operator, ""); err != nil {
			releaseClaim()
			return cardToast("error", "无法更新当前对话目标"), true, nil
		}
		bs.LastReplyKind = target.ReplyKind
		bs.LastReplyChatID = target.ChatID
		bs.LastReplyOpenID = operator
		b.startSingletonStream(bs, operator)
		chat = b.botSessionChat(bs, botSessionInput{ChatID: target.ChatID, OpenID: operator, ReplyKind: target.ReplyKind})
	} else {
		b.ensureThreadStream(chat)
	}
	if _, err := b.submitQueuedToSessionForMessages(submitCtx, chat, skillPrompt(name, task), compactTurnInputRefs(operator, actionMessageID)); err != nil {
		releaseClaim()
		log.Warn().Err(err).Str("session", target.SessionName).Str("skill", name).
			Msg("bridge: skill picker submit failed")
		return cardToast("error", "Skill 提交失败，请稍后重试"), true, nil
	}
	log.Info().Str("session", target.SessionName).Str("skill", name).
		Msg("bridge: skill picker submitted")
	return cardToastWithCard("success", "已提交 $"+name, renderSkillPickerSubmittedCard(name, task)), true, nil
}

func (b *Bridge) skillPickerSubmissionKey(event *callback.CardActionTriggerEvent) string {
	if b == nil || event == nil || event.Event == nil {
		return ""
	}
	messageID := ""
	if event.Event.Context != nil {
		messageID = strings.TrimSpace(event.Event.Context.OpenMessageID)
	}
	if messageID == "" {
		return ""
	}
	return b.currentBridgeId() + "\x00" + messageID
}

func (c *skillPickerStore) claimSkillPickerSubmission(key string) bool {
	if c == nil || key == "" {
		return true
	}
	now := time.Now()
	c.skillPickerMu.Lock()
	defer c.skillPickerMu.Unlock()
	if c.skillPickerClaims == nil {
		c.skillPickerClaims = make(map[string]time.Time)
	}
	for existing, claimedAt := range c.skillPickerClaims {
		if now.Sub(claimedAt) >= skillPickerClaimTTL {
			delete(c.skillPickerClaims, existing)
		}
	}
	if _, exists := c.skillPickerClaims[key]; exists {
		return false
	}
	c.skillPickerClaims[key] = now
	return true
}

func (c *skillPickerStore) releaseSkillPickerSubmission(key string) {
	if c == nil || key == "" {
		return
	}
	c.skillPickerMu.Lock()
	delete(c.skillPickerClaims, key)
	c.skillPickerMu.Unlock()
}

func (b *Bridge) resolveSkillPickerTarget(target skillPickerTarget, operator string) (*LarkChat, *BotSession, error) {
	if b == nil || b.db == nil || target.TargetID == "" || target.SessionName == "" {
		return nil, nil, fmt.Errorf("Skill 选择卡已失效，请重新发送 /skill")
	}
	switch target.Surface {
	case skillPickerSurfaceThread:
		chat, err := b.db.FindByRootMsg(b.currentBridgeId(), target.RootMsgID)
		if err != nil || chat == nil || chat.Status != ChatStatusActive || chat.TargetID != target.TargetID || chat.SessionName != target.SessionName {
			return nil, nil, fmt.Errorf("Skill 选择卡已失效，请重新发送 /skill")
		}
		if !supportsNativeSkillDiscovery(chat.AgentType) {
			return nil, nil, fmt.Errorf("/skill 当前仅支持 %s session", distribution.CodexCompatibleChoice)
		}
		allowed, err := b.canUseBotInGroup(chat.ChatID, operator)
		if err != nil || !allowed {
			return nil, nil, fmt.Errorf("你没有权限在当前对话执行 Skill")
		}
		return chat, nil, nil
	case skillPickerSurfaceBot:
		bs, err := b.db.GetBotSession(b.currentBridgeId())
		if err != nil || !botSessionReady(bs) || bs.TargetID != target.TargetID || bs.SessionName != target.SessionName {
			return nil, nil, fmt.Errorf("Skill 选择卡已失效，请重新发送 /skill")
		}
		if !supportsNativeSkillDiscovery(bs.AgentType) {
			return nil, nil, fmt.Errorf("/skill 当前仅支持 %s session", distribution.CodexCompatibleChoice)
		}
		allowed := b.canUseBridge(operator)
		if target.ReplyKind == botSessionReplyGroup {
			allowed, err = b.canUseBotInGroup(target.ChatID, operator)
		}
		if err != nil || !allowed {
			return nil, nil, fmt.Errorf("你没有权限在当前对话执行 Skill")
		}
		return nil, bs, nil
	default:
		return nil, nil, fmt.Errorf("Skill 选择卡已失效，请重新发送 /skill")
	}
}

func supportsNativeSkillDiscovery(agentType string) bool {
	return adapter.IsCodexCompatible(agentType)
}

func callbackActionString(action *callback.CallBackAction, key string) string {
	if action == nil {
		return ""
	}
	if value, ok := action.Value[key].(string); ok {
		return strings.TrimSpace(value)
	}
	if value, ok := action.FormValue[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func renderSkillPickerSubmittedCard(name, task string) string {
	body := "$" + strings.TrimSpace(name)
	if task = strings.TrimSpace(task); task != "" {
		body += "\n\n" + task
	}
	return marshalNativeManagementCard(managementCardView{
		Title:    "Skill 已提交",
		Subtitle: "已进入当前 Session 队列",
		Summary:  "amux Skill 已提交",
		Theme:    "default",
		Icon:     managementIcon("check_outlined", "green"),
		Elements: []cardElement{managementTextElement(body, "normal", "default", nil)},
	})
}
