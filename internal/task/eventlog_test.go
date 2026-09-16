package task

import (
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/store"
)

// TestEventStoreParity runs the same contract against the in-memory and SQL
// EventStore implementations so they cannot drift.
func TestEventStoreParity(t *testing.T) {
	run := func(t *testing.T, es EventStore) {
		s1, err := es.Append("t1", TaskEvent{Kind: TaskEventStatus, Status: StatusRunning})
		if err != nil || s1 != 1 {
			t.Fatalf("append1 seq=%d err=%v", s1, err)
		}
		s2, _ := es.Append("t1", TaskEvent{Kind: TaskEventOutput, Output: []byte("hello")})
		if s2 != 2 {
			t.Fatalf("append2 seq=%d", s2)
		}
		evs, err := es.Events("t1", 0)
		if err != nil || len(evs) != 2 {
			t.Fatalf("events len=%d err=%v", len(evs), err)
		}
		if evs[0].Seq != 1 || evs[0].Status != StatusRunning {
			t.Fatalf("ev0 = %+v", evs[0])
		}
		if evs[1].Seq != 2 || string(evs[1].Output) != "hello" {
			t.Fatalf("ev1 = %+v", evs[1])
		}
		from1, _ := es.Events("t1", 1)
		if len(from1) != 1 || from1[0].Seq != 2 {
			t.Fatalf("from1 = %+v", from1)
		}
	}
	t.Run("mem", func(t *testing.T) { run(t, NewMemStore()) })
	t.Run("sql", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer db.Close()
		run(t, NewSQLStore(db))
	})
}
