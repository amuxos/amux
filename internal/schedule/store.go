package schedule

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/util"

	_ "modernc.org/sqlite"
)

const (
	RunModeReuseSession     = "reuse-session"
	RunModeNewSessionPerRun = "new-session-per-run"
)

const (
	ScheduleTypeSessionPrompt = "session-prompt"
	ScheduleTypeSubtask       = "subtask"
	DefaultRunHistoryLimit    = 50
)

const (
	StateEnabled   = "enabled"
	StatePaused    = "paused"
	StateCompleted = "completed"
)

const (
	TriggerSourceScheduled = "scheduled"
	TriggerSourceManual    = "manual"
)

// NormalizeRunMode validates a schedule execution mode. Empty means the legacy default.
func NormalizeRunMode(mode string) (string, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return RunModeReuseSession, nil
	}
	switch mode {
	case RunModeReuseSession, RunModeNewSessionPerRun:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid schedule run mode %q", mode)
	}
}

// NormalizeScheduleType validates a schedule type. Empty means the legacy default.
func NormalizeScheduleType(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return ScheduleTypeSessionPrompt, nil
	}
	switch v {
	case ScheduleTypeSessionPrompt, ScheduleTypeSubtask:
		return v, nil
	default:
		return "", fmt.Errorf("invalid schedule type %q", v)
	}
}

// Task is a persisted scheduled task.
type Task struct {
	ID           int64
	Name         string
	TargetID     string
	TargetName   string
	SessionName  string
	Prompt       string
	Spec         string
	Kind         string
	Timezone       string
	RunMode        string
	ScheduleType   string
	DeliveryTarget string
	DeliveryPolicy string
	TaskSpecJson   []byte
	Enabled      bool
	NextRunAt    *int64 // unix seconds; nil when no further run is scheduled
	LastRunAt    *int64
	LastStatus   string
	CreatedBy    string
	// OwnerPrincipalID is the principal that created the schedule, frozen at
	// creation. Empty means the creator could not be identified (an
	// installation with no enrollment, or a schedule stored before ownership
	// was recorded); the control plane decides what that implies.
	OwnerPrincipalID string
	CreatedAt        int64
}

// LifecycleState returns the user-facing schedule state: "completed" for a
// one-shot schedule that has already run (next_run_at == nil && last_run_at != nil),
// "paused" when disabled, and "enabled" when active.
func (t Task) LifecycleState() string {
	if t.Kind == KindOnce && t.NextRunAt == nil && t.LastRunAt != nil {
		return StateCompleted
	}
	if !t.Enabled {
		return StatePaused
	}
	return StateEnabled
}

// Run is a persisted schedule firing history entry.
type Run struct {
	ID         int64
	ScheduleID int64
	FiredAt    int64
	TaskID     string
	Status     string
}

// Filter narrows List/queries. Empty fields are ignored.
type Filter struct {
	TargetID    string
	SessionName string
}

// Store persists scheduled tasks in SQLite (modernc, CGO-free).
type Store struct {
	db    *sql.DB
	visMu sync.Mutex
}

// DefaultSchedulePath returns ~/.amux/schedule.db.
func DefaultSchedulePath() string {
	return filepath.Join(amuxhome.Dir(), "schedule.db")
}

const scheduleDBBusyTimeoutMS = 5000

func scheduleSQLiteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%s_pragma=busy_timeout%%3d%d&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate", path, separator, scheduleDBBusyTimeoutMS)
}

