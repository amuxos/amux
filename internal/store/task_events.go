package store

// TaskEventRow is one persisted task event. event_json holds the full
// (already-redacted) task.TaskEvent; seq is the per-task monotonic cursor.
type TaskEventRow struct {
	TaskID    string
	Seq       uint64
	Kind      string
	EventJson string
	At        int64
}

// AppendTaskEvent appends an event with the next per-task seq
// (COALESCE(MAX(seq),0)+1) and returns the assigned seq. The runtime keeps a
// single sqlite connection (SetMaxOpenConns(1)) and there is one writer per
// task, so the read-modify-write is race-free.
func (db *DB) AppendTaskEvent(taskID, kind, eventJSON string, at int64) (uint64, error) {
	if _, err := db.Exec(
		`INSERT INTO task_events (task_id, seq, kind, event_json, at)
		 VALUES (?, (SELECT COALESCE(MAX(seq),0)+1 FROM task_events WHERE task_id=?), ?, ?, ?)`,
		taskID, taskID, kind, eventJSON, at); err != nil {
		return 0, err
	}
	var seq uint64
	err := db.QueryRow(`SELECT MAX(seq) FROM task_events WHERE task_id=?`, taskID).Scan(&seq)
	return seq, err
}

// TaskEvents returns events with seq > fromSeq, ordered by seq.
func (db *DB) TaskEvents(taskID string, fromSeq uint64) ([]TaskEventRow, error) {
	rows, err := db.Query(
		`SELECT task_id, seq, kind, event_json, at FROM task_events
		 WHERE task_id=? AND seq>? ORDER BY seq`, taskID, fromSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskEventRow
	for rows.Next() {
		var r TaskEventRow
		if err := rows.Scan(&r.TaskID, &r.Seq, &r.Kind, &r.EventJson, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteTaskEvents removes all events for a task.
func (db *DB) DeleteTaskEvents(taskID string) error {
	_, err := db.Exec(`DELETE FROM task_events WHERE task_id=?`, taskID)
	return err
}
