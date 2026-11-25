package turnresult

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// DefaultPath returns ~/.amux/turn_results.db.
func DefaultPath() string {
	return filepath.Join(amuxhome.Dir(), "turn_results.db")
}

// Store persists bounded, opt-in single-turn result snapshots in SQLite.
type Store struct {
	db     *sql.DB
	policy Policy
	mu     sync.Mutex
}

// OpenStore opens (creating if needed) the TurnResult SQLite store and migrates it.
func OpenStore(path string, policy Policy) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("turnresult db mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("turnresult db open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("turnresult db pragma: %w", err)
	}
	s := &Store{db: db, policy: policy.Normalize()}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying SQLite connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Policy returns the active normalized retention policy.
func (s *Store) Policy() Policy {
	if s == nil {
		return DefaultPolicy()
	}
	return s.policy
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS turn_results (
			record_id               TEXT    PRIMARY KEY,
			status                  TEXT    NOT NULL,
			reason                  TEXT    NOT NULL DEFAULT '',
			target_id               TEXT    NOT NULL DEFAULT '',
			session_id              TEXT    NOT NULL DEFAULT '',
			session_name            TEXT    NOT NULL DEFAULT '',
			launch_id               TEXT    NOT NULL DEFAULT '',
			turn_id                 TEXT    NOT NULL DEFAULT '',
			schedule_id             INTEGER NOT NULL DEFAULT 0,
			execution_id            INTEGER NOT NULL DEFAULT 0,
			task_id                 TEXT    NOT NULL DEFAULT '',
			bridge_id               TEXT    NOT NULL DEFAULT '',
			source_json             BLOB    NOT NULL,
			execution_json          BLOB    NOT NULL,
			output_json             BLOB    NOT NULL,
			deliveries_json         BLOB    NOT NULL,
			delivery_expected       INTEGER NOT NULL DEFAULT 0,
			execution_settled       INTEGER NOT NULL DEFAULT 0,
			delivery_settled        INTEGER NOT NULL DEFAULT 0,
			artifacts_pending       INTEGER NOT NULL DEFAULT 0,
			owner_principal_id      TEXT    NOT NULL DEFAULT '',
			execution_identity_json TEXT    NOT NULL DEFAULT '',
			payload_bytes           INTEGER NOT NULL DEFAULT 0,
			created_at              INTEGER NOT NULL,
			updated_at              INTEGER NOT NULL,
			settled_at              INTEGER NOT NULL DEFAULT 0,
			expires_at              INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_turn_results_session_turn
			ON turn_results(session_id, turn_id);
		CREATE INDEX IF NOT EXISTS idx_turn_results_target_session_turn
			ON turn_results(target_id, session_name, turn_id);
		CREATE INDEX IF NOT EXISTS idx_turn_results_turn_id
			ON turn_results(turn_id);
		CREATE INDEX IF NOT EXISTS idx_turn_results_schedule_exec
			ON turn_results(schedule_id, execution_id DESC);
		CREATE INDEX IF NOT EXISTS idx_turn_results_task_id
			ON turn_results(task_id);
		CREATE INDEX IF NOT EXISTS idx_turn_results_session_created
			ON turn_results(session_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_turn_results_target_session_created
			ON turn_results(target_id, session_name, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_turn_results_expires
			ON turn_results(expires_at);
		CREATE TABLE IF NOT EXISTS turn_result_feishu_messages (
			bridge_id  TEXT NOT NULL,
			message_id TEXT NOT NULL,
			record_id  TEXT NOT NULL REFERENCES turn_results(record_id) ON DELETE CASCADE,
			PRIMARY KEY (bridge_id, message_id)
		);
		CREATE INDEX IF NOT EXISTS idx_turn_result_feishu_msg_id
			ON turn_result_feishu_messages(message_id);
	`)
	if err != nil {
		return fmt.Errorf("turnresult migrate: %w", err)
	}
	return nil
}

// ClearAll removes every stored single-turn result snapshot. Used when
// turn_results.enabled is false so disabling the feature clears cached data.
func (s *Store) ClearAll() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM turn_result_feishu_messages`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM turn_results`); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearFile deletes any cached turn results in the SQLite file at path if it
// exists. Missing files are ignored without creating a new database.
func ClearFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	store, err := OpenStore(path, DefaultPolicy())
	if err != nil {
		return err
	}
	defer store.Close()
	return store.ClearAll()
}

// RegisterSubmission records or merges a newly accepted turn or scheduled execution.
func (s *Store) RegisterSubmission(in SubmissionInput) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, nil
	}
	now := nowOr(in.Now)
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, found, err := s.findMatchingLocked(in.Ref, in.Source)
	if err != nil {
		return Record{}, err
	}
	if !found {
		rec = Record{
			RecordID:  uuid.NewString(),
			Status:    StatusNotReady,
			Execution: Execution{Status: ExecutionRunning},
			Output:    Output{Status: OutputUnavailable},
			CreatedAt: now.Unix(),
		}
	}
	mergeRef(&rec.Ref, in.Ref)
	mergeSource(&rec.Source, in.Source)
	if in.DeliveryExpected {
		rec.DeliveryExpected = true
	}
	if strings.TrimSpace(in.OwnerPrincipalID) != "" && rec.OwnerPrincipalID == "" {
		rec.OwnerPrincipalID = strings.TrimSpace(in.OwnerPrincipalID)
	}
	if strings.TrimSpace(in.ExecutionIdentityJSON) != "" && rec.ExecutionIdentityJSON == "" {
		rec.ExecutionIdentityJSON = strings.TrimSpace(in.ExecutionIdentityJSON)
	}
	rec.UpdatedAt = now.Unix()
	s.recomputeStateLocked(&rec, now)
	if err := s.saveLocked(&rec, now); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// RecordExecutionOutput merges canonical execution completion and final output.
func (s *Store) RecordExecutionOutput(in ExecutionInput) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, nil
	}
	now := nowOr(in.Now)
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, found, err := s.findMatchingLocked(in.Ref, in.Source)
	if err != nil {
		return Record{}, err
	}
	if !found {
		rec = Record{
			RecordID:  uuid.NewString(),
			Status:    StatusNotReady,
			CreatedAt: now.Unix(),
		}
	}
	mergeRef(&rec.Ref, in.Ref)
	mergeSource(&rec.Source, in.Source)
	if strings.TrimSpace(in.OwnerPrincipalID) != "" && rec.OwnerPrincipalID == "" {
		rec.OwnerPrincipalID = strings.TrimSpace(in.OwnerPrincipalID)
	}
	if strings.TrimSpace(in.ExecutionIdentityJSON) != "" && rec.ExecutionIdentityJSON == "" {
		rec.ExecutionIdentityJSON = strings.TrimSpace(in.ExecutionIdentityJSON)
	}
	mergeExecution(&rec.Execution, in.Execution)
	rec.ExecutionSettled = isTerminalExecution(rec.Execution.Status)
	if rec.ExecutionSettled {
		rec.Output = normalizeOutput(rec.Execution, in.Output, s.policy.MaxContentSize)
	}
	rec.UpdatedAt = now.Unix()
	s.recomputeStateLocked(&rec, now)
	if err := s.saveLocked(&rec, now); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// RecordDelivery records or updates external channel delivery facts for a turn.
func (s *Store) RecordDelivery(in DeliveryInput) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, nil
	}
	now := nowOr(in.Now)
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, found, err := s.findMatchingLocked(in.Ref, in.Source)
	if err != nil {
		return Record{}, err
	}
	if !found {
		rec = Record{
			RecordID:  uuid.NewString(),
			Status:    StatusNotReady,
			Execution: Execution{Status: ExecutionRunning},
			Output:    Output{Status: OutputUnavailable},
			CreatedAt: now.Unix(),
		}
	}
	mergeRef(&rec.Ref, in.Ref)
	mergeSource(&rec.Source, in.Source)
	rec.DeliveryExpected = true
	upsertDelivery(&rec.Deliveries, in.Delivery)
	rec.ArtifactsPending = in.ArtifactsPending
	rec.DeliverySettled = !in.ArtifactsPending
	rec.UpdatedAt = now.Unix()
	s.recomputeStateLocked(&rec, now)
	if err := s.saveLocked(&rec, now); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// RecordArtifactDelivery appends confirmed asynchronous attachment receipts and
// finalizes the Feishu delivery status for a turn.
func (s *Store) RecordArtifactDelivery(in ArtifactDeliveryInput) (Record, error) {
	if s == nil || s.db == nil || strings.TrimSpace(in.TurnID) == "" {
		return Record{}, nil
	}
	now := nowOr(in.Now)
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, found, err := s.findMatchingLocked(Ref{TurnID: strings.TrimSpace(in.TurnID)}, Source{BridgeID: strings.TrimSpace(in.BridgeID)})
	if err != nil || !found {
		return Record{}, err
	}
	if len(rec.Deliveries) == 0 {
		rec.Deliveries = append(rec.Deliveries, Delivery{
			Channel:  "feishu",
			Status:   DeliveryUnknown,
			BridgeID: strings.TrimSpace(in.BridgeID),
		})
	}
	d := &rec.Deliveries[0]
	seen := make(map[string]struct{}, len(d.Messages))
	for _, m := range d.Messages {
		seen[m.Kind+":"+m.MessageID] = struct{}{}
	}
	for _, att := range in.Attachments {
		if strings.TrimSpace(att.MessageID) == "" {
			continue
		}
		if att.Kind == "" {
			att.Kind = MessageKindAttachment
		}
		key := att.Kind + ":" + att.MessageID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		d.Messages = append(d.Messages, att)
	}
	// Re-evaluate combined status across primary reply + attachments.
	if in.FailedCount > 0 {
		if len(d.Messages) > 0 {
			d.Status = DeliveryPartial
		} else {
			d.Status = DeliveryFailed
		}
	} else if d.Status == DeliveryUnknown && len(d.Messages) > 0 {
		d.Status = DeliveryDelivered
	}
	rec.ArtifactsPending = false
	rec.DeliverySettled = true
	rec.UpdatedAt = now.Unix()
	s.recomputeStateLocked(&rec, now)
	if err := s.saveLocked(&rec, now); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// MarkUnsupported marks a tracked turn as unsupported_mode (settled unavailable).
func (s *Store) MarkUnsupported(ref Ref, source Source, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	now = nowOr(now)
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, found, err := s.findMatchingLocked(ref, source)
	if err != nil {
		return err
	}
	if !found {
		rec = Record{
			RecordID:  uuid.NewString(),
			CreatedAt: now.Unix(),
		}
	}
	mergeRef(&rec.Ref, ref)
	mergeSource(&rec.Source, source)
	rec.Status = StatusUnavailable
	rec.Reason = ReasonUnsupportedMode
	rec.ExecutionSettled = true
	rec.DeliverySettled = true
	rec.ArtifactsPending = false
	rec.UpdatedAt = now.Unix()
	if rec.SettledAt == 0 {
		rec.SettledAt = now.Unix()
	}
	rec.ExpiresAt = s.expiresAt(now)
	return s.saveLocked(&rec, now)
}

// LookupBySessionTurn looks up a tracked turn by session reference and turn_id.
// Passing turnID == "latest" returns the most recently created tracked turn for
// the session. Reading never extends TTL.
func (s *Store) LookupBySessionTurn(targetID, sessionID, sessionName, turnID string, now time.Time) (LookupResult, error) {
	if s == nil || s.db == nil {
		return LookupResult{Status: StatusUnavailable, Reason: ReasonDisabled}, nil
	}
	now = nowOr(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sweepLocked(now); err != nil {
		return LookupResult{}, err
	}
	targetID = strings.TrimSpace(targetID)
	sessionID = strings.TrimSpace(sessionID)
	sessionName = strings.TrimSpace(sessionName)
	turnID = strings.TrimSpace(turnID)

	var (
		rec   Record
		found bool
		err   error
	)
	if strings.EqualFold(turnID, "latest") {
		switch {
		case sessionID != "":
			rec, found, err = s.queryOneLocked(`WHERE session_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, sessionID)
		case targetID != "" && sessionName != "":
			rec, found, err = s.queryOneLocked(`WHERE target_id=? AND session_name=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, targetID, sessionName)
		case sessionName != "":
			rec, found, err = s.queryOneLocked(`WHERE session_name=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, sessionName)
		default:
			return LookupResult{Status: StatusNotFound}, nil
		}
	} else {
		switch {
		case sessionID != "" && turnID != "":
			rec, found, err = s.queryOneLocked(`WHERE session_id=? AND turn_id=? LIMIT 1`, sessionID, turnID)
		case targetID != "" && sessionName != "" && turnID != "":
			rec, found, err = s.queryOneLocked(`WHERE target_id=? AND session_name=? AND turn_id=? LIMIT 1`, targetID, sessionName, turnID)
		case sessionName != "" && turnID != "":
			rec, found, err = s.queryOneLocked(`WHERE session_name=? AND turn_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, sessionName, turnID)
		case turnID != "":
			rec, found, err = s.queryOneLocked(`WHERE turn_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, turnID)
		default:
			return LookupResult{Status: StatusNotFound}, nil
		}
	}
	if err != nil {
		return LookupResult{}, err
	}
	return toLookupResult(rec, found), nil
}

