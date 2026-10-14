package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// ---- fakes ----

type fakeChild struct {
	id, name string
	cwd      string

	env              []string
	recordEnv        []string
	extraArgs        []string
	agentProfile     string
	profileSpec      *pb.AgentProfileSpec
	effectiveModel   string
	permissionPolicy PermissionPolicy
	instanceID       string
	actorToken       string

	mu             sync.Mutex
	subs           []chan agentstream.Event
	submitted      []string
	responded      []respRecord
	interrupted    bool
	killed         bool
	agentSessionID string
	sleeping       bool
	sleepReason    string
	turnSeq        int
	currentTurnID  string

	onSubmit   func(c *fakeChild, text string)
	onRespond  func(c *fakeChild, permID, optID string)
	respondErr error
}

type respRecord struct{ permID, optID string }

func newFakeChild(spec ChildSpec) *fakeChild {
	return &fakeChild{
		id:               "child-" + spec.TaskID,
		name:             "sess-" + spec.TaskID,
		cwd:              spec.Cwd,
		env:              append([]string(nil), spec.Env...),
		recordEnv:        append([]string(nil), spec.RecordEnv...),
		extraArgs:        spec.ExtraArgs,
		agentProfile:     spec.AgentProfile,
		profileSpec:      spec.ProfileSpec,
		effectiveModel:   spec.EffectiveModel,
		permissionPolicy: spec.PermissionPolicy,
		instanceID:       spec.InstanceID,
		actorToken:       spec.AgentActorToken,
	}
}

func (c *fakeChild) ID() string   { return c.id }
func (c *fakeChild) Name() string { return c.name }

func (c *fakeChild) Follow(_ context.Context) <-chan agentstream.Event {
	ch := make(chan agentstream.Event, 256)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	return ch
}

func (c *fakeChild) emit(ev agentstream.Event) {
	c.mu.Lock()
	if ev.Turn == nil {
		if _, sessionScoped := ev.Body.(agentstream.SessionPayload); !sessionScoped {
			ev.Turn = &agentstream.TurnRef{ID: c.currentTurnID, Seq: uint64(c.turnSeq)}
		}
	}
	subs := append([]chan agentstream.Event(nil), c.subs...)
	if metadata, ok := ev.Body.(agentstream.SessionMetadataChanged); ok && metadata.AgentSessionID != "" {
		c.agentSessionID = metadata.AgentSessionID
	}
	c.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (c *fakeChild) AgentSessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agentSessionID
}

func (c *fakeChild) SetSleeping(reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeping = true
	c.sleepReason = reason
	return nil
}

func (c *fakeChild) wasSleeping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sleeping
}

func (c *fakeChild) SubmitAsync(_, text string) error {
	c.mu.Lock()
	c.turnSeq++
	c.currentTurnID = fmt.Sprintf("turn-%d", c.turnSeq)
	c.submitted = append(c.submitted, text)
	cb := c.onSubmit
	c.mu.Unlock()
	if cb != nil {
		go cb(c, text)
	}
	return nil
}

func (c *fakeChild) SubmitTracked(source, text string) (TurnSubmission, error) {
	if err := c.SubmitAsync(source, text); err != nil {
		return TurnSubmission{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return TurnSubmission{TurnID: c.currentTurnID}, nil
}

func (c *fakeChild) RespondPermission(permID, optID string) error {
	c.mu.Lock()
	c.responded = append(c.responded, respRecord{permID, optID})
	cb := c.onRespond
	err := c.respondErr
	c.mu.Unlock()
	if cb != nil && err == nil {
		go cb(c, permID, optID)
	}
	return err
}
func (c *fakeChild) RespondPermissionMulti(permID string, optIDs []string) error {
	c.mu.Lock()
	c.responded = append(c.responded, respRecord{permID, strings.Join(optIDs, ",")})
	c.mu.Unlock()
	return nil
}

func (c *fakeChild) Interrupt() error {
	c.mu.Lock()
	c.interrupted = true
	c.mu.Unlock()
	return nil
}

func (c *fakeChild) Kill() error {
	c.mu.Lock()
	c.killed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeChild) responses() []respRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]respRecord(nil), c.responded...)
}

func (c *fakeChild) submittedText(i int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i < 0 || i >= len(c.submitted) {
		return ""
	}
	return c.submitted[i]
}

func (c *fakeChild) wasKilled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killed
}

func (c *fakeChild) wasInterrupted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interrupted
}

type fakeController struct {
	target   string
	mu       sync.Mutex
	created  []*fakeChild
	tornDown []string
	onCreate func(c *fakeChild)
}

func (f *fakeController) TargetID() string { return f.target }

func (f *fakeController) CreateChild(_ context.Context, spec ChildSpec) (ChildSession, error) {
	c := newFakeChild(spec)
	if f.onCreate != nil {
		f.onCreate(c)
	}
	f.mu.Lock()
	f.created = append(f.created, c)
	f.mu.Unlock()
	return c, nil
}

func (f *fakeController) TeardownChild(_ context.Context, sessionID string) error {
	f.mu.Lock()
	f.tornDown = append(f.tornDown, sessionID)
	f.mu.Unlock()
	return nil
}

func (f *fakeController) teardowns() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tornDown...)
}

func (f *fakeController) last() *fakeChild {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		return nil
	}
	return f.created[len(f.created)-1]
}

// agentText returns canonical agent transcript output.
func agentText(s string) agentstream.Event {
	return agentstream.Event{Body: agentstream.AssistantDelta{Text: s}}
}

