package threadcontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/util"

	_ "modernc.org/sqlite"
)

const (
	WorkspaceModeOwnerOnly = "owner_only"
	WorkspaceModeShared    = "shared"
)

const (
	EventTypeTopicRoot    EventType = "topic_root"
	EventTypeHumanMessage EventType = "human_message"
	EventTypeUserRequest  EventType = "user_request"
	EventTypeAgentOutput  EventType = "agent_output"
	EventTypeCommand      EventType = "command"
)

const (
	defaultThreadContextLimit  = 20
	defaultRetentionDays       = 30
	defaultMaxAgentOutputChars = 20000
	defaultMaxContextChars     = 24000
)

type EventType string

type Store struct {
	db *sql.DB
}

type ThreadEvent struct {
	Source       string
	ChatID       string
	RootMsgID    string
	ThreadID     string
	MessageID    string
	EventType    EventType
	ActorType    string
	ActorID      string
	BridgeId     string
	BotProfile   string
	TargetID     string
	TargetName   string
	SessionName  string
	Text         string
	MetadataJSON string
	CreatedAt    int64
}

type BuildRequest struct {
	ChatID             string
	RootMsgID          string
	MessageID          string
	BridgeId           string
	BotProfile         string
	WorkspaceMode      string
	RequestText        string
	TargetID           string
	TargetName         string
	SessionName        string
	ThreadContextLimit int
	MaxContextChars    int
}

type ContextPackage struct {
	Request       BuildRequest
	ThreadContext string
	SelectedItems []ContextItem
	ExcludedItems []ExcludedItem
	Diagnostics   []string
}

type ContextItem struct {
	ID            int64
	EventId       int64
	ItemType      string
	Role          string
	AuthorLabel   string
	Text          string
	BridgeId      string
	BotProfile    string
	TargetID      string
	TargetName    string
	SessionName   string
	MessageID     string
	CreatedAt     int64
	TokenEstimate int
	Truncated     bool
}

type ExcludedItem struct {
	ID     int64  `json:"id,omitempty"`
	Reason string `json:"reason"`
}

type Decision struct {
	ID              int64
	ChatID          string
	RootMsgID       string
	MessageID       string
	BridgeId        string
	BotProfile      string
	WorkspaceMode   string
	RequestText     string
	ThreadContext   string
	PromptPreview   string
	SelectedItemIDs []int64
	ExcludedItems   []ExcludedItem
	Diagnostics     []string
	CreatedAt       int64
}

type ExplainRequest struct {
	RootMsgID  string
	MessageID  string
	BridgeId   string
	BotProfile string
	DecisionID int64
}

type ExplainResult struct {
	Decision      Decision
	SelectedItems []ContextItem
}

