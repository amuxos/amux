package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/credentialref"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
)

// sourceTask labels child input that originates from the task runner (vs cli/lark).
const sourceTask = "task"

// defaultTaskTimeout bounds a turn when neither the spec nor Options sets one.
const defaultTaskTimeout = 30 * time.Minute

var (
	errTaskCancelled = errors.New("task cancelled")
	errTaskTimedOut  = errors.New("task timed out")
	errTaskFinished  = errors.New("task finished")
)

// outBuf accumulates agent output between flushes so the durable log gets a few
// coalesced output events instead of one row per chunk.
type outBuf struct{ b []byte }

func (o *outBuf) add(p []byte) { o.b = append(o.b, p...) }
func (o *outBuf) len() int     { return len(o.b) }
func (o *outBuf) take() []byte {
	if len(o.b) == 0 {
		return nil
	}
	b := o.b
	o.b = nil
	return b
}

const (
	outFlushBytes    = 16 * 1024
	outFlushInterval = 200 * time.Millisecond
)

// ChildSpec is the request to create and start a child session for a task. The
// controller persists it with OwnerTaskId so owner enforcement can reject direct
// mutating RPCs from anyone but this runner.
type ChildSpec struct {
	AgentExecutable  string
	TaskID           string
	AgentType        string
	AgentProfile     string
	ProfileSpec      *pb.AgentProfileSpec
	EffectiveModel   string
	PermissionPolicy PermissionPolicy
	Cwd              string
	Env              []string
	// RecordEnv is the non-secret subset of Env that may be persisted as launch
	// params for restart/resume bookkeeping. Credential material belongs only in
	// Env and must never be written to the session store.
	RecordEnv   []string
	ExtraArgs   []string
	OwnerTaskId string
	// InstanceID is the persistent agent instance id (Phase 3). Empty for
	// ephemeral tasks. The controller uses it to name the child session
	// (instance-<id>) so a follow-up task for the same instance reuses the
	// sleeping session rather than spawning a new one.
	InstanceID        string
	ExecutionIdentity principal.ExecutionIdentity
	LaunchID          string
	// AgentActorToken is the hub-minted actor credential for this launch. The
	// controller injects it into the child process env and keeps it out of the
	// persisted record env. It travels in memory only: never on the Spec
	// (spec_json), never on the TaskRun (task store), never in launch params.
	AgentActorToken string
	// AgentActorTokenExpiresAt is the credential's unix expiry (0 when
	// unknown), carried so the controller can emit the same expiry warning the
	// session launch path does.
	AgentActorTokenExpiresAt int64
}

// ChildSession is the runner's handle to a started child session: the subset of
// session operations the control plane needs. The amuxlet binary backs it with a
// real *session.Session; tests back it with a fake.
type ChildSession interface {
	ID() string
	Name() string
	Follow(ctx context.Context) <-chan agentstream.Event
	SubmitAsync(source, text string) error
	SubmitTracked(source, text string) (TurnSubmission, error)
	RespondPermission(permissionID, optionID string) error
	// RespondPermissionMulti responds to a multiSelect user-input permission.
	RespondPermissionMulti(permissionID string, optionIDs []string) error
	Interrupt() error
	Kill() error
	// AgentSessionID returns the provider-side conversation id recorded for this
	// child (empty until the provider emits one). Persistent tasks use it as the
	// B2 resume handle and as the gate for keeping the instance alive at terminal
	// state: a persistent child without one cannot be resumed and is torn down.
	AgentSessionID() string
	// SetSleeping transitions the child to the sleeping state, stopping the
	// runtime process while preserving session identity, replay, cwd, launch
	// params, and the provider AgentSessionID for a later resume. Used by the
	// persistent execution path at task terminal state instead of Kill+teardown.
	SetSleeping(reason string) error
}

// TurnSubmission identifies the exact child turn created for one TaskRun.
// Persistent child sessions may contain earlier or queued turns, so task
// completion must never be inferred from a session-wide terminal event.
type TurnSubmission struct {
	TurnID string
}

// SessionController creates and starts child sessions for the runner. The
// implementation owns the session.Manager + process launch and is responsible
// for recording OwnerTaskId before the session can be mutated.
type SessionController interface {
	// CreateChild creates, persists with owner_task_id, and starts a child
	// session, returning a handle once the agent process is running.
	CreateChild(ctx context.Context, spec ChildSpec) (ChildSession, error)
	// TeardownChild kills and archives a child session by id. It is used by
	// orphan recovery after a restart, where the runner failed the owning task
	// but the child session (a broker subprocess) may have survived and must be
	// reaped so it does not linger as a counted-but-dead session. Unknown ids are
	// a no-op (nil error).
	TeardownChild(ctx context.Context, sessionID string) error
	// TargetID is the runtime/target identity these sessions live on, used to
	// build the structured SessionRef. Empty for the purely-local shim.
	TargetID() string
}

// TaskEventKind classifies a runner event published to stream subscribers.
type TaskEventKind string

const (
	TaskEventStatus     TaskEventKind = "status"
	TaskEventOutput     TaskEventKind = "output"
	TaskEventTool       TaskEventKind = "tool"
	TaskEventUsage      TaskEventKind = "usage"
	TaskEventPermission TaskEventKind = "permission" // escalation awaiting main-agent answer
	TaskEventResult     TaskEventKind = "result"
)

// ToolActivity is the redacted, display-safe projection of one canonical tool
// change. Raw inputs, outputs, content and locations stay on the Agent Timeline.
type ToolActivity struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
}

// TaskEvent is one item on a task's stream (the MCP amux.task_stream source).
// Output bytes and permission payloads are already redacted.
type TaskEvent struct {
	TaskID     string             `json:"task_id"`
	TurnID     string             `json:"turn_id,omitempty"`
	Seq        uint64             `json:"seq"`
	Kind       TaskEventKind      `json:"kind"`
	Status     Status             `json:"status,omitempty"`
	Output     []byte             `json:"output,omitempty"`
	Tool       *ToolActivity      `json:"tool,omitempty"`
	Usage      json.RawMessage    `json:"usage,omitempty"`
	Permission *PermissionRequest `json:"permission,omitempty"`
	Result     *TaskResult        `json:"result,omitempty"`
	At         time.Time          `json:"at"`
}

// PermissionResponse is the main agent's answer to an escalated permission.
type PermissionResponse struct {
	PermissionId string
	Allow        bool
	OptionId     string // optional explicit provider option id; derived from Allow when empty
	Reason       string
}

// Options configures a Runner.
type Options struct {
	AmuxDir        string
	Store          Store
	Events         EventStore // nil => defaults to Store if it implements EventStore, else a MemStore
	Control        SessionController
	Artifacts      ArtifactPruner     // nil => prune only task rows/events
	Creds          CredentialProvider // nil => NoCredentials
	DefaultTimeout time.Duration      // applied when Spec.TimeoutSeconds == 0
	Clock          func() time.Time   // nil => time.Now
	IDGen          func() string      // nil => uuid
	// NativeResult reports whether an agent type's provider emits a native turn
	// result event (so the runner records the summary from it and skips injecting
	// the fallback envelope). nil => treat all agents as non-native (always
	// inject the envelope), preserving the pre-native behaviour.
	NativeResult func(agentType string) bool
	// MaterializeProfile, when set, writes a resolved Agent Profile's content into
	// the per-run sandbox and returns extra CLI args (e.g. the system prompt flag)
	// to pass to the child. It is wired in cmd/amuxlet so internal/task never
	// imports internal/agentprofile. nil => profiles are not materialized.
	MaterializeProfile func(profile *pb.AgentProfileSpec, sb *Sandbox) ([]string, error)
	// MaterializeMCP, when set, writes the runtime's built-in MCP configuration
	// for the agent into the per-run sandbox and returns extra CLI args. It is
	// called for raw agent tasks and as a fallback when MaterializeProfile did not
	// add MCP args. nil => no automatic MCP projection.
	MaterializeMCP func(agentType string, sb *Sandbox) ([]string, error)
	// LaunchEnv returns runtime-owned, non-recorded environment defaults for an
	// execution identity. cmd/amuxlet uses it for isolation.global_env. Values
	// are tracked as secrets for result/event redaction and never enter RecordEnv.
	LaunchEnv func(identity principal.ExecutionIdentity) []string
}

// Runner is the Phase 1 local control-plane shim. It composes the existing
// session primitives (via SessionController) and adds isolation, credential
// materialization, permission resolution, completion judgement and owner-scoped
// task records.
type Runner struct {
	opts     Options
	resolver Resolver

	mu     sync.Mutex
	active map[string]*activeTask
}

// activeTask holds the live control handles for an in-flight task. All TaskRun
// mutations happen on the execute goroutine; cross-goroutine access (Subscribe,
// Cancel, RespondPermission) only touches the mutex-guarded fields and channels.
type activeTask struct {
	id         string
	instanceID string
	ctx        context.Context
	cancel     context.CancelCauseFunc
	timer      *time.Timer
	done       chan struct{}

	permResp chan PermissionResponse

	mu            sync.Mutex
	notifs        []chan struct{}    // per-subscriber wakeup channels (buffered 1)
	turnID        string             // exact accepted child turn (mutex-guarded)
	pendingPermID string             // current escalated permission id (mutex-guarded)
	pendingPerm   *PermissionRequest // touched only by execute goroutine
}

