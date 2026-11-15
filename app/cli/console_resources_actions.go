package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/gateway"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/webconsole"
)

type consoleSessionAction struct {
	Name         string   `json:"name"`
	Session      string   `json:"session,omitempty"`
	Target       string   `json:"target,omitempty"`
	TargetID     string   `json:"target_id,omitempty"`
	Text         string   `json:"text,omitempty"`
	QueueID      string   `json:"queue_id,omitempty"`
	PermissionID string   `json:"permission_id,omitempty"`
	OptionID     string   `json:"option_id,omitempty"`
	OptionIDs    []string `json:"option_ids,omitempty"`
	TurnID       string   `json:"turn_id,omitempty"`
	Cancel       bool     `json:"cancel,omitempty"`
}

// Validate action-specific keys as well as the resource DTO. Accepting an
// ignored mutation field would falsely report that a browser change applied.
func consoleValidateActionFields(resource, action string, data json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := consoleDecode(data, &fields); err != nil {
		return err
	}
	var allowed []string
	switch resource {
	case "sessions", "session":
		if action == "create" {
			return nil
		}
		allowed = []string{"name", "session", "target", "target_id"}
		switch action {
		case "submit":
			allowed = append(allowed, "text")
		case "queue-cancel":
			allowed = append(allowed, "queue_id")
		case "respond-permission":
			allowed = append(allowed, "permission_id", "option_id", "option_ids", "turn_id", "cancel")
		}
	case "tasks", "task":
		allowed = []string{"task_id"}
		if action == "respond" {
			allowed = append(allowed, "permission_id", "allow", "option_id", "reason")
		}
	case "instances", "instance":
		if action == "spawn" || action == "followup" {
			return nil
		}
		allowed = []string{"agent_instance_id"}
		if action == "send" || action == "steer" {
			allowed = append(allowed, "text")
		}
	case "schedules", "schedule":
		allowed = []string{"schedule_id"}
		switch action {
		case "create":
			allowed = []string{"schedule_type", "spec", "name"}
			kind := ""
			if raw, ok := fields["schedule_type"]; ok {
				if json.Unmarshal(raw, &kind) != nil {
					return consoleInvalid("invalid_data", "")
				}
			}
			if kind == "session-prompt" {
				allowed = append(allowed, "target_id", "target_name", "session_name", "prompt", "run_mode")
			} else {
				allowed = append(allowed, "task")
			}
		case "update":
			allowed = append(allowed, "name", "spec", "task")
		}
	case "packs":
		allowed = []string{"namespace"}
		if action == "install" {
			allowed = append(allowed, "url", "ref", "confirm")
		}
		if action == "rollback" {
			allowed = append(allowed, "revision", "confirm")
		}
		if action == "remove" {
			allowed = append(allowed, "confirm", "keep_files", "force", "keep_worktree")
		}
	case "credentials":
		switch action {
		case "set_env", "set_file":
			allowed = []string{"profile", "name", "value"}
		case "unset_env", "unset_file":
			allowed = []string{"profile", "name"}
		case "delete":
			allowed = []string{"profile", "confirm"}
		}
	}
	for key := range fields {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return consoleInvalid("invalid_data", "")
		}
	}
	return nil
}

