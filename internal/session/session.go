package session

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/replay"
	"github.com/amuxos/amux/internal/sourceframe"
)

var (
	ErrProcessUnavailable = errors.New("session process unavailable")
	ErrQueueItemCancelled = errors.New("queue item cancelled")
)

const (
	MinIdleTimeoutSeconds = 60

	SleepReasonManual        = "manual"
	SleepReasonIdleTimeout   = "idle_timeout"
	SleepReasonRestartFailed = "restart_failed"
)

type State string

const (
	StateCreated  State = "created"
	StateRunning  State = "running"
	StateIdle     State = "idle"
	StateSleeping State = "sleeping"
	StateArchived State = "archived"
)

type EventKind string

const (
	EventOutput            EventKind = "output"
	EventStateChange       EventKind = "state_change"
	EventPermissionRequest EventKind = "permission_request"
)

type Event struct {
	Kind         EventKind
	Data         []byte
	Source       string
	Input        bool
	State        string
	StopReason   string
	PermissionId string
}

// Session represents a managed agent process.
type Session struct {
	maintenance           func() bool
	ID                    string
	Name                  string
	TimelineEpoch         string
	AgentProfile          string
	EffectiveModel        string
	InputModalities       []string
	InputCapabilitySource string
	AgentType             string
	State                 State
	PID                   int
	Cwd                   string
	BrokerSocket          string
	ExtraArgs             []string
	Env                   []string
	WorkspaceSource       string
	WorkspaceScope        string
	AgentSessionID        string
	OwnerTaskId           string // non-empty => owned by a distributed task; direct mutating RPCs are rejected
	AgentInstanceID       string // non-empty => owned by a persistent agent instance; skipped by idle reclaim, reused across tasks
	ExecutionIdentity     principal.ExecutionIdentity
	LaunchID              string
	LaunchSpec            string
	HandoffContext        string
	IdleTimeoutSeconds    int
	IdleTimeoutSet        bool
	LastActivityAt        time.Time
	LastInteractionAt     time.Time
	LastSleepReason       string
	CreatedAt             time.Time
	UpdatedAt             time.Time

	Replay   *replay.Buffer
	replayMu sync.Mutex

	procLifecycleMu            sync.Mutex
	runtimeMu                  sync.RWMutex
	proc                       Proc
	procGeneration             uint64
	intentionalCloseGeneration uint64

	queueMu      sync.Mutex
	queueBusy    bool
	queueGoal    bool
	queueNextID  int64
	queuePending []*queuedInput
	// Serializes queue advancement and explicit promotion of a pending item.
	queueSteerMu     sync.Mutex
	queueIdle        chan struct{}
	goalMu           sync.Mutex
	goalControlMu    sync.Mutex
	goalGrant        *GoalGrant
	persistGoalGrant func(*GoalGrant) error
	// parkedNotices are subtask completion notices whose turn was torn down
	// before the model processed them; released when a process is installed.
	parkedNotices []*queuedInput
	turnTerminals map[string]*agentstream.Terminal

	tapMu sync.Mutex
	tap   func(agentstream.Event) bool

	activityMu          sync.Mutex
	lastActivityPersist time.Time
	pendingPermissions  map[string]struct{}

	turnMu              sync.Mutex
	canonicalTurn       *agentstream.Turn
	canonicalAuth       TurnAuthorization
	canonicalDispatched bool
	// canonicalInterruptedTurn records the turn whose cancellation was
	// accepted, so later input is never steered into a turn that is ending.
	canonicalInterruptedTurn string

	timelineMu              sync.Mutex
	timeline                *agentstream.Timeline
	timelineFaultOnce       sync.Once
	canonicalAgentSessionID string
	canonicalEffectiveModel string

	persistHandoff func(string) error
}

