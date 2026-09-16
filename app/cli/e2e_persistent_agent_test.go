package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPersistentAgentE2E drives the full Phase 3 persistent-agent chain over a
// real `amux mcp` stdio JSON-RPC session against a hub + runtime started on an
// isolated temp HOME:
//
//	instance_spawn → instance_list → instance_send(QueueOnly) → instance_followup →
//	instance_interrupt → instance_wait
//
// A fake claude-sdk sidecar emits a stable session.id so the persistent child
// records an AgentSessionID and the runner puts it to sleep (rather than the
// no-AgentSessionID teardown fallback), exercising the B2 provider-resume path
// on instance_followup.
func TestPersistentAgentE2E(t *testing.T) {
	requireLongTest(t)
	t.Parallel()

	e := newCLIE2E(t)
	e.config(t, "runtime.id", "e2e-target")
	e.config(t, "runtime.name", "e2e-target")
	e.writeFakeClaudePersistentSidecar(t)

	e.startSystemOnFreePort(t)
	e.waitForCommandOutput(t, "hub target", "e2e-target", "system", "target", "list")

	// Start `amux mcp` as a stdio JSON-RPC subprocess and drive it.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mcpCmd := exec.CommandContext(ctx, e.amux, "mcp")
	mcpCmd.Env = e.env
	mcpCmd.Dir = e.repoRoot
	stdin, err := mcpCmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := mcpCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	mcpCmd.Stderr = os.Stderr
	if err := mcpCmd.Start(); err != nil {
		t.Fatalf("start amux mcp: %v", err)
	}
	defer func() {
		if err := waitForCommandExitOrKill(mcpCmd, stdin.Close, 2*time.Second); err != nil {
			t.Logf("cleanup amux mcp: %v", err)
		}
	}()

	client := newMCPClient(t, stdout, stdin)

	// initialize + initialized notification.
	initResp := client.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e-test", "version": "dev"},
	})
	if errStr := mcpError(initResp); errStr != "" {
		t.Fatalf("initialize error: %s", errStr)
	}
	client.notify(t, "notifications/initialized", map[string]any{})

	// tools/list must include the 6 agent-level tools.
	listResp := client.call(t, "tools/list", map[string]any{})
	if errStr := mcpError(listResp); errStr != "" {
		t.Fatalf("tools/list error: %s", errStr)
	}
	toolNames := toolNamesFromList(t, listResp)
	for _, want := range []string{
		"amux.instance_spawn", "amux.instance_send", "amux.instance_followup",
		"amux.instance_wait", "amux.instance_list", "amux.instance_interrupt",
	} {
		if !contains(toolNames, want) {
			t.Fatalf("tools/list missing %q; got %v", want, toolNames)
		}
	}

	// instance_spawn: persistent, agent=claude, returns agent_instance_id + task_id.
	spawnArgs := map[string]any{
		"agent": "claude",
		"task":  "remember the number 42",
		"mode":  "persistent",
	}
	spawnResp := client.call(t, "tools/call", map[string]any{
		"name":      "amux.instance_spawn",
		"arguments": spawnArgs,
	})
	if errStr := mcpError(spawnResp); errStr != "" {
		t.Fatalf("instance_spawn error: %s", errStr)
	}
	spawnOut := toolResultText(t, spawnResp)
	var spawnView map[string]any
	if err := json.Unmarshal([]byte(spawnOut), &spawnView); err != nil {
		t.Fatalf("instance_spawn result %q is not JSON: %v", spawnOut, err)
	}
	instanceID, _ := spawnView["agent_instance_id"].(string)
	if instanceID == "" {
		t.Fatalf("instance_spawn returned empty agent_instance_id: %s", spawnOut)
	}
	if !strings.HasPrefix(instanceID, "agent-") {
		t.Fatalf("instance_spawn agent_instance_id = %q, want agent- prefix", instanceID)
	}
	taskID, _ := spawnView["task_id"].(string)
	if taskID == "" {
		t.Fatalf("instance_spawn returned empty task_id: %s", spawnOut)
	}
	t.Logf("instance_spawn -> instance=%s task=%s status=%v", instanceID, taskID, spawnView["status"])

	// Wait for the spawned task to reach terminal + the child to sleep. The
	// fake sidecar emits turn_done immediately, so the runner judges and sleeps.
	e.waitForTaskTerminal(t, "spawned task", taskID)

	// instance_list: the instance should appear.
	listAgentsOut := e.runMCPTool(t, client, "amux.instance_list", map[string]any{})
	if !strings.Contains(listAgentsOut, instanceID) {
		t.Fatalf("instance_list missing instance %q: %s", instanceID, listAgentsOut)
	}
	t.Logf("instance_list -> %s", listAgentsOut)

	// instance_send (QueueOnly): enqueue without waking; pending_messages grows.
	sendOut := e.runMCPTool(t, client, "amux.instance_send", map[string]any{
		"agent_instance_id": instanceID,
		"text":              "follow up: what was the number?",
		"source":            "e2e",
	})
	var sendView map[string]any
	if err := json.Unmarshal([]byte(sendOut), &sendView); err != nil {
		t.Fatalf("instance_send result %q is not JSON: %v", sendOut, err)
	}
	if queued, _ := sendView["queued"].(bool); !queued {
		t.Fatalf("instance_send not queued: %s", sendOut)
	}
	if pm, _ := sendView["pending_messages"].(float64); pm < 1 {
		t.Fatalf("instance_send pending_messages = %v, want >= 1", sendView["pending_messages"])
	}
	t.Logf("instance_send -> %s", sendOut)

	// instance_followup (TriggerTurn): resume the sleeping instance and trigger a new
	// turn. This sticky-routes to the same target and resumes the provider
	// session; the mailbox message is drained ahead of the new prompt.
	followupArgs := map[string]any{
		"task":              "answer the follow up",
		"agent_instance_id": instanceID,
	}
	followupOut := e.runMCPTool(t, client, "amux.instance_followup", followupArgs)
	var followupView map[string]any
	if err := json.Unmarshal([]byte(followupOut), &followupView); err != nil {
		t.Fatalf("instance_followup result %q is not JSON: %v", followupOut, err)
	}
	if fid, _ := followupView["agent_instance_id"].(string); fid != instanceID {
		t.Fatalf("instance_followup agent_instance_id = %q, want %q", fid, instanceID)
	}
	followupTaskID, _ := followupView["task_id"].(string)
	if followupTaskID == "" {
		t.Fatalf("instance_followup returned empty task_id: %s", followupOut)
	}
	t.Logf("instance_followup -> task=%s", followupTaskID)

	// Wait for the followup task to reach terminal again (child sleeps again).
	e.waitForTaskTerminal(t, "followup task", followupTaskID)

	// instance_interrupt on an idle (sleeping) instance: the instance is not running
	// a turn, so the interrupt should report not-interrupted (false) rather than
	// error. This asserts the agent-level interrupt path is wired end-to-end.
	interruptOut := e.runMCPTool(t, client, "amux.instance_interrupt", map[string]any{
		"agent_instance_id": instanceID,
	})
	t.Logf("instance_interrupt (idle) -> %s", interruptOut)
	if !strings.Contains(interruptOut, instanceID) {
		t.Fatalf("instance_interrupt result missing instance %q: %s", instanceID, interruptOut)
	}

	// instance_wait: with the instance already idle (sleeping), join should return
	// immediately as completed rather than waiting for the timeout.
	waitOut := e.runMCPTool(t, client, "amux.instance_wait", map[string]any{
		"agent_instance_id": instanceID,
		"timeout_seconds":   1,
	})
	t.Logf("instance_wait -> %s", waitOut)
	var waitView map[string]any
	if err := json.Unmarshal([]byte(waitOut), &waitView); err != nil {
		t.Fatalf("instance_wait result %q is not JSON: %v", waitOut, err)
	}
	if waitView["agent_instance_id"] != instanceID || waitView["completed"] != true || waitView["timed_out"] != false {
		t.Fatalf("instance_wait result = %+v, want instance=%q completed=true timed_out=false", waitView, instanceID)
	}
}

