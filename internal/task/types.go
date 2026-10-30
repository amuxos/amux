// Package task implements the Phase 1 local control-plane shim for the
// distributed agent runtime described in docs/distributed-task-model.md.
//
// A task is one execution of a subagent on behalf of a main agent. The runner
// composes the existing session primitives (Run/Submit/Attach/Interrupt/Kill via
// a SessionController) and adds the cross-cutting concepts defined in
// docs/distributed-task-model.md: isolation, credential profiles, permission
// policy, completion judgement (TerminalOutcome + structured TaskResult) and
// owner enforcement.
//
// This package is intentionally decoupled from gRPC/mux/adapter wiring: it
// depends only on internal/session for the Event type and on a SessionController
// interface that the amuxlet binary implements. That keeps the control-plane
// logic unit-testable with a fake controller.
package task

import (
	"time"

	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// Mode is the task lifecycle kind. Phase 1 implements ephemeral only.
type Mode string

const (
	ModeEphemeral  Mode = "ephemeral"
	ModePersistent Mode = "persistent"
)

// Status is the TaskRun state. The full set matches the design's state machine;
// Phase 1 drives the ephemeral subset (no reserving/scheduled/reconciling, which
// belong to the Phase 2 distributed scheduler).
type Status string

const (
	StatusPending           Status = "pending"
	StatusScheduled         Status = "scheduled"
	StatusReserving         Status = "reserving"
	StatusPreparing         Status = "preparing"
	StatusRunning           Status = "running"
	StatusWaitingPermission Status = "waiting_permission"
	StatusCollecting        Status = "collecting"
	StatusReconciling       Status = "reconciling"
	StatusCancelling        Status = "cancelling"
	StatusSucceeded         Status = "succeeded"
	StatusFailed            Status = "failed"
	StatusCancelled         Status = "cancelled"
	StatusTimedOut          Status = "timed_out"
)

// IsTerminal reports whether the status is a final state.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusTimedOut:
		return true
	default:
		return false
	}
}

// IsolationMode selects the sandbox backend. Phase 1 implements process and
// private-home (both trusted-mode weak isolation); container/vm are Phase 4.
type IsolationMode string

const (
	IsolationProcess     IsolationMode = "process"
	IsolationPrivateHome IsolationMode = "private-home"
	IsolationContainer   IsolationMode = "container"
	IsolationVM          IsolationMode = "vm"
)

// IsStrong reports whether the isolation provides an OS-level security boundary.
// Only container/vm qualify; process/private-home are best-effort trusted-mode.
func (i IsolationMode) IsStrong() bool {
	return i == IsolationContainer || i == IsolationVM
}

// PermissionPolicy is how the runner answers child permission requests.
type PermissionPolicy string

const (
	PolicyAllow          PermissionPolicy = "policy-allow"
	PolicyEscalateToMain PermissionPolicy = "escalate-to-main"
	PolicyDenyAll        PermissionPolicy = "deny-all"
)

// WorkspacePolicy selects how the child workspace is provisioned.
type WorkspacePolicy string

const (
	WorkspaceTempDir     WorkspacePolicy = "temp-dir"
	WorkspaceGitWorktree WorkspacePolicy = "git-worktree"
	WorkspacePersistent  WorkspacePolicy = "persistent"
)

// ResultGranularity controls how much of the result is returned inline.
type ResultGranularity string

const (
	GranularitySummary    ResultGranularity = "summary"
	GranularityFull       ResultGranularity = "full"
	GranularityStructured ResultGranularity = "structured-summary"
)

// RepoSpec is a cross-target-resolvable repo identity (design "分布式 Repo Identity").
type RepoSpec struct {
	Kind     string `json:"kind,omitempty"`      // "git"
	Remote   string `json:"remote,omitempty"`    // repo remote or stable id
	CacheKey string `json:"cache_key,omitempty"` // logical name of amuxlet repo cache
	Commit   string `json:"commit,omitempty"`    // concrete commit SHA (never a moving ref)
	PatchRef string `json:"patch_ref,omitempty"` // optional uncommitted patch artifact
	Subdir   string `json:"subdir,omitempty"`    // optional working subdir
}

