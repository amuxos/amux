package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
)

func workspaceDefaultsFromConfig(profile string) workspaceDefaults {
	cfg := config.GetBridgeFeishuWorkspaceDefaultsForProfile(profile)
	defaults := workspaceDefaults{Agent: strings.TrimSpace(cfg.Agent), Target: strings.TrimSpace(cfg.Target), Cwd: strings.TrimSpace(cfg.Cwd), Model: strings.TrimSpace(cfg.Model), Args: cleanWorkspaceDefaultArgs(cfg.Args), IdleTimeoutSeconds: cfg.IdleTimeoutSeconds}
	if defaults.Agent == "" {
		defaults.Agent = agentprofile.DefaultProfileRef
	}
	return defaults
}
func cleanWorkspaceDefaultArgs(args []string) []string {
	clean := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			continue
		}
		clean = append(clean, arg)
	}
	return clean
}
func validWorkspaceMode(mode string) bool {
	return mode == WorkspaceModeOwnerOnly || mode == WorkspaceModeShared
}
func validWorkspaceKind(kind string) bool {
	return kind == WorkspaceKindGroup || kind == WorkspaceKindTopicGroup || kind == WorkspaceKindPrivate
}
func (b *bridgeDB) GetWorkspace(bridgeID, chatID string) (*FeishuWorkspace, error) {
	if strings.TrimSpace(bridgeID) == "" {
		return nil, fmt.Errorf("workspace bridge_id is required")
	}
	if strings.TrimSpace(chatID) == "" {
		return &FeishuWorkspace{BridgeId: bridgeID, Kind: WorkspaceKindGroup, Mode: defaultWorkspaceMode, DefaultAgent: agentprofile.DefaultProfileRef, DefaultModel: strings.TrimSpace(b.workspaceDefaults.Model), ThreadContextLimit: defaultThreadContextLimit, MaxSessionsTotal: defaultWorkspaceMaxSessionsTotal, MaxSessionsPerUser: defaultWorkspaceMaxSessionsUser, IdleTimeoutSeconds: b.workspaceDefaults.IdleTimeoutSeconds}, nil
	}
	if err := b.ensureWorkspace(bridgeID, chatID); err != nil {
		return nil, err
	}
	row := b.db.QueryRow(`
		SELECT bridge_id, chat_id, kind, mode, default_agent, default_target, default_cwd, default_model, default_args_json, thread_context_limit, max_sessions_total, max_sessions_per_user, idle_timeout_seconds
		FROM feishu_workspaces WHERE bridge_id=? AND chat_id=?`, bridgeID, chatID)
	var ws FeishuWorkspace
	var defaultArgsJSON string
	if err := row.Scan(&ws.BridgeId, &ws.ChatID, &ws.Kind, &ws.Mode, &ws.DefaultAgent, &ws.DefaultTarget, &ws.DefaultCwd, &ws.DefaultModel, &defaultArgsJSON, &ws.ThreadContextLimit, &ws.MaxSessionsTotal, &ws.MaxSessionsPerUser, &ws.IdleTimeoutSeconds); err != nil {
		return nil, err
	}
	args, err := decodeWorkspaceDefaultArgs(defaultArgsJSON)
	if err != nil {
		return nil, err
	}
	ws.DefaultArgs = args
	return &ws, nil
}

func (b *bridgeDB) ReadWorkspace(bridgeID, chatID string) (*FeishuWorkspace, error) {
	return b.ReadWorkspaceContext(context.Background(), bridgeID, chatID)
}

func (b *bridgeDB) ReadWorkspaceContext(ctx context.Context, bridgeID, chatID string) (*FeishuWorkspace, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	chatID = strings.TrimSpace(chatID)
	if bridgeID == "" {
		return nil, fmt.Errorf("workspace bridge_id is required")
	}
	if chatID == "" {
		return b.defaultWorkspaceContext(ctx, bridgeID, chatID)
	}
	row := b.db.QueryRowContext(ctx, `
		SELECT bridge_id, chat_id, kind, mode, default_agent, default_target, default_cwd, default_model, default_args_json, thread_context_limit, max_sessions_total, max_sessions_per_user, idle_timeout_seconds
		FROM feishu_workspaces WHERE bridge_id=? AND chat_id=?`, bridgeID, chatID)
	var ws FeishuWorkspace
	var defaultArgsJSON string
	if err := row.Scan(&ws.BridgeId, &ws.ChatID, &ws.Kind, &ws.Mode, &ws.DefaultAgent, &ws.DefaultTarget, &ws.DefaultCwd, &ws.DefaultModel, &defaultArgsJSON, &ws.ThreadContextLimit, &ws.MaxSessionsTotal, &ws.MaxSessionsPerUser, &ws.IdleTimeoutSeconds); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return b.defaultWorkspaceContext(ctx, bridgeID, chatID)
		}
		return nil, err
	}
	args, err := decodeWorkspaceDefaultArgs(defaultArgsJSON)
	if err != nil {
		return nil, err
	}
	ws.DefaultArgs = args
	return &ws, nil
}

