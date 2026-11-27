package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestMergedMaterialsKeepContainerAuthority(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	msg := func(id, parent, kind, body string) *larkim.Message {
		return &larkim.Message{MessageId: str(id), UpperMessageId: str(parent), MsgType: str(kind), Body: &larkim.MessageBody{Content: str(body)}}
	}
	items := []*larkim.Message{
		msg("container", "", "merge_forward", "{}"),
		msg("child", "container", "text", `{"text":"转发内容"}`),
		msg("nested", "container", "merge_forward", "{}"),
		msg("picture", "nested", "image", `{"image_key":"img-k"}`),
		msg("file", "nested", "file", `{"file_key":"file-k","file_name":"../报告.csv"}`),
		msg("unrelated", "other", "image", `{"image_key":"secret"}`),
	}
	text, m, err := expandMessageMaterials("container", items)
	if err != nil || !strings.Contains(text, "转发内容") || len(m.images) != 1 || len(m.files) != 1 {
		t.Fatalf("expand %q %#v %v", text, m, err)
	}
	if m.images[0].messageID != "container" || m.files[0].messageID != "container" || m.files[0].name != "报告.csv" {
		t.Fatalf("resource authority %#v", m)
	}
	for i := 0; i < 60; i++ {
		items = append(items, msg(strings.Repeat("x", i+1), "container", "text", `{"text":"more"}`))
	}
	_, m, err = expandMessageMaterials("container", items)
	if err != nil || len(m.notices) == 0 {
		t.Fatal("missing truncation notice", err)
	}
}

func TestMergedMaterialsSameChatResourceAuthority(t *testing.T) {
	t.Parallel()
	for _, chatID := range []string{"oc_current", "oc_other", ""} {
		t.Run(chatID, func(t *testing.T) {
			items := []*larkim.Message{
				{MessageId: testStringPtr("container"), ChatId: testStringPtr("oc_current"), MsgType: testStringPtr("merge_forward"), Body: &larkim.MessageBody{Content: testStringPtr("Merged and Forwarded Message")}},
				{MessageId: testStringPtr("image"), ChatId: testStringPtr(chatID), UpperMessageId: testStringPtr("container"), MsgType: testStringPtr("image"), Body: &larkim.MessageBody{Content: testStringPtr(`{"image_key":"original-image-key"}`)}},
				{MessageId: testStringPtr("file"), ChatId: testStringPtr(chatID), UpperMessageId: testStringPtr("container"), MsgType: testStringPtr("file"), Body: &larkim.MessageBody{Content: testStringPtr(`{"file_key":"original-file-key","file_name":"input.csv"}`)}},
			}
			_, m, err := expandMessageMaterials("container", items)
			if err != nil || len(m.images) != 1 || len(m.files) != 1 {
				t.Fatalf("materials=%+v err=%v", m, err)
			}
			wantImage, wantFile := "container", "container"
			if chatID == "oc_current" {
				wantImage, wantFile = "image", "file"
			}
			if m.images[0].messageID != wantImage || m.files[0].messageID != wantFile {
				t.Fatalf("resource authority image=%s file=%s; want %s/%s", m.images[0].messageID, m.files[0].messageID, wantImage, wantFile)
			}
		})
	}
}

type materialTestHub struct {
	bridgeTestHub
	writes   []*pb.SessionFileRequest
	reads    []*pb.SessionFileRequest
	readData []byte
}