type Snapshot struct {
	ID                    string
	Name                  string
	AgentProfile          string
	EffectiveModel        string
	InputModalities       []string
	InputCapabilitySource string
	AgentType             string
	State                 State
	PID                   int
	Cwd                   string
	BrokerSocket          string
	ExtraArgs             []string
	Env                   []string
	WorkspaceSource       string
	WorkspaceScope        string
	AgentSessionID        string
	OwnerTaskId           string
	AgentInstanceID       string
	ExecutionIdentity     principal.ExecutionIdentity
	LaunchID              string
	LaunchSpec            string
	HandoffContext        string
	IdleTimeoutSeconds    int
	IdleTimeoutSet        bool
	LastActivityAt        time.Time
	LastInteractionAt     time.Time
	LastSleepReason       string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (s *Session) Snapshot() Snapshot {
	s.runtimeMu.RLock()
	snap := Snapshot{
		ID:                    s.ID,
		Name:                  s.Name,
		AgentProfile:          s.AgentProfile,
		EffectiveModel:        s.EffectiveModel,
		InputModalities:       append([]string(nil), s.InputModalities...),
		InputCapabilitySource: s.InputCapabilitySource,
		AgentType:             s.AgentType,
		State:                 s.State,
		PID:                   s.PID,
		Cwd:                   s.Cwd,
		BrokerSocket:          s.BrokerSocket,
		ExtraArgs:             append([]string(nil), s.ExtraArgs...),
		Env:                   append([]string(nil), s.Env...),
		WorkspaceSource:       s.WorkspaceSource,
		WorkspaceScope:        s.WorkspaceScope,
		AgentSessionID:        s.AgentSessionID,
		OwnerTaskId:           s.OwnerTaskId,
		AgentInstanceID:       s.AgentInstanceID,
		ExecutionIdentity:     s.ExecutionIdentity,
		LaunchID:              s.LaunchID,
		LaunchSpec:            s.LaunchSpec,
		HandoffContext:        s.HandoffContext,
		IdleTimeoutSeconds:    s.IdleTimeoutSeconds,
		IdleTimeoutSet:        s.IdleTimeoutSet,
		LastSleepReason:       s.LastSleepReason,
		CreatedAt:             s.CreatedAt,
		UpdatedAt:             s.UpdatedAt,
	}
	s.runtimeMu.RUnlock()
	s.activityMu.Lock()
	snap.LastActivityAt = s.LastActivityAt
	snap.LastInteractionAt = s.LastInteractionAt
	s.activityMu.Unlock()
	return snap
}

func (s *Session) StateSnapshot() State {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.State
}

func (s *Session) SetBrokerSocket(path string) {
	s.runtimeMu.Lock()
	s.BrokerSocket = path
	s.runtimeMu.Unlock()
}

// SetEventTap installs the Session-policy observer used by amuxlet to persist
// provider metadata and resolve permissions after Timeline acceptance.
func (s *Session) SetEventTap(fn func(agentstream.Event) bool) {
	s.tapMu.Lock()
	s.tap = fn
	s.tapMu.Unlock()
}

type QueueItem struct {
	Waiting   bool
	ID        string
	TurnID    string
	Source    string
	Text      string
	Control   string
	CreatedAt time.Time
}

// TurnAuthorization is runtime-internal metadata attached by trusted input
// adapters. It is deliberately absent from QueueItem snapshots and provider
// input so a later caller cannot inherit or model-generate it.
type TurnAuthorization struct {
	LocalExpertAuthoring bool
}

type queuedInput struct {
	QueueItem
	input                  UserInput
	preempt                bool
	auth                   TurnAuthorization
	expectedAgentSessionID string
	lifecycle              func() error
	requeues               int
	done                   chan error
}

// RecordReplayEvent appends a visible non-agent system transcript frame.
// Canonical agent events reach replay through observeCanonicalEvent instead.
func (s *Session) RecordReplayEvent(ev Event) {
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if s.Replay != nil && len(ev.Data) > 0 {
		s.Replay.Append(ev.Data)
	}
	s.applySessionEvent(ev)
}

func (s *Session) AppendReplay(data []byte) {
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if s.Replay != nil && len(data) > 0 {
		s.Replay.Append(data)
	}
}

func (s *Session) applySessionEvent(ev Event) bool {
	if ev.Kind == "" {
		ev.Kind = EventOutput
	}
	s.noteEventActivity(ev, time.Now())
	return false
}

// FailActiveTurn finishes the current Timeline turn. Process owners call it
// when an agent exits without a protocol-level terminal frame, so queued input
// cannot remain blocked forever.
func (s *Session) FailActiveTurn(stopReason string) bool {
	return s.failCanonicalTurn(stopReason)
}

// ActiveTurnAuthorization returns the authorization attached to the turn that
// is executing now. Terminal/error paths clear it with the active TurnID;
// interrupt revokes the authorization immediately while terminal remains
// asynchronous.
func (s *Session) ActiveTurnAuthorization() (string, TurnAuthorization, bool) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if s.canonicalTurn != nil {
		return s.canonicalTurn.Ref().ID, s.canonicalAuth, true
	}
	return "", TurnAuthorization{}, false
}

