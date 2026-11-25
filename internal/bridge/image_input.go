package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/gif"
	"image/png"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/sessioninput"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type feishuImageRef struct {
	messageID string
	imageKey  string
}

type feishuImageContext struct {
	refs      []feishuImageRef
	chatID    string
	openID    string
	replyKind string
}

type feishuImageContextKey struct{}

const (
	imageCapabilityPollInterval = 50 * time.Millisecond
	imageCapabilityWaitTimeout  = 2 * time.Second
)

var feishuMarkdownImagePattern = regexp.MustCompile(`!\[[^\]\r\n]*\]\((img_[A-Za-z0-9_-]+)\)`)

func withFeishuImages(ctx context.Context, value feishuImageContext) context.Context {
	ctx = withMaterialCollection(ctx)
	return context.WithValue(ctx, feishuImageContextKey{}, value)
}

func feishuImagesFromContext(ctx context.Context) feishuImageContext {
	value, _ := ctx.Value(feishuImageContextKey{}).(feishuImageContext)
	return value
}

func parseFeishuImageRefs(messageID string, content *string) []feishuImageRef {
	if content == nil || strings.TrimSpace(messageID) == "" {
		return nil
	}
	var body struct {
		ImageKey string          `json:"image_key"`
		Content  [][]postContent `json:"content"`
	}
	if json.Unmarshal([]byte(*content), &body) != nil {
		return nil
	}
	var refs []feishuImageRef
	if strings.TrimSpace(body.ImageKey) != "" {
		refs = append(refs, feishuImageRef{messageID: messageID, imageKey: body.ImageKey})
	}
	for _, line := range body.Content {
		for _, item := range line {
			if item.Tag == "img" && strings.TrimSpace(item.ImageKey) != "" {
				refs = append(refs, feishuImageRef{messageID: messageID, imageKey: item.ImageKey})
			}
			if item.Tag == "md" {
				for _, match := range feishuMarkdownImagePattern.FindAllStringSubmatch(item.Text, -1) {
					refs = append(refs, feishuImageRef{messageID: messageID, imageKey: match[1]})
				}
			}
		}
	}
	return refs
}

func (b *Bridge) sessionInputCapabilities(ctx context.Context, chat *LarkChat) (*pb.Session, bool) {
	if b == nil || b.hub == nil || chat == nil {
		return nil, false
	}
	var lastSeen *pb.Session
	for _, target := range b.hub.ListTargets() {
		if target == nil || target.Id != chat.TargetID {
			continue
		}
		for _, candidate := range target.Sessions {
			if candidate != nil && candidate.Name == chat.SessionName {
				lastSeen = candidate
				if supported, known := imageCapability(candidate); known {
					return candidate, supported
				}
			}
		}
	}

	timer := time.NewTimer(imageCapabilityWaitTimeout)
	defer timer.Stop()
	for {
		candidate := b.listSession(ctx, chat.TargetID, chat.SessionName)
		if candidate != nil {
			lastSeen = candidate
			if supported, known := imageCapability(candidate); known {
				return candidate, supported
			}
		}
		select {
		case <-ctx.Done():
			return lastSeen, false
		case <-timer.C:
			return lastSeen, false
		case <-time.After(imageCapabilityPollInterval):
		}
	}
}

func imageCapability(sess *pb.Session) (supported, known bool) {
	if sess == nil {
		return false, false
	}
	if sessioninput.SupportsImage(sess.InputModalities) {
		return true, true
	}
	return false, len(sess.InputModalities) > 0 || strings.TrimSpace(sess.InputCapabilitySource) != ""
}

func (b *Bridge) listSession(ctx context.Context, targetID, sessionName string) *pb.Session {
	resp, err := b.hub.Route(ctx, targetID, "List", &pb.ListRequest{})
	if err != nil || resp == nil {
		return nil
	}
	var listed pb.ListResponse
	if json.Unmarshal(resp.Payload, &listed) != nil {
		return nil
	}
	for _, candidate := range listed.Sessions {
		if candidate != nil && candidate.Name == sessionName {
			return candidate
		}
	}
	return nil
}

func (b *Bridge) multipartSubmitParts(ctx context.Context, chat *LarkChat, text string) ([]*pb.InputPart, bool, error) {
	imageCtx := feishuImagesFromContext(ctx)
	if len(imageCtx.refs) == 0 {
		return nil, true, nil
	}
	sess, supported := b.sessionInputCapabilities(ctx, chat)
	if !supported {
		b.notifyImagesIgnoredOnce(ctx, chat, sess, imageCtx, strings.TrimSpace(text) != "")
		return nil, strings.TrimSpace(text) != "", nil
	}
	parts := make([]*pb.InputPart, 0, 1+len(imageCtx.refs))
	if text != "" {
		parts = append(parts, &pb.InputPart{Value: &pb.InputPart_Text{Text: text}})
	}
	refs := imageCtx.refs
	if len(refs) > sessioninput.MaxImages {
		refs = refs[:sessioninput.MaxImages]
	}
	totalBytes := 0
	var firstReadErr error
	for _, ref := range refs {
		data, mimeType, name, err := b.fetchFeishuImage(ctx, ref)
		if err != nil {
			if firstReadErr == nil {
				firstReadErr = err
			}
			continue
		}
		if err := sessioninput.ValidateImage(data, mimeType); err != nil {
			continue
		}
		if totalBytes+len(data) > sessioninput.MaxTotalBytes {
			continue
		}
		totalBytes += len(data)
		parts = append(parts, &pb.InputPart{Value: &pb.InputPart_Image{Image: &pb.ImageInput{
			Data: data, MimeType: mimeType, DisplayName: name,
		}}})
	}
	imageCount := 0
	hasImage := false
	for _, part := range parts {
		if part.GetImage() != nil {
			hasImage = true
			imageCount++
		}
	}
	if !hasImage {
		hasText := strings.TrimSpace(text) != ""
		b.notifyImageReadFailureOnce(ctx, chat, sess, imageCtx, hasText)
		if firstReadErr != nil {
			b.observeCapabilityPermissionError(ctx, capabilityCoreMessaging, "图片消息", firstReadErr, true)
		}
		return nil, hasText, nil
	}

	if omitted := len(imageCtx.refs) - imageCount; omitted > 0 {
		notice := fmt.Sprintf("本次读取 %d 张图片，%d 张因数量、大小、格式或读取失败未交给助手。", imageCount, omitted)
		b.notifyImageNotice(ctx, chat, imageCtx, notice)
		parts = append(parts, &pb.InputPart{Value: &pb.InputPart_Text{Text: "[素材读取状态] " + notice}})
	}
	return parts, true, nil
}

