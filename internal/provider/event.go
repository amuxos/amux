package provider

import "encoding/json"

type EventType string

const (
	EventAssistantTextDelta EventType = "assistant_text_delta"
	EventAssistantText      EventType = "assistant_text"
	EventThinkingDelta      EventType = "thinking_delta"
	EventAvailableCommands  EventType = "available_commands"
	EventPlan               EventType = "plan"
	EventToolStarted        EventType = "tool_started"
	EventToolUpdated        EventType = "tool_updated"
	EventToolCompleted      EventType = "tool_completed"
	EventToolFailed         EventType = "tool_failed"
	EventPermissionRequest  EventType = "permission_requested"
	EventPermissionResolved EventType = "permission_resolved"
	EventUsage              EventType = "usage"
	EventTurnStarted        EventType = "turn_started"
	EventTurnDone           EventType = "turn_done"
	EventResult             EventType = "result"
	EventInterrupted        EventType = "interrupted"
	EventError              EventType = "error"
	EventUnknown            EventType = "unknown"
)

type Event struct {
	ID                string             `json:"id,omitempty"`
	Seq               int64              `json:"seq,omitempty"`
	TurnID            string             `json:"turn_id,omitempty"`
	TurnSeq           int64              `json:"turn_seq,omitempty"`
	Provider          string             `json:"provider,omitempty"`
	Type              EventType          `json:"type"`
	Text              string             `json:"text,omitempty"`
	AvailableCommands []AvailableCommand `json:"available_commands,omitempty"`
	State             *StateChange       `json:"state,omitempty"`
	Tool              *ToolEvent         `json:"tool,omitempty"`
	Permission        *Permission        `json:"permission,omitempty"`
	Plan              *Plan              `json:"plan,omitempty"`
	Usage             json.RawMessage    `json:"usage,omitempty"`
	Result            *TurnResult        `json:"result,omitempty"`
	Error             *ProviderError     `json:"error,omitempty"`
	Raw               json.RawMessage    `json:"raw,omitempty"`
}

// AvailableCommand is a provider-neutral command advertised by the current
// agent session. A command name never includes the leading slash.
type AvailableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"input_hint,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

// TurnResult is a provider's native "final result of this turn" payload, carried
// on an EventResult event emitted just before the terminal EventTurnDone. It lets
// the task control plane record a summary from the provider's own final message
// instead of an injected text envelope. Text is the final assistant message;
// Usage/CostUSD are filled when the provider reports them (e.g. the Claude SDK
// result message).
type TurnResult struct {
	Text    string          `json:"text,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	CostUSD float64         `json:"cost_usd,omitempty"`
}

type StateChange struct {
	State      string `json:"state"`
	StopReason string `json:"stop_reason,omitempty"`
}

type ToolEvent struct {
	ID            string          `json:"id,omitempty"`
	Name          string          `json:"name,omitempty"`
	Title         string          `json:"title,omitempty"`
	Status        string          `json:"status,omitempty"`
	Input         json.RawMessage `json:"input,omitempty"`
	OutputPreview string          `json:"output_preview,omitempty"`
	Error         string          `json:"error,omitempty"`
	Content       json.RawMessage `json:"content,omitempty"`
	Locations     json.RawMessage `json:"locations,omitempty"`
	RawOutput     json.RawMessage `json:"raw_output,omitempty"`
}

type Plan struct {
	Entries []PlanEntry `json:"entries"`
}

type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status"`
}

type ProviderError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}
