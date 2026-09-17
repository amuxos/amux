package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/config"
	"github.com/rs/zerolog/log"
)

func (b *Bridge) routeBotSessionInput(ctx context.Context, in botSessionInput) (bool, error) {
	if in.ReplyKind == botSessionReplyGroup && b != nil && b.db != nil {
		settings, err := b.db.ReadGroupSettings(b.currentBridgeId(), in.ChatID)
		if err != nil {
			return true, err
		}
		if settings != nil {
			return false, nil
		}
	}
	imageCtx := feishuImagesFromContext(ctx)
	imageCtx.chatID = in.ChatID
	imageCtx.openID = in.OpenID
	imageCtx.replyKind = in.ReplyKind
	ctx = withFeishuImages(ctx, imageCtx)
	mode, err := b.interactionMode()
	if err != nil {
		return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, "⚠️ Feishu interaction mode 配置错误："+err.Error())
	}
	if mode != config.FeishuInteractionSingleton {
		return false, nil
	}
	key := singletonStreamKey(b.currentBridgeId())
	unlock := b.lockRoute(key)
	defer unlock()
	if !b.ownerConfirmed() {
		return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, ownerConfirmationRequiredText())
	}
	if in.MessageID != "" && !b.markMessageSeen(b.currentBridgeId(), in.ChatID, key, in.MessageID) {
		log.Debug().Str("message_id", in.MessageID).Msg("bridge: duplicate singleton bot-session message ignored")
		return true, nil
	}
	if in.ReplyKind == botSessionReplyGroup {
		allowed, err := b.canUseBotInGroup(in.ChatID, in.OpenID)
		if err != nil {
			return true, err
		}
		if !allowed {
			return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, memberBlockedByBotAccessText())
		}
	}
	bs, err := b.db.GetBotSession(b.currentBridgeId())
	if err != nil {
		return true, err
	}
	if !botSessionReady(bs) {
		return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, botSessionUnboundText(b.profile))
	}
	if err := b.db.SetBotSessionLastReply(b.currentBridgeId(), in.ReplyKind, in.ChatID, in.OpenID, in.MessageID); err != nil {
		return true, err
	}
	chat := b.botSessionChat(bs, in)
	if alive, err := b.boundSessionAlive(ctx, chat); err == nil && !alive {
		b.StopStream(chat.RootMsgID)
		return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, botSessionReclaimedText(bs, b.profile))
	}
	b.startSingletonStream(bs, in.OpenID)
	routedText := strings.TrimSpace(in.Text)
	if handled, err := b.handleBotSessionCommand(ctx, bs, in, routedText); handled {
		return true, err
	}
	if _, _, skillCommand := parseSkillCommand(routedText); skillCommand {
		return true, b.handleBotSessionSkillCommand(ctx, bs, in, routedText)
	}
	routedText = b.expandAdvertisedAgentCommand(bs.TargetID, bs.SessionName, routedText)
	quoted, quotedPresent, err := b.resolveFeishuQuotedMessage(ctx)
	if err != nil {
		log.Warn().Err(err).Str("message_id", in.MessageID).Msg("bridge: resolve quoted Feishu message")
		return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, quotedMessageUnavailableText())
	}
	if routedText == "" && quotedPresent {
		routedText = defaultQuotedMessageRequest()
	}
	routedText, err = b.renderFeishuQuotedMessage(routedText, quoted, quotedPresent)
	if err != nil {
		log.Warn().Err(err).Str("message_id", in.MessageID).Msg("bridge: render quoted Feishu message")
		return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, quotedMessageUnavailableText())
	}
	if b.canAddReaction() && in.MessageID != "" {
		b.addReactionAsync(in.MessageID, "OneSecond")
	}
	if !hasFeishuMaterials(ctx) && b.shouldCoalesceInput(chat.RootMsgID) {
		b.enqueueCoalescedInputForMessage(chat, routedText, in.MessageID, in.OpenID, func(notifyCtx context.Context, err error) error {
			return b.notifyBotSession(notifyCtx, in.ReplyKind, in.ChatID, in.OpenID, botSessionSubmitFailureText(err))
		})
		return true, nil
	}
	if _, err := b.submitToSessionForMessages(ctx, chat, routedText, compactTurnInputRefs(in.OpenID, in.MessageID)); err != nil {
		return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, botSessionSubmitFailureText(err))
	}
	return true, nil
}

