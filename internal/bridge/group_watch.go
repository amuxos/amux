package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// groupWatchState owns group-watch configuration and change notification.
type groupWatchState struct {
	groupWatchMu            sync.RWMutex
	groupWatchSettingsMu    sync.Mutex
	groupWatchConfig        config.BridgeFeishuGroupWatchConfig
	groupWatchConfigChanged chan struct{}
}

const maxGroupWatchPollPages = 4

type groupWatchMessage struct {
	ChatID     string
	MessageID  string
	RootID     string
	SenderType string
	SenderID   string
	Text       string
	Kind       string
	Content    *string
}

func (b *Bridge) groupWatchConfigured() bool {
	if b == nil {
		return false
	}
	cfg := b.groupWatchSnapshot()
	return cfg.Enabled && len(cfg.ChatIDs) > 0
}

func (c *groupWatchState) groupWatchSnapshot() config.BridgeFeishuGroupWatchConfig {
	if c == nil {
		return config.BridgeFeishuGroupWatchConfig{}
	}
	c.groupWatchMu.RLock()
	defer c.groupWatchMu.RUnlock()
	cfg := c.groupWatchConfig
	cfg.ChatIDs = append([]string(nil), cfg.ChatIDs...)
	cfg.SenderTypes = append([]string(nil), cfg.SenderTypes...)
	return cfg
}

func (c *groupWatchState) setGroupWatchConfig(cfg config.BridgeFeishuGroupWatchConfig) {
	if c == nil {
		return
	}
	cfg.ChatIDs = append([]string(nil), cfg.ChatIDs...)
	cfg.SenderTypes = append([]string(nil), cfg.SenderTypes...)
	c.groupWatchMu.Lock()
	c.groupWatchConfig = cfg
	if c.groupWatchConfigChanged == nil {
		c.groupWatchConfigChanged = make(chan struct{}, 1)
	}
	changed := c.groupWatchConfigChanged
	c.groupWatchMu.Unlock()
	select {
	case changed <- struct{}{}:
	default:
	}
}

// applyGroupWatchExecutionSettings persists group-watch execution settings and
// refreshes the in-memory snapshot. It owns groupWatchSettingsMu so that the
// management settings surface never locks group-watch state directly.
func (b *Bridge) applyGroupWatchExecutionSettings(agentRef, runtimeRef, modelRef, reasoningEffort, thinkingProcess string) error {
	b.groupWatchSettingsMu.Lock()
	defer b.groupWatchSettingsMu.Unlock()
	if err := config.SetBridgeFeishuGroupWatchExecution(b.ProfileName(), agentRef, runtimeRef, modelRef, reasoningEffort, thinkingProcess); err != nil {
		return err
	}
	b.setGroupWatchConfig(config.GetBridgeFeishuGroupWatchForProfile(b.ProfileName()))
	return nil
}

func (b *Bridge) groupWatchConfigChanges() <-chan struct{} {
	b.groupWatchMu.Lock()
	defer b.groupWatchMu.Unlock()
	if b.groupWatchConfigChanged == nil {
		b.groupWatchConfigChanged = make(chan struct{}, 1)
	}
	return b.groupWatchConfigChanged
}

func (b *Bridge) groupWatchHasChat(chatID string) bool {
	return groupWatchConfigHasChat(b.groupWatchSnapshot(), chatID)
}

func groupWatchConfigHasChat(cfg config.BridgeFeishuGroupWatchConfig, chatID string) bool {
	chatID = strings.TrimSpace(chatID)
	for _, configured := range cfg.ChatIDs {
		if chatID != "" && chatID == strings.TrimSpace(configured) {
			return true
		}
	}
	return false
}

func (b *Bridge) groupWatchStatusForChat(chatID string) string {
	if !b.groupWatchHasChat(chatID) {
		return "未加入"
	}
	if !b.groupWatchSnapshot().Enabled {
		return "已暂停"
	}
	return "已开启"
}

