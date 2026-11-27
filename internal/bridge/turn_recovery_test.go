package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type turnRecoveryRouteCall struct {
	targetID string
	method   string
	payload  any
}

type turnRecoveryTestHub struct {
	mu      sync.Mutex
	targets []*pb.Target
	calls   []turnRecoveryRouteCall
	onRoute func(targetID, method string, payload any) (*pb.TunnelMsg, error)
}

func (h *turnRecoveryTestHub) ListTargets() []*pb.Target {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.targets
}

func (h *turnRecoveryTestHub) Route(_ context.Context, targetID, method string, payload any) (*pb.TunnelMsg, error) {
	h.mu.Lock()
	h.calls = append(h.calls, turnRecoveryRouteCall{targetID: targetID, method: method, payload: payload})
	fn := h.onRoute
	h.mu.Unlock()
	if fn != nil {
		return fn(targetID, method, payload)
	}
	switch method {
	case "Restart":
		req, _ := payload.(*pb.RestartRequest)
		model := "model-b"
		if req != nil && req.Replacement != nil {
			for i := 0; i+1 < len(req.Replacement.ExtraArgs); i++ {
				if req.Replacement.ExtraArgs[i] == "--model" {
					model = req.Replacement.ExtraArgs[i+1]
				}
			}
		}
		raw, _ := json.Marshal(&pb.RunResponse{
			Session: &pb.Session{
				Name:           "work-sess",
				State:          "running",
				AgentType:      "codex",
				EffectiveModel: model,
			},
		})
		return &pb.TunnelMsg{Payload: raw}, nil
	case "Submit":
		raw, _ := json.Marshal(&pb.SubmitResponse{
			SessionId: "sid-1",
			LaunchId:  "launch-1",
			TurnId:    "turn-retry-2",
		})
		return &pb.TunnelMsg{Payload: raw}, nil
	default:
		return &pb.TunnelMsg{}, nil
	}
}

func (h *turnRecoveryTestHub) RouteStream(_ context.Context, _, _ string, _ any, _ func(*pb.TunnelMsg) error) error {
	return nil
}

func (h *turnRecoveryTestHub) SetSessionEventHandler(func(string, *pb.Session, bool)) {}

func (h *turnRecoveryTestHub) snapshotCalls() []turnRecoveryRouteCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]turnRecoveryRouteCall(nil), h.calls...)
}

func TestTurnRecoveryQueuedThreshold60s(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	base := toolActivityCardMeta{
		AgentType:      "codex",
		EffectiveModel: "gpt-5",
		Status:         "running",
		Activity: &agentui.ActivityView{
			StartedAt:      started,
			LastActivityAt: started,
			RetryCount:     1,
			RetryCode:      "rate_limit_retry",
		},
	}

	below := base
	below.Now = started.Add(59 * time.Second)
	if shouldOfferQueuedModelRecovery(below, 60*time.Second) {
		t.Fatal("queued recovery should not appear before 60s")
	}

	at60 := base
	at60.Now = started.Add(60 * time.Second)
	if !shouldOfferQueuedModelRecovery(at60, 60*time.Second) {
		t.Fatal("queued recovery should appear at 60s during upstream retry")
	}

	// Custom shorter threshold (e.g. owner set 15s in experiments card).
	at15 := base
	at15.Now = started.Add(15 * time.Second)
	if !shouldOfferQueuedModelRecovery(at15, 15*time.Second) {
		t.Fatal("queued recovery should respect custom 15s threshold")
	}

	// Threshold 0 disables queued recovery controls.
	if shouldOfferQueuedModelRecovery(at60, 0) {
		t.Fatal("queued recovery must be disabled when threshold is 0")
	}

	withOutput := at60
	viewWithStep := *at60.Activity
	viewWithStep.CurrentStep = "Reading files"
	withOutput.Activity = &viewWithStep
	if shouldOfferQueuedModelRecovery(withOutput, 60*time.Second) {
		t.Fatal("queued recovery must not appear once visible output exists")
	}

	subtask := at60
	subtask.SubtaskReport = true
	if shouldOfferQueuedModelRecovery(subtask, 60*time.Second) {
		t.Fatal("queued recovery must not appear on subtask report turns")
	}

	// Bridge live atomic threshold defaults to 60s and honors live updates including 0.
	b := &Bridge{}
	if got := b.modelRecoveryQueueWaitThreshold(); got != 60*time.Second {
		t.Fatalf("default threshold = %v, want 60s", got)
	}
	b.setModelRecoveryQueueWaitSeconds(20)
	if got := b.modelRecoveryQueueWaitThreshold(); got != 20*time.Second {
		t.Fatalf("updated threshold = %v, want 20s", got)
	}
	b.setModelRecoveryQueueWaitSeconds(0)
	if got := b.modelRecoveryQueueWaitThreshold(); got != 0 {
		t.Fatalf("disabled threshold = %v, want 0", got)
	}
}

