package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/amuxos/amux/internal/agentui"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestRenderPermissionRequestCardUserInput(t *testing.T) {
	payload := permissionPayload{
		Tool: permissionTool{
			Title:    "更新类型",
			Question: "你说的「更新 agent profile」具体是想做哪件事？",
		},
		Options: []permissionOption{
			{OptionId: "改/加 profile 的 skill", Name: "改/加 profile 的 skill"},
			{OptionId: "改 profile 配置本身", Name: "改 profile 配置本身"},
		},
	}
	content := renderPermissionRequestCard("tgt", "sess", "9001:q1", payload)
	if !strings.Contains(content, "Agent 需要你回答") {
		t.Errorf("missing user-input card title; content=%s", content)
	}
	if !strings.Contains(content, "更新 agent profile") {
		t.Errorf("missing question prompt; content=%s", content)
	}
	if !strings.Contains(content, `"tag":"form"`) || !strings.Contains(content, `"tag":"select_static"`) {
		t.Errorf("missing single-select form; content=%s", content)
	}
	if !strings.Contains(content, "改/加 profile 的 skill") {
		t.Errorf("missing option label; content=%s", content)
	}
	if !strings.Contains(content, `"permission_id":"9001:q1"`) {
		t.Errorf("missing permission_id in button value; content=%s", content)
	}
}

func TestPermissionSummaryShowsOperationAndImpactScope(t *testing.T) {
	payload := permissionPayload{Tool: permissionTool{
		Title: "Write file", RawInput: json.RawMessage(`{"path":"/repo/main.go","content":"new"}`),
	}}
	summary := permissionSummary(payload)
	if !strings.Contains(summary, "**操作**: Write file") || !strings.Contains(summary, "**影响范围**: /repo/main.go") {
		t.Fatalf("permission summary = %q", summary)
	}
}

func TestPermissionPayloadFromSemanticEvent(t *testing.T) {
	payload := permissionPayloadFromEvent(&agentui.PermissionEvent{
		RequestID: "perm-1",
		Tool: &agentui.PermissionTool{
			Title: "Choose targets", Question: "Which targets?", MultiSelect: true,
			RawInput: json.RawMessage(`{"prompt":"targets"}`),
		},
		Options: []agentui.PermissionOption{{ID: "api", Label: "API", Kind: "answer"}},
	})
	if payload.Tool.Question != "Which targets?" || !payload.Tool.MultiSelect || payload.Options[0].OptionId != "api" {
		t.Fatalf("permission payload = %+v, want semantic question fields", payload)
	}
}

func TestRenderPermissionRequestCardUserInputMultiSelectForm(t *testing.T) {
	payload := permissionPayload{
		Tool: permissionTool{
			Title:       "多选",
			Question:    "选哪些？",
			MultiSelect: true,
		},
		Options: []permissionOption{
			{OptionId: "A", Name: "A"},
			{OptionId: "B", Name: "B"},
		},
	}
	content := renderPermissionRequestCard("tgt", "sess", "9001:q1", payload)
	if !strings.Contains(content, `"tag":"multi_select_static"`) || !strings.Contains(content, `"action_type":"form_submit"`) {
		t.Errorf("missing multi-select submit form; content=%s", content)
	}
}

func TestHandleCardActionRespondsWithMultipleOptions(t *testing.T) {
	hub := &bridgeTestHub{}
	b := &Bridge{hub: hub}
	key, ok := b.beginPermissionRequest(&pendingPermission{
		PermissionId: "perm-multi", TargetID: "target-1", SessionName: "session-1", OpenId: "owner-open",
	})
	if !ok || key == "" {
		t.Fatal("beginPermissionRequest() failed")
	}

	resp, err := b.handleCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Action: &callback.CallBackAction{
				Value: map[string]interface{}{
					"permission_id": "perm-multi",
					"target_id":     "target-1",
					"session_name":  "session-1",
				},
				FormValue: map[string]interface{}{"option_ids": []interface{}{"A", "B"}},
			},
		},
	})
	if err != nil || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("handleCardAction() = %#v, %v, want success", resp, err)
	}
	if resp.Card == nil || resp.Card.Type != "raw" {
		t.Fatalf("callback card = %#v, want immediate resolved card", resp.Card)
	}
	cardJSON, _ := json.Marshal(resp.Card.Data)
	if bytes.Contains(cardJSON, []byte(`"tag":"form"`)) || !bytes.Contains(cardJSON, []byte("A、B")) {
		t.Fatalf("resolved callback card = %s", cardJSON)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.routes) != 1 {
		t.Fatalf("routes = %+v, want one response", hub.routes)
	}
	req, ok := hub.routes[0].payload.(*pb.RespondPermissionRequest)
	if !ok || strings.Join(req.OptionIds, ",") != "A,B" {
		t.Fatalf("permission response = %+v, want A,B", hub.routes[0].payload)
	}
}

