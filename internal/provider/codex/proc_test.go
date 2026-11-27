package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessioninput"
)

func startTestProc(t *testing.T, opts StartOptions) (*Proc, <-chan []byte, error) {
	t.Helper()
	if opts.IdleCompletionDelay <= 0 {
		opts.IdleCompletionDelay = 20 * time.Millisecond
	}
	if opts.WatchdogTimeoutUnit <= 0 {
		opts.WatchdogTimeoutUnit = 100 * time.Millisecond
	}
	if opts.WatchdogInterruptTimeout <= 0 {
		opts.WatchdogInterruptTimeout = 20 * time.Millisecond
	}
	return Start(context.Background(), opts)
}

func TestCodexArgsPlacesExtrasBeforeSubcommand(t *testing.T) {
	t.Parallel()
	got := appServerArgs([]string{"--model", "opus", "-s", "workspace-write"}, "app-server")
	want := []string{"--model", "opus", "-s", "workspace-write", "app-server"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("appServerArgs() = %v, want %v", got, want)
	}

	got = appServerArgs(nil, "")
	if !reflect.DeepEqual(got, []string{"app-server"}) {
		t.Fatalf("appServerArgs(nil, \"\") = %v, want [app-server]", got)
	}
}

func TestNormalizeWatchdogConfigUsesProviderDefaults(t *testing.T) {
	input := config.StructuredProviderConfig{
		TurnIdleTimeoutSeconds: -1,
		TurnHardCapSeconds:     -1,
		ToolIdleTimeoutSeconds: -1,
		RepeatToolCallLimit:    -1,
	}
	codex := normalizeWatchdogConfig("codex", input)
	if codex.TurnIdleTimeoutSeconds != 900 || codex.TurnHardCapSeconds != 21600 || codex.ToolIdleTimeoutSeconds != 3600 || codex.RepeatToolCallLimit != 50 {
		t.Fatalf("codex defaults = %+v, want 900/21600/3600/50", codex)
	}
	clamped := normalizeWatchdogConfig("codex", config.StructuredProviderConfig{RepeatToolCallLimit: 5000})
	if clamped.RepeatToolCallLimit != 1000 {
		t.Fatalf("repeat tool call limit = %d, want clamp 1000", clamped.RepeatToolCallLimit)
	}
}

func TestRepeatToolCallKeyUsesCallDefiningPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a    threadItem
		b    threadItem
	}{
		{name: "command", a: threadItem{Type: "commandExecution", Command: "go test ./a", Cwd: "/tmp"}, b: threadItem{Type: "commandExecution", Command: "go test ./b", Cwd: "/tmp"}},
		{name: "file change", a: threadItem{Type: "fileChange", Changes: []fileUpdateChange{{Path: "a.go", Diff: "+a"}}}, b: threadItem{Type: "fileChange", Changes: []fileUpdateChange{{Path: "b.go", Diff: "+b"}}}},
		{name: "mcp arguments", a: threadItem{Type: "mcpToolCall", Server: "amux", Tool: "run", Arguments: json.RawMessage(`{"task":"a"}`)}, b: threadItem{Type: "mcpToolCall", Server: "amux", Tool: "run", Arguments: json.RawMessage(`{"task":"b"}`)}},
		{name: "dynamic namespace", a: threadItem{Type: "dynamicToolCall", Namespace: "one", Tool: "update_plan", Arguments: json.RawMessage(`{"done":true}`)}, b: threadItem{Type: "dynamicToolCall", Namespace: "two", Tool: "update_plan", Arguments: json.RawMessage(`{"done":true}`)}},
		{name: "collab target", a: threadItem{Type: "collabAgentToolCall", AgentNickname: "alpha", AgentRole: "reviewer"}, b: threadItem{Type: "collabAgentToolCall", AgentNickname: "beta", AgentRole: "reviewer"}},
		{name: "web query", a: threadItem{Type: "webSearch", Query: "first"}, b: threadItem{Type: "webSearch", Query: "second"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if repeatToolCallKey(tt.a) == repeatToolCallKey(tt.b) {
				t.Fatalf("different %s payloads produced the same repeat key", tt.name)
			}
			tt.a.ID, tt.a.Status = "other-id", "completed"
			if repeatToolCallKey(tt.a) != repeatToolCallKey(threadItemWithoutLifecycle(tt.a)) {
				t.Fatalf("%s repeat key included lifecycle-only fields", tt.name)
			}
		})
	}
}

func threadItemWithoutLifecycle(item threadItem) threadItem {
	item.ID = ""
	item.Status = ""
	return item
}

func TestDecodeThreadMetadataIncludesProviderConfirmedModel(t *testing.T) {
	threadID, model, err := decodeThreadMetadata(json.RawMessage(`{"thread":{"id":" thread-1 "},"model":" example-model "}`))
	if err != nil {
		t.Fatalf("decodeThreadMetadata: %v", err)
	}
	if threadID != "thread-1" || model != "example-model" {
		t.Fatalf("metadata = %q/%q, want thread-1/example-model", threadID, model)
	}
}

func TestProcDiscoversImageCapabilityAndSendsLocalImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake provider is unix-only")
	}
	fake := writeFakeCodex(t, fakeCodexImageScript)
	proc, _, err := startTestProc(t, StartOptions{Cwd: t.TempDir(), CodexExecutable: fake})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	modalities, source := proc.InputCapabilities()
	if strings.Join(modalities, ",") != "text,image" || source != "codex:model/list" {
		t.Fatalf("modalities=%v source=%q", modalities, source)
	}
	if err := proc.SubmitInput(session.UserInput{Parts: []session.InputPart{
		{Text: "inspect"},
		{Image: &session.ImageInput{Data: []byte("image-bytes"), MIMEType: "image/png"}},
	}}); err != nil {
		t.Fatalf("SubmitInput: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		proc.stagingMu.Lock()
		stagingDir := proc.stagingDir
		proc.stagingMu.Unlock()
		if stagingDir == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("image staging directory was not removed after turn completion")
}

func TestProcSendsInlineImagesForLinuxDAC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake provider is unix-only")
	}
	fake := writeFakeCodex(t, fakeCodexImageScript)
	stagingRoot := t.TempDir()
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Env:             append(os.Environ(), "TMPDIR="+stagingRoot, "AMUX_TEST_EXPECT_INLINE_IMAGES=1"),
		LaunchContext:   isolation.LaunchContext{Backend: principal.BackendLinuxDAC},
		ResumeSessionID: "t-image",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	input := session.UserInput{Parts: []session.InputPart{
		{Text: "inspect"},
		{Image: &session.ImageInput{Data: bytes.Repeat([]byte{1}, sessioninput.MaxImageBytes), MIMEType: "image/png"}},
		{Text: "and"},
		{Image: &session.ImageInput{Data: bytes.Repeat([]byte{2}, sessioninput.MaxImageBytes), MIMEType: " image/jpeg "}},
	}}
	if err := proc.SubmitInput(input); err != nil {
		t.Fatalf("SubmitInput: %v", err)
	}
	if got := waitFor(t, out, 10*time.Second); got != "image received" {
		t.Fatalf("delta = %q, want image received", got)
	}
	entries, err := os.ReadDir(stagingRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging root should be empty, entries=%v err=%v", entries, err)
	}
}

func TestProcStreamsDeltaAndCompletesTurn(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "turn-1",
		Input:     session.TextUserInput("hello"),
	}, captureEventSink(events)); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}

	boundary := waitForEmissionKind(t, events, string(agentstream.KindAssistantMessage), 3*time.Second)
	if boundary == nil || boundary.Body == nil {
		t.Fatal("did not observe assistant_text boundary event")
	}
	if boundary.Body.ID != "i1" {
		t.Fatalf("assistant boundary id = %q, want i1", boundary.Body.ID)
	}

	gotDelta := waitFor(t, out, 3*time.Second)
	if gotDelta != "hello from fake" {
		t.Fatalf("delta = %q, want %q", gotDelta, "hello from fake")
	}

	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("did not observe turn_done state change")
	}
}

