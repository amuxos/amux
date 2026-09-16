package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestOwnerGuardRejectsDirectMutations verifies that mutating session RPCs are
// rejected for task-owned sessions but allowed for ordinary sessions.
func TestOwnerGuardRejectsDirectMutations(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	s := &server{mgr: mgr, amuxDir: dir}

	owned, err := mgr.CreateOwned("subtask-1", "claude", "task-1", "", filepath.Join(dir, "w"))
	if err != nil {
		t.Fatalf("create owned: %v", err)
	}
	if owned.OwnerTaskId != "task-1" {
		t.Fatalf("owner not set: %+v", owned)
	}

	ctx := context.Background()
	mutations := map[string]func() error{
		"Submit":    func() error { _, e := s.Submit(ctx, &pb.SubmitRequest{Name: "subtask-1", Text: "x"}); return e },
		"Kill":      func() error { _, e := s.Kill(ctx, &pb.KillRequest{Name: "subtask-1"}); return e },
		"Interrupt": func() error { _, e := s.Interrupt(ctx, &pb.InterruptRequest{Name: "subtask-1"}); return e },
		"Restart":   func() error { _, e := s.Restart(ctx, &pb.RestartRequest{Name: "subtask-1"}); return e },
		"RespondPermission": func() error {
			_, e := s.RespondPermission(ctx, &pb.RespondPermissionRequest{Name: "subtask-1"})
			return e
		},
		"QueueClear":  func() error { _, e := s.QueueClear(ctx, &pb.QueueClearRequest{Name: "subtask-1"}); return e },
		"QueueCancel": func() error { _, e := s.QueueCancel(ctx, &pb.QueueCancelRequest{Name: "subtask-1", Id: "1"}); return e },
	}
	for name, call := range mutations {
		err := call()
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s on owned session: want PermissionDenied, got %v", name, err)
		}
	}

	// A normal (unowned) session is not blocked by the guard.
	if _, err := mgr.Create("normal", "claude", filepath.Join(dir, "w2")); err != nil {
		t.Fatalf("create normal: %v", err)
	}
	if _, err := s.Submit(ctx, &pb.SubmitRequest{Name: "normal", Text: "x"}); status.Code(err) == codes.PermissionDenied {
		t.Fatalf("unowned session must not be guarded: %v", err)
	}
	// Read-only List is never guarded and still sees both sessions.
	list, err := s.List(ctx, &pb.ListRequest{})
	if err != nil || len(list.Sessions) != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
}