// revokeActiveTurnAuthorization removes privileged turn metadata as soon as
// cancellation is accepted. The provider may emit its terminal frame later,
// so the active Timeline turn remains while its authority is revoked.
func (s *Session) revokeActiveTurnAuthorization() {
	s.turnMu.Lock()
	s.canonicalAuth = TurnAuthorization{}
	if s.canonicalTurn != nil {
		s.canonicalInterruptedTurn = s.canonicalTurn.Ref().ID
	}
	s.turnMu.Unlock()
}

func isTerminalTurnEvent(ev Event) bool {
	if ev.Kind == EventStateChange {
		// "turn_done" is the clean-completion state; "failed"/"interrupted"
		// are terminal too. This helper is only for non-agent system replay;
		// canonical Agent terminals are handled by completeCanonicalTurn.
		switch ev.State {
		case "turn_done", "failed", "interrupted":
			return true
		}
	}
	return false
}

func (s *Session) MarkActivity(now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	s.activityMu.Lock()
	s.LastActivityAt = now
	s.activityMu.Unlock()
}

// InteractionAt returns the last real user/agent interaction timestamp used by
// the reclaim clock. Internal state writes and system notices do not bump it.
func (s *Session) InteractionAt() time.Time {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	return s.LastInteractionAt
}

func (s *Session) ApplyEventActivity(ev Event, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	s.noteEventActivity(ev, now)
}

func (s *Session) noteEventActivity(ev Event, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	s.LastActivityAt = now
	// An autonomous FAILED turn (watchdog timeout, provider error) is not a
	// genuine interaction: it fires only after a stretch of silence, so letting
	// it bump LastInteractionAt would postpone idle reclaim past the last real
	// activity. A clean turn_done / interrupted still bumps it — turn
	// completion is meaningful recent activity, and a long zero-output turn
	// must not become reclaim-eligible the instant it finishes.
	autonomousFailure := ev.Kind == EventStateChange && ev.State == "failed"
	if ev.Source != sourceframe.SourceSystem && !autonomousFailure {
		s.LastInteractionAt = now
	}
	switch ev.Kind {
	case EventPermissionRequest:
		if ev.PermissionId != "" {
			if s.pendingPermissions == nil {
				s.pendingPermissions = make(map[string]struct{})
			}
			s.pendingPermissions[ev.PermissionId] = struct{}{}
		}
	case EventStateChange:
		// Clear pending permissions on any terminal turn event, not only a
		// clean turn_done: a failed turn (watchdog timeout, turn/failed) can
		// end while an approval/user-input request is still unanswered, and
		// leaking the entry blocks session sleep/archive/prune reclaim
		// forever.
		if isTerminalTurnEvent(ev) {
			s.pendingPermissions = nil
		}
	}
}

func (s *Session) PendingPermissionCount() int {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	return len(s.pendingPermissions)
}

func (s *Session) QueueState() (busy bool, pending int) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.queueBusy, len(s.queuePending)
}

// TryLockIdleQueue establishes a turn-boundary barrier for lifecycle changes.
// On success, new submissions block until unlock is called, so a cwd switch
// cannot race with an input accepted against the previous workspace.
func (s *Session) TryLockIdleQueue() (unlock func(), busy bool, pending int) {
	s.queueMu.Lock()
	busy = s.queueBusy
	pending = len(s.queuePending)
	if busy || pending > 0 {
		s.queueMu.Unlock()
		return nil, busy, pending
	}
	return s.queueMu.Unlock, false, 0
}

// ReadyForUpgradeHandoff reports whether the queue can be reconstructed from
// the durable Timeline and the live broker. Callers must first block admission.
func (s *Session) ReadyForUpgradeHandoff() bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if len(s.queuePending) > 0 || len(s.parkedNotices) > 0 || s.PendingPermissionCount() > 0 {
		return false
	}
	active := s.AgentTimeline().ActiveTurn()
	if active == nil {
		return !s.queueBusy
	}
	s.turnMu.Lock()
	bound := s.canonicalTurn != nil && s.canonicalTurn.Ref() == active.Ref() &&
		s.canonicalDispatched && s.canonicalAuth == (TurnAuthorization{})
	s.turnMu.Unlock()
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	replay, ok := proc.(interface{ SupportsTurnRebind() bool })
	return bound && ok && replay.SupportsTurnRebind()
}

