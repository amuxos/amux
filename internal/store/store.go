package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/util"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

const runtimeDBBusyTimeoutMS = 5000

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: create: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("store: close created db: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("store: chmod: %w", err)
	}
	sqldb, err := sql.Open("sqlite", runtimeDBDSN(path))
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	sqldb.SetMaxOpenConns(1) // sqlite doesn't support concurrent writers
	db := &DB{sqldb}
	if err := db.migrate(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return db, nil
}

func runtimeDBDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%s_pragma=busy_timeout%%3d%d&_pragma=journal_mode(WAL)&_txlock=immediate", path, separator, runtimeDBBusyTimeoutMS)
}

type SessionRow struct {
	ID                 string
	Name               string
	TimelineEpoch      string
	AgentProfile       string
	EffectiveModel     string
	AgentType          string
	State              string
	PID                int
	Cwd                string
	BrokerSocket       string
	ExtraArgs          []string
	Env                []string
	WorkspaceSource    string
	WorkspaceScope     string
	AgentSessionID     string
	OwnerTaskId        string
	AgentInstanceID    string
	ExecutionIdentity  string
	LaunchID           string
	IsolationBackend   string
	LaunchSpec         string
	HandoffContext     string
	IdleTimeoutSeconds int
	IdleTimeoutSet     bool
	LastActivityAt     int64
	LastInteractionAt  int64
	LastSleepReason    string
	CreatedAt          int64
	UpdatedAt          int64
}

type MessageRow struct {
	Data   []byte
	Source string
}

func (db *DB) SaveSession(r SessionRow) error {
	extraJSON, err := encodeStringSlice(r.ExtraArgs)
	if err != nil {
		return fmt.Errorf("encode extra_args: %w", err)
	}
	envJSON, err := encodeStringSlice(r.Env)
	if err != nil {
		return fmt.Errorf("encode env: %w", err)
	}
	_, err = db.Exec(`
			INSERT INTO sessions (id, name, timeline_epoch, agent_profile, effective_model, agent_type, state, pid, cwd, broker_socket, extra_args, env, workspace_source, workspace_scope, agent_session_id, owner_task_id, agent_instance_id, execution_identity_json, launch_id, isolation_backend, launch_spec, handoff_context, idle_timeout_seconds, idle_timeout_seconds_set, last_activity_at, last_interaction_at, last_sleep_reason, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				timeline_epoch=CASE
					WHEN sessions.timeline_epoch='' THEN excluded.timeline_epoch
					ELSE sessions.timeline_epoch
				END,
				agent_profile=excluded.agent_profile,
				effective_model=excluded.effective_model,
				agent_type=excluded.agent_type,
			state=excluded.state, pid=excluded.pid,
			cwd=excluded.cwd, broker_socket=excluded.broker_socket,
			extra_args=excluded.extra_args, env=excluded.env,
			workspace_source=excluded.workspace_source, workspace_scope=excluded.workspace_scope,
			agent_session_id=excluded.agent_session_id,
			owner_task_id=excluded.owner_task_id,
			agent_instance_id=excluded.agent_instance_id,
			execution_identity_json=excluded.execution_identity_json,
			launch_id=excluded.launch_id,
			isolation_backend=excluded.isolation_backend,
				launch_spec=excluded.launch_spec,
				handoff_context=excluded.handoff_context,
			idle_timeout_seconds=excluded.idle_timeout_seconds,
			idle_timeout_seconds_set=excluded.idle_timeout_seconds_set,
			last_activity_at=excluded.last_activity_at,
			last_interaction_at=excluded.last_interaction_at,
			last_sleep_reason=excluded.last_sleep_reason,
			updated_at=excluded.updated_at`,
		r.ID, r.Name, r.TimelineEpoch, r.AgentProfile, r.EffectiveModel, r.AgentType, r.State, r.PID, r.Cwd, r.BrokerSocket,
		extraJSON, envJSON, r.WorkspaceSource, r.WorkspaceScope, r.AgentSessionID, r.OwnerTaskId, r.AgentInstanceID,
		r.ExecutionIdentity, r.LaunchID, r.IsolationBackend, r.LaunchSpec, r.HandoffContext,
		r.IdleTimeoutSeconds, util.BoolInt(r.IdleTimeoutSet), r.LastActivityAt, r.LastInteractionAt, r.LastSleepReason,
		r.CreatedAt, r.UpdatedAt,
	)
	return err
}