func (h *materialTestHub) Route(ctx context.Context, target, method string, payload interface{}) (*pb.TunnelMsg, error) {
	var result interface{}
	switch method {
	case "PutSessionFile":
		r := payload.(*pb.SessionFileRequest)
		h.writes = append(h.writes, r)
		result = &pb.SessionFileResponse{Path: "/target/amux-inbox/digest/" + r.FileName}
	case "ReadSessionArtifact":
		r := payload.(*pb.SessionFileRequest)
		h.reads = append(h.reads, r)
		result = &pb.SessionFileResponse{Data: h.readData}
	default:
		return h.bridgeTestHub.Route(ctx, target, method, payload)
	}
	data, _ := json.Marshal(result)
	return &pb.TunnelMsg{Payload: data}, nil
}
func TestFileMaterializesOnTargetAndReportsPartialFailure(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	h := &materialTestHub{}
	b.hub = h
	h.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Id: "session-id", Name: "s1", LaunchId: "launch-id"}}}
	var notices []string
	b.threadNotice = func(_ context.Context, _ string, text string) error { notices = append(notices, text); return nil }
	b.fileResourceFetch = func(_ context.Context, id, key string) (io.Reader, string, error) {
		if id != "message" {
			t.Fatal(id)
		}
		if key == "bad" {
			return nil, "", errors.New("denied")
		}
		return strings.NewReader("payload"), "input.txt", nil
	}
	ctx := withFeishuImages(context.Background(), feishuImageContext{chatID: "chat"})
	collectMaterials(ctx, "message", messageMaterials{files: []feishuFileRef{{"message", "good", "input.txt"}, {"message", "bad", "failed.txt"}}})
	_, text, err := b.prepareFileMaterials(ctx, &LarkChat{TargetID: "target", SessionName: "s1", ChatID: "chat", RootMsgID: "root"}, "分析附件")
	if err != nil || !strings.Contains(text, "/target/amux-inbox/") || len(h.writes) != 1 || len(notices) != 1 {
		t.Fatalf("prepare %q %v writes=%d notices=%v", text, err, len(h.writes), notices)
	}
	if r := h.writes[0]; r.SessionId != "session-id" || r.LaunchId != "launch-id" || string(r.Data) != "payload" {
		t.Fatalf("unfrozen identity %+v", r)
	}
	if !strings.Contains(text, "failed.txt") {
		t.Fatal("agent did not learn about omitted file")
	}
}
func TestQuotedImageCollectedOnlyAfterChatValidation(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	q := feishuQuotedMessage{MessageID: "quoted", ChatID: "other", MsgType: "image", Text: "[图片素材]", Materials: messageMaterials{images: []feishuImageRef{{"quoted", "img"}}}}
	b.quotedMessageLookup = func(context.Context, string) (feishuQuotedMessage, error) { return q, nil }
	ctx := withFeishuImages(context.Background(), feishuImageContext{chatID: "chat"})
	ctx = withFeishuQuotedMessage(ctx, feishuQuotedMessageRef{"quoted", "chat"})
	if _, _, err := b.resolveFeishuQuotedMessage(ctx); err == nil || len(materialsFromContext(ctx).images) != 0 {
		t.Fatal("cross-chat resource collected")
	}
	q.ChatID = "chat"
	if _, _, err := b.resolveFeishuQuotedMessage(ctx); err != nil || !hasFeishuMaterials(ctx) {
		t.Fatal("quoted resource missing", err)
	}
}
func TestPartialImageNotice(t *testing.T) {
	b, h := newBridgeForInputTest(t)
	h.targets = []*pb.Target{{Id: "target", Sessions: []*pb.Session{{Name: "s1", InputModalities: []string{"text", "image"}}}}}
	var notices []string
	b.threadNotice = func(_ context.Context, _ string, text string) error { notices = append(notices, text); return nil }
	png := bridgeTestPNG(t)
	b.imageResourceFetch = func(_ context.Context, _, key string) (io.Reader, string, error) {
		if key == "bad" {
			return nil, "", errors.New("denied")
		}
		return bytes.NewReader(png), "x.png", nil
	}
	ctx := withFeishuImages(context.Background(), feishuImageContext{refs: []feishuImageRef{{"msg", "good"}, {"msg", "bad"}}})
	parts, ok, err := b.multipartSubmitParts(ctx, &LarkChat{TargetID: "target", SessionName: "s1", RootMsgID: "root"}, "look")
	if err != nil || !ok || len(parts) != 3 || len(notices) != 1 || !strings.Contains(notices[0], "1 张") {
		t.Fatalf("parts=%v notices=%v err=%v", parts, notices, err)
	}
}
