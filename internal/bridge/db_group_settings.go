package bridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
)

var errGroupSettingsChanged = errors.New("group settings changed")

// GroupSettings is the complete saved policy for one Bot in one group. An empty
// DefaultAgent inherits the Bot default; other empty execution selections use
// the selected Profile's native defaults.
type GroupSettings struct {
	BridgeID               string    `json:"bridge_id"`
	ChatID                 string    `json:"chat_id"`
	InteractionMode        string    `json:"interaction_mode"`
	IsolationRequired      bool      `json:"isolation_required"`
	DefaultAgent           string    `json:"default_agent"`
	DefaultRuntimeRef      string    `json:"default_runtime_ref"`
	DefaultModelRef        string    `json:"default_model_ref"`
	DefaultReasoningEffort string    `json:"default_reasoning_effort"`
	ThinkingProcess        string    `json:"thinking_process"`
	Mode                   string    `json:"mode"`
	DefaultTarget          string    `json:"default_target"`
	DefaultCwd             string    `json:"default_cwd"`
	DefaultArgs            []string  `json:"default_args"`
	IdleTimeoutSeconds     int       `json:"idle_timeout_seconds"`
	ThreadContextLimit     int       `json:"thread_context_limit"`
	MaxSessionsTotal       int       `json:"max_sessions_total"`
	MaxSessionsPerUser     int       `json:"max_sessions_per_user"`
	Version                int64     `json:"version"`
	UpdatedBy              string    `json:"updated_by"`
	UpdatedAt              time.Time `json:"updated_at"`
}

