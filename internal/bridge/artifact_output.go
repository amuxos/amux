package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/sessionfiles"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/rs/zerolog/log"
)

// materialDelivery owns artifact and image delivery dedupe state.
type materialDelivery struct {
	artifactMu       sync.Mutex
	artifactInFlight map[string]bool
	imageNoticeMu    sync.Mutex
	imageNotices     map[string]struct{}
}

var artifactLinkPattern = regexp.MustCompile(`!?\[([^\]\r\n]*)\]\(amux-artifact:([^\r\n)]*)\)`)

type artifactRef struct {
	label, name string
	image       bool
}

type artifactDestinationKey struct{}

type artifactDestination struct {
	target *directReplyTarget
	direct bool
	err    error
}

// Singleton LastReply fields are mutable independently of the stream binding.
// Async sends and their failure notices must use the same captured destination.
func (b *Bridge) artifactDirectTarget(ctx context.Context, chat *LarkChat) (*directReplyTarget, bool, error) {
	if destination, ok := ctx.Value(artifactDestinationKey{}).(artifactDestination); ok {
		return destination.target, destination.direct, destination.err
	}
	return b.directReplyTargetForBinding(chat)
}

func artifactRefs(text string) ([]artifactRef, error) {
	matches := artifactLinkPattern.FindAllStringSubmatch(text, -1)
	refs := []artifactRef{}
	seen := map[string]bool{}
	for _, m := range matches {
		name, err := url.PathUnescape(m[2])
		if err != nil || sessionfiles.Validate("read", name) != nil {
			return nil, fmt.Errorf("成果链接包含无效路径")
		}
		if !seen[name] {
			seen[name] = true
			refs = append(refs, artifactRef{m[1], name, strings.HasPrefix(m[0], "!")})
		}
	}
	return refs, nil
}
func renderArtifactLinks(text string) string {
	return artifactLinkPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := artifactLinkPattern.FindStringSubmatch(match)
		label := parts[1]
		if label == "" {
			label = "成果附件"
		}
		return label + "（附件见对话）"
	})
}