func TestTurnRecoveryCardBilingualAndInPlaceElement(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 24, 10, 1, 30, 0, time.UTC)
	for _, reason := range []string{modelRecoveryReasonRateLimit, modelRecoveryReasonQueued} {
		t.Run(reason, func(t *testing.T) {
			status := "failed"
			if reason == modelRecoveryReasonQueued {
				status = "running"
			}
			prompt := &modelRecoveryCardPrompt{
				TurnID:      "turn-1",
				TargetID:    "local",
				SessionName: "work-sess",
				RootMsgID:   "om_root",
				Reason:      reason,
				Candidates: []FallbackModelCandidate{
					{Model: "model-a", Label: "Model A", Hint: "25%"},
					{Model: "model-b", Label: "Model B"},
				},
				MoreCandidates: []FallbackModelCandidate{
					{Model: "model-c", Label: "Model C", Hint: "40%"},
					{Model: "model-d", Label: "Model D"},
				},
			}
			meta := toolActivityCardMeta{
				Status:           status,
				AgentType:        "codex",
				EffectiveModel:   "model-old",
				Placeholder:      true,
				PlaceholderError: "Rate limited",
				ModelRecovery:    prompt,
				Now:              now,
				Activity: &agentui.ActivityView{
					Status:         status,
					StartedAt:      now.Add(-90 * time.Second),
					LastActivityAt: now.Add(-90 * time.Second),
					RetryCount:     2,
					RetryCode:      "rate_limit_retry",
				},
			}
			raw := renderToolActivityCard(meta, "")
			en := assertSessionCardLanguages(t, raw)
			if !strings.Contains(en, "Switch to Model A (25%) & retry") || !strings.Contains(en, "Switch to Model B & retry") {
				t.Fatalf("missing English switch buttons in card: %s", en)
			}
			// Verify the recovery buttons live inside cardKitActivityContentID
			// so CardKit ReplaceElement can dynamically attach and remove them.
			var doc cardDoc
			if err := json.Unmarshal([]byte(selectManagementCardLocale(raw, i18n.Chinese)), &doc); err != nil {
				t.Fatal(err)
			}
			contentEl, ok := findCardElementByID(doc.Body.Elements, cardKitActivityContentID)
			if !ok {
				t.Fatal("missing cardKitActivityContentID")
			}
			if _, ok := findCardElementByID(contentEl.Columns[0].Elements, modelRecoveryActionsElementID); !ok {
				t.Fatal("recovery button row must live inside cardKitActivityContentID")
			}
			moreEl, ok := findCardElementByID(contentEl.Columns[0].Elements, modelRecoveryMoreElementID)
			if !ok || moreEl.Tag != "select_static" || len(moreEl.Options) != 2 {
				t.Fatalf("missing other-models select_static inside cardKitActivityContentID: %+v", moreEl)
			}

			// Standalone error recovery card must also be valid bilingual Card 2.0.
			errCard := renderModelRecoveryErrorCard("⚠️ Rate limit reached.", prompt)
			enErr := assertSessionCardLanguages(t, errCard)
			if !strings.Contains(enErr, "Switch to Model A (25%) & retry") {
				t.Fatalf("missing button in standalone recovery error card: %s", enErr)
			}
		})
	}
}

