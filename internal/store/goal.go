package store

import "database/sql"

const sessionGoalGrantsSQL = `CREATE TABLE IF NOT EXISTS session_goal_grants (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 grant_json TEXT NOT NULL
);`

func (db *DB) SaveSessionGoalGrant(sessionID, value string) error {
	if value == "" {
		_, err := db.Exec(`DELETE FROM session_goal_grants WHERE session_id=?`, sessionID)
		return err
	}
	_, err := db.Exec(`INSERT INTO session_goal_grants(session_id,grant_json) VALUES(?,?) ON CONFLICT(session_id) DO UPDATE SET grant_json=excluded.grant_json`, sessionID, value)
	return err
}

func (db *DB) SessionGoalGrant(sessionID string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT grant_json FROM session_goal_grants WHERE session_id=?`, sessionID).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}