func (db *DB) UpdateSessionState(id, state string, pid int) error {
	_, err := db.Exec(
		`UPDATE sessions SET state=?, pid=?, last_activity_at=strftime('%s','now'), updated_at=strftime('%s','now') WHERE id=?`,
		state, pid, id,
	)
	return err
}

func (db *DB) UpdateSessionActivity(id string, lastActivityAt, lastInteractionAt int64) error {
	_, err := db.Exec(
		`UPDATE sessions SET last_activity_at=?,
			last_interaction_at = CASE WHEN ? > 0 THEN ? ELSE last_interaction_at END,
			updated_at=strftime('%s','now') WHERE id=?`,
		lastActivityAt, lastInteractionAt, lastInteractionAt, id,
	)
	return err
}

func (db *DB) UpdateSessionHandoffContext(id, value string) error {
	_, err := db.Exec(`UPDATE sessions SET handoff_context=?, updated_at=strftime('%s','now') WHERE id=?`, value, id)
	return err
}

func (db *DB) AppendMessage(sessionID, source string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	_, err := db.Exec(`INSERT INTO messages (session_id, data, source, created_at) VALUES (?, ?, ?, strftime('%s','now'))`, sessionID, data, source)
	return err
}

func (db *DB) SessionMessages(sessionID string) ([]MessageRow, error) {
	rows, err := db.Query(`SELECT data, source FROM messages WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageRow
	for rows.Next() {
		var row MessageRow
		if err := rows.Scan(&row.Data, &row.Source); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (db *DB) AllSessions() ([]SessionRow, error) {
	rows, err := db.Query(
		`SELECT id, name, timeline_epoch, agent_profile, effective_model, agent_type, state, pid, cwd, broker_socket, extra_args, env, workspace_source, workspace_scope, agent_session_id, owner_task_id, agent_instance_id, execution_identity_json, launch_id, isolation_backend, launch_spec, handoff_context, idle_timeout_seconds, idle_timeout_seconds_set, last_activity_at, last_interaction_at, last_sleep_reason, created_at, updated_at FROM sessions`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionRow
	for rows.Next() {
		var (
			r                  SessionRow
			extraJSON, envJSON string
			idleTimeoutSet     int
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.TimelineEpoch, &r.AgentProfile, &r.EffectiveModel, &r.AgentType, &r.State, &r.PID, &r.Cwd,
			&r.BrokerSocket, &extraJSON, &envJSON, &r.WorkspaceSource, &r.WorkspaceScope,
			&r.AgentSessionID, &r.OwnerTaskId, &r.AgentInstanceID, &r.ExecutionIdentity, &r.LaunchID, &r.IsolationBackend,
			&r.LaunchSpec, &r.HandoffContext, &r.IdleTimeoutSeconds, &idleTimeoutSet, &r.LastActivityAt,
			&r.LastInteractionAt, &r.LastSleepReason, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.IdleTimeoutSet = idleTimeoutSet != 0
		if r.ExtraArgs, err = decodeStringSlice(extraJSON); err != nil {
			return nil, fmt.Errorf("decode extra_args for %s: %w", r.ID, err)
		}
		if r.Env, err = decodeStringSlice(envJSON); err != nil {
			return nil, fmt.Errorf("decode env for %s: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func encodeStringSlice(v []string) (string, error) {
	if len(v) == 0 {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeStringSlice(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteSession atomically removes a session and all session-owned history.
func (db *DB) DeleteSession(id string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`DELETE FROM session_timeline_events WHERE session_id=?`,
		`DELETE FROM messages WHERE session_id=?`,
		`DELETE FROM sessions WHERE id=?`,
	} {
		if _, err := tx.Exec(query, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