func (b *Bridge) updateGroupWatchForProfile(operation, chatID string) (config.BridgeFeishuGroupWatchConfig, error) {
	if b == nil {
		return config.BridgeFeishuGroupWatchConfig{}, fmt.Errorf("nil bridge")
	}
	b.groupWatchSettingsMu.Lock()
	defer b.groupWatchSettingsMu.Unlock()

	operation = strings.ToLower(strings.TrimSpace(operation))
	chatID = strings.TrimSpace(chatID)
	next := b.groupWatchSnapshot()
	if len(next.SenderTypes) == 0 {
		next.SenderTypes = []string{"user", "app"}
	}
	if next.PollIntervalSeconds == 0 {
		next.PollIntervalSeconds = 15
	}
	if next.LookbackSeconds == 0 {
		next.LookbackSeconds = 60
	}
	switch operation {
	case "add":
		if chatID == "" {
			return next, fmt.Errorf("chat_id is required")
		}
		if len(next.ChatIDs) == 0 {
			next.SenderTypes = []string{"user", "app"}
			next.PollIntervalSeconds = 15
			next.LookbackSeconds = 60
		}
		next.ChatIDs = appendUniqueGroupWatchChat(next.ChatIDs, chatID)
		next.Enabled = true
	case "remove":
		if chatID == "" {
			return next, fmt.Errorf("chat_id is required")
		}
		next.ChatIDs = removeGroupWatchChat(next.ChatIDs, chatID)
		if len(next.ChatIDs) == 0 {
			next.Enabled = false
		}
	case "pause":
		next.Enabled = false
	case "resume":
		if len(next.ChatIDs) == 0 {
			return next, fmt.Errorf("请先在目标群发送 /amux，将群加入值守")
		}
		next.Enabled = true
	default:
		return next, fmt.Errorf("unsupported group watch operation %q", operation)
	}
	if operation == "add" || operation == "resume" {
		mode, err := b.interactionMode()
		if err != nil {
			return next, err
		}
		if err := config.ValidateBridgeFeishuGroupWatchInteraction(next, mode); err != nil {
			return next, err
		}
	}
	if err := config.SetBridgeFeishuGroupWatch(b.ProfileName(), next); err != nil {
		return next, fmt.Errorf("persist group watch setting: %w", err)
	}
	effective := config.GetBridgeFeishuGroupWatchForProfile(b.ProfileName())
	b.setGroupWatchConfig(effective)
	return effective, nil
}

func appendUniqueGroupWatchChat(chatIDs []string, chatID string) []string {
	chatID = strings.TrimSpace(chatID)
	out := make([]string, 0, len(chatIDs)+1)
	seen := make(map[string]bool, len(chatIDs)+1)
	for _, value := range append(append([]string(nil), chatIDs...), chatID) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func removeGroupWatchChat(chatIDs []string, chatID string) []string {
	chatID = strings.TrimSpace(chatID)
	out := make([]string, 0, len(chatIDs))
	for _, value := range chatIDs {
		value = strings.TrimSpace(value)
		if value != "" && value != chatID {
			out = append(out, value)
		}
	}
	return out
}

func groupWatchOperationSuccessText(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "add":
		return "已将当前群加入值守"
	case "remove":
		return "已将当前群移出值守"
	case "pause":
		return "已暂停群值守"
	case "resume":
		return "已恢复群值守"
	default:
		return "群值守设置已保存"
	}
}

func (b *Bridge) groupWatchActive() bool {
	if !b.groupWatchConfigured() {
		return false
	}
	mode, err := b.interactionMode()
	return err == nil && mode == config.FeishuInteractionThread
}

func (b *Bridge) validateGroupWatchInteractionMode(mode string) error {
	return config.ValidateBridgeFeishuGroupWatchInteraction(b.groupWatchSnapshot(), mode)
}

