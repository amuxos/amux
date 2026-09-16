package acptui

import "context"

// OutputEventKind identifies the type of an output event from the agent.
type OutputEventKind int

const (
	// OutputKindData indicates normal agent output data.
	OutputKindData OutputEventKind = iota
	// OutputKindTurnDone indicates the agent finished its turn.
	OutputKindTurnDone
	// OutputKindPermissionRequest asks the UI user to choose a permission option.
	OutputKindPermissionRequest
	// OutputKindToolEvent carries a structured tool execution event
	// (started/completed/failed) from a structured provider (for example, Codex or Claude)
	// or ACP. Rendered into the line-mode tool timeline without relying on
	// text-prefix matching. See agent-adapter product model v0.1.32.
	OutputKindToolEvent
	// OutputKindThinkingDelta carries a structured thinking/reasoning delta from
	// a structured provider, rendered as a "current step" hint.
	OutputKindThinkingDelta
	// OutputKindUsage carries a structured token usage update from a
	// structured provider. It refreshes the status-bar context indicator and,
	// at turn end, the usage timeline row.
	OutputKindUsage
)

type PermissionOption struct {
	OptionId string
	Label    string
	Kind     string
}

type PermissionRequest struct {
	RequestID string
	Title     string
	Options   []PermissionOption
}

// ToolEvent is the terminal UI's provider-neutral projection of structured
// tool activity. It is defined locally because the public terminal library
// cannot import amux internal packages.
type ToolEvent struct {
	Name          string
	Title         string
	Status        string
	Input         []byte
	OutputPreview string
}

// UsageStats is the terminal UI's provider-neutral projection of token usage.
// ContextWindow is 0 when the
// provider did not report a model context window (e.g. claude result usage).
// TTFTMs and TokensPerSec are optional speed metrics measured by the provider
// proc; 0 means "not reported" and is omitted from rendering.
type UsageStats struct {
	InputTokens              int64
	OutputTokens             int64
	TotalTokens              int64
	CachedInputTokens        int64
	CacheCreationInputTokens int64
	ReasoningOutputTokens    int64
	ContextWindow            int64
	TTFTMs                   int64
	TokensPerSec             int64
}

// OutputEvent represents a single event received from the agent.
type OutputEvent struct {
	Kind              OutputEventKind
	Data              []byte
	Source            string
	Input             bool
	StopReason        string
	PermissionRequest *PermissionRequest
	// ToolEvent is set when Kind == OutputKindToolEvent.
	ToolEvent *ToolEvent
	// ThinkingDelta is set when Kind == OutputKindThinkingDelta.
	ThinkingDelta string
	// Usage is set when Kind == OutputKindUsage.
	Usage *UsageStats
}

// AgentStream is the abstraction for receiving output from an Acp agent.
// Implementations adapt transport-specific streams (gRPC, stdio, etc.)
// to this simple interface.
type AgentStream interface {
	// Recv blocks until the next output event is available.
	// Returns io.EOF when the stream ends.
	Recv() (OutputEvent, error)
}

// AgentSender is the abstraction for sending user input to an Acp agent.
type AgentSender interface {
	// Send delivers user input to the agent. It may block until the agent
	// processes the input (synchronous prompt) or return immediately
	// (asynchronous).
	Send(ctx context.Context, data []byte) error
}

// Interrupter is optionally implemented by AgentSender values that can request
// cancellation of the current agent turn without detaching the UI.
type Interrupter interface {
	Interrupt(ctx context.Context) error
}

// WorkingDirectoryOptions controls a conversation-preserving workspace
// switch. Wait permits waiting for the active turn boundary; Interrupt first
// requests cancellation of the active turn and implies Wait.
type WorkingDirectoryOptions struct {
	Wait      bool
	Interrupt bool
}

// WorkingDirectoryManager is optionally implemented by AgentSender values
// that can inspect or change the attached session's working directory.
type WorkingDirectoryManager interface {
	WorkingDirectory(ctx context.Context) (string, error)
	ChangeWorkingDirectory(ctx context.Context, path string, opts WorkingDirectoryOptions) (string, error)
}

// QueueItem describes one pending, not-yet-started user input.
type QueueItem struct {
	ID     string
	Source string
	Text   string
}

// QueueManager is optionally implemented by AgentSender values that expose
// pending input queue operations for the attached session.
type QueueManager interface {
	QueueList(ctx context.Context) ([]QueueItem, error)
	QueueClear(ctx context.Context) (int, error)
	QueueCancel(ctx context.Context, id string) error
}

// PermissionResponder is optionally implemented by AgentSender values that can
// answer an agent permission request.
type PermissionResponder interface {
	RespondToPermission(ctx context.Context, requestID, optionID string) error
}