// WorkspaceSpec describes the requested workspace for a task.
type WorkspaceSpec struct {
	Policy WorkspacePolicy `json:"policy,omitempty"`
	Repo   *RepoSpec       `json:"repo,omitempty"`
	// LocalDir is only honoured with WorkspacePolicy existing-cwd style use; for
	// Phase 1 a non-empty value is used directly as the child cwd.
	LocalDir string `json:"local_dir,omitempty"`
}

// Spec is the validated input to submit an agent task (the MCP amux.task_submit
// payload, minus transport concerns).
type Spec struct {
	// AgentExecutable is the installation-selected program for a new child Session.
	AgentExecutable string `json:"agent_executable,omitempty"`
	Agent           string `json:"agent"`
	Runtime         string `json:"runtime,omitempty"`
	Model           string `json:"model,omitempty"`
	// ReasoningEffort is the requested reasoning level. A subtask submitted
	// from a session inherits the parent session effort here when the caller
	// omitted it and the parent effort is known.
	ReasoningEffort   string            `json:"reasoning_effort,omitempty"`
	Task              string            `json:"task"`
	Mode              Mode              `json:"mode,omitempty"`
	Isolation         IsolationMode     `json:"isolation,omitempty"`
	CredentialProfile string            `json:"credential_profile,omitempty"`
	PermissionPolicy  PermissionPolicy  `json:"permission_policy,omitempty"`
	Workspace         WorkspaceSpec     `json:"workspace,omitempty"`
	TimeoutSeconds    int               `json:"timeout_seconds,omitempty"`
	ResultGranularity ResultGranularity `json:"result_granularity,omitempty"`
	ExtraArgs         []string          `json:"extra_args,omitempty"`
	// TrustedModeAck is a legacy no-op (v0.38). Long-lived secrets inject under
	// weak isolation by default; the field is kept for backward-compatible wire
	// compatibility and no longer gates injection.
	TrustedModeAck bool `json:"trusted_mode_ack,omitempty"`
	// Blocking marks a task_run (synchronous sugar) call.
	// escalate-to-main is incompatible with blocking and is rejected at validation time.
	Blocking bool `json:"blocking,omitempty"`
	// ParentSessionID/ParentTaskID record provenance of the caller.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	ParentTaskID    string `json:"parent_task_id,omitempty"`
	// ParentTurnID is filled only by the trusted control plane after resolving
	// ParentSessionID to the runtime's exact active turn.
	ParentTurnID string `json:"parent_turn_id,omitempty"`
	// AgentInstanceID identifies the persistent agent instance this task belongs
	// to (Phase 3). Empty for ephemeral tasks. For persistent tasks it is the
	// cross-task durable identity: amuxd uses it for sticky scheduling (route a
	// follow-up task back to the target hosting the instance's durable
	// environment) and to record the instance's target/provider-session handle.
	AgentInstanceID string `json:"agent_instance_id,omitempty"`
	// NotifyParent opts a non-blocking task out of the completion message the
	// control plane submits to ParentSessionID when the task finishes
	// (experimental, steer_foreground). Nil means true.
	NotifyParent *bool `json:"notify_parent,omitempty"`
	// RunAs is an unresolved caller intent. ExecutionIdentity is set only by the
	// trusted control plane before the spec reaches an amuxlet.
	RunAs             string                       `json:"run_as,omitempty"`
	ExecutionIdentity *principal.ExecutionIdentity `json:"execution_identity,omitempty"`
	// ResolvedProfile is filled by the control plane after resolving Agent (a
	// profile ref) to a base agent type; raw clients never set it. It travels on
	// SpecJson to the amuxlet, which trusts the control plane's value.
	ResolvedProfile *pb.AgentProfileSpec `json:"resolved_profile,omitempty"`
}

// SessionRef is the structured handle returned to the main agent. It is never a
// concatenated string, to avoid brittle routing keys.
type SessionRef struct {
	TargetID       string `json:"target_id"`
	ChildSessionId string `json:"child_session_id"`
}

