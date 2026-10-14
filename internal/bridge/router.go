package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/bridge/inputrouter"
	"github.com/amuxos/amux/internal/bridge/threadcontext"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// inputRouter owns per-root-message routing locks and in-memory dedupe.
type inputRouter struct {
	routeMu    sync.Mutex
	routeLocks map[string]*sync.Mutex
	seenMsgs   map[string]struct{}
}

func (b *Bridge) routeToSessionInChatWithMention(ctx context.Context, chatID, rootMsgID, msgID, openID, text string, mention botMentionInfo) error {
	return b.routeToSessionInChatWithMentionOptions(ctx, chatID, rootMsgID, msgID, openID, text, mention, routeInputOptions{})
}

type routeInputOptions struct {
	groupRouted              bool
	source                   routeInputSource
	executionActorOpenID     string
	alertDedup               *groupWatchAlertDedup
	agentProfileRef          string
	runtimeRef               string
	modelRef                 string
	reasoningEffort          string
	maxActiveSessionsPerChat int
	idleTimeoutSeconds       int
}

type routeInputSource uint8

const (
	routeInputSourceManual routeInputSource = iota
	routeInputSourceGroupWatch
)

func inputRouterWorkspaceMode(mode string) inputrouter.WorkspaceMode {
	switch mode {
	case WorkspaceModeOwnerOnly:
		return inputrouter.WorkspaceOwnerOnly
	case WorkspaceModeShared:
		return inputrouter.WorkspaceShared
	default:
		return inputrouter.WorkspaceUnknown
	}
}

