package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/amuxhome/amuxhometest"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/system"
)

type cliE2EBinaryCache struct {
	once         sync.Once
	dir          string
	ownedTempDir bool
	err          error
}

func (c *cliE2EBinaryCache) Get(build func() (string, bool, error)) (string, error) {
	c.once.Do(func() {
		c.dir, c.ownedTempDir, c.err = build()
	})
	return c.dir, c.err
}

var sharedCLIE2EBinaries cliE2EBinaryCache

const longTestsEnv = "AMUX_RUN_LONG_TESTS"

func longTestsEnabled(short bool, optIn string) bool {
	return !short && optIn == "1"
}

func requireLongTest(t *testing.T) {
	t.Helper()
	if !longTestsEnabled(testing.Short(), os.Getenv(longTestsEnv)) {
		t.Skip("skipping long-running test; set AMUX_RUN_LONG_TESTS=1")
	}
}

func TestMain(m *testing.M) {
	installManagedContractRuntime = func(context.Context) (agentprofile.ManagedContractRuntime, error) {
		return agentprofile.ManagedContractRuntime{Version: agentprofile.ManagedContractRuntimeVersion, Root: agentprofile.DefaultManagedContractRuntimeRoot(), Executable: agentprofile.DefaultManagedContractExecutablePath()}, nil
	}
	prepareManagedLocalLibrary = func(context.Context) error { return nil }
	// Keep tests away from the caller's real amux home even when the test
	// binary inherits AMUX_HOME from an amux-managed Agent Session.
	code := amuxhometest.Run(m)
	if sharedCLIE2EBinaries.ownedTempDir && sharedCLIE2EBinaries.dir != "" {
		_ = os.RemoveAll(sharedCLIE2EBinaries.dir)
	}
	os.Exit(code)
}