func turnDone(stop string) agentstream.Event {
	return agentstream.Event{Body: agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed", Message: stop}}}
}

func newTestRunner(t *testing.T, ctrl SessionController, deflt time.Duration) *Runner {
	t.Helper()
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		DefaultTimeout: deflt,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

type actorCapturingCredentialProvider struct {
	tokens chan string
}

func (p actorCapturingCredentialProvider) Materialize(ctx context.Context, profile, _ string) (CredentialMaterial, error) {
	p.tokens <- CredentialActorToken(ctx)
	return CredentialMaterial{Env: []string{"API_KEY=secret"}, Profiles: []string{profile}}, nil
}

func TestRunnerScopesCredentialMaterializationWithLaunchActorToken(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"credential used"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	provider := actorCapturingCredentialProvider{tokens: make(chan string, 1)}
	r, err := NewRunner(Options{
		AmuxDir: t.TempDir(), Store: NewMemStore(), Control: ctrl, Creds: provider,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "claude", Task: "use credential", CredentialProfile: "openai-compat",
	}, "launch-1", "principal-token", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s, error = %s", final.Status, final.Error)
	}
	select {
	case token := <-provider.tokens:
		if token != "principal-token" {
			t.Fatalf("credential actor token = %q, want principal-token", token)
		}
	default:
		t.Fatal("credential provider was not called")
	}
}

// waitTerminal polls the store until the task is terminal or the deadline hits.
func waitTerminal(t *testing.T, r *Runner, id string) *TaskRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := r.Get(id)
		if err == nil && run.Status.IsTerminal() {
			return run
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach terminal state in time", id)
	return nil
}

// ---- tests ----

func TestRunnerSuccessWithEnvelope(t *testing.T) {
	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`working... {"amux_task_result":{"status":"ok","summary":"did the thing"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)

	run, err := r.Submit(Spec{Agent: "claude", Task: "do it"}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	if final.Result == nil || final.Result.Summary != "did the thing" {
		t.Fatalf("result = %+v", final.Result)
	}
	if final.TargetID != "local" || final.ChildSessionId == "" {
		t.Fatalf("ref not recorded: %+v", final.Ref())
	}
	if !ctrl.last().wasKilled() {
		t.Fatal("child should be killed at terminal")
	}
}

// TestSubmitLaunchIDAndActorCredential pins the hub-supplied launch identity
// channel. When the control plane passes a launch id, the run uses it verbatim
// (the hub minted the actor credential for exactly that launch), and the
// credential reaches the child spec in memory only: it must never appear on the
// persisted TaskRun. Without a supplied id the runner generates its own, so
// local callers keep working.
func TestSubmitLaunchIDAndActorCredential(t *testing.T) {
	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)

	run, err := r.Submit(Spec{Agent: "claude", Task: "do it"}, "launch-hub-1", "tok-123", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if run.LaunchID != "launch-hub-1" {
		t.Fatalf("LaunchID = %q, want the hub-supplied launch-hub-1", run.LaunchID)
	}
	final := waitTerminal(t, r, run.ID)
	if final.LaunchID != "launch-hub-1" {
		t.Fatalf("persisted LaunchID = %q, want launch-hub-1", final.LaunchID)
	}
	child := ctrl.last()
	if child == nil {
		t.Fatal("no child created")
	}
	if child.actorToken != "tok-123" {
		t.Fatalf("child actor token = %q, want tok-123", child.actorToken)
	}
	// The persisted run (returned from the store) must not carry the credential.
	data, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("marshal run: %v", err)
	}
	if strings.Contains(string(data), "tok-123") {
		t.Fatalf("persisted TaskRun leaks the actor credential: %s", data)
	}

	// No hub identity: the runner generates its own launch id and the child
	// carries no credential.
	run2, err := r.Submit(Spec{Agent: "claude", Task: "again"}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !strings.HasPrefix(run2.LaunchID, "launch-") || run2.LaunchID == "launch-hub-1" {
		t.Fatalf("generated LaunchID = %q, want a fresh launch-* id", run2.LaunchID)
	}
	waitTerminal(t, r, run2.ID)
	if got := ctrl.last().actorToken; got != "" {
		t.Fatalf("child actor token without a hub credential = %q, want empty", got)
	}
}

func TestRunnerRecordsResolvedAgentProfile(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"reviewed"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, err := r.Submit(Spec{
		Agent:           "claude",
		Task:            "review",
		ResolvedProfile: &pb.AgentProfileSpec{Ref: "team/reviewer", Type: "claude"},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Cancel(run.ID); err != nil {
			t.Errorf("Cancel: %v", err)
			return
		}
		waitTerminal(t, r, run.ID)
	})
	if run.AgentProfile != "team/reviewer" {
		t.Fatalf("AgentProfile = %q, want team/reviewer", run.AgentProfile)
	}
	waitTerminal(t, r, run.ID)
}

func TestRunnerIgnoresTerminalFromAnotherChildTurn(t *testing.T) {
	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			stale := turnDone("end_turn")
			stale.Turn = &agentstream.TurnRef{ID: "previous-turn", Seq: 1}
			c.emit(stale)
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"current turn"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)

	run, err := r.Submit(Spec{Agent: "claude", Task: "do it"}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded || final.Result == nil || final.Result.Summary != "current turn" {
		t.Fatalf("task completed from stale turn: status=%s error=%q result=%+v", final.Status, final.Error, final.Result)
	}
	if final.TurnID != "turn-1" {
		t.Fatalf("TaskRun TurnID = %q, want turn-1", final.TurnID)
	}
	events, err := r.opts.Events.Events(run.ID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.TurnID == "turn-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("task events did not retain accepted turn: %+v", events)
	}
}

// TestRunnerInjectsEnvelopeInstruction guards the fallback result contract: the
// runner must arrange the child prompt to emit the amux_task_result envelope
// (design "降级 fallback：文本 result envelope" / "约定 child prompt 输出 envelope").
// Without it, an agent that answers a read-only task in prose reaches terminal
// success with no collectable summary and is wrongly judged failed:missing_result.
func TestRunnerInjectsEnvelopeInstruction(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "codex", Task: "summarize the directory"}, "", "", 0)
	waitTerminal(t, r, run.ID)

	got := ctrl.last().submittedText(0)
	if !strings.Contains(got, "summarize the directory") {
		t.Fatalf("child prompt dropped the user task: %q", got)
	}
	if !strings.Contains(got, "amux_task_result") {
		t.Fatalf("child prompt missing envelope instruction: %q", got)
	}
}

func TestRunnerSuccessMissingResultFails(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(turnDone("end_turn")) // terminal success but no content
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusFailed || final.Error != "missing_result" {
		t.Fatalf("expected missing_result failure, got status=%s err=%s", final.Status, final.Error)
	}
}

func TestRunnerErrorOutcome(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentstream.Event{Body: agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "provider_error", Message: "boom"}}})
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "codex", Task: "x"}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusFailed {
		t.Fatalf("status = %s", final.Status)
	}
	if !strings.Contains(final.Error, "boom") {
		t.Fatalf("error = %q", final.Error)
	}
}

func TestRunnerPolicyAllowAutoApproves(t *testing.T) {
	ctrl := &fakeController{}
	perm := permissionEvent("p1", "Bash", "git status")
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(perm)
		}
		c.onRespond = func(c *fakeChild, _, _ string) {
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"ok"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x", PermissionPolicy: PolicyAllow}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	resp := ctrl.last().responses()
	if len(resp) != 1 || resp[0].optID != "allow" {
		t.Fatalf("expected allow response, got %+v", resp)
	}
}

func TestRunnerPolicyAllowDoesNotRestrictExecByDefault(t *testing.T) {
	ctrl := &fakeController{}
	perm := permissionEvent("p1", "Bash", "example-cli auth login --begin")
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(perm)
		}
		c.onRespond = func(c *fakeChild, _, _ string) {
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"ok"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "codex", Task: "x", PermissionPolicy: PolicyAllow}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	resp := ctrl.last().responses()
	if len(resp) != 1 || resp[0].optID != "allow" {
		t.Fatalf("expected allow response, got %+v", resp)
	}
}

func TestRunnerDenyAllRejects(t *testing.T) {
	ctrl := &fakeController{}
	perm := permissionEvent("p1", "Bash", "curl evil.com")
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(perm)
		}
		c.onRespond = func(c *fakeChild, _, _ string) {
			// agent ends without producing content after being denied
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x", PermissionPolicy: PolicyDenyAll}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	resp := ctrl.last().responses()
	if len(resp) != 1 || resp[0].optID != "deny" {
		t.Fatalf("expected deny response, got %+v", resp)
	}
	// denied + no content => failed missing_result, with a diagnostic recorded.
	if final.Result == nil || len(final.Result.Diagnostics) == 0 {
		t.Fatalf("expected a denial diagnostic, result=%+v", final.Result)
	}
}

func TestRunnerEscalateToMain(t *testing.T) {
	ctrl := &fakeController{}
	perm := permissionEvent("p9", "Write", "")
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(perm)
		}
		c.onRespond = func(c *fakeChild, _, _ string) {
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"wrote"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x", PermissionPolicy: PolicyEscalateToMain}, "", "", 0)

	stream, unsub, _ := r.Subscribe(run.ID, 0)
	defer unsub()

	// Wait for the escalation event, then answer allow.
	gotEscalation := false
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				break loop
			}
			if ev.Kind == TaskEventPermission {
				gotEscalation = true
				if err := r.RespondPermission(run.ID, PermissionResponse{PermissionId: "p9", Allow: true}); err != nil {
					t.Fatalf("RespondPermission: %v", err)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for escalation")
		}
		if gotEscalation {
			break
		}
	}

	final := waitTerminal(t, r, run.ID)
	if !gotEscalation {
		t.Fatal("never saw escalation event")
	}
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	resp := ctrl.last().responses()
	if len(resp) != 1 || resp[0].optID != "allow" {
		t.Fatalf("expected allow response, got %+v", resp)
	}
}

func TestRunnerTimeout(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText("thinking forever")) // never terminal
		}
	}
	r := newTestRunner(t, ctrl, 40*time.Millisecond)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusTimedOut {
		t.Fatalf("status = %s", final.Status)
	}
	if !ctrl.last().wasInterrupted() || !ctrl.last().wasKilled() {
		t.Fatalf("timeout should interrupt then kill: interrupted=%v killed=%v",
			ctrl.last().wasInterrupted(), ctrl.last().wasKilled())
	}
}

type blockingCreateController struct{}

func (blockingCreateController) CreateChild(ctx context.Context, _ ChildSpec) (ChildSession, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingCreateController) TeardownChild(context.Context, string) error { return nil }
func (blockingCreateController) TargetID() string                            { return "target-test" }

func TestRunnerTimeoutIncludesChildCreation(t *testing.T) {
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        blockingCreateController{},
		DefaultTimeout: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusTimedOut {
		t.Fatalf("status = %s, want timed_out", final.Status)
	}
}

func TestRunnerRejectsNegativeTimeout(t *testing.T) {
	spec := Spec{Agent: "claude", Task: "x", TimeoutSeconds: -1}
	if err := validateSpec(&spec); err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
		t.Fatalf("validateSpec error = %v, want timeout_seconds error", err)
	}
}

// TestRunnerDefaultTimeout guards the fallback when neither Spec.TimeoutSeconds
// nor Options.DefaultTimeout is set: the turn must be bounded by
// defaultTaskTimeout (currently 30 minutes). This is the production default for
// expert agent tasks dispatched via MCP, where amuxlet does not inject
// Options.DefaultTimeout.
func TestRunnerDefaultTimeout(t *testing.T) {
	ctrl := &fakeController{}
	r, err := NewRunner(Options{
		AmuxDir: t.TempDir(),
		Store:   NewMemStore(),
		Control: ctrl,
		// DefaultTimeout intentionally zero → exercise the hardcode fallback.
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if got := r.timeout(Spec{}); got != defaultTaskTimeout {
		t.Fatalf("default timeout = %s, want %s (30m)", got, defaultTaskTimeout)
	}
	if defaultTaskTimeout != 30*time.Minute {
		t.Fatalf("defaultTaskTimeout drifted = %s, want 30m", defaultTaskTimeout)
	}
	// Explicit spec value still wins over the fallback.
	if got := r.timeout(Spec{TimeoutSeconds: 120}); got != 2*time.Minute {
		t.Fatalf("spec timeout = %s, want 2m", got)
	}
}

func TestRunnerCancel(t *testing.T) {
	ctrl := &fakeController{}
	started := make(chan struct{})
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText("running")) // no terminal; we cancel
			close(started)
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("task never started")
	}
	if err := r.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("status = %s", final.Status)
	}
}

// TestRunnerGitWorktreeChangedFiles drives a git-worktree task end to end: the
// runner prepares a worktree, the (fake) child writes a file into it, and the
// runner derives changed_files from the workspace diff — not from agent text.
func TestRunnerGitWorktreeChangedFiles(t *testing.T) {
	repo := initGitRepo(t)
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			// child writes a new file into its provisioned worktree
			if err := os.WriteFile(filepath.Join(c.cwd, "new.txt"), []byte("hi"), 0o644); err != nil {
				t.Errorf("write in worktree: %v", err)
			}
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"added file"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, err := r.Submit(Spec{
		Agent:     "claude",
		Task:      "add a file",
		Workspace: WorkspaceSpec{Policy: WorkspaceGitWorktree, LocalDir: repo},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	if final.Result == nil || len(final.Result.ChangedFiles) != 1 || final.Result.ChangedFiles[0].Path != "new.txt" {
		t.Fatalf("changed_files = %+v", final.Result)
	}
	if final.Result.ChangedFiles[0].ChangeType != ChangeAdded {
		t.Fatalf("change type = %s", final.Result.ChangedFiles[0].ChangeType)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
	}
	return dir
}

func TestRepoCacheAndCommitUsesCacheKey(t *testing.T) {
	amuxDir := t.TempDir()
	spec := Spec{Workspace: WorkspaceSpec{Repo: &RepoSpec{CacheKey: "org/repo", Commit: "abc123"}}}
	repoCache, commit := repoCacheAndCommit(amuxDir, spec)
	if repoCache != filepath.Join(amuxDir, "repos", "org/repo") || commit != "abc123" {
		t.Fatalf("repoCacheAndCommit = %q/%q, want cache key path/abc123", repoCache, commit)
	}

	spec.Workspace.LocalDir = "/tmp/local-repo"
	repoCache, commit = repoCacheAndCommit(amuxDir, spec)
	if repoCache != "/tmp/local-repo" || commit != "abc123" {
		t.Fatalf("local_dir precedence = %q/%q, want /tmp/local-repo/abc123", repoCache, commit)
	}
}

func TestRunnerValidation(t *testing.T) {
	r := newTestRunner(t, &fakeController{}, time.Second)
	cases := []Spec{
		{Task: "no agent"},
		{Agent: "claude"}, // no task
		{Agent: "claude", Task: "x", Isolation: IsolationContainer},
		{Agent: "claude", Task: "x", Mode: ModePersistent}, // persistent without instance id
		{Agent: "claude", Task: "x", PermissionPolicy: PolicyEscalateToMain, Blocking: true},
		{Agent: "claude", Task: "x", Workspace: WorkspaceSpec{Policy: WorkspacePersistent}},
	}
	for i, c := range cases {
		if _, err := r.Submit(c, "", "", 0); err == nil {
			t.Fatalf("case %d: expected validation error for %+v", i, c)
		}
	}
}

// TestRunnerValidationPersistentWithInstance verifies that a persistent task
// carrying an agent instance id passes validation, and that the instance id
// propagates onto the TaskRun and into the ChildSpec.
func TestRunnerValidationPersistentWithInstance(t *testing.T) {
	ctrl := &fakeController{}
	r := newTestRunner(t, ctrl, time.Second)
	run, err := r.Submit(Spec{Agent: "claude", Task: "x", Mode: ModePersistent, AgentInstanceID: "inst-1"}, "", "", 0)
	if err != nil {
		t.Fatalf("persistent+instance should validate: %v", err)
	}
	if run.AgentInstanceID != "inst-1" {
		t.Fatalf("run.AgentInstanceID = %q, want inst-1", run.AgentInstanceID)
	}
	// The async execute creates the child; wait for it to surface the instance id.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctrl.mu.Lock()
		created := len(ctrl.created)
		ctrl.mu.Unlock()
		if created > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if len(ctrl.created) == 0 {
		t.Fatal("child was not created")
	}
	if got := ctrl.created[0].instanceID; got != "inst-1" {
		t.Fatalf("child instance id = %q, want inst-1", got)
	}
}

func TestRunnerWatchActiveInstanceCoversPendingTaskUntilFinish(t *testing.T) {
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        blockingCreateController{},
		DefaultTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{Agent: "claude", Task: "x", Mode: ModePersistent, AgentInstanceID: "inst-1"}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done, active := r.WatchActiveInstance("inst-1")
	if !active || done == nil {
		t.Fatal("WatchActiveInstance did not expose the pending persistent task")
	}
	if err := r.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("active-instance watch did not close when task finished")
	}
	if _, active := r.WatchActiveInstance("inst-1"); active {
		t.Fatal("WatchActiveInstance still reports a finished task")
	}
}

// agentSessionIDEvent emits the provider-side session id the way the real
// manager's event tap records it (a metadata-only event carrying AgentSessionID).
func agentSessionIDEvent(id string) agentstream.Event {
	return agentstream.Event{Body: agentstream.SessionMetadataChanged{AgentSessionID: id}}
}

// TestRunnerPersistentSleepsOnTerminalWithAgentSessionID guards the B2 timing
// constraint落地点: a persistent task that reaches a terminal state with a durable
// provider session id puts its child to sleep (keeping the instance) instead of
// killing it and tearing down the sandbox.
func TestRunnerPersistentSleepsOnTerminalWithAgentSessionID(t *testing.T) {
	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentSessionIDEvent("claude-sess-1"))
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)

	run, err := r.Submit(Spec{Agent: "claude", Task: "x", Mode: ModePersistent, AgentInstanceID: "inst-1"}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	child := ctrl.last()
	if !child.wasSleeping() {
		t.Fatal("persistent child with AgentSessionID should be sleeping, not killed")
	}
	if child.wasKilled() {
		t.Fatal("persistent child should not be killed at terminal")
	}
	if child.AgentSessionID() != "claude-sess-1" {
		t.Fatalf("child AgentSessionID = %q, want claude-sess-1", child.AgentSessionID())
	}
	// The durable sandbox tree must be retained for the next task on this instance.
	if _, err := os.Stat(filepath.Join(r.opts.AmuxDir, "sandboxes", "instance-inst-1")); err != nil {
		t.Fatalf("durable sandbox not retained: %v", err)
	}
}

// TestRunnerPersistentFallsBackToTeardownWithoutAgentSessionID guards invariant 3:
// a persistent task that never acquired a provider session id has nothing to
// resume, so it falls back to the ephemeral teardown (kill + sandbox removal).
func TestRunnerPersistentFallsBackToTeardownWithoutAgentSessionID(t *testing.T) {
	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			// No agentSessionIDEvent: AgentSessionID stays empty.
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)

	run, err := r.Submit(Spec{Agent: "claude", Task: "x", Mode: ModePersistent, AgentInstanceID: "inst-2"}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	child := ctrl.last()
	if child.wasSleeping() {
		t.Fatal("persistent child without AgentSessionID should NOT sleep")
	}
	if !child.wasKilled() {
		t.Fatal("persistent child without AgentSessionID should be killed (ephemeral fallback)")
	}
}

// TestRunnerEphemeralStillTearsDownAtTerminal confirms the ephemeral path is
// unchanged by the persistent fork: terminal state kills the child regardless of
// whether an AgentSessionID was emitted.
func TestRunnerEphemeralStillTearsDownAtTerminal(t *testing.T) {
	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentSessionIDEvent("claude-sess-9"))
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)

	run, err := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0) // ephemeral (default)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	child := ctrl.last()
	if child.wasSleeping() {
		t.Fatal("ephemeral child should never sleep")
	}
	if !child.wasKilled() {
		t.Fatal("ephemeral child should be killed at terminal")
	}
}

// staticCreds is a CredentialProvider that materializes a fixed env var and a
// secret file, so tests can assert the per-run secret dir is destroyed while
// the durable workspace is retained.
type staticCreds struct{}

func (staticCreds) Materialize(context.Context, string, string) (CredentialMaterial, error) {
	return CredentialMaterial{
		Env:   []string{"ANTHROPIC_API_KEY=sk-test-secret-value"},
		Files: map[string]string{"auth.json": `{"token":"sk-test-secret-value"}`},
	}, nil
}

// TestRunnerPersistentTeardownDestroysSecretRetainsWorkspace guards the
// durable/per-run secret split (Task 3.3, invariant 6): at a persistent task's
// terminal state the per-run secret materialization is destroyed, but the
// durable workspace (and HOME) are retained for the next task on the instance.
func TestRunnerPersistentTeardownDestroysSecretRetainsWorkspace(t *testing.T) {
	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentSessionIDEvent("claude-sess-7"))
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	amuxDir := t.TempDir()
	r, err := NewRunner(Options{
		AmuxDir:        amuxDir,
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          staticCreds{},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	run, err := r.Submit(Spec{Agent: "claude", Task: "x", Mode: ModePersistent, AgentInstanceID: "inst-s", CredentialProfile: "p"}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}

	root := filepath.Join(amuxDir, "sandboxes", "instance-inst-s")
	secretDir := filepath.Join(root, "secret")
	workspaceDir := filepath.Join(root, "workspace")
	if _, err := os.Stat(secretDir); !os.IsNotExist(err) {
		t.Fatalf("per-run secret dir must be destroyed at persistent terminal: %v", err)
	}
	if _, err := os.Stat(workspaceDir); err != nil {
		t.Fatalf("durable workspace must be retained: %v", err)
	}
	// The auth.json secret file must not survive.
	if _, err := os.Stat(filepath.Join(secretDir, "auth.json")); err == nil {
		t.Fatal("secret file must be destroyed at persistent terminal")
	}
}

// TestRunnerPersistentGitWorktreeRetainedAcrossTasks guards that a persistent
// git-worktree workspace is not deregistered at terminal state, so the next
// task on the instance reuses the same worktree instead of rebuilding it.
func TestRunnerPersistentGitWorktreeRetainedAcrossTasks(t *testing.T) {
	repo := initGitRepo(t)
	amuxDir := t.TempDir()

	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentSessionIDEvent("claude-sess-w"))
			c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        amuxDir,
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          NoCredentials{},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	spec := Spec{
		Agent: "claude", Task: "x", Mode: ModePersistent, AgentInstanceID: "inst-w",
		Workspace: WorkspaceSpec{Policy: WorkspaceGitWorktree, LocalDir: repo},
	}
	run, err := r.Submit(spec, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}

	workspace := filepath.Join(amuxDir, "sandboxes", "instance-inst-w", "workspace")
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		t.Fatalf("durable worktree must be retained: %v", err)
	}
	// The worktree must still be registered in the repo, proving teardownSandbox
	// did not deregister it for the persistent instance.
	listOut, lerr := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").CombinedOutput()
	if lerr != nil {
		t.Fatalf("git worktree list: %v: %s", lerr, listOut)
	}
	if !strings.Contains(string(listOut), workspace) {
		t.Fatalf("persistent worktree deregistered at terminal:\n%s", listOut)
	}
}

// permissionEvent builds the canonical permission shape consumed by Task.
func permissionEvent(permID, tool, command string) agentstream.Event {
	var input json.RawMessage
	if command != "" {
		input = json.RawMessage(`{"command":` + quote(command) + `}`)
	}
	return agentstream.Event{Body: agentstream.PermissionRequested{Request: agentstream.PermissionRequest{
		ID:       permID,
		ToolCall: &agentstream.PermissionToolCall{Name: tool, RawInput: input},
		Options: []agentstream.PermissionOption{
			{ID: "allow", Name: "Allow", Kind: "allow_once"},
			{ID: "deny", Name: "Deny", Kind: "reject_once"},
		},
	}}}
}

func quote(s string) string { return `"` + s + `"` }

// failingCreds is a CredentialProvider that always errors, to exercise the
// prepare-failure cleanup path.
type failingCreds struct{}

func (failingCreds) Materialize(context.Context, string, string) (CredentialMaterial, error) {
	return CredentialMaterial{}, errInjected
}

var errInjected = errInjectedT{}

type errInjectedT struct{}

func (errInjectedT) Error() string { return "injected credential failure" }

// TestRunnerPrepareFailureCleansSandbox guards that a prepare() failure (here a
// credential error) leaves no sandbox/secret directory on disk and fails the task.
func TestRunnerPrepareFailureCleansSandbox(t *testing.T) {
	ctrl := &fakeController{}
	amuxDir := t.TempDir()
	r, err := NewRunner(Options{
		AmuxDir:        amuxDir,
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          failingCreds{},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x", CredentialProfile: "p"}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusFailed {
		t.Fatalf("status = %s", final.Status)
	}
	sandboxDir := filepath.Join(amuxDir, "sandboxes", run.ID)
	if _, err := os.Stat(sandboxDir); !os.IsNotExist(err) {
		t.Fatalf("sandbox dir should be removed after prepare failure: %v", err)
	}
	if ctrl.last() != nil {
		t.Fatal("no child should be created when prepare fails")
	}
}

// TestRunnerEscalationRedacted guards that a secret embedded in an escalated
// permission command is masked in the published TaskEventPermission frame.
func TestRunnerEscalationRedacted(t *testing.T) {
	ctrl := &fakeController{}
	secret := "test-credential-value"
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(permissionEvent("p1", "Bash", "curl -H authorization:bearer "+secret))
		}
		c.onRespond = func(c *fakeChild, _, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"ok"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	// Inject the secret as a materialized credential so the redactor knows it.
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          fixedCreds{env: "TOKEN=" + secret},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x", PermissionPolicy: PolicyEscalateToMain}, "", "", 0)
	stream, unsub, _ := r.Subscribe(run.ID, 0)
	defer unsub()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				t.Fatal("stream closed before escalation")
			}
			if ev.Kind == TaskEventPermission {
				if ev.Permission == nil {
					t.Fatal("nil permission")
				}
				if strings.Contains(ev.Permission.Command, secret) {
					t.Fatalf("secret leaked in escalation command: %q", ev.Permission.Command)
				}
				if !strings.Contains(ev.Permission.Command, "[REDACTED]") {
					t.Fatalf("expected redaction mask, got %q", ev.Permission.Command)
				}
				_ = r.RespondPermission(run.ID, PermissionResponse{PermissionId: "p1", Allow: true})
				waitTerminal(t, r, run.ID)
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for escalation")
		}
	}
}

type fixedCreds struct {
	env string
	mat CredentialMaterial
}

func (f fixedCreds) Materialize(context.Context, string, string) (CredentialMaterial, error) {
	if len(f.mat.Env) > 0 || len(f.mat.Files) > 0 || f.mat.Kind != "" || len(f.mat.Secrets) > 0 {
		return f.mat, nil
	}
	return CredentialMaterial{Env: []string{f.env}, Kind: "short-lived"}, nil
}

type profileCreds map[string]CredentialMaterial

func (p profileCreds) Materialize(_ context.Context, profile, _ string) (CredentialMaterial, error) {
	mat, ok := p[profile]
	if !ok {
		return CredentialMaterial{}, fmt.Errorf("credential profile %q not found", profile)
	}
	return mat, nil
}

type lookupCreds map[string]CredentialMaterial

func (p lookupCreds) Materialize(_ context.Context, profile, _ string) (CredentialMaterial, error) {
	mat, ok := p[profile]
	if !ok {
		return CredentialMaterial{}, fmt.Errorf("credential profile %q not found", profile)
	}
	return mat, nil
}

func (p lookupCreds) MaterializeEnvByName(_ context.Context, _ string, envNames []string) (CredentialMaterial, error) {
	var out CredentialMaterial
	for _, envName := range envNames {
		var foundProfile string
		var foundValue string
		var foundKind string
		for profile, mat := range p {
			for _, kv := range mat.Env {
				key, value, ok := strings.Cut(kv, "=")
				if !ok || key != envName {
					continue
				}
				if foundProfile != "" && foundProfile != profile {
					return CredentialMaterial{}, fmt.Errorf("credential env %s is provided by multiple profiles: %q and %q", envName, foundProfile, profile)
				}
				foundProfile = profile
				foundValue = value
				foundKind = mat.Kind
			}
		}
		if foundProfile == "" {
			return CredentialMaterial{}, fmt.Errorf("credential store does not define env %s", envName)
		}
		out.Env = append(out.Env, envName+"="+foundValue)
		out.Profiles = appendUniqueString(out.Profiles, foundProfile)
		if out.Kind == "" {
			out.Kind = foundKind
		}
	}
	return out, nil
}

// TestRunnerCanonicalSuccessIgnoresTerminalMessage documents that Task trusts
// the canonical outcome and never reinterprets its human-readable message.
func TestRunnerCanonicalSuccessIgnoresTerminalMessage(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"has content"}}`))
			c.emit(turnDone("weird_reason"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
}

// TestJudgeConservative covers the judge() branches that map terminations to
// terminal statuses, including the conservative unknown and content-aware
// stream-closed rules.
func TestJudgeConservative(t *testing.T) {
	content := &TaskResult{Summary: "x"}
	empty := &TaskResult{}

	if s, _ := judge(termOutcome, TerminalOutcome{Category: OutcomeUnknown}, content); s != StatusFailed {
		t.Fatalf("unknown+content should fail conservatively, got %s", s)
	}
	if s, _ := judge(termOutcome, TerminalOutcome{Category: OutcomeSuccess}, empty); s != StatusFailed {
		t.Fatalf("success without content => missing_result failed, got %s", s)
	}
	if s, _ := judge(termStreamClosed, TerminalOutcome{}, content); s != StatusSucceeded {
		t.Fatalf("stream-closed WITH content => succeeded, got %s", s)
	}
	if s, msg := judge(termStreamClosed, TerminalOutcome{}, empty); s != StatusFailed || msg == "child_session_lost" {
		t.Fatalf("stream-closed without content should fail (not reconciler label), got %s/%s", s, msg)
	}
	if s, _ := judge(termTimeout, TerminalOutcome{}, empty); s != StatusTimedOut {
		t.Fatalf("timeout => timed_out, got %s", s)
	}
	if s, _ := judge(termCancel, TerminalOutcome{}, empty); s != StatusCancelled {
		t.Fatalf("cancel => cancelled, got %s", s)
	}
}

// TestRunnerRespondPermissionGating guards that RespondPermission is rejected
// when no permission is pending.
func TestRunnerRespondPermissionGating(t *testing.T) {
	ctrl := &fakeController{}
	block := make(chan struct{})
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText("running"))
			<-block // hold the task open (no terminal) until released
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	// Give the task a moment to reach running.
	time.Sleep(30 * time.Millisecond)
	if err := r.RespondPermission(run.ID, PermissionResponse{Allow: true}); err == nil {
		t.Fatal("expected error responding with no pending permission")
	}
	close(block)
	r.Cancel(run.ID)
	waitTerminal(t, r, run.ID)
}

// TestRunnerCancelRoutesThroughCancelling guards the state sequence
// running -> cancelling -> cancelled (cancelling is persisted/published).
func TestRunnerCancelRoutesThroughCancelling(t *testing.T) {
	ctrl := &fakeController{}
	started := make(chan struct{})
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText("running"))
			close(started)
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	stream, unsub, _ := r.Subscribe(run.ID, 0)
	defer unsub()
	<-started
	r.Cancel(run.ID)

	sawCancelling := false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				if !sawCancelling {
					t.Fatal("never observed cancelling state")
				}
				return
			}
			if ev.Kind == TaskEventStatus && ev.Status == StatusCancelling {
				sawCancelling = true
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
}

