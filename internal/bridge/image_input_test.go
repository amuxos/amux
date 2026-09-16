package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"sync"
	"testing"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type delayedImageCapabilityHub struct {
	mu        sync.Mutex
	listCalls int
	targets   []*pb.Target
	lists     []*pb.ListResponse
}

func (h *delayedImageCapabilityHub) ListTargets() []*pb.Target { return h.targets }

func (h *delayedImageCapabilityHub) Route(_ context.Context, _, method string, _ interface{}) (*pb.TunnelMsg, error) {
	if method != "List" {
		return nil, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	index := h.listCalls
	h.listCalls++
	if index >= len(h.lists) {
		index = len(h.lists) - 1
	}
	raw, _ := json.Marshal(h.lists[index])
	return &pb.TunnelMsg{Payload: raw}, nil
}

func (h *delayedImageCapabilityHub) RouteStream(context.Context, string, string, interface{}, func(*pb.TunnelMsg) error) error {
	return nil
}

func bridgeTestPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestParseFeishuImageRefs(t *testing.T) {
	standalone := `{"image_key":"img-1"}`
	refs := parseFeishuImageRefs("om-1", &standalone)
	if len(refs) != 1 || refs[0].messageID != "om-1" || refs[0].imageKey != "img-1" {
		t.Fatalf("standalone refs = %#v", refs)
	}
	post := `{"content":[[{"tag":"text","text":"look"},{"tag":"img","image_key":"img-2"}],[{"tag":"img","image_key":"img-3"}]]}`
	refs = parseFeishuImageRefs("om-2", &post)
	if len(refs) != 2 || refs[0].imageKey != "img-2" || refs[1].imageKey != "img-3" {
		t.Fatalf("post refs = %#v", refs)
	}
	markdownPost := `{"content":[[{"tag":"md","text":"before ![first](img_v3_first) after ![remote](https://example.com/image.png)"}],[{"tag":"md","text":"![second](img_v3_second)"}]]}`
	refs = parseFeishuImageRefs("om-3", &markdownPost)
	if len(refs) != 2 || refs[0].messageID != "om-3" || refs[0].imageKey != "img_v3_first" || refs[1].imageKey != "img_v3_second" {
		t.Fatalf("markdown post refs = %#v", refs)
	}
}

func TestBotToBotHandoffSubmitsNativePostImage(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open", BotName: "amux", BotOpenId: "ou-amux"}
	b.botToBot = config.BridgeFeishuBotToBotConfig{Enabled: true, MaxTurnsPerThread: 2}
	setSharedWorkspace(t, b, "oc-workspace")
	insertActiveChat(t, b.db)
	b.threadNotice = func(context.Context, string, string) error { return nil }
	hub.targets = []*pb.Target{{Id: "sb-1", Sessions: []*pb.Session{{
		Name: "s1", State: "running", InputModalities: []string{"text", "image"},
	}}}}

	imageData := bridgeTestPNG(t)
	b.imageResourceFetch = func(_ context.Context, messageID, imageKey string) (io.Reader, string, error) {
		if messageID != "om-bot-image" || imageKey != "img-bot" {
			t.Fatalf("image resource = %q/%q, want om-bot-image/img-bot", messageID, imageKey)
		}
		return bytes.NewReader(imageData), "bot-image.png", nil
	}
	event := feishuBotMessageEvent("topic_group", "oc-workspace", "root-1", "om-bot-image", "ou-external-bot", "")
	event.Event.Message.MessageType = testStringPtr("post")
	event.Event.Message.Content = testStringPtr(`{"title":"","content":[[{"tag":"text","text":"@_bot inspect the image"},{"tag":"img","image_key":"img-bot"}]]}`)
	addStructuredBotMention(event, "@_bot", "amux", "ou-amux")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(hub.routes) != 1 || hub.routes[0].method != "Submit" {
		t.Fatalf("routes = %+v, want one Submit", hub.routes)
	}
	request := hub.routes[0].payload.(*pb.SubmitRequest)
	if len(request.Parts) != 2 || request.Parts[0].GetText() == "" {
		t.Fatalf("parts = %+v, want handoff text followed by one image", request.Parts)
	}
	imagePart := request.Parts[1].GetImage()
	if imagePart == nil || imagePart.MimeType != "image/png" || imagePart.DisplayName != "bot-image.png" || !bytes.Equal(imagePart.Data, imageData) {
		t.Fatalf("image part = %+v, want original PNG attachment", imagePart)
	}
}

func TestMultipartSubmitDoesNotDownloadForUnsupportedModel(t *testing.T) {
	hub := &bridgeTestHub{targets: []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{Name: "s1", EffectiveModel: "text-model", InputModalities: []string{"text"}}}}}}
	b := &Bridge{
		hub:              hub,
		materialDelivery: materialDelivery{imageNotices: make(map[string]struct{})},
		messagingPort:    messagingPort{threadNotice: func(context.Context, string, string) error { return nil }},
	}
	fetches := 0
	b.imageResourceFetch = func(context.Context, string, string) (io.Reader, string, error) {
		fetches++
		return bytes.NewReader(nil), "", nil
	}
	ctx := withFeishuImages(context.Background(), feishuImageContext{refs: []feishuImageRef{{messageID: "om", imageKey: "secret-key"}}})
	parts, submit, err := b.multipartSubmitParts(ctx, &LarkChat{TargetID: "t1", SessionName: "s1", RootMsgID: "root"}, "continue")
	if err != nil || !submit || parts != nil || fetches != 0 {
		t.Fatalf("parts=%v submit=%v fetches=%d err=%v", parts, submit, fetches, err)
	}
	_, submit, err = b.multipartSubmitParts(ctx, &LarkChat{TargetID: "t1", SessionName: "s1", RootMsgID: "root"}, "")
	if err != nil || submit || fetches != 0 {
		t.Fatalf("image-only submit=%v fetches=%d err=%v", submit, fetches, err)
	}
}