func (b *Bridge) routeToSessionInChatWithMentionOptions(ctx context.Context, chatID, rootMsgID, msgID, openID, text string, mention botMentionInfo, opts routeInputOptions) error {
	if handled, err := b.routeGroupSessionInput(ctx, chatID, rootMsgID, msgID, openID, text, mention, opts); handled {
		return err
	}
	unlock := b.lockRoute(rootMsgID)
	isGroupWatch := opts.source == routeInputSourceGroupWatch
	notificationOpenID := strings.TrimSpace(openID)
	if isGroupWatch || mention.ExternalBot {
		notificationOpenID = ""
	}

	ws, err := b.currentWorkspace(chatID)
	if err != nil {
		unlock()
		return err
	}
	chat, err := b.db.FindByRootMsg(b.currentBridgeId(), rootMsgID)
	if err != nil {
		unlock()
		return err
	}
	if msgID != "" && !mention.ExternalBot && !b.markMessageSeen(b.currentBridgeId(), chatID, groupInputRoot(ctx, rootMsgID), msgID) {
		unlock()
		log.Debug().Str("message_id", msgID).Msg("bridge: duplicate message ignored")
		return nil
	}

	if isGroupWatch && opts.alertDedup != nil {
		accepted, err := b.db.GroupWatchAlertAccepted(*opts.alertDedup, time.Now())
		if err != nil || accepted {
			unlock()
			if err != nil {
				return err
			}
			return b.db.ObserveGroupWatchDuplicate(*opts.alertDedup, msgID, time.Now())
		}
	}

	if chat == nil {
		err = b.routeFreshTopicInput(ctx, chatID, rootMsgID, msgID, openID, text, mention, ws, opts)
		unlock()
		return err
	}
	chat.localExpertAuthoring = !isGroupWatch && b.isBotOwner(openID)

	routedText := text
	if mention.Mentioned && !mention.PreserveText {
		routedText = strings.TrimSpace(stripMentionTokens(text, mention.Tokens))
	}
	allowed := true
	if !isGroupWatch {
		allowed, err = b.canUseBotInGroup(chatID, openID)
		if err != nil {
			unlock()
			return err
		}
	}
	threadState := inputrouter.ThreadActive
	boundSessionAlive := true
	if chat.Status != ChatStatusActive {
		threadState = inputrouter.ThreadTerminal
	} else if alive, probeErr := b.boundSessionAlive(ctx, chat); probeErr == nil {
		boundSessionAlive = alive
	}
	advertisedAgentCommand := false
	if !isGroupWatch && allowed && threadState == inputrouter.ThreadActive && boundSessionAlive {
		_, advertisedAgentCommand = b.advertisedAgentCommandName(chat.TargetID, chat.SessionName, routedText)
		if advertisedAgentCommand {
			routedText = b.expandAdvertisedAgentCommand(chat.TargetID, chat.SessionName, routedText)
		}
	}
	outcome := inputrouter.Decide(inputrouter.Input{
		Text: routedText,
		Mention: inputrouter.Mention{
			AtBot: mention.Mentioned,
		},
		AgentCommand: advertisedAgentCommand,
		Automated:    isGroupWatch,
	}, inputrouter.Snapshot{
		ThreadState:       threadState,
		WorkspaceMode:     inputRouterWorkspaceMode(ws.Mode),
		CanControlBinding: b.canControlBinding(openID, chat),
		BoundSessionAlive: boundSessionAlive,
		CanUseBot:         allowed,
	})
	cmd := command.CommandNone
	switch outcome.Kind {
	case inputrouter.OutcomeDenied:
		unlock()
		if outcome.Reason == inputrouter.ReasonBindingControlDenied {
			return b.notifyThread(ctx, rootMsgID, "你没有权限操作此 amux session。")
		}
		return b.notifyThread(ctx, rootMsgID, memberBlockedByBotAccessText())
	case inputrouter.OutcomeTerminalNotice:
		if outcome.Reason == inputrouter.ReasonInactiveBinding {
			// Lazily terminate a binding whose session was archived or pruned.
			b.StopStream(rootMsgID)
			if err := b.db.TerminateChat(b.currentBridgeId(), rootMsgID); err != nil {
				unlock()
				return err
			}
		}
		unlock()
		return b.notifyThread(ctx, rootMsgID, terminalTopicText(chat))
	case inputrouter.OutcomeRecordHumanContext:
		b.recordHumanThreadContext(ctx, chatID, rootMsgID, msgID, openID, text)
		unlock()
		return nil
	case inputrouter.OutcomeCommand:
		cmd = outcome.Command
	case inputrouter.OutcomeSubmit:
		cmd = command.CommandNone
	default:
		unlock()
		return nil
	}

	log.Debug().Str("session", chat.SessionName).Str("text", routedText).Msg("bridge: routing user input to session")

	switch cmd {
	case command.CommandGoal:
		unlock()
		return b.handleGoalCommand(ctx, chat, msgID, openID, routedText)
	case command.CommandAmux:
		unlock()
		return b.notifyThreadHelpCard(ctx, rootMsgID, renderActiveTopicStatusCard(chat))
	case command.CommandHelp:
		unlock()
		return b.notifyThreadHelpCard(ctx, rootMsgID, b.boundTopicHelpCard(ctx, chat))
	case command.CommandSkill:
		unlock()
		return b.handleThreadSkillCommand(ctx, chat, msgID, notificationOpenID, routedText)
	case command.CommandStatus:
		message, _, controlErr := b.executeSessionControl(ctx, chat, cmd)
		unlock()
		if controlErr != nil {
			return b.notifyThread(ctx, rootMsgID, "⚠️ "+controlErr.Error())
		}
		return b.notifyThread(ctx, rootMsgID, message)
	case command.CommandUnbind:
		log.Info().Str("session", chat.SessionName).Msg("bridge: /unbind — detaching thread binding")
		b.StopStream(rootMsgID)
		err := b.db.TerminateChat(b.currentBridgeId(), rootMsgID)
		unlock()
		if err != nil {
			return err
		}
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("已解除当前话题绑定: %s/%s", chat.TargetName, chat.SessionName))
	case command.CommandStop:
		log.Info().Str("session", chat.SessionName).Msg("bridge: /stop — killing session")
		killCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := b.hub.Route(killCtx, chat.TargetID, "Kill", &pb.KillRequest{Name: chat.SessionName}); err != nil {
			unlock()
			return err
		}
		b.StopStream(rootMsgID)
		err := b.db.TerminateChat(b.currentBridgeId(), rootMsgID)
		unlock()
		if err != nil {
			return err
		}
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("已停止当前 session: %s/%s", chat.TargetName, chat.SessionName))
	case command.CommandInterrupt:
		log.Info().Str("session", chat.SessionName).Msg("bridge: /interrupt — interrupting session")
		message, _, controlErr := b.executeSessionControl(ctx, chat, cmd)
		if controlErr != nil {
			unlock()
			return b.notifyThread(ctx, rootMsgID, "⚠️ "+controlErr.Error())
		}
		unlock()
		return b.notifyThread(ctx, rootMsgID, message)
	case command.CommandRestart:
		log.Info().Str("session", chat.SessionName).Msg("bridge: /restart — restarting session")
		message, _, controlErr := b.executeSessionControl(ctx, chat, cmd)
		if controlErr != nil {
			unlock()
			return b.notifyThread(ctx, rootMsgID, "⚠️ "+controlErr.Error())
		}
		unlock()
		return b.notifyThread(ctx, rootMsgID, message)
	case command.CommandCompact, command.CommandClear:
		unlock()
		message, _, controlErr := b.executeSessionControl(ctx, chat, cmd)
		if controlErr != nil {
			return b.notifyThread(ctx, rootMsgID, "⚠️ "+controlErr.Error())
		}
		return b.notifyThread(ctx, rootMsgID, message)
	case command.CommandBind:
		err := b.stopCurrentSessionAndBindThread(ctx, chatID, rootMsgID, openID, routedText, chat)
		unlock()
		return err
	case command.CommandQueue:
		unlock()
		return b.notifyQueue(ctx, rootMsgID, chat)
	case command.CommandQueueClear:
		clearCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		resp, err := b.hub.Route(clearCtx, chat.TargetID, "QueueClear", &pb.QueueClearRequest{Name: chat.SessionName})
		unlock()
		if err != nil {
			return b.notifyThread(ctx, rootMsgID, "⚠️ 清空队列失败："+err.Error())
		}
		var out pb.QueueClearResponse
		_ = json.Unmarshal(resp.Payload, &out)
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("🧹 已清空 %d 条 pending 输入", out.Cleared))
	case command.CommandQueueCancel:
		id := queueCancelID(routedText)
		if id == "" {
			unlock()
			return b.notifyThread(ctx, rootMsgID, "用法：/queue cancel <queue-id>")
		}
		cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := b.hub.Route(cancelCtx, chat.TargetID, "QueueCancel", &pb.QueueCancelRequest{Name: chat.SessionName, Id: id})
		unlock()
		if err != nil {
			return b.notifyThread(ctx, rootMsgID, "⚠️ 取消 pending 输入失败："+err.Error())
		}
		return b.notifyThread(ctx, rootMsgID, "已取消 pending 输入 "+id)
	case command.CommandRun, command.CommandWorkspace:
		unlock()
		return b.notifyThread(ctx, rootMsgID, boundTopicConflictText(chat))
	case command.CommandUnsupported:
		unlock()
		return b.notifyThreadHelpCard(ctx, rootMsgID, b.boundTopicHelpCard(ctx, chat))
	default:
		if cmd != command.CommandNone {
			unlock()
			return b.notifyThreadHelpCard(ctx, rootMsgID, b.boundTopicHelpCard(ctx, chat))
		}
	}

	input := routedText
	var contextPackage *threadcontext.ContextPackage
	if !mention.AmuxDelegation {
		// Feishu repeats parent_id == root_id on ordinary messages inside a topic.
		// Once this topic is bound, that relation expresses thread membership rather
		// than a fresh user-selected quote. Replies to a specific child message keep
		// their distinct parent_id and still pass through unchanged.
		ctx = withoutBoundTopicRootQuote(ctx, chat.RootMsgID)
		quoted, quotedPresent, err := b.resolveFeishuQuotedMessage(ctx)
		if err != nil {
			log.Warn().Err(err).Str("message_id", msgID).Msg("bridge: resolve quoted Feishu message")
			unlock()
			return b.notifyThread(ctx, rootMsgID, quotedMessageUnavailableText())
		}
		if routedText == "" && quotedPresent {
			routedText = defaultQuotedMessageRequest()
			input = routedText
		}
		topicRootDelivered := b.topicRootPromptDelivered(chat)
		quoteSuppliesRoot := quotedPresent && b.topicRootPromptEnabled() && !topicRootDelivered &&
			feishuQuotedMessageFromContext(ctx).messageID == chat.RootMsgID
		rootText := ""
		if b.topicRootPromptEnabled() && !topicRootDelivered && !quoteSuppliesRoot {
			rootText = b.rootThreadRequestText(ctx, chat.ChatID, chat.RootMsgID)
		}
		input, contextPackage = b.withThreadContext(ctx, chat, msgID, routedText, ws)
		if rootText != "" {
			input, chat.topicRootPromptPending = b.ensureTopicRootInInput(rootText, routedText, input)
		}
		if quoteSuppliesRoot {
			chat.topicRootPromptPending = true
		}
		input, err = b.renderFeishuQuotedMessage(input, quoted, quotedPresent)
		if err != nil {
			log.Warn().Err(err).Str("message_id", msgID).Msg("bridge: render quoted Feishu message")
			unlock()
			return b.notifyThread(ctx, rootMsgID, quotedMessageUnavailableText())
		}
	}
	if msgID != "" && b.canAddReaction() {
		b.addReactionAsync(msgID, "OneSecond")
	}
	b.recordUserRequestContext(ctx, chat, msgID, openID, routedText)
	input, err = b.withBotHandoffInstructions(chat.RootMsgID, msgID, openID, input)
	if err != nil {
		unlock()
		return err
	}
	b.recordThreadContextDecision(ctx, contextPackage, input)
	if !hasFeishuMaterials(ctx) && b.shouldCoalesceInput(chat.RootMsgID) {
		b.enqueueCoalescedInputForMessage(chat, input, msgID, notificationOpenID, nil)
		unlock()
		return nil
	}
	// Restore the output stream before submitting. After a hub/runtime restart
	// while this thread was idle past autoReattachRecentBindingWindow,
	// OnSessionAdded skipped the automatic reattach; without re-establishing the
	// stream here the agent would run with no bridge consumer forwarding its
	// output back to Feishu. No-op when a stream is already attached, so an
	// in-flight turn is never disrupted.
	b.ensureThreadStream(chat)
	// Mark the turn active before releasing the route lock so a concurrently
	// arriving message coalesces instead of issuing a second preempt.
	b.setTurnActive(chat.RootMsgID, true)
	if chat.topicRootPromptPending {
		// Keep the per-topic route lock through the first root-bearing Submit.
		// Otherwise a concurrent input can observe the persisted delivery flag
		// as false and enqueue a second copy of the root before this RPC returns.
		_, err := b.submitToSessionForMessages(ctx, chat, input, compactTurnInputRefs(notificationOpenID, msgID))
		if err != nil {
			b.setTurnActive(chat.RootMsgID, false)
		}
		unlock()
		return err
	}
	unlock()
	if _, err := b.submitToSessionForMessages(ctx, chat, input, compactTurnInputRefs(notificationOpenID, msgID)); err != nil {
		b.setTurnActive(chat.RootMsgID, false)
		return err
	}
	return nil
}