func (s *Session) AutoSleepEligible(now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	s.runtimeMu.RLock()
	state := s.State
	idleTimeoutSeconds := s.IdleTimeoutSeconds
	agentSessionID := s.AgentSessionID
	s.runtimeMu.RUnlock()
	if state != StateRunning {
		return false
	}
	if idleTimeoutSeconds < MinIdleTimeoutSeconds {
		return false
	}
	if agentSessionID == "" {
		return false
	}
	busy, pending := s.QueueState()
	if busy || pending > 0 {
		return false
	}
	if s.PendingPermissionCount() > 0 {
		return false
	}
	s.activityMu.Lock()
	last := s.LastActivityAt
	s.activityMu.Unlock()
	if last.IsZero() {
		return false
	}
	return now.Sub(last) >= time.Duration(idleTimeoutSeconds)*time.Second
}

func ValidateIdleTimeoutSeconds(seconds int) error {
	if seconds == 0 {
		return nil
	}
	if seconds < MinIdleTimeoutSeconds {
		return fmt.Errorf("idle_timeout_seconds must be 0 or at least %d seconds", MinIdleTimeoutSeconds)
	}
	return nil
}

// Interrupt requests cancellation of the current agent turn while keeping the
// session alive for future input.
func (s *Session) Interrupt() error {
	if s.GoalSupported() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		current, err := s.Goal(ctx, GoalRequest{Action: "get"})
		if err != nil {
			return err
		}
		if current != nil && !current.IsTerminal() {
			_, err = s.Goal(ctx, GoalRequest{Action: "pause"})
			return err
		}
	}
	return s.InterruptForInput()
}

// InterruptForInput preserves an explicitly authorized goal across ordinary
// input preemption. Explicit user interrupts go through Interrupt instead.
func (s *Session) InterruptForInput() error {
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	if proc == nil {
		return nil
	}
	if err := proc.Interrupt(); err != nil {
		return err
	}
	s.revokeActiveTurnAuthorization()
	return nil
}

func (s *Session) ReconfigureExecution(model, reasoningEffort string) error {
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	if proc == nil {
		return ErrProcessUnavailable
	}
	reconfigurer, ok := proc.(ExecutionReconfigurer)
	if !ok {
		return errors.New("session process does not support execution reconfiguration")
	}
	return reconfigurer.ReconfigureExecution(model, reasoningEffort)
}

func (s *Session) CancelUserInput(permissionID string) error {
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	if proc == nil {
		return ErrProcessUnavailable
	}
	s.activityMu.Lock()
	_, pending := s.pendingPermissions[permissionID]
	s.activityMu.Unlock()
	if !pending {
		return errors.New("user-input request is no longer pending")
	}
	canceller, ok := proc.(UserInputCanceller)
	if !ok {
		return errors.New("provider does not support cancelling user input")
	}
	return canceller.CancelUserInput(permissionID)
}

func (s *Session) RespondPermission(permissionID, optionID string) error {
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	if proc == nil {
		return nil
	}
	if err := proc.RespondPermission(permissionID, optionID); err != nil {
		return err
	}
	s.activityMu.Lock()
	if s.pendingPermissions != nil {
		delete(s.pendingPermissions, permissionID)
		if len(s.pendingPermissions) == 0 {
			s.pendingPermissions = nil
		}
	}
	s.LastActivityAt = time.Now()
	s.activityMu.Unlock()
	return nil
}

// RespondPermissionMulti responds to a multiSelect user-input permission with
// multiple selected option labels. Mirrors RespondPermission's bookkeeping.
func (s *Session) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	if proc == nil {
		return nil
	}
	if err := proc.RespondPermissionMulti(permissionID, optionIDs); err != nil {
		return err
	}
	s.activityMu.Lock()
	if s.pendingPermissions != nil {
		delete(s.pendingPermissions, permissionID)
		if len(s.pendingPermissions) == 0 {
			s.pendingPermissions = nil
		}
	}
	s.LastActivityAt = time.Now()
	s.activityMu.Unlock()
	return nil
}

func (s *Session) Submit(ctx context.Context, source, text string) error {
	_, err := s.SubmitInput(ctx, source, TextUserInput(text))
	return err
}

func (s *Session) SubmitInput(ctx context.Context, source string, input UserInput) (QueueItem, error) {
	return s.SubmitInputWithAuthorization(ctx, source, input, TurnAuthorization{})
}

