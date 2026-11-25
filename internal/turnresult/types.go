package turnresult

import "time"

// SchemaVersion is the wire schema version for structured single-turn results.
const SchemaVersion = 1

// Query status values returned to callers.
const (
	StatusReady       = "ready"
	StatusNotReady    = "not_ready"
	StatusUnavailable = "unavailable"
	StatusNotFound    = "not_found"
)

// Common unavailable reasons.
const (
	ReasonDisabled               = "disabled"
	ReasonResultNotSettled       = "result_not_settled"
	ReasonUnsupportedMode        = "unsupported_mode"
	ReasonResultTooLarge         = "result_too_large"
	ReasonExecutionNotCompleted  = "execution_not_completed"
	ReasonObservationUnavailable = "observation_unavailable"
)

// Execution status values.
const (
	ExecutionRunning     = "running"
	ExecutionCompleted   = "completed"
	ExecutionFailed      = "failed"
	ExecutionCancelled   = "cancelled"
	ExecutionInterrupted = "interrupted"
	ExecutionMissed      = "missed"
	ExecutionPartial     = "partial"
	ExecutionTimedOut    = "timed_out"
	ExecutionUnknown     = "unknown"
)

// Output status values.
const (
	OutputAvailable   = "available"
	OutputEmpty       = "empty"
	OutputUnavailable = "unavailable"
)

// Delivery status values.
const (
	DeliveryDelivered  = "delivered"
	DeliveryPartial    = "partial"
	DeliveryFailed     = "failed"
	DeliverySuppressed = "suppressed"
	DeliveryUnknown    = "unknown"
)

// Delivery message kinds.
const (
	MessageKindPrimary      = "primary"
	MessageKindContinuation = "continuation"
	MessageKindAttachment   = "attachment"
)

// Source kinds.
const (
	SourceFeishu   = "feishu"
	SourceSchedule = "schedule"
	SourceCLI      = "cli"
	SourceDesktop  = "desktop"
	SourceMCP      = "mcp"
	SourceSubtask  = "subtask"
	SourceGoal     = "goal"
)

