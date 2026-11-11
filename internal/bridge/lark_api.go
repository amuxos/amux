package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/rs/zerolog/log"
)

// larkLookupCache owns the chat bot-count cache.
type larkLookupCache struct {
	chatBotCountMu    sync.Mutex
	chatBotCountCache map[string]chatBotCountCacheEntry
}

func (b *Bridge) downloadMessageImage(ctx context.Context, messageID, imageKey string) (io.Reader, string, error) {
	if b == nil || b.client == nil {
		return nil, "", fmt.Errorf("nil lark client")
	}
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build()
	resp, err := b.client.Im.MessageResource.Get(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("lark get message image: %w", err)
	}
	if !resp.Success() || resp.File == nil {
		return nil, "", feishuAPIResponseErrorForRequiredScopes(
			"lark get message image", b.secretsAppID(), resp.CodeError, "im:message:readonly",
		)
	}
	b.resolveCapabilityPermissionScopes(capabilityCoreMessaging, "im:message:readonly")
	return resp.File, resp.FileName, nil
}

const chatBotCountCacheTTL = 5 * time.Minute

type topicRootMessage struct {
	Materials messageMaterials
	ChatID    string
	MessageID string
	ThreadID  string
	ActorType string
	ActorID   string
	Text      string
}

func (b *Bridge) chatBotCount(ctx context.Context, chatID string) (int, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return 0, fmt.Errorf("chat_id is required")
	}
	if b == nil {
		return 0, fmt.Errorf("nil bridge")
	}
	b.chatBotCountMu.Lock()
	if b.chatBotCountCache != nil {
		if cached, ok := b.chatBotCountCache[chatID]; ok && time.Since(cached.FetchedAt) < chatBotCountCacheTTL {
			b.chatBotCountMu.Unlock()
			return cached.Count, nil
		}
	}
	b.chatBotCountMu.Unlock()

	lookup := b.chatBotCountLookup
	if lookup == nil {
		lookup = b.fetchChatBotCount
	}
	count, err := lookup(ctx, chatID)
	if err != nil {
		return 0, err
	}
	b.chatBotCountMu.Lock()
	if b.chatBotCountCache == nil {
		b.chatBotCountCache = make(map[string]chatBotCountCacheEntry)
	}
	b.chatBotCountCache[chatID] = chatBotCountCacheEntry{Count: count, FetchedAt: time.Now()}
	b.chatBotCountMu.Unlock()
	return count, nil
}