// OpenStore opens (creating if needed) the schedule database and migrates it.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("schedule db mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", scheduleSQLiteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("schedule db open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("schedule db foreign keys: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS scheduled_tasks (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           TEXT    NOT NULL DEFAULT '',
			target_id      TEXT    NOT NULL,
			target_name    TEXT    NOT NULL DEFAULT '',
			session_name   TEXT    NOT NULL,
			prompt         TEXT    NOT NULL,
			spec           TEXT    NOT NULL,
			kind           TEXT    NOT NULL,
			timezone       TEXT    NOT NULL DEFAULT '',
			run_mode       TEXT    NOT NULL DEFAULT 'reuse-session',
			schedule_type  TEXT    NOT NULL DEFAULT 'session-prompt',
			delivery_target TEXT   NOT NULL DEFAULT 'none',
			delivery_policy TEXT   NOT NULL DEFAULT 'always',
			task_spec_json BLOB,
			enabled        INTEGER NOT NULL DEFAULT 1,
			next_run_at    INTEGER,
			last_run_at    INTEGER,
			last_status    TEXT    NOT NULL DEFAULT '',
			created_by     TEXT    NOT NULL DEFAULT '',
			owner_principal_id TEXT NOT NULL DEFAULT '',
			created_at     INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at     INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`)
	if err != nil {
		return fmt.Errorf("schedule migrate: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE scheduled_tasks ADD COLUMN run_mode TEXT NOT NULL DEFAULT 'reuse-session'`); err != nil {
		return fmt.Errorf("schedule migrate run mode: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE scheduled_tasks ADD COLUMN schedule_type TEXT NOT NULL DEFAULT 'session-prompt'`); err != nil {
		return fmt.Errorf("schedule migrate schedule type: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE scheduled_tasks ADD COLUMN task_spec_json BLOB`); err != nil {
		return fmt.Errorf("schedule migrate task spec: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE scheduled_tasks ADD COLUMN owner_principal_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("schedule migrate owner: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE scheduled_tasks ADD COLUMN timezone TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("schedule migrate timezone: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE scheduled_tasks ADD COLUMN delivery_target TEXT NOT NULL DEFAULT 'none'`); err != nil {
		return fmt.Errorf("schedule migrate delivery target: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE scheduled_tasks ADD COLUMN delivery_policy TEXT NOT NULL DEFAULT 'always'`); err != nil {
		return fmt.Errorf("schedule migrate delivery policy: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schedule_runs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule_id INTEGER NOT NULL,
			fired_at    INTEGER NOT NULL,
			task_id     TEXT    NOT NULL DEFAULT '',
			status      TEXT    NOT NULL,
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			FOREIGN KEY(schedule_id) REFERENCES scheduled_tasks(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("schedule runs migrate: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_due
		ON scheduled_tasks(enabled, next_run_at)
	`); err != nil {
		return fmt.Errorf("schedule index: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_schedule_runs_schedule
		ON schedule_runs(schedule_id, fired_at DESC, id DESC)
	`); err != nil {
		return fmt.Errorf("schedule runs index: %w", err)
	}
	return s.migrateVisibility()
}

// Create inserts a new task and returns it with its assigned ID.
func (s *Store) Create(t Task) (Task, error) {
	runMode, err := NormalizeRunMode(t.RunMode)
	if err != nil {
		return Task{}, err
	}
	scheduleType, err := NormalizeScheduleType(t.ScheduleType)
	if err != nil {
		return Task{}, err
	}
	deliveryTarget, err := NormalizeDeliveryTarget(t.DeliveryTarget)
	if err != nil {
		return Task{}, err
	}
	deliveryPolicy, err := NormalizeDeliveryPolicy(t.DeliveryPolicy)
	if err != nil {
		return Task{}, err
	}
	res, err := s.db.Exec(`
		INSERT INTO scheduled_tasks
			(name, target_id, target_name, session_name, prompt, spec, kind, timezone, run_mode, schedule_type, delivery_target, delivery_policy, task_spec_json, enabled, next_run_at, created_by, owner_principal_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, t.Name, t.TargetID, t.TargetName, t.SessionName, t.Prompt, t.Spec, t.Kind, strings.TrimSpace(t.Timezone), runMode, scheduleType, deliveryTarget, deliveryPolicy, t.TaskSpecJson,
		util.BoolInt(t.Enabled), t.NextRunAt, t.CreatedBy, t.OwnerPrincipalID)
	if err != nil {
		return Task{}, fmt.Errorf("schedule create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Task{}, err
	}
	return s.Get(id)
}

// Update rewrites a task's mutable fields in place and returns the stored row.
// It does not touch enabled, last_run_at, last_status, created_by,
// owner_principal_id or created_at; use SetEnabled to toggle enabled and the
// scheduler to record runs. Ownership is frozen at creation, so an update can
// never move a schedule to another principal. Returns sql.ErrNoRows if no task
// with the given ID exists.
func (s *Store) Update(t Task) (Task, error) {
	runMode, err := NormalizeRunMode(t.RunMode)
	if err != nil {
		return Task{}, err
	}
	scheduleType, err := NormalizeScheduleType(t.ScheduleType)
	if err != nil {
		return Task{}, err
	}
	deliveryTarget, err := NormalizeDeliveryTarget(t.DeliveryTarget)
	if err != nil {
		return Task{}, err
	}
	deliveryPolicy, err := NormalizeDeliveryPolicy(t.DeliveryPolicy)
	if err != nil {
		return Task{}, err
	}
	res, err := s.db.Exec(`
		UPDATE scheduled_tasks SET
			name = ?, target_id = ?, target_name = ?, session_name = ?, prompt = ?,
			spec = ?, kind = ?, timezone = ?, run_mode = ?, schedule_type = ?, delivery_target = ?, delivery_policy = ?, task_spec_json = ?,
			next_run_at = ?, updated_at = strftime('%s','now')
		WHERE id = ?
	`, t.Name, t.TargetID, t.TargetName, t.SessionName, t.Prompt, t.Spec, t.Kind, strings.TrimSpace(t.Timezone), runMode,
		scheduleType, deliveryTarget, deliveryPolicy, t.TaskSpecJson, t.NextRunAt, t.ID)
	if err != nil {
		return Task{}, fmt.Errorf("schedule update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Task{}, sql.ErrNoRows
	}
	return s.Get(t.ID)
}

const selectColumns = `id, name, target_id, target_name, session_name, prompt, spec, kind, timezone, run_mode,
	schedule_type, delivery_target, delivery_policy, task_spec_json, enabled, next_run_at, last_run_at, last_status, created_by,
	owner_principal_id, created_at`

// Get returns a task by ID, or sql.ErrNoRows if absent.
func (s *Store) Get(id int64) (Task, error) {
	row := s.db.QueryRow(`SELECT `+selectColumns+` FROM scheduled_tasks WHERE id = ?`, id)
	return scanTask(row)
}

// List returns tasks matching the filter, newest first.
func (s *Store) List(f Filter) ([]Task, error) {
	q := `SELECT ` + selectColumns + ` FROM scheduled_tasks WHERE 1=1`
	var args []any
	if f.TargetID != "" {
		q += ` AND target_id = ?`
		args = append(args, f.TargetID)
	}
	if f.SessionName != "" {
		q += ` AND session_name = ?`
		args = append(args, f.SessionName)
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("schedule list: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetOwnerIfUnset records an owner for a task that has none, and does nothing
// once one is set. Ownership is frozen at creation, so this is only for
// attributing rows stored before it was recorded; the WHERE clause makes
// concurrent attempts converge on whichever ran first rather than fight.
func (s *Store) SetOwnerIfUnset(id int64, principalID string) error {
	if strings.TrimSpace(principalID) == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE scheduled_tasks SET owner_principal_id = ?
		WHERE id = ? AND owner_principal_id = ''`, principalID, id)
	if err != nil {
		return fmt.Errorf("schedule set owner: %w", err)
	}
	return nil
}

// Delete removes a task. Returns whether a row was deleted.
func (s *Store) Delete(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("schedule delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetEnabled toggles a task's enabled flag.
func (s *Store) SetEnabled(id int64, enabled bool) (bool, error) {
	res, err := s.db.Exec(`UPDATE scheduled_tasks SET enabled = ?, updated_at = strftime('%s','now') WHERE id = ?`,
		util.BoolInt(enabled), id)
	if err != nil {
		return false, fmt.Errorf("schedule set enabled: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetEnabledWithNextRun atomically updates both enabled and next_run_at.
func (s *Store) SetEnabledWithNextRun(id int64, enabled bool, nextRunAt *int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE scheduled_tasks SET enabled = ?, next_run_at = ?, updated_at = strftime('%s','now') WHERE id = ?`,
		util.BoolInt(enabled), nextRunAt, id)
	if err != nil {
		return false, fmt.Errorf("schedule set enabled with next run: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DueTasks returns enabled tasks whose next_run_at is at or before now.
func (s *Store) DueTasks(now int64) ([]Task, error) {
	rows, err := s.db.Query(`SELECT `+selectColumns+`
		FROM scheduled_tasks
		WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
		ORDER BY next_run_at ASC`, now)
	if err != nil {
		return nil, fmt.Errorf("schedule due: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EarliestNextRun returns the smallest next_run_at across enabled schedules, or
// nil when no enabled schedule has a pending run.
func (s *Store) EarliestNextRun() (*int64, error) {
	var next sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(next_run_at) FROM scheduled_tasks WHERE enabled = 1 AND next_run_at IS NOT NULL`).Scan(&next); err != nil {
		return nil, fmt.Errorf("schedule earliest next run: %w", err)
	}
	if !next.Valid {
		return nil, nil
	}
	v := next.Int64
	return &v, nil
}

// RecordRun updates a task after a (possibly skipped) trigger. nextRunAt is nil
// to clear the schedule (one-shot consumed).
func (s *Store) RecordRun(id int64, ranAt int64, nextRunAt *int64, status string) error {
	_, err := s.db.Exec(`UPDATE scheduled_tasks
		SET last_run_at = ?, next_run_at = ?, last_status = ?, updated_at = strftime('%s','now')
		WHERE id = ?`, ranAt, nextRunAt, status, id)
	if err != nil {
		return fmt.Errorf("schedule record run: %w", err)
	}
	return nil
}

// ClaimRun atomically claims a due task run if the task is still enabled and its
// next_run_at still matches the due snapshot that the scheduler observed.
func (s *Store) ClaimRun(id int64, expectedNextRunAt *int64, ranAt int64, nextRunAt *int64, status string) (bool, error) {
	q := `UPDATE scheduled_tasks
		SET last_run_at = ?, next_run_at = ?, last_status = ?, updated_at = strftime('%s','now')
		WHERE id = ? AND enabled = 1 AND `
	args := []any{ranAt, nextRunAt, status, id}
	if expectedNextRunAt == nil {
		q += `next_run_at IS NULL`
	} else {
		q += `next_run_at = ?`
		args = append(args, *expectedNextRunAt)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, fmt.Errorf("schedule claim run: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("schedule claim run rows affected: %w", err)
	}
	return n == 1, nil
}

// UpdateLastStatusIfRun updates the summary status only if the schedule row still
// points at the same run that produced the completion.
func (s *Store) UpdateLastStatusIfRun(id int64, ranAt int64, status string) (bool, error) {
	res, err := s.db.Exec(`UPDATE scheduled_tasks
		SET last_status = ?, updated_at = strftime('%s','now')
		WHERE id = ? AND last_run_at = ?`, status, id, ranAt)
	if err != nil {
		return false, fmt.Errorf("schedule update last status if run: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("schedule update last status if run rows affected: %w", err)
	}
	return n == 1, nil
}

// RecordScheduleRun appends a run history row and prunes old rows for the schedule.
func (s *Store) RecordScheduleRun(scheduleID int64, firedAt int64, taskID string, status string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("schedule run begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO schedule_runs (schedule_id, fired_at, task_id, status) VALUES (?, ?, ?, ?)`,
		scheduleID, firedAt, taskID, status); err != nil {
		return fmt.Errorf("schedule run insert: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM schedule_runs
		WHERE schedule_id = ?
		AND id NOT IN (
			SELECT id FROM schedule_runs
			WHERE schedule_id = ?
			ORDER BY fired_at DESC, id DESC
			LIMIT ?
		)`, scheduleID, scheduleID, DefaultRunHistoryLimit); err != nil {
		return fmt.Errorf("schedule run prune: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("schedule run commit: %w", err)
	}
	return nil
}

// ListRuns returns run history for a schedule, newest first.
func (s *Store) ListRuns(scheduleID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > DefaultRunHistoryLimit {
		limit = DefaultRunHistoryLimit
	}
	rows, err := s.db.Query(`SELECT id, schedule_id, fired_at, task_id, status
		FROM schedule_runs
		WHERE schedule_id = ?
		ORDER BY fired_at DESC, id DESC
		LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, fmt.Errorf("schedule runs list: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.FiredAt, &r.TaskID, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanTask(sc scanner) (Task, error) {
	var t Task
	var enabled int
	var taskSpec []byte
	var name, targetName, timezone, runMode, scheduleType, deliveryTarget, deliveryPolicy, lastStatus, createdBy, owner sql.NullString
	var nextRunAt, lastRunAt sql.NullInt64
	if err := sc.Scan(&t.ID, &name, &t.TargetID, &targetName, &t.SessionName, &t.Prompt,
		&t.Spec, &t.Kind, &timezone, &runMode, &scheduleType, &deliveryTarget, &deliveryPolicy, &taskSpec, &enabled, &nextRunAt, &lastRunAt, &lastStatus,
		&createdBy, &owner, &t.CreatedAt); err != nil {
		return Task{}, err
	}
	t.Name = name.String
	t.TargetName = targetName.String
	t.Timezone = timezone.String
	t.RunMode, _ = NormalizeRunMode(runMode.String)
	var err error
	t.ScheduleType, err = NormalizeScheduleType(scheduleType.String)
	if err != nil {
		return Task{}, err
	}
	t.DeliveryTarget, _ = NormalizeDeliveryTarget(deliveryTarget.String)
	t.DeliveryPolicy, _ = NormalizeDeliveryPolicy(deliveryPolicy.String)
	t.TaskSpecJson = append([]byte(nil), taskSpec...)
	t.LastStatus = lastStatus.String
	t.CreatedBy = createdBy.String
	t.OwnerPrincipalID = owner.String
	t.Enabled = enabled != 0
	if nextRunAt.Valid {
		t.NextRunAt = &nextRunAt.Int64
	}
	if lastRunAt.Valid {
		t.LastRunAt = &lastRunAt.Int64
	}
	return t, nil
}

func (s *Store) addColumnIfMissing(stmt string) error {
	if _, err := s.db.Exec(stmt); err != nil && !isDuplicateColumnError(err) {
		return err
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column")
}

// unixPtr is a helper for callers building a Task.
func unixPtr(t time.Time) *int64 {
	v := t.Unix()
	return &v
}
