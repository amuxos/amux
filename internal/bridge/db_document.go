package bridge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (b *bridgeDB) shouldNotifyFeishuPermissionAudit(bridgeID, fingerprint string) (bool, error) {
	if b == nil || b.db == nil {
		return true, nil
	}
	bridgeID = strings.TrimSpace(bridgeID)
	fingerprint = strings.TrimSpace(fingerprint)
	if bridgeID == "" || fingerprint == "" {
		return false, fmt.Errorf("bridge_id and permission fingerprint are required")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var stored string
	var notifiedAt, claimUntil int64
	err = tx.QueryRow(`
		SELECT fingerprint, notified_at, claim_until
		FROM feishu_permission_audits
		WHERE bridge_id=?`, bridgeID).Scan(&stored, &notifiedAt, &claimUntil)
	now := time.Now().Unix()
	const claimTTLSeconds = int64(60)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.Exec(`
			INSERT INTO feishu_permission_audits (bridge_id, fingerprint, notified_at, claim_until)
			VALUES (?, ?, 0, ?)`, bridgeID, fingerprint, now+claimTTLSeconds)
	case err != nil:
		return false, err
	case stored != fingerprint:
		_, err = tx.Exec(`
			UPDATE feishu_permission_audits
			SET fingerprint=?, notified_at=0, claim_until=?, updated_at=strftime('%s','now')
			WHERE bridge_id=?`, fingerprint, now+claimTTLSeconds, bridgeID)
	case notifiedAt > 0 || claimUntil > now:
		return false, tx.Commit()
	default:
		_, err = tx.Exec(`
			UPDATE feishu_permission_audits
			SET claim_until=?, updated_at=strftime('%s','now')
			WHERE bridge_id=? AND fingerprint=?`, now+claimTTLSeconds, bridgeID, fingerprint)
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
func (b *bridgeDB) markFeishuPermissionAuditNotified(bridgeID, fingerprint string) error {
	if b == nil || b.db == nil {
		return nil
	}
	result, err := b.db.Exec(`
		UPDATE feishu_permission_audits
		SET notified_at=strftime('%s','now'), claim_until=0, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND fingerprint=?`, strings.TrimSpace(bridgeID), strings.TrimSpace(fingerprint))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("permission audit state changed before notification was recorded")
	}
	return nil
}
func (b *bridgeDB) releaseFeishuPermissionAuditClaim(bridgeID, fingerprint string) error {
	if b == nil || b.db == nil {
		return nil
	}
	_, err := b.db.Exec(`
		UPDATE feishu_permission_audits
		SET claim_until=0, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND fingerprint=? AND notified_at=0`, strings.TrimSpace(bridgeID), strings.TrimSpace(fingerprint))
	return err
}
func (b *bridgeDB) clearFeishuPermissionAudit(bridgeID string) error {
	if b == nil || b.db == nil {
		return nil
	}
	_, err := b.db.Exec(`DELETE FROM feishu_permission_audits WHERE bridge_id=?`, strings.TrimSpace(bridgeID))
	return err
}
func (b *bridgeDB) upsertOperationalPermissionIssue(bridgeID string, issue capabilityPermissionIssue) error {
	if b == nil || b.db == nil {
		return nil
	}
	bridgeID = strings.TrimSpace(bridgeID)
	capability := strings.TrimSpace(issue.Capability)
	scopes := normalizeFeishuScopes(issue.Scopes)
	if bridgeID == "" || capability == "" || len(scopes) == 0 {
		return fmt.Errorf("bridge_id, capability and scopes are required")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existingRaw string
	err = tx.QueryRow(`
		SELECT scopes_json FROM feishu_operational_permission_issues
		WHERE bridge_id=? AND capability=?`, bridgeID, capability).Scan(&existingRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		var existing []string
		if decodeErr := json.Unmarshal([]byte(existingRaw), &existing); decodeErr != nil {
			return decodeErr
		}
		scopes = normalizeFeishuScopes(append(scopes, existing...))
	}
	raw, err := json.Marshal(scopes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT INTO feishu_operational_permission_issues (bridge_id, capability, name, scopes_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(bridge_id, capability) DO UPDATE SET
			name=excluded.name,
			scopes_json=excluded.scopes_json,
			updated_at=strftime('%s','now')`, bridgeID, capability, strings.TrimSpace(issue.Name), string(raw))
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (b *bridgeDB) operationalPermissionIssues(bridgeID string) ([]capabilityPermissionIssue, error) {
	if b == nil || b.db == nil {
		return nil, nil
	}
	rows, err := b.db.Query(`
		SELECT capability, name, scopes_json
		FROM feishu_operational_permission_issues
		WHERE bridge_id=?
		ORDER BY capability`, strings.TrimSpace(bridgeID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var issues []capabilityPermissionIssue
	for rows.Next() {
		var issue capabilityPermissionIssue
		var raw string
		if err := rows.Scan(&issue.Capability, &issue.Name, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &issue.Scopes); err != nil {
			return nil, fmt.Errorf("decode operational permission issue %s: %w", issue.Capability, err)
		}
		issue.Scopes = normalizeFeishuScopes(issue.Scopes)
		if len(issue.Scopes) > 0 {
			issues = append(issues, issue)
		}
	}
	return issues, rows.Err()
}
func (b *bridgeDB) resolveOperationalPermissionScopes(bridgeID, capability string, resolved []string) error {
	if b == nil || b.db == nil {
		return nil
	}
	bridgeID = strings.TrimSpace(bridgeID)
	capability = strings.TrimSpace(capability)
	resolved = normalizeFeishuScopes(resolved)
	if bridgeID == "" || capability == "" || len(resolved) == 0 {
		return nil
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRow(`
		SELECT scopes_json FROM feishu_operational_permission_issues
		WHERE bridge_id=? AND capability=?`, bridgeID, capability).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	var current []string
	if err := json.Unmarshal([]byte(raw), &current); err != nil {
		return err
	}
	resolvedSet := make(map[string]bool, len(resolved))
	for _, scope := range resolved {
		resolvedSet[scope] = true
	}
	remaining := make([]string, 0, len(current))
	for _, scope := range normalizeFeishuScopes(current) {
		if !resolvedSet[scope] {
			remaining = append(remaining, scope)
		}
	}
	if len(remaining) == 0 {
		_, err = tx.Exec(`
			DELETE FROM feishu_operational_permission_issues
			WHERE bridge_id=? AND capability=?`, bridgeID, capability)
	} else {
		raw, marshalErr := json.Marshal(remaining)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.Exec(`
			UPDATE feishu_operational_permission_issues
			SET scopes_json=?, updated_at=strftime('%s','now')
			WHERE bridge_id=? AND capability=?`, string(raw), bridgeID, capability)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (b *bridgeDB) ClaimDocumentCommentEvent(event DocumentCommentEvent) (bool, error) {
	if b == nil || b.db == nil {
		return false, fmt.Errorf("bridge db unavailable")
	}
	event.BridgeID = strings.TrimSpace(event.BridgeID)
	event.EventID = strings.TrimSpace(event.EventID)
	if event.BridgeID == "" || event.EventID == "" {
		return false, fmt.Errorf("document comment bridge_id and event_id are required")
	}
	result, err := b.db.Exec(`
		INSERT OR IGNORE INTO feishu_document_events (
			bridge_id, event_id, file_token, file_type, comment_id, reply_id, open_id, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, event.BridgeID, event.EventID, strings.TrimSpace(event.FileToken), strings.TrimSpace(event.FileType), strings.TrimSpace(event.CommentID), strings.TrimSpace(event.ReplyID), strings.TrimSpace(event.OpenID), DocumentCommentEventProcessing)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}
func (b *bridgeDB) MarkDocumentCommentEventDone(bridgeID, eventID, replyID string) error {
	return b.markDocumentCommentEvent(bridgeID, eventID, DocumentCommentEventDone, replyID)
}
func (b *bridgeDB) TryBeginDocumentCommentEventFinalization(bridgeID, eventID string) (bool, error) {
	if b == nil || b.db == nil {
		return false, fmt.Errorf("bridge db unavailable")
	}
	result, err := b.db.Exec(`
		UPDATE feishu_document_events
		SET status=?, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND event_id=? AND status=?
	`, DocumentCommentEventFinalizing, strings.TrimSpace(bridgeID), strings.TrimSpace(eventID), DocumentCommentEventProcessing)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}
func (b *bridgeDB) MarkDocumentCommentEventFailed(bridgeID, eventID string) error {
	return b.markDocumentCommentEvent(bridgeID, eventID, DocumentCommentEventFailed, "")
}
func (b *bridgeDB) markDocumentCommentEvent(bridgeID, eventID, status, replyID string) error {
	if b == nil || b.db == nil {
		return fmt.Errorf("bridge db unavailable")
	}
	_, err := b.db.Exec(`
		UPDATE feishu_document_events
		SET status=?, final_reply_id=?, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND event_id=?
	`, status, strings.TrimSpace(replyID), strings.TrimSpace(bridgeID), strings.TrimSpace(eventID))
	return err
}
func (b *bridgeDB) UpsertDocumentCommentSession(binding DocumentCommentSession) error {
	if b == nil || b.db == nil {
		return fmt.Errorf("bridge db unavailable")
	}
	if strings.TrimSpace(binding.Status) == "" {
		binding.Status = DocumentCommentSessionActive
	}
	_, err := b.db.Exec(`
		INSERT INTO feishu_document_sessions (
			bridge_id, file_token, file_type, comment_id, target_id, target_name,
			session_name, agent_profile, agent_type, effective_model, created_by,
			status, last_activity_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(bridge_id, file_token, comment_id) DO UPDATE SET
			file_type=excluded.file_type,
			target_id=excluded.target_id,
			target_name=excluded.target_name,
			session_name=excluded.session_name,
			agent_profile=excluded.agent_profile,
			agent_type=excluded.agent_type,
			effective_model=excluded.effective_model,
			status=excluded.status,
			last_activity_at=strftime('%s','now')
	`, strings.TrimSpace(binding.BridgeID), strings.TrimSpace(binding.FileToken), strings.TrimSpace(binding.FileType), strings.TrimSpace(binding.CommentID), strings.TrimSpace(binding.TargetID), strings.TrimSpace(binding.TargetName), strings.TrimSpace(binding.SessionName), strings.TrimSpace(binding.AgentProfile), strings.TrimSpace(binding.AgentType), strings.TrimSpace(binding.EffectiveModel), strings.TrimSpace(binding.CreatedBy), strings.TrimSpace(binding.Status))
	return err
}
func (b *bridgeDB) FindDocumentCommentSession(bridgeID, fileToken, commentID string) (*DocumentCommentSession, error) {
	if b == nil || b.db == nil {
		return nil, fmt.Errorf("bridge db unavailable")
	}
	var out DocumentCommentSession
	var lastActivity int64
	err := b.db.QueryRow(`
		SELECT bridge_id, file_token, file_type, comment_id, target_id, target_name,
		       session_name, agent_profile, agent_type, effective_model, created_by,
		       status, last_activity_at
		FROM feishu_document_sessions
		WHERE bridge_id=? AND file_token=? AND comment_id=?
	`, strings.TrimSpace(bridgeID), strings.TrimSpace(fileToken), strings.TrimSpace(commentID)).Scan(&out.BridgeID, &out.FileToken, &out.FileType, &out.CommentID, &out.TargetID, &out.TargetName, &out.SessionName, &out.AgentProfile, &out.AgentType, &out.EffectiveModel, &out.CreatedBy, &out.Status, &lastActivity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out.LastActivity = time.Unix(lastActivity, 0)
	return &out, nil
}
func (b *bridgeDB) ConsumeManagementAction(actionKey string) (bool, error) {
	if b == nil || b.db == nil {
		return false, fmt.Errorf("bridge db unavailable")
	}
	actionKey = strings.TrimSpace(actionKey)
	if actionKey == "" {
		return false, fmt.Errorf("management action key is required")
	}
	result, err := b.db.Exec(`
		INSERT OR IGNORE INTO feishu_management_actions (action_key, created_at)
		VALUES (?, strftime('%s','now'))
	`, actionKey)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}
func (b *bridgeDB) UpsertPendingManagementRecovery(bridgeID, openID, messageID string) error {
	return b.UpsertPendingManagementRecoveryForOperation(bridgeID, openID, messageID, managementRecoveryRestart, "")
}
func (b *bridgeDB) UpsertPendingManagementRecoveryForOperation(bridgeID, openID, messageID, operation, expectedVersion string) error {
	return b.UpsertPendingManagementRecoveryForSurface(bridgeID, openID, messageID, operation, expectedVersion, "")
}
func (b *bridgeDB) UpsertPendingManagementRecoveryForSurface(bridgeID, openID, messageID, operation, expectedVersion, surface string) error {
	bridgeID = strings.TrimSpace(bridgeID)
	openID = strings.TrimSpace(openID)
	messageID = strings.TrimSpace(messageID)
	operation = strings.TrimSpace(operation)
	expectedVersion = strings.TrimSpace(expectedVersion)
	surface = strings.TrimSpace(surface)
	if bridgeID == "" {
		return fmt.Errorf("management recovery bridge_id is required")
	}
	if openID == "" {
		return fmt.Errorf("management recovery open_id is required")
	}
	if operation != managementRecoveryRestart && operation != managementRecoveryUpgrade {
		return fmt.Errorf("unsupported management recovery operation %q", operation)
	}
	if operation == managementRecoveryUpgrade && expectedVersion == "" {
		return fmt.Errorf("management recovery expected_version is required for upgrade")
	}
	_, err := b.db.Exec(`
		INSERT INTO feishu_management_recovery (
			bridge_id, open_id, message_id, operation, expected_version, surface, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(bridge_id) DO UPDATE SET
			open_id=excluded.open_id,
			message_id=excluded.message_id,
			operation=excluded.operation,
			expected_version=excluded.expected_version,
			surface=excluded.surface,
			created_at=excluded.created_at
	`, bridgeID, openID, messageID, operation, expectedVersion, surface)
	return err
}
func (b *bridgeDB) GetPendingManagementRecovery(bridgeID string) (*PendingManagementRecovery, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	if bridgeID == "" {
		return nil, fmt.Errorf("management recovery bridge_id is required")
	}
	var recovery PendingManagementRecovery
	var createdAt int64
	err := b.db.QueryRow(`
		SELECT bridge_id, open_id, message_id, operation, expected_version, surface, created_at
		FROM feishu_management_recovery
		WHERE bridge_id=?
	`, bridgeID).Scan(&recovery.BridgeID, &recovery.OpenID, &recovery.MessageID, &recovery.Operation, &recovery.ExpectedVersion, &recovery.Surface, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	recovery.CreatedAt = time.Unix(createdAt, 0)
	return &recovery, nil
}
func (b *bridgeDB) DeletePendingManagementRecovery(bridgeID, messageID string) error {
	bridgeID = strings.TrimSpace(bridgeID)
	messageID = strings.TrimSpace(messageID)
	if bridgeID == "" {
		return fmt.Errorf("management recovery bridge_id is required")
	}
	_, err := b.db.Exec(`
		DELETE FROM feishu_management_recovery
		WHERE bridge_id=? AND message_id=?
	`, bridgeID, messageID)
	return err
}