func (b *Bridge) routeFreshTopicInput(ctx context.Context, chatID, rootMsgID, msgID, openID, text string, mention botMentionInfo, ws *FeishuWorkspace, opts routeInputOptions) error {
	isGroupWatch := opts.source == routeInputSourceGroupWatch
	routedText := text
	if !isGroupWatch {
		routedText = strings.TrimSpace(text)
	}
	if !isGroupWatch && mention.Mentioned {
		routedText = b.stripCurrentBotMention(text, mention)
	}
	allowed := true
	if !isGroupWatch {
		var err error
		allowed, err = b.canUseBotInGroup(chatID, openID)
		if err != nil {
			return err
		}
	}
	outcome := inputrouter.Decide(inputrouter.Input{
		Text:      routedText,
		Automated: isGroupWatch,
		Mention: inputrouter.Mention{
			AtBot: mention.Mentioned,
		},
	}, inputrouter.Snapshot{
		ThreadState:   inputrouter.ThreadFresh,
		WorkspaceMode: inputRouterWorkspaceMode(ws.Mode),
		CanUseBot:     allowed,
	})
	switch outcome.Kind {
	case inputrouter.OutcomeAmux, inputrouter.OutcomeHelp:
		return b.notifyThreadHelpCard(ctx, rootMsgID, renderSessionEntryHelpCard())
	case inputrouter.OutcomeBind:
		return b.bindThread(ctx, chatID, rootMsgID, openID, routedText)
	case inputrouter.OutcomeRun:
		if isGroupWatch {
			return b.runAndBindNaturalEntryWithOptions(ctx, chatID, rootMsgID, msgID, openID, text, mention, naturalEntryOptions{
				source:                   routeInputSourceGroupWatch,
				executionActorOpenID:     opts.executionActorOpenID,
				alertDedup:               opts.alertDedup,
				agentProfileRef:          opts.agentProfileRef,
				runtimeRef:               opts.runtimeRef,
				modelRef:                 opts.modelRef,
				reasoningEffort:          opts.reasoningEffort,
				maxActiveSessionsPerChat: opts.maxActiveSessionsPerChat,
				idleTimeoutSeconds:       opts.idleTimeoutSeconds,
			})
		}
		if outcome.Command == command.CommandRun {
			return b.runAndBindThread(ctx, chatID, rootMsgID, openID, routedText)
		}
		return b.runAndBindNaturalEntry(ctx, chatID, rootMsgID, msgID, openID, routedText, mention)
	case inputrouter.OutcomeCommand:
		if outcome.Command == command.CommandSkill {
			return b.notifyThread(ctx, rootMsgID,
				"当前话题尚未绑定 Session，无法列出 Skill。请先发送 /run codex -n <name> 创建任务，也可发送 /bind <target/session> 接入已有 Session；绑定成功后在该话题发送 /skill。")
		}
		return nil
	case inputrouter.OutcomeDenied:
		return b.notifyThread(ctx, rootMsgID, memberBlockedByBotAccessText())
	case inputrouter.OutcomeNoop:
		return nil
	default:
		return nil
	}
}