func (b *Bridge) handleGroupWatchEvent(ctx context.Context, event *larkim.P2MessageReceiveV1) (bool, error) {
	if !b.groupWatchActive() || event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil {
		return false, nil
	}
	msg := event.Event.Message
	chatID := strings.TrimSpace(stringPtr(msg.ChatId))
	if !b.groupWatchHasChat(chatID) {
		return false, nil
	}
	text := groupWatchInputText(stringPtr(msg.MessageId), stringPtr(msg.MessageType), msg.Content)
	if text == "" || strings.HasPrefix(text, "/") {
		return false, nil
	}
	if isGroupWatchManagementCommand(text) {
		return false, nil
	}
	if b.messageBotMentionInfo(msg).Mentioned {
		return false, nil
	}
	senderType := normalizeGroupWatchSenderType(stringPtr(event.Event.Sender.SenderType))
	// An app-authored root with any structured mention is an explicit directed
	// message (including Coordination outbound). It belongs to the mentioned
	// Bot's normal/Bot-to-Bot router, never to unrelated group watchers.
	if senderType == "app" && len(msg.Mentions) > 0 {
		return false, nil
	}
	senderID := ""
	if event.Event.Sender.SenderId != nil {
		senderID = firstNonEmpty(
			stringPtr(event.Event.Sender.SenderId.OpenId),
			stringPtr(event.Event.Sender.SenderId.UserId),
			stringPtr(event.Event.Sender.SenderId.UnionId),
		)
	}
	if b.isOwnGroupWatchSender(senderType, senderID) {
		return true, nil
	}
	if strings.TrimSpace(senderID) == "" {
		log.Warn().Str("message_id", stringPtr(msg.MessageId)).
			Msg("bridge: group_watch message ignored because sender identity is missing")
		return true, nil
	}
	rootID := strings.TrimSpace(messageRootID(msg))
	if rootID != "" {
		if senderType == "app" {
			return true, nil
		}
		return false, nil
	}
	if !b.groupWatchSenderAllowed(senderType) {
		return false, nil
	}
	return true, b.routeGroupWatchMessage(ctx, groupWatchMessage{
		ChatID:     chatID,
		MessageID:  strings.TrimSpace(stringPtr(msg.MessageId)),
		RootID:     rootID,
		SenderType: senderType,
		SenderID:   senderID,
		Text:       text,
		Kind:       stringPtr(msg.MessageType),
		Content:    msg.Content,
	})
}

func (b *Bridge) routeGroupWatchMessage(ctx context.Context, msg groupWatchMessage) error {
	msg.ChatID = strings.TrimSpace(msg.ChatID)
	msg.MessageID = strings.TrimSpace(msg.MessageID)
	msg.RootID = strings.TrimSpace(msg.RootID)
	msg.SenderID = strings.TrimSpace(msg.SenderID)
	msg.Text = strings.TrimSpace(msg.Text)
	if msg.Text == "" {
		msg.Text = groupWatchInputText(msg.MessageID, msg.Kind, msg.Content)
	}
	if msg.ChatID == "" || msg.MessageID == "" || msg.Text == "" {
		return nil
	}
	if isGroupWatchManagementCommand(msg.Text) {
		return nil
	}
	if msg.RootID == "" {
		msg.RootID = msg.MessageID
	}
	if msg.SenderID == "" {
		log.Warn().Str("message_id", msg.MessageID).
			Msg("bridge: group_watch message ignored because sender identity is missing")
		return nil
	}
	b.groupWatchMu.RLock()
	defer b.groupWatchMu.RUnlock()
	cfg := b.groupWatchConfig
	if !cfg.Enabled || !groupWatchConfigHasChat(cfg, msg.ChatID) || !groupWatchConfigAllowsSender(cfg, msg.SenderType) {
		return nil
	}
	mode, err := b.interactionMode()
	if err != nil {
		return err
	}
	if mode != config.FeishuInteractionThread {
		return nil
	}
	// Polling reuses its parent context across messages. Each routed input owns
	// a fresh collector, including its resource budget and expected identity.
	ctx = context.WithValue(ctx, materialContextKey{}, &materialCollection{seen: map[string]bool{}})
	ctx = withFeishuImages(ctx, feishuImageContext{
		chatID: msg.ChatID, openID: msg.SenderID,
	})
	collectMessageInput(ctx, msg.MessageID, msg.Kind, msg.Content)
	return b.routeToSessionInChatWithMentionOptions(
		ctx,
		msg.ChatID,
		msg.RootID,
		msg.MessageID,
		msg.SenderID,
		msg.Text,
		botMentionInfo{PreserveText: true},
		routeInputOptions{
			source:                   routeInputSourceGroupWatch,
			agentProfileRef:          cfg.AgentProfileRef,
			runtimeRef:               cfg.RuntimeRef,
			modelRef:                 cfg.ModelRef,
			reasoningEffort:          cfg.ReasoningEffort,
			maxActiveSessionsPerChat: cfg.MaxActiveSessionsPerChat,
			idleTimeoutSeconds:       cfg.IdleTimeoutSeconds,
		},
	)
}