func (s *Session) SubmitInputWithAuthorization(ctx context.Context, source string, input UserInput, authorization TurnAuthorization) (QueueItem, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !s.HasProcess() {
		return QueueItem{}, ErrProcessUnavailable
	}
	item := s.enqueueUserInputWithAuthorization(source, input, authorization)

	select {
	case err := <-item.done:
		return item.QueueItem, err
	case <-ctx.Done():
		if s.cancelPendingItem(item.ID, ctx.Err()) {
			return item.QueueItem, ctx.Err()
		}
		return item.QueueItem, ctx.Err()
	}
}

func (s *Session) SubmitAsync(source, text string) error {
	_, err := s.SubmitInputAsync(source, TextUserInput(text))
	return err
}

func (s *Session) SubmitInputAsync(source string, input UserInput) (QueueItem, error) {
	return s.SubmitInputAsyncWithAuthorization(source, input, TurnAuthorization{})
}

func (s *Session) SubmitInputAsyncWithAuthorization(source string, input UserInput, authorization TurnAuthorization) (QueueItem, error) {
	return s.SubmitInputAsyncPinned(source, input, authorization, "")
}

// SubmitInputAsyncPinned rechecks the native conversation at dispatch, so an
// earlier queued clear cannot redirect a follow-up answer to a new context.
func (s *Session) SubmitInputAsyncPinned(source string, input UserInput, authorization TurnAuthorization, expectedAgentSessionID string) (QueueItem, error) {
	if !s.HasProcess() {
		return QueueItem{}, ErrProcessUnavailable
	}
	return s.enqueueInputWithControlAndParts(source, input.DisplayText(), "", input, authorization, expectedAgentSessionID).QueueItem, nil
}

// SubmitControlAsync queues a provider-native session control without
// converting it into model-visible prompt text. The display text is retained
// in replay and queue inspection for the user who initiated the action.
func (s *Session) SubmitControlAsync(source, displayText, control string) error {
	if !s.HasProcess() {
		return ErrProcessUnavailable
	}
	control = strings.TrimSpace(control)
	if control == "" {
		return errors.New("session control is required")
	}
	s.enqueueInputWithControl(source, displayText, control)
	return nil
}

// SubmitLifecycleAsync queues a runtime-owned lifecycle operation at the next
// turn boundary. Lifecycle items are ordered before ordinary pending input so
// later messages cannot run against stale session metadata or workspace.
func (s *Session) SubmitLifecycleAsync(source, displayText, control string, action func() error) (QueueItem, error) {
	if !s.HasProcess() {
		return QueueItem{}, ErrProcessUnavailable
	}
	control = strings.TrimSpace(control)
	if control == "" {
		return QueueItem{}, errors.New("session lifecycle control is required")
	}
	if action == nil {
		return QueueItem{}, errors.New("session lifecycle action is required")
	}

	s.queueMu.Lock()
	s.queueNextID++
	item := &queuedInput{
		QueueItem: QueueItem{
			ID:        strconv.FormatInt(s.queueNextID, 10),
			TurnID:    uuid.NewString(),
			Source:    source,
			Text:      displayText,
			Control:   control,
			CreatedAt: time.Now(),
		},
		lifecycle: action,
		done:      make(chan error, 1),
	}
	if !s.queueBusy {
		s.queueBusy = true
		s.queueGoal = false
		s.queueIdle = make(chan struct{})
		s.queueMu.Unlock()
		go s.runQueuedInput(item)
		return item.QueueItem, nil
	}
	// Keep lifecycle operations stable relative to one another, while placing
	// them ahead of normal queued prompts accepted during the active turn.
	pos := 0
	for pos < len(s.queuePending) && (s.queuePending[pos].preempt || s.queuePending[pos].lifecycle != nil) {
		pos++
	}
	s.queuePending = append(s.queuePending, nil)
	copy(s.queuePending[pos+1:], s.queuePending[pos:])
	s.queuePending[pos] = item
	s.queueMu.Unlock()
	return item.QueueItem, nil
}

// SubmitAsyncTracked enqueues one input and returns the stable amux turn id
// assigned to that queue item. Consumers can use it to reject events from
// earlier turns when a persistent session is reused.
func (s *Session) SubmitAsyncTracked(source, text string) (QueueItem, error) {
	return s.SubmitInputAsync(source, TextUserInput(text))
}

// SubmitPreempt enqueues an input ahead of all non-preempt pending inputs and
// interrupts the current turn (if any) so the agent picks it up immediately.
// Multiple preempt inputs keep their arrival order. When the session is idle it
// behaves like SubmitAsync without interrupting.
func (s *Session) SubmitPreempt(source, text string) error {
	_, err := s.enqueuePreemptInput(source, TextUserInput(text), true)
	return err
}