func TestHubCLICommandsE2E(t *testing.T) {
	requireLongTest(t)
	t.Parallel()

	e := newCLIE2E(t)
	// Exercise ordinary CLI identity in this temporary installation even when
	// the test runner itself is an Agent inside another managed conversation.
	e.env = append(e.env, "AMUX_SESSION_ID=", "AMUX_MEMORY_BINDING=", "AMUX_ACTOR_TOKEN=", "AMUX_ACTOR_TOKEN_FILE=")
	e.writeFakeClaude(t)
	e.writeFakeClaudeSidecar(t)

	setupOut := e.run(t, "setup", "--no-start")
	setupOperation := operationIDFromOutput(t, setupOut)
	e.config(t, "runtime.id", "e2e-target")
	e.config(t, "runtime.name", "e2e-target")
	e.config(t, "memory.enabled", "true")

	startOut := e.startSystemOnFreePort(t)
	startOperation := operationIDFromOutput(t, startOut)
	e.waitForCommandOutput(t, "hub target", "e2e-target", "system", "target", "list")
	status := e.systemStatus(t)
	if status.Schema != system.SnapshotSchema || status.State != system.StateHealthy {
		t.Fatalf("system status after start = schema %q state %q; want %q/%q\n%s", status.Schema, status.State, system.SnapshotSchema, system.StateHealthy, e.run(t, "system", "status", "--json"))
	}

	// Memory RPCs and session/task/schedule/operation commands are independent
	// Hub workloads; running them concurrently exercises concurrent Hub access
	// and cuts E2E wall-clock time without skipping any assertion.
	t.Run("hub_workloads", func(t *testing.T) {
		t.Run("memory", func(t *testing.T) {
			t.Parallel()
			e.checkMemoryCommands(t)
		})
		t.Run("sessions_and_lifecycle", func(t *testing.T) {
			t.Parallel()

			e.run(t, "session", "run", "claude", "-n", "claude-e2e", "--on", "e2e-target")
			e.waitForOutput(t, "structured session", func() string {
				out, _ := e.runAllowError("session", "list")
				return out
			}, "claude-e2e")

			e.run(t, "session", "send", "e2e-target/claude-e2e", "hello from send")

			queueOut := e.run(t, "session", "queue", "e2e-target/claude-e2e")
			if !strings.Contains(queueOut, "No pending input.") {
				t.Fatalf("queue output = %q, want empty queue", queueOut)
			}
			clearOut := e.run(t, "session", "queue", "clear", "e2e-target/claude-e2e")
			if !strings.Contains(clearOut, "Cleared 0 pending input") {
				t.Fatalf("queue clear output = %q, want clear count", clearOut)
			}
			cancelOut, cancelErr := e.runAllowError("session", "queue", "cancel", "e2e-target/claude-e2e", "missing")
			if cancelErr == nil || !strings.Contains(cancelOut, "queue item") {
				t.Fatalf("queue cancel missing = err %v output %q, want not-found error", cancelErr, cancelOut)
			}

			e.run(t, "session", "sleep", "e2e-target/claude-e2e")
			e.waitForOutput(t, "sleeping structured session", func() string {
				out, _ := e.runAllowError("session", "list")
				return out
			}, "sleeping")
			e.run(t, "session", "resume", "e2e-target/claude-e2e")
			e.waitForOutput(t, "resumed structured session", func() string {
				out, _ := e.runAllowError("session", "list")
				return out
			}, "running")
			e.run(t, "session", "send", "e2e-target/claude-e2e", "after resume")
			e.run(t, "session", "interrupt", "e2e-target/claude-e2e")

			if out := e.run(t, "task", "list"); !strings.Contains(out, "no tasks") {
				t.Fatalf("task list output = %q, want empty canonical list", out)
			}
			if out := e.run(t, "schedule", "list"); !strings.Contains(out, "no schedules") {
				t.Fatalf("schedule list output = %q, want empty canonical list", out)
			}

			// Each invocation opens and closes the lifecycle journal. Querying setup and
			// start from later processes proves that completed operations survive reopen.
			for _, operationID := range []string{setupOperation, startOperation} {
				out := e.run(t, "system", "operation", "show", operationID, "--json")
				var operation system.Operation
				if err := json.Unmarshal([]byte(out), &operation); err != nil {
					t.Fatalf("decode operation %s: %v\n%s", operationID, err, out)
				}
				if string(operation.ID) != operationID || operation.State != system.OperationSucceeded {
					t.Fatalf("operation %s after reopen = id %q state %q", operationID, operation.ID, operation.State)
				}
			}
			operationsOut := e.run(t, "system", "operation", "list", "--json")
			if !strings.Contains(operationsOut, setupOperation) || !strings.Contains(operationsOut, startOperation) {
				t.Fatalf("operation list after reopen missing setup/start operations:\n%s", operationsOut)
			}

			e.trackManagedPIDs(t)
			e.run(t, "session", "stop", "e2e-target/claude-e2e")
		})
	})

	stopOut := e.run(t, "system", "stop", "--force")
	operationIDFromOutput(t, stopOut)
	stoppedStatus := e.systemStatus(t)
	if stoppedStatus.Schema != system.SnapshotSchema || (stoppedStatus.State != system.StateStopped && stoppedStatus.State != system.StateDegraded) {
		t.Fatalf("system status with hub stopped = schema %q state %q, want %q and stopped/degraded", stoppedStatus.Schema, stoppedStatus.State, system.SnapshotSchema)
	}

	out, err := e.runAllowError("session", "list")
	if err == nil {
		t.Fatalf("amux session list unexpectedly succeeded with hub stopped:\n%s", out)
	}
	for _, want := range []string{"hub is not reachable at " + e.hubAddr, "amux system status", "amux system start"} {
		if !strings.Contains(out, want) {
			t.Fatalf("amux session list hub-unavailable output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "amux hub start") {
		t.Fatalf("amux session list should only suggest canonical recovery:\n%s", out)
	}

	// Agent Pack and credential metadata are local file operations and remain
	// available while the hub is down.
	e.run(t, "agent", "pack", "list")
	e.runWithInput(t, "e2e-secret\n", "agent", "credential", "set", "e2e", "E2E_TOKEN")
	credentialOut := e.run(t, "agent", "credential", "show", "e2e")
	if !strings.Contains(credentialOut, "E2E_TOKEN") || strings.Contains(credentialOut, "e2e-secret") {
		t.Fatalf("credential metadata output = %q, want env name without secret", credentialOut)
	}
}

func (e *cliE2E) checkMemoryCommands(t *testing.T) {
	t.Helper()
	decode := func(output string, value any) {
		t.Helper()
		if err := json.Unmarshal([]byte(output), value); err != nil {
			t.Fatalf("decode memory response: %v\n%s", err, output)
		}
	}
	type document struct {
		Path       string `json:"path"`
		Content    string `json:"content"`
		Revision   string `json:"revision"`
		Exists     bool   `json:"exists"`
		Truncated  bool   `json:"truncated"`
		NextOffset int64  `json:"next_offset"`
	}
	show := func(path string) document {
		t.Helper()
		var result document
		decode(e.run(t, "memory", "show", path), &result)
		return result
	}
	upsert := func(path, content, expected string) string {
		t.Helper()
		var result document
		decode(e.runWithInput(t, content, "memory", "upsert", path, "--expected-revision", expected), &result)
		wantRevision := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		if result.Path != path || result.Revision != wantRevision {
			t.Fatalf("memory upsert = %+v, want path %q revision %q", result, path, wantRevision)
		}
		return result.Revision
	}

	// Every operation starts a separate CLI process. Memory is owned by the
	// Hub rather than a CLI process, Runtime workspace or selected Profile.
	if got := show("MEMORY.md"); got.Exists || got.Revision != "absent" || got.Content != "" {
		t.Fatalf("initial memory entry = %+v, want absent", got)
	}
	entry := "# Memory\n- 发布约定见 projects/e2e/release.md。\n"
	entryRevision := upsert("MEMORY.md", entry, "absent")
	if got := show("MEMORY.md"); !got.Exists || got.Content != entry || got.Revision != entryRevision || got.Truncated {
		t.Fatalf("memory entry after create = %+v", got)
	}

	const path = "projects/e2e/release.md"
	content := "# Release\n发布门禁：运行完整检查。\n" + strings.Repeat("Verified release procedure details.\n", 400)
	revision := upsert(path, content, "absent")
	stored, err := os.ReadFile(filepath.Join(e.amuxHome, "memory", "global", filepath.FromSlash(path)))
	if err != nil || string(stored) != content {
		t.Fatalf("Hub Markdown file differs from written content: err=%v bytes=%d", err, len(stored))
	}
	var search struct {
		Matches []struct {
			Path     string `json:"path"`
			Snippet  string `json:"snippet"`
			Revision string `json:"revision"`
			Line     int    `json:"line"`
		} `json:"matches"`
		Incomplete   bool `json:"incomplete"`
		Truncated    bool `json:"truncated"`
		ScannedFiles int  `json:"scanned_files"`
	}
	decode(e.run(t, "memory", "search", "发布门禁", "--path-prefix", "projects/e2e/", "--limit", "1"), &search)
	if search.Incomplete || search.Truncated || search.ScannedFiles < 1 || len(search.Matches) != 1 {
		t.Fatalf("memory search = %+v, want one complete hit", search)
	}
	if hit := search.Matches[0]; hit.Path != path || hit.Revision != revision || hit.Line != 2 || !strings.Contains(hit.Snippet, "发布门禁") {
		t.Fatalf("memory search hit = %+v", hit)
	}
	if got := show(path); !got.Exists || got.Revision != revision || !got.Truncated || got.NextOffset <= 0 || !strings.HasPrefix(content, got.Content) {
		t.Fatalf("large memory read missing a bounded continuation: exists=%t revision=%q truncated=%t offset=%d bytes=%d", got.Exists, got.Revision, got.Truncated, got.NextOffset, len(got.Content))
	}
	if exported := e.run(t, "memory", "export", path); exported != content {
		t.Fatalf("memory export differs after paging: got %d bytes, want %d", len(exported), len(content))
	}

	updated := "# Release\n发布门禁：先检查差异，再执行完整检查。\n"
	updatedRevision := upsert(path, updated, revision)
	conflictOut, conflictErr := e.runAllowErrorWithInput("stale overwrite\n", "memory", "upsert", path, "--expected-revision", revision)
	if conflictErr == nil || !strings.Contains(conflictOut, "revision conflict") {
		t.Fatalf("stale memory write: err=%v output=%q, want revision conflict", conflictErr, conflictOut)
	}
	conflictOut, conflictErr = e.runAllowError("memory", "forget", path, "--expected-revision", revision)
	if conflictErr == nil || !strings.Contains(conflictOut, "revision conflict") {
		t.Fatalf("stale memory delete: err=%v output=%q, want revision conflict", conflictErr, conflictOut)
	}
	if got := show(path); !got.Exists || got.Content != updated || got.Revision != updatedRevision {
		t.Fatalf("memory changed after rejected stale writes: %+v", got)
	}

	var forgotten struct {
		Path          string `json:"path"`
		AlreadyAbsent bool   `json:"already_absent"`
	}
	decode(e.run(t, "memory", "forget", path, "--expected-revision", updatedRevision), &forgotten)
	if forgotten.Path != path || forgotten.AlreadyAbsent {
		t.Fatalf("memory forget = %+v, want deleted document", forgotten)
	}
	decode(e.run(t, "memory", "forget", path, "--expected-revision", "absent"), &forgotten)
	if forgotten.Path != path || !forgotten.AlreadyAbsent {
		t.Fatalf("repeated memory forget = %+v, want already absent", forgotten)
	}
	if got := show(path); got.Exists || got.Revision != "absent" || got.Content != "" {
		t.Fatalf("forgotten memory remains readable: %+v", got)
	}
	decode(e.run(t, "memory", "search", "发布门禁", "--path-prefix", "projects/e2e/"), &search)
	if len(search.Matches) != 0 || search.Incomplete {
		t.Fatalf("forgotten memory remains searchable: %+v", search)
	}
	exportOut, exportErr := e.runAllowError("memory", "export", path)
	if exportErr == nil || !strings.Contains(exportOut, "does not exist") {
		t.Fatalf("forgotten memory export: err=%v output=%q, want missing document", exportErr, exportOut)
	}
	e.run(t, "memory", "forget", "MEMORY.md", "--expected-revision", entryRevision)
}

func TestWaitForCommandExitOrKillKillsHungProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap '' TERM; while true; do sleep 1; done")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = waitForCommandExitOrKill(cmd, stdin.Close, 100*time.Millisecond)
	if err == nil {
		t.Fatal("waitForCommandExitOrKill returned nil for hung process")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cleanup took %s, want bounded kill", elapsed)
	}
	if processRunning(cmd.Process.Pid) {
		t.Fatalf("hung process pid %d is still running", cmd.Process.Pid)
	}
}

func TestWaitForCommandExitOrKillAllowsGracefulExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "cat >/dev/null")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := waitForCommandExitOrKill(cmd, stdin.Close, time.Second); err != nil {
		t.Fatalf("waitForCommandExitOrKill graceful exit: %v", err)
	}
}

