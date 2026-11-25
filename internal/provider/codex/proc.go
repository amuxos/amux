package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/provider"
	providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessioninput"
)

// Proc drives a `codex app-server` subprocess over the JSON-RPC protocol
// emitted by codex CLI 0.133.0. See `codex app-server generate-json-schema`
// for the canonical schema.
type turnStartAttempt struct {
	generation      uint64
	candidateTurnID string
	method          string
	started         chan struct{}
	responseLess    bool
}

type Proc struct {
	defaultModeUserInput     bool
	asyncInputTurnID         string
	asyncInputItems          map[string]bool // guarded by threadMu; bounded to one native turn
	notificationMu           sync.Mutex
	notificationQueue        []func()
	notificationsInitialized bool
	notificationsReady       bool

	handle isolation.ProcessHandle
	client *providerjsonrpc.Client
	stdin  io.WriteCloser

	providerName       string
	discoverSkills     bool
	excludedSkillRoots []string

	inputMu               sync.Mutex
	inputModalities       []string
	inputCapabilitySource string

	eventSinks    session.EventSinks
	goalControlMu sync.Mutex
	goalSupported bool                     // guarded by threadMu
	goalWireSeq   uint64                   // guarded by threadMu
	goal          *agentstream.GoalState   // guarded by threadMu
	goalAdmitter  session.GoalTurnAdmitter // guarded by threadMu

	skillsMu           sync.Mutex
	skillPaths         map[string]string
	skillsSnapshot     []provider.AvailableCommand
	skillsLoaded       bool
	skillRefreshActive bool
	skillRefreshAgain  bool
	skillRefreshForce  bool

	// turnFailureHint, when non-nil, is consulted at turn-failure emission time
	// to append an operator-actionable hint and marker error code (see
	// StartOptions.TurnFailureHint).
	turnFailureHint func(reason, errorCode string) (hint, code string)

	// closing marks an intentional shutdown. Close is only invoked by callers
	// that deliberately tear down the agent process (session Kill for
	// stop/restart/set-model reconfigure, structured broker shutdown); a
	// process crashing on its own never reaches Close. When set, the exit
	// watcher in Start must not report the resulting exit as a mid-turn
	// failure — the session side ends the turn itself (Kill -> abortTurn).
	closing atomic.Bool

	memoryOptions StartOptions
	cwd           string
	inputTempDir  string
	inlineImages  bool
	turnModel     string
	turnEffort    string
	stagingMu     sync.Mutex
	stagingDir    string

	threadMu       sync.Mutex
	threadID       string
	effectiveModel string
	turnID         string
	idleTimer      *time.Timer
	idleGeneration uint64
	// idleCompletionDelay is the thread-idle debounce. Production uses
	// stableIdleCompletionDelay; tests may shorten it without changing provider
	// behavior or sleeping for real seconds.
	idleCompletionDelay time.Duration
	// lastFailureTurnID is the turnID for which emitTurnFailure most recently
	// emitted a terminal failed event. It deduplicates failures within one
	// turn (multiple error signals can fire for the same dead turn) without
	// suppressing a genuine failure on the next turn. It is cleared on
	// turn/started so the next turn can fail visibly.
	lastFailureTurnID string
	// zombieTurnID is a turn the client-side watchdog declared dead while the
	// provider may still be running it server-side. Unlike lastFailureTurnID
	// (a dedup marker cleared by sendTurn on the next submission), it is set
	// ONLY by the watchdog callbacks — never by provider-reported failures,
	// whose turns are already dead server-side — and survives a failed
	// resubmission so Interrupt() can still cancel the zombie. It is cleared only
	// when a server-side terminal frame confirms that zombie is dead.
	zombieTurnID string
	// zombieTurnIDs contains watchdog-failed turns that have not yet produced a
	// server-side terminal frame. Keep these tombstones across later turns: the
	// thread/status channel carries no turn id, so any outstanding zombie makes
	// an unscoped idle ambiguous until the live turn produces scoped activity.
	zombieTurnIDs map[string]struct{}
	// terminalTurnIDs is the connection-lifetime tombstone set for every turn
	// amux has already completed or failed. All inbound adoption paths consult
	// it so a delayed turn/started or scoped request cannot resurrect an older
	// turn after newer turns have advanced the scalar last* markers.
	terminalTurnIDs map[string]struct{}
	// pendingTurnStart identifies the one turn/start RPC currently issued by
	// this proc (Write is serialized by inputMu). Notifications/requests may
	// provisionally reveal its turn id before the RPC response; the response
	// must reconcile to the same id before the attempt is retired.
	turnStartGeneration uint64
	pendingTurnStart    *turnStartAttempt

	// watchdogConfig holds the configured timeouts for the client-side turn
	// watchdog. Zero values disable the corresponding layer; negative values
	// are normalized to the package defaults at Start.
	watchdogConfig     config.StructuredProviderConfig
	watchdogGeneration uint64
	// watchdogTimeoutUnit converts the integer timeout config to durations.
	// Production uses time.Second; tests may inject a shorter unit.
	watchdogTimeoutUnit time.Duration
	// watchdogInterruptTimeout bounds the ordered turn/interrupt sent before a
	// watchdog failure. Production uses two seconds; tests may shorten it when
	// a fake provider intentionally never responds.
	watchdogInterruptTimeout time.Duration
	// activeTools tracks in-flight tool-like items by item id. The value is
	// true when the item type has interim progress notifications that can
	// reset the tool idle timer (commandExecution/fileChange/mcpToolCall);
	// false for types where item/completed is the only guaranteed follow-up
	// (webSearch/dynamicToolCall/collabAgentToolCall) — those are bounded by
	// the turn hard cap only. Keyed tracking (rather than a bare count) makes
	// lost or duplicate item lifecycle notifications idempotent instead of
	// desynchronizing the watchdog.
	activeTools map[string]bool
	// A provider request blocked on a user response (approval or
	// requestUserInput) suspends the turn idle timer, since the provider
	// cannot make progress until the user answers. "Is a user wait pending" is
	// derived from the approvals/userInputs maps (see hasPendingUserWaitLocked)
	// rather than a separate counter, so it cannot drift out of sync with the
	// maps across a turn boundary. All three — approvals, userInputs, and the
	// derived wait state — are guarded by threadMu.
	turnIdleTimer    *time.Timer
	turnHardCapTimer *time.Timer
	toolIdleTimer    *time.Timer
	// lastTurnActivity / lastToolActivity back the timeout callbacks'
	// re-check: an AfterFunc callback that already started cannot be recalled
	// by Stop/Reset, so on fire the callback re-verifies the real deadline
	// and re-arms instead of killing a turn that just showed activity.
	lastTurnActivity time.Time
	lastToolActivity time.Time
	// curTurnProduced records whether the active turn has produced any
	// provider-scoped activity (delta, tool, token usage, user wait) since it
	// was armed. A thread-level idle status is only treated as a completion
	// candidate for a turn that has produced something — otherwise a stale idle
	// belonging to a just-interrupted zombie turn would complete a freshly
	// armed live turn that is still silently thinking, dropping its output.
	curTurnProduced bool
	// curTurnHadTool distinguishes a genuinely empty provider completion from
	// a tool-only or async-question turn, which must not invite a manual retry.
	curTurnHadTool bool
	// These fields back the repeated-identical-tool-call watchdog: the most
	// recent call-defining key, its consecutive count, and the item IDs already
	// observed during this turn. Guarded by threadMu; reset per turn.
	lastToolCallKey     string
	repeatToolCallCount int
	seenToolItemIDs     map[string]struct{}

	// msgMu guards the accumulation of the current turn's final agent message.
	// Codex streams the answer as agentMessage deltas and turn/completed carries
	// no text, so the proc rebuilds the last message item here to emit a native
	// EventResult at turn end.
	msgMu     sync.Mutex
	curItemID string
	curMsg    strings.Builder
	usageMu   sync.Mutex
	turnUsage json.RawMessage

	// Turn speed metrics (TTFT / output tokens per second), guarded by usageMu.
	// turnStartAt is stamped when turn/start is sent; firstTokenAt on the first
	// agentMessage/reasoning delta of the turn. Both reset on fresh turn arm.
	turnStartAt  time.Time
	firstTokenAt time.Time
	// genActiveAt is the start of the currently open generation segment, or the
	// zero time while a tool is running (or before the first delta). genElapsedNs
	// accumulates closed segments. The generation rate divides output tokens by
	// the active generation window, so tool execution time — which the model
	// spends waiting, not producing — is excluded instead of deflating the rate.
	genActiveAt  time.Time
	genElapsedNs int64

	// approvals and userInputs track in-flight server->client permission
	// requests. Both are guarded by threadMu (NOT a separate mutex) so that
	// registering/removing an entry and re-deriving the turn idle suspension
	// happen atomically — a split-lock design let a stale response from a dead
	// turn corrupt the live turn's wait accounting. userInputs handles
	// item/tool/requestUserInput: one request carries N questions, split into N
	// permissions whose answers are aggregated before responding.
	approvals  map[string]pendingApproval
	userInputs map[int64]*pendingUserInput
}

