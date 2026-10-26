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
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/schedule"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestScheduleCardsBilingualPaginationAndUntrustedNames(t *testing.T) {
	entries := make([]schedule.Entry, 11)
	for i := range entries {
		entries[i] = schedule.Entry{Task: schedule.Task{ID: int64(i + 1), Name: fmt.Sprintf("name-%02d", i), Enabled: true}, Visibility: schedule.Visibility{Latest: schedule.Execution{Status: "failed"}, IncidentAt: 1, IncidentCount: 1}}
	}
	card := renderSchedules(entries, "all", 0)
	if strings.Contains(card, "name-10") {
		t.Fatal("pagination leaked next page")
	}
	if next := renderSchedules(entries, "all", 1); !strings.Contains(next, "name-10") || strings.Contains(next, "name-00") {
		t.Fatal("wrong next page")
	}
	entry := entries[0]
	entry.Task.Name = "<at id=all>literal</at>"
	entry.Visibility.Blocked = true
	entry.Visibility.DeliveryError = "230013"
	missingAdmin := entry
	missingAdmin.Visibility.Blocked = false
	missingAdmin.Visibility.DeliveryError = "missing_admin"
	cards := []string{renderScheduleDetail(missingAdmin, false), renderScheduleAlerts([]schedule.Entry{missingAdmin}, 0), card, renderSchedules(nil, "all", 0), renderScheduleDetail(entry, false), renderScheduleDetail(entry, true), renderScheduleNotice(entries[:2]), renderScheduleAlerts(entries, 0), renderScheduleAlerts(nil, 0)}
	for _, card := range cards {
		if len(card) >= maxFeishuCardBytes {
			t.Fatal("oversized card")
		}
		for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
			selected := selectManagementCardLocale(card, locale)
			var doc any
			if e := json.Unmarshal([]byte(selected), &doc); e != nil {
				t.Fatal(e)
			}
			names := map[string]bool{}
			var checkButtons func(any)
			checkButtons = func(value any) {
				switch v := value.(type) {
				case map[string]any:
					if v["tag"] == "button" {
						name, _ := v["name"].(string)
						if name == "" || names[name] {
							t.Fatalf("duplicate/empty native button name: %q", name)
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
					t.Fatalf("unresolved copy: %s", text)
				}
			})
		}
	}
	var doc map[string]any
	json.Unmarshal([]byte(selectManagementCardLocale(renderScheduleDetail(entry, false), i18n.English)), &doc)
	if !strings.Contains(fmt.Sprint(doc), "plain_text") {
		t.Fatal("untrusted names not plain text")
	}
}

type scheduleTestControl struct{ store *schedule.Store }