func (b *Bridge) markMessageSeen(bridgeID, chatID, rootMsgID, msgID string) bool {
	if b != nil && b.db != nil && strings.TrimSpace(bridgeID) != "" {
		seen, err := b.db.MarkMessageSeen(bridgeID, msgID, rootMsgID, chatID)
		if err == nil {
			return seen
		}
		log.Warn().Err(err).Str("message_id", msgID).Msg("bridge: persistent message dedupe failed; falling back to memory")
	}
	b.routeMu.Lock()
	defer b.routeMu.Unlock()
	if b.seenMsgs == nil {
		b.seenMsgs = make(map[string]struct{})
	}
	if _, ok := b.seenMsgs[msgID]; ok {
		return false
	}
	b.seenMsgs[msgID] = struct{}{}
	return true
}

func threadCommand(text string) command.Command {
	cmdText, ok := slashCommandText(text)
	if !ok {
		return command.CommandNone
	}
	return command.Classify(command.ScopeActiveTopic, "/"+cmdText)
}

func freshThreadCommand(text string) command.Command {
	return command.Classify(command.ScopeFreshTopic, text)
}

func bindSessionRef(text string) string {
	text, ok := slashCommandText(text)
	if !ok {
		return ""
	}
	parts := strings.Fields(text)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bind" {
		return ""
	}
	return parts[1]
}

