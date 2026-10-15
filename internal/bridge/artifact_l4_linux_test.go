//go:build linux

package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/isolation"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// This opt-in L4 fixture exercises a real target-UID worker, helper socket/FD
// exchange, Bridge persistence/upload/delivery, and Feishu resource readback.
// The routing adapter replaces fleet discovery; it does not mock file bytes or
// Feishu responses. It creates no OS account and does not restart any service.
type artifactL4Hub struct {
	bridgeTestHub
	client  *isolation.HelperClient
	mapping isolation.AccountMapping
}

func (h *artifactL4Hub) Route(ctx context.Context, _ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
	if method != "ReadSessionArtifact" {
		return nil, fmt.Errorf("unexpected L4 method %s", method)
	}
	req := payload.(*pb.SessionFileRequest)
	data, err := h.client.SessionFile(ctx, h.mapping, "artifact_l4", "read", req.FileName, nil)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(&pb.SessionFileResponse{Data: data})
	return &pb.TunnelMsg{Payload: raw}, err
}

func TestArtifactDeliveryLinuxDACL4(t *testing.T) {
	if os.Getenv("AMUX_ARTIFACT_L4") != "1" {
		t.Skip("explicit Linux/Feishu L4 opt-in required")
	}
	uid, err := strconv.Atoi(os.Getenv("AMUX_TEST_FILE_UID"))
	if err != nil || uid < 10000 || os.Geteuid() != 0 {
		t.Fatal("requires root and an unused AMUX_TEST_FILE_UID >= 10000")
	}
	if _, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		t.Fatal("L4 UID belongs to an existing account")
	}
	binary, credentialFile, chatID := os.Getenv("AMUX_TEST_HELPER_BINARY"), os.Getenv("AMUX_TEST_FEISHU_CREDENTIAL_FILE"), os.Getenv("AMUX_TEST_FEISHU_CHAT_ID")
	if binary == "" || credentialFile == "" || chatID == "" {
		t.Fatal("helper binary, credential file and dedicated test chat are required")
	}
	credentials, err := os.ReadFile(credentialFile)
	if err != nil {
		t.Fatal(err)
	}
	var secrets Secrets
	if err := json.Unmarshal(credentials, &secrets); err != nil {
		t.Fatal(err)
	}
	if secrets.AppId == "" || secrets.AppSecret == "" {
		t.Fatal("missing test bot credentials")
	}
	root, err := os.MkdirTemp("/tmp", "amux-artifact-l4-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	helperCtx, stopHelper := context.WithCancel(ctx)
	defer stopHelper()
	cfg := isolation.HelperConfig{SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"), PrincipalRoot: filepath.Join(root, "principals"), ServiceUID: 0, ServiceGID: 0, UIDRangeStart: uid, UIDRangeEnd: uid, MaterializerPath: "/bin/false", ExecWrapperPath: binary, SkipAccountProvision: true}
	done := make(chan error, 1)
	go func() { done <- isolation.ServeHelper(helperCtx, cfg) }()
	defer func() {
		stopHelper()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("helper did not stop")
		}
	}()
	client := isolation.NewHelperClient(cfg.SocketPath)
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, err := client.Handshake(ctx); err == nil {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("test helper not ready")
	}
	mapping := isolation.AccountMapping{PrincipalID: uuid.NewString(), Account: "amux_" + uuid.NewString()[:8] + "abcd", UID: uid, GID: uid}
	paths, err := client.PrepareRun(ctx, mapping, "artifact_l4", "artifact-l4-launch")
	if err != nil {
		t.Fatal(err)
	}
	outbox := filepath.Join(paths.Workspace, "amux-outbox")
	if err := os.Mkdir(outbox, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(outbox, uid, uid); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{"result.png": bridgeTestPNG(t), "large.bin": bytes.Repeat([]byte("amux-l4\n"), (11<<20)/8)}
	for name, data := range want {
		file := filepath.Join(outbox, name)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(file, uid, uid); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := newBridgeForInputTest(t)
	transport := &artifactL4Transport{}
	b.client = lark.NewClient(secrets.AppId, secrets.AppSecret, lark.WithHttpClient(&http.Client{Transport: transport, Timeout: 90 * time.Second}))
	b.hub = &artifactL4Hub{client: client, mapping: mapping}
	content, _ := json.Marshal(map[string]string{"text": "amux L4: isolated artifact delivery / 隔离文件回传验收 " + uuid.NewString()})
	created, err := b.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chatID).MsgType("text").Content(string(content)).Build()).Build())
	if err != nil {
		t.Fatal(err)
	}
	if !created.Success() || created.Data == nil {
		t.Fatalf("create L4 root: %d %s", created.Code, created.Msg)
	}
	rootID := stringPtr(created.Data.MessageId)
	t.Logf("L4 root message_id=%s", rootID)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		messageIDs := append([]string{rootID}, transport.replyIDs...)
		for i := len(messageIDs) - 1; i >= 0; i-- {
			resp, err := b.client.Im.Message.Delete(cleanupCtx, larkim.NewDeleteMessageReqBuilder().MessageId(messageIDs[i]).Build())
			if err != nil {
				t.Error(err)
			} else if !resp.Success() {
				t.Errorf("cleanup L4 message: %d %s", resp.Code, resp.Msg)
			}
		}
	}()
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: rootID, ChatID: chatID, TargetID: "l4-runtime", SessionName: "l4-session"}
	// Feishu deduplicates request UUIDs across test runs as well as retries.
	// Each run needs a fresh Turn so it cannot reuse an earlier run's messages.
	turnID := uuid.NewString()
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: chat.BridgeId, RootMsgID: rootID, MessageID: rootID, TargetID: chat.TargetID, SessionName: chat.SessionName, SessionID: uuid.NewString(), LaunchID: uuid.NewString(), TurnID: turnID}}); err != nil {
		t.Fatal(err)
	}
	text := "[image](amux-artifact:result.png) [file](amux-artifact:large.bin)"
	b.deliverSessionArtifacts(ctx, chat, turnID, text, nil)
	b.deliverSessionArtifacts(ctx, chat, turnID, text, nil)
	var delivered int
	if err := b.db.db.QueryRow(`SELECT COUNT(*) FROM feishu_artifact_deliveries WHERE sent=1`).Scan(&delivered); err != nil || delivered != 2 {
		t.Fatalf("delivered=%d err=%v", delivered, err)
	}
	verified := 0
	for _, id := range transport.replyIDs {
		response, err := b.client.Im.Message.Get(ctx, larkim.NewGetMessageReqBuilder().MessageId(id).Build())
		if err != nil {
			t.Fatal(err)
		}
		if !response.Success() || response.Data == nil || len(response.Data.Items) != 1 {
			t.Fatalf("read L4 reply: %d %s", response.Code, response.Msg)
		}
		msg := response.Data.Items[0]
		if stringPtr(msg.RootId) != rootID || (msg.Deleted != nil && *msg.Deleted) {
			t.Fatalf("L4 reply does not belong to this run: %s", id)
		}
		kind := stringPtr(msg.MsgType)
		if kind != "image" && kind != "file" {
			t.Fatalf("unexpected L4 reply type %s", kind)
		}
		var body map[string]string
		if err := json.Unmarshal([]byte(stringPtr(msg.Body.Content)), &body); err != nil {
			t.Fatal(err)
		}
		key, name := body["image_key"], "result.png"
		if kind == "file" {
			key, name = body["file_key"], "large.bin"
		}
		resource, err := b.client.Im.MessageResource.Get(ctx, larkim.NewGetMessageResourceReqBuilder().MessageId(id).FileKey(key).Type(kind).Build())
		if err != nil {
			t.Fatal(err)
		}
		if !resource.Success() {
			t.Fatalf("read L4 resource: %d %s", resource.Code, resource.Msg)
		}
		data, err := io.ReadAll(resource.File)
		if closer, ok := resource.File.(io.Closer); ok {
			closer.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, want[name]) {
			t.Fatalf("readback bytes changed for %s: %d", name, len(data))
		}
		t.Logf("L4 delivered and read back %s: bytes=%d sha256=%x message_id=%s uid=%d", name, len(data), sha256.Sum256(data), id, uid)
		verified++
	}
	if verified != 2 {
		t.Fatalf("readback count=%d; expected two messages after retry", verified)
	}
}

// Observe IDs returned by the real API without replacing production sending or
// relying on eventually-consistent thread listings. Retain IDs even on a later
// assertion failure so cleanup can retract exactly the messages this test made.
type artifactL4Transport struct{ replyIDs []string }

func (t *artifactL4Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || !strings.HasSuffix(req.URL.Path, "/reply") {
		return response, err
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	var result struct {
		Code int `json:"code"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &result) == nil && result.Code == 0 && result.Data.MessageID != "" {
		t.replyIDs = append(t.replyIDs, result.Data.MessageID)
	}
	return response, nil
}
