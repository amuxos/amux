package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/mcp"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func mcpScheduleCreateError(err error) string {
	var fe *clientop.FieldError
	if errors.As(err, &fe) {
		return mcpScheduleValidationMessage(fe)
	}
	return err.Error()
}

func mcpScheduleValidationMessage(err *clientop.FieldError) string {
	if err.Field == clientop.FieldPermissionPolicy {
		if err.RawValue == string(task.PolicyEscalateToMain) {
			return "permission_policy=escalate-to-main is not supported for scheduled agent tasks"
		}
		return fmt.Sprintf("invalid permission policy %q (expected default, policy-allow, or deny-all)", err.RawValue)
	}
	if err.Field == clientop.FieldWorkspaceLocalDir && err.Also == clientop.FieldRepoCacheKey {
		return "task.workspace.local_dir or task.workspace.repo.cache_key is required with workspace.policy=git-worktree"
	}
	return err.Render(mcpScheduleFieldNames)
}
func (h *mcpHandler) createSchedule(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a mcpCreateScheduleArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	a.Spec = strings.TrimSpace(a.Spec)
	if a.Spec == "" {
		return mcp.ErrorText("spec is required"), nil
	}
	scheduleType, err := normalizeMCPScheduleType(a.ScheduleType)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	sessionRef := ""
	if scheduleType == schedule.ScheduleTypeSessionPrompt {
		// Validate session-ref / AMUX_SESSION_ID before dialing the hub so missing
		// inputs fail as parameter errors instead of hub-unreachable timeouts.
		sessionRef, err = mcpSessionPromptRef(a)
		if err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		if strings.TrimSpace(a.Prompt) == "" {
			return mcp.ErrorText("prompt is required for schedule_type=session-prompt"), nil
		}
	}
	client, closeFn, err := newScheduleClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()

	var result clientop.ScheduleCreateResult
	switch scheduleType {
	case schedule.ScheduleTypeSessionPrompt:
		result, err = createMCPSessionPromptSchedule(ctx, client, a, sessionRef)
	default:
		result, err = clientop.ScheduleCreateTask(ctx, client, clientop.ScheduleCreateTaskRequest{Spec: a.Spec, Name: a.Name, CreatedBy: "mcp", Task: a.Task})
	}
	if err != nil {
		return mcp.ErrorText(mcpScheduleCreateError(err)), nil
	}
	return mcp.Text(toJSON(map[string]any{
		"schedule_id":     result.ID,
		"next_run_at":     result.NextRunAt,
		"next_run_at_iso": mcpScheduleTimeISO(result.NextRunAt),
		"schedule_type":   scheduleType,
	})), nil
}

// normalizeMCPScheduleType keeps the MCP default as subtask so existing callers
// that omit schedule_type continue to create temporary child Tasks.
func normalizeMCPScheduleType(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return schedule.ScheduleTypeSubtask, nil
	}
	normalized, err := schedule.NormalizeScheduleType(raw)
	if err != nil {
		return "", fmt.Errorf("invalid schedule_type %q (expected subtask or session-prompt)", raw)
	}
	return normalized, nil
}

// mcpSessionPromptRef picks the session-ref for schedule_type=session-prompt.
// An explicit `session` argument wins; otherwise AMUX_SESSION_ID is used so an
// Agent scheduling against its own Session can omit the field.
func mcpSessionPromptRef(a mcpCreateScheduleArgs) (string, error) {
	ref := strings.TrimSpace(a.Session)
	if ref == "" {
		ref = strings.TrimSpace(os.Getenv("AMUX_SESSION_ID"))
	}
	if ref == "" {
		return "", fmt.Errorf("session is required for schedule_type=session-prompt (pass session as a session-ref like CLI amux schedule create, or run inside a Session so AMUX_SESSION_ID is set)")
	}
	return ref, nil
}

