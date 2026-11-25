package schedule

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Creator is trusted provenance frozen by the control plane, never request data.
type Creator struct{ PrincipalID, BotID, OpenID, Source, AccessIdentityJSON string }
type Execution struct {
	AlertCounted                                                bool
	ID                                                          int64
	ScheduleID                                                  int64
	ScheduledAt, SubmittedAt, StartedAt, FinishedAt, ObservedAt int64
	TargetID, SessionName, SessionID, LaunchID, TurnID, TaskID  string
	Status, Reason, Result, Cursor                              string
	Terminal                                                    bool
	ObserveFailures                                             int
	NextObserveAt                                               int64
}
type Visibility struct {
	LastCompletedScheduledAt                  int64
	UnknownSince                              int64
	RecoveryAt                                int64
	IncidentStatus, IncidentReason            string
	Creator                                   Creator
	Latest                                    Execution
	LastCompletedAt                           int64
	IncidentAt, IncidentCount, IncidentRun    int64
	Acknowledged                              bool
	SnoozeUntil                               int64
	NotificationsOff                          bool
	PendingAt, LastSentAt                     int64
	MessageID, DeliveredStatus, DeliveryError string
	Blocked                                   bool
	Version                                   int64
}
type RecentCounts struct{ Completed, Failed, Missed int }
type Entry struct {
	CreatorName string
	Counts      RecentCounts
	Task        Task
	Visibility  Visibility
	Runs        []Execution
}
type Notice struct {
	ID, BotID, OpenID, Route string
	ScheduleIDs              []int64
	Versions                 map[int64]int64
	MessageID                string
	CreatedAt, RetryAt       int64
	Attempts                 int
}

// visMu guards read/modify/write operations within this store. Network operations
// never hold it; delivery receipts use versions to preserve newer observations.