func TestProcDiscoversRefreshesAndInvokesSkills(t *testing.T) {
	t.Parallel()
	for _, providerName := range testAppServerProviders() {
		t.Run(providerName, func(t *testing.T) {
			fake := writeFakeCodex(t, fakeCodexSkillsScript)
			cwd := t.TempDir()
			proc, _, err := Start(context.Background(), StartOptions{
				Cwd:             cwd,
				CodexExecutable: fake,
				ProviderName:    providerName,
				DiscoverSkills:  true,
				ExcludedSkillRoots: []string{
					filepath.Join(cwd, ".agents", "skills", "profile-only"),
				},
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer proc.Close()

			events := make(chan testEmission, 16)
			proc.SetEventSink(captureEventSink(events))
			deadline := time.After(3 * time.Second)
			for {
				select {
				case ev := <-events:
					pe := ev.Body
					if pe == nil || pe.Type != string(agentstream.KindCommandsReplaced) || len(pe.AvailableCommands) != 1 {
						continue
					}
					command := pe.AvailableCommands[0]
					if pe.Provider == providerName && command.Name == "code-review" && command.Description == "Updated review workflow" && command.Kind == "skill" {
						goto refreshed
					}
				case <-deadline:
					t.Fatalf("timed out waiting for refreshed %s skills", providerName)
				}
			}

		refreshed:
			if _, err := proc.Write([]byte("$code-review inspect changes\r")); err != nil {
				t.Fatalf("Write skill: %v", err)
			}
			if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
				t.Fatal("skill turn did not complete")
			}
		})
	}
}

func TestProcCompactAndClearControls(t *testing.T) {
	t.Parallel()
	for _, providerName := range testAppServerProviders() {
		t.Run(providerName, func(t *testing.T) {
			fake := writeFakeCodex(t, fakeCodexControlsScript)
			proc, _, err := Start(context.Background(), StartOptions{
				Cwd:             t.TempDir(),
				CodexExecutable: fake,
				ProviderName:    providerName,
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer proc.Close()

			events := make(chan testEmission, 32)
			proc.SetEventSink(captureEventSink(events))
			if err := proc.Control("compact"); err != nil {
				t.Fatalf("Control(compact): %v", err)
			}
			if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
				t.Fatal("compact did not complete through the native turn lifecycle")
			}

			if err := proc.Control("clear"); err != nil {
				t.Fatalf("Control(clear): %v", err)
			}
			var sawNewThread, sawClearDone bool
			deadline := time.After(3 * time.Second)
			for !sawNewThread || !sawClearDone {
				select {
				case ev := <-events:
					sawNewThread = sawNewThread || ev.Metadata.AgentSessionID == "thread-2"
					sawClearDone = sawClearDone || ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess
				case <-deadline:
					t.Fatalf("clear events: new thread=%v turn_done=%v", sawNewThread, sawClearDone)
				}
			}

			if _, err := proc.Write([]byte("after clear\r")); err != nil {
				t.Fatalf("Write after clear: %v", err)
			}
			if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
				t.Fatal("turn on the new thread did not complete")
			}
		})
	}
}

func TestProcEmitsAssistantDeltaEventBeforeTurnDone(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var sawDelta bool
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("did not observe turn_done")
		case ev := <-events:
			if ev.Body != nil && ev.Body.Type == string(agentstream.KindAssistantDelta) {
				sawDelta = true
				if got := string([]byte(ev.Text)); got != "hello from fake" {
					t.Fatalf("assistant delta data = %q, want hello from fake", got)
				}
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				if !sawDelta {
					t.Fatal("turn_done arrived before structured assistant delta")
				}
				return
			}
		}
	}
}

func TestProcPreservesBurstAssistantDeltasInOrder(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexBurstDeltaScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 1024)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var got strings.Builder
	deltaCount := 0
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("did not observe turn_done")
		case ev := <-events:
			if ev.Body != nil && ev.Body.Type == string(agentstream.KindAssistantDelta) {
				deltaCount++
				got.Write([]byte(ev.Text))
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				var want strings.Builder
				for i := 0; i < 600; i++ {
					want.WriteString(fmt.Sprintf("%03d|", i))
				}
				if deltaCount != 600 || got.String() != want.String() {
					t.Fatalf("assistant deltas = %d bytes=%d, want 600 ordered deltas (%d bytes)", deltaCount, got.Len(), want.Len())
				}
				return
			}
		}
	}
}

func TestProcCanSendExperimentalInitializedHandshake(t *testing.T) {
	t.Parallel()
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	fake := writeFakeCodex(t, fakeCodexResumeScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		ResumeSessionID: "existing-thread",
		CodexExecutable: fake,
		Env:             append(os.Environ(), "AMUX_FAKE_CODEX_CAPTURE="+capture),
		ExperimentalAPI: true,
		SendInitialized: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !bytes.Contains(data, []byte(`"experimentalApi": true`)) && !bytes.Contains(data, []byte(`"experimentalApi":true`)) {
		t.Fatalf("capture missing experimentalApi capability:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`"method": "initialized"`)) && !bytes.Contains(data, []byte(`"method":"initialized"`)) {
		t.Fatalf("capture missing initialized notification:\n%s", data)
	}
}

func TestProcEmitsThreadIDAsAgentSessionID(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	proc.SetEventSink(captureEventSink(events))

	select {
	case ev := <-events:
		if ev.Metadata.AgentSessionID != "t1" || ev.Origin.Channel != "agent" {
			t.Fatalf("event = %+v, want AgentSessionID t1 from agent", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive AgentSessionID event")
	}
}

func TestProcResumesThreadWhenResumeSessionIDProvided(t *testing.T) {
	t.Parallel()
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	fake := writeFakeCodex(t, fakeCodexResumeScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:                   t.TempDir(),
		ResumeSessionID:       "existing-thread",
		ResumeModel:           "example-model",
		ResumeReasoningEffort: "xhigh",
		CodexExecutable:       fake,
		Env:                   append(os.Environ(), "AMUX_FAKE_CODEX_CAPTURE="+capture),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	proc.SetEventSink(captureEventSink(events))
	select {
	case ev := <-events:
		if ev.Metadata.AgentSessionID != "existing-thread" || ev.Origin.Channel != "agent" {
			t.Fatalf("event = %+v, want resumed AgentSessionID existing-thread from agent", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive AgentSessionID event")
	}
	if err := proc.ReconfigureExecution("replacement-model", "high"); err != nil {
		t.Fatalf("ReconfigureExecution: %v", err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Metadata != nil && ev.Metadata.AgentSessionID == "existing-thread" && ev.Metadata.EffectiveModel == "replacement-model" {
				goto gotReconfigureMetadata
			}
		case <-deadline:
			t.Fatal("did not receive SessionMetadataChanged event with EffectiveModel=replacement-model after ReconfigureExecution")
		}
	}
gotReconfigureMetadata:
	if gotTID, gotModel := proc.snapshotThreadMetadata(); gotTID != "existing-thread" || gotModel != "replacement-model" {
		t.Fatalf("snapshotThreadMetadata() = (%q, %q), want (existing-thread, replacement-model)", gotTID, gotModel)
	}
	if _, err := proc.Write([]byte("continue\r")); err != nil {
		t.Fatalf("write resumed turn: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("resumed turn did not complete")
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !bytes.Contains(data, []byte(`"method": "thread/resume"`)) && !bytes.Contains(data, []byte(`"method":"thread/resume"`)) {
		t.Fatalf("capture missing thread/resume:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`"threadId": "existing-thread"`)) && !bytes.Contains(data, []byte(`"threadId":"existing-thread"`)) {
		t.Fatalf("capture missing resume thread id:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`"model": "example-model"`)) && !bytes.Contains(data, []byte(`"model":"example-model"`)) {
		t.Fatalf("capture missing resume model:\n%s", data)
	}
	for _, want := range []string{`"method":"thread/settings/update"`, `"model":"replacement-model"`, `"effort":"high"`, `"method":"turn/start"`} {
		if !bytes.Contains(bytes.ReplaceAll(data, []byte(" "), nil), []byte(want)) {
			t.Fatalf("capture missing %s:\n%s", want, data)
		}
	}
}

func TestProcInterruptUsesThreadAndTurnID(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexInterruptScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("did not observe turn_done")
	}
	// turnID is cleared on turn/completed; second write spins up a new turn.
	go func() {
		_, _ = proc.Write([]byte("again\r"))
	}()
	waitForNativeTurnID(t, proc, 2*time.Second)
	if err := proc.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}

func TestProcInterruptEmitsOutcomeInterrupted(t *testing.T) {
	t.Parallel()
	const script = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_counter = 0
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        turn_counter += 1
        turn_id = "u%d" % turn_counter
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        if turn_counter == 2:
            # Provider-initiated interrupted status without client Interrupt().
            print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id,"status":"interrupted"}}}), flush=True)
    elif method == "turn/interrupt":
        tid = msg["params"]["turnId"]
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":tid}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`
	fake := writeFakeCodex(t, script)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	// Turn 1: interrupted via proc.Interrupt() before turn/completed without status.
	if _, err := proc.Write([]byte("wait\r")); err != nil {
		t.Fatalf("Write turn 1: %v", err)
	}
	waitForNativeTurnID(t, proc, 2*time.Second)
	if err := proc.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeInterrupted, 3*time.Second) {
		t.Fatal("expected OutcomeInterrupted after proc.Interrupt()")
	}

	// Turn 2: turn/completed carries turn.status == "interrupted".
	if _, err := proc.Write([]byte("second\r")); err != nil {
		t.Fatalf("Write turn 2: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeInterrupted, 3*time.Second) {
		t.Fatal("expected OutcomeInterrupted for turn/completed status=interrupted")
	}
}

func TestProcApprovalRoundTrip(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexApprovalScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	proc.SetEventSink(captureEventSink(events))

	go func() { _, _ = proc.Write([]byte("run\r")) }()

	permID := waitForPermission(t, events, 3*time.Second)
	if permID == "" {
		t.Fatal("did not receive permission event")
	}
	if err := proc.RespondPermission(permID, optAccept); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn did not complete after approval")
	}
}

// TestProcDispatchTurnSendsMultiLineInputAsSingleTurn guards the regression where
// the provider split a multi-line user submission by newline into one
// turn/start per line. The Feishu thread-context wrapper renders a multi-line
// prompt; line-framing made the model see a bare `[Amux Feishu Thread Mode]`
// line as its own turn and reply "no content". The input boundary is one
// DispatchTurn call, so the whole value must become one turn/start.
func TestProcDispatchTurnSendsMultiLineInputAsSingleTurn(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "turns.txt")
	t.Setenv("AMUX_FAKE_CODEX_CAPTURE_TURNS", capture)
	fake := writeFakeCodex(t, fakeCodexCaptureTurnsScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	multiline := "  [Amux Feishu Thread Mode]\nUse it only as background context.\n[User request]\n当前sessionid 是什么  \n"
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-multiline", Input: session.TextUserInput(multiline)}, func(testProducerEmission) bool { return false }); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}

	// Wait briefly for the fake to record the turn, then inspect captures.
	deadline := time.After(3 * time.Second)
	for {
		if got, _ := os.ReadFile(capture); len(got) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for turn/start to be recorded")
		case <-time.After(10 * time.Millisecond):
		}
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	turnCount := bytes.Count(got, []byte("===TURN==="))
	if turnCount != 1 {
		t.Fatalf("expected exactly 1 turn/start, got %d: %q", turnCount, string(got))
	}
	// Extract the single turn's text between markers.
	start := bytes.Index(got, []byte("===TURN===\n")) + len("===TURN===\n")
	end := bytes.Index(got[start:], []byte("\n===ENDTURN==="))
	turnText := string(got[start : start+end])
	if turnText != multiline {
		t.Fatalf("turn text = %q, want full multi-line %q", turnText, multiline)
	}
}

func writeFakeCodex(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake codex is unix-only")
	}
	// Resolve the fixture interpreter before the provider applies its launch
	// environment, which may intentionally omit PATH.
	if strings.HasPrefix(script, "#!/usr/bin/env python3\n") {
		python, err := exec.LookPath("python3")
		if err != nil {
			t.Fatalf("find fixture Python: %v", err)
		}
		script = "#!" + python + "\n" + strings.TrimPrefix(script, "#!/usr/bin/env python3\n")
	}
	path := filepath.Join(t.TempDir(), "fake-codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	return path
}

func waitFor(t *testing.T, ch <-chan []byte, d time.Duration) string {
	t.Helper()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatal("output channel closed")
		}
		return string(b)
	case <-time.After(d):
		t.Fatal("timed out waiting for delta")
		return ""
	}
}

func captureEventSink(ch chan<- testEmission) func(testProducerEmission) bool {
	return func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		ch <- ev
		return false
	}
}

func waitForOutcome(t *testing.T, ch <-chan testEmission, outcome agentstream.Outcome, d time.Duration) bool {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev := <-ch:
			if ev.Terminal != nil && ev.Terminal.Outcome == outcome {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func waitForNativeTurnID(t *testing.T, proc *Proc, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if proc.snapshotTurnID() != "" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("native turn id was not observed")
}

// collectUntilOutcome drains events until a terminal state change with State==state
// (or timeout) and returns every event seen INCLUDING the terminal one. Unlike
// waitForOutcome it does not discard the events preceding the terminal, so a test
// can assert on events (result, permission) that are emitted just before the
// state change rather than racing a post-wait drain that already consumed them.
func collectUntilOutcome(t *testing.T, ch <-chan testEmission, outcome agentstream.Outcome, d time.Duration) ([]testEmission, bool) {
	t.Helper()
	timeout := time.After(d)
	var got []testEmission
	for {
		select {
		case ev := <-ch:
			got = append(got, ev)
			if ev.Terminal != nil && ev.Terminal.Outcome == outcome {
				return got, true
			}
		case <-timeout:
			return got, false
		}
	}
}

func waitForPermission(t *testing.T, ch <-chan testEmission, d time.Duration) string {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == agentstream.KindPermissionRequested && ev.Permission.ID != "" {
				return ev.Permission.ID
			}
		case <-timeout:
			return ""
		}
	}
}

func waitForEmissionKind(t *testing.T, ch <-chan testEmission, kind string, d time.Duration) *testEmission {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev := <-ch:
			if ev.Body != nil && ev.Body.Type == kind {
				return &ev
			}
		case <-timeout:
			return nil
		}
	}
}

