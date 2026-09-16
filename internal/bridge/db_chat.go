package bridge

import (
	"database/sql"
	"fmt"
	"strings"
)

type PendingBotHandoff struct {
	BotOpenID string
	MessageID string
}

func (b *bridgeDB) TopicRootPromptDelivered(bridgeID, rootMsgID string) (bool, error) {
	var delivered bool
	err := b.db.QueryRow(`
		SELECT topic_root_prompt_delivered
		FROM lark_chats
		WHERE bridge_id=? AND root_msg_id=?`, strings.TrimSpace(bridgeID), strings.TrimSpace(rootMsgID)).Scan(&delivered)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return delivered, err
}
func (b *bridgeDB) MarkTopicRootPromptDelivered(bridgeID, rootMsgID, targetID, sessionName string) (bool, error) {
	result, err := b.db.Exec(`
		UPDATE lark_chats
		SET topic_root_prompt_delivered=1
		WHERE bridge_id=? AND root_msg_id=? AND target_id=? AND session_name=?`, strings.TrimSpace(bridgeID), strings.TrimSpace(rootMsgID), strings.TrimSpace(targetID), strings.TrimSpace(sessionName))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}
func (b *bridgeDB) FindByRootMsg(bridgeID, rootMsgID string) (*LarkChat, error) {
	row := b.db.QueryRow(`
		SELECT bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, display_title, status, last_activity_at
		FROM lark_chats WHERE bridge_id = ? AND root_msg_id = ?`, bridgeID, rootMsgID)
	return scanChat(row)
}
func (b *bridgeDB) FindChatEntrySource(bridgeID, rootMsgID string) (string, error) {
	var source string
	err := b.db.QueryRow(`
		SELECT source
		FROM feishu_binding_sources
		WHERE bridge_id = ? AND root_msg_id = ?`,
		strings.TrimSpace(bridgeID), strings.TrimSpace(rootMsgID),
	).Scan(&source)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return strings.TrimSpace(source), err
}
func (b *bridgeDB) FindBySession(targetID, sessionName string) (*LarkChat, error) {
	row := b.db.QueryRow(`
		SELECT bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, display_title, status, last_activity_at
		FROM lark_chats WHERE target_id = ? AND session_name = ? AND status = 'active'
		ORDER BY created_at DESC LIMIT 1`, targetID, sessionName)
	return scanChat(row)
}
func (b *bridgeDB) FindByBridgeSession(bridgeID, targetID, sessionName string) (*LarkChat, error) {
	row := b.db.QueryRow(`
		SELECT bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, display_title, status, last_activity_at
		FROM lark_chats WHERE bridge_id = ? AND target_id = ? AND session_name = ? AND status = 'active'
		ORDER BY created_at DESC LIMIT 1`, bridgeID, targetID, sessionName)
	return scanChat(row)
}

