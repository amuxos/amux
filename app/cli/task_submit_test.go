package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

// captureTaskClient is a fake AmuxTaskClient that records the submitted SpecJson
// so a CLI test can assert exactly what `amux task submit` sends to the control
// plane.
type captureTaskClient struct {
	specJSON         []byte
	listed           bool
	pruneReq         *pb.PruneSubtasksRequest
	pruneResp        *pb.PruneSubtasksResponse
	listInstancesReq *pb.ListAgentInstancesRequest
	sendMsgReq       *pb.SendAgentMessageRequest
	interruptReq     *pb.InterruptAgentInstanceRequest
	waitReq          *pb.WaitAgentInstanceRequest
	waitResp         *pb.WaitAgentInstanceResponse
	lastCleanupID    string
	cancelReq        *pb.CancelSubtaskRequest
	streamCtx        context.Context
	streamStarted    chan struct{}
	streamEvents     []*pb.SubtaskEvent
	getResp          *pb.GetSubtaskResponse
	getErr           error
}

func (c *captureTaskClient) ResolveParentTurn(context.Context, *pb.ActiveTurnRequest, ...grpc.CallOption) (*pb.ActiveTurnResponse, error) {
	return &pb.ActiveTurnResponse{}, nil
}

func (c *captureTaskClient) SubmitSubtask(_ context.Context, in *pb.SubmitSubtaskRequest, _ ...grpc.CallOption) (*pb.SubmitSubtaskResponse, error) {
	c.specJSON = in.SpecJson
	return &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"t1","status":"pending"}`)}, nil
}

func (c *captureTaskClient) GetSubtask(context.Context, *pb.GetSubtaskRequest, ...grpc.CallOption) (*pb.GetSubtaskResponse, error) {
	return c.getResp, c.getErr
}

func (c *captureTaskClient) ListSubtasks(context.Context, *pb.ListSubtasksRequest, ...grpc.CallOption) (*pb.ListSubtasksResponse, error) {
	c.listed = true
	return &pb.ListSubtasksResponse{}, nil
}

func (c *captureTaskClient) PruneSubtasks(_ context.Context, req *pb.PruneSubtasksRequest, _ ...grpc.CallOption) (*pb.PruneSubtasksResponse, error) {
	cp := *req
	c.pruneReq = &cp
	if c.pruneResp != nil {
		return c.pruneResp, nil
	}
	return &pb.PruneSubtasksResponse{DeletedCount: 1, Tasks: []*pb.PrunedSubtask{{Id: "t1", Status: "succeeded"}}}, nil
}

func (c *captureTaskClient) CancelSubtask(_ context.Context, req *pb.CancelSubtaskRequest, _ ...grpc.CallOption) (*pb.CancelSubtaskResponse, error) {
	cp := *req
	c.cancelReq = &cp
	return &pb.CancelSubtaskResponse{}, nil
}

func (c *captureTaskClient) RespondSubtaskPermission(context.Context, *pb.RespondSubtaskPermissionRequest, ...grpc.CallOption) (*pb.RespondSubtaskPermissionResponse, error) {
	return nil, nil
}

func (c *captureTaskClient) StreamSubtask(ctx context.Context, _ *pb.StreamSubtaskRequest, _ ...grpc.CallOption) (pb.AmuxTask_StreamSubtaskClient, error) {
	c.streamCtx = ctx
	if c.streamStarted != nil {
		close(c.streamStarted)
	}
	if c.streamEvents != nil {
		return &taskEventStream{events: append([]*pb.SubtaskEvent(nil), c.streamEvents...)}, nil
	}
	return &blockingTaskStream{ClientStream: nil, ctx: ctx}, nil
}

type taskEventStream struct {
	grpc.ClientStream
	events []*pb.SubtaskEvent
}

func (s *taskEventStream) Recv() (*pb.SubtaskEvent, error) {
	if len(s.events) == 0 {
		return nil, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

type blockingTaskStream struct {
	grpc.ClientStream
	ctx context.Context
}

func (s *blockingTaskStream) Recv() (*pb.SubtaskEvent, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (c *captureTaskClient) ListAgentInstances(_ context.Context, req *pb.ListAgentInstancesRequest, _ ...grpc.CallOption) (*pb.ListAgentInstancesResponse, error) {
	cp := *req
	c.listInstancesReq = &cp
	return &pb.ListAgentInstancesResponse{Instances: []*pb.AgentInstance{{Id: "agent-x", Status: "idle"}}}, nil
}

func (c *captureTaskClient) GetAgentInstance(context.Context, *pb.GetAgentInstanceRequest, ...grpc.CallOption) (*pb.GetAgentInstanceResponse, error) {
	return &pb.GetAgentInstanceResponse{Instance: &pb.AgentInstance{Id: "agent-x", Status: "idle"}}, nil
}

func (c *captureTaskClient) SendAgentMessage(_ context.Context, req *pb.SendAgentMessageRequest, _ ...grpc.CallOption) (*pb.SendAgentMessageResponse, error) {
	cp := *req
	c.sendMsgReq = &cp
	return &pb.SendAgentMessageResponse{Ok: true, PendingMessages: 1}, nil
}

func (c *captureTaskClient) InterruptAgentInstance(_ context.Context, req *pb.InterruptAgentInstanceRequest, _ ...grpc.CallOption) (*pb.InterruptAgentInstanceResponse, error) {
	cp := *req
	c.interruptReq = &cp
	return &pb.InterruptAgentInstanceResponse{Ok: true}, nil
}

func (c *captureTaskClient) WaitAgentInstance(_ context.Context, req *pb.WaitAgentInstanceRequest, _ ...grpc.CallOption) (*pb.WaitAgentInstanceResponse, error) {
	cp := *req
	c.waitReq = &cp
	if c.waitResp != nil {
		return c.waitResp, nil
	}
	return &pb.WaitAgentInstanceResponse{Activity: false, State: "running"}, nil
}

func (c *captureTaskClient) DescribeAgentInstance(context.Context, *pb.DescribeAgentInstanceRequest, ...grpc.CallOption) (*pb.DescribeAgentInstanceResponse, error) {
	return &pb.DescribeAgentInstanceResponse{ChildSessionState: "sleeping", PendingMessages: 0}, nil
}

func (c *captureTaskClient) CleanupAgentInstance(_ context.Context, req *pb.CleanupAgentInstanceRequest, _ ...grpc.CallOption) (*pb.CleanupAgentInstanceResponse, error) {
	c.lastCleanupID = req.Id
	return &pb.CleanupAgentInstanceResponse{Ok: true}, nil
}

func runSubmit(t *testing.T, args ...string) task.Spec {
	t.Helper()
	specJSON := runSubmitJSON(t, args...)
	var spec task.Spec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		t.Fatalf("decode captured spec: %v", err)
	}
	return spec
}

func runSubmitJSON(t *testing.T, args ...string) []byte {
	t.Helper()
	cap := &captureTaskClient{}
	orig := newTaskClient
	newTaskClient = func() (pb.AmuxTaskClient, func() error, error) {
		return cap, func() error { return nil }, nil
	}
	t.Cleanup(func() { newTaskClient = orig })

	cmd := newTaskSubmitCmd()
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("submit %v: %v", args, err)
	}
	return cap.specJSON
}

func TestSubmitAcceptsAndDropsLegacyTrustedModeAck(t *testing.T) {
	specJSON := runSubmitJSON(t, "do something", "--agent", "claude", "--trusted-mode-ack=false")
	if bytes.Contains(specJSON, []byte("trusted_mode_ack")) {
		t.Fatalf("submitted spec retained legacy trusted_mode_ack: %s", specJSON)
	}
}

// TestSubmitDoesNotPrefillPolicyFlags: when the user does NOT pass
// --isolation/--permission/--workspace, the CLI must leave those Spec fields
// EMPTY so the control plane can apply Agent Profile defaults (and the runner
// its own fallback). Pre-filling them with cobra defaults silently masks profile
// defaults.
func TestSubmitDoesNotPrefillPolicyFlags(t *testing.T) {
	spec := runSubmit(t, "do something", "--agent", "claude")
	if spec.Isolation != "" {
		t.Errorf("isolation pre-filled to %q; want empty so defaults apply", spec.Isolation)
	}
	if spec.PermissionPolicy != "" {
		t.Errorf("permission pre-filled to %q; want empty so defaults apply", spec.PermissionPolicy)
	}
	if spec.Workspace.Policy != "" {
		t.Errorf("workspace pre-filled to %q; want empty so defaults apply", spec.Workspace.Policy)
	}
}

// TestSubmitExplicitFlagsWin: an explicitly passed flag is carried on the Spec
// and (per the model) wins over any definition default.
func TestSubmitExplicitFlagsWin(t *testing.T) {
	spec := runSubmit(t, "do something", "--agent", "claude",
		"--isolation", "process", "--permission", "deny-all", "--workspace", "git-worktree")
	if spec.Isolation != task.IsolationProcess {
		t.Errorf("isolation = %q, want process", spec.Isolation)
	}
	if spec.PermissionPolicy != "deny-all" {
		t.Errorf("permission = %q, want deny-all", spec.PermissionPolicy)
	}
	if spec.Workspace.Policy != task.WorkspaceGitWorktree {
		t.Errorf("workspace = %q, want git-worktree", spec.Workspace.Policy)
	}
}

func TestTaskSubmitJSONUsesVersionedEnvelope(t *testing.T) {
	cap := &captureTaskClient{}
	original := newTaskClient
	newTaskClient = func() (pb.AmuxTaskClient, func() error, error) {
		return cap, func() error { return nil }, nil
	}
	t.Cleanup(func() { newTaskClient = original })

	cmd := newJSONCommandTestRoot(newTaskSubmitCmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"submit", "do something", "--agent", "codex", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task submit --json: %v", err)
	}
	var got struct {
		Schema string `json:"schema"`
		Task   struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if got.Schema != "amux.task.submit.v1" || got.Task.ID != "t1" || got.Task.Status != "pending" {
		t.Fatalf("task submit JSON = %#v", got)
	}
}

func TestStreamTaskJSONLinesEmitsOneDocumentPerEvent(t *testing.T) {
	events := []task.TaskEvent{
		{TurnID: "turn-accepted", Seq: 1, Kind: task.TaskEventStatus, Status: task.StatusRunning},
		{Seq: 2, Kind: task.TaskEventOutput, Output: []byte("hello")},
		{Seq: 3, Kind: task.TaskEventPermission, Permission: &task.PermissionRequest{PermissionId: "perm-1", Tool: "Bash"}},
		{Seq: 4, Kind: task.TaskEventUsage, Usage: json.RawMessage(`{"cumulative_input_tokens":343000,"cumulative_cached_input_tokens":300000,"cumulative_output_tokens":12000}`)},
	}
	client := &captureTaskClient{}
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		client.streamEvents = append(client.streamEvents, &pb.SubtaskEvent{EventJson: data})
	}

	var out bytes.Buffer
	if err := streamTask(context.Background(), client, "t1", &out, true, true); err != nil {
		t.Fatalf("streamTask JSON Lines: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(events) {
		t.Fatalf("JSON Lines count = %d, output=%q", len(lines), out.String())
	}
	for index, line := range lines {
		var got struct {
			Schema string         `json:"schema"`
			Event  task.TaskEvent `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is invalid JSON %q: %v", index, line, err)
		}
		if got.Schema != "amux.task.event.v1" || got.Event.Seq != uint64(index+1) {
			t.Fatalf("line %d = %#v", index, got)
		}
		if index == 0 && got.Event.TurnID != "turn-accepted" {
			t.Fatalf("line %d turn_id = %q, want turn-accepted; output=%s", index, got.Event.TurnID, line)
		}
		if !bytes.Equal(got.Event.Usage, events[index].Usage) {
			t.Fatalf("usage JSON lost: %s", line)
		}
		if strings.Contains(line, "PermissionId") {
			t.Fatalf("line %d contains non-snake-case permission field: %s", index, line)
		}
	}
}

