package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestRenderScheduleResultDeliveryCardBilingualAndBounded(t *testing.T) {
	t.Parallel()
	specJSON, _ := json.Marshal(task.Spec{Agent: "builtin/claude", Model: "claude-sonnet-4-6"})
	longSummary := strings.Repeat("检查正常，发现关键告警指标。\n", 600)
	nextRun := int64(1767229200)
	cases := []struct {
		name  string
		entry schedule.Entry
		run   schedule.Execution
	}{
		{
			name: "completed-long-untrusted-name",
			entry: schedule.Entry{
				Task: schedule.Task{
					ID:           42,
					Name:         "<at id=all>日报巡检</at>",
					ScheduleType: schedule.ScheduleTypeSubtask,
					Kind:         schedule.KindInterval,
					Enabled:      true,
					NextRunAt:    &nextRun,
					TaskSpecJson: specJSON,
				},
			},
			run: schedule.Execution{
				ID:            101,
				ScheduleID:    42,
				Status:        "completed",
				Terminal:      true,
				TriggerSource: schedule.TriggerSourceScheduled,
				FinishedAt:    1767225600,
				Result:        longSummary,
			},
		},
		{
			name: "failed-manual-once",
			entry: schedule.Entry{
				Task: schedule.Task{
					ID:   43,
					Kind: schedule.KindOnce,
				},
			},
			run: schedule.Execution{
				ID:            102,
				ScheduleID:    43,
				Status:        "failed",
				Terminal:      true,
				TriggerSource: schedule.TriggerSourceManual,
				FinishedAt:    1767225600,
				Reason:        "runtime exited with code 1",
			},
		},
		{
			name: "timed-out-empty-output",
			entry: schedule.Entry{
				Task: schedule.Task{ID: 44, Name: "check"},
			},
			run: schedule.Execution{
				ID:         103,
				ScheduleID: 44,
				Status:     "timed_out",
				Terminal:   true,
				FinishedAt: 1767225600,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := renderScheduleResultDeliveryCard(tc.entry, tc.run)
			if len(raw) >= maxFeishuCardBytes {
				t.Fatalf("card size %d exceeds maxFeishuCardBytes %d", len(raw), maxFeishuCardBytes)
			}
			for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
				rendered := selectManagementCardLocale(raw, locale)
				var doc any
				if err := json.Unmarshal([]byte(rendered), &doc); err != nil {
					t.Fatalf("invalid json for locale %q: %v", locale, err)
				}
				walkManagementTestText(doc, func(text string) {
					if strings.Contains(text, "schedule.") || strings.Contains(text, "%!") {
						t.Fatalf("unresolved copy in locale %q: %s", locale, text)
					}
				})
			}
		})
	}
}

type fakeDeliveryRequest struct {
	Path          string
	ReceiveIDType string
	Body          map[string]any
}

type deliveryAuthScheduleControl struct {
	store *schedule.Store
	deny  map[string]bool
	acted []string
	onAct func(open string, id int64, action string) error
}

func (c *deliveryAuthScheduleControl) List(ctx context.Context, bot, open string) ([]schedule.Entry, error) {
	return c.store.Personal(bot, open)
}
func (c *deliveryAuthScheduleControl) Get(ctx context.Context, bot, open string, id int64) (schedule.Entry, error) {
	if c.deny[open] {
		return schedule.Entry{}, errors.New("creator authorization revoked")
	}
	entries, err := c.store.Personal(bot, open)
	if err != nil {
		return schedule.Entry{}, err
	}
	for _, entry := range entries {
		if entry.Task.ID == id {
			return entry, nil
		}
	}
	return schedule.Entry{}, errors.New("not found")
}
func (c *deliveryAuthScheduleControl) Act(ctx context.Context, bot, open string, id int64, action string) error {
	if c.deny[open] {
		return errors.New("creator authorization revoked")
	}
	if _, err := c.Get(ctx, bot, open, id); err != nil {
		return err
	}
	c.acted = append(c.acted, fmt.Sprintf("%s:%d:%s", open, id, action))
	if c.onAct != nil {
		return c.onAct(open, id, action)
	}
	return nil
}
func (c *deliveryAuthScheduleControl) Creators(bot string) ([]string, error) {
	return nil, nil
}
func (c *deliveryAuthScheduleControl) Store() *schedule.Store {
	return c.store
}