// pendingUserInput tracks one item/tool/requestUserInput request awaiting user
// answers. The app server expects a single response carrying answers for all
// questions, so amux collects per-question answers and responds once every
// question has been answered.
type pendingUserInput struct {
	reqID     int64
	questions []inputQuestion
	// answered maps questionId -> selected option labels (1 for single-select,
	// 1+ for multiSelect). Order of insertion per question is preserved.
	answered map[string][]string
	permToQ  map[string]string // permission_id (reqID:questionId) -> questionId
	multiSel map[string]bool   // questionId -> multiSelect
}

type inputQuestion struct {
	id          string
	header      string
	question    string
	multiSelect bool
	allowOther  bool
	secret      bool
	blocking    *bool
	options     []inputOption
}

type inputOption struct {
	label       string
	description string
}

type pendingApproval struct {
	reqID  int64
	method string
}

type StartOptions struct {
	Cwd       string
	Env       []string
	ExtraArgs []string
	// ResumeSessionID is the Codex app-server thread id to resume. Empty means
	// start a new thread.
	ResumeSessionID string
	// ResumeModel and ResumeReasoningEffort override the resumed thread's next
	// turn defaults when an amux session changes execution settings in place.
	ResumeModel           string
	ResumeReasoningEffort string
	// CodexExecutable overrides the codex binary path. Empty means "codex" in
	// $PATH. Used by tests to point at a fake app-server.
	CodexExecutable string
	// Executable overrides the app-server binary path. Empty falls back to
	// CodexExecutable for existing tests, then to "codex".
	Executable string
	// ProviderName is the provider id used in amux events and errors. Empty
	// defaults to "codex".
	ProviderName string
	// ReadPrincipalHomeFiles reads HOME-relative files as the target user
	// during isolated model discovery. It is nil on host launches.
	ReadPrincipalHomeFiles func(context.Context, []string) (map[string][]byte, error)
	// DiscoverSkills enables Codex-compatible app-server skills/list discovery
	// for the current Cwd. It does not add any Agent Profile Pack roots.
	DiscoverSkills bool
	// ExcludedSkillRoots removes skills whose returned SKILL.md path is inside
	// one of these roots. amux uses this to keep Agent Profile Pack projections
	// out of runtime app-server discovery while allowing ordinary repo skills.
	ExcludedSkillRoots []string
	// AppServerSubcommand is appended after global args. Empty defaults to
	// "app-server".
	AppServerSubcommand string
	// ExperimentalAPI includes capabilities.experimentalApi=true in initialize.
	ExperimentalAPI bool
	// SendInitialized sends the initialized notification after initialize.
	SendInitialized bool
	// Watchdog configures client-side turn watchdog timeouts. Zero values
	// disable the corresponding layer; negative values use the package
	// defaults. The production path always passes explicit values resolved
	// from internal/config (where 0 likewise means disabled).
	Watchdog      config.StructuredProviderConfig
	Backend       isolation.LaunchBackend
	LaunchContext isolation.LaunchContext
	// IdleCompletionDelay overrides the thread-idle debounce. It exists
	// for deterministic provider tests; production leaves it zero.
	IdleCompletionDelay time.Duration
	// WatchdogTimeoutUnit overrides the unit used for integer watchdog timeout
	// values. Values above one second are ignored. It exists for deterministic
	// provider tests; production leaves it zero and uses time.Second.
	WatchdogTimeoutUnit time.Duration
	// WatchdogInterruptTimeout overrides the ordered interrupt timeout used by
	// watchdog tests. Production leaves it zero and uses two seconds.
	WatchdogInterruptTimeout time.Duration
	// TurnFailureHint, when non-nil, is consulted each time a turn fails
	// terminally. It may return an operator-actionable hint (appended to the
	// failure reason in the [system] frame and ProviderError message) and a
	// marker error code, which is used only when the failure itself carries no
	// code. It runs synchronously on the event path, so it must be cheap.
	TurnFailureHint func(reason, errorCode string) (hint, code string)
}

// Approval method names recognised on the server-initiated request channel.
const (
	methodCommandExecApproval     = "item/commandExecution/requestApproval"
	methodFileChangeApproval      = "item/fileChange/requestApproval"
	methodApplyPatchApproval      = "applyPatchApproval"
	methodExecCommandApprovalRoot = "execCommandApproval"
	methodPermissionsApproval     = "item/permissions/requestApproval"
	// methodToolRequestUserInput is the app-server's user-question
	// request asking the user to answer one or more questions.
	methodToolRequestUserInput = "item/tool/requestUserInput"
)

// Stable amux-side option ids used in PermissionOption.OptionId. The handler
// maps these to the codex-specific decision shape when responding.
const (
	optAccept           = "accept"
	optAcceptForSession = "acceptForSession"
	optDecline          = "decline"
	optCancel           = "cancel"
)

// turnStartTimeout caps how long sendTurn waits for the app-server to
// acknowledge `turn/start`. This is an ack timeout, not a turn-completion
// timeout: a provider that accepts the turn but takes long to complete it is
// not affected. Generous for an ack; tunable as a named constant.
const turnStartTimeout = 300 * time.Second

// skillListTimeout bounds optional discovery. An old or partially compatible
// app-server must never delay thread startup or block normal turns.
const skillListTimeout = 10 * time.Second

// stableIdleCompletionDelay distinguishes a final idle (a provider waiting for
// user input without turn/completed) from a transient idle between internal
// execution phases of the same amux submission.
const stableIdleCompletionDelay = 5 * time.Second

// maxWatchdogSeconds caps configured watchdog durations before duration
// multiplication, so an absurd value cannot overflow to a short timeout.
const maxWatchdogSeconds = 7 * 24 * 3600

// watchdogDurationWithUnit converts configured timeout units to a clamped
// time.Duration. The clamp is applied first: multiplying an out-of-range int
// by the unit could overflow int64 and escape a post-multiplication check.

// toolItemTypes are item/... types that represent a long-running external
// action. While one of these is active, the turn idle watchdog is suspended
// (see turnIdleSuspendedLocked for the exact rule).
var toolItemTypes = map[string]bool{
	"commandExecution":    true,
	"fileChange":          true,
	"mcpToolCall":         true,
	"dynamicToolCall":     true,
	"collabAgentToolCall": true,
	"webSearch":           true,
}

// progressToolItemTypes are the tool-like item types with interim
// progress/delta notifications (outputDelta / patchUpdated / progress) that
// reset the tool idle timer. The remaining tool-like types (webSearch,
// dynamicToolCall, collabAgentToolCall) have NO interim notification in the
// app-server schema — item/completed is their only guaranteed follow-up — so a
// tool idle timer would measure total runtime rather than idleness and would
// kill healthy long-running tools (e.g. a 15-minute sub-agent). They are
// bounded by the turn hard cap only.
var progressToolItemTypes = map[string]bool{
	"commandExecution": true,
	"fileChange":       true,
	"mcpToolCall":      true,
}

// startTurnWatchdogLocked arms the turn idle and hard-cap timers for a new
// turn. The caller must hold p.threadMu. A timeout of 0 means disabled.

// Drop any user-wait entries left over from a previous turn that ended
// without clearing them, so the new turn starts with no phantom waits
// suspending its idle watchdog.

// stopTurnWatchdogLocked stops all watchdog timers. The caller must hold
// p.threadMu.

// stopTurnWatchdog stops all watchdog timers. It is safe to call even when no
// turn is active.

// hasProgressToolActiveLocked reports whether any in-flight tool item can
// produce interim progress events. The caller must hold p.threadMu.

// hasPendingUserWaitLocked reports whether the provider is blocked on any
// unanswered approval or requestUserInput. Derived from the maps so it cannot
// drift from them. The caller must hold p.threadMu.

// markTerminalTurnLocked records that amux has already finalized turnID. The
// caller must hold p.threadMu.

// isTerminalTurnLocked reports whether amux has already finalized turnID. The
// caller must hold p.threadMu.

// markZombieTurnLocked records a watchdog-failed turn whose server-side death
// has not yet been confirmed. zombieTurnID remains the preferred manual
// Interrupt fallback; zombieTurnIDs retains every outstanding tombstone so an
// older zombie cannot become invisible when a newer turn also times out. The
// caller must hold p.threadMu.

// clearZombieTurnLocked consumes the server-side terminal proof for turnID.
// If it was the preferred Interrupt fallback, choose another still-outstanding
// zombie (if any). The caller must hold p.threadMu.

// quarantineUnexpectedTurnLocked retires a provider turn that cannot belong
// to the current amux submission. Remember it as terminal immediately so
// duplicate late frames cannot adopt it, retain a zombie tombstone until the
// provider confirms death, and cancel it asynchronously (notification
// handlers run on the JSON-RPC read loop and therefore must not wait for a
// response on that same loop). The caller must hold p.threadMu.

// canAcceptUnscopedIdleLocked reports whether a thread-level idle can safely
// be attributed to the current turn. An outstanding zombie makes the signal
// ambiguous until the live turn has produced scope-verified activity. The
// caller must hold p.threadMu.

// toolIdleSuspendedForWaitLocked reports whether a pending user wait currently
// suspends the tool idle timer. It does so ONLY when the hard cap is armed to
// bound the wait — an already-running tool that goes naturally silent while
// the user takes minutes to answer must not be false-killed. With the hard cap
// disabled (0), suspending tool idle during a wait would leave NO clock
// watching the turn at all, so tool idle keeps running as the bound. The
// caller must hold p.threadMu.