func (b *Bridge) enforceGroupWatchActiveSessionLimit(ctx context.Context, rootMsgID, chatID string, limit int) (bool, error) {
	if limit < 1 {
		limit = config.DefaultFeishuGroupWatchMaxActiveSessionsPerChat
	}
	bindings, err := b.db.ListGroupWatchCapacityBindingsByChatID(b.currentBridgeId(), chatID)
	if err != nil {
		return false, err
	}
	sessionsByTarget := make(map[string]map[string]string)
	countedSessions := make(map[string]struct{})
	active := 0
	for _, binding := range bindings {
		states := sessionsByTarget[binding.TargetID]
		if states == nil {
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			resp, routeErr := b.hub.Route(probeCtx, binding.TargetID, "List", &pb.ListRequest{})
			cancel()
			if routeErr != nil {
				log.Warn().Err(routeErr).Str("chat_id", chatID).Str("target_id", binding.TargetID).
					Msg("bridge: group_watch capacity probe failed")
				return true, b.notifyThread(ctx, rootMsgID, "群值守暂时无法确认 Session 容量，已停止自动创建；请稍后重试。")
			}
			var list pb.ListResponse
			if err := json.Unmarshal(resp.Payload, &list); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Str("target_id", binding.TargetID).
					Msg("bridge: group_watch capacity response invalid")
				return true, b.notifyThread(ctx, rootMsgID, "群值守暂时无法确认 Session 容量，已停止自动创建；请稍后重试。")
			}
			states = make(map[string]string, len(list.Sessions))
			for _, item := range list.Sessions {
				if item != nil {
					states[item.Name] = item.State
				}
			}
			sessionsByTarget[binding.TargetID] = states
		}
		state, found := states[binding.SessionName]
		sessionKey := binding.TargetID + "\x00" + binding.SessionName
		switch session.State(state) {
		case session.StateCreated, session.StateRunning, session.StateIdle:
			if _, counted := countedSessions[sessionKey]; !counted {
				countedSessions[sessionKey] = struct{}{}
				active++
			}
		case session.StateArchived:
			if err := b.db.TerminateChat(b.currentBridgeId(), binding.RootMsgID); err != nil {
				return false, err
			}
		default:
			if !found {
				if err := b.db.TerminateChat(b.currentBridgeId(), binding.RootMsgID); err != nil {
					return false, err
				}
			}
		}
		if active >= limit {
			return true, b.notifyThread(ctx, rootMsgID,
				fmt.Sprintf("当前群的群值守活跃 Session 已达上限（%d）。空闲 Session 会按群值守配置自动休眠；也可以 @当前智能体 人工处理。", limit))
		}
	}
	return false, nil
}

func isGroupWatchManagementCommand(text string) bool {
	intent := command.Resolve(command.ScopeGroup, strings.TrimSpace(text))
	return isGroupWatchIntent(intent)
}

