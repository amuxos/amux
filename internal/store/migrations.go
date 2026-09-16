package store

import (
	"fmt"
	"strings"
)

type migration struct {
	version int
	sql     string
}

const baseTablesSQL = `
CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT    PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,
  timeline_epoch TEXT NOT NULL DEFAULT '',
  agent_type TEXT    NOT NULL,
  state      TEXT    NOT NULL,
  pid        INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT    NOT NULL,
  data       BLOB    NOT NULL,
  source     TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  FOREIGN KEY(session_id) REFERENCES sessions(id)
);
`

const tasksTableSQL = `
CREATE TABLE IF NOT EXISTS tasks (
  id                 TEXT    PRIMARY KEY,
  parent_session_id  TEXT    NOT NULL DEFAULT '',
  parent_task_id     TEXT    NOT NULL DEFAULT '',
  parent_turn_id     TEXT    NOT NULL DEFAULT '',
  turn_id            TEXT    NOT NULL DEFAULT '',
  mode               TEXT    NOT NULL DEFAULT '',
  agent_type         TEXT    NOT NULL DEFAULT '',
  agent_profile      TEXT    NOT NULL DEFAULT '',
  target_id          TEXT    NOT NULL DEFAULT '',
  child_session_id   TEXT    NOT NULL DEFAULT '',
  child_session_name TEXT    NOT NULL DEFAULT '',
  isolation_mode     TEXT    NOT NULL DEFAULT '',
  credential_profile TEXT    NOT NULL DEFAULT '',
  credential_profiles TEXT   NOT NULL DEFAULT '',
  execution_identity_json TEXT NOT NULL DEFAULT '',
  launch_id            TEXT    NOT NULL DEFAULT '',
  isolation_backend    TEXT    NOT NULL DEFAULT '',
  permission_policy  TEXT    NOT NULL DEFAULT '',
  workspace_policy   TEXT    NOT NULL DEFAULT '',
  result_granularity TEXT    NOT NULL DEFAULT '',
  security_level     TEXT    NOT NULL DEFAULT '',
  status             TEXT    NOT NULL DEFAULT '',
  task               TEXT    NOT NULL DEFAULT '',
  result_json        TEXT    NOT NULL DEFAULT '',
  error              TEXT    NOT NULL DEFAULT '',
  timeout_seconds    INTEGER NOT NULL DEFAULT 0,
  requested_at       INTEGER NOT NULL DEFAULT 0,
  started_at         INTEGER NOT NULL DEFAULT 0,
  finished_at        INTEGER NOT NULL DEFAULT 0
);
`

const taskEventsTableSQL = `
CREATE TABLE IF NOT EXISTS task_events (
  task_id    TEXT    NOT NULL,
  seq        INTEGER NOT NULL,
  kind       TEXT    NOT NULL DEFAULT '',
  event_json TEXT    NOT NULL DEFAULT '',
  at         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (task_id, seq)
);
`

const sessionTimelineEventsTableSQL = `
CREATE TABLE IF NOT EXISTS session_timeline_events (
  session_id   TEXT    NOT NULL,
  seq          INTEGER NOT NULL CHECK (seq > 0),
  epoch        TEXT    NOT NULL,
  turn_id      TEXT    NOT NULL DEFAULT '',
  turn_seq     INTEGER NOT NULL DEFAULT 0,
  kind         TEXT    NOT NULL,
  event_json   TEXT    NOT NULL,
  at_unix_nano INTEGER NOT NULL,
  PRIMARY KEY (session_id, seq),
  FOREIGN KEY(session_id) REFERENCES sessions(id)
);
CREATE INDEX IF NOT EXISTS session_timeline_turn_idx
  ON session_timeline_events(session_id, turn_seq, seq);
`

