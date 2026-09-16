package task

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/store"
)

// TestSQLStoreRoundTrip exercises the tasks table (migration 7) and the
// TaskRun <-> TaskRow mapping, including the JSON-marshaled result blob.
func TestSQLStoreRoundTrip(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	s := NewSQLStore(db)
	now := time.Unix(1700000000, 0)
	run := &TaskRun{
		ID:                "task-1",
		ParentSessionID:   "psess",
		ParentTaskID:      "parent-task",
		ParentTurnID:      "parent-turn",
		TurnID:            "child-turn",
		Mode:              ModeEphemeral,
		AgentType:         "claude",
		AgentProfile:      "team/reviewer",
		TargetID:          "local",
		ChildSessionId:    "child-1",
		ChildSessionName:  "sess-1",
		IsolationMode:     IsolationPrivateHome,
		CredentialProfile: "claude-short",
		CredentialProfiles: []string{
			"github",
			"slack",
		},
		PermissionPolicy:  PolicyAllow,
		WorkspacePolicy:   WorkspaceGitWorktree,
		ResultGranularity: GranularitySummary,
		SecurityLevel:     "weak",
		Status:            StatusSucceeded,
		Task:              "do the thing",
		TimeoutSeconds:    120,
		RequestedAt:       now,
		StartedAt:         now.Add(time.Second),
		FinishedAt:        now.Add(2 * time.Second),
		Result: &TaskResult{
			Status:       ResultOK,
			Summary:      "done",
			ChangedFiles: []ChangedFile{{Path: "a.go", ChangeType: ChangeModified}},
		},
	}
	if err := s.Save(run); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.Get("task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentType != "claude" || got.AgentProfile != "team/reviewer" || got.Status != StatusSucceeded || got.WorkspacePolicy != WorkspaceGitWorktree {
		t.Fatalf("mismatch: %+v", got)
	}
	if got.ParentTaskID != "parent-task" || got.ParentTurnID != "parent-turn" || got.TurnID != "child-turn" {
		t.Fatalf("turn provenance not round-tripped: %+v", got)
	}
	if got.Result == nil || got.Result.Summary != "done" || len(got.Result.ChangedFiles) != 1 {
		t.Fatalf("result not round-tripped: %+v", got.Result)
	}
	if !equalStrings(got.CredentialProfiles, []string{"github", "slack"}) {
		t.Fatalf("credential_profiles = %v, want [github slack]", got.CredentialProfiles)
	}
	if !got.RequestedAt.Equal(now) {
		t.Fatalf("requested_at = %v want %v", got.RequestedAt, now)
	}

	// Update in place (ON CONFLICT path).
	run.Status = StatusFailed
	run.Error = "boom"
	if err := s.Save(run); err != nil {
		t.Fatalf("resave: %v", err)
	}
	got, _ = s.Get("task-1")
	if got.Status != StatusFailed || got.Error != "boom" {
		t.Fatalf("update not applied: %+v", got)
	}

	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	if err := s.Delete("task-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get("task-1"); err == nil {
		t.Fatal("expected not-found after delete")
	}
}

// TestSQLStoreDoesNotPersistFullTranscript guards the json:"-" decision on
// TaskResult.FullTranscript: a full transcript is large and lives in the task
// event log, so it must never round-trip through the store result_json column.
func TestSQLStoreDoesNotPersistFullTranscript(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	s := NewSQLStore(db)
	run := &TaskRun{
		ID:        "task-ft",
		AgentType: "claude",
		Status:    StatusSucceeded,
		Result: &TaskResult{
			Status:         ResultOK,
			Summary:        "done",
			FullTranscript: "the entire agent transcript that must not persist",
			DetailsRef:     "task-ft",
		},
	}
	if err := s.Save(run); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Get("task-ft")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Result == nil {
		t.Fatal("result missing")
	}
	if got.Result.FullTranscript != "" {
		t.Fatalf("full_transcript must not persist: %q", got.Result.FullTranscript)
	}
	if got.Result.Summary != "done" || got.Result.DetailsRef != "task-ft" {
		t.Fatalf("other result fields must round-trip: %+v", got.Result)
	}
}

// TestSessionOwnerTaskIdPersists verifies migration 6 added owner_task_id and
// that it round-trips through the session store.
func TestSessionOwnerTaskIdPersists(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	row := store.SessionRow{
		ID:          "s1",
		Name:        "child",
		AgentType:   "claude",
		State:       "running",
		OwnerTaskId: "task-1",
		CreatedAt:   1,
		UpdatedAt:   1,
	}
	if err := db.SaveSession(row); err != nil {
		t.Fatalf("save session: %v", err)
	}
	rows, err := db.AllSessions()
	if err != nil {
		t.Fatalf("all sessions: %v", err)
	}
	if len(rows) != 1 || rows[0].OwnerTaskId != "task-1" {
		t.Fatalf("owner_task_id not persisted: %+v", rows)
	}
}