// turnIdleSuspendedLocked reports whether the turn idle timer should currently
// be suspended. The caller must hold p.threadMu. Suspension rules:
//   - a user response is pending (approval / requestUserInput): the provider
//     cannot make progress until the user answers;
//   - only no-progress tools are active: nothing can reset either idle timer,
//     so the hard cap owns the clock;
//   - a progress-capable tool is active AND the tool idle layer is enabled:
//     the tool idle watchdog owns the clock. With tool idle disabled (0) the
//     turn idle timer keeps running during tools, so a silent hanging tool is
//     still caught instead of being watched by nothing.

// reconcileWatchdogTimersLocked derives the turn idle / tool idle timer state
// from activeTools and pending user waits. Centralizing the transitions here
// (instead of incremental Stop/Reset at each call site) keeps the timers
// correct under duplicate, lost, or reordered item notifications. The caller
// must hold p.threadMu and have an active turn.

// Tool idle: armed only while a progress-capable tool runs and the layer is
// enabled. See toolIdleSuspendedForWaitLocked for the user-wait rule.

// Turn idle.

// resetTurnActivity records turn-level activity and re-derives the watchdog
// timers. It is safe to call from notification handlers.

// resetTurnActivityLocked records turn-level activity (used by the fired
// callback's deadline re-check) and reconciles the timers. The caller must
// hold p.threadMu.

// resetToolActivityLocked records tool progress and rearms the tool idle
// timer. The caller must hold p.threadMu.

// resetToolProgress records a tool progress event in one locked section: it
// counts as turn-level activity and rearms the tool idle timer. It is safe to
// call from notification handlers.

// noteToolStarted records that a tool-like item has started. It is safe to
// call from notification handlers.

// noteToolStartedLocked registers itemID as active and reconciles the timers.
// A duplicate item/started for an already-tracked id is a no-op, so replayed
// notifications cannot inflate the active set. The caller must hold
// p.threadMu.

// noteToolCompleted records that a tool-like item has completed. It is safe to
// call from notification handlers.

// noteToolCompletedLocked removes itemID from the active set and reconciles
// the timers. A completion without a matching start still proves tool activity;
// duplicate completions may refresh turn activity but cannot prematurely zero
// the set while other tools still run. The caller must hold p.threadMu.

// registerApprovalWaitLocked records an approval request as a pending user
// wait and reconciles the watchdog. Returns false if permID is already
// registered (a duplicate/retransmitted request), so the caller skips
// re-surfacing it and double-counting the wait. The caller must hold threadMu.
// It does NOT set curTurnProduced: an unscoped approval (which may belong to a
// zombie) must not arm the stale-idle trust bit; the caller sets it only for
// scope-verified requests.

// registerUserInputWaitLocked records a requestUserInput as a pending user
// wait and reconciles the watchdog. Returns false if reqID is already
// registered. The caller must hold threadMu. Like registerApprovalWaitLocked it
// leaves curTurnProduced to the caller (set only for scope-verified requests).

// interruptTurnBestEffort asks the provider to cancel a turn the client-side
// watchdog has declared dead. Without this the server-side turn keeps running
// as a zombie: the next turn/start is rejected or hangs (single active turn
// per thread) and the session re-wedges right after the watchdog freed it.
// Fire-and-forget; the provider may already have finished the turn.

// interruptTurnSyncOrdered sends turn/interrupt synchronously so the request is
// written to the pipe BEFORE the caller emits the queue-freeing failure — an
// async (goroutine) dispatch gives no such ordering, letting a queued
// resubmission's turn/start reach the provider first and be rejected by the
// still-active zombie turn. The write is bounded by the client's OS write
// deadline, and the response is matched by the jsonrpc read loop without
// needing threadMu, so a healthy provider ACKs immediately; a wedged provider
// costs at most the short timeout. The response is irrelevant (the turn is
// being declared dead regardless) and any error is swallowed.

// failTurnByWatchdogLocked is the shared tail of the three watchdog timeout
// callbacks. The caller must hold p.threadMu and have confirmed the deadline
// truly elapsed for the active turn. It records the turn as a zombie (so a
// later user Interrupt() can still cancel it), sends turn/interrupt
// SYNCHRONOUSLY (guaranteeing it precedes any queued resubmission on the pipe),
// and then emits the failure (which unlocks p.threadMu).

// Stale fire: the timer expired before a suspension Stop landed. The
// turn is in a state (user wait / tool) the idle watchdog must not
// judge.

// Activity raced with the fired callback (Stop/Reset cannot recall an
// AfterFunc callback that already started): re-arm instead of killing
// a turn that just showed activity.

// Stale fire: no progress-capable tool is active anymore, so the tool
// idle watchdog no longer owns the clock.

// Stale fire: the timer expired before the user-wait suspension landed
// (Stop cannot recall an already-fired AfterFunc callback). A pending
// user response suspends tool idle (when the hard cap bounds the wait),
// so this fire must not judge.

// Tool progress raced with the fired callback: re-arm.

// appServerArgs builds argv for an app-server style CLI given the user's extra
// args. Codex-compatible CLIs parse --model, -s/--sandbox, -a/--ask-for-approval,
// and -c/--config as GLOBAL flags, which must come BEFORE the app-server
// subcommand. Splicing extras in before the subcommand lets `amux run codex --
// --model opus -s workspace-write` (and the desktop's structured Model /
// Sandbox pickers) reach the provider unchanged.
func appServerArgs(extraArgs []string, subcommand string) []string {
	subcommand = strings.TrimSpace(subcommand)
	if subcommand == "" {
		subcommand = "app-server"
	}
	args := make([]string, 0, len(extraArgs)+1)
	args = append(args, extraArgs...)
	args = append(args, subcommand)
	return args
}

