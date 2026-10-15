package hub

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestShouldNotifyParent(t *testing.T) {
	off := false
	cases := []struct {
		spec task.Spec
		want bool
	}{
		{task.Spec{}, false},
		{task.Spec{ParentSessionID: "p"}, true},
		{task.Spec{ParentSessionID: "p", Blocking: true}, false},
		{task.Spec{ParentSessionID: "p", NotifyParent: &off}, false},
	}
	for i, c := range cases {
		if got := shouldNotifyParent(c.spec); got != c.want {
			t.Fatalf("case %d = %v", i, got)
		}
	}
}

func TestParentNotifyText(t *testing.T) {
	run := &task.TaskRun{ID: "t1", AgentInstanceID: "agent-1", Status: task.StatusSucceeded, Result: &task.TaskResult{Summary: strings.Repeat("好", 900)}}
	got := parentNotifyText(run)
	if !strings.HasPrefix(got, "[后台任务完成] agent_instance_id=agent-1 task_id=t1 status=succeeded\n结论: ") || !strings.Contains(got, "已截断") {
		t.Fatalf("text = %q", got)
	}
}

type notifyFixture struct {
	mu      sync.Mutex
	status  task.Status
	submits []*pb.SubmitRequest
}

func newNotifyRouter(t *testing.T, f *notifyFixture, enabled bool, owner string) *taskRouter {
	hub := &fakeRouterHub{
		targets: []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{Id: "parent-id", Name: "main", OwnerTaskId: owner}}}},
		routeFn: func(_ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			switch method {
			case pb.TaskMethodGet:
				run, _ := json.Marshal(task.TaskRun{ID: "task-1", ParentSessionID: "parent-id", AgentInstanceID: "agent-1", Status: f.status, FinishedAt: time.Unix(10, 0), Result: &task.TaskResult{Summary: "ok"}})
				return respJSON(t, &pb.GetSubtaskResponse{TaskJson: run}), nil
			case "Submit":
				f.submits = append(f.submits, payload.(*pb.SubmitRequest))
				return respJSON(t, &pb.SubmitResponse{Ok: true}), nil
			}
			return respJSON(t, struct{}{}), nil
		},
		streamFn: func(string, string, interface{}, func(*pb.TunnelMsg) error) error {
			f.mu.Lock()
			f.status = task.StatusSucceeded
			f.mu.Unlock()
			return nil
		},
	}
	r := newTaskRouter(hub, nil, newTaskIndex(), nil)
	r.parentNotify = &parentNotifier{enabled: func() bool { return enabled }, retryDelay: time.Millisecond}
	r.index.put("task-1", taskPlacement{TargetID: "t1", ParentSessionID: "parent-id"})
	return r
}

func TestParentNotifyQueuesOnceToParent(t *testing.T) {
	f := &notifyFixture{status: task.StatusRunning}
	r := newNotifyRouter(t, f, true, "")
	run, ok := r.awaitTerminalTask(context.Background(), "task-1")
	if !ok {
		t.Fatal("no terminal run")
	}
	r.deliverParentNotify(context.Background(), run)
	r.deliverParentNotify(context.Background(), run) // dedup
	if len(f.submits) != 1 {
		t.Fatalf("submits = %d", len(f.submits))
	}
	s := f.submits[0]
	if s.Name != "main" || s.Source != "subtask" || !s.Async || s.Steer || s.Preempt || !strings.Contains(s.Text, "task_id=task-1") {
		t.Fatalf("submit = %+v", s)
	}
}

func TestParentNotifySkips(t *testing.T) {
	for name, tc := range map[string]struct {
		enabled bool
		owner   string
	}{"switch off": {false, ""}, "task child parent": {true, "owner-task"}} {
		f := &notifyFixture{status: task.StatusFailed}
		r := newNotifyRouter(t, f, tc.enabled, tc.owner)
		run, _ := r.readTaskRun(context.Background(), "task-1")
		r.deliverParentNotify(context.Background(), run)
		if len(f.submits) != 0 {
			t.Fatalf("%s: submitted", name)
		}
	}
	f := &notifyFixture{status: task.StatusFailed}
	r := newNotifyRouter(t, f, true, "")
	run, _ := r.readTaskRun(context.Background(), "task-1")
	run.ParentSessionID = "gone"
	r.deliverParentNotify(context.Background(), run)
	if len(f.submits) != 0 {
		t.Fatal("gone parent: submitted")
	}
}

func TestParentNotifyTextTailHasNoRawIDsInstruction(t *testing.T) {
	run := &task.TaskRun{ID: "t1", Status: task.StatusFailed, Error: "interrupted by runtime restart"}
	got := parentNotifyText(run)
	if !strings.Contains(got, "不要复述") || !strings.Contains(got, "由你判断") || !strings.Contains(got, "interrupted by runtime restart") {
		t.Fatalf("text = %q", got)
	}
	if strings.Contains(got, "task_get task_id=") {
		t.Fatalf("tail leaks raw id: %q", got)
	}
}

func TestNoticeConclusionPrefersFinalConclusion(t *testing.T) {
	in := "我先看一下代码。\n\n然后运行测试，发现两个失败。\n\n## 结论\n已修复两个失败测试，全部通过。"
	if got := noticeConclusion(in); got != "## 结论\n已修复两个失败测试，全部通过。" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("思考过程。", 300) + "\n\n最后：完成了。"
	got := noticeConclusion(long)
	if !strings.HasSuffix(got, "最后：完成了。") || len([]rune(got)) > parentNotifySummaryRunes+1 {
		t.Fatalf("tail not kept: %d %q", len([]rune(got)), got[len(got)-30:])
	}
	if got := noticeConclusion("短结果"); got != "短结果" {
		t.Fatalf("short = %q", got)
	}
}

func TestParentNotifyResumesPersistedWatchAfterRestart(t *testing.T) {
	path := t.TempDir() + "/parent-notify.json"
	// Before the restart: a watch was registered but amuxd died.
	before := newPersistentParentNotifier(path)
	before.addPending("task-1", "parent-id")

	f := &notifyFixture{status: task.StatusFailed}
	r := newNotifyRouter(t, f, true, "")
	n := newPersistentParentNotifier(path)
	n.enabled = func() bool { return true }
	n.retryDelay = time.Millisecond
	r.parentNotify = n
	r.resumeParentNotify()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		got := len(f.submits)
		f.mu.Unlock()
		if got == 1 && len(n.pendingTasks()) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("submits=%d pending=%d", got, len(n.pendingTasks()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A second restart must not deliver the same run again.
	again := newPersistentParentNotifier(path)
	run, _ := r.readTaskRun(context.Background(), "task-1")
	if again.claimDelivery(run) {
		t.Fatal("delivered key was not persisted")
	}
}

func TestParentNotifyRetriesFailedSubmit(t *testing.T) {
	f := &notifyFixture{status: task.StatusFailed}
	r := newNotifyRouter(t, f, true, "")
	fails := 2
	hub := r.hub.(*fakeRouterHub)
	inner := hub.routeFn
	hub.routeFn = func(target string, method string, payload interface{}) (*pb.TunnelMsg, error) {
		if method == "Submit" && fails > 0 {
			fails--
			return nil, context.DeadlineExceeded
		}
		return inner(target, method, payload)
	}
	run, _ := r.readTaskRun(context.Background(), "task-1")
	r.deliverParentNotify(context.Background(), run)
	if len(f.submits) != 1 {
		t.Fatalf("submits = %d", len(f.submits))
	}
}