func (b *bridgeDB) FindRecentBridgeSession(bridgeID, targetID, sessionName string, sinceUnix int64) (*LarkChat, error) {
	row := b.db.QueryRow(`
		SELECT bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, display_title, status, last_activity_at
		FROM lark_chats
		WHERE bridge_id = ? AND target_id = ? AND session_name = ? AND status = 'active' AND last_activity_at >= ?
		ORDER BY last_activity_at DESC, created_at DESC LIMIT 1`, bridgeID, targetID, sessionName, sinceUnix)
	return scanChat(row)
}
func (b *bridgeDB) FindAnyBySession(targetID, sessionName string) (*LarkChat, error) {
	row := b.db.QueryRow(`
		SELECT bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, display_title, status, last_activity_at
		FROM lark_chats WHERE target_id = ? AND session_name = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, targetID, sessionName)
	return scanChat(row)
}
func (b *bridgeDB) DetachActiveTopicBindingForSession(targetID, sessionName string) (*LarkChat, error) {
	chat, err := b.FindBySession(targetID, sessionName)
	if err != nil || chat == nil {
		return chat, err
	}
	_, err = b.db.Exec(`UPDATE lark_chats SET status='detached' WHERE target_id=? AND session_name=? AND status='active'`, targetID, sessionName)
	return chat, err
}
func (b *bridgeDB) DeactivateChat(bridgeID, rootMsgID string) error {
	_, err := b.db.Exec(`UPDATE lark_chats SET status='detached' WHERE bridge_id=? AND root_msg_id=?`, bridgeID, rootMsgID)
	return err
}
func (b *bridgeDB) TerminateChat(bridgeID, rootMsgID string) error {
	_, err := b.db.Exec(`UPDATE lark_chats SET status='terminal' WHERE bridge_id=? AND root_msg_id=?`, bridgeID, rootMsgID)
	return err
}
func (b *bridgeDB) ActivateChat(bridgeID, rootMsgID string) error {
	chat, err := b.FindByRootMsg(bridgeID, rootMsgID)
	if err != nil || chat == nil {
		return err
	}
	return b.UpsertChat(*chat)
}
func (b *bridgeDB) SetLatestAgentOutput(bridgeID, rootMsgID, text string) error {
	_, err := b.db.Exec(`UPDATE lark_chats SET latest_agent_output=?, last_activity_at=strftime('%s','now') WHERE bridge_id=? AND root_msg_id=?`, text, bridgeID, rootMsgID)
	return err
}
func (b *bridgeDB) TouchChat(bridgeID, rootMsgID string) error {
	_, err := b.db.Exec(`UPDATE lark_chats SET last_activity_at=strftime('%s','now') WHERE bridge_id=? AND root_msg_id=?`, bridgeID, rootMsgID)
	return err
}
func (b *bridgeDB) MarkMessageSeen(bridgeID, messageID, rootMsgID, chatID string) (bool, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	messageID = strings.TrimSpace(messageID)
	if bridgeID == "" {
		return false, fmt.Errorf("seen message bridge_id is required")
	}
	if messageID == "" {
		return true, nil
	}
	res, err := b.db.Exec(`
		INSERT OR IGNORE INTO feishu_seen_messages (bridge_id, message_id, root_msg_id, chat_id)
		VALUES (?, ?, ?, ?)`, bridgeID, messageID, strings.TrimSpace(rootMsgID), strings.TrimSpace(chatID))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return true, nil
	}
	return n > 0, nil
}
func (b *bridgeDB) ClaimImageNotice(bridgeID, targetID, sessionName, effectiveModel, reason string) (bool, error) {
	res, err := b.db.Exec(`
		INSERT OR IGNORE INTO feishu_image_notices
		(bridge_id, target_id, session_name, effective_model, reason)
		VALUES (?, ?, ?, ?, ?)`, strings.TrimSpace(bridgeID), strings.TrimSpace(targetID), strings.TrimSpace(sessionName), strings.TrimSpace(effectiveModel), strings.TrimSpace(reason))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return true, nil
	}
	return n > 0, nil
}

func (b *bridgeDB) ClaimBotTurn(bridgeID, chatID, rootMsgID, messageID, botOpenID string, maxTurns int) (BotTurnClaim, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	rootMsgID = strings.TrimSpace(rootMsgID)
	messageID = strings.TrimSpace(messageID)
	botOpenID = strings.TrimSpace(botOpenID)
	if bridgeID == "" || rootMsgID == "" || messageID == "" || botOpenID == "" {
		return BotTurnClaim{}, fmt.Errorf("bot turn requires bridge_id, root_msg_id, message_id, and bot_open_id")
	}
	if maxTurns <= 0 {
		return BotTurnClaim{}, fmt.Errorf("bot turn max_turns must be positive")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return BotTurnClaim{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var turn int
	var pendingMessageID string
	err = tx.QueryRow(`SELECT turn_count, pending_message_id FROM feishu_bot_threads WHERE bridge_id=? AND root_msg_id=?`, bridgeID, rootMsgID).Scan(&turn, &pendingMessageID)
	if err != nil && err != sql.ErrNoRows {
		return BotTurnClaim{}, err
	}
	if pendingMessageID != "" && pendingMessageID != messageID {
		if err := tx.Commit(); err != nil {
			return BotTurnClaim{}, err
		}
		return BotTurnClaim{Busy: true, Turn: turn}, nil
	}
	res, err := tx.Exec(`
		INSERT OR IGNORE INTO feishu_seen_messages (bridge_id, message_id, root_msg_id, chat_id)
		VALUES (?, ?, ?, ?)`, bridgeID, messageID, rootMsgID, strings.TrimSpace(chatID))
	if err != nil {
		return BotTurnClaim{}, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return BotTurnClaim{}, err
	}
	if inserted == 0 {
		if err := tx.Commit(); err != nil {
			return BotTurnClaim{}, err
		}
		return BotTurnClaim{Turn: turn}, nil
	}
	if turn >= maxTurns {
		if err := tx.Commit(); err != nil {
			return BotTurnClaim{}, err
		}
		return BotTurnClaim{Exceeded: true, Turn: turn}, nil
	}
	turn++
	_, err = tx.Exec(`
		INSERT INTO feishu_bot_threads
			(bridge_id, root_msg_id, turn_count, pending_bot_open_id, pending_message_id, updated_at)
		VALUES (?, ?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(bridge_id, root_msg_id) DO UPDATE SET
			turn_count=excluded.turn_count,
			pending_bot_open_id=excluded.pending_bot_open_id,
			pending_message_id=excluded.pending_message_id,
			updated_at=excluded.updated_at`, bridgeID, rootMsgID, turn, botOpenID, messageID)
	if err != nil {
		return BotTurnClaim{}, err
	}
	if err := tx.Commit(); err != nil {
		return BotTurnClaim{}, err
	}
	return BotTurnClaim{Accepted: true, Turn: turn}, nil
}
func (b *bridgeDB) PendingBotHandoff(bridgeID, rootMsgID string) (*PendingBotHandoff, error) {
	var pending PendingBotHandoff
	err := b.db.QueryRow(`
		SELECT pending_bot_open_id, pending_message_id
		FROM feishu_bot_threads WHERE bridge_id=? AND root_msg_id=?`, strings.TrimSpace(bridgeID), strings.TrimSpace(rootMsgID)).Scan(&pending.BotOpenID, &pending.MessageID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pending.BotOpenID == "" || pending.MessageID == "" {
		return nil, nil
	}
	return &pending, nil
}
func (b *bridgeDB) ClearPendingBotHandoff(bridgeID, rootMsgID, messageID string) (bool, error) {
	res, err := b.db.Exec(`
		UPDATE feishu_bot_threads
		SET pending_bot_open_id='', pending_message_id='', updated_at=strftime('%s','now')
		WHERE bridge_id=? AND root_msg_id=? AND pending_message_id=?`, strings.TrimSpace(bridgeID), strings.TrimSpace(rootMsgID), strings.TrimSpace(messageID))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
func (b *bridgeDB) ClearPendingBotHandoffs(bridgeID string) error {
	_, err := b.db.Exec(`
		UPDATE feishu_bot_threads
		SET pending_bot_open_id='', pending_message_id='', updated_at=strftime('%s','now')
		WHERE bridge_id=? AND pending_message_id<>''`, strings.TrimSpace(bridgeID))
	return err
}
func (b *bridgeDB) LatestAgentOutput(bridgeID, rootMsgID string) (string, error) {
	var out string
	err := b.db.QueryRow(`SELECT latest_agent_output FROM lark_chats WHERE bridge_id=? AND root_msg_id=?`, bridgeID, rootMsgID).Scan(&out)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return out, err
}
func (b *bridgeDB) LatestAgentOutputsByRoot(rootMsgID, excludeBridgeId string, limit int) ([]AgentThreadOutput, // ClaimBotTurn atomically deduplicates one inbound Bot message and advances
	// the durable per-topic collaboration counter. Exceeded messages are still
	// marked seen, preventing retries from repeatedly emitting the limit notice.
	error) {
	rootMsgID = strings.TrimSpace(rootMsgID)
	if rootMsgID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := b.db.Query(`
		SELECT bridge_id, target_name, session_name, latest_agent_output
		FROM lark_chats
		WHERE root_msg_id=?
		  AND bridge_id<>?
		  AND status='active'
		  AND TRIM(latest_agent_output)<>''
		ORDER BY id DESC
		LIMIT ?`, rootMsgID, excludeBridgeId, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentThreadOutput
	for rows.Next() {
		var item AgentThreadOutput
		if err := rows.Scan(&item.BridgeId, &item.TargetName, &item.SessionName, &item.Text); err != nil {
			return nil, err
		}
		item.Text = strings.TrimSpace(item.Text)
		if item.Text != "" {
			out = append(out, item)
		}
	}
	return out, rows.Err()
}
func (b *bridgeDB) CountActiveBindings() (int, error) {
	var count int
	err := b.db.QueryRow(`SELECT COUNT(*) FROM lark_chats WHERE status='active'`).Scan(&count)
	return count, err
}
func (b *bridgeDB) CountActiveBindingsByOpenId(openID string) (int, error) {
	var count int
	err := b.db.QueryRow(`SELECT COUNT(*) FROM lark_chats WHERE status='active' AND open_id=?`, openID).Scan(&count)
	return count, err
}
func (b *bridgeDB) CountActiveBindingsByChatID(bridgeID, chatID string) (int, error) {
	var count int
	err := b.db.QueryRow(`SELECT COUNT(*) FROM lark_chats WHERE status='active' AND bridge_id=? AND chat_id=?`, bridgeID, chatID).Scan(&count)
	return count, err
}
func (b *bridgeDB) CountActiveBindingsByChatIDAndOpenId(bridgeID, chatID, openID string) (int, error) {
	var count int
	err := b.db.QueryRow(`SELECT COUNT(*) FROM lark_chats WHERE status='active' AND bridge_id=? AND chat_id=? AND open_id=?`, bridgeID, chatID, openID).Scan(&count)
	return count, err
}
func (b *bridgeDB) ListGroupWatchCapacityBindingsByChatID(bridgeID, chatID string) ([]LarkChat, error) {
	rows, err := b.db.Query(`
		SELECT c.bridge_id, c.chat_id, c.root_msg_id, c.thread_id, c.open_id,
		       c.target_id, c.target_name, c.session_name, c.agent_profile, c.agent_type,
		       c.effective_model, c.display_title, c.status, c.last_activity_at,
		       COALESCE(s.source, '')
		FROM lark_chats c
		LEFT JOIN feishu_binding_sources s
		  ON s.bridge_id=c.bridge_id AND s.root_msg_id=c.root_msg_id
		WHERE c.bridge_id=? AND c.chat_id=? AND c.status<>'terminal'
		  AND (s.source=? OR s.source IS NULL)
		ORDER BY c.last_activity_at ASC, c.id ASC`, strings.TrimSpace(bridgeID), strings.TrimSpace(chatID), ChatEntrySourceGroupWatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LarkChat
	for rows.Next() {
		var c LarkChat
		if err := rows.Scan(&c.BridgeId, &c.ChatID, &c.RootMsgID, &c.ThreadID, &c.OpenId, &c.TargetID, &c.TargetName, &c.SessionName, &c.AgentProfile, &c.AgentType, &c.EffectiveModel, &c.DisplayTitle, &c.Status, &c.LastActivity, &c.EntrySource); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (b *bridgeDB) ListWorkspaceBindingsByChatID(bridgeID, chatID string) ([]LarkChat, error) {
	rows, err := b.db.Query(`
		SELECT bridge_id, chat_id, root_msg_id, thread_id, open_id,
		       target_id, target_name, session_name, agent_profile, agent_type,
		       effective_model, display_title, status, last_activity_at
		FROM lark_chats
		WHERE bridge_id=? AND chat_id=? AND status<>'terminal'
		ORDER BY last_activity_at ASC, id ASC`, strings.TrimSpace(bridgeID), strings.TrimSpace(chatID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LarkChat
	for rows.Next() {
		var c LarkChat
		if err := rows.Scan(&c.BridgeId, &c.ChatID, &c.RootMsgID, &c.ThreadID, &c.OpenId, &c.TargetID, &c.TargetName, &c.SessionName, &c.AgentProfile, &c.AgentType, &c.EffectiveModel, &c.DisplayTitle, &c.Status, &c.LastActivity); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (b *bridgeDB) NormalizeActiveBindings() error {
	_, err := b.db.Exec(`
		UPDATE lark_chats
		SET status='detached'
		WHERE status='active'
		  AND id NOT IN (
		    SELECT keep_id FROM (
		      SELECT MAX(id) AS keep_id
		      FROM lark_chats
		      WHERE status='active'
		      GROUP BY bridge_id, root_msg_id
		    )
		  )`)
	if err != nil {
		return err
	}
	_, err = b.db.Exec(`
		UPDATE lark_chats
		SET status='detached'
		WHERE status='active'
		  AND id NOT IN (
		    SELECT keep_id FROM (
		      SELECT MAX(id) AS keep_id
		      FROM lark_chats
		      WHERE status='active'
		      GROUP BY target_id, session_name
		    )
		  )`)
	return err
}
func scanChat(row *sql.Row) (*LarkChat, error) {
	var c LarkChat
	err := row.Scan(&c.BridgeId, &c.ChatID, &c.RootMsgID, &c.ThreadID, &c.OpenId, &c.TargetID, &c.TargetName, &c.SessionName, &c.AgentProfile, &c.AgentType, &c.EffectiveModel, &c.DisplayTitle, &c.Status, &c.LastActivity)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// NormalizeActiveBindings enforces the current product invariant on existing DB rows:
// one active binding per Feishu root and one active binding per target/session.
