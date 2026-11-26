package feishuexchange

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/coordination"
)

// Peer is a recently observed external Feishu Bot collaborator in a known group.
type Peer struct {
	CollaboratorID  string
	BridgeID        string
	ChatID          string
	BotOpenID       string
	BotAppID        string
	BotDisplayName  string
	ChatDisplayName string
	LastSeenAt      time.Time
}

// Exchange is the authoritative lifecycle record for one outbound external Bot
// delegation in Feishu.
type Exchange struct {
	BackendRef      string
	BridgeID        string
	CollaboratorID  string
	ChatID          string
	BotOpenID       string
	RootMessageID   string
	Status          coordination.Status
	Deadline        time.Time
	ResultContent   string
	ResultMessageID string
	FailureCode     coordination.FailureCode
	FailureMessage  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      time.Time
}

// ErrExchangeAlreadyRunning is returned when another non-terminal outbound
// exchange for the same bridge, thread root, and target Bot already exists.
var ErrExchangeAlreadyRunning = errors.New("an external Bot exchange is already running for this thread and Bot")

// Store owns the SQLite persistence and state transitions for external Feishu
// Bot peers, outbound exchanges, and exchange event logs. It operates on the
// caller-supplied SQLite connection so existing bridge.db tables remain intact.
type Store struct {
	db *sql.DB
}