func TestTurnRecoveryClickAuthIdempotencyAndReplay(t *testing.T) {
	t.Parallel()
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	chat := LarkChat{
		BridgeId:       "feishu:test",
		ChatID:         "oc_group",
		RootMsgID:      "om_root",
		OpenId:         "ou_user",
		TargetID:       "local",
		SessionName:    "work-sess",
		AgentProfile:   "builtin/codex",
		AgentType:      "codex",
		EffectiveModel: "model-old",
	}
	if err := db.UpsertChat(chat); err != nil {
		t.Fatal(err)
	}

	hub := &turnRecoveryTestHub{
		targets: []*pb.Target{{
			Id: "local",
			Sessions: []*pb.Session{{
				Name:           "work-sess",
				State:          "running",
				AgentType:      "codex",
				AgentProfile:   "builtin/codex",
				EffectiveModel: "model-old",
				ExtraArgs:      []string{"--model", "model-old"},
			}},
		}},
	}
	var notices []string
	b := &Bridge{
		bridgeID: "feishu:test",
		hub:      hub,
		db:       db,
		secrets:  &Secrets{OpenId: "ou_owner"},
	}
	b.turnRecovery.runAsync = func(fn func()) { fn() }
	b.turnRecovery.sendNotice = func(_ context.Context, _ *LarkChat, text string) {
		notices = append(notices, text)
	}

	// Simulate an active running turn (queued escape) that needs Interrupt before Restart.
	b.setTurnActive(chat.RootMsgID, true)
	hub.onRoute = func(targetID, method string, payload any) (*pb.TunnelMsg, error) {
		if method == "Interrupt" {
			b.setTurnActive(chat.RootMsgID, false)
			return &pb.TunnelMsg{}, nil
		}
		if method == "Restart" {
			raw, _ := json.Marshal(&pb.RunResponse{
				Session: &pb.Session{Name: "work-sess", State: "running", AgentType: "codex", EffectiveModel: "model-b"},
			})
			return &pb.TunnelMsg{Payload: raw}, nil
		}
		raw, _ := json.Marshal(&pb.SubmitResponse{SessionId: "sid-1", LaunchId: "launch-1", TurnId: "turn-2"})
		return &pb.TunnelMsg{Payload: raw}, nil
	}

	parts := []*pb.InputPart{{Value: &pb.InputPart_Text{Text: "fix the bug"}}}
	b.recordTurnRecovery(&chat, "turn-1", "fix the bug", parts, "sys", []turnInputRef{{OpenID: "ou_user", MessageID: "om_1"}}, false)
	b.turnRecovery.mu.Lock()
	b.turnRecovery.entries["turn-1"].Candidates = []FallbackModelCandidate{{Model: "model-b", Label: "Model B"}}
	b.turnRecovery.entries["turn-1"].CandidatesLoaded = true
	b.turnRecovery.mu.Unlock()

	makeEvent := func(operator, model string) *callback.CardActionTriggerEvent {
		return &callback.CardActionTriggerEvent{
			Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: operator},
				Action: &callback.CallBackAction{
					Value: map[string]any{
						modelRecoveryActionKey: modelRecoveryActionSwitch,
						"turn_id":              "turn-1",
						"target_id":            "local",
						"session_name":         "work-sess",
						"root_msg_id":          "om_root",
						"model":                model,
					},
				},
			},
		}
	}

	// 1. Unauthorized operator is rejected.
	resp, handled, err := b.handleModelRecoveryCardAction(context.Background(), makeEvent("ou_other", "model-b"))
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "error" {
		t.Fatalf("unauthorized response = %#v, handled=%v, err=%v", resp, handled, err)
	}
	if len(hub.snapshotCalls()) != 0 {
		t.Fatalf("unauthorized click should not call hub, got %+v", hub.snapshotCalls())
	}

	// 2. Tampered model not in offered candidates is rejected.
	resp, handled, err = b.handleModelRecoveryCardAction(context.Background(), makeEvent("ou_user", "evil-model"))
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("invalid model response = %#v, handled=%v, err=%v", resp, handled, err)
	}

	// 3. Initiator click interrupts, switches model with PreserveHistory, and replays original turn.
	resp, handled, err = b.handleModelRecoveryCardAction(context.Background(), makeEvent("ou_user", "model-b"))
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "info" {
		t.Fatalf("valid click response = %#v, handled=%v, err=%v", resp, handled, err)
	}
	calls := hub.snapshotCalls()
	if len(calls) != 3 || calls[0].method != "Interrupt" || calls[1].method != "Restart" || calls[2].method != "Submit" {
		t.Fatalf("hub calls = %+v, want [Interrupt, Restart, Submit]", calls)
	}
	restartReq, ok := calls[1].payload.(*pb.RestartRequest)
	if !ok || !restartReq.PreserveHistory || restartReq.Replacement == nil {
		t.Fatalf("unexpected restart request: %+v", calls[1].payload)
	}
	submitReq, ok := calls[2].payload.(*pb.SubmitRequest)
	if !ok || submitReq.Text != "fix the bug" || len(submitReq.Parts) != 1 {
		t.Fatalf("unexpected resubmit request: %+v", calls[2].payload)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "model-b") {
		t.Fatalf("notices = %v, want switched_retrying notice", notices)
	}
	updatedChat, err := db.FindByRootMsg("feishu:test", "om_root")
	if err != nil || updatedChat == nil || updatedChat.EffectiveModel != "model-b" {
		t.Fatalf("persisted chat effective model = %+v, err=%v", updatedChat, err)
	}

	// 4. Duplicate click after consumption is rejected (expired/already consumed).
	resp, handled, err = b.handleModelRecoveryCardAction(context.Background(), makeEvent("ou_user", "model-b"))
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("duplicate click response = %#v", resp)
	}
	if len(hub.snapshotCalls()) != 3 {
		t.Fatalf("duplicate click triggered extra hub calls: %+v", hub.snapshotCalls())
	}
}

