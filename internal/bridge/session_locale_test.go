package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestSessionLocalePrivateTopicsAndGroupIsolation(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	bot := b.currentBridgeId()
	if err := b.db.setManagementLocale(bot, "owner", "en"); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		chat, kind, owner string
		want              i18n.Locale
	}{
		{"dm", WorkspaceKindPrivate, "owner", i18n.English},
		{"group", WorkspaceKindGroup, "owner", ""},
		{"topic", WorkspaceKindTopicGroup, "owner", ""},
		{"other-dm", WorkspaceKindPrivate, "another-user", ""},
	} {
		t.Run(sample.chat, func(t *testing.T) {
			if err := b.db.EnsureWorkspaceKind(bot, sample.chat, sample.kind); err != nil {
				t.Fatal(err)
			}
			chat := &LarkChat{BridgeId: bot, ChatID: sample.chat, RootMsgID: "root-" + sample.chat, OpenId: sample.owner, SessionName: "session-" + sample.chat, TargetID: "runtime", Status: "active"}
			if err := b.db.UpsertChat(*chat); err != nil {
				t.Fatal(err)
			}
			delivery, err := b.prepareSessionCardDelivery(chat)
			if err != nil || delivery.locale != sample.want {
				t.Fatalf("locale=%q err=%v", delivery.locale, err)
			}
		})
	}
	// Caller-supplied author and chat IDs cannot turn an unrecorded group into a DM.
	delivery, err := b.prepareSessionCardDelivery(&LarkChat{ChatID: "dm", OpenId: "owner", RootMsgID: "unknown"})
	if err != nil || delivery.locale != "" {
		t.Fatal("unknown binding inherited owner preference")
	}
	if err := b.db.setManagementLocale(bot, "owner", "auto"); err != nil {
		t.Fatal(err)
	}
	delivery, err = b.prepareSessionCardDelivery(&LarkChat{RootMsgID: "root-dm"})
	if err != nil || delivery.locale != "" {
		t.Fatal("auto did not restore native language")
	}
}

func TestSessionLocaleCardKitCreationInsertionAndReplacement(t *testing.T) {
	t.Parallel()
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese, ""} {
		for _, activityFirst := range []bool{true, false} {
			t.Run(string(locale)+map[bool]string{true: "-activity", false: "-answer"}[activityFirst], func(t *testing.T) {
				api := &fakeCardKitReplyAPI{cardID: "card-locale"}
				nextLocale := locale
				backend := &bindingReplyCardBackend{cardKit: api, prepareDelivery: func() (replyCardDelivery, error) {
					return replyCardDelivery{locale: nextLocale, send: func(context.Context, string, string) (string, error) { return "message", nil }}, nil
				}}
				activity := renderTurnActivity(toolActivityCardMeta{Status: "waiting", Activity: &agentui.ActivityView{Status: "waiting", CurrentStep: "Agent 中文原文"}})
				var handle *replyCardHandle
				var err error
				if activityFirst {
					handle, err = backend.CreateActivity(context.Background(), activity)
				} else {
					handle, err = backend.Create(context.Background(), "Agent 中文原文")
				}
				if err != nil {
					t.Fatal(err)
				}
				// A later route/preference change cannot alter this already-delivered card.
				nextLocale = i18n.Chinese
				if activityFirst {
					err = backend.Update(context.Background(), handle, "Agent 中文原文")
				} else {
					err = backend.UpdateActivity(context.Background(), handle, activity)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = backend.UpdateActivity(context.Background(), handle, renderTurnActivity(toolActivityCardMeta{Status: "done"})); err != nil {
					t.Fatal(err)
				}
				raws := append([]string{}, api.createdData...)
				for _, insert := range api.inserts {
					raws = append(raws, insert.Elements)
				}
				for _, replace := range api.replaces {
					raws = append(raws, replace.Element)
				}
				for _, raw := range raws {
					if locale != "" && strings.Contains(raw, "i18n_content") {
						t.Fatalf("explicit preference lost: %s", raw)
					}
					if locale == "" && !strings.Contains(raw, "i18n_content") {
						t.Fatal("shared card lost native translations")
					}
					if locale == i18n.English && (strings.Contains(raw, "等待你回复") || strings.Contains(raw, "已完成") || strings.Contains(raw, "**最终结果**")) {
						t.Fatal("Chinese product copy in English private card")
					}
				}
				if err := backend.Update(context.Background(), handle, "literal 最终结果"); err != nil {
					t.Fatal(err)
				}
				if api.updates[len(api.updates)-1].Text != "literal 最终结果" {
					t.Fatal("Agent body translated")
				}
			})
		}
	}
}

func TestSessionLocaleRetryKeepsOriginalRecipient(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	bot := b.currentBridgeId()
	if err := b.db.setManagementLocale(bot, "owner", "en"); err != nil {
		t.Fatal(err)
	}
	bs := BotSession{BridgeId: bot, Enabled: true, SessionName: "bot-session", TargetID: "runtime", LastReplyKind: botSessionReplyDM, LastReplyOpenID: "owner"}
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	var destinations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		destinations = append(destinations, r.URL.Query().Get("receive_id_type")+":"+request["receive_id"].(string))
		_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"message"}}`))
	}))
	defer server.Close()
	b.client = lark.NewClient("session-locale-app", "secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	delivery, err := b.prepareSessionCardDelivery(&LarkChat{RootMsgID: singletonStreamKey(bot)})
	if err != nil || delivery.locale != i18n.English {
		t.Fatalf("delivery: %+v %v", delivery, err)
	}
	bs.LastReplyKind = botSessionReplyGroup
	bs.LastReplyChatID = "shared-group"
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	handle := &replyCardHandle{delivery: delivery, cardID: "card", messageUUID: "same-id", deliveryPending: true}
	backend := &bindingReplyCardBackend{sendCard: func(context.Context, string, string) (string, error) {
		return "", errors.New("must use original destination")
	}}
	if err := backend.ensureDelivered(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 1 || destinations[0] != "open_id:owner" {
		t.Fatalf("retry redirected to group: %v", destinations)
	}
	group, err := b.prepareSessionCardDelivery(&LarkChat{RootMsgID: singletonStreamKey(bot)})
	if err != nil || group.locale != "" {
		t.Fatal("new group card inherited DM language")
	}
}

func TestSessionLocaleLegacyReceiptsPersistAndRemainPrivate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	b := &Bridge{bridgeID: "bot-a", db: db}
	if err := db.setManagementLocale("bot-a", "owner", "en"); err != nil {
		t.Fatal(err)
	}
	b.rememberPrivateSessionCard("owner", "private-card")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b.db = db
	raw := renderToolActivityCard(toolActivityCardMeta{Status: "waiting"}, "")
	selected := b.sessionCardForPatch("private-card", raw)
	if strings.Contains(selected, "i18n_content") || !strings.Contains(selected, "Waiting for your reply") {
		t.Fatal("private legacy patch ignored preference")
	}
	if got := b.sessionCardForPatch("group-card", raw); got != raw {
		t.Fatal("unrecorded shared card selected a language")
	}
	b.bridgeID = "bot-b"
	if got := b.sessionCardForPatch("private-card", raw); got != raw {
		t.Fatal("receipt leaked between Bots")
	}
}