func queueCancelID(text string) string {
	text, ok := slashCommandText(text)
	if !ok {
		return ""
	}
	parts := strings.Fields(text)
	if len(parts) != 3 || strings.ToLower(parts[0]) != "queue" || strings.ToLower(parts[1]) != "cancel" {
		return ""
	}
	return parts[2]
}

func sessionEntryHelpText() string {
	return ordinarySessionEntryHelpText()
}

func boundTopicScopeText(chat *LarkChat) string {
	binding := "当前话题已经绑定到一个 amux session。"
	if chat != nil {
		binding = fmt.Sprintf("当前话题已经绑定到一个 amux session：%s/%s。", chat.TargetName, chat.SessionName)
	}
	return binding + "\n" +
		"当前话题是 session 话题，只用于和这个 session 对话、查看状态、队列和控制生命周期。\n" +
		"不能再使用 /run 创建新 session；需要切换到另一个已有 session 时，可以发送 /bind <target/session>，当前 session 会先自动停止。\n" +
		"workspace 管理命令（例如 /workspace status、/workspace mode）请作为管理话题根消息发送。\n" +
		"发送 /help 可以查看当前 session 话题支持的命令。"
}

func boundTopicConflictText(chat *LarkChat) string {
	return bindingStatusText(chat) + "\n\n" + boundTopicScopeText(chat)
}