// waitForPermissions collects up to n distinct permission ids within d.
func waitForPermissions(t *testing.T, ch <-chan testEmission, n int, d time.Duration) []string {
	t.Helper()
	deadline := time.After(d)
	seen := make(map[string]bool)
	var out []string
	for len(out) < n {
		select {
		case ev := <-ch:
			if ev.Kind == agentstream.KindPermissionRequested && ev.Permission.ID != "" && !seen[ev.Permission.ID] {
				seen[ev.Permission.ID] = true
				out = append(out, ev.Permission.ID)
			}
		case <-deadline:
			return out
		}
	}
	return out
}

// waitForPermissionEvent waits for a permission request event and returns the
// full event (including the provider-labelled ProviderEvent) so callers can
// assert the provider label alongside the permission id.
func waitForPermissionEvent(t *testing.T, ch <-chan testEmission, d time.Duration) *testEmission {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == agentstream.KindPermissionRequested && ev.Permission.ID != "" {
				return &ev
			}
		case <-timeout:
			return nil
		}
	}
}

const fakeCodexImageScript = `#!/usr/bin/env python3
import base64, json, os, sys, time
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":"t-image"},"model":"vision-model"}}), flush=True)
    elif method == "thread/resume":
        assert msg["params"]["threadId"] == "t-image"
        assert msg["params"]["excludeTurns"] is True
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":"t-image"},"model":"vision-model"}}), flush=True)
    elif method == "model/list":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"data":[{"id":"vision-model","model":"vision-model","inputModalities":["text","image"]}]}}), flush=True)
    elif method == "turn/start":
        inputs = msg["params"]["input"]
        assert inputs[0] == {"type":"text","text":"inspect"}
        path = None
        if os.getenv("AMUX_TEST_EXPECT_INLINE_IMAGES") == "1":
            assert len(inputs) == 4
            assert inputs[2] == {"type":"text","text":"and"}
            for part, mime, fill in [(inputs[1], "image/png", 1), (inputs[3], "image/jpeg", 2)]:
                assert part["type"] == "image"
                header, payload = part["url"].split(",", 1)
                assert header == "data:" + mime + ";base64"
                assert base64.b64decode(payload) == bytes([fill]) * (10 * 1024 * 1024)
        else:
            assert len(inputs) == 2
            assert inputs[1]["type"] == "localImage"
            path = inputs[1]["path"]
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":"u-image"}}}), flush=True)
        if path is not None:
            time.sleep(0.1)
            assert os.path.exists(path)
            assert open(path, "rb").read() == b"image-bytes"
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"t-image","turn":{"id":"u-image"}}}), flush=True)
        if path is None:
            print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"t-image","turnId":"u-image","item":{"type":"userMessage","id":"u-input","content":inputs}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"image received","threadId":"t-image","turnId":"u-image","itemId":"i-image"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"t-image","turn":{"id":"u-image"}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        assert msg["params"]["clientInfo"]["name"] == "amux"
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        assert msg["params"]["threadId"] == thread_id
        assert msg["params"]["input"][0]["text"] == "hello"
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"hello from fake","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexSkillsScript = `#!/usr/bin/env python3
import json, sys
thread_id = "skills-thread"
turn_id = "skills-turn"
skills_calls = 0
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "skills/list":
        skills_calls += 1
        assert len(msg["params"]["cwds"]) == 1
        if skills_calls == 1:
            assert msg["params"]["forceReload"] is False
            skills = [
                {"name":"code-review","description":"Review workflow","enabled":True,"path":"/skills/old/SKILL.md","scope":"user"},
                {"name":"profile-only","description":"Projected profile skill","enabled":True,"path":msg["params"]["cwds"][0] + "/.agents/skills/profile-only/SKILL.md","scope":"repo"},
                {"name":"disabled-skill","description":"Disabled","enabled":False,"path":"/skills/disabled/SKILL.md","scope":"user"}
            ]
        else:
            assert msg["params"]["forceReload"] is True
            skills = [
                {"name":"code-review","description":"Updated review workflow","enabled":True,"path":"/skills/new/SKILL.md","scope":"repo"},
                {"name":"profile-only","description":"Projected profile skill","enabled":True,"path":msg["params"]["cwds"][0] + "/.agents/skills/profile-only/SKILL.md","scope":"repo"}
            ]
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"data":[{"cwd":msg["params"]["cwds"][0],"skills":skills,"errors":[]}]}}), flush=True)
        if skills_calls == 1:
            print(json.dumps({"jsonrpc":"2.0","method":"skills/changed","params":{}}), flush=True)
    elif method == "turn/start":
        inputs = msg["params"]["input"]
        assert inputs[0] == {"type":"text","text":"$code-review inspect changes"}
        assert inputs[1] == {"type":"skill","name":"code-review","path":"/skills/new/SKILL.md"}
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexControlsScript = `#!/usr/bin/env python3
import json, sys
thread_count = 0
thread_id = ""
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        thread_count += 1
        thread_id = "thread-" + str(thread_count)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "thread/compact/start":
        assert msg["params"]["threadId"] == "thread-1", msg
        turn_id = "compact-turn"
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        item = {"id":"compact-item","type":"contextCompaction"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
    elif method == "turn/start":
        assert msg["params"]["threadId"] == "thread-2", msg
        assert msg["params"]["input"][0]["text"] == "after clear", msg
        turn_id = "new-thread-turn"
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexBurstDeltaScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        for i in range(600):
            delta = f"{i:03d}|"
            print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":delta,"threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexTokenUsageScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        usage = {
            "last": {
                "cachedInputTokens": 3,
                "cacheCreationInputTokens": 2,
                "inputTokens": 100,
                "outputTokens": 20,
                "reasoningOutputTokens": 5,
                "totalTokens": 120
            },
            "total": {
                "cachedInputTokens": 30,
                "cacheCreationInputTokens": 20,
                "inputTokens": 900,
                "outputTokens": 99,
                "reasoningOutputTokens": 50,
                "totalTokens": 999
            },
            "modelContextWindow": 128000,
            "autoCompactTokenLimit": 100000
        }
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":thread_id,"turnId":turn_id,"tokenUsage":usage}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"done","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexIdleThenActiveScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"phase one","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":thread_id}}), flush=True)
        time.sleep(0.1)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"active"},"threadId":thread_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":" phase two","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexForeignChildThreadScript = `#!/usr/bin/env python3