// LookupByFeishuMessage looks up a tracked turn by triggering Feishu message ID.
func (s *Store) LookupByFeishuMessage(bridgeID, messageID string, now time.Time) (LookupResult, error) {
	if s == nil || s.db == nil {
		return LookupResult{Status: StatusUnavailable, Reason: ReasonDisabled}, nil
	}
	now = nowOr(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sweepLocked(now); err != nil {
		return LookupResult{}, err
	}
	bridgeID = strings.TrimSpace(bridgeID)
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return LookupResult{Status: StatusNotFound}, nil
	}
	var recordID string
	var err error
	if bridgeID != "" {
		err = s.db.QueryRow(`SELECT record_id FROM turn_result_feishu_messages WHERE bridge_id=? AND message_id=? LIMIT 1`, bridgeID, messageID).Scan(&recordID)
	} else {
		err = s.db.QueryRow(`SELECT record_id FROM turn_result_feishu_messages WHERE message_id=? ORDER BY rowid DESC LIMIT 1`, messageID).Scan(&recordID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return LookupResult{Status: StatusNotFound}, nil
	}
	if err != nil {
		return LookupResult{}, err
	}
	rec, found, err := s.queryOneLocked(`WHERE record_id=? LIMIT 1`, recordID)
	if err != nil {
		return LookupResult{}, err
	}
	return toLookupResult(rec, found), nil
}

// LookupByScheduleExecution looks up a tracked scheduled execution result.
// Passing executionID <= 0 returns the latest tracked execution for scheduleID.
func (s *Store) LookupByScheduleExecution(scheduleID, executionID int64, now time.Time) (LookupResult, error) {
	if s == nil || s.db == nil {
		return LookupResult{Status: StatusUnavailable, Reason: ReasonDisabled}, nil
	}
	now = nowOr(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sweepLocked(now); err != nil {
		return LookupResult{}, err
	}
	if scheduleID <= 0 {
		return LookupResult{Status: StatusNotFound}, nil
	}
	var (
		rec   Record
		found bool
		err   error
	)
	if executionID > 0 {
		rec, found, err = s.queryOneLocked(`WHERE schedule_id=? AND execution_id=? LIMIT 1`, scheduleID, executionID)
	} else {
		rec, found, err = s.queryOneLocked(`WHERE schedule_id=? ORDER BY execution_id DESC, created_at DESC, rowid DESC LIMIT 1`, scheduleID)
	}
	if err != nil {
		return LookupResult{}, err
	}
	return toLookupResult(rec, found), nil
}

// Sweep expires old entries, settles timed-out pending deliveries, and enforces
// count and byte budgets.
func (s *Store) Sweep(now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepLocked(nowOr(now))
}

func toLookupResult(rec Record, found bool) LookupResult {
	if !found {
		return LookupResult{Status: StatusNotFound}
	}
	copied := rec
	out := LookupResult{
		Status: rec.Status,
		Reason: rec.Reason,
		Record: &copied,
	}
	if rec.Status == StatusReady {
		out.Result = rec.ToResult()
	}
	return out
}

func (s *Store) findMatchingLocked(ref Ref, source Source) (Record, bool, error) {
	if source.ScheduleID > 0 && source.ExecutionID > 0 {
		if rec, ok, err := s.queryOneLocked(`WHERE schedule_id=? AND execution_id=? LIMIT 1`, source.ScheduleID, source.ExecutionID); err != nil || ok {
			return rec, ok, err
		}
	}
	if strings.TrimSpace(ref.SessionID) != "" && strings.TrimSpace(ref.TurnID) != "" {
		if rec, ok, err := s.queryOneLocked(`WHERE session_id=? AND turn_id=? LIMIT 1`, strings.TrimSpace(ref.SessionID), strings.TrimSpace(ref.TurnID)); err != nil || ok {
			return rec, ok, err
		}
	}
	if strings.TrimSpace(ref.TargetID) != "" && strings.TrimSpace(ref.SessionName) != "" && strings.TrimSpace(ref.TurnID) != "" {
		if rec, ok, err := s.queryOneLocked(`WHERE target_id=? AND session_name=? AND turn_id=? LIMIT 1`, strings.TrimSpace(ref.TargetID), strings.TrimSpace(ref.SessionName), strings.TrimSpace(ref.TurnID)); err != nil || ok {
			return rec, ok, err
		}
	}
	if strings.TrimSpace(ref.TurnID) != "" {
		if rec, ok, err := s.queryOneLocked(`WHERE turn_id=? LIMIT 1`, strings.TrimSpace(ref.TurnID)); err != nil || ok {
			return rec, ok, err
		}
	}
	if strings.TrimSpace(source.TaskID) != "" {
		if rec, ok, err := s.queryOneLocked(`WHERE task_id=? LIMIT 1`, strings.TrimSpace(source.TaskID)); err != nil || ok {
			return rec, ok, err
		}
	}
	return Record{}, false, nil
}

const selectRecordColumns = `record_id, status, reason, target_id, session_id, session_name, launch_id, turn_id,
	schedule_id, execution_id, task_id, bridge_id,
	source_json, execution_json, output_json, deliveries_json,
	delivery_expected, execution_settled, delivery_settled, artifacts_pending,
	owner_principal_id, execution_identity_json, payload_bytes,
	created_at, updated_at, settled_at, expires_at`

func (s *Store) queryOneLocked(where string, args ...any) (Record, bool, error) {
	row := s.db.QueryRow(`SELECT `+selectRecordColumns+` FROM turn_results `+where, args...)
	var (
		rec                         Record
		sourceRaw, execRaw          []byte
		outputRaw, deliveriesRaw    []byte
		deliveryExp, execSet        int
		deliverySet, artifactsPend  int
		schedID, execID             int64
		taskID, bridgeID            string
	)
	err := row.Scan(
		&rec.RecordID, &rec.Status, &rec.Reason,
		&rec.Ref.TargetID, &rec.Ref.SessionID, &rec.Ref.SessionName, &rec.Ref.LaunchID, &rec.Ref.TurnID,
		&schedID, &execID, &taskID, &bridgeID,
		&sourceRaw, &execRaw, &outputRaw, &deliveriesRaw,
		&deliveryExp, &execSet, &deliverySet, &artifactsPend,
		&rec.OwnerPrincipalID, &rec.ExecutionIdentityJSON, &rec.PayloadBytes,
		&rec.CreatedAt, &rec.UpdatedAt, &rec.SettledAt, &rec.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	_ = json.Unmarshal(sourceRaw, &rec.Source)
	_ = json.Unmarshal(execRaw, &rec.Execution)
	_ = json.Unmarshal(outputRaw, &rec.Output)
	_ = json.Unmarshal(deliveriesRaw, &rec.Deliveries)
	if rec.Deliveries == nil {
		rec.Deliveries = []Delivery{}
	}
	rec.DeliveryExpected = deliveryExp != 0
	rec.ExecutionSettled = execSet != 0
	rec.DeliverySettled = deliverySet != 0
	rec.ArtifactsPending = artifactsPend != 0
	return rec, true, nil
}

func (s *Store) recomputeStateLocked(rec *Record, now time.Time) {
	if rec.Status == StatusUnavailable && rec.Reason == ReasonUnsupportedMode {
		return
	}
	if rec.ExecutionSettled && (!rec.DeliveryExpected || rec.DeliverySettled) {
		rec.Status = StatusReady
		rec.Reason = ""
		if rec.SettledAt == 0 {
			rec.SettledAt = now.Unix()
		}
		if rec.ExpiresAt == 0 {
			rec.ExpiresAt = s.expiresAt(time.Unix(rec.SettledAt, 0))
		}
		return
	}
	rec.Status = StatusNotReady
	rec.Reason = ""
}

func (s *Store) expiresAt(settledAt time.Time) int64 {
	if s.policy.TTL <= 0 {
		return 0
	}
	return settledAt.Add(s.policy.TTL).Unix()
}

func (s *Store) saveLocked(rec *Record, now time.Time) error {
	if rec.Deliveries == nil {
		rec.Deliveries = []Delivery{}
	}
	sourceRaw, err := json.Marshal(rec.Source)
	if err != nil {
		return err
	}
	execRaw, err := json.Marshal(rec.Execution)
	if err != nil {
		return err
	}
	outputRaw, err := json.Marshal(rec.Output)
	if err != nil {
		return err
	}
	deliveriesRaw, err := json.Marshal(rec.Deliveries)
	if err != nil {
		return err
	}
	rec.PayloadBytes = int64(len(sourceRaw) + len(execRaw) + len(outputRaw) + len(deliveriesRaw))

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO turn_results (
			record_id, status, reason, target_id, session_id, session_name, launch_id, turn_id,
			schedule_id, execution_id, task_id, bridge_id,
			source_json, execution_json, output_json, deliveries_json,
			delivery_expected, execution_settled, delivery_settled, artifacts_pending,
			owner_principal_id, execution_identity_json, payload_bytes,
			created_at, updated_at, settled_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(record_id) DO UPDATE SET
			status=excluded.status, reason=excluded.reason,
			target_id=excluded.target_id, session_id=excluded.session_id,
			session_name=excluded.session_name, launch_id=excluded.launch_id, turn_id=excluded.turn_id,
			schedule_id=excluded.schedule_id, execution_id=excluded.execution_id,
			task_id=excluded.task_id, bridge_id=excluded.bridge_id,
			source_json=excluded.source_json, execution_json=excluded.execution_json,
			output_json=excluded.output_json, deliveries_json=excluded.deliveries_json,
			delivery_expected=excluded.delivery_expected, execution_settled=excluded.execution_settled,
			delivery_settled=excluded.delivery_settled, artifacts_pending=excluded.artifacts_pending,
			owner_principal_id=excluded.owner_principal_id, execution_identity_json=excluded.execution_identity_json,
			payload_bytes=excluded.payload_bytes, updated_at=excluded.updated_at,
			settled_at=excluded.settled_at, expires_at=excluded.expires_at`,
		rec.RecordID, rec.Status, rec.Reason,
		rec.Ref.TargetID, rec.Ref.SessionID, rec.Ref.SessionName, rec.Ref.LaunchID, rec.Ref.TurnID,
		rec.Source.ScheduleID, rec.Source.ExecutionID, rec.Source.TaskID, rec.Source.BridgeID,
		sourceRaw, execRaw, outputRaw, deliveriesRaw,
		boolInt(rec.DeliveryExpected), boolInt(rec.ExecutionSettled), boolInt(rec.DeliverySettled), boolInt(rec.ArtifactsPending),
		rec.OwnerPrincipalID, rec.ExecutionIdentityJSON, rec.PayloadBytes,
		rec.CreatedAt, rec.UpdatedAt, rec.SettledAt, rec.ExpiresAt,
	); err != nil {
		return err
	}
	if rec.Source.BridgeID != "" {
		for _, msgID := range rec.Source.MessageIDs {
			msgID = strings.TrimSpace(msgID)
			if msgID == "" {
				continue
			}
			if _, err := tx.Exec(`
				INSERT INTO turn_result_feishu_messages(bridge_id, message_id, record_id)
				VALUES(?, ?, ?)
				ON CONFLICT(bridge_id, message_id) DO UPDATE SET record_id=excluded.record_id`,
				rec.Source.BridgeID, msgID, rec.RecordID,
			); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.sweepLocked(now, rec.RecordID)
}

func (s *Store) sweepLocked(now time.Time, protectRecordID ...string) error {
	nowUnix := now.Unix()
	protect := ""
	if len(protectRecordID) > 0 {
		protect = protectRecordID[0]
	}

	// 1. Transition execution-completed records whose delivery never settled within pending_timeout.
	pendingCutoff := now.Add(-s.policy.PendingTimeout).Unix()
	settledExpires := s.expiresAt(now)
	if _, err := s.db.Exec(`
		UPDATE turn_results
		SET status=?, reason=?, delivery_settled=1, artifacts_pending=0,
		    settled_at=CASE WHEN settled_at=0 THEN ? ELSE settled_at END,
		    expires_at=CASE WHEN expires_at=0 THEN ? ELSE expires_at END,
		    updated_at=?
		WHERE status=? AND execution_settled=1 AND delivery_expected=1 AND delivery_settled=0
		  AND updated_at<=?`,
		StatusUnavailable, ReasonResultNotSettled, nowUnix, settledExpires, nowUnix, StatusNotReady, pendingCutoff,
	); err != nil {
		return err
	}

	// 2. Delete expired entries.
	if _, err := s.db.Exec(`DELETE FROM turn_results WHERE expires_at > 0 AND expires_at <= ?`, nowUnix); err != nil {
		return err
	}

	// 3. Enforce max_entries and max_total_bytes by evicting oldest entries first
	// (settled entries first, then oldest overall; never evicting protect).
	for {
		var count int
		var totalBytes int64
		if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(payload_bytes), 0) FROM turn_results`).Scan(&count, &totalBytes); err != nil {
			return err
		}
		if count <= s.policy.MaxEntries && totalBytes <= s.policy.MaxTotalBytes {
			return nil
		}
		var victim string
		err := s.db.QueryRow(`
			SELECT record_id FROM turn_results
			WHERE record_id <> ?
			ORDER BY CASE WHEN status = ? THEN 1 ELSE 0 END ASC, created_at ASC, rowid ASC
			LIMIT 1`, protect, StatusNotReady).Scan(&victim)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`DELETE FROM turn_results WHERE record_id=?`, victim); err != nil {
			return err
		}
	}
}

func normalizeOutput(exec Execution, raw Output, maxContentBytes int) Output {
	if exec.Status != ExecutionCompleted {
		reason := strings.TrimSpace(raw.Reason)
		if reason == "" {
			if strings.TrimSpace(exec.Code) != "" {
				reason = strings.TrimSpace(exec.Code)
			} else {
				reason = ReasonExecutionNotCompleted
			}
		}
		return Output{
			Status: OutputUnavailable,
			Reason: reason,
		}
	}
	if raw.Status == OutputUnavailable && strings.TrimSpace(raw.Reason) != "" {
		return Output{
			Status: OutputUnavailable,
			Reason: strings.TrimSpace(raw.Reason),
		}
	}
	if len(raw.Content) > maxContentBytes {
		return Output{
			Status: OutputUnavailable,
			Reason: ReasonResultTooLarge,
		}
	}
	if strings.TrimSpace(raw.Content) == "" {
		return Output{
			Status: OutputEmpty,
			Format: "markdown",
		}
	}
	format := strings.TrimSpace(raw.Format)
	if format == "" {
		format = "markdown"
	}
	return Output{
		Status:  OutputAvailable,
		Format:  format,
		Content: raw.Content,
	}
}

func isTerminalExecution(status string) bool {
	switch status {
	case ExecutionCompleted, ExecutionFailed, ExecutionCancelled, ExecutionInterrupted, ExecutionMissed, ExecutionPartial, ExecutionTimedOut:
		return true
	default:
		return false
	}
}

func mergeRef(dst *Ref, src Ref) {
	if v := strings.TrimSpace(src.TargetID); v != "" {
		dst.TargetID = v
	}
	if v := strings.TrimSpace(src.SessionID); v != "" {
		dst.SessionID = v
	}
	if v := strings.TrimSpace(src.SessionName); v != "" {
		dst.SessionName = v
	}
	if v := strings.TrimSpace(src.LaunchID); v != "" {
		dst.LaunchID = v
	}
	if v := strings.TrimSpace(src.TurnID); v != "" {
		dst.TurnID = v
	}
}

func mergeSource(dst *Source, src Source) {
	// Schedule source takes precedence over generic/feishu source when a
	// scheduled prompt runs inside a Feishu-bound session, while still
	// preserving Feishu metadata (bridge/chat/root/messages).
	if v := strings.TrimSpace(src.Kind); v != "" {
		if dst.Kind == "" || v == SourceSchedule || dst.Kind == SourceCLI {
			dst.Kind = v
		}
	}
	if v := strings.TrimSpace(src.BridgeID); v != "" {
		dst.BridgeID = v
	}
	if v := strings.TrimSpace(src.ChatID); v != "" {
		dst.ChatID = v
	}
	if v := strings.TrimSpace(src.RootMessageID); v != "" {
		dst.RootMessageID = v
	}
	if len(src.MessageIDs) > 0 {
		seen := make(map[string]struct{}, len(dst.MessageIDs))
		for _, id := range dst.MessageIDs {
			seen[id] = struct{}{}
		}
		for _, id := range src.MessageIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			dst.MessageIDs = append(dst.MessageIDs, id)
		}
	}
	if src.ScheduleID > 0 {
		dst.ScheduleID = src.ScheduleID
	}
	if src.ExecutionID > 0 {
		dst.ExecutionID = src.ExecutionID
	}
	if v := strings.TrimSpace(src.ScheduleType); v != "" {
		dst.ScheduleType = v
	}
	if v := strings.TrimSpace(src.RunMode); v != "" {
		dst.RunMode = v
	}
	if src.ScheduledAt > 0 {
		dst.ScheduledAt = src.ScheduledAt
	}
	if v := strings.TrimSpace(src.TaskID); v != "" {
		dst.TaskID = v
	}
}

func mergeExecution(dst *Execution, src Execution) {
	if v := strings.TrimSpace(src.Status); v != "" {
		dst.Status = v
	}
	if v := strings.TrimSpace(src.Code); v != "" {
		dst.Code = v
	}
	if src.StartedAt > 0 {
		dst.StartedAt = src.StartedAt
	}
	if src.FinishedAt > 0 {
		dst.FinishedAt = src.FinishedAt
	}
}

func upsertDelivery(dst *[]Delivery, d Delivery) {
	if strings.TrimSpace(d.Channel) == "" {
		d.Channel = "feishu"
	}
	for i := range *dst {
		if (*dst)[i].Channel == d.Channel && (*dst)[i].BridgeID == d.BridgeID {
			(*dst)[i] = d
			return
		}
	}
	*dst = append(*dst, d)
}

func nowOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
