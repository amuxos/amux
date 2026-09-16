package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// Exercise the incoming Feishu event through the real router to the runtime
// Submit boundary. Only the remote runtime and Feishu resource API are fakes.
func TestGroupWatchEventSubmitsPostImages(t *testing.T) {
	for _, content := range []string{
		`{"image_key":"img_v3_failure"}`,
		`{"content":[[{"tag":"img","image_key":"img_v3_failure"}]]}`,
		`{"content":[[{"tag":"text","text":"inspect this failure"},{"tag":"img","image_key":"img_v3_failure"}]]}`,
		`{"content":[[{"tag":"md","text":"inspect this failure ![screenshot](img_v3_failure)"}]]}`,
	} {
		t.Run(content, func(t *testing.T) {
			b, hub := groupWatchMediaBridge(t)
			data := bridgeTestPNG(t)
			b.imageResourceFetch = func(_ context.Context, messageID, imageKey string) (io.Reader, string, error) {
				if messageID != "om_media" || imageKey != "img_v3_failure" {
					t.Fatalf("unexpected resource: %s/%s", messageID, imageKey)
				}
				return bytes.NewReader(data), "failure.png", nil
			}
			event := groupWatchBotEvent("oc_lab", "om_media", "ou_importer", "")
			event.Event.Message.MessageType = testStringPtr("post")
			if strings.HasPrefix(content, `{"image_key"`) {
				event.Event.Message.MessageType = testStringPtr("image")
			}
			event.Event.Message.Content = &content
			if err := b.handleMessage(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			request := groupWatchMediaSubmit(t, hub)
			if len(request.Parts) != 2 || request.Parts[0].GetText() == "" || !bytes.Equal(request.Parts[1].GetImage().GetData(), data) {
				t.Fatalf("Submit parts = %v; want text and original screenshot bytes", request.Parts)
			}
		})
	}
}

func TestGroupWatchPollSubmitsPostImagesOnce(t *testing.T) {
	for _, content := range []string{
		`{"image_key":"img_v3_failure"}`,
		`{"content":[[{"tag":"img","image_key":"img_v3_failure"}]]}`,
		`{"content":[[{"tag":"text","text":"inspect this failure"},{"tag":"img","image_key":"img_v3_failure"}]]}`,
		`{"content":[[{"tag":"md","text":"inspect this failure ![screenshot](img_v3_failure)"}]]}`,
	} {
		t.Run(content, func(t *testing.T) {
			b, hub := groupWatchMediaBridge(t)
			data := bridgeTestPNG(t)
			fetches := 0
			b.imageResourceFetch = func(_ context.Context, messageID, imageKey string) (io.Reader, string, error) {
				fetches++
				if messageID != "om_media" || imageKey != "img_v3_failure" {
					t.Fatalf("unexpected resource: %s/%s", messageID, imageKey)
				}
				return bytes.NewReader(data), "failure.png", nil
			}
			now := time.Now()
			kind := "post"
			if strings.HasPrefix(content, `{"image_key"`) {
				kind = "image"
			}
			b.groupWatchListMessages = func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error) {
				return []*larkim.Message{{
					MessageId: testStringPtr("om_media"), ChatId: testStringPtr("oc_lab"), MsgType: &kind,
					CreateTime: testStringPtr(strconv.FormatInt(now.Add(-time.Second).UnixMilli(), 10)),
					Sender:     &larkim.Sender{Id: testStringPtr("cli_importer"), IdType: testStringPtr("app_id"), SenderType: testStringPtr("app")},
					Body:       &larkim.MessageBody{Content: &content},
				}}, nil
			}
			for range 2 {
				if err := b.pollGroupWatchOnce(context.Background(), now); err != nil {
					t.Fatal(err)
				}
			}
			// An event arriving after the poll must not duplicate the attachment or turn.
			event := groupWatchBotEvent("oc_lab", "om_media", "ou_importer", "")
			event.Event.Message.MessageType = testStringPtr("post")
			if strings.HasPrefix(content, `{"image_key"`) {
				event.Event.Message.MessageType = testStringPtr("image")
			}
			event.Event.Message.Content = &content
			if err := b.handleMessage(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			request := groupWatchMediaSubmit(t, hub)
			if fetches != 1 || len(request.Parts) != 2 || !bytes.Equal(request.Parts[1].GetImage().GetData(), data) {
				t.Fatalf("fetches=%d, Submit parts=%v; want one original screenshot", fetches, request.Parts)
			}
		})
	}
}

func groupWatchMediaBridge(t *testing.T) (*Bridge, *bridgeTestHub) {
	t.Helper()
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli_amux", OpenId: "owner-open", BotOpenId: "ou_amux"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{
		Enabled: true, ChatIDs: []string{"oc_lab"}, SenderTypes: []string{"app"},
	}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	hub.listResp = &pb.ListResponse{Sessions: []*pb.Session{{
		Name:  naturalEntrySessionName(b.currentBridgeId(), "oc_lab", "om_media"),
		State: "running", InputModalities: []string{"text", "image"},
	}}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	b.threadNotice = func(context.Context, string, string) error { return nil }
	return b, hub
}

func TestGroupWatchPreservesNamedLinkTargets(t *testing.T) {
	for _, source := range []string{"event", "poll"} {
		t.Run(source, func(t *testing.T) {
			b, hub := groupWatchMediaBridge(t)
			content := `{"content":[[{"tag":"text","text":"inspect "},{"tag":"a","text":"回溯任务","href":"https://example.com/jobs/42?stage=7"}],[{"tag":"a","text":"https://example.com/log","href":"https://example.com/log"}],[{"tag":"a","href":"https://example.com/unnamed"}]]}`
			if source == "event" {
				event := groupWatchBotEvent("oc_lab", "om_media", "ou_importer", "")
				event.Event.Message.MessageType = testStringPtr("post")
				event.Event.Message.Content = &content
				if err := b.handleMessage(context.Background(), event); err != nil {
					t.Fatal(err)
				}
			} else {
				now := time.Now()
				b.groupWatchListMessages = func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error) {
					return []*larkim.Message{{
						MessageId: testStringPtr("om_media"), ChatId: testStringPtr("oc_lab"), MsgType: testStringPtr("post"),
						CreateTime: testStringPtr(strconv.FormatInt(now.Add(-time.Second).UnixMilli(), 10)),
						Sender:     &larkim.Sender{Id: testStringPtr("cli_importer"), SenderType: testStringPtr("app")},
						Body:       &larkim.MessageBody{Content: &content},
					}}, nil
				}
				if err := b.pollGroupWatchOnce(context.Background(), now); err != nil {
					t.Fatal(err)
				}
			}
			text := groupWatchMediaSubmit(t, hub).Text
			for _, want := range []string{"回溯任务", "https://example.com/jobs/42?stage=7", "https://example.com/log", "https://example.com/unnamed"} {
				if strings.Count(text, want) != 1 {
					t.Errorf("Submit text = %q, want %q exactly once", text, want)
				}
			}
		})
	}
}