func consoleAlias(primary, alias, field string) (string, error) {
	primary = strings.TrimSpace(primary)
	alias = strings.TrimSpace(alias)
	if primary != "" && alias != "" && primary != alias {
		return "", consoleInvalid("invalid_field", field)
	}
	if primary == "" {
		primary = alias
	}
	return primary, nil
}
func consoleOK(ok bool, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &consoleResourceError{code: "operation_rejected", cause: webconsole.ErrConflict}
	}
	return map[string]any{"ok": true}, nil
}
func consoleRunResponse(resp *pb.RunResponse, target string, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.GetSession() == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	value := map[string]any{"session": &gateway.SessionEntry{Session: consoleSession(resp.GetSession()), TargetID: target}, "ok": true}
	// Warnings may contain raw provider diagnostics. Record their existence, not
	// their potentially credential-bearing launch text.
	if len(resp.GetWarnings()) > 0 {
		value["warning_code"] = "launch_warning"
		value["warning_count"] = len(resp.GetWarnings())
	}
	return value, nil
}
func (b *consoleBackend) consoleExecuteSession(ctx context.Context, action string, data json.RawMessage) (any, error) {
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	if action == "create" {
		var req clientop.SessionRunRequest
		if err := consoleDecode(data, &req); err != nil {
			return nil, err
		}
		if err := consoleRequired(req.Name, "name"); err != nil {
			return nil, err
		}
		if err := consoleRequired(req.Profile, "profile"); err != nil {
			return nil, err
		}
		if _, err := normalizeRepoInput(req.Repo); err != nil {
			return nil, consoleInvalid("invalid_field", "repo")
		}
		launch, err := projectRunLaunchWithRuntime(req.Profile, req.AgentArgs, nil, req.Runtime, req.Model, func(ref string) (agentprofile.Profile, error) { return resolveHubRunProfile(ctx, b.hub, ref) })
		if err != nil {
			return nil, err
		}
		req.AgentType = launch.AgentType
		req.AgentProfile = launch.AgentProfile
		req.ProfileSpec = launch.ProfileSpec
		req.ExtraArgs = launch.ExtraArgs
		req.Env = launch.Env
		req.Target, err = clientop.ResolveSessionRunTarget(ctx, b.hub, req.Target)
		if err != nil {
			return nil, err
		}
		resp, err := clientop.SessionRun(ctx, b.hub, req)
		return consoleRunResponse(resp, req.Target, err)
	}
	var req consoleSessionAction
	if err := consoleDecode(data, &req); err != nil {
		return nil, err
	}
	var err error
	req.Name, err = consoleAlias(req.Name, req.Session, "name")
	if err != nil {
		return nil, err
	}
	req.Target, err = consoleAlias(req.Target, req.TargetID, "target")
	if err != nil {
		return nil, err
	}
	if err := consoleRequired(req.Name, "name"); err != nil {
		return nil, err
	}
	// Validate operation-specific inputs before any service call.
	switch action {
	case "submit":
		if err := consoleRequired(req.Text, "text"); err != nil {
			return nil, err
		}
	case "queue-cancel":
		if err := consoleRequired(req.QueueID, "queue_id"); err != nil {
			return nil, err
		}
	case "respond-permission":
		if err := consoleRequired(req.PermissionID, "permission_id"); err != nil {
			return nil, err
		}
		if !req.Cancel && len(req.OptionIDs) == 0 {
			if err := consoleRequired(req.OptionID, "option_id"); err != nil {
				return nil, err
			}
		}
		if req.Cancel && (req.OptionID != "" || len(req.OptionIDs) > 0) {
			return nil, consoleInvalid("invalid_field", "cancel")
		}
		for _, option := range req.OptionIDs {
			if strings.TrimSpace(option) == "" {
				return nil, consoleInvalid("invalid_field", "option_ids")
			}
		}
	}
	ref, err := clientop.ResolveSessionRef(ctx, b.hub, clientop.SessionRef{Name: req.Name, Target: req.Target})
	if err != nil {
		return nil, err
	}
	switch action {
	case "submit":
		resp, err := b.hub.Submit(ctx, &pb.AmuxdSubmitRequest{TargetId: ref.Target, Name: ref.Name, Text: req.Text, Source: "console", Async: true})
		return consoleOK(resp.GetOk(), err)
	case "interrupt":
		resp, err := clientop.SessionInterrupt(ctx, b.hub, ref)
		return consoleOK(resp.GetOk(), err)
	case "archive":
		resp, err := clientop.SessionStop(ctx, b.hub, ref)
		return consoleOK(resp.GetOk(), err)
	case "sleep":
		resp, err := clientop.SessionSleep(ctx, b.hub, ref)
		return consoleOK(resp.GetOk(), err)
	case "resume":
		resp, err := clientop.SessionResume(ctx, b.hub, ref)
		return consoleRunResponse(resp, ref.Target, err)
	case "restart":
		resp, err := clientop.SessionRestart(ctx, b.hub, ref)
		return consoleRunResponse(resp, ref.Target, err)
	case "queue-list":
		resp, err := clientop.SessionQueueList(ctx, b.hub, ref)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, consoleInvalid("backend_response_invalid", "")
		}
		items := resp.GetItems()
		if items == nil {
			items = []*pb.QueueItem{}
		}
		return map[string]any{"queue": items}, nil
	case "queue-clear":
		resp, err := clientop.SessionQueueClear(ctx, b.hub, ref)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, consoleInvalid("backend_response_invalid", "")
		}
		return map[string]any{"ok": true, "cleared": resp.GetCleared()}, nil
	case "queue-cancel":
		resp, err := clientop.SessionQueueCancel(ctx, b.hub, ref, req.QueueID)
		return consoleOK(resp.GetOk(), err)
	case "respond-permission":
		resp, err := b.hub.RespondPermission(ctx, &pb.AmuxdRespondPermissionRequest{TargetId: ref.Target, Name: ref.Name, PermissionId: req.PermissionID, OptionId: req.OptionID, OptionIds: req.OptionIDs, TurnId: req.TurnID, Cancel: req.Cancel})
		return consoleOK(resp.GetOk(), err)
	}
	return nil, consoleInvalid("unsupported_action", "")
}