func TestTurnRecoverySkipsReplayWhenToolsExecuted(t *testing.T) {
	t.Parallel()
	hub := &turnRecoveryTestHub{
		targets: []*pb.Target{{
			Id: "local",
			Sessions: []*pb.Session{{
				Name:           "work-sess",
				State:          "running",
				AgentType:      "codex",
				AgentProfile:   "builtin/codex",
				EffectiveModel: "model-old",
				ExtraArgs:      []string{"--model", "model-old"},
			}},
		}},
	}
	var notices []string
	b := &Bridge{bridgeID: "feishu:test", hub: hub}
	b.turnRecovery.runAsync = func(fn func()) { fn() }
	b.turnRecovery.sendNotice = func(_ context.Context, _ *LarkChat, text string) {
		notices = append(notices, text)
	}

	chat := LarkChat{
		BridgeId: "feishu:test", RootMsgID: "om_root", OpenId: "ou_user",
		TargetID: "local", SessionName: "work-sess", AgentProfile: "builtin/codex",
		AgentType: "codex", EffectiveModel: "model-old",
	}
	b.recordTurnRecovery(&chat, "turn-tools", "edit files", nil, "", []turnInputRef{{OpenID: "ou_user"}}, false)
	b.markTurnRecoveryToolUsed("turn-tools")
	b.markTurnRecoveryRateLimited("turn-tools", &chat, "model-old")
	b.turnRecovery.mu.Lock()
	b.turnRecovery.entries["turn-tools"].Candidates = []FallbackModelCandidate{{Model: "model-b", Label: "Model B"}}
	b.turnRecovery.entries["turn-tools"].CandidatesLoaded = true
	b.turnRecovery.mu.Unlock()

	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Action: &callback.CallBackAction{
				Value: map[string]any{
					modelRecoveryActionKey: modelRecoveryActionSwitch,
					"turn_id":              "turn-tools",
					"target_id":            "local",
					"session_name":         "work-sess",
					"root_msg_id":          "om_root",
					"model":                "model-b",
				},
			},
		},
	}
	resp, handled, err := b.handleModelRecoveryCardAction(context.Background(), event)
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "info" {
		t.Fatalf("response = %#v, handled=%v, err=%v", resp, handled, err)
	}
	calls := hub.snapshotCalls()
	if len(calls) != 1 || calls[0].method != "Restart" {
		t.Fatalf("hub calls = %+v, want only Restart (no Submit replay when tools ran)", calls)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "tool calls already ran") {
		t.Fatalf("notices = %v, want English tool side-effect warning", notices)
	}
	if zh := sessionCopy([]i18n.Locale{i18n.Chinese}, "recovery.notice.switched_tools_used", "model-b"); !strings.Contains(zh, "工具操作") {
		t.Fatalf("zh tool warning = %q", zh)
	}
}

func TestTurnRecoveryOmitsOversizedImages(t *testing.T) {
	t.Parallel()
	big := make([]byte, modelRecoveryMaxImageBytes+1)
	parts := []*pb.InputPart{
		{Value: &pb.InputPart_Text{Text: "check image"}},
		{Value: &pb.InputPart_Image{Image: &pb.ImageInput{Data: big, MimeType: "image/png"}}},
	}
	cloned, omitted := cloneRecoveryParts(parts)
	if !omitted || cloned != nil {
		t.Fatalf("expected oversized image parts to be omitted, got omitted=%v len=%d", omitted, len(cloned))
	}
}