func TestSanitizeE2EProcessLineRedactsSecretsAndBoundsOutput(t *testing.T) {
	raw := "123 amuxlet _structured_broker --env ANTHROPIC_AUTH_TOKEN=super-secret --env SAFE=value --env GLM_API_KEY=also-secret -- " + strings.Repeat("x", 4096)
	got := sanitizeE2EProcessLine(raw)
	for _, notWant := range []string{"super-secret", "also-secret"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("sanitized line leaked %q: %s", notWant, got)
		}
	}
	for _, want := range []string{"ANTHROPIC_AUTH_TOKEN=REDACTED", "GLM_API_KEY=REDACTED", "SAFE=value"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized line missing %q: %s", want, got)
		}
	}
	if len(got) > maxE2EProcessLineLen+len("…") {
		t.Fatalf("sanitized line length = %d, want bounded to %d", len(got), maxE2EProcessLineLen)
	}
}

func TestLongTestsRequireExplicitOptIn(t *testing.T) {
	tests := []struct {
		name  string
		short bool
		env   string
		want  bool
	}{
		{name: "default development run", want: false},
		{name: "explicit full gate", env: "1", want: true},
		{name: "short always wins", short: true, env: "1", want: false},
		{name: "unexpected value", env: "true", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := longTestsEnabled(tc.short, tc.env); got != tc.want {
				t.Fatalf("longTestsEnabled(%v, %q) = %v, want %v", tc.short, tc.env, got, tc.want)
			}
		})
	}
}

