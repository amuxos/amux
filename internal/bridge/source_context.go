package bridge

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

const sourceContextStart = "\n\n[amux-source-context]\n"
const sourceContextEnd = "\n[/amux-source-context]"

type feishuSource struct {
	ChatID        string
	RootMessageID string
}

type feishuSourceKey struct{}

func withFeishuSource(ctx context.Context, source feishuSource) context.Context {
	return context.WithValue(ctx, feishuSourceKey{}, source)
}

func feishuSourceForInput(ctx context.Context, chat *LarkChat) feishuSource {
	if source, ok := ctx.Value(feishuSourceKey{}).(feishuSource); ok {
		return source
	}
	root := groupInputRoot(ctx, chat.RootMsgID)
	if root == singletonStreamKey(chat.BridgeId) {
		root = ""
	}
	return feishuSource{ChatID: chat.ChatID, RootMessageID: root}
}

type feishuSourceContext struct {
	Platform             string `json:"platform"`
	BridgeID             string `json:"bridge_id"`
	TargetID             string `json:"target_id"`
	SessionName          string `json:"session_name"`
	ChatID               string `json:"chat_id"`
	MessageID            string `json:"message_id"`
	RootMessageID        string `json:"root_message_id,omitempty"`
	ThreadID             string `json:"thread_id,omitempty"`
	ConversationURL      string `json:"conversation_url,omitempty"`
	ConversationURLError string `json:"conversation_url_error,omitempty"`
}

func (b *Bridge) sourceContextInstructions(ctx context.Context, chat *LarkChat, inputs []turnInputRef) string {
	messageID := ""
	for _, input := range inputs {
		if messageID = strings.TrimSpace(input.MessageID); messageID != "" {
			break
		}
	}
	if messageID == "" {
		return ""
	}
	source := feishuSourceForInput(ctx, chat)
	data := feishuSourceContext{
		Platform: "feishu", BridgeID: b.currentBridgeId(), TargetID: chat.TargetID,
		SessionName: chat.SessionName, ChatID: strings.TrimSpace(source.ChatID),
		MessageID: messageID, RootMessageID: strings.TrimSpace(source.RootMessageID),
	}
	if data.RootMessageID == chat.RootMsgID && strings.HasPrefix(chat.ThreadID, "omt_") {
		data.ThreadID = chat.ThreadID
	}
	b.resolveSourceContext(ctx, chat, &data)
	raw, _ := json.Marshal(data)
	return sourceContextStart + sessionMessage("source.context", string(raw)).For(b.guideLocale(chat.OpenId)) + sourceContextEnd
}

func (b *Bridge) resolveSourceContext(ctx context.Context, chat *LarkChat, data *feishuSourceContext) {
	if chat.BridgeId != "" && chat.BridgeId != b.currentBridgeId() {
		data.ConversationURLError = "source_binding_mismatch"
		return
	}
	if data.ChatID == "" {
		data.ConversationURLError = "missing_source_chat"
		return
	}
	lookup := b.sourceMessageLookup
	if lookup == nil {
		lookup = b.fetchSourceMessage
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	message, err := lookup(lookupCtx, data.MessageID)
	if err != nil {
		data.ConversationURLError = "source_lookup_failed"
		return
	}
	if message == nil || stringPtr(message.MessageId) != data.MessageID {
		data.ConversationURLError = "source_message_mismatch"
		return
	}
	if stringPtr(message.ChatId) != data.ChatID {
		data.ConversationURLError = "source_chat_mismatch"
		return
	}
	root := strings.TrimSpace(stringPtr(message.RootId))
	if root == "" {
		root = data.MessageID
	}
	if data.RootMessageID != "" && data.RootMessageID != root {
		data.ConversationURLError = "source_root_mismatch"
		return
	}
	if message.Deleted != nil && *message.Deleted {
		data.ConversationURLError = "source_message_deleted"
		return
	}
	expectsThread := data.ThreadID != "" || root != data.MessageID
	data.RootMessageID = root
	data.ThreadID = strings.TrimSpace(stringPtr(message.ThreadId))
	link := stringPtr(message.MessageAppLink)
	if link == "" {
		if (data.ThreadID == "" && expectsThread) || (data.ThreadID != "" && !strings.HasPrefix(data.ThreadID, "omt_")) {
			data.ConversationURLError = "source_thread_unavailable"
			return
		}
		data.ConversationURL = feishuConversationURL(data.ChatID, data.ThreadID)
		return
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		data.ConversationURLError = "source_link_invalid"
		return
	}
	data.ConversationURL = link
}