func isGroupWatchIntent(intent command.Command) bool {
	return intent == command.CommandGroupWatchOn || intent == command.CommandGroupWatchOff
}

func (b *Bridge) runGroupWatchPoller(ctx context.Context) {
	if err := b.pollGroupWatchOnce(ctx, time.Now()); err != nil && ctx.Err() == nil {
		b.observeCapabilityPermissionError(ctx, capabilityGroupWatch, "群值守", err, true)
		log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: initial group_watch poll failed")
	} else if err == nil {
		b.resolveCapabilityPermissionScopes(capabilityGroupWatch, "im:message.group_msg")
	}
	timer := time.NewTimer(b.groupWatchPollInterval())
	defer timer.Stop()
	changes := b.groupWatchConfigChanges()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			if err := b.pollGroupWatchOnce(ctx, now); err != nil && ctx.Err() == nil {
				b.observeCapabilityPermissionError(ctx, capabilityGroupWatch, "群值守", err, true)
				log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: group_watch poll failed")
			} else if err == nil {
				b.resolveCapabilityPermissionScopes(capabilityGroupWatch, "im:message.group_msg")
			}
			timer.Reset(b.groupWatchPollInterval())
		case <-changes:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if err := b.pollGroupWatchOnce(ctx, time.Now()); err != nil && ctx.Err() == nil {
				b.observeCapabilityPermissionError(ctx, capabilityGroupWatch, "群值守", err, true)
				log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: group_watch config refresh poll failed")
			} else if err == nil {
				b.resolveCapabilityPermissionScopes(capabilityGroupWatch, "im:message.group_msg")
			}
			timer.Reset(b.groupWatchPollInterval())
		}
	}
}

func (b *Bridge) groupWatchPollInterval() time.Duration {
	interval := time.Duration(b.groupWatchSnapshot().PollIntervalSeconds) * time.Second
	if interval <= 0 {
		return 15 * time.Second
	}
	return interval
}

func (b *Bridge) pollGroupWatchOnce(ctx context.Context, now time.Time) error {
	if !b.groupWatchActive() {
		return nil
	}
	cfg := b.groupWatchSnapshot()
	lookback := time.Duration(cfg.LookbackSeconds) * time.Second
	if lookback <= 0 {
		lookback = 60 * time.Second
	}
	start := now.Add(-lookback)
	list := b.groupWatchListMessages
	if list == nil {
		list = b.listGroupWatchMessages
	}
	var pollErrs []error
	for _, chatID := range cfg.ChatIDs {
		messages, err := list(ctx, chatID, start, now)
		if err != nil {
			pollErrs = append(pollErrs, fmt.Errorf("%s: %w", chatID, err))
			continue
		}
		sort.SliceStable(messages, func(i, j int) bool {
			left := groupWatchHistoryCreateTime(messages[i])
			right := groupWatchHistoryCreateTime(messages[j])
			return left.Before(right)
		})
		for _, raw := range messages {
			msg, ok := b.groupWatchMessageFromHistory(raw, start, now)
			if !ok {
				continue
			}
			if err := b.routeGroupWatchMessage(ctx, msg); err != nil {
				pollErrs = append(pollErrs, fmt.Errorf("%s/%s: %w", chatID, msg.MessageID, err))
			}
		}
	}
	return errors.Join(pollErrs...)
}

