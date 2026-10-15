package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// instanceMailbox holds instance_send inputs queued for a persistent agent
// instance while it has no live turn to consume them (QueueOnly). Messages
// survive SetSleeping — sleeping kills the agent process but the controller
// (a long-lived amuxlet object) retains the mailbox — and are drained into the
// child session's input queue, ahead of the new task prompt, the next time the
// instance is woken by instance_spawn / instance_followup.
//
// The mailbox is in-memory and target-local: it does not survive an amuxlet
// restart. That matches Phase 3's honest target-local durability boundary
// (sticky single-target is a single point of failure); cross-restart mailbox
// persistence is a future phase. It is keyed by agent_instance_id.
type instanceMailbox struct {
	mu         sync.Mutex
	byInstance map[string][]mailboxEntry
}

type mailboxEntry struct {
	Text   string
	Source string
	SentAt time.Time
}

func newInstanceMailbox() *instanceMailbox {
	return &instanceMailbox{byInstance: map[string][]mailboxEntry{}}
}

// enqueue appends a message to the instance mailbox and returns the new pending
// depth.
func (m *instanceMailbox) enqueue(instanceID, source, text string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byInstance[instanceID] = append(m.byInstance[instanceID], mailboxEntry{
		Text:   text,
		Source: source,
		SentAt: time.Now(),
	})
	return len(m.byInstance[instanceID])
}

// drain returns and clears the pending messages for an instance. The caller
// (CreateChild, after waking the child) replays them into the child session
// input queue ahead of the new task prompt. Returns nil when the instance has
// no pending messages.
func (m *instanceMailbox) drain(instanceID string) []mailboxEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.byInstance[instanceID]) == 0 {
		return nil
	}
	out := m.byInstance[instanceID]
	delete(m.byInstance, instanceID)
	return out
}

// pending returns the current pending depth for an instance.
func (m *instanceMailbox) pending(instanceID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byInstance[instanceID])
}

// instanceName is the child-session name for an agent instance. It mirrors
// childSessionName for the persistent path so RPCs that address an instance by
// id resolve to the same session the runner creates.
func instanceName(instanceID string) string { return "instance-" + instanceID }

// findInstanceSession returns the live (non-archived) child session for an
// agent instance, or (nil, false) if none exists.
func (c *taskController) findInstanceSession(instanceID string) (*session.Session, bool) {
	return c.srv.mgr.FindOwnedByName(instanceName(instanceID))
}

// SendInstanceMessage enqueues a QueueOnly message into the instance mailbox.
// It never wakes a sleeping agent; the message is delivered on the next
// instance_spawn / instance_followup wake-up.
func (c *taskController) SendInstanceMessage(_ context.Context, instanceID, source, text string) (int, error) {
	if err := validateInstanceMessage(instanceID, text); err != nil {
		return 0, err
	}
	if source == "" {
		source = "instance_send"
	}
	pending := c.mailbox.enqueue(instanceID, source, text)
	return pending, nil
}

// SteerInstance injects a correction into the instance's running turn. When
// the instance has no steerable active turn (sleeping, idle, provider without
// steering, or the turn just ended) the text goes to the mailbox like
// instance_send, so the next instance_spawn / instance_followup wake-up
// delivers it ahead of the new prompt.
func (c *taskController) SteerInstance(ctx context.Context, instanceID, source, text string) (string, string, int, error) {
	if err := validateInstanceMessage(instanceID, text); err != nil {
		return "", "", 0, err
	}
	if source == "" {
		source = "instance_steer"
	}
	reason := "agent instance has no live session"
	if sess, ok := c.findInstanceSession(instanceID); ok {
		if state := sess.StateSnapshot(); state != session.StateRunning {
			reason = fmt.Sprintf("agent instance is not running (state=%s)", state)
		} else {
			input := session.FrameSteerInput(session.SteerNoteSourceCoordinator, session.TextUserInput(text))
			_, err := sess.SteerInput(ctx, source, input, "", session.TurnAuthorization{})
			if err == nil {
				return "steered", "", 0, nil
			}
			if errors.Is(err, session.ErrSteerOutcomeUnknown) {
				// The turn may already have the message; the mailbox would
				// deliver it a second time.
				return "", "", 0, status.Errorf(codes.Unknown, "steer outcome unknown; the message was not queued to avoid delivering it twice: %v", err)
			}
			reason = err.Error()
		}
	}
	pending, err := c.SendInstanceMessage(ctx, instanceID, source, text)
	if err != nil {
		return "", "", 0, err
	}
	return "mailbox", reason, pending, nil
}

