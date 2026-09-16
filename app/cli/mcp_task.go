package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/mcp"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func (h *mcpHandler) submit(ctx context.Context, args json.RawMessage, blocking bool) (mcp.ToolResult, error) {
	var spec task.Spec
	if err := json.Unmarshal(args, &spec); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	spec.Blocking = blocking
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
	if !blocking {
		return mcp.Text(toJSON(map[string]any{"task_id": run.ID, "status": run.Status})), nil
	}
	if err := drainStream(ctx, client, run.ID); err != nil {
		cancelTaskBestEffort(client, run.ID)
		return mcp.ErrorText(fmt.Sprintf("task_run task %s interrupted: %v", run.ID, err)), nil
	}
	final, err := getTask(ctx, client, run.ID)
	if err != nil {
		cancelTaskBestEffort(client, run.ID)
		return mcp.ErrorText(err.Error()), nil
	}
	if !final.Status.IsTerminal() {
		cancelTaskBestEffort(client, run.ID)
		return mcp.ErrorText(fmt.Sprintf("task_run task %s stream ended before a terminal status (last status: %s)", run.ID, final.Status)), nil
	}
	return mcp.Text(toJSON(taskView(final))), nil
}
func cancelTaskBestEffort(client pb.AmuxTaskClient, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = client.CancelSubtask(ctx, &pb.CancelSubtaskRequest{Id: id})
}

func fillParentSessionFromEnv(spec *task.Spec) {
	if spec.ParentSessionID == "" {
		spec.ParentSessionID = strings.TrimSpace(os.Getenv("AMUX_SESSION_ID"))
	}
}
func (h *mcpHandler) get(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	id, err := taskID(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	run, err := getTask(ctx, client, id)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(taskView(run))), nil
}

func (h *mcpHandler) stream(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		TaskID  string `json:"task_id"`
		ID      string `json:"id"`
		FromSeq uint64 `json:"from_seq"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	id := a.TaskID
	if id == "" {
		id = a.ID
	}
	if id == "" {
		return mcp.ErrorText("task_id is required"), nil
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	sctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	stream, err := client.StreamSubtask(sctx, &pb.StreamSubtaskRequest{Id: id, FromSeq: a.FromSeq})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	var events []task. // fillParentSessionFromEnv sets spec.ParentSessionID from the AMUX_SESSION_ID
		// environment variable when the caller has not already supplied a value.
		// Explicit caller values always take precedence.
		// stream long-polls the task event stream: it returns the batch of events seen
		// until the task reaches a terminal state or a server-side deadline elapses, so
		// the host can follow progress and answer escalated permissions across calls.
		// stream long-polls the task event stream from an optional from_seq cursor and
		// returns the batch seen until terminal or a deadline, plus next_seq so the host
		// can resume the next call without missing events (e.g. an escalated permission).
		TaskEvent
	nextSeq := a.FromSeq
	terminal := false
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			terminal = true
			break
		}
		if err != nil {
			break
		}
		var te task.TaskEvent
		if json.Unmarshal(ev.EventJson, &te) == nil {
			if te.Seq > nextSeq {
				nextSeq = te.Seq
			}
			te.Output = nil
			events = append(events, te)
		}
	}
	return mcp.Text(toJSON(map[string]any{"task_id": id, "terminal": terminal, "next_seq": nextSeq, "events": events})), nil
}
func (h *mcpHandler) cancel(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	id, err := taskID(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	if _, err := client.CancelSubtask(ctx, &pb.CancelSubtaskRequest{Id: id}); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[string]any{"task_id": id, "cancelled": true})), nil
}
func (h *mcpHandler) respond(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		TaskID       string `json:"task_id"`
		PermissionId string `json:"permission_id"`
		Allow        bool   `json:"allow"`
		OptionId     string `json:"option_id"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if a.TaskID == "" {
		return mcp.ErrorText("task_id is required"), nil
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	_, err = client.RespondSubtaskPermission(ctx, &pb.RespondSubtaskPermissionRequest{Id: a.TaskID, PermissionId: a.PermissionId, Allow: a.Allow, OptionId: a.OptionId, Reason: a.Reason})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[string]any{"task_id": a.TaskID, "allow": a.Allow})), nil
}

func (h *mcpHandler) taskList(ctx context.Context, _ json.RawMessage) (mcp.ToolResult, error) {
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.ListSubtasks(ctx, &pb.ListSubtasksRequest{})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	tasks := make([]map[ // taskList dispatches amux.task_list: it lists the tasks visible to the caller.
	// The hub applies ownership filtering only while fleet isolation is active;
	// each task is rendered through the same redacted taskView used by
	// task_get/task_run.
	string]any, 0, len(resp.TasksJson))
	for _, b := range resp.TasksJson {
		run, err := decodeTask(b)
		if err != nil {
			return mcp.ErrorText(err.Error()), nil
		}
		tasks = append(tasks, taskView(run))
	}
	return mcp.Text(toJSON(map[string]any{"tasks": tasks})), nil
}

func (h *mcpHandler) taskPrune(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a struct {
		DryRun           bool     `json:"dry_run"`
		OlderThanSeconds int64    `json:"older_than_seconds"`
		Statuses         []string `json:"statuses"` // taskPrune dispatches amux.task_prune: it deletes terminal task history via
		// the hub PruneSubtasks RPC. The hub permits delegated credentials only when
		// fleet principal isolation is inactive.

	}
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if a.OlderThanSeconds < 0 {
		return mcp.ErrorText("older_than_seconds must not be negative"), nil
	}
	client, closeFn, err := newTaskClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.PruneSubtasks(ctx, &pb.PruneSubtasksRequest{DryRun: a.DryRun, OlderThanSeconds: a.OlderThanSeconds, Statuses: a.Statuses})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	pruned := resp.GetTasks()
	if pruned == nil {
		pruned = []*pb.PrunedSubtask{}
	}
	skipped := resp.GetSkipped()
	if skipped == nil {
		skipped = []*pb.SkippedSubtask{}
	}
	return mcp.Text(toJSON(map[string]any{"dry_run": a.DryRun, "deleted_count": resp.GetDeletedCount(), "tasks": pruned, "skipped": skipped})), nil
}

func drainStream(ctx context.Context, client pb.AmuxTaskClient, id string) error {
	stream, err := client.StreamSubtask(ctx, &pb.StreamSubtaskRequest{Id: id})
	if err != nil {
		return err
	}
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func taskView(run *task.TaskRun) map[ // drainStream consumes a task's stream to completion for task_run.
// taskView is the redacted, structured view returned to the MCP host. It never
// exposes credential material; session_ref is a structured object, not a string.
string]any {
	view := map[string]any{"task_id": run.ID, "status": run.Status, "session_ref": map[string]string{"target_id": run.TargetID, "child_session_id": run.ChildSessionId}}
	if run.Error != "" {
		view["error"] = run.Error
	}
	if run.Result != nil {
		view["summary"] = run.Result.Summary
		resultView := map[string]any{"status": run.Result.Status, "changed_files": run.Result.ChangedFiles, "artifacts": run.Result.Artifacts, "diagnostics": run.Result.Diagnostics}
		if run.Result.FullTranscript != "" {
			resultView["full_transcript"] = run.Result.FullTranscript
		}
		if run.Result.DetailsRef != "" {
			resultView["details_ref"] = run.Result.DetailsRef
		}
		view["result"] = resultView
	}
	return view
}
func taskID(args json.RawMessage) (string, error) {
	var a struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.TaskID != "" {
		return a.TaskID, nil
	}
	if a.ID != "" {
		return a.ID, nil
	}
	return "", fmt.Errorf("task_id is required")
}