func Start(ctx context.Context, opts StartOptions) (*Proc, <-chan []byte, error) {
	providerName := strings.TrimSpace(opts.ProviderName)
	if providerName == "" {
		providerName = "codex"
	}
	if !supportedProvider(providerName) {
		return nil, nil, fmt.Errorf("unsupported app-server provider %q", providerName)
	}
	bin := strings.TrimSpace(opts.Executable)
	if bin == "" {
		bin = strings.TrimSpace(opts.CodexExecutable)
	}
	if bin == "" {
		bin = providerName
	}
	bin, err := provider.ResolveExecutableFromEnvPATH(bin, opts.Env)
	if err != nil {
		return nil, nil, err
	}
	backend := opts.Backend
	if backend == nil {
		backend = isolation.NewPassthroughBackend()
	}
	handle, err := backend.Start(ctx, isolation.LaunchSpec{
		Path:            bin,
		Args:            appServerArgs(memoryLaunchArgs(opts), opts.AppServerSubcommand),
		Env:             append([]string(nil), opts.Env...),
		Dir:             opts.Cwd,
		SetProcessGroup: true,
		Context:         opts.LaunchContext,
	})
	if err != nil {
		return nil, nil, err
	}
	stdout, stdin := handle.Stdout(), handle.Stdin()
	go func() { _, _ = io.Copy(os.Stderr, handle.Stderr()) }()

	p := &Proc{
		handle:                   handle,
		stdin:                    stdin,
		cwd:                      opts.Cwd,
		memoryOptions:            opts,
		approvals:                make(map[string]pendingApproval),
		userInputs:               make(map[int64]*pendingUserInput),
		activeTools:              make(map[string]bool),
		zombieTurnIDs:            make(map[string]struct{}),
		terminalTurnIDs:          make(map[string]struct{}),
		watchdogConfig:           normalizeWatchdogConfig(providerName, opts.Watchdog),
		watchdogTimeoutUnit:      opts.WatchdogTimeoutUnit,
		watchdogInterruptTimeout: opts.WatchdogInterruptTimeout,
		idleCompletionDelay:      opts.IdleCompletionDelay,
	}
	if p.watchdogTimeoutUnit <= 0 || p.watchdogTimeoutUnit > time.Second {
		p.watchdogTimeoutUnit = time.Second
	}
	if p.watchdogInterruptTimeout <= 0 {
		p.watchdogInterruptTimeout = 2 * time.Second
	}
	if p.idleCompletionDelay <= 0 {
		p.idleCompletionDelay = stableIdleCompletionDelay
	}
	p.providerName = providerName
	p.turnFailureHint = opts.TurnFailureHint
	p.inputTempDir = environmentValue(opts.Env, "TMPDIR")
	p.inlineImages = opts.LaunchContext.Backend == principal.BackendLinuxDAC
	p.turnModel = strings.TrimSpace(opts.ResumeModel)
	p.turnEffort = strings.TrimSpace(opts.ResumeReasoningEffort)
	p.discoverSkills = opts.DiscoverSkills
	p.excludedSkillRoots = normalizeSkillRoots(opts.Cwd, opts.ExcludedSkillRoots)
	p.skillPaths = make(map[string]string)
	p.client = providerjsonrpc.NewClient(stdout, stdin)
	p.notificationsInitialized = true

	out := make(chan []byte, 256)
	p.registerHandlers(out)

	go func() {
		extraFrameBytes := 0
		if p.inlineImages {
			// App-server notifications can echo the entire submitted image set.
			extraFrameBytes = base64.StdEncoding.EncodedLen(sessioninput.MaxTotalBytes)
		}
		_ = p.client.RunWithExtraFrameBytes(extraFrameBytes)
		p.notificationMu.Lock()
		p.notificationQueue = nil
		p.notificationMu.Unlock()
		// If the provider process / stream died mid-turn, surface it as a
		// visible terminal failure instead of letting the broker loop end
		// silently (which left the turn active with no output and no error).
		// Skip when Close marked this shutdown as intentional (session
		// stop/restart/reconfigure, broker shutdown): the session side ends
		// the turn itself via abortTurn, so reporting here would surface a
		// spurious failure for a successful teardown. Race note: a genuine
		// crash racing a concurrent Close may be swallowed; accepted.
		if !p.closing.Load() && p.snapshotTurnID() != "" {
			p.emitTurnFailure("provider process exited", "")
		}
		close(out)
	}()

	// Codex launchers can spend seconds before answering initialize. Start the
	// independent, bounded feature probe now rather than adding its full cost
	// after that handshake. Other app-server dialects retain handshake gating.
	var probeDone chan struct{}
	var probeSupported bool
	var cancelProbe context.CancelFunc
	if providerName == "codex" {
		var probeCtx context.Context
		probeCtx, cancelProbe = context.WithCancel(ctx)
		probeDone = make(chan struct{})
		go func() {
			probeSupported = probeUserInput(probeCtx, opts, bin, backend)
			close(probeDone)
		}()
		defer func() {
			cancelProbe()
			<-probeDone
		}()
	}
	initializeStarted := time.Now()
	initializeParams := map[string]any{
		"clientInfo": map[string]string{"name": "amux", "version": "dev"},
	}
	if opts.ExperimentalAPI || managedMemory(opts) {
		initializeParams["capabilities"] = map[string]bool{"experimentalApi": true}
	}
	initializeResponse, err := p.client.Call(ctx, "initialize", initializeParams)
	if err != nil {
		abortStart(handle)
		return nil, nil, fmt.Errorf("%s initialize: %w", providerName, err)
	}
	log.Debug().Str("provider", providerName).Dur("duration_ms", time.Since(initializeStarted)).Msg("app-server startup: initialize")
	if opts.SendInitialized {
		if err := p.client.Notify("initialized", nil); err != nil {
			abortStart(handle)
			return nil, nil, fmt.Errorf("%s initialized: %w", providerName, err)
		}
	}

	var host struct {
		UserAgent string `json:"userAgent"`
	}
	hostSupportsDiscovery := json.Unmarshal(initializeResponse.Result, &host) == nil && host.UserAgent != ""
	probeWaitStarted := time.Now()
	if probeDone != nil {
		if !hostSupportsDiscovery {
			cancelProbe()
		}
		<-probeDone
		p.defaultModeUserInput = hostSupportsDiscovery && probeSupported
	} else if hostSupportsDiscovery {
		p.discoverUserInput(ctx, opts, bin, backend)
	}
	log.Debug().Str("provider", providerName).Dur("duration_ms", time.Since(probeWaitStarted)).Bool("supported", p.defaultModeUserInput).Msg("app-server startup: user-input probe wait")

	resp, err := p.startOrResumeThread(ctx, opts)
	if err != nil {
		abortStart(handle)
		return nil, nil, err
	}
	threadID, effectiveModel, err := decodeThreadMetadata(resp.Result)
	if err != nil {
		abortStart(handle)
		return nil, nil, err
	}
	if err := p.disableThreadMemory(ctx, opts, threadID); err != nil {
		abortStart(handle)
		return nil, nil, err
	}
	p.threadMu.Lock()
	p.threadID = threadID
	p.effectiveModel = effectiveModel
	p.threadMu.Unlock()
	p.discoverInputCapabilities(ctx, effectiveModel)
	p.emitSessionMetadata(threadID, effectiveModel)
	p.discoverGoals(ctx)
	p.threadMu.Lock()
	deferNativeGoal := p.goalSupported && p.goal != nil
	p.threadMu.Unlock()
	if !deferNativeGoal {
		p.enableGoalTurns()
	}
	if p.discoverSkills {
		p.scheduleSkillRefresh(false)
	}

	return p, out, nil
}

func (p *Proc) startOrResumeThread(ctx context.Context, opts StartOptions) (providerjsonrpc.Message, error) {
	params := map[string]any{}
	if opts.Cwd != "" {
		params["cwd"] = opts.Cwd
	}
	providerName := p.provider()
	if err := p.memoryThreadParams(ctx, opts, params); err != nil {
		return providerjsonrpc.Message{}, err
	}
	if err := p.runtimeThreadParams(ctx, opts, params); err != nil {
		return providerjsonrpc.Message{}, err
	}
	p.sessionInstructionThreadParams(ctx, opts, params)
	p.userInputThreadParams(params)
	if resumeID := strings.TrimSpace(opts.ResumeSessionID); resumeID != "" {
		params["threadId"] = resumeID
		if p.inlineImages {
			// App-server restores its own history. The broker only needs the
			// thread identity and settings, without echoing prior image payloads.
			params["excludeTurns"] = true
		}
		if model := strings.TrimSpace(opts.ResumeModel); model != "" {
			params["model"] = model
		}
		resp, err := p.client.Call(ctx, "thread/resume", params)
		if err != nil {
			return providerjsonrpc.Message{}, fmt.Errorf("%s thread/resume: %w", providerName, err)
		}
		return resp, nil
	}
	resp, err := p.client.Call(ctx, "thread/start", params)
	if err != nil {
		return providerjsonrpc.Message{}, fmt.Errorf("%s thread/start: %w", providerName, err)
	}
	return resp, nil
}

func decodeThreadMetadata(raw json.RawMessage) (string, string, error) {
	var resp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", fmt.Errorf("app-server thread response: %w", err)
	}
	if strings.TrimSpace(resp.Thread.ID) == "" {
		return "", "", errors.New("app-server thread response: missing thread.id")
	}
	return strings.TrimSpace(resp.Thread.ID), strings.TrimSpace(resp.Model), nil
}

func (p *Proc) PID() int {
	if p.handle != nil {
		return p.handle.PID()
	}
	return 0
}

// markFirstToken stamps firstTokenAt on the first streamed delta of the turn
// (the TTFT anchor) and opens the first generation segment for the rate
// accumulation. Later deltas keep both anchors.
func (p *Proc) markFirstToken() {
	p.usageMu.Lock()
	if p.turnStartAt != (time.Time{}) && p.firstTokenAt == (time.Time{}) {
		p.firstTokenAt = time.Now()
	}
	if p.firstTokenAt != (time.Time{}) && p.genActiveAt == (time.Time{}) && p.genElapsedNs == 0 {
		p.genActiveAt = p.firstTokenAt
	}
	p.usageMu.Unlock()
}

// pauseGenerationAt closes the open generation segment at now, if any. Called
// when a tool item starts: while tools run the model is not producing tokens,
// so the window must not keep accumulating.
func (p *Proc) pauseGenerationAt(now time.Time) {
	p.usageMu.Lock()
	if p.genActiveAt != (time.Time{}) {
		p.genElapsedNs += now.Sub(p.genActiveAt).Nanoseconds()
		p.genActiveAt = time.Time{}
	}
	p.usageMu.Unlock()
}

// resumeGenerationAt opens a new generation segment at now. Called when a
// delta arrives while no segment is open (the first delta, or the first delta
// after tool execution). Anchoring at the delta itself — not at tool
// completion — keeps post-tool model thinking out of the generation window.
func (p *Proc) resumeGenerationAt(now time.Time) {
	p.usageMu.Lock()
	if p.firstTokenAt != (time.Time{}) && p.genActiveAt == (time.Time{}) {
		p.genActiveAt = now
	}
	p.usageMu.Unlock()
}

// generationElapsedSeconds returns the active generation window through now:
// accumulated closed segments plus the currently open one. Zero before the
// first delta.
func (p *Proc) generationElapsedSecondsLocked(now time.Time) float64 {
	elapsed := p.genElapsedNs
	if p.genActiveAt != (time.Time{}) {
		elapsed += now.Sub(p.genActiveAt).Nanoseconds()
	}
	return time.Duration(elapsed).Seconds()
}

// turnTTFTMillis returns the time-to-first-token interval of the current turn
// in milliseconds, measured from the turn/start send to the first streamed
// delta. ok is false before the first delta arrives.
func (p *Proc) turnTTFTMillis() (int64, bool) {
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	if p.turnStartAt == (time.Time{}) || p.firstTokenAt == (time.Time{}) {
		return 0, false
	}
	ms := p.firstTokenAt.Sub(p.turnStartAt).Milliseconds()
	if ms < 0 {
		return 0, false
	}
	return ms, true
}

// outputTokensPerSec derives the generation speed from the turn's output token
// count over the active generation window (tool execution excluded). ok is
// false when the window is missing or too small for a stable figure.
func (p *Proc) outputTokensPerSec(outputTokens int64) (int64, bool) {
	if outputTokens <= 0 {
		return 0, false
	}
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	return p.outputTokensPerSecLocked(outputTokens, time.Now())
}

