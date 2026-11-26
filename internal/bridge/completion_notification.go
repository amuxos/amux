package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
)

const (
	turnNotificationCompleted = "completed"
	turnNotificationWaiting   = "waiting"
	turnNotificationFailed    = "failed"
)

// dispatchTurnCompletionNotification owns the best-effort notification separately
// from the stream's mutable binding and event-processing state.
func (b *Bridge) dispatchTurnCompletionNotification(chat *LarkChat, turnID, status, stopReason string, elapsed time.Duration) {
	if b == nil || chat == nil {
		return
	}
	snapshot := *chat
	finishedAt := time.Now()
	send := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := b.notifyTurnCompletionAt(ctx, &snapshot, turnID, status, stopReason, elapsed, finishedAt); err != nil {
			log.Warn().Err(err).Str("session", snapshot.SessionName).Str("turn_id", turnID).
				Msg("bridge: send turn completion notification")
		}
	}
	go send()
}

func (b *Bridge) notifyTurnCompletion(ctx context.Context, chat *LarkChat, turnID, status, stopReason string, elapsed time.Duration) error {
	return b.notifyTurnCompletionAt(ctx, chat, turnID, status, stopReason, elapsed, time.Now())
}

func (b *Bridge) notifyTurnCompletionAt(ctx context.Context, chat *LarkChat, turnID, status, stopReason string, elapsed time.Duration, finishedAt time.Time) error {
	if b == nil || b.db == nil || chat == nil || strings.TrimSpace(turnID) == "" || turnCompletionCancelled(stopReason) {
		return nil
	}
	if !config.GetBridgeFeishuPresentation().CompletionNotificationEnabled {
		return nil
	}
	kind := turnCompletionNotificationKind(status)
	if kind == "" {
		return nil
	}
	bridgeID := strings.TrimSpace(chat.BridgeId)
	if bridgeID == "" {
		bridgeID = b.currentBridgeId()
	}
	if submittedAt, err := b.db.TurnSubmittedAt(bridgeID, turnID); err != nil {
		return err
	} else if submittedElapsed := finishedAt.Sub(submittedAt); !submittedAt.IsZero() && submittedElapsed > elapsed {
		elapsed = submittedElapsed
	}
	seconds := config.GetBridgeFeishuPresentationForProfile(b.ProfileName()).CompletionNotificationAfterSeconds
	if seconds == 0 || elapsed < time.Duration(seconds)*time.Second {
		return nil
	}
	if b.isSubtaskTurn(chat.RootMsgID, turnID) {
		// Background-task notice turns have no Feishu source chat.
		log.Debug().Str("session", chat.SessionName).Str("turn_id", turnID).Msg("bridge: completion notification skipped for subtask turn")
		return nil
	}
	recipients, err := b.db.TurnNotificationRecipients(bridgeID, turnID)
	if err != nil || len(recipients) == 0 {
		return err
	}
	if strings.TrimSpace(chat.ChatID) == "" {
		// Steered, coalesced and queued turns inherit the chat of their
		// first message.
		if source, ok := b.turnSourceChat(chat.RootMsgID, turnID); ok {
			resolved := *chat
			resolved.ChatID = source.ChatID
			if strings.TrimSpace(resolved.ThreadID) == "" {
				resolved.ThreadID = source.ThreadID
			}
			chat = &resolved
		}
	}
	jumpURL, err := b.completionNotificationURL(ctx, chat)
	if err != nil {
		return err
	}
	title := valueOrPlaceholder(chat.DisplayTitle, "任务结果")
	var failures []error
	for _, openID := range recipients {
		// Keep the existing table while giving each DM its own delivery claim.
		claimKind := kind + ":dm:" + openID
		claimed, err := b.db.ClaimTurnNotification(bridgeID, turnID, claimKind)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !claimed {
			continue
		}
		card := renderTurnCompletionNotificationCard(kind, title, jumpURL, openID)
		if b.dmInteractive != nil {
			_, _, err = b.dmInteractive(ctx, openID, card)
		} else if b.client != nil {
			_, _, err = b.sendInteractiveToDM(ctx, openID, card, "completion notification")
		} else {
			err = fmt.Errorf("completion notification: nil lark client")
		}
		if err != nil {
			failures = append(failures, err, b.db.ReleaseTurnNotification(bridgeID, turnID, claimKind))
		}
	}
	return errors.Join(failures...)
}