// TestRunnerRecoverOrphans guards that non-terminal tasks from a prior process
// are reconciled to failed at startup, and that an orphan whose child session
// survived the restart is torn down (the spec's "RecoverOrphans + 终态 teardown"
// session-orphan cleanup).
func TestRunnerRecoverOrphans(t *testing.T) {
	store := NewMemStore()
	_ = store.Save(&TaskRun{ID: "orphan", Status: StatusRunning, ChildSessionId: "sess-orphan"})
	_ = store.Save(&TaskRun{ID: "orphan-nosess", Status: StatusRunning})
	_ = store.Save(&TaskRun{ID: "done", Status: StatusSucceeded})
	ctrl := &fakeController{}
	r, err := NewRunner(Options{AmuxDir: t.TempDir(), Store: store, Control: ctrl})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	recovered, err := r.RecoverOrphans()
	if err != nil {
		t.Fatalf("RecoverOrphans: %v", err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recovered = %v, want 2", recovered)
	}
	got, _ := store.Get("orphan")
	if got.Status != StatusFailed {
		t.Fatalf("orphan status = %s", got.Status)
	}
	done, _ := store.Get("done")
	if done.Status != StatusSucceeded {
		t.Fatal("terminal task must not be touched")
	}
	// Only the orphan with a surviving child session is torn down.
	if td := ctrl.teardowns(); len(td) != 1 || td[0] != "sess-orphan" {
		t.Fatalf("teardowns = %v, want [sess-orphan]", td)
	}
	evs, _ := store.Events("orphan", 0)
	if len(evs) != 1 || evs[0].Status != StatusFailed {
		t.Fatalf("orphan recovery should append one terminal event, got %+v", evs)
	}
}

// TestRunnerSubscribeReplaysEarlyOutput: a subscriber that attaches AFTER some
// output has already streamed still receives that earlier output (impossible
// with the old in-memory, lastStatus-only replay).
func TestRunnerSubscribeReplaysEarlyOutput(t *testing.T) {
	ctrl := &fakeController{}
	release := make(chan struct{})
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText("alpha"))
			c.emit(agentText("beta"))
			<-release
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)

	time.Sleep(50 * time.Millisecond) // let alpha/beta stream before we subscribe
	stream, unsub, err := r.Subscribe(run.ID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()
	close(release)

	var out []byte
	sawResult := false
	for ev := range stream {
		if ev.Kind == TaskEventOutput {
			out = append(out, ev.Output...)
		}
		if ev.Kind == TaskEventResult {
			sawResult = true
		}
	}
	if !strings.Contains(string(out), "alpha") || !strings.Contains(string(out), "beta") {
		t.Fatalf("late subscriber missed early output: %q", out)
	}
	if !sawResult {
		t.Fatal("never saw result event")
	}
}

