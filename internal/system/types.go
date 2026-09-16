package system

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const SnapshotSchema = "amux.system.snapshot.v1"
const PlanPreviewSchema = "amux.system.plan.v1"

type Lifecycle interface {
	Inspect(context.Context, Scope) (Snapshot, error)
	Plan(context.Context, ChangeRequest) (ChangePlan, error)
	Apply(context.Context, ChangePlan) (OperationID, error)
	GetOperation(context.Context, OperationID) (Operation, error)
	Cancel(context.Context, OperationID) (CancelResult, error)
}

type OperationID string

type ScopeKind string

const (
	ScopeCurrent    ScopeKind = "current"
	ScopeOperations ScopeKind = "operations"
)

type Scope struct {
	Kind   ScopeKind `json:"kind"`
	Target string    `json:"target,omitempty"`
}

func CurrentScope() Scope { return Scope{Kind: ScopeCurrent} }

type State string

const (
	StateStopped  State = "stopped"
	StateHealthy  State = "healthy"
	StateDegraded State = "degraded"
	StateFailed   State = "failed"
)

type Action string

const (
	ActionSetup   Action = "setup"
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
)

type SetupSpec struct {
	Profile    string `json:"profile"`
	Agent      string `json:"agent,omitempty"`
	HubAddress string `json:"hub_address,omitempty"`
	NoStart    bool   `json:"no_start,omitempty"`
}

type ChangeRequest struct {
	Action Action     `json:"action"`
	Scope  Scope      `json:"scope"`
	Force  bool       `json:"force,omitempty"`
	Setup  *SetupSpec `json:"setup,omitempty"`
}

type Component struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type Check struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Required bool   `json:"required,omitempty"`
	Message  string `json:"message,omitempty"`
}

