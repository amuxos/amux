package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestParseSkillCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text     string
		wantName string
		wantTask string
		wantOK   bool
	}{
		{text: "/skill", wantOK: true},
		{text: "/skill code-review", wantName: "code-review", wantOK: true},
		{text: "/skill code-review fix login", wantName: "code-review", wantTask: "fix login", wantOK: true},
		{text: "/status", wantOK: false},
		{text: "skill code-review", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			name, task, ok := parseSkillCommand(tt.text)
			if name != tt.wantName || task != tt.wantTask || ok != tt.wantOK {
				t.Fatalf("parseSkillCommand(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.text, name, task, ok, tt.wantName, tt.wantTask, tt.wantOK)
			}
		})
	}
}

func TestRenderSkillPickerCardPaginatesSkills(t *testing.T) {
	t.Parallel()
	skills := make([]advertisedAgentCommand, 0, 51)
	for i := 0; i < 51; i++ {
		skills = append(skills, advertisedAgentCommand{Name: fmt.Sprintf("skill-%03d", i), Description: "Useful skill"})
	}
	card := renderSkillPickerCard(skillPickerTarget{SessionName: "s1"}, skills, 1)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(card), &decoded); err != nil {
		t.Fatalf("renderSkillPickerCard returned invalid JSON: %v", err)
	}
	for _, want := range []string{"skill-050", "第 2/2 页", "上一页"} {
		if !strings.Contains(card, want) {
			t.Fatalf("second page card missing %q: %s", want, card)
		}
	}
	if strings.Contains(card, "skill-049") {
		t.Fatalf("second page card leaked a first-page skill: %s", card)
	}
}

func TestRouteFreshTopicSkillExplainsRequiredBinding(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	var notice string
	b.threadNotice = func(_ context.Context, _ string, text string) error {
		notice = text
		return nil
	}

	if err := b.routeToSessionInChatWithMention(context.Background(), b.workspaceChatID(), "root-fresh", "msg-fresh", "open-1", "/skill", b.textBotMentionInfo("/skill")); err != nil {
		t.Fatalf("routeToSession: %v", err)
	}
	for _, want := range []string{"尚未绑定 Session", "/run codex", "/bind", "/skill"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice = %q, missing %q", notice, want)
		}
	}
	if len(hub.routes) != 0 {
		t.Fatalf("fresh /skill unexpectedly routed to runtime: %+v", hub.routes)
	}
}

func TestHandleSkillPickerCardActionRevalidatesAndSubmits(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	insertSkillChat(t, b, "codex")
	b.observeAvailableCommands("sb-1", "s1", json.RawMessage(`[{"name":"code-review","kind":"skill"}]`))
	event := skillPickerCallback("code-review", "fix login")

	response, handled, err := b.handleSkillPickerCardAction(context.Background(), event)
	if err != nil || !handled {
		t.Fatalf("handleSkillPickerCardAction = handled %v err %v", handled, err)
	}
	if response == nil || response.Toast == nil || response.Toast.Type != "success" || response.Card == nil {
		t.Fatalf("response = %+v, want success replacement card", response)
	}
	if response.Card.Type != "raw" {
		t.Fatalf("replacement card type = %q, want callback raw", response.Card.Type)
	}
	if len(hub.routes) != 1 {
		t.Fatalf("routes = %+v, want one Submit", hub.routes)
	}
	req := hub.routes[0].payload.(*pb.SubmitRequest)
	if req.Text != "$code-review fix login" || req.Preempt {
		t.Fatalf("submit = %+v", req)
	}
	assertPersistedTurnLinkMessage(t, b, "turn-test", "picker-message-1")

	response, handled, err = b.handleSkillPickerCardAction(context.Background(), event)
	if err != nil || !handled || response == nil || response.Toast == nil || response.Toast.Type != "warning" {
		t.Fatalf("duplicate action response = handled %v response %+v err %v", handled, response, err)
	}
	if len(hub.routes) != 1 {
		t.Fatalf("duplicate action unexpectedly submitted: %+v", hub.routes)
	}

	b.observeAvailableCommands("sb-1", "s1", json.RawMessage(`[]`))
	response, handled, err = b.handleSkillPickerCardAction(context.Background(), event)
	if err != nil || !handled || response == nil || response.Toast == nil || response.Toast.Type != "warning" {
		t.Fatalf("stale action response = handled %v response %+v err %v", handled, response, err)
	}
	if len(hub.routes) != 1 {
		t.Fatalf("stale action unexpectedly submitted: %+v", hub.routes)
	}
}

func assertPersistedTurnLinkMessage(t *testing.T, b *Bridge, turnID, wantMessageID string) {
	t.Helper()
	var messageID string
	if err := b.db.db.QueryRow(`SELECT message_id FROM feishu_turn_links WHERE turn_id = ? AND message_id = ?`, turnID, wantMessageID).Scan(&messageID); err != nil {
		t.Fatalf("lookup persisted turn link: %v", err)
	}
	if messageID != wantMessageID {
		t.Fatalf("persisted message_id = %q, want %q", messageID, wantMessageID)
	}
}

func insertSkillChat(t *testing.T, b *Bridge, agentType string) {
	t.Helper()
	if err := b.db.UpsertChat(LarkChat{
		BridgeId: "feishu:cli_test", ChatID: "oc-workspace", RootMsgID: "root-1", ThreadID: "thread-1",
		OpenId: "open-1", TargetID: "sb-1", TargetName: "target", SessionName: "s1", AgentType: agentType,
	}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
}

func skillPickerCallback(name, task string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "open-1"},
		Context:  &callback.Context{OpenMessageID: "picker-message-1"},
		Action: &callback.CallBackAction{
			Value: map[string]interface{}{
				skillPickerActionKey: skillPickerActionRun,
				"surface":            skillPickerSurfaceThread,
				"target_id":          "sb-1",
				"session_name":       "s1",
				"root_msg_id":        "root-1",
				"chat_id":            "oc-workspace",
				"page":               "0",
			},
			FormValue: map[string]interface{}{"skill_name": name, "skill_task": task},
		},
	}}
}