// TestRunnerSubscribeFinishedReplaysTranscript: subscribing AFTER a task is
// terminal replays the full transcript from the durable log.
func TestRunnerSubscribeFinishedReplaysTranscript(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText("transcript-body"))
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	waitTerminal(t, r, run.ID)

	stream, unsub, err := r.Subscribe(run.ID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()
	var out []byte
	var status Status
	for ev := range stream {
		if ev.Kind == TaskEventOutput {
			out = append(out, ev.Output...)
		}
		if ev.Kind == TaskEventStatus {
			status = ev.Status
		}
	}
	if !strings.Contains(string(out), "transcript-body") {
		t.Fatalf("finished-task replay missing transcript: %q", out)
	}
	if status != StatusSucceeded {
		t.Fatalf("final status = %s", status)
	}
}

// TestRunnerSubscribeResumeFromSeq: resuming from a cursor returns only events
// with seq strictly greater than the cursor, and fewer than a full replay.
func TestRunnerSubscribeResumeFromSeq(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText("one"))
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	waitTerminal(t, r, run.ID)

	s0, u0, _ := r.Subscribe(run.ID, 0)
	var maxSeq uint64
	n0 := 0
	for ev := range s0 {
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
		n0++
	}
	u0()
	if maxSeq < 2 {
		t.Fatalf("expected several events, maxSeq=%d", maxSeq)
	}

	s1, u1, _ := r.Subscribe(run.ID, maxSeq-1)
	defer u1()
	n1 := 0
	for ev := range s1 {
		n1++
		if ev.Seq <= maxSeq-1 {
			t.Fatalf("resume returned stale seq %d (<= %d)", ev.Seq, maxSeq-1)
		}
	}
	if n1 == 0 || n1 >= n0 {
		t.Fatalf("resume count n1=%d must be >0 and < n0=%d", n1, n0)
	}
}

