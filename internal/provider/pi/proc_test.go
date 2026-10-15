package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/session"
)

func startTestProc(t *testing.T, opts StartOptions) (*Proc, <-chan []byte) {
	t.Helper()
	proc, out, err := Start(context.Background(), opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = proc.Close() })
	return proc, out
}

func writeFakePi(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake pi is unix-only")
	}
	path := filepath.Join(t.TempDir(), "fake-pi")
	// Hold ForkLock while the write fd is open so a concurrently forked child
	// (parallel tests) cannot inherit it and make exec fail with ETXTBSY.
	syscall.ForkLock.Lock()
	err := os.WriteFile(path, []byte(script), 0o755)
	syscall.ForkLock.Unlock()
	if err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}

func collectEvents(t *testing.T, proc *Proc, timeout time.Duration) <-chan testEmission {
	t.Helper()
	events := make(chan testEmission, 64)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	return events
}

func TestEmitTurnProducesTypedTurnPayload(t *testing.T) {
	proc := &Proc{}
	events := make(chan agentstream.ProducerEmission, 1)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		events <- emission
		return false
	})

	proc.emitTurn(agentstream.AssistantDelta{Text: "hello"}, agentstream.ActorAgent, "agent", "text_delta", "")
	emission := <-events
	delta, ok := emission.Turn.(agentstream.AssistantDelta)
	if !ok || delta.Text != "hello" || emission.Terminal != nil || emission.Session != nil {
		t.Fatalf("producer emission = %#v", emission)
	}
}

func waitRunActive(t *testing.T, proc *Proc, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		proc.runMu.Lock()
		active := proc.runActive
		proc.runMu.Unlock()
		if active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("turn did not start")
}

func waitEvent(t *testing.T, events <-chan testEmission, timeout time.Duration) testEmission {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for session event")
		return testEmission{}
	}
}

// eventByTerminal drains events until a terminal (turn_done/failed) arrives and
// returns all events collected.
func drainUntilTerminal(t *testing.T, events <-chan testEmission, timeout time.Duration) []testEmission {
	t.Helper()
	var all []testEmission
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			all = append(all, ev)
			if ev.Terminal != nil {
				return all
			}
		case <-deadline:
			t.Fatalf("timed out waiting for terminal; got %d events", len(all))
			return nil
		}
	}
}

// fakePiScript simulates a pi RPC server for a single prompt: get_state at
// startup, then the full agent run event sequence, then abort handling.
const fakePiScript = `#!/usr/bin/env python3
import json, sys

def out(obj):
    print(json.dumps(obj), flush=True)

def emit_run():
    out({"type": "agent_start"})
    out({"type": "turn_start"})
    out({"type": "message_start", "message": {"role": "user", "content": [{"type": "text", "text": "hello"}]}})
    out({"type": "message_end", "message": {"role": "user", "content": [{"type": "text", "text": "hello"}]}})
    out({"type": "message_start", "message": {"id": "m1", "role": "assistant", "content": [], "stopReason": "pending"}})
    out({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 0, "delta": "hello from "}})
    out({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "contentIndex": 0, "delta": "fake"}})
    out({"type": "message_end", "message": {"id": "m1", "role": "assistant", "content": [{"type": "text", "text": "hello from fake"}], "stopReason": "stop"}})
    out({"type": "turn_end", "message": {"id": "m1", "role": "assistant", "content": [{"type": "text", "text": "hello from fake"}], "stopReason": "stop"}})
    out({"type": "agent_end", "messages": [], "willRetry": False})
    out({"type": "agent_settled"})

running = False
for line in sys.stdin:
    msg = json.loads(line)
    typ = msg.get("type")
    if typ == "get_state":
        out({"type": "response", "command": "get_state", "success": True,
             "data": {"sessionId": "sess-1", "model": {"id": "deepseek-v4-flash"}}})
    elif typ == "prompt":
        if msg.get("message") == "REJECT":
            out({"type": "response", "command": "prompt", "success": False, "error": "no model selected"})
            continue
        out({"type": "response", "command": "prompt", "success": True})
        running = True
        emit_run()
        running = False
    elif typ == "abort":
        out({"type": "response", "command": "abort", "success": True})
        if running:
            out({"type": "agent_end", "messages": [], "willRetry": False})
            out({"type": "agent_settled"})
            running = False
`