// TestOwnerGuardRejectsAgentInstanceOwned verifies that mutating RPCs are
// rejected for sessions owned by a persistent agent instance, directing the
// caller to the agent-level tools instead.
func TestOwnerGuardRejectsAgentInstanceOwned(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	s := &server{mgr: mgr, amuxDir: dir}

	if _, err := mgr.CreateOwned("instance-i1", "claude", "task-1", "i1", filepath.Join(dir, "w")); err != nil {
		t.Fatalf("create instance owned: %v", err)
	}

	ctx := context.Background()
	// Submit and Restart are representative mutating RPCs; both must be rejected.
	if _, err := s.Submit(ctx, &pb.SubmitRequest{Name: "instance-i1", Text: "x"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Submit on instance-owned: want PermissionDenied, got %v", err)
	}
	if _, err := s.Restart(ctx, &pb.RestartRequest{Name: "instance-i1"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Restart on instance-owned: want PermissionDenied, got %v", err)
	}
}

func TestDescribeAgentInstanceIncludesArchivedChild(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.CreateOwned("instance-inst-archived", "claude", "task-failed", "inst-archived", t.TempDir())
	if err != nil {
		t.Fatalf("CreateOwned: %v", err)
	}
	if err := mgr.SetState(sess, session.StateArchived); err != nil {
		t.Fatalf("SetState archived: %v", err)
	}
	srv := &server{mgr: mgr, amuxDir: t.TempDir()}
	taskSrv := &taskServer{ctrl: newTaskController(srv, "t-1")}

	resp, err := taskSrv.DescribeAgentInstance(context.Background(), &pb.DescribeAgentInstanceRequest{Id: "inst-archived"})
	if err != nil {
		t.Fatalf("DescribeAgentInstance: %v", err)
	}
	if resp.ChildSessionId != sess.ID || resp.ChildSessionState != string(session.StateArchived) {
		t.Fatalf("describe = %+v, want archived child %s", resp, sess.ID)
	}
}

// ---- task server delegation ----

type fakeTaskChild struct {
	id, name string
	mu       sync.Mutex
	subs     []chan agentstream.Event
	onSubmit func(c *fakeTaskChild)
}

func (c *fakeTaskChild) ID() string   { return c.id }
func (c *fakeTaskChild) Name() string { return c.name }
func (c *fakeTaskChild) Follow(_ context.Context) <-chan agentstream.Event {
	ch := make(chan agentstream.Event, 64)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	return ch
}
func (c *fakeTaskChild) emit(ev agentstream.Event) {
	c.mu.Lock()
	if ev.Turn == nil {
		ev.Turn = &agentstream.TurnRef{ID: "fake-turn", Seq: 1}
	}
	subs := append([]chan agentstream.Event(nil), c.subs...)
	c.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
func (c *fakeTaskChild) SubmitAsync(_, _ string) error {
	c.mu.Lock()
	cb := c.onSubmit
	c.mu.Unlock()
	if cb != nil {
		go cb(c)
	}
	return nil
}
func (c *fakeTaskChild) SubmitTracked(source, text string) (task.TurnSubmission, error) {
	return task.TurnSubmission{TurnID: "fake-turn"}, c.SubmitAsync(source, text)
}
func (c *fakeTaskChild) RespondPermission(string, string) error        { return nil }
func (c *fakeTaskChild) RespondPermissionMulti(string, []string) error { return nil }
func (c *fakeTaskChild) Interrupt() error                              { return nil }
func (c *fakeTaskChild) Kill() error                                   { return nil }
func (c *fakeTaskChild) AgentSessionID() string                        { return "" }
func (c *fakeTaskChild) SetSleeping(string) error                      { return nil }

type fakeTaskController struct{}

func (fakeTaskController) TargetID() string                                { return "local" }
func (fakeTaskController) TeardownChild(_ context.Context, _ string) error { return nil }
func (fakeTaskController) CreateChild(_ context.Context, spec task.ChildSpec) (task.ChildSession, error) {
	c := &fakeTaskChild{id: "child-" + spec.TaskID, name: spec.TaskID}
	c.onSubmit = func(c *fakeTaskChild) {
		c.emit(agentstream.Event{Body: agentstream.AssistantDelta{Text: `{"amux_task_result":{"summary":"done"}}`}})
		c.emit(agentstream.Event{Body: agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}}})
	}
	return c, nil
}

// TestTaskServerSubmitGetStream exercises the AmuxTask service end to end through
// its handlers: submit -> stream -> get, asserting JSON round-trips and that the
// task reaches succeeded.
func TestTaskServerSubmitGetStream(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	runner, err := task.NewRunner(task.Options{
		AmuxDir:        dir,
		Store:          task.NewSQLStore(db),
		Control:        fakeTaskController{},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	ts := newTaskServer(runner, nil)

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "do it"})
	sub, err := ts.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var submitted task.TaskRun
	if err := json.Unmarshal(sub.TaskJson, &submitted); err != nil {
		t.Fatalf("decode submitted: %v", err)
	}
	if submitted.ID == "" {
		t.Fatal("no task id")
	}

	// Stream until the terminal status arrives.
	stream := &fakeStream{ctx: context.Background(), events: make(chan *pb.SubtaskEvent, 64)}
	done := make(chan error, 1)
	go func() { done <- ts.StreamSubtask(&pb.StreamSubtaskRequest{Id: submitted.ID}, stream) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not end")
	}

	get, err := ts.GetSubtask(context.Background(), &pb.GetSubtaskRequest{Id: submitted.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var final task.TaskRun
	if err := json.Unmarshal(get.TaskJson, &final); err != nil {
		t.Fatalf("decode final: %v", err)
	}
	if final.Status != task.StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	if final.Result == nil || final.Result.Summary != "done" {
		t.Fatalf("result = %+v", final.Result)
	}

	// GetSubtask on an unknown id is NotFound.
	if _, err := ts.GetSubtask(context.Background(), &pb.GetSubtaskRequest{Id: "nope"}); status.Code(err) != codes.NotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}

// fakeStream is a minimal pb.AmuxTask_StreamSubtaskServer for tests.
type fakeStream struct {
	grpc.ServerStream
	ctx    context.Context
	events chan *pb.SubtaskEvent
}

func (s *fakeStream) Context() context.Context { return s.ctx }
func (s *fakeStream) Send(ev *pb.SubtaskEvent) error {
	select {
	case s.events <- ev:
	default:
	}
	return nil
}

// TestTaskServerStreamFromSeq verifies the gRPC stream handler honours FromSeq:
// a resumed stream returns only events with seq greater than the cursor.
func TestTaskServerStreamFromSeq(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	runner, err := task.NewRunner(task.Options{
		AmuxDir:        dir,
		Store:          task.NewSQLStore(db),
		Control:        fakeTaskController{},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	ts := newTaskServer(runner, nil)

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "do it"})
	sub, err := ts.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var submitted task.TaskRun
	_ = json.Unmarshal(sub.TaskJson, &submitted)

	// Drain from seq 0 to completion, learning the max seq.
	full := &fakeStream{ctx: context.Background(), events: make(chan *pb.SubtaskEvent, 256)}
	done := make(chan error, 1)
	go func() { done <- ts.StreamSubtask(&pb.StreamSubtaskRequest{Id: submitted.ID}, full) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not end")
	}
	var maxSeq uint64
	close(full.events)
	for ev := range full.events {
		var te task.TaskEvent
		if json.Unmarshal(ev.EventJson, &te) == nil && te.Seq > maxSeq {
			maxSeq = te.Seq
		}
	}
	if maxSeq < 2 {
		t.Fatalf("expected several events, maxSeq=%d", maxSeq)
	}

	// Resume from maxSeq-1: every delivered event must have seq > maxSeq-1.
	resumed := &fakeStream{ctx: context.Background(), events: make(chan *pb.SubtaskEvent, 256)}
	rdone := make(chan error, 1)
	go func() {
		rdone <- ts.StreamSubtask(&pb.StreamSubtaskRequest{Id: submitted.ID, FromSeq: maxSeq - 1}, resumed)
	}()
	select {
	case <-rdone:
	case <-time.After(3 * time.Second):
		t.Fatal("resumed stream did not end")
	}
	close(resumed.events)
	n := 0
	for ev := range resumed.events {
		var te task.TaskEvent
		if json.Unmarshal(ev.EventJson, &te) == nil {
			n++
			if te.Seq <= maxSeq-1 {
				t.Fatalf("resumed stream returned stale seq %d", te.Seq)
			}
		}
	}
	if n == 0 {
		t.Fatal("resumed stream returned no events")
	}
}

// TestTaskServerResolvesLocally: in local (no-hub) mode the taskserver resolves a
// profile ref before runner.Submit, so the persisted TaskRun records the base
// agent type.
func TestTaskServerResolvesLocally(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	runner, err := task.NewRunner(task.Options{
		AmuxDir:        dir,
		Store:          task.NewSQLStore(db),
		Control:        fakeTaskController{},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	ts := newTaskServer(runner, nil)
	ts.resolveProfile = func(spec *task.Spec) error {
		if spec.Agent == "code-reviewer" {
			spec.Agent = "claude"
			spec.ResolvedProfile = &pb.AgentProfileSpec{Name: "code-reviewer", Type: "claude"}
		}
		return nil
	}

	specJSON, _ := json.Marshal(task.Spec{Agent: "code-reviewer", Task: "do it"})
	resp, err := ts.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var run task.TaskRun
	if err := json.Unmarshal(resp.TaskJson, &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.AgentType != "claude" {
		t.Fatalf("run.AgentType = %q, want claude (resolved)", run.AgentType)
	}

	// Drain to a terminal status so the runner finishes and tears down its
	// sandbox before the test's TempDir cleanup runs (otherwise they race).
	deadline := time.After(3 * time.Second)
	for {
		get, gerr := ts.GetSubtask(context.Background(), &pb.GetSubtaskRequest{Id: run.ID})
		if gerr != nil {
			t.Fatalf("get: %v", gerr)
		}
		var cur task.TaskRun
		if err := json.Unmarshal(get.TaskJson, &cur); err != nil {
			t.Fatalf("decode cur: %v", err)
		}
		if cur.Status == task.StatusSucceeded || cur.Status == task.StatusFailed || cur.Status == task.StatusCancelled {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("task did not reach terminal status, last = %s", cur.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