// outputTokensPerSecLocked computes tokens per second against an explicit now,
// letting turn-end finalization evaluate the full generation window. The
// minimum window still applies: less active generation than the floor yields
// no figure rather than an inflated burst rate.
func (p *Proc) outputTokensPerSecLocked(outputTokens int64, now time.Time) (int64, bool) {
	if outputTokens <= 0 || p.firstTokenAt == (time.Time{}) {
		return 0, false
	}
	elapsed := p.generationElapsedSecondsLocked(now)
	if elapsed < 0.5 {
		return 0, false
	}
	tps := float64(outputTokens) / elapsed
	if tps < 1 {
		return 0, false
	}
	return int64(tps), true
}

// finalizeTurnSpeedMetrics recomputes the speed fields on the turn's last usage
// payload at turn end. Providers commonly emit thread/tokenUsage/updated only
// once, early in the turn — before the 0.5s generation window elapses — so the
// mid-turn payload may carry ttft_ms but no tokens_per_sec. The turn boundary
// is the natural measurement point: output tokens and the generation window
// (tool execution excluded) are both final. Missing fields are filled in;
// existing values are left untouched so later provider usage frames keep their
// fresher numbers.
func (p *Proc) finalizeTurnSpeedMetrics(usage json.RawMessage) json.RawMessage {
	if len(usage) == 0 {
		return usage
	}
	var payload map[string]int64
	if err := json.Unmarshal(usage, &payload); err != nil {
		return usage
	}
	changed := false
	now := time.Now()
	if _, ok := payload["ttft_ms"]; !ok {
		if ttftMs, ok := p.turnTTFTMillis(); ok && ttftMs > 0 {
			payload["ttft_ms"] = ttftMs
			changed = true
		}
	}
	if _, ok := payload["tokens_per_sec"]; !ok {
		p.pauseGenerationAt(now)
		p.usageMu.Lock()
		tps, ok := p.outputTokensPerSecLocked(payload["output_tokens"], now)
		p.usageMu.Unlock()
		if ok {
			payload["tokens_per_sec"] = tps
			changed = true
		}
	}
	if !changed {
		return usage
	}
	updated, err := json.Marshal(payload)
	if err != nil {
		return usage
	}
	return updated
}

func (p *Proc) scheduleSkillRefresh(force bool) {
	if p == nil || !p.discoverSkills {
		return
	}
	p.skillsMu.Lock()
	if force {
		p.skillRefreshForce = true
	}
	if p.skillRefreshActive {
		p.skillRefreshAgain = true
		p.skillsMu.Unlock()
		return
	}
	p.skillRefreshActive = true
	p.skillsMu.Unlock()
	go p.runSkillRefresh()
}

func (p *Proc) runSkillRefresh() {
	for {
		p.skillsMu.Lock()
		force := p.skillRefreshForce
		p.skillRefreshForce = false
		p.skillsMu.Unlock()

		if err := p.refreshSkills(force); err != nil {
			log.Warn().Err(err).Str("provider", p.provider()).Str("cwd", p.cwd).
				Msg("provider: skill discovery failed; continuing without refreshed skills")
		}

		p.skillsMu.Lock()
		if !p.skillRefreshAgain {
			p.skillRefreshActive = false
			p.skillsMu.Unlock()
			return
		}
		p.skillRefreshAgain = false
		p.skillsMu.Unlock()
	}
}