// Ref identifies the Session and Turn that produced a result.
type Ref struct {
	TargetID    string `json:"target_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	LaunchID    string `json:"launch_id,omitempty"`
	TurnID      string `json:"turn_id,omitempty"`
}

// Source captures how the turn or scheduled execution was triggered.
type Source struct {
	Kind          string   `json:"kind,omitempty"`
	BridgeID      string   `json:"bridge_id,omitempty"`
	ChatID        string   `json:"chat_id,omitempty"`
	RootMessageID string   `json:"root_message_id,omitempty"`
	MessageIDs    []string `json:"message_ids,omitempty"`
	ScheduleID    int64    `json:"schedule_id,omitempty"`
	ExecutionID   int64    `json:"execution_id,omitempty"`
	ScheduleType  string   `json:"schedule_type,omitempty"`
	RunMode       string   `json:"run_mode,omitempty"`
	ScheduledAt   int64    `json:"scheduled_at,omitempty"`
	TaskID        string   `json:"task_id,omitempty"`
}

// Execution records the canonical turn/task execution outcome.
type Execution struct {
	Status     string `json:"status,omitempty"`
	Code       string `json:"code,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

// Output records the final adopted answer independently of execution and delivery.
type Output struct {
	Status  string `json:"status,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Format  string `json:"format,omitempty"`
	Content string `json:"content,omitempty"`
}

// DeliveryMessage identifies one confirmed outbound message on a channel.
type DeliveryMessage struct {
	Kind      string `json:"kind"`
	MessageID string `json:"message_id"`
	Name      string `json:"name,omitempty"`
}

// Delivery records whether and how the turn output was delivered to an external channel.
type Delivery struct {
	Channel       string            `json:"channel"`
	Status        string            `json:"status"`
	Reason        string            `json:"reason,omitempty"`
	BridgeID      string            `json:"bridge_id,omitempty"`
	ChatID        string            `json:"chat_id,omitempty"`
	RootMessageID string            `json:"root_message_id,omitempty"`
	Messages      []DeliveryMessage `json:"messages,omitempty"`
}

// Result is the structured read-only payload returned when Status == StatusReady.
type Result struct {
	Version    int        `json:"version"`
	Ref        Ref        `json:"ref"`
	Source     Source     `json:"source"`
	Execution  Execution  `json:"execution"`
	Output     Output     `json:"output"`
	Deliveries []Delivery `json:"deliveries"`
}

// Record is the persisted internal state for one tracked turn or schedule execution.
type Record struct {
	RecordID              string
	Status                string
	Reason                string
	Ref                   Ref
	Source                Source
	Execution             Execution
	Output                Output
	Deliveries            []Delivery
	DeliveryExpected      bool
	ExecutionSettled      bool
	DeliverySettled       bool
	ArtifactsPending      bool
	OwnerPrincipalID      string
	ExecutionIdentityJSON string
	PayloadBytes          int64
	CreatedAt             int64
	UpdatedAt             int64
	SettledAt             int64
	ExpiresAt             int64
}

// LookupResult is the read-only answer returned to Hub callers.
type LookupResult struct {
	Status string
	Reason string
	Record *Record
	Result *Result
}

// Policy controls bounded retention and size limits for the TurnResult store.
type Policy struct {
	TTL            time.Duration // 0 disables time-based expiry for settled results
	PendingTimeout time.Duration
	MaxEntries     int
	MaxTotalBytes  int64
	MaxContentSize int
}

// DefaultPolicy returns the conservative default bounded retention policy.
func DefaultPolicy() Policy {
	return Policy{
		TTL:            7 * 24 * time.Hour,
		PendingTimeout: 5 * time.Minute,
		MaxEntries:     512,
		MaxTotalBytes:  32 << 20,
		MaxContentSize: 1 << 20,
	}
}

// Normalize fills zero fields with defaults.
func (p Policy) Normalize() Policy {
	def := DefaultPolicy()
	if p.TTL < 0 {
		p.TTL = def.TTL
	}
	if p.PendingTimeout <= 0 {
		p.PendingTimeout = def.PendingTimeout
	}
	if p.MaxEntries <= 0 {
		p.MaxEntries = def.MaxEntries
	}
	if p.MaxTotalBytes <= 0 {
		p.MaxTotalBytes = def.MaxTotalBytes
	}
	if p.MaxContentSize <= 0 {
		p.MaxContentSize = def.MaxContentSize
	}
	return p
}

// SubmissionInput registers a newly accepted turn or scheduled execution.
type SubmissionInput struct {
	Ref                   Ref
	Source                Source
	DeliveryExpected      bool
	OwnerPrincipalID      string
	ExecutionIdentityJSON string
	Now                   time.Time
}

// ExecutionInput records canonical execution completion and final output.
type ExecutionInput struct {
	Ref                   Ref
	Source                Source
	Execution             Execution
	Output                Output
	OwnerPrincipalID      string
	ExecutionIdentityJSON string
	Now                   time.Time
}

// DeliveryInput records confirmed external channel delivery facts.
type DeliveryInput struct {
	Ref              Ref
	Source           Source
	Delivery         Delivery
	ArtifactsPending bool
	Now              time.Time
}

// ArtifactDeliveryInput finalizes asynchronous attachment delivery for a Feishu turn.
type ArtifactDeliveryInput struct {
	BridgeID     string
	TurnID       string
	Attachments  []DeliveryMessage
	FailedCount  int
	TotalPlanned int
	Now          time.Time
}

// ToResult builds a clean external Result snapshot from a Record.
func (r Record) ToResult() *Result {
	deliveries := make([]Delivery, len(r.Deliveries))
	for i, d := range r.Deliveries {
		copied := d
		if len(d.Messages) > 0 {
			copied.Messages = append([]DeliveryMessage(nil), d.Messages...)
		}
		deliveries[i] = copied
	}
	src := r.Source
	if len(r.Source.MessageIDs) > 0 {
		src.MessageIDs = append([]string(nil), r.Source.MessageIDs...)
	}
	return &Result{
		Version:    SchemaVersion,
		Ref:        r.Ref,
		Source:     src,
		Execution:  r.Execution,
		Output:     r.Output,
		Deliveries: deliveries,
	}
}
