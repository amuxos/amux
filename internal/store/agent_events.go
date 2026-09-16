package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/amuxos/amux/internal/agentstream"
)

var (
	ErrTimelineEpochMismatch = errors.New("session timeline epoch mismatch")
	ErrTimelineSequence      = errors.New("session timeline sequence conflict")
)

// AppendAgentEvent implements agentstream.Journal. It validates the immutable
// Session epoch and exact next sequence inside one SQLite transaction.
func (db *DB) AppendAgentEvent(ctx context.Context, event agentstream.Event) error {
	raw, err := agentstream.MarshalEvent(event)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var epoch string
	if err := tx.QueryRowContext(ctx, `SELECT timeline_epoch FROM sessions WHERE id=?`, event.SessionID).Scan(&epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("session %q not found", event.SessionID)
		}
		return err
	}
	if epoch == "" || epoch != event.Cursor.Epoch {
		return fmt.Errorf("%w: session=%q stored=%q event=%q", ErrTimelineEpochMismatch, event.SessionID, epoch, event.Cursor.Epoch)
	}

	var existing string
	err = tx.QueryRowContext(ctx, `SELECT event_json FROM session_timeline_events WHERE session_id=? AND seq=?`, event.SessionID, event.Cursor.Seq).Scan(&existing)
	switch {
	case err == nil:
		if existing != string(raw) {
			return fmt.Errorf("%w: conflicting event at seq %d", ErrTimelineSequence, event.Cursor.Seq)
		}
		return tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	var maxSeq uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM session_timeline_events WHERE session_id=?`, event.SessionID).Scan(&maxSeq); err != nil {
		return err
	}
	if event.Cursor.Seq != maxSeq+1 {
		return fmt.Errorf("%w: got seq %d after %d", ErrTimelineSequence, event.Cursor.Seq, maxSeq)
	}
	turnID := ""
	var turnSeq uint64
	if event.Turn != nil {
		turnID = event.Turn.ID
		turnSeq = event.Turn.Seq
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_timeline_events
			(session_id, seq, epoch, turn_id, turn_seq, kind, event_json, at_unix_nano)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.SessionID, event.Cursor.Seq, event.Cursor.Epoch, turnID, turnSeq, event.Body.Kind(), string(raw), event.At.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

// LoadAgentEvents implements agentstream.Journal and fails closed on any row
// whose indexed envelope disagrees with its canonical JSON.
func (db *DB) LoadAgentEvents(ctx context.Context, sessionID string) ([]agentstream.Event, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT seq, epoch, turn_id, turn_seq, kind, event_json, at_unix_nano
		FROM session_timeline_events WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []agentstream.Event
	for rows.Next() {
		var (
			seq        uint64
			epoch      string
			turnID     string
			turnSeq    uint64
			kind       string
			raw        string
			atUnixNano int64
		)
		if err := rows.Scan(&seq, &epoch, &turnID, &turnSeq, &kind, &raw, &atUnixNano); err != nil {
			return nil, err
		}
		event, err := agentstream.UnmarshalEvent([]byte(raw))
		if err != nil {
			return nil, err
		}
		rowTurnID := ""
		var rowTurnSeq uint64
		if event.Turn != nil {
			rowTurnID = event.Turn.ID
			rowTurnSeq = event.Turn.Seq
		}
		if event.SessionID != sessionID || event.Cursor.Seq != seq || event.Cursor.Epoch != epoch ||
			rowTurnID != turnID || rowTurnSeq != turnSeq || string(event.Body.Kind()) != kind || event.At.UnixNano() != atUnixNano {
			return nil, fmt.Errorf("%w: indexed envelope mismatch at seq %d", agentstream.ErrCorruptJournal, seq)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
