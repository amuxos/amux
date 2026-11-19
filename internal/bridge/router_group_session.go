package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// routeGroupSessionInput owns the group-level decision and lock. The ordinary
// topic router still owns Session commands, queueing and output attachment.
func (b *Bridge) routeGroupSessionInput(ctx context.Context, chatID, rootMsgID, msgID, openID, text string, mention botMentionInfo, opts routeInputOptions) (bool, error) {
	if opts.groupRouted || b == nil || b.db == nil || chatID == "" {
		return false, nil
	}
	unlock := b.lockRoute(groupSettingsRouteKey(b.currentBridgeId(), chatID))
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()
	settings, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID)
	if err != nil {
		return true, err
	}
	if settings == nil {
		return false, nil
	}
	if err := requireGroupSettingsExperiment(); err != nil {
		if opts.source == routeInputSourceGroupWatch {
			return true, nil
		}
		release()
		return true, b.notifyThread(ctx, rootMsgID, err.Error())
	}
	opts.groupRouted = true
	ctx = withGroupLaunchPolicy(ctx, settings, settings.IsolationRequired, false)
	forward := func(root string, context context.Context) error {
		return b.routeToSessionInChatWithMentionOptions(context, chatID, root, msgID, openID, text, mention, opts)
	}
	activeRoot, err := b.db.GetGroupSessionRoot(b.currentBridgeId(), chatID)
	if err != nil {
		return true, err
	}
	originRoot, err := b.db.FindGroupSessionOrigin(b.currentBridgeId(), chatID, rootMsgID)
	if err != nil {
		return true, err
	}
	if originRoot != "" && originRoot != activeRoot {
		if opts.source == routeInputSourceGroupWatch {
			return true, nil
		}
		release()
		return true, b.notifyThread(ctx, rootMsgID, "这段本群连续会话已结束接收新消息，已提交的任务会继续完成。请新发一条群消息或另开话题开始新的对话。")
	}
	if opts.source == routeInputSourceGroupWatch {
		if settings.InteractionMode != config.FeishuInteractionThread {
			return true, nil
		}
		release()
		return true, forward(rootMsgID, ctx)
	}
	allowed, err := b.canUseBotInGroup(chatID, openID)
	if err != nil {
		return true, err
	}
	if !allowed {
		release()
		return true, b.notifyThread(ctx, rootMsgID, memberBlockedByBotAccessText())
	}
	existing, err := b.db.FindByRootMsg(b.currentBridgeId(), rootMsgID)
	if err != nil {
		return true, err
	}
	if existing != nil && originRoot == "" {
		release()
		return true, forward(rootMsgID, ctx)
	}
	if settings.InteractionMode != config.FeishuInteractionSingleton {
		release()
		return true, forward(rootMsgID, ctx)
	}
	routedText := b.stripCurrentBotMention(text, mention)
	intent := freshThreadCommand(routedText)
	// An explicit launch/bind creates an ordinary topic using the selected
	// group launch defaults; it never replaces the current group conversation.
	if originRoot == "" && (intent == command.CommandRun || intent == command.CommandBind) {
		release()
		return true, forward(rootMsgID, ctx)
	}
	if activeRoot == "" {
		if !mention.Mentioned || intent != command.CommandNone {
			release()
			return true, forward(rootMsgID, ctx)
		}
		// Keep the group lock while initializing the first singleton conversation
		// so two concurrent mentions do not race to create separate group roots.
		return true, forward(rootMsgID, withGroupLaunchPolicy(ctx, settings, false, true))
	}
	if !mention.Mentioned && originRoot == "" {
		release()
		return true, forward(rootMsgID, ctx)
	}
	chat, err := b.db.FindByRootMsg(b.currentBridgeId(), activeRoot)
	if err != nil {
		return true, err
	}
	if chat == nil || chat.Status != ChatStatusActive {
		release()
		return true, b.notifyThread(ctx, rootMsgID, "本群连续会话已不可用。请负责人在“当前群设置”中选择“新开连续会话”。")
	}
	if err := b.db.RecordGroupSessionOrigin(b.currentBridgeId(), chatID, rootMsgID, activeRoot); err != nil {
		return true, err
	}
	// Release the group-level lock now that the origin mapping is persisted;
	// forward(activeRoot, ...) acquires the per-topic lock for activeRoot.
	release()
	// Replying to an input topic's root is an ordinary continuation. Normalize
	// that relation before replacing the routing root with the output anchor;
	// genuine quotes of child messages still use the normal quote resolver.
	ctx = withoutBoundTopicRootQuote(ctx, rootMsgID)
	ctx = context.WithValue(ctx, groupInputRootKey{}, rootMsgID)
	return true, forward(activeRoot, withGroupLaunchPolicy(ctx, settings, false, true))
}

type groupInputRootKey struct{}

func groupInputRoot(ctx context.Context, fallback string) string {
	if root, _ := ctx.Value(groupInputRootKey{}).(string); root != "" {
		return root
	}
	return fallback
}

// Group conversation anchors remain eligible for recovery beyond the ordinary
// topic window. Retiring an anchor rejects future input, but accepted work must
// still be able to finish and deliver its output after a Bridge restart.
func (b *Bridge) reattachGroupSessionStream(targetID string, session *pb.Session) bool {
	chat, err := b.db.FindByBridgeSession(b.currentBridgeId(), targetID, session.Name)
	if err != nil || chat == nil {
		return false
	}
	unlock := b.lockRoute(groupSettingsRouteKey(b.currentBridgeId(), chat.ChatID))
	defer unlock()
	root, err := b.db.FindGroupSessionOrigin(b.currentBridgeId(), chat.ChatID, chat.RootMsgID)
	if err != nil || root == "" || root != chat.RootMsgID {
		return false
	}
	// Re-read after acquiring the route lock; a concurrent bind may have moved
	// the Session while discovery was inspecting its previous attachment.
	current, err := b.db.FindByRootMsg(b.currentBridgeId(), root)
	if err != nil || current == nil || current.Status != ChatStatusActive || current.TargetID != targetID || current.SessionName != session.Name {
		return false
	}
	b.startStream(root, targetID, session.Name, current.AgentProfile, session.AgentType, session.EffectiveModel, current.OpenId)
	return true
}

func (b *Bridge) bindNewGroupConversation(ctx context.Context, chatID, rootMsgID string) error {
	policy, ok := ctx.Value(groupLaunchPolicyKey{}).(groupLaunchPolicy)
	if !ok || !policy.shared || policy.settings == nil {
		return nil
	}
	if err := b.db.SetGroupSessionRoot(b.currentBridgeId(), chatID, policy.settings.Version, rootMsgID); err != nil {
		return err
	}
	return b.db.RecordGroupSessionOrigin(b.currentBridgeId(), chatID, rootMsgID, rootMsgID)
}

func (b *Bridge) checkGroupBindingIsolation(ctx context.Context, chatID string, resolved resolvedBridgeSession) error {
	policy, err := b.newSessionPolicy(ctx, chatID)
	if err != nil {
		return err
	}
	if !policy.isolationRequired {
		return nil
	}
	s := b.boundSession(resolved.TargetID, resolved.SessionName)
	if s == nil || s.ExecutionIdentity == nil || s.ExecutionIdentity.IdentityMode != string(principal.IdentityModeIdentified) || strings.TrimSpace(s.ExecutionIdentity.EffectivePrincipalId) == "" || strings.TrimSpace(s.ExecutionIdentity.IsolationBackend) != principal.BackendLinuxDAC {
		return fmt.Errorf("本群要求用户隔离，不能绑定未确认隔离身份的会话")
	}
	return nil
}