func (p *Proc) refreshSkills(force bool) error {
	params := map[string]any{"forceReload": force}
	if cwd := strings.TrimSpace(p.cwd); cwd != "" {
		params["cwds"] = []string{cwd}
	}
	ctx, cancel := context.WithTimeout(context.Background(), skillListTimeout)
	defer cancel()
	resp, err := p.client.Call(ctx, "skills/list", params)
	if err != nil {
		return err
	}
	var result struct {
		Data []struct {
			Skills []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Enabled     bool   `json:"enabled"`
				Path        string `json:"path"`
			} `json:"skills"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("decode skills/list: %w", err)
	}
	paths := make(map[string]string)
	commands := make([]provider.AvailableCommand, 0)
	for _, entry := range result.Data {
		for _, skill := range entry.Skills {
			name := strings.TrimSpace(skill.Name)
			path := strings.TrimSpace(skill.Path)
			if !skill.Enabled || name == "" || path == "" || p.isExcludedSkillPath(path) {
				continue
			}
			if _, duplicate := paths[name]; duplicate {
				continue
			}
			paths[name] = path
			commands = append(commands, provider.AvailableCommand{
				Name: name, Description: strings.TrimSpace(skill.Description), Kind: "skill",
			})
		}
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	p.skillsMu.Lock()
	p.skillPaths = paths
	p.skillsSnapshot = append([]provider.AvailableCommand(nil), commands...)
	p.skillsLoaded = true
	p.skillsMu.Unlock()
	p.emitCommands(commands)
	return nil
}

func normalizeSkillRoots(cwd string, roots []string) []string {
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(cwd, root)
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		normalized = append(normalized, filepath.Clean(absolute))
	}
	return normalized
}

func (p *Proc) isExcludedSkillPath(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absolute = filepath.Clean(absolute)
	for _, root := range p.excludedSkillRoots {
		rel, err := filepath.Rel(root, absolute)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Write delivers one complete user input frame to the agent as a single
// turn/start. The Codex-compatible app-server `turn/start` takes a UserInput array
// whose text is the full request for this turn (see agent-adapter product
// model). Splitting the frame by newline — the legacy line-framing this proc
// used historically — breaks multi-line prompts (e.g. the Feishu thread-context
// wrapper) into one turn per line, so the model sees a bare `[Amux Feishu
// Thread Mode]` line as its own empty-ish turn and replies "no content". The
// input boundary here is the Write call itself (one Submit frame / one attach
// line), not a newline; trailing CR/LF from submitNow's "\r" flush trims to
// empty and is skipped.
func (p *Proc) Write(data []byte) (int, error) {
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	written := len(data)
	text := strings.TrimSpace(string(data))
	if text == "" {
		return written, nil
	}
	if err := p.sendTurn(text); err != nil {
		return 0, err
	}
	return written, nil
}

// Control executes a provider-native session operation without presenting the
// command as model input.
func (p *Proc) Control(action string) error {
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	providerName := p.provider()
	if !supportedProvider(providerName) {
		return fmt.Errorf("%s: session control %q is unsupported", p.provider(), action)
	}
	switch strings.TrimSpace(action) {
	case "compact":
		return p.compactThread()
	case "clear":
		return p.clearThread()
	default:
		return fmt.Errorf("%s: unknown session control %q", providerName, action)
	}
}

func (p *Proc) ReconfigureExecution(model, reasoningEffort string) error {
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	tid := p.snapshotThreadID()
	if tid == "" {
		return fmt.Errorf("%s: thread not started", p.provider())
	}
	model = strings.TrimSpace(model)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	ctx, cancel := context.WithTimeout(context.Background(), turnStartTimeout)
	defer cancel()
	if _, err := p.client.Call(ctx, "thread/settings/update", map[string]any{
		"threadId": tid,
		"model":    model,
		"effort":   reasoningEffort,
	}); err != nil {
		return fmt.Errorf("%s thread/settings/update: %w", p.provider(), err)
	}
	p.turnModel = model
	p.turnEffort = reasoningEffort
	return nil
}

func (p *Proc) beginTurnStartAttempt(method string, responseLess bool) *turnStartAttempt {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	p.turnStartGeneration++
	attempt := &turnStartAttempt{
		generation:   p.turnStartGeneration,
		method:       method,
		responseLess: responseLess,
	}
	if responseLess {
		attempt.started = make(chan struct{})
	}
	p.pendingTurnStart = attempt
	return attempt
}

func (p *Proc) compactThread() error {
	tid := p.snapshotThreadID()
	if tid == "" {
		return fmt.Errorf("%s: thread not started", p.provider())
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnStartTimeout)
	defer cancel()
	attempt := p.beginTurnStartAttempt("thread/compact/start", true)
	if _, err := p.client.Call(ctx, "thread/compact/start", map[string]any{"threadId": tid}); err != nil {
		return p.finishTurnStartError(attempt, err)
	}
	select {
	case <-attempt.started:
		return nil
	case <-ctx.Done():
		return p.finishTurnStartError(attempt, ctx.Err())
	}
}

func (p *Proc) clearThread() error {
	if p.snapshotTurnID() != "" {
		return fmt.Errorf("%s: cannot clear while a turn is active", p.provider())
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnStartTimeout)
	defer cancel()
	if p.GoalSupported() {
		goal, err := p.Goal(ctx, session.GoalRequest{Action: "get"})
		if err != nil {
			return err
		}
		if goal != nil {
			if _, err := p.Goal(ctx, session.GoalRequest{Action: "clear"}); err != nil {
				return err
			}
		}
	}
	params := map[string]any{}
	if strings.TrimSpace(p.cwd) != "" {
		params["cwd"] = p.cwd
	}
	if err := p.memoryThreadParams(ctx, p.memoryOptions, params); err != nil {
		return err
	}
	if err := p.runtimeThreadParams(ctx, p.memoryOptions, params); err != nil {
		return err
	}
	p.sessionInstructionThreadParams(ctx, p.memoryOptions, params)
	p.userInputThreadParams(params)
	resp, err := p.client.Call(ctx, "thread/start", params)
	if err != nil {
		p.emitTurnFailure(fmt.Sprintf("thread/start for clear: %v", err), "")
		return err
	}
	threadID, effectiveModel, err := decodeThreadMetadata(resp.Result)
	if err != nil {
		p.emitTurnFailure(fmt.Sprintf("thread/start for clear: %v", err), "")
		return err
	}
	if err := p.disableThreadMemory(ctx, p.memoryOptions, threadID); err != nil {
		return err
	}
	p.threadMu.Lock()
	p.cancelIdleCompletionLocked()
	p.stopTurnWatchdogLocked()
	p.threadID = threadID
	p.effectiveModel = effectiveModel
	p.turnID = ""
	p.lastFailureTurnID = ""
	p.zombieTurnID = ""
	p.zombieTurnIDs = make(map[string]struct{})
	p.terminalTurnIDs = make(map[string]struct{})
	p.pendingTurnStart = nil
	p.approvals = make(map[string]pendingApproval)
	p.userInputs = make(map[int64]*pendingUserInput)
	p.activeTools = make(map[string]bool)
	p.curTurnHadTool = false
	p.threadMu.Unlock()
	p.msgMu.Lock()
	p.curMsg.Reset()
	p.curItemID = ""
	p.msgMu.Unlock()
	p.usageMu.Lock()
	p.turnUsage = nil
	p.usageMu.Unlock()
	p.emitSessionMetadata(threadID, effectiveModel)
	if p.GoalSupported() {
		p.observeGoal(nil)
	}
	if p.discoverSkills {
		p.scheduleSkillRefresh(false)
	}
	p.emitControlDone()
	return nil
}

func (p *Proc) emitControlDone() {
	terminal := agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}
	p.emit(agentstream.ProducerEmission{
		Origin:   agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.provider(), NativeType: "control_done"},
		Terminal: &terminal,
	})
}

func (p *Proc) sendTurn(text string) error {
	input := make([]map[string]any, 0, len(p.turnInput(text)))
	for _, part := range p.turnInput(text) {
		converted := make(map[string]any, len(part))
		for key, value := range part {
			converted[key] = value
		}
		input = append(input, converted)
	}
	return p.sendTurnInput(input)
}

// Arm the turn watchdog from the turn/start response, which carries the
// initial turn object, so a dropped turn/started notification — the exact
// defect class this watchdog exists for — cannot leave the turn
// unwatched. turn/started re-arms and reconciles when it does arrive.

// finishTurnStartError retires one pending turn/start RPC. If a scoped
// lifecycle event already proved that the provider accepted the turn, the RPC
// deadline is only a missing acknowledgement: keep the proven turn under its
// watchdog instead of falsely releasing the queue while it continues
// server-side (notably while waiting for a user approval). An explicit RPC
// error contradicts the provisional lifecycle, so fail closed by cancelling
// the discovered turn before releasing the queue. With no discovered id,
// surface the submission failure; any later id is quarantined by the gates.
func (p *Proc) finishTurnStartError(attempt *turnStartAttempt, callErr error) error {
	method := strings.TrimSpace(attempt.method)
	if method == "" {
		method = "turn/start"
	}
	p.threadMu.Lock()
	if p.pendingTurnStart == attempt {
		p.pendingTurnStart = nil
	}
	candidate := attempt.candidateTurnID
	if candidate != "" && p.isTerminalTurnLocked(candidate) {
		p.threadMu.Unlock()
		return nil
	}
	if candidate != "" && p.turnID == candidate {
		if errors.Is(callErr, context.DeadlineExceeded) {
			p.threadMu.Unlock()
			return nil
		}
		p.markZombieTurnLocked(candidate)
		p.interruptTurnSyncOrdered(p.threadID, candidate)
		p.emitTurnFailureLocked(fmt.Sprintf("%s: %v", method, callErr), "")
		return callErr
	}
	p.lastFailureTurnID = ""
	p.threadMu.Unlock()
	reason := fmt.Sprintf("%s: %v", method, callErr)
	p.emitTurnFailure(reason, "")
	return callErr
}

// finishTurnStartResponse reconciles the authoritative RPC response with a
// provisional id discovered from turn/started or a scoped server request. A
// mismatch means an older provider turn crossed this submission boundary; fail
// closed by cancelling both ids and emitting one visible terminal failure.
func (p *Proc) finishTurnStartResponse(attempt *turnStartAttempt, responseTurnID string, decodeErr error) error {
	p.threadMu.Lock()
	if p.pendingTurnStart == attempt {
		p.pendingTurnStart = nil
	}
	candidate := attempt.candidateTurnID
	if decodeErr != nil || responseTurnID == "" {
		if candidate != "" && (p.turnID == candidate || p.isTerminalTurnLocked(candidate)) {
			p.threadMu.Unlock()
			return nil
		}
		p.lastFailureTurnID = ""
		p.threadMu.Unlock()
		if decodeErr == nil {
			decodeErr = errors.New("turn/start response missing turn.id")
		}
		p.emitTurnFailure(decodeErr.Error(), "")
		return decodeErr
	}
	if candidate != "" && candidate != responseTurnID {
		reason := fmt.Sprintf(
			"turn/start identity mismatch for submission %d: provisional %s, response %s",
			attempt.generation,
			candidate,
			responseTurnID,
		)
		// Retire the provisional turn and clear its locally surfaced waits. Use
		// the response id as the terminal turn for the submission so session-side
		// permission state is cleared by the emitted failed event.
		if p.turnID == candidate {
			p.stopTurnWatchdogLocked()
			p.turnID = ""
			p.clearPendingUserWaitsLocked()
		}
		if !p.isTerminalTurnLocked(candidate) {
			p.markTerminalTurnLocked(candidate)
			p.markZombieTurnLocked(candidate)
			p.interruptTurnSyncOrdered(p.threadID, candidate)
		}
		p.turnID = responseTurnID
		p.markZombieTurnLocked(responseTurnID)
		p.interruptTurnSyncOrdered(p.threadID, responseTurnID)
		p.emitTurnFailureLocked(reason, "")
		return errors.New(reason)
	}
	if p.turnID == "" && !p.isTerminalTurnLocked(responseTurnID) {
		p.freshArmLocked(responseTurnID)
	}
	p.threadMu.Unlock()
	return nil
}

// freshArmLocked adopts turnID as a brand-new active turn: cancels any idle
// completion, clears the per-current-turn failure dedup marker, and (re)arms
// the watchdog with fresh per-turn state. Outstanding zombie tombstones are
// intentionally retained until their own server terminal frames arrive. The
// caller must hold p.threadMu and must only call this for a turn id that is NOT
// already active (see the turn/started handler for the idempotent path).

// adoptPendingTurnCandidateLocked provisionally binds turnID to the current
// turn/start attempt. The later RPC response is authoritative and reconciles
// this candidate in finishTurnStartResponse. The caller must hold p.threadMu.

type eventScope struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Turn     struct {
		ID string `json:"id"`
	} `json:"turn"`
}

// claimTurnCompletion lets the authoritative turn/completed signal finalize
// the active turn and cancel any pending idle fallback.

// clearPendingUserWaitsLocked drops all unanswered approval/user-input
// requests. Called when a turn ends (completed, failed, or idle-completed):
// the maps are connection-scoped, so without this a late response to a dead
// turn's permission would touch the NEW turn's wait state and prematurely
// resume its idle watchdog. The provider side is unblocked by the turn end
// itself (or by the watchdog's best-effort turn/interrupt for zombie turns).
// The caller must hold p.threadMu.

// A turn blocked on a user response is genuinely not done: re-arm so
// the completion waits until the user answers (the hard cap bounds an
// unanswered wait).

// Tool items still "active": do NOT complete immediately at the 5s
// debounce (a running silent tool would be cut off and its result
// dropped), and do NOT re-arm forever (a lost item/completed would
// wedge). Judge by the tool's own budget instead: re-arm while the tool
// is comfortably within budget, and once the deadline is within one
// debounce (or passed) treat the item as lost and COMPLETE — delivering
// the accumulated result, which beats the tool-idle failure that would
// discard it at the same deadline.

// toolBudgetDeadlineLocked returns the moment after which a still-"active"
// tool may be presumed lost/stuck for idle-completion purposes, derived from
// the same budget the watchdog would use to judge it: the tool idle timeout
// for progress-capable tools when that layer is enabled, else the turn hard
// cap, else the turn idle timeout. The zero Time means no budget is configured
// (all layers disabled), in which case a stable idle is taken at face value.
// The caller must hold p.threadMu.

// deferPendingIdleCompletion extends an existing idle candidate while the
// provider continues producing activity without first reporting active.