func (s *Store) migrateVisibility() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schedule_visibility (schedule_id INTEGER PRIMARY KEY REFERENCES scheduled_tasks(id) ON DELETE CASCADE, bot_id TEXT NOT NULL, open_id TEXT NOT NULL, data BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS schedule_visibility_recipient ON schedule_visibility(bot_id,open_id);
 CREATE TABLE IF NOT EXISTS schedule_executions (id INTEGER PRIMARY KEY AUTOINCREMENT, schedule_id INTEGER NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE, scheduled_at INTEGER NOT NULL, terminal INTEGER NOT NULL DEFAULT 0, data BLOB NOT NULL, UNIQUE(schedule_id,scheduled_at));
 CREATE TABLE IF NOT EXISTS schedule_notice_outbox (id TEXT PRIMARY KEY, bot_id TEXT NOT NULL, data BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS schedule_execution_counts (run_id INTEGER PRIMARY KEY, schedule_id INTEGER NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE, finished_at INTEGER NOT NULL, status TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS schedule_execution_counts_by_schedule ON schedule_execution_counts(schedule_id,finished_at);
 CREATE TABLE IF NOT EXISTS schedule_notice_route (bot_id TEXT PRIMARY KEY, open_id TEXT NOT NULL, generation TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS schedule_notice_budget (bot_id TEXT NOT NULL, open_id TEXT NOT NULL, sent_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS schedule_visibility_upgrade (id INTEGER PRIMARY KEY CHECK(id=1), max_schedule_id INTEGER NOT NULL);
 INSERT OR IGNORE INTO schedule_visibility_upgrade SELECT 1,COALESCE(MAX(id),0) FROM scheduled_tasks;`)
	return err
}
func (s *Store) BindCreator(id int64, c Creator) error {
	if c.PrincipalID == "" || c.BotID == "" || c.OpenID == "" || c.Source == "" {
		return errors.New("unverified schedule creator")
	}
	s.visMu.Lock()
	defer s.visMu.Unlock()
	data, err := json.Marshal(Visibility{Creator: c})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO schedule_visibility(schedule_id,bot_id,open_id,data) VALUES(?,?,?,?)`, id, c.BotID, c.OpenID, data)
	return err
}
func (s *Store) Visibility(id int64) (Visibility, error) {
	var b []byte
	err := s.db.QueryRow(`SELECT data FROM schedule_visibility WHERE schedule_id=?`, id).Scan(&b)
	var v Visibility
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	return v, err
}
func (s *Store) saveVisibility(id int64, v Visibility) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`UPDATE schedule_visibility SET data=? WHERE schedule_id=?`, b, id)
	return e
}

// UpdateCreatorAccessIdentity refreshes the frozen access-identity snapshot on
// an existing creator visibility binding (for example after an authorized
// subtask schedule update changes its effective run_as identity). No-op when
// the schedule has no visibility binding.
func (s *Store) UpdateCreatorAccessIdentity(id int64, accessIdentityJSON string) error {
	s.visMu.Lock()
	defer s.visMu.Unlock()
	v, err := s.Visibility(id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	v.Creator.AccessIdentityJSON = accessIdentityJSON
	return s.saveVisibility(id, v)
}
func (s *Store) Personal(bot, open string) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT schedule_id FROM schedule_visibility WHERE bot_id=? AND open_id=? ORDER BY schedule_id DESC`, bot, open)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, id := range ids {
		t, e := s.Get(id)
		if e != nil {
			return nil, e
		}
		v, e := s.Visibility(id)
		if e != nil {
			return nil, e
		}
		counts, e := s.RecentCounts(id, time.Now())
		if e != nil {
			return nil, e
		}
		out = append(out, Entry{Task: t, Visibility: v, Counts: counts})
	}
	return out, nil
}
func (s *Store) ChangeVisibility(id int64, action string, now time.Time) error {
	s.visMu.Lock()
	defer s.visMu.Unlock()
	v, e := s.Visibility(id)
	if e != nil {
		return e
	}
	switch action {
	case "ack":
		v.Acknowledged = true
	case "snooze":
		v.SnoozeUntil = now.Add(24 * time.Hour).Unix()
	case "off":
		v.NotificationsOff = true
	case "on":
		v.NotificationsOff = false
	case "recheck":
		v.Blocked = false
		v.DeliveryError = ""
		v.PendingAt = now.Unix()
	default:
		return errors.New("unknown schedule action")
	}
	v.Version++
	return s.saveVisibility(id, v)
}

// ClaimTracked advances the occurrence and journals it in one transaction. A
// crash after this commit must never replay the submission automatically.
func (s *Store) ClaimTracked(t Task, now time.Time, next *int64) (Execution, bool, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return Execution{}, false, e
	}
	defer tx.Rollback()
	if t.NextRunAt == nil {
		return Execution{}, false, nil
	}
	res, e := tx.Exec(`UPDATE scheduled_tasks SET last_run_at=?,next_run_at=?,last_status='submitting' WHERE id=? AND enabled=1 AND next_run_at=?`, now.Unix(), next, t.ID, *t.NextRunAt)
	if e != nil {
		return Execution{}, false, e
	}
	n, e := res.RowsAffected()
	if e != nil || n == 0 {
		return Execution{}, false, e
	}
	r := Execution{ScheduleID: t.ID, ScheduledAt: *t.NextRunAt, SubmittedAt: now.Unix(), ObservedAt: now.Unix(), Status: "submitting", TargetID: t.TargetID, SessionName: t.SessionName}
	b, _ := json.Marshal(r)
	res, e = tx.Exec(`INSERT INTO schedule_executions(schedule_id,scheduled_at,data) VALUES(?,?,?)`, t.ID, r.ScheduledAt, b)
	if e != nil {
		return Execution{}, false, e
	}
	r.ID, e = res.LastInsertId()
	if e != nil {
		return Execution{}, false, e
	}
	return r, true, tx.Commit()
}
func (s *Store) executions(where string, args ...any) ([]Execution, error) {
	rows, e := s.db.Query(`SELECT id,data FROM schedule_executions `+where, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var id int64
		var b []byte
		if e = rows.Scan(&id, &b); e != nil {
			return nil, e
		}
		var r Execution
		if e = json.Unmarshal(b, &r); e != nil {
			return nil, e
		}
		r.ID = id
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) PendingExecutions() ([]Execution, error) {
	return s.executions(`WHERE terminal=0 ORDER BY id`)
}
func (s *Store) ExecutionHistory(id int64) ([]Execution, error) {
	return s.executions(`WHERE schedule_id=? ORDER BY scheduled_at DESC,id DESC LIMIT 50`, id)
}
func Attention(status string) bool {
	return status == "failed" || status == "timed_out" || status == "missed" || status == "unknown" || status == "partial"
}
func (s *Store) Observe(r Execution, now time.Time) error {
	s.visMu.Lock()
	defer s.visMu.Unlock()
	v, ve := s.Visibility(r.ScheduleID)
	if ve != nil && !errors.Is(ve, sql.ErrNoRows) {
		return ve
	}
	t, e := s.Get(r.ScheduleID)
	if e != nil {
		return e
	}
	// Read the current execution row under visMu so we preserve AlertCounted
	// and never allow a stale observer snapshot without admission identifiers
	// (TaskID/TurnID) to clobber a concurrent delivery receipt.
	var existingRaw []byte
	var existingTerminal bool
	if e = s.db.QueryRow(`SELECT data, terminal FROM schedule_executions WHERE id=?`, r.ID).Scan(&existingRaw, &existingTerminal); e != nil {
		return e
	}
	if existingTerminal {
		return nil
	}
	var existing Execution
	if e = json.Unmarshal(existingRaw, &existing); e != nil {
		return e
	}
	if (existing.TaskID != "" && r.TaskID == "") || (existing.TurnID != "" && r.TurnID == "") {
		return nil
	}
	if r.SessionName == "" {
		r.SessionName = existing.SessionName
	}
	if r.SessionID == "" {
		r.SessionID = existing.SessionID
	}
	if r.LaunchID == "" {
		r.LaunchID = existing.LaunchID
	}
	if r.TargetID == "" {
		r.TargetID = existing.TargetID
	}
	r.AlertCounted = existing.AlertCounted
	r.ObservedAt = now.Unix()
	if ve == nil {
		if r.Status == "completed" {
			if r.FinishedAt > v.LastCompletedAt {
				v.LastCompletedAt = r.FinishedAt
			}
			if r.ScheduledAt > v.LastCompletedScheduledAt {
				v.LastCompletedScheduledAt = r.ScheduledAt
			}
		}
		previousLatest := v.Latest
		lateAttention := r.ScheduledAt < v.Latest.ScheduledAt && !v.Latest.Terminal && Attention(r.Status) && r.ScheduledAt >= v.LastCompletedScheduledAt
		if r.ScheduledAt >= v.Latest.ScheduledAt || lateAttention {
			changed := v.Latest.ID != r.ID || v.Latest.Status != r.Status || v.Latest.Reason != r.Reason
			if r.Status == "unknown" {
				if v.UnknownSince == 0 {
					v.UnknownSince = now.Unix()
				}
			} else if r.Terminal || v.IncidentAt == 0 {
				v.UnknownSince = 0
			}
			v.Latest = r
			if changed {
				v.Version++
				if Attention(r.Status) {
					if v.RecoveryAt > 0 && now.Unix() >= v.RecoveryAt+600 {
						v.IncidentAt = 0
					}
					v.RecoveryAt = 0
					v.IncidentStatus = r.Status
					v.IncidentReason = r.Reason
					if v.IncidentAt == 0 {
						v.IncidentAt = now.Unix()
						v.IncidentCount = 0
						v.Acknowledged = false
						v.MessageID = ""
						v.LastSentAt = 0
					}
					if !r.AlertCounted {
						v.IncidentCount++
						r.AlertCounted = true
					}
					v.IncidentRun = r.ID
					due := now.Add(time.Minute).Unix()
					if v.PendingAt == 0 || v.PendingAt > due {
						v.PendingAt = due
					}
					if r.Status == "unknown" && v.PendingAt < v.UnknownSince+300 {
						v.PendingAt = v.UnknownSince + 300
					}
				} else if r.Status == "completed" && v.IncidentAt != 0 {
					if v.RecoveryAt == 0 {
						v.RecoveryAt = now.Unix()
					}
					v.PendingAt = v.RecoveryAt + 600
				}

				if t.Kind == KindOnce && r.Terminal && v.PendingAt == 0 {
					v.PendingAt = now.Add(time.Minute).Unix()
				}
			}
		}
		if lateAttention {
			v.Latest = previousLatest
		}
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	res, e := tx.Exec(`UPDATE schedule_executions SET data=?,terminal=? WHERE id=? AND terminal=0`, raw, r.Terminal, r.ID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	if ve == nil {
		raw, e = json.Marshal(v)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(`UPDATE schedule_visibility SET data=? WHERE schedule_id=?`, raw, r.ScheduleID); e != nil {
			return e
		}
	}
	// Legacy RPC clients also see authoritative results once they become known,
	// without overwriting a descriptive admission error with generic "unknown".
	if r.Terminal && r.Status != "unknown" {
		if _, e = tx.Exec(`UPDATE schedule_runs SET status=? WHERE schedule_id=? AND fired_at=?`, r.Status, r.ScheduleID, r.SubmittedAt); e != nil {
			return e
		}
	}
	if r.Terminal {
		if _, e = tx.Exec(`INSERT OR IGNORE INTO schedule_execution_counts VALUES(?,?,?,?)`, r.ID, r.ScheduleID, r.FinishedAt, r.Status); e != nil {
			return e
		}
		if _, e = tx.Exec(`DELETE FROM schedule_execution_counts WHERE finished_at<?`, now.Add(-7*24*time.Hour).Unix()); e != nil {
			return e
		}
		if _, e = tx.Exec(`DELETE FROM schedule_executions WHERE schedule_id=? AND terminal=1 AND id NOT IN (SELECT id FROM schedule_executions WHERE schedule_id=? ORDER BY scheduled_at DESC,id DESC LIMIT 50)`, r.ScheduleID, r.ScheduleID); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func (s *Store) Outbox(bot string) ([]Notice, error) {
	rows, e := s.db.Query(`SELECT data FROM schedule_notice_outbox WHERE bot_id=? ORDER BY rowid`, bot)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Notice
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		var n Notice
		if e = json.Unmarshal(b, &n); e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Store) saveNotice(n Notice) error {
	b, e := json.Marshal(n)
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`INSERT INTO schedule_notice_outbox(id,bot_id,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, n.ID, n.BotID, b)
	return e
}

// SetNoticeRecipient retires requests and cards belonging to a previous delivery
// policy/administrator atomically. Creator identity is deliberately untouched.
func (s *Store) SetNoticeRecipient(bot, open string, now time.Time) error {
	s.visMu.Lock()
	defer s.visMu.Unlock()
	_, err := s.setNoticeRecipient(bot, open, now)
	return err
}
func (s *Store) setNoticeRecipient(bot, open string, now time.Time) (string, error) {
	var previous, generation string
	err := s.db.QueryRow(`SELECT open_id,generation FROM schedule_notice_route WHERE bot_id=?`, bot).Scan(&previous, &generation)
	if err == nil && previous == open {
		return generation, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	rows, err := s.db.Query(`SELECT schedule_id,data FROM schedule_visibility WHERE bot_id=?`, bot)
	if err != nil {
		return "", err
	}
	updates := map[int64][]byte{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		var v Visibility
		if err = json.Unmarshal(raw, &v); err != nil {
			break
		}
		v.MessageID, v.DeliveredStatus, v.DeliveryError = "", "", ""
		v.Blocked, v.Acknowledged = false, false
		v.LastSentAt, v.SnoozeUntil = 0, 0
		if v.PendingAt == 0 && v.IncidentAt != 0 {
			v.PendingAt = now.Unix()
		}
		if open == "" {
			v.DeliveryError = "missing_admin"
		}
		v.Version++
		updates[id], err = json.Marshal(v)
		if err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	for id, raw := range updates {
		if _, err = tx.Exec(`UPDATE schedule_visibility SET data=? WHERE schedule_id=?`, raw, id); err != nil {
			return "", err
		}
	}
	if _, err = tx.Exec(`DELETE FROM schedule_notice_outbox WHERE bot_id=?`, bot); err != nil {
		return "", err
	}
	generation = uuid.NewString()
	if _, err = tx.Exec(`INSERT INTO schedule_notice_route VALUES(?,?,?) ON CONFLICT(bot_id) DO UPDATE SET open_id=excluded.open_id,generation=excluded.generation`, bot, open, generation); err != nil {
		return "", err
	}
	return generation, tx.Commit()
}

// PrepareNotices aggregates entries authorized by the caller for one configured
// administrator. Quiet hours use the schedule evaluation timezone.
func (s *Store) PrepareNotices(bot, recipient string, entries []Entry, now time.Time) error {
	s.visMu.Lock()
	defer s.visMu.Unlock()
	route, e := s.setNoticeRecipient(bot, recipient, now)
	if e != nil {
		return e
	}
	pending, e := s.Outbox(bot)
	if e != nil {
		return e
	}
	busy := map[int64]bool{}
	for _, n := range pending {
		for _, id := range n.ScheduleIDs {
			busy[id] = true
		}
	}
	groups := map[string][]*Notice{}
	for _, entry := range entries {
		v, e := s.Visibility(entry.Task.ID)
		if e != nil {
			return e
		}
		if v.Creator.BotID != bot {
			continue
		}
		if recipient == "" {
			v.DeliveryError = "missing_admin"
			if e = s.saveVisibility(entry.Task.ID, v); e != nil {
				return e
			}
			continue
		}
		if busy[entry.Task.ID] || v.Blocked || v.NotificationsOff || v.SnoozeUntil > now.Unix() {
			continue
		}
		if v.PendingAt == 0 && v.IncidentAt != 0 && v.RecoveryAt == 0 && !v.Acknowledged && v.LastSentAt+86400 <= now.Unix() {
			v.PendingAt = now.Unix()
		}
		if v.PendingAt == 0 || v.PendingAt > now.Unix() || (v.RecoveryAt > 0 && v.Latest.Status != "completed") {
			continue
		}
		if now.Hour() >= 22 || now.Hour() < 8 {
			continue
		}
		message := v.MessageID
		if message != "" && v.IncidentAt != 0 && v.RecoveryAt == 0 && !v.Acknowledged && v.LastSentAt+86400 <= now.Unix() {
			message = ""
		}
		if message == "" {
			var count int
			if e = s.db.QueryRow(`SELECT COUNT(*) FROM schedule_notice_budget WHERE bot_id=? AND open_id=? AND sent_at>?`, bot, recipient, now.Add(-30*time.Minute).Unix()).Scan(&count); e != nil {
				return e
			}
			if count >= 3 {
				continue
			}
			// A recurring recovery with no prior delivered alert is included in the
			// overnight summary only when an incident was actually observed.
			if v.Latest.Status == "completed" && v.IncidentAt == 0 && entry.Task.Kind != KindOnce {
				continue
			}
		}
		key := message
		batches := groups[key]
		var n *Notice
		if len(batches) > 0 {
			n = batches[len(batches)-1]
		}
		if n == nil || len(n.ScheduleIDs) >= 5 {
			n = &Notice{ID: uuid.NewString(), BotID: bot, OpenID: recipient, Route: route, MessageID: message, CreatedAt: now.Unix(), RetryAt: now.Unix(), Versions: map[int64]int64{}}
			groups[key] = append(groups[key], n)
		}
		n.ScheduleIDs = append(n.ScheduleIDs, entry.Task.ID)
		n.Versions[entry.Task.ID] = v.Version
	}
	for _, batches := range groups {
		for _, n := range batches {
			if e = s.saveNotice(*n); e != nil {
				return e
			}
		}
	}
	_, e = s.db.Exec(`DELETE FROM schedule_notice_budget WHERE sent_at<?`, now.Add(-24*time.Hour).Unix())
	return e
}
func (s *Store) NoticeResult(n Notice, message string, code int, sendErr error, now time.Time) error {
	s.visMu.Lock()
	defer s.visMu.Unlock()
	var currentRoute, recipient string
	err := s.db.QueryRow(`SELECT generation,open_id FROM schedule_notice_route WHERE bot_id=?`, n.BotID).Scan(&currentRoute, &recipient)
	if err != nil {
		return err
	}
	// A late receipt must never attach an old recipient's card to the new route.
	if n.Route != currentRoute || n.OpenID != recipient || recipient == "" {
		return nil
	}
	blocked := code == 230013 || code == 230053
	created := message != "" && n.MessageID == ""
	updates := map[int64][]byte{}
	for _, id := range n.ScheduleIDs {
		v, e := s.Visibility(id)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		if v.Creator.BotID != n.BotID {
			continue
		}
		if sendErr != nil {
			if created {
				v.MessageID = message
				v.LastSentAt = now.Unix()
			}
			v.DeliveryError = fmt.Sprint(code)
			if code == 0 {
				v.DeliveryError = "transient"
			}
			v.Blocked = blocked
		} else {
			v.MessageID = message
			v.DeliveryError = ""
			v.Blocked = false
			if n.MessageID == "" {
				v.LastSentAt = now.Unix()
			}
			if v.Version == n.Versions[id] {
				v.DeliveredStatus = v.Latest.Status
				v.PendingAt = 0
				if v.Latest.Status == "completed" {
					v.IncidentAt = 0
					v.RecoveryAt = 0
					v.IncidentCount = 0
					v.Acknowledged = false
				}
			}
		}
		raw, e := json.Marshal(v)
		if e != nil {
			return e
		}
		updates[id] = raw
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for id, raw := range updates {
		if _, e = tx.Exec(`UPDATE schedule_visibility SET data=? WHERE schedule_id=?`, raw, id); e != nil {
			return e
		}
	}
	if (sendErr == nil && n.MessageID == "") || created {
		if _, e = tx.Exec(`INSERT INTO schedule_notice_budget VALUES(?,?,?)`, n.BotID, n.OpenID, now.Unix()); e != nil {
			return e
		}
	}
	if sendErr == nil || blocked {
		_, e = tx.Exec(`DELETE FROM schedule_notice_outbox WHERE id=?`, n.ID)
	} else {
		if created {
			n.MessageID = message
		}
		n.Attempts++
		n.RetryAt = now.Add(time.Duration(1<<min(n.Attempts, 8)) * time.Minute).Unix()
		var raw []byte
		raw, e = json.Marshal(n)
		if e == nil {
			_, e = tx.Exec(`UPDATE schedule_notice_outbox SET data=? WHERE id=?`, raw, n.ID)
		}
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}

func (s *Store) DropNotice(id string) error {
	_, e := s.db.Exec(`DELETE FROM schedule_notice_outbox WHERE id=?`, id)
	return e
}

func (s *Store) RecentCounts(id int64, now time.Time) (RecentCounts, error) {
	var c RecentCounts
	err := s.db.QueryRow(`SELECT COALESCE(SUM(status='completed'),0),COALESCE(SUM(status IN ('failed','timed_out','partial')),0),COALESCE(SUM(status='missed'),0) FROM schedule_execution_counts WHERE schedule_id=? AND finished_at>=?`, id, now.Add(-24*time.Hour).Unix()).Scan(&c.Completed, &c.Failed, &c.Missed)
	return c, err
}

func (s *Store) CanSendNotice(bot, open string, now time.Time) (bool, error) {
	if now.Hour() >= 22 || now.Hour() < 8 {
		return false, nil
	}
	var count int
	e := s.db.QueryRow(`SELECT COUNT(*) FROM schedule_notice_budget WHERE bot_id=? AND open_id=? AND sent_at>?`, bot, open, now.Add(-30*time.Minute).Unix()).Scan(&count)
	return count < 3, e
}

// CreatorForTask follows a scheduler-owned Task reference, never user input.
func (s *Store) CreatorForTask(taskID string) (Creator, error) {
	var raw []byte
	err := s.db.QueryRow(`SELECT v.data FROM schedule_executions e JOIN schedule_visibility v ON v.schedule_id=e.schedule_id WHERE json_extract(e.data,'$.TaskID')=? LIMIT 1`, taskID).Scan(&raw)
	if err != nil {
		return Creator{}, err
	}
	var v Visibility
	err = json.Unmarshal(raw, &v)
	return v.Creator, err
}