// TestRunnerPermissionPayloadReplayable is the regression test for the cross-poll
// escalation bug: the permission payload must be replayable even when the
// subscriber attaches AFTER the escalation already fired.
func TestRunnerPermissionPayloadReplayable(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(permissionEvent("p1", "Bash", "git status"))
		}
		c.onRespond = func(c *fakeChild, _, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"ok"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x", PermissionPolicy: PolicyEscalateToMain}, "", "", 0)

	// Wait for waiting_permission WITHOUT subscribing (simulates a poll gap).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g, _ := r.Get(run.ID); g.Status == StatusWaitingPermission {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	stream, unsub, _ := r.Subscribe(run.ID, 0)
	defer unsub()
	var got *PermissionRequest
	timeout := time.After(2 * time.Second)
	for got == nil {
		select {
		case ev, ok := <-stream:
			if !ok {
				t.Fatal("stream closed before permission replay")
			}
			if ev.Kind == TaskEventPermission && ev.Permission != nil {
				got = ev.Permission
			}
		case <-timeout:
			t.Fatal("permission payload was not replayable after the fact")
		}
	}
	if got.Command != "git status" || got.Tool != "Bash" {
		t.Fatalf("replayed permission payload wrong: %+v", got)
	}
	_ = r.RespondPermission(run.ID, PermissionResponse{PermissionId: "p1", Allow: true})
	waitTerminal(t, r, run.ID)
}

// TestRunnerOutputCoalesced: many small output chunks are persisted as a few
// coalesced output events, with content preserved.
func TestRunnerOutputCoalesced(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			for i := 0; i < 5; i++ {
				c.emit(agentText("chunk"))
			}
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r := newTestRunner(t, ctrl, 2*time.Second)
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	waitTerminal(t, r, run.ID)

	stream, unsub, _ := r.Subscribe(run.ID, 0)
	defer unsub()
	var out []byte
	outEvents := 0
	for ev := range stream {
		if ev.Kind == TaskEventOutput {
			out = append(out, ev.Output...)
			outEvents++
		}
	}
	if n := strings.Count(string(out), "chunk"); n != 5 {
		t.Fatalf("content not preserved: %d chunks in %q", n, out)
	}
	if outEvents == 0 || outEvents > 3 {
		t.Fatalf("expected output coalesced into <=3 events, got %d", outEvents)
	}
}

// TestRunnerMaterializesProfile: when a Spec carries a ResolvedProfile and the
// runner has a MaterializeProfile hook, the hook runs with the sandbox and its
// returned extra args reach the child's ChildSpec.ExtraArgs (after the caller's
// own ExtraArgs).
func TestRunnerMaterializesProfile(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	var gotProfile *pb.AgentProfileSpec
	var gotHome string
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		DefaultTimeout: 2 * time.Second,
		MaterializeProfile: func(profile *pb.AgentProfileSpec, sb *Sandbox) ([]string, error) {
			gotProfile = profile
			gotHome = sb.Home
			return []string{"--append-system-prompt", profile.AgentsContent}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	spec := Spec{
		Agent:           "claude",
		Task:            "x",
		Isolation:       IsolationPrivateHome,
		ExtraArgs:       []string{"--model", "opus"},
		ResolvedProfile: &pb.AgentProfileSpec{Name: "cr", Type: "claude", Model: "opus", AgentsContent: "be terse"},
	}
	run, err := r.Submit(spec, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitTerminal(t, r, run.ID)

	if gotProfile == nil || gotProfile.AgentsContent != "be terse" {
		t.Fatalf("hook not called with profile: %+v", gotProfile)
	}
	if gotHome == "" {
		t.Fatalf("hook should receive a private-home sandbox (Home set)")
	}
	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if !hasArgSeq(child.extraArgs, "--model", "opus") || !hasArgSeq(child.extraArgs, "--append-system-prompt", "be terse") {
		t.Fatalf("child extraArgs missing caller/profile args: %v", child.extraArgs)
	}
	if child.effectiveModel != "opus" {
		t.Fatalf("child effective model = %q, want opus", child.effectiveModel)
	}
	if child.agentProfile != "cr" || child.profileSpec != spec.ResolvedProfile {
		t.Fatalf("child profile = %q / %+v, want resolved profile passthrough", child.agentProfile, child.profileSpec)
	}
}

func TestRunnerInfersProviderCredentialProfile(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          fixedCreds{mat: CredentialMaterial{Kind: "short-lived", Env: []string{"PROVIDER_API_KEY=ak-secret", "OTHER=ignored"}}},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "assistant",
			Type: "codex",
			Provider: &pb.AgentProfileProvider{
				Platform:          "provider",
				ApiKeyEnv:         "PROVIDER_API_KEY",
				CredentialProfile: "provider-prod",
			},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if run.CredentialProfile != "provider-prod" {
		t.Fatalf("credential profile = %q, want provider-prod", run.CredentialProfile)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if got := envValueForTest(child.env, "PROVIDER_API_KEY"); got != "ak-secret" {
		t.Fatalf("PROVIDER_API_KEY = %q, want injected secret", got)
	}
	if got := envValueForTest(child.env, "OTHER"); got != "" {
		t.Fatalf("OTHER = %q, want provider-inferred credentials filtered to api_key_env", got)
	}
}

func TestRunnerInfersCredentialProfileFromEnvRefs(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          fixedCreds{mat: CredentialMaterial{Kind: "short-lived", Env: []string{"GITHUB_TOKEN=gh-secret", "OTHER=ignored"}}},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name:    "repo-review",
			Type:    "codex",
			EnvRefs: []string{"credential:github:GITHUB_TOKEN"},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if run.CredentialProfile != "github" {
		t.Fatalf("credential profile = %q, want github", run.CredentialProfile)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if got := envValueForTest(child.env, "GITHUB_TOKEN"); got != "gh-secret" {
		t.Fatalf("GITHUB_TOKEN = %q, want injected secret", got)
	}
	if got := envValueForTest(child.env, "OTHER"); got != "" {
		t.Fatalf("OTHER = %q, want env-ref inferred credentials filtered to declared env", got)
	}
}

func TestRunnerDoesNotPersistCredentialEnvInChildSpec(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          fixedCreds{mat: CredentialMaterial{Kind: "short-lived", Env: []string{"GITHUB_TOKEN=gh-secret"}}},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name:    "repo-review",
			Type:    "codex",
			EnvRefs: []string{"credential:github:GITHUB_TOKEN"},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if got := envValueForTest(child.env, "GITHUB_TOKEN"); got != "gh-secret" {
		t.Fatalf("launch GITHUB_TOKEN = %q, want injected secret", got)
	}
	if got := envValueForTest(child.recordEnv, "GITHUB_TOKEN"); got != "" {
		t.Fatalf("record env persisted credential: %q", got)
	}
	if got := envValueForTest(child.recordEnv, "TMPDIR"); got == "" {
		t.Fatalf("record env missing TMPDIR: %v", child.recordEnv)
	}
}

func TestRunnerUsesRuntimeLaunchEnvWithoutCredentialLookupOrPersistence(t *testing.T) {
	const secret = "shared-global-secret"
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"result contains ` + secret + `"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          failingCreds{},
		DefaultTimeout: 2 * time.Second,
		LaunchEnv: func(principal.ExecutionIdentity) []string {
			return []string{"GLM_API_KEY=" + secret}
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "shared-glm",
			Type: "codex",
			Envs: []string{"GLM_API_KEY"},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	child := ctrl.last()
	if got := envValueForTest(child.env, "GLM_API_KEY"); got != secret {
		t.Fatalf("launch GLM_API_KEY = %q, want shared value", got)
	}
	if got := envValueForTest(child.recordEnv, "GLM_API_KEY"); got != "" {
		t.Fatalf("record env persisted shared value %q", got)
	}
	if strings.Contains(final.Result.Summary, secret) || !strings.Contains(final.Result.Summary, "[REDACTED]") {
		t.Fatalf("result summary was not redacted: %q", final.Result.Summary)
	}
	if len(final.CredentialProfiles) != 0 || final.CredentialProfile != "" {
		t.Fatalf("global env recorded credential profiles: %v / %q", final.CredentialProfiles, final.CredentialProfile)
	}
}

func TestRunnerInfersCredentialProfileFromProfileEnvs(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          lookupCreds{"service-admin": {Kind: "short-lived", Env: []string{"SERVICE_TOKEN=demo-secret", "OTHER=ignored"}}},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "service-admin",
			Type: "codex",
			Envs: []string{"SERVICE_TOKEN"},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if run.CredentialProfile != "" {
		t.Fatalf("initial credential profile = %q, want empty before env lookup", run.CredentialProfile)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	if final.CredentialProfile != "service-admin" {
		t.Fatalf("final credential profile = %q, want service-admin", final.CredentialProfile)
	}
	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if got := envValueForTest(child.env, "SERVICE_TOKEN"); got != "demo-secret" {
		t.Fatalf("SERVICE_TOKEN = %q, want injected secret", got)
	}
	if got := envValueForTest(child.env, "OTHER"); got != "" {
		t.Fatalf("OTHER = %q, want profile env credentials filtered to declared env", got)
	}
}

func TestRunnerResolvesProfileEnvsByGlobalCredentialEnv(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir: t.TempDir(),
		Store:   NewMemStore(),
		Control: ctrl,
		Creds: lookupCreds{
			"shared-service": {Kind: "short-lived", Env: []string{"SERVICE_TOKEN=demo-secret", "OTHER=ignored"}},
		},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "service-admin",
			Type: "codex",
			Envs: []string{"SERVICE_TOKEN"},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	if got, want := final.CredentialProfiles, []string{"shared-service"}; !equalStrings(got, want) {
		t.Fatalf("credential_profiles = %v, want %v", got, want)
	}
	if final.CredentialProfile != "shared-service" {
		t.Fatalf("credential_profile = %q, want shared-service", final.CredentialProfile)
	}
	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if got := envValueForTest(child.env, "SERVICE_TOKEN"); got != "demo-secret" {
		t.Fatalf("SERVICE_TOKEN = %q, want injected secret", got)
	}
	if got := envValueForTest(child.env, "OTHER"); got != "" {
		t.Fatalf("OTHER = %q, want globally resolved env filtered to declared env", got)
	}
}

func TestRunnerResolvesDeclaredProviderAPIKeyByGlobalCredentialEnv(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir: t.TempDir(),
		Store:   NewMemStore(),
		Control: ctrl,
		Creds: lookupCreds{
			"service-credentials": {Kind: "short-lived", Env: []string{"EXAMPLE_API_KEY=example-secret"}},
		},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "document-review",
			Type: "codex",
			Envs: []string{"EXAMPLE_API_KEY"},
			CredentialEnvSuggestions: map[string]string{
				"EXAMPLE_API_KEY": "example-provider",
			},
			Provider: &pb.AgentProfileProvider{
				Platform:          "openai-compat",
				CredentialProfile: "example-provider",
				ApiKeyEnv:         "EXAMPLE_API_KEY",
			},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	if got, want := final.CredentialProfiles, []string{"service-credentials"}; !equalStrings(got, want) {
		t.Fatalf("credential_profiles = %v, want %v", got, want)
	}
	child := ctrl.last()
	if child == nil || envValueForTest(child.env, "EXAMPLE_API_KEY") != "example-secret" {
		t.Fatalf("child env = %v, want globally resolved EXAMPLE_API_KEY", child)
	}
}

func TestCredentialPlanDeduplicatesProfileEnvsAndProviderRef(t *testing.T) {
	plan, err := ResolveCredentialPlan(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "openai-compat",
			Type: "codex",
			Envs: []string{"GLM_API_KEY"},
			CredentialEnvSuggestions: map[string]string{
				"GLM_API_KEY": "openai-compat",
			},
			Provider: &pb.AgentProfileProvider{
				Platform:  "openai-compat",
				ApiKeyEnv: "GLM_API_KEY",
			},
		},
	})
	if err != nil {
		t.Fatalf("ResolveCredentialPlan: %v", err)
	}
	if len(plan.Requests) != 1 {
		t.Fatalf("requests = %+v, want one deduplicated request", plan.Requests)
	}
	if got := plan.Requests[0].Profile; got != "" {
		t.Fatalf("request profile = %q, want one global request", got)
	}
	if got, want := plan.Requests[0].SuggestedProfile, "openai-compat"; got != want {
		t.Fatalf("suggested profile = %q, want %q", got, want)
	}
	if got := plan.Requests[0].EnvNames; len(got) != 1 || got[0] != "GLM_API_KEY" {
		t.Fatalf("request envs = %v, want [GLM_API_KEY]", got)
	}
}

func TestCredentialPlanResolvesProviderBackedProfileEnvsGlobally(t *testing.T) {
	plan, err := ResolveCredentialPlan(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "assistant",
			Type: "codex",
			Envs: []string{"GLM_API_KEY"},
			CredentialEnvSuggestions: map[string]string{
				"GLM_API_KEY": "openai-compat",
			},
			Provider: &pb.AgentProfileProvider{
				Platform:  "openai-compat",
				ApiKeyEnv: "GLM_API_KEY",
			},
		},
	})
	if err != nil {
		t.Fatalf("ResolveCredentialPlan: %v", err)
	}
	if len(plan.Requests) != 1 {
		t.Fatalf("requests = %+v, want one global env request", plan.Requests)
	}
	if got := plan.Requests[0].Profile; got != "" {
		t.Fatalf("request profile = %q, want global key lookup", got)
	}
	if got, want := plan.Requests[0].SuggestedProfile, "openai-compat"; got != want {
		t.Fatalf("suggested profile = %q, want %q", got, want)
	}
	if got := plan.Requests[0].EnvNames; len(got) != 1 || got[0] != "GLM_API_KEY" {
		t.Fatalf("request envs = %v, want [GLM_API_KEY]", got)
	}
}

func TestCredentialPlanSuggestsProviderProfileForNonSelectedProviderEnv(t *testing.T) {
	plan, err := ResolveCredentialPlan(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "document-review",
			Type: "codex",
			Envs: []string{"GLM_API_KEY", "EXAMPLE_API_KEY", "PROVIDER_API_KEY"},
			CredentialEnvSuggestions: map[string]string{
				"GLM_API_KEY":      "openai-compat",
				"EXAMPLE_API_KEY":  "example-provider",
				"PROVIDER_API_KEY": "provider",
			},
			Provider: &pb.AgentProfileProvider{
				Platform:          "openai-compat",
				CredentialProfile: "openai-compat",
				ApiKeyEnv:         "GLM_API_KEY",
			},
		},
	})
	if err != nil {
		t.Fatalf("ResolveCredentialPlan: %v", err)
	}
	if len(plan.Requests) != 3 {
		t.Fatalf("requests = %+v, want selected provider plus global provider envs", plan.Requests)
	}
	requestForEnv := func(envName string) CredentialMaterialRequest {
		t.Helper()
		for _, request := range plan.Requests {
			if equalStrings(request.EnvNames, []string{envName}) {
				return request
			}
		}
		t.Fatalf("requests = %+v, missing %s", plan.Requests, envName)
		return CredentialMaterialRequest{}
	}
	example := requestForEnv("EXAMPLE_API_KEY")
	if example.Profile != "" || example.SuggestedProfile != "example-provider" || !equalStrings(example.EnvNames, []string{"EXAMPLE_API_KEY"}) {
		t.Fatalf("example request = %+v, want example-provider setup suggestion", example)
	}
	extraProvider := requestForEnv("PROVIDER_API_KEY")
	if extraProvider.Profile != "" || extraProvider.SuggestedProfile != "provider" || !equalStrings(extraProvider.EnvNames, []string{"PROVIDER_API_KEY"}) {
		t.Fatalf("provider request = %+v, want provider setup suggestion", extraProvider)
	}
	provider := requestForEnv("GLM_API_KEY")
	if provider.Profile != "" || provider.SuggestedProfile != "openai-compat" {
		t.Fatalf("selected provider request = %+v, want global lookup with openai-compat suggestion", provider)
	}
}

