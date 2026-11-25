package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func turnResultTools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        "amux.turn_result_get",
			Description: "只读获取单轮对话的结构化结果（执行状态、最终 Markdown 正文与飞书卡片/附件送达回执）。当需要核实某轮回答是否真正执行完成、取回完整正文、或确认飞书是否送达及对应 message_id 时调用；支持按 session+turn_id（可用 latest）或 feishu_message_id 查询。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"session": {"type": "string", "description": "Session 引用（如 target/name 或 session_id）；省略时若未传 feishu_message_id 则回落 AMUX_SESSION_ID"},
					"turn_id": {"type": "string", "description": "轮次 ID，或传 latest 查该会话最新一轮"},
					"feishu_message_id": {"type": "string", "description": "触发该轮的飞书消息 ID（om_...）；与 session+turn_id 二选一"},
					"bridge_id": {"type": "string", "description": "可选飞书机器人/bridge ID，配合 feishu_message_id 消歧"}
				}
			}`),
		},
		{
			Name:        "amux.schedule_result_get",
			Description: "只读获取定时任务单次执行的结构化结果（覆盖 session-prompt 与 subtask，含执行状态、最终 Markdown 正文与飞书送达回执）。当需要查看定时任务最近一次或指定 execution_id 的最终产出与送达情况时调用。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"schedule_id": {"type": "integer", "description": "定时任务 ID"},
					"execution_id": {"type": "integer", "description": "可选执行 ID（来自 amux.schedule_get 的 runs[].execution_id）；省略或 0 返回最近一次执行结果"}
				},
				"required": ["schedule_id"]
			}`),
		},
	}
}

func (h *mcpHandler) getTurnResult(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		Session         string `json:"session"`
		TurnID          string `json:"turn_id"`
		FeishuMessageID string `json:"feishu_message_id"`
		BridgeID        string `json:"bridge_id"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return mcp.ErrorText("invalid arguments: " + err.Error()), nil
		}
	}
	a.Session = strings.TrimSpace(a.Session)
	a.TurnID = strings.TrimSpace(a.TurnID)
	a.FeishuMessageID = strings.TrimSpace(a.FeishuMessageID)
	a.BridgeID = strings.TrimSpace(a.BridgeID)

	req := &pb.AmuxdGetTurnResultRequest{}
	switch {
	case a.FeishuMessageID != "" && a.TurnID == "":
		req.FeishuMessageId = a.FeishuMessageID
		req.BridgeId = a.BridgeID
	case a.FeishuMessageID == "" && a.TurnID != "":
		sessionRef := a.Session
		if sessionRef == "" {
			sessionRef = strings.TrimSpace(os.Getenv("AMUX_SESSION_ID"))
		}
		if sessionRef == "" {
			return mcp.ErrorText("session is required with turn_id (or pass feishu_message_id)"), nil
		}
		r, err := newSessionResolver()
		if err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		defer r.Close()
		resolved, err := r.ResolveSession(ctx, sessionRef)
		if err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		req.TargetId = resolved.Target.ID
		req.SessionId = resolved.Session.Id
		req.SessionName = resolved.Session.Name
		req.TurnId = a.TurnID
	default:
		return mcp.ErrorText("specify either (session + turn_id) or feishu_message_id"), nil
	}

	client, closeFn, err := newAmuxdClient()
	if err != nil {
		return mcp.ErrorText("turn result requires hub: " + err.Error()), nil
	}
	defer closeFn()

	resp, err := client.GetTurnResult(ctx, req)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(turnResultJSONEnvelope(resp))), nil
}

func (h *mcpHandler) getScheduleResult(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		ScheduleID  json.RawMessage `json:"schedule_id"`
		ID          json.RawMessage `json:"id"`
		ExecutionID json.RawMessage `json:"execution_id"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return mcp.ErrorText("invalid arguments: " + err.Error()), nil
		}
	}
	rawSched := a.ScheduleID
	if len(rawSched) == 0 {
		rawSched = a.ID
	}
	if len(rawSched) == 0 || string(rawSched) == "null" {
		return mcp.ErrorText("schedule_id is required"), nil
	}
	scheduleID, err := parseMCPInt64(rawSched)
	if err != nil || scheduleID <= 0 {
		return mcp.ErrorText("invalid schedule_id"), nil
	}
	var executionID int64
	if len(a.ExecutionID) > 0 && string(a.ExecutionID) != "null" {
		executionID, err = parseMCPInt64(a.ExecutionID)
		if err != nil || executionID < 0 {
			return mcp.ErrorText("invalid execution_id"), nil
		}
	}

	client, closeFn, err := newAmuxdClient()
	if err != nil {
		return mcp.ErrorText("schedule result requires hub: " + err.Error()), nil
	}
	defer closeFn()

	resp, err := client.GetTurnResult(ctx, &pb.AmuxdGetTurnResultRequest{
		ScheduleId:  scheduleID,
		ExecutionId: executionID,
	})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(turnResultJSONEnvelope(resp))), nil
}