func (b *Bridge) handleBotSessionCommand(ctx context.Context, bs *BotSession, in botSessionInput, text string) (bool, error) {
	cmd := threadCommand(text)
	notify := func(text string) (bool, error) {
		return true, b.notifyBotSession(ctx, in.ReplyKind, in.ChatID, in.OpenID, text)
	}
	switch cmd.SingletonDisposition() {
	case command.SingletonControl:
		message, _, err := b.executeSessionControl(ctx, b.botSessionChat(bs, in), cmd)
		if err != nil {
			return notify("⚠️ " + err.Error())
		}
		return notify(message)
	case command.SingletonConflict:
		return notify("singleton 模式下请发送 /amux 管理当前连续对话。")
	default:
		return false, nil
	}
}

func isSingletonSessionCommand(text string) bool {
	switch threadCommand(text).SingletonDisposition() {
	case command.SingletonControl, command.SingletonSkill:
		return true
	default:
		return false
	}
}

func (b *Bridge) BindBotSession(bs BotSession, openID string) error {
	if b != nil && b.managementPackActions != nil {
		return b.managementPackActions.WithRegistryStable(func() error {
			return b.bindBotSessionRegistryStable(bs, openID)
		})
	}
	return b.bindBotSessionRegistryStable(bs, openID)
}
func (b *Bridge) bindBotSessionRegistryStable(bs BotSession, openID string) error {
	if b == nil || b.db == nil {
		return fmt.Errorf("bridge bot-session database is unavailable")
	}
	bridgeID := b.currentBridgeId()
	if strings.TrimSpace(bs.BridgeId) == "" {
		bs.BridgeId = bridgeID
	}
	if strings.TrimSpace(bs.BridgeId) != bridgeID {
		return fmt.Errorf("bot-session bridge_id %q does not match active bridge %q", bs.BridgeId, bridgeID)
	}
	if err := b.validateGroupWatchInteractionMode(config.FeishuInteractionSingleton); err != nil {
		return err
	}
	key := singletonStreamKey(bridgeID)
	unlock := b.lockRoute(key)
	defer unlock()
	existing, err := b.db.FindBySession(strings.TrimSpace(bs.TargetID), strings.TrimSpace(bs.SessionName))
	if err != nil {
		return err
	}
	state, err := b.currentBotProfileState()
	if err != nil {
		return err
	}
	defaultAgent := strings.TrimSpace(bs.AgentProfile)
	if defaultAgent == "" {
		defaultAgent = state.DefaultAgent
	}
	if defaultAgent != state.DefaultAgent {
		if _, err := resolveBridgeAgentLaunchWithSelection(defaultAgent, b.db.workspaceDefaults.Args,
			state.DefaultRuntimeRef, b.effectiveBotLaunchModel(state.DefaultRuntimeRef, state.DefaultModelRef),
			state.DefaultReasoningEffort); err != nil {
			return err
		}
	}
	if _, err := b.db.CommitSingletonBotSettings(bridgeID, state.Version, defaultAgent, bs); err != nil {
		return err
	}
	if existing != nil && existing.BridgeId == bridgeID {
		b.StopStream(existing.RootMsgID)
	}
	stored, err := b.db.GetBotSession(bridgeID)
	if err != nil {
		return err
	}
	if !botSessionReady(stored) {
		return fmt.Errorf("bot-session binding is not ready")
	}
	if strings.TrimSpace(openID) == "" {
		openID = strings.TrimSpace(stored.LastReplyOpenID)
	}
	b.startSingletonStream(stored, openID)
	state, err = b.db.GetBotProfileState(bridgeID, config.FeishuInteractionSingleton)
	if err == nil {
		b.projectLegacyBotSettings(state)
	}
	b.scheduleNativeSlashCommandSync()
	return nil
}

