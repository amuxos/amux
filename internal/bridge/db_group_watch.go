package bridge

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type groupWatchAlertDedup struct {
	BridgeID    string
	ChatID      string
	SenderID    string
	Fingerprint string
	Window      time.Duration
	Resume      *pb.Session
	ResumeClaim *groupWatchAlertClaim
}

func (a groupWatchAlertDedup) lockKey() string {
	return a.BridgeID + ":" + a.ChatID + ":" + a.SenderID + ":" + a.Fingerprint
}

type groupWatchAlertClaim struct {
	MessageID           string
	StartedAt           time.Time
	SubmissionStartedAt time.Time
	TargetID            string
	SessionName         string
}

// ReserveGroupWatchAlert serializes competing roots without treating a failed
// launch as an accepted alert. Pending outcomes are retained across restarts.
func (b *bridgeDB) ReserveGroupWatchAlert(a groupWatchAlertDedup, messageID, targetID, sessionName string, now time.Time) (bool, error) {
	if a.BridgeID == "" || a.ChatID == "" || a.SenderID == "" || a.Fingerprint == "" || messageID == "" {
		return false, fmt.Errorf("group watch alert identity is incomplete")
	}
	if _, err := b.db.Exec(`DELETE FROM feishu_group_watch_alert_dedup WHERE pending_message_id='' AND last_seen_at<=?`, now.Add(-24*time.Hour).Unix()); err != nil {
		return false, err
	}
	res, err := b.db.Exec(`INSERT INTO feishu_group_watch_alert_dedup
 (bridge_id,chat_id,sender_id,fingerprint,last_message_id,last_seen_at,pending_message_id,pending_at,target_id,session_name)
 VALUES (?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(bridge_id,chat_id,sender_id,fingerprint) DO UPDATE SET
 pending_message_id=excluded.pending_message_id,pending_at=excluded.pending_at,
 target_id=excluded.target_id,session_name=excluded.session_name,
 last_message_id=excluded.last_message_id,last_seen_at=excluded.last_seen_at
 WHERE feishu_group_watch_alert_dedup.pending_message_id='' AND feishu_group_watch_alert_dedup.accepted_at<=?`,
		a.BridgeID, a.ChatID, a.SenderID, a.Fingerprint, messageID, now.Unix(), messageID, now.Unix(), targetID, sessionName, now.Add(-a.Window).Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (b *bridgeDB) PendingGroupWatchAlert(a groupWatchAlertDedup) (*groupWatchAlertClaim, error) {
	var c groupWatchAlertClaim
	var started, submitted int64
	err := b.db.QueryRow(`SELECT pending_message_id,pending_at,target_id,session_name,submission_started_at FROM feishu_group_watch_alert_dedup
 WHERE bridge_id=? AND chat_id=? AND sender_id=? AND fingerprint=? AND pending_message_id<>''`,
		a.BridgeID, a.ChatID, a.SenderID, a.Fingerprint).Scan(&c.MessageID, &started, &c.TargetID, &c.SessionName, &submitted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.StartedAt = time.Unix(started, 0)
	if submitted != 0 {
		c.SubmissionStartedAt = time.Unix(submitted, 0)
	}
	return &c, nil
}

// Finish only changes the reservation owned by this message. A failed attempt
// preserves the previous accepted timestamp; suppression never slides it.
func (b *bridgeDB) FinishGroupWatchAlert(a groupWatchAlertDedup, messageID string, created bool, now time.Time) error {
	_, err := b.db.Exec(`UPDATE feishu_group_watch_alert_dedup SET
 accepted_at=CASE WHEN ? THEN ? ELSE accepted_at END,
 accepted_message_id=CASE WHEN ? THEN ? ELSE accepted_message_id END,
 suppressed_count=CASE WHEN ? THEN 0 ELSE suppressed_count END,
 pending_message_id='',pending_at=0,submission_started_at=0,target_id='',session_name=''
 WHERE bridge_id=? AND chat_id=? AND sender_id=? AND fingerprint=? AND pending_message_id=?`,
		created, now.Unix(), created, messageID, created, a.BridgeID, a.ChatID, a.SenderID, a.Fingerprint, messageID)
	return err
}

func (b *bridgeDB) ObserveGroupWatchDuplicate(a groupWatchAlertDedup, messageID string, now time.Time) error {
	_, err := b.db.Exec(`UPDATE feishu_group_watch_alert_dedup SET last_message_id=?,last_seen_at=?,suppressed_count=suppressed_count+1
 WHERE bridge_id=? AND chat_id=? AND sender_id=? AND fingerprint=?`, messageID, now.Unix(), a.BridgeID, a.ChatID, a.SenderID, a.Fingerprint)
	return err
}

func (b *bridgeDB) GroupWatchAlertAccepted(a groupWatchAlertDedup, now time.Time) (bool, error) {
	var acceptedAt int64
	err := b.db.QueryRow(`SELECT accepted_at FROM feishu_group_watch_alert_dedup WHERE bridge_id=? AND chat_id=? AND sender_id=? AND fingerprint=?`, a.BridgeID, a.ChatID, a.SenderID, a.Fingerprint).Scan(&acceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return acceptedAt > now.Add(-a.Window).Unix(), err
}

func (b *bridgeDB) GroupWatchAlertSubmitted(a groupWatchAlertDedup, c *groupWatchAlertClaim) (bool, error) {
	var count int
	err := b.db.QueryRow(`SELECT count(*) FROM feishu_turn_links WHERE bridge_id=? AND root_msg_id=? AND target_id=? AND session_name=?`, a.BridgeID, c.MessageID, c.TargetID, c.SessionName).Scan(&count)
	return count > 0, err
}

func (b *bridgeDB) ResumeGroupWatchAlert(a groupWatchAlertDedup, c *groupWatchAlertClaim, now time.Time) (bool, error) {
	res, err := b.db.Exec(`UPDATE feishu_group_watch_alert_dedup SET pending_at=? WHERE bridge_id=? AND chat_id=? AND sender_id=? AND fingerprint=? AND pending_message_id=? AND pending_at=?`, now.Unix(), a.BridgeID, a.ChatID, a.SenderID, a.Fingerprint, c.MessageID, c.StartedAt.Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (b *bridgeDB) StartGroupWatchAlertSubmission(a groupWatchAlertDedup, messageID string, now time.Time) error {
	_, err := b.db.Exec(`UPDATE feishu_group_watch_alert_dedup SET submission_started_at=? WHERE bridge_id=? AND chat_id=? AND sender_id=? AND fingerprint=? AND pending_message_id=?`, now.Unix(), a.BridgeID, a.ChatID, a.SenderID, a.Fingerprint, messageID)
	return err
}