func (p *Proc) Interrupt() error {
	p.threadMu.Lock()
	tid, turn := p.threadID, p.turnID
	live := turn != ""
	if !live {
		// No live turn: fall back to the most recently failed turn, which may
		// still be running server-side. Prefer zombieTurnID (watchdog-declared,
		// cleared once a terminal frame confirms death); otherwise lastFailureTurnID
		// covers provider-reported failures (error notification / systemError),
		// which are NOT verified server-terminal either.
		turn = p.zombieTurnID
		if turn == "" {
			turn = p.lastFailureTurnID
		}
	}
	p.threadMu.Unlock()
	if tid == "" || turn == "" {
		return nil
	}
	if !live {
		// Fallback for a possibly-dead (zombie or provider-reported) turn: send
		// best-effort and swallow any error/timeout. The turn may already be
		// finished server-side, and the request may already have been written
		// when a timeout fires — either way an error is not a real user-facing
		// failure. Surfacing one would be a spurious "interrupt failed" (and,
		// for preempt submits, a spurious Submit error while the queued input
		// still runs, inviting a duplicate).
		p.interruptTurnBestEffort(tid, turn)
		return nil
	}
	// Bound the call: the fallback target is by definition a possibly-wedged
	// provider, so an unbounded Call would hang the interrupt path (and the
	// broker control connection / gRPC handler behind it) forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := p.client.Call(ctx, "turn/interrupt", map[string]string{
		"threadId": tid,
		"turnId":   turn,
	})
	return err
}

// notePossibleZombieDeath consumes a terminal frame (turn/completed or
// turn/failed) scoped to a turn we declared a zombie: it confirms the zombie
// died server-side, so we stop targeting it with the Interrupt() fallback.

func (p *Proc) RespondPermission(permissionID, optionID string) error {
	// item/tool/requestUserInput permissions use "<reqID>:<questionId>" ids.
	if _, _, ok := parseUserInputPermissionID(permissionID); ok {
		return p.respondUserInput(permissionID, []string{optionID})
	}
	p.threadMu.Lock()
	pending, ok := p.approvals[permissionID]
	if ok {
		delete(p.approvals, permissionID)
		p.reconcileWatchdogTimersLocked()
	}
	p.threadMu.Unlock()
	if !ok {
		return fmt.Errorf("%s: unknown permission id %q", p.provider(), permissionID)
	}
	payload := approvalResponsePayload(pending.method, optionID)
	return p.client.Respond(pending.reqID, payload)
}

// RespondPermissionMulti responds to a multiSelect user-input permission with
// multiple selected option labels. Non-user-input permissions reject this path.
func (p *Proc) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	if _, _, ok := parseUserInputPermissionID(permissionID); !ok {
		return fmt.Errorf("%s: multi-select response not supported for permission %q", p.provider(), permissionID)
	}
	return p.respondUserInput(permissionID, optionIDs)
}

func (p *Proc) provider() string {
	name := strings.TrimSpace(p.providerName)
	if name == "" {
		return "codex"
	}
	return name
}

func (p *Proc) SetEventSink(fn func(agentstream.ProducerEmission) bool) {
	p.eventSinks.SetGlobal(fn)
	if fn != nil {
		threadID, effectiveModel := p.snapshotThreadMetadata()
		if threadID != "" || effectiveModel != "" {
			fn(agentstream.ProducerEmission{
				Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.provider(), NativeType: "session_metadata"},
				Session: agentstream.SessionMetadataChanged{AgentSessionID: threadID, EffectiveModel: effectiveModel},
			})
		}
		p.skillsMu.Lock()
		loaded := p.skillsLoaded
		commands := append([]provider.AvailableCommand(nil), p.skillsSnapshot...)
		p.skillsMu.Unlock()
		if loaded {
			p.emitCommands(commands)
		}
	}
}

func (p *Proc) Close() error {
	// Mark the shutdown as intentional before touching the process: every
	// Close caller (session Kill for stop/restart/reconfigure, broker
	// shutdown) is a deliberate teardown, so the exit watcher must not
	// report the resulting exit as a "provider process exited" turn
	// failure. A process crashing on its own never reaches Close, so
	// genuine mid-turn failures are still reported.
	p.closing.Store(true)
	p.cancelIdleCompletion()
	p.stopTurnWatchdog()
	p.cleanupInputStaging()
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.handle != nil {
		_ = p.handle.Signal(context.Background(), "TERM")
		_ = p.handle.Close()
	}
	return nil
}

// ─── notification + request dispatch ───────────────────────────────────────

// Notification handlers execute on the JSON-RPC read loop. Refresh on
// another goroutine so the response can be read and dispatched.

// Accumulate the final message item so turn/completed can emit a native
// result. A new itemId supersedes the previous one (keep the last item's
// full text).

// decodeEventScope reads the turn id from either the top-level turnId
// or the nested turn.id, so a malformed/alternate-shape notification
// with no id at all yields "" — in which case we must NOT clobber a
// turn already armed from the turn/start response.

// This turn already reached a client terminal state. A delayed or
// replayed turn/started must never resurrect it.

// Already armed for this turn from the turn/start response: this is
// a duplicate/late turn/started. Stay idempotent — do NOT reset the
// watchdog, activeTools, pendingUserWaits, or curMsg accumulated
// since arming. This duplicate IS the first EventTurnStarted in the
// response-armed flow (response reconciliation does not emit it), so still
// emit below.

// A live turn is already armed: a turn/started for a DIFFERENT turn
// is a stale straggler (single active turn per thread). Quarantine it
// rather than merely dropping it server-side as an unknown active turn.

// A terminal frame for a non-active turn may confirm a zombie's death.

// Emit the native result (accumulated final agent message) before the
// terminal turn_done so the task runner has it at the terminal signal.

// thread/status/changed carries the Codex-compatible app-server thread status
// Providers can use status.type="idle" both after a followup that has no separate
// turn/completed and transiently between internal phases of one submission.
// Treat idle as a debounced fallback: active or further activity cancels or
// extends it, while turn/completed remains the authoritative immediate exit.
// notLoaded and unknown statuses are not turn-end signals; systemError uses
// the failure exit.

// Candidate fallback for provider turns that wait for input without a
// turn/completed. thread/status carries no turn id, so distrust it
// only when a zombie turn exists (a just-interrupted zombie's idle
// could straggle in and complete a freshly armed live turn that is
// still silently thinking). With no zombie, idle is trustworthy —
// schedule completion even for a turn that produced nothing yet
// (a zero-output completion), so such turns are not
// killed as a spurious idle timeout.

// A thread-level active cancels a pending idle completion and counts
// as activity for the watchdog, but does NOT set curTurnProduced —
// the status channel carries no turn id, so it must not arm the
// stale-idle trust bit for a turn that has produced nothing scoped.

// Provider-reported system error: surface as a visible terminal
// failure instead of silently ignoring it (which left the turn
// hanging with no output and no error).

// notLoaded / unknown: not turn-end signals, ignore.

// A terminal frame for a non-active turn may confirm a zombie's death.

// Current Codex-compatible app-server schemas wrap diagnostics in
// params.error. Keep the legacy flat fields for older providers.

// When a turn is active, route through emitTurnFailure so the turn gets
// a terminal failed outcome and the error becomes visible. Without an
// active turn, emit a session-scoped Diagnostic so the error is not silently
// dropped.

// Tool execution lifecycle. The Codex-compatible app-server emits item lifecycle
// notifications (item/started, item/completed) whose `item` field is a
// ThreadItem discriminated by `type` (commandExecution / fileChange / ...),
// plus incremental output deltas. We map command/file items directly to
// canonical ToolChanged so the bridge (Feishu activity card) and CLI attach
// (line-mode tool timeline) can show tool execution detail.

// Incremental stdout/stderr for a running command. Emit as a running
// tool_updated carrying the delta as OutputPreview; downstream (acptui
// tool digest, bridge activity card) aggregates per item.

// Patch progress update: emit a running file_change tool event with the
// current change set as Input/Title so the UI shows which files are
// being touched.

// MCP tool progress: surface the provider's progress message as a
// running tool event's OutputPreview so the UI can show live status.

// item/tool/requestUserInput (user questions): provider asks the user
// to answer one or more questions. Split into N permissions and aggregate.

// Observe unhandled server->client requests.
// The jsonrpc client already replies with -32601 so the provider does not
// block; this hook surfaces the drop as a visible error when a turn is
// active, instead of the failure being silent.

// isStaleTurnScopeLocked reports whether a server->client request carries a
// thread/turn scope that does not match the active turn and cannot be adopted
// (a zombie turn the watchdog failed client-side, or a cross-turn straggler).
// Such requests are dropped WITHOUT a response: responding could unblock
// server-side execution for a turn whose events amux no longer surfaces, while
// leaving the request pending keeps it blocked until the watchdog's
// turn/interrupt cancels the zombie. Unscoped requests cannot be validated and
// are accepted, matching the pre-gate behavior. The caller must hold p.threadMu.
//
// A scoped request proves the provider created a turn, so when no turn is
// currently armed (the response-to-arm window, with turn/started dropped) and
// the scope matches our thread and is NOT a turn we already declared dead, we
// arm from the scope instead of dropping the request — otherwise a valid
// approval would be dropped without a response and the provider blocks forever.

// A finalized turn stays terminal for the lifetime of this provider
// connection; delayed requests must not resurrect it.

// Register the wait under threadMu: the stale-turn gate, dedup, map insert,
// and watchdog reconcile must be one atomic step so a turn boundary or a
// retransmitted request cannot corrupt the live turn's wait accounting.

