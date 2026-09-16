package system

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"

	_ "modernc.org/sqlite"
)

const journalBusyTimeoutMS = 5000

const journalTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

const orphanedOperationError = "system operation failed because its execution owner exited before recording a terminal state"

const operationColumns = `
	id, action, scope_json, plan_json, state, error,
	created_at, updated_at, finished_at`

// Journal persists local system operations in SQLite.
type Journal struct {
	db       *sql.DB
	lockPath string
}

// DefaultJournalPath returns the operation journal under AMUX_HOME.
func DefaultJournalPath() string {
	return amuxhome.Path("system", "operations.db")
}

// OpenJournal opens or creates an operation journal at path.
func OpenJournal(path string) (*Journal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("system journal path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("system journal mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", journalSQLiteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("system journal open: %w", err)
	}
	db.SetMaxOpenConns(1)
	journal := &Journal{db: db, lockPath: path + ".active.lock"}
	if err := journal.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := journal.recoverOrphanedOperations(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("system journal permissions: %w", err)
	}
	return journal, nil
}

func (j *Journal) recoverOrphanedOperations(ctx context.Context) error {
	lock, acquired, err := j.tryExecutionLock(ctx)
	if err != nil {
		return fmt.Errorf("recover orphaned system operations: %w", err)
	}
	if !acquired {
		return nil
	}
	defer releaseExecutionLock(lock)
	return j.failOrphanedOperations(ctx)
}

func (j *Journal) failOrphanedOperations(ctx context.Context) error {
	now := formatJournalTime(time.Now().UTC())
	if _, err := j.db.ExecContext(ctx, `
		UPDATE operations
		SET state = ?, error = ?, updated_at = ?, finished_at = ?
		WHERE state IN ('pending', 'draining', 'staging', 'applying', 'verifying')`,
		OperationFailed, orphanedOperationError, now, now,
	); err != nil {
		return fmt.Errorf("mark orphaned system operations failed: %w", err)
	}
	return nil
}

func (j *Journal) acquireExecutionLease(ctx context.Context) (func(), error) {
	lock, acquired, err := j.tryExecutionLock(ctx)
	if err != nil {
		return nil, err
	}
	if acquired {
		if err := j.failOrphanedOperations(ctx); err != nil {
			releaseExecutionLock(lock)
			return nil, fmt.Errorf("recover orphaned system operations after lease acquisition: %w", err)
		}
		return func() { releaseExecutionLock(lock) }, nil
	}
	activeID, queryErr := queryActiveOperationID(ctx, j.db)
	if queryErr == nil {
		return nil, &ConflictError{ExistingOperationID: activeID}
	}
	if errors.Is(queryErr, sql.ErrNoRows) {
		return nil, &ConflictError{}
	}
	return nil, fmt.Errorf("identify execution lease owner: %w", queryErr)
}

func (j *Journal) tryExecutionLock(ctx context.Context) (*os.File, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	lock, err := os.OpenFile(j.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open execution lock: %w", err)
	}
	if err := os.Chmod(j.lockPath, 0o600); err != nil {
		_ = lock.Close()
		return nil, false, fmt.Errorf("set execution lock permissions: %w", err)
	}
	acquired, err := tryFileLock(lock)
	if err != nil {
		_ = lock.Close()
		return nil, false, fmt.Errorf("lock execution owner: %w", err)
	}
	if !acquired {
		_ = lock.Close()
		return nil, false, nil
	}
	return lock, true, nil
}

func releaseExecutionLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = unlockFile(lock)
	_ = lock.Close()
}

func journalSQLiteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%s_pragma=busy_timeout%%3d%d&_pragma=journal_mode(WAL)&_txlock=immediate", path, separator, journalBusyTimeoutMS)
}

func (j *Journal) migrate() error {
	if _, err := j.db.Exec(`
		CREATE TABLE IF NOT EXISTS operations (
			id          TEXT PRIMARY KEY,
			action      TEXT NOT NULL,
			scope_json  BLOB NOT NULL,
			plan_json   BLOB NOT NULL,
			state       TEXT NOT NULL,
			error       TEXT NOT NULL DEFAULT '',
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL,
			finished_at TEXT
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_system_operations_active
		ON operations ((1))
		WHERE state IN ('pending', 'draining', 'staging', 'applying', 'verifying');
	`); err != nil {
		return fmt.Errorf("system journal migrate: %w", err)
	}
	return nil
}

