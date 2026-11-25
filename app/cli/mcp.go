package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newMcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server exposing amux.* agent coordination and task tools over stdio",
		Long: "Starts a Model Context Protocol server on stdio so an MCP host (e.g. a " +
			"main agent) can discover collaborators, delegate single-turn work, and manage " +
			"agent tasks and persistent agents via amux.* tools. The server is on-demand and " +
			"uses the hub CoordinationService and task control plane as backends.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = loadUserConfig()
			if !config.GetMCP().Enabled {
				return fmt.Errorf("MCP is disabled by mcp.enabled=false; enable it with `amux system config set mcp.enabled true`")
			}
			cleanup := registerTrackedProcess(trackedProcessRoleMCP)
			defer cleanup()
			// Bind a session-launched MCP server's lifetime to its owners so it
			// cannot leak past the session that spawned it. Gated on
			// AMUX_SESSION_ID so manual/debug `amux mcp` runs are unaffected.
			if sid := strings.TrimSpace(os.Getenv("AMUX_SESSION_ID")); sid != "" {
				startMCPWatchdog(cmd.Context(), mcpWatchdogConfig{
					sessionID:   sid,
					initialPPID: os.Getppid(),
				}, func(reason string) {
					fmt.Fprintf(os.Stderr, "amux mcp: shutting down: %s\n", reason)
					cleanup()
					os.Exit(0)
				})
			}
			h := &mcpHandler{}
			srv := mcp.NewServer("amux", version, runtimeAMUXTools(cmd.Context()), h.handle)
			return srv.Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(newMCPCallCmd())
	return cmd
}

// mcpHandler dispatches MCP tool calls to the local AmuxTask service. It dials
// lazily per call so the MCP server can start before the runtime is up.
type mcpHandler struct {
	continuationsMu sync.Mutex
	continuations   map[string]mcpContinuation
}

const (
	// Codex 0.146 keeps only the head and tail of sufficiently large MCP tool
	// results. Keep every amux response comfortably below that client-side
	// threshold and expose the remainder through amux.result_continue.
	mcpResultMaxBytes       = 6 << 10
	mcpContinuationTTL      = 5 * time.Minute
	mcpContinuationMaxItems = 16
)

type mcpContinuation struct {
	text      string
	createdAt time.Time
	isError   bool
	memory    bool
}

func (h *mcpHandler) handle(ctx context.Context, name string, args json.RawMessage) (result mcp.ToolResult, err error) {
	if name != "amux.result_continue" {
		defer func() {
			if err == nil {
				result = h.boundResult(result, strings.HasPrefix(name, "amux.memory_"))
			}
		}()
	}
	switch name {
	case "amux.feishu_send_message":
		return h.feishuSendMessage(ctx, args)
	case "amux.memory_search":
		return h.memorySearch(ctx, args)
	case "amux.memory_get":
		return h.memoryGet(ctx, args)
	case "amux.memory_upsert":
		return h.memoryUpsert(ctx, args)
	case "amux.memory_forget":
		return h.memoryForget(ctx, args)
	case "amux.profile_list":
		return h.listAgentProfilesPage(ctx, args)
	case "amux.profile_get":
		return h.getAgentProfile(ctx, args)
	case "amux.local_expert_read":
		return h.readLocalExpert(ctx, args)
	case "amux.local_expert_change":
		return h.changeLocalExpert(ctx, args)
	case "amux.result_continue":
		return h.continueResult(ctx, args), nil
	case "amux.task_submit":
		return h.submit(ctx, args, false)
	case "amux.task_run":
		return h.submit(ctx, args, true)
	case "amux.task_get":
		return h.get(ctx, args)
	case "amux.task_stream":
		return h.stream(ctx, args)
	case "amux.task_cancel":
		return h.cancel(ctx, args)
	case "amux.task_respond_permission":
		return h.respond(ctx, args)
	case "amux.task_list":
		return h.taskList(ctx, args)
	case "amux.task_prune":
		return h.taskPrune(ctx, args)
	case "amux.schedule_create":
		return h.createSchedule(ctx, args)
	case "amux.schedule_list":
		return h.listSchedules(ctx, args)
	case "amux.schedule_get":
		return h.getSchedule(ctx, args)
	case "amux.schedule_delete":
		return h.deleteSchedule(ctx, args)
	case "amux.schedule_update":
		return h.updateSchedule(ctx, args)
	case "amux.schedule_set_enabled":
		return h.setScheduleEnabled(ctx, args)
	case "amux.schedule_trigger":
		return h.triggerSchedule(ctx, args)
	case "amux.instance_spawn":
		return h.spawnAgent(ctx, args)
	case "amux.instance_send":
		return h.sendMessage(ctx, args)
	case "amux.instance_steer":
		return h.steerAgent(ctx, args)
	case "amux.instance_followup":
		return h.followupTask(ctx, args)
	case "amux.instance_wait":
		return h.waitAgent(ctx, args)
	case "amux.instance_list":
		return h.listAgents(ctx, args)
	case "amux.instance_interrupt":
		return h.interruptAgent(ctx, args)
	case "amux.collaborator_list":
		return h.listCollaborators(ctx, args)
	case "amux.delegation_create":
		return h.delegateCoordination(ctx, args)
	case "amux.delegation_get":
		return h.getDelegation(ctx, args)
	case "amux.delegation_wait":
		return h.waitDelegation(ctx, args)
	case "amux.delegation_intervene":
		return h.interveneDelegation(ctx, args)
	case "amux.delegation_cancel":
		return h.cancelDelegation(ctx, args)
	case "amux.session_list":
		return h.sessionList(ctx, args)
	case "amux.session_run":
		return h.sessionRun(ctx, args)
	case "amux.session_send":
		return h.sessionSend(ctx, args)
	case "amux.session_interrupt":
		return h.sessionInterrupt(ctx, args)
	case "amux.session_sleep":
		return h.sessionSleep(ctx, args)
	case "amux.session_resume":
		return h.sessionResume(ctx, args)
	case "amux.session_stop":
		return h.sessionStop(ctx, args)
	case "amux.session_restart":
		return h.sessionRestart(ctx, args)
	case "amux.session_set_model":
		return h.sessionSetModel(ctx, args)
	case "amux.session_stream":
		return h.sessionStream(ctx, args)
	case "amux.session_queue_list":
		return h.sessionQueueList(ctx, args)
	case "amux.session_queue_clear":
		return h.sessionQueueClear(ctx, args)
	case "amux.session_queue_cancel":
		return h.sessionQueueCancel(ctx, args)
	case "amux.session_prune":
		return h.sessionPrune(ctx, args)
	default:
		return mcp.ErrorText("unknown tool: " + name), nil
	}
}