func (b *Bridge) listGroupWatchMessages(ctx context.Context, chatID string, start, end time.Time) ([]*larkim.Message, error) {
	if b == nil || b.client == nil {
		return nil, fmt.Errorf("nil lark client")
	}
	var (
		out       []*larkim.Message
		pageToken string
	)
	for page := 0; page < maxGroupWatchPollPages; page++ {
		builder := larkim.NewListMessageReqBuilder().
			ContainerIdType("chat").
			ContainerId(strings.TrimSpace(chatID)).
			StartTime(strconv.FormatInt(start.Unix(), 10)).
			EndTime(strconv.FormatInt(end.Unix(), 10)).
			SortType("ByCreateTimeAsc").
			PageSize(50).
			CardMsgContentType("user_card_content")
		if pageToken != "" {
			builder.PageToken(pageToken)
		}
		resp, err := b.client.Im.Message.List(ctx, builder.Build())
		if err != nil {
			return nil, fmt.Errorf("lark list messages: %w", err)
		}
		if !resp.Success() {
			appID := ""
			if b.secrets != nil {
				appID = b.secrets.AppId
			}
			return nil, feishuAPIResponseError(
				"lark list messages", appID, resp.CodeError, "im:message.group_msg",
			)
		}
		if resp.Data == nil {
			break
		}
		out = append(out, resp.Data.Items...)
		if resp.Data.HasMore == nil || !*resp.Data.HasMore || strings.TrimSpace(stringPtr(resp.Data.PageToken)) == "" {
			break
		}
		pageToken = strings.TrimSpace(stringPtr(resp.Data.PageToken))
	}
	return out, nil
}

func (b *Bridge) groupWatchMessageFromHistory(raw *larkim.Message, start, end time.Time) (groupWatchMessage, bool) {
	if raw == nil || raw.Deleted != nil && *raw.Deleted {
		return groupWatchMessage{}, false
	}
	chatID := strings.TrimSpace(stringPtr(raw.ChatId))
	if !b.groupWatchHasChat(chatID) {
		return groupWatchMessage{}, false
	}
	senderType := ""
	senderID := ""
	if raw.Sender != nil {
		senderType = normalizeGroupWatchSenderType(stringPtr(raw.Sender.SenderType))
		senderID = strings.TrimSpace(stringPtr(raw.Sender.Id))
	}
	if !b.groupWatchSenderAllowed(senderType) || b.isOwnGroupWatchSender(senderType, senderID) {
		return groupWatchMessage{}, false
	}
	if senderID == "" {
		log.Warn().Str("message_id", stringPtr(raw.MessageId)).
			Msg("bridge: group_watch history message ignored because sender identity is missing")
		return groupWatchMessage{}, false
	}
	if b.groupWatchHistoryMentionsCurrentBot(raw) {
		return groupWatchMessage{}, false
	}
	if senderType == "app" && len(raw.Mentions) > 0 {
		return groupWatchMessage{}, false
	}
	rootID := groupWatchHistoryRootID(raw)
	if rootID != "" {
		return groupWatchMessage{}, false
	}
	createdAt, ok := parseFeishuMessageTime(stringPtr(raw.CreateTime))
	if !ok || createdAt.Before(start) || createdAt.After(end.Add(time.Second)) {
		return groupWatchMessage{}, false
	}
	var content *string
	if raw.Body != nil {
		content = raw.Body.Content
	}
	text := groupWatchInputText(stringPtr(raw.MessageId), stringPtr(raw.MsgType), content)
	if text == "" || strings.HasPrefix(text, "/") {
		return groupWatchMessage{}, false
	}
	return groupWatchMessage{
		ChatID:     chatID,
		MessageID:  strings.TrimSpace(stringPtr(raw.MessageId)),
		RootID:     rootID,
		SenderType: senderType,
		SenderID:   senderID,
		Text:       text,
		Kind:       stringPtr(raw.MsgType),
		Content:    content,
	}, true
}

func groupWatchHistoryRootID(msg *larkim.Message) string {
	if msg == nil {
		return ""
	}
	return strings.TrimSpace(firstNonEmpty(stringPtr(msg.RootId), stringPtr(msg.ParentId)))
}

