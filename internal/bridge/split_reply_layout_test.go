package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestMentionConversationSectionPlacesActionAfterRecipientsAndExpandsRichContent(t *testing.T) {
	t.Parallel()
	action := json.RawMessage(`{"tag":"column_set","flex_mode":"none","columns":[{"tag":"column","width":"auto","elements":[{"tag":"button","name":"solved","text":{"tag":"plain_text","content":"已解决？","i18n_content":{"zh_cn":"已解决？","en_us":"Resolved?"}},"behaviors":[{"type":"callback","value":{"action":"feedback_solved","opaque":"keep"}}]}]}]}`)
	rich := json.RawMessage(`{"tag":"div","text":{"tag":"plain_text","content":"A complete draft","i18n_content":{"zh_cn":"完整草稿","en_us":"A complete draft"}},"extra_extension_attribute":"preserved"}`)
	for _, locale := range []i18n.Locale{i18n.Chinese, i18n.English} {
		t.Run(string(locale), func(t *testing.T) {
			ref := ConversationCard{MentionOpenIDs: []string{"ou-a", "ou-b"}, Locale: string(locale)}
			initial := mentionedConversationSection(ref.MentionOpenIDs)
			raw := renderConversationSectionUpdate(ref, []json.RawMessage{action})
			var section cardElement
			if err := json.Unmarshal([]byte(raw), &section); err != nil {
				t.Fatal(err)
			}
			if section.ElementID != initial.ElementID || len(section.Columns) != 1 {
				t.Fatalf("section changed shape: %s", raw)
			}
			children := section.Columns[0].Elements
			if len(children) != 1 || children[0].ElementID != amuxBrandFooterID || len(children[0].Columns) != 3 {
				t.Fatalf("action must follow recipients in footer row: %s", raw)
			}
			footer := children[0]
			if !strings.Contains(activityElementText(footer.Columns[1].Elements[0]), `<at id="ou-a"></at>`) || footer.Columns[2].Width != "auto" {
				t.Fatalf("recipients and action columns: %+v", footer)
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(raw, `"opaque":"keep"`) {
				t.Fatalf("extension callback was lost: %s", raw)
			}
			localized := selectManagementCardLocale(raw, locale)
			want := "Resolved?"
			if locale == i18n.Chinese {
				want = "已解决？"
			}
			if !strings.Contains(localized, want) {
				t.Fatalf("missing localized feedback %q: %s", want, localized)
			}
			raw = renderConversationSectionUpdate(ref, []json.RawMessage{rich, action})
			if err := json.Unmarshal([]byte(raw), &section); err != nil {
				t.Fatal(err)
			}
			children = section.Columns[0].Elements
			if len(children) != 3 || len(children[0].Columns) != 2 || children[1].Tag != "div" {
				t.Fatalf("rich content must use the full-width section below footer: %s", raw)
			}
			if !strings.Contains(raw, `"extra_extension_attribute":"preserved"`) {
				t.Fatalf("extension JSON was rewritten: %s", raw)
			}
			if got := renderConversationSectionUpdate(ConversationCard{}, []json.RawMessage{rich}); got != string(conversationSection([]json.RawMessage{rich})) {
				t.Fatalf("ordinary cards changed layout: %s", got)
			}
		})
	}
}

func TestMentionFinalCardRetainsOneNativeMentionAndOnlyAnswerIsQuoted(t *testing.T) {
	t.Parallel()
	for _, section := range []bool{false, true} {
		raw := renderMentionFinalCard("original answer", []string{"ou-a", "ou-a"}, section)
		requireCardMentions(t, raw, "ou-a")
		if got := extractInteractiveMessageText(&raw); got != "original answer" {
			t.Fatalf("quoted answer = %q", got)
		}
		var doc cardDoc
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Config.UpdateMulti == nil || !*doc.Config.UpdateMulti {
			t.Fatal("final CardKit must allow section updates")
		}
		var ids []string
		var walk func([]cardElement)
		seen := map[string]bool{}
		walk = func(elements []cardElement) {
			for _, e := range elements {
				if e.ElementID != "" {
					if seen[e.ElementID] {
						t.Fatalf("duplicate element_id %s", e.ElementID)
					}
					seen[e.ElementID] = true
					ids = append(ids, e.ElementID)
				}
				walk(e.Elements)
				for _, c := range e.Columns {
					walk(c.Elements)
				}
			}
		}
		walk(doc.Body.Elements)
		if seen[cardKitActivityContentID] || seen[cardKitAnswerHeadingID] {
			t.Fatalf("final contains activity: %v", ids)
		}
	}
}

func TestMentionConversationFinalDeliveryRetainsReferenceAcrossFailuresAndTurns(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"none", "send_once", "send_always", "compact_always"} {
		t.Run(failure, func(t *testing.T) {
			api := &fakeCardKitReplyAPI{cardIDs: []string{"progress-card", "final-card", "next-card"}}
			var sent, uuids []string
			sends := 0
			compacts := 0
			backend := &bindingReplyCardBackend{conversationSection: true, cardKit: api,
				prepareDelivery: func() (replyCardDelivery, error) {
					return replyCardDelivery{locale: i18n.English, send: func(_ context.Context, content, id string) (string, error) {
						sent = append(sent, content)
						uuids = append(uuids, id)
						if strings.Contains(content, "final-card") {
							sends++
							if failure == "send_always" || (failure == "send_once" && sends == 1) {
								return "", context.DeadlineExceeded
							}
							return "final-message", nil
						}
						if strings.Contains(content, "next-card") {
							return "next-message", nil
						}
						return "progress-message", nil
					}}, nil
				},
				sendMentionedFinal: func(context.Context, string, []string, string) (string, error) {
					t.Error("observer final used legacy send")
					return "", errors.New("unexpected")
				},
				compactMessage: func(_ context.Context, _ string, content string) error {
					compacts++
					replyCompletionCardText(t, content, i18n.English)
					if failure == "compact_always" {
						return errors.New("compact failed")
					}
					return nil
				},
			}
			sender := newStreamingReplySender(0, backend, nil)
			defer sender.Close()
			sender.enqueue(replyCommandAdd, "answer")
			sender.Flush()
			prior := sender.ConversationCard()
			completed := sender.CompleteTurn([]string{"ou-a"})
			if failure == "send_always" {
				if !reflect.DeepEqual(completed, prior) || compacts != 0 {
					t.Fatalf("failed final changed original binding: %+v -> %+v, compacts %d", prior, completed, compacts)
				}
			} else {
				if completed == nil || completed.MessageID != "final-message" || completed.CardID != "final-card" || completed.Sequence != 0 || completed.Locale != "en" || !reflect.DeepEqual(completed.MentionOpenIDs, []string{"ou-a"}) {
					t.Fatalf("final reference=%+v", completed)
				}
			}
			if failure == "send_once" || failure == "send_always" {
				if len(uuids) != 3 || uuids[1] == "" || uuids[1] != uuids[2] || sent[1] != sent[2] {
					t.Fatalf("send retry changed CardID/UUID: %v %v", sent, uuids)
				}
			}
			if len(api.createdData) != 2 {
				t.Fatalf("created %d cards; retry must reuse final CardID", len(api.createdData))
			}
			sender.enqueue(replyCommandAdd, "next answer")
			sender.Flush()
			next := sender.CompleteTurn(nil)
			if next == nil || next.CardID != "next-card" || next.MessageID != "next-message" || len(next.MentionOpenIDs) > 0 {
				t.Fatalf("prior final leaked to next turn: %+v", next)
			}
		})
	}
}