func (b *Bridge) deliverSessionArtifacts(ctx context.Context, chat *LarkChat, turnID, text string, canonicalJSON []byte) {
	refs, err := artifactRefs(text)
	if len(refs) == 0 && err == nil {
		return
	}
	if b.db == nil || chat == nil {
		return
	}
	// The stream is serial, but a replacement stream may overlap its predecessor.
	unlock := b.lockRoute("artifacts:" + chat.BridgeId + ":" + turnID)
	defer unlock()
	fail := func(message string) {
		sendErr := b.notifyArtifactFailure(ctx, chat, message)
		if sendErr != nil {
			log.Warn().Err(sendErr).Msg("bridge: artifact failure notice")
		}
	}
	if err != nil {
		fail("成果附件未发送：" + err.Error())
		return
	}
	sessionID, launchID := "", ""
	if event, parseErr := agentstream.UnmarshalEvent(canonicalJSON); parseErr == nil && event.Turn != nil && event.Turn.ID == turnID {
		sessionID = event.SessionID
		launchID = event.Origin.LaunchID
	}
	if sessionID == "" || launchID == "" {
		err = b.db.db.QueryRow(`SELECT session_id,launch_id FROM feishu_turn_links WHERE bridge_id=? AND turn_id=? AND root_msg_id=? AND target_id=? AND session_name=? LIMIT 1`, chat.BridgeId, turnID, chat.RootMsgID, chat.TargetID, chat.SessionName).Scan(&sessionID, &launchID)
		if err != nil {
			fail("成果附件未发送：无法确认本轮执行身份。")
			return
		}
	}
	if len(refs) > maxMaterialFiles {
		fail("成果超过 10 个，本次仅发送前 10 个。")
		refs = refs[:maxMaterialFiles]
	}
	total := 0
	if err := b.db.db.QueryRow(`SELECT COALESCE(SUM(size_bytes),0) FROM feishu_artifact_deliveries WHERE bridge_id=? AND turn_id=?`, chat.BridgeId, turnID).Scan(&total); err != nil {
		fail("无法读取成果总量。")
		return
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			fail("成果发送超时，请稍后重试。")
			return
		}
		kind, key := "", ""
		sent := false
		err = b.db.db.QueryRow(`SELECT kind,media_key,sent FROM feishu_artifact_deliveries WHERE bridge_id=? AND turn_id=? AND path=?`, chat.BridgeId, turnID, ref.name).Scan(&kind, &key, &sent)
		if err != nil && err != sql.ErrNoRows {
			fail("无法读取成果发送状态。")
			return
		}
		if sent {
			continue
		}
		if key == "" {
			response, readErr := b.hub.Route(ctx, chat.TargetID, "ReadSessionArtifact", &pb.SessionFileRequest{Name: chat.SessionName, SessionId: sessionID, LaunchId: launchID, FileName: ref.name})
			var file pb.SessionFileResponse
			if readErr != nil || response == nil || json.Unmarshal(response.Payload, &file) != nil || len(file.Data) > sessionfiles.MaxArtifactBytes {
				fail(fmt.Sprintf("成果 %q 读取失败；请确认文件位于 amux-outbox/ 且不超过 30 MB。", ref.name))
				continue
			}
			if total+len(file.Data) > maxArtifactTotalBytes {
				fail(fmt.Sprintf("成果 %q 超过本轮 300 MB 总量限制，未发送。", ref.name))
				continue
			}
			total += len(file.Data)
			kind, key, err = b.uploadArtifact(ctx, ref, file.Data)
			if err != nil {
				log.Warn().Err(err).Str("turn_id", turnID).Msg("bridge: upload artifact")
				fail(fmt.Sprintf("成果 %q 上传失败。", ref.name))
				continue
			}
			_, err = b.db.db.Exec(`INSERT INTO feishu_artifact_deliveries(bridge_id,turn_id,path,kind,media_key,size_bytes,sent) VALUES(?,?,?,?,?,?,0) ON CONFLICT(bridge_id,turn_id,path) DO NOTHING`, chat.BridgeId, turnID, ref.name, kind, key, len(file.Data))
			if err != nil {
				fail("无法保存成果发送状态，附件尚未发送。")
				continue
			}
		}
		digest := sha256.Sum256([]byte(chat.BridgeId + "\x00" + turnID + "\x00" + ref.name))
		requestUUID := fmt.Sprintf("%x", digest[:16])
		send := b.artifactSend
		if send == nil {
			send = b.sendArtifactToBinding
		}
		if err = send(ctx, chat, kind, key, requestUUID); err != nil {
			log.Warn().Err(err).Str("turn_id", turnID).Msg("bridge: send artifact")
			fail(fmt.Sprintf("成果 %q 发送失败，可在当前对话请求重新发送。", ref.name))
			continue
		}
		if _, err = b.db.db.Exec(`UPDATE feishu_artifact_deliveries SET sent=1 WHERE bridge_id=? AND turn_id=? AND path=?`, chat.BridgeId, turnID, ref.name); err != nil {
			log.Warn().Err(err).Msg("bridge: persist sent artifact")
		}
	}
}

func (b *Bridge) uploadArtifact(ctx context.Context, ref artifactRef, data []byte) (string, string, error) {
	isImage := detectImageMIME(data) != "" && len(data) <= maxFeishuImageBytes
	if b.artifactUpload != nil {
		return b.artifactUpload(ctx, ref.name, isImage, data)
	}
	if b.client == nil {
		return "", "", fmt.Errorf("nil lark client")
	}
	if isImage {
		resp, err := b.client.Im.Image.Create(ctx, larkim.NewCreateImageReqBuilder().Body(larkim.NewCreateImageReqBodyBuilder().ImageType("message").Image(bytes.NewReader(data)).Build()).Build())
		if err != nil {
			return "", "", err
		}
		if !resp.Success() || resp.Data == nil || stringPtr(resp.Data.ImageKey) == "" {
			return "", "", fmt.Errorf("image upload failed: %d %s", resp.Code, resp.Msg)
		}
		return "image", stringPtr(resp.Data.ImageKey), nil
	}
	resp, err := b.client.Im.File.Create(ctx, larkim.NewCreateFileReqBuilder().Body(larkim.NewCreateFileReqBodyBuilder().FileType("stream").FileName(path.Base(ref.name)).File(bytes.NewReader(data)).Build()).Build())
	if err != nil {
		return "", "", err
	}
	if !resp.Success() || resp.Data == nil || stringPtr(resp.Data.FileKey) == "" {
		return "", "", fmt.Errorf("file upload failed: %d %s", resp.Code, resp.Msg)
	}
	return "file", stringPtr(resp.Data.FileKey), nil
}

