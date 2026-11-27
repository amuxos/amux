package bridge

import (
	"context"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type fakeSubtaskReader struct {
	ids  []string
	json map[string]string
}

func (f *fakeSubtaskReader) SubtaskIDsByParent(string) []string { return f.ids }

func (f *fakeSubtaskReader) GetSubtaskJSON(_ context.Context, id string) ([]byte, error) {
	return []byte(f.json[id]), nil
}

type fakeTargetsHub struct {
	fakeSubmitHub
	targets []*pb.Target
}

func (h *fakeTargetsHub) ListTargets() []*pb.Target { return h.targets }

func TestRefreshSubtaskProgressRendersBlock(t *testing.T) {
	t.Parallel()
	hub := &fakeTargetsHub{targets: []*pb.Target{{
		Id:       "t1",
		Sessions: []*pb.Session{{Id: "sess-1", Name: "main"}},
	}}}
	reader := &fakeSubtaskReader{
		ids: []string{"task-a"},
		json: map[string]string{
			"task-a": `{"id":"task-a","agent_type":"codex","agent_profile":"team/reviewer","status":"succeeded","task":"重构 session 队列","result":{"summary":"完成队列重构"},"requested_at":"2026-06-11T09:59:59Z","started_at":"2026-06-11T10:00:00Z","finished_at":"2026-06-11T10:02:08Z"}`,
		},
	}
	b := &Bridge{hub: hub, subtasks: reader}
	a := &toolActivityState{nowFn: func() time.Time {
		return time.Date(2026, 6, 11, 10, 2, 8, 0, time.UTC)
	}}
	a.StartTurn(time.Date(2026, 6, 11, 9, 59, 58, 0, time.UTC))
	b.refreshSubtaskProgress(a, "t1", "main")

	rows := a.Subtasks()
	if len(rows) != 1 || rows[0].AgentType != "codex" || rows[0].AgentProfile != "team/reviewer" || rows[0].Status != "succeeded" || rows[0].Summary != "完成队列重构" || rows[0].Title != "重构 session 队列" || rows[0].Elapsed != 2*time.Minute+8*time.Second {
		t.Fatalf("subtasks = %+v, want running row at 2m8s", rows)
	}
}

func TestRefreshSubtaskProgressExcludesTasksFromEarlierTurn(t *testing.T) {
	t.Parallel()
	hub := &fakeTargetsHub{targets: []*pb.Target{{
		Id:       "t1",
		Sessions: []*pb.Session{{Id: "sess-1", Name: "main"}},
	}}}
	reader := &fakeSubtaskReader{
		ids: []string{"old", "current"},
		json: map[string]string{
			"old":     `{"id":"old","status":"cancelled","task":"上一轮任务","requested_at":"2026-06-11T09:55:00Z"}`,
			"current": `{"id":"current","status":"running","task":"当前任务","requested_at":"2026-06-11T10:00:01Z"}`,
		},
	}
	b := &Bridge{hub: hub, subtasks: reader}
	a := &toolActivityState{nowFn: func() time.Time { return time.Date(2026, 6, 11, 10, 1, 0, 0, time.UTC) }}
	a.StartTurn(time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC))

	b.refreshSubtaskProgress(a, "t1", "main")
	rows := a.Subtasks()
	if len(rows) != 1 || rows[0].ID != "current" {
		t.Fatalf("subtasks = %+v, want only current turn task", rows)
	}
}

func TestRefreshSubtaskProgressNoReaderIsNoop(t *testing.T) {
	t.Parallel()
	b := &Bridge{hub: &fakeSubmitHub{}}
	a := &toolActivityState{}
	b.refreshSubtaskProgress(a, "t1", "main") // must not panic
	if rows := a.Subtasks(); len(rows) != 0 {
		t.Fatalf("subtasks = %+v, want empty", rows)
	}
}