var migrations = []migration{
	{1, `
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (0);
` + baseTablesSQL},
	{2, `ALTER TABLE sessions ADD COLUMN broker_socket TEXT NOT NULL DEFAULT ''`},
	{3, `ALTER TABLE sessions ADD COLUMN cwd TEXT NOT NULL DEFAULT ''`},
	{4, `
ALTER TABLE sessions ADD COLUMN extra_args TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN env TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN workspace_source TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN workspace_scope TEXT NOT NULL DEFAULT '';
	`},
	{5, `ALTER TABLE sessions ADD COLUMN agent_session_id TEXT NOT NULL DEFAULT ''`},
	{6, `ALTER TABLE sessions ADD COLUMN agent_profile TEXT NOT NULL DEFAULT ''`},
	{7, `ALTER TABLE sessions ADD COLUMN launch_spec TEXT NOT NULL DEFAULT ''`},
	{8, `
ALTER TABLE sessions ADD COLUMN idle_timeout_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN last_activity_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN last_sleep_reason TEXT NOT NULL DEFAULT '';
	`},
	{9, `ALTER TABLE sessions ADD COLUMN owner_task_id TEXT NOT NULL DEFAULT ''`},
	{10, tasksTableSQL},
	{11, taskEventsTableSQL},
	{12, `ALTER TABLE sessions ADD COLUMN idle_timeout_seconds_set INTEGER NOT NULL DEFAULT 0`},
	{13, `ALTER TABLE sessions ADD COLUMN agent_instance_id TEXT NOT NULL DEFAULT ''`},
	{14, tasksTableSQL + `ALTER TABLE tasks ADD COLUMN credential_profiles TEXT NOT NULL DEFAULT ''`},
	{15, tasksTableSQL + `ALTER TABLE tasks ADD COLUMN agent_profile TEXT NOT NULL DEFAULT ''`},
	{16, `ALTER TABLE sessions ADD COLUMN effective_model TEXT NOT NULL DEFAULT ''`},
	// v17 columns are additive and installed by ensureCurrentSchema so a fresh
	// database (whose CREATE TABLE already contains them) and an upgraded
	// database follow the same idempotent path.
	{17, `SELECT 1`},
	{18, sessionTimelineEventsTableSQL},
}