import json, sys
thread_id = "parent-thread"
turn_id = "parent-turn"
child_thread_id = "child-thread"
child_turn_id = "child-turn"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"parent before","threadId":thread_id,"turnId":turn_id,"itemId":"parent-message"}}), flush=True)

        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":child_thread_id,"turn":{"id":child_turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"child output","threadId":child_thread_id,"turnId":child_turn_id,"itemId":"child-message"}}), flush=True)
        child_item = {"id":"child-tool","type":"commandExecution","command":"pwd","status":"completed","aggregatedOutput":"child cwd"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":child_thread_id,"turnId":child_turn_id,"item":child_item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":child_thread_id,"turnId":child_turn_id,"item":child_item}}), flush=True)
        usage = {"last":{"inputTokens":10,"outputTokens":2,"totalTokens":12},"total":{"inputTokens":10,"outputTokens":2,"totalTokens":12}}
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":child_thread_id,"turnId":child_turn_id,"tokenUsage":usage}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":child_thread_id,"turn":{"id":child_turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":child_thread_id}}), flush=True)

        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":" parent after","threadId":thread_id,"turnId":turn_id,"itemId":"parent-message"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexResumeScript = `#!/usr/bin/env python3
import json, os, sys
capture = os.environ.get("AMUX_FAKE_CODEX_CAPTURE")
for line in sys.stdin:
    if capture:
        with open(capture, "a") as f:
            f.write(line)
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/resume":
        assert msg["params"]["threadId"] == "existing-thread", msg
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":"existing-thread"}}}), flush=True)
    elif method == "thread/settings/update":
        assert msg["params"]["threadId"] == "existing-thread", msg
        assert msg["params"]["model"] == "replacement-model", msg
        assert msg["params"]["effort"] == "high", msg
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
    elif method == "turn/start":
        assert msg["params"]["threadId"] == "existing-thread", msg
        assert msg["params"]["model"] == "replacement-model", msg
        assert msg["params"]["effort"] == "high", msg
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"existing-thread","turn":{"id":"resumed-turn"}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"existing-thread","turn":{"id":"resumed-turn"}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":"resumed-turn"}}}), flush=True)
    elif method == "thread/start":
        raise AssertionError("thread/start should not be called when resuming")
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexCaptureTurnsScript = `#!/usr/bin/env python3
import json, os, sys
thread_id = "t1"
turn_id = "u1"
capture = os.environ.get("AMUX_FAKE_CODEX_CAPTURE_TURNS")
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        assert msg["params"]["clientInfo"]["name"] == "amux"
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        assert msg["params"]["threadId"] == thread_id
        text = msg["params"]["input"][0]["text"]
        if capture:
            with open(capture, "a") as f:
                f.write("===TURN===\n" + text + "\n===ENDTURN===\n")
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexInterruptScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_counter = 0
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        turn_counter += 1
        turn_id = "u%d" % turn_counter
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        if turn_counter == 1:
            print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        # On second turn: wait for an interrupt before completing.
    elif method == "turn/interrupt":
        assert msg["params"]["threadId"] == thread_id
        assert msg["params"]["turnId"].startswith("u")
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
        sys.exit(0)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexApprovalScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
pending_turn_id = None
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        pending_turn_id = mid
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        # Ask for command execution approval. Use a request id distinct from
        # the client's id space (any large positive int).
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/commandExecution/requestApproval","params":{"command":"ls"}}), flush=True)
    elif mid == 9001 and "result" in msg:
        assert msg["result"]["decision"] == "accept", msg
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":pending_turn_id,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexRequestUserInputMultiScript sends one multiSelect question and
// asserts the response carries multiple labels.
const fakeCodexRequestUserInputMultiScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
pending_turn_id = None
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        pending_turn_id = mid
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/tool/requestUserInput","params":{"itemId":"it1","threadId":thread_id,"turnId":turn_id,"questions":[{"id":"q1","header":"多选","question":"选哪些？","multiSelect":True,"options":[{"label":"A","description":"a"},{"label":"B","description":"b"},{"label":"C","description":"c"}]}]}}), flush=True)
    elif mid == 9001 and "result" in msg:
        ans = msg["result"]["answers"]["q1"]["answers"]
        assert set(ans) == {"A", "B"}, ans
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":pending_turn_id,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexThreadStatusSystemErrorScript emits thread/status/changed with
// status.type=systemError after turn/started, exercising the v0.1.38
// systemError -> emitTurnFailure normalization.
const fakeCodexThreadStatusSystemErrorScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"systemError"},"threadId":thread_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexTurnFailedScript emits turn/failed after turn/started.
const fakeCodexTurnFailedScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/failed","params":{"threadId":thread_id,"turn":{"id":turn_id},"error":{"message":"boom","code":"internal"}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexTopLevelErrorScript emits a top-level "error" notification after
// turn/started, exercising the v0.1.38 top-level-error -> emitTurnFailure path.
const fakeCodexTopLevelErrorScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"error","params":{"message":" model unavailable ","code":" no_models "}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexRetryableTopLevelErrorScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        for attempt in range(1, 4):
            print(json.dumps({"jsonrpc":"2.0","method":"error","params":{"error":{"message":"stream disconnected - retrying (%d/5)" % attempt,"codexErrorInfo":"responseStreamDisconnected"},"threadId":thread_id,"turnId":turn_id,"willRetry":True}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"recovered response","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexRetryAfterCompletedMessageScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"partial","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        item = {"id":"i1","type":"agentMessage","text":"partial"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"error","params":{"error":{"message":"stream disconnected - retrying (1/5)","codexErrorInfo":"responseStreamDisconnected"},"threadId":thread_id,"turnId":turn_id,"willRetry":True}}), flush=True)
        time.sleep(0.15)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":" recovered","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexWatchdogStuckTurnScript sends turn/started and one assistant delta
// but never emits a terminal event, so the watchdog must terminate the turn.
const fakeCodexWatchdogStuckTurnScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"stuck","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        # Now hang forever; the client-side watchdog should fire.
        sys.stdin.read()
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexWatchdogHardCapScript sends deltas repeatedly but never completes,
// so the hard-cap watchdog must terminate the turn.
const fakeCodexWatchdogHardCapScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        i = 0
        while True:
            print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":f"{i}\n","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
            time.sleep(0.05)
            i += 1
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexWatchdogToolIdleScript starts a commandExecution tool, emits one
// output delta, then hangs so the tool idle watchdog fires.
const fakeCodexWatchdogToolIdleScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        item = {"id":"tool-1","type":"commandExecution","command":"sleep 999","status":"in_progress"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/commandExecution/outputDelta","params":{"delta":"start\n","itemId":"tool-1","threadId":thread_id,"turnId":turn_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        sys.stdin.read()
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexWatchdogActivityResetsScript emits deltas frequently and then
// completes normally, proving the idle timer is reset.
const fakeCodexWatchdogActivityResetsScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        for i in range(8):
            print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":f"{i}","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
            time.sleep(0.05)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexWatchdogLongToolScript emits command output every 50ms for ~500ms
// and then completes, proving the tool idle timer is reset.
const fakeCodexWatchdogLongToolScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        item = {"id":"tool-1","type":"commandExecution","command":"for i in range(8)","status":"in_progress"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        for i in range(8):
            print(json.dumps({"jsonrpc":"2.0","method":"item/commandExecution/outputDelta","params":{"delta":f"{i}\n","itemId":"tool-1","threadId":thread_id,"turnId":turn_id}}), flush=True)
            time.sleep(0.05)
        item_done = {"id":"tool-1","type":"commandExecution","command":"for i in range(8)","status":"completed"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item_done}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexRepeatToolCallsScript exercises the repeat-call watchdog and its
// false-positive boundaries. "loop" emits three identical dynamic tool calls
// without a terminal event. All other modes complete normally.
const fakeCodexRepeatToolCallsScript = `#!/usr/bin/env python3
import json, sys
thread_id = "repeat-thread"
turn_counter = 0

def notify(method, params):
    print(json.dumps({"jsonrpc":"2.0","method":method,"params":params}), flush=True)

def lifecycle(turn_id, item, duplicate_started=False, output=None):
    params = {"threadId":thread_id,"turnId":turn_id,"item":item}
    notify("item/started", params)
    if duplicate_started:
        notify("item/started", params)
        notify("item/started", params)
    done = dict(item, status="completed")
    if output is not None:
        done.update(aggregatedOutput=output, exitCode=0)
    notify("item/completed", {"threadId":thread_id,"turnId":turn_id,"item":done})

for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        turn_counter += 1
        turn_id = "repeat-turn-%d" % turn_counter
        mode = msg["params"]["input"][0]["text"].strip()
        notify("turn/started", {"threadId":thread_id,"turn":{"id":turn_id}})
        if mode == "loop":
            for i in range(3):
                item = {"id":"plan-%d" % i,"type":"dynamicToolCall","namespace":"builtin","tool":"update_plan","status":"inProgress","arguments":{"plan":"done"}}
                notify("item/started", {"threadId":thread_id,"turnId":turn_id,"item":item})
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
            continue
        if mode == "different":
            for i in range(6):
                lifecycle(turn_id, {"id":"command-%d" % i,"type":"commandExecution","command":"echo %d" % i,"cwd":"/tmp","status":"inProgress"})
        elif mode.startswith("polling-"):
            _, output_mode, count = mode.split("-")
            for i in range(int(count)):
                result = {"status":"RUNNING"}
                if output_mode == "progress":
                    result["elapsed_time"] = (i + 1) * 45
                item = {"id":"poll-%d" % i,"type":"commandExecution","command":"sleep 45; cat /tmp/test-job-status.json","cwd":"/tmp","status":"inProgress"}
                lifecycle(turn_id, item, output=json.dumps(result))
        elif mode == "duplicate":
            item = {"id":"one-item","type":"dynamicToolCall","namespace":"builtin","tool":"update_plan","status":"inProgress","arguments":{"plan":"done"}}
            lifecycle(turn_id, item, True)
            notify("item/started", {"threadId":thread_id,"turnId":turn_id,"item":item})
        else:
            count = 6 if mode == "disabled" else 2
            for i in range(count):
                lifecycle(turn_id, {"id":"plan-%d-%d" % (turn_counter, i),"type":"dynamicToolCall","namespace":"builtin","tool":"update_plan","status":"inProgress","arguments":{"plan":"done"}})
        notify("turn/completed", {"threadId":thread_id,"turn":{"id":turn_id}})
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    elif method == "turn/interrupt":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcRepeatToolCallLimitFailsLoopingTurn(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexRepeatToolCallsScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:                      t.TempDir(),
		CodexExecutable:          fake,
		WatchdogInterruptTimeout: 20 * time.Millisecond,
		Watchdog: config.StructuredProviderConfig{
			RepeatToolCallLimit: 3,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("loop\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, ok := collectUntilOutcome(t, events, agentstream.OutcomeError, 2*time.Second)
	if !ok {
		t.Fatal("repeated identical tool calls did not fail the turn")
	}
	failureText := ""
	for _, ev := range got {
		if ev.Diagnostic != nil {
			failureText += string([]byte(ev.Text))
		}
		if ev.Terminal != nil {
			failureText += ev.Terminal.Message
		}
	}
	for _, want := range []string{"update_plan called 3 times", "code=repeat_tool_loop"} {
		if !strings.Contains(failureText, want) {
			t.Fatalf("repeat-loop failure missing %q: %q", want, failureText)
		}
	}
}

func TestProcDefaultRepeatToolCallLimitPollingBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mode    string
		calls   int
		outcome agentstream.Outcome
	}{
		{name: "progressing output below limit", mode: "polling-progress-49", calls: 49, outcome: agentstream.OutcomeSuccess},
		{name: "identical output below limit", mode: "polling-static-49", calls: 49, outcome: agentstream.OutcomeSuccess},
		{name: "progressing output reaches limit", mode: "polling-progress-50", calls: 50, outcome: agentstream.OutcomeError},
		{name: "identical output reaches limit", mode: "polling-static-50", calls: 50, outcome: agentstream.OutcomeError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := writeFakeCodex(t, fakeCodexRepeatToolCallsScript)
			proc, out, err := startTestProc(t, StartOptions{
				Cwd:             t.TempDir(),
				CodexExecutable: fake,
				Watchdog:        config.DefaultStructuredProviderTimeoutsForProvider("codex"),
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer proc.Close()
			go func() {
				for range out {
				}
			}()
			// Write waits for turn/start while the provider can already emit all
			// polling lifecycles, so buffer the full sequence before collecting it.
			events := make(chan testEmission, 2*tt.calls+16)
			proc.SetEventSink(captureEventSink(events))
			if _, err := proc.Write([]byte(tt.mode + "\r")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, ok := collectUntilOutcome(t, events, tt.outcome, 2*time.Second)
			if !ok {
				t.Fatalf("%d polling calls did not finish with %s", tt.calls, tt.outcome)
			}
			started, completed := 0, 0
			for _, ev := range got {
				if ev.Tool != nil && ev.Tool.Name == "command" {
					switch ev.ToolPhase {
					case agentstream.ToolPhaseStarted:
						started++
					case agentstream.ToolPhaseCompleted:
						completed++
					}
				}
				if ev.Terminal != nil && tt.outcome == agentstream.OutcomeError {
					for _, want := range []string{"called 50 times", "code=repeat_tool_loop"} {
						if !strings.Contains(ev.Terminal.Message, want) {
							t.Fatalf("polling failure missing %q: %q", want, ev.Terminal.Message)
						}
					}
				}
			}
			wantCompleted := tt.calls
			if tt.outcome == agentstream.OutcomeError {
				wantCompleted-- // The limit is enforced when the final call starts.
			}
			if started != tt.calls || completed != wantCompleted {
				t.Fatalf("polling lifecycle counts = %d started / %d completed, want %d / %d", started, completed, tt.calls, wantCompleted)
			}
		})
	}
}

func TestProcRepeatToolCallLimitAvoidsFalsePositives(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		limit int
		turns int
	}{
		{name: "different command payloads", mode: "different", limit: 3, turns: 1},
		{name: "duplicate item started frames", mode: "duplicate", limit: 3, turns: 1},
		{name: "disabled", mode: "disabled", limit: 0, turns: 1},
		{name: "new turn resets streak", mode: "reset", limit: 3, turns: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := writeFakeCodex(t, fakeCodexRepeatToolCallsScript)
			proc, out, err := startTestProc(t, StartOptions{
				Cwd:             t.TempDir(),
				CodexExecutable: fake,
				Watchdog: config.StructuredProviderConfig{
					RepeatToolCallLimit: tt.limit,
				},
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer proc.Close()
			go func() {
				for range out {
				}
			}()
			events := make(chan testEmission, 64)
			proc.SetEventSink(captureEventSink(events))
			for i := 0; i < tt.turns; i++ {
				if _, err := proc.Write([]byte(tt.mode + "\r")); err != nil {
					t.Fatalf("Write turn %d: %v", i+1, err)
				}
				if _, ok := collectUntilOutcome(t, events, agentstream.OutcomeSuccess, 2*time.Second); !ok {
					t.Fatalf("turn %d did not complete normally", i+1)
				}
			}
		})
	}
}

func TestProcTurnIdleTimeoutEmitsFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexWatchdogStuckTurnScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:                      t.TempDir(),
		CodexExecutable:          fake,
		WatchdogTimeoutUnit:      20 * time.Millisecond,
		WatchdogInterruptTimeout: 20 * time.Millisecond,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The [system] output frame is emitted before the failed state change, so
	// watch for both in a single loop; consuming one while waiting for the
	// other would race with emit order.
	deadline := time.After(time.Second)
	failedSeen, frameSeen := false, false
	for !failedSeen || !frameSeen {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				failedSeen = true
			}
			if ev.Diagnostic != nil && strings.Contains(string([]byte(ev.Text)), "turn idle timeout") {
				frameSeen = true
			}
		case <-deadline:
			t.Fatalf("turn idle watchdog incomplete: failedSeen=%v frameSeen=%v", failedSeen, frameSeen)
		}
	}
}