func TestMentionConversationLongFinalOnlyLastPartOwnsFeedback(t *testing.T) {
	t.Parallel()
	api := &fakeCardKitReplyAPI{cardID: "card"}
	var legacyParts []string
	var cardSends int
	backend := &bindingReplyCardBackend{conversationSection: true, cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			cardSends++
			return fmt.Sprintf("message-%d", cardSends), nil
		},
		sendMentionedFinal: func(_ context.Context, text string, ids []string, _ string) (string, error) {
			if len(ids) > 0 {
				t.Error("last card must use mutable CardKit")
			}
			legacyParts = append(legacyParts, text)
			return fmt.Sprintf("part-%d", len(legacyParts)), nil
		},
		compactMessage: func(context.Context, string, string) error { return nil },
	}
	sender := newStreamingReplySender(0, backend, nil)
	defer sender.Close()
	answer := strings.Repeat("a long answer line\n", 1000)
	sender.enqueue(replyCommandAdd, answer)
	sender.Flush()
	final := sender.CompleteTurn([]string{"ou-a"})
	if final == nil || len(legacyParts) == 0 {
		t.Fatalf("missing split-final reference: %+v parts %d", final, len(legacyParts))
	}
	parts := splitMentionFinalAnswer(answer)
	if len(legacyParts) != len(parts)-1 {
		t.Fatalf("sent %d non-final parts, want %d", len(legacyParts), len(parts)-1)
	}
	raw := api.createdData[len(api.createdData)-1]
	requireCardMentions(t, raw, "ou-a")
	decoded := decodeActivityCard(t, raw)
	body, found := findCardElementByID(decoded.Body.Elements, cardKitReplyElementID)
	if !strings.Contains(raw, conversationSectionID) || !found || body.Content != strings.TrimSpace(parts[len(parts)-1]) {
		t.Fatalf("final card lacks last part/feedback: %s", raw)
	}
}

