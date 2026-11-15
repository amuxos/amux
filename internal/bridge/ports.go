package bridge

import (
	"context"
	"io"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// The port structs below group Bridge's injected dependencies by domain. They
// are embedded in Bridge so call sites keep using short field names, while the
// injected surface of each domain becomes an explicit, testable unit.

// identityPort resolves Feishu launch identities without coupling Bridge to the
// amuxd principal registry implementation.
type identityPort struct {
	resolveModelServicePrincipal func(string) (string, error)
	resolveExecutionIdentity     func(openID, executionRef, targetID string, shared bool) (LaunchIdentity, error)
}

// messagingPort is the outbound Feishu messaging surface used by Bridge.
type messagingPort struct {
	threadNotice        func(ctx context.Context, rootMsgID, text string) error
	chatNotice          func(ctx context.Context, chatID, text string) (msgID, threadID string, err error)
	dmNotice            func(ctx context.Context, openID, text string) (msgID, threadID string, err error)
	dmInteractive       func(ctx context.Context, openID, content string) (msgID, threadID string, err error)
	managementCardPatch func(ctx context.Context, messageID, content string) error
	reactionAdd         func(ctx context.Context, msgID, emojiType string) error
}

// lookupPort is the read-only Feishu lookup surface.
type lookupPort struct {
	sourceMessageLookup func(context.Context, string) (*larkim.Message, error)
	topicRootLookup     func(context.Context, string) (topicRootMessage, error)
	quotedMessageLookup func(context.Context, string) (feishuQuotedMessage, error)
	chatNameLookup      func(context.Context, string) (string, error)
	chatListLookup      func(context.Context) (map[string]string, error)
	chatBotCountLookup  func(context.Context, string) (int, error)
	botIdentityLookup   func(context.Context) (BotIdentity, error)
}

// documentCommentAPI is the document-comment delivery surface.
type documentCommentAPI struct {
	documentCommentFetch func(context.Context, documentCommentRef) (documentCommentSnapshot, error)
	documentCommentReply func(context.Context, documentCommentRef, string) (string, error)
	documentCommentReact func(context.Context, documentCommentRef, string, string) error
	documentSubscribe    func(context.Context) error
}

// externalBotPort is the bot-to-bot delegation surface.
type externalBotPort struct {
	botHandoff              func(context.Context, string, string, string) error
	botHandoffPrompt        func(string) (string, error)
	externalBotDelegation   func(context.Context, string, string, string, string) (string, string, error)
	externalBotCatalogFetch func(context.Context, string) (externalBotCatalogSnapshot, error)
}

// artifactPort is the artifact and material delivery surface.
type artifactPort struct {
	artifactUpload     func(context.Context, string, bool, []byte) (string, string, error)
	artifactSend       func(context.Context, *LarkChat, string, string, string) error
	fileResourceFetch  func(context.Context, string, string) (io.Reader, string, error)
	imageResourceFetch func(context.Context, string, string) (io.Reader, string, error)
}

// groupWatchAPI is the group-watch message listing surface.
type groupWatchAPI struct {
	groupWatchListMessages func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error)
}

// accessScopePort lists granted Feishu permission scopes per capability.
type accessScopePort struct {
	permissionScopeList func(context.Context) (map[string]int, error)
}

// subtaskCardPort supplies the dedicated subtask progress-card backend.
type subtaskCardPort struct {
	subtaskCardBackend func(*LarkChat) replyCardBackend
}