// A safe, editable task projection is returned separately from ScheduleItem:
// task_spec_json stays absent and native args/resolved launch env never leave
// the server. Agent prompts are operator-owned content, not launch secrets.
func consoleScheduleTask(item *pb.ScheduleItem) (map[string]any, error) {
	if item == nil || len(item.GetTaskSpecJson()) == 0 {
		return nil, nil
	}
	var spec task.Spec
	if err := json.Unmarshal(item.GetTaskSpecJson(), &spec); err != nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	view := taskSpecMCPView(spec)
	for _, key := range []string{"extra_args", "mode", "result_granularity"} {
		delete(view, key)
	}
	if spec.RunAs != "" {
		view["run_as"] = spec.RunAs
	}
	return view, nil
}

func consoleTaskView(run *task.TaskRun) map[string]any {
	view := taskView(run)
	// taskView is the existing redacted MCP contract. Add only useful execution
	// metadata, never the source Spec, resolved_profile or execution identity.
	view["agent_profile"] = run.AgentProfile
	view["agent_type"] = run.AgentType
	view["agent_instance_id"] = run.AgentInstanceID
	view["mode"] = run.Mode
	view["requested_at"] = run.RequestedAt
	view["started_at"] = run.StartedAt
	view["finished_at"] = run.FinishedAt
	view["permission_policy"] = run.PermissionPolicy
	view["task"] = run.Task
	view["target_id"] = run.TargetID
	view["parent_session_id"] = run.ParentSessionID
	// Error strings originate in launch failures and can contain native args.
	if run.Error != "" {
		delete(view, "error")
		view["error_code"] = "task_failed"
	}
	return view
}
func (b *consoleBackend) consoleTasks(ctx context.Context, resource string, query url.Values) (map[string]any, error) {
	if b.tasks == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	if resource == "task" {
		id := consoleQueryFirst(query, "task_id", "id")
		if err := consoleRequired(id, "task_id"); err != nil {
			return nil, err
		}
		resp, err := b.tasks.GetSubtask(ctx, &pb.GetSubtaskRequest{Id: id})
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
		view := consoleTaskView(run)
		consoleAnnotateTask(view, run, b.consoleScheduleIndex(ctx))
		return map[string]any{"task": view}, nil
	}
	var scheduleFilter int64
	if raw := strings.TrimSpace(query.Get("schedule_id")); raw != "" {
		id, err := consolePositiveID(raw)
		if err != nil {
			return nil, consoleInvalid("invalid_field", "schedule_id")
		}
		scheduleFilter = id
	}
	runs, err := b.consoleTaskRuns(ctx)
	if err != nil {
		return nil, err
	}
	index := b.consoleScheduleIndex(ctx)
	views := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		view := consoleTaskView(run)
		consoleAnnotateTask(view, run, index)
		if scheduleFilter != 0 && view["schedule_id"] != scheduleFilter {
			continue
		}
		views = append(views, view)
	}
	return map[string]any{"tasks": views, "schedules": index.schedules, "schedule_index_complete": index.complete}, nil
}