// createMCPSessionPromptSchedule resolves session-ref with the same path as CLI
// `amux schedule create <session-ref>` (sessionResolver.ResolveSession), then
// reuses hub session-prompt delivery (Scheduler.deliverReuseSession /
// deliverNewSessionPerRun).
func createMCPSessionPromptSchedule(ctx context.Context, client pb.AmuxdClient, a mcpCreateScheduleArgs, sessionRef string) (clientop.ScheduleCreateResult, error) {
	r, err := newSessionResolver()
	if err != nil {
		return clientop.ScheduleCreateResult{}, err
	}
	defer r.Close()
	resolved, err := r.ResolveSession(ctx, sessionRef)
	if err != nil {
		return clientop.ScheduleCreateResult{}, err
	}
	return clientop.ScheduleCreateSessionPrompt(ctx, client, clientop.ScheduleCreateSessionPromptRequest{
		Spec:        a.Spec,
		Name:        a.Name,
		TargetID:    resolved.Target.ID,
		TargetName:  resolved.Target.Name,
		SessionName: resolved.Session.Name,
		Prompt:      a.Prompt,
		RunMode:     a.RunMode,
		CreatedBy:   "mcp",
	})
}

func (h *mcpHandler) listSchedules(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		Cursor string `json:"cursor"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			return mcp.ErrorText("invalid arguments: " + err.Error()), nil
		}
	}
	client, closeFn, err := newScheduleClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	items, err := clientop.ScheduleList(ctx, client)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	view, err := listSchedulesMCPView(items, input.Cursor)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(view)), nil
}
func (h *mcpHandler) getSchedule(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	id, includeRuns, err := scheduleID(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newScheduleClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	item, err := clientop.ScheduleGet(ctx, client, clientop.ScheduleIDRequest{ID: id, IncludeRuns: includeRuns})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[string]any{"schedule": scheduleItemMCPView(item)})), nil
}
func listSchedulesMCPView(items []*pb.ScheduleItem, cursor string) (map[string]any, error) {
	offset, err := parseMCPListCursor(cursor)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", err)
	}
	if offset > len(items) {
		return nil, fmt.Errorf("cursor is past the end of the schedule list; call schedule_list again without cursor")
	}
	pageEnd := offset + mcpScheduleListPageSize
	if pageEnd > len(items) {
		pageEnd = len(items)
	}
	schedules := make([]map[string]any, 0, pageEnd-offset)
	for _, item := range items[offset:pageEnd] {
		schedules = append(schedules, scheduleItemMCPListSummary(item))
	}
	view := map[string]any{"schema_version": "mcp.list_schedules.v2", "schedules": schedules, "page_count": len(schedules), "total_count": len(items), "has_more": pageEnd < len(items), "detail_tool": "amux.schedule_get"}
	if pageEnd < len(items) {
		view["next_cursor"] = strconv.Itoa(pageEnd)
		view["pagination_instruction"] = "Call amux.schedule_list again with next_cursor as cursor until has_more=false. Do not report the schedule inventory as complete before reading every page."
	}
	return view, nil
}

func scheduleItemMCPListSummary(item *pb.ScheduleItem) map[ // scheduleItemMCPListSummary is the compact per-schedule row returned by
// schedule_list. It deliberately omits the embedded task prompt, credential
// profile, and run history: with many schedules those full bodies bloat the
// single MCP tool response into tens of KB, and agent-side MCP clients keep
// only the head and tail of an oversized output — silently dropping everything
// in the middle. What the caller needs for triage (id, name, cadence, owner
// agent ref, enabled, last run) fits comfortably, and full detail remains
// available on demand via get_schedule.
string]any {
	if item == nil {
		return nil
	}
	view := map[string]any{"id": item.Id, "name": item.Name, "schedule_type": item.ScheduleType, "spec": item.Spec, "kind": item.Kind, "enabled": item.Enabled, "next_run_at": item.NextRunAt, "last_run_at": item.LastRunAt, "last_status": item.LastStatus}
	if item.NextRunAt > 0 {
		view["next_run_at_iso"] = mcpScheduleTimeISO(item.NextRunAt)
	}
	if item.LastRunAt > 0 {
		view["last_run_at_iso"] = mcpScheduleTimeISO(item.LastRunAt)
	}
	if item.ScheduleType == "" || item.ScheduleType == schedule.ScheduleTypeSessionPrompt {
		view["target_name"] = item.TargetName
		view["session_name"] = item.SessionName
		view["run_mode"] = item.RunMode
	}
	if item.ScheduleType == schedule.ScheduleTypeSubtask {
		var spec task.Spec
		if err := json.Unmarshal(item.TaskSpecJson, &spec); err == nil {
			if spec.Agent != "" {
				view["agent"] = spec.Agent
			}
		}
	}
	return view
}
func scheduleItemMCPView(item *pb.ScheduleItem) map[ // Surface just the agent ref (not the full prompt) so the list stays
// small while still identifying who runs each schedule.
string]any {
	if item == nil {
		return nil
	}
	view := map[string]any{"id": item.Id, "name": item.Name, "schedule_type": item.ScheduleType, "spec": item.Spec, "kind": item.Kind, "enabled": item.Enabled, "next_run_at": item.NextRunAt, "last_run_at": item.LastRunAt, "last_status": item.LastStatus}
	if item.NextRunAt > 0 {
		view["next_run_at_iso"] = mcpScheduleTimeISO(item.NextRunAt)
	}
	if item.LastRunAt > 0 {
		view["last_run_at_iso"] = mcpScheduleTimeISO(item.LastRunAt)
	}
	if item.ScheduleType == "" || item.ScheduleType == schedule.ScheduleTypeSessionPrompt {
		view["target_id"] = item.TargetId
		view["target_name"] = item.TargetName
		view["session_name"] = item.SessionName
		view["run_mode"] = item.RunMode
		view["prompt"] = item.Prompt
	}
	if item.ScheduleType == schedule.ScheduleTypeSubtask && len(item.TaskSpecJson) > 0 {
		var spec task.Spec
		if err := json.Unmarshal(item.TaskSpecJson, &spec); err == nil {
			view["task"] = taskSpecMCPView(spec)
		} else {
			view["task_error"] = "decode task spec: " + err.Error()
		}
	}
	if len(item.Runs) > 0 {
		view["runs"] = scheduleRunsMCPView(item.Runs)
	}
	return view
}
func taskSpecMCPView(spec task.Spec) map[string]any {
	view := map[string]any{"agent": spec.Agent, "task": spec.Task}
	if spec.Runtime != "" {
		view["runtime"] = spec.Runtime
	}
	if spec.Model != "" {
		view["model"] = spec.Model
	}
	if spec.ReasoningEffort != "" {
		view["reasoning_effort"] = spec.ReasoningEffort
	}
	if spec.Mode != "" {
		view["mode"] = spec.Mode
	}
	if spec.Isolation != "" {
		view["isolation"] = spec.Isolation
	}
	if spec.CredentialProfile != "" {
		view["credential_profile"] = spec.CredentialProfile
	}
	if spec.PermissionPolicy != "" {
		view["permission_policy"] = spec.PermissionPolicy
	}
	if spec.Workspace.Policy != "" || spec.Workspace.LocalDir != "" || spec.Workspace.Repo != nil {
		workspace := map[string]any{}
		if spec.Workspace.Policy != "" {
			workspace["policy"] = spec.Workspace.Policy
		}
		if spec.Workspace.LocalDir != "" {
			workspace["local_dir"] = spec.Workspace.LocalDir
		}
		if spec.Workspace.Repo != nil {
			repo := map[string]any{}
			if spec.Workspace.Repo.Kind != "" {
				repo["kind"] = spec.Workspace.Repo.Kind
			}
			if spec.Workspace.Repo.CacheKey != "" {
				repo["cache_key"] = spec.Workspace.Repo.CacheKey
			}
			if spec.Workspace.Repo.Commit != "" {
				repo["commit"] = spec.Workspace.Repo.Commit
			}
			workspace["repo"] = repo
		}
		view["workspace"] = workspace
	}
	if spec.TimeoutSeconds != 0 {
		view["timeout_seconds"] = spec.TimeoutSeconds
	}
	if spec.ResultGranularity != "" {
		view["result_granularity"] = spec.ResultGranularity
	}
	if len(spec.ExtraArgs) > 0 {
		view["extra_args"] = spec.ExtraArgs
	}
	return view
}
func scheduleRunsMCPView(runs []*pb.ScheduleRun) []map[string]any {
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		if run == nil {
			continue
		}
		out = append(out, map[string]any{"fired_at": run.FiredAt, "task_id": run.TaskId, "status": run.Status})
	}
	return out
}
func (h *mcpHandler) deleteSchedule(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	id, _, err := scheduleID(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newScheduleClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	deleted, err := clientop.ScheduleDelete(ctx, client, clientop.ScheduleIDRequest{ID: id})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[string]any{"schedule_id": id, "deleted": deleted})), nil
}
func mergeMCPTaskPatch(rawTask json.RawMessage, base clientop.ScheduledTaskSpec) (clientop.ScheduledTaskSpec, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawTask, &fields); err != nil {
		return clientop.ScheduledTaskSpec{}, fmt.Errorf("invalid task object: %w", err)
	}
	var partial clientop.ScheduledTaskSpec
	if err := json.Unmarshal(rawTask, &partial); err != nil {
		return clientop.ScheduledTaskSpec{}, fmt.Errorf("invalid task arguments: %w", err)
	}
	if _, ok := fields["extra_args"]; ok {
		base.ExtraArgs = append([]string(nil), partial.ExtraArgs...)
	}
	if _, ok := fields["agent"]; ok {
		base.Agent = partial.Agent
	}
	if _, ok := fields["runtime"]; ok {
		base.Runtime = partial.Runtime
	}
	if _, ok := fields["model"]; ok {
		base.Model = partial.Model
	}
	if _, ok := fields["reasoning_effort"]; ok {
		base.ReasoningEffort = partial.ReasoningEffort
	}
	if _, ok := fields["task"]; ok {
		base.Task = partial.Task
	}
	if _, ok := fields["run_as"]; ok {
		base.RunAs = partial.RunAs
	}
	if _, ok := fields["isolation"]; ok {
		base.Isolation = partial.Isolation
	}
	if _, ok := fields["credential_profile"]; ok {
		base.CredentialProfile = partial.CredentialProfile
	}
	if _, ok := fields["permission_policy"]; ok {
		base.PermissionPolicy = partial.PermissionPolicy
	}
	if _, ok := fields["timeout_seconds"]; ok {
		base.TimeoutSeconds = partial.TimeoutSeconds
	}
	if wsRaw, ok := fields["workspace"]; ok {
		var wsFields map[string]json.RawMessage
		if err := json.Unmarshal(wsRaw, &wsFields); err != nil {
			return clientop.ScheduledTaskSpec{}, fmt.Errorf("invalid task.workspace: %w", err)
		}
		if _, ok := wsFields["policy"]; ok {
			base.Workspace.Policy = partial.Workspace.Policy
		}
		if _, ok := wsFields["local_dir"]; ok {
			base.Workspace.LocalDir = partial.Workspace.LocalDir
		}
		if repoRaw, ok := wsFields["repo"]; ok {
			if strings.TrimSpace(string(repoRaw)) == "null" {
				base.Workspace.Repo = nil
			} else {
				var repoFields map[string]json.RawMessage
				if err := json.Unmarshal(repoRaw, &repoFields); err != nil {
					return clientop.ScheduledTaskSpec{}, fmt.Errorf("invalid task.workspace.repo: %w", err)
				}
				if base.Workspace.Repo == nil {
					base.Workspace.Repo = &clientop.RepoSpec{}
				}
				if partial.Workspace.Repo != nil {
					if _, ok := repoFields["kind"]; ok {
						base.Workspace.Repo.Kind = partial.Workspace.Repo.Kind
					}
					if _, ok := repoFields["remote"]; ok {
						base.Workspace.Repo.Remote = partial.Workspace.Repo.Remote
					}
					if _, ok := repoFields["cache_key"]; ok {
						base.Workspace.Repo.CacheKey = partial.Workspace.Repo.CacheKey
					}
					if _, ok := repoFields["commit"]; ok {
						base.Workspace.Repo.Commit = partial.Workspace.Repo.Commit
					}
					if _, ok := repoFields["subdir"]; ok {
						base.Workspace.Repo.Subdir = partial.Workspace.Repo.Subdir
					}
				}
			}
		}
	}
	return base, nil
}

func (h *mcpHandler) updateSchedule(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	id, _, err := scheduleID(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	var a struct {
		Name    *string         `json:"name"`
		Spec    *string         `json:"spec"`
		Prompt  *string         `json:"prompt"`
		RunMode *string         `json:"run_mode"`
		Task    json.RawMessage `json:"task"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if a.Spec != nil && strings.TrimSpace(*a.Spec) == "" {
		return mcp.ErrorText("spec must not be empty"), nil
	}
	if a.Prompt != nil && strings.TrimSpace(*a.Prompt) == "" {
		return mcp.ErrorText("prompt must not be empty"), nil
	}
	if a.RunMode != nil {
		if _, err := schedule.NormalizeRunMode(*a.RunMode); err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
	}
	client, closeFn, err := newScheduleClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	req := clientop.ScheduleUpdateRequest{
		ID:      id,
		Name:    a.Name,
		Spec:    a.Spec,
		Prompt:  a.Prompt,
		RunMode: a.RunMode,
	}
	if trimmedTask := strings.TrimSpace(string(a.Task)); trimmedTask != "" && trimmedTask != "null" {
		current, err := clientop.ScheduleGet(ctx, client, clientop.ScheduleIDRequest{ID: id})
		if err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		if current.ScheduleType != schedule.ScheduleTypeSubtask {
			return mcp.ErrorText("task is only valid for subtask schedules"), nil
		}
		base, err := scheduledTaskSpecFromPB(current)
		if err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		merged, err := mergeMCPTaskPatch(a.Task, base)
		if err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		req.Task = &merged
	}
	out, err := clientop.ScheduleUpdate(ctx, client, req)
	if err != nil {
		var fe *clientop.FieldError
		if errors.As(err, &fe) {
			return mcp.ErrorText(mcpScheduleValidationMessage(fe)), nil
		}
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[string]any{
		"schedule_id":     out.ID,
		"name":            out.Name,
		"spec":            out.Spec,
		"next_run_at":     out.NextRunAt,
		"next_run_at_iso": mcpScheduleTimeISO(out.NextRunAt),
	})), nil
}
func (h *mcpHandler) setScheduleEnabled(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	id, _, err := scheduleID(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if a.Enabled == nil {
		return mcp.ErrorText("enabled is required"), nil
	}
	client, closeFn, err := newScheduleClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	_, err = clientop.ScheduleSetEnabled(ctx, client, id, *a.Enabled)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[string]any{"schedule_id": id, "enabled": *a.Enabled})), nil
}
var newScheduleClientForMCP = defaultScheduleClientForMCP