func TestDeliverScheduleResultsRoutingFallbackAndAuthRevocation(t *testing.T) {
	t.Parallel()
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var mu sync.Mutex
	var calls []fakeDeliveryRequest
	replyErrCode := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/auth/v3/tenant_access_token") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"tok","expire":3600}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, fakeDeliveryRequest{
			Path:          r.URL.Path,
			ReceiveIDType: r.URL.Query().Get("receive_id_type"),
			Body:          body,
		})
		errCode := replyErrCode
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/reply") && errCode != 0 {
			_, _ = io.WriteString(w, `{"code":230002,"msg":"message recalled"}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"om_sent_1"}}`)
	}))
	defer srv.Close()

	ctrl := &deliveryAuthScheduleControl{store: store, deny: map[string]bool{}}
	b := &Bridge{
		bridgeID:  "bot-1",
		secrets:   &Secrets{AppId: "bot-1", AppSecret: "sec"},
		client:    lark.NewClient("bot-1", "sec", lark.WithOpenBaseUrl(srv.URL)),
		schedules: ctrl,
	}
	deliverNow := time.Unix(1700001000, 0)

	enqueueTerminal := func(name, openID, target, rootMsg, chatID, summary string) (int64, int64) {
		t.Helper()
		now := time.Unix(1700000000, 0)
		nextRun := now.Unix()
		tsk, err := store.Create(schedule.Task{
			Name:           name,
			Spec:           "every 1h",
			ScheduleType:   schedule.ScheduleTypeSubtask,
			DeliveryTarget: target,
			DeliveryPolicy: schedule.DeliveryPolicyAlways,
			Enabled:        true,
			NextRunAt:      &nextRun,
		})
		if err != nil {
			t.Fatal(err)
		}
		creator := schedule.Creator{
			BotID:           "bot-1",
			OpenID:          openID,
			PrincipalID:     "p_" + openID,
			Source:          "turn:" + openID,
			OriginRootMsgID: rootMsg,
			OriginChatID:    chatID,
		}
		if target == schedule.DeliveryTargetCreatorDM && openID != "ou_legacy" {
			creator = isolatedScheduleDeliveryTestCreator(openID)
		}
		if err := store.BindCreator(tsk.ID, creator); err != nil {
			t.Fatal(err)
		}
		nextAfter := now.Add(time.Hour).Unix()
		run, claimed, err := store.ClaimTracked(tsk, now, &nextAfter)
		if err != nil || !claimed {
			t.Fatalf("ClaimTracked: claimed=%v err=%v", claimed, err)
		}
		run.Status = "completed"
		run.Terminal = true
		run.FinishedAt = 1700000010
		run.Result = summary
		if err := store.Observe(run, time.Unix(1700000010, 0)); err != nil {
			t.Fatal(err)
		}
		return tsk.ID, run.ID
	}

	// 1) origin with root message -> thread reply (reply_in_thread=true).
	s1, _ := enqueueTerminal("thread-job", "ou_alice", schedule.DeliveryTargetOrigin, "om_root_1", "oc_chat_1", "All green")
	b.deliverScheduleResults(context.Background(), deliverNow)
	mu.Lock()
	if len(calls) != 1 || !strings.HasSuffix(calls[0].Path, "/om_root_1/reply") || calls[0].Body["reply_in_thread"] != true {
		t.Fatalf("case 1 calls = %+v", calls)
	}
	if calls[0].Body["uuid"] == "" {
		t.Fatalf("expected deterministic uuid on reply")
	}
	calls = nil
	mu.Unlock()
	if hist, _ := store.ExecutionHistory(s1); len(hist) != 1 || hist[0].DeliveryStatus != schedule.DeliveryStatusDelivered {
		t.Fatalf("case 1 status = %+v", hist)
	}

	// 2) origin when root message fails permanently (230002) -> fallback to OriginChatID.
	mu.Lock()
	replyErrCode = 230002
	mu.Unlock()
	s2, _ := enqueueTerminal("fallback-job", "ou_alice", schedule.DeliveryTargetOrigin, "om_deleted", "oc_chat_2", "Report ready")
	b.deliverScheduleResults(context.Background(), deliverNow)
	mu.Lock()
	if len(calls) != 2 || !strings.HasSuffix(calls[0].Path, "/om_deleted/reply") || calls[1].ReceiveIDType != "chat_id" || calls[1].Body["receive_id"] != "oc_chat_2" {
		t.Fatalf("case 2 fallback calls = %+v", calls)
	}
	calls = nil
	replyErrCode = 0
	mu.Unlock()
	if hist, _ := store.ExecutionHistory(s2); len(hist) != 1 || hist[0].DeliveryStatus != schedule.DeliveryStatusDelivered {
		t.Fatalf("case 2 status = %+v", hist)
	}

	// Each creator's verified identity and own Runtime determine the private route.
	var personal []feishumsg.PersonalRequest
	b.hub = replyEndTestHub{send: func(_ context.Context, target string, req feishumsg.PersonalRequest) error {
		want := isolatedScheduleDeliveryTestCreator([]string{"ou_bob", "ou_alice"}[len(personal)])
		if target != want.OriginTargetID || req.PrincipalID != want.PrincipalID ||
			req.ScheduleIdentity == nil || req.ScheduleIdentity.EffectivePrincipalID != want.PrincipalID ||
			req.SessionID != "" || req.Message.ReceiveIDType != feishumsg.Self || req.Message.Validate() != nil {
			t.Fatalf("unexpected private delivery to %s: %+v", target, req)
		}
		personal = append(personal, req)
		return nil
	}}
	s3, _ := enqueueTerminal("dm-job", "ou_bob", schedule.DeliveryTargetCreatorDM, "", "", "Bob summary")
	s5, _ := enqueueTerminal("alice-job", "ou_alice", schedule.DeliveryTargetCreatorDM, "", "", "Alice summary")
	legacy, _ := enqueueTerminal("legacy-job", "ou_legacy", schedule.DeliveryTargetCreatorDM, "", "", "Legacy summary")
	b.deliverScheduleResults(context.Background(), deliverNow)
	mu.Lock()
	if len(calls) != 0 {
		t.Fatalf("private results must not use the shared Bot: %+v", calls)
	}
	mu.Unlock()
	if len(personal) != 2 || !strings.Contains(personal[0].Message.Text, "Bob summary") ||
		!strings.Contains(personal[1].Message.Text, "Alice summary") || personal[0].Message.UUID == personal[1].Message.UUID {
		t.Fatalf("wrong private results: %+v", personal)
	}
	for _, id := range []int64{s3, s5} {
		if hist, _ := store.ExecutionHistory(id); len(hist) != 1 || hist[0].DeliveryStatus != schedule.DeliveryStatusDelivered {
			t.Fatalf("private result status = %+v", hist)
		}
	}
	if hist, _ := store.ExecutionHistory(legacy); len(hist) != 1 || hist[0].DeliveryStatus != schedule.DeliveryStatusDenied {
		t.Fatalf("unverified private result should be denied: %+v", hist)
	}

	// 4) Creator revoked -> denied, zero outbound Feishu calls.
	ctrl.deny["ou_revoked"] = true
	s4, _ := enqueueTerminal("revoked-job", "ou_revoked", schedule.DeliveryTargetOrigin, "om_root_4", "oc_chat_4", "Secret report")
	b.deliverScheduleResults(context.Background(), deliverNow)
	mu.Lock()
	if len(calls) != 0 {
		t.Fatalf("case 4 revoked creator must never send, got calls = %+v", calls)
	}
	mu.Unlock()
	if hist, _ := store.ExecutionHistory(s4); len(hist) != 1 || hist[0].DeliveryStatus != schedule.DeliveryStatusDenied {
		t.Fatalf("case 4 status = %+v, want denied", hist)
	}
}