// SubmitPreemptAfterInterrupt preserves preempt queue priority when the caller
// has already interrupted the old turn. It must not issue another interrupt,
// which could otherwise race with and cancel the newly started turn.
func (s *Session) SubmitPreemptAfterInterrupt(source, text string) error {
	_, err := s.enqueuePreemptInput(source, TextUserInput(text), false)
	return err
}

// SubmitPreemptWait waits until the preempt input has been written to the
// provider, while still returning before the provider turn completes.
func (s *Session) SubmitPreemptWait(ctx context.Context, source, text string) error {
	_, err := s.SubmitPreemptInputWait(ctx, source, TextUserInput(text), true)
	return err
}

// SubmitPreemptAfterInterruptWait is SubmitPreemptWait for callers that have
// already interrupted the previous turn.
func (s *Session) SubmitPreemptAfterInterruptWait(ctx context.Context, source, text string) error {
	_, err := s.SubmitPreemptInputWait(ctx, source, TextUserInput(text), false)
	return err
}

func (s *Session) SubmitPreemptInputWait(ctx context.Context, source string, input UserInput, interrupt bool) (QueueItem, error) {
	return s.SubmitPreemptInputWaitWithAuthorization(ctx, source, input, interrupt, TurnAuthorization{})
}

func (s *Session) SubmitPreemptInputWaitWithAuthorization(ctx context.Context, source string, input UserInput, interrupt bool, authorization TurnAuthorization) (QueueItem, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	item, err := s.enqueuePreemptInputWithAuthorization(source, input, interrupt, authorization)
	if err != nil {
		return QueueItem{}, err
	}
	select {
	case err := <-item.done:
		return item.QueueItem, err
	case <-ctx.Done():
		if s.cancelPendingItem(item.ID, ctx.Err()) {
			return item.QueueItem, ctx.Err()
		}
		return item.QueueItem, ctx.Err()
	}
}

func (s *Session) enqueuePreemptInput(source string, input UserInput, interrupt bool) (*queuedInput, error) {
	return s.enqueuePreemptInputWithAuthorization(source, input, interrupt, TurnAuthorization{})
}

func (s *Session) enqueuePreemptInputWithAuthorization(source string, input UserInput, interrupt bool, authorization TurnAuthorization) (*queuedInput, error) {
	if !s.HasProcess() {
		return nil, ErrProcessUnavailable
	}
	s.queueMu.Lock()
	s.queueNextID++
	item := &queuedInput{
		QueueItem: QueueItem{
			ID:        strconv.FormatInt(s.queueNextID, 10),
			TurnID:    uuid.NewString(),
			Source:    source,
			Text:      input.DisplayText(),
			CreatedAt: time.Now(),
		},
		input:   input,
		preempt: true,
		auth:    authorization,
		done:    make(chan error, 1),
	}
	if !s.queueBusy {
		s.queueBusy = true
		s.queueGoal = false
		s.queueIdle = make(chan struct{})
		s.queueMu.Unlock()
		go s.runQueuedInput(item)
		return item, nil
	}
	// Insert after existing preempt items, before the first normal item.
	pos := 0
	for pos < len(s.queuePending) && s.queuePending[pos].preempt {
		pos++
	}
	s.queuePending = append(s.queuePending, nil)
	copy(s.queuePending[pos+1:], s.queuePending[pos:])
	s.queuePending[pos] = item
	s.queueMu.Unlock()
	if interrupt {
		if err := s.InterruptForInput(); err != nil {
			s.cancelPendingItem(item.ID, err)
			return nil, err
		}
	}
	return item, nil
}

func (s *Session) QueueSnapshot() []QueueItem {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	out := make([]QueueItem, 0, len(s.queuePending))
	for _, item := range s.queuePending {
		out = append(out, item.QueueItem)
	}
	return out
}

func (s *Session) ClearQueue() int {
	s.queueSteerMu.Lock()
	defer s.queueSteerMu.Unlock()
	s.queueMu.Lock()
	pending := s.queuePending
	s.queuePending = nil
	s.queueMu.Unlock()

	for _, item := range pending {
		item.finish(ErrQueueItemCancelled)
	}
	return len(pending)
}

func (s *Session) CancelQueueItem(id string) bool {
	s.queueSteerMu.Lock()
	defer s.queueSteerMu.Unlock()
	return s.cancelPendingItem(id, ErrQueueItemCancelled)
}