func (c scheduleTestControl) Store() *schedule.Store { return c.store }
func (c scheduleTestControl) Creators(string) ([]string, error) {
	return []string{"alice", "bob"}, nil
}
func (c scheduleTestControl) List(_ context.Context, bot, open string) ([]schedule.Entry, error) {
	return c.store.Personal(bot, open)
}
func (c scheduleTestControl) Get(_ context.Context, bot, open string, id int64) (schedule.Entry, error) {
	v, e := c.store.Visibility(id)
	if e != nil || v.Creator.BotID != bot || v.Creator.OpenID != open {
		return schedule.Entry{}, errors.New("denied")
	}
	row, e := c.store.Get(id)
	return schedule.Entry{Task: row, Visibility: v}, e
}
func (c scheduleTestControl) Act(ctx context.Context, bot, open string, id int64, op string) error {
	if _, e := c.Get(ctx, bot, open, id); e != nil {
		return e
	}
	return c.store.ChangeVisibility(id, op, time.Now())
}
func TestScheduleDeliveryUsesBotAdministratorStableUUIDAndClassifiesBlocked(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		if r.URL.Path != "/open-apis/im/v1/messages" {
			t.Errorf("unexpected API: %s", r.URL.Path)
		}
		if r.URL.Query().Get("receive_id_type") != "open_id" {
			t.Error("wrong recipient type")
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		fmt.Fprint(w, `{"code":230013,"msg":"Bot has NO availability to this user."}`)
	}))
	defer server.Close()
	store, e := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	bot := BridgeIdFromAppId("app-test")
	due := now.Unix()
	row, e := store.Create(schedule.Task{Name: "failure", Spec: "every 1m", Kind: schedule.KindInterval, Enabled: true, NextRunAt: &due})
	if e != nil {
		t.Fatal(e)
	}
	if e = store.BindCreator(row.ID, schedule.Creator{PrincipalID: "creator", BotID: bot, OpenID: "alice", Source: "turn:alice"}); e != nil {
		t.Fatal(e)
	}
	r, ok, e := store.ClaimTracked(row, now, nil)
	if e != nil || !ok {
		t.Fatal(e)
	}
	r.Status = "failed"
	r.Terminal = true
	if e = store.Observe(r, now); e != nil {
		t.Fatal(e)
	}
	b := &Bridge{secrets: &Secrets{AppId: "app-test", OpenId: "bot-owner"}, schedules: scheduleTestControl{store}, client: lark.NewClient("app-test", "secret-test", lark.WithOpenBaseUrl(server.URL))}
	if e = b.deliverScheduleNotices(context.Background(), now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if len(bodies) != 1 || bodies[0]["receive_id"] != "bot-owner" || bodies[0]["uuid"] == "" {
		t.Fatalf("wrong delivery: %+v", bodies)
	}
	if e = b.deliverScheduleNotices(context.Background(), now.Add(24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(bodies) != 1 {
		t.Fatal("blocked recipient retried")
	}
	v, _ := store.Visibility(row.ID)
	if !v.Blocked || v.Latest.Status != "failed" || v.MessageID != "" {
		t.Fatalf("delivery changed execution: %+v", v)
	}
}

func TestScheduleForwardedCallbacksDoNotExposeOrChangeAnotherCreator(t *testing.T) {
	store, e := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	row, e := store.Create(schedule.Task{Name: "alice-private", Spec: "every 1m", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	bot := BridgeIdFromAppId("test-app")
	if e = store.BindCreator(row.ID, schedule.Creator{PrincipalID: "alice-id", BotID: bot, OpenID: "alice", Source: "turn:alice"}); e != nil {
		t.Fatal(e)
	}
	sent := 0
	b := &Bridge{secrets: &Secrets{AppId: "test-app", OpenId: "owner"}, schedules: scheduleTestControl{store}}
	b.dmInteractive = func(_ context.Context, open, card string) (string, string, error) {
		sent++
		if open != "bob" || strings.Contains(card, "alice-private") {
			t.Error("private content leaked")
		}
		return "private-bob", "", nil
	}
	for _, op := range []string{"detail", "result", "pause", "resume", "off", "on", "ack", "snooze", "recheck"} {
		response, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "bob"}, Context: &callback.Context{OpenMessageID: "forwarded"}, Action: &callback.CallBackAction{Value: map[string]interface{}{managementActionKey: "schedule." + op, "schedule_id": fmt.Sprint(row.ID)}}}})
		if err != nil || !handled || response.Toast.Type != "error" {
			t.Fatalf("%s not refused", op)
		}
	}
	if sent != 0 {
		t.Fatal("forged callback sent a message")
	}
	_, _, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "bob"}, Context: &callback.Context{OpenMessageID: "forwarded"}, Action: &callback.CallBackAction{Value: map[string]interface{}{managementActionKey: "schedule.list"}}}})
	if err != nil || sent != 1 {
		t.Fatal("personal list did not route privately")
	}
}

func TestScheduleCardsBoundUserContentAndHistory(t *testing.T) {
	entries := make([]schedule.Entry, 10)
	for i := range entries {
		entries[i] = schedule.Entry{Task: schedule.Task{ID: int64(i), Name: strings.Repeat("很长的任务名称", 100), Spec: "every 1m"}, Visibility: schedule.Visibility{Latest: schedule.Execution{Status: "failed"}}}
	}
	e := entries[0]
	for i := 0; i < 50; i++ {
		e.Runs = append(e.Runs, schedule.Execution{ID: int64(i), Status: "completed", Result: strings.Repeat("结果", 2000), TaskID: strings.Repeat("t", 40), SessionName: strings.Repeat("s", 128), TurnID: strings.Repeat("i", 36)})
	}
	for name, card := range map[string]string{"list": renderSchedules(entries, "all", 0), "history": renderScheduleDetail(e, true), "notice": renderScheduleNotice(entries[:5]), "alerts": renderScheduleAlerts(entries, 0)} {
		if len(card) >= maxFeishuCardBytes {
			t.Errorf("%s card too large: %d", name, len(card))
		}
	}
}

func TestScheduleDeduplicatedCreatePatchesCurrentState(t *testing.T) {
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		if r.Method == http.MethodPatch {
			patches++
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["content"] != "current recovery" {
				t.Error("patched stale state")
			}
			fmt.Fprint(w, `{"code":0}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"message_id":"deduplicated-original"}}`)
	}))
	defer server.Close()
	b := &Bridge{client: lark.NewClient("test", "test", lark.WithOpenBaseUrl(server.URL))}
	id, code, err := b.sendScheduleNotice(context.Background(), schedule.Notice{ID: "stable-uuid", OpenID: "creator"}, "current recovery")
	if err != nil || code != 0 || id != "deduplicated-original" || patches != 1 {
		t.Fatalf("dedup left old content: %s %d %v patches=%d", id, code, err, patches)
	}
}

