package system

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJournalSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Create(context.Background(), Operation{ID: "op-1", State: OperationPending}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	operation, err := reopened.Get(context.Background(), "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if operation.ID != "op-1" {
		t.Fatalf("id = %q", operation.ID)
	}
}

func TestJournalReopenFailsOrphanedApplyingOperationAndReleasesGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalCrashHelper$")
	cmd.Env = append(os.Environ(), "AMUX_SYSTEM_CRASH_HELPER="+path)
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("crash helper = %v, output:\n%s", err, output)
	}

	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	orphan, err := reopened.Get(context.Background(), "op-crash")
	if err != nil {
		t.Fatal(err)
	}
	if orphan.State != OperationFailed || orphan.FinishedAt == nil || !strings.Contains(orphan.Error, "execution owner exited") {
		t.Fatalf("orphan after reopen = %#v, want failed with owner-exited reason", orphan)
	}

	lifecycle := New(Dependencies{
		Journal: reopened,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateDegraded}, nil
		},
		Execute: func(context.Context, ChangePlan) error { return nil },
		IDGen:   func() string { return "op-after-crash" },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionStart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := lifecycle.Apply(context.Background(), plan); err != nil || id != "op-after-crash" {
		t.Fatalf("Apply after orphan recovery = %q, %v", id, err)
	}
}

func TestJournalOpenObserverTakesOverOrphanWithoutReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operations.db")
	readyPath := filepath.Join(dir, "owner-ready")
	crashPath := filepath.Join(dir, "owner-crash")
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalCrashHelper$")
	cmd.Env = append(os.Environ(),
		"AMUX_SYSTEM_CRASH_HELPER="+path,
		"AMUX_SYSTEM_CRASH_READY="+readyPath,
		"AMUX_SYSTEM_CRASH_RELEASE="+crashPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if err := waitForJournalCrashFile(readyPath, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	observer, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	live, err := observer.Get(context.Background(), "op-crash")
	if err != nil {
		t.Fatal(err)
	}
	if live.State != OperationApplying || live.Error != "" || live.FinishedAt != nil {
		t.Fatalf("live operation after observer open = %#v", live)
	}

	if err := os.WriteFile(crashPath, []byte("crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("crash helper = %v, want exit 23", err)
	}

	lifecycle := New(Dependencies{
		Journal: observer,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateDegraded}, nil
		},
		Execute: func(context.Context, ChangePlan) error { return nil },
		IDGen:   func() string { return "op-takeover" },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionStart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := lifecycle.Apply(context.Background(), plan); err != nil || id != "op-takeover" {
		t.Fatalf("Apply through existing observer after owner crash = %q, %v", id, err)
	}
	orphan, err := observer.Get(context.Background(), "op-crash")
	if err != nil {
		t.Fatal(err)
	}
	if orphan.State != OperationFailed || orphan.FinishedAt == nil || !strings.Contains(orphan.Error, "execution owner exited") {
		t.Fatalf("orphan after observer takeover = %#v", orphan)
	}
	takeover, err := observer.Get(context.Background(), "op-takeover")
	if err != nil {
		t.Fatal(err)
	}
	if takeover.State != OperationSucceeded || takeover.FinishedAt == nil {
		t.Fatalf("takeover operation = %#v, want succeeded", takeover)
	}
}

func TestJournalCrashHelper(t *testing.T) {
	path := os.Getenv("AMUX_SYSTEM_CRASH_HELPER")
	if path == "" {
		return
	}
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := New(Dependencies{
		Journal: journal,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			if readyPath := os.Getenv("AMUX_SYSTEM_CRASH_READY"); readyPath != "" {
				if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
					return err
				}
				if err := waitForJournalCrashFile(os.Getenv("AMUX_SYSTEM_CRASH_RELEASE"), 5*time.Second); err != nil {
					return err
				}
			}
			os.Exit(23)
			return nil
		},
		IDGen: func() string { return "op-crash" },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	t.Fatal("Apply returned instead of simulating a process crash")
}

func waitForJournalCrashFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestJournalReopenDoesNotFailOperationWithLiveExecutor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	owner, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	executing := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	lifecycle := New(Dependencies{
		Journal: owner,
		Inspect: func(context.Context, Scope) (Snapshot, error) {
			return Snapshot{State: StateHealthy}, nil
		},
		Execute: func(context.Context, ChangePlan) error {
			close(executing)
			<-release
			return nil
		},
		IDGen: func() string { return "op-live" },
	})
	plan, err := lifecycle.Plan(context.Background(), ChangeRequest{Action: ActionRestart, Scope: CurrentScope()})
	if err != nil {
		t.Fatal(err)
	}
	applyResult := make(chan error, 1)
	go func() {
		_, applyErr := lifecycle.Apply(context.Background(), plan)
		applyResult <- applyErr
	}()
	select {
	case <-executing:
	case <-time.After(2 * time.Second):
		t.Fatal("live executor did not start")
	}

	observer, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	live, err := observer.Get(context.Background(), "op-live")
	if err != nil {
		_ = observer.Close()
		t.Fatal(err)
	}
	if live.State != OperationApplying || live.Error != "" || live.FinishedAt != nil {
		_ = observer.Close()
		t.Fatalf("live operation after concurrent reopen = %#v", live)
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}

	close(release)
	if err := <-applyResult; err != nil {
		t.Fatalf("live Apply: %v", err)
	}
}

func TestDefaultJournalPathUsesAMUXHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "custom-amux-home")
	t.Setenv("AMUX_HOME", home)
	if got, want := DefaultJournalPath(), filepath.Join(home, "system", "operations.db"); got != want {
		t.Fatalf("DefaultJournalPath() = %q, want %q", got, want)
	}
}