// Only a scope-verified request marks the turn produced; an unscoped one
// (possibly a zombie's) must not arm the stale-idle trust bit.

// Duplicate/retransmitted request: already surfaced and counted.

// Register AND emit under threadMu so a watchdog timeout cannot interleave
// between them: if it emitted the terminal failure between registration and
// emission, the permission event would arrive after the session cleared
// pendingPermissions and re-pollute it (the answer then fails proc-side,
// leaving the entry stuck and blocking session reclaim forever).

// handleUserInputRequest handles item/tool/requestUserInput (user questions).
// One request carries N questions; amux emits one permission_requested event per
// question (permission_id = "<reqID>:<questionId>") and aggregates the answers,
// responding to the server once every question has been answered.

// Malformed request: respond with an error so the server doesn't wait.

// Stale-gate + register + produced-mark under one threadMu section (atomic
// with any turn boundary, matching handleApprovalRequest).

// Duplicate/retransmitted request id: already surfaced and counted.

// Emit one permission per question (under threadMu, so no terminal failure
// can interleave between registration and emission and re-pollute the
// session's just-cleared pendingPermissions).

// respondUserInput records an answer for one question of a requestUserInput
// request. When every question has been answered, it responds to the server.
// optionIDs carries the selected labels (1 for single-select, 1+ for multiSelect).
// Returns an error if the permission id is unknown.

// Filter empty labels and dedupe while preserving order.

// Build the response preserving question order.

// userInputPermissionID composes "<reqID>:<questionId>".

// parseUserInputPermissionID splits "<reqID>:<questionId>" back out.

type inputQuestionParams struct {
	ID          string              `json:"id"`
	Header      string              `json:"header"`
	Question    string              `json:"question"`
	MultiSelect bool                `json:"multiSelect"`
	AllowOther  bool                `json:"isOther"`
	Secret      bool                `json:"isSecret"`
	Options     []inputOptionParams `json:"options"`
}

type inputOptionParams struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

var errNotUserInputPermission = errors.New("not a user-input permission id")

// First-pass: only allow / deny; granting a permission profile is
// codex-specific and not yet modelled here.

// Always deny for now. amux does not yet model permission profile grants.

// ─── event sink helpers ────────────────────────────────────────────────────

// codexErrorInfoCode extracts the stable diagnostic category from the
// Codex-compatible TurnError.codexErrorInfo union. Current schemas encode simple
// categories as strings and categories with metadata as a single-key object.

// emitTurnDone flushes the accumulated final agent message as a native result
// event and then emits the terminal turn_done state change. It is the single
// exit point for turn completion, shared by the turn/completed and
// thread/status/changed(idle) handlers. Callers must have already cleared
// the active turn through claimTurnCompletion.

// emitTurnFailure is the single exit point for provider-side turn failures,
// the failure counterpart to emitTurnDone. It emits (1) a visible, replayable
// `[system] <provider>: <reason>` output frame so the error shows in attach
// and later replay, and (2) a terminal `turn_done(failed)` state change
// carrying a typed EventError. It deduplicates per turn via
// lastFailureTurnID (cleared on turn/started) so multiple error signals for
// the same dead turn don't double-emit, without suppressing the next turn's
// real failure. errorCode is the provider error code if known (e.g. from
// turn/failed or the top-level error notification); empty means unknown.

// emitTurnFailureLocked emits a terminal failed event. The caller must hold
// p.threadMu. It stops all watchdog timers and clears the active turn.

// Dedup: if we already emitted a failure for this turn (turnID still set
// and equals lastFailureTurnID, or turnID cleared but lastFailureTurnID
// matches the turn we just failed), suppress. turn/started clears
// lastFailureTurnID so the next turn can fail visibly.

// turnID already cleared by a prior failure for this turn; suppress
// the duplicate.

// Drop the failed turn's pending user waits under the lock (the maps are
// threadMu-guarded) so a late response cannot touch the next turn's state.

// Drop the failed turn's partially accumulated message and usage so they
// cannot leak into the NEXT turn's result: emitTurnDone flushes curMsg
// verbatim at the next turn/completed, and a tool-only turn produces no
// delta that would reset it.

// emitProviderError emits a typed provider EventError plus a visible
// `[system]` frame, used when a provider error arrives with no active turn
// (so there is no turn to fail terminally, but the error must still be
// visible instead of silently dropped).

// ─── item lifecycle / tool event mapping ───────────────────────────────────

// itemLifecycleNotification carries a ThreadItem at its started/completed
// lifecycle points (ItemStartedNotification / ItemCompletedNotification).
type itemLifecycleNotification struct {
	Item     threadItem `json:"item"`
	ThreadID string     `json:"threadId"`
	TurnID   string     `json:"turnId"`
	// startedAtMs / completedAtMs are present but unused for event mapping.
}

type tokenUsageBreakdown struct {
	CachedInputTokens        int64 `json:"cachedInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	ReasoningOutputTokens    int64 `json:"reasoningOutputTokens"`
	TotalTokens              int64 `json:"totalTokens"`
}

type threadTokenUsageUpdatedNotification struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	TokenUsage struct {
		Last               tokenUsageBreakdown  `json:"last"`
		Total              *tokenUsageBreakdown `json:"total"`
		ModelContextWindow *int64               `json:"modelContextWindow"`
	} `json:"tokenUsage"`
}

// threadItem is the discriminated union of codex ThreadItem types. The
// tool-like variants (commandExecution, fileChange, mcpToolCall,
// dynamicToolCall, collabAgentToolCall, webSearch) are mapped to canonical
// ToolChanged; narrative types (agentMessage, reasoning, plan,
// userMessage, ...) are handled elsewhere or ignored.
type threadItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Status  string `json:"status,omitempty"`
	// commandExecution fields
	AggregatedOutput string `json:"aggregatedOutput,omitempty"`
	ExitCode         *int   `json:"exitCode,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	// fileChange fields
	Changes []fileUpdateChange `json:"changes,omitempty"`
	// mcpToolCall / dynamicToolCall fields. `Tool` is the tool name; `Server`
	// is the MCP server (mcpToolCall only). `Namespace` is the dynamic tool
	// namespace (dynamicToolCall only). `Arguments` is the raw call args.
	Tool      string          `json:"tool,omitempty"`
	Server    string          `json:"server,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// collabAgentToolCall fields
	AgentNickname string `json:"agentNickname,omitempty"`
	AgentRole     string `json:"agentRole,omitempty"`
	// webSearch fields
	Query string `json:"query,omitempty"`
	// agentMessage text (unused for tool mapping but parsed for completeness).
	Text      string               `json:"text,omitempty"`
	Delivery  string               `json:"delivery,omitempty"`
	Questions []asyncInputQuestion `json:"questions,omitempty"`
}

type asyncInputQuestion struct {
	Title   string   `json:"title"`
	Options []string `json:"options"`
}

// fileUpdateChange is one file change in a fileChange item / patchUpdated
// notification. Kind is an object {type: add|delete|update}.
type fileUpdateChange struct {
	Path string          `json:"path"`
	Diff string          `json:"diff"`
	Kind json.RawMessage `json:"kind"`
}

// fileChangePatchUpdatedNotification is the item/fileChange/patchUpdated
// notification payload.
type fileChangePatchUpdatedNotification struct {
	Changes  []fileUpdateChange `json:"changes"`
	ItemID   string             `json:"itemId"`
	ThreadID string             `json:"threadId"`
	TurnID   string             `json:"turnId"`
}

// emitItemToolEvent maps a tool-like ThreadItem at its started/completed
// lifecycle point directly to canonical ToolChanged. `started` is true for
// item/started (running), false for item/completed (terminal). Non-tool item
// types (agentMessage, reasoning, plan, userMessage, ...) are ignored here.

// agentMessage / reasoning / plan / userMessage / taskNotification /
// hookPrompt: not tool events; agentMessage is already streamed via
// item/agentMessage/delta.

// Terminal: classify completed vs failed and attach output preview.

// emitGenericToolCallEvent maps mcpToolCall / dynamicToolCall /
// collabAgentToolCall / webSearch items to a structured tool event. These all
// carry a tool name (or a derivable title) and a status, so they share one
// rendering path. MCP/dynamic tool calls additionally carry raw `arguments`.

// genericToolNameAndTitle derives the amux tool name and a human-readable
// title from a generic tool-call item. MCP calls use the server namespace;
// collab agent calls use the agent role/nickname; webSearch uses the query.

// genericToolCallPhase maps codex McpToolCallStatus / DynamicToolCallStatus to
// the canonical tool lifecycle.

// commandExecToolPhase maps codex CommandExecutionStatus to the canonical tool
// lifecycle.

// summarizeFileChanges builds a human-readable title (e.g. "src/a.go (+1) src/b.go (-3)")
// and a JSON input payload from a set of file changes.

// patchChangeKindType extracts the {type: add|delete|update} discriminator.

// truncateOutput caps a tool output preview to keep attach frames and activity
// cards readable.

// abortStart tears down a handle whose start never completed. Kill stops the
// child and Close releases this side's stdio; the backend no longer reclaims
// those descriptors on reap, so a failed start must release them itself.