// Create inserts operation. Operation IDs are idempotency keys, while the
// active-operation index limits each installation to one mutation at a time.
func (j *Journal) Create(ctx context.Context, operation Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scopeJSON, err := json.Marshal(operation.Scope)
	if err != nil {
		return fmt.Errorf("encode system operation %s scope: %w", operation.ID, err)
	}
	planJSON, err := json.Marshal(operation.Plan)
	if err != nil {
		return fmt.Errorf("encode system operation %s plan: %w", operation.ID, err)
	}

	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create system operation %s: %w", operation.ID, err)
	}
	defer tx.Rollback()
	if exists, err := operationExists(ctx, tx, operation.ID); err != nil {
		return fmt.Errorf("check duplicate system operation %s: %w", operation.ID, err)
	} else if exists {
		return &DuplicateOperationError{OperationID: operation.ID}
	}
	if activeID, err := queryActiveOperationID(ctx, tx); err == nil {
		return &ConflictError{ExistingOperationID: activeID}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check active system operation: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO operations (
			id, action, scope_json, plan_json, state, error,
			created_at, updated_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		operation.ID,
		operation.Action,
		scopeJSON,
		planJSON,
		operation.State,
		operation.Error,
		formatJournalTime(operation.CreatedAt),
		formatJournalTime(operation.UpdatedAt),
		formatOptionalJournalTime(operation.FinishedAt),
	)
	if err != nil {
		if classified := classifyCreateConstraint(ctx, tx, operation.ID); classified != nil {
			return classified
		}
		return fmt.Errorf("insert system operation %s: %w", operation.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit system operation %s: %w", operation.ID, err)
	}
	return nil
}

func operationExists(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id OperationID) (bool, error) {
	var exists int
	err := queryer.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func queryActiveOperationID(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (OperationID, error) {
	var id OperationID
	err := queryer.QueryRowContext(ctx, `
		SELECT id FROM operations
		WHERE state IN ('pending', 'draining', 'staging', 'applying', 'verifying')
		ORDER BY created_at DESC, id ASC
		LIMIT 1`).Scan(&id)
	return id, err
}

func classifyCreateConstraint(ctx context.Context, tx *sql.Tx, id OperationID) error {
	if exists, err := operationExists(ctx, tx, id); err == nil && exists {
		return &DuplicateOperationError{OperationID: id}
	}
	if activeID, err := queryActiveOperationID(ctx, tx); err == nil {
		return &ConflictError{ExistingOperationID: activeID}
	}
	return nil
}

// Get returns one operation by ID.
func (j *Journal) Get(ctx context.Context, id OperationID) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	operation, err := scanOperation(j.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operations WHERE id = ?`, id))
	if err != nil {
		return Operation{}, operationQueryError("get", id, err)
	}
	return operation, nil
}

// List returns operations newest first. Non-positive limits mean no limit.
func (j *Journal) List(ctx context.Context, limit int) ([]Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query := `SELECT ` + operationColumns + ` FROM operations ORDER BY created_at DESC, id ASC`
	args := []any(nil)
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := j.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list system operations: %w", err)
	}
	defer rows.Close()
	operations := make([]Operation, 0)
	for rows.Next() {
		operation, scanErr := scanOperation(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list system operations: %w", scanErr)
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list system operations: %w", err)
	}
	return operations, nil
}

// Transition atomically validates and applies an operation state transition.
func (j *Journal) Transition(ctx context.Context, id OperationID, state OperationState, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transition system operation %s: %w", id, err)
	}
	defer tx.Rollback()
	var from OperationState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM operations WHERE id = ?`, id).Scan(&from); err != nil {
		return operationQueryError("transition", id, err)
	}
	if !validOperationTransition(from, state) {
		return &InvalidTransitionError{OperationID: id, From: from, To: state}
	}
	now := time.Now().UTC()
	var finishedAt any
	if isTerminalOperationState(state) {
		finishedAt = formatJournalTime(now)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE operations
		SET state = ?, error = ?, updated_at = ?, finished_at = ?
		WHERE id = ?`, state, message, formatJournalTime(now), finishedAt, id); err != nil {
		return fmt.Errorf("update system operation %s transition %s to %s: %w", id, from, state, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit system operation %s transition: %w", id, err)
	}
	return nil
}

// CancelPending atomically changes a pending operation to cancelled. Once an
// operation has advanced, cancellation is rejected without changing state.
func (j *Journal) CancelPending(ctx context.Context, id OperationID) (CancelResult, error) {
	if err := ctx.Err(); err != nil {
		return CancelResult{}, err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return CancelResult{}, fmt.Errorf("begin cancel system operation %s: %w", id, err)
	}
	defer tx.Rollback()
	var state OperationState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM operations WHERE id = ?`, id).Scan(&state); err != nil {
		return CancelResult{}, operationQueryError("cancel", id, err)
	}
	result := CancelResult{OperationID: id, State: state}
	if state != OperationPending {
		result.Reason = "operation is no longer pending"
		if err := tx.Commit(); err != nil {
			return CancelResult{}, fmt.Errorf("commit rejected cancellation for system operation %s: %w", id, err)
		}
		return result, nil
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE operations
		SET state = ?, updated_at = ?, finished_at = ?
		WHERE id = ?`, OperationCancelled, formatJournalTime(now), formatJournalTime(now), id); err != nil {
		return CancelResult{}, fmt.Errorf("cancel system operation %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return CancelResult{}, fmt.Errorf("commit cancellation for system operation %s: %w", id, err)
	}
	result.Accepted = true
	result.State = OperationCancelled
	return result, nil
}

// Close closes the journal database.
func (j *Journal) Close() error {
	if err := j.db.Close(); err != nil {
		return fmt.Errorf("close system journal: %w", err)
	}
	return nil
}

type operationScanner interface {
	Scan(...any) error
}

func scanOperation(scanner operationScanner) (Operation, error) {
	var (
		operation            Operation
		scopeJSON, planJSON  []byte
		createdAt, updatedAt string
		finishedAt           sql.NullString
	)
	if err := scanner.Scan(
		&operation.ID,
		&operation.Action,
		&scopeJSON,
		&planJSON,
		&operation.State,
		&operation.Error,
		&createdAt,
		&updatedAt,
		&finishedAt,
	); err != nil {
		return Operation{}, err
	}
	if err := json.Unmarshal(scopeJSON, &operation.Scope); err != nil {
		return Operation{}, fmt.Errorf("decode system operation %s scope: %w", operation.ID, err)
	}
	if err := json.Unmarshal(planJSON, &operation.Plan); err != nil {
		return Operation{}, fmt.Errorf("decode system operation %s plan: %w", operation.ID, err)
	}
	var err error
	if operation.CreatedAt, err = parseJournalTime(createdAt); err != nil {
		return Operation{}, fmt.Errorf("decode system operation %s created_at: %w", operation.ID, err)
	}
	if operation.UpdatedAt, err = parseJournalTime(updatedAt); err != nil {
		return Operation{}, fmt.Errorf("decode system operation %s updated_at: %w", operation.ID, err)
	}
	if finishedAt.Valid {
		finished, err := parseJournalTime(finishedAt.String)
		if err != nil {
			return Operation{}, fmt.Errorf("decode system operation %s finished_at: %w", operation.ID, err)
		}
		operation.FinishedAt = &finished
	}
	return operation, nil
}

func operationQueryError(action string, id OperationID, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s system operation %s: operation not found: %w", action, id, err)
	}
	return fmt.Errorf("%s system operation %s: %w", action, id, err)
}

func isTerminalOperationState(state OperationState) bool {
	switch state {
	case OperationSucceeded, OperationRolledBack, OperationFailed, OperationCancelled:
		return true
	default:
		return false
	}
}

func formatJournalTime(value time.Time) string {
	return value.UTC().Format(journalTimeFormat)
}

func formatOptionalJournalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatJournalTime(*value)
}

func parseJournalTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}
