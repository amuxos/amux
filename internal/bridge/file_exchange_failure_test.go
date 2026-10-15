package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/sessionfiles"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type failingFileHub struct {
	materialTestHub
	cause error
}

func (h *failingFileHub) Route(ctx context.Context, target, method string, payload interface{}) (*pb.TunnelMsg, error) {
	if method == "ReadSessionArtifact" || method == "PutSessionFile" {
		return nil, h.cause
	}
	return h.materialTestHub.Route(ctx, target, method, payload)
}

func TestFileExchangeFailureDiagnosticsAndLocales(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN", "auto"} {
		for _, action := range []string{"read", "put"} {
			for _, upgrade := range []bool{true, false} {
				t.Run(locale+"/"+action+"/"+map[bool]string{true: "upgrade", false: "permission"}[upgrade], func(t *testing.T) {
					b, _ := newBridgeForInputTest(t)
					cause := errors.New("permission denied")
					if upgrade {
						cause = errors.New(status.Error(codes.Unimplemented, sessionfiles.ErrHelperUpgradeRequired.Error()+": unsupported helper operation").Error())
					}
					h := &failingFileHub{cause: cause}
					h.listResp = &pb.ListResponse{Sessions: []*pb.Session{{Id: "sid", Name: "session", LaunchId: "launch"}}}
					b.hub = h
					chat := &LarkChat{BridgeId: b.currentBridgeId(), ChatID: "dm", RootMsgID: "root", OpenId: "owner", TargetID: "runtime", SessionName: "session", Status: "active"}
					if err := b.db.EnsureWorkspaceKind(chat.BridgeId, chat.ChatID, WorkspaceKindPrivate); err != nil {
						t.Fatal(err)
					}
					if err := b.db.UpsertChat(*chat); err != nil {
						t.Fatal(err)
					}
					if err := b.db.setManagementLocale(chat.BridgeId, chat.OpenId, locale); err != nil {
						t.Fatal(err)
					}
					if err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: chat.BridgeId, RootMsgID: "root", MessageID: "input", TargetID: "runtime", SessionName: "session", SessionID: "sid", LaunchID: "launch", TurnID: "turn"}}); err != nil {
						t.Fatal(err)
					}
					var notices []string
					b.threadNotice = func(_ context.Context, _ string, text string) error { notices = append(notices, text); return nil }
					var logs bytes.Buffer
					previous := log.Logger
					log.Logger = zerolog.New(&logs)
					defer func() { log.Logger = previous }()
					if action == "read" {
						b.deliverSessionArtifacts(context.Background(), chat, "turn", "[image](amux-artifact:result.png)", nil)
					} else {
						b.fileResourceFetch = func(context.Context, string, string) (io.Reader, string, error) {
							return strings.NewReader("payload"), "result.png", nil
						}
						ctx := withFeishuImages(context.Background(), feishuImageContext{chatID: "dm"})
						collectMaterials(ctx, "message", messageMaterials{files: []feishuFileRef{{"message", "key", "result.png"}}})
						if _, _, err := b.prepareFileMaterials(ctx, chat, "read"); err != nil {
							t.Fatal(err)
						}
					}
					if len(notices) != 1 {
						t.Fatalf("notices=%v", notices)
					}
					text := notices[0]
					if !strings.Contains(text, "result.png") || strings.Contains(text, "session.file.") || strings.Contains(text, cause.Error()) {
						t.Fatalf("unsafe notice: %s", text)
					}
					if strings.Contains(text, "amux isolation helper install") != upgrade {
						t.Fatalf("incorrect recovery: %s", text)
					}
					if upgrade && strings.Contains(text, "30 MB") {
						t.Fatalf("misleading size: %s", text)
					}
					if strings.Contains(text, "请联系管理员") != (locale != "en") {
						t.Fatalf("Chinese locale: %s", text)
					}
					if strings.Contains(text, "Ask the operator") != (locale != "zh-CN") {
						t.Fatalf("English locale: %s", text)
					}
					for _, want := range []string{"result.png", `"target_id":"runtime"`, `"session_id":"sid"`, `"launch_id":"launch"`, cause.Error()} {
						if !strings.Contains(logs.String(), want) {
							t.Fatalf("missing %q from logs: %s", want, logs.String())
						}
					}
				})
			}
		}
	}
}
