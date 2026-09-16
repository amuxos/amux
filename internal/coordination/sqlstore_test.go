package coordination

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSQLiteStorePersistsOnlyBoundRoute(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "coordination.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	ctx := context.Background()
	route := &Route{ID: "delegation-1", BackendKind: "amux_task", TargetRef: "team/reviewer@abc", IdempotencyKey: "request-1", CreatedAt: time.Now().UTC()}
	if _, created, err := store.Claim(ctx, route); err != nil || !created {
		t.Fatalf("Claim: created=%v err=%v", created, err)
	}
	if _, err := store.Bind(ctx, route.ID, "amux-task:task-1"); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.Get(ctx, route.ID)
	if err != nil || got.BackendRef != "amux-task:task-1" || got.TargetRef != route.TargetRef {
		t.Fatalf("Get = %#v, %v", got, err)
	}
	var eventTable string
	err = reopened.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='delegation_events'`).Scan(&eventTable)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected event table %q", eventTable)
	}
}

func TestSQLiteStoreMigratesLegacyBackendRouteAndDropsLifecycleTables(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "coordination.db")
	db, err := sql.Open("sqlite", coordinationSQLiteDSN(path))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE delegations (id TEXT PRIMARY KEY, record_json TEXT NOT NULL);
		CREATE TABLE delegation_events (delegation_id TEXT, seq INTEGER);
	`)
	if err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	legacy := Delegation{ID: "legacy-1", TargetRef: "team/reviewer@abc", InitiatorRef: "session:main", ScopeRef: "turn:1", Input: "review", BackendRef: "amux-task:task-1", ResultTrustLevel: TrustInternal, CreatedAt: time.Now().UTC()}
	encoded, _ := json.Marshal(legacy)
	if _, err := db.Exec(`INSERT INTO delegations (id, record_json) VALUES (?, ?)`, legacy.ID, string(encoded)); err != nil {
		t.Fatalf("insert legacy: %v", err)
	}
	_ = db.Close()

	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	route, err := store.Get(context.Background(), legacy.ID)
	if err != nil || route.BackendRef != legacy.BackendRef || route.TargetRef != legacy.TargetRef {
		t.Fatalf("migrated route = %#v, %v", route, err)
	}
	for _, table := range []string{"delegations", "delegation_events"} {
		var name string
		err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("legacy table %s still exists: %v", table, err)
		}
	}
}

func TestSQLiteStoreClaimIsIdempotentAcrossConnections(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "coordination.db")
	stores := openSQLiteStores(t, path, 2)
	start := make(chan struct{})
	type result struct {
		route   *Route
		created bool
		err     error
	}
	results := make(chan result, len(stores))
	var workers sync.WaitGroup
	for i, store := range stores {
		workers.Add(1)
		go func(i int, store *SQLiteStore) {
			defer workers.Done()
			<-start
			route, created, err := store.Claim(context.Background(), &Route{
				ID:             fmt.Sprintf("delegation-%d", i),
				IdempotencyKey: "same-request",
				CreatedAt:      time.Now().UTC(),
			})
			results <- result{route: route, created: created, err: err}
		}(i, store)
	}
	close(start)
	workers.Wait()
	close(results)

	createdCount := 0
	winnerID := ""
	for result := range results {
		if result.err != nil {
			t.Fatalf("Claim: %v", result.err)
		}
		if result.created {
			createdCount++
			winnerID = result.route.ID
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
	for _, store := range stores {
		route, created, err := store.Claim(context.Background(), &Route{ID: "replay", IdempotencyKey: "same-request"})
		if err != nil || created || route.ID != winnerID {
			t.Fatalf("replayed Claim = %#v, created=%v, err=%v; winner=%q", route, created, err, winnerID)
		}
	}
}

func TestSQLiteStoreBindUsesFirstWriterAcrossConnections(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "coordination.db")
	stores := openSQLiteStores(t, path, 2)
	if _, created, err := stores[0].Claim(context.Background(), &Route{ID: "delegation-1"}); err != nil || !created {
		t.Fatalf("Claim: created=%v err=%v", created, err)
	}

	start := make(chan struct{})
	type result struct {
		backendRef string
		route      *Route
		err        error
	}
	results := make(chan result, len(stores))
	var workers sync.WaitGroup
	for i, store := range stores {
		workers.Add(1)
		go func(i int, store *SQLiteStore) {
			defer workers.Done()
			backendRef := fmt.Sprintf("amux-task:task-%d", i)
			<-start
			route, err := store.Bind(context.Background(), "delegation-1", backendRef)
			results <- result{backendRef: backendRef, route: route, err: err}
		}(i, store)
	}
	close(start)
	workers.Wait()
	close(results)

	successes := 0
	winnerRef := ""
	for result := range results {
		if result.err == nil {
			successes++
			winnerRef = result.backendRef
			if result.route.BackendRef != result.backendRef {
				t.Fatalf("bound route = %#v, want %q", result.route, result.backendRef)
			}
			continue
		}
		if !errors.Is(result.err, ErrRouteAlreadyBound) {
			t.Fatalf("losing Bind error = %v, want ErrRouteAlreadyBound", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful binds = %d, want 1", successes)
	}
	stored, err := stores[0].Get(context.Background(), "delegation-1")
	if err != nil || stored.BackendRef != winnerRef {
		t.Fatalf("stored route = %#v, err=%v; winner=%q", stored, err, winnerRef)
	}
}

func openSQLiteStores(t *testing.T, path string, count int) []*SQLiteStore {
	t.Helper()
	stores := make([]*SQLiteStore, 0, count)
	for range count {
		store, err := OpenSQLiteStore(path)
		if err != nil {
			t.Fatalf("OpenSQLiteStore: %v", err)
		}
		stores = append(stores, store)
		t.Cleanup(func() { _ = store.Close() })
	}
	return stores
}