func TestStartResolvesExecutableFromLaunchEnvPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake provider is unix-only")
	}
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "pi")
	if err := os.WriteFile(fake, []byte(fakePiScript), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")

	proc, out := startTestProc(t, StartOptions{
		Cwd: t.TempDir(),
		Env: append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin"),
	})
	_ = proc
	_ = out
}

type recordingLaunchBackend struct {
	delegate isolation.LaunchBackend
	spec     isolation.LaunchSpec
}

func (b *recordingLaunchBackend) Start(ctx context.Context, spec isolation.LaunchSpec) (isolation.ProcessHandle, error) {
	b.spec = spec
	return b.delegate.Start(ctx, spec)
}

func TestStartUsesIsolationLaunchBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake provider is unix-only")
	}
	fake := writeFakePi(t, fakePiScript)
	cwd := t.TempDir()
	backend := &recordingLaunchBackend{delegate: isolation.NewPassthroughBackend()}
	launchContext := isolation.LaunchContext{LaunchID: "launch-pi-test", Backend: "linux-dac"}
	proc, _ := startTestProc(t, StartOptions{
		Cwd:             cwd,
		Env:             append(os.Environ(), "PI_TEST=1"),
		ExtraArgs:       []string{"--model", "provider/model"},
		ResumeSessionID: "pi-resume-1",
		Executable:      fake,
		Backend:         backend,
		LaunchContext:   launchContext,
	})

	if backend.spec.Path != fake || backend.spec.Dir != cwd || !backend.spec.SetProcessGroup {
		t.Fatalf("launch spec = %+v", backend.spec)
	}
	if backend.spec.Context != launchContext {
		t.Fatalf("launch context = %+v, want %+v", backend.spec.Context, launchContext)
	}
	wantArgs := []string{"--mode", "rpc", "--session-id", "pi-resume-1", "--model", "provider/model"}
	if !reflect.DeepEqual(backend.spec.Args, wantArgs) {
		t.Fatalf("launch args = %q, want %q", backend.spec.Args, wantArgs)
	}
	if !slices.Contains(backend.spec.Env, "PI_TEST=1") {
		t.Fatalf("launch env does not contain PI_TEST=1: %q", backend.spec.Env)
	}
	if proc.PID() <= 0 {
		t.Fatalf("PID = %d, want launched process", proc.PID())
	}
}

func TestStartPassesResumeSessionID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake provider is unix-only")
	}
	// The fake asserts nothing about args; verify the proc command line carries
	// --session-id so resume wiring cannot silently regress.
	fake := writeFakePi(t, fakePiScript)
	proc, _ := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		Executable:      fake,
		ResumeSessionID: "sess-1",
	})
	if len(proc.args) == 0 {
		t.Fatal("proc args are empty")
	}
	joined := strings.Join(proc.args, " ")
	if !strings.Contains(joined, "--session-id") || !strings.Contains(joined, "sess-1") {
		t.Fatalf("proc args = %q, want --session-id sess-1", joined)
	}
}

func TestProcStreamsTextAndCompletesTurn(t *testing.T) {
	t.Parallel()
	fake := writeFakePi(t, fakePiScript)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := make(chan testEmission, 64)
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "turn-1",
		Input:     session.TextUserInput("hello"),
	}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	}); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}

	all := drainUntilTerminal(t, events, 10*time.Second)

	var deltas []string
	var result *agentstream.ResultObserved
	var terminalOutcome agentstream.Outcome
	for _, ev := range all {
		if ev.Body != nil {
			switch ev.Body.Type {
			case string(agentstream.KindAssistantDelta):
				deltas = append(deltas, ev.Body.Text)
			case string(agentstream.KindResultObserved):
				result = ev.Body.Result
			}
		}
		if ev.Terminal != nil {
			terminalOutcome = ev.Terminal.Outcome
		}
	}
	if got, want := strings.Join(deltas, ""), "hello from fake"; got != want {
		t.Fatalf("streamed text = %q, want %q", got, want)
	}
	if result == nil || result.Text != "hello from fake" {
		t.Fatalf("native result = %+v, want text %q", result, "hello from fake")
	}
	if terminalOutcome != agentstream.OutcomeSuccess {
		t.Fatalf("terminal outcome = %q, want success", terminalOutcome)
	}
}