func TestTurnRecoveryExtensionHookRankingAndFallback(t *testing.T) {
	orig := bridgeExtensions.snapshot()
	t.Cleanup(func() {
		bridgeExtensions.mu.Lock()
		bridgeExtensions.extension = orig
		bridgeExtensions.mu.Unlock()
	})

	bridgeExtensions.mu.Lock()
	bridgeExtensions.extension.RankFallbackModels = func(_ context.Context, req FallbackModelRequest) ([]FallbackModelCandidate, error) {
		return []FallbackModelCandidate{
			{Model: req.CurrentModel, Label: "Current (must filter)"},
			{Model: "cand-1", Label: "Candidate 1", Hint: "15%"},
			{Model: "cand-2", Label: "Candidate 2", Hint: "40%"},
			{Model: "cand-3", Label: "Candidate 3", Hint: "20%"},
			{Model: "cand-4", Label: "Candidate 4", Hint: "60%"},
		}, nil
	}
	bridgeExtensions.mu.Unlock()

	b := &Bridge{}
	chat := &LarkChat{AgentType: "codex", SessionName: "work-sess", TargetID: "local"}
	got, more := b.resolveTurnRecoveryCandidates(context.Background(), chat, "turn-rank", "model-cur")
	if len(got) != 3 || got[0].Model != "cand-1" || got[0].Hint != "15%" || got[1].Model != "cand-2" || got[2].Model != "cand-3" {
		t.Fatalf("ranked candidates = %+v, want [cand-1(15%%), cand-2(40%%), cand-3(20%%)]", got)
	}
	if len(more) == 0 || more[0].Model != "cand-4" {
		t.Fatalf("more candidates = %+v, want cand-4 first in dropdown", more)
	}

	// Hook error falls back cleanly without panicking or dropping base candidates.
	bridgeExtensions.mu.Lock()
	bridgeExtensions.extension.RankFallbackModels = func(context.Context, FallbackModelRequest) ([]FallbackModelCandidate, error) {
		return nil, errors.New("probe failed")
	}
	bridgeExtensions.mu.Unlock()
	_, _ = b.resolveTurnRecoveryCandidates(context.Background(), chat, "turn-fallback", "model-cur")
}

func TestTurnRecoverySelectOtherModelOption(t *testing.T) {
	t.Parallel()
	hub := &turnRecoveryTestHub{
		targets: []*pb.Target{{
			Id: "local",
			Sessions: []*pb.Session{{
				Name:           "work-sess",
				State:          "running",
				AgentType:      "codex",
				AgentProfile:   "builtin/codex",
				EffectiveModel: "model-old",
				ExtraArgs:      []string{"--model", "model-old"},
			}},
		}},
	}
	b := &Bridge{bridgeID: "feishu:test", hub: hub, secrets: &Secrets{OpenId: "ou_owner"}}
	b.turnRecovery.runAsync = func(fn func()) { fn() }
	b.turnRecovery.sendNotice = func(context.Context, *LarkChat, string) {}

	chat := LarkChat{
		BridgeId: "feishu:test", RootMsgID: "om_root", OpenId: "ou_user",
		TargetID: "local", SessionName: "work-sess", AgentProfile: "builtin/codex",
		AgentType: "codex", EffectiveModel: "model-old",
	}
	b.recordTurnRecovery(&chat, "turn-select", "hello", nil, "", []turnInputRef{{OpenID: "ou_user"}}, false)
	b.markTurnRecoveryRateLimited("turn-select", &chat, "model-old")
	b.turnRecovery.mu.Lock()
	b.turnRecovery.entries["turn-select"].Candidates = []FallbackModelCandidate{{Model: "model-a", Label: "Model A"}}
	b.turnRecovery.entries["turn-select"].MoreCandidates = []FallbackModelCandidate{{Model: "model-other", Label: "Model Other"}}
	b.turnRecovery.entries["turn-select"].CandidatesLoaded = true
	b.turnRecovery.mu.Unlock()

	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Action: &callback.CallBackAction{
				Option: "model-other",
				Value: map[string]any{
					modelRecoveryActionKey: modelRecoveryActionSelect,
					"turn_id":              "turn-select",
					"target_id":            "local",
					"session_name":         "work-sess",
					"root_msg_id":          "om_root",
				},
			},
		},
	}
	resp, handled, err := b.handleModelRecoveryCardAction(context.Background(), event)
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "info" {
		t.Fatalf("select other model response = %#v, handled=%v, err=%v", resp, handled, err)
	}
	calls := hub.snapshotCalls()
	if len(calls) != 2 || calls[0].method != "Restart" || calls[1].method != "Submit" {
		t.Fatalf("hub calls = %+v, want [Restart, Submit]", calls)
	}
}