func TestCredentialPlanLiteralProviderKeySkipsStoreLookup(t *testing.T) {
	// When the provider declares api_key_literal, the plan must produce a
	// self-contained literal request instead of a per-user store lookup, even if
	// the profile also lists the env in envs.
	plan, err := ResolveCredentialPlan(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "assistant-codex",
			Type: "codex",
			Envs: []string{"GLM_API_KEY"},
			CredentialEnvSuggestions: map[string]string{
				"GLM_API_KEY": "openai-compat",
			},
			Provider: &pb.AgentProfileProvider{
				Platform:      "openai-compat",
				ApiKeyEnv:     "GLM_API_KEY",
				ApiKeyLiteral: "test-api-key",
			},
		},
	})
	if err != nil {
		t.Fatalf("ResolveCredentialPlan: %v", err)
	}
	if len(plan.Requests) != 1 {
		t.Fatalf("requests = %+v, want exactly one literal request", plan.Requests)
	}
	req := plan.Requests[0]
	if got, want := req.LiteralEnv["GLM_API_KEY"], "test-api-key"; got != want {
		t.Fatalf("literal env GLM_API_KEY = %q, want %q (req=%+v)", got, want, req)
	}
	if len(req.EnvNames) != 0 || req.Profile != "" {
		t.Fatalf("literal request must not carry a store lookup, got %+v", req)
	}
}

func TestFilterCredentialPlanForEnvRemovesSatisfiedNamedRequests(t *testing.T) {
	plan, err := ResolveCredentialPlan(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Type:    "codex",
			EnvRefs: []string{"credential:openai-compat:GLM_API_KEY", "credential:github:GITHUB_TOKEN"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := FilterCredentialPlanForEnv(plan, []string{"GLM_API_KEY=shared"})
	if len(got.Requests) != 1 || !equalStrings(got.Requests[0].EnvNames, []string{"GITHUB_TOKEN"}) {
		t.Fatalf("filtered requests = %+v", got.Requests)
	}
	if !equalStrings(got.Profiles, []string{"github"}) || got.LegacyProfile != "github" {
		t.Fatalf("filtered profiles = %v legacy=%q", got.Profiles, got.LegacyProfile)
	}
}

func TestCredentialPlanLiteralProviderKeyMaterializesDirectly(t *testing.T) {
	// materialize against an empty store must still succeed because the literal
	// request never consults a store (or per-user credential profile).
	plan := CredentialPlan{Requests: []CredentialMaterialRequest{{
		LiteralEnv: map[string]string{"GLM_API_KEY": "test-api-key"},
		Source:     string(credentialSourceProvider),
	}}}
	mat, err := MaterializeCredentialPlan(context.Background(), FileCredentials{Dir: t.TempDir()}, plan, "codex")
	if err != nil {
		t.Fatalf("MaterializeCredentialPlan with literal: %v", err)
	}
	if got, want := strings.Join(mat.Env, ","), "GLM_API_KEY=test-api-key"; got != want {
		t.Fatalf("materialized env = %q, want %q", got, want)
	}
}

func TestCredentialPlanLiteralWinsOverStoreEnvWhenBothDeclared(t *testing.T) {
	// Regression: profile envs lists GLM_API_KEY AND provider has literal; the
	// literal must win (no store lookup) and no duplicate request may be added.
	plan, err := ResolveCredentialPlan(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "assistant",
			Type: "codex",
			Envs: []string{"GLM_API_KEY", "SERVICE_TOKEN"},
			CredentialEnvSuggestions: map[string]string{
				"GLM_API_KEY":   "openai-compat",
				"SERVICE_TOKEN": "service-admin",
			},
			Provider: &pb.AgentProfileProvider{
				Platform:      "openai-compat",
				ApiKeyEnv:     "GLM_API_KEY",
				ApiKeyLiteral: "test-api-key",
			},
		},
	})
	if err != nil {
		t.Fatalf("ResolveCredentialPlan: %v", err)
	}
	if len(plan.Requests) != 2 {
		t.Fatalf("requests = %+v, want literal GLM + global SERVICE_TOKEN", plan.Requests)
	}
	var literalSeen, serviceSeen bool
	for _, req := range plan.Requests {
		if len(req.LiteralEnv) > 0 {
			literalSeen = true
			if req.LiteralEnv["GLM_API_KEY"] != "test-api-key" {
				t.Fatalf("literal GLM_API_KEY = %q, want amux", req.LiteralEnv["GLM_API_KEY"])
			}
		}
		if equalStrings(req.EnvNames, []string{"SERVICE_TOKEN"}) {
			serviceSeen = true
		}
	}
	if !literalSeen || !serviceSeen {
		t.Fatalf("requests = %+v, want literal GLM_API_KEY and global SERVICE_TOKEN requests", plan.Requests)
	}
}