func TestProcTurnHardCapTimeoutEmitsFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexWatchdogHardCapScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 0,
			TurnHardCapSeconds:     1,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !waitForOutcome(t, events, agentstream.OutcomeError, 5*time.Second) {
		t.Fatal("did not observe failed state change from turn hard cap watchdog")
	}
}

func TestProcToolIdleTimeoutEmitsFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexWatchdogToolIdleScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 0,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 1,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !waitForOutcome(t, events, agentstream.OutcomeError, 5*time.Second) {
		t.Fatal("did not observe failed state change from tool idle watchdog")
	}
}

func TestProcActivityResetsTurnIdleTimeout(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexWatchdogActivityResetsScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 5*time.Second) {
		t.Fatal("did not observe turn_done; idle watchdog may have fired too early")
	}
}

func TestProcLongToolResetsToolIdleTimeout(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexWatchdogLongToolScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 0,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 1,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 5*time.Second) {
		t.Fatal("did not observe turn_done; tool idle watchdog may have fired too early")
	}
}

// fakeCodexWatchdogSilentToolScript starts a tool, stays silent for 1.5s
// (longer than the test's 1s turn idle timeout but shorter than its 4s tool
// idle timeout), then completes normally. It proves the turn idle timer is
// truly suspended while a tool runs.
const fakeCodexWatchdogSilentToolScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        item = {"id":"tool-1","type":"commandExecution","command":"sleep 1.5","status":"in_progress"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        time.sleep(0.25)
        print(json.dumps({"jsonrpc":"2.0","method":"item/commandExecution/outputDelta","params":{"delta":"done\n","itemId":"tool-1","threadId":thread_id,"turnId":turn_id}}), flush=True)
        item_done = {"id":"tool-1","type":"commandExecution","command":"sleep 1.5","status":"completed"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item_done}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcTurnIdleSuspendedDuringTool(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexWatchdogSilentToolScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 4,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The tool stays silent for 1.5s > turn idle timeout (1s). If the turn
	// idle timer were not suspended while the tool runs, the turn would fail
	// before the tool completes.
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 6*time.Second) {
		t.Fatal("did not observe turn_done; turn idle watchdog fired during a silent tool")
	}
}

func TestProcTurnIdleSuspendedDuringApproval(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexApprovalScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	// turn/start only returns after the approval round trip, so Write blocks
	// until RespondPermission; run it in the background like other approval
	// tests.
	go func() { _, _ = proc.Write([]byte("hello\r")) }()

	permID := waitForPermission(t, events, 3*time.Second)
	if permID == "" {
		t.Fatal("did not receive permission event")
	}
	// Take longer than the turn idle timeout to answer; the watchdog must not
	// fail the turn while the provider is blocked on the user's decision.
	time.Sleep(150 * time.Millisecond)
	if err := proc.RespondPermission(permID, optAccept); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 5*time.Second) {
		t.Fatal("did not observe turn_done; turn idle watchdog fired during an approval wait")
	}
}