func (b *Bridge) sendArtifactToBinding(ctx context.Context, chat *LarkChat, kind, key, requestUUID string) error {
	if b.client == nil {
		return fmt.Errorf("nil lark client")
	}
	field := "file_key"
	if kind == "image" {
		field = "image_key"
	}
	body, _ := json.Marshal(map[string]string{field: key})
	if bs, ok, err := b.artifactDirectTarget(ctx, chat); ok || err != nil {
		if err != nil {
			return err
		}
		receiveType, receiveID := "chat_id", bs.ChatID
		if bs.Kind == botSessionReplyDM {
			receiveType, receiveID = "open_id", bs.OpenID
		}
		if receiveID == "" {
			return fmt.Errorf("missing artifact destination")
		}
		resp, err := b.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(receiveType).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(receiveID).MsgType(kind).Content(string(body)).Uuid(requestUUID).Build()).Build())
		if err != nil {
			return err
		}
		if !resp.Success() || resp.Data == nil || stringPtr(resp.Data.MessageId) == "" {
			return fmt.Errorf("send artifact: %d %s", resp.Code, resp.Msg)
		}
		_, _, err = b.recordDirectReply(bs, stringPtr(resp.Data.MessageId), stringPtr(resp.Data.ThreadId), nil)
		return err
	}
	resp, err := b.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().MessageId(chat.RootMsgID).Body(larkim.NewReplyMessageReqBodyBuilder().MsgType(kind).Content(string(body)).ReplyInThread(true).Uuid(requestUUID).Build()).Build())
	if err != nil {
		return err
	}
	if !resp.Success() || resp.Data == nil || stringPtr(resp.Data.MessageId) == "" {
		return fmt.Errorf("reply artifact: %d %s", resp.Code, resp.Msg)
	}
	return nil
}

// dispatchArtifactDelivery captures the completed turn before the stream advances.
// Network delivery owns its timeout and cannot hold up subsequent stream events.
func (b *Bridge) dispatchArtifactDelivery(chat *LarkChat, turnID, text string, canonicalJSON []byte) {
	if b == nil || chat == nil || turnID == "" || !strings.Contains(text, "amux-artifact:") {
		return
	}
	snapshot := *chat
	canonicalJSON = bytes.Clone(canonicalJSON)
	key := snapshot.BridgeId + ":" + turnID
	b.artifactMu.Lock()
	if b.artifactInFlight[key] {
		b.artifactMu.Unlock()
		return
	}
	if b.artifactInFlight == nil {
		b.artifactInFlight = make(map[string]bool)
	}
	b.artifactInFlight[key] = true
	b.artifactMu.Unlock()
	bs, singleton, err := b.directReplyTargetForBinding(&snapshot)
	if bs != nil {
		copy := *bs
		bs = &copy
	}
	destination := artifactDestination{target: bs, direct: singleton, err: err}
	go func() {
		defer func() {
			b.artifactMu.Lock()
			delete(b.artifactInFlight, key)
			b.artifactMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, artifactDestinationKey{}, destination)
		b.deliverSessionArtifacts(ctx, &snapshot, turnID, text, canonicalJSON)
		if err := ctx.Err(); err != nil {
			log.Warn().Err(err).Str("bridge_id", snapshot.BridgeId).Str("turn_id", turnID).
				Str("root_msg_id", snapshot.RootMsgID).Msg("bridge: artifact delivery expired")
		}
	}()
}

func (b *Bridge) notifyArtifactFailure(ctx context.Context, chat *LarkChat, text string) error {
	if bs, ok, err := b.artifactDirectTarget(ctx, chat); ok || err != nil {
		if err != nil {
			return err
		}
		_, _, err := b.sendTextToDirectTarget(ctx, bs, text)
		return err
	}
	return b.notifyThread(ctx, chat.RootMsgID, text)
}
