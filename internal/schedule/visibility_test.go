package schedule

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func visibilityFixture(t *testing.T) (*Store, Task, time.Time) {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.FixedZone("test", 8*3600))
	due := now.Unix()
	row, e := s.Create(Task{Name: "private", TargetID: "runtime", SessionName: "session", Spec: "every 1m", Kind: KindInterval, Enabled: true, NextRunAt: &due})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.BindCreator(row.ID, Creator{PrincipalID: "alice", BotID: "bot", OpenID: "alice-open", Source: "turn:original"}); e != nil {
		t.Fatal(e)
	}
	return s, row, now
}
func visibilityFire(t *testing.T, s *Store, row Task, at time.Time, status string) Execution {
	t.Helper()
	fresh, e := s.Get(row.ID)
	if e != nil {
		t.Fatal(e)
	}
	next := at.Add(time.Minute).Unix()
	r, ok, e := s.ClaimTracked(fresh, at, &next)
	if e != nil || !ok {
		t.Fatalf("claim: %v %v", ok, e)
	}
	r.Status = status
	r.Terminal = status != "running" && status != "unknown"
	if r.Terminal {
		r.FinishedAt = at.Add(time.Second).Unix()
	}
	if e = s.Observe(r, at); e != nil {
		t.Fatal(e)
	}
	return r
}
func visibilityPrepare(t *testing.T, s *Store, at time.Time) []Notice {
	t.Helper()
	es, e := s.Personal("bot", "alice-open")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.PrepareNotices("bot", "admin-open", es, at); e != nil {
		t.Fatal(e)
	}
	out, e := s.Outbox("bot")
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestVisibilityRecurringSuccessIsQuietAndLateSuccessCannotClearFailure(t *testing.T) {
	s, row, now := visibilityFixture(t)
	for i := 0; i < 100; i++ {
		visibilityFire(t, s, row, now.Add(time.Duration(i)*time.Minute), "completed")
	}
	if n := visibilityPrepare(t, s, now.Add(101*time.Minute)); len(n) != 0 {
		t.Fatalf("healthy spam: %+v", n)
	}
	old := visibilityFire(t, s, row, now.Add(102*time.Minute), "running")
	visibilityFire(t, s, row, now.Add(103*time.Minute), "failed")
	old.Status = "completed"
	old.Terminal = true
	old.FinishedAt = now.Add(104 * time.Minute).Unix()
	if e := s.Observe(old, now.Add(104*time.Minute)); e != nil {
		t.Fatal(e)
	}
	v, _ := s.Visibility(row.ID)
	if v.Latest.Status != "failed" || v.IncidentCount != 1 {
		t.Fatalf("late result overwrote incident: %+v", v)
	}
	runs, _ := s.ExecutionHistory(row.ID)
	if len(runs) != 50 {
		t.Fatalf("history count %d", len(runs))
	}
}
func TestVisibilityIncidentUpdatesRecoveryAndNewIncident(t *testing.T) {
	s, row, now := visibilityFixture(t)
	visibilityFire(t, s, row, now, "failed")
	if n := visibilityPrepare(t, s, now.Add(59*time.Second)); len(n) != 0 {
		t.Fatal("aggregation bypassed")
	}
	n := visibilityPrepare(t, s, now.Add(time.Minute))
	if len(n) != 1 {
		t.Fatalf("notices %d", len(n))
	}
	if e := s.NoticeResult(n[0], "card", 0, nil, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	for i := 1; i < 20; i++ {
		visibilityFire(t, s, row, now.Add(time.Duration(i)*time.Minute), "failed")
	}
	n = visibilityPrepare(t, s, now.Add(21*time.Minute))
	if len(n) != 1 || n[0].MessageID != "card" {
		t.Fatalf("repeated failure did not patch: %+v", n)
	}
	if e := s.NoticeResult(n[0], "card", 0, nil, now.Add(21*time.Minute)); e != nil {
		t.Fatal(e)
	}
	visibilityFire(t, s, row, now.Add(22*time.Minute), "completed")
	if n := visibilityPrepare(t, s, now.Add(31*time.Minute)); len(n) != 0 {
		t.Fatal("early recovery")
	}
	n = visibilityPrepare(t, s, now.Add(32*time.Minute))
	if len(n) != 1 || n[0].MessageID != "card" {
		t.Fatalf("recovery: %+v", n)
	}
	if e := s.NoticeResult(n[0], "card", 0, nil, now.Add(32*time.Minute)); e != nil {
		t.Fatal(e)
	}
	visibilityFire(t, s, row, now.Add(33*time.Minute), "failed")
	n = visibilityPrepare(t, s, now.Add(34*time.Minute))
	if len(n) != 1 || n[0].MessageID != "" {
		t.Fatalf("new incident suppressed: %+v", n)
	}
}
func TestVisibilityBlockedDeliverySurvivesRestartAndRequiresRecheck(t *testing.T) {
	s, row, now := visibilityFixture(t)
	visibilityFire(t, s, row, now, "failed")
	n := visibilityPrepare(t, s, now.Add(time.Minute))[0]
	if e := s.NoticeResult(n, "", 230013, errors.New("blocked"), now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	var dbPath string
	if e := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbPath); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := OpenStore(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	s = reopened
	if out := visibilityPrepare(t, s, now.Add(48*time.Hour)); len(out) != 0 {
		t.Fatal("blocked delivery retried")
	}
	v, _ := s.Visibility(row.ID)
	if !v.Blocked || v.Latest.Status != "failed" {
		t.Fatalf("state: %+v", v)
	}
	if e := s.ChangeVisibility(row.ID, "recheck", now.Add(48*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if out := visibilityPrepare(t, s, now.Add(48*time.Hour)); len(out) != 1 {
		t.Fatal("explicit recheck did not retry")
	}
}
func TestVisibilityQuietUnknownAckAndDurableRetry(t *testing.T) {
	s, row, now := visibilityFixture(t)
	visibilityFire(t, s, row, now, "unknown")
	if n := visibilityPrepare(t, s, now.Add(4*time.Minute)); len(n) != 0 {
		t.Fatal("unknown alerted too soon")
	}
	n := visibilityPrepare(t, s, now.Add(5*time.Minute))[0]
	if e := s.NoticeResult(n, "", 0, errors.New("network"), now.Add(5*time.Minute)); e != nil {
		t.Fatal(e)
	}
	retry, _ := s.Outbox("bot")
	if len(retry) != 1 || retry[0].ID != n.ID || retry[0].RetryAt <= now.Add(5*time.Minute).Unix() {
		t.Fatalf("unstable retry: %+v", retry)
	}
	if e := s.NoticeResult(retry[0], "card", 0, nil, now.Add(8*time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e := s.ChangeVisibility(row.ID, "ack", now); e != nil {
		t.Fatal(e)
	}
	if n := visibilityPrepare(t, s, now.Add(25*time.Hour)); len(n) != 0 {
		t.Fatal("ack did not suppress reminder")
	}
	s2, row2, night := visibilityFixture(t)
	night = night.Add(13 * time.Hour)
	visibilityFire(t, s2, row2, night, "failed")
	if n := visibilityPrepare(t, s2, night.Add(time.Minute)); len(n) != 0 {
		t.Fatal("quiet hours ignored")
	}
	if n := visibilityPrepare(t, s2, night.Add(10*time.Hour)); len(n) != 1 {
		t.Fatal("overnight summary missing")
	}
}
func TestVisibilityClaimCrashAndRecipientsRemainIndependent(t *testing.T) {
	s, row, now := visibilityFixture(t)
	next := now.Add(time.Minute).Unix()
	r, ok, e := s.ClaimTracked(row, now, &next)
	if e != nil || !ok {
		t.Fatal(e)
	}
	if _, again, e := s.ClaimTracked(row, now, &next); e != nil || again {
		t.Fatal("double dispatch")
	}
	pending, _ := s.PendingExecutions()
	if len(pending) != 1 || pending[0].ID != r.ID {
		t.Fatal("claim not durable")
	}
	bob, e := s.Personal("bot", "bob-open")
	if e != nil || len(bob) != 0 {
		t.Fatal("recipient leak")
	}
	other, _ := s.Personal("other-bot", "alice-open")
	if len(other) != 0 {
		t.Fatal("bot scope leak")
	}
	if e = s.BindCreator(row.ID, Creator{PrincipalID: "bob", BotID: "bot", OpenID: "bob-open", Source: "turn:later"}); e == nil {
		t.Fatal("creator changed")
	}
}

func TestVisibilityRecoveryStabilityDailyReminderAndBudget(t *testing.T) {
	s, row, now := visibilityFixture(t)
	visibilityFire(t, s, row, now, "failed")
	n := visibilityPrepare(t, s, now.Add(time.Minute))[0]
	if e := s.NoticeResult(n, "card", 0, nil, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	for i := 2; i < 9; i++ {
		visibilityFire(t, s, row, now.Add(time.Duration(i)*time.Minute), "completed")
	}
	out := visibilityPrepare(t, s, now.Add(12*time.Minute))
	if len(out) != 1 {
		t.Fatal("healthy recurring runs kept postponing recovery")
	}
	if e := s.NoticeResult(out[0], "card", 0, nil, now.Add(12*time.Minute)); e != nil {
		t.Fatal(e)
	}
	visibilityFire(t, s, row, now.Add(13*time.Minute), "failed")
	n = visibilityPrepare(t, s, now.Add(14*time.Minute))[0]
	if e := s.NoticeResult(n, "new-card", 0, nil, now.Add(14*time.Minute)); e != nil {
		t.Fatal(e)
	}
	visibilityFire(t, s, row, now.Add(24*time.Hour), "failed")
	n = visibilityPrepare(t, s, now.Add(24*time.Hour+time.Minute))[0]
	if n.MessageID != "new-card" {
		t.Fatal("early daily reminder")
	}
	if e := s.NoticeResult(n, "new-card", 0, nil, now.Add(24*time.Hour+time.Minute)); e != nil {
		t.Fatal(e)
	}
	out = visibilityPrepare(t, s, now.Add(24*time.Hour+14*time.Minute))
	if len(out) != 1 || out[0].MessageID != "" {
		t.Fatal("card updates postponed daily reminder")
	}
	// Persisted budget is checked immediately before new sends, including retries.
	for i := 0; i < 3; i++ {
		if _, e := s.db.Exec(`INSERT INTO schedule_notice_budget VALUES('bot','alice-open',?)`, now.Unix()); e != nil {
			t.Fatal(e)
		}
	}
	if allowed, e := s.CanSendNotice("bot", "alice-open", now.Add(time.Minute)); e != nil || allowed {
		t.Fatal("recipient budget bypassed")
	}
	if allowed, e := s.CanSendNotice("bot", "bob-open", now.Add(time.Minute)); e != nil || !allowed {
		t.Fatal("cross-recipient throttling")
	}
}

func TestVisibilityCreatedMessagePersistsWhenCurrentStatePatchFails(t *testing.T) {
	s, row, now := visibilityFixture(t)
	visibilityFire(t, s, row, now, "failed")
	n := visibilityPrepare(t, s, now.Add(time.Minute))[0]
	if e := s.NoticeResult(n, "known-message", 0, errors.New("patch network error"), now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	out, e := s.Outbox("bot")
	if e != nil || len(out) != 1 || out[0].MessageID != "known-message" {
		t.Fatalf("known message lost: %+v %v", out, e)
	}
	v, _ := s.Visibility(row.ID)
	if v.MessageID != "known-message" || v.PendingAt == 0 {
		t.Fatal("state update incorrectly acknowledged")
	}
	var count int
	if e = s.db.QueryRow(`SELECT COUNT(*) FROM schedule_notice_budget`).Scan(&count); e != nil || count != 1 {
		t.Fatal("created message omitted from budget")
	}
}

func TestVisibilityLateFailureRemainsVisibleWhileNewerRunIsPending(t *testing.T) {
	s, row, now := visibilityFixture(t)
	older := visibilityFire(t, s, row, now, "running")
	newer := visibilityFire(t, s, row, now.Add(time.Minute), "running")
	older.Status = "unknown"
	if e := s.Observe(older, now.Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e := s.Observe(newer, now.Add(3*time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e := s.Observe(older, now.Add(4*time.Minute)); e != nil {
		t.Fatal(e)
	}
	v, _ := s.Visibility(row.ID)
	if v.Latest.ID != newer.ID || v.IncidentCount != 1 {
		t.Fatalf("overlapping observation hidden or double-counted: %+v", v)
	}
	older.Status = "failed"
	older.Terminal = true
	older.FinishedAt = now.Add(5 * time.Minute).Unix()
	if e := s.Observe(older, now.Add(5*time.Minute)); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Visibility(row.ID)
	if v.Latest.ID != newer.ID || v.IncidentCount != 1 || v.IncidentStatus != "failed" {
		t.Fatalf("late failure lost: %+v", v)
	}
	if n := visibilityPrepare(t, s, now.Add(6*time.Minute)); len(n) != 1 {
		t.Fatal("newer running turn suppressed older failure alert")
	}
}
func TestVisibilityOlderFailureCannotReopenAfterNewerCompletion(t *testing.T) {
	s, row, now := visibilityFixture(t)
	older := visibilityFire(t, s, row, now, "running")
	visibilityFire(t, s, row, now.Add(time.Minute), "completed")
	latest := visibilityFire(t, s, row, now.Add(2*time.Minute), "running")
	older.Status = "failed"
	older.Terminal = true
	if e := s.Observe(older, now.Add(3*time.Minute)); e != nil {
		t.Fatal(e)
	}
	v, _ := s.Visibility(row.ID)
	if v.Latest.ID != latest.ID || v.IncidentAt != 0 {
		t.Fatalf("stale failure reopened recovered execution: %+v", v)
	}
}

func TestVisibilityAdministratorMigrationRetiresCreatorDelivery(t *testing.T) {
	s, row, now := visibilityFixture(t)
	visibilityFire(t, s, row, now, "failed")
	v, _ := s.Visibility(row.ID)
	v.MessageID, v.DeliveryError, v.Blocked = "creator-card", "230013", true
	v.Acknowledged, v.SnoozeUntil = true, now.Add(24*time.Hour).Unix()
	if err := s.saveVisibility(row.ID, v); err != nil {
		t.Fatal(err)
	}
	legacy := Notice{ID: "creator-request", BotID: "bot", OpenID: "alice-open", MessageID: "creator-card", ScheduleIDs: []int64{row.ID}}
	if err := s.saveNotice(legacy); err != nil {
		t.Fatal(err)
	}
	n := visibilityPrepare(t, s, now.Add(time.Minute))
	if len(n) != 1 || n[0].OpenID != "admin-open" || n[0].ID == legacy.ID || n[0].MessageID != "" || n[0].Route == "" {
		t.Fatalf("old delivery reused: %+v", n)
	}
	v, _ = s.Visibility(row.ID)
	if v.Creator.OpenID != "alice-open" || v.Latest.Status != "failed" || v.Blocked || v.Acknowledged || v.SnoozeUntil != 0 {
		t.Fatalf("migration lost ownership/execution or retained recipient state: %+v", v)
	}
	if err := s.NoticeResult(n[0], "admin-card", 0, nil, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.Personal("bot", "alice-open")
	if err := s.PrepareNotices("bot", "new-admin", entries, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	next, _ := s.Outbox("bot")
	if len(next) != 1 || next[0].OpenID != "new-admin" || next[0].MessageID != "" || next[0].Route == n[0].Route {
		t.Fatalf("admin change reused old card: %+v", next)
	}
	if err := s.NoticeResult(n[0], "late-old-card", 0, nil, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Visibility(row.ID)
	if v.MessageID != "" || v.PendingAt == 0 {
		t.Fatal("late old receipt corrupted new delivery")
	}
	if err := s.NoticeResult(next[0], "new-admin-card", 0, nil, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNoticeRecipient("bot", "", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Visibility(row.ID)
	if v.PendingAt == 0 || v.DeliveryError != "missing_admin" || v.MessageID != "" {
		t.Fatal("missing administrator lost pending notice")
	}
	if out, _ := s.Outbox("bot"); len(out) != 0 {
		t.Fatal("old recipient queued without administrator")
	}
}

func TestVisibilityAdministratorAggregationBudgetAndBotIsolation(t *testing.T) {
	s, row, now := visibilityFixture(t)
	visibilityFire(t, s, row, now, "failed")
	entries, _ := s.Personal("bot", "alice-open")
	// More than one card must not silently mark omitted tasks as delivered.
	for i := 0; i < 6; i++ {
		due := now.Unix()
		other, err := s.Create(Task{Name: "another creator", Spec: "every 1m", Kind: KindInterval, Enabled: true, NextRunAt: &due})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.BindCreator(other.ID, Creator{PrincipalID: "bob", BotID: "bot", OpenID: "bob-open", Source: "turn:bob"}); err != nil {
			t.Fatal(err)
		}
		visibilityFire(t, s, other, now, "failed")
	}
	bob, _ := s.Personal("bot", "bob-open")
	entries = append(entries, bob...)
	if err := s.PrepareNotices("bot", "admin", entries, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	queued, _ := s.Outbox("bot")
	seen := map[int64]bool{}
	for _, n := range queued {
		if n.OpenID != "admin" || len(n.ScheduleIDs) > 5 {
			t.Fatalf("wrong aggregation: %+v", n)
		}
		for _, id := range n.ScheduleIDs {
			if seen[id] {
				t.Fatal("duplicate task")
			}
			seen[id] = true
		}
	}
	if len(seen) != 7 || len(queued) != 2 {
		t.Fatalf("lost tasks: %v", queued)
	}
	if err := s.PrepareNotices("other-bot", "other-admin", entries, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Outbox("other-bot"); len(out) != 0 {
		t.Fatal("cross-Bot delivery")
	}
	for i := 0; i < 3; i++ {
		if _, err := s.db.Exec(`INSERT INTO schedule_notice_budget VALUES('bot','admin',?)`, now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if allowed, err := s.CanSendNotice("bot", "admin", now.Add(time.Minute)); err != nil || allowed {
		t.Fatal("administrator budget bypassed")
	}
	if allowed, err := s.CanSendNotice("other-bot", "admin", now.Add(time.Minute)); err != nil || !allowed {
		t.Fatal("Bot budgets mixed")
	}
	personal, _ := s.Personal("bot", "admin")
	if len(personal) != 0 {
		t.Fatal("delivery recipient became task owner")
	}
}
