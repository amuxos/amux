package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateSkipsAlreadyAppliedOwnerTaskIdColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(`
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (8);
CREATE TABLE sessions (
  id                   TEXT    PRIMARY KEY,
  name                 TEXT    NOT NULL UNIQUE,
  agent_profile        TEXT    NOT NULL DEFAULT '',
  agent_type           TEXT    NOT NULL,
  state                TEXT    NOT NULL,
  pid                  INTEGER,
  cwd                  TEXT    NOT NULL DEFAULT '',
  broker_socket        TEXT    NOT NULL DEFAULT '',
  extra_args           TEXT    NOT NULL DEFAULT '',
  env                  TEXT    NOT NULL DEFAULT '',
  workspace_source     TEXT    NOT NULL DEFAULT '',
  workspace_scope      TEXT    NOT NULL DEFAULT '',
  agent_session_id     TEXT    NOT NULL DEFAULT '',
  launch_spec          TEXT    NOT NULL DEFAULT '',
  idle_timeout_seconds INTEGER NOT NULL DEFAULT 0,
  last_activity_at     INTEGER NOT NULL DEFAULT 0,
  last_sleep_reason    TEXT    NOT NULL DEFAULT '',
  owner_task_id        TEXT    NOT NULL DEFAULT '',
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);
CREATE TABLE messages (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT    NOT NULL,
  data       BLOB    NOT NULL,
  source     TEXT    NOT NULL,
  created_at INTEGER NOT NULL
);
`); err != nil {
		raw.Close()
		t.Fatalf("seed raw db: %v", err)
	}
	raw.Close()

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != latestMigrationVersion() {
		t.Fatalf("schema version = %d, want %d", version, latestMigrationVersion())
	}
	assertQuerySucceeds(t, db, `SELECT owner_task_id FROM sessions`)
	assertQuerySucceeds(t, db, `SELECT agent_instance_id FROM sessions`)
	assertQuerySucceeds(t, db, `SELECT effective_model FROM sessions`)
	assertQuerySucceeds(t, db, `SELECT agent_profile FROM tasks`)
	assertQuerySucceeds(t, db, `SELECT execution_identity_json, launch_id, isolation_backend FROM sessions`)
	assertQuerySucceeds(t, db, `SELECT execution_identity_json, launch_id, isolation_backend FROM tasks`)
	assertQuerySucceeds(t, db, `SELECT parent_turn_id, turn_id FROM tasks`)
}

func TestMigrateRepairsCurrentTasksWithoutTurnProvenance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	legacyTasks := strings.ReplaceAll(tasksTableSQL,
		"  parent_turn_id     TEXT    NOT NULL DEFAULT '',\n", "")
	legacyTasks = strings.ReplaceAll(legacyTasks,
		"  turn_id            TEXT    NOT NULL DEFAULT '',\n", "")
	if _, err := raw.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL); INSERT INTO schema_version VALUES (` +
		strconv.Itoa(latestMigrationVersion()) + `);` + legacyTasks); err != nil {
		raw.Close()
		t.Fatalf("seed current db: %v", err)
	}
	raw.Close()

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open repaired db: %v", err)
	}
	defer db.Close()
	assertQuerySucceeds(t, db, `SELECT parent_turn_id, turn_id FROM tasks`)
}

func TestMigrateRepairsCurrentVersionWithMissingSessionColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(`
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (12);
CREATE TABLE sessions (
  id               TEXT    PRIMARY KEY,
  name             TEXT    NOT NULL UNIQUE,
  agent_type       TEXT    NOT NULL,
  state            TEXT    NOT NULL,
  pid              INTEGER,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  broker_socket    TEXT    NOT NULL DEFAULT '',
  cwd              TEXT    NOT NULL DEFAULT '',
  extra_args       TEXT    NOT NULL DEFAULT '',
  env              TEXT    NOT NULL DEFAULT '',
  workspace_source TEXT    NOT NULL DEFAULT '',
  workspace_scope  TEXT    NOT NULL DEFAULT '',
  agent_session_id TEXT    NOT NULL DEFAULT '',
  owner_task_id    TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE messages (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT    NOT NULL,
  data       BLOB    NOT NULL,
  source     TEXT    NOT NULL,
  created_at INTEGER NOT NULL
);
`); err != nil {
		raw.Close()
		t.Fatalf("seed raw db: %v", err)
	}
	raw.Close()

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer db.Close()

	for _, col := range []string{"timeline_epoch", "agent_profile", "launch_spec", "effective_model", "idle_timeout_seconds", "idle_timeout_seconds_set", "last_activity_at", "last_interaction_at", "last_sleep_reason", "owner_task_id", "agent_instance_id"} {
		assertQuerySucceeds(t, db, "SELECT "+col+" FROM sessions")
	}
	for _, table := range []string{"tasks", "task_events", "session_timeline_events"} {
		assertQuerySucceeds(t, db, "SELECT 1 FROM "+table+" LIMIT 1")
	}
}

func TestSessionLastInteractionColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	row := SessionRow{ID: "s1", Name: "n1", State: "running", IdleTimeoutSeconds: 120, IdleTimeoutSet: true, LastInteractionAt: 12345}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := db.AllSessions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].LastInteractionAt != 12345 {
		t.Fatalf("LastInteractionAt = %v, want 12345", got)
	}
	if got[0].IdleTimeoutSeconds != 120 || !got[0].IdleTimeoutSet {
		t.Fatalf("idle timeout = %d set:%v, want 120 set:true", got[0].IdleTimeoutSeconds, got[0].IdleTimeoutSet)
	}
}

func TestSessionEffectiveModelRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	row := SessionRow{ID: "s-model", Name: "model-session", AgentType: "codex", State: "running", EffectiveModel: "gpt-5.5"}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := db.AllSessions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].EffectiveModel != "gpt-5.5" {
		t.Fatalf("effective_model = %+v, want gpt-5.5", got)
	}
}

func TestSessionExecutionReconfigurationAndMessagesRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	row := SessionRow{ID: "s-reconfigure", Name: "reconfigure", AgentType: "claude", State: "running"}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("save initial session: %v", err)
	}
	row.AgentType = "codex"
	row.HandoffContext = "historical transcript"
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("save reconfigured session: %v", err)
	}
	if err := db.AppendMessage(row.ID, "user", []byte("first question")); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := db.AppendMessage(row.ID, "assistant", []byte("first answer")); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}

	sessions, err := db.AllSessions()
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].AgentType != "codex" || sessions[0].HandoffContext != "historical transcript" {
		t.Fatalf("sessions = %+v", sessions)
	}
	messages, err := db.SessionMessages(row.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Source != "user" || string(messages[0].Data) != "first question" ||
		messages[1].Source != "assistant" || string(messages[1].Data) != "first answer" {
		t.Fatalf("messages = %+v", messages)
	}
}

func assertQuerySucceeds(t *testing.T, db *DB, query string) {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s failed: %v", query, err)
	}
	rows.Close()
}
