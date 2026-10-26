package schedule

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestVisibilityUpgradePreservesHistoryAndNeverReplaysNotices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	due := now.Unix()
	row, err := s.Create(Task{Spec: "every 1m", Kind: KindInterval, ScheduleType: ScheduleTypeSubtask, TaskSpecJson: []byte(`{}`), Enabled: true, NextRunAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordScheduleRun(row.ID, due-60, "legacy-task", "ok:task"); err != nil {
		t.Fatal(err)
	}
	completed := visibilityFire(t, s, row, now, "completed")
	failed := visibilityFire(t, s, row, now.Add(time.Minute), "failed")
	if err = s.RecordScheduleRun(row.ID, failed.SubmittedAt, "", "failed"); err != nil {
		t.Fatal(err)
	}
	// Reopen a pre-upgrade database, with existing tasks and the original journal.
	if _, err = s.db.Exec(`DROP TABLE schedule_visibility_upgrade`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, _ := s.Get(row.ID)
	creator := Creator{PrincipalID: "alice", BotID: "bot", OpenID: "alice-open", Source: "upgrade:frozen-origin", AccessIdentityJSON: `{}`}
	if err = s.RecoverCreator(before, creator); err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverCreator(before, Creator{PrincipalID: "bob", BotID: "bot", OpenID: "bob", Source: "other", AccessIdentityJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Visibility(row.ID)
	if err != nil || v.Creator != creator || v.Latest.Status != "failed" || v.LastCompletedAt != completed.FinishedAt || v.PendingAt != 0 || v.IncidentAt != 0 {
		t.Fatalf("incorrect recovered state: %+v %v", v, err)
	}
	after, _ := s.Get(row.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("recovery changed dispatch state")
	}
	history, err := s.VisibleHistory(row.ID)
	if err != nil || len(history) != 3 || history[2].Status != "legacy_unconfirmed" || history[2].FinishedAt != 0 {
		t.Fatalf("history: %+v %v", history, err)
	}
	if notices := visibilityPrepare(t, s, now.Add(48*time.Hour)); len(notices) != 0 {
		t.Fatal("replayed historical notification")
	}
	counts, err := s.RecentCounts(row.ID, now.Add(time.Hour))
	if err != nil || counts.Completed != 1 || counts.Failed != 1 {
		t.Fatalf("legacy changed counters: %+v %v", counts, err)
	}
	newRow, err := s.Create(Task{Spec: "every 1m", ScheduleType: ScheduleTypeSubtask, TaskSpecJson: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverCreator(newRow, creator); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Visibility(newRow.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("new schedule included in upgrade")
	}
	if err = s.migrateVisibility(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.UnboundLegacy()
	if err != nil || len(rows) != 0 {
		t.Fatal("cutoff moved after migration")
	}
}

func TestVisibilityUpgradeRejectsChangedDefinition(t *testing.T) {
	s, row, _ := visibilityFixture(t)
	if _, err := s.db.Exec(`DELETE FROM schedule_visibility; UPDATE schedule_visibility_upgrade SET max_schedule_id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	row.TaskSpecJson = []byte(`{"old":true}`)
	if err := s.RecoverCreator(row, Creator{PrincipalID: "alice", BotID: "bot", OpenID: "alice", Source: "upgrade", AccessIdentityJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Visibility(row.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("bound stale definition")
	}
}
