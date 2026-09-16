// Package coordination defines the platform-neutral facade DTOs and the
// minimal route needed to reach the backend that owns each lifecycle.
package coordination

import "time"

// BackendKind selects the lifecycle owner behind a resolved collaborator.
// These values are durable because Route persists them across amuxd restarts.
const (
	BackendKindAmuxTask  = "amux_task"
	BackendKindFeishuBot = "feishu_bot"
)

// Direction describes which side of amux initiated a Delegation.
type Direction string

const (
	DirectionOutbound Direction = "outbound"
	DirectionInbound  Direction = "inbound"
)

// CollaboratorKind identifies an Adapter family without exposing its backend
// identity to the Coordinator.
type CollaboratorKind string

const (
	CollaboratorInternalAgent CollaboratorKind = "internal_agent"
	CollaboratorExternalBot   CollaboratorKind = "external_bot"
)

// CollaboratorAvailability is a discovery-time observation, not a promise
// that scheduler admission or a remote peer response will succeed.
type CollaboratorAvailability string

const (
	AvailabilityConfigured CollaboratorAvailability = "configured"
	AvailabilityBestEffort CollaboratorAvailability = "best_effort"
)

// CollaboratorCapability gates Coordinator operations that are not supported
// uniformly by every backend.
type CollaboratorCapability string

const (
	CapabilityProgress           CollaboratorCapability = "progress"
	CapabilityStructuredResult   CollaboratorCapability = "structured_result"
	CapabilityPermissionResponse CollaboratorCapability = "permission_response"
	CapabilityCancelable         CollaboratorCapability = "cancelable"
)

// CollaboratorDescriptor is the public discovery view. ID is opaque to the
// caller and must be passed back unchanged when creating a Delegation.
type CollaboratorDescriptor struct {
	ID           string                   `json:"id"`
	Name         string                   `json:"name"`
	Description  string                   `json:"description,omitempty"`
	Kind         CollaboratorKind         `json:"kind"`
	Availability CollaboratorAvailability `json:"availability"`
	Capabilities []CollaboratorCapability `json:"capabilities,omitempty"`
	Skills       []string                 `json:"skills,omitempty"`
}

// ResolvedCollaborator is the private routing decision returned by a Catalog.
// TargetRef is adapter-owned and never crosses the public API.
type ResolvedCollaborator struct {
	BackendKind string
	TargetRef   string
	TrustLevel  TrustLevel
}

// Status is the normalized Delegation lifecycle state.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusTimedOut  Status = "timed_out"
)

var terminalStatuses = [...]Status{
	StatusSucceeded,
	StatusFailed,
	StatusCancelled,
	StatusTimedOut,
}

// IsTerminal reports whether no further state transition is allowed.
func (s Status) IsTerminal() bool {
	for _, terminal := range terminalStatuses {
		if s == terminal {
			return true
		}
	}
	return false
}

// EventKind identifies one durable Delegation event.
type EventKind string

const (
	EventStatusChanged       EventKind = "status_changed"
	EventOutput              EventKind = "output"
	EventPermissionRequested EventKind = "permission_requested"
	EventResult              EventKind = "result"
	EventError               EventKind = "error"
)

// TrustLevel tells the Coordinator how strongly it may rely on a result.
type TrustLevel string

const (
	TrustInternal          TrustLevel = "internal"
	TrustExternalUntrusted TrustLevel = "external_untrusted"
)

// ResultEnvelope is the backend-neutral terminal result contract.
type ResultEnvelope struct {
	Summary     string     `json:"summary,omitempty"`
	Content     string     `json:"content,omitempty"`
	ContentType string     `json:"content_type,omitempty"`
	DetailsRef  string     `json:"details_ref,omitempty"`
	TrustLevel  TrustLevel `json:"trust_level"`
	SourceRefs  []string   `json:"source_refs,omitempty"`
}

// FailureCode is a stable, transport-safe failure classification.
type FailureCode string

const (
	FailureDeadlineExceeded FailureCode = "deadline_exceeded"
	FailureBackendFailed    FailureCode = "backend_failed"
)

