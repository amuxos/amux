package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type consoleLinksHub struct {
	*consoleResourceHub
	schedules []*pb.ScheduleItem
}

func (h *consoleLinksHub) ListSchedules(context.Context, *pb.ListSchedulesRequest, ...grpc.CallOption) (*pb.ListSchedulesResponse, error) {
	return &pb.ListSchedulesResponse{Schedules: h.schedules}, nil
}

func (h *consoleLinksHub) GetSchedule(_ context.Context, req *pb.GetScheduleRequest, _ ...grpc.CallOption) (*pb.GetScheduleResponse, error) {
	for _, item := range h.schedules {
		if item.GetId() == req.GetId() {
			return &pb.GetScheduleResponse{Schedule: item}, nil
		}
	}
	return &pb.GetScheduleResponse{}, nil
}

type consoleLinksTasks struct {
	pb.AmuxTaskClient
	runs      []*task.TaskRun
	instances []*pb.AgentInstance
}

func (f *consoleLinksTasks) ListSubtasks(context.Context, *pb.ListSubtasksRequest, ...grpc.CallOption) (*pb.ListSubtasksResponse, error) {
	out := &pb.ListSubtasksResponse{}
	for _, run := range f.runs {
		raw, _ := json.Marshal(run)
		out.TasksJson = append(out.TasksJson, raw)
	}
	return out, nil
}

func (f *consoleLinksTasks) GetSubtask(_ context.Context, req *pb.GetSubtaskRequest, _ ...grpc.CallOption) (*pb.GetSubtaskResponse, error) {
	for _, run := range f.runs {
		if run.ID == req.GetId() {
			raw, _ := json.Marshal(run)
			return &pb.GetSubtaskResponse{TaskJson: raw}, nil
		}
	}
	return &pb.GetSubtaskResponse{}, nil
}

func (f *consoleLinksTasks) ListAgentInstances(context.Context, *pb.ListAgentInstancesRequest, ...grpc.CallOption) (*pb.ListAgentInstancesResponse, error) {
	return &pb.ListAgentInstancesResponse{Instances: f.instances}, nil
}

func (f *consoleLinksTasks) GetAgentInstance(_ context.Context, req *pb.GetAgentInstanceRequest, _ ...grpc.CallOption) (*pb.GetAgentInstanceResponse, error) {
	for _, instance := range f.instances {
		if instance.GetId() == req.GetId() {
			return &pb.GetAgentInstanceResponse{Instance: instance}, nil
		}
	}
	return &pb.GetAgentInstanceResponse{}, nil
}

func consoleLinksFixture(t *testing.T) *consoleBackend {
	t.Helper()
	consoleTestOperator(t)
	base := consoleTestHub()
	base.targets = []*pb.Target{{Id: "runtime-a", Name: "worker", Sessions: []*pb.Session{{Id: "owner-1", Name: "feishu-thread-7", State: "running", AgentProfile: "team/lead", WorkspaceSource: "feishu"}}}}
	hub := &consoleLinksHub{consoleResourceHub: base, schedules: []*pb.ScheduleItem{
		{Id: 1, Name: "nightly review", Kind: "task", Enabled: true, Runs: []*pb.ScheduleRun{{Id: 1, ScheduleId: 1, TaskId: "task-sched-1"}, {Id: 2, ScheduleId: 1, TaskId: "task-sched-2"}}},
		{Id: 2, Kind: "session-prompt", Enabled: false, Runs: []*pb.ScheduleRun{{Id: 3, ScheduleId: 2}}},
	}}
	now := time.Now()
	tasks := &consoleLinksTasks{
		runs: []*task.TaskRun{
			{ID: "task-sched-1", Status: task.StatusSucceeded, Task: "review", RequestedAt: now.Add(-3 * time.Hour)},
			{ID: "task-sched-2", Status: task.StatusRunning, Task: "review", RequestedAt: now.Add(-1 * time.Hour)},
			{ID: "task-sub", Status: task.StatusSucceeded, Task: "child", ParentSessionID: "owner-1", RequestedAt: now.Add(-2 * time.Hour)},
			{ID: "task-direct", Status: task.StatusFailed, Task: "direct", Error: "secret --api-key=x", RequestedAt: now.Add(-4 * time.Hour)},
			{ID: "inst-first", AgentInstanceID: "agent-abc", ParentSessionID: "owner-1", Status: task.StatusSucceeded, Task: "Watch the  deploy\\npipeline and report regressions", RequestedAt: now.Add(-5 * time.Hour)},
			{ID: "inst-latest", AgentInstanceID: "agent-abc", ParentSessionID: "owner-1", Status: task.StatusRunning, Task: "check again", RequestedAt: now.Add(-30 * time.Minute)},
		},
		instances: []*pb.AgentInstance{
			{Id: "agent-abc", Name: "agent-abc", Status: "active", OwnerSessionId: "owner-1", AgentProfile: "team/watcher"},
			{Id: "agent-orphan", Name: "agent-orphan", Status: "idle", OwnerSessionId: "owner-gone"},
		},
	}
	return &consoleBackend{hub: hub, tasks: tasks}
}

