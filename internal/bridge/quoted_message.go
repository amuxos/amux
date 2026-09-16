package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bridgeprompt "github.com/amuxos/amux/internal/bridge/prompt"
	"github.com/rs/zerolog/log"
)

const maxFeishuQuotedMessageRunes = 12000

type feishuQuotedMessageRef struct {
	messageID string
	chatID    string
}

type feishuQuotedMessage struct {
	Materials messageMaterials
	MessageID string
	ChatID    string
	MsgType   string
	ActorType string
	ActorID   string
	Text      string
}

type feishuQuotedMessageContextKey struct{}

func withFeishuQuotedMessage(ctx context.Context, ref feishuQuotedMessageRef) context.Context {
	ref.messageID = strings.TrimSpace(ref.messageID)
	ref.chatID = strings.TrimSpace(ref.chatID)
	if ref.messageID == "" || ref.chatID == "" {
		return ctx
	}
	return context.WithValue(ctx, feishuQuotedMessageContextKey{}, ref)
}

func feishuQuotedMessageFromContext(ctx context.Context) feishuQuotedMessageRef {
	ref, _ := ctx.Value(feishuQuotedMessageContextKey{}).(feishuQuotedMessageRef)
	return ref
}

func withoutBoundTopicRootQuote(ctx context.Context, rootMsgID string) context.Context {
	ref := feishuQuotedMessageFromContext(ctx)
	if ref.messageID == "" || ref.messageID != strings.TrimSpace(rootMsgID) {
		return ctx
	}
	return context.WithValue(ctx, feishuQuotedMessageContextKey{}, feishuQuotedMessageRef{})
}

func (b *Bridge) resolveFeishuQuotedMessage(ctx context.Context) (feishuQuotedMessage, bool, error) {
	ref := feishuQuotedMessageFromContext(ctx)
	if ref.messageID == "" {
		return feishuQuotedMessage{}, false, nil
	}
	localText := ""
	if b != nil && b.threadContext != nil {
		text, err := b.threadContext.MessageText(ctx, ref.chatID, ref.messageID)
		if err != nil {
			log.Warn().Err(err).Str("message_id", ref.messageID).Msg("bridge: read quoted message from thread context")
		} else {
			localText = strings.TrimSpace(text)
		}
	}
	lookup := b.quotedMessageLookup
	if lookup == nil {
		lookup = b.fetchQuotedMessage
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	quoted, err := lookup(lookupCtx, ref.messageID)
	if err != nil {
		b.observeCapabilityPermissionError(ctx, capabilityCoreMessaging, "引用消息", err, true)
		return feishuQuotedMessage{}, true, err
	}
	if strings.TrimSpace(quoted.MessageID) != ref.messageID {
		return feishuQuotedMessage{}, true, fmt.Errorf("quoted message id mismatch: got %q", quoted.MessageID)
	}
	if strings.TrimSpace(quoted.ChatID) != ref.chatID {
		return feishuQuotedMessage{}, true, fmt.Errorf("quoted message belongs to another chat")
	}
	if strings.TrimSpace(quoted.Text) == "" && quoted.MsgType == "interactive" {
		quoted.Text = localText
	}
	quoted.Text = truncateFeishuQuotedMessage(quoted.Text)
	if strings.TrimSpace(quoted.Text) == "" {
		return feishuQuotedMessage{}, true, fmt.Errorf("quoted message has no readable text")
	}
	collectMaterials(ctx, ref.messageID, quoted.Materials)
	return quoted, true, nil
}

func (b *Bridge) renderFeishuQuotedMessage(input string, quoted feishuQuotedMessage, present bool) (string, error) {
	if !present {
		return input, nil
	}
	renderer := b.promptRenderer
	if renderer == nil {
		renderer = bridgeprompt.NewRenderer(bridgeprompt.Options{})
	}
	return renderer.RenderFeishuQuotedReply(bridgeprompt.QuotedReplyInput{
		QuotedMessage:     quoted.Text,
		ConversationInput: input,
	})
}

func truncateFeishuQuotedMessage(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= maxFeishuQuotedMessageRunes {
		return text
	}
	const suffix = "\n\n[Quoted message truncated]"
	suffixRunes := []rune(suffix)
	keep := maxFeishuQuotedMessageRunes - len(suffixRunes)
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + suffix
}

func quotedMessageUnavailableText() string {
	return "无法读取被引用的消息，消息未发送给智能体。请将需要处理的正文粘贴到新消息后重试。"
}

func defaultQuotedMessageRequest() string {
	return "请阅读用户显式引用的飞书消息，并针对其内容提供帮助。"
}

func extractInteractiveMessageText(content *string) string {
	if content == nil || strings.TrimSpace(*content) == "" {
		return ""
	}
	var doc struct {
		Header *cardHeader `json:"header"`
		Body   cardBody    `json:"body"`
	}
	if err := json.Unmarshal([]byte(*content), &doc); err != nil {
		return ""
	}
	var lines []string
	if doc.Header != nil {
		lines = appendQuotedLine(lines, doc.Header.Title.Content)
		if doc.Header.Subtitle != nil {
			lines = appendQuotedLine(lines, doc.Header.Subtitle.Content)
		}
	}
	for _, element := range doc.Body.Elements {
		lines = appendQuotedCardElement(lines, element)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func appendQuotedCardElement(lines []string, element cardElement) []string {
	if element.ElementID == amuxBrandFooterID || element.ElementID == amuxBrandDividerID {
		return lines
	}
	lines = appendQuotedLine(lines, element.Content)
	if element.Text != nil {
		lines = appendQuotedLine(lines, element.Text.Content)
	}
	if element.Header != nil {
		lines = appendQuotedLine(lines, element.Header.Title.Content)
	}
	for _, child := range element.Elements {
		lines = appendQuotedCardElement(lines, child)
	}
	for _, column := range element.Columns {
		for _, child := range column.Elements {
			lines = appendQuotedCardElement(lines, child)
		}
	}
	return lines
}

func appendQuotedLine(lines []string, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" || (len(lines) > 0 && lines[len(lines)-1] == text) {
		return lines
	}
	return append(lines, text)
}