func (b *Bridge) DisableBotSession() error {
	if b == nil || b.db == nil {
		return fmt.Errorf("bridge bot-session database is unavailable")
	}
	key := singletonStreamKey(b.currentBridgeId())
	unlock := b.lockRoute(key)
	defer unlock()
	state, err := b.currentBotProfileState()
	if err != nil {
		return err
	}
	state, err = b.db.CommitThreadBotSettings(b.currentBridgeId(), state.Version, state.DefaultAgent)
	if err != nil {
		return err
	}
	b.StopStream(key)
	b.projectLegacyBotSettings(state)
	b.scheduleNativeSlashCommandSync()
	return nil
}
func (b *Bridge) interactionMode() (string, error) {
	if b != nil && b.db != nil {
		state, err := b.currentBotProfileState()
		if err != nil {
			return "", err
		}
		return state.InteractionMode, nil
	}
	return b.legacyInteractionMode()
}
func (b *Bridge) legacyInteractionMode() (string, error) {
	profile := DefaultProfileName
	if b != nil && strings.TrimSpace(b.profile) != "" {
		profile = b.profile
	}
	return config.GetBridgeFeishuInteractionMode(profile)
}
func botSessionReady(bs *BotSession) bool {
	return bs != nil && bs.Enabled && strings.TrimSpace(bs.TargetID) != "" && strings.TrimSpace(bs.SessionName) != ""
}
func (b *Bridge) botSessionChat(bs *BotSession, in botSessionInput) *LarkChat {
	if bs == nil {
		return nil
	}
	return &LarkChat{BridgeId: b.currentBridgeId(), ChatID: strings.TrimSpace(in.ChatID), RootMsgID: singletonStreamKey(b.currentBridgeId()), OpenId: strings.TrimSpace(in.OpenID), TargetID: strings.TrimSpace(bs.TargetID), TargetName: strings.TrimSpace(bs.TargetName), SessionName: strings.TrimSpace(bs.SessionName), AgentProfile: strings.TrimSpace(bs.AgentProfile), AgentType: strings.TrimSpace(bs.AgentType), Status: ChatStatusActive, localExpertAuthoring: b.isBotOwner(in.OpenID)}
}
func (b *Bridge) notifyBotSession(ctx context.Context, kind, chatID, openID, text string) error {
	switch kind {
	case botSessionReplyDM:
		_, _, err := b.notifyDM(ctx, openID, text)
		return err
	default:
		_, _, err := b.notifyChat(ctx, chatID, text)
		return err
	}
}
func botSessionUnboundText(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultProfileName
	}
	return "尚未绑定 bot-session。\n" + "请私聊智能体发送 /amux，在“助手”中重新配置；如需救援，可在机器上运行：\n" + fmt.Sprintf("amux bridge feishu bot-session bind --profile %s <target/session>", profile)
}
func botSessionReclaimedText(bs *BotSession, profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = DefaultProfileName
	}
	sessionRef := "<target/session>"
	if bs != nil {
		targetName := strings.TrimSpace(bs.TargetName)
		if targetName == "" {
			targetName = strings.TrimSpace(bs.TargetID)
		}
		if targetName != "" && strings.TrimSpace(bs.SessionName) != "" {
			sessionRef = targetName + "/" + strings.TrimSpace(bs.SessionName)
		}
	}
	return "当前 bot-session 绑定的 session 已结束或不可用：" + sessionRef + "\n" + "请私聊智能体发送 /amux，在“助手”中重新配置；如需救援，可在机器上运行：\n" + fmt.Sprintf("amux bridge feishu bot-session bind --profile %s <target/session>", profile)
}
func botSessionSubmitFailureText(err error) string {
	return "⚠️ 当前对话暂时不可用，消息未发送。\n请稍后重试；如持续失败，请私聊发送 /amux 查看状态。"
}
func (b *Bridge) canUseBridge(openID string) bool {
	if b == nil || b.secrets == nil || b.secrets.OpenId == "" {
		return true
	}
	return openID == b.secrets.OpenId
}
func (b *Bridge) isBotOwner(openID string) bool {
	return b != nil && b.secrets != nil && strings.TrimSpace(b.secrets.OpenId) != "" && strings.TrimSpace(openID) == strings.TrimSpace(b.secrets.OpenId)
}
func (b *Bridge) canUseBotInGroup(chatID, openID string) (bool, error) {
	if b.canUseBridge(openID) {
		return true, nil
	}
	if b == nil || b.db == nil {
		return false, nil
	}
	ws, err := b.currentWorkspace(chatID)
	if err != nil {
		return false, err
	}
	return ws != nil && ws.Mode == WorkspaceModeShared, nil
}

// BindBotSession persists and activates a singleton binding while serializing
// against incoming singleton messages and asynchronous restart reconciliation.
// It is also used by the hub RPC path so all live binding mutations share the
// same stream lifecycle boundary.
// DisableBotSession disables the persistent singleton binding and stops its
// live stream under the same route lock used by restart reconciliation.