func TestOpenJournalCreatesSecureDatabaseWithExpectedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "system", "operations.db")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database permissions = %o, want 600", got)
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("parent permissions = %o, want 700", got)
	}

	var journalMode string
	if err := journal.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var busyTimeout int
	if err := journal.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout < 5000 {
		t.Fatalf("busy_timeout = %d, want at least 5000", busyTimeout)
	}

	rows, err := journal.db.Query(`PRAGMA table_info(operations)`)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	wantColumns := []string{"id", "action", "scope_json", "plan_json", "state", "error", "created_at", "updated_at", "finished_at"}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("columns = %v, want %v", columns, wantColumns)
	}

	var indexSQL string
	if err := journal.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_system_operations_active'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(indexSQL), " "))
	for _, fragment := range []string{"create unique index", "where state in", "'pending'", "'draining'", "'staging'", "'applying'", "'verifying'"} {
		if !strings.Contains(normalized, fragment) {
			t.Fatalf("active index SQL = %q, missing %q", indexSQL, fragment)
		}
	}
}

func TestJournalRoundTripsExportedOperationFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	createdAt := time.Date(2026, time.July, 14, 9, 8, 7, 654321000, time.UTC)
	updatedAt := createdAt.Add(2 * time.Minute)
	finishedAt := updatedAt.Add(time.Second)
	operation := Operation{
		ID:         "op-roundtrip",
		Action:     ActionSetup,
		Scope:      Scope{Kind: ScopeCurrent, Target: "local"},
		State:      OperationFailed,
		Error:      "setup failed",
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
		FinishedAt: &finishedAt,
		Plan: ChangePlan{
			OperationID: "op-roundtrip",
			Request: ChangeRequest{
				Action: ActionSetup,
				Scope:  Scope{Kind: ScopeCurrent, Target: "local"},
				Force:  true,
				Setup: &SetupSpec{
					Profile:    "remote",
					Agent:      "builtin/codex",
					HubAddress: "hub.example.com:7100",
					NoStart:    true,
				},
			},
			Before: Snapshot{
				Schema:     SnapshotSchema,
				Scope:      Scope{Kind: ScopeCurrent, Target: "local"},
				State:      StateDegraded,
				Version:    "v1.2.3",
				Components: []Component{{Name: "hub", State: "stopped", Detail: "not running"}},
				Checks:     []Check{{Name: "hub", State: "failed", Required: true, Message: "unreachable"}},
			},
			CreatedAt: createdAt.Add(-time.Minute),
			integrity: [32]byte{1, 2, 3},
		},
	}

	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Create(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.ID != operation.ID || got.Action != operation.Action || got.Scope != operation.Scope || got.State != operation.State || got.Error != operation.Error {
		t.Fatalf("operation fields = %#v, want %#v", got, operation)
	}
	if !got.CreatedAt.Equal(operation.CreatedAt) || !got.UpdatedAt.Equal(operation.UpdatedAt) {
		t.Fatalf("operation times = %s, %s; want %s, %s", got.CreatedAt, got.UpdatedAt, operation.CreatedAt, operation.UpdatedAt)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(*operation.FinishedAt) {
		t.Fatalf("finished_at = %v, want %v", got.FinishedAt, operation.FinishedAt)
	}
	wantPlanJSON, err := json.Marshal(operation.Plan)
	if err != nil {
		t.Fatal(err)
	}
	gotPlanJSON, err := json.Marshal(got.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotPlanJSON) != string(wantPlanJSON) {
		t.Fatalf("plan JSON = %s, want %s", gotPlanJSON, wantPlanJSON)
	}
	wantSetup := SetupSpec{
		Profile:    "remote",
		Agent:      "builtin/codex",
		HubAddress: "hub.example.com:7100",
		NoStart:    true,
	}
	if got.Plan.Request.Setup == nil || *got.Plan.Request.Setup != wantSetup {
		t.Fatalf("setup spec = %#v, want %#v", got.Plan.Request.Setup, wantSetup)
	}
	if got.Plan.integrity != ([32]byte{}) {
		t.Fatalf("restored plan unexpectedly has an integrity token: %x", got.Plan.integrity)
	}
}

func TestJournalCreateReportsDuplicateBeforeActiveConflict(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	if err := journal.Create(ctx, Operation{ID: "op-active", State: OperationPending}); err != nil {
		t.Fatal(err)
	}

	err := journal.Create(ctx, Operation{ID: "op-active", State: OperationPending})
	var duplicate *DuplicateOperationError
	if !errors.As(err, &duplicate) || !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("duplicate error = %T %v, want DuplicateOperationError", err, err)
	}
	if duplicate.OperationID != "op-active" {
		t.Fatalf("duplicate operation ID = %q", duplicate.OperationID)
	}

	err = journal.Create(ctx, Operation{ID: "op-new", State: OperationPending})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflict error = %T %v, want ConflictError", err, err)
	}
	if conflict.ExistingOperationID != "op-active" {
		t.Fatalf("existing operation ID = %q", conflict.ExistingOperationID)
	}
}

