package bridge

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
	_ "modernc.org/sqlite"
)

// bridgeDB stores bridge-side Feishu state: session↔thread bindings.
type bridgeDB struct {
	db                *sql.DB
	workspaceDefaults workspaceDefaults
}

type workspaceDefaults struct {
	Agent              string
	Target             string
	Cwd                string
	Model              string
	Args               []string
	IdleTimeoutSeconds int
}

const (
	WorkspaceModeOwnerOnly = "owner_only"
	WorkspaceModeShared    = "shared"
)

const (
	WorkspaceKindGroup      = "group"
	WorkspaceKindTopicGroup = "topic_group"
	WorkspaceKindPrivate    = "private"
)

const (
	ChatStatusActive   = "active"
	ChatStatusDetached = "detached"
	ChatStatusTerminal = "terminal"
)

const (
	ChatEntrySourceManual     = "manual"
	ChatEntrySourceGroupWatch = "group_watch"
)

const (
	DocumentCommentSessionActive   = "active"
	DocumentCommentEventProcessing = "processing"
	DocumentCommentEventFinalizing = "finalizing"
	DocumentCommentEventDone       = "done"
	DocumentCommentEventFailed     = "failed"
)

type DocumentCommentSession struct {
	BridgeID       string
	FileToken      string
	FileType       string
	CommentID      string
	TargetID       string
	TargetName     string
	SessionName    string
	AgentProfile   string
	AgentType      string
	EffectiveModel string
	CreatedBy      string
	Status         string
	LastActivity   time.Time
}

type DocumentCommentEvent struct {
	BridgeID  string
	EventID   string
	FileToken string
	FileType  string
	CommentID string
	ReplyID   string
	OpenID    string
}

type BotTurnClaim struct {
	Accepted bool
	Exceeded bool
	Busy     bool
	Turn     int
}