func (b *bridgeDB) migrateGroupSettings() error {
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_group_settings (
			bridge_id TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			interaction_mode TEXT NOT NULL CHECK (interaction_mode IN ('thread', 'singleton')),
			isolation_required INTEGER NOT NULL CHECK (isolation_required IN (0, 1)),
			default_agent TEXT NOT NULL,
			default_runtime_ref TEXT NOT NULL,
			default_model_ref TEXT NOT NULL,
			default_reasoning_effort TEXT NOT NULL,
			thinking_process TEXT NOT NULL,
			mode TEXT NOT NULL CHECK (mode IN ('owner_only', 'shared')),
			default_target TEXT NOT NULL,
			default_cwd TEXT NOT NULL,
			default_args_json TEXT NOT NULL,
			idle_timeout_seconds INTEGER NOT NULL CHECK (idle_timeout_seconds >= 0),
			thread_context_limit INTEGER NOT NULL CHECK (thread_context_limit >= 0),
			max_sessions_total INTEGER NOT NULL CHECK (max_sessions_total >= 0),
			max_sessions_per_user INTEGER NOT NULL CHECK (max_sessions_per_user >= 0),
			state_version INTEGER NOT NULL CHECK (state_version > 0),
			updated_by TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			session_root_msg_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (bridge_id, chat_id),
			CHECK (interaction_mode = 'thread' OR isolation_required = 0)
		);
		CREATE TABLE IF NOT EXISTS feishu_group_session_origins (
			bridge_id TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			origin_root_msg_id TEXT NOT NULL,
			session_root_msg_id TEXT NOT NULL,
			PRIMARY KEY (bridge_id, chat_id, origin_root_msg_id)
		)`)
	return err
}

// ReadGroupSettings never seeds a row. Nil means the group still follows the
// legacy Bot-wide policy.
func (b *bridgeDB) ReadGroupSettings(bridgeID, chatID string) (*GroupSettings, error) {
	return b.ReadGroupSettingsContext(context.Background(), bridgeID, chatID)
}

func (b *bridgeDB) ReadGroupSettingsContext(ctx context.Context, bridgeID, chatID string) (*GroupSettings, error) {
	bridgeID, chatID = strings.TrimSpace(bridgeID), strings.TrimSpace(chatID)
	if err := validateGroupSettingsScope(bridgeID, chatID); err != nil {
		return nil, err
	}
	var settings GroupSettings
	var argsJSON string
	var updatedAt int64
	err := b.db.QueryRowContext(ctx, `
		SELECT bridge_id, chat_id, interaction_mode, isolation_required,
			default_agent, default_runtime_ref, default_model_ref, default_reasoning_effort,
			thinking_process, mode, default_target, default_cwd, default_args_json,
			idle_timeout_seconds, thread_context_limit, max_sessions_total, max_sessions_per_user,
			state_version, updated_by, updated_at
		FROM feishu_group_settings WHERE bridge_id=? AND chat_id=?`, bridgeID, chatID).Scan(
		&settings.BridgeID, &settings.ChatID, &settings.InteractionMode, &settings.IsolationRequired,
		&settings.DefaultAgent, &settings.DefaultRuntimeRef, &settings.DefaultModelRef, &settings.DefaultReasoningEffort,
		&settings.ThinkingProcess, &settings.Mode, &settings.DefaultTarget, &settings.DefaultCwd, &argsJSON,
		&settings.IdleTimeoutSeconds, &settings.ThreadContextLimit, &settings.MaxSessionsTotal, &settings.MaxSessionsPerUser,
		&settings.Version, &settings.UpdatedBy, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	settings.DefaultArgs, err = decodeWorkspaceDefaultArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	settings.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &settings, nil
}

// SaveGroupSettings atomically replaces the complete snapshot and projects its
// workspace fields for existing access, context and capacity queries. The group
// snapshot remains the authority for saved defaults.
func (b *bridgeDB) SaveGroupSettings(settings GroupSettings, expectedVersion int64) (*GroupSettings, error) {
	return b.SaveGroupSettingsContext(context.Background(), settings, expectedVersion)
}

// SaveGroupSettingsContext cancels connection-pool and transaction waits with ctx.
func (b *bridgeDB) SaveGroupSettingsContext(ctx context.Context, settings GroupSettings, expectedVersion int64) (*GroupSettings, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	settings = normalizeGroupSettings(settings)
	if err := validateGroupSettings(settings); err != nil {
		return nil, err
	}
	if expectedVersion < 0 {
		return nil, fmt.Errorf("group settings expected version must not be negative")
	}
	argsJSON, err := encodeWorkspaceDefaultArgs(settings.DefaultArgs)
	if err != nil {
		return nil, err
	}
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	settings.Version = expectedVersion + 1
	settings.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	var result sql.Result
	args := []any{
		settings.InteractionMode, boolToInt(settings.IsolationRequired), settings.DefaultAgent,
		settings.DefaultRuntimeRef, settings.DefaultModelRef, settings.DefaultReasoningEffort,
		settings.ThinkingProcess, settings.Mode, settings.DefaultTarget, settings.DefaultCwd, argsJSON,
		settings.IdleTimeoutSeconds, settings.ThreadContextLimit, settings.MaxSessionsTotal,
		settings.MaxSessionsPerUser, settings.Version, settings.UpdatedBy, settings.UpdatedAt.Unix(),
	}
	if expectedVersion == 0 {
		result, err = tx.ExecContext(ctx, `
			INSERT INTO feishu_group_settings (
				interaction_mode, isolation_required, default_agent, default_runtime_ref,
				default_model_ref, default_reasoning_effort, thinking_process, mode,
				default_target, default_cwd, default_args_json, idle_timeout_seconds,
				thread_context_limit, max_sessions_total, max_sessions_per_user,
				state_version, updated_by, updated_at, bridge_id, chat_id
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(bridge_id, chat_id) DO NOTHING`, append(args, settings.BridgeID, settings.ChatID)...)
	} else {
		result, err = tx.ExecContext(ctx, `
			UPDATE feishu_group_settings SET
				session_root_msg_id=CASE WHEN interaction_mode<>? THEN '' ELSE session_root_msg_id END,
				interaction_mode=?, isolation_required=?, default_agent=?, default_runtime_ref=?,
				default_model_ref=?, default_reasoning_effort=?, thinking_process=?, mode=?,
				default_target=?, default_cwd=?, default_args_json=?, idle_timeout_seconds=?,
				thread_context_limit=?, max_sessions_total=?, max_sessions_per_user=?,
				state_version=?, updated_by=?, updated_at=?
			WHERE bridge_id=? AND chat_id=? AND state_version=?`,
			append(append([]any{settings.InteractionMode}, args...), settings.BridgeID, settings.ChatID, expectedVersion)...)
	}
	if err != nil {
		return nil, err
	}
	if err := requireGroupSettingsUpdate(result); err != nil {
		return nil, err
	}
	result, err = tx.ExecContext(ctx, `
		INSERT INTO feishu_workspaces (
			bridge_id, chat_id, kind, mode, default_agent, default_target, default_cwd,
			default_model, default_args_json, idle_timeout_seconds, thread_context_limit,
			max_sessions_total, max_sessions_per_user, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bridge_id, chat_id) DO UPDATE SET
			mode=excluded.mode, default_agent=excluded.default_agent,
			default_target=excluded.default_target, default_cwd=excluded.default_cwd,
			default_model=excluded.default_model, default_args_json=excluded.default_args_json,
			idle_timeout_seconds=excluded.idle_timeout_seconds,
			thread_context_limit=excluded.thread_context_limit,
			max_sessions_total=excluded.max_sessions_total,
			max_sessions_per_user=excluded.max_sessions_per_user, updated_at=excluded.updated_at
		WHERE feishu_workspaces.kind IN ('group', 'topic_group')`,
		settings.BridgeID, settings.ChatID, WorkspaceKindGroup, settings.Mode, settings.DefaultAgent,
		settings.DefaultTarget, settings.DefaultCwd, settings.DefaultModelRef, argsJSON,
		settings.IdleTimeoutSeconds, settings.ThreadContextLimit, settings.MaxSessionsTotal,
		settings.MaxSessionsPerUser, settings.UpdatedAt.Unix())
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, fmt.Errorf("group settings require a group workspace")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &settings, nil
}

func normalizeGroupSettings(settings GroupSettings) GroupSettings {
	settings.BridgeID = strings.TrimSpace(settings.BridgeID)
	settings.ChatID = strings.TrimSpace(settings.ChatID)
	settings.InteractionMode = strings.TrimSpace(settings.InteractionMode)
	settings.DefaultAgent = strings.TrimSpace(settings.DefaultAgent)
	settings.DefaultRuntimeRef = strings.TrimSpace(settings.DefaultRuntimeRef)
	settings.DefaultModelRef = strings.TrimSpace(settings.DefaultModelRef)
	settings.DefaultReasoningEffort = strings.TrimSpace(settings.DefaultReasoningEffort)
	settings.ThinkingProcess = strings.TrimSpace(settings.ThinkingProcess)
	settings.Mode = strings.TrimSpace(settings.Mode)
	settings.DefaultTarget = strings.TrimSpace(settings.DefaultTarget)
	settings.DefaultCwd = strings.TrimSpace(settings.DefaultCwd)
	settings.DefaultArgs = cleanWorkspaceDefaultArgs(settings.DefaultArgs)
	settings.UpdatedBy = strings.TrimSpace(settings.UpdatedBy)
	return settings
}

func validateGroupSettingsScope(bridgeID, chatID string) error {
	if bridgeID == "" || chatID == "" {
		return fmt.Errorf("group settings bridge_id and chat_id are required")
	}
	return nil
}

func validateGroupSettings(settings GroupSettings) error {
	if err := validateGroupSettingsScope(settings.BridgeID, settings.ChatID); err != nil {
		return err
	}
	if !validBotInteractionMode(settings.InteractionMode) {
		return fmt.Errorf("invalid group interaction mode %q", settings.InteractionMode)
	}
	if settings.IsolationRequired && settings.InteractionMode != config.FeishuInteractionThread {
		return fmt.Errorf("group isolation requires thread interaction mode")
	}
	if !validWorkspaceMode(settings.Mode) {
		return fmt.Errorf("invalid group workspace mode %q", settings.Mode)
	}
	if err := config.ValidateFeishuThinkingProcess(settings.ThinkingProcess); err != nil {
		return err
	}
	if err := session.ValidateIdleTimeoutSeconds(settings.IdleTimeoutSeconds); err != nil {
		return err
	}
	if settings.ThreadContextLimit < 0 || settings.MaxSessionsTotal < 0 || settings.MaxSessionsPerUser < 0 {
		return fmt.Errorf("group context and session limits must not be negative")
	}
	return nil
}

func requireGroupSettingsUpdate(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errGroupSettingsChanged
	}
	return nil
}

func (b *bridgeDB) GetGroupSessionRoot(bridgeID, chatID string) (string, error) {
	return b.GetGroupSessionRootContext(context.Background(), bridgeID, chatID)
}

func (b *bridgeDB) GetGroupSessionRootContext(ctx context.Context, bridgeID, chatID string) (string, error) {
	bridgeID, chatID = strings.TrimSpace(bridgeID), strings.TrimSpace(chatID)
	if err := validateGroupSettingsScope(bridgeID, chatID); err != nil {
		return "", err
	}
	var root string
	err := b.db.QueryRowContext(ctx, `SELECT session_root_msg_id FROM feishu_group_settings
		WHERE bridge_id=? AND chat_id=?`, bridgeID, chatID).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return root, err
}

// SetGroupSessionRoot anchors a singleton to its first real topic binding. It
// does not change the settings version, so a save racing a launch always wins
// through the caller's expected version. Retired roots cannot be reactivated.
func (b *bridgeDB) SetGroupSessionRoot(bridgeID, chatID string, expectedVersion int64, root string) error {
	bridgeID, chatID, root = strings.TrimSpace(bridgeID), strings.TrimSpace(chatID), strings.TrimSpace(root)
	if err := validateGroupSettingsScope(bridgeID, chatID); err != nil {
		return err
	}
	if root == "" {
		return fmt.Errorf("group session root is required")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var activeRoot string
	err = tx.QueryRow(`SELECT session_root_msg_id FROM feishu_group_settings
		WHERE bridge_id=? AND chat_id=? AND state_version=? AND interaction_mode=?`,
		bridgeID, chatID, expectedVersion, config.FeishuInteractionSingleton).Scan(&activeRoot)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && activeRoot != "" && activeRoot != root) {
		return errGroupSettingsChanged
	}
	if err != nil {
		return err
	}
	var bound bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM lark_chats
		WHERE bridge_id=? AND chat_id=? AND root_msg_id=? AND status='active')`,
		bridgeID, chatID, root).Scan(&bound); err != nil {
		return err
	}
	if !bound {
		return fmt.Errorf("group session requires an active topic binding in the same group")
	}
	if activeRoot == "" {
		var retired bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM feishu_group_session_origins
			WHERE bridge_id=? AND chat_id=? AND origin_root_msg_id=?)`, bridgeID, chatID, root).Scan(&retired); err != nil {
			return err
		}
		if retired {
			return errGroupSettingsChanged
		}
	}
	if err := recordGroupSessionOriginTx(tx, bridgeID, chatID, root, root); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE feishu_group_settings SET session_root_msg_id=?
		WHERE bridge_id=? AND chat_id=?`, root, bridgeID, chatID); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearGroupSessionRoot retires only the default pointer. A new settings version
