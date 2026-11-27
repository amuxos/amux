package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/sessionfiles"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestArtifactDispatchDoesNotBlockAndFreezesDeliveryIdentity(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	h := &materialTestHub{readData: []byte("result")}
	b.hub = h
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "singleton:" + b.currentBridgeId(), TargetID: "target", SessionName: "original"}
	bs := BotSession{BridgeId: chat.BridgeId, Enabled: true, TargetID: "target", SessionName: "original", LastReplyKind: botSessionReplyGroup, LastReplyChatID: "original-group"}
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	original := *chat
	canonical, err := agentstream.MarshalEvent(agentstream.Event{
		SessionID: "sid", Origin: agentstream.Origin{LaunchID: "launch"},
		Cursor: agentstream.Cursor{Epoch: "epoch", Seq: 1}, At: time.Now(),
		Turn: &agentstream.TurnRef{ID: "turn", Seq: 1},
		Body: agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	var uploads, sends atomic.Int32
	b.artifactUpload = func(ctx context.Context, _ string, _ bool, _ []byte) (string, string, error) {
		if uploads.Add(1) == 1 {
			context.AfterFunc(ctx, func() { close(finished) })
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 90*time.Second {
			t.Error("worker missing bounded timeout")
		}
		return "file", "key", nil
	}
	b.artifactSend = func(ctx context.Context, got *LarkChat, _, _, _ string) error {
		sends.Add(1)
		if *got != original {
			t.Errorf("delivery followed mutable binding: %+v", got)
		}
		destination, singleton, err := b.artifactDirectTarget(ctx, got)
		if err != nil || !singleton || destination == nil || destination.ChatID != "original-group" {
			t.Errorf("delivery followed mutable singleton destination: %+v, %v", destination, err)
		}
		return nil
	}
	// Block before any identity parsing/read. Dispatch must still return; the
	// stream can then reuse its binding and canonical-event buffer safely.
	unlock := b.lockRoute("artifacts:" + chat.BridgeId + ":turn")
	var once sync.Once
	release := func() { once.Do(unlock) }
	t.Cleanup(func() {
		release()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("artifact worker did not finish")
		}
	})
	returned := make(chan struct{})
	go func() {
		for range 5 {
			b.dispatchArtifactDelivery(chat, "turn", "[report](amux-artifact:report.txt)", canonical)
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("artifact delivery blocked the stream")
	}
	chat.RootMsgID, chat.TargetID, chat.SessionName = "next-root", "next-target", "next-session"
	bs.LastReplyChatID = "next-group"
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	clear(canonical)
	release()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("artifact delivery did not complete")
	}
	if uploads.Load() != 1 || sends.Load() != 1 || len(h.reads) != 1 {
		t.Fatalf("duplicate delivery: uploads=%d sends=%d reads=%d", uploads.Load(), sends.Load(), len(h.reads))
	}
	if h.reads[0].SessionId != "sid" || h.reads[0].LaunchId != "launch" || h.reads[0].Name != "original" {
		t.Fatalf("execution identity changed: %+v", h.reads[0])
	}
}

func TestArtifactLinksOnlyExplicitOutboxReferences(t *testing.T) {
	t.Parallel()
	refs, invalid := artifactRefs("[local](/etc/passwd) [report](amux-artifact:报告%20v1.csv) ![plot](amux-artifact:plot.png) [again](amux-artifact:plot.png)")
	if invalid != 0 || len(refs) != 2 || refs[0].name != "报告 v1.csv" || !refs[1].image {
		t.Fatalf("%+v %v", refs, invalid)
	}
	for _, bad := range []string{
		"", ".", "..", "../secret", "%2Fetc%2Fpasswd", "a%5Cb", "a/../../secret", "%ZZ",
		"amux-outbox/", "amux-outbox/../secret", "amux-outbox/%2e%2e/secret",
		"amux-outbox//etc/passwd", "amux-outbox/a%5Cb", "amux-outbox/./chart.png",
	} {
		if refs, invalid := artifactRefs("[x](amux-artifact:" + bad + ")"); invalid != 1 || len(refs) != 0 {
			t.Fatalf("accepted %s", bad)
		}
	}
	rendered := renderArtifactLinks("[report](amux-artifact:report.csv)")
	if strings.Contains(rendered, "amux-artifact:") || !strings.Contains(rendered, "report") {
		t.Fatal(rendered)
	}
}

func TestArtifactLinksNormalizeOutboxPrefix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path string
		want string
	}{
		{"chart.png", "chart.png"},
		{"amux-outbox/chart.png", "chart.png"},
		{"plots/chart.png", "plots/chart.png"},
		{"amux-outbox/plots/chart.png", "plots/chart.png"},
		{"amux-outbox%2Fplots%2F报告%20v1.png", "plots/报告 v1.png"},
		{"amux-outbox/amux-outbox/chart.png", "amux-outbox/chart.png"},
		{"amux-outbox-chart.png", "amux-outbox-chart.png"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			refs, invalid := artifactRefs("![chart](amux-artifact:" + tc.path + ")")
			if invalid != 0 || len(refs) != 1 || refs[0].name != tc.want || !refs[0].image {
				t.Fatalf("refs=%+v err=%v; want image %q", refs, invalid, tc.want)
			}
		})
	}
	refs, invalid := artifactRefs("[chart](amux-artifact:amux-outbox/plots/chart.png) [again](amux-artifact:plots/chart.png) [encoded](amux-artifact:amux-outbox%2Fplots%2Fchart.png)")
	if invalid != 0 || len(refs) != 1 || refs[0].name != "plots/chart.png" {
		t.Fatalf("equivalent links were not deduplicated: refs=%+v err=%v", refs, invalid)
	}
}

