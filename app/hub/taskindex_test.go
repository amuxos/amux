package hub

import (
	"reflect"
	"testing"
)

func TestTaskIndexDiscoveryPreservesAdmissionAndProvenance(t *testing.T) {
	idx := newTaskIndex()
	placed := taskPlacement{TargetID: "runtime-1", ReservationId: "reservation-1", ParentSessionID: "parent-1", AgentInstanceID: "instance-1"}
	idx.put("task-1", placed)
	// A discovery that started before Submit completed may arrive later without
	// the reservation ID. It must not remove the facts needed to release quota.
	idx.put("task-1", taskPlacement{TargetID: "runtime-1"})
	if got, ok := idx.get("task-1"); !ok || got != placed {
		t.Fatalf("discovery replaced admission facts: got %+v, want %+v", got, placed)
	}
}

func TestTaskIndexByParent(t *testing.T) {
	idx := newTaskIndex()
	idx.put("t1", taskPlacement{TargetID: "a", ParentSessionID: "sess-1"})
	idx.put("t2", taskPlacement{TargetID: "a", ParentSessionID: "sess-2"})
	idx.put("t3", taskPlacement{TargetID: "b", ParentSessionID: "sess-1"})
	idx.put("t4", taskPlacement{TargetID: "b"}) // no parent: never matched

	if got := idx.byParent("sess-1"); !reflect.DeepEqual(got, []string{"t1", "t3"}) {
		t.Fatalf("byParent(sess-1) = %v, want [t1 t3]", got)
	}
	if got := idx.byParent(""); got != nil {
		t.Fatalf("byParent(empty) = %v, want nil", got)
	}
}

func TestTaskIndexByInstance(t *testing.T) {
	idx := newTaskIndex()
	idx.put("t1", taskPlacement{TargetID: "a", AgentInstanceID: "inst-1"})
	idx.put("t2", taskPlacement{TargetID: "b", AgentInstanceID: "inst-2"})
	idx.put("t3", taskPlacement{TargetID: "a", AgentInstanceID: "inst-1"})
	idx.put("t4", taskPlacement{TargetID: "c"}) // no instance: never matched

	// getInstance returns the placement of a task belonging to the instance.
	p, ok := idx.getInstance("inst-2")
	if !ok || p.TargetID != "b" {
		t.Fatalf("getInstance(inst-2) = %+v ok=%v, want b", p, ok)
	}
	if _, ok := idx.getInstance("nope"); ok {
		t.Fatal("getInstance(unknown) should be false")
	}
	if _, ok := idx.getInstance(""); ok {
		t.Fatal("getInstance(empty) should be false")
	}
}
