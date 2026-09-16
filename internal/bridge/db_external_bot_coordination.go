package bridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/coordination"
)

type externalBotPeer struct {
	CollaboratorID  string
	BridgeID        string
	ChatID          string
	BotOpenID       string
	BotAppID        string
	BotDisplayName  string
	ChatDisplayName string
	LastSeenAt      time.Time
}

type externalBotExchange struct {
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

var errExternalBotExchangeAlreadyRunning = errors.New("an external Bot exchange is already running for this thread and Bot")

func (b *bridgeDB) UpsertExternalBotPeer(peer externalBotPeer) error {
	if b == nil || b.db == nil {
		return errors.New("bridge db is required")
	}
	if strings.TrimSpace(peer.CollaboratorID) == "" || strings.TrimSpace(peer.BridgeID) == "" || strings.TrimSpace(peer.ChatID) == "" || strings.TrimSpace(peer.BotOpenID) == "" {
		return errors.New("external Bot peer requires collaborator, bridge, chat, and Bot ids")
	}
	when := peer.LastSeenAt.UTC()
	if when.IsZero() {
		when = time.Now().UTC()
	}
	_, err := b.db.Exec(`
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

func (b *bridgeDB) SyncExternalBotPeers(bridgeID, chatID string, peers []externalBotPeer, complete bool) error {
	if b == nil || b.db == nil {
		return errors.New("bridge db is required")
	}
	bridgeID = strings.TrimSpace(bridgeID)
	chatID = strings.TrimSpace(chatID)
	if bridgeID == "" || chatID == "" {
		return errors.New("external Bot peer sync requires bridge and chat ids")
	}
	tx, err := b.db.BeginTx(context.Background(), nil)
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

func (b *bridgeDB) ListExternalBotPeers(bridgeID string) ([]externalBotPeer, error) {
	rows, err := b.db.Query(`
		SELECT collaborator_id, bridge_id, chat_id, bot_open_id, bot_app_id, bot_display_name, chat_display_name, last_seen_at
		FROM feishu_external_bot_peers
		WHERE bridge_id=?
		ORDER BY last_seen_at DESC, collaborator_id`, strings.TrimSpace(bridgeID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var peers []externalBotPeer
	for rows.Next() {
		var peer externalBotPeer
		var lastSeen int64
		if err := rows.Scan(&peer.CollaboratorID, &peer.BridgeID, &peer.ChatID, &peer.BotOpenID, &peer.BotAppID, &peer.BotDisplayName, &peer.ChatDisplayName, &lastSeen); err != nil {
			return nil, err
		}
		peer.LastSeenAt = time.UnixMilli(lastSeen).UTC()
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}

func (b *bridgeDB) ListExternalBotPeersForChat(bridgeID, chatID string) ([]externalBotPeer, error) {
	rows, err := b.db.Query(`
		SELECT collaborator_id, bridge_id, chat_id, bot_open_id, bot_app_id, bot_display_name, chat_display_name, last_seen_at
		FROM feishu_external_bot_peers
		WHERE bridge_id=? AND chat_id=?
		ORDER BY last_seen_at DESC, collaborator_id`, strings.TrimSpace(bridgeID), strings.TrimSpace(chatID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var peers []externalBotPeer
	for rows.Next() {
		var peer externalBotPeer
		var lastSeen int64
		if err := rows.Scan(&peer.CollaboratorID, &peer.BridgeID, &peer.ChatID, &peer.BotOpenID, &peer.BotAppID, &peer.BotDisplayName, &peer.ChatDisplayName, &lastSeen); err != nil {
			return nil, err
		}
		peer.LastSeenAt = time.UnixMilli(lastSeen).UTC()
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}

func (b *bridgeDB) ExternalBotPeer(bridgeID, collaboratorID string) (*externalBotPeer, error) {
	var peer externalBotPeer
	var lastSeen int64
	err := b.db.QueryRow(`
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

func (b *bridgeDB) ExternalBotExchangeExists(backendRef string) (bool, error) {
	var exists int
	err := b.db.QueryRow(`SELECT 1 FROM feishu_external_bot_exchanges WHERE backend_ref=?`, strings.TrimSpace(backendRef)).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (b *bridgeDB) CreateExternalBotExchange(exchange externalBotExchange) (bool, error) {
	when := exchange.CreatedAt.UTC()
	if when.IsZero() {
		when = time.Now().UTC()
	}
	tx, err := b.db.BeginTx(context.Background(), nil)
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
			return false, errExternalBotExchangeAlreadyRunning
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
				return false, errExternalBotExchangeAlreadyRunning
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
	if err := insertExternalBotEvent(tx, exchange.BackendRef, 1, coordination.EventStatusChanged, coordination.StatusPending, "", "", "", "", "", when); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (b *bridgeDB) MarkExternalBotExchangeRunning(backendRef, rootMessageID string, when time.Time) error {
	return b.transitionExternalBotExchange(backendRef, coordination.StatusPending, coordination.StatusRunning, rootMessageID, "", "", "", "", when)
}

func (b *bridgeDB) FailExternalBotExchange(backendRef string, code coordination.FailureCode, message string, when time.Time) error {
	return b.FailExternalBotExchangeWithRoot(backendRef, "", code, message, when)
}

func (b *bridgeDB) FailExternalBotExchangeWithRoot(backendRef, rootMessageID string, code coordination.FailureCode, message string, when time.Time) error {
	return b.transitionExternalBotExchange(backendRef, "", coordination.StatusFailed, rootMessageID, "", "", code, message, when)
}

func (b *bridgeDB) transitionExternalBotExchange(backendRef string, required, next coordination.Status, rootMessageID, result, resultMessageID string, failureCode coordination.FailureCode, failureMessage string, when time.Time) error {
	when = when.UTC()
	if when.IsZero() {
		when = time.Now().UTC()
	}
	tx, err := b.db.BeginTx(context.Background(), nil)
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
	if err := insertExternalBotEvent(tx, backendRef, seq+1, kind, next, "", result, resultMessageID, string(failureCode), failureMessage, when); err != nil {
		return err
	}
	return tx.Commit()
}

func insertExternalBotEvent(tx *sql.Tx, ref string, seq uint64, kind coordination.EventKind, status coordination.Status, content, result, resultMessageID, failureCode, failureMessage string, when time.Time) error {
	_, err := tx.Exec(`
		INSERT INTO feishu_external_bot_events
			(backend_ref, seq, kind, status, content, result_content, result_message_id, failure_code, failure_message, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ref, seq, kind, status, content, result, resultMessageID, failureCode, failureMessage, when.UnixMilli())
	return err
}

func (b *bridgeDB) CompleteExternalBotExchangeReply(bridgeID, rootMessageID, botOpenID, messageID, text string, when time.Time) (bool, error) {
	var ref string
	err := b.db.QueryRow(`
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
	if err := b.adjudicateExternalBotDeadline(ref, when); err != nil {
		return true, err
	}
	if err := b.transitionExternalBotExchange(ref, coordination.StatusRunning, coordination.StatusSucceeded, "", strings.TrimSpace(text), strings.TrimSpace(messageID), "", "", when); err != nil {
		var status coordination.Status
		if scanErr := b.db.QueryRow(`SELECT status FROM feishu_external_bot_exchanges WHERE backend_ref=?`, ref).Scan(&status); scanErr == nil && status.IsTerminal() {
			return true, nil
		}
		return true, err
	}
	return true, nil
}

func (b *bridgeDB) RecoverExternalBotExchanges(bridgeID string, when time.Time) error {
	rows, err := b.db.Query(`SELECT backend_ref FROM feishu_external_bot_exchanges WHERE bridge_id=? AND status=?`, strings.TrimSpace(bridgeID), coordination.StatusPending)
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
		if err := b.FailExternalBotExchange(ref, coordination.FailureBackendFailed, "platform delivery outcome is unknown after restart", when); err != nil {
			return err
		}
	}
	return nil
}

func (b *bridgeDB) ReadExternalBotExchange(backendRef string, fromSeq uint64, now time.Time) (*externalBotExchange, []coordination.Event, uint64, error) {
	if err := b.adjudicateExternalBotDeadline(backendRef, now); err != nil {
		return nil, nil, fromSeq, err
	}
	row := b.db.QueryRow(`
		SELECT backend_ref, bridge_id, collaborator_id, chat_id, bot_open_id, root_msg_id, status, deadline_at,
			result_content, result_message_id, failure_code, failure_message, created_at, updated_at, finished_at
		FROM feishu_external_bot_exchanges WHERE backend_ref=?`, strings.TrimSpace(backendRef))
	exchange, err := scanExternalBotExchange(row)
	if err != nil {
		return nil, nil, fromSeq, err
	}
	rows, err := b.db.Query(`
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
		event.DelegationID = strings.TrimPrefix(backendRef, externalBotExchangePrefix)
		event.Kind, event.Status, event.At = kind, status, time.UnixMilli(at).UTC()
		if result != "" {
			event.Result = externalBotResult(result, resultMessageID)
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

func (b *bridgeDB) adjudicateExternalBotDeadline(backendRef string, now time.Time) error {
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var status coordination.Status
	var deadline int64
	err := b.db.QueryRow(`SELECT status, deadline_at FROM feishu_external_bot_exchanges WHERE backend_ref=?`, strings.TrimSpace(backendRef)).Scan(&status, &deadline)
	if err != nil {
		return err
	}
	if !status.IsTerminal() && deadline > 0 && !now.Before(time.UnixMilli(deadline)) {
		return b.transitionExternalBotExchange(backendRef, "", coordination.StatusTimedOut, "", "", "", coordination.FailureDeadlineExceeded, "external Bot did not reply before the deadline", now)
	}
	return nil
}

func scanExternalBotExchange(row *sql.Row) (*externalBotExchange, error) {
	var exchange externalBotExchange
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

func externalBotResult(content, messageID string) *coordination.ResultEnvelope {
	sources := []string(nil)
	if strings.TrimSpace(messageID) != "" {
		sources = []string{"feishu-message:" + strings.TrimSpace(messageID)}
	}
	return &coordination.ResultEnvelope{
		Summary: externalBotSummary(content), Content: content, ContentType: "text/plain",
		TrustLevel: coordination.TrustExternalUntrusted, SourceRefs: sources,
	}
}

func externalBotSummary(content string) string {
	const maxRunes = 240
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes])
}
