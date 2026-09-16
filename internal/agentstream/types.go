package agentstream

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrClosed           = errors.New("agent event timeline closed")
	ErrTurnActive       = errors.New("another turn is active")
	ErrDuplicateTurnID  = errors.New("turn id already used")
	ErrNoActiveTurn     = errors.New("no active turn")
	ErrTurnFinished     = errors.New("turn already finished")
	ErrTerminalConflict = errors.New("conflicting terminal outcome")
	ErrInvalidPayload   = errors.New("invalid agent event payload")
	ErrInvalidCursor    = errors.New("invalid agent event cursor")
	ErrCorruptJournal   = errors.New("corrupt agent event journal")
	ErrJournalFault     = errors.New("agent event journal fault")
	ErrLagged           = errors.New("agent event subscriber lagged")
)

// Cursor identifies an event within one durable Session timeline epoch.
// Sequence numbers are not comparable across epochs.
type Cursor struct {
	Epoch string `json:"epoch"`
	Seq   uint64 `json:"seq"`
}

// TurnRef is the amux-owned identity of one accepted Agent turn.
type TurnRef struct {
	ID  string `json:"id"`
	Seq uint64 `json:"seq"`
}

type ActorClass string

const (
	ActorUser   ActorClass = "user"
	ActorAgent  ActorClass = "agent"
	ActorSystem ActorClass = "system"
	ActorTool   ActorClass = "tool"
)

// Origin is provenance only. Native provider identifiers never participate in
// canonical event or turn identity.
type Origin struct {
	Actor        ActorClass `json:"actor"`
	Channel      string     `json:"channel"`
	Provider     string     `json:"provider"`
	NativeType   string     `json:"native_type"`
	NativeID     string     `json:"native_id"`
	NativeSeq    int64      `json:"native_seq"`
	TransportSeq uint64     `json:"transport_seq"`
	LaunchID     string     `json:"launch_id"`
}

type Kind string

const (
	KindTurnStarted            Kind = "turn_started"
	KindAssistantDelta         Kind = "assistant_delta"
	KindAssistantMessage       Kind = "assistant_message"
	KindThinkingDelta          Kind = "thinking_delta"
	KindPlanReplaced           Kind = "plan_replaced"
	KindToolChanged            Kind = "tool_changed"
	KindPermissionRequested    Kind = "permission_requested"
	KindPermissionResolved     Kind = "permission_resolved"
	KindUsageObserved          Kind = "usage_observed"
	KindResultObserved         Kind = "result_observed"
	KindDiagnostic             Kind = "diagnostic"
	KindTurnFinished           Kind = "turn_finished"
	KindSessionMetadataChanged Kind = "session_metadata_changed"
	KindCommandsReplaced       Kind = "commands_replaced"
	KindSessionDiagnostic      Kind = "session_diagnostic"
	KindExtensionObserved      Kind = "extension_observed"
)

// Body is a closed union. Concrete payloads can be constructed by callers,
// but only this package can add new implementations.
type Body interface {
	Kind() Kind
	eventBody()
	cloneBody() Body
}

// TurnPayload can be emitted only through a capability-bound Turn.
type TurnPayload interface {
	Body
	turnPayload()
}

// SessionPayload can be published without an active Turn.
type SessionPayload interface {
	Body
	sessionPayload()
}

type TurnStarted struct {
	InputSummary string `json:"input_summary"`
}

func (TurnStarted) Kind() Kind        { return KindTurnStarted }
func (TurnStarted) eventBody()        {}
func (v TurnStarted) cloneBody() Body { return v }

type AssistantDelta struct {
	Text string `json:"text"`
}

func (AssistantDelta) Kind() Kind        { return KindAssistantDelta }
func (AssistantDelta) eventBody()        {}
func (AssistantDelta) turnPayload()      {}
func (v AssistantDelta) cloneBody() Body { return v }

type Outcome string

const (
	OutcomeSuccess     Outcome = "success"
	OutcomeError       Outcome = "error"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeInterrupted Outcome = "interrupted"
)

type Terminal struct {
	Outcome Outcome `json:"outcome"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
}

type TurnFinished struct {
	Terminal Terminal `json:"terminal"`
}

func (TurnFinished) Kind() Kind        { return KindTurnFinished }
func (TurnFinished) eventBody()        {}
func (v TurnFinished) cloneBody() Body { return v }

// SessionMetadataChanged is a complete sticky snapshot. Publishing a new value
// replaces the previous snapshot, including empty fields.
type SessionMetadataChanged struct {
	AgentSessionID string `json:"agent_session_id"`
	EffectiveModel string `json:"effective_model"`
}

func (SessionMetadataChanged) Kind() Kind        { return KindSessionMetadataChanged }
func (SessionMetadataChanged) eventBody()        {}
func (SessionMetadataChanged) sessionPayload()   {}
func (v SessionMetadataChanged) cloneBody() Body { return v }

type Command struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputHint   string `json:"input_hint"`
	Kind        string `json:"kind"`
}

type CommandsReplaced struct {
	Commands []Command `json:"commands"`
}

func (CommandsReplaced) Kind() Kind      { return KindCommandsReplaced }
func (CommandsReplaced) eventBody()      {}
func (CommandsReplaced) sessionPayload() {}
func (v CommandsReplaced) cloneBody() Body {
	v.Commands = append([]Command(nil), v.Commands...)
	return v
}

type Event struct {
	SessionID string
	Cursor    Cursor
	Turn      *TurnRef
	At        time.Time
	Origin    Origin
	Body      Body
}

type Completion struct {
	Turn     TurnRef
	Terminal Terminal
	Event    Event
	Err      error
}

// Journal is the durable append-only boundary owned by a Timeline. Append must
// be atomic: returning nil means the exact event is durable before it can be
// observed by any subscriber.
type Journal interface {
	AppendAgentEvent(context.Context, Event) error
	LoadAgentEvents(context.Context, string) ([]Event, error)
}

type Config struct {
	SessionID   string
	Epoch       string
	MailboxSize int
	Now         func() time.Time
	NewTurnID   func() string
	Journal     Journal
}

type StartRequest struct {
	TurnID       string
	InputSummary string
	Origin       Origin
}

// DispatchError means the turn was observably started, dispatch failed, and
// the Timeline attempted to close it with submit_failed.
type DispatchError struct {
	Turn      TurnRef
	Err       error
	RecordErr error
}

func (e *DispatchError) Error() string {
	if e.RecordErr != nil {
		return fmt.Sprintf("dispatch turn %s: %v; record submit_failed terminal: %v", e.Turn.ID, e.Err, e.RecordErr)
	}
	return fmt.Sprintf("dispatch turn %s: %v", e.Turn.ID, e.Err)
}

func (e *DispatchError) Unwrap() []error {
	if e.RecordErr == nil {
		return []error{e.Err}
	}
	return []error{e.Err, e.RecordErr}
}

type Head struct {
	Cursor   Cursor
	Metadata *SessionMetadataChanged
	Commands *CommandsReplaced
}

type ReadMode string

const (
	// ReadHistory returns events after the cursor up to the captured head, then
	// Recv returns io.EOF.
	ReadHistory ReadMode = "history"
	// ReadFollow first returns history after the cursor, then continues with
	// live events from the same atomic read boundary.
	ReadFollow ReadMode = "follow"
	// ReadTail ignores prior history and returns only events accepted after the
	// captured head.
	ReadTail ReadMode = "tail"
)

type ReadRequest struct {
	Mode  ReadMode
	After Cursor
}