func (s *Session) consumeHandoffContext() (string, error) {
	s.runtimeMu.Lock()
	value := s.HandoffContext
	if value == "" {
		s.runtimeMu.Unlock()
		return "", nil
	}
	s.HandoffContext = ""
	persist := s.persistHandoff
	s.runtimeMu.Unlock()
	if persist != nil {
		if err := persist(""); err != nil {
			s.runtimeMu.Lock()
			s.HandoffContext = value
			s.runtimeMu.Unlock()
			return "", err
		}
	}
	return value, nil
}

func (s *Session) restoreHandoffContext(value string) {
	if value == "" {
		return
	}
	s.runtimeMu.Lock()
	s.HandoffContext = value
	persist := s.persistHandoff
	s.runtimeMu.Unlock()
	if persist != nil {
		_ = persist(value)
	}
}

// Kill terminates the agent process.
func (s *Session) Kill() error {
	s.procLifecycleMu.Lock()
	defer s.procLifecycleMu.Unlock()

	s.runtimeMu.Lock()
	proc := s.proc
	if proc == nil {
		s.runtimeMu.Unlock()
		return nil
	}
	generation := s.procGeneration
	s.intentionalCloseGeneration = generation
	s.runtimeMu.Unlock()
	if err := proc.Close(); err != nil {
		// Close failure does not prove that the provider stopped. Preserve the
		// active turn so callers can retry or escalate instead of advancing the
		// queue while the old process may still be executing.
		s.runtimeMu.Lock()
		if s.procGeneration == generation && s.intentionalCloseGeneration == generation {
			s.intentionalCloseGeneration = 0
		}
		s.runtimeMu.Unlock()
		return err
	}
	s.runtimeMu.RLock()
	stillCurrent := s.procGeneration == generation
	s.runtimeMu.RUnlock()
	if !stillCurrent {
		return nil
	}
	if err := s.FinishRecoveredTurn("session_closed", "session process closed intentionally"); err != nil {
		return err
	}
	return nil
}

// SetProc assigns the running ACP or structured broker process.
func (s *Session) SetProc(p Proc) {
	s.procLifecycleMu.Lock()
	defer s.procLifecycleMu.Unlock()
	s.runtimeMu.Lock()
	s.proc = p
	s.procGeneration++
	s.intentionalCloseGeneration = 0
	s.PID = p.PID()
	s.InputModalities = nil
	s.InputCapabilitySource = ""
	if caps, ok := p.(InputCapabilityProvider); ok {
		s.InputModalities, s.InputCapabilitySource = caps.InputCapabilities()
		s.InputModalities = append([]string(nil), s.InputModalities...)
	}
	s.runtimeMu.Unlock()
	p.SetEventSink(s.recordProducerEmission)
	if native, ok := p.(NativeGoalTurnProvider); ok {
		generation := s.ProcessGeneration()
		native.SetGoalTurnAdmitter(func(request GoalTurnRequest) (GoalTurnBinding, error) {
			if s.ProcessGeneration() != generation {
				return GoalTurnBinding{}, ErrProcessUnavailable
			}
			return s.admitGoalTurn(request)
		})
	}
	if ready, ok := p.(GoalTurnReadyProvider); ok && s.AgentTimeline().ActiveTurn() == nil {
		ready.EnableGoalTurns()
	}
	go s.releaseParkedNotices()
}

// ProcessGeneration identifies the currently installed process owner. Process
// waiters capture it so an old process cannot terminate a replacement turn.
func (s *Session) ProcessGeneration() uint64 {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.procGeneration
}

// FailActiveTurnForProcess records an unexpected exit only when the waiter
// still belongs to the installed process and that process was not deliberately
// closed by Kill.
func (s *Session) FailActiveTurnForProcess(generation uint64, stopReason string) bool {
	s.procLifecycleMu.Lock()
	defer s.procLifecycleMu.Unlock()
	s.runtimeMu.RLock()
	current := generation != 0 && s.procGeneration == generation
	intentional := s.intentionalCloseGeneration == generation
	s.runtimeMu.RUnlock()
	if !current || intentional {
		return false
	}
	return s.FailActiveTurn(stopReason)
}

func (s *Session) HasProcess() bool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.proc != nil
}

func (s *Session) enqueueUserInputWithAuthorization(source string, input UserInput, authorization TurnAuthorization) *queuedInput {
	return s.enqueueInputWithControlAndParts(source, input.DisplayText(), "", input, authorization, "")
}