// Failure is a stable, transport-safe terminal error.
type Failure struct {
	Code    FailureCode `json:"code"`
	Message string      `json:"message,omitempty"`
}

// PermissionRequest is a backend-neutral request that the Coordinator may
// answer through a later intervention.
type PermissionRequest struct {
	ID      string   `json:"id"`
	Summary string   `json:"summary"`
	Tool    string   `json:"tool,omitempty"`
	Command string   `json:"command,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Write   bool     `json:"write,omitempty"`
	Network bool     `json:"network,omitempty"`
}

// PermissionDecision is the Coordinator's structured response.
type PermissionDecision string

const (
	PermissionAllow PermissionDecision = "allow"
	PermissionDeny  PermissionDecision = "deny"
)

// PermissionResponse answers one pending backend permission request.
type PermissionResponse struct {
	RequestID string             `json:"request_id"`
	Decision  PermissionDecision `json:"decision"`
	OptionID  string             `json:"option_id,omitempty"`
	Reason    string             `json:"reason,omitempty"`
}

// Route is the only durable Coordination record. Mutable lifecycle fields are
// intentionally absent: BackendRef identifies the backend that owns them.
type Route struct {
	ID                 string     `json:"id"`
	BackendKind        string     `json:"backend_kind"`
	BackendRef         string     `json:"backend_ref,omitempty"`
	CollaboratorID     string     `json:"collaborator_id"`
	TargetRef          string     `json:"target_ref"`
	InitiatorRef       string     `json:"initiator_ref"`
	ScopeRef           string     `json:"scope_ref"`
	ParentDelegationID string     `json:"parent_delegation_id,omitempty"`
	Input              string     `json:"input"`
	Deadline           time.Time  `json:"deadline"`
	TrustLevel         TrustLevel `json:"trust_level"`
	IdempotencyKey     string     `json:"idempotency_key,omitempty"`
	HopCount           int        `json:"hop_count"`
	HopBudget          int        `json:"hop_budget"`
	CreatedAt          time.Time  `json:"created_at"`
}

// Delegation is one unit of work passed from an initiator to a target.
type Delegation struct {
	ID                 string          `json:"id"`
	Direction          Direction       `json:"direction"`
	InitiatorRef       string          `json:"initiator_ref"`
	TargetRef          string          `json:"target_ref"`
	ReplyTo            string          `json:"reply_to,omitempty"`
	ScopeRef           string          `json:"scope_ref"`
	ParentDelegationID string          `json:"parent_delegation_id,omitempty"`
	HopCount           int             `json:"hop_count"`
	HopBudget          int             `json:"hop_budget"`
	Input              string          `json:"input"`
	Status             Status          `json:"status"`
	Deadline           time.Time       `json:"deadline"`
	BackendRef         string          `json:"backend_ref,omitempty"`
	ResultTrustLevel   TrustLevel      `json:"result_trust_level"`
	Result             *ResultEnvelope `json:"result,omitempty"`
	Failure            *Failure        `json:"failure,omitempty"`
	IdempotencyKey     string          `json:"idempotency_key,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	FinishedAt         time.Time       `json:"finished_at,omitempty"`
}

// Event is a backend observation projected into the public Delegation stream.
type Event struct {
	DelegationID string             `json:"delegation_id"`
	Seq          uint64             `json:"seq"`
	Kind         EventKind          `json:"kind"`
	Status       Status             `json:"status,omitempty"`
	Content      string             `json:"content,omitempty"`
	Result       *ResultEnvelope    `json:"result,omitempty"`
	Failure      *Failure           `json:"failure,omitempty"`
	Permission   *PermissionRequest `json:"permission,omitempty"`
	At           time.Time          `json:"at"`
}

// Snapshot is a current Delegation plus events after the requested sequence.
type Snapshot struct {
	Delegation *Delegation
	Events     []Event
	// NextSeq is the resume cursor: pass it back as fromSeq to receive events
	// whose sequence is strictly greater, matching the existing task stream API.
	NextSeq uint64
}