func (b *Bridge) fetchFeishuImage(ctx context.Context, ref feishuImageRef) ([]byte, string, string, error) {
	fetch := b.imageResourceFetch
	if fetch == nil {
		fetch = b.downloadMessageImage
	}
	reader, name, err := fetch(ctx, ref.messageID, ref.imageKey)
	if err != nil {
		return nil, "", "", err
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	data, err := io.ReadAll(io.LimitReader(reader, sessioninput.MaxImageBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(data) > sessioninput.MaxImageBytes {
		return nil, "", "", fmt.Errorf("image exceeds %d bytes", sessioninput.MaxImageBytes)
	}
	if bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")) {
		preview, err := gifPNGPreview(data)
		if err != nil {
			return nil, "", "", err
		}
		if name == "" {
			name = "gif-preview"
		}
		return preview, "image/png", strings.TrimSuffix(name, path.Ext(name)) + ".png", nil
	}
	mimeType := detectImageMIME(data)
	if mimeType == "" {
		return nil, "", "", fmt.Errorf("unsupported image format")
	}
	return data, mimeType, name, nil
}

func gifPNGPreview(data []byte) ([]byte, error) {
	config, err := gif.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	// Check the logical canvas before decoding the first frame. The GIF
	// decoder bounds that frame by the logical canvas.
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > sessioninput.MaxPixels {
		return nil, fmt.Errorf("GIF dimensions %dx%d exceed %d pixels", config.Width, config.Height, sessioninput.MaxPixels)
	}
	frame, err := gif.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var preview gifPreviewBuffer
	if err := png.Encode(&preview, frame); err != nil {
		return nil, err
	}
	return preview.Bytes(), nil
}

type gifPreviewBuffer struct{ bytes.Buffer }

func (b *gifPreviewBuffer) Write(data []byte) (int, error) {
	if len(data) > sessioninput.MaxImageBytes-b.Len() {
		return 0, fmt.Errorf("GIF preview exceeds %d bytes", sessioninput.MaxImageBytes)
	}
	return b.Buffer.Write(data)
}

func detectImageMIME(data []byte) string {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	return ""
}

func (b *Bridge) notifyImagesIgnoredOnce(ctx context.Context, chat *LarkChat, sess *pb.Session, imageCtx feishuImageContext, hasText bool) {
	model := "unknown"
	if sess != nil && strings.TrimSpace(sess.EffectiveModel) != "" {
		model = strings.TrimSpace(sess.EffectiveModel)
	}
	if !b.claimImageNotice(chat, model, "unsupported") {
		return
	}
	text := "当前模型暂不支持图片，图片已忽略。"
	if hasText {
		text = "当前模型暂不支持图片，已忽略图片并继续处理文字。"
	}
	b.notifyImageNotice(ctx, chat, imageCtx, text)
}

func (b *Bridge) notifyImageReadFailureOnce(ctx context.Context, chat *LarkChat, sess *pb.Session, imageCtx feishuImageContext, hasText bool) {
	model := "unknown"
	if sess != nil {
		model = strings.TrimSpace(sess.EffectiveModel)
	}
	if !b.claimImageNotice(chat, model, "read-failed") {
		return
	}
	text := "图片暂时无法读取，本次没有提交给助手。"
	if hasText {
		text = "图片暂时无法读取，已继续处理文字。"
	}
	b.notifyImageNotice(ctx, chat, imageCtx, text)
}

func (b *Bridge) claimImageNotice(chat *LarkChat, model, reason string) bool {
	if b.db != nil {
		claimed, err := b.db.ClaimImageNotice(b.currentBridgeId(), chat.TargetID, chat.SessionName, model, reason)
		if err == nil {
			return claimed
		}
	}
	key := chat.TargetID + "\x00" + chat.SessionName + "\x00" + model + "\x00" + reason
	b.imageNoticeMu.Lock()
	defer b.imageNoticeMu.Unlock()
	if b.imageNotices == nil {
		b.imageNotices = make(map[string]struct{})
	}
	if _, exists := b.imageNotices[key]; exists {
		return false
	}
	b.imageNotices[key] = struct{}{}
	return true
}

func (b *Bridge) notifyImageNotice(ctx context.Context, chat *LarkChat, imageCtx feishuImageContext, text string) {
	if imageCtx.replyKind != "" {
		_ = b.notifyBotSession(ctx, imageCtx.replyKind, imageCtx.chatID, imageCtx.openID, text)
		return
	}
	_ = b.notifyThread(ctx, chat.RootMsgID, text)
}