type SuggestedAction struct {
	Command              string `json:"command"`
	Reason               string `json:"reason,omitempty"`
	Destructive          bool   `json:"destructive,omitempty"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

type SnapshotSummary struct {
	Installation *InstallationSummary `json:"installation,omitempty"`
	Workloads    *WorkloadSummary     `json:"workloads,omitempty"`
	Integrations *IntegrationSummary  `json:"integrations,omitempty"`
	Operations   *OperationSummary    `json:"operations,omitempty"`
}

type InstallationSummary struct {
	Home         string `json:"home,omitempty"`
	ConfigPath   string `json:"config_path,omitempty"`
	ConfigExists bool   `json:"config_exists"`
	Mode         string `json:"mode,omitempty"`
	HubAddress   string `json:"hub_address,omitempty"`
	RuntimeID    string `json:"runtime_id,omitempty"`
	RuntimeName  string `json:"runtime_name,omitempty"`
}

type WorkloadSummary struct {
	LocalSessions  SessionWorkloadSummary  `json:"local_sessions,omitempty"`
	TargetSessions SessionWorkloadSummary  `json:"target_sessions,omitempty"`
	Tasks          TaskWorkloadSummary     `json:"tasks,omitempty"`
	Schedules      ScheduleWorkloadSummary `json:"schedules,omitempty"`
}

type SessionWorkloadSummary struct {
	Total    int            `json:"total,omitempty"`
	Running  int            `json:"running,omitempty"`
	Archived int            `json:"archived,omitempty"`
	Other    int            `json:"other,omitempty"`
	Agents   map[string]int `json:"agents,omitempty"`
	Error    string         `json:"error,omitempty"`
}

type TaskWorkloadSummary struct {
	Total    int    `json:"total,omitempty"`
	Running  int    `json:"running,omitempty"`
	Terminal int    `json:"terminal,omitempty"`
	Other    int    `json:"other,omitempty"`
	Error    string `json:"error,omitempty"`
}

type ScheduleWorkloadSummary struct {
	Total    int    `json:"total,omitempty"`
	Enabled  int    `json:"enabled,omitempty"`
	Disabled int    `json:"disabled,omitempty"`
	Error    string `json:"error,omitempty"`
}

type IntegrationSummary struct {
	Feishu        FeishuIntegrationSummary `json:"feishu,omitempty"`
	MCP           MCPIntegrationSummary    `json:"mcp,omitempty"`
	LocalServices LocalServiceSummary      `json:"local_services,omitempty"`
}

type FeishuIntegrationSummary struct {
	Configured         bool   `json:"configured"`
	ProfilesConfigured int    `json:"profiles_configured,omitempty"`
	ProfilesTotal      int    `json:"profiles_total,omitempty"`
	BridgeDBHealthy    bool   `json:"bridge_db_healthy"`
	Error              string `json:"error,omitempty"`
}

type MCPIntegrationSummary struct {
	Enabled    bool   `json:"enabled"`
	AutoInject bool   `json:"auto_inject"`
	Backend    string `json:"backend,omitempty"`
	State      string `json:"state,omitempty"`
	Error      string `json:"error,omitempty"`
}

type LocalServiceSummary struct {
	Total        int    `json:"total,omitempty"`
	Servers      int    `json:"servers,omitempty"`
	SessionOwned int    `json:"session_owned,omitempty"`
	Error        string `json:"error,omitempty"`
}

type OperationSummary struct {
	Active     *OperationBrief `json:"active,omitempty"`
	LastFailed *OperationBrief `json:"last_failed,omitempty"`
}

type OperationBrief struct {
	ID     OperationID    `json:"id"`
	Action Action         `json:"action"`
	State  OperationState `json:"state"`
	Error  string         `json:"error,omitempty"`
}

type Impact struct {
	WillRestartHub       bool     `json:"will_restart_hub,omitempty"`
	WillRestartRuntime   bool     `json:"will_restart_runtime,omitempty"`
	MayInterruptSessions []string `json:"may_interrupt_sessions,omitempty"`
	RequiresForce        bool     `json:"requires_force,omitempty"`
	Notes                []string `json:"notes,omitempty"`
}

type Snapshot struct {
	Schema          string            `json:"schema"`
	Scope           Scope             `json:"scope"`
	State           State             `json:"state"`
	Version         string            `json:"version,omitempty"`
	Update          *UpdateSummary    `json:"update,omitempty"`
	Summary         *SnapshotSummary  `json:"summary,omitempty"`
	Components      []Component       `json:"components,omitempty"`
	Checks          []Check           `json:"checks,omitempty"`
	ActiveOperation *Operation        `json:"active_operation,omitempty"`
	Operations      []Operation       `json:"operations,omitempty"`
	NextActions     []SuggestedAction `json:"next_actions,omitempty"`
}

type UpdateSummary struct {
	Available       bool      `json:"available"`
	CurrentVersion  string    `json:"current_version,omitempty"`
	LatestVersion   string    `json:"latest_version"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	PromptDismissed bool      `json:"prompt_dismissed,omitempty"`
}

type ChangePlan struct {
	OperationID OperationID   `json:"operation_id"`
	Request     ChangeRequest `json:"request"`
	Before      Snapshot      `json:"before"`
	CreatedAt   time.Time     `json:"created_at"`
	integrity   [32]byte
}

type PlanPreview struct {
	Schema      string            `json:"schema"`
	Action      Action            `json:"action"`
	Scope       Scope             `json:"scope"`
	Force       bool              `json:"force,omitempty"`
	Impact      Impact            `json:"impact"`
	NextActions []SuggestedAction `json:"next_actions,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

type OperationState string

const (
	OperationPending    OperationState = "pending"
	OperationDraining   OperationState = "draining"
	OperationStaging    OperationState = "staging"
	OperationApplying   OperationState = "applying"
	OperationVerifying  OperationState = "verifying"
	OperationSucceeded  OperationState = "succeeded"
	OperationRolledBack OperationState = "rolled_back"
	OperationFailed     OperationState = "failed"
	OperationCancelled  OperationState = "cancelled"
)

type Operation struct {
	ID          OperationID       `json:"id"`
	Action      Action            `json:"action"`
	Scope       Scope             `json:"scope"`
	State       OperationState    `json:"state"`
	Error       string            `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	FinishedAt  *time.Time        `json:"finished_at,omitempty"`
	Plan        ChangePlan        `json:"plan"`
	NextActions []SuggestedAction `json:"next_actions,omitempty"`
}