func TestPollForOutputRetriesAfterOneProbeTimeout(t *testing.T) {
	probes := 0
	last, err := pollForOutput(100*time.Millisecond, 10*time.Millisecond, time.Millisecond, func(timeout time.Duration) (string, error) {
		probes++
		if probes == 1 {
			time.Sleep(timeout)
			return "hub is not reachable", context.DeadlineExceeded
		}
		return "e2e-target", nil
	}, "e2e-target")
	if err != nil {
		t.Fatalf("pollForOutput: %v; last output: %s", err, last)
	}
	if probes != 2 {
		t.Fatalf("probe count = %d, want 2", probes)
	}
}

const maxE2EProcessLineLen = 2000

func waitForCommandExitOrKill(cmd *exec.Cmd, closeInput func() error, timeout time.Duration) error {
	if closeInput != nil {
		_ = closeInput()
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		err := <-done
		if err == nil {
			return fmt.Errorf("process did not exit within %s", timeout)
		}
		return fmt.Errorf("process did not exit within %s; killed: %w", timeout, err)
	}
}

func sanitizeE2EProcessLine(line string) string {
	fields := strings.Fields(line)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "--env" && i+1 < len(fields) {
			fields[i+1] = redactEnvAssignment(fields[i+1])
			i++
			continue
		}
		fields[i] = redactEnvAssignment(fields[i])
	}
	out := strings.Join(fields, " ")
	if len(out) > maxE2EProcessLineLen {
		return out[:maxE2EProcessLineLen] + "…"
	}
	return out
}

func redactEnvAssignment(value string) string {
	key, _, ok := strings.Cut(value, "=")
	if !ok || !isSensitiveEnvName(key) {
		return value
	}
	return key + "=REDACTED"
}

func isSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "API_KEY", "AUTH"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func TestSystemStartReconcilesRuntimeStartedBeforeHubConfigChangeE2E(t *testing.T) {
	requireLongTest(t)
	t.Parallel()

	e := newCLIE2E(t)
	e.run(t, "setup", "--no-start")
	e.config(t, "runtime.id", "e2e-target")
	e.config(t, "runtime.name", "e2e-target")

	// Start the runtime against an address that will never host this test's hub,
	// then change hub.addr while the process is still alive. This reproduces an
	// upgraded installation whose runtime retained startup-time configuration.
	e.hubAddr = freeTCPAddr(t)
	e.config(t, "hub.addr", e.hubAddr)
	e.cleanupEnabled = true
	t.Cleanup(func() { e.cleanup(t) })
	e.startRuntimeOnly(t)
	e.waitForOutput(t, "stale runtime socket", func() string {
		if _, err := os.Stat(filepath.Join(e.amuxHome, "runtime.sock")); err == nil {
			return "(exists)"
		}
		return "(missing)"
	}, "(exists)")
	oldPID, ok := readPIDFile(filepath.Join(e.amuxHome, "runtime.pid"))
	if !ok {
		t.Fatal("stale runtime did not write a PID file")
	}

	e.startSystemOnFreePort(t)
	e.waitForCommandOutput(t, "reconciled hub target", "e2e-target", "system", "target", "list")
	newPID, ok := readPIDFile(filepath.Join(e.amuxHome, "runtime.pid"))
	if !ok {
		t.Fatal("reconciled runtime did not write a PID file")
	}
	if newPID == oldPID {
		t.Fatalf("runtime PID remained %d after hub config changed", oldPID)
	}
	if status := e.systemStatus(t); status.State != system.StateHealthy {
		t.Fatalf("system status after runtime reconciliation = %q, want healthy", status.State)
	}
}