func TestPermissionAndActivityCardsStayUnderFeishuLimit(t *testing.T) {
	payload := permissionPayload{
		Tool: permissionTool{Title: strings.Repeat("tool", 1000), RawInput: json.RawMessage(`{"value":"` + strings.Repeat("x", 100000) + `"}`)},
	}
	for i := 0; i < 500; i++ {
		payload.Options = append(payload.Options, permissionOption{OptionId: fmt.Sprintf("option-%d", i), Name: strings.Repeat("label", 100)})
	}
	permissionCard := renderPermissionRequestCard("target", "session", "permission", payload)
	if len(permissionCard) > maxFeishuCardBytes {
		t.Fatalf("permission card bytes = %d, limit %d", len(permissionCard), maxFeishuCardBytes)
	}
	if !strings.Contains(permissionCard, `"tag":"action"`) {
		t.Fatalf("permission card lost actions: %s", permissionCard)
	}

	entries := make([]agentui.PlanEntry, 500)
	for i := range entries {
		entries[i] = agentui.PlanEntry{Content: strings.Repeat("plan", 1000), Status: "pending"}
	}
	activityCard := renderToolActivityCard(toolActivityCardMeta{
		Status: "running", ViewURL: "https://viewer.example/session",
		Activity: &agentui.ActivityView{Status: "running", CurrentStep: strings.Repeat("step", 10000), Plan: entries},
	}, "fingerprint")
	if len(activityCard) > maxFeishuCardBytes {
		t.Fatalf("activity card bytes = %d, limit %d", len(activityCard), maxFeishuCardBytes)
	}
	if !strings.Contains(activityCard, "查看完整输出") {
		t.Fatalf("activity fallback lost viewer action: %s", activityCard)
	}
}

func TestPermissionCardBudgetHandlesLongWireOptionIDs(t *testing.T) {
	payload := permissionPayload{Tool: permissionTool{Title: "Choose"}}
	for i := 0; i < maxPermissionOptions; i++ {
		payload.Options = append(payload.Options, permissionOption{
			OptionId: fmt.Sprintf("option-%d-%s", i, strings.Repeat("x", 2000)),
			Name:     fmt.Sprintf("Option %d", i),
		})
	}
	card := renderPermissionRequestCard("target", "session", "permission", payload)
	if len(card) > maxFeishuCardBytes {
		t.Fatalf("permission card bytes = %d, limit %d", len(card), maxFeishuCardBytes)
	}
	if !strings.Contains(card, `"tag":"action"`) || !strings.Contains(card, "Option 0") {
		t.Fatalf("budgeted permission card lost all usable controls: %s", card)
	}
}

func TestPermissionClaimAllowsOnlyOneResponder(t *testing.T) {
	b := &Bridge{}
	key, ok := b.beginPermissionRequest(&pendingPermission{PermissionId: "perm-1", TargetID: "target-1", SessionName: "session-1"})
	if !ok {
		t.Fatal("beginPermissionRequest failed")
	}
	first := b.claimPermission(key)
	second := b.claimPermission(key)
	if first == nil || second != nil {
		t.Fatalf("claims = %+v then %+v, want exactly one claimant", first, second)
	}
	b.releasePermissionClaim(key)
	if retry := b.claimPermission(key); retry == nil {
		t.Fatal("claim was not released for retry")
	}
}