type CancelResult struct {
	OperationID OperationID    `json:"operation_id"`
	Accepted    bool           `json:"accepted"`
	State       OperationState `json:"state"`
	Reason      string         `json:"reason,omitempty"`
}

type ConflictError struct {
	ExistingOperationID OperationID
}

func (e *ConflictError) Error() string {
	if e.ExistingOperationID == "" {
		return "another system operation is active"
	}
	return fmt.Sprintf("system operation %s is already active", e.ExistingOperationID)
}

func (e *ConflictError) ExitCode() int { return 3 }

type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string {
	if e == nil || e.Err == nil {
		return "invalid system request"
	}
	return e.Err.Error()
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ValidationError) ExitCode() int { return 2 }

type BlockedError struct {
	Err error
}

func (e *BlockedError) Error() string {
	if e == nil || e.Err == nil {
		return "system operation is blocked"
	}
	return e.Err.Error()
}

func (e *BlockedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *BlockedError) ExitCode() int { return 3 }

var ErrDuplicateOperation = errors.New("system operation already exists")

type DuplicateOperationError struct {
	OperationID OperationID
}

func (e *DuplicateOperationError) Error() string {
	if e.OperationID == "" {
		return ErrDuplicateOperation.Error()
	}
	return fmt.Sprintf("system operation %s already exists", e.OperationID)
}

func (e *DuplicateOperationError) Unwrap() error { return ErrDuplicateOperation }

var ErrInvalidOperationTransition = errors.New("invalid system operation transition")

type InvalidTransitionError struct {
	OperationID OperationID
	From        OperationState
	To          OperationState
}

func (e *InvalidTransitionError) Error() string {
	if e.OperationID == "" {
		return fmt.Sprintf("%s: %s -> %s", ErrInvalidOperationTransition, e.From, e.To)
	}
	return fmt.Sprintf("%s for %s: %s -> %s", ErrInvalidOperationTransition, e.OperationID, e.From, e.To)
}

func (e *InvalidTransitionError) Unwrap() error { return ErrInvalidOperationTransition }

var ErrInvalidChangePlan = errors.New("invalid system change plan")

var ErrOperationInProgress = errors.New("system operation is in progress")

type OperationInProgressError struct {
	OperationID OperationID
	State       OperationState
}

func (e *OperationInProgressError) Error() string {
	return fmt.Sprintf("%s: %s (%s)", ErrOperationInProgress, e.OperationID, e.State)
}

func (e *OperationInProgressError) Unwrap() error { return ErrOperationInProgress }

var ErrOperationNotSuccessful = errors.New("system operation did not succeed")

type OperationNotSuccessfulError struct {
	OperationID    OperationID
	State          OperationState
	OperationError string
}

func (e *OperationNotSuccessfulError) Error() string {
	if e.OperationError == "" {
		return fmt.Sprintf("%s: %s (%s)", ErrOperationNotSuccessful, e.OperationID, e.State)
	}
	return fmt.Sprintf("%s: %s (%s): %s", ErrOperationNotSuccessful, e.OperationID, e.State, e.OperationError)
}

func (e *OperationNotSuccessfulError) Unwrap() error { return ErrOperationNotSuccessful }

type JournalStore interface {
	Create(context.Context, Operation) error
	Get(context.Context, OperationID) (Operation, error)
	List(context.Context, int) ([]Operation, error)
	Transition(context.Context, OperationID, OperationState, string) error
	CancelPending(context.Context, OperationID) (CancelResult, error)
}

type Inspector func(context.Context, Scope) (Snapshot, error)

type Executor func(context.Context, ChangePlan) error

type Dependencies struct {
	Journal JournalStore
	Inspect Inspector
	Execute Executor
	IDGen   func() string
	Now     func() time.Time
}