func (b *Bridge) fetchChatBotCount(ctx context.Context, chatID string) (int, error) {
	if b == nil || b.client == nil {
		return 0, fmt.Errorf("nil lark client")
	}
	req := larkim.NewGetChatReqBuilder().ChatId(chatID).Build()
	resp, err := b.client.Im.Chat.Get(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("lark get chat: %w", err)
	}
	if !resp.Success() {
		return 0, fmt.Errorf("lark get chat: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.BotCount == nil {
		return 0, fmt.Errorf("lark get chat: missing bot_count")
	}
	count, err := strconv.Atoi(strings.TrimSpace(*resp.Data.BotCount))
	if err != nil {
		return 0, fmt.Errorf("lark get chat: invalid bot_count %q", *resp.Data.BotCount)
	}
	return count, nil
}

func (b *Bridge) fetchChatName(ctx context.Context, chatID string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "", fmt.Errorf("chat_id is required")
	}
	if b == nil || b.client == nil {
		return "", fmt.Errorf("nil lark client")
	}
	req := larkim.NewGetChatReqBuilder().ChatId(chatID).Build()
	resp, err := b.client.Im.Chat.Get(ctx, req)
	if err != nil {
		return "", fmt.Errorf("lark get chat: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark get chat: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || strings.TrimSpace(stringPtr(resp.Data.Name)) == "" {
		return "", fmt.Errorf("lark get chat: missing name")
	}
	return strings.TrimSpace(stringPtr(resp.Data.Name)), nil
}

func (b *Bridge) fetchSourceMessage(ctx context.Context, messageID string) (*larkim.Message, error) {
	if b.client == nil {
		return nil, fmt.Errorf("nil lark client")
	}
	req := larkim.NewGetMessageReqBuilder().MessageId(messageID).UserIdType("open_id").Build()
	resp, err := b.client.Im.Message.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lark get source message: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("lark get source message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return nil, fmt.Errorf("lark get source message: empty message")
	}
	return resp.Data.Items[0], nil
}

func (b *Bridge) fetchTopicRootMessage(ctx context.Context, rootMsgID string) (topicRootMessage, error) {
	rootMsgID = strings.TrimSpace(rootMsgID)
	if rootMsgID == "" {
		return topicRootMessage{}, fmt.Errorf("root message id is required")
	}
	if b == nil || b.client == nil {
		return topicRootMessage{}, fmt.Errorf("nil lark client")
	}
	req := larkim.NewGetMessageReqBuilder().
		MessageId(rootMsgID).
		UserIdType("open_id").
		Build()
	resp, err := b.client.Im.Message.Get(ctx, req)
	if err != nil {
		return topicRootMessage{}, fmt.Errorf("lark get topic root: %w", err)
	}
	if !resp.Success() {
		return topicRootMessage{}, fmt.Errorf("lark get topic root: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return topicRootMessage{}, fmt.Errorf("lark get topic root: empty message")
	}
	msg := resp.Data.Items[0]
	if stringPtr(msg.MessageId) != rootMsgID {
		return topicRootMessage{}, fmt.Errorf("lark get topic root: unexpected message id %q", stringPtr(msg.MessageId))
	}
	if msg.Deleted != nil && *msg.Deleted {
		return topicRootMessage{}, fmt.Errorf("lark get topic root: message was deleted")
	}
	msgType := strings.TrimSpace(stringPtr(msg.MsgType))
	if msgType != "text" && msgType != "post" && msgType != "image" && msgType != "file" && msgType != "merge_forward" {
		return topicRootMessage{}, fmt.Errorf("lark get topic root: unsupported message type %q", msgType)
	}
	if msg.Body == nil {
		return topicRootMessage{}, fmt.Errorf("lark get topic root: missing message body")
	}
	text, materials, parseErr := expandMessageMaterials(rootMsgID, resp.Data.Items)
	if parseErr != nil {
		return topicRootMessage{}, parseErr
	}
	if text == "" {
		return topicRootMessage{}, fmt.Errorf("lark get topic root: empty text")
	}
	replacements := make(map[string]string)
	for _, mention := range msg.Mentions {
		if mention == nil || mention.Key == nil || mention.Name == nil {
			continue
		}
		key := normalizeMentionToken(*mention.Key)
		name := strings.TrimSpace(*mention.Name)
		if key != "" && name != "" {
			replacements[key] = "@" + name
		}
	}
	actorType, actorID := "", ""
	if msg.Sender != nil {
		actorType = strings.TrimSpace(stringPtr(msg.Sender.SenderType))
		actorID = strings.TrimSpace(stringPtr(msg.Sender.Id))
	}
	return topicRootMessage{
		Materials: materials,
		ChatID:    strings.TrimSpace(stringPtr(msg.ChatId)),
		MessageID: rootMsgID,
		ThreadID:  strings.TrimSpace(stringPtr(msg.ThreadId)),
		ActorType: actorType,
		ActorID:   actorID,
		Text:      strings.TrimSpace(normalizeMentionTokens(text, replacements)),
	}, nil
}

func (b *Bridge) fetchQuotedMessage(ctx context.Context, messageID string) (feishuQuotedMessage, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return feishuQuotedMessage{}, fmt.Errorf("quoted message id is required")
	}
	if b == nil || b.client == nil {
		return feishuQuotedMessage{}, fmt.Errorf("nil lark client")
	}
	req := larkim.NewGetMessageReqBuilder().
		MessageId(messageID).
		UserIdType("open_id").
		CardMsgContentType("user_card_content").
		Build()
	resp, err := b.client.Im.Message.Get(ctx, req)
	if err != nil {
		return feishuQuotedMessage{}, fmt.Errorf("lark get quoted message: %w", err)
	}
	if !resp.Success() {
		return feishuQuotedMessage{}, feishuAPIResponseErrorForRequiredScopes(
			"lark get quoted message", b.secretsAppID(), resp.CodeError, "im:message:readonly",
		)
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return feishuQuotedMessage{}, fmt.Errorf("lark get quoted message: empty message")
	}
	msg := resp.Data.Items[0]
	if stringPtr(msg.MessageId) != messageID {
		return feishuQuotedMessage{}, fmt.Errorf("lark get quoted message: unexpected message id %q", stringPtr(msg.MessageId))
	}
	if msg.Deleted != nil && *msg.Deleted {
		return feishuQuotedMessage{}, fmt.Errorf("lark get quoted message: message was deleted")
	}
	if msg.Body == nil {
		return feishuQuotedMessage{}, fmt.Errorf("lark get quoted message: missing message body")
	}
	msgType := strings.TrimSpace(stringPtr(msg.MsgType))
	var text string
	switch msgType {
	case "text", "post", "image", "file", "merge_forward":
		text, _, err = expandMessageMaterials(messageID, resp.Data.Items)
		if err != nil {
			return feishuQuotedMessage{}, err
		}
	case "interactive":
		text = strings.TrimSpace(extractInteractiveMessageText(msg.Body.Content))
	default:
		return feishuQuotedMessage{}, fmt.Errorf("lark get quoted message: unsupported message type %q", msgType)
	}
	if text == "" && msgType != "interactive" {
		return feishuQuotedMessage{}, fmt.Errorf("lark get quoted message: empty text")
	}
	b.resolveCapabilityPermissionScopes(capabilityCoreMessaging, "im:message:readonly")
	replacements := make(map[string]string)
	for _, mention := range msg.Mentions {
		if mention == nil || mention.Key == nil || mention.Name == nil {
			continue
		}
		key := normalizeMentionToken(*mention.Key)
		name := strings.TrimSpace(*mention.Name)
		if key != "" && name != "" {
			replacements[key] = "@" + name
		}
	}
	actorType, actorID := "", ""
	if msg.Sender != nil {
		actorType = strings.TrimSpace(stringPtr(msg.Sender.SenderType))
		actorID = strings.TrimSpace(stringPtr(msg.Sender.Id))
	}
	_, materials, _ := expandMessageMaterials(messageID, resp.Data.Items)
	return feishuQuotedMessage{
		Materials: materials,
		MessageID: messageID,
		ChatID:    strings.TrimSpace(stringPtr(msg.ChatId)),
		MsgType:   msgType,
		ActorType: actorType,
		ActorID:   actorID,
		Text:      strings.TrimSpace(normalizeMentionTokens(text, replacements)),
	}, nil
}

// sendDM sends a text message to a user's DM and returns (message_id, thread_id, error).
// thread_id may be empty if the API doesn't return one.
func (b *Bridge) sendDM(ctx context.Context, openID, text string) (string, string, error) {
	content := fmt.Sprintf(`{"text":%s}`, jsonQuote(text))
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("open_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(openID).
			MsgType("text").
			Content(content).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("lark create message: %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark create message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", "", fmt.Errorf("lark create message: no message_id in response")
	}
	threadID := ""
	if resp.Data.ThreadId != nil {
		threadID = *resp.Data.ThreadId
	}
	log.Info().
		Str("msg_id", *resp.Data.MessageId).
		Str("thread_id", threadID).
		Msg("sendDM: response")
	return *resp.Data.MessageId, threadID, nil
}

// sendToChat sends a text message to a Feishu chat (group or DM) by chat_id and returns
// (message_id, thread_id, error). Used for group chat notifications where topic threading works.
func (b *Bridge) sendToChat(ctx context.Context, chatID, text string) (string, string, error) {
	content := fmt.Sprintf(`{"text":%s}`, jsonQuote(text))
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(content).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("lark create group message: %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark create group message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", "", fmt.Errorf("lark create group message: no message_id in response")
	}
	threadID := ""
	if resp.Data.ThreadId != nil {
		threadID = *resp.Data.ThreadId
	}
	log.Info().
		Str("chat_id", chatID).
		Str("msg_id", *resp.Data.MessageId).
		Str("thread_id", threadID).
		Msg("sendToChat: response")
	return *resp.Data.MessageId, threadID, nil
}

func (b *Bridge) sendExternalBotDelegationToChat(ctx context.Context, chatID, threadRootID, botOpenID, text string) (string, string, error) {
	callbackBotOpenID := ""
	if b != nil && b.secrets != nil {
		callbackBotOpenID = strings.TrimSpace(b.secrets.BotOpenId)
	}
	if callbackBotOpenID == "" {
		return "", "", fmt.Errorf("lark send external Bot delegation: current Bot open_id is unavailable")
	}
	content := renderExternalBotDelegationCard(botOpenID, callbackBotOpenID, text)
	if threadRootID != "" {
		req := larkim.NewReplyMessageReqBuilder().
			MessageId(threadRootID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("interactive").
				Content(content).
				ReplyInThread(true).
				Build()).
			Build()
		resp, err := b.client.Im.Message.Reply(ctx, req)
		if err != nil {
			return "", "", fmt.Errorf("lark reply external Bot delegation: %w", err)
		}
		if !resp.Success() {
			return "", "", fmt.Errorf("lark reply external Bot delegation: code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil || resp.Data.MessageId == nil {
			return "", "", errors.New("lark reply external Bot delegation: no message_id in response")
		}
		threadID := ""
		if resp.Data.ThreadId != nil {
			threadID = *resp.Data.ThreadId
		}
		return *resp.Data.MessageId, threadID, nil
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("interactive").
			Content(content).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("lark create external Bot delegation: %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark create external Bot delegation: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", "", errors.New("lark create external Bot delegation: no message_id in response")
	}
	threadID := ""
	if resp.Data.ThreadId != nil {
		threadID = *resp.Data.ThreadId
	}
	return *resp.Data.MessageId, threadID, nil
}

func (b *Bridge) sendInteractiveToChat(ctx context.Context, chatID, content, label string) (string, string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("interactive").
			Content(content).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("lark create "+label+": %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark create "+label+": code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", "", nil
	}
	threadID := ""
	if resp.Data.ThreadId != nil {
		threadID = *resp.Data.ThreadId
	}
	return *resp.Data.MessageId, threadID, nil
}

func (b *Bridge) sendInteractiveToDM(ctx context.Context, openID, content, label string) (string, string, error) {
	return b.sendInteractiveToDMWithUUID(ctx, openID, content, label, "")
}
func (b *Bridge) sendInteractiveToDMWithUUID(ctx context.Context, openID, content, label, requestID string) (string, string, error) {
	body := larkim.NewCreateMessageReqBodyBuilder().ReceiveId(openID).MsgType("interactive").Content(content)
	if requestID != "" {
		body.Uuid(requestID)
	}
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").Body(body.Build()).Build()
	resp, err := b.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("lark create "+label+": %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark create "+label+": code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", "", nil
	}
	threadID := ""
	if resp.Data.ThreadId != nil {
		threadID = *resp.Data.ThreadId
	}
	return *resp.Data.MessageId, threadID, nil
}

// addReaction adds an emoji reaction to a message.
func (b *Bridge) addReaction(ctx context.Context, msgID, emojiType string) error {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(msgID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
			Build()).
		Build()
	resp, err := b.client.Im.MessageReaction.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("lark add reaction: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark add reaction: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (b *Bridge) sendTextToThread(ctx context.Context, rootMsgID, text string) (string, string, error) {
	content := fmt.Sprintf(`{"text":%s}`, jsonQuote(text))
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(rootMsgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("text").
			Content(content).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("lark reply text: %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark reply text: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", "", nil
	}
	threadID := ""
	if resp.Data.ThreadId != nil {
		threadID = *resp.Data.ThreadId
	}
	return *resp.Data.MessageId, threadID, nil
}

func (b *Bridge) sendAgentReplyToThread(ctx context.Context, rootMsgID, text string) (string, string, error) {
	return b.sendAgentReplyToThreadWithMentions(ctx, rootMsgID, text, nil)
}

func (b *Bridge) sendAgentReplyToThreadWithMentions(ctx context.Context, rootMsgID, text string, openIDs []string) (string, string, error) {
	content := renderAgentReplyPostWithMentions(text, openIDs)
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(rootMsgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("post").
			Content(content).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("lark reply agent post: %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark reply agent post: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", "", nil
	}
	threadID := ""
	if resp.Data.ThreadId != nil {
		threadID = *resp.Data.ThreadId
	}
	return *resp.Data.MessageId, threadID, nil
}

func (b *Bridge) sendBotHandoffToThread(ctx context.Context, rootMsgID, botOpenID, text string) error {
	content := renderBotHandoffPost(botOpenID, text)
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(rootMsgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("post").
			Content(content).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return fmt.Errorf("lark reply bot handoff post: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark reply bot handoff post: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (b *Bridge) sendPermissionRequestCard(ctx context.Context, chat *LarkChat, permissionID string, payload permissionPayload) (string, string, error) {
	content := renderPermissionRequestCard(chat.TargetID, chat.SessionName, permissionID, payload)
	return b.sendInteractiveToBinding(ctx, chat, content, "permission request card")
}

func (b *Bridge) sendToolActivityToThread(ctx context.Context, rootMsgID string, meta toolActivityCardMeta, text string) (string, string, error) {
	content := renderToolActivityCard(meta, text)
	return b.sendInteractiveToThread(ctx, rootMsgID, content, "tool activity card")
}

func (b *Bridge) sendAgentReplyCardToThread(ctx context.Context, rootMsgID, text string) (string, string, error) {
	content := renderAgentReplyCard(text)
	return b.sendInteractiveToThread(ctx, rootMsgID, content, "agent reply card")
}

func (b *Bridge) patchAgentReplyCard(ctx context.Context, msgID, text string) error {
	content := renderAgentReplyCard(text)
	return b.patchInteractiveMessage(ctx, msgID, content, "agent reply card")
}

func (b *Bridge) sendInteractiveToThread(ctx context.Context, rootMsgID, content, label string) (string, string, error) {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(rootMsgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(content).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("lark reply "+label+": %w", err)
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark reply "+label+": code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", "", nil
	}
	threadID := ""
	if resp.Data.ThreadId != nil {
		threadID = *resp.Data.ThreadId
	}
	return *resp.Data.MessageId, threadID, nil
}

// patchToolActivityMessage updates an existing Feishu card message in-place.
func (b *Bridge) patchToolActivityMessage(ctx context.Context, msgID string, meta toolActivityCardMeta, text string) error {
	content := renderToolActivityCard(meta, text)
	return b.patchInteractiveMessage(ctx, msgID, content, "tool activity card")
}

func (b *Bridge) patchPermissionMessage(ctx context.Context, msgID string, text any) error {
	content := renderPermissionResolvedCard(text)
	return b.patchInteractiveMessage(ctx, msgID, content, "permission request card")
}

func (b *Bridge) patchInteractiveMessage(ctx context.Context, msgID, content, label string) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(msgID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(content).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("lark patch "+label+": %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark patch "+label+": code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
