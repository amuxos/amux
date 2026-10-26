package bridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/bridge/command"
	bridgeprompt "github.com/amuxos/amux/internal/bridge/prompt"
	"github.com/amuxos/amux/internal/bridge/threadcontext"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
	"github.com/rs/zerolog/log"
)

func (b *Bridge) currentWorkspace(chatID string) (*FeishuWorkspace, error) {
	if b == nil || b.db == nil {
		return &FeishuWorkspace{Mode: defaultWorkspaceMode, DefaultAgent: agentprofile.DefaultProfileRef, DefaultArgs: nil, ThreadContextLimit: defaultThreadContextLimit, MaxSessionsTotal: defaultWorkspaceMaxSessionsTotal, MaxSessionsPerUser: defaultWorkspaceMaxSessionsUser, IdleTimeoutSeconds: defaultIdleTimeoutSeconds}, nil
	}
	return b.db.GetWorkspace(b.currentBridgeId(), chatID)
}
func (b *Bridge) recordHumanThreadContext(ctx context.Context, chatID, rootMsgID, msgID, openID, text string) {
	if b == nil || b.threadContext == nil || strings.TrimSpace(text) == "" || threadCommand(text) != command.CommandNone {
		return
	}
	if err := b.threadContext.RecordEvent(ctx, threadcontext.ThreadEvent{ChatID: chatID, RootMsgID: rootMsgID, MessageID: msgID, EventType: threadcontext.EventTypeHumanMessage, ActorType: "human", ActorID: openID, BridgeId: b.currentBridgeId(), BotProfile: b.profile, Text: text}); err != nil {
		log.Warn().Err(err).Str("root_msg_id", rootMsgID).Msg("bridge: failed to record human thread context")
	}
}
func (b *Bridge) recordRootThreadContext(ctx context.Context, chatID, rootMsgID, actorType, actorID, text string) {
	if b == nil || b.threadContext == nil || strings.TrimSpace(rootMsgID) == "" || strings.TrimSpace(text) == "" || freshThreadCommand(text) != command.CommandNone {
		return
	}
	if err := b.threadContext.RecordEvent(ctx, threadcontext.ThreadEvent{ChatID: chatID, RootMsgID: rootMsgID, ThreadID: rootMsgID, MessageID: rootMsgID, EventType: threadcontext.EventTypeTopicRoot, ActorType: actorType, ActorID: actorID, BridgeId: b.currentBridgeId(), BotProfile: b.profile, Text: text}); err != nil {
		log.Warn().Err(err).Str("root_msg_id", rootMsgID).Msg("bridge: failed to record root thread context")
	}
}
func (b *Bridge) recordUserRequestContext(ctx context.Context, chat *LarkChat, msgID, openID, text string) {
	if b == nil || b.threadContext == nil || chat == nil || strings.TrimSpace(text) == "" || threadCommand(text) != command.CommandNone {
		return
	}
	actorType := "human"
	if pending, err := b.db.PendingBotHandoff(b.currentBridgeId(), chat.RootMsgID); err == nil && pending != nil && pending.MessageID == msgID && pending.BotOpenID == openID {
		actorType = "bot"
	}
	if sourceRoot := groupInputRoot(ctx, chat.RootMsgID); sourceRoot != chat.RootMsgID {
		sourceChat := *chat
		sourceChat.RootMsgID, sourceChat.ThreadID = sourceRoot, sourceRoot
		chat = &sourceChat
	}
	if err := b.threadContext.RecordEvent(ctx, threadcontext.ThreadEvent{ChatID: chat.ChatID, RootMsgID: chat.RootMsgID, ThreadID: chat.ThreadID, MessageID: msgID, EventType: threadcontext.EventTypeUserRequest, ActorType: actorType, ActorID: openID, BridgeId: b.currentBridgeId(), BotProfile: b.profile, TargetID: chat.TargetID, TargetName: chat.TargetName, SessionName: chat.SessionName, Text: text}); err != nil {
		log.Warn().Err(err).Str("root_msg_id", chat.RootMsgID).Msg("bridge: failed to record user request context")
	}
}
func (b *Bridge) recordAgentThreadContext(ctx context.Context, chat *LarkChat, msgID, text string) {
	if b == nil || b.threadContext == nil || chat == nil || strings.TrimSpace(text) == "" {
		return
	}
	if err := b.threadContext.RecordEvent(ctx, threadcontext.ThreadEvent{ChatID: chat.ChatID, RootMsgID: chat.RootMsgID, ThreadID: chat.ThreadID, MessageID: msgID, EventType: threadcontext.EventTypeAgentOutput, ActorType: "agent", ActorID: chat.SessionName, BridgeId: b.currentBridgeId(), BotProfile: b.profile, TargetID: chat.TargetID, TargetName: chat.TargetName, SessionName: chat.SessionName, Text: text}); err != nil {
		log.Warn().Err(err).Str("root_msg_id", chat.RootMsgID).Msg("bridge: failed to record agent thread context")
	}
}
func (b *Bridge) withThreadContext(ctx context.Context, chat *LarkChat, msgID, request string, ws *FeishuWorkspace) (string, *threadcontext.ContextPackage) {
	request = strings.TrimSpace(request)
	if b != nil && b.db != nil && chat != nil && chat.ChatID != "" {
		if root, err := b.db.FindGroupSessionOrigin(b.currentBridgeId(), chat.ChatID, chat.RootMsgID); err == nil && root != "" {
			return request, nil
		}
	}
	if b == nil || b.threadContext == nil || chat == nil || ws == nil || !workspaceModeUsesThreadContext(ws.Mode) || request == "" || threadCommand(request) != command.CommandNone {
		return request, nil
	}
	pkg, err := b.threadContext.BuildPackage(ctx, threadcontext.BuildRequest{ChatID: chat.ChatID, RootMsgID: chat.RootMsgID, MessageID: msgID, BridgeId: b.currentBridgeId(), BotProfile: b.profile, WorkspaceMode: ws.Mode, RequestText: request, TargetID: chat.TargetID, TargetName: chat.TargetName, SessionName: chat.SessionName, ThreadContextLimit: ws.ThreadContextLimit})
	if err != nil {
		log.Warn().Err(err).Str("root_msg_id", chat.RootMsgID).Msg("bridge: failed to build Feishu thread context")
		return request, nil
	}
	if ws.Mode == WorkspaceModeOwnerOnly && pkg.ThreadContext == "" {
		return request, nil
	}
	renderer := b.promptRenderer
	if renderer == nil {
		renderer = bridgeprompt.NewRenderer(bridgeprompt.Options{})
	}
	out, err := renderer.RenderFeishuSharedThread(bridgeprompt.SharedThreadInput{ThreadContext: pkg.ThreadContext, UserRequest: request})
	if err != nil {
		log.Warn().Err(err).Msg("bridge: failed to render Feishu shared thread prompt")
		return request, &pkg
	}
	return out, &pkg
}
func (b *Bridge) recordThreadContextDecision(ctx context.Context, pkg *threadcontext.ContextPackage, prompt string) {
	if b == nil || b.threadContext == nil || pkg == nil {
		return
	}
	if _, err := b.threadContext.RecordDecision(ctx, *pkg, prompt); err != nil {
		log.Warn().Err(err).Str("root_msg_id", pkg.Request.RootMsgID).Msg("bridge: failed to record Feishu thread context decision")
	}
}
func (b *Bridge) withBotHandoffInstructions(rootMsgID, msgID, openID, request string) (string, error) {
	request = strings.TrimSpace(request)
	if b == nil || b.db == nil || request == "" || strings.TrimSpace(msgID) == "" || strings.TrimSpace(openID) == "" {
		return request, nil
	}
	pending, err := b.db.PendingBotHandoff(b.currentBridgeId(), rootMsgID)
	if err != nil {
		return "", fmt.Errorf("read Bot handoff prompt state: %w", err)
	}
	if pending == nil || pending.MessageID != strings.TrimSpace(msgID) || pending.BotOpenID != strings.TrimSpace(openID) {
		return request, nil
	}
	if b.botHandoffPrompt != nil {
		out, err := b.botHandoffPrompt(request)
		if err != nil {
			return "", fmt.Errorf("render Bot handoff prompt: %w", err)
		}
		return strings.TrimSpace(out), nil
	}
	renderer := b.promptRenderer
	if renderer == nil {
		renderer = bridgeprompt.NewRenderer(bridgeprompt.Options{})
	}
	out, err := renderer.RenderFeishuBotHandoff(request)
	if err != nil {
		return "", fmt.Errorf("render Bot handoff prompt: %w", err)
	}
	return out, nil
}
func workspaceModeUsesThreadContext(mode string) bool {
	return mode == WorkspaceModeOwnerOnly || mode == WorkspaceModeShared
}
func (b *Bridge) rootThreadRequestText(ctx context.Context, chatID, rootMsgID string) string {
	if b == nil || b.threadContext == nil || !b.topicRootPromptEnabled() {
		return ""
	}
	text, err := b.threadContext.RootMessageText(ctx, rootMsgID, b.currentBridgeId())
	if err != nil {
		log.Warn().Err(err).Str("root_msg_id", rootMsgID).Msg("bridge: failed to read root thread context")
		return ""
	}
	if text = strings.TrimSpace(text); text != "" && materialsFromContext(ctx) == nil {
		return text
	}
	lookup := b.topicRootLookup
	if lookup == nil {
		lookup = b.fetchTopicRootMessage
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	root, err := lookup(lookupCtx, rootMsgID)
	if err != nil {
		log.Warn().Err(err).Str("root_msg_id", rootMsgID).Msg("bridge: failed to fetch Feishu topic root")
		return text
	}
	if strings.TrimSpace(root.MessageID) != strings.TrimSpace(rootMsgID) {
		log.Warn().Str("root_msg_id", rootMsgID).Str("message_id", root.MessageID).Msg("bridge: fetched Feishu topic root has unexpected message id")
		return ""
	}
	if strings.TrimSpace(root.ChatID) != strings.TrimSpace(chatID) {
		log.Warn().Str("root_msg_id", rootMsgID).Str("chat_id", root.ChatID).Msg("bridge: fetched Feishu topic root belongs to another chat")
		return ""
	}
	collectMaterials(ctx, rootMsgID, root.Materials)
	text = strings.TrimSpace(root.Text)
	if text == "" {
		return ""
	}
	b.recordRootThreadContext(ctx, root.ChatID, root.MessageID, root.ActorType, root.ActorID, text)
	return text
}
func (b *Bridge) topicRootPromptDelivered(chat *LarkChat) bool {
	if b == nil || b.db == nil || chat == nil {
		return false
	}
	delivered, err := b.db.TopicRootPromptDelivered(chat.BridgeId, chat.RootMsgID)
	if err != nil {
		log.Warn().Err(err).Str("root_msg_id", chat.RootMsgID).Msg("bridge: failed to read topic root prompt delivery state")
		return false
	}
	return delivered
}
func (b *Bridge) ensureTopicRootInInput(rootText, request, input string) (string, bool) {
	rootText = strings.TrimSpace(rootText)
	request = strings.TrimSpace(request)
	input = strings.TrimSpace(input)
	if rootText == "" || !b.topicRootPromptEnabled() {
		return input, false
	}
	if rootText == request && strings.Contains(input, rootText) {
		return input, true
	}
	if b.promptRenderer != nil && b.promptRenderer.SharedThreadEnabled() && strings.Contains(input, rootText) {
		return input, true
	}
	renderer := b.promptRenderer
	if renderer == nil {
		renderer = bridgeprompt.NewRenderer(bridgeprompt.Options{})
	}
	rendered, err := renderer.RenderFeishuTopic(bridgeprompt.TopicInput{TopicRoot: rootText, UserRequest: input})
	if err != nil {
		log.Warn().Err(err).Msg("bridge: failed to render Feishu topic prompt")
		return input, false
	}
	return rendered, strings.Contains(rendered, rootText)
}
func (b *Bridge) topicRootPromptEnabled() bool {
	if b == nil {
		return false
	}
	return b.promptRenderer == nil || b.promptRenderer.TopicRootEnabled()
}
func (b *Bridge) canCreateOrBindInChat(chatID, openID string) (bool, error) {
	return b.canUseBotInGroup(chatID, openID)
}
func (b *Bridge) canControlBinding(openID string, chat *LarkChat) bool {
	if b.canUseBridge(openID) {
		return true
	}
	return chat != nil && chat.OpenId != "" && openID == chat.OpenId
}
func (b *Bridge) workspaceChatID() string {
	if b != nil && b.secrets != nil && b.secrets.NotifyChatId != "" {
		return b.secrets.NotifyChatId
	}
	return ""
}
func (b *Bridge) isWorkspaceChat(chatID string) (bool, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return false, nil
	}
	if b.workspaceChatID() == chatID {
		return true, nil
	}
	if b == nil || b.db == nil {
		return false, nil
	}
	return b.db.WorkspaceExists(b.currentBridgeId(), chatID)
}
func (b *Bridge) ensureWorkspaceForChat(chatID string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" || b == nil || b.db == nil {
		return nil
	}
	_, err := b.db.GetWorkspace(b.currentBridgeId(), chatID)
	return err
}
func (b *Bridge) ownerConfirmed() bool {
	return b != nil && b.secrets != nil && strings.TrimSpace(b.secrets.OpenId) != ""
}
func (b *Bridge) handleGroupPublicCommand(ctx context.Context, chatID, rootMsgID, openID string, intent command.Command, mentioned bool) error {
	if intent == command.CommandNone {
		return nil
	}
	if !b.ownerConfirmed() {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, ownerConfirmationRequiredText())
	}
	switch intent {
	case command.CommandAmux:
		if b.schedules != nil && !b.canUseBridge(openID) {
			return b.cmdAmuxManagement(ctx, openID)
		}
		ws, err := b.db.ReadWorkspace(b.currentBridgeId(), chatID)
		if err != nil {
			return err
		}
		profileState, err := b.currentBotProfileStateReadOnly()
		if err != nil {
			return err
		}
		groupSettings, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID)
		if err != nil {
			return err
		}
		mode := profileState.InteractionMode
		if groupSettings != nil {
			mode = groupSettings.InteractionMode
			ws = groupSettingsWorkspace(ws, groupSettings)
		}
		groupWatchStatus := b.groupWatchStatusForChat(chatID)
		paused := groupSettings != nil && !config.GetBridgeFeishuGroupSettingsEnabled()
		card := renderGroupStatusCardWithGroupWatch(ws, mode, groupWatchStatus)
		if paused {
			card = renderGroupSettingsPausedCard(chatID, "")
		}
		if err := b.notifyRootHelpCard(ctx, chatID, rootMsgID, card); err != nil {
			return err
		}
		if b.canUseBridge(openID) {
			chatName := ""
			lookup := b.chatNameLookup
			if lookup == nil {
				lookup = b.fetchChatName
			}
			if name, lookupErr := lookup(ctx, chatID); lookupErr == nil {
				chatName = name
			} else {
				log.Warn().Err(lookupErr).Str("chat_id", chatID).Msg("bridge: resolve group name for group watch management")
			}
			if b.dmInteractive != nil || b.client != nil {
				var err error
				switch {
				case paused:
					err = b.sendManagementCardToDM(ctx, openID, renderGroupSettingsPausedCard(chatID, chatName))
				case config.GetBridgeFeishuGroupSettingsEnabled():
					err = b.sendCurrentGroupSettingsCard(ctx, openID, chatID, chatName)
				default:
					state := managementState{ProfileName: b.ProfileName(), Mode: profileState.InteractionMode, DefaultAgent: profileState.DefaultAgent, GroupWatch: b.groupWatchSnapshot()}
					err = b.sendManagementCardToDM(ctx, openID, renderCurrentGroupWatchCard(state, chatID, chatName, b.groupWatchHasChat(chatID)))
				}
				if err != nil {
					log.Warn().Err(err).Str("chat_id", chatID).Msg("bridge: send current group management card")
				}
			}
		}
		return nil
	case command.CommandGroupWatchOn, command.CommandGroupWatchOff:
		if !mentioned {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, "请 @当前智能体 发送“开启群值守”或“关闭群值守”。")
		}
		return b.handleGroupWatchCommand(ctx, chatID, rootMsgID, openID, intent)
	}
	if err := b.ensureWorkspaceForChat(chatID); err != nil {
		return err
	}
	switch intent {
	case command.CommandHelp:
		allowed, err := b.canUseBotInGroup(chatID, openID)
		if err != nil {
			return err
		}
		if !allowed {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, memberBlockedInOwnerModeText())
		}
		if intent == command.CommandHelp {
			return b.notifyRootHelpCard(ctx, chatID, rootMsgID, renderWorkspaceHelpCard())
		}
		return b.notifyRootHelpCard(ctx, chatID, rootMsgID, renderWorkspaceHelpCard())
	case command.CommandCollaborationOn, command.CommandCollaborationOff:
		if !b.canUseBridge(openID) {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, memberBlockedInOwnerModeText())
		}
	default:
		return nil
	}
	switch intent {
	case command.CommandCollaborationOn:
		if err := b.updateGroupWorkspace(ctx, chatID, openID, func(s *GroupSettings) { s.Mode = WorkspaceModeShared }, func() error { return b.db.SetWorkspaceMode(b.currentBridgeId(), chatID, WorkspaceModeShared) }); err != nil {
			return err
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "已开启协作模式。群成员可以 @我发起任务。")
	case command.CommandCollaborationOff:
		if err := b.updateGroupWorkspace(ctx, chatID, openID, func(s *GroupSettings) { s.Mode = WorkspaceModeOwnerOnly }, func() error { return b.db.SetWorkspaceMode(b.currentBridgeId(), chatID, WorkspaceModeOwnerOnly) }); err != nil {
			return err
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "已关闭协作模式。现在只有负责人可以 @我发起任务。")
	default:
		return nil
	}
}
func (b *Bridge) handleGroupWatchCommand(ctx context.Context, chatID, rootMsgID, openID string, intent command.Command) error {
	status := b.groupWatchStatusForChat(chatID)
	if !b.canUseBridge(openID) {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("当前群值守：%s。只有负责人可以开启或关闭。", status))
	}
	cfg := b.groupWatchSnapshot().ForChat(chatID)
	joined := groupWatchConfigHasChat(cfg, chatID)
	operation, action := "add", "开启"
	if intent == command.CommandGroupWatchOff {
		operation, action = "remove", "关闭"
	}
	if operation == "add" {
		if joined && cfg.Enabled {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, "当前群已开启群值守。")
		}
		mode, err := b.interactionModeForChat(chatID)
		if err != nil {
			return err
		}
		if mode == config.FeishuInteractionSingleton {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, "群值守仅支持“每个话题独立”。请先私聊智能体，在“助手设置”中修改处理方式。")
		}
	} else if !joined {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "当前群未加入群值守，无需关闭。")
	}
	chatName := ""
	lookup := b.chatNameLookup
	if lookup == nil {
		lookup = b.fetchChatName
	}
	if name, err := lookup(ctx, chatID); err == nil {
		chatName = strings.TrimSpace(name)
	} else {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("bridge: resolve group name for group watch command")
	}
	card := renderGroupWatchCommandConfirmationCard(b.managementState(), chatID, chatName, operation)
	if err := b.sendManagementCardToDM(ctx, openID, card); err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Str("operation", operation).Msg("bridge: send group watch command confirmation")
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "暂时无法发送群值守确认卡，请稍后重试或私聊智能体发送 /amux。")
	}
	return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("已私聊负责人确认%s群值守。", action))
}
func (b *Bridge) handleNaturalWorkspaceDefaultAgent(ctx context.Context, chatID, rootMsgID, openID, agentRef string) error {
	if !b.ownerConfirmed() {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, ownerConfirmationRequiredText())
	}
	if !b.canUseBridge(openID) {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, memberBlockedInOwnerModeText())
	}
	settings, err := b.groupSettingsDraft(chatID)
	if err != nil {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "无法设置本群默认专家："+err.Error())
	}
	if strings.EqualFold(agentRef, "default") || strings.EqualFold(agentRef, "inherit") {
		agentRef = ""
	}
	settings.DefaultAgent = agentRef
	if _, err := b.applyGroupSettings(ctx, settings, settings.Version, openID); err != nil {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "无法设置本群默认专家："+err.Error())
	}
	return b.notifyRootCommand(ctx, chatID, rootMsgID, "已保存本群默认专家，仅影响之后新建的话题或会话。发送 /amux 可查看和修改完整群设置。")
}
func (b *Bridge) handleWorkspaceRootCommandReply(ctx context.Context, chatID, rootMsgID, openID, text string) error {
	if strings.TrimSpace(chatID) == "" {
		return nil
	}
	parts := strings.Fields(strings.TrimSpace(stripMention(text)))
	if len(parts) == 0 {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, workspaceCommandUsage())
	}
	parts[0] = strings.TrimPrefix(parts[0], "/")
	if strings.ToLower(parts[0]) != "workspace" {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, workspaceCommandUsage())
	}
	ownerID := ""
	if b != nil && b.secrets != nil {
		ownerID = strings.TrimSpace(b.secrets.OpenId)
	}
	if ownerID == "" {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "请先私聊智能体完成 owner 初始化，然后再在群里发送 /workspace init。")
	}
	if openID != ownerID {
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "你没有权限操作 Feishu workspace。")
	}
	if len(parts) == 2 && strings.ToLower(parts[1]) == "init" {
		if strings.TrimSpace(openID) == "" {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, "无法识别当前用户，请稍后重试。")
		}
		b.configureWorkspace(chatID)
		if err := b.saveProfileSecrets(); err == nil {
			log.Info().Str("chat_id", chatID).Str("open_id", b.secrets.OpenId).Msg("bridge: configured workspace from /workspace init")
		}
		if b.db != nil {
			if _, err := b.db.GetWorkspace(b.currentBridgeId(), chatID); err != nil {
				return err
			}
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, workspaceInitSuccessText(chatID))
	}
	if b.workspaceChatID() == "" {
		isWorkspace, err := b.isWorkspaceChat(chatID)
		if err != nil {
			return err
		}
		if !isWorkspace {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, "当前群尚未配置为 Feishu workspace。请先在群里发送 /workspace init。")
		}
	}
	isWorkspace, err := b.isWorkspaceChat(chatID)
	if err != nil {
		return err
	}
	if !isWorkspace {
		return nil
	}
	if len(parts) == 2 && (strings.ToLower(parts[1]) == "status" || strings.ToLower(parts[1]) == "defaults") {
		ws, err := b.db.GetWorkspace(b.currentBridgeId(), chatID)
		if err != nil {
			return err
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, workspaceStatusText(ws))
	}
	if len(parts) == 3 && strings.ToLower(parts[1]) == "mode" {
		mode := strings.ToLower(parts[2])
		if !validWorkspaceMode(mode) {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, "用法：/workspace mode owner_only|shared")
		}
		if err := b.updateGroupWorkspace(ctx, chatID, openID, func(s *GroupSettings) { s.Mode = mode }, func() error { return b.db.SetWorkspaceMode(b.currentBridgeId(), chatID, mode) }); err != nil {
			return err
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("已切换 Feishu workspace mode: %s", mode))
	}
	if len(parts) == 3 && strings.ToLower(parts[1]) == "interaction" {
		settings, err := b.groupSettingsDraft(chatID)
		if err != nil {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, err.Error())
		}
		settings.InteractionMode, settings.IsolationRequired = parts[2], false
		if parts[2] == "thread_isolated" {
			settings.InteractionMode, settings.IsolationRequired = config.FeishuInteractionThread, true
		}
		if _, err := b.applyGroupSettings(ctx, settings, settings.Version, openID); err != nil {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, "无法保存本群处理方式："+err.Error())
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, "已保存本群处理方式。已提交的任务继续完成；新话题或会话使用新设置。")
	}
	if len(parts) == 3 && strings.ToLower(parts[1]) == "default-agent" {
		return b.handleNaturalWorkspaceDefaultAgent(ctx, chatID, rootMsgID, openID, parts[2])
	}
	if len(parts) == 3 && strings.ToLower(parts[1]) == "default-target" {
		target := strings.TrimSpace(parts[2])
		if strings.EqualFold(target, "default") {
			target = ""
		} else if _, ok := b.resolveTarget(target); !ok {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("找不到唯一 target: %s", parts[2]))
		}
		if err := b.updateGroupWorkspace(ctx, chatID, openID, func(s *GroupSettings) { s.DefaultTarget = target }, func() error { return b.db.SetWorkspaceDefaultTarget(b.currentBridgeId(), chatID, target) }); err != nil {
			return err
		}
		label := target
		if label == "" {
			label = "(default)"
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("已设置 workspace default target: %s", label))
	}
	if len(parts) == 3 && strings.ToLower(parts[1]) == "default-cwd" {
		cwd := strings.TrimSpace(parts[2])
		if strings.EqualFold(cwd, "default") {
			cwd = ""
		}
		if err := b.updateGroupWorkspace(ctx, chatID, openID, func(s *GroupSettings) { s.DefaultCwd = cwd }, func() error { return b.db.SetWorkspaceDefaultCwd(b.currentBridgeId(), chatID, cwd) }); err != nil {
			return err
		}
		label := cwd
		if label == "" {
			label = "(default)"
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("已设置 workspace default cwd: %s", label))
	}
	if len(parts) == 3 && strings.ToLower(parts[1]) == "default-model" {
		model := strings.TrimSpace(parts[2])
		if strings.EqualFold(model, "default") {
			model = ""
		}
		if err := b.updateGroupWorkspace(ctx, chatID, openID, func(s *GroupSettings) { s.DefaultModelRef = model }, func() error { return b.db.SetWorkspaceDefaultModel(b.currentBridgeId(), chatID, model) }); err != nil {
			return err
		}
		label := model
		if label == "" {
			label = "(default)"
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("已设置 workspace default model: %s", label))
	}
	if len(parts) == 3 && strings.ToLower(parts[1]) == "idle-timeout" {
		seconds, ok := parseWorkspaceIdleTimeoutSeconds(parts[2])
		if !ok {
			return b.notifyRootCommand(ctx, chatID, rootMsgID, workspaceIdleTimeoutUsage())
		}
		if err := b.updateGroupWorkspace(ctx, chatID, openID, func(s *GroupSettings) { s.IdleTimeoutSeconds = seconds }, func() error { return b.db.SetWorkspaceIdleTimeoutSeconds(b.currentBridgeId(), chatID, seconds) }); err != nil {
			if notifyErr := b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("设置 workspace idle timeout 失败：%v", err)); notifyErr != nil {
				return fmt.Errorf("set workspace idle timeout: %w; notify: %v", err, notifyErr)
			}
			return err
		}
		label := fmt.Sprintf("%ds", seconds)
		if seconds == 0 {
			label = "disabled"
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("已设置 workspace idle timeout: %s", label))
	}
	if len(parts) >= 3 && strings.ToLower(parts[1]) == "default-args" {
		var args []string
		switch {
		case len(parts) == 3 && strings.EqualFold(parts[2], "clear"):
			args = nil
		case len(parts) >= 4 && parts[2] == "--":
			args = parts[3:]
		default:
			return b.notifyRootCommand(ctx, chatID, rootMsgID, "用法：/workspace default-args -- <agent-args...> 或 /workspace default-args clear")
		}
		if err := b.updateGroupWorkspace(ctx, chatID, openID, func(s *GroupSettings) { s.DefaultArgs = args }, func() error { return b.db.SetWorkspaceDefaultArgs(b.currentBridgeId(), chatID, args) }); err != nil {
			return err
		}
		label := strings.Join(args, " ")
		if label == "" {
			label = "(none)"
		}
		return b.notifyRootCommand(ctx, chatID, rootMsgID, fmt.Sprintf("已设置 workspace default args: %s", label))
	}
	return b.notifyRootCommand(ctx, chatID, rootMsgID, workspaceCommandUsage())
}
func (b *Bridge) notifyRootCommand(ctx context.Context, chatID, rootMsgID, text string) error {
	if strings.TrimSpace(rootMsgID) != "" {
		return b.notifyThread(ctx, rootMsgID, text)
	}
	_, _, err := b.notifyChat(ctx, chatID, text)
	return err
}
func workspaceCommandUsage() string {
	groupCommands := ""
	if config.GetBridgeFeishuGroupSettingsEnabled() {
		groupCommands = "、/workspace interaction singleton|thread|thread_isolated、/workspace default-agent <pack/profile|inherit>"
	}
	return "用法：/workspace init、/workspace status、/workspace defaults、/workspace mode owner_only|shared" + groupCommands + "、/workspace default-target <target|default>、/workspace default-cwd <dir|default>、/workspace default-model <model|default>、/workspace default-args -- <agent-args...>、/workspace default-args clear、/workspace idle-timeout <0|seconds>"
}
func workspaceIdleTimeoutUsage() string {
	return "用法：/workspace idle-timeout <0|seconds>，0 表示关闭；正数必须不少于 60 秒。"
}
func workspaceInitSuccessText(chatID string) string {
	return fmt.Sprintf("✅ 已将此群配置为 amux workspace\n"+"chat_id: %s\n"+"默认模式：owner_only\n\n"+"下一步：\n"+"1. 新建一个话题/thread，根消息可以写任务标题或背景，不需要命令。\n"+"2. 在这个 thread 的回复里发送 /run 创建新 session，或发送 /bind 接入或接管已有 session；同一 session 仍只有一个 active 话题。\n"+"3. 之后继续在同一 thread 里对话、查看状态或控制 session。\n\n"+"Workspace 管理命令：\n"+"/workspace status                   — 查看 workspace 状态\n"+"/workspace defaults                 — 查看 workspace 默认 session 配置\n"+"/workspace mode owner_only|shared   — 切换 workspace 模式\n"+"/workspace default-target <target|default> — 设置或清空默认 target\n"+"/workspace default-cwd <dir|default>       — 设置或清空默认 cwd\n"+"/workspace default-model <model|default>   — 设置或清空默认 model\n"+"/workspace default-args -- <args...>       — 设置默认 agent 参数\n"+"/workspace default-args clear              — 清空默认 agent 参数\n"+"/workspace idle-timeout <0|seconds>        — 设置自动 sleep 空闲时间，0 表示关闭\n"+"/help                               — 显示 workspace 帮助\n\n"+"%s", chatID, sessionEntryHelpText())
}
func parseWorkspaceIdleTimeoutSeconds(raw string) (int, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, false
	}
	switch raw {
	case "0", "off", "disabled", "default":
		return 0, true
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < session.MinIdleTimeoutSeconds {
		return 0, false
	}
	return seconds, true
}
func workspaceStatusText(ws *FeishuWorkspace) string {
	if ws == nil {
		return "Feishu workspace 未配置"
	}
	return fmt.Sprintf("Feishu workspace\n"+"Mode: %s\n"+"Default target: %s\n"+"Default cwd: %s\n"+"Default model: %s\n"+"Default args: %s\n"+"Max sessions total: %d\n"+"Max sessions per user: %d\n"+"Thread context limit: %d\n"+"Idle timeout seconds: %d", ws.Mode, workspaceDefaultValueText(ws.DefaultTarget), workspaceDefaultValueText(ws.DefaultCwd), workspaceDefaultValueText(ws.DefaultModel), workspaceDefaultArgsText(ws.DefaultArgs), ws.MaxSessionsTotal, ws.MaxSessionsPerUser, ws.ThreadContextLimit, ws.IdleTimeoutSeconds)
}
func workspaceDefaultValueText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(default)"
	}
	return value
}
func workspaceDefaultArgsText(args []string) string {
	if len(args) == 0 {
		return "(none)"
	}
	return strings.Join(args, " ")
}
