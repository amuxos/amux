package store_test

import (
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/store"
)

func TestTaskEventsAppendReadDelete(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// seq is monotonic per task, starting at 1.
	s1, err := db.AppendTaskEvent("t1", "status", `{"kind":"status"}`, 100)
	if err != nil || s1 != 1 {
		t.Fatalf("append1 seq=%d err=%v", s1, err)
	}
	s2, _ := db.AppendTaskEvent("t1", "output", `{"kind":"output"}`, 101)
	if s2 != 2 {
		t.Fatalf("append2 seq=%d", s2)
	}
	// A different task has its own independent seq.
	o1, _ := db.AppendTaskEvent("t2", "status", `{"kind":"status"}`, 102)
	if o1 != 1 {
		t.Fatalf("task2 seq=%d", o1)
	}

	evs, err := db.TaskEvents("t1", 0)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events len=%d err=%v", len(evs), err)
	}
	if evs[0].Seq != 1 || evs[1].Seq != 2 || evs[1].Kind != "output" {
		t.Fatalf("rows = %+v", evs)
	}

	// fromSeq returns strictly seq > fromSeq.
	from1, _ := db.TaskEvents("t1", 1)
	if len(from1) != 1 || from1[0].Seq != 2 {
		t.Fatalf("from1 = %+v", from1)
	}

	// DeleteTask cascades to task_events.
	if err := db.DeleteTask("t1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	left, _ := db.TaskEvents("t1", 0)
	if len(left) != 0 {
		t.Fatalf("events remain after delete: %d", len(left))
	}
	// t2 untouched.
	if other, _ := db.TaskEvents("t2", 0); len(other) != 1 {
		t.Fatalf("t2 events = %d", len(other))
	}
}