func (db *DB) migrate() error {
	// Ensure schema_version exists before reading it.
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`)
	if err != nil {
		return err
	}

	var current int
	row := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`)
	if err := row.Scan(&current); err != nil {
		return err
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if _, err := db.Exec(m.sql); err != nil {
			if !ignoreDuplicateColumnMigrations[m.version] || !isDuplicateColumnError(err) {
				return fmt.Errorf("migration v%d: %w", m.version, err)
			}
		}
		if _, err := db.Exec(`UPDATE schema_version SET version=?`, m.version); err != nil {
			return fmt.Errorf("migration v%d update version: %w", m.version, err)
		}
		current = m.version
	}
	if err := db.ensureCurrentSchema(); err != nil {
		return err
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

var ignoreDuplicateColumnMigrations = map[int]bool{
	9:  true,
	14: true,
	15: true,
}

type additiveColumn struct {
	name string
	def  string
}

var sessionAdditiveColumns = []additiveColumn{
	{"timeline_epoch", "timeline_epoch TEXT NOT NULL DEFAULT ''"},
	{"broker_socket", "broker_socket TEXT NOT NULL DEFAULT ''"},
	{"cwd", "cwd TEXT NOT NULL DEFAULT ''"},
	{"extra_args", "extra_args TEXT NOT NULL DEFAULT ''"},
	{"env", "env TEXT NOT NULL DEFAULT ''"},
	{"workspace_source", "workspace_source TEXT NOT NULL DEFAULT ''"},
	{"workspace_scope", "workspace_scope TEXT NOT NULL DEFAULT ''"},
	{"agent_session_id", "agent_session_id TEXT NOT NULL DEFAULT ''"},
	{"agent_profile", "agent_profile TEXT NOT NULL DEFAULT ''"},
	{"effective_model", "effective_model TEXT NOT NULL DEFAULT ''"},
	{"launch_spec", "launch_spec TEXT NOT NULL DEFAULT ''"},
	{"handoff_context", "handoff_context TEXT NOT NULL DEFAULT ''"},
	{"idle_timeout_seconds", "idle_timeout_seconds INTEGER NOT NULL DEFAULT 0"},
	{"idle_timeout_seconds_set", "idle_timeout_seconds_set INTEGER NOT NULL DEFAULT 0"},
	{"last_activity_at", "last_activity_at INTEGER NOT NULL DEFAULT 0"},
	{"last_interaction_at", "last_interaction_at INTEGER NOT NULL DEFAULT 0"},
	{"last_sleep_reason", "last_sleep_reason TEXT NOT NULL DEFAULT ''"},
	{"owner_task_id", "owner_task_id TEXT NOT NULL DEFAULT ''"},
	{"agent_instance_id", "agent_instance_id TEXT NOT NULL DEFAULT ''"},
	{"execution_identity_json", "execution_identity_json TEXT NOT NULL DEFAULT ''"},
	{"launch_id", "launch_id TEXT NOT NULL DEFAULT ''"},
	{"isolation_backend", "isolation_backend TEXT NOT NULL DEFAULT ''"},
}

var taskAdditiveColumns = []additiveColumn{
	{"credential_profiles", "credential_profiles TEXT NOT NULL DEFAULT ''"},
	{"agent_profile", "agent_profile TEXT NOT NULL DEFAULT ''"},
	{"execution_identity_json", "execution_identity_json TEXT NOT NULL DEFAULT ''"},
	{"launch_id", "launch_id TEXT NOT NULL DEFAULT ''"},
	{"isolation_backend", "isolation_backend TEXT NOT NULL DEFAULT ''"},
	{"parent_turn_id", "parent_turn_id TEXT NOT NULL DEFAULT ''"},
	{"turn_id", "turn_id TEXT NOT NULL DEFAULT ''"},
}

func (db *DB) ensureCurrentSchema() error {
	if _, err := db.Exec(baseTablesSQL); err != nil {
		return fmt.Errorf("ensure base tables: %w", err)
	}
	for _, col := range sessionAdditiveColumns {
		ok, err := db.columnExists("sessions", col.name)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN " + col.def); err != nil {
			return fmt.Errorf("ensure sessions.%s: %w", col.name, err)
		}
	}
	if _, err := db.Exec(`UPDATE sessions SET timeline_epoch=lower(hex(randomblob(16))) WHERE timeline_epoch=''`); err != nil {
		return fmt.Errorf("backfill sessions.timeline_epoch: %w", err)
	}
	if _, err := db.Exec(tasksTableSQL); err != nil {
		return fmt.Errorf("ensure tasks table: %w", err)
	}
	for _, col := range taskAdditiveColumns {
		ok, err := db.columnExists("tasks", col.name)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if _, err := db.Exec("ALTER TABLE tasks ADD COLUMN " + col.def); err != nil {
			return fmt.Errorf("ensure tasks.%s: %w", col.name, err)
		}
	}
	if _, err := db.Exec(taskEventsTableSQL); err != nil {
		return fmt.Errorf("ensure task_events table: %w", err)
	}
	if _, err := db.Exec(sessionTimelineEventsTableSQL); err != nil {
		return fmt.Errorf("ensure session_timeline_events table: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_version`); err != nil {
		return fmt.Errorf("reset schema version: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version VALUES (?)`, latestMigrationVersion()); err != nil {
		return fmt.Errorf("write schema version: %w", err)
	}
	return nil
}

func (db *DB) columnExists(table, name string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			colName    string
			colType    string
			notNull    int
			defaultVal any
			pk         int
		)
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &defaultVal, &pk); err != nil {
			return false, fmt.Errorf("scan %s columns: %w", table, err)
		}
		if colName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

func latestMigrationVersion() int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}
