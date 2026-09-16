package runtime

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
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

// newTestController builds a taskController backed by a fresh in-memory session
// manager, wiring a startSessionHook that counts launches and flips the session
// to Running. It returns the controller, the manager, and a pointer to the
// launch counter so a test can assert resume/restart behavior.
func newTestController(t *testing.T) (*taskController, *session.Manager, *int) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := session.NewManager(db)
	launches := 0
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, _ []string) error {
			launches++
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	return newTaskController(srv, "t-1"), mgr, &launches
}

func TestInstanceMailboxEnqueueAndDrain(t *testing.T) {
	mb := newInstanceMailbox()
	if got := mb.pending("inst-1"); got != 0 {
		t.Fatalf("initial pending = %d, want 0", got)
	}
	if got := mb.enqueue("inst-1", "src", "hello"); got != 1 {
		t.Fatalf("enqueue pending = %d, want 1", got)
	}
	mb.enqueue("inst-1", "src", "world")
	entries := mb.drain("inst-1")
	if len(entries) != 2 {
		t.Fatalf("drain len = %d, want 2", len(entries))
	}
	if entries[0].Text != "hello" || entries[1].Text != "world" {
		t.Fatalf("drain order = %q %q, want hello then world", entries[0].Text, entries[1].Text)
	}
	if got := mb.pending("inst-1"); got != 0 {
		t.Fatalf("pending after drain = %d, want 0", got)
	}
	// drain of an empty mailbox is a no-op (nil).
	if entries := mb.drain("inst-1"); entries != nil {
		t.Fatalf("drain empty = %v, want nil", entries)
	}
}

func TestSendInstanceMessageEnqueuesWithoutWaking(t *testing.T) {
	ctrl, mgr, launches := newTestController(t)
	// Create a sleeping instance child (no live process to wake).
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	before := *launches

	pending, err := ctrl.SendInstanceMessage(context.Background(), "inst-1", "instance_send", "queued note")
	if err != nil {
		t.Fatalf("SendInstanceMessage: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
	if *launches != before {
		t.Fatalf("instance_send woke the agent: launches %d -> %d, want no wake", before, *launches)
	}
	if got := ctrl.mailbox.pending("inst-1"); got != 1 {
		t.Fatalf("mailbox pending = %d, want 1", got)
	}
}

func TestCreateChildDrainsMailboxOnWake(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)

	// First task: create + sleep the instance child.
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 1: %v", err)
	}
	if err := mgr.RecordAgentSessionID(c1.(*childSession).sess, "prov-1"); err != nil {
		t.Fatalf("record agent session id: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}

	// Queue two messages while the agent sleeps.
	if _, err := ctrl.SendInstanceMessage(context.Background(), "inst-1", "instance_send", "msg-1"); err != nil {
		t.Fatalf("SendInstanceMessage 1: %v", err)
	}
	if _, err := ctrl.SendInstanceMessage(context.Background(), "inst-1", "instance_send", "msg-2"); err != nil {
		t.Fatalf("SendInstanceMessage 2: %v", err)
	}
	if got := ctrl.mailbox.pending("inst-1"); got != 2 {
		t.Fatalf("mailbox pending before wake = %d, want 2", got)
	}

	// Follow-up task wakes the instance: the mailbox must be drained.
	c2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-b", AgentType: "claude", OwnerTaskId: "task-b", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 2: %v", err)
	}
	if c2.ID() != c1.ID() {
		t.Fatalf("wake should reuse session: c1=%s c2=%s", c1.ID(), c2.ID())
	}
	if got := ctrl.mailbox.pending("inst-1"); got != 0 {
		t.Fatalf("mailbox pending after wake = %d, want 0 (drained)", got)
	}
}

func TestDescribeInstanceReportsStateAndMailbox(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	ctrl.SendInstanceMessage(context.Background(), "inst-1", "instance_send", "hi")

	resp, err := ctrl.DescribeInstance(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("DescribeInstance: %v", err)
	}
	if resp.ChildSessionState != string(session.StateSleeping) {
		t.Fatalf("child state = %q, want sleeping", resp.ChildSessionState)
	}
	if resp.ChildSessionName != "instance-inst-1" {
		t.Fatalf("child name = %q, want instance-inst-1", resp.ChildSessionName)
	}
	if resp.PendingMessages != 1 {
		t.Fatalf("pending messages = %d, want 1", resp.PendingMessages)
	}
}