// TaskRun is the amuxd/local-shim source of truth for one subtask execution.
type TaskRun struct {
	ID              string `json:"id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	ParentTaskID    string `json:"parent_task_id,omitempty"`
	ParentTurnID    string `json:"parent_turn_id,omitempty"`
	// TurnID is the exact accepted child-session turn driven by this run.
	TurnID       string `json:"turn_id,omitempty"`
	Mode         Mode   `json:"mode"`
	AgentType    string `json:"agent_type"`
	AgentProfile string `json:"agent_profile,omitempty"`
	// AgentInstanceID is the persistent agent instance id (Phase 3). Empty for
	// ephemeral tasks. Carried on the run so the runner can fill ChildSpec
	// and so the control plane can correlate the task with its instance.
	AgentInstanceID    string                      `json:"agent_instance_id,omitempty"`
	ExecutionIdentity  principal.ExecutionIdentity `json:"execution_identity"`
	LaunchID           string                      `json:"launch_id,omitempty"`
	IsolationBackend   string                      `json:"isolation_backend,omitempty"`
	TargetID           string                      `json:"target_id,omitempty"`
	ChildSessionId     string                      `json:"child_session_id,omitempty"`
	ChildSessionName   string                      `json:"child_session_name,omitempty"`
	IsolationMode      IsolationMode               `json:"isolation_mode"`
	CredentialProfile  string                      `json:"credential_profile,omitempty"`
	CredentialProfiles []string                    `json:"credential_profiles,omitempty"`
	PermissionPolicy   PermissionPolicy            `json:"permission_policy"`
	WorkspacePolicy    WorkspacePolicy             `json:"workspace_policy"`
	ResultGranularity  ResultGranularity           `json:"result_granularity"`
	SecurityLevel      string                      `json:"security_level,omitempty"`
	Status             Status                      `json:"status"`
	Task               string                      `json:"task,omitempty"`
	Result             *TaskResult                 `json:"result,omitempty"`
	Error              string                      `json:"error,omitempty"`
	TimeoutSeconds     int                         `json:"timeout_seconds,omitempty"`
	RequestedAt        time.Time                   `json:"requested_at"`
	StartedAt          time.Time                   `json:"started_at,omitempty"`
	FinishedAt         time.Time                   `json:"finished_at,omitempty"`
}

// Ref returns the structured session handle for this task.
func (t *TaskRun) Ref() SessionRef {
	return SessionRef{TargetID: t.TargetID, ChildSessionId: t.ChildSessionId}
}

// ResultStatus is the coarse TaskResult outcome.
type ResultStatus string

const (
	ResultOK      ResultStatus = "ok"
	ResultFailed  ResultStatus = "failed"
	ResultPartial ResultStatus = "partial"
)

// TaskResult is the minimal structured result contract. Large objects are
// referenced (details_ref / diff_ref / artifacts), not inlined.
type TaskResult struct {
	Status  ResultStatus `json:"status"`
	Summary string       `json:"summary,omitempty"`
	// FullTranscript carries the complete redacted agent transcript inline, only
	// under result_granularity=full. It is json:"-" so it never persists into
	// the store result_json column or task_events result payloads (a full
	// transcript is large and lives in the task event log); it is returned only
	// in the in-memory runner result that the MCP layer renders to the caller.
	FullTranscript string        `json:"-"`
	DetailsRef     string        `json:"details_ref,omitempty"`
	ChangedFiles   []ChangedFile `json:"changed_files,omitempty"`
	Artifacts      []ArtifactRef `json:"artifacts,omitempty"`
	Diagnostics    []Diagnostic  `json:"diagnostics,omitempty"`
	UsageJson      []byte        `json:"usage_json,omitempty"`
}

// MaxFullTranscriptBytes caps the inline full transcript under
// result_granularity=full. Larger transcripts are truncated with a pointer to
// amux task_stream / amux task logs for the complete content.
const MaxFullTranscriptBytes = 64 * 1024

// ChangeType classifies a changed file.
type ChangeType string

const (
	ChangeAdded    ChangeType = "added"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
	ChangeRenamed  ChangeType = "renamed"
)

// ChangedFile is one entry derived from the workspace (not from agent text).
type ChangedFile struct {
	Path       string     `json:"path"`
	ChangeType ChangeType `json:"change_type"`
	DiffRef    string     `json:"diff_ref,omitempty"`
}

// ArtifactRef references a large output stored outside the MCP response.
type ArtifactRef struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"` // transcript|patch|report|log|file
	URI       string `json:"uri,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Redacted  bool   `json:"redacted,omitempty"`
}

// Severity classifies a diagnostic.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Diagnostic is a structured note (e.g. a denied permission record).
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Path     string   `json:"path,omitempty"`
	Line     int      `json:"line,omitempty"`
}