func TestGroupWatchImageFailureKeepsTextAndNotifies(t *testing.T) {
	for _, source := range []string{"event", "poll"} {
		for _, reason := range []string{"unsupported", "read-failed"} {
			t.Run(source+"/"+reason, func(t *testing.T) {
				b, hub := groupWatchMediaBridge(t)
				if reason == "unsupported" {
					hub.listResp.Sessions[0].InputModalities = []string{"text"}
				}
				fetches := 0
				b.imageResourceFetch = func(context.Context, string, string) (io.Reader, string, error) {
					fetches++
					return nil, "", errors.New("resource unavailable")
				}
				var notices []string
				b.threadNotice = func(_ context.Context, root, text string) error {
					if root != "om_media" {
						t.Errorf("notice root = %q, want om_media", root)
					}
					notices = append(notices, text)
					return nil
				}
				content := `{"content":[[{"tag":"text","text":"inspect failure"},{"tag":"a","text":"logs","href":"https://example.com/log"},{"tag":"img","image_key":"img_v3_failure"}]]}`
				if source == "event" {
					event := groupWatchBotEvent("oc_lab", "om_media", "ou_importer", "")
					event.Event.Message.MessageType = testStringPtr("post")
					event.Event.Message.Content = &content
					if err := b.handleMessage(context.Background(), event); err != nil {
						t.Fatal(err)
					}
				} else {
					now := time.Now()
					b.groupWatchListMessages = func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error) {
						return []*larkim.Message{{
							MessageId: testStringPtr("om_media"), ChatId: testStringPtr("oc_lab"), MsgType: testStringPtr("post"),
							CreateTime: testStringPtr(strconv.FormatInt(now.Add(-time.Second).UnixMilli(), 10)),
							Sender:     &larkim.Sender{Id: testStringPtr("cli_importer"), SenderType: testStringPtr("app")},
							Body:       &larkim.MessageBody{Content: &content},
						}}, nil
					}
					if err := b.pollGroupWatchOnce(context.Background(), now); err != nil {
						t.Fatal(err)
					}
				}
				request := groupWatchMediaSubmit(t, hub)
				if len(request.Parts) != 1 || request.Parts[0].GetImage() != nil || !strings.Contains(request.Text, "inspect failure") || !strings.Contains(request.Text, "https://example.com/log") {
					t.Fatalf("Submit = %+v, want usable text and URL without image", request)
				}
				wantFetches, wantNotice := 1, "图片暂时无法读取"
				if reason == "unsupported" {
					wantFetches, wantNotice = 0, "当前模型暂不支持图片"
				}
				if fetches != wantFetches || len(notices) != 1 || !strings.Contains(notices[0], wantNotice) {
					t.Fatalf("fetches=%d notices=%v, want %d fetches and %q", fetches, notices, wantFetches, wantNotice)
				}
			})
		}
	}
}

func groupWatchMediaSubmit(t *testing.T, hub *bridgeTestHub) *pb.SubmitRequest {
	t.Helper()
	var requests []*pb.SubmitRequest
	for _, call := range hub.routes {
		if call.method == "Submit" {
			requests = append(requests, call.payload.(*pb.SubmitRequest))
		}
	}
	if len(requests) != 1 {
		t.Fatalf("Submit count = %d, want exactly one", len(requests))
	}
	return requests[0]
}