func isolatedScheduleDeliveryTestCreator(openID string) schedule.Creator {
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(openID)).String()
	raw, err := json.Marshal(principal.ExecutionIdentity{
		IdentityMode: principal.IdentityModeIdentified, Source: principal.SourceSchedule,
		OriginActorPrincipalID: id, EffectivePrincipalID: id, CreationAuthorizationDecisionID: "decision",
	})
	if err != nil {
		panic(err)
	}
	return schedule.Creator{BotID: "bot-1", OpenID: openID, PrincipalID: id, Source: "turn:fixture",
		AccessIdentityJSON: string(raw), OriginTargetID: "origin-" + openID}
}

func TestIsolatedScheduleOffersPersonalDeliveryAndBoundsLocalizedMessage(t *testing.T) {
	t.Parallel()
	entry := schedule.Entry{
		Task:       schedule.Task{ID: 1, Name: "fixture", ScheduleType: schedule.ScheduleTypeSubtask},
		Visibility: schedule.Visibility{Creator: isolatedScheduleDeliveryTestCreator("ou_bob")},
	}
	card := renderScheduleDetail(entry, false)
	if !strings.Contains(card, `"value":"creator_dm"`) {
		t.Fatalf("missing personal delivery choice: %s", card)
	}
	d := schedule.ResultDelivery{ScheduleID: 1, RunID: 2, BotID: "bot-1", Run: schedule.Execution{
		Status: "completed", Result: strings.Repeat("𠮷<", 3000),
	}}
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese, ""} {
		msg := schedulePersonalResultMessage(entry, d, locale)
		if err := msg.Validate(); err != nil || !strings.Contains(msg.Text, "fixture") || !strings.HasSuffix(msg.Text, "…") {
			t.Fatalf("invalid localized result (%q): %v", locale, err)
		}
	}
}