func DefaultDBPath() string {
	return filepath.Join(amuxhome.Dir(), "feishu_context.db")
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultDBPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("thread context db mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("thread context db open: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS thread_context_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT NOT NULL DEFAULT 'feishu',
			chat_id TEXT NOT NULL DEFAULT '',
			root_msg_id TEXT NOT NULL,
			thread_id TEXT NOT NULL DEFAULT '',
			message_id TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL,
			actor_type TEXT NOT NULL DEFAULT '',
			actor_id TEXT NOT NULL DEFAULT '',
			bridge_id TEXT NOT NULL DEFAULT '',
			bot_profile TEXT NOT NULL DEFAULT '',
			target_id TEXT NOT NULL DEFAULT '',
			target_name TEXT NOT NULL DEFAULT '',
			session_name TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		);
		CREATE TABLE IF NOT EXISTS thread_context_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id INTEGER NOT NULL,
			chat_id TEXT NOT NULL DEFAULT '',
			root_msg_id TEXT NOT NULL,
			thread_id TEXT NOT NULL DEFAULT '',
			message_id TEXT NOT NULL DEFAULT '',
			item_type TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT '',
			author_label TEXT NOT NULL DEFAULT '',
			actor_id TEXT NOT NULL DEFAULT '',
			bridge_id TEXT NOT NULL DEFAULT '',
			bot_profile TEXT NOT NULL DEFAULT '',
			target_id TEXT NOT NULL DEFAULT '',
			target_name TEXT NOT NULL DEFAULT '',
			session_name TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '',
			truncated INTEGER NOT NULL DEFAULT 0,
			token_estimate INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		);
		CREATE TABLE IF NOT EXISTS thread_context_decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id TEXT NOT NULL DEFAULT '',
			root_msg_id TEXT NOT NULL,
			message_id TEXT NOT NULL DEFAULT '',
			bridge_id TEXT NOT NULL DEFAULT '',
			bot_profile TEXT NOT NULL DEFAULT '',
			workspace_mode TEXT NOT NULL DEFAULT '',
			request_text TEXT NOT NULL DEFAULT '',
			thread_context TEXT NOT NULL DEFAULT '',
			prompt_preview TEXT NOT NULL DEFAULT '',
			selected_item_ids_json TEXT NOT NULL DEFAULT '[]',
			excluded_json TEXT NOT NULL DEFAULT '[]',
			diagnostics_json TEXT NOT NULL DEFAULT '[]',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		);
		CREATE INDEX IF NOT EXISTS idx_thread_context_items_root ON thread_context_items(root_msg_id, bridge_id, item_type, id);
		CREATE INDEX IF NOT EXISTS idx_thread_context_decisions_root ON thread_context_decisions(root_msg_id, bridge_id, id);
	`)
	return err
}

func (s *Store) RecordEvent(ctx context.Context, ev ThreadEvent) error {
	if s == nil || s.db == nil {
		return nil
	}
	ev = normalizeEvent(ev)
	if ev.RootMsgID == "" || ev.Text == "" || ev.EventType == "" {
		return nil
	}
	text := ev.Text
	truncated := false
	if ev.EventType == EventTypeAgentOutput && len(text) > defaultMaxAgentOutputChars {
		text = text[:defaultMaxAgentOutputChars]
		truncated = true
		ev.Text = text
		ev.MetadataJSON = mergeTruncatedMetadata(ev.MetadataJSON)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO thread_context_events (
			source, chat_id, root_msg_id, thread_id, message_id, event_type, actor_type, actor_id,
			bridge_id, bot_profile, target_id, target_name, session_name, text, metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Source, ev.ChatID, ev.RootMsgID, ev.ThreadID, ev.MessageID, string(ev.EventType), ev.ActorType, ev.ActorID,
		ev.BridgeId, ev.BotProfile, ev.TargetID, ev.TargetName, ev.SessionName, ev.Text, ev.MetadataJSON, ev.CreatedAt)
	if err != nil {
		return err
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if shouldMaterializeItem(ev.EventType) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO thread_context_items (
				event_id, chat_id, root_msg_id, thread_id, message_id, item_type, role, author_label, actor_id,
				bridge_id, bot_profile, target_id, target_name, session_name, text, truncated, token_estimate, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			eventID, ev.ChatID, ev.RootMsgID, ev.ThreadID, ev.MessageID, string(ev.EventType), itemRole(ev.EventType),
			authorLabel(ev), ev.ActorID, ev.BridgeId, ev.BotProfile, ev.TargetID, ev.TargetName, ev.SessionName,
			text, util.BoolInt(truncated), len(text), ev.CreatedAt)
		if err != nil {
			return err
		}
	}
	if err := cleanupOldEvents(ctx, tx, defaultRetentionDays); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RootMessageText(ctx context.Context, rootMsgID, bridgeID string) (string, error) {
	rootMsgID = strings.TrimSpace(rootMsgID)
	bridgeID = strings.TrimSpace(bridgeID)
	if s == nil || s.db == nil || rootMsgID == "" || bridgeID == "" {
		return "", nil
	}
	var text string
	err := s.db.QueryRowContext(ctx, `
		SELECT text
		FROM thread_context_items
			WHERE root_msg_id=?
			  AND message_id=?
			  AND item_type IN (?, ?)
			  AND bridge_id=?
			ORDER BY id DESC
			LIMIT 1`, rootMsgID, rootMsgID, string(EventTypeTopicRoot), string(EventTypeHumanMessage), bridgeID).Scan(&text)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// MessageText returns visible context already recorded for one Feishu message.
// The chat boundary is required because message IDs supplied by an incoming
// event must never be allowed to select context from another conversation.
func (s *Store) MessageText(ctx context.Context, chatID, messageID string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	messageID = strings.TrimSpace(messageID)
	if s == nil || s.db == nil || chatID == "" || messageID == "" {
		return "", nil
	}
	var text string
	err := s.db.QueryRowContext(ctx, `
		SELECT text
		FROM thread_context_items
		WHERE chat_id=? AND message_id=?
		ORDER BY id DESC
		LIMIT 1`, chatID, messageID).Scan(&text)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func (s *Store) BuildPackage(ctx context.Context, req BuildRequest) (ContextPackage, error) {
	req = normalizeBuildRequest(req)
	pkg := ContextPackage{Request: req}
	if s == nil || s.db == nil || req.RootMsgID == "" || req.BridgeId == "" {
		return pkg, nil
	}
	if req.WorkspaceMode != WorkspaceModeOwnerOnly && req.WorkspaceMode != WorkspaceModeShared {
		pkg.Diagnostics = append(pkg.Diagnostics, "workspace mode does not use thread context")
		return pkg, nil
	}
	items, err := s.loadCandidateItems(ctx, req)
	if err != nil {
		return pkg, err
	}
	maxChars := req.MaxContextChars
	used := 0
	lines := []string{}
	for _, item := range items {
		line := formatItem(item)
		if maxChars > 0 && used+len(line)+1 > maxChars {
			pkg.ExcludedItems = append(pkg.ExcludedItems, ExcludedItem{ID: item.ID, Reason: "max_context_chars"})
			continue
		}
		lines = append(lines, line)
		used += len(line) + 1
		pkg.SelectedItems = append(pkg.SelectedItems, item)
	}
	if len(lines) > 0 {
		pkg.ThreadContext = strings.Join(lines, "\n")
	}
	return pkg, nil
}

func (s *Store) RecordDecision(ctx context.Context, pkg ContextPackage, promptPreview string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	req := normalizeBuildRequest(pkg.Request)
	if req.RootMsgID == "" || req.BridgeId == "" {
		return 0, nil
	}
	ids := make([]int64, 0, len(pkg.SelectedItems))
	for _, item := range pkg.SelectedItems {
		ids = append(ids, item.ID)
	}
	selectedJSON, _ := json.Marshal(ids)
	excludedJSON, _ := json.Marshal(pkg.ExcludedItems)
	diagnosticsJSON, _ := json.Marshal(pkg.Diagnostics)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO thread_context_decisions (
			chat_id, root_msg_id, message_id, bridge_id, bot_profile, workspace_mode, request_text,
			thread_context, prompt_preview, selected_item_ids_json, excluded_json, diagnostics_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ChatID, req.RootMsgID, req.MessageID, req.BridgeId, req.BotProfile, req.WorkspaceMode, req.RequestText,
		pkg.ThreadContext, promptPreview, string(selectedJSON), string(excludedJSON), string(diagnosticsJSON), nowUnix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Explain(ctx context.Context, req ExplainRequest) (ExplainResult, error) {
	var out ExplainResult
	if s == nil || s.db == nil {
		return out, nil
	}
	req.RootMsgID = strings.TrimSpace(req.RootMsgID)
	req.BridgeId = strings.TrimSpace(req.BridgeId)
	if req.RootMsgID == "" || req.BridgeId == "" {
		return out, fmt.Errorf("root_msg_id and bridge_id are required")
	}
	decision, err := s.loadDecision(ctx, req)
	if err != nil {
		return out, err
	}
	out.Decision = decision
	if len(decision.SelectedItemIDs) == 0 {
		return out, nil
	}
	items, err := s.loadItemsByID(ctx, decision.SelectedItemIDs)
	if err != nil {
		return out, err
	}
	out.SelectedItems = items
	return out, nil
}

func (s *Store) loadCandidateItems(ctx context.Context, req BuildRequest) ([]ContextItem, error) {
	limit := req.ThreadContextLimit
	if limit <= 0 {
		limit = defaultThreadContextLimit
	}
	var rows *sql.Rows
	var err error
	if req.WorkspaceMode == WorkspaceModeOwnerOnly {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, event_id, item_type, role, author_label, text, bridge_id, bot_profile, target_id, target_name, session_name, message_id, created_at, token_estimate, truncated
			FROM thread_context_items
			WHERE root_msg_id=?
			  AND message_id<>?
			  AND (
			    (item_type=? AND bridge_id<>?)
			    OR (item_type IN (?, ?) AND message_id=root_msg_id AND bridge_id=?)
			  )
			ORDER BY id DESC
			LIMIT ?`, req.RootMsgID, req.MessageID, string(EventTypeAgentOutput), req.BridgeId,
			string(EventTypeTopicRoot), string(EventTypeHumanMessage), req.BridgeId, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, event_id, item_type, role, author_label, text, bridge_id, bot_profile, target_id, target_name, session_name, message_id, created_at, token_estimate, truncated
			FROM thread_context_items
			WHERE root_msg_id=?
			  AND message_id<>?
			  AND (
			    item_type IN (?, ?)
			    OR (item_type=? AND bridge_id<>?)
			  )
			ORDER BY id DESC
			LIMIT ?`, req.RootMsgID, req.MessageID, string(EventTypeTopicRoot), string(EventTypeHumanMessage),
			string(EventTypeAgentOutput), req.BridgeId, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reverse []ContextItem
	seen := make(map[string]struct{})
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		if key := dedupeKey(item); key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		reverse = append(reverse, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]ContextItem, 0, len(reverse))
	for i := len(reverse) - 1; i >= 0; i-- {
		items = append(items, reverse[i])
	}
	return items, nil
}

func dedupeKey(item ContextItem) string {
	if (item.ItemType != string(EventTypeTopicRoot) && item.ItemType != string(EventTypeHumanMessage)) ||
		strings.TrimSpace(item.MessageID) == "" {
		return ""
	}
	return "message:" + strings.TrimSpace(item.MessageID)
}

func (s *Store) loadDecision(ctx context.Context, req ExplainRequest) (Decision, error) {
	query := `
		SELECT id, chat_id, root_msg_id, message_id, bridge_id, bot_profile, workspace_mode, request_text,
		       thread_context, prompt_preview, selected_item_ids_json, excluded_json, diagnostics_json, created_at
		FROM thread_context_decisions
		WHERE root_msg_id=? AND bridge_id=?`
	args := []interface{}{req.RootMsgID, req.BridgeId}
	if req.DecisionID > 0 {
		query += ` AND id=?`
		args = append(args, req.DecisionID)
	}
	if strings.TrimSpace(req.MessageID) != "" {
		query += ` AND message_id=?`
		args = append(args, strings.TrimSpace(req.MessageID))
	}
	if strings.TrimSpace(req.BotProfile) != "" {
		query += ` AND bot_profile=?`
		args = append(args, strings.TrimSpace(req.BotProfile))
	}
	query += ` ORDER BY id DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, query, args...)
	decision, err := scanDecision(row)
	if err == sql.ErrNoRows {
		return Decision{}, fmt.Errorf("no context decision found for root_msg_id %q", req.RootMsgID)
	}
	return decision, err
}

