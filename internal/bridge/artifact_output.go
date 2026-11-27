package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/sessionfiles"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/turnresult"
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

func artifactRefs(text string) ([]artifactRef, int) {
	matches := artifactLinkPattern.FindAllStringSubmatch(text, -1)
	refs := []artifactRef{}
	invalid := 0
	seen := map[string]bool{}
	for _, m := range matches {
		name, err := url.PathUnescape(m[2])
		name = strings.TrimPrefix(name, "amux-outbox/")
		if err != nil || sessionfiles.Validate("read", name) != nil {
			invalid++
			continue
		}
		if !seen[name] {
			seen[name] = true
			refs = append(refs, artifactRef{m[1], name, strings.HasPrefix(m[0], "!")})
		}
	}
	return refs, invalid
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

const artifactTransferTimeout = 90 * time.Second

func (b *Bridge) deliverSessionArtifacts(ctx context.Context, chat *LarkChat, turnID, text string, canonicalJSON []byte) {
	if b.db == nil || chat == nil {
		return
	}
	refs, invalid := artifactRefs(text)
	if len(refs)+invalid == 0 {
		return
	}
	// A replacement stream may overlap its predecessor.
	unlock := b.lockRoute("artifacts:" + chat.BridgeId + ":" + turnID)
	defer unlock()
	var deliveredAttachments []turnresult.DeliveryMessage
	failedCount, totalPlanned := invalid, len(refs)+invalid
	defer func() {
		b.recordTurnResultArtifacts(chat.BridgeId, turnID, deliveredAttachments, failedCount, totalPlanned)
	}()
	fail := func(key string, args ...any) {
		notifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		message := b.fileExchangeFailureText(notifyCtx, chat, key, args...)
		if err := b.notifyArtifactFailure(notifyCtx, chat, message); err != nil {
			log.Warn().Err(err).Str("turn_id", turnID).Msg("bridge: artifact failure notice")
		}
	}
	if invalid > 0 {
		fail("artifact.invalid_link")
	}
	if len(refs) == 0 {
		return
	}
	if len(refs) > maxMaterialFiles {
		failedCount += len(refs) - maxMaterialFiles
		fail("artifact.too_many")
		refs = refs[:maxMaterialFiles]
	}
	sessionID, launchID := "", ""
	if event, err := agentstream.UnmarshalEvent(canonicalJSON); err == nil && event.Turn != nil && event.Turn.ID == turnID {
		sessionID = event.SessionID
		launchID = event.Origin.LaunchID
	}
	if sessionID == "" || launchID == "" {
		err := b.db.db.QueryRowContext(ctx, `SELECT session_id,launch_id FROM feishu_turn_links WHERE bridge_id=? AND turn_id=? AND root_msg_id=? AND target_id=? AND session_name=? LIMIT 1`, chat.BridgeId, turnID, chat.RootMsgID, chat.TargetID, chat.SessionName).Scan(&sessionID, &launchID)
		if err != nil || sessionID == "" || launchID == "" {
			failedCount += len(refs)
			fail("artifact.execution_unknown")
			return
		}
	}
	total := 0
	if err := b.db.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM feishu_artifact_deliveries WHERE bridge_id=? AND turn_id=?`, chat.BridgeId, turnID).Scan(&total); err != nil {
		failedCount += len(refs)
		fail("artifact.state_unavailable")
		return
	}
	for _, ref := range refs {
		transferCtx, cancel := context.WithTimeout(ctx, artifactTransferTimeout)
		msgID, failure := b.deliverSessionArtifact(transferCtx, chat, turnID, sessionID, launchID, ref, &total)
		if failure != "" && errors.Is(transferCtx.Err(), context.DeadlineExceeded) {
			failure = "artifact.timed_out"
		}
		cancel()
		if failure != "" {
			failedCount++
			fail(failure, ref.name)
			continue
		}
		if msgID != "" {
			deliveredAttachments = append(deliveredAttachments, turnresult.DeliveryMessage{
				Kind:      turnresult.MessageKindAttachment,
				MessageID: msgID,
				Name:      ref.name,
			})
		}
	}
}

func (b *Bridge) deliverSessionArtifact(ctx context.Context, chat *LarkChat, turnID, sessionID, launchID string, ref artifactRef, total *int) (messageID, failureKey string) {
	kind, key, existingMsgID := "", "", ""
	sent := false
	logger := log.With().Str("target_id", chat.TargetID).Str("session_id", sessionID).
		Str("launch_id", launchID).Str("turn_id", turnID).Str("file_name", ref.name).Logger()
	err := b.db.db.QueryRowContext(ctx, `SELECT kind,media_key,sent,message_id FROM feishu_artifact_deliveries WHERE bridge_id=? AND turn_id=? AND path=?`, chat.BridgeId, turnID, ref.name).Scan(&kind, &key, &sent, &existingMsgID)
	if err != nil && err != sql.ErrNoRows {
		logger.Warn().Err(err).Msg("bridge: read artifact delivery state")
		return "", "artifact.state_read_failed"
	}
	if sent {
		return existingMsgID, ""
	}
	if key == "" {
		response, readErr := b.hub.Route(ctx, chat.TargetID, "ReadSessionArtifact", &pb.SessionFileRequest{Name: chat.SessionName, SessionId: sessionID, LaunchId: launchID, FileName: ref.name})
		var file pb.SessionFileResponse
		if readErr == nil {
			if response == nil {
				readErr = fmt.Errorf("empty artifact response")
			} else {
				readErr = json.Unmarshal(response.Payload, &file)
			}
		}
		if readErr != nil {
			logger.Warn().Err(readErr).Msg("bridge: read artifact")
			switch {
			case sessionfiles.NeedsHelperUpgrade(readErr):
				return "", "file.helper_upgrade_required"
			case sessionfiles.ArtifactTooLarge(readErr):
				return "", "artifact.too_large"
			default:
				return "", "artifact.read_failed"
			}
		}
		size := len(file.Data)
		if size == 0 {
			return "", "artifact.empty"
		}
		if size > sessionfiles.MaxArtifactBytes {
			return "", "artifact.too_large"
		}
		if *total+size > maxArtifactTotalBytes {
			return "", "artifact.total_too_large"
		}
		kind, key, err = b.uploadArtifact(ctx, ref, file.Data)
		if err != nil {
			logger.Warn().Err(err).Msg("bridge: upload artifact")
			if errors.Is(err, sessionfiles.ErrArtifactTooLarge) {
				return "", "artifact.upload_size_rejected"
			}
			if _, ok := capabilityPermissionIssueFromError(err, capabilityArtifactUpload, "附件上传"); ok {
				b.observeCapabilityPermissionError(ctx, capabilityArtifactUpload, "附件上传", err, true)
				return "", "artifact.upload_permission_required"
			}
			return "", "artifact.upload_failed"
		}
		b.resolveCapabilityPermissionScopes(capabilityArtifactUpload, artifactUploadScope)
		_, err = b.db.db.ExecContext(ctx, `INSERT INTO feishu_artifact_deliveries(bridge_id,turn_id,path,kind,media_key,size_bytes,sent) VALUES(?,?,?,?,?,?,0)`, chat.BridgeId, turnID, ref.name, kind, key, size)
		if err != nil {
			logger.Warn().Err(err).Msg("bridge: persist artifact upload")
			return "", "artifact.state_write_failed"
		}
		*total += size
	}
	digest := sha256.Sum256([]byte(chat.BridgeId + "\x00" + turnID + "\x00" + ref.name))
	requestUUID := fmt.Sprintf("%x", digest[:16])
	var msgID string
	if b.artifactSend != nil {
		err = b.artifactSend(ctx, chat, kind, key, requestUUID)
	} else {
		err = retryFeishuRateLimit(ctx, "artifact.send", func() error {
			var sendErr error
			msgID, sendErr = b.sendArtifactToBindingWithID(ctx, chat, kind, key, requestUUID)
			return sendErr
		})
	}
	if err != nil && msgID == "" {
		logger.Warn().Err(err).Msg("bridge: send artifact")
		return "", "artifact.send_failed"
	}
	// A direct reply may have reached Feishu even if storing its routing alias failed.
	if err != nil {
		logger.Warn().Err(err).Msg("bridge: record artifact reply alias")
	}
	if _, err := b.db.db.Exec(`UPDATE feishu_artifact_deliveries SET sent=1, message_id=? WHERE bridge_id=? AND turn_id=? AND path=?`, msgID, chat.BridgeId, turnID, ref.name); err != nil {
		logger.Warn().Err(err).Msg("bridge: persist sent artifact")
	}
	return msgID, ""
}

func (b *Bridge) fileExchangeFailureText(ctx context.Context, chat *LarkChat, key string, args ...any) string {
	openID := b.privateThreadRecipient(chat)
	if target, direct, err := b.artifactDirectTarget(ctx, chat); direct && err == nil && target != nil {
		openID = ""
		if target.Kind == botSessionReplyDM {
			openID = target.OpenID
		}
	}
	return b.managementFallbackText(openID, sessionMessage(key, args...))
}

func (b *Bridge) uploadArtifact(ctx context.Context, ref artifactRef, data []byte) (string, string, error) {
	if len(data) > sessionfiles.MaxArtifactBytes {
		return "", "", sessionfiles.ErrArtifactTooLarge
	}
	isImage := len(data) <= maxFeishuImageBytes &&
		(detectImageMIME(data) != "" || http.DetectContentType(data) == "image/gif")
	if b.artifactUpload != nil {
		return b.artifactUpload(ctx, ref.name, isImage, data)
	}
	if b.client == nil {
		return "", "", fmt.Errorf("nil lark client")
	}
	var mediaKey string
	if isImage {
		fallback := false
		err := retryFeishuRateLimit(ctx, "artifact.upload_image", func() error {
			resp, err := b.client.Im.Image.Create(ctx, larkim.NewCreateImageReqBuilder().Body(larkim.NewCreateImageReqBodyBuilder().ImageType("message").Image(bytes.NewReader(data)).Build()).Build())
			if err != nil {
				return err
			}
			if isFeishuPermissionCode(resp.Code) {
				return feishuAPIResponseErrorForRequiredScopes("upload artifact image", b.secretsAppID(), resp.CodeError, artifactUploadScope)
			}
			// Feishu's image endpoint has its own size, format and resolution limits.
			switch resp.Code {
			case 234006, 234011, 234039:
				fallback = true
			}
			fallback = fallback || resp.StatusCode == http.StatusRequestEntityTooLarge
			if !resp.Success() || fallback {
				return fmt.Errorf("image upload failed: HTTP %d, code %d, %s", resp.StatusCode, resp.Code, resp.Msg)
			}
			if resp.Data == nil || stringPtr(resp.Data.ImageKey) == "" {
				return fmt.Errorf("image upload returned no image key")
			}
			mediaKey = stringPtr(resp.Data.ImageKey)
			return nil
		})
		if err == nil || !fallback {
			return "image", mediaKey, err
		}
		log.Debug().Err(err).Str("file_name", ref.name).Msg("bridge: deliver rejected image as file")
	}
	err := retryFeishuRateLimit(ctx, "artifact.upload_file", func() error {
		resp, err := b.client.Im.File.Create(ctx, larkim.NewCreateFileReqBuilder().Body(larkim.NewCreateFileReqBodyBuilder().FileType("stream").FileName(path.Base(ref.name)).File(bytes.NewReader(data)).Build()).Build())
		if err != nil {
			return err
		}
		if isFeishuPermissionCode(resp.Code) {
			return feishuAPIResponseErrorForRequiredScopes("upload artifact file", b.secretsAppID(), resp.CodeError, artifactUploadScope)
		}
		if resp.Code == 234006 || resp.StatusCode == http.StatusRequestEntityTooLarge {
			return fmt.Errorf("%w: file upload failed: HTTP %d, code %d, %s", sessionfiles.ErrArtifactTooLarge, resp.StatusCode, resp.Code, resp.Msg)
		}
		if !resp.Success() {
			return fmt.Errorf("file upload failed: HTTP %d, code %d, %s", resp.StatusCode, resp.Code, resp.Msg)
		}
		if resp.Data == nil || stringPtr(resp.Data.FileKey) == "" {
			return fmt.Errorf("file upload returned no file key")
		}
		mediaKey = stringPtr(resp.Data.FileKey)
		return nil
	})
	return "file", mediaKey, err
}

func (b *Bridge) sendArtifactToBinding(ctx context.Context, chat *LarkChat, kind, key, requestUUID string) error {
	_, err := b.sendArtifactToBindingWithID(ctx, chat, kind, key, requestUUID)
	return err
}

func (b *Bridge) sendArtifactToBindingWithID(ctx context.Context, chat *LarkChat, kind, key, requestUUID string) (string, error) {
	if b.client == nil {
		return "", fmt.Errorf("nil lark client")
	}
	field := "file_key"
	if kind == "image" {
		field = "image_key"
	}
	body, _ := json.Marshal(map[string]string{field: key})
	if bs, ok, err := b.artifactDirectTarget(ctx, chat); ok || err != nil {
		if err != nil {
			return "", err
		}
		receiveType, receiveID := "chat_id", bs.ChatID
		if bs.Kind == botSessionReplyDM {
			receiveType, receiveID = "open_id", bs.OpenID
		}
		if receiveID == "" {
			return "", fmt.Errorf("missing artifact destination")
		}
		resp, err := b.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(receiveType).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(receiveID).MsgType(kind).Content(string(body)).Uuid(requestUUID).Build()).Build())
		if err != nil {
			return "", err
		}
		if !resp.Success() || resp.Data == nil || stringPtr(resp.Data.MessageId) == "" {
			return "", fmt.Errorf("send artifact: %d %s", resp.Code, resp.Msg)
		}
		msgID := stringPtr(resp.Data.MessageId)
		_, _, err = b.recordDirectReply(bs, msgID, stringPtr(resp.Data.ThreadId), nil)
		return msgID, err
	}
	resp, err := b.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().MessageId(chat.RootMsgID).Body(larkim.NewReplyMessageReqBodyBuilder().MsgType(kind).Content(string(body)).ReplyInThread(true).Uuid(requestUUID).Build()).Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() || resp.Data == nil || stringPtr(resp.Data.MessageId) == "" {
		return "", fmt.Errorf("reply artifact: %d %s", resp.Code, resp.Msg)
	}
	return stringPtr(resp.Data.MessageId), nil
}

// dispatchArtifactDelivery captures the completed turn before the stream advances.
// Network delivery owns its timeout and cannot hold up subsequent stream events.
func (b *Bridge) dispatchArtifactDelivery(chat *LarkChat, turnID, text string, canonicalJSON []byte) {
	if b == nil || chat == nil || turnID == "" || !artifactLinkPattern.MatchString(text) {
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
		ctx := context.WithValue(context.Background(), artifactDestinationKey{}, destination)
		b.deliverSessionArtifacts(ctx, &snapshot, turnID, text, canonicalJSON)
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
	if b.threadNotice != nil {
		return b.threadNotice(ctx, chat.RootMsgID, text)
	}
	_, _, err := b.sendTextToThread(ctx, chat.RootMsgID, text)
	return err
}