func defaultScheduleClientForMCP() (pb.AmuxdClient, func() error, error) {
	client, closeFn, err := newAmuxdClient()
	if err != nil {
		return nil, nil, fmt.Errorf("schedule requires hub: %w", err)
	}
	return client, closeFn, nil
}
func scheduleID(args json.RawMessage) (int64, bool, error) {
	var a struct {
		ScheduleID  json.RawMessage `json:"schedule_id"`
		ID          json.RawMessage `json:"id"`
		IncludeRuns bool            `json:"include_runs"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return 0, false, fmt.Errorf("invalid arguments: %w", err)
	}
	raw := a.ScheduleID
	if len(raw) == 0 {
		raw = a.ID
	}
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false, fmt.Errorf("schedule_id is required")
	}
	id, err := parseMCPInt64(raw)
	if err != nil {
		return 0, false, fmt.Errorf("invalid schedule_id: %w", err)
	}
	return id, a.IncludeRuns, nil
}
func parseMCPInt64(raw json.RawMessage) (int64, error) {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0, fmt.Errorf("empty")
		}
		return strconv.ParseInt(s, 10, 64)
	}
	return 0, fmt.Errorf("expected integer or string")
}
func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// UTC is unambiguous even when the caller runs in a different timezone from the Hub.
func mcpScheduleTimeISO(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}
