package coordination

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const sqliteBusyTimeoutMS = 5000

// SQLiteStore persists only delegation routes. Backend lifecycle data is never
// copied into this database.
type SQLiteStore struct{ db *sql.DB }

func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("coordination sqlite path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("coordination db mkdir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", coordinationSQLiteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("coordination db open: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func coordinationSQLiteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%s_pragma=busy_timeout%%3d%d&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate", path, separator, sqliteBusyTimeoutMS)
}

func (s *SQLiteStore) migrate() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS delegation_routes (
			id TEXT PRIMARY KEY,
			idempotency_key TEXT,
			backend_ref TEXT NOT NULL DEFAULT '',
			route_json TEXT NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_delegation_routes_idempotency
			ON delegation_routes(idempotency_key) WHERE idempotency_key IS NOT NULL;
	`); err != nil {
		return fmt.Errorf("coordination route db migrate: %w", err)
	}
	return s.migrateLegacyLifecycleStore()
}

// migrateLegacyLifecycleStore preserves bound backend identities from v0.4,
// then removes the superseded status/event truth. Unbound legacy rows have no
// backend to query and therefore cannot become routes.
func (s *SQLiteStore) migrateLegacyLifecycleStore() error {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='delegations'`).Scan(&exists); err != nil || exists == 0 {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT record_json FROM delegations`)
	if err != nil {
		return fmt.Errorf("read legacy coordination routes: %w", err)
	}
	var routes []*Route
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			_ = rows.Close()
			return err
		}
		var legacy Delegation
		if err := json.Unmarshal([]byte(encoded), &legacy); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode legacy delegation: %w", err)
		}
		if strings.TrimSpace(legacy.BackendRef) == "" {
			continue
		}
		routes = append(routes, &Route{
			ID: legacy.ID, BackendKind: "amux_task", BackendRef: legacy.BackendRef,
			CollaboratorID: "agent-profile:" + legacy.TargetRef, TargetRef: legacy.TargetRef,
			InitiatorRef: legacy.InitiatorRef, ScopeRef: legacy.ScopeRef,
			ParentDelegationID: legacy.ParentDelegationID, Input: legacy.Input,
			Deadline: legacy.Deadline, TrustLevel: legacy.ResultTrustLevel,
			IdempotencyKey: legacy.IdempotencyKey, HopCount: legacy.HopCount,
			HopBudget: legacy.HopBudget, CreatedAt: legacy.CreatedAt,
		})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, route := range routes {
		encoded, err := json.Marshal(route)
		if err != nil {
			return err
		}
		var key any
		if route.IdempotencyKey != "" {
			key = route.IdempotencyKey
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO delegation_routes (id, idempotency_key, backend_ref, route_json) VALUES (?, ?, ?, ?)`, route.ID, key, route.BackendRef, string(encoded)); err != nil {
			return fmt.Errorf("migrate legacy delegation route: %w", err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS delegation_events; DROP TABLE delegations;`); err != nil {
		return fmt.Errorf("remove legacy coordination lifecycle store: %w", err)
	}
	return tx.Commit()
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) Claim(ctx context.Context, route *Route) (*Route, bool, error) {
	if err := validateRoute(route); err != nil {
		return nil, false, err
	}
	encoded, err := json.Marshal(route)
	if err != nil {
		return nil, false, err
	}
	var key any
	if route.IdempotencyKey != "" {
		key = route.IdempotencyKey
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO delegation_routes (id, idempotency_key, backend_ref, route_json) VALUES (?, ?, '', ?) ON CONFLICT DO NOTHING`, route.ID, key, string(encoded))
	if err != nil {
		return nil, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if inserted == 1 {
		return cloneRoute(route), true, nil
	}
	if route.IdempotencyKey != "" {
		existing, getErr := getRoute(ctx, s.db, `SELECT route_json FROM delegation_routes WHERE idempotency_key = ?`, route.IdempotencyKey)
		if getErr == nil {
			return existing, false, nil
		}
		if !errors.Is(getErr, sql.ErrNoRows) {
			return nil, false, getErr
		}
	}
	if _, getErr := getRoute(ctx, s.db, `SELECT route_json FROM delegation_routes WHERE id = ?`, route.ID); getErr == nil {
		return nil, false, fmt.Errorf("delegation route %q already exists", route.ID)
	} else if !errors.Is(getErr, sql.ErrNoRows) {
		return nil, false, getErr
	}
	return nil, false, errors.New("delegation route claim conflicted but no existing route was found")
}

func (s *SQLiteStore) Bind(ctx context.Context, id, backendRef string) (*Route, error) {
	backendRef = strings.TrimSpace(backendRef)
	if backendRef == "" {
		return nil, errors.New("backend ref is required")
	}
	route, err := getRoute(ctx, s.db, `SELECT route_json FROM delegation_routes WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("delegation route %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if route.BackendRef != "" {
		if route.BackendRef == backendRef {
			return route, nil
		}
		return nil, fmt.Errorf("delegation route %q: %w", id, ErrRouteAlreadyBound)
	}
	route.BackendRef = backendRef
	encoded, err := json.Marshal(route)
	if err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE delegation_routes SET backend_ref = ?, route_json = ? WHERE id = ? AND backend_ref = ''`, backendRef, string(encoded), id)
	if err != nil {
		return nil, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if updated == 1 {
		return route, nil
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.BackendRef == backendRef {
		return current, nil
	}
	return nil, fmt.Errorf("delegation route %q: %w", id, ErrRouteAlreadyBound)
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*Route, error) {
	route, err := getRoute(ctx, s.db, `SELECT route_json FROM delegation_routes WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("delegation route %q: %w", id, ErrNotFound)
	}
	return route, err
}

func (s *SQLiteStore) DeleteUnbound(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM delegation_routes WHERE id = ? AND backend_ref = ''`, id)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil || deleted > 0 {
		return err
	}
	var backendRef string
	err = s.db.QueryRowContext(ctx, `SELECT backend_ref FROM delegation_routes WHERE id = ?`, id).Scan(&backendRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("delegation route %q: %w", id, ErrRouteAlreadyBound)
}

type routeQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRoute(ctx context.Context, querier routeQuerier, query string, args ...any) (*Route, error) {
	var encoded string
	if err := querier.QueryRowContext(ctx, query, args...).Scan(&encoded); err != nil {
		return nil, err
	}
	var route Route
	if err := json.Unmarshal([]byte(encoded), &route); err != nil {
		return nil, fmt.Errorf("decode delegation route: %w", err)
	}
	return &route, nil
}