// writeFakeClaudePersistentSidecar writes a fake amux-claude-sdk-sidecar that:
//   - responds to initialize / session.start / turn.start / interrupt / shutdown;
//   - emits a stable session.id notification (a dedicated JSON-RPC method, not
//     an event) right after session.start so the persistent child records an
//     AgentSessionID and the runner sleeps it (exercising the B2 resume path on
//     instance_followup);
//   - on turn.start emits a native result event (provider result.text) followed
//     by turn_done, so the runner judges the task succeeded with content rather
//     than failed:missing_result;
//   - echoes the resume id back via session.id so resume is observable.
func (e *cliE2E) writeFakeClaudePersistentSidecar(t *testing.T) {
	t.Helper()
	path := filepath.Join(e.fakeBin, "amux-claude-sdk-sidecar")
	body := `#!/usr/bin/env python3
import json, sys, os

resume_holder = {"resume": ""}

def emit(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()

def session_id_for(req_id, resume):
    # Stable id per (process) side; when resuming, echo the resume id so the
    # recorded AgentSessionID matches across the sleeping -> resume boundary.
    return resume if resume else ("sess-" + str(os.getpid()))

for line in sys.stdin:
    try:
        msg = json.loads(line)
    except Exception:
        continue
    mid = msg.get("id")
    method = msg.get("method")
    params = msg.get("params") or {}
    if method == "initialize":
        emit({"jsonrpc":"2.0","id":mid,"result":{"ok":True}})
    elif method == "session.start":
        # Remember the resume id; the session.id notification is emitted on the
        # first turn.start (after the amuxlet has wired the proc's event sink via
        # SetProc, which happens only after session.start returns). Emitting it
        # during session.start would race the sink and the AgentSessionID would
        # be dropped, defeating the persistent sleep path.
        resume_holder["resume"] = (params or {}).get("resume") or ""
        emit({"jsonrpc":"2.0","id":mid,"result":{"ok":True}})
    elif method == "turn.start":
        text = (params or {}).get("text") or ""
        sid = session_id_for(mid, resume_holder["resume"])
        emit({"jsonrpc":"2.0","method":"session.id","params":{"sessionId":sid}})
        emit({"jsonrpc":"2.0","method":"event","params":{"event":{"provider":"claude","type":"result","result":{"text":"ack: " + text}}}})
        emit({"jsonrpc":"2.0","method":"event","params":{"event":{"provider":"claude","type":"turn_done","state":{"state":"turn_done","stop_reason":"end_turn"}}}})
        emit({"jsonrpc":"2.0","id":mid,"result":{"state":"turn_done"}})
    elif method in ("interrupt","permission.respond"):
        emit({"jsonrpc":"2.0","id":mid,"result":{"ok":True}})
    elif method == "shutdown":
        emit({"jsonrpc":"2.0","id":mid,"result":{"ok":True}})
        break
`
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

// waitForTaskTerminal polls `amux task show` until the task reaches a terminal
// status (succeeded/failed/cancelled/timed_out), or fails with a dump of the
// task logs + last show output.
func (e *cliE2E) waitForTaskTerminal(t *testing.T, label, taskID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, _ := e.runAllowError("task", "show", taskID)
		last = out
		if strings.Contains(out, "succeeded") || strings.Contains(out, "failed") ||
			strings.Contains(out, "cancelled") || strings.Contains(out, "timed_out") {
			if !strings.Contains(out, "succeeded") {
				t.Fatalf("%s did not succeed:\n%s\n--- logs ---\n%s", label, out, e.taskLogs(t, taskID))
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to reach terminal; last show:\n%s\n--- logs ---\n%s",
		label, last, e.taskLogs(t, taskID))
}

func (e *cliE2E) taskLogs(t *testing.T, taskID string) string {
	t.Helper()
	out, err := e.runAllowError("task", "logs", taskID)
	if err != nil {
		return "(logs error: " + err.Error() + ")"
	}
	// Also dump the JSON form for the full result/error fields.
	jsonOut, _ := e.runAllowError("task", "show", taskID, "--json")
	return out + "\n--- json show ---\n" + jsonOut
}

// runMCPTool calls a tools/call over the MCP client and returns the text result.
func (e *cliE2E) runMCPTool(t *testing.T, c *mcpClient, name string, args map[string]any) string {
	t.Helper()
	resp := c.call(t, "tools/call", map[string]any{"name": name, "arguments": args})
	if errStr := mcpError(resp); errStr != "" {
		t.Fatalf("MCP %s error: %s", name, errStr)
	}
	return toolResultText(t, resp)
}

// mcpClient is a minimal line-based JSON-RPC client for `amux mcp`.
type mcpClient struct {
	t      *testing.T
	out    *bufio.Reader
	in     io.WriteCloser
	nextID int
}

func newMCPClient(t *testing.T, stdout io.ReadCloser, stdin io.WriteCloser) *mcpClient {
	return &mcpClient{t: t, out: bufio.NewReader(stdout), in: stdin}
}

func (c *mcpClient) call(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	c.nextID++
	id := c.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		t.Fatalf("mcp write %s: %v", method, err)
	}
	line, err := c.out.ReadString('\n')
	if err != nil {
		t.Fatalf("mcp read %s: %v", method, err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		t.Fatalf("mcp %s response not JSON: %v\nraw: %s", method, err, line)
	}
	return resp
}

func (c *mcpClient) notify(t *testing.T, method string, params any) {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		t.Fatalf("mcp notify %s: %v", method, err)
	}
}

func mcpError(resp map[string]any) string {
	if e, ok := resp["error"].(map[string]any); ok {
		if msg, _ := e["message"].(string); msg != "" {
			return msg
		}
		return "rpc error"
	}
	return ""
}

func toolResultText(t *testing.T, resp map[string]any) string {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("MCP response has no result: %v", resp)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		// Some tools return a bare text; fall back to marshalling result.
		b, _ := json.Marshal(result)
		return string(b)
	}
	first, _ := content[0].(map[string]any)
	if text, _ := first["text"].(string); text != "" {
		return text
	}
	b, _ := json.Marshal(result)
	return string(b)
}

func toolNamesFromList(t *testing.T, resp map[string]any) []string {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list has no result: %v", resp)
	}
	tools, _ := result["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tc := range tools {
		tm, _ := tc.(map[string]any)
		if name, _ := tm["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