func TestScheduleAdministratorGetsAggregatedStatusWithoutCreatorAuthority(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := BridgeIdFromAppId("admin-aggregation-test")
	var ids []int64
	for _, open := range []string{"alice", "bob"} {
		due := now.Unix()
		row, err := store.Create(schedule.Task{Name: open + " job", Prompt: "private prompt", Spec: "every 1m", Kind: schedule.KindInterval, Enabled: true, NextRunAt: &due})
		if err != nil {
			t.Fatal(err)
		}
		if err = store.BindCreator(row.ID, schedule.Creator{PrincipalID: open, BotID: bot, OpenID: open, Source: "turn:" + open}); err != nil {
			t.Fatal(err)
		}
		run, ok, err := store.ClaimTracked(row, now, nil)
		if err != nil || !ok {
			t.Fatal(err)
		}
		run.Status, run.Terminal, run.Result, run.Reason = "failed", true, "private result", "private diagnostic"
		if err = store.Observe(run, now); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.ID)
	}
	creates, patches := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		card, _ := body["content"].(string)
		if !strings.Contains(card, "alice") || !strings.Contains(card, "bob") || strings.Contains(card, "private ") || strings.Contains(card, "schedule.detail") || strings.Contains(card, "schedule.pause") {
			t.Errorf("wrong administrator contents: %s", card)
		}
		if r.Method == http.MethodPatch {
			patches++
			fmt.Fprint(w, `{"code":0}`)
			return
		}
		creates++
		if body["receive_id"] != "administrator" {
			t.Error("sent to creator")
		}
		fmt.Fprint(w, `{"code":0,"data":{"message_id":"admin-card"}}`)
	}))
	defer server.Close()
	b := &Bridge{secrets: &Secrets{AppId: "admin-aggregation-test"}, schedules: scheduleTestControl{store}, client: lark.NewClient("admin-aggregation-test", "test", lark.WithOpenBaseUrl(server.URL))}
	if err = b.deliverScheduleNotices(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if creates != 0 {
		t.Fatal("missing administrator fell back to creator")
	}
	for _, id := range ids {
		v, _ := store.Visibility(id)
		if v.DeliveryError != "missing_admin" || v.PendingAt == 0 {
			t.Fatal("pending delivery missing")
		}
	}
	b.secrets.OpenId = "administrator"
	if err = b.deliverScheduleNotices(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || patches != 1 {
		t.Fatalf("not aggregated: create=%d patch=%d", creates, patches)
	}
	for _, id := range ids {
		if _, err = b.schedules.Get(context.Background(), bot, "administrator", id); err == nil {
			t.Fatal("recipient acquired creator access")
		}
	}
	sent := 0
	b.dmInteractive = func(_ context.Context, open, card string) (string, string, error) {
		sent++
		if open != "administrator" || strings.Contains(card, "private ") {
			t.Error("admin view leaked content or recipient")
		}
		return "admin-view", "", nil
	}
	callbackFor := func(open, action string) *callback.CardActionTriggerEvent {
		return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: open}, Context: &callback.Context{OpenMessageID: "forwarded"}, Action: &callback.CallBackAction{Value: map[string]interface{}{managementActionKey: "schedule." + action, "schedule_id": fmt.Sprint(ids[0])}}}}
	}
	for _, action := range []string{"alerts", "alert_ack", "alert_snooze", "alert_recheck"} {
		resp, _, err := b.handleManagementCardAction(context.Background(), callbackFor("bob", action))
		if err != nil || resp.Toast.Type != "error" {
			t.Fatalf("non-admin action allowed: %s", action)
		}
	}
	for _, action := range []string{"detail", "result", "pause", "resume", "alert_pause", "alert_result", "alert_off"} {
		resp, _, err := b.handleManagementCardAction(context.Background(), callbackFor("administrator", action))
		if err != nil || resp.Toast.Type != "error" {
			t.Fatalf("admin acquired task authority: %s", action)
		}
	}
	for _, action := range []string{"alerts", "alert_ack", "alert_snooze", "alert_recheck"} {
		resp, _, err := b.handleManagementCardAction(context.Background(), callbackFor("administrator", action))
		if err != nil || resp.Toast.Type != "success" {
			t.Fatalf("admin action failed: %s %v", action, err)
		}
	}
	v, _ := store.Visibility(ids[0])
	if !v.Acknowledged || v.SnoozeUntil == 0 || sent != 4 {
		t.Fatal("administrator notification controls ineffective")
	}
	row, _ := store.Get(ids[0])
	if !row.Enabled {
		t.Fatal("notification control changed execution")
	}
	b.secrets.OpenId = "replacement-admin"
	resp, _, err := b.handleManagementCardAction(context.Background(), callbackFor("administrator", "alert_recheck"))
	if err != nil || resp.Toast.Type != "error" {
		t.Fatal("old administrator retained access")
	}
}