func TestTurnRecoveryUpdatesLiveStreamEffectiveModelInPlace(t *testing.T) {
	t.Parallel()
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()

	chat := LarkChat{
		BridgeId:       "feishu:test",
		ChatID:         "oc_chat",
		RootMsgID:      "om_root",
		ThreadID:       "omt_thread",
		OpenId:         "ou_user",
		TargetID:       "local",
		SessionName:    "work-sess",
		AgentProfile:   "builtin/codex",
		AgentType:      "codex",
		EffectiveModel: "model-old",
		Status:         ChatStatusActive,
	}
	if err := db.UpsertChat(chat); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	hub := &turnRecoveryTestHub{
		targets: []*pb.Target{{
			Id: "local",
			Sessions: []*pb.Session{{
				Id:             "sid-1",
				LaunchId:       "launch-1",
				Name:           "work-sess",
				State:          "running",
				AgentType:      "codex",
				AgentProfile:   "builtin/codex",
				EffectiveModel: "model-old",
				ExtraArgs:      []string{"--model", "model-old"},
			}},
		}},
	}
	hub.onRoute = func(targetID, method string, payload any) (*pb.TunnelMsg, error) {
		if method == "Restart" {
			raw, _ := json.Marshal(&pb.RunResponse{
				Session: &pb.Session{
					Id:             "sid-1",
					LaunchId:       "launch-1",
					Name:           "work-sess",
					State:          "running",
					AgentType:      "codex",
					AgentProfile:   "builtin/codex",
					EffectiveModel: "model-new",
				},
			})
			return &pb.TunnelMsg{Payload: raw}, nil
		}
		return &pb.TunnelMsg{}, nil
	}

	b := &Bridge{
		bridgeID: "feishu:test",
		db:       db,
		hub:      hub,
		secrets:  &Secrets{OpenId: "ou_owner"},
	}
	b.turnRecovery.runAsync = func(fn func()) { fn() }
	b.turnRecovery.sendNotice = func(context.Context, *LarkChat, string) {}

	liveModel := "model-old"
	sessionKey := chat.TargetID + "\x00" + chat.SessionName
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.streamSessions = map[string]sessionStreamRegistration{
		sessionKey: {
			rootMsgID:      chat.RootMsgID,
			token:          1,
			ctx:            ctx,
			sessionID:      "sid-1",
			launchID:       "launch-1",
			agentType:      chat.AgentType,
			agentProfile:   chat.AgentProfile,
			effectiveModel: "model-old",
			setEffectiveModel: func(m string) bool {
				changed := m != liveModel
				liveModel = m
				return changed
			},
		},
	}

	b.markTurnRecoveryRateLimited("turn-hot", &chat, "model-old")
	b.turnRecovery.mu.Lock()
	b.turnRecovery.entries["turn-hot"].Candidates = []FallbackModelCandidate{{Model: "model-new", Label: "Model New"}}
	b.turnRecovery.entries["turn-hot"].CandidatesLoaded = true
	b.turnRecovery.mu.Unlock()

	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Action: &callback.CallBackAction{
				Value: map[string]any{
					modelRecoveryActionKey: modelRecoveryActionSwitch,
					"turn_id":              "turn-hot",
					"target_id":            "local",
					"session_name":         "work-sess",
					"root_msg_id":          "om_root",
					"model":                "model-new",
				},
			},
		},
	}
	resp, handled, err := b.handleModelRecoveryCardAction(context.Background(), event)
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "info" {
		t.Fatalf("response = %#v, handled=%v, err=%v", resp, handled, err)
	}
	if liveModel != "model-new" {
		t.Fatalf("live stream model = %q, want model-new", liveModel)
	}
	if got := b.streamSessions[sessionKey].effectiveModel; got != "model-new" {
		t.Fatalf("registered stream effectiveModel = %q, want model-new", got)
	}
	if !b.isTurnRecoveryClaimed("turn-hot") {
		t.Fatal("claimed turn should stay marked so its terminal event renders stopped and suppresses completion DMs")
	}
	// Subsequent ensureThreadStream with the updated chat must reuse the live
	// stream seamlessly without cancelling or replacing it.
	chat.EffectiveModel = "model-new"
	b.ensureThreadStream(&chat)
	if ctx.Err() != nil {
		t.Fatal("ensureThreadStream cancelled the live stream after in-place model sync")
	}
}