func TestRunnerInfersMultipleCredentialProfilesFromEnvRefs(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir: t.TempDir(),
		Store:   NewMemStore(),
		Control: ctrl,
		Creds: profileCreds{
			"github": {Kind: "short-lived", Env: []string{"GITHUB_TOKEN=gh-secret", "GITHUB_OTHER=ignored"}},
			"slack":  {Kind: "short-lived", Env: []string{"SLACK_TOKEN=sl-secret", "SLACK_OTHER=ignored"}},
		},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "repo-review",
			Type: "codex",
			EnvRefs: []string{
				"credential:github:GITHUB_TOKEN",
				"credential:slack:SLACK_TOKEN",
			},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got, want := run.CredentialProfiles, []string{"github", "slack"}; !equalStrings(got, want) {
		t.Fatalf("credential_profiles = %v, want %v", got, want)
	}
	if run.CredentialProfile != "" {
		t.Fatalf("legacy credential_profile = %q, want empty for multi-profile plan", run.CredentialProfile)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	if got, want := final.CredentialProfiles, []string{"github", "slack"}; !equalStrings(got, want) {
		t.Fatalf("final credential_profiles = %v, want %v", got, want)
	}
	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if got := envValueForTest(child.env, "GITHUB_TOKEN"); got != "gh-secret" {
		t.Fatalf("GITHUB_TOKEN = %q, want injected github secret", got)
	}
	if got := envValueForTest(child.env, "SLACK_TOKEN"); got != "sl-secret" {
		t.Fatalf("SLACK_TOKEN = %q, want injected slack secret", got)
	}
	if got := envValueForTest(child.env, "GITHUB_OTHER"); got != "" {
		t.Fatalf("GITHUB_OTHER = %q, want filtered env", got)
	}
	if got := envValueForTest(child.env, "SLACK_OTHER"); got != "" {
		t.Fatalf("SLACK_OTHER = %q, want filtered env", got)
	}
}

func TestRunnerCredentialEnvRefsRequireDeclaredEnv(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          fixedCreds{mat: CredentialMaterial{Kind: "short-lived", Env: []string{"OTHER=secret"}}},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name:    "repo-review",
			Type:    "codex",
			EnvRefs: []string{"credential:github:GITHUB_TOKEN"},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusFailed || !strings.Contains(final.Error, `credential profile "github" does not define env GITHUB_TOKEN`) {
		t.Fatalf("final = %s/%q, want missing credential env failure", final.Status, final.Error)
	}
	if ctrl.last() != nil {
		t.Fatal("no child should be created when credential env_ref env is missing")
	}
}

func TestRunnerCredentialEnvRefsRejectDuplicateEnvAcrossProfiles(t *testing.T) {
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        &fakeController{},
		Creds:          fixedCreds{mat: CredentialMaterial{}},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	_, err = r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "repo-review",
			Type: "codex",
			EnvRefs: []string{
				"credential:github:GITHUB_TOKEN",
				"credential:backup:GITHUB_TOKEN",
			},
		},
	}, "", "", 0)
	if err == nil || !strings.Contains(err.Error(), "credential env GITHUB_TOKEN is provided by multiple profiles") {
		t.Fatalf("Submit error = %v, want duplicate env conflict", err)
	}
}

func TestRunnerProviderCredentialRequiresDeclaredEnv(t *testing.T) {
	ctrl := &fakeController{}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		Creds:          fixedCreds{mat: CredentialMaterial{Kind: "short-lived", Env: []string{"OTHER=secret"}}},
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent: "codex",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name: "assistant",
			Type: "codex",
			Provider: &pb.AgentProfileProvider{
				Platform:  "provider",
				ApiKeyEnv: "PROVIDER_API_KEY",
			},
		},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusFailed || !strings.Contains(final.Error, `credential profile "provider" does not define env PROVIDER_API_KEY`) {
		t.Fatalf("final = %s/%q, want missing provider env failure", final.Status, final.Error)
	}
	if ctrl.last() != nil {
		t.Fatal("no child should be created when provider credential env is missing")
	}
}

func TestCredentialSetupErrorLabelsSuggestedProfile(t *testing.T) {
	err := credentialSetupError(&CredentialEnvNotFoundError{EnvName: "GLM_API_KEY"}, CredentialMaterialRequest{
		SuggestedProfile: "openai-compat",
		EnvNames:         []string{"GLM_API_KEY"},
	})
	if err == nil || !strings.Contains(err.Error(), "suggested setup: amux agent credential set openai-compat GLM_API_KEY") {
		t.Fatalf("credentialSetupError() = %v, want labelled setup suggestion", err)
	}
}

func TestCredentialSetupErrorUsesActualMissingGlobalEnv(t *testing.T) {
	err := credentialSetupError(&CredentialEnvNotFoundError{EnvName: "PROVIDER_API_KEY"}, CredentialMaterialRequest{
		SuggestedProfile: "provider",
		EnvNames:         []string{"EXAMPLE_API_KEY", "PROVIDER_API_KEY"},
	})
	if err == nil || !strings.Contains(err.Error(), "suggested setup: amux agent credential set provider PROVIDER_API_KEY") {
		t.Fatalf("credentialSetupError() = %v, want actual missing env in setup suggestion", err)
	}
	if strings.Contains(err.Error(), "set provider EXAMPLE_API_KEY") {
		t.Fatalf("credentialSetupError() = %v, suggestion used first requested env instead of missing env", err)
	}
}

func TestCredentialSetupErrorSkipsSuggestionForAmbiguousGlobalEnv(t *testing.T) {
	base := &CredentialEnvAmbiguousError{
		EnvName:       "PROVIDER_API_KEY",
		FirstProfile:  "provider-a",
		SecondProfile: "provider-b",
	}
	err := credentialSetupError(base, CredentialMaterialRequest{
		SuggestedProfile: "document-review",
		EnvNames:         []string{"PROVIDER_API_KEY"},
	})
	if err != base {
		t.Fatalf("credentialSetupError() = %v, want unchanged ambiguous error", err)
	}
	if strings.Contains(err.Error(), "setup") {
		t.Fatalf("credentialSetupError() = %v, ambiguous env must not suggest adding another source", err)
	}
}

func TestCredentialSetupErrorKeepsRequiredProfileLabel(t *testing.T) {
	err := credentialSetupError(errors.New("credential missing"), CredentialMaterialRequest{
		Profile:  "github",
		EnvNames: []string{"GITHUB_TOKEN"},
	})
	if err == nil || !strings.Contains(err.Error(), "setup: amux agent credential set github GITHUB_TOKEN") {
		t.Fatalf("credentialSetupError() = %v, want required profile setup", err)
	}
	if strings.Contains(err.Error(), "suggested setup") {
		t.Fatalf("credentialSetupError() = %v, explicit profile must not be labelled suggested", err)
	}
}

func TestRunnerDoesNotMaterializeMCPByDefaultForRawTask(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		DefaultTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{Agent: "codex", Task: "x", ExtraArgs: []string{"--model", "gpt-5"}}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitTerminal(t, r, run.ID)

	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if !hasArgSeq(child.extraArgs, "--model", "gpt-5") {
		t.Fatalf("child extraArgs missing caller args: %v", child.extraArgs)
	}
	for _, arg := range child.extraArgs {
		if strings.Contains(arg, "mcp_servers.amux.") {
			t.Fatalf("child extraArgs include amux MCP by default: %v", child.extraArgs)
		}
	}
}

