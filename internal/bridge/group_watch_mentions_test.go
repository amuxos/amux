package bridge

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestGroupWatchAppMentionRouting(t *testing.T) {
	human := &larkim.MentionEvent{Id: &larkim.UserId{OpenId: testStringPtr("ou_oncall")}, Name: testStringPtr("On-call"), MentionedType: testStringPtr("user")}
	otherBot := &larkim.MentionEvent{Id: &larkim.UserId{OpenId: testStringPtr("ou_peer")}, Name: testStringPtr("Peer"), MentionedType: testStringPtr("bot")}
	currentBot := &larkim.MentionEvent{Id: &larkim.UserId{OpenId: testStringPtr("ou_amux")}, MentionedType: testStringPtr("bot")}
	untypedHuman := &larkim.MentionEvent{Id: human.Id, Name: human.Name}
	untypedBot := &larkim.MentionEvent{Id: otherBot.Id, Name: otherBot.Name}
	appBot := &larkim.MentionEvent{Id: otherBot.Id, MentionedType: testStringPtr("app")}
	for _, source := range []string{"event", "poll"} {
		for _, tc := range []struct {
			name     string
			mentions []*larkim.MentionEvent
			wantTask bool
		}{
			{"none", nil, true},
			{"human", []*larkim.MentionEvent{human}, true},
			{"multiple_humans", []*larkim.MentionEvent{human, {Id: &larkim.UserId{OpenId: testStringPtr("ou_backup")}, MentionedType: testStringPtr("user")}}, true},
			{"untyped_human", []*larkim.MentionEvent{untypedHuman}, true},
			{"human_named_like_bot", []*larkim.MentionEvent{human}, true},
			{"other_bot", []*larkim.MentionEvent{otherBot}, false},
			{"app_bot", []*larkim.MentionEvent{appBot}, false},
			{"untyped_bot", []*larkim.MentionEvent{untypedBot}, false},
			{"current_bot", []*larkim.MentionEvent{currentBot}, false},
			{"mixed", []*larkim.MentionEvent{human, otherBot}, false},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				b, hub := groupWatchMediaBridge(t)
				if tc.name == "human_named_like_bot" {
					b.secrets.BotName = "On-call"
				}
				lookups := 0
				b.externalBotCatalogFetch = func(_ context.Context, chatID string) (externalBotCatalogSnapshot, error) {
					lookups++
					if chatID != "oc_lab" {
						t.Fatalf("catalog chat = %q", chatID)
					}
					return externalBotCatalogSnapshot{Complete: true, Members: []externalBotCatalogMember{{BotOpenID: "ou_amux"}, {BotOpenID: "ou_peer"}}}, nil
				}
				event, history := groupWatchMentionAlert(tc.mentions)
				for range 2 {
					if source == "event" {
						if err := b.handleMessage(context.Background(), event); err != nil {
							t.Fatal(err)
						}
					} else {
						b.groupWatchListMessages = func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error) {
							return []*larkim.Message{history}, nil
						}
						if err := b.pollGroupWatchOnce(context.Background(), time.Now()); err != nil {
							t.Fatal(err)
						}
					}
				}
				wantRoutes := 0
				if tc.wantTask {
					wantRoutes = 2
				}
				if len(hub.routes) != wantRoutes {
					t.Fatalf("routes = %+v; want %d (one Run+Submit for human alerts, none for bot requests)", hub.routes, wantRoutes)
				}
				if tc.wantTask && (hub.routes[0].method != "Run" || hub.routes[1].method != "Submit") {
					t.Fatalf("routes = %+v; want Run+Submit", hub.routes)
				}
				if source == "event" && !strings.HasPrefix(tc.name, "untyped") && lookups != 0 {
					t.Fatalf("typed event performed %d unnecessary lookups", lookups)
				}
			})
		}
	}
}