// boundResult is the final safety net for every MCP tool. Domain handlers keep
// common list responses structured and compact; this layer ensures an unusual
// large detail, transcript, queue item, or future tool can never be silently
// head/tail-truncated by the MCP host.
func (h *mcpHandler) boundResult(result mcp.ToolResult, memoryResult ...bool) mcp.ToolResult {
	if len(result.Content) != 1 || len(result.Content[0].Text) <= mcpResultMaxBytes {
		return result
	}
	token := uuid.NewString()
	h.continuationsMu.Lock()
	h.pruneContinuationsLocked(time.Now())
	if h.continuations == nil {
		h.continuations = make(map[string]mcpContinuation)
	}
	for len(h.continuations) >= mcpContinuationMaxItems {
		h.deleteOldestContinuationLocked()
	}
	h.continuations[token] = mcpContinuation{
		text: result.Content[0].Text, createdAt: time.Now(), isError: result.IsError,
		memory: len(memoryResult) > 0 && memoryResult[0],
	}
	h.continuationsMu.Unlock()
	return h.continuationPage(formatMCPContinuationCursor(token, 0))
}

func (h *mcpHandler) continueResult(ctx context.Context, args json.RawMessage) mcp.ToolResult {
	var input struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error())
	}
	if strings.TrimSpace(input.Cursor) == "" {
		return mcp.ErrorText("cursor is required")
	}
	// A cached memory response still belongs to the experiment. Do not let
	// the generic continuation tool bypass a disabled Hub or frozen scope.
	token, _, _ := parseMCPContinuationCursor(input.Cursor)
	h.continuationsMu.Lock()
	memoryResult := h.continuations[token].memory
	h.continuationsMu.Unlock()
	if memoryResult && !memoryAvailable(ctx) {
		return mcp.ErrorText("memory is unavailable; cached memory continuation is disabled")
	}
	return h.continuationPage(input.Cursor)
}

func (h *mcpHandler) continuationPage(cursor string) mcp.ToolResult {
	token, start, err := parseMCPContinuationCursor(cursor)
	if err != nil {
		return mcp.ErrorText("invalid continuation cursor; call the original tool again")
	}
	h.continuationsMu.Lock()
	defer h.continuationsMu.Unlock()
	h.pruneContinuationsLocked(time.Now())
	continuation, ok := h.continuations[token]
	if !ok {
		return mcp.ErrorText("continuation cursor is invalid or expired; call the original tool again")
	}

	if start > len(continuation.text) {
		return mcp.ErrorText("continuation cursor is past the end of the result; call the original tool again")
	}
	end := continuationPageEnd(continuation.text, start, token, continuation.isError)
	complete := end == len(continuation.text)
	page := continuationEnvelope(continuation.text[start:end], formatMCPContinuationCursor(token, end), complete, len(continuation.text), continuation.isError)
	return mcp.Text(toJSON(page))
}

func formatMCPContinuationCursor(token string, offset int) string {
	return token + ":" + strconv.Itoa(offset)
}

func parseMCPContinuationCursor(cursor string) (string, int, error) {
	separator := strings.LastIndexByte(cursor, ':')
	if separator <= 0 || separator == len(cursor)-1 {
		return "", 0, fmt.Errorf("malformed cursor")
	}
	offset, err := strconv.Atoi(cursor[separator+1:])
	if err != nil || offset < 0 {
		return "", 0, fmt.Errorf("malformed cursor offset")
	}
	return cursor[:separator], offset, nil
}

func (h *mcpHandler) pruneContinuationsLocked(now time.Time) {
	for token, continuation := range h.continuations {
		if now.Sub(continuation.createdAt) > mcpContinuationTTL {
			delete(h.continuations, token)
		}
	}
}

func (h *mcpHandler) deleteOldestContinuationLocked() {
	var oldestToken string
	var oldest time.Time
	for token, continuation := range h.continuations {
		if oldestToken == "" || continuation.createdAt.Before(oldest) {
			oldestToken, oldest = token, continuation.createdAt
		}
	}
	if oldestToken != "" {
		delete(h.continuations, oldestToken)
	}
}