// prevents an old reset callback or in-flight launch from retiring its successor.
// Topic bindings and immutable origin receipts remain available for completion
// delivery and rejection of later replies to retired entries.
func (b *bridgeDB) ClearGroupSessionRoot(bridgeID, chatID string, expectedVersion int64) error {
	bridgeID, chatID = strings.TrimSpace(bridgeID), strings.TrimSpace(chatID)
	if err := validateGroupSettingsScope(bridgeID, chatID); err != nil {
		return err
	}
	result, err := b.db.Exec(`UPDATE feishu_group_settings
		SET session_root_msg_id='', state_version=state_version+1, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND chat_id=? AND state_version=? AND interaction_mode=?`,
		bridgeID, chatID, expectedVersion, config.FeishuInteractionSingleton)
	if err != nil {
		return err
	}
	return requireGroupSettingsUpdate(result)
}

// RecordGroupSessionOrigin associates an input topic with a current singleton.
// Existing identical receipts are idempotent, including after retirement.
func (b *bridgeDB) RecordGroupSessionOrigin(bridgeID, chatID, originRoot, sessionRoot string) error {
	return b.recordGroupSessionOrigin(bridgeID, chatID, originRoot, sessionRoot, false)
}

// Outputs from already accepted work may arrive after a conversation is retired.
// Keep their reply origins attached to that retired conversation, never its successor.
func (b *bridgeDB) RecordGroupSessionOutput(bridgeID, chatID, outputRoot, sessionRoot string) error {
	return b.recordGroupSessionOrigin(bridgeID, chatID, outputRoot, sessionRoot, true)
}