func TestArtifactDeliveryPersistsUploadAndRetriesSameMessage(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	h := &materialTestHub{readData: []byte("result")}
	b.hub = h
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root", TargetID: "target", SessionName: "s1"}
	err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: chat.BridgeId, RootMsgID: "root", MessageID: "input", TargetID: "target", SessionName: "s1", SessionID: "sid", LaunchID: "launch", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	var notices []string
	b.threadNotice = func(_ context.Context, _ string, text string) error { notices = append(notices, text); return nil }
	uploads, sends := 0, 0
	var uuids []string
	b.artifactUpload = func(_ context.Context, name string, image bool, data []byte) (string, string, error) {
		uploads++
		if name != "report.txt" || string(data) != "result" {
			t.Fatal("bad upload")
		}
		return "file", "key", nil
	}
	b.artifactSend = func(_ context.Context, got *LarkChat, kind, key, id string) error {
		sends++
		uuids = append(uuids, id)
		if got.RootMsgID != "root" || kind != "file" || key != "key" {
			t.Fatal("wrong destination")
		}
		if sends == 1 {
			return errors.New("ambiguous send")
		}
		return nil
	}
	for _, text := range []string{
		"[report](amux-artifact:amux-outbox/report.txt) [again](amux-artifact:report.txt)",
		"[report](amux-artifact:report.txt)",
		"[report](amux-artifact:amux-outbox%2Freport.txt)",
	} {
		b.deliverSessionArtifacts(context.Background(), chat, "turn", text, nil)
	}
	if uploads != 1 || sends != 2 || len(h.reads) != 1 || len(notices) != 1 || uuids[0] != uuids[1] {
		t.Fatalf("uploads=%d sends=%d reads=%d notices=%v uuids=%v", uploads, sends, len(h.reads), notices, uuids)
	}
	if h.reads[0].SessionId != "sid" || h.reads[0].LaunchId != "launch" || h.reads[0].FileName != "report.txt" {
		t.Fatalf("artifact read changed identity or path: %+v", h.reads[0])
	}
}

func TestArtifactUploadDetectsImageBehindOrdinaryLink(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	seenImage := false
	b.artifactUpload = func(_ context.Context, _ string, image bool, _ []byte) (string, string, error) {
		seenImage = image
		return "image", "key", nil
	}
	if _, _, err := b.uploadArtifact(context.Background(), artifactRef{name: "chart.png"}, bridgeTestPNG(t)); err != nil || !seenImage {
		t.Fatal("ordinary PNG link was not an image", err)
	}
}

func TestArtifactUploadSendsLargeImageAsFile(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	seenImage := true
	b.artifactUpload = func(_ context.Context, _ string, image bool, _ []byte) (string, string, error) {
		seenImage = image
		return "file", "key", nil
	}
	data := append(bridgeTestPNG(t), make([]byte, maxFeishuImageBytes+1)...)
	if _, _, err := b.uploadArtifact(context.Background(), artifactRef{name: "chart.png"}, data); err != nil || seenImage {
		t.Fatal("large image was not downgraded to a file", err)
	}
}

func TestArtifactInstructionsStayOutOfQuestionTitle(t *testing.T) {
	t.Parallel()
	if got := toolActivityQuestionTitle("请生成图表" + artifactInstructions); got != "请生成图表" {
		t.Fatal(got)
	}
}