func TestGroupWatchPollSharesMentionLookupPerChat(t *testing.T) {
	for _, failLookup := range []bool{false, true} {
		t.Run(strconv.FormatBool(failLookup), func(t *testing.T) {
			b, hub := groupWatchMediaBridge(t)
			lookups := 0
			b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
				lookups++
				if failLookup {
					return externalBotCatalogSnapshot{}, errors.New("catalog unavailable")
				}
				return externalBotCatalogSnapshot{Complete: true}, nil
			}
			_, mentioned := groupWatchMentionAlert([]*larkim.MentionEvent{{Id: &larkim.UserId{OpenId: testStringPtr("ou_oncall")}}})
			second := *mentioned
			second.MessageId = testStringPtr("om_second")
			_, plain := groupWatchMentionAlert(nil)
			plain.MessageId = testStringPtr("om_plain")
			b.groupWatchListMessages = func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error) {
				return []*larkim.Message{mentioned, &second, plain}, nil
			}
			err := b.pollGroupWatchOnce(context.Background(), time.Now())
			if (err != nil) != failLookup {
				t.Fatalf("poll error = %v; want error=%v", err, failLookup)
			}
			wantRoutes := 6
			if failLookup {
				wantRoutes = 2 // An ambiguous mention must not block the plain alert.
			}
			if lookups != 1 || len(hub.routes) != wantRoutes {
				t.Fatalf("lookups=%d, routes=%+v; want one lookup and %d routes", lookups, hub.routes, wantRoutes)
			}
		})
	}
}

func TestGroupWatchRequiresMemberScopeWithoutBotCollaboration(t *testing.T) {
	b, _ := groupWatchMediaBridge(t)
	b.botToBot.Enabled = false
	if !strings.Contains(strings.Join(testActiveFeishuTenantScopes(b), ","), "im:chat.members:read") {
		t.Fatal("group watch must declare the scope used to classify history mentions")
	}
}

func TestGroupWatchMentionLookupFailureCanRecover(t *testing.T) {
	for _, source := range []string{"event", "poll"} {
		for _, failure := range []string{"unavailable", "incomplete"} {
			t.Run(source+"/"+failure, func(t *testing.T) {
				b, hub := groupWatchMediaBridge(t)
				b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
					if failure == "unavailable" {
						return externalBotCatalogSnapshot{}, errors.New("catalog unavailable")
					}
					return externalBotCatalogSnapshot{Complete: false}, nil
				}
				event, history := groupWatchMentionAlert([]*larkim.MentionEvent{{Id: &larkim.UserId{OpenId: testStringPtr("ou_oncall")}}})
				b.groupWatchListMessages = func(context.Context, string, time.Time, time.Time) ([]*larkim.Message, error) {
					return []*larkim.Message{history}, nil
				}
				run := func() error {
					if source == "event" {
						return b.handleMessage(context.Background(), event)
					}
					return b.pollGroupWatchOnce(context.Background(), time.Now())
				}
				if err := run(); err == nil || len(hub.routes) != 0 {
					t.Fatalf("unresolved mention: err=%v, routes=%+v; want error without submission", err, hub.routes)
				}
				b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
					return externalBotCatalogSnapshot{Complete: true}, nil
				}
				if err := run(); err != nil {
					t.Fatal(err)
				}
				if len(hub.routes) != 2 {
					t.Fatalf("routes after recovery = %+v; want Run+Submit", hub.routes)
				}
			})
		}
	}
}

// Feishu events include target types; the history API only returns target IDs.
func groupWatchMentionAlert(mentions []*larkim.MentionEvent) (*larkim.P2MessageReceiveV1, *larkim.Message) {
	content := `{"elements":[{"tag":"div","text":{"tag":"lark_md","content":"[warn] Service crashed; notify on-call"}}]}`
	event := groupWatchBotEvent("oc_lab", "om_media", "ou_alarm", "")
	event.Event.Message.MessageType = testStringPtr("interactive")
	event.Event.Message.Content = &content
	event.Event.Message.Mentions = mentions
	history := &larkim.Message{
		MessageId: testStringPtr("om_media"), ChatId: testStringPtr("oc_lab"), MsgType: testStringPtr("interactive"),
		CreateTime: testStringPtr(strconv.FormatInt(time.Now().Add(-time.Second).UnixMilli(), 10)),
		Sender:     &larkim.Sender{Id: testStringPtr("cli_alarm"), SenderType: testStringPtr("app")},
		Body:       &larkim.MessageBody{Content: &content},
	}
	for _, mention := range mentions {
		history.Mentions = append(history.Mentions, &larkim.Mention{Id: mention.Id.OpenId, Name: mention.Name})
	}
	return event, history
}
