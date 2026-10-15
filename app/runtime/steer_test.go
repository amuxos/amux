package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// steeringServerProc keeps its turn open and accepts (or fails) steer input.
type steeringServerProc struct {
	serverTestProc
	mu     sync.Mutex
	steers []session.TurnSteerRequest
	err    error
}

func (p *steeringServerProc) SteerTurn(_ context.Context, req session.TurnSteerRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.steers = append(p.steers, req)
	return nil
}

func (p *steeringServerProc) steerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.steers)
}

func waitRuntimeActiveTurn(t *testing.T, sess *session.Session) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if id, _, ok := sess.ActiveTurnAuthorization(); ok {
			return id
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("turn never became active")
	return ""
}

// startSteerableInstance creates a running instance child whose turn stays open.
func startSteerableInstance(t *testing.T, ctrl *taskController, instanceID string, proc *steeringServerProc) *session.Session {
	t.Helper()
	child, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-" + instanceID, AgentType: "claude", OwnerTaskId: "task-" + instanceID, InstanceID: instanceID})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	sess := child.(*childSession).sess
	proc.holdTurnOpen = true
	sess.SetProc(proc)
	if err := sess.SubmitAsync("task", "long work"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	waitRuntimeActiveTurn(t, sess)
	return sess
}

func TestSteerInstanceInjectsIntoRunningTurn(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	proc := &steeringServerProc{}
	startSteerableInstance(t, ctrl, "inst-steer", proc)
	delivery, reason, pending, err := ctrl.SteerInstance(context.Background(), "inst-steer", "", "use dry-run")
	if err != nil || delivery != "steered" || reason != "" || pending != 0 {
		t.Fatalf("SteerInstance = %q %q %d %v, want steered", delivery, reason, pending, err)
	}
	if proc.steerCount() != 1 {
		t.Fatalf("provider steers = %d, want 1", proc.steerCount())
	}
	if text := proc.steers[0].Input.Text(); !strings.Contains(text, "use dry-run") || !strings.Contains(text, "前台协调 Agent") {
		t.Fatalf("steer text = %q, want coordinator-framed correction", text)
	}
	if got := ctrl.mailbox.pending("inst-steer"); got != 0 {
		t.Fatalf("mailbox pending = %d, want 0", got)
	}
}

func TestSteerInstanceWithUnknownOutcomeIsNotQueued(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	proc := &steeringServerProc{err: fmt.Errorf("%w: context deadline exceeded", session.ErrSteerOutcomeUnknown)}
	startSteerableInstance(t, ctrl, "inst-unknown", proc)
	_, _, _, err := ctrl.SteerInstance(context.Background(), "inst-unknown", "", "use dry-run")
	if status.Code(err) != codes.Unknown {
		t.Fatalf("err = %v, want codes.Unknown", err)
	}
	if got := ctrl.mailbox.pending("inst-unknown"); got != 0 {
		t.Fatalf("mailbox pending = %d, want 0 (the turn may already have it)", got)
	}
}

func TestSteerInstanceMailboxFallbackIsDrainedByFollowupOnRunningInstance(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	writes := make(chan string, 8)
	ctrl.srv.startSessionHook = func(_ adapter.Adapter, sess *session.Session, _, _ []string) error {
		sess.SetProc(&serverTestProc{onWriteData: func(data []byte) { writes <- string(data) }})
		return ctrl.srv.mgr.SetRunning(sess)
	}
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-idle"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	// Running but between turns: nothing to steer, so the correction waits in
	// the mailbox.
	delivery, reason, pending, err := ctrl.SteerInstance(context.Background(), "inst-idle", "", "correction")
	if err != nil || delivery != "mailbox" || reason == "" || pending != 1 {
		t.Fatalf("SteerInstance = %q %q %d %v, want mailbox fallback", delivery, reason, pending, err)
	}
	// instance_followup reuses the running child; the mailbox goes first.
	c2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-b", AgentType: "claude", OwnerTaskId: "task-b", InstanceID: "inst-idle"})
	if err != nil {
		t.Fatalf("CreateChild followup: %v", err)
	}
	if c1.ID() != c2.ID() {
		t.Fatalf("followup should reuse the running child: %s vs %s", c1.ID(), c2.ID())
	}
	if got := ctrl.mailbox.pending("inst-idle"); got != 0 {
		t.Fatalf("mailbox pending after followup = %d, want 0", got)
	}
	select {
	case got := <-writes:
		if got != "correction" {
			t.Fatalf("first provider write = %q, want the mailbox correction", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox correction was not delivered")
	}
}

func TestSteerInstanceRejectsMissingArguments(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	for _, tc := range []struct{ id, text string }{{"", "x"}, {"inst", ""}} {
		if _, _, _, err := ctrl.SteerInstance(context.Background(), tc.id, "", tc.text); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("SteerInstance(%q, %q) err = %v, want InvalidArgument", tc.id, tc.text, err)
		}
	}
}

func TestSubmitSteerWithUnknownOutcomeDoesNotQueue(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "claude", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	proc := &steeringServerProc{serverTestProc: serverTestProc{holdTurnOpen: true}}
	sess.SetProc(proc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("SetRunning: %v", err)
	}
	if err := sess.SubmitAsync("lark", "first"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	turnID := waitRuntimeActiveTurn(t, sess)
	srv := &server{mgr: mgr}

	resp, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "s1", Source: "lark", Text: "改成 Python", Async: true, Steer: true})
	if err != nil || resp.GetDelivery() != "steered" || resp.GetTurnId() != turnID {
		t.Fatalf("Submit = %+v, %v; want steered into %s", resp, err, turnID)
	}

	proc.mu.Lock()
	proc.err = fmt.Errorf("%w: broker disconnected", session.ErrSteerOutcomeUnknown)
	proc.mu.Unlock()
	if _, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "s1", Source: "lark", Text: "再加测试", Async: true, Steer: true}); status.Code(err) != codes.Unknown {
		t.Fatalf("Submit err = %v, want codes.Unknown", err)
	}
	if items := sess.QueueSnapshot(); len(items) != 0 {
		t.Fatalf("queue = %+v, want nothing queued after an unknown steer outcome", items)
	}

	proc.mu.Lock()
	proc.err = fmt.Errorf("%w: no turn in flight", session.ErrSteerUnavailable)
	proc.mu.Unlock()
	resp, err = srv.Submit(context.Background(), &pb.SubmitRequest{Name: "s1", Source: "lark", Text: "再加测试", Async: true, Steer: true})
	if err != nil || resp.GetDelivery() != "queued" {
		t.Fatalf("Submit = %+v, %v; want queued fallback", resp, err)
	}
	proc.turnSink(testSuccessEmission())
}
