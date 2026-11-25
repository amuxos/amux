package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestRenderScheduleResultDeliveryCardBilingualAndBounded(t *testing.T) {
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
}

func (c deliveryAuthScheduleControl) List(ctx context.Context, bot, open string) ([]schedule.Entry, error) {
	return c.store.Personal(bot, open)
}
func (c deliveryAuthScheduleControl) Get(ctx context.Context, bot, open string, id int64) (schedule.Entry, error) {
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
func (c deliveryAuthScheduleControl) Act(ctx context.Context, bot, open string, id int64, action string) error {
	return nil
}
func (c deliveryAuthScheduleControl) Creators(bot string) ([]string, error) {
	return nil, nil
}
func (c deliveryAuthScheduleControl) Store() *schedule.Store {
	return c.store
}

func TestDeliverScheduleResultsRoutingFallbackAndAuthRevocation(t *testing.T) {
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

	ctrl := deliveryAuthScheduleControl{store: store, deny: map[string]bool{}}
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
		if err := store.BindCreator(tsk.ID, schedule.Creator{
			BotID:           "bot-1",
			OpenID:          openID,
			PrincipalID:     "p_" + openID,
			Source:          "turn:" + openID,
			OriginRootMsgID: rootMsg,
			OriginChatID:    chatID,
		}); err != nil {
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

	// 3) creator_dm -> sends directly to open_id.
	s3, _ := enqueueTerminal("dm-job", "ou_bob", schedule.DeliveryTargetCreatorDM, "om_root_3", "oc_chat_3", "DM summary")
	b.deliverScheduleResults(context.Background(), deliverNow)
	mu.Lock()
	if len(calls) != 1 || calls[0].ReceiveIDType != "open_id" || calls[0].Body["receive_id"] != "ou_bob" {
		t.Fatalf("case 3 DM calls = %+v", calls)
	}
	calls = nil
	mu.Unlock()
	if hist, _ := store.ExecutionHistory(s3); len(hist) != 1 || hist[0].DeliveryStatus != schedule.DeliveryStatusDelivered {
		t.Fatalf("case 3 status = %+v", hist)
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