func withTaskClient(t *testing.T) *captureTaskClient {
	t.Helper()
	cap := &captureTaskClient{streamStarted: make(chan struct{})}
	orig := newTaskClient
	newTaskClient = func() (pb.AmuxTaskClient, func() error, error) {
		return cap, func() error { return nil }, nil
	}
	t.Cleanup(func() { newTaskClient = orig })
	return cap
}

func TestTaskCanonicalCommandSurface(t *testing.T) {
	root := newRootCmd()
	tests := []struct {
		path        []string
		name        string
		validArgs   []string
		invalidArgs []string
	}{
		{path: []string{"task", "list"}, name: "list", validArgs: nil, invalidArgs: []string{"extra"}},
		{path: []string{"task", "show"}, name: "show", validArgs: []string{"task-1"}, invalidArgs: nil},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path, " "), func(t *testing.T) {
			found, _, err := root.Find(tt.path)
			if err != nil || found.Name() != tt.name {
				t.Fatalf("find %v: found=%v err=%v", tt.path, found, err)
			}
			if len(found.Aliases) != 0 {
				t.Fatalf("%v aliases = %v, want a distinct command", tt.path, found.Aliases)
			}
			if err := found.Args(found, tt.validArgs); err != nil {
				t.Fatalf("%v rejected valid args %v: %v", tt.path, tt.validArgs, err)
			}
			if err := found.Args(found, tt.invalidArgs); err == nil {
				t.Fatalf("%v accepted invalid args %v", tt.path, tt.invalidArgs)
			}
		})
	}

	var help bytes.Buffer
	root.SetOut(&help)
	root.SetErr(&help)
	root.SetArgs([]string{"task", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("task --help: %v", err)
	}
	for _, want := range []string{"  list", "  show"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("task help missing %q:\n%s", want, help.String())
		}
	}
	for _, hidden := range []string{"  ls", "  get"} {
		if strings.Contains(help.String(), hidden) {
			t.Fatalf("task help exposed hidden command %q:\n%s", hidden, help.String())
		}
	}

	root = newRootCmd()
	var completion bytes.Buffer
	root.SetOut(&completion)
	root.SetErr(&completion)
	root.SetArgs([]string{"__complete", "task", ""})
	if err := root.Execute(); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	for _, want := range []string{"list\t", "show\t"} {
		if !strings.Contains(completion.String(), want) {
			t.Fatalf("task completion missing %q:\n%s", want, completion.String())
		}
	}
	for _, hidden := range []string{"ls\t", "get\t"} {
		if strings.Contains(completion.String(), hidden) {
			t.Fatalf("task completion exposed hidden command %q:\n%s", hidden, completion.String())
		}
	}
	for _, path := range [][]string{{"task", "ls"}, {"task", "get"}} {
		found, _, err := root.Find(path)
		if err == nil && found != nil && found.Name() == path[len(path)-1] {
			t.Fatalf("%v should not be registered: found=%v", path, found)
		}
	}
}

