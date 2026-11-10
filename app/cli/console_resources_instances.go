package cli

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// This is intentionally not task.Spec: browser callers cannot supply an
// executable, identity, parent provenance, resolved profile or blocking mode.
// The ordinary Hub task admission resolves content, placement and authority.
type consoleInstanceSubmission struct {
	Agent             string                 `json:"agent"`
	Runtime           string                 `json:"runtime,omitempty"`
	Model             string                 `json:"model,omitempty"`
	ReasoningEffort   string                 `json:"reasoning_effort,omitempty"`
	Task              string                 `json:"task"`
	AgentInstanceID   string                 `json:"agent_instance_id,omitempty"`
	RunAs             string                 `json:"run_as,omitempty"`
	Isolation         string                 `json:"isolation,omitempty"`
	CredentialProfile string                 `json:"credential_profile,omitempty"`
	PermissionPolicy  string                 `json:"permission_policy,omitempty"`
	Workspace         clientop.WorkspaceSpec `json:"workspace,omitempty"`
	TimeoutSeconds    int                    `json:"timeout_seconds,omitempty"`
	ResultGranularity string                 `json:"result_granularity,omitempty"`
	ExtraArgs         []string               `json:"extra_args,omitempty"`
}

func (b *consoleBackend) consoleSubmitInstance(ctx context.Context, action string, data json.RawMessage) (any, error) {
	var req consoleInstanceSubmission
	if err := consoleDecode(data, &req); err != nil {
		return nil, err
	}
	if err := consoleRequired(req.Task, "task"); err != nil {
		return nil, err
	}
	if action == "followup" {
		if err := consoleRequired(req.AgentInstanceID, "agent_instance_id"); err != nil {
			return nil, err
		}
	}
	if err := consoleRequired(req.Agent, "agent"); err != nil {
		return nil, err
	}
	if req.TimeoutSeconds < 0 {
		return nil, consoleInvalid("invalid_field", "timeout_seconds")
	}
	switch req.PermissionPolicy {
	case "", "default", "policy-allow", "deny-all", "escalate-to-main":
	default:
		return nil, consoleInvalid("invalid_field", "permission_policy")
	}
	switch req.Isolation {
	case "", "process", "private-home":
	default:
		return nil, consoleInvalid("invalid_field", "isolation")
	}
	switch req.ResultGranularity {
	case "", "summary", "full", "structured-summary":
	default:
		return nil, consoleInvalid("invalid_field", "result_granularity")
	}
	switch req.Workspace.Policy {
	case "", "temp-dir", "git-worktree":
	default:
		return nil, consoleInvalid("invalid_field", "workspace.policy")
	}
	// Reuse workspace validation without imposing the scheduled-task restriction
	// on escalated permissions: an asynchronous instance can escalate normally.
	workspaceCheck := clientop.ScheduledTaskSpec{Agent: req.Agent, Task: req.Task, Workspace: req.Workspace}
	if err := workspaceCheck.Validate(); err != nil {
		return nil, err
	}
	if b.tasks == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	instanceID := strings.TrimSpace(req.AgentInstanceID)
	if action == "spawn" {
		// Spawn always allocates a fresh identity. Existing instances must be
		// addressed explicitly through followup, not accidentally reused.
		if instanceID != "" {
			return nil, consoleInvalid("invalid_field", "agent_instance_id")
		}
		instanceID = newAgentInstanceID()
	}
	policy := req.PermissionPolicy
	if policy == "default" {
		policy = ""
	}
	spec := task.Spec{Agent: req.Agent, Runtime: req.Runtime, Model: req.Model, ReasoningEffort: req.ReasoningEffort, Task: req.Task, Mode: task.ModePersistent, AgentInstanceID: instanceID, RunAs: req.RunAs, Isolation: task.IsolationMode(req.Isolation), CredentialProfile: req.CredentialProfile, PermissionPolicy: task.PermissionPolicy(policy), Workspace: workspaceCheck.ToTaskSpec().Workspace, TimeoutSeconds: req.TimeoutSeconds, ResultGranularity: task.ResultGranularity(req.ResultGranularity), ExtraArgs: req.ExtraArgs}
	// No browser-supplied ParentSessionID/ExecutionIdentity: the Hub derives the
	// operator's authority from the already-authenticated task service client.
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	resp, err := b.tasks.SubmitSubtask(ctx, &pb.SubmitSubtaskRequest{SpecJson: raw})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	run, err := decodeTask(resp.GetTaskJson())
	if err != nil || run.ID == "" {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	return map[string]any{"ok": true, "agent_instance_id": instanceID, "task_id": run.ID, "status": run.Status}, nil
}