func (b *Bridge) completionNotificationURL(ctx context.Context, chat *LarkChat) (string, error) {
	chatID := strings.TrimSpace(chat.ChatID)
	if chatID == "" {
		return "", fmt.Errorf("completion notification: missing source chat")
	}
	threadID := strings.TrimSpace(chat.ThreadID)
	if !strings.HasPrefix(threadID, "omt_") && chat.RootMsgID != singletonStreamKey(b.currentBridgeId()) {
		lookup := b.topicRootLookup
		if lookup == nil {
			lookup = b.fetchTopicRootMessage
		}
		root, err := lookup(ctx, chat.RootMsgID)
		if err != nil {
			return "", err
		}
		if root.ChatID != chatID || !strings.HasPrefix(root.ThreadID, "omt_") {
			return "", fmt.Errorf("completion notification: source thread unavailable")
		}
		threadID = root.ThreadID
	}
	return feishuConversationURL(chatID, threadID), nil
}

func feishuConversationURL(chatID, threadID string) string {
	if threadID != "" {
		query := url.Values{"open_chat_id": {chatID}, "openchatid": {chatID}, "open_thread_id": {threadID}, "openthreadid": {threadID}, "thread_position": {"-1"}}
		return "https://applink.feishu.cn/client/thread/open?" + query.Encode()
	}
	return "https://applink.feishu.cn/client/chat/open?" + url.Values{"openChatId": {chatID}}.Encode()
}

func turnCompletionNotificationKind(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "waiting":
		return turnNotificationWaiting
	case "failed":
		return turnNotificationFailed
	case "done", "completed", "success", "succeeded":
		return turnNotificationCompleted
	default:
		return ""
	}
}

func turnCompletionCancelled(stopReason string) bool {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "cancelled", "canceled", "interrupted", "killed", "stopped", "superseded", "session_closed":
		return true
	default:
		return false
	}
}

func renderTurnCompletionNotificationCard(kind, title, jumpURL, openID string) string {
	heading, theme, button := "已完成", "green", "查看结果 →"
	switch kind {
	case turnNotificationWaiting:
		heading, theme, button = "需要你补充信息", "orange", "继续处理 →"
	case turnNotificationFailed:
		heading, theme = "处理失败", "red"
	}
	// Plain text prevents a task title from injecting mentions or links.
	if runes := []rune(title); len(runes) > 80 {
		title = string(runes[:80]) + "…"
	}
	label := sessionMessage("notification.sent.to")
	footer := managementContentElement(i18n.Render(func(locale i18n.Locale) string {
		return "<font color='grey'>" + label.For(locale) + "</font> " + renderTurnInitiatorMentionsMarkdown([]string{openID})
	}), cardElement{Tag: "markdown", ElementID: amuxBrandFooterID, TextSize: "notation"})
	doc := newCardDoc([]cardElement{
		{Tag: "div", Text: &cardTitle{Tag: "plain_text", Content: title, TextSize: "normal", Lines: 2}, Margin: "0px"},
		{Tag: "button", Text: &cardTitle{Tag: "plain_text", Content: button}, Type: "primary", Size: "small", Behaviors: []cardBehavior{{Type: "open_url", DefaultURL: jumpURL, PCURL: jumpURL, IOSURL: jumpURL, AndroidURL: jumpURL}}},
		footer,
	})
	doc.Config.WidthMode = "compact"
	doc.Config.WideScreenMode = false
	doc.Config.Summary = &cardSummary{Content: heading + " · " + title}
	doc.Header = &cardHeader{Title: cardTitle{Tag: "plain_text", Content: heading}, Template: theme, Padding: "8px 12px 8px 12px"}
	doc.Body.Padding = "8px 12px 8px 12px"
	doc.Body.VerticalSpacing = "8px"
	raw, _ := json.Marshal(doc)
	return string(raw)
}