func TestUpdateRestartContinuesOriginalTurnE2E(t *testing.T) {
	requireLongTest(t)
	t.Parallel()

	e := newCLIE2E(t)
	e.env = append(e.env, "AGENT_PACK_CONTRACT_SCM_URL=http://127.0.0.1:0", "AMUX_LANGUAGE=en")
	e.writeFakeClaude(t)
	startLog := filepath.Join(e.amuxHome, "turn-dispatches")
	release := filepath.Join(e.amuxHome, "finish-turn")
	e.env = append(e.env, "E2E_DISPATCH_LOG="+startLog, "E2E_FINISH_TURN="+release)
	sidecar := `#!/usr/bin/env python3
import json, os, sys, time
def emit(event):
    event['provider'] = 'claude'
    print(json.dumps({'jsonrpc':'2.0','method':'event','params':{'event':event}}), flush=True)
for line in sys.stdin:
    msg = json.loads(line)
    method = msg['method']
    if method == 'turn.start':
        text = msg['params']['text']
        with open(os.environ['E2E_DISPATCH_LOG'], 'a') as log:
            log.write(text + '\n')
    print(json.dumps({'jsonrpc':'2.0','id':msg.get('id'),'result':{'ok':True}}), flush=True)
    if method == 'turn.start':
        if text == 'first':
            emit({'type':'assistant_text_delta','text':'before update'})
            while not os.path.exists(os.environ['E2E_FINISH_TURN']):
                time.sleep(0.025)
            emit({'type':'assistant_text_delta','text':'after update'})
        emit({'type':'turn_done','state':{'state':'turn_done','stop_reason':'end_turn'}})
    elif method == 'shutdown':
        break
`
	if err := os.WriteFile(filepath.Join(e.fakeBin, "amux-claude-sdk-sidecar"), []byte(sidecar), 0755); err != nil {
		t.Fatal(err)
	}
	e.run(t, "setup", "--no-start")
	e.config(t, "runtime.id", "e2e-target")
	e.startSystemOnFreePort(t)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	e.waitForCommandOutput(t, "hub target", "e2e-target", "system", "target", "list")
	e.run(t, "session", "run", "claude", "-n", "claude-e2e", "--on", "e2e-target")
	e.run(t, "session", "send", "e2e-target/claude-e2e", "first")

	db, err := store.Open(filepath.Join(e.amuxHome, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	currentSession := func() store.SessionRow {
		t.Helper()
		rows, err := db.AllSessions()
		if err != nil || len(rows) != 1 {
			t.Fatalf("read session: count=%d, error=%v", len(rows), err)
		}
		return rows[0]
	}
	before := currentSession()
	oldRuntimePID, ok := readPIDFile(filepath.Join(e.amuxHome, "runtime.pid"))
	if !ok {
		t.Fatal("initial Runtime PID unavailable")
	}
	var originalTurn string
	events := func() []agentstream.Event {
		t.Helper()
		out, err := db.LoadAgentEvents(t.Context(), before.ID)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	e.waitForOutput(t, "original turn output", func() string {
		for _, event := range events() {
			if delta, ok := event.Body.(agentstream.AssistantDelta); ok && delta.Text == "before update" {
				originalTurn = event.Turn.ID
				return delta.Text
			}
		}
		return ""
	}, "before update")

	e.run(t, "update", "--source", filepath.Dir(e.amux), "--restart", "--drain-timeout", "5s")
	newRuntimePID, ok := readPIDFile(filepath.Join(e.amuxHome, "runtime.pid"))
	if !ok || newRuntimePID == oldRuntimePID {
		t.Fatalf("Runtime did not restart, PID before=%d after=%d", oldRuntimePID, newRuntimePID)
	}
	after := currentSession()
	if after.ID != before.ID || after.LaunchID != before.LaunchID || after.PID != before.PID || after.State != "running" {
		t.Fatal("the upgrade did not retain the broker's running session")
	}
	for _, event := range events() {
		if event.Body.Kind() == agentstream.KindTurnFinished {
			t.Fatal("the upgrade finished an active turn")
		}
	}
	e.waitForCommandOutput(t, "hub target after update", "e2e-target", "system", "target", "list")
	e.run(t, "session", "send", "e2e-target/claude-e2e", "second")
	e.waitForCommandOutput(t, "queued follow-up", "second", "session", "queue", "e2e-target/claude-e2e")
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.waitForOutput(t, "original and queued turns finish", func() string {
		var completed []string
		var resumedOutput bool
		for _, event := range events() {
			switch body := event.Body.(type) {
			case agentstream.AssistantDelta:
				resumedOutput = resumedOutput || (event.Turn.ID == originalTurn && body.Text == "after update")
			case agentstream.TurnFinished:
				if body.Terminal.Outcome != agentstream.OutcomeSuccess {
					t.Fatalf("turn %s outcome: %s", event.Turn.ID, body.Terminal.Outcome)
				}
				completed = append(completed, event.Turn.ID)
			}
		}
		if resumedOutput && len(completed) == 2 && completed[0] == originalTurn && completed[1] != originalTurn {
			return "both finished"
		}
		return fmt.Sprintf("resumed output=%t, completed=%v", resumedOutput, completed)
	}, "both finished")
	if data, err := os.ReadFile(startLog); err != nil || string(data) != "first\nsecond\n" {
		t.Fatalf("provider dispatches = %q, err=%v", data, err)
	}
}

type cliE2E struct {
	repoRoot       string
	fakeBin        string
	amux           string
	hubAddr        string
	amuxHome       string
	env            []string
	pids           map[int]struct{}
	cleanupEnabled bool
}

func newCLIE2E(t *testing.T) *cliE2E {
	t.Helper()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binDir, err := sharedCLIE2EBinaries.Get(func() (string, bool, error) {
		return buildCLIE2EBinaries(repoRoot)
	})
	if err != nil {
		t.Fatal(err)
	}

	home, err := os.MkdirTemp("/tmp", "amux-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	amuxHome := filepath.Join(home, "amux-home")
	fakeBin := filepath.Join(t.TempDir(), "fakebin")
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"HOME="+home,
		"AMUX_HOME="+amuxHome,
		"PATH="+fakeBin+string(os.PathListSeparator)+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	e := &cliE2E{
		repoRoot: repoRoot,
		fakeBin:  fakeBin,
		amux:     filepath.Join(binDir, "amux"),
		amuxHome: amuxHome,
		env:      env,
		pids:     make(map[int]struct{}),
	}
	t.Cleanup(func() { e.cleanupResidualProcesses(t) })
	return e
}

func prebuiltCLIE2EBinariesDir() (string, bool) {
	if cliE2EBuildTags != "" {
		return "", false
	}
	dir := strings.TrimSpace(os.Getenv("AMUX_TEST_BIN_DIR"))
	if dir == "" || !filepath.IsAbs(dir) {
		return "", false
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, name := range []string{"amux", "amuxd", "amuxlet"} {
		info, err := os.Stat(filepath.Join(dir, name+ext))
		if err != nil || info.IsDir() {
			return "", false
		}
	}
	return dir, true
}

func buildCLIE2EBinaries(repoRoot string) (string, bool, error) {
	if prebuilt, ok := prebuiltCLIE2EBinariesDir(); ok {
		return prebuilt, false, nil
	}
	// Distributions can exercise this process contract against their own thin
	// entrypoints while keeping the shared E2E implementation in the core module.
	if buildRoot := os.Getenv("AMUX_E2E_BUILD_ROOT"); buildRoot != "" {
		if !filepath.IsAbs(buildRoot) {
			return "", false, fmt.Errorf("AMUX_E2E_BUILD_ROOT must be absolute")
		}
		repoRoot = buildRoot
	}
	binDir, err := os.MkdirTemp("", "amux-e2e-bin-")
	if err != nil {
		return "", false, fmt.Errorf("create shared E2E binary dir: %w", err)
	}
	args := []string{"build", "-trimpath", "-buildvcs=false"}
	if cliE2EBuildTags != "" {
		args = append(args, "-tags", cliE2EBuildTags)
	}
	args = append(args, "-o", binDir+string(os.PathSeparator), "./cmd/amux", "./cmd/amuxd", "./cmd/amuxlet")
	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		_ = os.RemoveAll(binDir)
		return "", false, fmt.Errorf("build CLI E2E binaries: %w\n%s", buildErr, out)
	}
	return binDir, true, nil
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	// amuxd is launched later as a separate process, so it cannot inherit this
	// listener. Close it and let the caller configure+start immediately; the
	// caller retries only the explicit EADDRINUSE-equivalent race.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func (e *cliE2E) startSystemOnFreePort(t *testing.T) string {
	t.Helper()
	const maxAttempts = 3
	var lastOutput string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		e.hubAddr = freeTCPAddr(t)
		e.config(t, "hub.addr", e.hubAddr)
		if attempt == 1 {
			t.Cleanup(func() { e.cleanup(t) })
		}

		// The listener handoff is necessarily close-then-bind. Keep these calls
		// adjacent and retry only when another process won that exact race.
		e.cleanupEnabled = true
		out, err := e.runAllowError("system", "start")
		if err == nil {
			return out
		}
		lastOutput = out
		if !strings.Contains(strings.ToLower(out), "already in use") {
			t.Fatalf("amux system start: %v\n%s", err, out)
		}
		// Start rejects an occupied hub address before spawning our daemon. Do
		// not let cleanup stop or reclaim the unrelated listener at this address.
		e.cleanupEnabled = false
	}
	t.Fatalf("amux system start lost the hub port race %d times:\n%s", maxAttempts, lastOutput)
	return ""
}

func (e *cliE2E) config(t *testing.T, key, value string) {
	t.Helper()
	e.run(t, "system", "config", "set", key, value)
}

func (e *cliE2E) startRuntimeOnly(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(e.amuxHome, 0755); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(e.amuxHome, "runtime.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open runtime log: %v", err)
	}
	defer logFile.Close()
	cmd := exec.Command(filepath.Join(filepath.Dir(e.amux), "amuxlet"))
	cmd.Env = e.env
	cmd.Dir = e.repoRoot
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start runtime only: %v", err)
	}
	if err := os.WriteFile(filepath.Join(e.amuxHome, "runtime.pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0644); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		t.Fatalf("write runtime pid: %v", err)
	}
	e.pids[cmd.Process.Pid] = struct{}{}
	go func() { _ = cmd.Wait() }()
}

func (e *cliE2E) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := e.runAllowError(args...)
	if err != nil {
		t.Fatalf("amux %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (e *cliE2E) runAllowError(args ...string) (string, error) {
	return e.runAllowErrorWithInput("", args...)
}

func (e *cliE2E) runWithInput(t *testing.T, input string, args ...string) string {
	t.Helper()
	out, err := e.runAllowErrorWithInput(input, args...)
	if err != nil {
		t.Fatalf("amux %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (e *cliE2E) runAllowErrorWithInput(input string, args ...string) (string, error) {
	return e.runAllowErrorWithInputTimeout(30*time.Second, input, args...)
}

func (e *cliE2E) runAllowErrorWithInputTimeout(timeout time.Duration, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.amux, args...)
	cmd.Env = e.env
	cmd.Dir = e.repoRoot
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *cliE2E) systemStatus(t *testing.T) system.Snapshot {
	t.Helper()
	out := e.run(t, "system", "status", "--json")
	var snapshot system.Snapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatalf("decode system status: %v\n%s", err, out)
	}
	return snapshot
}

func operationIDFromOutput(t *testing.T, out string) string {
	t.Helper()
	const marker = "(operation "
	start := strings.LastIndex(out, marker)
	if start < 0 {
		t.Fatalf("operation output missing ID: %q", out)
	}
	start += len(marker)
	end := strings.Index(out[start:], ")")
	if end < 0 {
		t.Fatalf("operation output has unterminated ID: %q", out)
	}
	id := strings.TrimSpace(out[start : start+end])
	if id == "" {
		t.Fatalf("operation output has empty ID: %q", out)
	}
	return id
}

func (e *cliE2E) cleanup(t *testing.T) {
	t.Helper()
	if !e.cleanupEnabled {
		return
	}
	e.trackManagedPIDs(t)
	if e.hubAddr != "" && addrInUse(e.hubAddr) {
		_, _ = e.runAllowError("session", "stop", "e2e-target/claude-e2e")
	}
	if (e.hubAddr != "" && addrInUse(e.hubAddr)) || len(e.managedPIDs(t)) > 0 || len(e.residualProcesses(t)) > 0 {
		_, _ = e.runAllowError("system", "stop", "--force")
		e.trackManagedPIDs(t)
	}
	e.signalResidualProcesses(t, syscall.SIGTERM)

	deadline := time.Now().Add(5 * time.Second)
	escalated := false
	for time.Now().Before(deadline) {
		allStopped := !addrInUse(e.hubAddr) && len(e.residualProcessLines(t)) == 0
		for pid := range e.pids {
			if processRunning(pid) {
				allStopped = false
				break
			}
		}
		if allStopped {
			return
		}
		if !escalated && time.Until(deadline) < 3*time.Second {
			e.signalResidualProcesses(t, syscall.SIGKILL)
			escalated = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	var running []int
	for pid := range e.pids {
		if processRunning(pid) {
			running = append(running, pid)
		}
	}
	t.Fatalf("E2E cleanup left managed processes: addr_in_use=%v running_pids=%v matching_processes=%v pid_files=%v\nhub log:\n%s\nruntime log:\n%s",
		addrInUse(e.hubAddr), running, e.residualProcessLines(t), e.managedPIDFiles(t),
		readFileBestEffort(filepath.Join(e.amuxHome, "hub.log")),
		readFileBestEffort(filepath.Join(e.amuxHome, "runtime.log")))
}

func (e *cliE2E) signalResidualProcesses(t *testing.T, signal syscall.Signal) {
	t.Helper()
	pids := make(map[int]struct{}, len(e.pids))
	for pid := range e.pids {
		pids[pid] = struct{}{}
	}
	for pid := range e.residualProcesses(t) {
		pids[pid] = struct{}{}
	}
	for pid := range pids {
		if !processRunning(pid) {
			continue
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = signalServiceProcess(pid, process, signal == syscall.SIGKILL)
	}
}

func (e *cliE2E) trackManagedPIDs(t *testing.T) {
	t.Helper()
	for pid := range e.managedPIDs(t) {
		e.pids[pid] = struct{}{}
	}
}

func (e *cliE2E) cleanupResidualProcesses(t *testing.T) {
	t.Helper()
	residual := e.residualProcesses(t)
	if len(residual) == 0 {
		return
	}
	e.signalResidualProcesses(t, syscall.SIGTERM)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.residualProcesses(t)) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.signalResidualProcesses(t, syscall.SIGKILL)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.residualProcesses(t)) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("E2E cleanup left residual processes: %v", e.residualProcessLines(t))
}

func (e *cliE2E) residualProcessLines(t *testing.T) []string {
	t.Helper()
	processes := e.residualProcesses(t)
	lines := make([]string, 0, len(processes))
	for _, line := range processes {
		lines = append(lines, sanitizeE2EProcessLine(line))
	}
	return lines
}

func (e *cliE2E) residualProcesses(t *testing.T) map[int]string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		t.Fatalf("list E2E processes: %v", err)
	}
	matches := make(map[int]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, e.amuxHome) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil && pid > 0 {
			matches[pid] = line
		}
	}
	return matches
}

func (e *cliE2E) managedPIDs(t *testing.T) map[int]struct{} {
	t.Helper()
	pids := make(map[int]struct{})
	for _, path := range e.managedPIDFiles(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && pid > 0 {
			pids[pid] = struct{}{}
		}
	}
	return pids
}

func (e *cliE2E) managedPIDFiles(t *testing.T) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(e.amuxHome, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pid") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("scan E2E pid files: %v", err)
	}
	return paths
}

