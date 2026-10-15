package agentui

import (
	"encoding/json"
	"time"
)

type EventKind string

const (
	KindAssistantTextDelta  EventKind = "assistant_text_delta"
	KindAssistantText       EventKind = "assistant_text"
	KindInputSubmitted      EventKind = "input_submitted"
	KindThinkingDelta       EventKind = "thinking_delta"
	KindPlan                EventKind = "plan"
	KindToolStarted         EventKind = "tool_started"
	KindToolUpdated         EventKind = "tool_updated"
	KindToolCompleted       EventKind = "tool_completed"
	KindToolFailed          EventKind = "tool_failed"
	KindPermissionRequested EventKind = "permission_requested"
	KindPermissionResolved  EventKind = "permission_resolved"
	KindTurnStarted         EventKind = "turn_started"
	KindTurnDone            EventKind = "turn_done"
	KindInterrupted         EventKind = "interrupted"
	KindError               EventKind = "error"
	KindUsage               EventKind = "usage"
	KindResult              EventKind = "result"
	KindHeartbeat           EventKind = "heartbeat"
	KindUnknown             EventKind = "unknown"
)

type Source string

const SourceGoal Source = "goal"

const (
	SourceAgent     Source = "agent"
	SourceCLI       Source = "cli"
	SourceDesktop   Source = "desktop"
	SourceFeishu    Source = "feishu"
	SourceScheduler Source = "scheduler"
	// SourceSubtask marks input the control plane submits to a parent session
	// when one of its background tasks finishes.
	SourceSubtask Source = "subtask"
	SourceSystem  Source = "system"
	SourceTool    Source = "tool"
	SourceResult  Source = "result"
	SourceUnknown Source = "unknown"
)

type Event struct {
	ID       string
	Seq      int64
	Kind     EventKind
	Source   Source
	Provider string
	Text     string

	Input      *InputEvent
	Tool       *ToolEvent
	Permission *PermissionEvent
	Turn       *TurnEvent
	Plan       *PlanEvent
	Error      *ErrorEvent
	UsageJSON  json.RawMessage
	ResultJSON json.RawMessage
	RawJSON    json.RawMessage
}

type InputEvent struct {
	Source Source
	Text   string
}

type ToolEvent struct {
	ID            string          `json:"id,omitempty"`
	Name          string          `json:"name,omitempty"`
	Title         string          `json:"title,omitempty"`
	Status        string          `json:"status,omitempty"`
	InputJSON     json.RawMessage `json:"input,omitempty"`
	OutputPreview string          `json:"output_preview,omitempty"`
	Error         string          `json:"error,omitempty"`
	ContentJSON   json.RawMessage `json:"content,omitempty"`
	LocationsJSON json.RawMessage `json:"locations,omitempty"`
	RawOutputJSON json.RawMessage `json:"raw_output,omitempty"`
	Legacy        bool            `json:"legacy,omitempty"`
	StartedAt     time.Time       `json:"started_at,omitempty"`
}

type PlanEvent struct {
	Entries []PlanEntry `json:"entries"`
}

type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status"`
}

type PermissionEvent struct {
	RequestID  string
	Provider   string
	Tool       *PermissionTool
	Options    []PermissionOption
	RawPayload json.RawMessage
}

type PermissionTool struct {
	ID          string
	Name        string
	Title       string
	RawInput    json.RawMessage
	Question    string
	MultiSelect bool
	AllowOther  bool
	Secret      bool
	Blocking    *bool
}

type PermissionOption struct {
	ID          string
	Label       string
	Kind        string
	Description string
}

type TurnEvent struct {
	ID         string
	Seq        int64
	State      string
	StopReason string
}

type ErrorEvent struct {
	Message string
	// Code is the provider's structured error code when known (for example
	// "rate_limit"); empty otherwise.
	Code string
}