func TestJournalConcurrentCreateReturnsWinningConflictID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	first, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	type createResult struct {
		id  OperationID
		err error
	}
	start := make(chan struct{})
	results := make(chan createResult, 2)
	for index, item := range []struct {
		journal *Journal
		id      OperationID
	}{{first, "op-a"}, {second, "op-b"}} {
		_ = index
		go func(journal *Journal, id OperationID) {
			<-start
			results <- createResult{id: id, err: journal.Create(context.Background(), Operation{ID: id, State: OperationPending})}
		}(item.journal, item.id)
	}
	close(start)
	left, right := <-results, <-results

	var winner OperationID
	var loser createResult
	if left.err == nil && right.err != nil {
		winner, loser = left.id, right
	} else if right.err == nil && left.err != nil {
		winner, loser = right.id, left
	} else {
		t.Fatalf("concurrent Create errors = %v, %v; want one success", left.err, right.err)
	}
	var conflict *ConflictError
	if !errors.As(loser.err, &conflict) {
		t.Fatalf("loser error = %T %v, want ConflictError", loser.err, loser.err)
	}
	if conflict.ExistingOperationID != winner {
		t.Fatalf("conflict operation ID = %q, want winner %q", conflict.ExistingOperationID, winner)
	}
}

func TestJournalTransitionValidatesStateAndPersistsTerminalFailure(t *testing.T) {
	journal := openTestJournal(t)
	createdAt := time.Date(2020, time.July, 14, 10, 0, 0, 0, time.UTC)
	if err := journal.Create(context.Background(), Operation{
		ID:        "op-transition",
		State:     OperationPending,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Transition(context.Background(), "op-transition", OperationApplying, ""); err != nil {
		t.Fatal(err)
	}
	applying, err := journal.Get(context.Background(), "op-transition")
	if err != nil {
		t.Fatal(err)
	}
	if applying.State != OperationApplying || applying.FinishedAt != nil || !applying.UpdatedAt.After(createdAt) {
		t.Fatalf("applying operation = %#v", applying)
	}
	if err := journal.Transition(context.Background(), "op-transition", OperationFailed, "executor failed"); err != nil {
		t.Fatal(err)
	}
	failed, err := journal.Get(context.Background(), "op-transition")
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != OperationFailed || failed.Error != "executor failed" || failed.FinishedAt == nil {
		t.Fatalf("failed operation = %#v", failed)
	}
	if !failed.FinishedAt.Equal(failed.UpdatedAt) {
		t.Fatalf("finished_at = %v, updated_at = %v", failed.FinishedAt, failed.UpdatedAt)
	}

	err = journal.Transition(context.Background(), "op-transition", OperationApplying, "")
	var invalid *InvalidTransitionError
	if !errors.As(err, &invalid) || !errors.Is(err, ErrInvalidOperationTransition) {
		t.Fatalf("invalid transition error = %T %v", err, err)
	}
	if invalid.OperationID != "op-transition" || invalid.From != OperationFailed || invalid.To != OperationApplying {
		t.Fatalf("invalid transition = %#v", invalid)
	}
}

func TestJournalTransitionDoesNotReviveCancelledOperation(t *testing.T) {
	journal := openTestJournal(t)
	if err := journal.Create(context.Background(), Operation{ID: "op-cancelled", State: OperationPending}); err != nil {
		t.Fatal(err)
	}
	result, err := journal.CancelPending(context.Background(), "op-cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted {
		t.Fatalf("cancel result = %#v", result)
	}
	err = journal.Transition(context.Background(), "op-cancelled", OperationApplying, "")
	var invalid *InvalidTransitionError
	if !errors.As(err, &invalid) || invalid.From != OperationCancelled {
		t.Fatalf("revive error = %T %#v, want cancelled InvalidTransitionError", err, err)
	}
	operation, err := journal.Get(context.Background(), "op-cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != OperationCancelled {
		t.Fatalf("state = %s, want cancelled", operation.State)
	}
}

func TestJournalCancelPendingIsAtomicAndRejectsApplying(t *testing.T) {
	journal := openTestJournal(t)
	if err := journal.Create(context.Background(), Operation{ID: "op-pending", State: OperationPending}); err != nil {
		t.Fatal(err)
	}
	result, err := journal.CancelPending(context.Background(), "op-pending")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || result.State != OperationCancelled || result.OperationID != "op-pending" {
		t.Fatalf("pending cancel result = %#v", result)
	}
	cancelled, err := journal.Get(context.Background(), "op-pending")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != OperationCancelled || cancelled.FinishedAt == nil {
		t.Fatalf("cancelled operation = %#v", cancelled)
	}

	if err := journal.Create(context.Background(), Operation{ID: "op-applying", State: OperationApplying}); err != nil {
		t.Fatal(err)
	}
	result, err = journal.CancelPending(context.Background(), "op-applying")
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted || result.State != OperationApplying || result.Reason == "" {
		t.Fatalf("applying cancel result = %#v", result)
	}
	applying, err := journal.Get(context.Background(), "op-applying")
	if err != nil {
		t.Fatal(err)
	}
	if applying.State != OperationApplying {
		t.Fatalf("state after rejected cancel = %s", applying.State)
	}
}

func TestJournalCancelAndApplyRaceHasOneConsistentWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	first, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.Create(context.Background(), Operation{ID: "op-race", State: OperationPending}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var transitionErr error
	var cancelResult CancelResult
	var cancelErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		transitionErr = first.Transition(context.Background(), "op-race", OperationApplying, "")
	}()
	go func() {
		defer wait.Done()
		<-start
		cancelResult, cancelErr = second.CancelPending(context.Background(), "op-race")
	}()
	close(start)
	wait.Wait()
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	operation, err := first.Get(context.Background(), "op-race")
	if err != nil {
		t.Fatal(err)
	}
	switch operation.State {
	case OperationCancelled:
		if !cancelResult.Accepted || !errors.Is(transitionErr, ErrInvalidOperationTransition) {
			t.Fatalf("cancel won: result=%#v transitionErr=%v", cancelResult, transitionErr)
		}
	case OperationApplying:
		if transitionErr != nil || cancelResult.Accepted || cancelResult.State != OperationApplying {
			t.Fatalf("transition won: result=%#v transitionErr=%v", cancelResult, transitionErr)
		}
	default:
		t.Fatalf("race final state = %s", operation.State)
	}
}

func TestJournalListHasStableOrderAndLimitSemantics(t *testing.T) {
	journal := openTestJournal(t)
	base := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
	for _, operation := range []Operation{
		{ID: "op-b", State: OperationSucceeded, CreatedAt: base, UpdatedAt: base},
		{ID: "op-c", State: OperationSucceeded, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)},
		{ID: "op-a", State: OperationSucceeded, CreatedAt: base, UpdatedAt: base},
	} {
		if err := journal.Create(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		limit int
		want  []OperationID
	}{
		{limit: 0, want: []OperationID{"op-c", "op-a", "op-b"}},
		{limit: -1, want: []OperationID{"op-c", "op-a", "op-b"}},
		{limit: 2, want: []OperationID{"op-c", "op-a"}},
	} {
		operations, err := journal.List(context.Background(), tt.limit)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]OperationID, len(operations))
		for index := range operations {
			got[index] = operations[index].ID
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("List(%d) IDs = %v, want %v", tt.limit, got, tt.want)
		}
	}
}

