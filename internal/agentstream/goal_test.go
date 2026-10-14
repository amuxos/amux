package agentstream

import (
	"context"
	"testing"
)

func TestGoalStickySnapshotAndCodec(t *testing.T) {
	timeline := New(Config{SessionID: "goal-session"})
	budget := int64(40)
	goal := &GoalState{ThreadID: "native", Objective: "finish", Status: "active", TokenBudget: &budget}
	event, err := timeline.Publish(context.Background(), GoalChanged{Supported: true, Goal: goal}, Origin{})
	if err != nil {
		t.Fatal(err)
	}
	budget = 1
	goal.Objective = "changed"
	head := timeline.Head()
	if head.Goal == nil || head.Goal.Goal.Objective != "finish" || *head.Goal.Goal.TokenBudget != 40 {
		t.Fatalf("snapshot aliased: %+v", head.Goal)
	}
	*head.Goal.Goal.TokenBudget = 2
	if *timeline.Head().Goal.Goal.TokenBudget != 40 {
		t.Fatal("returned head aliases retained goal")
	}
	encoded, err := MarshalEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalEvent(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Body.(GoalChanged).Goal.Objective != "finish" {
		t.Fatal("codec lost goal")
	}
	if _, err := timeline.Publish(context.Background(), GoalChanged{Supported: true}, Origin{}); err != nil {
		t.Fatal(err)
	}
	if timeline.Head().Goal.Goal != nil {
		t.Fatal("clear did not replace sticky state")
	}
}