func TestScheduleResultCardQuickActionsAuthorizationAndNoDMSpam(t *testing.T) {
	t.Parallel()
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nextRun := int64(1767229200)
	tsk, err := store.Create(schedule.Task{
		Name:           "recurring-uptime",
		Spec:           "every 10m",
		Kind:           schedule.KindInterval,
		ScheduleType:   schedule.ScheduleTypeSubtask,
		DeliveryTarget: schedule.DeliveryTargetOrigin,
		DeliveryPolicy: schedule.DeliveryPolicyAlways,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindCreator(tsk.ID, schedule.Creator{
		BotID:       "bot-1",
		OpenID:      "ou_alice",
		PrincipalID: "p_alice",
		Source:      "turn:alice",
	}); err != nil {
		t.Fatal(err)
	}

	// Verify rendered result card has rerun, pause, and manage buttons for active recurring schedule,
	// and omits pause for a completed one-shot schedule.
	activeCard := renderScheduleResultDeliveryCard(schedule.Entry{Task: tsk}, schedule.Execution{Status: "completed", Terminal: true})
	if !strings.Contains(activeCard, "schedule.trigger") || !strings.Contains(activeCard, "schedule.pause") || !strings.Contains(activeCard, "schedule.detail") {
		t.Fatalf("active recurring delivery card missing quick action buttons: %s", activeCard)
	}
	onceDone := tsk
	onceDone.Kind = schedule.KindOnce
	onceDone.NextRunAt = nil
	onceCard := renderScheduleResultDeliveryCard(schedule.Entry{Task: onceDone}, schedule.Execution{Status: "completed", Terminal: true})
	if strings.Contains(onceCard, "schedule.pause") {
		t.Fatalf("completed one-shot delivery card should not show pause button")
	}

	ctrl := &deliveryAuthScheduleControl{store: store, deny: map[string]bool{}}
	dmSent := 0
	b := &Bridge{
		bridgeID:  "bot-1",
		secrets:   &Secrets{AppId: "bot-1", AppSecret: "sec"},
		schedules: ctrl,
	}
	b.dmInteractive = func(_ context.Context, open, card string) (string, string, error) {
		dmSent++
		if open != "ou_alice" {
			t.Fatalf("unexpected DM recipient %q", open)
		}
		return "om_dm", "", nil
	}

	click := func(who, op string) *callback.CardActionTriggerResponse {
		t.Helper()
		resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
			Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: who},
				Context:  &callback.Context{OpenMessageID: "om_group_result_card"},
				Action: &callback.CallBackAction{
					Value: map[string]interface{}{
						managementActionKey: "schedule." + op,
						"schedule_id":       fmt.Sprint(tsk.ID),
						"source":            "delivery",
					},
				},
			},
		})
		if err != nil || !handled {
			t.Fatalf("click(%s, %s): handled=%v err=%v", who, op, handled, err)
		}
		return resp
	}

	// Non-creator is refused with zero side effects and zero DM sends.
	if resp := click("ou_bob", "pause"); resp.Toast.Type != "error" {
		t.Fatalf("non-creator pause should fail, got %+v", resp.Toast)
	}
	if len(ctrl.acted) != 0 || dmSent != 0 {
		t.Fatalf("non-creator caused side effects: acted=%v dmSent=%d", ctrl.acted, dmSent)
	}

	// Creator clicking trigger and pause executes actions with crisp toasts and ZERO DM spam.
	if resp := click("ou_alice", "trigger"); resp.Toast.Type != "success" {
		t.Fatalf("creator trigger failed: %+v", resp.Toast)
	}
	if resp := click("ou_alice", "pause"); resp.Toast.Type != "success" {
		t.Fatalf("creator pause failed: %+v", resp.Toast)
	}
	if dmSent != 0 {
		t.Fatalf("trigger/pause on delivery card should not spam DM, got dmSent=%d", dmSent)
	}

	// Creator clicking detail sends management detail card privately to DM.
	if resp := click("ou_alice", "detail"); resp.Toast.Type != "success" {
		t.Fatalf("creator detail failed: %+v", resp.Toast)
	}
	if dmSent != 1 {
		t.Fatalf("detail click should send 1 private DM card, got %d", dmSent)
	}
}

func TestRenderScheduleCreatedAndAutoPausedCardsBilingualAndBounded(t *testing.T) {
	t.Parallel()
	specJSON, _ := json.Marshal(task.Spec{Agent: "builtin/claude", Model: "claude-sonnet-4-6"})
	nextRun := int64(1767229200)
	entries := []schedule.Entry{
		{
			Task: schedule.Task{
				ID:             51,
				Name:           "<at id=all>每日早报</at>",
				Spec:           "daily 09:00",
				Timezone:       "Asia/Shanghai",
				Kind:           schedule.KindDaily,
				ScheduleType:   schedule.ScheduleTypeSubtask,
				DeliveryTarget: schedule.DeliveryTargetOrigin,
				DeliveryPolicy: schedule.DeliveryPolicyOnNonEmpty,
				Enabled:        true,
				NextRunAt:      &nextRun,
				TaskSpecJson:   specJSON,
			},
			Visibility: schedule.Visibility{
				ConsecutiveFailures: 5,
				AutoPausedAt:        1767225600,
				AutoPausedReason:    "5 consecutive failures",
			},
			Runs: []schedule.Execution{
				{ID: 1, Status: "failed", DeliveryStatus: schedule.DeliveryStatusDelivered, ScheduledAt: 1767225000},
			},
		},
		{
			Task: schedule.Task{
				ID:             52,
				Name:           "session-ping",
				Spec:           "every 5m",
				Kind:           schedule.KindInterval,
				ScheduleType:   schedule.ScheduleTypeSessionPrompt,
				RunMode:        schedule.RunModeReuseSession,
				SessionName:    "feishu-s1",
				DeliveryTarget: schedule.DeliveryTargetNone,
				Enabled:        true,
				NextRunAt:      &nextRun,
			},
		},
	}

	for _, entry := range entries {
		cards := []string{
			renderScheduleCreatedCard(entry),
			renderScheduleResultDeliveryCard(entry, schedule.Execution{Status: "failed", Terminal: true, FinishedAt: 1767225600}),
			renderSchedules([]schedule.Entry{entry, {Task: schedule.Task{ID: 99, Name: "done", Kind: schedule.KindOnce, Enabled: false, LastRunAt: &nextRun}}}, "all", 0),
			renderScheduleDetail(entry, false),
			renderScheduleDetail(entry, true),
		}
		for _, raw := range cards {
			if len(raw) >= maxFeishuCardBytes {
				t.Fatalf("card size %d exceeds maxFeishuCardBytes %d", len(raw), maxFeishuCardBytes)
			}
			for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
				rendered := selectManagementCardLocale(raw, locale)
				var doc any
				if err := json.Unmarshal([]byte(rendered), &doc); err != nil {
					t.Fatalf("invalid json for locale %q: %v", locale, err)
				}
				names := map[string]bool{}
				var checkButtons func(any)
				checkButtons = func(value any) {
					switch v := value.(type) {
					case map[string]any:
						if v["tag"] == "button" {
							name, _ := v["name"].(string)
							if name == "" || names[name] {
								t.Fatalf("duplicate/empty button name %q in locale %q", name, locale)
							}
							names[name] = true
						}
						for _, child := range v {
							checkButtons(child)
						}
					case []any:
						for _, child := range v {
							checkButtons(child)
						}
					}
				}
				checkButtons(doc)
				walkManagementTestText(doc, func(text string) {
					if strings.Contains(text, "schedule.") || strings.Contains(text, "%!") {
						t.Fatalf("unresolved copy in locale %q: %s", locale, text)
					}
				})
			}
		}
	}

	// Verify created card has undo/delete button and reuse-session detail card omits origin target.
	createdCard := renderScheduleCreatedCard(entries[0])
	if !strings.Contains(createdCard, "schedule.delete") || !strings.Contains(createdCard, "schedule.trigger") {
		t.Fatalf("created card missing quick actions: %s", createdCard)
	}
	reuseDetail := renderScheduleDetail(entries[1], false)
	if strings.Contains(reuseDetail, `"value":"origin"`) {
		t.Fatalf("reuse-session detail card must not offer origin delivery target: %s", reuseDetail)
	}
}

