package diagnose

import (
	"context"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/task"
)

type fakeSource struct{}

func (fakeSource) LoadControlPlane(context.Context) ([]Session, []*task.TaskRun, []string) {
	return []Session{
			{TargetID: "target-1", SessionID: "parent-session", SessionName: "parent", LaunchID: "parent-launch"},
			{TargetID: "target-1", SessionID: "child-session", SessionName: "child", LaunchID: "child-launch", OwnerTaskID: "task-child"},
		}, []*task.TaskRun{
			{ID: "task-parent", TurnID: "parent-task-turn", ChildSessionId: "parent-child", RequestedAt: time.Unix(1, 0)},
			{ID: "task-child", ParentTaskID: "task-parent", ParentSessionID: "parent-session", ParentTurnID: "message-turn", TurnID: "child-turn", ChildSessionId: "child-session", RequestedAt: time.Unix(2, 0)},
		}, nil
}

func (fakeSource) LookupTurnLinks(_ context.Context, query bridge.TurnLinkQuery) ([]bridge.TurnLink, error) {
	if query.TurnID == "message-turn" {
		return []bridge.TurnLink{{BridgeID: "feishu:test", MessageID: "message-1", SessionID: "parent-session", TurnID: "message-turn"}}, nil
	}
	return nil, nil
}

func TestGatherExpandsTaskParentTurnToMessage(t *testing.T) {
	report := Gather(context.Background(), fakeSource{}, Query{Kind: QueryTaskID, Value: "task-child"})
	if len(report.Tasks) != 2 || len(report.Sessions) != 2 || len(report.Links) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Links[0].MessageID != "message-1" || report.Tasks[1].TurnID != "child-turn" {
		t.Fatalf("correlation lost: %+v", report)
	}
}
