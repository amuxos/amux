package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/store"
)

func TestRuntimeDBUsesOwnerOnlyPermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("runtime.db mode = %o, want 600", got)
	}
}

func TestRuntimeDBUsesWALAndBusyTimeout(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy timeout = %d, want 5000", busyTimeout)
	}
}

func TestAgentEventAppendSurvivesConcurrentReader(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveSession(store.SessionRow{ID: "session-1", Name: "one", State: "created", TimelineEpoch: "epoch-1"}); err != nil {
		t.Fatal(err)
	}
	first := agentstream.Event{
		SessionID: "session-1",
		Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: 1},
		At:        time.Now().UTC(),
		Body:      agentstream.CommandsReplaced{},
	}
	if err := db.AppendAgentEvent(context.Background(), first); err != nil {
		t.Fatalf("append seed event: %v", err)
	}

	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open concurrent reader: %v", err)
	}
	defer reader.Close()
	readerTx, err := reader.Begin()
	if err != nil {
		t.Fatalf("begin concurrent reader: %v", err)
	}
	defer readerTx.Rollback()
	var count int
	if err := readerTx.QueryRow(`SELECT COUNT(*) FROM session_timeline_events`).Scan(&count); err != nil {
		t.Fatalf("hold concurrent read transaction: %v", err)
	}
	if count != 1 {
		t.Fatalf("seed event count = %d, want 1", count)
	}

	second := first
	second.Cursor.Seq = 2
	second.At = first.At.Add(time.Nanosecond)
	if err := db.AppendAgentEvent(context.Background(), second); err != nil {
		t.Fatalf("append with concurrent reader: %v", err)
	}
}

func TestAgentEventAppendWaitsForTransientWriter(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveSession(store.SessionRow{ID: "session-1", Name: "one", State: "created", TimelineEpoch: "epoch-1"}); err != nil {
		t.Fatal(err)
	}

	blocker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open concurrent writer: %v", err)
	}
	defer blocker.Close()
	blockerTx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("begin concurrent writer: %v", err)
	}
	if _, err := blockerTx.Exec(`UPDATE sessions SET state=? WHERE id=?`, "blocked", "session-1"); err != nil {
		_ = blockerTx.Rollback()
		t.Fatalf("acquire concurrent write lock: %v", err)
	}

	event := agentstream.Event{
		SessionID: "session-1",
		Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: 1},
		At:        time.Now().UTC(),
		Body:      agentstream.CommandsReplaced{},
	}
	appendDone := make(chan error, 1)
	go func() {
		appendDone <- db.AppendAgentEvent(context.Background(), event)
	}()

	select {
	case err := <-appendDone:
		_ = blockerTx.Rollback()
		t.Fatalf("append returned before transient writer released lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blockerTx.Commit(); err != nil {
		t.Fatalf("commit concurrent writer: %v", err)
	}
	select {
	case err := <-appendDone:
		if err != nil {
			t.Fatalf("append after transient writer released lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("append remained blocked after transient writer released lock")
	}

	next := event
	next.Cursor.Seq = 2
	next.At = event.At.Add(time.Nanosecond)
	if err := db.AppendAgentEvent(context.Background(), next); err != nil {
		t.Fatalf("append after lock contention: %v", err)
	}
}

func TestAgentEventsAppendLoadAndDeleteWithSession(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, row := range []store.SessionRow{
		{ID: "session-1", Name: "one", AgentType: "codex", State: "created", TimelineEpoch: "epoch-1"},
		{ID: "session-2", Name: "two", AgentType: "codex", State: "created", TimelineEpoch: "epoch-2"},
	} {
		if err := db.SaveSession(row); err != nil {
			t.Fatal(err)
		}
	}
	events := []agentstream.Event{
		{
			SessionID: "session-1",
			Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: 1},
			Turn:      &agentstream.TurnRef{ID: "turn-1", Seq: 1},
			At:        time.Unix(100, 123).UTC(),
			Origin:    agentstream.Origin{Actor: agentstream.ActorUser, Channel: "cli"},
			Body:      agentstream.TurnStarted{InputSummary: "hello"},
		},
		{
			SessionID: "session-1",
			Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: 2},
			Turn:      &agentstream.TurnRef{ID: "turn-1", Seq: 1},
			At:        time.Unix(101, 456).UTC(),
			Origin:    agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "codex"},
			Body:      agentstream.AssistantDelta{Text: "world"},
		},
	}
	for _, event := range events {
		if err := db.AppendAgentEvent(context.Background(), event); err != nil {
			t.Fatalf("AppendAgentEvent(seq=%d) error = %v", event.Cursor.Seq, err)
		}
	}
	other := agentstream.Event{
		SessionID: "session-2",
		Cursor:    agentstream.Cursor{Epoch: "epoch-2", Seq: 1},
		At:        time.Unix(102, 0).UTC(),
		Origin:    agentstream.Origin{Actor: agentstream.ActorSystem},
		Body:      agentstream.CommandsReplaced{},
	}
	if err := db.AppendAgentEvent(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	got, err := db.LoadAgentEvents(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, events) {
		t.Fatalf("loaded events mismatch\n got: %#v\nwant: %#v", got, events)
	}
	if err := db.AppendAgentEvent(context.Background(), events[1]); err != nil {
		t.Fatalf("idempotent duplicate append error = %v", err)
	}
	conflict := events[1]
	conflict.Body = agentstream.AssistantDelta{Text: "different"}
	if err := db.AppendAgentEvent(context.Background(), conflict); err == nil {
		t.Fatal("conflicting duplicate append unexpectedly succeeded")
	}

	if err := db.DeleteSession("session-1"); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.LoadAgentEvents(context.Background(), "session-1")
	if err != nil || len(deleted) != 0 {
		t.Fatalf("deleted session events = (%#v, %v), want empty", deleted, err)
	}
	remaining, err := db.LoadAgentEvents(context.Background(), "session-2")
	if err != nil || len(remaining) != 1 {
		t.Fatalf("other session events = (%#v, %v), want one", remaining, err)
	}
}

func TestAgentEventAppendRejectsMissingSessionAndEpochMismatch(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveSession(store.SessionRow{ID: "session-1", Name: "one", State: "created", TimelineEpoch: "epoch-1"}); err != nil {
		t.Fatal(err)
	}
	base := agentstream.Event{
		SessionID: "missing",
		Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: 1},
		At:        time.Now().UTC(),
		Body:      agentstream.CommandsReplaced{},
	}
	if err := db.AppendAgentEvent(context.Background(), base); err == nil {
		t.Fatal("append for missing session unexpectedly succeeded")
	}
	base.SessionID = "session-1"
	base.Cursor.Epoch = "other-epoch"
	if err := db.AppendAgentEvent(context.Background(), base); !errors.Is(err, store.ErrTimelineEpochMismatch) {
		t.Fatalf("epoch mismatch error = %v, want ErrTimelineEpochMismatch", err)
	}
}

func TestSaveSessionCannotRewriteTimelineEpoch(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row := store.SessionRow{ID: "session-1", Name: "one", State: "created", TimelineEpoch: "epoch-1"}
	if err := db.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	row.Name = "renamed"
	row.TimelineEpoch = "epoch-2"
	if err := db.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	sessions, err := db.AllSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].TimelineEpoch != "epoch-1" {
		t.Fatalf("timeline epoch after update = %#v, want epoch-1", sessions)
	}
	if sessions[0].Name != "one" {
		t.Fatalf("session name after update = %q, want immutable insert value one", sessions[0].Name)
	}
}