func (b *Bridge) groupWatchHistoryMentionsCurrentBot(msg *larkim.Message) bool {
	if b == nil || msg == nil || b.secrets == nil {
		return false
	}
	botID := strings.TrimSpace(b.secrets.BotOpenId)
	botName := strings.TrimSpace(b.secrets.BotName)
	for _, mention := range msg.Mentions {
		if mention == nil {
			continue
		}
		if botID != "" && strings.TrimSpace(stringPtr(mention.Id)) == botID {
			return true
		}
		if botName != "" && strings.EqualFold(strings.TrimSpace(stringPtr(mention.Name)), botName) {
			return true
		}
	}
	return false
}

func groupWatchHistoryCreateTime(msg *larkim.Message) time.Time {
	if msg == nil {
		return time.Time{}
	}
	createdAt, _ := parseFeishuMessageTime(stringPtr(msg.CreateTime))
	return createdAt
}

func (b *Bridge) groupWatchSenderAllowed(senderType string) bool {
	return groupWatchConfigAllowsSender(b.groupWatchSnapshot(), senderType)
}

func groupWatchConfigAllowsSender(cfg config.BridgeFeishuGroupWatchConfig, senderType string) bool {
	senderType = normalizeGroupWatchSenderType(senderType)
	for _, allowed := range cfg.SenderTypes {
		if senderType != "" && senderType == normalizeGroupWatchSenderType(allowed) {
			return true
		}
	}
	return false
}

func (b *Bridge) isOwnGroupWatchSender(senderType, senderID string) bool {
	if b == nil || b.secrets == nil || normalizeGroupWatchSenderType(senderType) != "app" {
		return false
	}
	senderID = strings.TrimSpace(senderID)
	return senderID != "" && (senderID == strings.TrimSpace(b.secrets.AppId) || senderID == strings.TrimSpace(b.secrets.BotOpenId))
}

func normalizeGroupWatchSenderType(senderType string) string {
	switch strings.ToLower(strings.TrimSpace(senderType)) {
	case "bot", "app":
		return "app"
	case "user":
		return "user"
	default:
		return ""
	}
}

func parseFeishuMessageTime(raw string) (time.Time, bool) {
	ms, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

// groupWatchInputText admits supported materials without inventing a business
// instruction. The selected agent profile owns how to handle the material.
func groupWatchInputText(id, kind string, content *string) string {
	if text := strings.TrimSpace(extractGroupWatchText(kind, content)); text != "" {
		return text
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "image", "post", "file", "merge_forward":
	default:
		return ""
	}
	// Feishu represents a merged container with a plain placeholder, not JSON.
	// Its message ID is sufficient to look up and validate the descendants.
	text, materials := parseMessageMaterials(id, kind, content)
	if len(materials.images)+len(materials.files) > 0 || kind == "merge_forward" && strings.TrimSpace(id) != "" {
		return text
	}
	return ""
}

func extractGroupWatchText(messageType string, content *string) string {
	messageType = strings.ToLower(strings.TrimSpace(messageType))
	if messageType == "" || messageType == "text" || messageType == "post" {
		return strings.TrimSpace(extractText(content))
	}
	if messageType != "interactive" || content == nil || strings.TrimSpace(*content) == "" {
		return ""
	}
	var raw any
	if err := json.Unmarshal([]byte(*content), &raw); err != nil {
		return ""
	}
	var lines []string
	collectGroupWatchCardText(raw, &lines)
	seen := make(map[string]bool, len(lines))
	out := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func collectGroupWatchCardText(value any, lines *[]string) {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			collectGroupWatchCardText(item, lines)
		}
	case map[string]any:
		tag, _ := value["tag"].(string)
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "markdown", "plain_text", "lark_md":
			if content, ok := value["content"].(string); ok {
				*lines = append(*lines, content)
			}
			if text, ok := value["text"].(string); ok {
				*lines = append(*lines, text)
			}
		}
		for _, key := range []string{"header", "title", "body", "elements", "content", "text"} {
			child, ok := value[key]
			if !ok {
				continue
			}
			if _, scalar := child.(string); scalar {
				if key == "content" && tag == "" {
					*lines = append(*lines, child.(string))
				}
				continue
			}
			collectGroupWatchCardText(child, lines)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
