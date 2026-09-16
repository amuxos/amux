package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
)

var newCoordinationClient = func() (pb.CoordinationServiceClient, func() error, error) {
	_ = loadUserConfig()
	hc, err := newHubClientWithTimeout(hubAddr(), 3*time.Second)
	if err != nil {
		return nil, nil, err
	}
	return pb.NewCoordinationServiceClient(hc.conn), hc.Close, nil
}

const (
	delegationWaitReturnEvent      = "event"
	delegationWaitReturnActionable = "actionable"
	defaultDelegationWaitSeconds   = int32(25)
	maxDelegationWaitSeconds       = int32(30)
)

func (h *mcpHandler) listCollaborators(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		Kind string `json:"kind"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			return mcp.ErrorText("invalid arguments: " + err.Error()), nil
		}
	}
	input.Kind = strings.TrimSpace(input.Kind)
	if input.Kind != "" && input.Kind != "internal_agent" && input.Kind != "external_bot" {
		return mcp.ErrorText("kind must be internal_agent or external_bot"), nil
	}
	client, closeFn, err := newCoordinationClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	response, err := client.ListCollaborators(ctx, &pb.ListCollaboratorsRequest{InitiatorRef: coordinationInitiatorRef()})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	items := make([]map[string]any, 0, len(response.GetCollaborators()))
	for _, item := range response.GetCollaborators() {
		if input.Kind != "" && item.GetKind() != input.Kind {
			continue
		}
		items = append(items, collaboratorMCPView(item))
	}
	return mcp.Text(toJSON(map[string]any{
		"schema_version": "mcp.collaborator_list.v1",
		"collaborators":  items,
		"guidance":       "从 collaborators 中选择一项，并把 collaborator_id 原样传给 amux.delegation_create。",
	})), nil
}

func (h *mcpHandler) delegateCoordination(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		CollaboratorID string `json:"collaborator_id"`
		Task           string `json:"task"`
		TimeoutSeconds int32  `json:"timeout_seconds"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	input.CollaboratorID = strings.TrimSpace(input.CollaboratorID)
	input.Task = strings.TrimSpace(input.Task)
	if input.CollaboratorID == "" || input.Task == "" {
		return mcp.ErrorText("collaborator_id and task are required"), nil
	}
	if input.TimeoutSeconds < 0 {
		return mcp.ErrorText("timeout_seconds must not be negative"), nil
	}
	requestKey := strings.TrimSpace(input.IdempotencyKey)
	if requestKey == "" {
		requestKey = "mcp-delegation-" + uuid.NewString()
	}
	initiator := coordinationInitiatorRef()
	client, closeFn, err := newCoordinationClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	response, err := client.Delegate(ctx, &pb.DelegateRequest{
		CollaboratorId: input.CollaboratorID,
		Input:          input.Task,
		InitiatorRef:   initiator,
		ScopeRef:       initiator + ":coordination:" + requestKey,
		IdempotencyKey: requestKey,
		TimeoutSeconds: input.TimeoutSeconds,
	})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if response == nil || response.Snapshot == nil || response.Snapshot.Delegation == nil {
		return mcp.ErrorText("coordination service returned no delegation"), nil
	}
	view := coordinationSnapshotMCPView(response.Snapshot)
	view["created"] = response.Created
	return mcp.Text(toJSON(view)), nil
}

func coordinationInitiatorRef() string {
	if sessionID := strings.TrimSpace(os.Getenv("AMUX_SESSION_ID")); sessionID != "" {
		return "amux-session:" + sessionID
	}
	return "mcp:main"
}

