package bridge

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func (b *bridgeDB) RecordTurnLinks(links []TurnLink) error {
	if b == nil || b.db == nil || len(links) == 0 {
		return nil
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, link := range links {
		if strings.TrimSpace(link.MessageID) == "" || strings.TrimSpace(link.TurnID) == "" {
			continue
		}
		at := link.SubmittedAt
		if at.IsZero() {
			at = time.Now()
		}
		if _, err := tx.Exec(`
			INSERT INTO feishu_turn_links
				(bridge_id, root_msg_id, message_id, open_id, target_id, session_id, session_name, launch_id, turn_id, submitted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(bridge_id, message_id) DO UPDATE SET
				root_msg_id=excluded.root_msg_id, open_id=excluded.open_id, target_id=excluded.target_id,
				session_id=excluded.session_id, session_name=excluded.session_name,
				launch_id=excluded.launch_id, turn_id=excluded.turn_id,
				submitted_at=excluded.submitted_at`,
			link.BridgeID, link.RootMsgID, link.MessageID, link.OpenID, link.TargetID, link.SessionID,
			link.SessionName, link.LaunchID, link.TurnID, at.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LookupTurnLinks opens the Bridge DB in read-only mode and returns durable
// message-to-turn receipts matching every populated query field.
func LookupTurnLinks(path string, query TurnLinkQuery) ([]TurnLink, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("bridge db path is required")
	}
	db, err := sql.Open("sqlite", bridgeReadOnlyDSN(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	clauses := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if value := strings.TrimSpace(query.MessageID); value != "" {
		clauses = append(clauses, "message_id=?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.TurnID); value != "" {
		clauses = append(clauses, "turn_id=?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.SessionID); value != "" {
		clauses = append(clauses, "session_id=?")
		args = append(args, value)
	}
	if len(clauses) == 0 {
		return nil, fmt.Errorf("a turn-link query id is required")
	}
	rows, err := db.Query(`SELECT bridge_id, root_msg_id, message_id, open_id, target_id, session_id,
		session_name, launch_id, turn_id, submitted_at FROM feishu_turn_links WHERE `+
		strings.Join(clauses, " AND ")+` ORDER BY submitted_at, message_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []TurnLink
	for rows.Next() {
		var link TurnLink
		var submittedAt int64
		if err := rows.Scan(&link.BridgeID, &link.RootMsgID, &link.MessageID, &link.OpenID, &link.TargetID,
			&link.SessionID, &link.SessionName, &link.LaunchID, &link.TurnID, &submittedAt); err != nil {
			return nil, err
		}
		link.SubmittedAt = time.Unix(submittedAt, 0)
		links = append(links, link)
	}
	return links, rows.Err()
}

func (b *bridgeDB) TurnNotificationRecipients(bridgeID, turnID string) ([]string, error) {
	if b == nil || b.db == nil || strings.TrimSpace(turnID) == "" {
		return nil, nil
	}
	rows, err := b.db.Query(`
		SELECT open_id
		FROM feishu_turn_links
		WHERE bridge_id = ? AND turn_id = ? AND open_id <> ''
		GROUP BY open_id
		ORDER BY MIN(submitted_at), open_id`, strings.TrimSpace(bridgeID), strings.TrimSpace(turnID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recipients []string
	for rows.Next() {
		var openID string
		if err := rows.Scan(&openID); err != nil {
			return nil, err
		}
		if openID = strings.TrimSpace(openID); openID != "" {
			recipients = append(recipients, openID)
		}
	}
	return recipients, rows.Err()
}

func (b *bridgeDB) TurnSubmittedAt(bridgeID, turnID string) (time.Time, error) {
	if b == nil || b.db == nil {
		return time.Time{}, nil
	}
	var submittedAt sql.NullInt64
	err := b.db.QueryRow(`SELECT MIN(submitted_at) FROM feishu_turn_links WHERE bridge_id = ? AND turn_id = ?`,
		strings.TrimSpace(bridgeID), strings.TrimSpace(turnID)).Scan(&submittedAt)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if !submittedAt.Valid {
		return time.Time{}, nil
	}
	return time.Unix(submittedAt.Int64, 0), nil
}

func (b *bridgeDB) ClaimTurnNotification(bridgeID, turnID, kind string) (bool, error) {
	if b == nil || b.db == nil {
		return false, nil
	}
	result, err := b.db.Exec(`
		INSERT OR IGNORE INTO feishu_turn_notifications (bridge_id, turn_id, kind, sent_at)
		VALUES (?, ?, ?, ?)`, strings.TrimSpace(bridgeID), strings.TrimSpace(turnID), strings.TrimSpace(kind), time.Now().Unix())
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err

}

func (b *bridgeDB) ReleaseTurnNotification(bridgeID, turnID, kind string) error {
	if b == nil || b.db == nil {
		return nil
	}
	_, err := b.db.Exec(`DELETE FROM feishu_turn_notifications WHERE bridge_id = ? AND turn_id = ? AND kind = ?`,
		strings.TrimSpace(bridgeID), strings.TrimSpace(turnID), strings.TrimSpace(kind))
	return err
}

func bridgeReadOnlyDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%smode=ro&_pragma=busy_timeout%%3d%d", path, separator, bridgeDBBusyTimeoutMS)
}