// NewStore wraps an existing SQLite connection.
func NewStore(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

// EnsureSchema creates and migrates the external Bot peer, exchange, and event
// tables in the supplied SQLite database.
func EnsureSchema(db *sql.DB) error {
	if db == nil {
		return errors.New("exchange store db is required")
	}
	if _, err := db.Exec(`
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
	for _, stmt := range []string{
		`ALTER TABLE feishu_external_bot_peers ADD COLUMN bot_app_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE feishu_external_bot_peers ADD COLUMN bot_display_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE feishu_external_bot_peers ADD COLUMN chat_display_name TEXT NOT NULL DEFAULT ''`,
	} {
		if err := addColumnIfMissing(db, stmt); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_feishu_external_bot_peers_bridge
		ON feishu_external_bot_peers(bridge_id, last_seen_at DESC)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
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
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_feishu_external_bot_exchange_root`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_feishu_external_bot_exchange_active_root
		ON feishu_external_bot_exchanges(bridge_id, root_msg_id, bot_open_id)
		WHERE root_msg_id <> '' AND status IN ('pending', 'running')
	`); err != nil {
		return err
	}
	_, err := db.Exec(`
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
	`)
	return err
}

func addColumnIfMissing(db *sql.DB, stmt string) error {
	_, err := db.Exec(stmt)
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return nil
	}
	return err
}

// UpsertPeer inserts or updates one observed external Bot peer.
func (s *Store) UpsertPeer(peer Peer) error {
	if s == nil || s.db == nil {
		return errors.New("bridge db is required")
	}
	if strings.TrimSpace(peer.CollaboratorID) == "" || strings.TrimSpace(peer.BridgeID) == "" || strings.TrimSpace(peer.ChatID) == "" || strings.TrimSpace(peer.BotOpenID) == "" {
		return errors.New("external Bot peer requires collaborator, bridge, chat, and Bot ids")
	}
	when := peer.LastSeenAt.UTC()
	if when.IsZero() {
		when = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO feishu_external_bot_peers
			(collaborator_id, bridge_id, chat_id, bot_open_id, bot_app_id, bot_display_name, chat_display_name, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bridge_id, chat_id, bot_open_id) DO UPDATE SET
			collaborator_id=excluded.collaborator_id,
			bot_app_id=CASE WHEN excluded.bot_app_id<>'' THEN excluded.bot_app_id ELSE bot_app_id END,
			bot_display_name=CASE WHEN excluded.bot_display_name<>'' THEN excluded.bot_display_name ELSE bot_display_name END,
			chat_display_name=CASE WHEN excluded.chat_display_name<>'' THEN excluded.chat_display_name ELSE chat_display_name END,
			last_seen_at=excluded.last_seen_at`,
		peer.CollaboratorID, peer.BridgeID, peer.ChatID, peer.BotOpenID, strings.TrimSpace(peer.BotAppID),
		strings.TrimSpace(peer.BotDisplayName), strings.TrimSpace(peer.ChatDisplayName), when.UnixMilli())
	return err
}

// SyncPeers updates the cached peer directory for one group. When complete is
// true, previously cached peers no longer present in the snapshot are pruned.
func (s *Store) SyncPeers(bridgeID, chatID string, peers []Peer, complete bool) error {
	if s == nil || s.db == nil {
		return errors.New("bridge db is required")
	}
	bridgeID = strings.TrimSpace(bridgeID)
	chatID = strings.TrimSpace(chatID)
	if bridgeID == "" || chatID == "" {
		return errors.New("external Bot peer sync requires bridge and chat ids")
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if complete {
		if _, err := tx.Exec(`DELETE FROM feishu_external_bot_peers WHERE bridge_id=? AND chat_id=?`, bridgeID, chatID); err != nil {
			return err
		}
	}
	for _, peer := range peers {
		if strings.TrimSpace(peer.CollaboratorID) == "" || strings.TrimSpace(peer.BotOpenID) == "" {
			return errors.New("external Bot peer sync requires collaborator and Bot ids")
		}
		when := peer.LastSeenAt.UTC()
		if when.IsZero() {
			when = time.Now().UTC()
		}
		if _, err := tx.Exec(`
			INSERT INTO feishu_external_bot_peers
				(collaborator_id, bridge_id, chat_id, bot_open_id, bot_app_id, bot_display_name, chat_display_name, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(bridge_id, chat_id, bot_open_id) DO UPDATE SET
				collaborator_id=excluded.collaborator_id,
				bot_app_id=CASE WHEN excluded.bot_app_id<>'' THEN excluded.bot_app_id ELSE bot_app_id END,
				bot_display_name=CASE WHEN excluded.bot_display_name<>'' THEN excluded.bot_display_name ELSE bot_display_name END,
				chat_display_name=CASE WHEN excluded.chat_display_name<>'' THEN excluded.chat_display_name ELSE chat_display_name END,
				last_seen_at=excluded.last_seen_at`,
			peer.CollaboratorID, bridgeID, chatID, peer.BotOpenID, strings.TrimSpace(peer.BotAppID),
			strings.TrimSpace(peer.BotDisplayName), strings.TrimSpace(peer.ChatDisplayName), when.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListPeers returns all cached peers for one bridge ordered by recency.
func (s *Store) ListPeers(bridgeID string) ([]Peer, error) {
	rows, err := s.db.Query(`
		SELECT collaborator_id, bridge_id, chat_id, bot_open_id, bot_app_id, bot_display_name, chat_display_name, last_seen_at
		FROM feishu_external_bot_peers
		WHERE bridge_id=?
		ORDER BY last_seen_at DESC, collaborator_id`, strings.TrimSpace(bridgeID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var peers []Peer
	for rows.Next() {
		var peer Peer
		var lastSeen int64
		if err := rows.Scan(&peer.CollaboratorID, &peer.BridgeID, &peer.ChatID, &peer.BotOpenID, &peer.BotAppID, &peer.BotDisplayName, &peer.ChatDisplayName, &lastSeen); err != nil {
			return nil, err
		}
		peer.LastSeenAt = time.UnixMilli(lastSeen).UTC()
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}

// ListPeersForChat returns cached peers scoped to one bridge and group chat.
func (s *Store) ListPeersForChat(bridgeID, chatID string) ([]Peer, error) {
	rows, err := s.db.Query(`
		SELECT collaborator_id, bridge_id, chat_id, bot_open_id, bot_app_id, bot_display_name, chat_display_name, last_seen_at
		FROM feishu_external_bot_peers
		WHERE bridge_id=? AND chat_id=?
		ORDER BY last_seen_at DESC, collaborator_id`, strings.TrimSpace(bridgeID), strings.TrimSpace(chatID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var peers []Peer
	for rows.Next() {
		var peer Peer
		var lastSeen int64
		if err := rows.Scan(&peer.CollaboratorID, &peer.BridgeID, &peer.ChatID, &peer.BotOpenID, &peer.BotAppID, &peer.BotDisplayName, &peer.ChatDisplayName, &lastSeen); err != nil {
			return nil, err
		}
		peer.LastSeenAt = time.UnixMilli(lastSeen).UTC()
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}

// Peer returns a single cached peer by bridge and opaque collaborator ID.
func (s *Store) Peer(bridgeID, collaboratorID string) (*Peer, error) {
	var peer Peer
	var lastSeen int64
	err := s.db.QueryRow(`
		SELECT collaborator_id, bridge_id, chat_id, bot_open_id, bot_app_id, bot_display_name, chat_display_name, last_seen_at
		FROM feishu_external_bot_peers
		WHERE bridge_id=? AND collaborator_id=?`, strings.TrimSpace(bridgeID), strings.TrimSpace(collaboratorID)).
		Scan(&peer.CollaboratorID, &peer.BridgeID, &peer.ChatID, &peer.BotOpenID, &peer.BotAppID, &peer.BotDisplayName, &peer.ChatDisplayName, &lastSeen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	peer.LastSeenAt = time.UnixMilli(lastSeen).UTC()
	return &peer, nil
}

// ExchangeExists reports whether an exchange with backendRef is already recorded.
func (s *Store) ExchangeExists(backendRef string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM feishu_external_bot_exchanges WHERE backend_ref=?`, strings.TrimSpace(backendRef)).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// CreateExchange idempotently inserts a pending exchange and its initial event.
func (s *Store) CreateExchange(exchange Exchange) (bool, error) {
	when := exchange.CreatedAt.UTC()
	if when.IsZero() {
		when = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if rootMessageID := strings.TrimSpace(exchange.RootMessageID); rootMessageID != "" {
		var exists int
		err := tx.QueryRow(`
			SELECT 1 FROM feishu_external_bot_exchanges
			WHERE bridge_id=? AND root_msg_id=? AND bot_open_id=? AND status IN (?, ?)
			LIMIT 1`, strings.TrimSpace(exchange.BridgeID), rootMessageID, strings.TrimSpace(exchange.BotOpenID), coordination.StatusPending, coordination.StatusRunning).Scan(&exists)
		if err == nil {
			return false, ErrExchangeAlreadyRunning
		}
		if err != sql.ErrNoRows {
			return false, err
		}
	}
	result, err := tx.Exec(`
		INSERT INTO feishu_external_bot_exchanges
			(backend_ref, bridge_id, collaborator_id, chat_id, bot_open_id, root_msg_id, status, deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(backend_ref) DO NOTHING`, exchange.BackendRef, exchange.BridgeID, exchange.CollaboratorID,
		exchange.ChatID, exchange.BotOpenID, strings.TrimSpace(exchange.RootMessageID), coordination.StatusPending, exchange.Deadline.UnixMilli(), when.UnixMilli(), when.UnixMilli())
	if err != nil {
		if strings.TrimSpace(exchange.RootMessageID) != "" {
			var exists int
			if checkErr := tx.QueryRow(`
				SELECT 1 FROM feishu_external_bot_exchanges
				WHERE bridge_id=? AND root_msg_id=? AND bot_open_id=? AND status IN (?, ?)
				LIMIT 1`, strings.TrimSpace(exchange.BridgeID), strings.TrimSpace(exchange.RootMessageID), strings.TrimSpace(exchange.BotOpenID), coordination.StatusPending, coordination.StatusRunning).Scan(&exists); checkErr == nil {
				return false, ErrExchangeAlreadyRunning
			}
		}
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted == 0 {
		return false, tx.Commit()
	}
	if err := insertEvent(tx, exchange.BackendRef, 1, coordination.EventStatusChanged, coordination.StatusPending, "", "", "", "", "", when); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// MarkExchangeRunning transitions a pending exchange to running after platform send.
func (s *Store) MarkExchangeRunning(backendRef, rootMessageID string, when time.Time) error {
	return s.transition(backendRef, coordination.StatusPending, coordination.StatusRunning, rootMessageID, "", "", "", "", when)
}

// FailExchange records a terminal failure without updating the root message ID.
func (s *Store) FailExchange(backendRef string, code coordination.FailureCode, message string, when time.Time) error {
	return s.FailExchangeWithRoot(backendRef, "", code, message, when)
}

// FailExchangeWithRoot records a terminal failure while preserving correlation root.
func (s *Store) FailExchangeWithRoot(backendRef, rootMessageID string, code coordination.FailureCode, message string, when time.Time) error {
	return s.transition(backendRef, "", coordination.StatusFailed, rootMessageID, "", "", code, message, when)
}

func (s *Store) transition(backendRef string, required, next coordination.Status, rootMessageID, result, resultMessageID string, failureCode coordination.FailureCode, failureMessage string, when time.Time) error {
	when = when.UTC()
	if when.IsZero() {
		when = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current coordination.Status
	var seq uint64
	err = tx.QueryRow(`SELECT status, COALESCE((SELECT MAX(seq) FROM feishu_external_bot_events WHERE backend_ref=?), 0) FROM feishu_external_bot_exchanges WHERE backend_ref=?`, backendRef, backendRef).Scan(&current, &seq)
	if err != nil {
		return err
	}
	if current.IsTerminal() {
		return tx.Commit()
	}
	if required != "" && current != required {
		return fmt.Errorf("external Bot exchange %q is %s, want %s", backendRef, current, required)
	}
	finished := int64(0)
	if next.IsTerminal() {
		finished = when.UnixMilli()
	}
	_, err = tx.Exec(`
		UPDATE feishu_external_bot_exchanges
		SET root_msg_id=CASE WHEN ?<>'' THEN ? ELSE root_msg_id END,
			status=?, result_content=?, result_message_id=?, failure_code=?, failure_message=?, updated_at=?, finished_at=?
		WHERE backend_ref=?`, rootMessageID, rootMessageID, next, result, resultMessageID, failureCode, failureMessage, when.UnixMilli(), finished, backendRef)
	if err != nil {
		return err
	}
	kind := coordination.EventStatusChanged
	if next == coordination.StatusSucceeded {
		kind = coordination.EventResult
	} else if next == coordination.StatusFailed || next == coordination.StatusTimedOut {
		kind = coordination.EventError
	}
	if err := insertEvent(tx, backendRef, seq+1, kind, next, "", result, resultMessageID, string(failureCode), failureMessage, when); err != nil {
		return err
	}
	return tx.Commit()
}

func insertEvent(tx *sql.Tx, ref string, seq uint64, kind coordination.EventKind, status coordination.Status, content, result, resultMessageID, failureCode, failureMessage string, when time.Time) error {
	_, err := tx.Exec(`
		INSERT INTO feishu_external_bot_events
			(backend_ref, seq, kind, status, content, result_content, result_message_id, failure_code, failure_message, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ref, seq, kind, status, content, result, resultMessageID, failureCode, failureMessage, when.UnixMilli())
	return err
}

// CompleteReply correlates an inbound structured-mention reply from a target Bot
// to an outbound exchange in the same thread. Terminal exchanges still consume
// late/duplicate replies so they do not fall back into legacy Bot-to-Bot routing.
func (s *Store) CompleteReply(bridgeID, rootMessageID, botOpenID, messageID, text string, when time.Time) (bool, error) {
	var ref string
	err := s.db.QueryRow(`
		SELECT backend_ref FROM feishu_external_bot_exchanges
		WHERE bridge_id=? AND root_msg_id=? AND bot_open_id=?
			AND status IN (?, ?, ?, ?, ?)
		ORDER BY CASE WHEN status=? THEN 0 ELSE 1 END, created_at DESC
		LIMIT 1`, strings.TrimSpace(bridgeID), strings.TrimSpace(rootMessageID), strings.TrimSpace(botOpenID),
		coordination.StatusRunning, coordination.StatusSucceeded, coordination.StatusFailed, coordination.StatusCancelled, coordination.StatusTimedOut,
		coordination.StatusRunning).Scan(&ref)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.adjudicateDeadline(ref, when); err != nil {
		return true, err
	}
	if err := s.transition(ref, coordination.StatusRunning, coordination.StatusSucceeded, "", strings.TrimSpace(text), strings.TrimSpace(messageID), "", "", when); err != nil {
		var status coordination.Status
		if scanErr := s.db.QueryRow(`SELECT status FROM feishu_external_bot_exchanges WHERE backend_ref=?`, ref).Scan(&status); scanErr == nil && status.IsTerminal() {
			return true, nil
		}
		return true, err
	}
	return true, nil
}

// RecoverExchanges fails any pending exchanges whose outbound delivery outcome
// became uncertain across a process restart.
func (s *Store) RecoverExchanges(bridgeID string, when time.Time) error {
	rows, err := s.db.Query(`SELECT backend_ref FROM feishu_external_bot_exchanges WHERE bridge_id=? AND status=?`, strings.TrimSpace(bridgeID), coordination.StatusPending)
	if err != nil {
		return err
	}
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, ref)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, ref := range refs {
		if err := s.FailExchange(ref, coordination.FailureBackendFailed, "platform delivery outcome is unknown after restart", when); err != nil {
			return err
		}
	}
	return nil
}

// ReadExchange adjudicates any elapsed deadline, then returns the exchange
// snapshot and events strictly after fromSeq.
func (s *Store) ReadExchange(backendRef string, fromSeq uint64, now time.Time) (*Exchange, []coordination.Event, uint64, error) {
	if err := s.adjudicateDeadline(backendRef, now); err != nil {
		return nil, nil, fromSeq, err
	}
	row := s.db.QueryRow(`
		SELECT backend_ref, bridge_id, collaborator_id, chat_id, bot_open_id, root_msg_id, status, deadline_at,
			result_content, result_message_id, failure_code, failure_message, created_at, updated_at, finished_at
		FROM feishu_external_bot_exchanges WHERE backend_ref=?`, strings.TrimSpace(backendRef))
	exchange, err := scanExchange(row)
	if err != nil {
		return nil, nil, fromSeq, err
	}
	rows, err := s.db.Query(`
		SELECT seq, kind, status, content, result_content, result_message_id, failure_code, failure_message, at
		FROM feishu_external_bot_events WHERE backend_ref=? AND seq>? ORDER BY seq`, backendRef, fromSeq)
	if err != nil {
		return nil, nil, fromSeq, err
	}
	defer rows.Close()
	events := make([]coordination.Event, 0)
	next := fromSeq
	for rows.Next() {
		var event coordination.Event
		var status coordination.Status
		var kind coordination.EventKind
		var result, resultMessageID, failureCode, failureMessage string
		var at int64
		if err := rows.Scan(&event.Seq, &kind, &status, &event.Content, &result, &resultMessageID, &failureCode, &failureMessage, &at); err != nil {
			return nil, nil, fromSeq, err
		}
		event.DelegationID = strings.TrimPrefix(backendRef, ExchangePrefix)
		event.Kind, event.Status, event.At = kind, status, time.UnixMilli(at).UTC()
		if result != "" {
			event.Result = resultEnvelope(result, resultMessageID)
		}
		if failureCode != "" {
			event.Failure = &coordination.Failure{Code: coordination.FailureCode(failureCode), Message: failureMessage}
		}
		if event.Seq > next {
			next = event.Seq
		}
		events = append(events, event)
	}
	return exchange, events, next, rows.Err()
}

func (s *Store) adjudicateDeadline(backendRef string, now time.Time) error {
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var status coordination.Status
	var deadline int64
	err := s.db.QueryRow(`SELECT status, deadline_at FROM feishu_external_bot_exchanges WHERE backend_ref=?`, strings.TrimSpace(backendRef)).Scan(&status, &deadline)
	if err != nil {
		return err
	}
	if !status.IsTerminal() && deadline > 0 && !now.Before(time.UnixMilli(deadline)) {
		return s.transition(backendRef, "", coordination.StatusTimedOut, "", "", "", coordination.FailureDeadlineExceeded, "external Bot did not reply before the deadline", now)
	}
	return nil
}

func scanExchange(row *sql.Row) (*Exchange, error) {
	var exchange Exchange
	var deadline, created, updated, finished int64
	err := row.Scan(&exchange.BackendRef, &exchange.BridgeID, &exchange.CollaboratorID, &exchange.ChatID, &exchange.BotOpenID,
		&exchange.RootMessageID, &exchange.Status, &deadline, &exchange.ResultContent, &exchange.ResultMessageID,
		&exchange.FailureCode, &exchange.FailureMessage, &created, &updated, &finished)
	if err != nil {
		return nil, err
	}
	exchange.Deadline = time.UnixMilli(deadline).UTC()
	exchange.CreatedAt = time.UnixMilli(created).UTC()
	exchange.UpdatedAt = time.UnixMilli(updated).UTC()
	if finished > 0 {
		exchange.FinishedAt = time.UnixMilli(finished).UTC()
	}
	return &exchange, nil
}

func resultEnvelope(content, messageID string) *coordination.ResultEnvelope {
	sources := []string(nil)
	if strings.TrimSpace(messageID) != "" {
		sources = []string{"feishu-message:" + strings.TrimSpace(messageID)}
	}
	return &coordination.ResultEnvelope{
		Summary: resultSummary(content), Content: content, ContentType: "text/plain",
		TrustLevel: coordination.TrustExternalUntrusted, SourceRefs: sources,
	}
}

func resultSummary(content string) string {
	const maxRunes = 240
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes])
}