// NewRunner constructs a Runner. Store and Control are required.
func NewRunner(opts Options) (*Runner, error) {
	if opts.Store == nil {
		return nil, errors.New("task runner: Store is required")
	}
	if opts.Control == nil {
		return nil, errors.New("task runner: Control is required")
	}
	if opts.Events == nil {
		if es, ok := opts.Store.(EventStore); ok {
			opts.Events = es
		} else {
			opts.Events = NewMemStore()
		}
	}
	if opts.Creds == nil {
		opts.Creds = NoCredentials{}
	}
	return &Runner{opts: opts, active: map[string]*activeTask{}}, nil
}

// RecoverOrphans marks tasks left in a non-terminal state by a previous process
// (crash/restart) as failed, so the store never advertises a "running" task with
// no live execution behind it. The local shim has no cross-restart execution
// continuity; Phase 2's reconciler supersedes this with real reconciliation.
// Returns the ids it reconciled.
func (r *Runner) RecoverOrphans() ([]string, error) {
	runs, err := r.opts.Store.List()
	if err != nil {
		return nil, err
	}
	var recovered []string
	for _, run := range runs {
		if run.Status.IsTerminal() {
			continue
		}
		// Reap the child session before failing the task. A broker subprocess can
		// outlive an amuxlet restart, so the session may still be alive (and
		// counted toward load) even though its owning task is dead. The spec puts
		// session-side orphan cleanup on "RecoverOrphans + runner 终态 teardown".
		// Best-effort: a teardown failure must not block failing the task record.
		if run.ChildSessionId != "" {
			_ = r.opts.Control.TeardownChild(context.Background(), run.ChildSessionId)
		}
		run.Status = StatusFailed
		run.Error = "interrupted by runtime restart"
		run.FinishedAt = r.now()
		if err := r.opts.Store.Save(run); err != nil {
			return recovered, err
		}
		_, _ = r.opts.Events.Append(run.ID, TaskEvent{
			Kind:   TaskEventStatus,
			Status: StatusFailed,
			At:     r.now(),
		})
		recovered = append(recovered, run.ID)
	}
	return recovered, nil
}

func (r *Runner) now() time.Time {
	if r.opts.Clock != nil {
		return r.opts.Clock()
	}
	return time.Now()
}

func (r *Runner) newID() string {
	if r.opts.IDGen != nil {
		return r.opts.IDGen()
	}
	return "task-" + uuid.New().String()
}

// actorCredential is the hub-minted identity credential for one task child
// launch. The control plane hands it over at submit time and it is carried in
// memory to the child launch — never persisted on the Spec (spec_json), the
// TaskRun (task store), or the child session's recorded launch env.
type actorCredential struct {
	token     string
	expiresAt int64
}

