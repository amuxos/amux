package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type fakeFeishuMessageClient struct {
	pb.AmuxdClient
	calls int
	req   *pb.SendFeishuMessageRequest
	err   error
}

func (f *fakeFeishuMessageClient) SendFeishuMessage(_ context.Context, req *pb.SendFeishuMessageRequest, _ ...grpc.CallOption) (*pb.SendFeishuMessageResponse, error) {
	f.calls++
	f.req = req
	return &pb.SendFeishuMessageResponse{MessageId: "om-sent", BotProfile: "bound-bot"}, f.err
}

func TestMCPFeishuSendMessage(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.feishu_send_message")
	var schema struct {
		Properties           map[string]any
		AdditionalProperties bool
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties || len(schema.Properties) != 7 || schema.Properties["session_id"] != nil || schema.Properties["bot_profile"] != nil {
		t.Fatalf("schema permits identity overrides: %+v", schema)
	}
	if !strings.Contains(tool.Description, "user authorization") || !strings.Contains(tool.Description, "授权") {
		t.Fatal("missing bilingual guidance")
	}
	fake := &fakeFeishuMessageClient{}
	old := newFeishuMessageClient
	newFeishuMessageClient = func() (pb.AmuxdClient, func() error, error) { return fake, func() error { return nil }, nil }
	t.Cleanup(func() { newFeishuMessageClient = old })
	t.Setenv("AMUX_SESSION_ID", " current-session ")
	h := &mcpHandler{}
	result, err := h.handle(context.Background(), tool.Name, json.RawMessage(`{"receive_id":"oc-target","receive_id_type":"chat_id","text":"hello","uuid":"same-key"}`))
	if err != nil || result.IsError || fake.calls != 1 || fake.req.GetSessionId() != "current-session" || fake.req.GetReceiveId() != "oc-target" || fake.req.GetUuid() != "same-key" {
		t.Fatalf("send: %+v %v request=%+v", result, err, fake.req)
	}
	if !strings.Contains(result.Content[0].Text, "om-sent") {
		t.Fatal("missing delivery receipt")
	}
	for _, args := range []string{
		`{"text":"hello"}`,
		`{"receive_id":"oc-a","receive_id_type":"chat_id","reply_to_message_id":"om-a","text":"hello"}`,
		`{"reply_to_message_id":"om-a","text":"hello","session_id":"foreign"}`,
		`{"reply_to_message_id":"om-a","text":"hello","bot_profile":"foreign"}`,
		`{"reply_to_message_id":"om-a","text":"hello","format":"html"}`,
		`{"reply_to_message_id":"om-a","text":"hello"} {}`,
		`null`,
	} {
		result, err := h.handle(context.Background(), tool.Name, json.RawMessage(args))
		if err != nil || !result.IsError || fake.calls != 1 {
			t.Fatalf("invalid input reached RPC: %s %+v %v", args, result, err)
		}
	}
	t.Setenv("AMUX_SESSION_ID", "")
	result, _ = h.handle(context.Background(), tool.Name, json.RawMessage(`{"reply_to_message_id":"om-a","text":"hello"}`))
	if !result.IsError || fake.calls != 1 {
		t.Fatal("unbound host reached RPC")
	}
	t.Setenv("AMUX_SESSION_ID", "current-session")
	fake.err = errors.New("delivery uncertain")
	result, _ = h.handle(context.Background(), tool.Name, json.RawMessage(`{"reply_to_message_id":"om-a","text":"hello"}`))
	if !result.IsError || fake.req.GetUuid() == "" || !strings.Contains(result.Content[0].Text, fake.req.GetUuid()) {
		t.Fatal("failed send lost retry key")
	}
}

func TestMCPFeishuMessageFormats(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.feishu_send_message")
	var schema struct {
		Properties map[string]struct {
			Enum    []string
			Default string
		}
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	format := schema.Properties["format"]
	if format.Default != "text" || strings.Join(format.Enum, ",") != "text,markdown" {
		t.Fatalf("format schema: %+v", format)
	}
	fake := &fakeFeishuMessageClient{}
	old := newFeishuMessageClient
	newFeishuMessageClient = func() (pb.AmuxdClient, func() error, error) { return fake, func() error { return nil }, nil }
	t.Cleanup(func() { newFeishuMessageClient = old })
	t.Setenv("AMUX_SESSION_ID", "current-session")
	for _, format := range []string{"text", "markdown"} {
		t.Run(format, func(t *testing.T) {
			args, _ := json.Marshal(map[string]any{"reply_to_message_id": "om-a", "reply_in_thread": true, "text": "**结果**\n\n- 完成", "format": format})
			result, err := (&mcpHandler{}).handle(context.Background(), tool.Name, args)
			if err != nil || result.IsError || fake.req.GetFormat() != format || fake.req.GetText() != "**结果**\n\n- 完成" || !fake.req.GetReplyInThread() {
				t.Fatalf("format lost in RPC: %+v, %v, %+v", result, err, fake.req)
			}
		})
	}
}