func validateInstanceMessage(instanceID, text string) error {
	if instanceID == "" {
		return status.Error(codes.InvalidArgument, "agent_instance_id is required")
	}
	if text == "" {
		return status.Error(codes.InvalidArgument, "message text is required")
	}
	return nil
}

// InterruptInstance cancels the current owner TaskRun through its runner.
// Cancelling the task before provider teardown preserves cancellation truth even
// when a provider reports a successful terminal in response to interruption.
// A sleeping or absent instance is reported as not-running.
func (c *taskController) InterruptInstance(_ context.Context, instanceID string) error {
	if instanceID == "" {
		return status.Error(codes.InvalidArgument, "agent_instance_id is required")
	}
	sess, ok := c.findInstanceSession(instanceID)
	if !ok {
		return status.Errorf(codes.NotFound, "agent instance %q has no live session", instanceID)
	}
	snap := sess.Snapshot()
	if snap.State != session.StateRunning {
		return status.Errorf(codes.FailedPrecondition, "agent instance %q is not running (state=%s)", instanceID, snap.State)
	}
	caps := adapterCapabilities(snap.AgentType)
	if !caps.Interrupt {
		return status.Errorf(codes.FailedPrecondition, "interrupt unsupported for agent %s", snap.AgentType)
	}
	if snap.OwnerTaskId == "" || c.cancelTask == nil {
		return status.Error(codes.FailedPrecondition, "instance has no owner task cancellation path")
	}
	if err := c.cancelTask(snap.OwnerTaskId); err != nil {
		return status.Errorf(codes.FailedPrecondition, "cancel instance task: %v", err)
	}
	return nil
}

// WaitInstance joins the instance's current turn. It blocks while the child
// session is active and returns when the child becomes sleeping/archived or the
// timeout elapses. An already sleeping/archived child returns immediately;
// ordinary output does not end the wait. The bool reports whether a completing
// state transition was observed during this call.
func (c *taskController) WaitInstance(ctx context.Context, instanceID string, timeout time.Duration) (bool, string, error) {
	if instanceID == "" {
		return false, "", status.Error(codes.InvalidArgument, "agent_instance_id is required")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	waited := false
	for {
		// Install the manager watch before reading state so a transition cannot
		// land between the snapshot and subscription. The notification is global;
		// every wake-up re-resolves and re-checks this exact instance.
		changed := c.srv.mgr.WatchSessions()
		var taskDone <-chan struct{}
		taskActive := false
		if c.watchActiveInstance != nil {
			taskDone, taskActive = c.watchActiveInstance(instanceID)
		}
		sess, ok := c.findInstanceSessionForDescribe(instanceID)
		if !ok && !taskActive {
			return false, "", status.Errorf(codes.NotFound, "agent instance %q has no session", instanceID)
		}
		var state session.State
		if ok {
			state = sess.StateSnapshot()
		} else if taskActive {
			// The TaskRun is already in flight but has not created its child
			// session yet. Represent that active startup window as running so a
			// timeout/cancellation never leaks an invalid empty session state.
			state = session.StateRunning
		}
		if ok && waitInstanceComplete(state) && !taskActive {
			return waited, string(state), nil
		}
		waited = true
		select {
		case <-changed:
			continue
		case <-taskDone:
			continue
		case <-timer.C:
			return false, c.currentInstanceState(instanceID, state), nil
		case <-ctx.Done():
			return false, c.currentInstanceState(instanceID, state), status.FromContextError(ctx.Err()).Err()
		}
	}
}

func (c *taskController) currentInstanceState(instanceID string, fallback session.State) string {
	if sess, ok := c.findInstanceSessionForDescribe(instanceID); ok {
		return string(sess.StateSnapshot())
	}
	return string(fallback)
}

func waitInstanceComplete(state session.State) bool {
	return state == session.StateSleeping || state == session.StateArchived
}

// DescribeInstance returns the amuxlet-local live view for one instance: the
// child-session id/name/state and the mailbox depth. amuxd calls it to enrich
// ListAgentInstances / GetAgentInstance.
func (c *taskController) DescribeInstance(_ context.Context, instanceID string) (*pb.DescribeAgentInstanceResponse, error) {
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_instance_id is required")
	}
	out := &pb.DescribeAgentInstanceResponse{
		PendingMessages: int32(c.mailbox.pending(instanceID)),
	}
	sess, ok := c.findInstanceSessionForDescribe(instanceID)
	if ok {
		snap := sess.Snapshot()
		out.ChildSessionId = snap.ID
		out.ChildSessionName = snap.Name
		out.ChildSessionState = string(snap.State)
		out.ExecutionIdentity = principal.ToProtoIdentity(snap.ExecutionIdentity)
		out.LaunchId = snap.LaunchID
	}
	return out, nil
}