// Submit validates the spec, records a pending TaskRun, and starts execution in
// the background. It returns the initial TaskRun once persisted; the caller polls
// Get / streams Subscribe for progress. Use Run for blocking amux.task_run calls.
//
// launchID and the actor credential arrive from the trusted control plane,
// which mints the credential for exactly that launch; local callers pass empty
// values and the runner generates its own launch id.
func (r *Runner) Submit(spec Spec, launchID, actorToken string, actorTokenExpiresAt int64) (*TaskRun, error) {
	if err := validateSpec(&spec); err != nil {
		return nil, err
	}
	credential, err := ResolveCredentialPlan(spec)
	if err != nil {
		return nil, err
	}
	run := r.newRun(spec, credential.LegacyProfile, credential.Profiles, launchID)
	if err := r.opts.Store.Save(run); err != nil {
		return nil, fmt.Errorf("persist task: %w", err)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	at := &activeTask{
		id:         run.ID,
		instanceID: run.AgentInstanceID,
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		permResp:   make(chan PermissionResponse, 1),
	}
	at.timer = time.AfterFunc(r.timeout(spec), func() { cancel(errTaskTimedOut) })
	r.mu.Lock()
	r.active[run.ID] = at
	r.mu.Unlock()

	// Return a copy so the caller cannot observe later mutations through the pointer.
	snapshot := *run
	go r.execute(at, run, spec, actorCredential{token: actorToken, expiresAt: actorTokenExpiresAt})
	return &snapshot, nil
}

// Run submits a blocking task and waits for it to reach a terminal state. It is
// the task_run synchronous sugar. escalate-to-main is rejected at validation
// because a blocking caller cannot answer an out-of-band permission request.
func (r *Runner) Run(ctx context.Context, spec Spec) (*TaskRun, error) {
	spec.Blocking = true
	run, err := r.Submit(spec, "", "", 0)
	if err != nil {
		return nil, err
	}
	return r.Wait(ctx, run.ID)
}

// Wait blocks until the task is terminal (or ctx is done) and returns the final
// TaskRun.
func (r *Runner) Wait(ctx context.Context, id string) (*TaskRun, error) {
	r.mu.Lock()
	at := r.active[id]
	r.mu.Unlock()
	if at == nil {
		return r.opts.Store.Get(id) // already terminal (or unknown)
	}
	select {
	case <-at.done:
		return r.opts.Store.Get(id)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Get returns the current TaskRun record.
func (r *Runner) Get(id string) (*TaskRun, error) { return r.opts.Store.Get(id) }

// WatchActiveInstance returns one in-flight task completion channel for a
// persistent agent instance. Callers re-check after the channel closes; this
// supports serialized or accidentally overlapping tasks without polling. The
// active entry is installed before Submit returns, so an immediate wait after
// instance_followup cannot mistake the instance's still-sleeping child for an idle
// instance while its next task is pending startup.
func (r *Runner) WatchActiveInstance(instanceID string) (<-chan struct{}, bool) {
	if instanceID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, at := range r.active {
		if at.instanceID == instanceID {
			return at.done, true
		}
	}
	return nil, false
}

// TaskExists reports whether a TaskRun record with the id is still present in
// the store. A missing record returns (false, nil); a transient store failure
// returns the error so callers (orphan-session reaping) can stay conservative
// and not delete a child session on a flaky read.
func (r *Runner) TaskExists(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	if _, err := r.opts.Store.Get(id); err != nil {
		if errors.Is(err, ErrTaskNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// List returns all known TaskRun records.
func (r *Runner) List() ([]*TaskRun, error) { return r.opts.Store.List() }

// Cancel requests cancellation. For an active task it cancels the execution
// context (the runner maps this to Interrupt+Kill and a cancelled terminal
// state). For an already-finished or never-started task it is a no-op / records
// cancelled.
func (r *Runner) Cancel(id string) error {
	r.mu.Lock()
	at := r.active[id]
	r.mu.Unlock()
	if at != nil {
		at.cancel(errTaskCancelled)
		return nil
	}
	run, err := r.opts.Store.Get(id)
	if err != nil {
		return err
	}
	if run.Status.IsTerminal() {
		return nil
	}
	run.Status = StatusCancelled
	run.FinishedAt = r.now()
	return r.opts.Store.Save(run)
}

// RespondPermission delivers a main-agent answer to an escalated permission
// request. It is rejected unless the task is currently awaiting a permission and
// (when provided) the response's PermissionId matches the pending one.
func (r *Runner) RespondPermission(id string, resp PermissionResponse) error {
	r.mu.Lock()
	at := r.active[id]
	r.mu.Unlock()
	if at == nil {
		return fmt.Errorf("task %q is not active", id)
	}
	at.mu.Lock()
	pendingID := at.pendingPermID
	at.mu.Unlock()
	if pendingID == "" {
		return fmt.Errorf("task %q has no permission awaiting response", id)
	}
	if resp.PermissionId != "" && resp.PermissionId != pendingID {
		return fmt.Errorf("permission %q does not match the pending request %q", resp.PermissionId, pendingID)
	}
	select {
	case at.permResp <- resp:
		return nil
	default:
		return fmt.Errorf("a permission response for task %q is already queued", id)
	}
}

// coalesceOutputEvents merges runs of consecutive output events within a single
// batch into chunks of at most outFlushBytes, leaving every other event kind in
// place and in order. A merged chunk carries the LAST merged event's Seq (so a
// from_seq resume cursor stays lossless and non-duplicating) and the FIRST
// merged event's At. Non-output events flush the pending chunk; a single output
// event already larger than outFlushBytes passes through whole (never split).
//
// This collapses the burst a subscriber replays from the durable log — hundreds
// of small output frames delivered near-instantly — into a handful of frames,
// which keeps the hub's per-stream tunnel buffer from overflowing on replay. It
// never delays live output: it only merges events already present in one
// Events() batch, so steady-state streaming (one output frame per wakeup) is
// unchanged.
func coalesceOutputEvents(evs []TaskEvent) []TaskEvent {
	if len(evs) < 2 {
		return evs
	}
	out := make([]TaskEvent, 0, len(evs))
	var cur *TaskEvent // in-progress merged output chunk, nil when none open
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	start := func(ev TaskEvent) {
		e := ev
		e.Output = append([]byte(nil), ev.Output...) // own the buffer; don't alias the source
		cur = &e
	}
	for _, ev := range evs {
		if ev.Kind != TaskEventOutput {
			flush()
			out = append(out, ev)
			continue
		}
		if cur == nil {
			start(ev)
			continue
		}
		// Never split a single event's bytes: if appending would exceed the
		// chunk size, close the current chunk and open a new one.
		if len(cur.Output)+len(ev.Output) > outFlushBytes {
			flush()
			start(ev)
			continue
		}
		cur.Output = append(cur.Output, ev.Output...)
		cur.Seq = ev.Seq // advance to the last merged event's seq
	}
	flush()
	return out
}

// Subscribe streams a task's events with seq > fromSeq, in order, exactly once,
// by tailing the durable event log. For a finished task it replays the log from
// fromSeq and closes. The returned channel is closed when the task ends, the
// caller unsubscribes, or the context-free stop func runs.
//
// Consecutive output events read in one batch are coalesced (see
// coalesceOutputEvents) so a replay burst does not overflow the hub's bounded
// per-stream tunnel buffer; this is byte-lossless and keeps seq cursors valid.
func (r *Runner) Subscribe(id string, fromSeq uint64) (<-chan TaskEvent, func(), error) {
	out := make(chan TaskEvent, 64)
	r.mu.Lock()
	at := r.active[id]
	r.mu.Unlock()

	if at == nil {
		// Already terminal (or unknown): replay the log, then close.
		done := make(chan struct{})
		var once sync.Once
		stop := func() { once.Do(func() { close(done) }) }
		go func() {
			defer close(out)
			evs, _ := r.opts.Events.Events(id, fromSeq)
			for _, ev := range coalesceOutputEvents(evs) {
				select {
				case out <- ev:
				case <-done:
					return
				}
			}
		}()
		return out, stop, nil
	}

	notify := make(chan struct{}, 1)
	at.mu.Lock()
	at.notifs = append(at.notifs, notify)
	at.mu.Unlock()

	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			at.mu.Lock()
			for i, n := range at.notifs {
				if n == notify {
					at.notifs = append(at.notifs[:i], at.notifs[i+1:]...)
					break
				}
			}
			at.mu.Unlock()
			close(done)
		})
	}

	go func() {
		defer close(out)
		cursor := fromSeq
		deliver := func() bool { // returns false if we should stop
			evs, _ := r.opts.Events.Events(id, cursor)
			for _, ev := range coalesceOutputEvents(evs) {
				select {
				case out <- ev:
					cursor = ev.Seq
				case <-done:
					return false
				}
			}
			return true
		}
		for {
			if !deliver() {
				return
			}
			select {
			case <-notify:
				// new event(s); loop re-reads the log
			case <-at.done:
				deliver() // drain anything appended up to finish, then close
				return
			case <-done:
				return
			}
		}
	}()
	return out, stop, nil
}

// publish appends the (already-redacted) event to the durable log and wakes
// subscribers. The log is the source of truth; the wakeup is only a hint, so a
// missed/dropped wakeup never loses an event (subscribers re-read the log).
func (r *Runner) publish(at *activeTask, ev TaskEvent) {
	ev.TaskID = at.id
	at.mu.Lock()
	ev.TurnID = at.turnID
	at.mu.Unlock()
	if ev.At.IsZero() {
		ev.At = r.now()
	}
	_, _ = r.opts.Events.Append(at.id, ev) // best-effort, mirrors Store.Save error handling
	at.mu.Lock()
	notifs := append([]chan struct{}(nil), at.notifs...)
	at.mu.Unlock()
	for _, n := range notifs {
		select {
		case n <- struct{}{}:
		default: // already signalled; subscriber will re-read the log
		}
	}
}

// ---- execution ----

func (r *Runner) execute(at *activeTask, run *TaskRun, spec Spec, actor actorCredential) {
	defer r.finish(at)

	r.setStatus(at, run, StatusPreparing)

	credentialCtx := WithCredentialActorToken(at.ctx, actor.token)
	prep, err := r.prepare(credentialCtx, run, spec)
	if err != nil {
		// prepare() already unwound any partial sandbox/secret/worktree state.
		if at.ctx.Err() != nil {
			r.finalize(at, run, cancellationStatus(at), cancellationError(at), nil)
		} else {
			r.finalize(at, run, StatusFailed, err.Error(), nil)
		}
		return
	}
	red := prep.redactor

	// cleanupCtx is independent of at.ctx so mandatory resource reclamation still
	// runs when the task is cancelled (at.ctx is Done precisely on cancel).
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCleanup()

	child, err := r.opts.Control.CreateChild(at.ctx, ChildSpec{
		AgentExecutable:          spec.AgentExecutable,
		TaskID:                   run.ID,
		AgentType:                run.AgentType,
		AgentProfile:             run.AgentProfile,
		ProfileSpec:              spec.ResolvedProfile,
		EffectiveModel:           resolvedProfileModel(spec.ResolvedProfile),
		PermissionPolicy:         run.PermissionPolicy,
		Cwd:                      prep.workspaceDir,
		Env:                      prep.env,
		RecordEnv:                prep.recordEnv,
		ExtraArgs:                append(append([]string{}, spec.ExtraArgs...), prep.agentExtraArgs...),
		OwnerTaskId:              run.ID,
		InstanceID:               run.AgentInstanceID,
		ExecutionIdentity:        run.ExecutionIdentity,
		LaunchID:                 run.LaunchID,
		AgentActorToken:          actor.token,
		AgentActorTokenExpiresAt: actor.expiresAt,
	})
	if err != nil {
		// Agent never started: nothing worth retaining for debug — full cleanup.
		_ = r.teardownSandbox(cleanupCtx, prep, false)
		if at.ctx.Err() != nil {
			r.finalize(at, run, cancellationStatus(at), cancellationError(at), nil)
		} else {
			r.finalize(at, run, StatusFailed, "start agent: "+err.Error(), nil)
		}
		return
	}
	run.ChildSessionId = child.ID()
	run.ChildSessionName = child.Name()
	run.TargetID = r.opts.Control.TargetID()
	if started, ok := child.(interface {
		ExecutionIdentity() principal.ExecutionIdentity
		LaunchID() string
	}); ok {
		run.ExecutionIdentity = started.ExecutionIdentity()
		run.IsolationBackend = run.ExecutionIdentity.IsolationBackend
		run.LaunchID = started.LaunchID()
	}

	sub := child.Follow(at.ctx)

	submitted, err := child.SubmitTracked(sourceTask, childPrompt(spec.Task, r.injectEnvelope(run.AgentType)))
	if err != nil {
		r.teardownChild(child, StatusFailed)
		_ = r.teardownSandbox(cleanupCtx, prep, false)
		if at.ctx.Err() != nil {
			r.finalize(at, run, cancellationStatus(at), cancellationError(at), nil)
		} else {
			r.finalize(at, run, StatusFailed, "submit task: "+err.Error(), nil)
		}
		return
	}
	run.TurnID = submitted.TurnID
	at.mu.Lock()
	at.turnID = submitted.TurnID
	at.mu.Unlock()
	if err := r.opts.Store.Save(run); err != nil {
		r.teardownChild(child, StatusFailed)
		_ = r.teardownSandbox(cleanupCtx, prep, false)
		r.finalize(at, run, StatusFailed, "persist accepted turn: "+err.Error(), nil)
		return
	}
	r.setStatus(at, run, StatusRunning)

	outcome, agentText, finalMsg, diags, usage, term := r.consume(at, run, spec, child, sub, submitted.TurnID, prep, red)

	// State-machine fidelity: collecting only precedes a real terminal outcome;
	// cancel routes through cancelling; timeout/stream-closed go straight to terminal.
	switch term {
	case termOutcome:
		r.setStatus(at, run, StatusCollecting)
	case termCancel:
		r.setStatus(at, run, StatusCancelling)
	}

	// Always collect best-effort content (changed_files/result) even on
	// cancel/timeout, using cleanupCtx so a cancelled at.ctx does not abort it.
	var changed []ChangedFile
	if spec.Workspace.Policy == WorkspaceGitWorktree {
		changed, _ = GitChangedFiles(cleanupCtx, prep.workspaceDir, prep.baseCommit)
	}

	res := AssembleResult(AssembleInput{
		Outcome:      outcome,
		AgentText:    agentText,
		FinalMessage: finalMsg,
		ChangedFiles: changed,
		Diagnostics:  diags,
		UsageJson:    usage,
		Granularity:  spec.ResultGranularity,
		TaskID:       run.ID,
	}, red)

	status, errMsg := judge(term, outcome, res)
	alignResultStatus(res, status)
	if errMsg != "" && status != StatusSucceeded {
		res.Diagnostics = append(res.Diagnostics, Diagnostic{Severity: SeverityError, Message: errMsg})
	}

	if spec.Mode == ModePersistent && child.AgentSessionID() != "" {
		// Persistent execution path (B2): the agent reached a state where its
		// provider session id is durable, so put the child to sleep — stopping
		// the runtime process while preserving session identity, replay, cwd,
		// launch params, and the AgentSessionID for a later provider resume —
		// and keep the durable sandbox (workspace/non-secret home). The task
		// itself still resolves to its judged status; the *instance* is now idle.
		// Invariant 3: without an AgentSessionID there is nothing to resume, so
		// fall through to the ephemeral teardown below.
		sleepReason := "task-terminal:" + string(status)
		if err := child.SetSleeping(sleepReason); err != nil {
			// SetSleeping failed: the process may still be running. Fall back to
			// a full teardown so we never leave a live process + owned session
			// behind as a leaked instance.
			res.Diagnostics = append(res.Diagnostics, Diagnostic{Severity: SeverityWarning, Message: "persistent sleep failed: " + err.Error()})
			r.teardownChild(child, status)
			_ = r.teardownSandbox(cleanupCtx, prep, false)
		} else {
			// Durable non-secret state is retained; only per-run secrets are
			// destroyed. keepDebug is irrelevant here because the sandbox tree
			// is intentionally kept for the next task on this instance.
			_ = r.teardownSandbox(cleanupCtx, prep, false)
		}
		r.finalize(at, run, status, errMsg, res)
		return
	}

	r.teardownChild(child, status)
	keepDebug := status == StatusFailed || status == StatusTimedOut
	_ = r.teardownSandbox(cleanupCtx, prep, keepDebug)

	r.finalize(at, run, status, errMsg, res)
}

// prepared bundles the resources resolved before the child session starts.
type prepared struct {
	sandbox      *Sandbox
	workspaceDir string
	repoCacheDir string
	baseCommit   string
	env          []string
	recordEnv    []string
	redactor     *Redactor

	agentExtraArgs []string
}

func (r *Runner) prepare(ctx context.Context, run *TaskRun, spec Spec) (p prepared, err error) {
	sb, err := PrepareSandbox(r.opts.AmuxDir, run.ID, run.AgentInstanceID, run.IsolationMode)
	if err != nil {
		return p, fmt.Errorf("prepare sandbox: %w", err)
	}
	p.sandbox = sb
	p.env = sb.BaseEnv()
	p.recordEnv = append([]string(nil), p.env...)

	// Unwind partial state on any error after the sandbox tree exists, so a failed
	// prepare never leaves secret material, a sandbox dir, or a worktree on disk.
	// Cleanup runs under a fresh context (not ctx, which may already be cancelled).
	defer func() {
		if err != nil {
			if p.repoCacheDir != "" && p.workspaceDir != "" {
				RemoveGitWorktree(context.Background(), p.repoCacheDir, p.workspaceDir)
			}
			_ = sb.Cleanup(false, false)
		}
	}()

	switch spec.Workspace.Policy {
	case WorkspaceGitWorktree:
		repoCache, commit := repoCacheAndCommit(r.opts.AmuxDir, spec)
		if repoCache == "" {
			return p, errors.New("git-worktree workspace requires a repo cache (workspace.local_dir or repo.cache_key)")
		}
		// A persistent instance reuses its durable sandbox across tasks; only
		// create the worktree on the first task. A surviving worktree (with a
		// .git file/link) means a prior task already populated it — reusing it
		// preserves the agent's durable workspace state. The base commit is
		// still resolved so changed_files can diff against it.
		if !persistentWorktreeReady(sb.Workspace) {
			base, gerr := PrepareGitWorktree(ctx, repoCache, sb.Workspace, commit)
			if gerr != nil {
				return p, fmt.Errorf("git worktree: %w", gerr)
			}
			p.baseCommit = base
		} else {
			base, _ := resolveBaseCommit(ctx, repoCache, commit)
			p.baseCommit = base
		}
		p.repoCacheDir = repoCache
		p.workspaceDir = sb.Workspace
	default: // temp-dir
		p.workspaceDir = sb.Workspace
	}

	credential, err := ResolveCredentialPlan(spec)
	if err != nil {
		return p, err
	}
	var runtimeEnv []string
	if r.opts.LaunchEnv != nil {
		runtimeEnv = append([]string(nil), r.opts.LaunchEnv(run.ExecutionIdentity)...)
	}
	credential = FilterCredentialPlanForEnv(credential, runtimeEnv)
	mat, err := MaterializeCredentialPlan(ctx, r.opts.Creds, credential, spec.Agent)
	if err != nil {
		return p, fmt.Errorf("credential_profile: %w", err)
	}
	r.updateRunCredentialProfiles(run, mat.Profiles)
	// long-lived credentials inject under weak isolation by default (v0.38);
	// legacy trusted_mode_ack remains on the wire but is ignored by the runner.
	credEnv, err := sb.Materialize(mat)
	if err != nil {
		return p, fmt.Errorf("materialize credential: %w", err)
	}
	p.env = append(p.env, credEnv...)
	trackedRuntimeEnv, err := sb.Materialize(CredentialMaterial{Env: runtimeEnv})
	if err != nil {
		return p, fmt.Errorf("materialize runtime env: %w", err)
	}
	p.env = appendEnvDefaults(p.env, trackedRuntimeEnv)
	if r.opts.MaterializeProfile != nil && spec.ResolvedProfile != nil {
		args, merr := r.opts.MaterializeProfile(spec.ResolvedProfile, sb)
		if merr != nil {
			return p, fmt.Errorf("materialize profile: %w", merr)
		}
		p.agentExtraArgs = args
	}
	if r.opts.MaterializeMCP != nil && !taskArgsContainAmuxMCP(run.AgentType, p.agentExtraArgs) && !sandboxHasAmuxMCP(run.AgentType, sb) {
		args, merr := r.opts.MaterializeMCP(run.AgentType, sb)
		if merr != nil {
			return p, fmt.Errorf("materialize mcp: %w", merr)
		}
		p.agentExtraArgs = append(p.agentExtraArgs, args...)
	}
	p.redactor = NewRedactor(sb.Secrets()...)
	return p, nil
}

func taskArgsContainAmuxMCP(agentType string, args []string) bool {
	return adapter.ArgsContainAmuxMCP(agentType, args)
}

func sandboxHasAmuxMCP(agentType string, sb *Sandbox) bool {
	if sb == nil {
		return false
	}
	return false
}

// termReason records why the consume loop returned, so judge can map it to a
// terminal status without re-reading provider state.
type termReason int

const (
	termOutcome      termReason = iota // got a normalized terminal outcome
	termTimeout                        // task timer fired before any terminal signal
	termCancel                         // execution context cancelled
	termStreamClosed                   // child event stream closed without a terminal signal
)

func (r *Runner) consume(at *activeTask, run *TaskRun, spec Spec, child ChildSession, sub <-chan agentstream.Event, turnID string, prep prepared, red *Redactor) (TerminalOutcome, string, string, []Diagnostic, []byte, termReason) {
	var (
		buf      bytes.Buffer
		out      outBuf
		diags    []Diagnostic
		usage    []byte
		finalMsg string
	)
	tools := make(map[string]ToolActivity)
	wl := r.whitelist(run, prep)
	flushTick := time.NewTicker(outFlushInterval)
	defer flushTick.Stop()

	flush := func() {
		if b := out.take(); len(b) > 0 {
			r.publish(at, TaskEvent{Kind: TaskEventOutput, Output: b})
		}
	}
	defer flush()
	var lastUsage json.RawMessage
	publishUsage := func(raw []byte) {
		projected := projectUsage(raw)
		if len(projected) == 0 || bytes.Equal(lastUsage, projected) {
			return
		}
		flush()
		lastUsage = projected
		r.publish(at, TaskEvent{Kind: TaskEventUsage, Usage: projected})
	}

	for {
		select {
		case <-at.ctx.Done():
			if errors.Is(context.Cause(at.ctx), errTaskTimedOut) {
				return TerminalOutcome{Category: OutcomeInterrupted, RawError: errTaskTimedOut.Error()}, buf.String(), finalMsg, diags, usage, termTimeout
			}
			return TerminalOutcome{Category: OutcomeCancelled}, buf.String(), finalMsg, diags, usage, termCancel
		case <-flushTick.C:
			flush()
		case resp := <-at.permResp:
			flush()
			if err := r.applyEscalatedResponse(at, child, resp, &diags); err != nil {
				return TerminalOutcome{Category: OutcomeError, RawError: redactStr(red, err.Error())}, buf.String(), finalMsg, diags, usage, termOutcome
			}
			r.setStatus(at, run, StatusRunning)
		case ev, ok := <-sub:
			if !ok {
				if errors.Is(context.Cause(at.ctx), errTaskTimedOut) {
					return TerminalOutcome{Category: OutcomeInterrupted, RawError: errTaskTimedOut.Error()}, buf.String(), finalMsg, diags, usage, termTimeout
				}
				if at.ctx.Err() != nil {
					return TerminalOutcome{Category: OutcomeCancelled}, buf.String(), finalMsg, diags, usage, termCancel
				}
				return TerminalOutcome{Category: OutcomeUnknown, RawError: "child stream closed"}, buf.String(), finalMsg, diags, usage, termStreamClosed
			}
			if ev.Turn == nil || ev.Turn.ID != turnID {
				continue
			}
			if change, ok := ev.Body.(agentstream.ToolChanged); ok {
				tool := projectToolActivity(change, red)
				// Tool output streaming often repeats an unchanged display header.
				// Keep only display changes, without persisting the raw output.
				if previous, exists := tools[tool.ID]; !exists || previous != tool {
					flush()
					tools[tool.ID] = tool
					r.publish(at, TaskEvent{Kind: TaskEventTool, Tool: &tool})
				}
			}
			r.ingest(ev, &buf, &out, &usage, red)
			if _, ok := ev.Body.(agentstream.UsageObserved); ok {
				publishUsage(usage)
			}
			if out.len() >= outFlushBytes {
				flush()
			}
			// A provider's native turn result (final message + optional usage),
			// surfaced before turn_done; record it as the native summary source.
			// Not agent transcript, not a terminal signal.
			if result, ok := ev.Body.(agentstream.ResultObserved); ok {
				finalMsg = result.Text
				if len(result.Usage) > 0 {
					usage = append(usage[:0], result.Usage...)
					publishUsage(usage)
				}
				continue
			}
			if permission, ok := ev.Body.(agentstream.PermissionRequested); ok {
				flush() // keep the permission event ordered after prior output
				if err := r.onPermission(at, run, child, wl, permission.Request, red, &diags); err != nil {
					return TerminalOutcome{Category: OutcomeError, RawError: redactStr(red, err.Error())}, buf.String(), finalMsg, diags, usage, termOutcome
				}
				continue
			}
			// A turn boundary that arrives while an escalated permission is still
			// awaiting the main agent's answer is NOT task completion (design
			// 状态语义): keep waiting for the response or the timeout.
			if at.pendingPerm != nil {
				continue
			}
			if finished, ok := ev.Body.(agentstream.TurnFinished); ok {
				return outcomeFromTerminal(finished.Terminal), buf.String(), finalMsg, diags, usage, termOutcome
			}
		}
	}
}

// injectEnvelope reports whether the child prompt should carry the fallback
// result envelope for this agent type: true unless the agent's provider emits a
// native turn result.
func (r *Runner) injectEnvelope(agentType string) bool {
	if r.opts.NativeResult == nil {
		return true
	}
	return !r.opts.NativeResult(agentType)
}

// ingest accumulates agent transcript text into buf (envelope fallback) and the
// coalescing out buffer (redacted, for the streamed/persisted output events),
// plus the latest usage payload. It no longer publishes per chunk.
func (r *Runner) ingest(ev agentstream.Event, buf *bytes.Buffer, out *outBuf, usage *[]byte, red *Redactor) {
	var text string
	switch body := ev.Body.(type) {
	case agentstream.AssistantDelta:
		text = body.Text
	case agentstream.AssistantMessage:
		text = body.Text
	case agentstream.UsageObserved:
		*usage = append((*usage)[:0], body.Usage...)
	}
	if text != "" {
		data := []byte(text)
		buf.Write(data)
		b := data
		if red != nil {
			b = red.Bytes(data)
		}
		out.add(append([]byte(nil), b...))
	}
}

// onPermission resolves a child permission request per policy. policy-allow /
// deny-all answer immediately; escalate-to-main records the pending request,
// enters waiting_permission and publishes a redacted copy for the main agent to
// answer (the answer arrives on at.permResp). Phase 1 supports one in-flight
// escalation: a second permission while one is pending is denied.
func (r *Runner) onPermission(at *activeTask, run *TaskRun, child ChildSession, wl Whitelist, permission agentstream.PermissionRequest, red *Redactor, diags *[]Diagnostic) error {
	if permission.IsUserInput() {
		return taskUserInputUnavailableError()
	}
	req := PermissionFromAgentEvent(permission)

	// Defensive: a child should not raise a new permission while one is still
	// escalated (it is blocked on the first), but if it does, deny the new one
	// rather than strand the pending request.
	if at.pendingPerm != nil {
		if optID := req.ResolveOptionId(false); optID != "" {
			if err := child.RespondPermission(req.PermissionId, optID); err != nil {
				return taskPermissionResponseError(req.PermissionId, err)
			}
		}
		*diags = append(*diags, Diagnostic{Severity: SeverityWarning, Message: "concurrent permission denied (Phase 1 allows one escalation at a time)"})
		return nil
	}

	d := r.resolver.Resolve(run.PermissionPolicy, wl, req)

	if d.Escalate {
		at.pendingPerm = &req
		at.mu.Lock()
		at.pendingPermID = req.PermissionId
		at.mu.Unlock()
		r.setStatus(at, run, StatusWaitingPermission)
		r.publish(at, TaskEvent{Kind: TaskEventPermission, Status: StatusWaitingPermission, Permission: redactPermission(req, red)})
		return nil
	}

	if d.RecordOnly {
		*diags = append(*diags, Diagnostic{Severity: SeverityInfo, Message: redactStr(red, d.Reason+permSuffix(req))})
	}

	optID := req.ResolveOptionId(d.Allow)
	if !d.Allow {
		*diags = append(*diags, Diagnostic{
			Severity: SeverityWarning,
			Message:  redactStr(red, "permission denied ("+d.Reason+")"+permSuffix(req)),
		})
	}
	if err := child.RespondPermission(req.PermissionId, optID); err != nil {
		return taskPermissionResponseError(req.PermissionId, err)
	}
	return nil
}

// redactPermission returns a copy of req safe to publish to the main agent: the
// raw provider payload is dropped and the human-visible fields are masked.
func redactPermission(req PermissionRequest, red *Redactor) *PermissionRequest {
	pub := req
	pub.Raw = nil
	pub.options = nil
	pub.Command = redactStr(red, pub.Command)
	pub.Tool = redactStr(red, pub.Tool)
	if len(pub.Paths) > 0 {
		paths := make([]string, len(pub.Paths))
		for i, p := range pub.Paths {
			paths[i] = redactStr(red, p)
		}
		pub.Paths = paths
	}
	return &pub
}

func redactStr(red *Redactor, s string) string {
	if red == nil {
		return s
	}
	return red.String(s)
}

// applyEscalatedResponse answers a previously-escalated permission with the main
// agent's decision.
func (r *Runner) applyEscalatedResponse(at *activeTask, child ChildSession, resp PermissionResponse, diags *[]Diagnostic) error {
	pending := at.pendingPerm
	at.pendingPerm = nil
	at.mu.Lock()
	at.pendingPermID = ""
	at.mu.Unlock()
	if pending == nil {
		return nil
	}
	if resp.PermissionId != "" && resp.PermissionId != pending.PermissionId {
		return nil
	}
	optID := resp.OptionId
	if optID == "" {
		optID = pending.ResolveOptionId(resp.Allow)
	}
	if err := child.RespondPermission(pending.PermissionId, optID); err != nil {
		return taskPermissionResponseError(pending.PermissionId, err)
	}
	if !resp.Allow {
		msg := "escalated permission denied by main"
		if resp.Reason != "" {
			msg += ": " + resp.Reason
		}
		*diags = append(*diags, Diagnostic{Severity: SeverityWarning, Message: msg})
	}
	return nil
}

func (r *Runner) whitelist(run *TaskRun, prep prepared) Whitelist {
	// SandboxRoot is the whole per-task sandbox tree (workspace + tmp + home),
	// so writes anywhere inside the sandbox are permitted while writes outside it
	// are denied. FSRead additionally covers the workspace and repo cache.
	wl := DefaultWhitelist(prep.sandbox.Root)
	wl.StrongIsolation = run.IsolationMode.IsStrong()
	wl.FSRead = append(wl.FSRead, prep.workspaceDir)
	if prep.repoCacheDir != "" {
		wl.FSRead = append(wl.FSRead, prep.repoCacheDir)
	}
	return wl
}

func (r *Runner) timeout(spec Spec) time.Duration {
	if spec.TimeoutSeconds > 0 {
		return time.Duration(spec.TimeoutSeconds) * time.Second
	}
	if r.opts.DefaultTimeout > 0 {
		return r.opts.DefaultTimeout
	}
	return defaultTaskTimeout
}

func (r *Runner) teardownChild(child ChildSession, status Status) {
	if status == StatusTimedOut || status == StatusCancelled {
		_ = child.Interrupt()
	}
	_ = child.Kill()
}

// teardownSandbox destroys per-run secrets unconditionally and applies the
// debug-retention policy: when keepDebug is true (failed/timed_out with an agent
// that actually ran) the workspace/home are retained for inspection; otherwise
// everything is removed. git worktrees are deregistered before removal. NOTE:
// debug-retained worktrees remain registered in the repo cache until the sandbox
// sweeper reclaims them after TTL (Phase 1 has no TTL sweep loop yet).
//
// For a persistent agent instance (prep.sandbox.InstanceID != "") the durable
// non-secret state is always retained: per-run secrets are destroyed, but the
// workspace, non-secret HOME/cache, and the git worktree are kept so the next
// task on this instance reuses the durable environment. keepDebug is ignored in
// this mode — durability is governed by instance identity, not by task outcome.
// This is the security boundary (invariant 6): long-lived auth material lives
// only in the per-run secret dir and is destroyed here; durable HOME never
// accumulates secrets.
func (r *Runner) teardownSandbox(ctx context.Context, prep prepared, keepDebug bool) error {
	if prep.sandbox == nil {
		return nil
	}
	if prep.sandbox.InstanceID != "" {
		// Persistent: destroy per-run secrets, keep durable non-secret state +
		// the worktree registration for reuse by the next task on this instance.
		return prep.sandbox.Cleanup(true, true)
	}
	if prep.repoCacheDir != "" && prep.workspaceDir != "" && !keepDebug {
		RemoveGitWorktree(ctx, prep.repoCacheDir, prep.workspaceDir)
	}
	return prep.sandbox.Cleanup(keepDebug, keepDebug)
}

// finalize records the terminal status, result and error, then publishes the
// final status + result to stream subscribers.
func (r *Runner) finalize(at *activeTask, run *TaskRun, status Status, errMsg string, res *TaskResult) {
	run.Status = status
	run.Error = errMsg
	run.Result = res
	run.FinishedAt = r.now()
	_ = r.opts.Store.Save(run)
	r.publish(at, TaskEvent{Kind: TaskEventStatus, Status: status})
	if res != nil {
		r.publish(at, TaskEvent{Kind: TaskEventResult, Status: status, Result: res})
	}
}

func (r *Runner) setStatus(at *activeTask, run *TaskRun, status Status) {
	run.Status = status
	if status == StatusRunning && run.StartedAt.IsZero() {
		run.StartedAt = r.now()
	}
	_ = r.opts.Store.Save(run)
	r.publish(at, TaskEvent{Kind: TaskEventStatus, Status: status})
}

func (r *Runner) finish(at *activeTask) {
	if at.timer != nil {
		at.timer.Stop()
	}
	at.cancel(errTaskFinished)
	r.mu.Lock()
	delete(r.active, at.id)
	r.mu.Unlock()
	close(at.done) // wakes subscriber pumps so they drain the log and close
}

func cancellationStatus(at *activeTask) Status {
	if errors.Is(context.Cause(at.ctx), errTaskTimedOut) {
		return StatusTimedOut
	}
	return StatusCancelled
}

func cancellationError(at *activeTask) string {
	if errors.Is(context.Cause(at.ctx), errTaskTimedOut) {
		return errTaskTimedOut.Error()
	}
	return ""
}

func (r *Runner) newRun(spec Spec, credentialProfile string, credentialProfiles []string, launchID string) *TaskRun {
	iso := defaultIsolation(spec.Isolation)
	identity := principal.LegacyHostIdentity(principal.SourceMCP)
	if spec.ExecutionIdentity != nil {
		identity = *spec.ExecutionIdentity
	}
	if strings.TrimSpace(launchID) == "" {
		// No control-plane identity (local callers): mint our own so the
		// launch still has a stable id for isolation bookkeeping.
		launchID = "launch-" + uuid.NewString()
	}
	return &TaskRun{
		ID:                 r.newID(),
		ParentSessionID:    spec.ParentSessionID,
		ParentTaskID:       spec.ParentTaskID,
		ParentTurnID:       spec.ParentTurnID,
		Mode:               defaultMode(spec.Mode),
		AgentType:          spec.Agent,
		AgentProfile:       resolvedProfileRef(spec.ResolvedProfile),
		AgentInstanceID:    spec.AgentInstanceID,
		ExecutionIdentity:  identity,
		LaunchID:           launchID,
		IsolationMode:      iso,
		CredentialProfile:  credentialProfile,
		CredentialProfiles: append([]string(nil), credentialProfiles...),
		PermissionPolicy:   defaultPolicy(spec.PermissionPolicy),
		WorkspacePolicy:    defaultWorkspace(spec.Workspace.Policy),
		ResultGranularity:  defaultGranularity(spec.ResultGranularity),
		SecurityLevel:      securityLevel(iso),
		Status:             StatusPending,
		Task:               spec.Task,
		TimeoutSeconds:     spec.TimeoutSeconds,
		RequestedAt:        r.now(),
	}
}

func resolvedProfileRef(profile *pb.AgentProfileSpec) string {
	if profile == nil {
		return ""
	}
	if ref := strings.TrimSpace(profile.Ref); ref != "" {
		return ref
	}
	if ref := strings.TrimSpace(profile.PinnedRef); ref != "" {
		return ref
	}
	name := strings.TrimSpace(profile.Name)
	if namespace := strings.TrimSpace(profile.Namespace); namespace != "" && name != "" {
		return namespace + "/" + name
	}
	return name
}

func resolvedProfileModel(profile *pb.AgentProfileSpec) string {
	if profile == nil {
		return ""
	}
	return strings.TrimSpace(profile.Model)
}

func (r *Runner) updateRunCredentialProfiles(run *TaskRun, profiles []string) {
	profiles = normalizeCredentialProfiles(profiles)
	if equalStringSlices(run.CredentialProfiles, profiles) {
		return
	}
	run.CredentialProfiles = profiles
	run.CredentialProfile = legacyCredentialProfileForProfiles(profiles)
	_ = r.opts.Store.Save(run)
}

// ---- judgement ----

// judge maps the consume-loop termination into a terminal TaskRun status per the
// design's 判定规则.
func judge(term termReason, oc TerminalOutcome, res *TaskResult) (Status, string) {
	switch term {
	case termCancel:
		return StatusCancelled, ""
	case termTimeout:
		return StatusTimedOut, "task timeout before terminal signal"
	case termStreamClosed:
		// The child stream closed without an explicit terminal turn. If a usable
		// result was already collected, treat it as success; otherwise fail.
		// "child_session_lost" is reserved for the reconciler/not-found path.
		if res.HasContent() {
			return StatusSucceeded, ""
		}
		return StatusFailed, "child stream closed before terminal signal"
	}
	// termOutcome
	switch oc.Category {
	case OutcomeSuccess:
		if res.HasContent() {
			return StatusSucceeded, ""
		}
		return StatusFailed, "missing_result"
	case OutcomeError:
		msg := oc.RawError
		if msg == "" {
			msg = "agent reported error"
		}
		return StatusFailed, msg
	case OutcomeCancelled:
		return StatusCancelled, ""
	case OutcomeInterrupted:
		return StatusCancelled, "interrupted"
	default: // unknown — design requires conservative handling, never silent success
		return StatusFailed, "unknown_outcome"
	}
}

// alignResultStatus derives the structured result status from the judged task
// status so the two can never disagree in a stored record. The control plane's
// outcome wins over any agent-supplied envelope status.
func alignResultStatus(res *TaskResult, status Status) {
	if res == nil {
		return
	}
	switch status {
	case StatusSucceeded:
		res.Status = ResultOK
	case StatusFailed, StatusTimedOut:
		res.Status = ResultFailed
	case StatusCancelled:
		res.Status = ResultPartial
	}
}

// ---- validation & defaults ----

func validateSpec(spec *Spec) error {
	if spec.Agent == "" {
		return errors.New("task: agent is required")
	}
	if spec.Task == "" {
		return errors.New("task: task prompt is required")
	}
	if spec.TimeoutSeconds < 0 {
		return errors.New("task: timeout_seconds must be non-negative")
	}
	switch defaultMode(spec.Mode) {
	case ModeEphemeral:
	case ModePersistent:
		// A persistent task needs an agent instance id: it is the durable handle
		// the control plane uses for sticky scheduling and child-session reuse.
		// Without it the runner cannot name a reusable session, so reject rather
		// than silently degrading to ephemeral.
		if strings.TrimSpace(spec.AgentInstanceID) == "" {
			return errors.New("task: mode \"persistent\" requires agent_instance_id")
		}
	default:
		return fmt.Errorf("task: unknown mode %q", spec.Mode)
	}
	iso := defaultIsolation(spec.Isolation)
	if iso.IsStrong() {
		return fmt.Errorf("task: isolation %q (strong) not available in Phase 1; use process or private-home", spec.Isolation)
	}
	switch iso {
	case IsolationProcess, IsolationPrivateHome:
	default:
		return fmt.Errorf("task: unknown isolation %q", spec.Isolation)
	}
	switch defaultPolicy(spec.PermissionPolicy) {
	case PolicyAllow, PolicyEscalateToMain, PolicyDenyAll:
	default:
		return fmt.Errorf("task: unknown permission_policy %q", spec.PermissionPolicy)
	}
	switch defaultWorkspace(spec.Workspace.Policy) {
	case WorkspaceTempDir, WorkspaceGitWorktree:
	default:
		return fmt.Errorf("task: workspace policy %q not supported in Phase 1 (temp-dir or git-worktree)", spec.Workspace.Policy)
	}
	if spec.Blocking && defaultPolicy(spec.PermissionPolicy) == PolicyEscalateToMain {
		return errors.New("task: escalate-to-main is incompatible with a blocking amux.task_run (MCP deadlock)")
	}
	return nil
}

type credentialSource string

const (
	credentialSourceExplicit credentialSource = "explicit"
	credentialSourceEnvs     credentialSource = "envs"
	credentialSourceEnvRefs  credentialSource = "env_refs"
	credentialSourceProvider credentialSource = "provider"
)

// CredentialMaterialRequest is one credential profile lookup within a
// CredentialPlan. EnvNames is empty only for the legacy explicit
// credential_profile path, which injects the whole profile.
type CredentialMaterialRequest struct {
	Profile          string
	EnvNames         []string
	Source           string
	SuggestedProfile string
	// LiteralEnv carries provider-owned literal keys (api_key_literal) that are
	// rendered directly into the run env and never resolved from any credential
	// store. When non-nil the request produces exactly these KEY=VALUE pairs
	// without consulting the provider.
	LiteralEnv map[string]string
}

// CredentialPlan is the resolved credential materialization plan for one
// task/run. LegacyProfile is kept for existing task display/store fields that
// can represent only one profile; it is empty when inferred refs require
// multiple profiles.
type CredentialPlan struct {
	LegacyProfile string
	Profiles      []string
	Requests      []CredentialMaterialRequest
}

// FilterCredentialPlanForEnv removes named credential requests already
// satisfied by a launch environment. Literal provider keys and the legacy
// whole-profile request are retained because they have explicit value/source
// semantics rather than acting as defaults.
func FilterCredentialPlanForEnv(plan CredentialPlan, env []string) CredentialPlan {
	present := make(map[string]struct{}, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.TrimSpace(key) != "" {
			present[key] = struct{}{}
		}
	}
	var out CredentialPlan
	for _, req := range plan.Requests {
		if len(req.EnvNames) == 0 || len(req.LiteralEnv) > 0 {
			out.Requests = append(out.Requests, req)
			continue
		}
		filtered := make([]string, 0, len(req.EnvNames))
		for _, envName := range req.EnvNames {
			if _, ok := present[envName]; !ok {
				filtered = append(filtered, envName)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		req.EnvNames = filtered
		out.Requests = append(out.Requests, req)
	}
	out.finish()
	return out
}

func appendEnvDefaults(env, defaults []string) []string {
	out := append([]string(nil), env...)
	present := make(map[string]struct{}, len(out))
	for _, entry := range out {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			present[key] = struct{}{}
		}
	}
	for _, entry := range defaults {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := present[key]; exists {
			continue
		}
		present[key] = struct{}{}
		out = append(out, entry)
	}
	return out
}

// ResolveCredentialPlan turns explicit credential_profile, profile envs,
// profile env_refs, and provider fallback into a materialization plan. It does
// not read secret values.
func ResolveCredentialPlan(spec Spec) (CredentialPlan, error) {
	explicit := strings.TrimSpace(spec.CredentialProfile)
	if explicit != "" {
		return CredentialPlan{
			LegacyProfile: explicit,
			Profiles:      []string{explicit},
			Requests:      []CredentialMaterialRequest{{Profile: explicit, Source: string(credentialSourceExplicit)}},
		}, nil
	}

	var plan CredentialPlan
	refs, err := credentialEnvRefs(spec)
	if err != nil {
		return CredentialPlan{}, err
	}
	for _, ref := range refs {
		if err := plan.addEnvRequest(ref.Profile, ref.Env, credentialSourceEnvRefs); err != nil {
			return CredentialPlan{}, err
		}
	}
	providerProfile, providerEnv := providerCredentialRef(spec.ResolvedProfile)
	if err := plan.addProfileEnvRequests(spec.ResolvedProfile); err != nil {
		return CredentialPlan{}, err
	}
	if providerLiteral := providerLiteralEnv(spec.ResolvedProfile); len(providerLiteral) > 0 {
		// api_key_literal makes the provider's own auth self-contained: the key
		// is rendered directly into the run env and is never requested from any
		// credential store, so a per-user setup is not required.
		plan.Requests = append(plan.Requests, CredentialMaterialRequest{
			LiteralEnv: providerLiteral,
			Source:     string(credentialSourceProvider),
		})
	} else if providerProfile != "" && !profileDeclaresCredentialEnv(spec.ResolvedProfile, providerEnv) {
		if err := plan.addEnvRequest(providerProfile, providerEnv, credentialSourceProvider); err != nil {
			return CredentialPlan{}, err
		}
	}
	plan.finish()
	return plan, nil
}

// providerLiteralEnv returns the KEY=VALUE entries a provider must inject
// directly, keyed by env name, when it declares api_key_literal. Returns nil
// when the provider has no literal key.
func providerLiteralEnv(profile *pb.AgentProfileSpec) map[string]string {
	if profile == nil || profile.Provider == nil {
		return nil
	}
	literal := strings.TrimSpace(profile.Provider.ApiKeyLiteral)
	envName := strings.TrimSpace(profile.Provider.ApiKeyEnv)
	if literal == "" || envName == "" {
		return nil
	}
	return map[string]string{envName: literal}
}

func credentialEnvRefs(spec Spec) ([]credentialref.EnvRef, error) {
	if spec.ResolvedProfile == nil {
		return nil, nil
	}
	return credentialref.ParseEnvRefs(spec.ResolvedProfile.EnvRefs)
}

func (p *CredentialPlan) addProfileEnvRequests(profile *pb.AgentProfileSpec) error {
	if profile == nil || len(profile.Envs) == 0 {
		return nil
	}
	literal := providerLiteralEnv(profile)
	for _, envName := range profile.Envs {
		envName = strings.TrimSpace(envName)
		if _, covered := literal[envName]; covered {
			// Provider-owned literal key: rendered directly, never resolved from
			// a credential store even if the profile also lists it in envs.
			continue
		}
		suggestedCredentialProfile := strings.TrimSpace(profile.CredentialEnvSuggestions[envName])
		if err := p.addGlobalEnvRequest(envName, suggestedCredentialProfile, credentialSourceEnvs); err != nil {
			return err
		}
	}
	return nil
}

func profileDeclaresCredentialEnv(profile *pb.AgentProfileSpec, envName string) bool {
	if profile == nil {
		return false
	}
	envName = strings.TrimSpace(envName)
	for _, declared := range profile.Envs {
		if strings.TrimSpace(declared) == envName {
			return true
		}
	}
	return false
}

func (p *CredentialPlan) addEnvRequest(profile, envName string, source credentialSource) error {
	profile = strings.TrimSpace(profile)
	envName = strings.TrimSpace(envName)
	if profile == "" || envName == "" {
		return nil
	}
	for i := range p.Requests {
		req := &p.Requests[i]
		if containsCredentialString(req.EnvNames, envName) && req.Profile != profile {
			return fmt.Errorf("task: credential env %s is provided by multiple profiles: %q and %q", envName, req.Profile, profile)
		}
		if req.Profile == profile {
			req.EnvNames = appendUniqueString(req.EnvNames, envName)
			return nil
		}
	}
	p.Requests = append(p.Requests, CredentialMaterialRequest{
		Profile:  profile,
		EnvNames: []string{envName},
		Source:   string(source),
	})
	return nil
}

func (p *CredentialPlan) addGlobalEnvRequest(envName, suggestedProfile string, source credentialSource) error {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return nil
	}
	for i := range p.Requests {
		req := &p.Requests[i]
		if containsCredentialString(req.EnvNames, envName) {
			return nil
		}
		if strings.TrimSpace(req.Profile) == "" && req.Source == string(source) &&
			strings.TrimSpace(req.SuggestedProfile) == strings.TrimSpace(suggestedProfile) {
			req.EnvNames = appendUniqueString(req.EnvNames, envName)
			return nil
		}
	}
	p.Requests = append(p.Requests, CredentialMaterialRequest{
		EnvNames:         []string{envName},
		Source:           string(source),
		SuggestedProfile: strings.TrimSpace(suggestedProfile),
	})
	return nil
}

func (p *CredentialPlan) finish() {
	p.Profiles = credentialProfiles(p.Requests)
	p.LegacyProfile = legacyCredentialProfile(p.Requests)
}

func providerCredentialRef(profile *pb.AgentProfileSpec) (string, string) {
	if profile == nil || profile.Provider == nil {
		return "", ""
	}
	provider := profile.Provider
	apiKeyEnv := strings.TrimSpace(provider.ApiKeyEnv)
	if apiKeyEnv == "" {
		return "", ""
	}
	credentialProfile := strings.TrimSpace(provider.CredentialProfile)
	if credentialProfile == "" {
		credentialProfile = strings.TrimSpace(provider.Platform)
	}
	return credentialProfile, apiKeyEnv
}

func legacyCredentialProfile(requests []CredentialMaterialRequest) string {
	profile := ""
	for _, req := range requests {
		if strings.TrimSpace(req.Profile) == "" {
			continue
		}
		if profile == "" {
			profile = req.Profile
			continue
		}
		if profile != req.Profile {
			return ""
		}
	}
	return profile
}

func credentialProfiles(requests []CredentialMaterialRequest) []string {
	var out []string
	for _, req := range requests {
		profile := strings.TrimSpace(req.Profile)
		if profile == "" || containsCredentialString(out, profile) {
			continue
		}
		out = append(out, profile)
	}
	return out
}

// MaterializeCredentialPlan reads every requested profile through provider,
// filters inferred refs to declared env names, and merges the result for
// Sandbox.Materialize.
func MaterializeCredentialPlan(ctx context.Context, provider CredentialProvider, plan CredentialPlan, agentType string) (CredentialMaterial, error) {
	var out CredentialMaterial
	for _, req := range plan.Requests {
		if strings.TrimSpace(req.Profile) == "" && len(req.EnvNames) == 0 && len(req.LiteralEnv) == 0 {
			continue
		}
		if len(req.LiteralEnv) > 0 {
			mat, merr := literalCredentialMaterial(req.LiteralEnv)
			if merr != nil {
				return CredentialMaterial{}, merr
			}
			out, merr = mergeCredentialMaterial(out, mat)
			if merr != nil {
				return CredentialMaterial{}, merr
			}
			continue
		}
		mat, err := materializeCredentialRequest(ctx, provider, req, agentType)
		if err != nil {
			if wrapped := credentialSetupError(err, req); wrapped != nil {
				err = wrapped
			}
			return CredentialMaterial{}, err
		}
		if len(req.EnvNames) > 0 {
			var ferr error
			mat, ferr = filterCredentialMaterial(mat, req.Profile, req.EnvNames)
			if ferr != nil {
				if wrapped := credentialSetupError(ferr, req); wrapped != nil {
					ferr = wrapped
				}
				return CredentialMaterial{}, ferr
			}
		}
		if strings.TrimSpace(req.Profile) != "" {
			mat.Profiles = appendUniqueString(mat.Profiles, req.Profile)
		}
		var merr error
		out, merr = mergeCredentialMaterial(out, mat)
		if merr != nil {
			return CredentialMaterial{}, merr
		}
	}
	return out, nil
}

// literalCredentialMaterial renders a provider's literal api key into env
// material without consulting any credential store. Deterministic ordering keeps
// injection (and redaction tracking) stable.
func literalCredentialMaterial(literalEnv map[string]string) (CredentialMaterial, error) {
	if len(literalEnv) == 0 {
		return CredentialMaterial{}, nil
	}
	env := make([]string, 0, len(literalEnv))
	for _, envName := range sortedCredentialMapKeys(literalEnv) {
		env = append(env, envName+"="+literalEnv[envName])
	}
	return CredentialMaterial{Env: env, Kind: "long-lived"}, nil
}

func materializeCredentialRequest(ctx context.Context, provider CredentialProvider, req CredentialMaterialRequest, agentType string) (CredentialMaterial, error) {
	if strings.TrimSpace(req.Profile) == "" && len(req.EnvNames) > 0 {
		lookup, ok := provider.(EnvLookupCredentialProvider)
		if !ok {
			return CredentialMaterial{}, fmt.Errorf("credential env lookup requires provider support for %s", strings.Join(req.EnvNames, ", "))
		}
		return lookup.MaterializeEnvByName(ctx, agentType, req.EnvNames)
	}
	if len(req.EnvNames) > 0 {
		if filtered, ok := provider.(EnvCredentialProvider); ok {
			return filtered.MaterializeEnv(ctx, req.Profile, agentType, req.EnvNames)
		}
	}
	return provider.Materialize(ctx, req.Profile, agentType)
}

func filterCredentialMaterial(mat CredentialMaterial, credentialProfile string, envNames []string) (CredentialMaterial, error) {
	needed := make(map[string]bool, len(envNames))
	for _, envName := range envNames {
		needed[envName] = false
	}
	filtered := make([]string, 0, len(envNames))
	for _, kv := range mat.Env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, want := needed[key]; want {
			filtered = append(filtered, kv)
			needed[key] = true
		}
	}
	for _, envName := range envNames {
		if !needed[envName] {
			if strings.TrimSpace(credentialProfile) == "" {
				return CredentialMaterial{}, fmt.Errorf("credential store does not define env %s", envName)
			}
			return CredentialMaterial{}, fmt.Errorf("credential profile %q does not define env %s", credentialProfile, envName)
		}
	}
	return CredentialMaterial{Env: filtered, Kind: mat.Kind, Profiles: append([]string(nil), mat.Profiles...)}, nil
}

func mergeCredentialMaterial(base, next CredentialMaterial) (CredentialMaterial, error) {
	base.Env = append(base.Env, next.Env...)
	if len(next.Files) > 0 {
		if base.Files == nil {
			base.Files = map[string]string{}
		}
		for name, content := range next.Files {
			if _, exists := base.Files[name]; exists {
				return CredentialMaterial{}, fmt.Errorf("credential file %q is provided more than once", name)
			}
			base.Files[name] = content
		}
	}
	base.Secrets = append(base.Secrets, next.Secrets...)
	for _, profile := range next.Profiles {
		base.Profiles = appendUniqueString(base.Profiles, profile)
	}
	if base.Kind == "" {
		base.Kind = next.Kind
	}
	return base, nil
}

func credentialSetupError(err error, req CredentialMaterialRequest) error {
	if err == nil || len(req.EnvNames) == 0 {
		return err
	}
	profile := strings.TrimSpace(req.Profile)
	envName := req.EnvNames[0]
	label := "setup"
	if profile == "" {
		var notFound *CredentialEnvNotFoundError
		if !errors.As(err, &notFound) {
			return err
		}
		profile = strings.TrimSpace(req.SuggestedProfile)
		envName = notFound.EnvName
		label = "suggested setup"
	}
	if profile == "" {
		return err
	}
	return fmt.Errorf("%w; %s: amux agent credential set %s %s", err, label, profile, envName)
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	if containsCredentialString(values, value) {
		return values
	}
	return append(values, value)
}

func containsCredentialString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func normalizeCredentialProfiles(profiles []string) []string {
	out := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		out = appendUniqueString(out, profile)
	}
	return out
}

func legacyCredentialProfileForProfiles(profiles []string) string {
	profiles = normalizeCredentialProfiles(profiles)
	if len(profiles) == 1 {
		return profiles[0]
	}
	return ""
}

func defaultMode(m Mode) Mode {
	if m == "" {
		return ModeEphemeral
	}
	return m
}

func defaultIsolation(i IsolationMode) IsolationMode {
	if i == "" {
		return IsolationProcess
	}
	return i
}

func defaultPolicy(p PermissionPolicy) PermissionPolicy {
	if p == "" {
		return PolicyAllow
	}
	return p
}

func defaultWorkspace(w WorkspacePolicy) WorkspacePolicy {
	if w == "" {
		return WorkspaceTempDir
	}
	return w
}

func defaultGranularity(g ResultGranularity) ResultGranularity {
	if g == "" {
		return GranularitySummary
	}
	return g
}

// DefaultIsolation, DefaultWorkspace and DefaultMode expose the Phase-1 spec
// defaults so an out-of-process scheduler (amuxd) can resolve the SAME effective
// isolation/workspace/mode this runner will apply when a Spec leaves these fields
// empty. Building the scheduler's candidate filter from these prevents
// scheduler/runner drift (a task scheduled onto a runtime that cannot host the
// defaulted isolation/workspace).
func DefaultIsolation(i IsolationMode) IsolationMode     { return defaultIsolation(i) }
func DefaultWorkspace(w WorkspacePolicy) WorkspacePolicy { return defaultWorkspace(w) }
func securityLevel(i IsolationMode) string {
	if i.IsStrong() {
		return "strong"
	}
	return "weak"
}

// repoCacheAndCommit resolves the repo to branch from for a git-worktree task.
// The local shim treats workspace.local_dir as the repo cache; a RepoSpec with a
// cache_key resolves under <amuxDir>/repos/<cache_key> (Phase 2 distributed cache).
func repoCacheAndCommit(amuxDir string, spec Spec) (string, string) {
	commit := ""
	cacheKey := ""
	if spec.Workspace.Repo != nil {
		commit = spec.Workspace.Repo.Commit
		cacheKey = strings.TrimSpace(spec.Workspace.Repo.CacheKey)
	}
	if spec.Workspace.LocalDir != "" {
		return spec.Workspace.LocalDir, commit
	}
	if cacheKey != "" {
		return filepath.Join(amuxDir, "repos", cacheKey), commit
	}
	return "", commit
}

func permSuffix(req PermissionRequest) string {
	switch {
	case req.Command != "":
		return " cmd=" + req.Command
	case req.Tool != "":
		return " tool=" + req.Tool
	default:
		return ""
	}
}

// ActiveCount includes accepted work through final child/sandbox cleanup.
func (r *Runner) ActiveCount() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.active) }
