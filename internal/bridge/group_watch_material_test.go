package bridge

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Trigger selection is separate from material processing. Once admitted, a
// watcher must use the same file and merged-forward path as ordinary routing.
func TestGroupWatchAdmittedInputUsesSharedMaterials(t *testing.T) {
	for _, source := range []string{"event", "poll"} {
		for _, kind := range []string{"file", "merge_forward"} {
			t.Run(source+"/"+kind, func(t *testing.T) {
				b, base := groupWatchMediaBridge(t)
				hub := &materialTestHub{bridgeTestHub: *base}
				b.hub = hub
				hub.listResp.Sessions[0].Id = "session"
				hub.listResp.Sessions[0].LaunchId = "launch"
				content := `{"file_key":"file-key","file_name":"input.csv"}`
				if kind == "merge_forward" {
					content = `Merged and Forwarded Message`
					b.quotedMessageLookup = func(_ context.Context, id string) (feishuQuotedMessage, error) {
						if id != "om_media" {
							t.Fatalf("unexpected container %q", id)
						}
						return feishuQuotedMessage{MessageID: id, ChatID: "oc_lab", Text: "forwarded context", Materials: messageMaterials{
							files: []feishuFileRef{{id, "file-key", "input.csv"}},
						}}, nil
					}
				}
				b.fileResourceFetch = func(_ context.Context, id, key string) (io.Reader, string, error) {
					if id != "om_media" || key != "file-key" {
						t.Fatalf("wrong resource authority %s/%s", id, key)
					}
					return strings.NewReader("amount\n17\n"), "input.csv", nil
				}
				// A reused poll context must not import another message's files or
				// identity. Each routed message starts its own material collection.
				ctx := withMaterialCollection(context.Background())
				collectMaterials(ctx, "unrelated", messageMaterials{files: []feishuFileRef{{"unrelated", "secret", "secret.csv"}}})
				event := groupWatchBotEvent("oc_lab", "om_media", "ou_importer", "")
				event.Event.Message.MessageType, event.Event.Message.Content = &kind, &content
				if source == "event" {
					if err := b.handleMessage(ctx, event); err != nil {
						t.Fatal(err)
					}
				} else {
					now := time.Now()
					b.groupWatchListMessages = func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error) {
						return []*larkim.Message{{ChatId: testStringPtr("oc_lab"), MessageId: testStringPtr("om_media"), MsgType: &kind,
							CreateTime: testStringPtr(strconv.FormatInt(now.Add(-time.Second).UnixMilli(), 10)),
							Sender:     &larkim.Sender{Id: testStringPtr("cli_importer"), SenderType: testStringPtr("app")},
							Body:       &larkim.MessageBody{Content: &content}}}, nil
					}
					for range 2 {
						if err := b.pollGroupWatchOnce(ctx, now); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Event redelivery (including after polling) must not resubmit files.
				if err := b.handleMessage(ctx, event); err != nil {
					t.Fatal(err)
				}

				request := groupWatchMediaSubmit(t, &hub.bridgeTestHub)
				if len(hub.writes) != 1 || string(hub.writes[0].Data) != "amount\n17\n" || hub.writes[0].SessionId != "session" || hub.writes[0].LaunchId != "launch" {
					t.Fatalf("material writes = %+v", hub.writes)
				}
				if request.ExpectedSessionId != "session" || request.ExpectedLaunchId != "launch" || !strings.Contains(request.Text, "/target/amux-inbox/digest/input.csv") {
					t.Fatalf("unbound material Submit: %+v", request)
				}
				if kind == "merge_forward" && !strings.Contains(request.Text, "forwarded context") {
					t.Fatal("merged context omitted")
				}
				if !strings.Contains(request.Parts[0].GetText(), artifactInstructions) {
					t.Fatal("watcher missing shared artifact delivery guidance")
				}
				if len(materialsFromContext(ctx).files) != 1 || materialsFromContext(ctx).files[0].messageID != "unrelated" {
					t.Fatal("mutated parent poll context")
				}
			})
		}
	}

}

func TestGroupWatchEmptyOrInvalidRootsDoNotTrigger(t *testing.T) {
	for _, tc := range []struct{ kind, content string }{
		{"text", `{"text":" "}`}, {"image", `{}`}, {"file", `{"file_key":" "}`},
		{"image", `null`}, {"image", `{`}, {"post", `{"content":[]}`},
		{"audio", `{"file_key":"audio-key"}`},
	} {
		t.Run(tc.kind+tc.content, func(t *testing.T) {
			b, hub := groupWatchMediaBridge(t)
			event := groupWatchBotEvent("oc_lab", "om_media", "ou_importer", "")
			event.Event.Message.MessageType, event.Event.Message.Content = &tc.kind, &tc.content
			if handled, err := b.handleGroupWatchEvent(context.Background(), event); handled || err != nil {
				t.Fatalf("invalid root auto-triggered: %v %v", handled, err)
			}
			now := time.Now()
			if _, admitted := b.groupWatchMessageFromHistory(&larkim.Message{
				ChatId: testStringPtr("oc_lab"), MessageId: testStringPtr("om_media"), MsgType: &tc.kind,
				CreateTime: testStringPtr(strconv.FormatInt(now.UnixMilli(), 10)),
				Sender:     &larkim.Sender{Id: testStringPtr("cli_importer"), SenderType: testStringPtr("app")},
				Body:       &larkim.MessageBody{Content: &tc.content},
			}, now.Add(-time.Minute), now); admitted {
				t.Fatal("poll admitted invalid root")
			}
			for _, call := range hub.routes {
				if _, ok := call.payload.(*pb.SubmitRequest); ok {
					t.Fatal("unexpected automatic Submit")
				}
			}
		})
	}
}
