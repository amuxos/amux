package store

// TaskRow is the persisted form of a distributed-task TaskRun. The mutable,
// structured result is stored as a JSON blob; the rest are indexed columns.
type TaskRow struct {
	ID                 string
	ParentSessionID    string
	ParentTaskID       string
	ParentTurnID       string
	TurnID             string
	Mode               string
	AgentType          string
	AgentProfile       string
	TargetID           string
	ChildSessionId     string
	ChildSessionName   string
	IsolationMode      string
	CredentialProfile  string
	CredentialProfiles string
	ExecutionIdentity  string
	LaunchID           string
	IsolationBackend   string
	PermissionPolicy   string
	WorkspacePolicy    string
	ResultGranularity  string
	SecurityLevel      string
	Status             string
	Task               string
	ResultJSON         string
	Error              string
	TimeoutSeconds     int
	RequestedAt        int64
	StartedAt          int64
	FinishedAt         int64
}

const taskColumns = `id, parent_session_id, parent_task_id, parent_turn_id, turn_id, mode, agent_type, agent_profile, target_id,
	child_session_id, child_session_name, isolation_mode, credential_profile, credential_profiles,
	execution_identity_json, launch_id, isolation_backend,
	permission_policy, workspace_policy, result_granularity, security_level, status,
	task, result_json, error, timeout_seconds, requested_at, started_at, finished_at`

func (db *DB) SaveTask(r TaskRow) error {
	_, err := db.Exec(`
		INSERT INTO tasks (`+taskColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			parent_session_id=excluded.parent_session_id, parent_task_id=excluded.parent_task_id,
			parent_turn_id=excluded.parent_turn_id, turn_id=excluded.turn_id,
			mode=excluded.mode, agent_type=excluded.agent_type, agent_profile=excluded.agent_profile, target_id=excluded.target_id,
			child_session_id=excluded.child_session_id, child_session_name=excluded.child_session_name,
			isolation_mode=excluded.isolation_mode, credential_profile=excluded.credential_profile,
			credential_profiles=excluded.credential_profiles,
			execution_identity_json=excluded.execution_identity_json,
			launch_id=excluded.launch_id,
			isolation_backend=excluded.isolation_backend,
			permission_policy=excluded.permission_policy, workspace_policy=excluded.workspace_policy,
			result_granularity=excluded.result_granularity, security_level=excluded.security_level,
			status=excluded.status, task=excluded.task, result_json=excluded.result_json,
			error=excluded.error, timeout_seconds=excluded.timeout_seconds,
			requested_at=excluded.requested_at, started_at=excluded.started_at,
			finished_at=excluded.finished_at`,
		r.ID, r.ParentSessionID, r.ParentTaskID, r.ParentTurnID, r.TurnID, r.Mode, r.AgentType, r.AgentProfile, r.TargetID,
		r.ChildSessionId, r.ChildSessionName, r.IsolationMode, r.CredentialProfile,
		r.CredentialProfiles, r.ExecutionIdentity, r.LaunchID, r.IsolationBackend,
		r.PermissionPolicy, r.WorkspacePolicy, r.ResultGranularity, r.SecurityLevel, r.Status,
		r.Task, r.ResultJSON, r.Error, r.TimeoutSeconds, r.RequestedAt, r.StartedAt, r.FinishedAt,
	)
	return err
}

func (db *DB) GetTask(id string) (TaskRow, error) {
	row := db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=?`, id)
	return scanTask(row)
}

func (db *DB) AllTasks() ([]TaskRow, error) {
	rows, err := db.Query(`SELECT ` + taskColumns + ` FROM tasks ORDER BY requested_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		r, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) DeleteTask(id string) error {
	if _, err := db.Exec(`DELETE FROM task_events WHERE task_id=?`, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM tasks WHERE id=?`, id)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(s scanner) (TaskRow, error) {
	var r TaskRow
	err := s.Scan(&r.ID, &r.ParentSessionID, &r.ParentTaskID, &r.ParentTurnID, &r.TurnID, &r.Mode, &r.AgentType, &r.AgentProfile, &r.TargetID,
		&r.ChildSessionId, &r.ChildSessionName, &r.IsolationMode, &r.CredentialProfile,
		&r.CredentialProfiles, &r.ExecutionIdentity, &r.LaunchID, &r.IsolationBackend,
		&r.PermissionPolicy, &r.WorkspacePolicy, &r.ResultGranularity, &r.SecurityLevel, &r.Status,
		&r.Task, &r.ResultJSON, &r.Error, &r.TimeoutSeconds, &r.RequestedAt, &r.StartedAt, &r.FinishedAt)
	return r, err
}
