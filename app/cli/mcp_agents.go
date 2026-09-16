package cli

import (
	"context"
	"encoding/json"

	"github.com/amuxos/amux/internal/mcp"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
)

func (h *mcpHandler) spawnAgent(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var spec task.Spec
	if err := json.Unmarshal(args, &spec); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	spec.Mode = task.ModePersistent
	if spec.AgentInstanceID == "" {
		spec.AgentInstanceID = newAgentInstanceID()
	}
	spec.Blocking = false
	fillParentSessionFromEnv(&spec)
	specJSON, _ := json.Marshal(spec)
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.SubmitSubtask(ctx, &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	run, err := decodeTask(resp.TaskJson)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[ // spawnAgent dispatches amux.instance_spawn: a non-blocking persistent agent spawn
	// that returns a stable agent_instance_id handle. It is task_submit with
	// mode=persistent plus an explicit instance id (generated when the caller omits
	// one), so a later instance_followup / instance_send can address the same agent.
	string]any{"agent_instance_id": spec.AgentInstanceID, "task_id": run.ID, "status": run.Status})), nil
}

func (h *mcpHandler) sendMessage(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		AgentInstanceID string `json:"agent_instance_id"`
		Text            string `json:"text"`
		Source          string `json:"source"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if a.AgentInstanceID == "" {
		return mcp.ErrorText("agent_instance_id is required"), nil
	}
	if a.Text == "" {
		return mcp.ErrorText("text is required"), nil
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.SendAgentMessage(ctx, &pb.SendAgentMessageRequest{Id: a.AgentInstanceID, Text: a.Text, Source: a.Source})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[ // sendMessage dispatches amux.instance_send (QueueOnly): enqueue a message into
	// the persistent agent's mailbox without waking it. The message is delivered
	// ahead of the next task prompt when the agent is woken by instance_spawn /
	// instance_followup. It does not start a new turn.
	string]any{"agent_instance_id": a.AgentInstanceID, "queued": true, "pending_messages": resp.PendingMessages})), nil
}

func (h *mcpHandler) followupTask(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var spec task.Spec
	if err := json.Unmarshal(args, &spec); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if spec.AgentInstanceID == "" {
		return mcp.ErrorText("agent_instance_id is required"), nil
	}
	spec.Mode = task.ModePersistent
	spec.Blocking = false
	fillParentSessionFromEnv(&spec)
	specJSON, _ := json.Marshal(spec)
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.SubmitSubtask(ctx, &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	run, err := decodeTask(resp.TaskJson)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[ // followupTask dispatches amux.instance_followup (TriggerTurn): deliver a new task
	// prompt to an existing persistent agent and immediately trigger a new turn.
	// It is task_submit with mode=persistent and the agent's instance id, which
	// sticky-routes to the same target and resumes the provider session.
	string]any{"agent_instance_id": spec.AgentInstanceID, "task_id": run.ID, "status": run.Status})), nil
}

func (h *mcpHandler) waitAgent(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		AgentInstanceID string `json:"agent_instance_id"`
		TimeoutSeconds  int64  `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if a.AgentInstanceID == "" {
		return mcp.ErrorText("agent_instance_id is required"), nil
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.WaitAgentInstance(ctx, &pb.WaitAgentInstanceRequest{Id: a.AgentInstanceID, TimeoutSeconds: a.TimeoutSeconds})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	completed := resp.State == "sleeping" || resp.State == "archived"
	return mcp.Text(toJSON(map[ // waitAgent dispatches amux.instance_wait: join the agent's current turn until it
	// becomes sleeping/archived or the timeout elapses. Task results remain on the
	// task-level instance_get / instance_stream APIs.
	string]any{"agent_instance_id": a.AgentInstanceID, "activity": resp.Activity, "completed": completed, "state": resp.State, "timed_out": !completed})), nil
}

func (h *mcpHandler) listAgents(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		OwnerSessionID string `json:"owner_session_id"`
		Status         string `json:"status"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return mcp.ErrorText("invalid arguments: " + err.Error()), nil
		}
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.ListAgentInstances(ctx, &pb.ListAgentInstancesRequest{OwnerSessionId: a.OwnerSessionID, Status: a.Status})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(listAgentsMCPView(resp.Instances))), nil
}
func listAgentsMCPView(instances []* // listAgents dispatches amux.instance_list: list persistent agent instances and
// their lifecycle status (active / idle / archived), live child-session state,
// and pending mailbox depth.
pb.AgentInstance) map[string]any {
	views := make([]map[string]any, 0, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		view := map[string]any{"agent_instance_id": inst.Id, "name": inst.Name, "agent_type": inst.AgentType, "target_id": inst.TargetId, "status": inst.Status, "provider_session_id": inst.ProviderSessionId}
		if inst.OwnerSessionId != "" {
			view["owner_session_id"] = inst.OwnerSessionId
		}
		if inst.ChildSessionId != "" {
			view["child_session_id"] = inst.ChildSessionId
			view["child_session_state"] = inst.ChildSessionState
		}
		if inst.PendingMessages > 0 {
			view["pending_messages"] = inst.PendingMessages
		}
		if inst.LastUsedAt != 0 {
			view["last_used_at"] = inst.LastUsedAt
		}
		views = append(views, view)
	}
	return map[string]any{"instances": views}
}

func (h *mcpHandler) interruptAgent(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		AgentInstanceID string `json:"agent_instance_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if a.AgentInstanceID == "" {
		return mcp.ErrorText("agent_instance_id is required"), nil
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.InterruptAgentInstance(ctx, &pb.InterruptAgentInstanceRequest{Id: a.AgentInstanceID})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[ // interruptAgent dispatches amux.instance_interrupt: interrupt the agent's current
	// turn by agent_instance_id (the agent-level analog of task_cancel).
	string]any{"agent_instance_id": a.AgentInstanceID, "interrupted": resp.Ok})), nil
}

func newAgentInstanceID() string {
	return "agent-" + uuid.New().String()
}

// newAgentInstanceID returns a fresh agent_instance_id handle. It mirrors the
// task-id style (prefix + uuid) so handles are self-describing in logs.