func TestTurnRecoveryEndToEndStreamCardStoppedNoDMAndUpdatedModel(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.presentation]
completion_notification_enabled = true
completion_notification_after_seconds = 1
`)

	var mu sync.Mutex
	var cardOps []string
	var cardCount int
	cardReply := make(chan string, 16)
	cardDone := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tok","expire":7200}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		mu.Lock()
		cardOps = append(cardOps, r.Method+" "+r.URL.Path+" "+string(raw))
		mu.Unlock()
		switch {
		case r.URL.Path == "/open-apis/cardkit/v1/cards":
			mu.Lock()
			cardCount++
			id := fmt.Sprintf("card-%d", cardCount)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"card_id": id}})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": "msg-reply"}})
			if content, ok := body["content"].(string); ok {
				cardReply <- content
			}
		case strings.HasPrefix(r.URL.Path, "/open-apis/cardkit/v1/cards/"):
			_, _ = w.Write([]byte(`{"code":0}`))
			if strings.HasSuffix(r.URL.Path, "/settings") {
				cardDone <- r.URL.Path
			}
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"msg-other"}}`))
		}
	}))
	defer server.Close()

	b, hub, chat := newStreamLifecycleBridge(t)
	b.client = lark.NewClient("test-app", "test-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	b.secrets = &Secrets{OpenId: "ou_owner"}
	b.turnRecovery.runAsync = func(fn func()) { fn() }
	b.turnRecovery.sendNotice = func(context.Context, *LarkChat, string) {}

	var dmMu sync.Mutex
	var dmCards []string
	b.dmInteractive = func(_ context.Context, openID, card string) (string, string, error) {
		dmMu.Lock()
		dmCards = append(dmCards, openID+":"+card)
		dmMu.Unlock()
		return "dm-1", "", nil
	}

	chat.ThreadID = "omt_thread_1"
	chat.OpenId = "ou_user"
	chat.AgentProfile = "builtin/codex"
	chat.AgentType = "codex"
	chat.EffectiveModel = "model-old"
	if err := b.db.UpsertChat(*chat); err != nil {
		t.Fatal(err)
	}
	session := hub.setSession(chat, "session-id-1", "launch-1")
	session.EffectiveModel = "model-old"
	session.ExtraArgs = []string{"--model", "model-old"}

	var routeMu sync.Mutex
	var onRoute func(targetID, method string, payload any) (*pb.TunnelMsg, error)
	b.hub = &turnRecoveryStreamHub{
		lifecycleStreamHub: hub,
		routeFn: func(targetID, method string, payload any) (*pb.TunnelMsg, error) {
			routeMu.Lock()
			fn := onRoute
			routeMu.Unlock()
			if fn != nil {
				return fn(targetID, method, payload)
			}
			return &pb.TunnelMsg{}, nil
		},
	}

	b.startThreadStream(chat, "ou_user")
	stream := waitLifecycleSignal(t, hub.started)
	stream.events <- &pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT, CaughtUp: true, EffectiveModel: "model-old"}

	var seq uint64
	emitTurn := func(turnID string, turnSeq uint64, channel string, body agentstream.Body) {
		t.Helper()
		seq++
		raw, err := agentstream.MarshalEvent(agentstream.Event{
			SessionID: session.Id,
			Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: seq},
			Turn:      &agentstream.TurnRef{ID: turnID, Seq: turnSeq},
			At:        time.Now(),
			Origin:    agentstream.Origin{Actor: agentstream.ActorAgent, Channel: channel, Provider: "codex", LaunchID: session.LaunchId},
			Body:      body,
		})
		if err != nil {
			t.Fatal(err)
		}
		stream.events <- &pb.AttachEvent{CanonicalJson: raw}
	}

	// Turn 1 starts on model-old and has already been waiting > 2 minutes.
	if err := b.db.RecordTurnLinks([]TurnLink{{
		BridgeID:    chat.BridgeId,
		RootMsgID:   chat.RootMsgID,
		MessageID:   "om_user_1",
		OpenID:      "ou_user",
		TargetID:    chat.TargetID,
		SessionID:   session.Id,
		SessionName: chat.SessionName,
		LaunchID:    session.LaunchId,
		TurnID:      "turn-1",
		SubmittedAt: time.Now().Add(-2 * time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	b.setTurnActive(chat.RootMsgID, true)
	b.recordTurnRecovery(chat, "turn-1", "check cron", nil, "", []turnInputRef{{OpenID: "ou_user", MessageID: "om_user_1"}}, false)
	b.turnRecovery.mu.Lock()
	b.turnRecovery.entries["turn-1"].Candidates = []FallbackModelCandidate{{Model: "model-new", Label: "model-new"}}
	b.turnRecovery.entries["turn-1"].CandidatesLoaded = true
	b.turnRecovery.mu.Unlock()

	emitTurn("turn-1", 1, "lark", agentstream.TurnStarted{InputSummary: "check cron"})
	emitTurn("turn-1", 1, "agent", agentstream.ToolChanged{
		Phase: agentstream.ToolPhaseStarted,
		Tool:  agentstream.Tool{ID: "t1", Name: "bash", Title: "checking"},
	})
	_ = waitLifecycleSignal(t, cardReply)

	// Wire Route for Interrupt -> Restart (hot, same launch-1) -> Submit.
	routeMu.Lock()
	onRoute = func(targetID, method string, payload any) (*pb.TunnelMsg, error) {
		switch method {
		case "Interrupt":
			emitTurn("turn-1", 1, "agent", agentstream.TurnFinished{
				Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "interrupted"},
			})
			return &pb.TunnelMsg{}, nil
		case "Restart":
			raw, _ := json.Marshal(&pb.RunResponse{
				Session: &pb.Session{
					Id:             session.Id,
					LaunchId:       "launch-1",
					Name:           chat.SessionName,
					State:          "running",
					AgentType:      "codex",
					AgentProfile:   "builtin/codex",
					EffectiveModel: "model-new",
				},
			})
			return &pb.TunnelMsg{Payload: raw}, nil
		case "Submit":
			raw, _ := json.Marshal(&pb.SubmitResponse{
				SessionId: session.Id,
				LaunchId:  "launch-1",
				TurnId:    "turn-2",
			})
			return &pb.TunnelMsg{Payload: raw}, nil
		default:
			return &pb.TunnelMsg{}, nil
		}
	}
	routeMu.Unlock()

	// User clicks "Switch to model-new & retry".
	click := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_user"},
			Action: &callback.CallBackAction{
				Value: map[string]any{
					modelRecoveryActionKey: modelRecoveryActionSwitch,
					"turn_id":              "turn-1",
					"target_id":            chat.TargetID,
					"session_name":         chat.SessionName,
					"root_msg_id":          chat.RootMsgID,
					"model":                "model-new",
				},
			},
		},
	}
	resp, handled, err := b.handleModelRecoveryCardAction(context.Background(), click)
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "info" {
		t.Fatalf("click response = %#v handled=%v err=%v", resp, handled, err)
	}
	_ = waitLifecycleSignal(t, cardDone)

	// Allow any async DM goroutine a brief window if a bug tried to dispatch one.
	time.Sleep(150 * time.Millisecond)
	dmMu.Lock()
	gotDMs := append([]string(nil), dmCards...)
	dmMu.Unlock()
	if len(gotDMs) != 0 {
		t.Fatalf("interrupted model-recovery turn dispatched spurious DM notifications: %v", gotDMs)
	}

	// Turn 2 runs on the SAME live stream (same launch-1) and finishes.
	emitTurn("turn-2", 2, "lark", agentstream.TurnStarted{InputSummary: "check cron"})
	emitTurn("turn-2", 2, "agent", agentstream.ToolChanged{
		Phase: agentstream.ToolPhaseStarted,
		Tool:  agentstream.Tool{ID: "t2", Name: "bash", Title: "check cron"},
	})
	_ = waitLifecycleSignal(t, cardReply)
	emitTurn("turn-2", 2, "agent", agentstream.AssistantMessage{Text: "All good."})
	emitTurn("turn-2", 2, "agent", agentstream.ResultObserved{Text: "All good."})
	emitTurn("turn-2", 2, "agent", agentstream.TurnFinished{
		Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"},
	})
	_ = waitLifecycleSignal(t, cardDone)

	if stream.ctx.Err() != nil {
		t.Fatal("hot model switch unexpectedly disconnected the live stream")
	}

	mu.Lock()
	opsCopy := append([]string(nil), cardOps...)
	mu.Unlock()
	var card1Ops, card2Ops []string
	for _, op := range opsCopy {
		if strings.Contains(op, "/cards/card-1/") || strings.Contains(op, `"card_id":"card-1"`) {
			card1Ops = append(card1Ops, op)
		}
		if strings.Contains(op, "/cards/card-2/") || strings.Contains(op, `"card_id":"card-2"`) {
			card2Ops = append(card2Ops, op)
		}
	}
	card1All := strings.Join(card1Ops, "\n")
	if !strings.Contains(card1All, "已停止") {
		t.Fatalf("interrupted turn card-1 did not render stopped status:\n%s", card1All)
	}
	if strings.Contains(card1All, "已完成") {
		t.Fatalf("interrupted turn card-1 wrongly rendered done status:\n%s", card1All)
	}
	card2All := strings.Join(card2Ops, "\n")
	if !strings.Contains(card2All, "model-new") {
		t.Fatalf("post-switch turn card-2 did not show new model model-new:\n%s", card2All)
	}
	if strings.Contains(card2All, "model-old") {
		t.Fatalf("post-switch turn card-2 still showed stale model model-old:\n%s", card2All)
	}
}

type turnRecoveryStreamHub struct {
	*lifecycleStreamHub
	routeFn func(targetID, method string, payload any) (*pb.TunnelMsg, error)
}

func (h *turnRecoveryStreamHub) Route(_ context.Context, targetID, method string, payload any) (*pb.TunnelMsg, error) {
	return h.routeFn(targetID, method, payload)
}