func TestTaskCanonicalAndCompatExecution(t *testing.T) {
	for _, name := range []string{"list"} {
		t.Run(name+" shares list behavior", func(t *testing.T) {
			cap := withTaskClient(t)
			cmd := newTaskCmd()
			cmd.SetArgs([]string{name})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("task %s: %v", name, err)
			}
			if !cap.listed {
				t.Fatal("ListSubtasks was not called")
			}
		})
	}
}

func TestTaskPruneBuildsRequest(t *testing.T) {
	cap := withTaskClient(t)
	cmd := newTaskCmd()
	cmd.SetArgs([]string{"prune", "--dry-run", "--older-than", "7d", "--status", "succeeded,failed"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task prune: %v", err)
	}
	if cap.pruneReq == nil {
		t.Fatal("PruneSubtasks was not called")
	}
	if !cap.pruneReq.DryRun {
		t.Fatal("dry_run not set")
	}
	if cap.pruneReq.OlderThanSeconds != int64((7*24*time.Hour)/time.Second) {
		t.Fatalf("older_than_seconds = %d", cap.pruneReq.OlderThanSeconds)
	}
	want := []string{"succeeded", "failed"}
	if len(cap.pruneReq.Statuses) != len(want) {
		t.Fatalf("statuses = %#v", cap.pruneReq.Statuses)
	}
	for i := range want {
		if cap.pruneReq.Statuses[i] != want[i] {
			t.Fatalf("statuses = %#v", cap.pruneReq.Statuses)
		}
	}
}

func TestTaskPruneJSONUsesEmptyArrays(t *testing.T) {
	cap := withTaskClient(t)
	cap.pruneResp = &pb.PruneSubtasksResponse{}
	cmd := newJSONCommandTestRoot(newTaskCmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task", "prune", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task prune --json: %v", err)
	}
	var got struct {
		Result struct {
			Tasks   []any `json:"tasks"`
			Skipped []any `json:"skipped"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if got.Result.Tasks == nil || got.Result.Skipped == nil {
		t.Fatalf("task prune arrays = tasks:%#v skipped:%#v", got.Result.Tasks, got.Result.Skipped)
	}
}

func TestTaskPruneRejectsNonTerminalStatus(t *testing.T) {
	cmd := newTaskCmd()
	cmd.SetArgs([]string{"prune", "--status", "running"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("task prune accepted non-terminal status")
	}
}