func (h *mcpHandler) getDelegation(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	id, _, err := delegationCursorArgs(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newCoordinationClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	response, err := client.GetDelegation(ctx, &pb.GetDelegationRequest{DelegationId: id})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if response == nil || response.Snapshot == nil {
		return mcp.ErrorText("coordination service returned no snapshot"), nil
	}
	return mcp.Text(toJSON(coordinationSnapshotMCPView(response.Snapshot))), nil
}

func (h *mcpHandler) waitDelegation(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		DelegationID   string `json:"delegation_id"`
		FromSeq        uint64 `json:"from_seq"`
		TimeoutSeconds int32  `json:"timeout_seconds"`
		ReturnOn       string `json:"return_on"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	input.DelegationID = strings.TrimSpace(input.DelegationID)
	if input.DelegationID == "" {
		return mcp.ErrorText("delegation_id is required"), nil
	}
	if input.TimeoutSeconds < 0 {
		return mcp.ErrorText("timeout_seconds must not be negative"), nil
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = defaultDelegationWaitSeconds
	}
	if input.TimeoutSeconds > maxDelegationWaitSeconds {
		return mcp.ErrorText("timeout_seconds must not exceed 30"), nil
	}
	input.ReturnOn = strings.TrimSpace(input.ReturnOn)
	if input.ReturnOn == "" {
		input.ReturnOn = delegationWaitReturnEvent
	}
	if input.ReturnOn != delegationWaitReturnEvent && input.ReturnOn != delegationWaitReturnActionable {
		return mcp.ErrorText("return_on must be event or actionable"), nil
	}
	client, closeFn, err := newCoordinationClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	if input.ReturnOn == delegationWaitReturnActionable {
		return waitDelegationActionable(ctx, client, input.DelegationID, input.FromSeq, input.TimeoutSeconds)
	}
	response, err := client.WaitDelegation(ctx, &pb.WaitDelegationRequest{
		DelegationId:   input.DelegationID,
		FromSeq:        input.FromSeq,
		TimeoutSeconds: input.TimeoutSeconds,
	})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if response == nil || response.Snapshot == nil {
		return mcp.ErrorText("coordination service returned no snapshot"), nil
	}
	view := coordinationSnapshotMCPView(response.Snapshot)
	view["wait_timed_out"] = response.TimedOut
	return mcp.Text(toJSON(view)), nil
}

func waitDelegationActionable(
	ctx context.Context,
	client pb.CoordinationServiceClient,
	delegationID string,
	fromSeq uint64,
	pollSeconds int32,
) (mcp.ToolResult, error) {
	cursor := fromSeq
	deadlineUnixMillis := int64(0)
	suppressed := 0
	var lastResponse *pb.WaitDelegationResponse
	for {
		if deadlineUnixMillis > 0 {
			remaining := time.Until(time.UnixMilli(deadlineUnixMillis))
			if remaining <= 0 {
				return coordinationActionableWaitResult(lastResponse, true, suppressed), nil
			}
			remainingSeconds := int32((remaining + time.Second - 1) / time.Second)
			if remainingSeconds < pollSeconds {
				pollSeconds = remainingSeconds
			}
		}

		requestCursor := cursor
		response, err := client.WaitDelegation(ctx, &pb.WaitDelegationRequest{
			DelegationId:   delegationID,
			FromSeq:        requestCursor,
			TimeoutSeconds: pollSeconds,
		})
		if err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		if response == nil || response.Snapshot == nil {
			return mcp.ErrorText("coordination service returned no snapshot"), nil
		}
		lastResponse = response
		snapshot := response.Snapshot
		if snapshot.Delegation == nil {
			return mcp.ErrorText("coordination service returned no delegation"), nil
		}
		deadlineUnixMillis = snapshot.Delegation.DeadlineUnixMs

		originalEvents := snapshot.Events
		originalEventCount := len(originalEvents)
		if err := validateCoordinationWaitCursor(requestCursor, snapshot.NextSeq, originalEvents); err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		actionableEvents := make([]*pb.CoordinationEvent, 0, originalEventCount)
		for _, event := range originalEvents {
			if coordinationEventIsActionable(event) {
				actionableEvents = append(actionableEvents, event)
			} else {
				suppressed++
			}
		}
		cursor = snapshot.NextSeq
		snapshot.Events = actionableEvents

		if coordinationStatusIsTerminal(snapshot.Delegation.Status) || len(actionableEvents) > 0 {
			return coordinationActionableWaitResult(response, false, suppressed), nil
		}
		if deadlineUnixMillis <= 0 {
			return mcp.ErrorText("coordination service returned a non-terminal delegation without a deadline"), nil
		}
		if snapshot.Delegation.Status == "waiting" && response.TimedOut && originalEventCount == 0 {
			return mcp.ErrorText("delegation is waiting for permission, but no permission_requested event is available after from_seq; retry from an earlier cursor with return_on=event"), nil
		}
		if deadlineUnixMillis > 0 && time.Now().UnixMilli() >= deadlineUnixMillis {
			return coordinationActionableWaitResult(response, true, suppressed), nil
		}
		if originalEventCount == 0 && !response.TimedOut {
			return mcp.ErrorText("coordination service returned without an event, terminal state, or timeout"), nil
		}
	}
}

func validateCoordinationWaitCursor(fromSeq, nextSeq uint64, events []*pb.CoordinationEvent) error {
	if nextSeq < fromSeq {
		return errors.New("coordination service returned a non-monotonic delegation cursor")
	}
	previousSeq := fromSeq
	for _, event := range events {
		if event == nil || event.Seq <= previousSeq || event.Seq > nextSeq {
			return errors.New("coordination service returned events inconsistent with the delegation cursor")
		}
		previousSeq = event.Seq
	}
	return nil
}

func coordinationEventIsActionable(event *pb.CoordinationEvent) bool {
	if event == nil {
		return false
	}
	switch event.Kind {
	case "permission_requested", "result", "error":
		return true
	case "status_changed":
		return coordinationStatusIsTerminal(event.Status)
	default:
		return false
	}
}

func coordinationStatusIsTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "timed_out":
		return true
	default:
		return false
	}
}

func coordinationActionableWaitResult(response *pb.WaitDelegationResponse, timedOut bool, suppressed int) mcp.ToolResult {
	view := coordinationSnapshotMCPView(response.Snapshot)
	view["wait_timed_out"] = timedOut
	view["suppressed_event_count"] = suppressed
	return mcp.Text(toJSON(view))
}

func (h *mcpHandler) interveneDelegation(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input struct {
		DelegationID string `json:"delegation_id"`
		RequestID    string `json:"request_id"`
		Decision     string `json:"decision"`
		OptionID     string `json:"option_id"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if strings.TrimSpace(input.DelegationID) == "" || strings.TrimSpace(input.RequestID) == "" {
		return mcp.ErrorText("delegation_id and request_id are required"), nil
	}
	if input.Decision != "allow" && input.Decision != "deny" {
		return mcp.ErrorText("decision must be allow or deny"), nil
	}
	client, closeFn, err := newCoordinationClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	response, err := client.InterveneDelegation(ctx, &pb.InterveneDelegationRequest{DelegationId: input.DelegationID, RequestId: input.RequestID, Decision: input.Decision, OptionId: input.OptionID, Reason: input.Reason})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if response == nil || response.Delegation == nil {
		return mcp.ErrorText("coordination service returned no delegation"), nil
	}
	return mcp.Text(toJSON(map[string]any{"delegation_id": response.Delegation.Id, "status": response.Delegation.Status, "accepted": response.Accepted})), nil
}

func (h *mcpHandler) cancelDelegation(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	id, _, err := delegationCursorArgs(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newCoordinationClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	response, err := client.CancelDelegation(ctx, &pb.CancelDelegationRequest{DelegationId: id})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if response == nil || response.Delegation == nil {
		return mcp.ErrorText("coordination service returned no delegation"), nil
	}
	return mcp.Text(toJSON(map[string]any{"delegation_id": response.Delegation.Id, "status": response.Delegation.Status, "accepted": response.Accepted})), nil
}

func delegationCursorArgs(args json.RawMessage) (string, uint64, error) {
	var input struct {
		DelegationID string `json:"delegation_id"`
		FromSeq      uint64 `json:"from_seq"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", 0, errors.New("invalid arguments: " + err.Error())
	}
	input.DelegationID = strings.TrimSpace(input.DelegationID)
	if input.DelegationID == "" {
		return "", 0, errors.New("delegation_id is required")
	}
	return input.DelegationID, input.FromSeq, nil
}

func collaboratorMCPView(item *pb.CollaboratorDescriptor) map[string]any {
	if item == nil {
		return nil
	}
	return map[string]any{"collaborator_id": item.Id, "name": item.Name, "description": item.Description, "kind": item.Kind, "availability": item.Availability, "capabilities": item.Capabilities, "skills": item.Skills}
}

func coordinationSnapshotMCPView(snapshot *pb.CoordinationSnapshot) map[string]any {
	view := coordinationDelegationMCPView(snapshot.GetDelegation())
	events := make([]map[string]any, 0, len(snapshot.GetEvents()))
	for _, event := range snapshot.GetEvents() {
		events = append(events, coordinationEventMCPView(event))
	}
	view["events"] = events
	view["next_seq"] = snapshot.GetNextSeq()
	return view
}

func coordinationDelegationMCPView(item *pb.CoordinationDelegation) map[string]any {
	if item == nil {
		return map[string]any{}
	}
	view := map[string]any{"delegation_id": item.Id, "collaborator_id": item.CollaboratorId, "status": item.Status, "deadline_unix_ms": item.DeadlineUnixMs}
	if item.Result != nil {
		view["result"] = map[string]any{"summary": item.Result.Summary, "content": item.Result.Content, "content_type": item.Result.ContentType, "details_ref": item.Result.DetailsRef, "trust_level": item.Result.TrustLevel, "source_refs": item.Result.SourceRefs}
	}
	if item.Failure != nil {
		view["failure"] = map[string]any{"code": item.Failure.Code, "message": item.Failure.Message}
	}
	return view
}

func coordinationEventMCPView(event *pb.CoordinationEvent) map[string]any {
	if event == nil {
		return nil
	}
	view := map[string]any{"seq": event.Seq, "kind": event.Kind, "status": event.Status, "at_unix_ms": event.AtUnixMs}
	if event.Content != "" {
		view["content"] = event.Content
	}
	if event.Permission != nil {
		view["permission"] = map[string]any{"request_id": event.Permission.Id, "summary": event.Permission.Summary, "tool": event.Permission.Tool, "command": event.Permission.Command, "paths": event.Permission.Paths, "write": event.Permission.Write, "network": event.Permission.Network}
	}
	if event.Result != nil {
		view["result"] = map[string]any{"summary": event.Result.Summary, "details_ref": event.Result.DetailsRef, "trust_level": event.Result.TrustLevel}
	}
	if event.Failure != nil {
		view["failure"] = map[string]any{"code": event.Failure.Code, "message": event.Failure.Message}
	}
	return view
}

func coordinationTools() []mcp.Tool {
	listSchema := json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["internal_agent","external_bot"],"description":"可选的协作者类型过滤；明确寻找当前群内的外部 Bot 时必须传 external_bot"}}}`)
	delegateSchema := json.RawMessage(`{"type":"object","properties":{"collaborator_id":{"type":"string","description":"amux.collaborator_list 返回的不透明协作者 ID，必须原样传入"},"task":{"type":"string","description":"交给协作者的完整、可独立执行的任务说明"},"timeout_seconds":{"type":"integer","minimum":0,"description":"整个 Delegation 的截止时间；0 或省略时使用后端默认值"},"idempotency_key":{"type":"string","description":"可选的重试幂等键；仅同一逻辑委派可以复用"}},"required":["collaborator_id","task"]}`)
	getSchema := json.RawMessage(`{"type":"object","properties":{"delegation_id":{"type":"string","description":"要读取当前快照的 Delegation ID"}},"required":["delegation_id"]}`)
	waitSchema := json.RawMessage(`{"type":"object","properties":{"delegation_id":{"type":"string","description":"要等待的 Delegation ID"},"from_seq":{"type":"integer","minimum":0,"description":"上次响应返回的 next_seq；用于增量读取事件"},"timeout_seconds":{"type":"integer","minimum":0,"maximum":30,"description":"底层有界长轮询窗口，默认 25 秒，不是整个 Delegation 的超时；actionable 模式会在 MCP 调用内跨窗口继续等待"},"return_on":{"type":"string","enum":["event","actionable"],"default":"event","description":"event 保持兼容：下一事件、终态或窗口超时即返回；actionable 内部消费普通 output/status，只在权限请求、结果、错误、终态或 Delegation deadline 返回"}},"required":["delegation_id"]}`)
	interveneSchema := json.RawMessage(`{"type":"object","properties":{"delegation_id":{"type":"string","description":"需要处理权限请求的 Delegation ID"},"request_id":{"type":"string","description":"permission_requested 事件中的请求 ID"},"decision":{"type":"string","enum":["allow","deny"],"description":"允许或拒绝当前权限请求"},"option_id":{"type":"string","description":"后端权限请求提供的可选选项 ID"},"reason":{"type":"string","description":"可选的决策说明"}},"required":["delegation_id","request_id","decision"]}`)
	cancelSchema := json.RawMessage(`{"type":"object","properties":{"delegation_id":{"type":"string","description":"要请求取消的 Delegation ID"}},"required":["delegation_id"]}`)
	return []mcp.Tool{
		{Name: "amux.collaborator_list", Description: "仅在准备实际委派时按 kind 列出内部 Agent 或 external_bot；纯 Agent 能力发现请使用 amux.profile_list，不要调用本工具。external_bot 只返回当前 Session 所绑定飞书群内的 Bot；CLI、非飞书或无有效群 binding 时为空。", InputSchema: listSchema},
		{Name: "amux.delegation_create", Description: "创建一次性 Delegation 并返回 ID。external_bot 仅在用户明确要求或确认后使用，且只能发送给当前 Session 所绑定飞书群内的 Bot；结果为 external_untrusted。创建预算默认最多 2 次，用户指定数量为硬上限。", InputSchema: delegateSchema},
		{Name: "amux.delegation_get", Description: "读取 Delegation 当前快照；增量事件使用 delegation_wait。", InputSchema: getSchema},
		{Name: "amux.delegation_wait", Description: "等待 Delegation；event 增量返回事件，actionable 避免普通进度反复唤醒主模型并保留权限/终态唤醒。wait_timed_out 不是 Delegation 终态；按返回状态维护实际创建的尝试账本。", InputSchema: waitSchema},
		{Name: "amux.delegation_intervene", Description: "回答支持 permission_response 的 Delegation 权限请求。", InputSchema: interveneSchema},
		{Name: "amux.delegation_cancel", Description: "请求取消支持 cancelable 的 Delegation；cancelled 是停止信号，不得自动创建替代委派。", InputSchema: cancelSchema},
	}
}