// fakeCodexExitMidTurnScript starts a turn then exits immediately, exercising
// the v0.1.38 process-death -> emitTurnFailure finalizer.
const fakeCodexExitMidTurnScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        sys.exit(0)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexZombieTurnScript starts a turn and goes silent; every
// turn/interrupt it receives is appended to $CAPTURE_FILE as
// "interrupt <turnId>".
const fakeCodexZombieTurnScript = `#!/usr/bin/env python3
import json, os, sys
thread_id = "t1"
turn_id = "u1"
capture = os.environ.get("CAPTURE_FILE", "")
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        # Hang: no terminal event. The client-side watchdog should fire.
    elif method == "turn/interrupt":
        if capture:
            with open(capture, "a") as f:
                f.write("interrupt %s\n" % msg["params"]["turnId"])
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func waitForFileLines(t *testing.T, path string, n int, d time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= n && lines[0] != "" {
				return lines
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("capture file %s did not reach %d lines in %v; got %q", path, n, d, string(data))
	return nil
}

// TestProcWatchdogFireInterruptsZombieTurn verifies the watchdog cancels the
// provider-side turn it declares dead; otherwise the zombie turn rejects the
// next turn/start and re-wedges the session.
func TestProcWatchdogFireInterruptsZombieTurn(t *testing.T) {
	t.Parallel()
	capture := filepath.Join(t.TempDir(), "interrupts.txt")
	fake := writeFakeCodex(t, fakeCodexZombieTurnScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Env:             []string{"CAPTURE_FILE=" + capture},
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 8)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeError, 5*time.Second) {
		t.Fatal("watchdog did not fire")
	}
	lines := waitForFileLines(t, capture, 1, 3*time.Second)
	if lines[0] != "interrupt u1" {
		t.Fatalf("zombie interrupt = %q, want %q", lines[0], "interrupt u1")
	}

	// After the watchdog cleared turnID, a user Interrupt must still reach the
	// zombie turn via the zombieTurnID fallback.
	if err := proc.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	lines = waitForFileLines(t, capture, 2, 3*time.Second)
	if lines[1] != "interrupt u1" {
		t.Fatalf("fallback interrupt = %q, want %q", lines[1], "interrupt u1")
	}
}

// fakeCodexTwoTurnsSecondRejectedScript wedges turn 1 (watchdog fires), then
// rejects the second turn/start with a JSON-RPC error.
const fakeCodexTwoTurnsSecondRejectedScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_counter = 0
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        turn_counter += 1
        turn_id = "u%d" % turn_counter
        if turn_counter == 1:
            print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
            # Hang: no terminal event; the watchdog fires client-side.
        else:
            # Provider still busy with the zombie turn: reject the new turn.
            print(json.dumps({"jsonrpc":"2.0","id":mid,"error":{"code":-32000,"message":"turn already active"}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcSendTurnFailureVisibleAfterWatchdogFailure verifies that a new
// submission's turn/start failure is NOT swallowed by the failure dedup of
// the previously watchdog-failed turn: the user must see a [system] frame and
// a failed state for every dropped submission.
func TestProcSendTurnFailureVisibleAfterWatchdogFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexTwoTurnsSecondRejectedScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("first\r")); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeError, 5*time.Second) {
		t.Fatal("watchdog did not fire on first turn")
	}

	// Second submission: provider rejects turn/start. The failure must be
	// visible even though lastFailureTurnID is still set from turn 1.
	_, _ = proc.Write([]byte("second\r"))
	deadline := time.After(5 * time.Second)
	failedSeen, frameSeen := false, false
	for !failedSeen || !frameSeen {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				failedSeen = true
			}
			if ev.Diagnostic != nil && strings.Contains(string([]byte(ev.Text)), "turn/start") {
				frameSeen = true
			}
		case <-deadline:
			t.Fatalf("second submission failure was suppressed: failedSeen=%v frameSeen=%v", failedSeen, frameSeen)
		}
	}
}

// fakeCodexNoProgressToolScript runs a webSearch item with no interim
// notifications (the only events the schema defines for it are started and
// completed), then completes the turn.
const fakeCodexNoProgressToolScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        item = {"id":"ws-1","type":"webSearch","status":"in_progress"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        # A healthy long search: silent for 1.5s, then done.
        time.sleep(0.25)
        item_done = {"id":"ws-1","type":"webSearch","status":"completed"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item_done}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcToolIdleDoesNotKillNoProgressTool verifies that item types without
// interim progress notifications (webSearch/dynamicToolCall/collabAgentToolCall)
// are not bound by the tool idle timer — for them it would measure total
// runtime, not idleness, and kill healthy long tools.
func TestProcToolIdleDoesNotKillNoProgressTool(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexNoProgressToolScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 1,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("search\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 6*time.Second) {
		t.Fatal("no-progress tool was killed by the tool idle watchdog (or turn did not complete)")
	}
}

// fakeCodexDuplicateItemCompletedScript runs two tools, delivers X's
// item/completed twice, stays silent past the turn idle timeout, then
// completes Y and the turn.
const fakeCodexDuplicateItemCompletedScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        for tid in ("tool-x", "tool-y"):
            item = {"id":tid,"type":"commandExecution","command":"run","status":"in_progress"}
            print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        item_x = {"id":"tool-x","type":"commandExecution","command":"run","status":"completed"}
        # Duplicate completion for X (replayed notification).
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item_x}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item_x}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        # Y still running silently, past the turn idle timeout (1s) but well
        # within the tool idle timeout (4s).
        time.sleep(0.25)
        item_y = {"id":"tool-y","type":"commandExecution","command":"run","status":"completed"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item_y}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcDuplicateItemCompletedDoesNotDesyncWatchdog verifies keyed tool
// tracking: a duplicate item/completed must not drop the active-tool set to
// zero while another tool is still running (which would resume the turn idle
// timer and kill the turn prematurely).
func TestProcDuplicateItemCompletedDoesNotDesyncWatchdog(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexDuplicateItemCompletedScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 4,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 6*time.Second) {
		t.Fatal("duplicate item/completed desynced the watchdog and killed a healthy turn")
	}
}

// TestProcToolIdleDisabledKeepsTurnIdleDuringTool verifies the
// tool_idle_timeout_seconds=0 combination: with the tool layer disabled the
// turn idle timer must NOT be suspended during tools, otherwise a silent
// hanging tool would be watched by no clock at all.
func TestProcToolIdleDisabledKeepsTurnIdleDuringTool(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexWatchdogToolIdleScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				return
			}
		case <-deadline:
			t.Fatal("silent hanging tool was not caught with tool idle disabled: no clock was watching the turn")
		}
	}
}

// fakeCodexStaleApprovalScript sends an approval request scoped to an
// unknown/old turn, then completes the real turn.
const fakeCodexStaleApprovalScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        # Approval request scoped to a DIFFERENT (zombie) turn: must be dropped.
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/commandExecution/requestApproval","params":{"command":"rm -rf x","threadId":thread_id,"turnId":"u-zombie"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcStaleApprovalRequestDropped verifies that approval/user-input
// requests carrying a scope that does not match the active turn are not
// surfaced as live permissions (answering one would unblock invisible
// server-side execution for a zombie turn, and merely registering it would
// corrupt the live turn's idle suspension).
func TestProcStaleApprovalRequestDropped(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexStaleApprovalScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Collect every event up to and including turn_done so a permission
	// surfaced BEFORE turn_done is still observed (waitForOutcome would discard
	// it, making the assertion vacuous).
	got, ok := collectUntilOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second)
	if !ok {
		t.Fatal("turn did not complete")
	}
	for _, ev := range got {
		if ev.Kind == agentstream.KindPermissionRequested {
			t.Fatalf("stale-turn approval was surfaced as a live permission: %+v", ev)
		}
	}
}

// fakeCodexPartialThenToolOnlyTurnScript streams a partial answer in turn 1
// and hangs (watchdog fires); turn 2 completes with no agent message.
const fakeCodexPartialThenToolOnlyTurnScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_counter = 0
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        turn_counter += 1
        turn_id = "u%d" % turn_counter
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        if turn_counter == 1:
            print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"PARTIAL-ANSWER","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
            # Hang: watchdog fires.
        else:
            # Tool-only turn: completes with no agent message delta.
            print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcWatchdogFailureDropsPartialResult verifies that a watchdog-failed
// turn's partially accumulated message is not flushed as the NEXT turn's
// native result.
func TestProcWatchdogFailureDropsPartialResult(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexPartialThenToolOnlyTurnScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("first\r")); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeError, 5*time.Second) {
		t.Fatal("watchdog did not fire on first turn")
	}
	if _, err := proc.Write([]byte("second\r")); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	// Collect up to and including the second turn's turn_done: the EventResult
	// is emitted immediately BEFORE turn_done, so waitForOutcome + a post-wait
	// drain would consume it and never catch a leak.
	got, ok := collectUntilOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second)
	if !ok {
		t.Fatal("second turn did not complete")
	}
	for _, ev := range got {
		if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) &&
			ev.Body.Result != nil && strings.Contains(ev.Body.Result.Text, "PARTIAL-ANSWER") {
			t.Fatalf("failed turn's partial text leaked into next turn's result: %q", ev.Body.Result.Text)
		}
	}
}

// fakeCodexNoTurnStartedScript answers turn/start with the initial turn
// object but never sends the turn/started notification (the dropped-
// notification defect class the watchdog defends against), then hangs.
const fakeCodexNoTurnStartedScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        # Response carries the turn id, but turn/started is never sent.
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcWatchdogArmedFromTurnStartResponse verifies the watchdog is armed
// from the turn/start RPC response, so a dropped turn/started notification
// cannot leave the turn unwatched.
func TestProcWatchdogArmedFromTurnStartResponse(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexNoTurnStartedScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeError, 5*time.Second) {
		t.Fatal("watchdog was not armed from the turn/start response: turn wedged with no timer")
	}
}

// fakeCodexToolThenApprovalScript starts a progress-capable command tool, then
// (before the tool completes) sends an approval request scoped to the turn, and
// stays silent — no further tool output — until the approval is answered, then
// completes.
const fakeCodexToolThenApprovalScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
pending_turn_id = None
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        pending_turn_id = mid
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        item = {"id":"tool-1","type":"commandExecution","command":"sleep","status":"in_progress"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/commandExecution/outputDelta","params":{"delta":"start\n","itemId":"tool-1","threadId":thread_id,"turnId":turn_id}}), flush=True)
        # Approval arrives while the command tool is still active; then silence.
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/commandExecution/requestApproval","params":{"command":"rm","threadId":thread_id,"turnId":turn_id}}), flush=True)
    elif mid == 9001 and "result" in msg:
        item_done = {"id":"tool-1","type":"commandExecution","command":"sleep","status":"completed"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item_done}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":pending_turn_id,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcToolIdleSuspendedDuringApprovalWait verifies that a progress-capable
// tool that goes silent while the provider is blocked on an approval is NOT
// killed by the tool idle watchdog — the user wait suspends tool idle too, so
// long as the hard cap is armed to bound the wait.
func TestProcToolIdleSuspendedDuringApprovalWait(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexToolThenApprovalScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     30, // hard cap bounds the wait → tool idle suspends
			ToolIdleTimeoutSeconds: 1,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))
	go func() { _, _ = proc.Write([]byte("go\r")) }()
	permID := waitForPermission(t, events, 3*time.Second)
	if permID == "" {
		t.Fatal("did not receive permission event")
	}
	// Well past both 1s idle timeouts while the approval is pending: the turn
	// must NOT be failed.
	deadline := time.After(200 * time.Millisecond)
	for waiting := true; waiting; {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				t.Fatal("tool idle watchdog killed a turn that was legitimately waiting on an approval")
			}
		case <-deadline:
			waiting = false
		}
	}
	if err := proc.RespondPermission(permID, "accept"); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn did not complete after the approval was answered")
	}
}

// fakeCodexDupApprovalScript sends the SAME approval request id twice, then
// completes once the (single) approval is answered.
const fakeCodexDupApprovalScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
pending_turn_id = None
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        pending_turn_id = mid
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        # Same request id delivered twice (retransmission).
        for _ in range(2):
            print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/commandExecution/requestApproval","params":{"command":"ls","threadId":thread_id,"turnId":turn_id}}), flush=True)
    elif mid == 9001 and "result" in msg:
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":pending_turn_id,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcDuplicateApprovalSurfacedOnce verifies that a retransmitted approval
// request (same id) is surfaced as exactly one permission and, after a single
// answer, the turn idle watchdog is not left permanently suspended by a
// double-counted wait.
func TestProcDuplicateApprovalSurfacedOnce(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexDupApprovalScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 1,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))
	go func() { _, _ = proc.Write([]byte("go\r")) }()

	permID := waitForPermission(t, events, 3*time.Second)
	if permID == "" {
		t.Fatal("did not receive permission event")
	}
	// A second, distinct permission must NOT arrive for the duplicate request.
	select {
	case ev := <-events:
		if ev.Kind == agentstream.KindPermissionRequested {
			t.Fatalf("duplicate approval request surfaced a second permission: %+v", ev)
		}
		// non-permission events are fine; drain a couple more just in case.
	case <-time.After(100 * time.Millisecond):
	}
	if err := proc.RespondPermission(permID, "accept"); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn did not complete after the single answer (wait counter may be stuck from a double-count)")
	}
}

// fakeCodexLostToolCompletionScript runs a command tool, emits its output, then
// reports thread idle WITHOUT ever sending item/completed for the tool (a lost
// completion), then hangs. The idle-completion fallback should complete the
// turn and deliver the accumulated agent message rather than wedging.
const fakeCodexLostToolCompletionScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"FINAL-ANSWER","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        item = {"id":"tool-1","type":"commandExecution","command":"x","status":"in_progress"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/commandExecution/outputDelta","params":{"delta":"out\n","itemId":"tool-1","threadId":thread_id,"turnId":turn_id}}), flush=True)
        # No item/completed for tool-1. Report idle instead.
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":thread_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcIdleCompletionDeliversResultDespitePinnedTool verifies that a lost
// item/completed (activeTools pinned) does not wedge the turn or discard its
// result: the stable-idle fallback completes it and delivers the accumulated
// agent message.
func TestProcIdleCompletionDeliversResultDespitePinnedTool(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexLostToolCompletionScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 0,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, ok := collectUntilOutcome(t, events, agentstream.OutcomeSuccess, 8*time.Second)
	if !ok {
		t.Fatal("turn wedged: idle completion did not fire with a pinned (lost-completion) tool")
	}
	delivered := false
	for _, ev := range got {
		if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) &&
			ev.Body.Result != nil && strings.Contains(ev.Body.Result.Text, "FINAL-ANSWER") {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("accumulated result was discarded instead of delivered on idle completion")
	}
}

// fakeCodexToolOnlyTurnScript reports only a completed webSearch tool after
// losing item/started, then reports usage and thread idle. The completed item
// still proves a tool ran, so the turn must not invite a retry.
const fakeCodexToolOnlyTurnScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        done = {"id":"ws-1","type":"webSearch","status":"completed"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":done}}), flush=True)
        usage = {"last":{"inputTokens":10,"outputTokens":1,"totalTokens":11},"total":{"inputTokens":10,"outputTokens":1,"totalTokens":11}}
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":thread_id,"turnId":turn_id,"tokenUsage":usage}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":thread_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcToolOnlyTurnIdleCompletes verifies an orphan item/completed is enough
// to keep a usage-bearing tool-only turn out of the empty-response fallback.
func TestProcToolOnlyTurnIdleCompletes(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexToolOnlyTurnScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 30,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 8*time.Second) {
		t.Fatal("tool-only turn was not idle-completed after orphan item/completed")
	}
}

// fakeCodexArmWindowApprovalScript answers turn/start with the turn id but
// NEVER sends turn/started (the dropped-notification class), instead following
// the response with a turn-scoped approval; then completes on the answer. The
// approval must be surfaced (armed from its scope), not dropped.
const fakeCodexArmWindowApprovalScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
pending_turn_id = None
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        pending_turn_id = mid
        # Respond FIRST, no turn/started, then a scoped approval.
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/commandExecution/requestApproval","params":{"command":"ls","threadId":thread_id,"turnId":turn_id}}), flush=True)
    elif mid == 9001 and "result" in msg:
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcArmWindowScopedApprovalSurfaced verifies that a scoped approval that
// arrives in the response-to-arm window (turn/started dropped) is armed from
// its scope and surfaced, not dropped without a response (which would block the
// provider forever).
func TestProcArmWindowScopedApprovalSurfaced(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexArmWindowApprovalScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 30,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	go func() { _, _ = proc.Write([]byte("go\r")) }()
	permID := waitForPermission(t, events, 3*time.Second)
	if permID == "" {
		t.Fatal("scoped approval in the arm window was dropped instead of surfaced")
	}
	if err := proc.RespondPermission(permID, "accept"); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn did not complete after the approval was answered")
	}
}

// fakeCodexHardCapZeroToolApprovalScript: hard_cap=0, a progress tool runs,
// then an approval arrives and stays unanswered. With tool idle NOT suspended
// (no hard cap to bound it), the tool idle watchdog must still fire so the turn
// is not left unwatched.
const fakeCodexHardCapZeroToolApprovalScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        item = {"id":"tool-1","type":"commandExecution","command":"sleep","status":"in_progress"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/commandExecution/outputDelta","params":{"delta":"x\n","itemId":"tool-1","threadId":thread_id,"turnId":turn_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/commandExecution/requestApproval","params":{"command":"rm","threadId":thread_id,"turnId":turn_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcHardCapZeroToolIdleStillBoundsWait verifies that with the hard cap
// disabled, a pending approval does NOT suspend tool idle (otherwise the turn
// would be watched by no clock at all) — the tool idle watchdog fires.
func TestProcHardCapZeroToolIdleStillBoundsWait(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexHardCapZeroToolApprovalScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 0,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 1,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	go func() { _, _ = proc.Write([]byte("go\r")) }()
	if !waitForOutcome(t, events, agentstream.OutcomeError, 5*time.Second) {
		t.Fatal("hard_cap=0 + pending approval + running tool left the turn unwatched (tool idle wrongly suspended)")
	}
}

func TestFreshArmPreservesOutstandingZombieTombstone(t *testing.T) {
	t.Parallel()
	proc := &Proc{
		approvals:   make(map[string]pendingApproval),
		userInputs:  make(map[int64]*pendingUserInput),
		activeTools: make(map[string]bool),
	}

	proc.threadMu.Lock()
	proc.markZombieTurnLocked("zombie-turn")
	proc.freshArmLocked("live-turn")
	got := proc.zombieTurnID
	outstanding := proc.hasOutstandingZombieLocked()
	acceptsIdleBeforeActivity := proc.canAcceptUnscopedIdleLocked()
	proc.curTurnProduced = true
	acceptsIdleAfterActivity := proc.canAcceptUnscopedIdleLocked()
	proc.threadMu.Unlock()

	if got != "zombie-turn" {
		t.Fatalf("freshArmLocked cleared outstanding zombie tombstone: got %q", got)
	}
	if !outstanding {
		t.Fatal("freshArmLocked removed zombie from the outstanding tombstone set")
	}
	if acceptsIdleBeforeActivity {
		t.Fatal("outstanding zombie allowed ambiguous idle to complete a silent live turn")
	}
	if !acceptsIdleAfterActivity {
		t.Fatal("scope-verified live-turn activity did not disambiguate its idle")
	}
}

const fakeCodexLateCompletedTurnStartedScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        # A delayed duplicate must not resurrect the completed turn.
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    elif mid is not None:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcDropsLateTurnStartedForCompletedTurn(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexLateCompletedTurnStartedScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 0,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := proc.snapshotTurnID(); got != "" {
		t.Fatalf("late turn/started resurrected completed turn %q", got)
	}
}

const fakeCodexLateCompletedApprovalScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        # A delayed scoped request for the completed turn must be dropped.
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/commandExecution/requestApproval","params":{"command":"ls","threadId":thread_id,"turnId":turn_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    elif mid is not None:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcDropsLateScopedRequestForCompletedTurn(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexLateCompletedApprovalScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 0,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := proc.snapshotTurnID(); got != "" {
		t.Fatalf("late scoped request resurrected completed turn %q", got)
	}
}

func TestStaleTurnScopeQuarantinedWithoutPendingTurnStart(t *testing.T) {
	t.Parallel()
	proc := &Proc{
		threadID:    "t1",
		approvals:   make(map[string]pendingApproval),
		userInputs:  make(map[int64]*pendingUserInput),
		activeTools: make(map[string]bool),
	}
	scope := eventScope{ThreadID: "t1", TurnID: "unsolicited-turn"}

	proc.threadMu.Lock()
	stale := proc.isStaleTurnScopeLocked(scope)
	gotTurnID := proc.turnID
	terminal := proc.isTerminalTurnLocked(scope.TurnID)
	zombie := proc.hasOutstandingZombieLocked()
	proc.threadMu.Unlock()

	if !stale {
		t.Fatal("unsolicited scoped request was accepted without a turn/start in flight")
	}
	if gotTurnID != "" {
		t.Fatalf("unsolicited scoped request armed phantom turn %q", gotTurnID)
	}
	if !terminal || !zombie {
		t.Fatalf("unsolicited scoped request was dropped without quarantine: terminal=%v zombie=%v", terminal, zombie)
	}
}

// An explicit turn/start RPC error contradicting an earlier lifecycle event
// must fail closed: cancel the provisionally discovered server turn before
// releasing the client queue.
const fakeCodexStartedThenStartErrorScript = `#!/usr/bin/env python3
import json, os, sys
thread_id = "t1"
turn_id = "u1"
capture = os.environ.get("CAPTURE_FILE", "")
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"error":{"code":-32000,"message":"lost acknowledgement"}}), flush=True)
    elif method == "turn/interrupt":
        if capture:
            with open(capture, "a") as f:
                f.write("interrupt %s\n" % msg["params"]["turnId"])
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
    elif mid is not None:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcCancelsProvisionalTurnOnStartRPCError(t *testing.T) {
	t.Parallel()
	capture := filepath.Join(t.TempDir(), "interrupts.txt")
	fake := writeFakeCodex(t, fakeCodexStartedThenStartErrorScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Env:             []string{"CAPTURE_FILE=" + capture},
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 30,
			TurnHardCapSeconds:     0,
			ToolIdleTimeoutSeconds: 0,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("go\r")); err == nil {
		t.Fatal("contradictory turn/start RPC error was ignored")
	}
	if !waitForOutcome(t, events, agentstream.OutcomeError, 3*time.Second) {
		t.Fatal("turn/start RPC error did not emit a terminal failure")
	}
	lines := waitForFileLines(t, capture, 1, 3*time.Second)
	if lines[0] != "interrupt u1" {
		t.Fatalf("provisional turn was not cancelled: %q", lines[0])
	}
}