func terminalTopicText(chat *LarkChat) string {
	if chat == nil {
		return "当前话题已结束。请在新的 root 消息或 fresh 话题中使用 /run 创建任务，或使用 /bind 接管已有 session。"
	}
	return fmt.Sprintf("当前话题已结束，只读保留历史记录: %s/%s。请在新的 root 消息或 fresh 话题中使用 /run 创建任务，或使用 /bind 接管已有 session。", chat.TargetName, chat.SessionName)
}

func bindingStatusText(chat *LarkChat) string {
	return taskBindingStatusText(chat)
}

type botSessionInput struct {
	ChatID    string
	MessageID string
	OpenID    string
	Text      string
	ReplyKind string
}

// BindBotSession persists and activates a singleton binding while serializing
// against incoming singleton messages and asynchronous restart reconciliation.
// It is also used by the hub RPC path so all live binding mutations share the
// same stream lifecycle boundary.

// Binding a session as singleton detaches its active topic binding in the DB.
// Capture it first so an already-running thread stream is stopped as well.

// DisableBotSession disables the persistent singleton binding and stops its
// live stream under the same route lock used by restart reconciliation.

type bindThreadOptions struct {
	Action       string
	NoticeText   string
	DisplayTitle string
	SkipNotice   bool
	EntrySource  string
}

type topicRunRequest struct {
	AgentRef  string
	Name      string
	Target    string
	Cwd       string
	Model     string
	ExtraArgs []string
}

// runFeishuSession delegates ordinary session creation to the shared starter.
// Feishu-specific deadline and failure presentation remain at this boundary.

// 每个话题独立(用户隔离) is a hard requirement: when set, the session must
// carry an identified execution identity. Refusing here (rather than
// falling back to legacy-host) keeps the isolation promise real — a host
// whose identity resolver came back empty either never enabled isolation or
// has since degraded, so the owner should switch back to 每个话题独立.

// Launch warnings mean the session came up but something will bite the user
// (an expired provider credential makes every turn answer nothing). Put them
// in the thread rather than only in runtime.log.

type naturalEntryOptions struct {
	source                   routeInputSource
	executionActorOpenID     string
	alertDedup               *groupWatchAlertDedup
	agentProfileRef          string
	runtimeRef               string
	modelRef                 string
	reasoningEffort          string
	maxActiveSessionsPerChat int
	idleTimeoutSeconds       int
}

// Mark the authenticated Feishu turn before the RPC can emit provider
// events. Topic-scoped state survives asynchronous Attach replacement.

// boundSessionAlive reports whether the session bound to chat still exists and is
// not archived on its target. A reclaimed session (archived or pruned/absent)
// returns false. On a probe RPC/parse failure it returns a non-nil error so the
// caller leaves the binding untouched (avoids terminating on transient hub errors).

// not found => pruned

type resolvedBridgeSession struct {
	TargetID       string
	TargetName     string
	SessionName    string
	AgentProfile   string
	AgentType      string
	EffectiveModel string
}

type resolvedBridgeTarget struct {
	ID   string
	Name string
}

// Removed subcommands intentionally do not fall through to
// help because their capabilities now live in the card.
