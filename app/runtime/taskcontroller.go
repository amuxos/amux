package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/rs/zerolog/log"
)

// taskController is the in-process task.SessionController for the Phase 1 local
// shim. The runner runs inside amuxlet and drives child sessions directly (not
// over gRPC), which is exactly how it bypasses owner enforcement: the gRPC
// handlers reject mutating calls on owned sessions, while the runner holds the
// *session.Session and calls its methods in-process.
type taskController struct {
	srv                 *server
	targetID            string
	mailbox             *instanceMailbox
	watchActiveInstance func(string) (<-chan struct{}, bool)
	cancelTask          func(string) error
}

func newTaskController(srv *server, targetID string) *taskController {
	return &taskController{srv: srv, targetID: targetID, mailbox: newInstanceMailbox()}
}

// TargetID is this runtime's identity, used to build the structured SessionRef.
// It is the same id the runtime registers with the hub (config runtime.id,
// defaulting to hostname), so a routed task's session_ref.target_id matches the
// target it was scheduled onto.
func (c *taskController) TargetID() string { return c.targetID }

// CreateChild creates an owner-tagged child session and starts its agent process.
// For a persistent agent instance it reuses the existing child session for that
// instance (named instance-<id>) when one is still present — e.g. a sleeping
// session left by a prior task — so the durable environment and provider session
// handle carry across tasks. A sleeping hit is resumed in-process via
// relaunchSessionLocked, which restarts the provider process with
// ResumeSessionID = the recorded AgentSessionID (Claude Resume / Codex
// thread/resume), restoring the prior conversation context. Resume is
// best-effort: if the provider session is no longer resumable, the stale
// sleeping session is discarded and a fresh child is created for the instance
// (conversation context lost, but the task still executes).
func (c *taskController) CreateChild(ctx context.Context, spec task.ChildSpec) (task.ChildSession, error) {
	c.srv.cleanupLaunchMu.RLock()
	defer c.srv.cleanupLaunchMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a, err := adapter.Get(spec.AgentType)
	if err != nil {
		return nil, fmt.Errorf("agent %q: %w", spec.AgentType, err)
	}
	permissionSpec := task.Spec{Agent: spec.AgentType, PermissionPolicy: spec.PermissionPolicy, ExtraArgs: spec.ExtraArgs, ResolvedProfile: spec.ProfileSpec}
	if err := agentprofile.ApplyTaskPermissionPolicy(&permissionSpec, false); err != nil {
		return nil, err
	}
	spec.ExtraArgs = permissionSpec.ExtraArgs
	spec.PermissionPolicy = permissionSpec.PermissionPolicy
	name := childSessionName(spec.TaskID, spec.InstanceID)
	// Task children must use the same effective launch PATH as ordinary
	// sessions. Sandbox env only contains isolation overrides (HOME/TMPDIR), so
	// passing it through unchanged would make provider lookup depend on the
	// service manager's restricted PATH and miss user-installed CLIs.
	launchEnv := ensureLaunchEnvPATH(spec.Env)
	recordEnv := ensureLaunchEnvPATH(spec.RecordEnv)

	// Persistent path: reuse an existing session for this instance.
	if spec.InstanceID != "" {
		c.reviveArchivedInstanceSession(name, spec)
		if sess, ok := c.srv.mgr.FindOwnedByName(name); ok {
			snap := sess.Snapshot()
			if err := validateTaskPermissionResume(snap, spec.PermissionPolicy); err != nil {
				return nil, err
			}
			if !sameMemoryPrincipal(snap.ExecutionIdentity, spec.ExecutionIdentity) {
				return nil, fmt.Errorf("persistent instance execution principal changed; create a new instance")
			}
			if snap.State == session.StateSleeping {
				// Resume the sleeping instance via provider resume (B2). This
				// restarts the agent process with ResumeSessionID so the provider
				// restores the prior conversation. Task child credentials are
				// supplied from this task's fresh launch env rather than from
				// persisted session env, so secret values never need to be stored.
				// Owner enforcement is preserved because we drive the owned
				// session in-process.
				if _, rerr := c.srv.relaunchSessionWithEnvContext(ctx, sess, "resuming", snap.ExtraArgs, launchEnv, recordEnv, spec.AgentActorToken, spec.AgentActorTokenExpiresAt); rerr != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					// Provider session expired / resume failed: the instance is
					// not recoverable. Discard the stale sleeping session and
					// fall through to creating a fresh child so the task can
					// still run (with a reset conversation context).
					_ = sess.Kill()
					_ = c.srv.mgr.Delete(sess)
				} else {
					cs := c.childSession(sess)
					// Drain queued instance_send inputs ahead of the new task prompt
					// so a QueueOnly message is delivered when the agent wakes.
					c.drainMailboxInto(cs, spec.InstanceID)
					return cs, nil
				}
			} else {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				// Non-sleeping (e.g. still Running from a prior task that did not
				// reach terminal sleep): reuse the live session as-is. Mailbox
				// input (instance_send, or an instance_steer that found no
				// steerable turn) still goes ahead of the new task prompt.
				cs := c.childSession(sess)
				c.drainMailboxInto(cs, spec.InstanceID)
				return cs, nil
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	binding, err := c.srv.resolveMemoryBinding(ctx, spec.AgentActorToken, spec.ExecutionIdentity, spec.AgentType)
	var memoryWarning string
	if err != nil {
		var unavailable *memoryUnavailableError
		if errors.As(err, &unavailable) {
			memoryWarning = err.Error()
		} else {
			return nil, fmt.Errorf("child memory binding: %w", err)
		}
	}
	spec.Env = memoryprotocol.SetEnv(spec.Env, memoryprotocol.BindingEnv, binding)
	spec.RecordEnv = memoryprotocol.SetEnv(spec.RecordEnv, memoryprotocol.BindingEnv, binding)
	sourceCwd := spec.Cwd
	cwd, _, executionIdentity, principalPaths, err := c.srv.prepareExecution(ctx, spec.ExecutionIdentity, spec.LaunchID, &pb.RunRequest{Cwd: spec.Cwd, Name: name})
	if err != nil {
		return nil, fmt.Errorf("prepare child isolation: %w", err)
	}
	spec.Cwd = cwd
	spec.ExecutionIdentity = executionIdentity
	if executionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		spec.Env, spec.RecordEnv, spec.ExtraArgs, err = materializeLinuxDACTaskLaunch(
			func(sources []isolation.BundleSource) error {
				return c.srv.isolationRuntime.MaterializeExecution(ctx, executionIdentity, spec.LaunchID, principalPaths.Workspace, sources)
			},
			sourceCwd, principalPaths,
			isolatedHomeProjectionFor(spec.AgentType, func(msg string) {
				fmt.Fprintf(os.Stderr, "runtime: %s\n", msg)
			}), spec.Env, spec.RecordEnv, spec.ExtraArgs,
		)
		if err != nil {
			_ = c.srv.isolationRuntime.RemoveRun(context.Background(), executionIdentity, spec.LaunchID)
			return nil, fmt.Errorf("materialize child isolation bundle: %w", err)
		}
	}
	permissionSpec.ExtraArgs = spec.ExtraArgs
	if err := agentprofile.ApplyTaskPermissionPolicy(&permissionSpec, executionIdentity.IsolationBackend == principal.BackendLinuxDAC); err != nil {
		return nil, err
	}
	spec.ExtraArgs = permissionSpec.ExtraArgs
	// Applied after isolation materialization rewrites the launch env, so the
	// credential survives the rewrite, and kept out of recordEnv: the hub mints
	// a fresh one per task, so nothing may persist it. No token-file path is
	// injected either — nothing persists the credential, so there is no file
	// for an in-session binary to re-read.
	spec.Env, spec.RecordEnv = applyAgentActorToken(spec.Env, spec.RecordEnv, spec.AgentActorToken, "")
	// Re-derive the launch/record env now that the actor token is baked into
	// spec.Env; the PATH-fixed values computed before the resume path predate it.
	launchEnv = ensureLaunchEnvPATH(spec.Env)
	recordEnv = ensureLaunchEnvPATH(spec.RecordEnv)
	sess, err := c.srv.mgr.CreateOwned(name, spec.AgentType, spec.OwnerTaskId, spec.InstanceID, spec.Cwd)
	if err != nil {
		if spec.InstanceID != "" {
			return nil, fmt.Errorf("create child session: %w; the agent instance's previous session is unusable — start a fresh instance with amux.instance_spawn", err)
		}
		return nil, fmt.Errorf("create child session: %w", err)
	}
	if err := c.srv.mgr.SetExecutionIdentity(sess, spec.ExecutionIdentity, spec.LaunchID); err != nil {
		_ = c.srv.mgr.Delete(sess)
		return nil, fmt.Errorf("record child execution identity: %w", err)
	}
	effectiveModel := strings.TrimSpace(spec.EffectiveModel)
	extraArgs := spec.ExtraArgs
	if effectiveModel == "" {
		effectiveModel, _ = agentprofile.AgentArgsModel(spec.AgentType, spec.ExtraArgs)
	}
	// Auto-select model for fresh child sessions when no explicit model is set
	// (e.g. provider load-aware selection). Persistent resume paths above skip this
	// because they preserve the previously selected model.
	extraArgs, effectiveModel = c.srv.maybeAutoSelectModel(a, spec.AgentType, spec.Cwd, launchEnv, extraArgs, effectiveModel)
	launchSpec := launchSpecSnapshot(&pb.RunRequest{
		AgentProfile: spec.AgentProfile,
		ProfileSpec:  spec.ProfileSpec,
		AgentType:    spec.AgentType,
		Name:         name,
		Cwd:          spec.Cwd,
	})
	var launchFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(launchSpec), &launchFields); err != nil {
		return nil, err
	}
	policy := spec.PermissionPolicy
	if policy == "" {
		policy = task.PolicyAllow
	}
	launchFields["task_permission_policy"], _ = json.Marshal(policy)
	encodedLaunch, err := json.Marshal(launchFields)
	if err != nil {
		return nil, err
	}
	launchSpec = string(encodedLaunch)
	if err := c.srv.mgr.RecordLaunchParams(sess, extraArgs, recordEnv, "", "", spec.AgentProfile, effectiveModel, launchSpec); err != nil {
		return nil, fmt.Errorf("record child params: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = c.srv.mgr.Delete(sess)
		return nil, err
	}
	if err := c.srv.startSessionContext(ctx, a, sess, extraArgs, launchEnv); err != nil {
		_ = c.srv.mgr.SetState(sess, session.StateArchived)
		return nil, fmt.Errorf("start child agent: %w", err)
	}
	if memoryWarning != "" {
		c.srv.reportLaunchWarning(sess)(memoryWarning)
	}
	if warning := agentActorTokenExpiryWarning(spec.AgentActorTokenExpiresAt, time.Now()); warning != "" {
		sess.AppendReplay([]byte("\r\n[system] " + warning + "\r\n"))
	}
	cs := c.childSession(sess)
	c.drainMailboxInto(cs, spec.InstanceID)
	return cs, nil
}

func (c *taskController) childSession(sess *session.Session) *childSession {
	return &childSession{sess: sess, mgr: c.srv.mgr, isolationRuntime: c.srv.isolationRuntime}
}

// drainMailboxInto replays queued instance_send inputs for the instance into the
// child session's input queue ahead of the task prompt. It is a no-op for
// ephemeral tasks (empty InstanceID) or when the mailbox is empty.
func (c *taskController) drainMailboxInto(child *childSession, instanceID string) {
	if instanceID == "" {
		return
	}
	entries := c.mailbox.drain(instanceID)
	for _, e := range entries {
		_ = child.SubmitAsync(e.Source, e.Text)
	}
}

// childSessionName returns the child session name. Persistent agent instances
// use instance-<id> so a follow-up task finds and reuses the same session;
// ephemeral tasks keep the subtask-<taskID> name.
func childSessionName(taskID, instanceID string) string {
	if instanceID != "" {
		return "instance-" + instanceID
	}
	return "subtask-" + taskID
}

// TeardownChild kills and archives a surviving child session by id. Orphan
// recovery calls this after a restart: the broker subprocess can outlive an
// amuxlet restart, so the session must be reaped once its owning task is failed.
// An unknown id (already gone) is a no-op.
func (c *taskController) TeardownChild(_ context.Context, sessionID string) error {
	sess, err := c.srv.mgr.Get(sessionID)
	if err != nil {
		return nil // already gone — nothing to reap
	}
	return c.childSession(sess).Kill()
}

// childSession adapts *session.Session to task.ChildSession.
type childSession struct {
	sess             *session.Session
	mgr              *session.Manager
	isolationRuntime *isolation.Runtime
}

func (c *childSession) ID() string   { return c.sess.ID }
func (c *childSession) Name() string { return c.sess.Name }
func (c *childSession) Follow(ctx context.Context) <-chan agentstream.Event {
	out := make(chan agentstream.Event, 256)
	_, subscription, err := c.sess.AgentTimeline().Subscribe(ctx)
	if err != nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		defer subscription.Close()
		for {
			event, err := subscription.Recv(ctx)
			if err != nil {
				return
			}
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
func (c *childSession) SubmitAsync(source, text string) error {
	return c.sess.SubmitAsync(source, text)
}
func (c *childSession) SubmitTracked(source, text string) (task.TurnSubmission, error) {
	item, err := c.sess.SubmitAsyncTracked(source, text)
	if err != nil {
		return task.TurnSubmission{}, err
	}
	return task.TurnSubmission{TurnID: item.TurnID}, nil
}
func (c *childSession) RespondPermission(permissionID, optionID string) error {
	return c.sess.RespondPermission(permissionID, optionID)
}
func (c *childSession) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	return c.sess.RespondPermissionMulti(permissionID, optionIDs)
}
func (c *childSession) Interrupt() error { return c.sess.Interrupt() }
func (c *childSession) AgentSessionID() string {
	return c.sess.Snapshot().AgentSessionID
}
func (c *childSession) ExecutionIdentity() principal.ExecutionIdentity {
	return c.sess.Snapshot().ExecutionIdentity
}
func (c *childSession) LaunchID() string {
	return c.sess.Snapshot().LaunchID
}
func (c *childSession) SetSleeping(reason string) error {
	snap := c.sess.Snapshot()
	killErr := c.sess.Kill()
	removeErr := c.removeRun(snap)
	stateErr := c.mgr.SetSleeping(c.sess, reason)
	return errors.Join(killErr, removeErr, stateErr)
}
func (c *childSession) Kill() error {
	snap := c.sess.Snapshot()
	err := c.sess.Kill()
	removeErr := c.removeRun(snap)
	// Best-effort archive; the output-drain goroutine also archives on EOF.
	_ = c.mgr.SetState(c.sess, session.StateArchived)
	return errors.Join(err, removeErr)
}

func (c *childSession) removeRun(snap session.Snapshot) error {
	if c.isolationRuntime == nil {
		return nil
	}
	return c.isolationRuntime.RemoveRun(context.Background(), snap.ExecutionIdentity, snap.LaunchID)
}

func validateTaskPermissionResume(snap session.Snapshot, requested task.PermissionPolicy) error {
	policy, err := frozenTaskPermissionPolicy(snap)
	if err != nil {
		return err
	}
	if policy == "" {
		return agentprofile.TaskPermissionLegacyResumeError()
	}
	if requested == "" {
		requested = task.PolicyAllow
	}
	if policy != string(requested) {
		return agentprofile.TaskPermissionResumeError(policy, string(requested))
	}
	return nil
}

func frozenTaskPermissionPolicy(snap session.Snapshot) (string, error) {
	var saved struct {
		PermissionPolicy string `json:"task_permission_policy"`
	}
	if snap.LaunchSpec != "" {
		if err := json.Unmarshal([]byte(snap.LaunchSpec), &saved); err != nil {
			return "", err
		}
	}
	return saved.PermissionPolicy, nil
}

// reviveArchivedInstanceSession makes a persistent instance's child session
// that was archived after a failed task (start failure, runtime restart)
// reusable: when it still belongs to this instance and holds a provider
// conversation it is marked sleeping so the resume path below continues that
// conversation; otherwise the stale row is removed so a fresh child session
// can take the instance's name instead of failing with "already exists".
func (c *taskController) reviveArchivedInstanceSession(name string, spec task.ChildSpec) {
	stale, err := c.srv.mgr.Get(name)
	if err != nil || stale.StateSnapshot() != session.StateArchived {
		return
	}
	snap := stale.Snapshot()
	if snap.AgentInstanceID == spec.InstanceID && snap.AgentType == spec.AgentType &&
		strings.TrimSpace(snap.AgentSessionID) != "" && sameMemoryPrincipal(snap.ExecutionIdentity, spec.ExecutionIdentity) {
		if err := c.srv.mgr.SetSleeping(stale, session.SleepReasonRestartFailed); err == nil {
			log.Info().Str("session", name).Str("agent_instance_id", spec.InstanceID).Msg("runtime: reusing archived instance child session via resume")
			return
		}
	}
	_ = stale.Kill()
	if err := c.srv.mgr.Delete(stale); err != nil {
		log.Warn().Err(err).Str("session", name).Msg("runtime: remove stale archived instance child session")
	}
}