// consoleTaskRuns lists every TaskRun, newest request first.
func (b *consoleBackend) consoleTaskRuns(ctx context.Context) ([]*task.TaskRun, error) {
	if b.tasks == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	resp, err := b.tasks.ListSubtasks(ctx, &pb.ListSubtasksRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	runs := make([]*task.TaskRun, 0, len(resp.GetTasksJson()))
	for _, raw := range resp.GetTasksJson() {
		run, err := decodeTask(raw)
		if err != nil || run.ID == "" {
			return nil, consoleInvalid("backend_response_invalid", "")
		}
		runs = append(runs, run)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].RequestedAt.After(runs[j].RequestedAt) })
	return runs, nil
}
func (b *consoleBackend) consoleExecuteTask(ctx context.Context, action string, data json.RawMessage) (any, error) {
	var req struct {
		TaskID       string `json:"task_id"`
		PermissionID string `json:"permission_id,omitempty"`
		Allow        *bool  `json:"allow,omitempty"`
		OptionID     string `json:"option_id,omitempty"`
		Reason       string `json:"reason,omitempty"`
	}
	if err := consoleDecode(data, &req); err != nil {
		return nil, err
	}
	if err := consoleRequired(req.TaskID, "task_id"); err != nil {
		return nil, err
	}
	if action == "respond" {
		if err := consoleRequired(req.PermissionID, "permission_id"); err != nil {
			return nil, err
		}
		if req.Allow == nil {
			return nil, consoleInvalid("field_required", "allow")
		}
	}
	if b.tasks == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	switch action {
	case "cancel":
		resp, err := b.tasks.CancelSubtask(ctx, &pb.CancelSubtaskRequest{Id: req.TaskID})
		return consoleOK(resp.GetOk(), err)
	case "respond":
		resp, err := b.tasks.RespondSubtaskPermission(ctx, &pb.RespondSubtaskPermissionRequest{Id: req.TaskID, PermissionId: req.PermissionID, Allow: *req.Allow, OptionId: req.OptionID, Reason: req.Reason})
		return consoleOK(resp.GetOk(), err)
	}
	return nil, consoleInvalid("unsupported_action", "")
}
func consoleInstanceView(instance *pb.AgentInstance) map[string]any {
	if instance == nil {
		return nil
	}
	value := listAgentsMCPView([]*pb.AgentInstance{instance})["instances"].([]map[string]any)[0]
	value["agent_profile"] = instance.GetAgentProfile()
	value["model_ref"] = instance.GetModelRef()
	value["created_at"] = instance.GetCreatedAt()
	value["target_name"] = instance.GetTargetName()
	return value
}
func (b *consoleBackend) consoleInstances(ctx context.Context, resource string, query url.Values) (map[string]any, error) {
	if b.tasks == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	if resource == "instance" {
		id := consoleQueryFirst(query, "agent_instance_id", "id")
		if err := consoleRequired(id, "agent_instance_id"); err != nil {
			return nil, err
		}
		resp, err := b.tasks.GetAgentInstance(ctx, &pb.GetAgentInstanceRequest{Id: id})
		if err != nil {
			return nil, err
		}
		if resp.GetInstance() == nil {
			return nil, consoleMissing()
		}
		view := consoleInstanceView(resp.GetInstance())
		b.consoleInstanceLinks(ctx, []map[string]any{view}, true)
		return map[string]any{"instance": view}, nil
	}
	resp, err := b.tasks.ListAgentInstances(ctx, &pb.ListAgentInstancesRequest{OwnerSessionId: query.Get("owner_session_id"), Status: query.Get("status")})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	views := make([]map[string]any, 0, len(resp.GetInstances()))
	for _, instance := range resp.GetInstances() {
		if instance != nil {
			views = append(views, consoleInstanceView(instance))
		}
	}
	b.consoleInstanceLinks(ctx, views, false)
	return map[string]any{"instances": views}, nil
}
func (b *consoleBackend) consoleExecuteInstance(ctx context.Context, action string, data json.RawMessage) (any, error) {
	if action == "spawn" || action == "followup" {
		return b.consoleSubmitInstance(ctx, action, data)
	}
	var req struct {
		ID   string `json:"agent_instance_id"`
		Text string `json:"text,omitempty"`
	}
	if err := consoleDecode(data, &req); err != nil {
		return nil, err
	}
	if err := consoleRequired(req.ID, "agent_instance_id"); err != nil {
		return nil, err
	}
	if action == "send" || action == "steer" {
		if err := consoleRequired(req.Text, "text"); err != nil {
			return nil, err
		}
	}
	if b.tasks == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	switch action {
	case "interrupt":
		resp, err := b.tasks.InterruptAgentInstance(ctx, &pb.InterruptAgentInstanceRequest{Id: req.ID})
		return consoleOK(resp.GetOk(), err)
	case "send", "steer":
		resp, err := b.tasks.SendAgentMessage(ctx, &pb.SendAgentMessageRequest{Id: req.ID, Text: req.Text, Source: "console", Steer: action == "steer"})
		if err != nil {
			return nil, err
		}
		if _, err := consoleOK(resp.GetOk(), nil); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "agent_instance_id": req.ID, "pending_messages": resp.GetPendingMessages(), "delivery": resp.GetDelivery(), "fallback_reason": resp.GetSteerFallbackReason()}, nil
	}
	return nil, consoleInvalid("unsupported_action", "")
}