func TestArtifactUploadFallsBackToOriginalFileOnImageRejection(t *testing.T) {
	data := bridgeTestPNG(t)
	for _, tc := range []struct {
		name     string
		code     int
		fallback bool
	}{
		{"size", 234006, true},
		{"format", 234011, true},
		{"resolution", 234039, true},
		{"permission", 99991672, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var images, files atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
				case "/open-apis/im/v1/images":
					images.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"code":%d,"msg":"image rejected"}`, tc.code)
				case "/open-apis/im/v1/files":
					files.Add(1)
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if r.FormValue("file_type") != "stream" || r.FormValue("file_name") != "chart.png" {
						t.Errorf("wrong upload metadata: %v", r.MultipartForm.Value)
					}
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					defer file.Close()
					got, err := io.ReadAll(file)
					if err != nil || !bytes.Equal(got, data) {
						t.Errorf("fallback bytes changed: got %d bytes, error %v", len(got), err)
					}
					fmt.Fprint(w, `{"code":0,"data":{"file_key":"original-file"}}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			b := &Bridge{client: lark.NewClient("upload-test-"+tc.name, "secret", lark.WithOpenBaseUrl(server.URL))}
			kind, key, err := b.uploadArtifact(context.Background(), artifactRef{name: "plots/chart.png"}, data)
			if images.Load() != 1 {
				t.Fatalf("image requests: %d", images.Load())
			}
			if tc.fallback {
				if err != nil || kind != "file" || key != "original-file" || files.Load() != 1 {
					t.Fatalf("file fallback: %q %q %v, file requests=%d", kind, key, err, files.Load())
				}
			} else {
				issue, ok := capabilityPermissionIssueFromError(err, capabilityArtifactUpload, "附件上传")
				if !ok || !slices.Equal(issue.Scopes, []string{artifactUploadScope}) || files.Load() != 0 {
					t.Fatalf("permission failure=%v issue=%+v file requests=%d", err, issue, files.Load())
				}
			}
		})
	}
}

type artifactTestReadHub struct {
	materialTestHub
}

func (h *artifactTestReadHub) Route(ctx context.Context, target, method string, payload interface{}) (*pb.TunnelMsg, error) {
	req := payload.(*pb.SessionFileRequest)
	switch req.FileName {
	case "large.bin":
		return nil, errors.New(status.Error(codes.ResourceExhausted, sessionfiles.ErrArtifactTooLarge.Error()).Error())
	case "empty.txt":
		h.readData = nil
	case "valid.txt":
		h.readData = []byte("valid")
	}
	return h.materialTestHub.Route(ctx, target, method, payload)
}

func TestArtifactDeliveryContinuesAfterInvalidEmptyAndOversizedFiles(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.hub = &artifactTestReadHub{}
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root", TargetID: "target", SessionName: "session"}
	if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: chat.BridgeId, RootMsgID: chat.RootMsgID, MessageID: "input", TargetID: chat.TargetID, SessionName: chat.SessionName, SessionID: "session-id", LaunchID: "launch", TurnID: "turn"}}); err != nil {
		t.Fatal(err)
	}
	var notices, uploaded []string
	sends := 0
	b.threadNotice = func(ctx context.Context, root, notice string) error {
		notices = append(notices, notice)
		if root != chat.RootMsgID || ctx.Err() != nil {
			t.Errorf("invalid notice destination or context: %s %v", root, ctx.Err())
		}
		return nil
	}
	b.artifactUpload = func(_ context.Context, name string, _ bool, data []byte) (string, string, error) {
		uploaded = append(uploaded, name)
		if string(data) != "valid" {
			t.Errorf("unexpected upload data: %q", data)
		}
		return "file", "file-key", nil
	}
	b.artifactSend = func(_ context.Context, _ *LarkChat, _, _, _ string) error {
		sends++
		return nil
	}
	text := "[bad](amux-artifact:../escape) [large](amux-artifact:large.bin) [empty](amux-artifact:empty.txt) [valid](amux-artifact:valid.txt)"
	b.deliverSessionArtifacts(context.Background(), chat, "turn", text, nil)
	if len(uploaded) != 1 || uploaded[0] != "valid.txt" || sends != 1 {
		t.Fatalf("valid file didn't arrive exactly once: uploads=%v sends=%d", uploaded, sends)
	}
	if len(notices) != 3 {
		t.Fatalf("notices=%v", notices)
	}
	for i, want := range []string{"invalid paths", "exceeds the 30 MB limit", "is empty"} {
		if !strings.Contains(notices[i], want) || strings.Contains(notices[i], "session.artifact.") {
			t.Errorf("incorrect failure notice: %s", notices[i])
		}
	}
}