func continuationPageEnd(text string, start int, token string, isError bool) int {
	remaining := text[start:]
	boundaries := make([]int, 1, len(remaining)+1)
	for offset := range remaining {
		if offset > 0 {
			boundaries = append(boundaries, offset)
		}
	}
	boundaries = append(boundaries, len(remaining))
	low, high := 1, len(boundaries)-1
	best := 0
	for low <= high {
		mid := low + (high-low)/2
		end := start + boundaries[mid]
		complete := end == len(text)
		encoded := toJSON(continuationEnvelope(text[start:end], formatMCPContinuationCursor(token, end), complete, len(text), isError))
		if len(encoded) <= mcpResultMaxBytes {
			best = end
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == 0 {
		return start + boundaries[1]
	}
	return best
}

func continuationEnvelope(chunk, nextCursor string, complete bool, originalBytes int, isError bool) map[string]any {
	page := map[string]any{
		"schema_version": "mcp.result_page.v1",
		"complete":       complete,
		"content_chunk":  chunk,
		"original_bytes": originalBytes,
		"original_error": isError,
	}
	if !complete {
		page["next_cursor"] = nextCursor
		page["next_tool"] = "amux.result_continue"
		page["instruction"] = "Call amux.result_continue with next_cursor until complete=true; concatenate content_chunk values in order. Do not treat a partial page as the complete result."
	}
	return page
}

var newAgentProfileClient = func() (pb.AmuxdClient, func() error, error) {
	_ = loadUserConfig()
	hc, err := newHubClientWithTimeout(hubAddr(), 3*time.Second)
	if err != nil {
		return nil, nil, err
	}
	return hc.c, hc.Close, nil
}

type discoveredAgentProfile struct {
	Profile agentprofile.Profile
	Skills  []agentprofile.SkillInfo
}

const mcpProfileListPageSize = 10

// profileMCPViewListSummary is the compact per-profile row returned by
// profile_list. The full profile view embeds skill descriptions, placement
// requirements, envs, mcp config, etc.; with many profiles those bodies bloat
// the single MCP tool response to tens of KB and agent-side MCP clients keep
// only the head and tail of an oversized output — silently dropping profiles
// in the middle (the same class of failure this function's sibling fixes for
// schedule_list). The list keeps only what profile selection needs (ref, agent
// type, description, supported models, and skill names without their long
// descriptions), and full detail remains available on demand via profile_get.

// Skill names only — the descriptions are the bulk of the bloat and are
// available from profile_get when a specific profile is being evaluated.

// task_run blocks until terminal, then returns the redacted result view.

// fillParentSessionFromEnv sets spec.ParentSessionID from the AMUX_SESSION_ID
// environment variable when the caller has not already supplied a value.
// Explicit caller values always take precedence.

// stream long-polls the task event stream: it returns the batch of events seen
// until the task reaches a terminal state or a server-side deadline elapses, so
// the host can follow progress and answer escalated permissions across calls.
// stream long-polls the task event stream from an optional from_seq cursor and
// returns the batch seen until terminal or a deadline, plus next_seq so the host
// can resume the next call without missing events (e.g. an escalated permission).

// deadline or transport end: return what we have

// keep the batch compact; full output via logs

// taskList dispatches amux.task_list: it lists the tasks visible to the caller.
// The hub applies ownership filtering only while fleet isolation is active;
// each task is rendered through the same redacted taskView used by
// task_get/task_run.

// taskPrune dispatches amux.task_prune: it deletes terminal task history via
// the hub PruneSubtasks RPC. The hub restricts delegated callers only while
// fleet isolation is active; non-isolated fleets accept valid agent credentials.

type mcpCreateScheduleArgs struct {
	Name         string                     `json:"name"`
	Spec         string                     `json:"spec"`
	Timezone     string                     `json:"timezone"`
	ScheduleType string                     `json:"schedule_type"`
	Session      string                     `json:"session"`
	Prompt       string                     `json:"prompt"`
	RunMode      string                     `json:"run_mode"`
	Task         clientop.ScheduledTaskSpec `json:"task"`
}

// mcpScheduleFieldNames renders clientop validation errors in JSON-path
// vocabulary, matching the shape a tool caller actually sent.
// FieldPermissionPolicy, FieldWorkspaceLocalDir, and FieldRepoCacheKey cannot
// reach err.Render today: mcpScheduleValidationMessage special-cases those
// fields first to preserve the MCP surface's pre-clientop wording. Their
// entries stay here anyway as a safety net, so a future clientop rule that
// reaches err.Render through a path this function does not special-case
// still gets a JSON-path name instead of falling back to the bare
// clientop.Field identifier.
var mcpScheduleFieldNames = map[clientop.Field]string{
	clientop.FieldAgent:             "task.agent",
	clientop.FieldTask:              "task.task",
	clientop.FieldPermissionPolicy:  "task.permission_policy",
	clientop.FieldWorkspaceLocalDir: "task.workspace.local_dir",
	clientop.FieldRepoCacheKey:      "task.workspace.repo.cache_key",
	clientop.FieldPrompt:            "prompt",
	clientop.FieldScheduleType:      "schedule_type",
	clientop.FieldRunMode:           "run_mode",
	clientop.FieldTargetID:          "target_id",
	clientop.FieldScheduledSession:  "session_name",
	clientop.FieldSessionName:       "session",
}

// mcpScheduleCreateError renders a ScheduleCreateTask failure for the tool
// caller. A validation failure is a *clientop.FieldError and must be phrased
// in JSON-path vocabulary; anything else passes through as-is.

// mcpScheduleValidationMessage keeps the MCP surface's established wording.

const mcpScheduleListPageSize = 10

// scheduleItemMCPListSummary is the compact per-schedule row returned by
// schedule_list. It deliberately omits the embedded task prompt, credential
// profile, and run history: with many schedules those full bodies bloat the
// single MCP tool response into tens of KB, and agent-side MCP clients keep
// only the head and tail of an oversized output — silently dropping everything
// in the middle. What the caller needs for triage (id, name, cadence, owner
// agent ref, enabled, last run) fits comfortably, and full detail remains
// available on demand via get_schedule.

// Surface just the agent ref (not the full prompt) so the list stays
// small while still identifying who runs each schedule.

// drainStream consumes a task's stream to completion for task_run.

// taskView is the redacted, structured view returned to the MCP host. It never
// exposes credential material; session_ref is a structured object, not a string.

// spawnAgent dispatches amux.instance_spawn: a non-blocking persistent agent spawn
// that returns a stable agent_instance_id handle. It is task_submit with
// mode=persistent plus an explicit instance id (generated when the caller omits
// one), so a later instance_followup / instance_send can address the same agent.

// sendMessage dispatches amux.instance_send (QueueOnly): enqueue a message into
// the persistent agent's mailbox without waking it. The message is delivered
// ahead of the next task prompt when the agent is woken by instance_spawn /
// instance_followup. It does not start a new turn.

// followupTask dispatches amux.instance_followup (TriggerTurn): deliver a new task
// prompt to an existing persistent agent and immediately trigger a new turn.
// It is task_submit with mode=persistent and the agent's instance id, which
// sticky-routes to the same target and resumes the provider session.

// waitAgent dispatches amux.instance_wait: join the agent's current turn until it
// becomes sleeping/archived or the timeout elapses. Task results remain on the
// task-level instance_get / instance_stream APIs.

// listAgents dispatches amux.instance_list: list persistent agent instances and
// their lifecycle status (active / idle / archived), live child-session state,
// and pending mailbox depth.

// interruptAgent dispatches amux.instance_interrupt: interrupt the agent's current
// turn by agent_instance_id (the agent-level analog of task_cancel).

// newAgentInstanceID returns a fresh agent_instance_id handle. It mirrors the
// task-id style (prefix + uuid) so handles are self-describing in logs.

// ── Session tools ────────────────────────────────────────────────────────────
//
// The amux.session_* tools expose the session control plane to agent callers.
// Visibility and mutation scope are enforced hub-side: non-isolated fleets are
// shared, while isolated fleets apply principal ownership plus the delegated
// same-principal boundary. These handlers add no filtering of their own; they
// decode arguments, call clientop, and render.

const (
	// sessionStreamMaxWait bounds one session_stream call: the drain returns
	// what it has when the deadline passes, even mid-turn.
	sessionStreamMaxWait = 30 * time.Second
	// sessionStreamIdleGrace ends the drain after this much quiet: the replay
	// burst has been delivered and nothing live followed, so the session is
	// idle. Heartbeats do not count as activity.
	sessionStreamIdleGrace = 2 * time.Second
	// sessionStreamTranscriptCap bounds the aggregated transcript; beyond it
	// the drain keeps watching for quiet but stops appending.
	sessionStreamTranscriptCap = 64 << 10
)

// mcpSessionRefArgs is the shared argument shape of the session lifecycle and
// stream tools; their schemas derive from it so a renamed field cannot
// silently desync from what the handler decodes.
type mcpSessionRefArgs struct {
	Session string `json:"session" desc:"Session 名称或 ID"`
	Target  string `json:"target,omitempty" desc:"目标 runtime ID；留空时使用 hub 默认目标"`
}

// mcpSessionSendArgs is the amux.session_send argument shape.
type mcpSessionSendArgs struct {
	Session string `json:"session" desc:"Session 名称或 ID"`
	Target  string `json:"target,omitempty" desc:"目标 runtime ID；留空时使用 hub 默认目标"`
	Text    string `json:"text" desc:"要排入 Session 输入队列的提示词文本"`
}

// mcpSessionQueueCancelArgs is the amux.session_queue_cancel argument shape.
type mcpSessionQueueCancelArgs struct {
	Session string `json:"session" desc:"Session 名称或 ID"`
	Target  string `json:"target,omitempty" desc:"目标 runtime ID；留空时使用 hub 默认目标"`
	QueueID string `json:"queue_id" desc:"要取消的队列输入 ID，由 amux.session_queue_list 返回"`
}

// mcpSessionSetModelArgs is the amux.session_set_model argument shape.
type mcpSessionSetModelArgs struct {
	Session string `json:"session" desc:"Session 名称或 ID"`
	Target  string `json:"target,omitempty" desc:"目标 runtime ID；留空时使用 hub 默认目标"`
	Model   string `json:"model" desc:"要切换到的模型：原生模型名或 provider-backed 模型 ref（如 custom/example-model），无歧义时可用裸上游名（如 example-model）"`
}

// mcpSessionPruneArgs is the amux.session_prune argument shape; target is
// optional and defaults to the hub default target.
type mcpSessionPruneArgs struct {
	Target string `json:"target,omitempty" desc:"目标 runtime ID；留空时使用 hub 默认目标"`
}

func (a mcpSessionRefArgs) ref() clientop.SessionRef {
	return clientop.SessionRef{Name: strings.TrimSpace(a.Session), Target: strings.TrimSpace(a.Target)}
}

// sessionRefForMCP decodes the shared {session, target} arguments and
// validates that a session was named.
func sessionRefForMCP(args json.RawMessage) (clientop.SessionRef, error) {
	var a mcpSessionRefArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return clientop.SessionRef{}, errors.New("invalid arguments: " + err.Error())
	}
	ref := a.ref()
	if ref.Name == "" {
		return clientop.SessionRef{}, errors.New("session is required")
	}
	return ref, nil
}

// mcpSessionRunFieldNames renders clientop validation errors in the JSON-path
// vocabulary of the amux.session_run arguments.
var mcpSessionRunFieldNames = map[clientop.Field]string{
	clientop.FieldSessionName: "name",
	clientop.FieldAgent:       "profile",
}

// newSessionClientForMCP dials the hub for the session tools, which need the
// scoped control plane rather than the local runtime.
var newSessionClientForMCP = func() (pb.AmuxdClient, func() error, error) {
	client, closeFn, err := newAmuxdClient()
	if err != nil {
		return nil, nil, fmt.Errorf("session requires hub: %w", err)
	}
	return client, closeFn, nil
}

// Cheap required-field checks run before dialing so a malformed call fails
// the same way whether or not the hub is reachable; clientop re-validates
// (and owns the rule) before the RPC fires.

// sessionLifecycle is the shared parse-dial-call-render path for the lifecycle
// tools whose hub RPC answers a bare ok (interrupt/sleep/stop).

// sessionQueueList dispatches amux.session_queue_list: the session's pending
// queued inputs, oldest first as the hub returns them. The hub scopes queue
// visibility the same way it scopes the session itself.

// sessionQueueClear dispatches amux.session_queue_clear: drop every queued
// input pending on the session.

// sessionQueueCancel dispatches amux.session_queue_cancel: drop one queued
// input by the id amux.session_queue_list reported for it.

// sessionPrune dispatches amux.session_prune: delete archived sessions from a
// target. This is destructive and distinct from amux.task_prune, which deletes
// terminal task history.

// sessionStream dispatches amux.session_stream: a one-shot Attach with replay
// whose events drain into a transcript until the session goes quiet or a short
// deadline passes. It is not a live tail; the caller re-invokes for newer
// output.

// attachEventStream is the receive side of an Attach stream, abstracted so the
// drain can be tested without a hub.
type attachEventStream interface {
	Recv() (*pb.AttachEvent, error)
}

// drainSessionTranscript reads an Attach stream until the session goes quiet
// (idleGrace without a non-heartbeat event — replay delivered, nothing live
// following), the stream ends, or ctx's deadline passes; every path but a
// stream error returns the transcript aggregated so far. Heartbeats are
// keepalive noise: they neither extend the idle window nor reach the
// transcript. Output payloads aggregate up to maxBytes; past the cap the drain
// keeps watching for quiet but stops appending and reports truncated.

// Buffered so the receive goroutine can always report its terminal error
// and exit after the caller's context is cancelled, even if the drain has
// already returned.

// sessionMCPView is the compact session object the MCP tools return.

// sessionRunMCPView renders a RunResponse (session_run / session_resume /
// session_restart); launch warnings pass through when present.

func amuxTools() []mcp.Tool {
	quotedSchemaText := func(key string) string {
		text := commandCatalog.Text(key)
		quoted, _ := json.Marshal(text.For(i18n.English) + "\n" + text.For(i18n.Chinese))
		return string(quoted)
	}
	specSchema := json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "agent": {"type": "string", "description": "Agent Profile 引用，取自 amux.profile_list 的 profiles[].ref；也可传入先经 amux.profile_get 验证的精确开发版 Profile 引用，例如 builtin/codex、team/push_sre 或 team-task-dev/push_sre；raw Agent Type 字符串仅作为旧调用方兼容入口"},
    "runtime": {"type": "string", "description": %s},
    "model": {"type": "string", "description": %s},
    "reasoning_effort": {"type": "string", "description": %s},
    "extra_args": {"type": "array", "items": {"type": "string"}, "description": %s},
    "task": {"type": "string", "description": "交给 Agent Task 的提示词"},
    "mode": {"type": "string", "enum": ["ephemeral", "persistent"], "description": "ephemeral（默认）只运行一次并销毁；persistent 让 Agent Instance 跨 Task 保持存活，以支持多轮交互"},
    "agent_instance_id": {"type": "string", "description": "持久 Agent Instance 句柄（mode=persistent）；复用 instance_spawn 返回的值调用 instance_followup、instance_send 或 instance_interrupt"},
    "notify_parent": {"type": "boolean", "description": "非阻塞任务完成后是否自动向发起会话送达一条完成通知（实验功能，需开启 steer_foreground）；默认 true，传 false 关闭"},
    "run_as": {"type": "string", "description": "可选 Principal alias 意图；由 amuxd 解析并授权，未指定时 child 继承 parent 的 Execution Principal"},
    "isolation": {"type": "string", "enum": ["process", "private-home"], "description": "Sandbox 隔离模式；Phase 1 为 best-effort trusted mode"},
    "credential_profile": {"type": "string", "description": "控制面 credential profile 名称"},
    "permission_policy": {"type": "string", "enum": ["policy-allow", "escalate-to-main", "deny-all"], "description": %s},
    "workspace": {"type": "object", "properties": {
      "policy": {"type": "string", "enum": ["temp-dir", "git-worktree"]},
      "local_dir": {"type": "string", "description": "目标 runtime 本地的 repo cache 目录；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key"},
      "repo": {"type": "object", "properties": {"kind": {"type": "string"}, "cache_key": {"type": "string", "description": "在 runtime 的 amux repos 目录下解析的 repo cache key；policy=git-worktree 时必须提供 local_dir 或 repo.cache_key"}, "commit": {"type": "string"}}}
    }},
    "timeout_seconds": {"type": "integer", "minimum": 0, "description": "整个 Task 的截止时间（秒），从 Task 被接受时开始计算，并包含准备与 Agent 启动时间；0 或省略时使用 runtime 默认值（30 分钟）"},
    "trusted_mode_ack": {"type": "boolean", "description": "旧版兼容空操作（v0.38）：弱隔离下默认允许注入长期 secret；该字段仅为向后兼容保留"},
    "result_granularity": {"type": "string", "enum": ["summary", "full", "structured-summary"], "description": "summary（默认）仅返回 Agent 摘要；full 在 result.full_transcript 中内联完整脱敏 transcript，最大 64KB，截断时指向 amux task_stream 或 amux task logs；structured-summary 返回 summary 和 result.details_ref（task_id），供按需获取完整 transcript"}
  },
  "required": ["agent", "task"]
}`, quotedSchemaText("mcp.task.runtime"), quotedSchemaText("mcp.task.model"), quotedSchemaText("mcp.task.reasoning_effort"), quotedSchemaText("mcp.task.extra_args"), quotedSchemaText("mcp.task.permission_policy")))
	scheduledTaskInput := clientop.JSONSchema(clientop.ScheduledTaskSpec{})
	for field, key := range map[string]string{"runtime": "mcp.task.runtime", "model": "mcp.schedule.model", "reasoning_effort": "mcp.schedule.reasoning_effort", "extra_args": "mcp.task.extra_args", "permission_policy": "mcp.schedule.permission_policy"} {
		text := commandCatalog.Text(key)
		scheduledTaskInput["properties"].(map[string]any)[field].(map[string]any)["description"] = text.For(i18n.English) + "\n" + text.For(i18n.Chinese)
	}
	scheduledTaskSchemaBytes, err := json.Marshal(scheduledTaskInput)
	if err != nil {
		// A schema that cannot be built means the tool surface would advertise
		// nothing; failing loudly at startup beats serving a broken tool.
		panic("build scheduled task schema: " + err.Error())
	}
	scheduledTaskSchema := json.RawMessage(scheduledTaskSchemaBytes)
	var updateTaskSchemaObj map[string]any
	if err := json.Unmarshal(scheduledTaskSchemaBytes, &updateTaskSchemaObj); err != nil {
		panic("parse scheduled task schema for update: " + err.Error())
	}
	delete(updateTaskSchemaObj, "required")
	updateTaskSchemaBytes, err := json.Marshal(updateTaskSchemaObj)
	if err != nil {
		panic("marshal scheduled task update schema: " + err.Error())
	}
	createScheduleSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Schedule 的可选显示名称"},
    "spec": {"type": "string", "description": "时间规则：every <duration>（支持 d/w）、daily <HH:MM>、at <when>、in <duration>（相对一次性）或 cron <expr>（支持 @daily 等描述符）"},
    "timezone": {"type": "string", "description": "可选 IANA 时区名称（例如 Asia/Shanghai）；省略时使用 Hub 本地时区"},
    "schedule_type": {"type": "string", "enum": ["subtask", "session-prompt"], "description": "subtask（默认）创建临时子 Task；session-prompt 到期向目标 Session Submit 提示词并唤醒 Agent"},
    "session": {"type": "string", "description": "schedule_type=session-prompt 时的目标 session-ref（与 CLI amux schedule create <session-ref> 相同：target/name、Session ID 或名称）；省略时回落到 AMUX_SESSION_ID"},
    "prompt": {"type": "string", "description": "schedule_type=session-prompt 时到期投递到目标 Session 的提示词"},
    "run_mode": {"type": "string", "enum": ["reuse-session", "new-session-per-run"], "description": "仅 session-prompt：reuse-session（默认）复用目标 Session；new-session-per-run 每次按模板新开 Session"},
    "task": ` + string(scheduledTaskSchema) + `
  },
  "required": ["spec"]
}`)
	idSchema := json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}`)
	scheduleIDSchema := json.RawMessage(`{"type":"object","properties":{"schedule_id":{"type":"integer"}},"required":["schedule_id"]}`)
	updateScheduleSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "schedule_id": {"type": "integer"},
    "name": {"type": "string", "description": "Schedule 的新显示名称；省略时保持不变"},
    "spec": {"type": "string", "description": "新的时间规则（every <duration>、daily <HH:MM>、at <when>、in <duration> 或 cron <expr>）；省略时保持不变，修改后会重新计算 next_run_at"},
    "timezone": {"type": "string", "description": "新的 IANA 时区名称（例如 Asia/Shanghai，传空字符串回落 Hub 本地时区）；省略时保持不变"},
    "prompt": {"type": "string", "description": "仅 session-prompt：更新到期投递到目标 Session 的提示词；省略时保持不变"},
    "run_mode": {"type": "string", "enum": ["reuse-session", "new-session-per-run"], "description": "仅 session-prompt：更新执行模式（reuse-session 或 new-session-per-run）；省略时保持不变"},
    "task": ` + string(updateTaskSchemaBytes) + `
  },
  "required": ["schedule_id"]
}`)
	getScheduleSchema := json.RawMessage(`{"type":"object","properties":{"schedule_id":{"type":"integer"},"include_runs":{"type":"boolean","description":"是否包含 Schedule 运行历史；使用 runs 中的 task_id 调用 task_get 或 task_stream 获取结果"}},"required":["schedule_id"]}`)
	setScheduleEnabledSchema := json.RawMessage(`{"type":"object","properties":{"schedule_id":{"type":"integer"},"enabled":{"type":"boolean"}},"required":["schedule_id","enabled"]}`)
	streamSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "task_id": {"type": "string"},
    "from_seq": {"type": "integer", "description": "从该事件序号继续读取；传入上一次调用返回的 next_seq"}
  },
  "required": ["task_id"]
}`)
	respondSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "task_id": {"type": "string"},
    "permission_id": {"type": "string"},
    "allow": {"type": "boolean"},
    "option_id": {"type": "string"},
    "reason": {"type": "string"}
  },
  "required": ["task_id", "allow"]
}`)
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	taskPruneSchema := json.RawMessage(`{"type":"object","properties":{"dry_run":{"type":"boolean"},"older_than_seconds":{"type":"integer","minimum":0},"statuses":{"type":"array","items":{"type":"string"}}}}`)
	// Agent-level (Phase 3) tool schemas. instance_spawn / instance_followup reuse the
	// spec shape (mode=persistent); the others address an instance by id.
	spawnAgentSchema := json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "agent": {"type": "string", "description": "Agent Profile 引用，取自 amux.profile_list 的 profiles[].ref"},
    "runtime": {"type": "string", "description": %s},
    "model": {"type": "string", "description": %s},
    "reasoning_effort": {"type": "string", "description": %s},
    "extra_args": {"type": "array", "items": {"type": "string"}, "description": %s},
    "task": {"type": "string", "description": "交给 Agent 的初始 Task 提示词"},
    "run_as": {"type": "string", "description": "可选 Principal alias 意图；省略时使用 parent Execution Principal 创建实例"},
    "isolation": {"type": "string", "enum": ["process", "private-home"]},
    "credential_profile": {"type": "string"},
    "permission_policy": {"type": "string", "enum": ["policy-allow", "escalate-to-main", "deny-all"], "description": %s},
    "workspace": {"type": "object", "properties": {"policy": {"type": "string", "enum": ["temp-dir", "git-worktree"]}, "local_dir": {"type": "string"}, "repo": {"type": "object", "properties": {"kind": {"type": "string"}, "cache_key": {"type": "string"}, "commit": {"type": "string"}}}}},
	"timeout_seconds": {"type": "integer", "minimum": 0, "description": "整个 Task 从接受开始计算的截止时间（秒）；0 或省略时使用 runtime 默认值（30 分钟）"},
    "trusted_mode_ack": {"type": "boolean"},
    "agent_instance_id": {"type": "string", "description": "可选的显式 Instance 句柄；省略时生成并返回新的 agent-<uuid> 句柄"},
    "notify_parent": {"type": "boolean", "description": "非阻塞任务完成后是否自动向发起会话送达一条完成通知（实验功能，需开启 steer_foreground）；默认 true，传 false 关闭"},
    "result_granularity": {"type": "string", "enum": ["summary", "full", "structured-summary"], "description": "summary（默认）仅返回 Agent 摘要；full 在 result.full_transcript 中内联完整脱敏 transcript，超限时截断并指向 amux task_stream 或 amux task logs；structured-summary 返回 summary 和 result.details_ref（task_id），供按需获取完整 transcript"}
  },
  "required": ["agent", "task"]
}`, quotedSchemaText("mcp.task.runtime"), quotedSchemaText("mcp.instance.model"), quotedSchemaText("mcp.instance.reasoning_effort"), quotedSchemaText("mcp.task.extra_args"), quotedSchemaText("mcp.task.permission_policy")))
	followupTaskSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
	"agent_instance_id": {"type": "string", "description": "要唤醒并接收 Task 的持久 Agent Instance，取自 instance_spawn 的返回值"},
    "notify_parent": {"type": "boolean", "description": "非阻塞任务完成后是否自动向发起会话送达一条完成通知（实验功能，需开启 steer_foreground）；默认 true，传 false 关闭"},
	"task": {"type": "string", "description": "触发下一轮的新 Task 提示词"},
	"timeout_seconds": {"type": "integer", "minimum": 0, "description": "整个 Task 从接受开始计算的截止时间（秒）；0 或省略时使用 runtime 默认值（30 分钟）"},
    "trusted_mode_ack": {"type": "boolean"},
	"result_granularity": {"type": "string", "enum": ["summary", "full", "structured-summary"], "description": "summary（默认）仅返回 Agent 摘要；full 在 result.full_transcript 中内联完整脱敏 transcript，超限时截断并指向 amux task_stream 或 amux task logs；structured-summary 返回 summary 和 result.details_ref（task_id），供按需获取完整 transcript"}
  },
  "required": ["agent_instance_id", "task"]
}`)
	sendMessageSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "agent_instance_id": {"type": "string"},
	"text": {"type": "string", "description": "要排队的消息；Agent 唤醒后会在下一条 Task 提示词之前收到"},
	"source": {"type": "string", "description": "队列输入的可选来源标签"}
  },
  "required": ["agent_instance_id", "text"]
}`)
	steerAgentSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "agent_instance_id": {"type": "string"},
    "text": {"type": "string", "description": "要注入的纠正或补充；Agent 会判断是调整当前工作还是追加要求，并保留已完成进度"}
  },
  "required": ["agent_instance_id", "text"]
}`)
	waitAgentSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "agent_instance_id": {"type": "string"},
	"timeout_seconds": {"type": "integer", "description": "等待当前 turn 进入 idle 或 archived 的最长秒数；默认 30 秒"}
  },
  "required": ["agent_instance_id"]
}`)
	listAgentsSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
	"owner_session_id": {"type": "string", "description": "可选过滤条件：拥有这些 Instance 的 parent Session ID"},
    "status": {"type": "string", "enum": ["active", "idle", "archived"]}
  }
}`)
	interruptAgentSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
    "agent_instance_id": {"type": "string"}
  },
  "required": ["agent_instance_id"]
}`)
	getAgentProfileSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
	"ref": {"type": "string", "description": "精确的 Agent Profile 引用；需要时可包含显式开发版 namespace"}
  },
	  "required": ["ref"]
	}`)
	profileListSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
	"cursor": {"type": "string", "description": "上一页 profile_list 返回的不透明续页游标"},
	"namespace": {"type": "string", "description": "可选的精确 Pack namespace 过滤条件，例如 expert-agents；翻页时必须保持不变"}
  }
	}`)
	localExpertReadSchema := json.RawMessage(`{
	  "type": "object",
	  "properties": {
	    "operation": {"type": "string", "enum": ["create", "fork", "update"]},
	    "target_ref": {"type": "string", "description": "精确的 local/<name> 目标引用"},
	    "source_ref": {"type": "string", "description": "仅 fork 操作必填；精确的 builtin/<name> 引用"}
	  },
	  "required": ["operation", "target_ref"]
	}`)
	localExpertChangeSchema := json.RawMessage(`{
	  "type": "object",
	  "properties": {
	    "mode": {"type": "string", "enum": ["preview", "publish", "cleanup"]},
	    "operation": {"type": "string", "enum": ["create", "fork", "update"], "description": "preview/publish 必填；cleanup 忽略"},
	    "target_ref": {"type": "string", "description": "精确的 local/<name> 目标引用"},
	    "source_ref": {"type": "string", "description": "仅 fork 操作必填；精确的 builtin/<name> 引用"},
	    "base_revision": {"type": "string", "description": "preview/publish 必填；local_expert_read 返回的 base_revision，publish 必须原样携带，不能省略"},
	    "patch": {"type": "string", "description": "preview/publish 必填；基于 local_expert_read 返回精确基线的 unified text Git patch"},
	    "preview_receipt": {"type": "string", "description": "publish/cleanup 模式必填；preview 返回的不透明 receipt"},
	    "prepare_evaluation": {"type": "boolean", "default": false, "description": "仅 preview 使用，默认 false。只有用户对本次候选明确要求评测时才可设为 true；只要 Profile/Skill 本身或测试 case 可能产生写入、通知或其它副作用，即使 case 看起来只读，也必须先展示测试计划并取得用户后续确认。用于为相同 preview tree 准备隐藏的短期候选 Profile，供 task_run 做一次 A/B 评测；patch 变化或评测完成后重新评测需要新的明确要求和必要的副作用确认；评测确认不授权 publish"}
	  },
	  "required": ["mode"],
	  "allOf": [
	    {
	      "if": {"properties": {"mode": {"const": "preview"}}, "required": ["mode"]},
	      "then": {"required": ["operation", "target_ref", "base_revision", "patch"]}
	    },
	    {
	      "if": {"properties": {"mode": {"const": "publish"}}, "required": ["mode"]},
	      "then": {"required": ["operation", "target_ref", "base_revision", "patch", "preview_receipt"]}
	    },
	    {
	      "if": {"properties": {"mode": {"const": "cleanup"}}, "required": ["mode"]},
	      "then": {"required": ["preview_receipt"]}
	    },
	    {
	      "if": {
	        "properties": {"mode": {"enum": ["preview", "publish"]}, "operation": {"const": "fork"}},
	        "required": ["mode", "operation"]
	      },
	      "then": {"required": ["source_ref"]}
	    }
	  ]
	}`)
	scheduleListSchema := json.RawMessage(`{
	  "type": "object",
	  "properties": {
	    "cursor": {"type": "string", "description": "上一页 schedule_list 返回的不透明续页游标"}
	  }
	}`)
	resultContinueSchema := json.RawMessage(`{
  "type": "object",
  "properties": {
	"cursor": {"type": "string", "description": "分页 MCP 结果返回的 next_cursor"}
  },
  "required": ["cursor"]
}`)
	// Session tool schemas derive from the argument/request structs so a field
	// added there cannot be silently missing from the tool surface.
	sessionRunInput := clientop.JSONSchema(clientop.SessionRunRequest{})
	for field, key := range map[string]string{"runtime": "mcp.session.runtime", "model": "mcp.session.model", "args": "mcp.session.args"} {
		text := commandCatalog.Text(key)
		sessionRunInput["properties"].(map[string]any)[field].(map[string]any)["description"] = text.For(i18n.English) + "\n" + text.For(i18n.Chinese)
	}
	sessionRunSchemaBytes, err := json.Marshal(sessionRunInput)
	if err != nil {
		panic("build session run schema: " + err.Error())
	}
	sessionRefSchemaBytes, err := json.Marshal(clientop.JSONSchema(mcpSessionRefArgs{}))
	if err != nil {
		panic("build session ref schema: " + err.Error())
	}
	sessionSendSchemaBytes, err := json.Marshal(clientop.JSONSchema(mcpSessionSendArgs{}))
	if err != nil {
		panic("build session send schema: " + err.Error())
	}
	sessionQueueCancelSchemaBytes, err := json.Marshal(clientop.JSONSchema(mcpSessionQueueCancelArgs{}))
	if err != nil {
		panic("build session queue cancel schema: " + err.Error())
	}
	sessionSetModelSchemaBytes, err := json.Marshal(clientop.JSONSchema(mcpSessionSetModelArgs{}))
	if err != nil {
		panic("build session set model schema: " + err.Error())
	}
	sessionPruneSchemaBytes, err := json.Marshal(clientop.JSONSchema(mcpSessionPruneArgs{}))
	if err != nil {
		panic("build session prune schema: " + err.Error())
	}
	sessionRunSchema := json.RawMessage(sessionRunSchemaBytes)
	sessionRefSchema := json.RawMessage(sessionRefSchemaBytes)
	sessionSendSchema := json.RawMessage(sessionSendSchemaBytes)
	sessionQueueCancelSchema := json.RawMessage(sessionQueueCancelSchemaBytes)
	sessionSetModelSchema := json.RawMessage(sessionSetModelSchemaBytes)
	sessionPruneSchema := json.RawMessage(sessionPruneSchemaBytes)
	tools := []mcp.Tool{
		{Name: "amux.profile_list", Description: "分页列出非开发版 Agent Profile；用 profiles[].ref 提交任务，详情用 profile_get。沿 next_cursor 读取至 has_more=false。", InputSchema: profileListSchema},
		{Name: "amux.profile_get", Description: "按精确 ref 获取 Agent Profile 完整详情，支持开发版 namespace。", InputSchema: getAgentProfileSchema},
		{Name: "amux.local_expert_read", Description: "读取 managed local 专家的精确编辑基线；仅当前飞书 Bot owner turn 可用，不写入。", InputSchema: localExpertReadSchema},
		{Name: "amux.local_expert_change", Description: "预览、发布或清理 managed local 专家 patch。publish 仅限后续明确确认的 owner turn，并须原样提交相同 patch 与 receipt；评测确认不等于发布确认。", InputSchema: localExpertChangeSchema},
		{Name: "amux.result_continue", Description: "续读分页结果；按序拼接 content_chunk，直到 complete=true。游标仅当前 MCP 进程短期有效。", InputSchema: resultContinueSchema},
		{Name: "amux.task_submit", Description: "异步提交 Agent Task，立即返回 task_id。agent 优先使用 profile_list 的 ref；开发版先经 profile_get 验证。", InputSchema: specSchema},
		{Name: "amux.task_run", Description: "阻塞运行短 Agent Task；仅支持 policy-allow 或 deny-all，escalate-to-main 会死锁。", InputSchema: specSchema},
		{Name: "amux.task_get", Description: "获取 Agent Task 状态、结构化结果和 session_ref。", InputSchema: idSchema},
		{Name: "amux.task_stream", Description: "从 from_seq 长轮询 Agent Task 事件，返回 next_seq 供无损续读。", InputSchema: streamSchema},
		{Name: "amux.task_cancel", Description: "取消 Agent Task，并中断或终止其 child Session。", InputSchema: idSchema},
		{Name: "amux.task_respond_permission", Description: "应答 escalate-to-main Task 上抛的权限请求。", InputSchema: respondSchema},
		{Name: "amux.task_list", Description: "列出调用方可见的 Agent Task 及状态、session_ref 和可用结果。", InputSchema: empty},
		{Name: "amux.task_prune", Description: "清理终态 Task 历史；隔离集群仅 enrollment operator 可用，非隔离集群允许 delegated agent。", InputSchema: taskPruneSchema},
		{Name: "amux.schedule_create", Description: "创建 hub 定时规则。默认 schedule_type=subtask（临时子 Task，结果用 task_get/task_stream，不允许 escalate-to-main）；schedule_type=session-prompt 时向目标 Session（显式 session 参数为 session-ref，或回落 AMUX_SESSION_ID）Submit 提示词并唤醒 Agent。", InputSchema: createScheduleSchema},
		{Name: "amux.schedule_list", Description: "分页列出 Schedule；沿 next_cursor 读取至 has_more=false。", InputSchema: scheduleListSchema},
		{Name: "amux.schedule_get", Description: "获取 Schedule；include_runs=true 时包含运行历史及 task_id。", InputSchema: getScheduleSchema},
		{Name: "amux.schedule_delete", Description: "删除 Schedule；既有运行对应的 Task 结果仍可读取。", InputSchema: scheduleIDSchema},
		{Name: "amux.schedule_update", Description: "原地更新 Schedule 的名称、时间规则、session-prompt 的 prompt/run_mode 或 subtask 的局部 Task 字段；省略字段保持不变，不允许 escalate-to-main。", InputSchema: updateScheduleSchema},
		{Name: "amux.schedule_set_enabled", Description: "启用或停用 Schedule。", InputSchema: setScheduleEnabledSchema},
		{Name: "amux.schedule_trigger", Description: "立即手动触发一次 Schedule（不影响其启停状态与下次计划触发时间）。", InputSchema: scheduleIDSchema},
		{Name: "amux.instance_spawn", Description: "非阻塞创建持久 Agent Instance（后台实例），返回稳定的 agent_instance_id。适用于你判断需要较长时间执行或大量工具调用的任务；短问题直接回答，不要委派。prompt 要自包含：写清目标、约束、交付物，并要求后台 Agent 结束时给出一段简洁的、面向用户的结论（放在最终回复末尾，可用「结论：」开头），不要把工作过程当作结论。调用后简短告诉用户已在后台开始，并继续响应用户的新消息；记住 agent_instance_id 与任务的对应关系，但对用户用自然语言描述任务，不要展示原始 id。任务结束时系统会自动向你送达一条「后台任务完成」通知，无需轮询或反复调用 task_get。", InputSchema: spawnAgentSchema},
		{Name: "amux.instance_send", Description: "向持久 Agent mailbox 入队消息但不唤醒，不启动新 turn。", InputSchema: sendMessageSchema},
		{Name: "amux.instance_steer", Description: "向持久 Agent 正在执行的 turn 注入纠正或补充，不中断当前工作，适用于用户纠正或补充某个后台任务时。实例无可注入的 turn 时回落到 mailbox（delivery=mailbox），此时改用 instance_followup 继续该任务；mailbox 中的纠正会先送达，prompt 里不要重复。", InputSchema: steerAgentSchema},
		{Name: "amux.instance_followup", Description: "向持久 Agent 交付任务并立即触发新 turn，保留 provider 对话上下文；适用于后台任务完成后继续、或失败后由你决定重试。实例上次失败后遗留的子会话会被复用；若实例已无法使用，会返回错误并建议改用 instance_spawn 新建实例。", InputSchema: followupTaskSchema},
		{Name: "amux.instance_wait", Description: "等待持久 Agent 当前 turn 结束或超时；结果仍用 task_get/task_stream 获取。前台会话一般不需要调用：后台任务结束时会自动送达完成通知，等待会阻塞你对用户新消息的响应。", InputSchema: waitAgentSchema},
		{Name: "amux.instance_list", Description: "列出持久 Agent Instance 状态、child Session 状态和 mailbox 深度。", InputSchema: listAgentsSchema},
		{Name: "amux.instance_interrupt", Description: "中断持久 Agent 当前 turn。用于用户要求停止某个后台任务时；中断后简要告知用户，之后可用 instance_followup 继续。", InputSchema: interruptAgentSchema},
		{Name: "amux.session_list", Description: "按 runtime 分组列出调用方可见的 Session；创建 Session 时不要预先调用，先省略 target 调用 session_run，仅在其报告多个 target 后用本工具选择。", InputSchema: empty},
		{Name: "amux.session_run", Description: "用 Agent Profile 创建 Session；profile 和 name 必填。未指定 target 时先直接调用：唯一连接 target 会自动选择，多个 target 会在创建前返回明确错误。", InputSchema: sessionRunSchema},
		{Name: "amux.session_send", Description: "向 Session 输入队列提交提示词；名称有歧义时用 target 消歧。", InputSchema: sessionSendSchema},
		{Name: "amux.session_interrupt", Description: "中断 Session 当前 turn。", InputSchema: sessionRefSchema},
		{Name: "amux.session_sleep", Description: "暂停 Session Agent 进程并保持可恢复。", InputSchema: sessionRefSchema},
		{Name: "amux.session_resume", Description: "唤醒 sleeping Session。", InputSchema: sessionRefSchema},
		{Name: "amux.session_stop", Description: "归档 Session；Task 历史使用 task_prune 清理。", InputSchema: sessionRefSchema},
		{Name: "amux.session_restart", Description: "原地重启 Session Agent 进程。", InputSchema: sessionRefSchema},
		{Name: "amux.session_set_model", Description: "切换 Session 模型并保留对话历史；支持原生模型与 provider-backed 模型 ref（如 custom/example-model），无歧义时也可用裸上游名（如 example-model）；满足热条件时不重启进程，否则原地重启并经 provider resume/handoff 保留对话，跨 provider 切换会开新对话并注入 handoff 历史；Task/Instance 持有的 Session 不支持。", InputSchema: sessionSetModelSchema},
		{Name: "amux.session_stream", Description: "一次性 replay 并排空 Session 最近输出，返回有界聚合文本；不是实时 tail。", InputSchema: sessionRefSchema},
		{Name: "amux.session_queue_list", Description: "按最早优先列出 Session 待处理输入。", InputSchema: sessionRefSchema},
		{Name: "amux.session_queue_clear", Description: "丢弃 Session 全部待处理输入。", InputSchema: sessionRefSchema},
		{Name: "amux.session_queue_cancel", Description: "按 queue_id 丢弃 Session 队列中的一条输入。", InputSchema: sessionQueueCancelSchema},
		{Name: "amux.session_prune", Description: "破坏性操作：永久删除已归档 Session；隔离集群仅 enrollment operator 可用，非隔离集群允许 delegated agent；Task 历史使用 task_prune。", InputSchema: sessionPruneSchema},
	}
	tools = append(tools, coordinationTools()...)
	tools = append(tools, feishuMessageTool())
	tools = append(tools, memoryTools()...)
	return tools
}