// If turn/start fails before its id is known, a later turn/started is not a
// harmless phantom: it proves the failed submission is alive server-side and
// must be interrupted rather than silently dropped.
const fakeCodexLateStartedAfterStartErrorScript = `#!/usr/bin/env python3
import json, os, sys, time
thread_id = "t1"
turn_id = "late-u1"
capture = os.environ.get("CAPTURE_FILE", "")
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"error":{"code":-32000,"message":"start failed"}}), flush=True)
        time.sleep(0.05)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
    elif method == "turn/interrupt":
        if capture:
            with open(capture, "a") as f:
                f.write("interrupt %s\n" % msg["params"]["turnId"])
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
    elif mid is not None:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcCancelsLateTurnDiscoveredAfterStartError(t *testing.T) {
	t.Parallel()
	capture := filepath.Join(t.TempDir(), "interrupts.txt")
	fake := writeFakeCodex(t, fakeCodexLateStartedAfterStartErrorScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Env:             []string{"CAPTURE_FILE=" + capture},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()

	if _, err := proc.Write([]byte("go\r")); err == nil {
		t.Fatal("turn/start error was unexpectedly hidden without lifecycle proof")
	}
	lines := waitForFileLines(t, capture, 1, 3*time.Second)
	if lines[0] != "interrupt late-u1" {
		t.Fatalf("late discovered turn was not cancelled: %q", lines[0])
	}
}

// A provisional notification id and the authoritative response id must agree.
// Otherwise an old delayed turn can hijack the new submission.
const fakeCodexTurnStartIdentityMismatchScript = `#!/usr/bin/env python3
import json, os, sys
thread_id = "t1"
capture = os.environ.get("CAPTURE_FILE", "")
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":"stale-u0"}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":"real-u1"}}}), flush=True)
    elif method == "turn/interrupt":
        if capture:
            with open(capture, "a") as f:
                f.write("interrupt %s\n" % msg["params"]["turnId"])
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
    elif mid is not None:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcTurnStartIdentityMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	capture := filepath.Join(t.TempDir(), "interrupts.txt")
	fake := writeFakeCodex(t, fakeCodexTurnStartIdentityMismatchScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Env:             []string{"CAPTURE_FILE=" + capture},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()
	go func() {
		for range out {
		}
	}()
	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("go\r")); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("identity mismatch error = %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeError, 3*time.Second) {
		t.Fatal("identity mismatch did not emit a terminal failure")
	}
	lines := waitForFileLines(t, capture, 2, 3*time.Second)
	sort.Strings(lines)
	want := []string{"interrupt real-u1", "interrupt stale-u0"}
	if !reflect.DeepEqual(lines[:2], want) {
		t.Fatalf("identity mismatch interrupts = %v, want %v", lines[:2], want)
	}
}

const fakeCodexSpeedMetricsScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        time.sleep(0.2)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"hel","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        time.sleep(1.0)
        usage = {
            "last": {
                "cachedInputTokens": 0,
                "cacheCreationInputTokens": 0,
                "inputTokens": 100,
                "outputTokens": 20,
                "reasoningOutputTokens": 0,
                "totalTokens": 120
            },
            "total": {
                "cachedInputTokens": 0,
                "cacheCreationInputTokens": 0,
                "inputTokens": 100,
                "outputTokens": 20,
                "reasoningOutputTokens": 0,
                "totalTokens": 120
            },
            "modelContextWindow": 128000,
            "autoCompactTokenLimit": 100000
        }
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":thread_id,"turnId":turn_id,"tokenUsage":usage}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"lo","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexTwoTurnSpeedScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        turn_id = "u" + str(int(time.time()*1000) % 100000)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        time.sleep(0.2)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"d1","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        time.sleep(0.3)
        usage = {
            "last": {"cachedInputTokens":0,"cacheCreationInputTokens":0,"inputTokens":10,"outputTokens":5,"reasoningOutputTokens":0,"totalTokens":15},
            "total": {"cachedInputTokens":0,"cacheCreationInputTokens":0,"inputTokens":10,"outputTokens":5,"reasoningOutputTokens":0,"totalTokens":15},
            "modelContextWindow": 128000,
            "autoCompactTokenLimit": 100000
        }
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":thread_id,"turnId":turn_id,"tokenUsage":usage}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexToolTurnScript: first delta at ~0.5s, ~2s of generation, a 6s
// commandExecution item, then ~2s more generation, 120 output tokens total,
// single tokenUsage frame near the end, then turn/completed.
const fakeCodexToolTurnScript = `#!/usr/bin/env python3
import json, sys, time
thread_id = "t1"
turn_id = "u1"
tool_item = "tool-1"
for line in sys.stdin:
    msg = json.loads(line)
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"userAgent":"fake"}}), flush=True)
    elif method == "thread/start":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}), flush=True)
    elif method == "turn/start":
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        time.sleep(0.15)
        print(json.dumps({"jsonrpc":"2.0","method":"item/reasoning/textDelta","params":{"delta":"thinking about the tool call","threadId":thread_id,"turnId":turn_id}}), flush=True)
        time.sleep(0.35)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"tool args","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        time.sleep(0.35)
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"item":{"id":tool_item,"type":"commandExecution","status":"in_progress","command":"sleep 2"},"threadId":thread_id,"turnId":turn_id}}), flush=True)
        time.sleep(2.0)
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"item":{"id":tool_item,"type":"commandExecution","status":"completed","command":"sleep 2","aggregatedOutput":"done"},"threadId":thread_id,"turnId":turn_id}}), flush=True)
        time.sleep(0.35)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"the answer after the tool ran","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        time.sleep(0.35)
        usage = {"last":{"cachedInputTokens":0,"cacheCreationInputTokens":0,"inputTokens":100,"outputTokens":60,"reasoningOutputTokens":15,"totalTokens":160},"modelContextWindow":128000}
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":thread_id,"turnId":turn_id,"tokenUsage":usage}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`