// findInstanceSessionForDescribe prefers a live child, but falls back to the
// newest archived child so amuxd can converge a durable active record after a
// provider fails during startup. Operational methods intentionally keep using
// findInstanceSession, where archived children must not be treated as live.
func (c *taskController) findInstanceSessionForDescribe(instanceID string) (*session.Session, bool) {
	if sess, ok := c.findInstanceSession(instanceID); ok {
		return sess, true
	}
	var newest *session.Session
	var newestAt time.Time
	for _, sess := range c.srv.mgr.List() {
		snap := sess.Snapshot()
		if snap.AgentInstanceID != instanceID || snap.State != session.StateArchived {
			continue
		}
		if newest == nil || snap.UpdatedAt.After(newestAt) {
			newest = sess
			newestAt = snap.UpdatedAt
		}
	}
	return newest, newest != nil
}

// CleanupInstance destroys the durable environment of a persistent agent
// instance after amuxd has archived its durable record (typically because the
// owner session entered archived — the instance follows the owner lifecycle,
// v0.39). It is idempotent: a missing session or sandbox is not an error.
//
// Steps: stop any live broker for the instance's child session and archive it,
// remove the instance-keyed sandbox directory (durable workspace/home/non-secret
// state), clear the in-memory mailbox, and drop session artifacts (socket/pid).
// Per-run secrets are already destroyed at task terminal; this removes the
// durable non-secret remainder.
func (c *taskController) CleanupInstance(_ context.Context, instanceID string) error {
	if instanceID == "" {
		return status.Error(codes.InvalidArgument, "agent_instance_id is required")
	}
	// Stop the live broker and archive the child session so it disappears from
	// instance_list / amux ls and is not left as a sleeping orphan.
	if sess, ok := c.findInstanceSession(instanceID); ok {
		_ = sess.Kill()
		_ = c.srv.mgr.SetState(sess, session.StateArchived)
		c.srv.cleanupSessionArtifacts(sess)
	}
	// Remove the durable, instance-keyed sandbox (workspace / home / non-secret
	// state). SandboxKey(taskID, instanceID) = "instance-"+instanceID.
	sbDir := filepath.Join(task.SandboxesDir(c.srv.amuxDir), "instance-"+instanceID)
	if err := os.RemoveAll(sbDir); err != nil && !os.IsNotExist(err) {
		return status.Errorf(codes.Internal, "cleanup sandbox: %v", err)
	}
	// Drop any queued mailbox messages; the instance is gone.
	c.mailbox.drain(instanceID)
	fmt.Fprintf(os.Stderr, "runtime: cleaned up agent instance %s\n", instanceID)
	return nil
}

// ListInstanceSummaries returns live child-session state for every instance
// session currently on this runtime. amuxd uses it as a fallback to discover
// instances whose durable store record is missing (e.g. created before the
// store was initialized).
func (c *taskController) ListInstanceSummaries() []instanceSummary {
	var out []instanceSummary
	for _, sess := range c.srv.mgr.List() {
		snap := sess.Snapshot()
		if snap.AgentInstanceID == "" {
			continue
		}
		policy, _ := frozenTaskPermissionPolicy(snap)
		out = append(out, instanceSummary{
			InstanceID:        snap.AgentInstanceID,
			ChildSessionID:    snap.ID,
			ChildSessionName:  snap.Name,
			State:             string(snap.State),
			PermissionPolicy:  policy,
			ExecutionIdentity: snap.ExecutionIdentity,
		})
	}
	return out
}

type instanceSummary struct {
	InstanceID        string
	ChildSessionID    string
	ChildSessionName  string
	State             string
	PermissionPolicy  string
	ExecutionIdentity principal.ExecutionIdentity
}