func (b *bridgeDB) recordGroupSessionOrigin(bridgeID, chatID, originRoot, sessionRoot string, output bool) error {
	bridgeID, chatID = strings.TrimSpace(bridgeID), strings.TrimSpace(chatID)
	originRoot, sessionRoot = strings.TrimSpace(originRoot), strings.TrimSpace(sessionRoot)
	if err := validateGroupSettingsScope(bridgeID, chatID); err != nil {
		return err
	}
	if originRoot == "" || sessionRoot == "" {
		return fmt.Errorf("group origin and session roots are required")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow(`SELECT session_root_msg_id FROM feishu_group_session_origins
		WHERE bridge_id=? AND chat_id=? AND origin_root_msg_id=?`, bridgeID, chatID, originRoot).Scan(&existing)
	if err == nil {
		if existing != sessionRoot {
			return errGroupSettingsChanged
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var eligible bool
	if output {
		err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM feishu_group_session_origins
			WHERE bridge_id=? AND chat_id=? AND origin_root_msg_id=? AND session_root_msg_id=?)`,
			bridgeID, chatID, sessionRoot, sessionRoot).Scan(&eligible)
	} else {
		err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM feishu_group_settings
			WHERE bridge_id=? AND chat_id=? AND interaction_mode=? AND session_root_msg_id=?)`,
			bridgeID, chatID, config.FeishuInteractionSingleton, sessionRoot).Scan(&eligible)
	}
	if err != nil {
		return err
	}
	if !eligible {
		return errGroupSettingsChanged
	}
	if err := recordGroupSessionOriginTx(tx, bridgeID, chatID, originRoot, sessionRoot); err != nil {
		return err
	}
	return tx.Commit()
}

func recordGroupSessionOriginTx(tx *sql.Tx, bridgeID, chatID, originRoot, sessionRoot string) error {
	result, err := tx.Exec(`INSERT INTO feishu_group_session_origins
		(bridge_id, chat_id, origin_root_msg_id, session_root_msg_id) VALUES (?, ?, ?, ?)
		ON CONFLICT(bridge_id, chat_id, origin_root_msg_id) DO UPDATE
		SET session_root_msg_id=excluded.session_root_msg_id
		WHERE feishu_group_session_origins.session_root_msg_id=excluded.session_root_msg_id`,
		bridgeID, chatID, originRoot, sessionRoot)
	if err != nil {
		return err
	}
	return requireGroupSettingsUpdate(result)
}

func (b *bridgeDB) FindGroupSessionOrigin(bridgeID, chatID, originRoot string) (string, error) {
	bridgeID, chatID, originRoot = strings.TrimSpace(bridgeID), strings.TrimSpace(chatID), strings.TrimSpace(originRoot)
	if err := validateGroupSettingsScope(bridgeID, chatID); err != nil {
		return "", err
	}
	if originRoot == "" {
		return "", fmt.Errorf("group origin root is required")
	}
	var root string
	err := b.db.QueryRow(`SELECT session_root_msg_id FROM feishu_group_session_origins
		WHERE bridge_id=? AND chat_id=? AND origin_root_msg_id=?`, bridgeID, chatID, originRoot).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return root, err
}
