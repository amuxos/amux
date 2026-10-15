package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestQuestionCallbackValidationAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, actor, turn, message string
		form                       map[string]interface{}
		cancel, valid              bool
		want                       string
	}{
		{name: "choice", actor: "asker", turn: "turn", message: "", form: map[string]interface{}{"option_id": "A"}, valid: true, want: "A"},
		{name: "custom overrides selection", actor: "asker", turn: "turn", message: "", form: map[string]interface{}{"option_id": "A", "answer": "custom"}, valid: true, want: "custom"},
		{name: "other actor", actor: "other", turn: "turn", message: "", form: map[string]interface{}{"option_id": "A"}},
		{name: "old turn", actor: "asker", turn: "old", message: "", form: map[string]interface{}{"option_id": "A"}},
		{name: "different card", actor: "asker", turn: "turn", message: "another", form: map[string]interface{}{"option_id": "A"}},
		{name: "invented option", actor: "asker", turn: "turn", message: "", form: map[string]interface{}{"option_id": "invented"}},
		{name: "empty", actor: "asker", turn: "turn", message: ""},
		{name: "too many", actor: "asker", turn: "turn", message: "", form: map[string]interface{}{"option_ids": []string{"A", "B"}}},
		{name: "cancel", actor: "asker", turn: "turn", message: "", cancel: true, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := &bridgeTestHub{}
			b := &Bridge{hub: hub}
			payload := permissionPayload{Tool: permissionTool{Question: "choose", AllowOther: true}, Options: []permissionOption{{OptionId: "A"}, {OptionId: "B"}}}
			key, _ := b.beginPermissionRequest(&pendingPermission{PermissionId: "q", TargetID: "target", SessionName: "session", OpenId: "asker", TurnID: "turn", Question: &payload, ChatID: "chat"})
			action := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: tc.actor}, Context: &callback.Context{OpenMessageID: tc.message, OpenChatID: "chat"}, Action: &callback.CallBackAction{Value: map[string]interface{}{"permission_id": "q", "target_id": "target", "session_name": "session", "turn_id": tc.turn}, FormValue: tc.form}}}
			if tc.cancel {
				action.Event.Action.Value["cancel"] = "true"
			}
			_ = key
			resp, err := b.handleCardAction(context.Background(), action)
			if err != nil {
				t.Fatal(err)
			}
			if (resp.Toast.Type == "success") != tc.valid {
				t.Fatalf("response=%+v", resp)
			}
			hub.mu.Lock()
			routes := append([]routeCall(nil), hub.routes...)
			hub.mu.Unlock()
			if !tc.valid {
				if len(routes) != 0 {
					t.Fatal("invalid callback reached runtime")
				}
				return
			}
			if len(routes) != 1 {
				t.Fatalf("routes=%v", routes)
			}
			req := routes[0].payload.(*pb.RespondPermissionRequest)
			if req.TurnId != "turn" || req.Cancel != tc.cancel || req.OptionId != tc.want {
				t.Fatalf("request=%+v", req)
			}
			resp, _ = b.handleCardAction(context.Background(), action)
			if resp.Toast.Type == "success" {
				t.Fatal("duplicate accepted")
			}
		})
	}
}

func TestQuestionAnswerValidation(t *testing.T) {
	p := permissionPayload{Tool: permissionTool{Question: "choose", MultiSelect: true}, Options: []permissionOption{{OptionId: "A"}, {OptionId: "B"}}}
	got, ok := questionAnswers(p, &callback.CallBackAction{FormValue: map[string]interface{}{"option_ids": []string{"B", "A", "A"}}})
	if !ok || strings.Join(got, ",") != "B,A" {
		t.Fatalf("answers=%v valid=%v", got, ok)
	}
	if _, ok := questionAnswers(p, &callback.CallBackAction{FormValue: map[string]interface{}{"answer": "custom"}}); ok {
		t.Fatal("custom answer accepted without support")
	}
}

func TestQuestionCardPreservesMeaningAndLanguages(t *testing.T) {
	p := permissionPayload{TurnID: "turn", Tool: permissionTool{Question: "Choose targets", AllowOther: true, MultiSelect: true}, Options: []permissionOption{{OptionId: "api", Name: "API", Description: "Public endpoint"}}}
	var doc cardDoc
	raw := renderQuestionCard("target", "session", "q", p)
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Public endpoint", "en_us", "zh_cn", `"turn_id":"turn"`, `"name":"answer"`, `"cancel":"true"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %q in %s", want, raw)
		}
	}
	if doc.Body.Elements[1].Elements[0].Required {
		t.Fatal("selection is required despite custom answer")
	}
	if doc.Body.Elements[2].Tag == "form" {
		t.Fatal("cancel must bypass form validation")
	}
	p.Options[0].OptionId = strings.Repeat("x", 101)
	if questionCardFits(p) {
		t.Fatal("oversized option accepted")
	}
	raw = renderQuestionCard("target", "session", "q", p)
	if strings.Contains(raw, `"tag":"form"`) {
		t.Fatal("lossy question controls rendered")
	}
}

func TestQuestionExpiryCancelsWithoutAnswer(t *testing.T) {
	hub := &bridgeTestHub{}
	b := &Bridge{hub: hub}
	p := permissionPayload{Tool: permissionTool{Question: "choose"}}
	key, _ := b.beginPermissionRequest(&pendingPermission{PermissionId: "q", TargetID: "t", SessionName: "s", TurnID: "turn", Question: &p})
	b.armQuestionExpiry(key)
	b.expireQuestion(key)
	if pending, _ := b.getPermission("q", "t", "s", "turn"); pending != nil {
		t.Fatal("expired question retained")
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.routes) != 1 {
		t.Fatalf("routes=%v", hub.routes)
	}
	req := hub.routes[0].payload.(*pb.RespondPermissionRequest)
	if !req.Cancel || req.TurnId != "turn" || req.OptionId != "" || len(req.OptionIds) != 0 {
		t.Fatalf("request=%+v", req)
	}
}