func TestJournalListOrdersSubsecondTimesBeforeApplyingLimit(t *testing.T) {
	journal := openTestJournal(t)
	second := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
	for _, operation := range []Operation{
		{ID: "op-early", State: OperationSucceeded, CreatedAt: second, UpdatedAt: second},
		{ID: "op-late", State: OperationSucceeded, CreatedAt: second.Add(900 * time.Millisecond), UpdatedAt: second.Add(900 * time.Millisecond)},
	} {
		if err := journal.Create(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[OperationID]string{
		"op-early": "2026-07-14T10:00:00.000000000Z",
		"op-late":  "2026-07-14T10:00:00.900000000Z",
	} {
		var stored string
		if err := journal.db.QueryRow(`SELECT created_at FROM operations WHERE id = ?`, id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != want {
			t.Fatalf("%s created_at = %q, want %q", id, stored, want)
		}
	}

	operations, err := journal.List(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].ID != "op-late" {
		t.Fatalf("List(1) = %#v, want op-late", operations)
	}
}

func TestJournalReadsLegacyVariableWidthTimestamps(t *testing.T) {
	journal := openTestJournal(t)
	if err := journal.Create(context.Background(), Operation{ID: "op-legacy-time", State: OperationSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.db.Exec(`
		UPDATE operations
		SET created_at = ?, updated_at = ?, finished_at = ?
		WHERE id = ?`,
		"2026-07-14T10:00:00Z",
		"2026-07-14T10:00:00.9Z",
		"2026-07-14T10:00:00.123456Z",
		"op-legacy-time",
	); err != nil {
		t.Fatal(err)
	}

	operation, err := journal.Get(context.Background(), "op-legacy-time")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
	if !operation.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %s, want %s", operation.CreatedAt, createdAt)
	}
	if !operation.UpdatedAt.Equal(createdAt.Add(900 * time.Millisecond)) {
		t.Fatalf("updated_at = %s", operation.UpdatedAt)
	}
	if operation.FinishedAt == nil || !operation.FinishedAt.Equal(createdAt.Add(123456*time.Microsecond)) {
		t.Fatalf("finished_at = %v", operation.FinishedAt)
	}
}

func TestJournalReportsNotFoundClosedAndCancelledContext(t *testing.T) {
	journal := openTestJournal(t)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"get": func() error {
			_, err := journal.Get(ctx, "missing")
			return err
		},
		"transition": func() error {
			return journal.Transition(ctx, "missing", OperationApplying, "")
		},
		"cancel": func() error {
			_, err := journal.CancelPending(ctx, "missing")
			return err
		},
	} {
		t.Run("not found "+name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, sql.ErrNoRows) || !strings.Contains(err.Error(), "missing") {
				t.Fatalf("error = %T %v, want clear sql.ErrNoRows", err, err)
			}
		})
	}

	if err := journal.Create(ctx, Operation{ID: "op-context", State: OperationPending}); err != nil {
		t.Fatal(err)
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	for name, call := range map[string]func() error{
		"create": func() error {
			return journal.Create(cancelledContext, Operation{ID: "op-new", State: OperationSucceeded})
		},
		"get": func() error {
			_, err := journal.Get(cancelledContext, "op-context")
			return err
		},
		"list": func() error {
			_, err := journal.List(cancelledContext, 0)
			return err
		},
		"transition": func() error {
			return journal.Transition(cancelledContext, "op-context", OperationApplying, "")
		},
		"cancel": func() error {
			_, err := journal.CancelPending(cancelledContext, "op-context")
			return err
		},
	} {
		t.Run("cancelled context "+name, func(t *testing.T) {
			if err := call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %T %v, want context.Canceled", err, err)
			}
		})
	}

	closedPath := filepath.Join(t.TempDir(), "closed.db")
	closed, err := OpenJournal(closedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.Get(context.Background(), "op"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "closed") {
		t.Fatalf("Get after Close error = %v, want clear closed error", err)
	}
}

func openTestJournal(t *testing.T) *Journal {
	t.Helper()
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close journal: %v", err)
		}
	})
	return journal
}