func TestMultipartSubmitAcceptsAtMostTenImages(t *testing.T) {
	hub := &bridgeTestHub{targets: []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{Name: "s1", InputModalities: []string{"text", "image"}}}}}}
	b := &Bridge{
		hub:           hub,
		messagingPort: messagingPort{threadNotice: func(context.Context, string, string) error { return nil }},
	}
	data := bridgeTestPNG(t)
	fetches := 0
	b.imageResourceFetch = func(context.Context, string, string) (io.Reader, string, error) {
		fetches++
		return bytes.NewReader(data), "image.png", nil
	}
	refs := make([]feishuImageRef, 11)
	for i := range refs {
		refs[i] = feishuImageRef{messageID: "om", imageKey: "key"}
	}
	ctx := withFeishuImages(context.Background(), feishuImageContext{refs: refs})
	parts, submit, err := b.multipartSubmitParts(ctx, &LarkChat{TargetID: "t1", SessionName: "s1"}, "inspect")
	if err != nil || !submit || fetches != 10 || len(parts) != 12 {
		t.Fatalf("parts=%d submit=%v fetches=%d err=%v", len(parts), submit, fetches, err)
	}
}

func TestMultipartSubmitNotifiesWhenAllImagesFailButContinuesText(t *testing.T) {
	hub := &bridgeTestHub{targets: []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{Name: "s1", InputModalities: []string{"text", "image"}}}}}}
	var notice string
	b := &Bridge{
		hub:              hub,
		materialDelivery: materialDelivery{imageNotices: make(map[string]struct{})},
		messagingPort: messagingPort{threadNotice: func(_ context.Context, _, text string) error {
			notice = text
			return nil
		}},
	}
	b.imageResourceFetch = func(context.Context, string, string) (io.Reader, string, error) {
		return nil, "", errors.New("download unavailable")
	}
	ctx := withFeishuImages(context.Background(), feishuImageContext{refs: []feishuImageRef{{messageID: "om", imageKey: "key"}}})
	parts, submit, err := b.multipartSubmitParts(ctx, &LarkChat{TargetID: "t1", SessionName: "s1", RootMsgID: "root"}, "inspect")
	if err != nil || !submit || parts != nil {
		t.Fatalf("parts=%v submit=%v err=%v", parts, submit, err)
	}
	if notice != "图片暂时无法读取，已继续处理文字。" {
		t.Fatalf("notice = %q", notice)
	}
}

func TestMultipartSubmitWaitsForFreshSessionImageCapability(t *testing.T) {
	hub := &delayedImageCapabilityHub{
		targets: []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{Name: "s1", State: "running"}}}},
		lists: []*pb.ListResponse{
			{Sessions: []*pb.Session{{Name: "s1", State: "running"}}},
			{Sessions: []*pb.Session{{Name: "s1", State: "running", InputModalities: []string{"text", "image"}, InputCapabilitySource: "codex:default"}}},
		},
	}
	b := &Bridge{
		hub:           hub,
		messagingPort: messagingPort{threadNotice: func(context.Context, string, string) error { return nil }},
	}
	data := bridgeTestPNG(t)
	fetches := 0
	b.imageResourceFetch = func(context.Context, string, string) (io.Reader, string, error) {
		fetches++
		return bytes.NewReader(data), "image.png", nil
	}
	ctx := withFeishuImages(context.Background(), feishuImageContext{refs: []feishuImageRef{{messageID: "om", imageKey: "key"}}})
	parts, submit, err := b.multipartSubmitParts(ctx, &LarkChat{TargetID: "t1", SessionName: "s1"}, "inspect")
	if err != nil || !submit || fetches != 1 || len(parts) != 2 || parts[1].GetImage() == nil {
		t.Fatalf("parts=%v submit=%v fetches=%d err=%v", parts, submit, fetches, err)
	}
	if hub.listCalls != 2 {
		t.Fatalf("List calls = %d, want 2", hub.listCalls)
	}
}