// TurnLink is the durable Bridge receipt that correlates one accepted Feishu
// message with the exact runtime turn. Several messages may point to the same
// turn when burst coalescing combines them into one submission.
type TurnLink struct {
	BridgeID    string    `json:"bridge_id"`
	RootMsgID   string    `json:"root_msg_id"`
	MessageID   string    `json:"message_id"`
	OpenID      string    `json:"open_id"`
	TargetID    string    `json:"target_id"`
	SessionID   string    `json:"session_id"`
	SessionName string    `json:"session_name"`
	LaunchID    string    `json:"launch_id"`
	TurnID      string    `json:"turn_id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type TurnLinkQuery struct {
	MessageID string
	TurnID    string
	SessionID string
}

const (
	botSessionReplyDM    = "dm"
	botSessionReplyGroup = "group"
)

const (
	defaultWorkspaceMode             = WorkspaceModeOwnerOnly
	defaultWorkspaceMaxSessionsTotal = 500
	defaultWorkspaceMaxSessionsUser  = 0
	defaultThreadContextLimit        = 20
	defaultIdleTimeoutSeconds        = 0
	bridgeDBBusyTimeoutMS            = 5000
)

type FeishuWorkspace struct {
	BridgeId           string
	ChatID             string
	Kind               string
	Mode               string
	DefaultAgent       string
	DefaultTarget      string
	DefaultCwd         string
	DefaultModel       string
	DefaultArgs        []string
	ThreadContextLimit int
	MaxSessionsTotal   int
	MaxSessionsPerUser int
	IdleTimeoutSeconds int
}

var errBotProfileStateChanged = errors.New("bot profile state changed")

type BotProfileState struct {
	BridgeID               string
	InteractionMode        string
	DefaultAgent           string
	DefaultRuntimeRef      string
	DefaultModelRef        string
	DefaultReasoningEffort string
	// IsolationRequired marks the 每个话题独立(用户隔离) composite option: the
	// routing mode stays thread, but new topic sessions must launch with an
	// identified execution identity instead of falling back to legacy-host.
	IsolationRequired bool
	Version           int64
}

type PendingManagementRecovery struct {
	BridgeID        string
	OpenID          string
	MessageID       string
	Operation       string
	ExpectedVersion string
	Surface         string
	CreatedAt       time.Time
}

const (
	managementRecoveryRestart = "restart"
	managementRecoveryUpgrade = "upgrade"
)

func openBridgeDB(path string) (*bridgeDB, error) {
	return openBridgeDBForProfile(path, "")
}

func openBridgeDBForProfile(path, profile string) (*bridgeDB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("bridge db mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", bridgeSQLiteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("bridge db open: %w", err)
	}
	db.SetMaxOpenConns(1)
	defaults := workspaceDefaultsFromConfig(profile)
	if err := session.ValidateIdleTimeoutSeconds(defaults.IdleTimeoutSeconds); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("workspace default idle timeout: %w", err)
	}
	b := &bridgeDB{db: db, workspaceDefaults: defaults}
	return b, b.migrate()
}

func bridgeSQLiteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%s_pragma=busy_timeout%%3d%d&_pragma=journal_mode(WAL)&_txlock=immediate", path, separator, bridgeDBBusyTimeoutMS)
}

func (b *bridgeDB) migrate() error {
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS lark_chats (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			bridge_id    TEXT          NOT NULL DEFAULT '',
			root_msg_id  TEXT          NOT NULL,
			open_id      TEXT          NOT NULL,
			target_id   TEXT          NOT NULL,
			target_name TEXT          NOT NULL,
			session_name TEXT          NOT NULL,
			agent_profile TEXT        NOT NULL DEFAULT '',
			agent_type   TEXT          NOT NULL DEFAULT '',
			effective_model TEXT       NOT NULL DEFAULT '',
			topic_root_prompt_delivered INTEGER NOT NULL DEFAULT 0,
			status       TEXT          NOT NULL DEFAULT 'active',
			created_at   INTEGER       NOT NULL DEFAULT (strftime('%s','now'))
		)
	`)
	if err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN bridge_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN thread_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN chat_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN latest_agent_output TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN display_title TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN target_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN target_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN agent_profile TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN effective_model TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_binding_sources (
			bridge_id   TEXT NOT NULL,
			chat_id     TEXT NOT NULL,
			root_msg_id TEXT NOT NULL,
			source      TEXT NOT NULL,
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			PRIMARY KEY (bridge_id, root_msg_id)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_feishu_binding_sources_chat
		ON feishu_binding_sources(bridge_id, chat_id, source)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_permission_audits (
			bridge_id    TEXT PRIMARY KEY,
			fingerprint  TEXT NOT NULL,
			notified_at INTEGER NOT NULL DEFAULT 0,
			claim_until INTEGER NOT NULL DEFAULT 0,
			updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_turn_links (
			bridge_id    TEXT NOT NULL,
			root_msg_id  TEXT NOT NULL,
			message_id   TEXT NOT NULL,
			target_id    TEXT NOT NULL,
			session_id   TEXT NOT NULL,
			session_name TEXT NOT NULL,
			launch_id    TEXT NOT NULL,
			turn_id      TEXT NOT NULL,
			submitted_at INTEGER NOT NULL,
			PRIMARY KEY (bridge_id, message_id)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`CREATE INDEX IF NOT EXISTS idx_feishu_turn_links_turn ON feishu_turn_links(turn_id)`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`CREATE INDEX IF NOT EXISTS idx_feishu_turn_links_session ON feishu_turn_links(session_id)`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_turn_links ADD COLUMN open_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_artifact_deliveries (
            bridge_id TEXT NOT NULL, turn_id TEXT NOT NULL, path TEXT NOT NULL,
            kind TEXT NOT NULL, media_key TEXT NOT NULL, size_bytes INTEGER NOT NULL, sent INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY(bridge_id,turn_id,path)
        );
        CREATE TABLE IF NOT EXISTS feishu_turn_notifications (
			bridge_id TEXT NOT NULL,
			turn_id   TEXT NOT NULL,
			kind      TEXT NOT NULL,
			sent_at   INTEGER NOT NULL,
			PRIMARY KEY (bridge_id, turn_id, kind)
		)
	`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_permission_audits ADD COLUMN claim_until INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_operational_permission_issues (
			bridge_id   TEXT NOT NULL,
			capability  TEXT NOT NULL,
			name        TEXT NOT NULL,
			scopes_json TEXT NOT NULL,
			updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			PRIMARY KEY (bridge_id, capability)
		)
	`); err != nil {
		return err
	}
	hadTopicRootDeliveryColumn, err := b.columnExists("lark_chats", "topic_root_prompt_delivered")
	if err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN topic_root_prompt_delivered INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if !hadTopicRootDeliveryColumn {
		// Existing bindings may already be in the middle of a provider
		// conversation. Treat them as delivered so an upgrade cannot inject the
		// topic root into a later turn; only bindings created after this
		// migration start with the default false value.
		if _, err := b.db.Exec(`UPDATE lark_chats SET topic_root_prompt_delivered=1`); err != nil {
			return err
		}
	}
	if err := b.addColumnIfMissing(`ALTER TABLE lark_chats ADD COLUMN last_activity_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`UPDATE lark_chats SET last_activity_at=created_at WHERE last_activity_at=0`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_workspaces (
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
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_workspaces ADD COLUMN bridge_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_workspaces ADD COLUMN kind TEXT NOT NULL DEFAULT 'group'`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_workspaces ADD COLUMN default_target TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_workspaces ADD COLUMN default_cwd TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_workspaces ADD COLUMN default_model TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_workspaces ADD COLUMN default_args_json TEXT NOT NULL DEFAULT '[]'`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_workspaces ADD COLUMN idle_timeout_seconds INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := b.rebuildLegacyWorkspaceTableIfNeeded(); err != nil {
		return err
	}
	if ok, err := b.columnExists("lark_chats", "sandbox_id"); err != nil {
		return err
	} else if ok {
		if _, err := b.db.Exec(`UPDATE lark_chats SET target_id=sandbox_id WHERE target_id=''`); err != nil {
			return err
		}
	}
	if ok, err := b.columnExists("lark_chats", "sandbox_name"); err != nil {
		return err
	} else if ok {
		if _, err := b.db.Exec(`UPDATE lark_chats SET target_name=sandbox_name WHERE target_name=''`); err != nil {
			return err
		}
	}
	if err := b.rebuildLegacyChatTableIfNeeded(); err != nil {
		return err
	}
	if err := b.NormalizeActiveBindings(); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		DELETE FROM lark_chats
		WHERE id NOT IN (
			SELECT keep_id FROM (
				SELECT MAX(id) AS keep_id
				FROM lark_chats
				GROUP BY bridge_id, root_msg_id
			)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_lark_chats_bridge_root
		ON lark_chats(bridge_id, root_msg_id)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		DELETE FROM feishu_workspaces
		WHERE id NOT IN (
			SELECT keep_id FROM (
				SELECT MAX(id) AS keep_id
				FROM feishu_workspaces
				GROUP BY bridge_id, chat_id
			)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_feishu_workspaces_bridge_chat
		ON feishu_workspaces(bridge_id, chat_id)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_profile_defaults (
			bridge_id TEXT PRIMARY KEY,
			default_agent TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_bot_profiles (
			bridge_id TEXT PRIMARY KEY,
			interaction_mode TEXT NOT NULL DEFAULT 'thread',
			default_agent_profile TEXT NOT NULL DEFAULT '',
			default_runtime_ref TEXT NOT NULL DEFAULT '',
			default_model_ref TEXT NOT NULL DEFAULT '',
			default_reasoning_effort TEXT NOT NULL DEFAULT '',
			state_version INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_bot_profiles ADD COLUMN default_model_ref TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_bot_profiles ADD COLUMN default_runtime_ref TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_bot_profiles ADD COLUMN default_reasoning_effort TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_bot_profiles ADD COLUMN isolation_required INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_seen_messages (
			bridge_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			root_msg_id TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			PRIMARY KEY (bridge_id, message_id)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_group_watch_alert_dedup (
			bridge_id       TEXT NOT NULL,
			chat_id         TEXT NOT NULL,
			sender_id       TEXT NOT NULL,
			fingerprint     TEXT NOT NULL,
			last_message_id TEXT NOT NULL DEFAULT '',
			last_seen_at    INTEGER NOT NULL,
			accepted_message_id TEXT NOT NULL DEFAULT '',
			accepted_at     INTEGER NOT NULL DEFAULT 0,
			suppressed_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (bridge_id, chat_id, sender_id, fingerprint)
		)
	`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_group_watch_alert_dedup ADD COLUMN accepted_message_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_group_watch_alert_dedup ADD COLUMN accepted_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	for _, column := range []string{
		"pending_message_id TEXT NOT NULL DEFAULT ''",
		"pending_at INTEGER NOT NULL DEFAULT 0",
		"submission_started_at INTEGER NOT NULL DEFAULT 0",
		"target_id TEXT NOT NULL DEFAULT ''",
		"session_name TEXT NOT NULL DEFAULT ''",
	} {
		if err := b.addColumnIfMissing("ALTER TABLE feishu_group_watch_alert_dedup ADD COLUMN " + column); err != nil {
			return err
		}
	}
	if _, err := b.db.Exec(`CREATE INDEX IF NOT EXISTS idx_feishu_group_watch_alert_dedup_seen ON feishu_group_watch_alert_dedup(last_seen_at)`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_image_notices (
			bridge_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			session_name TEXT NOT NULL,
			effective_model TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			PRIMARY KEY (bridge_id, target_id, session_name, effective_model, reason)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_bot_threads (
			bridge_id TEXT NOT NULL,
			root_msg_id TEXT NOT NULL,
			turn_count INTEGER NOT NULL DEFAULT 0,
			pending_bot_open_id TEXT NOT NULL DEFAULT '',
			pending_message_id TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			PRIMARY KEY (bridge_id, root_msg_id)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_external_bot_peers (
			collaborator_id TEXT PRIMARY KEY,
			bridge_id TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			bot_open_id TEXT NOT NULL,
			bot_app_id TEXT NOT NULL DEFAULT '',
			bot_display_name TEXT NOT NULL DEFAULT '',
			chat_display_name TEXT NOT NULL DEFAULT '',
			last_seen_at INTEGER NOT NULL,
			UNIQUE (bridge_id, chat_id, bot_open_id)
		)
	`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_external_bot_peers ADD COLUMN bot_app_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_external_bot_peers ADD COLUMN bot_display_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_external_bot_peers ADD COLUMN chat_display_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_feishu_external_bot_peers_bridge
		ON feishu_external_bot_peers(bridge_id, last_seen_at DESC)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_external_bot_exchanges (
			backend_ref TEXT PRIMARY KEY,
			bridge_id TEXT NOT NULL,
			collaborator_id TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			bot_open_id TEXT NOT NULL,
			root_msg_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			deadline_at INTEGER NOT NULL,
			result_content TEXT NOT NULL DEFAULT '',
			result_message_id TEXT NOT NULL DEFAULT '',
			failure_code TEXT NOT NULL DEFAULT '',
			failure_message TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`DROP INDEX IF EXISTS idx_feishu_external_bot_exchange_root`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_feishu_external_bot_exchange_active_root
		ON feishu_external_bot_exchanges(bridge_id, root_msg_id, bot_open_id)
		WHERE root_msg_id <> '' AND status IN ('pending', 'running')
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_external_bot_events (
			backend_ref TEXT NOT NULL,
			seq INTEGER NOT NULL,
			kind TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			result_content TEXT NOT NULL DEFAULT '',
			result_message_id TEXT NOT NULL DEFAULT '',
			failure_code TEXT NOT NULL DEFAULT '',
			failure_message TEXT NOT NULL DEFAULT '',
			at INTEGER NOT NULL,
			PRIMARY KEY (backend_ref, seq)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_bot_sessions (
			bridge_id TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0,
			target_id TEXT NOT NULL DEFAULT '',
			target_name TEXT NOT NULL DEFAULT '',
			session_name TEXT NOT NULL DEFAULT '',
			agent_profile TEXT NOT NULL DEFAULT '',
			agent_type TEXT NOT NULL DEFAULT '',
			effective_model TEXT NOT NULL DEFAULT '',
			cwd TEXT NOT NULL DEFAULT '',
			last_reply_kind TEXT NOT NULL DEFAULT '',
			last_reply_chat_id TEXT NOT NULL DEFAULT '',
			last_reply_open_id TEXT NOT NULL DEFAULT '',
			last_input_message_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_bot_sessions ADD COLUMN effective_model TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_bot_sessions ADD COLUMN cwd TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_onboarding (
 bridge_id TEXT NOT NULL, open_id TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 0,
 body TEXT NOT NULL, PRIMARY KEY(bridge_id,open_id)
 );
 CREATE TABLE IF NOT EXISTS feishu_discovery_preferences (
 bridge_id TEXT NOT NULL, open_id TEXT NOT NULL, enabled INTEGER NOT NULL,
 start_date TEXT NOT NULL, PRIMARY KEY(bridge_id,open_id)
 );
 CREATE TABLE IF NOT EXISTS feishu_discovery_deliveries (
 bridge_id TEXT NOT NULL, open_id TEXT NOT NULL, local_date TEXT NOT NULL,
 capability_id TEXT NOT NULL, status TEXT NOT NULL, message_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(bridge_id,open_id,local_date)
 );
 CREATE TABLE IF NOT EXISTS feishu_management_locales (
 bridge_id TEXT NOT NULL, open_id TEXT NOT NULL, locale TEXT NOT NULL CHECK(locale IN ('en','zh-CN')),
 PRIMARY KEY(bridge_id,open_id)
 );
 CREATE TABLE IF NOT EXISTS feishu_private_management_cards (
 bridge_id TEXT NOT NULL, message_id TEXT NOT NULL, open_id TEXT NOT NULL,
 PRIMARY KEY(bridge_id,message_id)
 );
 CREATE TABLE IF NOT EXISTS feishu_private_session_cards (
 bridge_id TEXT NOT NULL, message_id TEXT NOT NULL, open_id TEXT NOT NULL,
 PRIMARY KEY(bridge_id,message_id)
 );
 CREATE TABLE IF NOT EXISTS feishu_management_recovery (
			bridge_id TEXT PRIMARY KEY,
			open_id TEXT NOT NULL,
			message_id TEXT NOT NULL DEFAULT '',
			operation TEXT NOT NULL DEFAULT 'restart',
			expected_version TEXT NOT NULL DEFAULT '',
			surface TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_management_actions (
			action_key TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_management_recovery ADD COLUMN operation TEXT NOT NULL DEFAULT 'restart'`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_management_recovery ADD COLUMN expected_version TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := b.addColumnIfMissing(`ALTER TABLE feishu_management_recovery ADD COLUMN surface TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_document_sessions (
			bridge_id TEXT NOT NULL,
			file_token TEXT NOT NULL,
			file_type TEXT NOT NULL,
			comment_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			target_name TEXT NOT NULL DEFAULT '',
			session_name TEXT NOT NULL,
			agent_profile TEXT NOT NULL DEFAULT '',
			agent_type TEXT NOT NULL DEFAULT '',
			effective_model TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			last_activity_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			PRIMARY KEY (bridge_id, file_token, comment_id)
		)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_feishu_document_sessions_target_name
		ON feishu_document_sessions(target_id, session_name)
	`); err != nil {
		return err
	}
	if _, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS feishu_document_events (
			bridge_id TEXT NOT NULL,
			event_id TEXT NOT NULL,
			file_token TEXT NOT NULL DEFAULT '',
			file_type TEXT NOT NULL DEFAULT '',
			comment_id TEXT NOT NULL DEFAULT '',
			reply_id TEXT NOT NULL DEFAULT '',
			open_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'processing',
			final_reply_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			PRIMARY KEY (bridge_id, event_id)
		)
	`); err != nil {
		return err
	}
	if err := b.migrateGroupSettings(); err != nil {
		return err
	}
	if err := b.migrateGoals(); err != nil {
		return err
	}
	return b.migrateDistributionDefaults()
}

// ReadWorkspace returns the effective workspace settings without creating a
// workspace row. This keeps read-only surfaces such as the group /amux card
// free of state changes.

// ConfigureThreadBotDefaultAgent applies an explicit onboarding default to the
// authoritative Bot Profile state. It deliberately refuses singleton profiles:
// changing their agent requires rebuilding the bound persistent Session through
// the regular settings transition.

func validBotInteractionMode(mode string) bool {
	return mode == config.FeishuInteractionThread || mode == config.FeishuInteractionSingleton
}

// ReadBotProfileState returns the effective profile state without seeding a
// feishu_bot_profiles row. Read-only cards use this path so opening them does
// not turn fallback configuration into persisted state.

// LarkChat is a binding between a Lark message thread and a session.
type LarkChat struct {
	BridgeId       string
	ChatID         string // Feishu topic-group chat_id containing this thread
	RootMsgID      string // root message_id — all replies in this thread share this root
	ThreadID       string // Feishu 话题 thread_id (may equal RootMsgID or be a separate ID)
	OpenId         string // user's Feishu open_id
	TargetID       string
	TargetName     string
	SessionName    string
	AgentProfile   string
	AgentType      string
	EffectiveModel string
	DisplayTitle   string
	Status         string // "active" | "terminal" | "detached" (legacy)
	LastActivity   int64
	EntrySource    string // creation provenance; currently persisted separately for group_watch

	topicRootPromptPending bool
	localExpertAuthoring   bool
	goalReplyTarget        *directReplyTarget
	goalGeneration         string
	goalSessionID          string
}

type AgentThreadOutput struct {
	BridgeId    string
	TargetName  string
	SessionName string
	Text        string
}

type BotSession struct {
	BridgeId           string
	Enabled            bool
	TargetID           string
	TargetName         string
	SessionName        string
	AgentProfile       string
	AgentType          string
	EffectiveModel     string
	Cwd                string
	LastReplyKind      string
	LastReplyChatID    string
	LastReplyOpenID    string
	LastInputMessageID string
}

// RefreshSessionIdentity updates the presentation snapshot for existing
// bindings after a profile-refresh restart. Routing identity and reply
// locations remain untouched.

// ClaimBotTurn atomically deduplicates one inbound Bot message and advances
// the durable per-topic collaboration counter. Exceeded messages are still
// marked seen, preventing retries from repeatedly emitting the limit notice.

// Binding is a Feishu thread-to-session binding for diagnostics.
type Binding = LarkChat

// ListBindingsForBridgeId returns stored Feishu thread bindings for one bridge_id.
// An empty bridge_id lists all bindings.
func ListBindingsForBridgeId(path, bridgeID string) ([]Binding, error) {
	db, err := openBridgeDB(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return listBindingsFromDB(db, bridgeID)
}

func SetBotSessionInDB(path string, s BotSession) error {
	return agentprofile.WithPackRegistryLock(agentprofile.DefaultPackRegistryPath(), func() error {
		return setBotSessionInDBRegistryStable(path, s)
	})
}

func setBotSessionInDBRegistryStable(path string, s BotSession) error {
	db, err := openBridgeDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	state, err := db.GetBotProfileState(s.BridgeId, config.FeishuInteractionThread)
	if err != nil {
		return err
	}
	defaultAgent := strings.TrimSpace(s.AgentProfile)
	if defaultAgent == "" {
		defaultAgent = strings.TrimSpace(s.AgentType)
	}
	if defaultAgent == "" {
		defaultAgent = state.DefaultAgent
	}
	if defaultAgent != state.DefaultAgent {
		modelRef := strings.TrimSpace(state.DefaultModelRef)
		if strings.TrimSpace(state.DefaultRuntimeRef) == "" && strings.TrimSpace(db.workspaceDefaults.Model) != "" {
			modelRef = strings.TrimSpace(db.workspaceDefaults.Model)
		}
		if _, err := resolveBridgeAgentLaunchWithSelection(defaultAgent, db.workspaceDefaults.Args,
			state.DefaultRuntimeRef, modelRef, state.DefaultReasoningEffort); err != nil {
			return err
		}
	}
	_, err = db.CommitSingletonBotSettings(s.BridgeId, state.Version, defaultAgent, s)
	return err
}

func GetBotSessionFromDB(path, bridgeID string) (*BotSession, error) {
	db, err := openBridgeDB(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.GetBotSession(bridgeID)
}

func GetBotProfileStateFromDB(path, bridgeID, fallbackMode string) (BotProfileState, error) {
	db, err := openBridgeDB(path)
	if err != nil {
		return BotProfileState{}, err
	}
	defer db.Close()
	return db.GetBotProfileState(bridgeID, fallbackMode)
}

// ConfigureThreadBotDefaultAgentInDB persists the explicit Agent Profile chosen
// by onboarding for one Feishu Bot Profile.
func ConfigureThreadBotDefaultAgentInDB(path, profile, bridgeID, agent string) (BotProfileState, error) {
	db, err := openBridgeDBForProfile(path, profile)
	if err != nil {
		return BotProfileState{}, err
	}
	defer db.Close()
	return db.ConfigureThreadBotDefaultAgent(bridgeID, agent)
}

// ConfigureThreadBotLaunchDefaultsInDB persists the independently selected
// Agent Profile, Runtime, and optional model chosen by machine onboarding.
func ConfigureThreadBotLaunchDefaultsInDB(path, profile, bridgeID, agent, runtimeRef, modelRef string) (BotProfileState, error) {
	db, err := openBridgeDBForProfile(path, profile)
	if err != nil {
		return BotProfileState{}, err
	}
	defer db.Close()
	return db.ConfigureThreadBotLaunchDefaults(bridgeID, agent, runtimeRef, modelRef)
}

func DisableBotSessionInDB(path, bridgeID string) error {
	db, err := openBridgeDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	state, err := db.GetBotProfileState(bridgeID, config.FeishuInteractionThread)
	if err != nil {
		return err
	}
	_, err = db.CommitThreadBotSettings(bridgeID, state.Version, state.DefaultAgent)
	return err
}

func listBindingsFromDB(b *bridgeDB, bridgeID string) ([]Binding, error) {
	query := `
		SELECT bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, display_title, status, last_activity_at
		FROM lark_chats
	`
	var (
		rows *sql.Rows
		err  error
	)
	if strings.TrimSpace(bridgeID) == "" {
		rows, err = b.db.Query(query + ` ORDER BY created_at DESC, id DESC`)
	} else {
		rows, err = b.db.Query(query+` WHERE bridge_id=? ORDER BY created_at DESC, id DESC`, bridgeID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var c Binding
		if err := rows.Scan(&c.BridgeId, &c.ChatID, &c.RootMsgID, &c.ThreadID, &c.OpenId, &c.TargetID, &c.TargetName, &c.SessionName, &c.AgentProfile, &c.AgentType, &c.EffectiveModel, &c.DisplayTitle, &c.Status, &c.LastActivity); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NormalizeActiveBindings enforces the current product invariant on existing DB rows:
// one active binding per Feishu root and one active binding per target/session.

func (b *bridgeDB) Close() error {
	return b.db.Close()
}