func TestDispatchTurnPreservesMultilineText(t *testing.T) {
	fake := writeFakePi(t, `#!/usr/bin/env python3
import json, sys

def out(obj):
    print(json.dumps(obj), flush=True)

for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("type") == "get_state":
        out({"type":"response","command":"get_state","success":True,"data":{"sessionId":"sess-1"}})
    elif msg.get("type") == "prompt":
        text = msg.get("message", "")
        out({"type":"response","command":"prompt","success":True})
        out({"type":"agent_start"})
        out({"type":"message_start","message":{"id":"m1","role":"assistant","content":[]}})
        out({"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":text}})
        out({"type":"message_end","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":text}]}})
        out({"type":"agent_settled"})
`)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := make(chan testEmission, 64)

	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-multiline", Input: session.TextUserInput("  first line\nsecond line  \n")}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	}); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}
	all := drainUntilTerminal(t, events, 10*time.Second)
	for _, ev := range all {
		if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) {
			if ev.Body.Result == nil || ev.Body.Result.Text != "  first line\nsecond line  \n" {
				t.Fatalf("result = %+v, want complete multiline input", ev.Body.Result)
			}
			return
		}
	}
	t.Fatal("native result event not observed")
}

func TestDispatchTurnPreservesWhitespaceOnlyText(t *testing.T) {
	fake := writeFakePi(t, `#!/usr/bin/env python3
import json, sys

def out(obj):
    print(json.dumps(obj), flush=True)

for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("type") == "get_state":
        out({"type":"response","command":"get_state","success":True,"data":{"sessionId":"sess-1"}})
    elif msg.get("type") == "prompt":
        text = msg.get("message", "")
        out({"type":"response","command":"prompt","success":True})
        out({"type":"agent_start"})
        out({"type":"message_start","message":{"id":"m1","role":"assistant","content":[]}})
        out({"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":text}})
        out({"type":"message_end","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":text}]}})
        out({"type":"agent_settled"})
`)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := make(chan testEmission, 64)

	const input = " \t\n"
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-whitespace", Input: session.TextUserInput(input)}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	}); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}
	all := drainUntilTerminal(t, events, 10*time.Second)
	for _, ev := range all {
		if ev.Body != nil && ev.Body.Type == string(agentstream.KindAssistantDelta) && ev.Body.Text == input {
			return
		}
	}
	t.Fatal("exact whitespace input was not delivered")
}