func TestRunnerCanMaterializeMCPWhenExplicitlyConfigured(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	var gotAgent string
	var gotWorkspace string
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		DefaultTimeout: 2 * time.Second,
		MaterializeMCP: func(agentType string, sb *Sandbox) ([]string, error) {
			gotAgent = agentType
			gotWorkspace = sb.Workspace
			return []string{"-c", "mcp_servers.amux.command=\"amux\""}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{Agent: "codex", Task: "x", ExtraArgs: []string{"--model", "gpt-5"}}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitTerminal(t, r, run.ID)

	if gotAgent != "codex" || gotWorkspace == "" {
		t.Fatalf("MaterializeMCP got agent=%q workspace=%q", gotAgent, gotWorkspace)
	}
	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if !hasArgSeq(child.extraArgs, "--model", "gpt-5") || !hasArgSeq(child.extraArgs, "-c", "mcp_servers.amux.command=\"amux\"") {
		t.Fatalf("child extraArgs missing caller/mcp args: %v", child.extraArgs)
	}
}

func TestRunnerDoesNotMaterializeMCPAfterProfileMaterializationByDefault(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		DefaultTimeout: 2 * time.Second,
		MaterializeProfile: func(profile *pb.AgentProfileSpec, sb *Sandbox) ([]string, error) {
			return []string{"--append-system-prompt", "profile rules"}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, err := r.Submit(Spec{
		Agent:           "claude",
		Task:            "x",
		ResolvedProfile: &pb.AgentProfileSpec{Name: "cr", Type: "claude"},
	}, "", "", 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitTerminal(t, r, run.ID)

	child := ctrl.last()
	if child == nil {
		t.Fatalf("no child created")
	}
	if !hasArgSeq(child.extraArgs, "--append-system-prompt", "profile rules") {
		t.Fatalf("child extraArgs missing profile args: %v", child.extraArgs)
	}
	if countArgs(child.extraArgs, "--mcp-config") != 0 {
		t.Fatalf("child extraArgs include amux MCP by default: %v", child.extraArgs)
	}
}

func hasArgSeq(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func envValueForTest(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

func equalStrings(a, b []string) bool {
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

func countArgs(args []string, want string) int {
	count := 0
	for _, arg := range args {
		if arg == want {
			count++
		}
	}
	return count
}

type fakeArtifactPruner struct {
	calls  []string
	dryRun []bool
}

func (f *fakeArtifactPruner) PruneTaskArtifacts(_ context.Context, run *TaskRun, dryRun bool) (PruneArtifactReport, error) {
	f.calls = append(f.calls, run.ID)
	f.dryRun = append(f.dryRun, dryRun)
	return PruneArtifactReport{ChildSessionDeleted: run.ChildSessionId != "", SandboxDeleted: true}, nil
}

func TestRunnerPruneDeletesOnlyTerminalTasks(t *testing.T) {
	store := NewMemStore()
	now := time.Unix(1000, 0)
	for _, run := range []*TaskRun{
		{ID: "ok", Status: StatusSucceeded, ChildSessionId: "child-ok", FinishedAt: now.Add(-2 * time.Hour)},
		{ID: "fail", Status: StatusFailed, FinishedAt: now.Add(-2 * time.Hour)},
		{ID: "run", Status: StatusRunning, FinishedAt: now.Add(-2 * time.Hour)},
	} {
		if err := store.Save(run); err != nil {
			t.Fatalf("save %s: %v", run.ID, err)
		}
	}
	artifacts := &fakeArtifactPruner{}
	r, err := NewRunner(Options{AmuxDir: t.TempDir(), Store: store, Control: &fakeController{}, Artifacts: artifacts})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	res, err := r.Prune(context.Background(), PruneRequest{Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Tasks) != 2 {
		t.Fatalf("pruned tasks = %+v, want 2", res.Tasks)
	}
	if _, err := store.Get("ok"); err == nil {
		t.Fatal("terminal task ok still exists")
	}
	if _, err := store.Get("fail"); err == nil {
		t.Fatal("terminal task fail still exists")
	}
	if _, err := store.Get("run"); err != nil {
		t.Fatalf("running task should remain: %v", err)
	}
	if len(artifacts.calls) != 2 {
		t.Fatalf("artifact calls = %v, want 2", artifacts.calls)
	}
}

func TestRunnerPruneHonorsDryRunAgeAndStatus(t *testing.T) {
	store := NewMemStore()
	now := time.Unix(2000, 0)
	for _, run := range []*TaskRun{
		{ID: "old-ok", Status: StatusSucceeded, FinishedAt: now.Add(-48 * time.Hour)},
		{ID: "old-fail", Status: StatusFailed, FinishedAt: now.Add(-48 * time.Hour)},
		{ID: "new-ok", Status: StatusSucceeded, FinishedAt: now.Add(-time.Hour)},
	} {
		if err := store.Save(run); err != nil {
			t.Fatalf("save %s: %v", run.ID, err)
		}
	}
	artifacts := &fakeArtifactPruner{}
	r, err := NewRunner(Options{AmuxDir: t.TempDir(), Store: store, Control: &fakeController{}, Artifacts: artifacts})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	res, err := r.Prune(context.Background(), PruneRequest{DryRun: true, OlderThan: 24 * time.Hour, Statuses: []Status{StatusSucceeded}, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Tasks) != 1 || res.Tasks[0].ID != "old-ok" {
		t.Fatalf("pruned tasks = %+v, want old-ok", res.Tasks)
	}
	for _, id := range []string{"old-ok", "old-fail", "new-ok"} {
		if _, err := store.Get(id); err != nil {
			t.Fatalf("dry-run deleted %s: %v", id, err)
		}
	}
	if len(artifacts.dryRun) != 1 || !artifacts.dryRun[0] {
		t.Fatalf("artifact dry-run flags = %v", artifacts.dryRun)
	}
	if _, err := r.Prune(context.Background(), PruneRequest{Statuses: []Status{StatusRunning}, Now: now}); err == nil {
		t.Fatal("Prune accepted non-terminal status")
	}
}

// pendingArtifactPruner reports a still-live child session for one task id, so
// Prune must defer (not delete) that task's record.
type pendingArtifactPruner struct {
	pendingFor string
}

func (p *pendingArtifactPruner) PruneTaskArtifacts(_ context.Context, run *TaskRun, _ bool) (PruneArtifactReport, error) {
	if run.ID == p.pendingFor {
		return PruneArtifactReport{ChildSessionPending: true}, nil
	}
	return PruneArtifactReport{ChildSessionDeleted: run.ChildSessionId != "", SandboxDeleted: true}, nil
}

func TestRunnerPruneDefersTaskWithLiveChildSession(t *testing.T) {
	store := NewMemStore()
	now := time.Unix(3000, 0)
	for _, run := range []*TaskRun{
		{ID: "reapable", Status: StatusSucceeded, ChildSessionId: "child-r", FinishedAt: now.Add(-time.Hour)},
		{ID: "pending", Status: StatusSucceeded, ChildSessionId: "child-p", FinishedAt: now.Add(-time.Hour)},
	} {
		if err := store.Save(run); err != nil {
			t.Fatalf("save %s: %v", run.ID, err)
		}
	}
	r, err := NewRunner(Options{AmuxDir: t.TempDir(), Store: store, Control: &fakeController{}, Artifacts: &pendingArtifactPruner{pendingFor: "pending"}})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	res, err := r.Prune(context.Background(), PruneRequest{Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Tasks) != 1 || res.Tasks[0].ID != "reapable" {
		t.Fatalf("pruned tasks = %+v, want only reapable", res.Tasks)
	}
	// The deferred task must keep its record so a later prune retries — otherwise
	// its child session is orphaned forever.
	if _, err := r.Get("pending"); err != nil {
		t.Fatalf("deferred task record was deleted (would orphan its child): %v", err)
	}
	if _, err := r.Get("reapable"); err == nil {
		t.Fatal("reapable task record should have been deleted")
	}
	var deferred bool
	for _, s := range res.Skipped {
		if s.ID == "pending" {
			deferred = true
		}
	}
	if !deferred {
		t.Fatalf("deferred task not reported in Skipped: %+v", res.Skipped)
	}
}

func TestRunnerTaskExists(t *testing.T) {
	store := NewMemStore()
	if err := store.Save(&TaskRun{ID: "present", Status: StatusSucceeded}); err != nil {
		t.Fatalf("save: %v", err)
	}
	r, err := NewRunner(Options{AmuxDir: t.TempDir(), Store: store, Control: &fakeController{}})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if ok, err := r.TaskExists("present"); err != nil || !ok {
		t.Fatalf("TaskExists(present) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := r.TaskExists("missing"); err != nil || ok {
		t.Fatalf("TaskExists(missing) = %v, %v; want false, nil", ok, err)
	}
	if ok, err := r.TaskExists(""); err != nil || ok {
		t.Fatalf("TaskExists(\"\") = %v, %v; want false, nil", ok, err)
	}
}

func TestStoreGetReturnsErrTaskNotFound(t *testing.T) {
	store := NewMemStore()
	_, err := store.Get("nope")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("MemStore.Get missing err = %v, want ErrTaskNotFound", err)
	}
}

func outEv(seq uint64, s string) TaskEvent {
	return TaskEvent{Seq: seq, Kind: TaskEventOutput, Output: []byte(s), At: time.Unix(int64(seq), 0)}
}

func TestCoalesceOutputEventsMergesConsecutiveRuns(t *testing.T) {
	in := []TaskEvent{
		outEv(1, "a"),
		outEv(2, "b"),
		outEv(3, "c"),
		{Seq: 4, Kind: TaskEventStatus, Status: StatusRunning, At: time.Unix(4, 0)},
		outEv(5, "d"),
		outEv(6, "e"),
		{Seq: 7, Kind: TaskEventResult, Result: &TaskResult{Summary: "done"}, At: time.Unix(7, 0)},
	}
	got := coalesceOutputEvents(in)

	// Expect: [output "abc" seq3], [status seq4], [output "de" seq6], [result seq7].
	if len(got) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(got), got)
	}
	if got[0].Kind != TaskEventOutput || string(got[0].Output) != "abc" {
		t.Fatalf("chunk0 = %q kind=%s, want output \"abc\"", got[0].Output, got[0].Kind)
	}
	if got[0].Seq != 3 {
		t.Fatalf("chunk0 seq = %d, want 3 (last merged)", got[0].Seq)
	}
	if !got[0].At.Equal(time.Unix(1, 0)) {
		t.Fatalf("chunk0 At = %v, want first merged (1)", got[0].At)
	}
	if got[1].Kind != TaskEventStatus || got[1].Seq != 4 {
		t.Fatalf("event1 = %+v, want status seq4", got[1])
	}
	if got[2].Kind != TaskEventOutput || string(got[2].Output) != "de" || got[2].Seq != 6 {
		t.Fatalf("chunk2 = %q seq=%d, want \"de\" seq6", got[2].Output, got[2].Seq)
	}
	if got[3].Kind != TaskEventResult || got[3].Seq != 7 {
		t.Fatalf("event3 = %+v, want result seq7", got[3])
	}
}

func TestCoalesceOutputEventsIsByteLossless(t *testing.T) {
	var in []TaskEvent
	var want []byte
	for i := uint64(1); i <= 500; i++ {
		s := fmt.Sprintf("frame-%d;", i)
		in = append(in, outEv(i, s))
		want = append(want, s...)
	}
	got := coalesceOutputEvents(in)
	if len(got) >= len(in) {
		t.Fatalf("coalescing 500 small frames produced %d events, want far fewer", len(got))
	}
	var joined []byte
	lastSeq := uint64(0)
	for _, ev := range got {
		if ev.Kind != TaskEventOutput {
			t.Fatalf("unexpected non-output event: %+v", ev)
		}
		if len(ev.Output) > outFlushBytes {
			t.Fatalf("chunk of %d bytes exceeds outFlushBytes %d", len(ev.Output), outFlushBytes)
		}
		if ev.Seq <= lastSeq {
			t.Fatalf("seq not increasing: %d after %d", ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
		joined = append(joined, ev.Output...)
	}
	if string(joined) != string(want) {
		t.Fatal("coalesced bytes differ from source concatenation")
	}
	if lastSeq != 500 {
		t.Fatalf("final chunk seq = %d, want 500 (cursor must reach batch max)", lastSeq)
	}
}

func TestCoalesceOutputEventsPassesOversizedFrameWhole(t *testing.T) {
	big := strings.Repeat("x", outFlushBytes+100)
	in := []TaskEvent{
		outEv(1, "small"),
		outEv(2, big),
		outEv(3, "tail"),
	}
	got := coalesceOutputEvents(in)
	// "small" cannot absorb the oversized frame, which passes through whole; the
	// oversized chunk cannot absorb "tail" either.
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3: sizes must not be split/merged across the oversized frame", len(got))
	}
	if string(got[0].Output) != "small" || got[0].Seq != 1 {
		t.Fatalf("chunk0 = %q seq=%d, want \"small\" seq1", got[0].Output, got[0].Seq)
	}
	if len(got[1].Output) != len(big) || got[1].Seq != 2 {
		t.Fatalf("chunk1 len=%d seq=%d, want oversized frame whole at seq2", len(got[1].Output), got[1].Seq)
	}
	if string(got[2].Output) != "tail" || got[2].Seq != 3 {
		t.Fatalf("chunk2 = %q seq=%d, want \"tail\" seq3", got[2].Output, got[2].Seq)
	}
}

func TestCoalesceOutputEventsShortInputsUnchanged(t *testing.T) {
	if got := coalesceOutputEvents(nil); got != nil {
		t.Fatalf("nil input = %+v, want nil", got)
	}
	one := []TaskEvent{outEv(1, "solo")}
	got := coalesceOutputEvents(one)
	if len(got) != 1 || string(got[0].Output) != "solo" {
		t.Fatalf("single-event input mangled: %+v", got)
	}
}