func readFileBestEffort(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func pollForOutput(overallTimeout, probeTimeout, interval time.Duration, probe func(time.Duration) (string, error), want string) (string, error) {
	deadline := time.Now().Add(overallTimeout)
	var last string
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		attemptTimeout := probeTimeout
		if attemptTimeout <= 0 || attemptTimeout > remaining {
			attemptTimeout = remaining
		}
		last, lastErr = probe(attemptTimeout)
		if strings.Contains(last, want) {
			return last, nil
		}
		remaining = time.Until(deadline)
		if remaining <= 0 {
			break
		}
		pause := interval
		if pause > remaining {
			pause = remaining
		}
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	if lastErr != nil {
		return last, fmt.Errorf("output did not contain %q within %s; last probe: %w", want, overallTimeout, lastErr)
	}
	return last, fmt.Errorf("output did not contain %q within %s", want, overallTimeout)
}

func (e *cliE2E) waitForCommandOutput(t *testing.T, label, want string, args ...string) {
	t.Helper()
	last, err := pollForOutput(15*time.Second, time.Second, 150*time.Millisecond, func(timeout time.Duration) (string, error) {
		return e.runAllowErrorWithInputTimeout(timeout, "", args...)
	}, want)
	if err == nil {
		return
	}
	t.Fatalf("timed out waiting for %s: %v; command: amux %s; last output:\n%s\nhub log:\n%s\nruntime log:\n%s",
		label, err, strings.Join(args, " "), last,
		readFileBestEffort(filepath.Join(e.amuxHome, "hub.log")),
		readFileBestEffort(filepath.Join(e.amuxHome, "runtime.log")))
}

func (e *cliE2E) waitForOutput(t *testing.T, label string, fn func() string, want string) {
	t.Helper()
	last, err := pollForOutput(5*time.Second, 5*time.Second, 150*time.Millisecond, func(time.Duration) (string, error) {
		return fn(), nil
	}, want)
	if err != nil {
		t.Fatalf("timed out waiting for %s: %v; last output:\n%s", label, err, last)
	}
}

func (e *cliE2E) writeFakeClaude(t *testing.T) {
	t.Helper()
	path := filepath.Join(e.fakeBin, "claude")
	body := `#!/bin/sh
trap 'printf "INTERRUPTED\n"' INT
printf "READY\n"
while IFS= read -r line; do
  printf "CLAUDE:%s\n" "$line"
done
`
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

func (e *cliE2E) writeFakeClaudeSidecar(t *testing.T) {
	t.Helper()
	path := filepath.Join(e.fakeBin, "amux-claude-sdk-sidecar")
	body := `#!/usr/bin/env python3
import json, sys
for line in sys.stdin:
    msg=json.loads(line)
    mid=msg.get('id')
    method=msg.get('method')
    if method in ('initialize','session.start'):
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
    elif method == 'turn.start':
        print(json.dumps({'jsonrpc':'2.0','method':'event','params':{'event':{'provider':'claude','type':'turn_done','state':{'state':'turn_done','stop_reason':'end_turn'}}}}), flush=True)
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'state':'turn_done'}}), flush=True)
    elif method in ('interrupt','shutdown','permission.respond'):
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
        if method == 'shutdown':
            break
`
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCLIE2EBinariesReusesPrebuiltDir(t *testing.T) {
	dir := t.TempDir()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, name := range []string{"amux", "amuxd", "amuxlet"} {
		if err := os.WriteFile(filepath.Join(dir, name+ext), []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AMUX_TEST_BIN_DIR", dir)
	got, ownedTemp, err := buildCLIE2EBinaries(t.TempDir())
	if err != nil {
		t.Fatalf("buildCLIE2EBinaries: %v", err)
	}
	if got != dir {
		t.Fatalf("dir = %q, want %q", got, dir)
	}
	if ownedTemp {
		t.Fatal("ownedTemp = true for prebuilt dir, want false so caller bin dir is never deleted")
	}
}