func TestConversationObserverMentionCompletionReceivesNewFinalCard(t *testing.T) {
	var mu sync.Mutex
	var creates []string
	messages := map[string]string{}
	var compactions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token","expire":7200}`))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var body map[string]json.RawMessage
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		switch {
		case r.URL.Path == "/open-apis/cardkit/v1/cards":
			creates = append(creates, string(body["data"]))
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"card_id": fmt.Sprintf("card-%d", len(creates))}})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			var content string
			_ = json.Unmarshal(body["content"], &content)
			id := fmt.Sprintf("message-%d", len(messages)+1)
			messages[id] = content
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": id}})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			var content string
			_ = json.Unmarshal(body["content"], &content)
			compactions = append(compactions, content)
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			_, _ = w.Write([]byte(`{"code":0}`))
		}
	}))
	t.Cleanup(server.Close)
	b, hub, chat := newStreamLifecycleBridge(t)
	b.client = lark.NewClient("test-app", "test-secret", lark.WithOpenBaseUrl(server.URL))
	observer := &streamObserver{done: make(chan ConversationCompletion, 4)}
	b.conversationObserver = observer
	b.mentionTurnInitiator.Store(true)
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), chat.ChatID, WorkspaceKindPrivate); err != nil {
		t.Fatal(err)
	}
	b.recordTurnSenders(chat.RootMsgID, "turn-1", compactTurnInputRefs("ou-a", "input-1"))
	b.startThreadStream(chat, "")
	stream := waitLifecycleSignal(t, hub.started)
	for i, body := range []agentstream.Body{agentstream.TurnStarted{InputSummary: "question"}, agentstream.AssistantMessage{Text: "intermediate"}, agentstream.ResultObserved{Text: "complete answer"}, agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}}} {
		channel := "agent"
		if i == 0 {
			channel = "lark"
		}
		raw, err := agentstream.MarshalEvent(agentstream.Event{SessionID: "session-id-1", Cursor: agentstream.Cursor{Epoch: "epoch-1", Seq: uint64(i + 1)}, Turn: &agentstream.TurnRef{ID: "turn-1", Seq: 1}, At: time.Now(), Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: channel, Provider: "codex", LaunchID: "launch-1"}, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		stream.events <- &pb.AttachEvent{CanonicalJson: raw}
	}
	completed := waitLifecycleSignal(t, observer.done)
	mu.Lock()
	defer mu.Unlock()
	if len(creates) != 2 || len(messages) != 2 || len(compactions) != 1 {
		t.Fatalf("creates %d messages %v compactions %d", len(creates), messages, len(compactions))
	}
	if completed.Card == nil || completed.Card.CardID != "card-2" || completed.Card.MessageID != "message-2" || completed.Card.Sequence != 0 || completed.Card.BridgeID != b.currentBridgeId() {
		t.Fatalf("observer received old/invalid final: %+v", completed.Card)
	}
	if completed.Answer != "complete answer" || !reflect.DeepEqual(completed.Card.MentionOpenIDs, []string{"ou-a"}) {
		t.Fatalf("completion mutated: %+v", completed)
	}
	var final string
	_ = json.Unmarshal([]byte(creates[1]), &final)
	requireCardMentions(t, final, "ou-a")
	replyCompletionCardText(t, compactions[0], i18n.Chinese)
	select {
	case extra := <-observer.done:
		t.Fatalf("duplicate completion %+v", extra)
	default:
	}
}