func TestDescribeInstanceMissingInstance(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	resp, err := ctrl.DescribeInstance(context.Background(), "no-such-instance")
	if err != nil {
		t.Fatalf("DescribeInstance missing instance: %v", err)
	}
	if resp.ChildSessionId != "" || resp.ChildSessionState != "" {
		t.Fatalf("describe missing = %+v, want empty child fields", resp)
	}
	if resp.PendingMessages != 0 {
		t.Fatalf("pending = %d, want 0", resp.PendingMessages)
	}
}

func TestInterruptInstanceRejectsSleeping(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	err = ctrl.InterruptInstance(context.Background(), "inst-1")
	if err == nil {
		t.Fatal("InterruptInstance on sleeping instance should fail")
	}
}

func TestInterruptInstanceMissingInstance(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	if err := ctrl.InterruptInstance(context.Background(), "no-such"); err == nil {
		t.Fatal("InterruptInstance on missing instance should fail")
	}
}

func TestWaitInstanceBlocksWhileRunningUntilSleeping(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	type result struct {
		activity bool
		state    string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		activity, state, err := ctrl.WaitInstance(context.Background(), "inst-1", 5*time.Second)
		done <- result{activity: activity, state: state, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("WaitInstance returned while child was still running: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("WaitInstance: %v", got.err)
		}
		if !got.activity || got.state != string(session.StateSleeping) {
			t.Fatalf("WaitInstance = activity=%v state=%q, want true/sleeping", got.activity, got.state)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitInstance did not return after child became sleeping")
	}
}

func TestWaitInstanceReturnsImmediatelyWhenSleeping(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	start := time.Now()
	activity, state, err := ctrl.WaitInstance(context.Background(), "inst-1", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitInstance: %v", err)
	}
	if activity {
		t.Fatal("activity = true for already sleeping instance, want false")
	}
	if state != string(session.StateSleeping) {
		t.Fatalf("state = %q, want sleeping", state)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("WaitInstance blocked %v for an already sleeping instance", elapsed)
	}
}

func TestWaitInstanceDoesNotReturnWhileSleepingInstanceHasPendingTask(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	taskDone := make(chan struct{})
	var taskActive atomic.Bool
	taskActive.Store(true)
	ctrl.watchActiveInstance = func(instanceID string) (<-chan struct{}, bool) {
		if instanceID != "inst-1" || !taskActive.Load() {
			return nil, false
		}
		return taskDone, true
	}
	type result struct {
		activity bool
		state    string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		activity, state, err := ctrl.WaitInstance(context.Background(), "inst-1", 5*time.Second)
		done <- result{activity: activity, state: state, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("WaitInstance returned while a follow-up task was pending: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	taskActive.Store(false)
	close(taskDone)
	select {
	case got := <-done:
		if got.err != nil || !got.activity || got.state != string(session.StateSleeping) {
			t.Fatalf("WaitInstance = %+v, want observed completion in sleeping state", got)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitInstance did not return after pending task completed")
	}
}

func TestWaitInstanceWaitsForPendingTaskBeforeChildExists(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	taskDone := make(chan struct{})
	var taskActive atomic.Bool
	taskActive.Store(true)
	ctrl.watchActiveInstance = func(instanceID string) (<-chan struct{}, bool) {
		if instanceID != "inst-1" || !taskActive.Load() {
			return nil, false
		}
		return taskDone, true
	}
	type result struct {
		activity bool
		state    string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		activity, state, err := ctrl.WaitInstance(context.Background(), "inst-1", 5*time.Second)
		done <- result{activity: activity, state: state, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("WaitInstance returned before the pending task created its child: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := mgr.SetSleeping(c1.(*childSession).sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	taskActive.Store(false)
	close(taskDone)
	select {
	case got := <-done:
		if got.err != nil || !got.activity || got.state != string(session.StateSleeping) {
			t.Fatalf("WaitInstance = %+v, want observed completion in sleeping state", got)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitInstance did not return after pending task completed")
	}
}

func TestWaitInstancePendingTaskBeforeChildReturnsRunning(t *testing.T) {
	newPendingController := func(t *testing.T) *taskController {
		t.Helper()
		ctrl, _, _ := newTestController(t)
		taskDone := make(chan struct{})
		ctrl.watchActiveInstance = func(instanceID string) (<-chan struct{}, bool) {
			if instanceID != "inst-1" {
				return nil, false
			}
			return taskDone, true
		}
		return ctrl
	}

	t.Run("timeout", func(t *testing.T) {
		ctrl := newPendingController(t)
		activity, state, err := ctrl.WaitInstance(context.Background(), "inst-1", 25*time.Millisecond)
		if err != nil {
			t.Fatalf("WaitInstance: %v", err)
		}
		if activity || state != string(session.StateRunning) {
			t.Fatalf("WaitInstance = activity=%v state=%q, want false/running", activity, state)
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		ctrl := newPendingController(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		activity, state, err := ctrl.WaitInstance(ctx, "inst-1", 5*time.Second)
		if status.Code(err) != codes.Canceled {
			t.Fatalf("WaitInstance error = %v, want canceled", err)
		}
		if activity || state != string(session.StateRunning) {
			t.Fatalf("WaitInstance = activity=%v state=%q, want false/running", activity, state)
		}
	})
}

func TestWaitInstanceIgnoresOutputUntilSleeping(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	sess := c1.(*childSession).sess
	done := make(chan error, 1)
	go func() {
		_, _, err := ctrl.WaitInstance(context.Background(), "inst-1", 5*time.Second)
		done <- err
	}()
	time.Sleep(25 * time.Millisecond)
	sess.RecordReplayEvent(session.Event{Kind: session.EventOutput, Data: []byte("partial output"), Source: "agent"})
	select {
	case err := <-done:
		t.Fatalf("WaitInstance returned on ordinary output: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := mgr.SetSleeping(sess, "task-terminal:success"); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitInstance: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitInstance did not return after child became sleeping")
	}
}

func TestWaitInstanceTimesOutWhileRunning(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	if _, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"}); err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	start := time.Now()
	activity, state, err := ctrl.WaitInstance(context.Background(), "inst-1", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitInstance: %v", err)
	}
	if activity || state != string(session.StateRunning) {
		t.Fatalf("WaitInstance = activity=%v state=%q, want false/running", activity, state)
	}
	if elapsed := time.Since(start); elapsed < 75*time.Millisecond {
		t.Fatalf("WaitInstance returned before timeout: %v", elapsed)
	}
}

func TestWaitInstanceReturnsImmediatelyWhenArchived(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := mgr.SetArchived(c1.(*childSession).sess, "test"); err != nil {
		t.Fatalf("set archived: %v", err)
	}
	activity, state, err := ctrl.WaitInstance(context.Background(), "inst-1", 5*time.Second)
	if err != nil {
		t.Fatalf("WaitInstance: %v", err)
	}
	if activity || state != string(session.StateArchived) {
		t.Fatalf("WaitInstance = activity=%v state=%q, want false/archived", activity, state)
	}
}

func TestWaitInstancePropagatesCallerCancellation(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	if _, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"}); err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, state, err := ctrl.WaitInstance(ctx, "inst-1", 5*time.Second)
	if status.Code(err) != codes.Canceled {
		t.Fatalf("WaitInstance error = %v, want canceled", err)
	}
	if state != string(session.StateRunning) {
		t.Fatalf("state = %q, want running", state)
	}
}

func TestListInstanceSummariesSkipsEphemeral(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	// A persistent instance child.
	if _, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"}); err != nil {
		t.Fatalf("CreateChild persistent: %v", err)
	}
	// An ephemeral child (no InstanceID).
	if _, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-b", AgentType: "claude", OwnerTaskId: "task-b"}); err != nil {
		t.Fatalf("CreateChild ephemeral: %v", err)
	}
	summaries := ctrl.ListInstanceSummaries()
	if len(summaries) != 1 {
		t.Fatalf("instance summaries = %d, want 1 (ephemeral excluded)", len(summaries))
	}
	if summaries[0].InstanceID != "inst-1" {
		t.Fatalf("instance id = %q, want inst-1", summaries[0].InstanceID)
	}
}

// TestCleanupInstanceArchivesSessionAndRemovesSandbox verifies that
// CleanupInstance stops the instance's child session, archives it, removes the
// instance-keyed sandbox directory, and clears the mailbox (v0.39).
func TestCleanupInstanceArchivesSessionAndRemovesSandbox(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	sess := c1.(*childSession).sess
	// Create the instance-keyed sandbox dir with a marker file.
	sbDir := filepath.Join(task.SandboxesDir(ctrl.srv.amuxDir), "instance-inst-1")
	if err := os.MkdirAll(sbDir, 0o700); err != nil {
		t.Fatalf("mkdir sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "marker"), []byte("durable"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	// Queue a mailbox message that should be dropped on cleanup.
	if _, err := ctrl.SendInstanceMessage(context.Background(), "inst-1", "instance_send", "pending"); err != nil {
		t.Fatalf("SendInstanceMessage: %v", err)
	}
	if got := ctrl.mailbox.pending("inst-1"); got != 1 {
		t.Fatalf("mailbox pending before cleanup = %d, want 1", got)
	}

	if err := ctrl.CleanupInstance(context.Background(), "inst-1"); err != nil {
		t.Fatalf("CleanupInstance: %v", err)
	}

	// Session is archived.
	if got := sess.StateSnapshot(); got != session.StateArchived {
		t.Fatalf("session state = %s, want archived", got)
	}
	// Sandbox directory is gone.
	if _, err := os.Stat(sbDir); !os.IsNotExist(err) {
		t.Fatalf("sandbox dir stat err = %v, want IsNotExist", err)
	}
	// Mailbox is drained.
	if got := ctrl.mailbox.pending("inst-1"); got != 0 {
		t.Fatalf("mailbox pending after cleanup = %d, want 0", got)
	}
}

// TestCleanupInstanceIdempotentOnMissingInstance verifies cleanup is safe when
// the instance has no live session and no sandbox — e.g. a repeat call or a
// target that already reclaimed locally.
func TestCleanupInstanceIdempotentOnMissingInstance(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	if err := ctrl.CleanupInstance(context.Background(), "inst-gone"); err != nil {
		t.Fatalf("CleanupInstance on missing instance should not error: %v", err)
	}
}

// TestCleanupInstanceRejectsEmptyID mirrors the empty-id guard on the other
// instance RPCs.
func TestCleanupInstanceRejectsEmptyID(t *testing.T) {
	ctrl, _, _ := newTestController(t)
	if err := ctrl.CleanupInstance(context.Background(), ""); err == nil {
		t.Fatal("empty agent_instance_id should be rejected")
	}
}

// Some providers acknowledge interrupt with a successful turn terminal. The
// instance operation must establish Task cancellation before reaching them.
type successfulInterruptProc struct{ serverTestProc }

func (p *successfulInterruptProc) Interrupt() error {
	if p.turnSink != nil {
		p.turnSink(testSuccessEmission())
	}
	return nil
}

func TestInstanceInterruptCancelsOwnerTaskAndPreservesSleepingChild(t *testing.T) {
	ctrl, mgr, _ := newTestController(t)
	ctrl.srv.startSessionHook = func(_ adapter.Adapter, sess *session.Session, _ []string, _ []string) error {
		sess.SetProc(&successfulInterruptProc{serverTestProc: serverTestProc{holdTurnOpen: true}})
		if err := mgr.RecordAgentSessionID(sess, "provider-persistent"); err != nil {
			return err
		}
		return mgr.SetRunning(sess)
	}
	runner, err := task.NewRunner(task.Options{AmuxDir: ctrl.srv.amuxDir, Store: task.NewMemStore(), Control: ctrl, DefaultTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	server := newTaskServer(runner, ctrl)
	run, err := runner.Submit(task.Spec{Agent: "claude", Task: "long work", Mode: task.ModePersistent, AgentInstanceID: "inst-interrupt"}, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Cancel(run.ID) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		current, err := runner.Get(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == task.StatusRunning {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("task did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	before, ok := ctrl.findInstanceSession("inst-interrupt")
	if !ok {
		t.Fatal("missing child")
	}
	response, err := server.InterruptAgentInstance(ctx, &pb.InterruptAgentInstanceRequest{Id: "inst-interrupt"})
	if err != nil || !response.GetOk() {
		t.Fatalf("interrupt: %v %v", response, err)
	}
	final, err := runner.Wait(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != task.StatusCancelled {
		t.Fatalf("interrupt resolved to %s, want cancelled", final.Status)
	}
	after, ok := ctrl.findInstanceSession("inst-interrupt")
	if !ok || before.ID != after.ID || after.Snapshot().State != session.StateSleeping || after.Snapshot().AgentSessionID != "provider-persistent" {
		t.Fatal("cancel did not preserve sleeping instance identity")
	}
	events, unsubscribe, err := runner.Subscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	sawCancelling := false
	for event := range events {
		if event.Kind == task.TaskEventStatus && event.Status == task.StatusCancelling {
			sawCancelling = true
		}
	}
	if !sawCancelling {
		t.Fatal("missing cancelling lifecycle event")
	}
}