func (s *Session) enqueueInputWithControl(source, text, control string) *queuedInput {
	return s.enqueueInputWithControlAndParts(source, text, control, UserInput{}, TurnAuthorization{}, "")
}

func (s *Session) enqueueInputWithControlAndParts(source, text, control string, input UserInput, authorization TurnAuthorization, expectedAgentSessionID string) *queuedInput {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()

	s.queueNextID++
	item := &queuedInput{
		QueueItem: QueueItem{
			ID:        strconv.FormatInt(s.queueNextID, 10),
			TurnID:    uuid.NewString(),
			Source:    source,
			Text:      text,
			Control:   control,
			CreatedAt: time.Now(),
		},
		input:                  input,
		auth:                   authorization,
		expectedAgentSessionID: expectedAgentSessionID,
		done:                   make(chan error, 1),
	}
	if s.queueBusy {
		item.Waiting = true
		s.queuePending = append(s.queuePending, item)
		return item
	}
	s.queueBusy = true
	s.queueGoal = false
	s.queueIdle = make(chan struct{})
	go s.runQueuedInput(item)
	return item
}

func (s *Session) runQueuedInput(item *queuedInput) {
	var done <-chan struct{}
	var err error
	if item.lifecycle != nil {
		err = item.lifecycle()
	} else if item.Control != "" {
		done, err = s.submitControlNow(item.Source, item.Text, item.Control, item.TurnID)
	} else {
		input := item.input
		if len(input.Parts) == 0 {
			input = TextUserInput(item.Text)
		}
		if item.Source == SubtaskNoticeSource {
			s.watchTurnTerminal(item.TurnID)
		}
		done, err = s.submitInputNowWithAuthorization(item.Source, input, item.TurnID, item.auth, item.expectedAgentSessionID)
	}
	item.finish(err)
	if done != nil {
		<-done
	}
	var terminal *agentstream.Terminal
	if item.Source == SubtaskNoticeSource {
		terminal = s.takeTurnTerminal(item.TurnID)
	}
	requeue := shouldRequeueSubtaskNotice(item, err, terminal)
	hasProcess := s.HasProcess()
	s.advanceInputQueueWith(func() {
		if requeue {
			s.requeueSubtaskNoticeLocked(item, hasProcess && (terminal == nil || terminal.Code != "session_closed"))
		}
	})
	if item.Control == "" && item.lifecycle == nil {
		go s.resumeGoalAfterInput()
	}
}

func (s *Session) advanceInputQueue() {
	s.advanceInputQueueWith(nil)
}

// advanceInputQueueWith runs beforeLocked (if any) under queueMu, then
// starts the next pending input or marks the queue idle.
func (s *Session) advanceInputQueueWith(beforeLocked func()) {
	s.queueSteerMu.Lock()
	defer s.queueSteerMu.Unlock()
	s.queueMu.Lock()
	if beforeLocked != nil {
		beforeLocked()
	}
	if len(s.queuePending) == 0 {
		s.queueBusy = false
		s.queueGoal = false
		if s.queueIdle != nil {
			close(s.queueIdle)
			s.queueIdle = nil
		}
		s.queueMu.Unlock()
		return
	}
	next := s.queuePending[0]
	s.queuePending = s.queuePending[1:]
	s.queueGoal = false
	s.queueMu.Unlock()

	go s.runQueuedInput(next)
}

func (s *Session) submitControlNow(source, displayText, control, turnID string) (<-chan struct{}, error) {
	dispatcher, ok := s.correlatedDispatcher()
	if !ok {
		return nil, ErrProcessUnavailable
	}
	return s.submitCorrelatedTurn(dispatcher, source, displayText, control, UserInput{}, turnID, TurnAuthorization{}, "")
}

func (s *Session) submitInputNowWithAuthorization(source string, input UserInput, turnID string, authorization TurnAuthorization, expectedAgentSessionID string) (<-chan struct{}, error) {
	dispatcher, ok := s.correlatedDispatcher()
	if !ok {
		return nil, ErrProcessUnavailable
	}
	return s.submitCorrelatedTurn(dispatcher, source, input.DisplayText(), "", input, turnID, authorization, expectedAgentSessionID)
}

func (s *Session) cancelPendingItem(id string, err error) bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	for i, item := range s.queuePending {
		if item.ID != id {
			continue
		}
		s.queuePending = append(s.queuePending[:i], s.queuePending[i+1:]...)
		item.finish(err)
		return true
	}
	return false
}

func (i *queuedInput) finish(err error) {
	select {
	case i.done <- err:
	default:
	}
}