func (b *consoleBackend) consoleExecuteSchedule(ctx context.Context, action string, data json.RawMessage) (any, error) {
	if b.hub == nil {
		return nil, consoleInvalid("backend_unavailable", "")
	}
	if action == "create" {
		var req struct {
			ScheduleType string                     `json:"schedule_type,omitempty"`
			Spec         string                     `json:"spec"`
			Name         string                     `json:"name,omitempty"`
			Task         clientop.ScheduledTaskSpec `json:"task,omitempty"`
			TargetID     string                     `json:"target_id,omitempty"`
			TargetName   string                     `json:"target_name,omitempty"`
			SessionName  string                     `json:"session_name,omitempty"`
			Prompt       string                     `json:"prompt,omitempty"`
			RunMode      string                     `json:"run_mode,omitempty"`
		}
		if err := consoleDecode(data, &req); err != nil {
			return nil, err
		}
		switch req.ScheduleType {
		case "", "subtask":
			return clientop.ScheduleCreateTask(ctx, b.hub, clientop.ScheduleCreateTaskRequest{Spec: req.Spec, Name: req.Name, Task: req.Task, CreatedBy: "console"})
		case "session-prompt":
			// Resolve through the Hub listing rather than accepting a guessed target or
			// a browser-supplied identity. The Hub still enforces mutation authority.
			if err := consoleRequired(req.Prompt, "prompt"); err != nil {
				return nil, err
			}
			if err := consoleRequired(req.Spec, "spec"); err != nil {
				return nil, err
			}
			ref, err := clientop.ResolveSessionRef(ctx, b.hub, clientop.SessionRef{Name: req.SessionName, Target: req.TargetID})
			if err != nil {
				return nil, err
			}
			return clientop.ScheduleCreateSessionPrompt(ctx, b.hub, clientop.ScheduleCreateSessionPromptRequest{Spec: req.Spec, Name: req.Name, TargetID: ref.Target, SessionName: ref.Name, Prompt: req.Prompt, RunMode: req.RunMode, CreatedBy: "console"})
		default:
			return nil, consoleInvalid("invalid_field", "schedule_type")
		}
	}
	var req struct {
		ID   int64                       `json:"schedule_id"`
		Name *string                     `json:"name,omitempty"`
		Spec *string                     `json:"spec,omitempty"`
		Task *clientop.ScheduledTaskSpec `json:"task,omitempty"`
	}
	if err := consoleDecode(data, &req); err != nil {
		return nil, err
	}
	if req.ID <= 0 {
		return nil, consoleInvalid("invalid_field", "schedule_id")
	}
	switch action {
	case "update":
		if req.Task != nil {
			// Native args are intentionally absent from safe read projections. A
			// content/model edit must not silently erase them. Explicit [] still
			// permits the operator to clear the field deliberately.
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(data, &fields)
			var taskFields map[string]json.RawMessage
			_ = json.Unmarshal(fields["task"], &taskFields)
			if _, explicit := taskFields["extra_args"]; !explicit {
				current, err := clientop.ScheduleGet(ctx, b.hub, clientop.ScheduleIDRequest{ID: req.ID})
				if err != nil {
					return nil, err
				}
				var original task.Spec
				if len(current.GetTaskSpecJson()) > 0 {
					if err := json.Unmarshal(current.GetTaskSpecJson(), &original); err != nil {
						return nil, consoleInvalid("backend_response_invalid", "")
					}
					req.Task.ExtraArgs = append([]string(nil), original.ExtraArgs...)
				}
			}
		}
		return clientop.ScheduleUpdate(ctx, b.hub, clientop.ScheduleUpdateRequest{ID: req.ID, Name: req.Name, Spec: req.Spec, Task: req.Task})
	case "enable", "disable":
		ok, err := clientop.ScheduleSetEnabled(ctx, b.hub, req.ID, action == "enable")
		return consoleOK(ok, err)
	case "delete":
		ok, err := clientop.ScheduleDelete(ctx, b.hub, clientop.ScheduleIDRequest{ID: req.ID})
		return consoleOK(ok, err)
	}
	return nil, consoleInvalid("unsupported_action", "")
}