func TestDeliverScheduleCreatedCardEvenWhenResultTargetNone(t *testing.T) {
	t.Parallel()
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var mu sync.Mutex
	var calls []fakeDeliveryRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/auth/v3/tenant_access_token") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"tok","expire":3600}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, fakeDeliveryRequest{
			Path:          r.URL.Path,
			ReceiveIDType: r.URL.Query().Get("receive_id_type"),
			Body:          body,
		})
		mu.Unlock()
		_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"om_created_1"}}`)
	}))
	defer srv.Close()

	now := time.Unix(1700000000, 0)
	nextRun := now.Add(time.Hour).Unix()
	tsk, err := store.Create(schedule.Task{
		Name:           "session-job",
		Spec:           "every 1h",
		Kind:           schedule.KindInterval,
		ScheduleType:   schedule.ScheduleTypeSessionPrompt,
		RunMode:        schedule.RunModeReuseSession,
		DeliveryTarget: schedule.DeliveryTargetNone,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	creator := schedule.Creator{
		BotID:           "bot-1",
		OpenID:          "ou_alice",
		PrincipalID:     "p_alice",
		OriginRootMsgID: "om_root_created",
		OriginChatID:    "oc_chat_1",
		Source:          "turn:alice",
	}
	if err := store.BindCreator(tsk.ID, creator); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueCreatedCard(tsk, creator, now); err != nil {
		t.Fatal(err)
	}

	b := &Bridge{
		bridgeID:  "bot-1",
		secrets:   &Secrets{AppId: "bot-1", AppSecret: "sec"},
		client:    lark.NewClient("bot-1", "sec", lark.WithOpenBaseUrl(srv.URL)),
		schedules: &deliveryAuthScheduleControl{store: store, deny: map[string]bool{}},
	}
	if err := b.deliverScheduleResults(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || !strings.HasSuffix(calls[0].Path, "/om_root_created/reply") {
		t.Fatalf("expected created card reply in origin thread, got %+v", calls)
	}
	content, _ := calls[0].Body["content"].(string)
	if !strings.Contains(content, "schedule.delete") {
		t.Fatalf("created card missing undo/delete button: %s", content)
	}
}

func TestScheduleP1ActionsAdminEmergencyPauseDeliverySwitchDeleteAndPrune(t *testing.T) {
	t.Parallel()
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := int64(1767229200)
	active, err := store.Create(schedule.Task{
		Name:           "alice-monitor",
		Spec:           "every 10m",
		Kind:           schedule.KindInterval,
		ScheduleType:   schedule.ScheduleTypeSubtask,
		DeliveryTarget: schedule.DeliveryTargetOrigin,
		DeliveryPolicy: schedule.DeliveryPolicyAlways,
		Enabled:        true,
		NextRunAt:      &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	doneOnce, err := store.Create(schedule.Task{
		Name:         "alice-once-done",
		Spec:         "at 2026-01-01T00:00:00Z",
		Kind:         schedule.KindOnce,
		ScheduleType: schedule.ScheduleTypeSubtask,
		Enabled:      false,
		LastRunAt:    &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{active.ID, doneOnce.ID} {
		if err := store.BindCreator(id, schedule.Creator{
			BotID:       "bot-1",
			OpenID:      "ou_alice",
			PrincipalID: "p_alice",
			Source:      "turn:alice",
		}); err != nil {
			t.Fatal(err)
		}
	}

	ctrl := &deliveryAuthScheduleControl{
		store: store,
		deny:  map[string]bool{},
		onAct: func(open string, id int64, action string) error {
			switch {
			case action == "delete":
				_, err := store.Delete(id)
				return err
			case action == "prune_completed":
				_, err := store.Delete(doneOnce.ID)
				return err
			case strings.HasPrefix(action, "delivery_target:"):
				row, err := store.Get(id)
				if err != nil {
					return err
				}
				row.DeliveryTarget = strings.TrimPrefix(action, "delivery_target:")
				_, err = store.Update(row)
				return err
			case strings.HasPrefix(action, "delivery_policy:"):
				row, err := store.Get(id)
				if err != nil {
					return err
				}
				row.DeliveryPolicy = strings.TrimPrefix(action, "delivery_policy:")
				_, err = store.Update(row)
				return err
			}
			return nil
		},
	}
	// Wrap Act so admin_pause and prune_completed match hub's bridgeSchedules semantics.
	wrappedCtrl := &p1ScheduleControl{deliveryAuthScheduleControl: ctrl}

	dmSent := 0
	var lastDMCard string
	b := &Bridge{
		bridgeID:  "bot-1",
		secrets:   &Secrets{AppId: "bot-1", AppSecret: "sec", OpenId: "ou_admin"},
		schedules: wrappedCtrl,
	}
	b.dmInteractive = func(_ context.Context, open, card string) (string, string, error) {
		dmSent++
		lastDMCard = card
		return "om_dm", "", nil
	}

	click := func(who, op, source string, id int64, extra map[string]interface{}) *callback.CardActionTriggerResponse {
		t.Helper()
		vals := map[string]interface{}{
			managementActionKey: "schedule." + op,
			"schedule_id":       fmt.Sprint(id),
		}
		if source != "" {
			vals["source"] = source
		}
		for k, v := range extra {
			vals[k] = v
		}
		resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
			Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: who},
				Context:  &callback.Context{OpenMessageID: "om_shared_card"},
				Action:   &callback.CallBackAction{Value: vals},
			},
		})
		if err != nil || !handled {
			t.Fatalf("click(%s, %s): handled=%v err=%v", who, op, handled, err)
		}
		return resp
	}

	// 1) Non-creator Bot admin can emergency-pause on a delivery card with zero DM leak,
	//    but is STILL strictly refused for trigger, detail, delete, or forwarded pause.
	if resp := click("ou_admin", "pause", "delivery", active.ID, nil); resp.Toast.Type != "success" {
		t.Fatalf("bot owner emergency pause on delivery card failed: %+v", resp.Toast)
	}
	if dmSent != 0 {
		t.Fatalf("admin emergency pause must not send DM, got %d", dmSent)
	}
	for _, forbidden := range []string{"trigger", "detail", "delete", "resume"} {
		if resp := click("ou_admin", forbidden, "delivery", active.ID, nil); resp.Toast.Type != "error" {
			t.Fatalf("bot owner should not be allowed %s on delivery card", forbidden)
		}
	}
	if resp := click("ou_admin", "pause", "", active.ID, nil); resp.Toast.Type != "error" {
		t.Fatalf("bot owner pause on non-delivery forwarded card must remain refused")
	}
	// Non-admin non-creator is refused even for delivery pause.
	if resp := click("ou_bob", "pause", "delivery", active.ID, nil); resp.Toast.Type != "error" {
		t.Fatalf("non-admin non-creator pause on delivery card must fail")
	}

	// 2) Creator switches delivery_target and delivery_policy from detail card.
	respTarget := click("ou_alice", "delivery_target", "", active.ID, map[string]interface{}{"value": "creator_dm"})
	if respTarget.Toast.Type != "success" || !strings.Contains(respTarget.Toast.I18nContent["en_us"], "Creator DM") || !strings.Contains(respTarget.Toast.I18nContent["zh_cn"], "创建人私聊") {
		t.Fatalf("creator delivery_target switch failed or lacked specific toast: %+v", respTarget.Toast)
	}
	respPolicy := click("ou_alice", "delivery_policy", "", active.ID, map[string]interface{}{"value": "on_failure"})
	if respPolicy.Toast.Type != "success" || !strings.Contains(respPolicy.Toast.I18nContent["en_us"], "Only on failure") || !strings.Contains(respPolicy.Toast.I18nContent["zh_cn"], "仅失败推送") {
		t.Fatalf("creator delivery_policy switch failed or lacked specific toast: %+v", respPolicy.Toast)
	}
	if !strings.Contains(lastDMCard, "✓ ") || !strings.Contains(lastDMCard, `"confirm"`) {
		t.Fatalf("expected active checkmark and delete confirmation dialog in detail card")
	}
	got, _ := store.Get(active.ID)
	if got.DeliveryTarget != schedule.DeliveryTargetCreatorDM || got.DeliveryPolicy != schedule.DeliveryPolicyOnFailure {
		t.Fatalf("delivery settings not persisted: target=%q policy=%q", got.DeliveryTarget, got.DeliveryPolicy)
	}

	// 3) Creator clicks prune_completed -> removes completed one-shot, keeps active schedule.
	if resp := click("ou_alice", "prune_completed", "", 0, map[string]interface{}{"filter": "all"}); resp.Toast.Type != "success" {
		t.Fatalf("prune_completed failed: %+v", resp.Toast)
	}
	if _, err := store.Get(doneOnce.ID); err == nil {
		t.Fatalf("expected completed one-shot to be deleted")
	}
	if !strings.Contains(lastDMCard, "alice-monitor") {
		t.Fatalf("pruned list card missing remaining active schedule")
	}

	// 4) Creator clicks undo/delete on delivery card -> deletes schedule with crisp toast and zero DM spam.
	dmBefore := dmSent
	if resp := click("ou_alice", "delete", "delivery", active.ID, nil); resp.Toast.Type != "success" {
		t.Fatalf("creator delivery delete failed: %+v", resp.Toast)
	}
	if dmSent != dmBefore {
		t.Fatalf("delivery delete should not send unsolicited DM")
	}
	if _, err := store.Get(active.ID); err == nil {
		t.Fatalf("expected active schedule to be deleted")
	}
}

type p1ScheduleControl struct {
	*deliveryAuthScheduleControl
}

func (c *p1ScheduleControl) Act(ctx context.Context, bot, open string, id int64, action string) error {
	if action == "prune_completed" {
		return c.onAct(open, 0, action)
	}
	if action == "admin_pause" {
		v, err := c.store.Visibility(id)
		if err != nil || v.Creator.BotID != bot {
			return errors.New("unavailable")
		}
		c.acted = append(c.acted, fmt.Sprintf("%s:%d:%s", open, id, action))
		return nil
	}
	return c.deliveryAuthScheduleControl.Act(ctx, bot, open, id, action)
}

func TestScheduleRunDetailCardFullOutputAndSessionViewerLink(t *testing.T) {
	t.Parallel()
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1700000000, 0)
	nextRun := now.Unix()
	tsk, err := store.Create(schedule.Task{
		Name:           "daily-audit",
		Spec:           "daily 09:00",
		Kind:           schedule.KindDaily,
		ScheduleType:   schedule.ScheduleTypeSubtask,
		DeliveryTarget: schedule.DeliveryTargetOrigin,
		DeliveryPolicy: schedule.DeliveryPolicyAlways,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindCreator(tsk.ID, schedule.Creator{
		BotID:       "bot-1",
		OpenID:      "ou_alice",
		PrincipalID: "p_alice",
		Source:      "turn:alice",
	}); err != nil {
		t.Fatal(err)
	}
	nextAfter := now.Add(24 * time.Hour).Unix()
	run, claimed, err := store.ClaimTracked(tsk, now, &nextAfter)
	if err != nil || !claimed {
		t.Fatalf("ClaimTracked: claimed=%v err=%v", claimed, err)
	}
	longBody := "## Summary\n" + strings.Repeat("Line of detailed audit output.\n", 80) + "TAIL_MARKER_VISIBLE"
	run.Status = "completed"
	run.Terminal = true
	run.StartedAt = now.Unix()
	run.FinishedAt = now.Add(5 * time.Second).Unix()
	run.TargetID = "runtime-1"
	run.SessionName = "subtask-task-audit-1"
	run.TaskID = "task-audit-1"
	run.TurnID = "turn-audit-1"
	run.Result = longBody
	if err := store.Observe(run, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}

	// Render full run detail both with and without session viewer URL and check bilingual/size/buttons.
	entry := schedule.Entry{Task: tsk, Runs: []schedule.Execution{run}}
	for _, viewer := range []string{"", "https://viewer.example/s/tok-123"} {
		raw := renderScheduleRunDetail(entry, run, 0, viewer)
		if len(raw) >= maxFeishuCardBytes {
			t.Fatalf("run detail card too large: %d", len(raw))
		}
		if !strings.Contains(raw, "TAIL_MARKER_VISIBLE") {
			t.Fatalf("run detail card truncated 2500-char body that should be preserved in full")
		}
		if viewer != "" && !strings.Contains(raw, viewer) {
			t.Fatalf("expected session viewer link %q in run detail card", viewer)
		}
		for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
			selected := selectManagementCardLocale(raw, locale)
			var doc any
			if err := json.Unmarshal([]byte(selected), &doc); err != nil {
				t.Fatalf("invalid json for locale %q: %v", locale, err)
			}
			names := map[string]bool{}
			var checkButtons func(any)
			checkButtons = func(value any) {
				switch v := value.(type) {
				case map[string]any:
					if v["tag"] == "button" {
						name, _ := v["name"].(string)
						if name == "" || names[name] {
							t.Fatalf("duplicate/empty button name %q in locale %q", name, locale)
						}
						names[name] = true
					}
					for _, child := range v {
						checkButtons(child)
					}
				case []any:
					for _, child := range v {
						checkButtons(child)
					}
				}
			}
			checkButtons(doc)
			walkManagementTestText(doc, func(text string) {
				if strings.Contains(text, "schedule.") || strings.Contains(text, "%!") {
					t.Fatalf("unresolved copy in locale %q: %s", locale, text)
				}
			})
		}
	}

	// Exercise schedule.run_detail callback via Bridge with creator auth and non-creator refusal.
	ctrl := &deliveryAuthScheduleControl{store: store, deny: map[string]bool{}}
	var sentCard string
	b := &Bridge{
		bridgeID:  "bot-1",
		secrets:   &Secrets{AppId: "bot-1", AppSecret: "sec"},
		schedules: ctrl,
	}
	b.SetSessionViewer("https://viewer.example", "secret-signing-token")
	b.dmInteractive = func(_ context.Context, open, card string) (string, string, error) {
		sentCard = card
		return "om_dm", "", nil
	}

	// Ensure deliveryAuthScheduleControl.Get includes Runs for run_detail lookup.
	b.schedules = &runsAwareScheduleControl{deliveryAuthScheduleControl: ctrl}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_bob"},
			Context:  &callback.Context{OpenMessageID: "om_shared"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: "schedule.run_detail",
				"schedule_id":       fmt.Sprint(tsk.ID),
				"run_id":            fmt.Sprint(run.ID),
				"page":              "0",
			}},
		},
	})
	if err != nil || !handled || resp.Toast.Type != "error" || sentCard != "" {
		t.Fatalf("non-creator run_detail should be refused: handled=%v err=%v toast=%+v", handled, err, resp.Toast)
	}

	resp, handled, err = b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_alice"},
			Context:  &callback.Context{OpenMessageID: "om_shared"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: "schedule.run_detail",
				"schedule_id":       fmt.Sprint(tsk.ID),
				"run_id":            fmt.Sprint(run.ID),
				"page":              "0",
			}},
		},
	})
	if err != nil || !handled || resp.Toast.Type != "success" {
		t.Fatalf("creator run_detail failed: handled=%v err=%v toast=%+v", handled, err, resp.Toast)
	}
	if !strings.Contains(sentCard, "TAIL_MARKER_VISIBLE") || !strings.Contains(sentCard, "https://viewer.example/s/") {
		t.Fatalf("run_detail card missing full output or session viewer URL: %s", sentCard)
	}
}

type runsAwareScheduleControl struct {
	*deliveryAuthScheduleControl
}

func (c *runsAwareScheduleControl) Get(ctx context.Context, bot, open string, id int64) (schedule.Entry, error) {
	e, err := c.deliveryAuthScheduleControl.Get(ctx, bot, open, id)
	if err != nil {
		return e, err
	}
	e.Runs, err = c.store.VisibleHistory(id)
	return e, err
}

func TestScheduleDetailCardHidesCreatorDMOption(t *testing.T) {
	t.Parallel()
	subtaskCard := renderScheduleDetail(schedule.Entry{
		Task: schedule.Task{
			ID:             11,
			Name:           "daily-check",
			ScheduleType:   schedule.ScheduleTypeSubtask,
			Spec:           "daily 09:00",
			DeliveryTarget: schedule.DeliveryTargetOrigin,
			DeliveryPolicy: schedule.DeliveryPolicyAlways,
			Enabled:        true,
		},
	}, false)
	if strings.Contains(subtaskCard, `"value":"creator_dm"`) {
		t.Fatalf("subtask detail card should hide creator_dm option: %s", subtaskCard)
	}
	if !strings.Contains(subtaskCard, `"value":"origin"`) || !strings.Contains(subtaskCard, `"value":"none"`) {
		t.Fatalf("subtask detail card should keep origin and none options: %s", subtaskCard)
	}

	reuseNoneCard := renderScheduleDetail(schedule.Entry{
		Task: schedule.Task{
			ID:             12,
			Name:           "session-followup",
			ScheduleType:   schedule.ScheduleTypeSessionPrompt,
			RunMode:        schedule.RunModeReuseSession,
			Spec:           "every 30m",
			DeliveryTarget: schedule.DeliveryTargetNone,
			Enabled:        true,
		},
	}, false)
	if strings.Contains(reuseNoneCard, `"value":"creator_dm"`) || strings.Contains(reuseNoneCard, `"schedule.delivery_target"`) {
		t.Fatalf("reuse-session detail card with none should hide delivery target selector: %s", reuseNoneCard)
	}

	reuseLegacyDMCard := renderScheduleDetail(schedule.Entry{
		Task: schedule.Task{
			ID:             13,
			Name:           "legacy-dm-followup",
			ScheduleType:   schedule.ScheduleTypeSessionPrompt,
			RunMode:        schedule.RunModeReuseSession,
			Spec:           "every 30m",
			DeliveryTarget: schedule.DeliveryTargetCreatorDM,
			DeliveryPolicy: schedule.DeliveryPolicyOnFailure,
			Enabled:        true,
		},
	}, false)
	if strings.Contains(reuseLegacyDMCard, `"value":"creator_dm"`) {
		t.Fatalf("legacy reuse-session detail card should not offer creator_dm button: %s", reuseLegacyDMCard)
	}
	if !strings.Contains(reuseLegacyDMCard, `"value":"none"`) {
		t.Fatalf("legacy reuse-session detail card should keep none button to disable creator_dm: %s", reuseLegacyDMCard)
	}
}
