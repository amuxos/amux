package bridge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/feishumsg"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// HasAgentMessageBinding checks every supported conversation binding without
// exposing credentials or accepting a Bot selector from the Agent.
func (b *Bridge) HasAgentMessageBinding(targetID, sessionName string) (bool, error) {
	if b == nil || b.db == nil || b.currentBridgeId() == "" {
		return false, nil
	}
	return b.db.hasAgentMessageBinding(b.currentBridgeId(), targetID, sessionName)
}

// SendAgentMessage uses this Bridge's tenant credentials only. The Hub verifies
// the caller and resolves a unique Bot before entering this boundary.
func (b *Bridge) SendAgentMessage(ctx context.Context, targetID, sessionName string, msg feishumsg.Message) (feishumsg.Result, error) {
	if err := msg.Validate(); err != nil {
		return feishumsg.Result{}, err
	}
	if msg.ReceiveIDType == feishumsg.Self {
		return feishumsg.Result{}, errors.New(feishumsg.Copy("group_only"))
	}
	bound, err := b.HasAgentMessageBinding(targetID, sessionName)
	if err != nil {
		return feishumsg.Result{}, errors.New(feishumsg.Copy("lookup", err))
	}
	if !bound {
		return feishumsg.Result{}, errors.New(feishumsg.Copy("unbound"))
	}
	if b.client == nil {
		return feishumsg.Result{}, errors.New(feishumsg.Copy("unavailable"))
	}
	if err := b.requireAgentMessageGroup(ctx, msg); err != nil {
		return feishumsg.Result{}, err
	}
	// Scope duplicate suppression to the originating Session, including when
	// several Sessions use the same Bot and caller-selected key.
	requestUUID := ""
	if msg.UUID != "" {
		digest := sha256.Sum256([]byte(targetID + "\x00" + sessionName + "\x00" + msg.UUID))
		requestUUID = fmt.Sprintf("%x", digest[:16])
	}
	var result feishumsg.Result
	if strings.TrimSpace(msg.ReplyToMessageID) != "" {
		resp, err := b.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
			MessageId(msg.ReplyToMessageID).Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msg.MessageType()).Content(msg.Content()).ReplyInThread(msg.ReplyInThread).Uuid(requestUUID).Build()).Build())
		if err != nil {
			return result, errors.New(feishumsg.Copy("failed", err))
		}
		if resp == nil {
			return result, errors.New(feishumsg.Copy("empty_response"))
		}
		if !resp.Success() {
			return result, errors.New(feishumsg.Copy("failed", fmt.Sprintf("code=%d msg=%s", resp.Code, resp.Msg)))
		}
		if resp.Data != nil {
			result = feishumsg.Result{MessageID: stringPtr(resp.Data.MessageId), ChatID: stringPtr(resp.Data.ChatId), ThreadID: stringPtr(resp.Data.ThreadId)}
		}
	} else {
		resp, err := b.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(msg.ReceiveIDType).Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(msg.ReceiveID).MsgType(msg.MessageType()).Content(msg.Content()).Uuid(requestUUID).Build()).Build())
		if err != nil {
			return result, errors.New(feishumsg.Copy("failed", err))
		}
		if resp == nil {
			return result, errors.New(feishumsg.Copy("empty_response"))
		}
		if !resp.Success() {
			return result, errors.New(feishumsg.Copy("failed", fmt.Sprintf("code=%d msg=%s", resp.Code, resp.Msg)))
		}
		if resp.Data != nil {
			result = feishumsg.Result{MessageID: stringPtr(resp.Data.MessageId), ChatID: stringPtr(resp.Data.ChatId), ThreadID: stringPtr(resp.Data.ThreadId)}
		}
	}
	if strings.TrimSpace(result.MessageID) == "" {
		return result, errors.New(feishumsg.Copy("empty_response"))
	}
	return result, nil
}

// Check platform metadata with the sending Bot's credentials. A chat_id alone
// does not distinguish a group from a private chat, including for replies.
func (b *Bridge) requireAgentMessageGroup(ctx context.Context, msg feishumsg.Message) error {
	chatID := msg.ReceiveID
	if strings.TrimSpace(msg.ReplyToMessageID) != "" {
		resp, err := b.client.Im.Message.Get(ctx, larkim.NewGetMessageReqBuilder().MessageId(msg.ReplyToMessageID).Build())
		if err != nil {
			return errors.New(feishumsg.Copy("destination_lookup", err))
		}
		if resp == nil {
			return errors.New(feishumsg.Copy("destination_metadata"))
		}
		if !resp.Success() {
			return errors.New(feishumsg.Copy("destination_lookup", fmt.Sprintf("code=%d msg=%s", resp.Code, resp.Msg)))
		}
		if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
			return errors.New(feishumsg.Copy("destination_metadata"))
		}
		parent := resp.Data.Items[0]
		if stringPtr(parent.MessageId) != msg.ReplyToMessageID || strings.TrimSpace(stringPtr(parent.ChatId)) == "" || (parent.Deleted != nil && *parent.Deleted) {
			return errors.New(feishumsg.Copy("destination_metadata"))
		}
		chatID = *parent.ChatId
	}
	resp, err := b.client.Im.Chat.Get(ctx, larkim.NewGetChatReqBuilder().ChatId(chatID).Build())
	if err != nil {
		return errors.New(feishumsg.Copy("destination_lookup", err))
	}
	if resp == nil {
		return errors.New(feishumsg.Copy("destination_metadata"))
	}
	if !resp.Success() {
		return errors.New(feishumsg.Copy("destination_lookup", fmt.Sprintf("code=%d msg=%s", resp.Code, resp.Msg)))
	}
	if resp.Data == nil {
		return errors.New(feishumsg.Copy("destination_metadata"))
	}
	switch stringPtr(resp.Data.ChatMode) {
	case "group", "topic":
		return nil
	case "p2p":
		return errors.New(feishumsg.Copy("group_only"))
	default:
		return errors.New(feishumsg.Copy("destination_metadata"))
	}
}
