package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type recordingObserver struct {
	inputs      []ConversationSubmission
	completions []ConversationCompletion
	actions     int
}

type conversationTaskRecorder struct{ submits, reads int }

func (r *conversationTaskRecorder) SubtaskIDsByParent(string) []string { return nil }
func (r *conversationTaskRecorder) GetSubtaskJSON(context.Context, string) ([]byte, error) {
	panic("unscoped task read")
}
func (r *conversationTaskRecorder) SubmitConversationTask(_ context.Context, target, session string, _ []byte) (string, error) {
	if target != "t1" || session != "s1" {
		panic("wrong conversation")
	}
	r.submits++
	return "task", nil
}
func (r *conversationTaskRecorder) ReadConversationTask(_ context.Context, target, session, id string) ([]byte, error) {
	if target != "t1" || session != "s1" || id != "task" {
		panic("wrong task conversation")
	}
	r.reads++
	return []byte(`{"status":"succeeded"}`), nil
}

func TestConversationTasksValidateDurableBinding(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root", ChatID: "chat", OpenId: "user", TargetID: "t1", SessionName: "s1"}
	if err := b.db.UpsertChat(*chat); err != nil {
		t.Fatal(err)
	}
	recorder := &conversationTaskRecorder{}
	b.subtasks = recorder
	var services ConversationServices
	b.conversationTaskServices(&services)
	binding := b.conversationBinding(chat)
	if _, err := services.SubmitConversationTask(context.Background(), binding, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := services.ReadConversationTask(context.Background(), binding, "task"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ConversationBinding){
		func(v *ConversationBinding) { v.BridgeID = "other" },
		func(v *ConversationBinding) { v.RootID = "missing" },
		func(v *ConversationBinding) { v.TargetID = "other" },
		func(v *ConversationBinding) { v.Session = "other" },
		func(v *ConversationBinding) { v.Initiator = "other" },
		func(v *ConversationBinding) { v.ChatID = "other" },
	} {
		bad := binding
		mutate(&bad)
		if _, err := services.SubmitConversationTask(context.Background(), bad, nil); err == nil {
			t.Fatal("submitted invalid binding")
		}
		if _, err := services.ReadConversationTask(context.Background(), bad, "task"); err == nil {
			t.Fatal("read with invalid binding")
		}
	}
	chat.SessionName = "replacement"
	if err := b.db.UpsertChat(*chat); err != nil {
		t.Fatal(err)
	}
	if _, err := services.SubmitConversationTask(context.Background(), binding, nil); err == nil {
		t.Fatal("submitted stale binding")
	}
	if recorder.submits != 1 || recorder.reads != 1 {
		t.Fatalf("invalid binding reached task runner: %+v", recorder)
	}
}

type streamObserver struct {
	recordingObserver
	done chan ConversationCompletion
}

func (o *streamObserver) Completed(_ context.Context, e ConversationCompletion) { o.done <- e }

func TestConversationObserverCanonicalCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"message","card_id":"card"}}`))
	}))
	t.Cleanup(server.Close)
	b, hub, chat := newStreamLifecycleBridge(t)
	b.client = lark.NewClient("test-app", "test-secret", lark.WithOpenBaseUrl(server.URL))
	o := &streamObserver{done: make(chan ConversationCompletion, 4)}
	b.conversationObserver = o
	b.startThreadStream(chat, "")
	stream := waitLifecycleSignal(t, hub.started)
	for i, body := range []agentstream.Body{
		agentstream.TurnStarted{InputSummary: "question"},
		agentstream.AssistantMessage{Text: "intermediate"},
		agentstream.ResultObserved{Text: "canonical answer"},
		agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}},
	} {
		channel := "agent"
		if i == 0 {
			channel = "lark"
		}
		raw, err := agentstream.MarshalEvent(agentstream.Event{
			SessionID: "session-id-1", Cursor: agentstream.Cursor{Epoch: "epoch-1", Seq: uint64(i + 1)},
			Turn: &agentstream.TurnRef{ID: "turn-1", Seq: 1}, At: time.Now(),
			Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: channel, Provider: "codex", LaunchID: "launch-1"}, Body: body,
		})
		if err != nil {
			t.Fatal(err)
		}
		stream.events <- &pb.AttachEvent{CanonicalJson: raw}
	}
	completion := waitLifecycleSignal(t, o.done)
	if completion.TurnID != "turn-1" || completion.Answer != "canonical answer" || completion.Status != "done" || completion.Automation {
		t.Fatalf("canonical completion: %+v", completion)
	}
	if completion.Card == nil || completion.Card.CardID != "card" || completion.Card.MessageID != "message" || completion.Card.Sequence < 1 {
		t.Fatalf("completed reply reference missing: %+v", completion.Card)
	}
}

func TestConversationSectionPreservesReply(t *testing.T) {
	t.Parallel()
	if len(conversationSectionID) > 20 {
		t.Fatal("CardKit element IDs have a 20-character limit")
	}
	before := renderCardKitAnswerElements("original answer", false)
	after := renderCardKitAnswerElements("original answer", true)
	var preserved []cardElement
	sections := 0
	for i, element := range after {
		if element.ElementID == conversationSectionID {
			sections++
			if i == 0 || i+1 >= len(after) || after[i-1].ElementID != amuxBrandDividerID || after[i+1].ElementID != amuxBrandFooterID {
				t.Fatal("want divider, controls, brand footer")
			}
			// The placeholder must use the same structure as later updates.
			raw, err := json.Marshal(element)
			if err != nil {
				t.Fatal(err)
			}
			var created, updated any
			if err := json.Unmarshal(raw, &created); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(conversationSection(nil), &updated); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(created, updated) {
				t.Fatal("initial section differs from the section update structure")
			}
			continue
		}
		preserved = append(preserved, element)
	}
	if sections != 1 || !reflect.DeepEqual(preserved, before) {
		t.Fatal("extension changed existing reply content")
	}
	if after[len(after)-1].ElementID != amuxBrandFooterID {
		t.Fatal("brand footer must remain the final visible element")
	}
}

func (o *recordingObserver) Submitted(_ context.Context, e ConversationSubmission) {
	o.inputs = append(o.inputs, e)
}
func (o *recordingObserver) Completed(_ context.Context, e ConversationCompletion) {
	o.completions = append(o.completions, e)
}
func (o *recordingObserver) CardAction(_ context.Context, e *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool, error) {
	if e.Event.Action.Value["example"] != "action" {
		return nil, false, nil
	}
	o.actions++
	return &callback.CardActionTriggerResponse{}, true, nil
}
func (o *recordingObserver) Close() error { return nil }

func TestConversationObserverAcceptedSubmission(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	o := &recordingObserver{}
	b.conversationObserver = o
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root", ChatID: "chat", OpenId: "user", TargetID: "t1", SessionName: "s1", AgentProfile: "example"}
	if err := b.db.UpsertChat(*chat); err != nil {
		t.Fatal(err)
	}
	receipt, err := b.submitToSessionForMessages(context.Background(), chat, "hello", compactTurnInputRefs("user", "message"))
	if err != nil {
		t.Fatal(err)
	}
	if len(o.inputs) != 1 || o.inputs[0].TurnID != receipt.TurnID || o.inputs[0].Text != "hello" || o.inputs[0].HumanIDs[0] != "user" || o.inputs[0].Binding.Initiator != "user" {
		t.Fatalf("input snapshot: %+v", o.inputs)
	}
	// The no-extension public composition remains a no-op.
	b.conversationObserver = nil
	b.observeSubmission(context.Background(), chat, "ignored", "hello", nil)
	if len(o.inputs) != 1 {
		t.Fatal("nil extension received event")
	}
}

func TestConversationObserverCallbackNamespaceAndCompletion(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	o := &recordingObserver{}
	b.conversationObserver = o
	var event callback.CardActionTriggerEvent
	if err := json.Unmarshal([]byte(`{"event":{"action":{"value":{"example":"action"}}}}`), &event); err != nil {
		t.Fatal(err)
	}
	if _, err := b.handleCardAction(context.Background(), &event); err != nil {
		t.Fatal(err)
	}
	if o.actions != 1 {
		t.Fatal("extension callback not routed")
	}
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root", TargetID: "target", SessionName: "session", goalGeneration: "goal"}
	b.observeCompletion(context.Background(), chat, "turn", "completed", "", "answer", true)
	if len(o.completions) != 1 || !o.completions[0].Automation || !o.completions[0].Superseded {
		t.Fatalf("completion provenance: %+v", o.completions)
	}
}