func (s *Store) loadItemsByID(ctx context.Context, ids []int64) ([]ContextItem, error) {
	out := make([]ContextItem, 0, len(ids))
	for _, id := range ids {
		row := s.db.QueryRowContext(ctx, `
			SELECT id, event_id, item_type, role, author_label, text, bridge_id, bot_profile, target_id, target_name, session_name, message_id, created_at, token_estimate, truncated
			FROM thread_context_items
			WHERE id=?`, id)
		item, err := scanItem(row)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

type itemScanner interface {
	Scan(dest ...interface{}) error
}

func scanItem(scanner itemScanner) (ContextItem, error) {
	var item ContextItem
	var truncated int
	err := scanner.Scan(&item.ID, &item.EventId, &item.ItemType, &item.Role, &item.AuthorLabel, &item.Text, &item.BridgeId, &item.BotProfile, &item.TargetID, &item.TargetName, &item.SessionName, &item.MessageID, &item.CreatedAt, &item.TokenEstimate, &truncated)
	item.Truncated = truncated != 0
	return item, err
}

func scanDecision(row *sql.Row) (Decision, error) {
	var decision Decision
	var selectedJSON, excludedJSON, diagnosticsJSON string
	err := row.Scan(&decision.ID, &decision.ChatID, &decision.RootMsgID, &decision.MessageID, &decision.BridgeId, &decision.BotProfile, &decision.WorkspaceMode, &decision.RequestText, &decision.ThreadContext, &decision.PromptPreview, &selectedJSON, &excludedJSON, &diagnosticsJSON, &decision.CreatedAt)
	if err != nil {
		return decision, err
	}
	_ = json.Unmarshal([]byte(selectedJSON), &decision.SelectedItemIDs)
	_ = json.Unmarshal([]byte(excludedJSON), &decision.ExcludedItems)
	_ = json.Unmarshal([]byte(diagnosticsJSON), &decision.Diagnostics)
	return decision, nil
}

func normalizeEvent(ev ThreadEvent) ThreadEvent {
	ev.Source = firstNonEmpty(strings.TrimSpace(ev.Source), "feishu")
	ev.ChatID = strings.TrimSpace(ev.ChatID)
	ev.RootMsgID = strings.TrimSpace(ev.RootMsgID)
	ev.ThreadID = strings.TrimSpace(ev.ThreadID)
	ev.MessageID = strings.TrimSpace(ev.MessageID)
	ev.EventType = EventType(strings.TrimSpace(string(ev.EventType)))
	ev.ActorType = strings.TrimSpace(ev.ActorType)
	ev.ActorID = strings.TrimSpace(ev.ActorID)
	ev.BridgeId = strings.TrimSpace(ev.BridgeId)
	ev.BotProfile = strings.TrimSpace(ev.BotProfile)
	ev.TargetID = strings.TrimSpace(ev.TargetID)
	ev.TargetName = strings.TrimSpace(ev.TargetName)
	ev.SessionName = strings.TrimSpace(ev.SessionName)
	ev.Text = strings.TrimSpace(ev.Text)
	ev.MetadataJSON = strings.TrimSpace(ev.MetadataJSON)
	if ev.CreatedAt <= 0 {
		ev.CreatedAt = nowUnix()
	}
	return ev
}

func normalizeBuildRequest(req BuildRequest) BuildRequest {
	req.ChatID = strings.TrimSpace(req.ChatID)
	req.RootMsgID = strings.TrimSpace(req.RootMsgID)
	req.MessageID = strings.TrimSpace(req.MessageID)
	req.BridgeId = strings.TrimSpace(req.BridgeId)
	req.BotProfile = strings.TrimSpace(req.BotProfile)
	req.WorkspaceMode = strings.TrimSpace(req.WorkspaceMode)
	req.RequestText = strings.TrimSpace(req.RequestText)
	req.TargetID = strings.TrimSpace(req.TargetID)
	req.TargetName = strings.TrimSpace(req.TargetName)
	req.SessionName = strings.TrimSpace(req.SessionName)
	if req.ThreadContextLimit <= 0 {
		req.ThreadContextLimit = defaultThreadContextLimit
	}
	if req.MaxContextChars <= 0 {
		req.MaxContextChars = defaultMaxContextChars
	}
	return req
}

func shouldMaterializeItem(eventType EventType) bool {
	return eventType == EventTypeTopicRoot || eventType == EventTypeHumanMessage || eventType == EventTypeAgentOutput
}

func itemRole(eventType EventType) string {
	switch eventType {
	case EventTypeTopicRoot:
		return "context"
	case EventTypeAgentOutput:
		return "agent"
	default:
		return "human"
	}
}

func authorLabel(ev ThreadEvent) string {
	if ev.EventType == EventTypeTopicRoot {
		if ev.ActorType != "" && ev.ActorID != "" {
			return ev.ActorType + "/" + ev.ActorID
		}
		if ev.ActorID != "" {
			return ev.ActorID
		}
		if ev.ActorType != "" {
			return ev.ActorType
		}
		return "topic-root"
	}
	if ev.EventType == EventTypeAgentOutput {
		label := ev.SessionName
		if ev.TargetName != "" && label != "" {
			label = ev.TargetName + "/" + label
		} else if ev.TargetName != "" {
			label = ev.TargetName
		}
		if label != "" {
			return label
		}
		if ev.BotProfile != "" {
			return ev.BotProfile
		}
	}
	if ev.ActorID != "" {
		return ev.ActorID
	}
	return "user"
}

func formatItem(item ContextItem) string {
	prefix := strings.TrimSpace(item.AuthorLabel)
	if prefix == "" {
		prefix = item.Role
	}
	return fmt.Sprintf("%s: %s", prefix, item.Text)
}

func cleanupOldEvents(ctx context.Context, tx *sql.Tx, retentionDays int) error {
	if retentionDays <= 0 {
		return nil
	}
	cutoff := nowUnix() - int64(retentionDays*24*60*60)
	if _, err := tx.ExecContext(ctx, `DELETE FROM thread_context_events WHERE created_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		DELETE FROM thread_context_items
		WHERE event_id NOT IN (SELECT id FROM thread_context_events)`)
	return err
}

func mergeTruncatedMetadata(raw string) string {
	var meta map[string]interface{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &meta)
	}
	if meta == nil {
		meta = make(map[string]interface{})
	}
	meta["truncated"] = true
	out, _ := json.Marshal(meta)
	return string(out)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func nowUnix() int64 {
	return time.Now().Unix()
}