func (b *bridgeDB) ListGroupWorkspaceChatIDs(bridgeID string) ([]string, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	if bridgeID == "" {
		return nil, fmt.Errorf("workspace bridge_id is required")
	}
	rows, err := b.db.Query(`
		SELECT chat_id
		FROM feishu_workspaces
		WHERE bridge_id=? AND kind IN (?, ?)
		ORDER BY chat_id`, bridgeID, WorkspaceKindGroup, WorkspaceKindTopicGroup)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chatIDs []string
	for rows.Next() {
		var chatID string
		if err := rows.Scan(&chatID); err != nil {
			return nil, err
		}
		if chatID = strings.TrimSpace(chatID); chatID != "" {
			chatIDs = append(chatIDs, chatID)
		}
	}
	return chatIDs, rows.Err()
}
func (b *bridgeDB) defaultWorkspace(bridgeID, chatID string) (*FeishuWorkspace, error) {
	return b.defaultWorkspaceContext(context.Background(), bridgeID, chatID)
}

func (b *bridgeDB) defaultWorkspaceContext(ctx context.Context, bridgeID, chatID string) (*FeishuWorkspace, error) {
	defaults := b.workspaceDefaults
	if agent, err := b.ProfileDefaultAgentContext(ctx, bridgeID); err != nil {
		return nil, err
	} else if strings.TrimSpace(agent) != "" {
		defaults.Agent = agent
	}
	if strings.TrimSpace(defaults.Agent) == "" {
		defaults.Agent = agentprofile.DefaultProfileRef
	}
	return &FeishuWorkspace{BridgeId: bridgeID, ChatID: chatID, Kind: WorkspaceKindGroup, Mode: defaultWorkspaceMode, DefaultAgent: strings.TrimSpace(defaults.Agent), DefaultTarget: strings.TrimSpace(defaults.Target), DefaultCwd: strings.TrimSpace(defaults.Cwd), DefaultModel: strings.TrimSpace(defaults.Model), DefaultArgs: append([]string( // ReadWorkspace returns the effective workspace settings without creating a
		// workspace row. This keeps read-only surfaces such as the group /amux card
		// free of state changes.
		nil), defaults.Args...), ThreadContextLimit: defaultThreadContextLimit, MaxSessionsTotal: defaultWorkspaceMaxSessionsTotal, MaxSessionsPerUser: defaultWorkspaceMaxSessionsUser, IdleTimeoutSeconds: defaults.IdleTimeoutSeconds}, nil
}
func (b *bridgeDB) EnsureWorkspaceKind(bridgeID, chatID, kind string) error {
	bridgeID = strings.TrimSpace(bridgeID)
	chatID = strings.TrimSpace(chatID)
	kind = strings.TrimSpace(kind)
	if bridgeID == "" {
		return fmt.Errorf("workspace bridge_id is required")
	}
	if chatID == "" {
		return fmt.Errorf("workspace chat_id is required")
	}
	if !validWorkspaceKind(kind) {
		return fmt.Errorf("invalid workspace kind %q", kind)
	}
	if err := b.ensureWorkspace(bridgeID, chatID); err != nil {
		return err
	}
	_, err := b.db.Exec(`
		UPDATE feishu_workspaces
		SET kind=?, mode=CASE WHEN ?='private' THEN 'owner_only' ELSE mode END, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND chat_id=?`, kind, kind, bridgeID, chatID)
	return err
}
func (b *bridgeDB) WorkspaceKind(bridgeID, chatID string) (string, error) {
	if strings.TrimSpace(bridgeID) == "" {
		return "", fmt.Errorf("workspace bridge_id is required")
	}
	if strings.TrimSpace(chatID) == "" {
		return WorkspaceKindGroup, nil
	}
	if err := b.ensureWorkspace(bridgeID, chatID); err != nil {
		return "", err
	}
	var kind string
	err := b.db.QueryRow(`SELECT kind FROM feishu_workspaces WHERE bridge_id=? AND chat_id=?`, bridgeID, chatID).Scan(&kind)
	if err != nil {
		return "", err
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return WorkspaceKindGroup, nil
	}
	return kind, nil
}
func (b *bridgeDB) WorkspaceExists(bridgeID, chatID string) (bool, error) {
	if strings.TrimSpace(bridgeID) == "" {
		return false, fmt.Errorf("workspace bridge_id is required")
	}
	if strings.TrimSpace(chatID) == "" {
		return false, nil
	}
	var exists int
	err := b.db.QueryRow(`SELECT 1 FROM feishu_workspaces WHERE bridge_id=? AND chat_id=?`, bridgeID, chatID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
func (b *bridgeDB) SetWorkspaceMode(bridgeID, chatID, mode string) error {
	if strings.TrimSpace(bridgeID) == "" {
		return fmt.Errorf("workspace bridge_id is required")
	}
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("workspace chat_id is required")
	}
	if !validWorkspaceMode(mode) {
		return fmt.Errorf("invalid workspace mode %q", mode)
	}
	if err := b.ensureWorkspace(bridgeID, chatID); err != nil {
		return err
	}
	kind, err := b.WorkspaceKind(bridgeID, chatID)
	if err != nil {
		return err
	}
	if kind == WorkspaceKindPrivate && mode == WorkspaceModeShared {
		return fmt.Errorf("private workspace does not support shared mode")
	}
	_, err = b.db.Exec(`
		UPDATE feishu_workspaces
		SET mode=?, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND chat_id=?`, mode, bridgeID, chatID)
	return err
}
func (b *bridgeDB) SetWorkspaceDefaultAgent(bridgeID, chatID, agent string) error {
	return b.updateWorkspaceDefaultText(bridgeID, chatID, "default_agent", agent)
}
func getBotProfileStateTx(tx *sql.Tx, bridgeID string) (BotProfileState, error) {
	var state BotProfileState
	var isolationRequired int
	err := tx.QueryRow(`
		SELECT bridge_id, interaction_mode, default_agent_profile, default_runtime_ref,
			default_model_ref, default_reasoning_effort, isolation_required, state_version
		FROM feishu_bot_profiles
		WHERE bridge_id=?`, bridgeID).Scan(&state.BridgeID, &state.InteractionMode, &state.DefaultAgent,
		&state.DefaultRuntimeRef, &state.DefaultModelRef, &state.DefaultReasoningEffort, &isolationRequired, &state.Version)
	state.IsolationRequired = isolationRequired != 0
	return state, err
}
func requireBotProfileUpdate(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errBotProfileStateChanged
	}
	return nil
}
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (b *bridgeDB) SetWorkspaceDefaultTarget(bridgeID, chatID, target string) error {
	return b.updateWorkspaceDefaultText(bridgeID, chatID, "default_target", target)
}
func (b *bridgeDB) SetWorkspaceDefaultCwd(bridgeID, chatID, cwd string) error {
	return b.updateWorkspaceDefaultText(bridgeID, chatID, "default_cwd", cwd)
}
func (b *bridgeDB) SetWorkspaceDefaultModel(bridgeID, chatID, model string) error {
	return b.updateWorkspaceDefaultText(bridgeID, chatID, "default_model", model)
}
func (b *bridgeDB) SetWorkspaceDefaultArgs(bridgeID, chatID string, args []string) error {
	if strings.TrimSpace(bridgeID) == "" {
		return fmt.Errorf("workspace bridge_id is required")
	}
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("workspace chat_id is required")
	}
	raw, err := encodeWorkspaceDefaultArgs(args)
	if err != nil {
		return err
	}
	if err := b.ensureWorkspace(bridgeID, chatID); err != nil {
		return err
	}
	_, err = b.db.Exec(`
		UPDATE feishu_workspaces
		SET default_args_json=?, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND chat_id=?`, raw, bridgeID, chatID)
	return err
}
func (b *bridgeDB) SetWorkspaceIdleTimeoutSeconds(bridgeID, chatID string, seconds int) error {
	if strings.TrimSpace(bridgeID) == "" {
		return fmt.Errorf("workspace bridge_id is required")
	}
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("workspace chat_id is required")
	}
	if err := session.ValidateIdleTimeoutSeconds(seconds); err != nil {
		return err
	}
	if err := b.ensureWorkspace(bridgeID, chatID); err != nil {
		return err
	}
	_, err := b.db.Exec(`
		UPDATE feishu_workspaces
		SET idle_timeout_seconds=?, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND chat_id=?`, seconds, bridgeID, chatID)
	return err
}
func (b *bridgeDB) updateWorkspaceDefaultText(bridgeID, chatID, column, value string) error {
	if strings.TrimSpace(bridgeID) == "" {
		return fmt.Errorf("workspace bridge_id is required")
	}
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("workspace chat_id is required")
	}
	switch column {
	case "default_agent", "default_target", "default_cwd", "default_model":
	default:
		return fmt.Errorf("unsupported workspace default column %q", column)
	}
	if err := b.ensureWorkspace(bridgeID, chatID); err != nil {
		return err
	}
	_, err := b.db.Exec(`
		UPDATE feishu_workspaces
		SET `+column+`=?, updated_at=strftime('%s','now')
		WHERE bridge_id=? AND chat_id=?`, strings.TrimSpace(value), bridgeID, chatID)
	return err
}
func encodeWorkspaceDefaultArgs(args []string) (string, error) {
	raw, err := json.Marshal(cleanWorkspaceDefaultArgs(args))
	if err != nil {
		return "", fmt.Errorf("marshal workspace default args: %w", err)
	}
	return string(raw), nil
}
func decodeWorkspaceDefaultArgs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("parse workspace default args: %w", err)
	}
	return args, nil
}
func (b *bridgeDB) ensureWorkspace(bridgeID, chatID string) error {
	defaults := b.workspaceDefaults
	if agent, err := b.ProfileDefaultAgent(bridgeID); err != nil {
		return err
	} else if strings.TrimSpace(agent) != "" {
		defaults.Agent = agent
	}
	if strings.TrimSpace(defaults.Agent) == "" {
		defaults.Agent = agentprofile.DefaultProfileRef
	}
	argsJSON, err := encodeWorkspaceDefaultArgs(defaults.Args)
	if err != nil {
		return err
	}
	_, err = b.db.Exec(`
		INSERT INTO feishu_workspaces (
			bridge_id, chat_id, kind, mode, default_agent, default_target, default_cwd, default_model, default_args_json, thread_context_limit, max_sessions_total, max_sessions_per_user, idle_timeout_seconds
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bridge_id, chat_id) DO NOTHING`, bridgeID, chatID, WorkspaceKindGroup, defaultWorkspaceMode, strings.TrimSpace(defaults.Agent), strings.TrimSpace(defaults.Target), strings.TrimSpace(defaults.Cwd), strings.TrimSpace(defaults.Model), argsJSON, defaultThreadContextLimit, defaultWorkspaceMaxSessionsTotal, defaultWorkspaceMaxSessionsUser, defaults.IdleTimeoutSeconds)
	return err
}
func (b *bridgeDB) rebuildLegacyWorkspaceTableIfNeeded() error {
	legacy, err := b.workspaceHasLegacyChatIDUnique()
	if err != nil {
		return err
	}
	if !legacy {
		return nil
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		CREATE TABLE feishu_workspaces_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bridge_id TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT 'group',
			mode TEXT NOT NULL DEFAULT 'owner_only',
			default_agent TEXT NOT NULL DEFAULT '` + agentprofile.DefaultProfileRef + `',
			default_target TEXT NOT NULL DEFAULT '',
			default_cwd TEXT NOT NULL DEFAULT '',
			default_model TEXT NOT NULL DEFAULT '',
			default_args_json TEXT NOT NULL DEFAULT '[]',
			thread_context_limit INTEGER NOT NULL DEFAULT 20,
			max_sessions_total INTEGER NOT NULL DEFAULT 500,
			max_sessions_per_user INTEGER NOT NULL DEFAULT 50,
			idle_timeout_seconds INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO feishu_workspaces_new (
			bridge_id, chat_id, kind, mode, default_agent, default_target, default_cwd, default_model, default_args_json, thread_context_limit, max_sessions_total, max_sessions_per_user, idle_timeout_seconds, created_at, updated_at
		)
		SELECT bridge_id, chat_id, 'group', mode, default_agent, default_target, default_cwd, default_model, default_args_json, thread_context_limit, max_sessions_total, max_sessions_per_user, idle_timeout_seconds, created_at, updated_at
		FROM feishu_workspaces
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE feishu_workspaces`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE feishu_workspaces_new RENAME TO feishu_workspaces`); err != nil {
		return err
	}
	return tx.Commit()
}
func (b *bridgeDB) rebuildLegacyChatTableIfNeeded() error {
	legacy, err := b.chatHasLegacyRootMsgUnique()
	if err != nil {
		return err
	}
	if !legacy {
		return nil
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		CREATE TABLE lark_chats_new (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			bridge_id    TEXT          NOT NULL DEFAULT '',
			chat_id      TEXT          NOT NULL DEFAULT '',
			root_msg_id  TEXT          NOT NULL,
			thread_id    TEXT          NOT NULL DEFAULT '',
			open_id      TEXT          NOT NULL,
			target_id    TEXT          NOT NULL DEFAULT '',
			target_name  TEXT          NOT NULL DEFAULT '',
			session_name TEXT          NOT NULL,
				agent_profile TEXT        NOT NULL DEFAULT '',
				agent_type   TEXT          NOT NULL DEFAULT '',
				effective_model TEXT       NOT NULL DEFAULT '',
				topic_root_prompt_delivered INTEGER NOT NULL DEFAULT 0,
				status       TEXT          NOT NULL DEFAULT 'active',
				created_at   INTEGER       NOT NULL DEFAULT (strftime('%s','now')),
				last_activity_at INTEGER    NOT NULL DEFAULT (strftime('%s','now')),
				latest_agent_output TEXT   NOT NULL DEFAULT '',
				display_title TEXT         NOT NULL DEFAULT ''
			)
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO lark_chats_new (
			id, bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, topic_root_prompt_delivered, status, created_at, last_activity_at, latest_agent_output, display_title
		)
		SELECT id, bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, topic_root_prompt_delivered, status, created_at, last_activity_at, latest_agent_output, display_title
		FROM lark_chats
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE lark_chats`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE lark_chats_new RENAME TO lark_chats`); err != nil {
		return err
	}
	return tx.Commit()
}
func (b *bridgeDB) chatHasLegacyRootMsgUnique() (bool, error) {
	rows, err := b.db.Query(`PRAGMA table_info(lark_chats)`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return false, err
		}
		if name == "root_msg_id" && pk > 0 {
			rows.Close()
			return true, nil
		}
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return b.tableHasSingleColumnUniqueIndex("lark_chats", "root_msg_id")
}
func (b *bridgeDB) workspaceHasLegacyChatIDUnique() (bool, error) {
	rows, err := b.db.Query(`PRAGMA table_info(feishu_workspaces)`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return false, err
		}
		if name == "chat_id" && pk > 0 {
			rows.Close()
			return true, nil
		}
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return b.tableHasSingleColumnUniqueIndex("feishu_workspaces", "chat_id")
}
func (b *bridgeDB) tableHasSingleColumnUniqueIndex(table, column string) (bool, error) {
	indexes, err := b.db.Query(`PRAGMA index_list("` + strings.ReplaceAll(table, `"`, `""`) + `")`)
	if err != nil {
		return false, err
	}
	var uniqueIndexNames []string
	for indexes.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := indexes.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			indexes.Close()
			return false, err
		}
		if unique != 0 {
			uniqueIndexNames = append(uniqueIndexNames, name)
		}
	}
	if err := indexes.Close(); err != nil {
		return false, err
	}
	if err := indexes.Err(); err != nil {
		return false, err
	}
	for _, name := range uniqueIndexNames {
		legacy, err := b.indexIsSingleColumn(name, column)
		if err != nil {
			return false, err
		}
		if legacy {
			return true, nil
		}
	}
	return false, nil
}
func (b *bridgeDB) indexIsSingleColumn(indexName, column string) (bool, error) {
	rows, err := b.db.Query(`PRAGMA index_info("` + strings.ReplaceAll(indexName, `"`, `""`) + `")`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			return false, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return len(columns) == 1 && columns[0] == column, nil
}
func (b *bridgeDB) addColumnIfMissing(stmt string) error {
	if _, err := b.db.Exec(stmt); err != nil && !isDuplicateColumnError(err) {
		return err
	}
	return nil
}
func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column")
}
func (b *bridgeDB) columnExists(table, column string) (bool, error) {
	rows, err := b.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