func TestConsoleTasksLinkSchedulesAndFilter(t *testing.T) {
	backend := consoleLinksFixture(t)
	ctx := context.Background()
	value, err := backend.snapshotResource(ctx, "tasks", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tasks     []map[string]any `json:"tasks"`
		Schedules []map[string]any `json:"schedules"`
		Complete  bool             `json:"schedule_index_complete"`
	}
	if err := json.Unmarshal([]byte(consoleTestJSON(t, value)), &listed); err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, row := range listed.Tasks {
		sources[row["task_id"].(string)] = row["source"].(string)
		if row["task_id"] == "task-sched-2" && (row["schedule_name"] != "nightly review" || row["schedule_id"] != float64(1)) {
			t.Fatalf("scheduled task = %#v", row)
		}
	}
	want := map[string]string{"task-sched-1": "schedule", "task-sched-2": "schedule", "task-sub": "session", "task-direct": "direct", "inst-first": "instance", "inst-latest": "instance"}
	for id, source := range want {
		if sources[id] != source {
			t.Fatalf("source[%s] = %q, want %q (%v)", id, sources[id], source, sources)
		}
	}
	if !listed.Complete || len(listed.Schedules) != 2 || listed.Schedules[0]["task_count"] != float64(2) || listed.Schedules[1]["name"] != "#2" {
		t.Fatalf("schedules = %#v complete=%v", listed.Schedules, listed.Complete)
	}
	filtered, err := backend.snapshotResource(ctx, "tasks", url.Values{"schedule_id": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, filtered)
	if !strings.Contains(raw, "task-sched-1") || !strings.Contains(raw, "task-sched-2") || strings.Contains(raw, "task-direct") {
		t.Fatalf("filtered = %s", raw)
	}
	_, err = backend.snapshotResource(ctx, "tasks", url.Values{"schedule_id": {"x"}})
	consoleTestCode(t, err, "invalid_field")
	detail, err := backend.snapshotResource(ctx, "task", url.Values{"task_id": {"task-sched-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, detail); !strings.Contains(raw, `"schedule":{"id":1,"name":"nightly review","kind":"task"}`) {
		t.Fatalf("detail = %s", raw)
	}
}

func TestConsoleInstancesShowOwnerAndPurpose(t *testing.T) {
	backend := consoleLinksFixture(t)
	ctx := context.Background()
	value, err := backend.snapshotResource(ctx, "instances", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Instances []map[string]any `json:"instances"`
	}
	if err := json.Unmarshal([]byte(consoleTestJSON(t, value)), &listed); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, row := range listed.Instances {
		byID[row["agent_instance_id"].(string)] = row
	}
	watcher := byID["agent-abc"]
	owner, _ := watcher["owner"].(map[string]any)
	if owner["name"] != "feishu-thread-7" || owner["workspace_source"] != "feishu" || owner["agent_profile"] != "team/lead" {
		t.Fatalf("owner = %#v", watcher["owner"])
	}
	if watcher["purpose"] != `Watch the deploy\npipeline and report regressions` || watcher["task_count"] != float64(2) {
		t.Fatalf("purpose = %#v count=%v", watcher["purpose"], watcher["task_count"])
	}
	if latest := watcher["latest_task"].(map[string]any); latest["task_id"] != "inst-latest" || latest["status"] != "running" {
		t.Fatalf("latest = %#v", latest)
	}
	if _, ok := watcher["tasks"]; ok {
		t.Fatal("list should not embed task history")
	}
	if orphan := byID["agent-orphan"]; orphan["owner"].(map[string]any)["missing"] != true || orphan["task_count"] != float64(0) {
		t.Fatalf("orphan = %#v", orphan)
	}
	detail, err := backend.snapshotResource(ctx, "instance", url.Values{"agent_instance_id": {"agent-abc"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, detail)
	if !strings.Contains(raw, `"task_id":"inst-latest"`) || !strings.Contains(raw, `"task_id":"inst-first"`) || !strings.Contains(raw, `"source":"instance"`) {
		t.Fatalf("detail = %s", raw)
	}
	if long := consolePurpose(strings.Repeat("长", 400)); len([]rune(long)) != consolePurposeRunes+1 {
		t.Fatalf("purpose not truncated: %d", len([]rune(long)))
	}
}
