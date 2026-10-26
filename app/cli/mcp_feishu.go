package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
)

var newFeishuMessageClient = newAmuxdClient

func feishuMessageTool() mcp.Tool {
	properties := map[string]any{}
	for _, name := range []string{"receive_id", "receive_id_type", "reply_to_message_id", "reply_in_thread", "text", "format", "uuid"} {
		kind := "string"
		if name == "reply_in_thread" {
			kind = "boolean"
		}
		properties[name] = map[string]any{"type": kind, "description": feishumsg.Copy(name)}
	}
	properties["receive_id_type"].(map[string]any)["enum"] = []string{"chat_id", "open_id", "user_id", "union_id", "email"}
	properties["format"].(map[string]any)["enum"] = []string{"text", "markdown"}
	properties["format"].(map[string]any)["default"] = "text"
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": []string{"text"}, "additionalProperties": false})
	return mcp.Tool{Name: "amux.feishu_send_message", Description: feishumsg.Copy("description"), InputSchema: schema}
}

func (h *mcpHandler) feishuSendMessage(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input feishumsg.Message
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return mcp.ErrorText(feishumsg.Copy("invalid", err)), nil
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return mcp.ErrorText(feishumsg.Copy("json_object")), nil
	}
	if err := input.Validate(); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	sessionID := strings.TrimSpace(os.Getenv("AMUX_SESSION_ID"))
	if sessionID == "" {
		return mcp.ErrorText(feishumsg.Copy("session")), nil
	}
	if input.UUID == "" {
		input.UUID = uuid.NewString()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, closeFn, err := newFeishuMessageClient()
	if err != nil {
		return mcp.ErrorText(feishumsg.Copy("request_failed", input.UUID, err)), nil
	}
	defer closeFn()
	resp, err := client.SendFeishuMessage(ctx, &pb.SendFeishuMessageRequest{SessionId: sessionID, ReceiveId: input.ReceiveID, ReceiveIdType: input.ReceiveIDType, ReplyToMessageId: input.ReplyToMessageID, ReplyInThread: input.ReplyInThread, Text: input.Text, Format: input.Format, Uuid: input.UUID})
	if err != nil {
		return mcp.ErrorText(feishumsg.Copy("request_failed", input.UUID, err)), nil
	}
	if resp.GetMessageId() == "" {
		return mcp.ErrorText(feishumsg.Copy("request_failed", input.UUID, feishumsg.Copy("empty_response"))), nil
	}
	return mcp.Text(toJSON(map[string]any{"schema_version": "mcp.feishu_send_message.v1", "message_id": resp.GetMessageId(), "chat_id": resp.GetChatId(), "thread_id": resp.GetThreadId(), "bot_profile": resp.GetBotProfile(), "uuid": input.UUID})), nil
}