func TestProcPublishesSessionMetadata(t *testing.T) {
	t.Parallel()
	fake := writeFakePi(t, fakePiScript)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := collectEvents(t, proc, time.Second)

	deadline := time.After(10 * time.Second)
	var got testEmission
	for {
		select {
		case ev := <-events:
			if ev.Metadata.AgentSessionID != "" || ev.Metadata.EffectiveModel != "" {
				got = ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for session metadata")
		}
		if got.Metadata.AgentSessionID != "" {
			break
		}
	}
	if got.Metadata.AgentSessionID != "sess-1" {
		t.Fatalf("AgentSessionID = %q, want sess-1", got.Metadata.AgentSessionID)
	}
	if got.Metadata.EffectiveModel != "deepseek-v4-flash" {
		t.Fatalf("EffectiveModel = %q, want deepseek-v4-flash", got.Metadata.EffectiveModel)
	}
}

func TestProcInterruptEmitsCancelledTerminal(t *testing.T) {
	t.Parallel()
	fake := writeFakePi(t, fakePiScript)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := collectEvents(t, proc, time.Second)

	// Send the prompt first, then interrupt shortly after (the fake run emits
	// its full sequence immediately, so interrupt lands after agent_settled —
	// still a valid no-op-or-terminal path). To exercise the cancelled path,
	// interrupt before the run settles: block the run with a slow fake.
	_ = events

	// Use a slow fake so the interrupt lands mid-run.
	slowScript := `#!/usr/bin/env python3
import json, sys, time
def out(obj): print(json.dumps(obj), flush=True)
running = False
for line in sys.stdin:
    msg = json.loads(line)
    typ = msg.get("type")
    if typ == "get_state":
        out({"type":"response","command":"get_state","success":True,"data":{"sessionId":"sess-slow"}})
    elif typ == "prompt":
        out({"type":"response","command":"prompt","success":True})
        running = True
        out({"type":"agent_start"})
        out({"type":"message_start","message":{"id":"m1","role":"assistant","content":[],"stopReason":"pending"}})
        out({"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"partial"}})
        time.sleep(5)
        out({"type":"message_end","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"partial"}],"stopReason":"aborted"}})
        out({"type":"turn_end","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"partial"}],"stopReason":"aborted"}})
        out({"type":"agent_end","messages":[],"willRetry":False})
        out({"type":"agent_settled"})
        running = False
    elif typ == "abort":
        out({"type":"response","command":"abort","success":True})
        running = False
`
	fakeSlow := writeFakePi(t, slowScript)
	procSlow, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fakeSlow})
	eventsSlow := collectEvents(t, procSlow, time.Second)

	if _, err := procSlow.Write([]byte("hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Wait for the run to start, then interrupt.
	waitRunActive(t, procSlow, 10*time.Second)
	if err := procSlow.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	all := drainUntilTerminal(t, eventsSlow, 10*time.Second)
	var terminal testEmission
	for _, ev := range all {
		if ev.Terminal != nil {
			terminal = ev
		}
	}
	if terminal.Terminal == nil || terminal.Terminal.Outcome != agentstream.OutcomeCancelled || terminal.Terminal.Message != "cancelled" {
		t.Fatalf("terminal = %+v, want turn_done with stop reason cancelled", terminal)
	}
}

func TestProcPromptRejectionEmitsTurnFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakePi(t, fakePiScript)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := collectEvents(t, proc, time.Second)

	if _, err := proc.Write([]byte("REJECT\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	all := drainUntilTerminal(t, events, 10*time.Second)
	var terminal testEmission
	for _, ev := range all {
		if ev.Terminal != nil {
			terminal = ev
		}
	}
	if terminal.Terminal == nil || terminal.Terminal.Outcome != agentstream.OutcomeError {
		t.Fatalf("terminal = %+v, want error", terminal.Terminal)
	}
	if terminal.Body == nil || terminal.Body.Error == nil || terminal.Body.Error.Message != "no model selected" {
		t.Fatalf("terminal ProviderEvent = %+v, want error message %q", terminal.Body, "no model selected")
	}
}

func TestProcProcessExitFailsActiveTurn(t *testing.T) {
	t.Parallel()
	script := `#!/usr/bin/env python3
import json, sys
def out(obj): print(json.dumps(obj), flush=True)
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("type") == "get_state":
        out({"type":"response","command":"get_state","success":True,"data":{"sessionId":"s"}})
    elif msg.get("type") == "prompt":
        out({"type":"response","command":"prompt","success":True})
        out({"type":"agent_start"})
        sys.exit(3)  # dies mid-run without a terminal
`
	fake := writeFakePi(t, script)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := collectEvents(t, proc, time.Second)

	if _, err := proc.Write([]byte("hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	all := drainUntilTerminal(t, events, 10*time.Second)
	var terminal testEmission
	for _, ev := range all {
		if ev.Terminal != nil {
			terminal = ev
		}
	}
	if terminal.Terminal == nil || terminal.Terminal.Outcome != agentstream.OutcomeError {
		t.Fatalf("terminal = %+v, want error", terminal.Terminal)
	}
}

// TestProcCloseSuppressesProcessExitFailure covers intentional teardown: Close
// kills the provider mid-run during session stop/restart/reconfigure, and
// readLoop must not turn that into a spurious "pi process exited" failure —
// the session side ends the turn itself (Kill -> abortTurn). The counterpart
// self-exit case is TestProcProcessExitFailsActiveTurn.
func TestProcCloseSuppressesProcessExitFailure(t *testing.T) {
	t.Parallel()
	script := `#!/usr/bin/env python3
import json, sys
def out(obj): print(json.dumps(obj), flush=True)
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("type") == "get_state":
        out({"type":"response","command":"get_state","success":True,"data":{"sessionId":"s"}})
    elif msg.get("type") == "prompt":
        out({"type":"response","command":"prompt","success":True})
        out({"type":"agent_start"})
        # Hang: no terminal event. Intentional Close must not produce a
        # "pi process exited" failure.
`
	fake := writeFakePi(t, script)
	proc, outCh := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := collectEvents(t, proc, time.Second)

	if _, err := proc.Write([]byte("hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Wait until agent_start armed the run before closing, so the exit would
	// have been reported without the suppression.
	waitRunActive(t, proc, 5*time.Second)

	// Intentional teardown with a live run.
	if err := proc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// readLoop closes outCh after deciding whether to emit a failure, so once
	// outCh is closed any failure event would already be queued.
	for range outCh {
	}

	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				t.Fatalf("intentional Close emitted a turn failure: %+v", ev)
			}
			if ev.Diagnostic != nil && strings.Contains(ev.Diagnostic.Message, "process exited") {
				t.Fatalf("intentional Close emitted a process-exit diagnostic: %q", ev.Diagnostic.Message)
			}
		default:
			return
		}
	}
}

func TestProcToolExecutionEvents(t *testing.T) {
	t.Parallel()
	script := `#!/usr/bin/env python3
import json, sys
def out(obj): print(json.dumps(obj), flush=True)
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("type") == "get_state":
        out({"type":"response","command":"get_state","success":True,"data":{"sessionId":"s"}})
    elif msg.get("type") == "prompt":
        out({"type":"response","command":"prompt","success":True})
        out({"type":"agent_start"})
        out({"type":"tool_execution_start","toolCallId":"call_1","toolName":"read","args":{"path":"/tmp/x"}})
        out({"type":"tool_execution_update","toolCallId":"call_1","toolName":"read","partialResult":{"content":[{"type":"text","text":"partial out"}]}})
        out({"type":"tool_execution_end","toolCallId":"call_1","toolName":"read","result":{"content":[{"type":"text","text":"total output"}]},"isError":False})
        out({"type":"agent_end","messages":[],"willRetry":False})
        out({"type":"agent_settled"})
`
	fake := writeFakePi(t, script)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := collectEvents(t, proc, time.Second)

	if _, err := proc.Write([]byte("hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	all := drainUntilTerminal(t, events, 10*time.Second)

	var started, updated, completed *agentstream.Tool
	for _, ev := range all {
		if ev.Body == nil || ev.Body.Tool == nil {
			continue
		}
		switch ev.Body.ToolPhase {
		case agentstream.ToolPhaseStarted:
			started = ev.Body.Tool
		case agentstream.ToolPhaseUpdated:
			updated = ev.Body.Tool
		case agentstream.ToolPhaseCompleted:
			completed = ev.Body.Tool
		}
	}
	if started == nil || started.Name != "read" {
		t.Fatalf("tool started = %+v, want read/running", started)
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(started.Input, &input); err != nil || input.Path != "/tmp/x" {
		t.Fatalf("tool input = %s, want path /tmp/x", started.Input)
	}
	if updated == nil || updated.OutputPreview != "partial out" {
		t.Fatalf("tool updated = %+v, want output preview %q", updated, "partial out")
	}
	if completed == nil || completed.OutputPreview != "total output" {
		t.Fatalf("tool completed = %+v, want completed with output preview", completed)
	}
}

func TestProcRespondPermissionRejected(t *testing.T) {
	fake := writeFakePi(t, fakePiScript)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	if err := proc.RespondPermission("p1", "allow"); err == nil {
		t.Fatal("RespondPermission succeeded, want unsupported error")
	}
	if err := proc.RespondPermissionMulti("p1", []string{"a"}); err == nil {
		t.Fatal("RespondPermissionMulti succeeded, want unsupported error")
	}
}

func TestExtractText(t *testing.T) {
	m := agentMessage{
		Role: "assistant",
		Content: []agentContentBlock{
			{Type: "thinking", Text: "hidden"},
			{Type: "text", Text: "visible "},
			{Type: "text", Text: "answer"},
		},
	}
	if got, want := extractText(m), "visible answer"; got != want {
		t.Fatalf("extractText = %q, want %q", got, want)
	}
}

func TestRPCCommandMarshalsWithTypeField(t *testing.T) {
	data, err := json.Marshal(rpcCommand{Type: "prompt", Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, map[string]string{"type": "prompt", "message": "hi"}) {
		t.Fatalf("marshaled command = %v", m)
	}
}

// fakeSteerPiScript keeps a run open after prompt until a steer arrives.
// Steer message "REJECT" -> success:false; "SILENT" -> no reply; anything else
// is accepted (id echoed), folded into the run and the run settles once.
const fakeSteerPiScript = `#!/usr/bin/env python3
import json, sys
def out(obj): print(json.dumps(obj), flush=True)
for line in sys.stdin:
    msg = json.loads(line)
    typ = msg.get("type")
    if typ == "get_state":
        out({"type":"response","command":"get_state","success":True,"data":{"sessionId":"sess-steer"}})
    elif typ == "prompt":
        out({"type":"response","command":"prompt","success":True})
        out({"type":"agent_start"})
    elif typ == "steer":
        m = msg.get("message","")
        if m == "SILENT":
            continue
        if m == "REJECT":
            out({"type":"response","id":msg.get("id"),"command":"steer","success":False,"error":"not streaming"})
            continue
        out({"type":"response","id":msg.get("id"),"command":"steer","success":True})
        out({"type":"queue_update","steering":[m],"followUp":[]})
        out({"type":"message_start","message":{"id":"m2","role":"assistant","content":[]}})
        out({"type":"message_end","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"steered:"+m}]}})
        out({"type":"agent_settled"})
    elif typ == "abort":
        out({"type":"response","command":"abort","success":True})
        out({"type":"agent_settled"})
`

func startSteerTurn(t *testing.T, token string) (*Proc, chan testEmission) {
	t.Helper()
	fake := writeFakePi(t, fakeSteerPiScript)
	proc, _ := startTestProc(t, StartOptions{Cwd: t.TempDir(), Executable: fake})
	events := make(chan testEmission, 64)
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: token, Input: session.TextUserInput("long task")}, func(e testProducerEmission) bool {
		events <- captureEmission(e)
		return false
	}); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}
	waitRunActive(t, proc, 10*time.Second)
	return proc, events
}

func TestSteerTurnQueuedThenSingleTerminal(t *testing.T) {
	proc, events := startSteerTurn(t, "turn-steer")
	if err := proc.SteerTurn(context.Background(), session.TurnSteerRequest{TurnToken: "turn-steer", Input: session.TextUserInput("fix it")}); err != nil {
		t.Fatalf("SteerTurn: %v", err)
	}
	all := drainUntilTerminal(t, events, 10*time.Second)
	var result string
	for _, ev := range all {
		if ev.Body != nil && ev.Body.Result != nil {
			result = ev.Body.Result.Text
		}
	}
	if result != "steered:fix it" {
		t.Fatalf("result = %q", result)
	}
	select {
	case ev := <-events:
		if ev.Terminal != nil {
			t.Fatalf("second terminal: %+v", ev)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSteerTurnRejectedIsUnavailable(t *testing.T) {
	proc, _ := startSteerTurn(t, "turn-rej")
	err := proc.SteerTurn(context.Background(), session.TurnSteerRequest{TurnToken: "turn-rej", Input: session.TextUserInput("REJECT")})
	if !errors.Is(err, session.ErrSteerUnavailable) || errors.Is(err, session.ErrSteerOutcomeUnknown) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestSteerTurnTimeoutIsOutcomeUnknown(t *testing.T) {
	proc, _ := startSteerTurn(t, "turn-silent")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := proc.SteerTurn(ctx, session.TurnSteerRequest{TurnToken: "turn-silent", Input: session.TextUserInput("SILENT")})
	if !errors.Is(err, session.ErrSteerOutcomeUnknown) {
		t.Fatalf("err = %v, want outcome unknown", err)
	}
	proc.pendingMu.Lock()
	n := len(proc.pending)
	proc.pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("pending replies leaked: %d", n)
	}
}

func TestSteerTurnUnavailableCases(t *testing.T) {
	proc, _ := startSteerTurn(t, "turn-a")
	cases := []session.TurnSteerRequest{
		{TurnToken: "turn-other", Input: session.TextUserInput("x")},
		{TurnToken: "turn-a", Input: session.TextUserInput("   ")},
	}
	for _, req := range cases {
		if err := proc.SteerTurn(context.Background(), req); !errors.Is(err, session.ErrSteerUnavailable) {
			t.Fatalf("SteerTurn(%+v) = %v, want unavailable", req, err)
		}
	}
	idle := &Proc{}
	if err := idle.SteerTurn(context.Background(), session.TurnSteerRequest{TurnToken: "t", Input: session.TextUserInput("x")}); !errors.Is(err, session.ErrSteerUnavailable) {
		t.Fatalf("idle steer = %v", err)
	}
}
