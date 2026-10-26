package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

type writeCloserBuffer struct {
	bytes.Buffer
}

func (w *writeCloserBuffer) Close() error { return nil }

func TestStartCreatesSessionAndEmitsPromptUpdates(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, out, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer proc.Close()

	if _, err := proc.Write([]byte("ping")); err != nil {
		t.Fatalf("Write text: %v", err)
	}
	if _, err := proc.Write([]byte("\r")); err != nil {
		t.Fatalf("Write CR: %v", err)
	}

	select {
	case got := <-out:
		if string(got) != "pong" {
			t.Fatalf("output = %q, want pong", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Acp update")
	}
}

func TestPromptEmitsTurnDoneEventWithStopReason(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_STOP_REASON=cancelled")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	if _, err := proc.Write([]byte("stop")); err != nil {
		t.Fatalf("Write text: %v", err)
	}
	if _, err := proc.Write([]byte("\r")); err != nil {
		t.Fatalf("Write CR: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Terminal == nil {
				continue
			}
			if ev.Terminal.Outcome != agentstream.OutcomeCancelled || ev.Terminal.Message != "cancelled" {
				t.Fatalf("event = %+v, want turn_done cancelled", ev)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for turn_done event")
		}
	}
}

func TestCorrelatedPromptFailureIsReportedOnlyByFailedTerminal(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_PROMPT_ERROR=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer proc.Close()

	var events []testEmission
	err = proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "turn-1",
		Input:     session.TextUserInput("fail"),
	}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events = append(events, ev)
		return false
	})
	if err != nil {
		t.Fatalf("DispatchTurn error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want only failed terminal", events)
	}
	ev := events[0]
	if ev.Terminal == nil || ev.Terminal.Outcome != agentstream.OutcomeError || ev.Body == nil || ev.Body.Type != string(agentstream.KindTurnFinished) || ev.Body.Error == nil || ev.Body.Error.Code != "acp_prompt_failed" || !strings.Contains(ev.Body.Error.Message, "prompt exploded") {
		t.Fatalf("terminal event = %+v", ev)
	}
}

func TestPromptEmitsNativeResultBeforeTurnDone(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	if _, err := proc.Write([]byte("go")); err != nil {
		t.Fatalf("Write text: %v", err)
	}
	if _, err := proc.Write([]byte("\r")); err != nil {
		t.Fatalf("Write CR: %v", err)
	}

	deadline := time.After(2 * time.Second)
	sawAssistant := false
	sawResult := false
	for {
		select {
		case ev := <-events:
			if pe := ev.Body; pe != nil && pe.Type == string(agentstream.KindAssistantDelta) {
				if pe.Text != "pong" || string([]byte(ev.Text)) != "pong" {
					t.Fatalf("assistant event = %+v, want pong delta", ev)
				}
				sawAssistant = true
			}
			if pe := ev.Body; pe != nil && pe.Type == string(agentstream.KindResultObserved) {
				if !sawAssistant {
					t.Fatal("native result emitted before assistant delta")
				}
				if ev.Origin.Channel != "result" {
					t.Fatalf("result event source = %q, want result", ev.Origin.Channel)
				}
				if pe.Result == nil || pe.Result.Text != "pong" {
					t.Fatalf("result event = %+v, want TurnResult text pong", pe)
				}
				sawResult = true
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				if !sawResult {
					t.Fatal("turn_done emitted before native result event")
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for native result / turn_done")
		}
	}
}

func TestPromptDrainsLateAssistantChunkBeforeResultAndTurnDone(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_LATE_CHUNK=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{
		Cwd:             t.TempDir(),
		PromptTailDrain: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartWithOptions() error = %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write prompt: %v", err)
	}

	var chunks []string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if pe := ev.Body; pe != nil {
				switch pe.Type {
				case string(agentstream.KindAssistantDelta):
					chunks = append(chunks, pe.Text)
				case string(agentstream.KindResultObserved):
					if got := strings.Join(chunks, ""); got != "pong tail" {
						t.Fatalf("assistant text before result = %q, want %q", got, "pong tail")
					}
					if pe.Result == nil || pe.Result.Text != "pong tail" {
						t.Fatalf("native result = %+v, want text %q", pe.Result, "pong tail")
					}
				}
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				if got := strings.Join(chunks, ""); got != "pong tail" {
					t.Fatalf("assistant text before turn_done = %q, want %q", got, "pong tail")
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for late assistant chunk and turn_done")
		}
	}
}

func TestPromptIgnoresUpdatesForForeignSession(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_FOREIGN_SESSION=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, out, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 32)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write prompt: %v", err)
	}

	var output strings.Builder
	var resultText string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case chunk := <-out:
			output.Write(chunk)
		case ev := <-events:
			if pe := ev.Body; pe != nil {
				switch pe.Type {
				case string(agentstream.KindResultObserved):
					if pe.Result != nil {
						resultText = pe.Result.Text
					}
				case string(agentstream.KindToolChanged), string(agentstream.KindUsageObserved):
					t.Fatalf("foreign provider event leaked: %+v", pe)
				}
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
			drainOutput:
				for {
					select {
					case chunk, ok := <-out:
						if !ok {
							break drainOutput
						}
						output.Write(chunk)
					default:
						break drainOutput
					}
				}
				if got := output.String(); got != "pong" {
					t.Fatalf("output = %q, want parent-only pong", got)
				}
				if resultText != "pong" {
					t.Fatalf("result = %q, want parent-only pong", resultText)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_done")
		}
	}
}

func TestPromptSuppressesFinalAssistantEventAfterStreamedChunk(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_AGENT_END=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, out, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	if _, err := proc.Write([]byte("go")); err != nil {
		t.Fatalf("Write text: %v", err)
	}
	if _, err := proc.Write([]byte("\r")); err != nil {
		t.Fatalf("Write CR: %v", err)
	}

	var outputs []string
	var resultText string
	var turnDone bool
	deadline := time.After(2 * time.Second)
	var drain <-chan time.Time
	for {
		select {
		case got := <-out:
			outputs = append(outputs, string(got))
		case ev := <-events:
			if pe := ev.Body; pe != nil && pe.Type == string(agentstream.KindResultObserved) && pe.Result != nil {
				resultText = pe.Result.Text
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				turnDone = true
				drain = time.After(50 * time.Millisecond)
			}
		case <-drain:
			if !turnDone {
				continue
			}
			if strings.Join(outputs, "") != "pong" {
				t.Fatalf("outputs = %#v, want only one streamed pong", outputs)
			}
			if resultText != "pong" {
				t.Fatalf("native result = %q, want pong", resultText)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for turn_done")
		}
	}
}

func TestPromptEmitsThinkingDeltaNotOutputForThoughtChunk(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_THOUGHT=1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, out, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	if _, err := proc.Write([]byte("go")); err != nil {
		t.Fatalf("Write text: %v", err)
	}
	if _, err := proc.Write([]byte("\r")); err != nil {
		t.Fatalf("Write CR: %v", err)
	}

	// The reasoning text must surface as a structured thinking event, never on
	// the output stream (which only carries the assistant message "pong").
	select {
	case got := <-out:
		if string(got) != "pong" {
			t.Fatalf("output = %q, want only pong (thought must not reach output)", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent message output")
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			pe := ev.Body
			if pe == nil {
				continue
			}
			if pe.Type == string(agentstream.KindThinkingDelta) {
				if pe.Text != "reasoning..." {
					t.Fatalf("thinking delta text = %q, want reasoning...", pe.Text)
				}
				// A thought-only chunk must not become the native result text.
				if pe.Type == string(agentstream.KindResultObserved) {
					t.Fatal("thought chunk leaked into native result")
				}
				return
			}
			if pe.Type == string(agentstream.KindResultObserved) && pe.Result != nil && strings.Contains(pe.Result.Text, "reasoning") {
				t.Fatalf("result text = %q, must not include thought text", pe.Result.Text)
			}
		case <-deadline:
			t.Fatal("timed out waiting for thinking delta event")
		}
	}
}

func TestPromptEmitsPlanUsageAndRichToolEvents(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_SEMANTIC=1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 32)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	if _, err := proc.Write([]byte("go\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var sawPlan, sawUsage, sawRichTool bool
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if pe := ev.Body; pe != nil {
				switch pe.Type {
				case string(agentstream.KindPlanReplaced):
					sawPlan = pe.Plan != nil && len(pe.Plan.Entries) == 2
				case string(agentstream.KindUsageObserved):
					sawUsage = len(pe.Usage) > 0
				case string(agentstream.KindToolChanged):
					sawRichTool = pe.ToolPhase == agentstream.ToolPhaseCompleted && pe.Tool != nil && len(pe.Tool.Content) > 0 && len(pe.Tool.Locations) > 0 && len(pe.Tool.RawOutput) > 0
				}
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				if !sawPlan || !sawUsage || !sawRichTool {
					t.Fatalf("semantic events: plan=%v usage=%v rich_tool=%v", sawPlan, sawUsage, sawRichTool)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for semantic ACP events")
		}
	}
}

func TestStartWithOptionsResumesAdvertisedSessionAndEmitsAgentSessionID(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_RESUME=1", "AMUX_FAKE_ACP_CAPTURE="+capture)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir(), ResumeSessionID: "existing-session"})
	if err != nil {
		t.Fatalf("StartWithOptions() error = %v", err)
	}
	defer proc.Close()

	if proc.sessionID != "existing-session" {
		t.Fatalf("sessionID = %q, want existing-session", proc.sessionID)
	}

	events := make(chan testEmission, 1)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	select {
	case ev := <-events:
		if ev.Metadata.AgentSessionID != "existing-session" || ev.Origin.Channel != "agent" {
			t.Fatalf("event = %+v, want AgentSessionID existing-session from agent", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AgentSessionID event")
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !bytes.Contains(data, []byte(`"method":"session/resume"`)) {
		t.Fatalf("capture missing session/resume:\n%s", data)
	}
	if bytes.Contains(data, []byte(`"method":"session/new"`)) {
		t.Fatalf("capture unexpectedly called session/new:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`"sessionId":"existing-session"`)) {
		t.Fatalf("capture missing resume session id:\n%s", data)
	}
}

func TestStartWithOptionsFallsBackToLoadSessionWhenResumeIsNotAdvertised(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_LOAD=1", "AMUX_FAKE_ACP_CAPTURE="+capture)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir(), ResumeSessionID: "loadable-session"})
	if err != nil {
		t.Fatalf("StartWithOptions() error = %v", err)
	}
	defer proc.Close()

	if proc.sessionID != "loadable-session" {
		t.Fatalf("sessionID = %q, want loadable-session", proc.sessionID)
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !bytes.Contains(data, []byte(`"method":"session/load"`)) {
		t.Fatalf("capture missing session/load:\n%s", data)
	}
	if bytes.Contains(data, []byte(`"method":"session/resume"`)) || bytes.Contains(data, []byte(`"method":"session/new"`)) {
		t.Fatalf("capture used unexpected session setup method:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`"sessionId":"loadable-session"`)) {
		t.Fatalf("capture missing load session id:\n%s", data)
	}
}

func TestLoadSessionReplayDoesNotEnterLiveEvents(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(),
		"AMUX_FAKE_ACP=1",
		"AMUX_FAKE_ACP_LOAD=1",
		"AMUX_FAKE_ACP_LOAD_REPLAY=1",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, out, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir(), ResumeSessionID: "loadable-session"})
	if err != nil {
		t.Fatalf("StartWithOptions() error = %v", err)
	}
	defer proc.Close()

	var events []testEmission
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events = append(events, ev)
		return false
	})
	for _, ev := range events {
		if ev.Turn != nil || ev.Terminal != nil {
			t.Fatalf("load replay leaked turn event: %+v", ev)
		}
	}
	select {
	case replayed := <-out:
		t.Fatalf("load replay leaked output: %q", replayed)
	default:
	}

	if _, err := proc.Write([]byte("live\r")); err != nil {
		t.Fatalf("Write live prompt: %v", err)
	}
	select {
	case got := <-out:
		if string(got) != "pong" {
			t.Fatalf("live output = %q, want pong", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live output after load")
	}
}

func TestStartWithOptionsFailsWhenResumeInitializeResultIsInvalid(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_BAD_INITIALIZE=1", "AMUX_FAKE_ACP_CAPTURE="+capture)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir(), ResumeSessionID: "existing-session"})
	if err == nil {
		if proc != nil {
			_ = proc.Close()
		}
		t.Fatal("StartWithOptions succeeded with invalid initialize result, want error")
	}
	if proc != nil {
		t.Fatalf("proc = %+v, want nil on initialize decode failure", proc)
	}
	if !strings.Contains(err.Error(), "decode initialize") {
		t.Fatalf("error = %v, want decode initialize failure", err)
	}
	data, readErr := os.ReadFile(capture)
	if readErr != nil {
		t.Fatalf("read capture: %v", readErr)
	}
	if bytes.Contains(data, []byte(`"method":"session/new"`)) {
		t.Fatalf("capture unexpectedly called session/new after bad initialize:\n%s", data)
	}
}

func TestStartReturnsChildExitDuringInitialize(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(),
		"AMUX_FAKE_ACP=1",
		"AMUX_FAKE_ACP_EXIT_BEFORE_INITIALIZE=1",
	)

	errCh := make(chan error, 1)
	go func() {
		proc, _, err := StartWithOptions(context.Background(), cmd, StartOptions{Cwd: t.TempDir()})
		if proc != nil {
			_ = proc.Close()
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("StartWithOptions succeeded after the ACP child exited")
		}
		if !strings.Contains(err.Error(), "acp process exited during initialize") {
			t.Fatalf("error = %v, want initialize child-exit context", err)
		}
		if !strings.Contains(err.Error(), "fake ACP bootstrap failed") {
			t.Fatalf("error = %v, want captured child stderr", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartWithOptions hung after the ACP child exited")
	}
}

func TestEmitToolUpdateUsesToolTitleAcrossUpdates(t *testing.T) {
	p := &Proc{toolTitles: make(map[string]string)}
	var got []testEmission
	p.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		got = append(got, ev)
		return false
	})

	p.emitToolUpdate(toolUpdate{
		Update: "tool_call",
		ID:     "call-1",
		Title:  "List files",
		Status: "pending",
	})
	p.emitToolUpdate(toolUpdate{
		Update: "tool_call_update",
		ID:     "call-1",
		Status: "completed",
	})

	if len(got) != 2 {
		t.Fatalf("events = %d, want 2", len(got))
	}
	start := got[0].Body
	if start == nil || start.Type != string(agentstream.KindToolChanged) || start.ToolPhase != agentstream.ToolPhaseStarted || start.Tool == nil || start.Tool.Title != "List files" {
		t.Fatalf("start event = %+v", start)
	}
	done := got[1].Body
	if done == nil || done.Type != string(agentstream.KindToolChanged) || done.ToolPhase != agentstream.ToolPhaseCompleted || done.Tool == nil || done.Tool.Title != "List files" {
		t.Fatalf("done event = %+v", done)
	}
}

func TestEmitToolUpdateKeepsStatuslessRichUpdate(t *testing.T) {
	p := &Proc{toolTitles: make(map[string]string)}
	var got []testEmission
	p.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		got = append(got, ev)
		return false
	})

	p.emitToolUpdate(toolUpdate{
		Update:    "tool_call_update",
		ID:        "call-1",
		Content:   json.RawMessage(`[{"type":"content_diff","text":"+new line"}]`),
		Locations: json.RawMessage(`[{"path":"main.go","line":10}]`),
	})

	if len(got) != 1 || got[0].Body == nil || got[0].Body.Type != string(agentstream.KindToolChanged) || got[0].Body.ToolPhase != agentstream.ToolPhaseUpdated {
		t.Fatalf("events = %+v, want one tool_updated", got)
	}
	tool := got[0].Body.Tool
	if tool == nil || len(tool.Content) == 0 || len(tool.Locations) == 0 {
		t.Fatalf("tool = %+v, want preserved rich detail", tool)
	}
}

func TestReadLoopDoesNotExposeUnknownRawSessionUpdate(t *testing.T) {
	p := &Proc{out: make(chan []byte, 1), toolTitles: make(map[string]string)}
	events := make(chan testEmission, 1)
	p.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"current_mode_update","modeId":"code"}}}`
	p.readLoop(strings.NewReader(line + "\n"))

	select {
	case ev := <-events:
		t.Fatalf("unknown raw event leaked to producer stream: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestReadLoopEmitsAvailableCommandsDuringRestore(t *testing.T) {
	p := &Proc{out: make(chan []byte, 1), toolTitles: make(map[string]string)}
	p.restoring.Store(true)
	events := make(chan testEmission, 2)
	p.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	commands := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"amux-skill","description":"Run skill","input":{"hint":"<task>"}}]}}}`
	plan := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"content":"old","status":"completed"}]}}}`
	p.readLoop(strings.NewReader(commands + "\n" + plan + "\n"))

	select {
	case ev := <-events:
		pe := ev.Body
		if pe == nil || pe.Type != string(agentstream.KindCommandsReplaced) || len(pe.AvailableCommands) != 1 || pe.AvailableCommands[0].InputHint != "<task>" {
			t.Fatalf("available commands event = %+v", pe)
		}
	default:
		t.Fatal("available commands event was dropped during restore")
	}
	select {
	case ev := <-events:
		t.Fatalf("restore emitted replayed non-state event: %+v", ev)
	default:
	}
}

func TestEmitToolUpdateEnrichesBashTitleAcrossUpdates(t *testing.T) {
	p := &Proc{toolTitles: make(map[string]string)}
	var got []testEmission
	p.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		got = append(got, ev)
		return false
	})

	p.emitToolUpdate(toolUpdate{
		Update:   "tool_call",
		ID:       "call-1",
		Title:    "bash",
		Status:   "in_progress",
		RawInput: json.RawMessage(`{"Command":"echo hello-from-bash","Description":"Echo"}`),
	})
	// The completion update carries no title/rawInput; it must reuse the
	// enriched title cached from the initial tool_call.
	p.emitToolUpdate(toolUpdate{
		Update: "tool_call_update",
		ID:     "call-1",
		Status: "completed",
	})

	if len(got) != 2 {
		t.Fatalf("events = %d, want 2", len(got))
	}
	start := got[0].Body
	if start == nil || start.Tool == nil || start.Tool.Title != "bash echo hello-from-bash" {
		t.Fatalf("start title = %+v, want enriched bash echo hello-from-bash", start)
	}
	if string(start.Tool.Input) != `{"Command":"echo hello-from-bash","Description":"Echo"}` {
		t.Fatalf("start input = %q", string(start.Tool.Input))
	}
	done := got[1].Body
	if done == nil || done.Type != string(agentstream.KindToolChanged) || done.ToolPhase != agentstream.ToolPhaseCompleted || done.Tool == nil || done.Tool.Title != "bash echo hello-from-bash" {
		t.Fatalf("done event = %+v", done)
	}
}

func TestDebugACPAgentChunkWritesQuotedTextWhenEnabled(t *testing.T) {
	t.Setenv("AMUX_DEBUG_ACP_CHUNKS", "1")

	var buf bytes.Buffer
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
	})

	debugACPAgentChunk("hello\nworld")
	_ = w.Close()
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy stderr: %v", err)
	}

	if got, want := buf.String(), "amux acp agent chunk: \"hello\\nworld\"\n"; got != want {
		t.Fatalf("debug output = %q, want %q", got, want)
	}
}

func TestDebugACPAgentChunkSilentByDefault(t *testing.T) {
	t.Setenv("AMUX_DEBUG_ACP_CHUNKS", "")

	var buf bytes.Buffer
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
	})

	debugACPAgentChunk("hello")
	_ = w.Close()
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy stderr: %v", err)
	}

	if got := buf.String(); got != "" {
		t.Fatalf("debug output = %q, want empty", got)
	}
}

func TestRespondPermissionWritesJSONRPCResponse(t *testing.T) {
	stdin := &writeCloserBuffer{}
	p := &Proc{
		stdin:       stdin,
		permissions: map[string]int64{"fake-session:7": 7},
	}

	if err := p.RespondPermission("fake-session:7", "allow"); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	var got struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Result  struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionId string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdin.Bytes()), &got); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdin.String())
	}
	if got.JSONRPC != "2.0" || got.ID != 7 || got.Method != "" {
		t.Fatalf("response envelope = %+v, want id 7 without method", got)
	}
	if got.Result.Outcome.Outcome != "selected" || got.Result.Outcome.OptionId != "allow" {
		t.Fatalf("outcome = %+v, want selected allow", got.Result.Outcome)
	}
	if _, ok := p.permissions["fake-session:7"]; ok {
		t.Fatal("permission was not removed after response")
	}
}

func TestACPPermissionRequestBecomesTypedPayload(t *testing.T) {
	requested := acpPermissionRequested("native-session:7", json.RawMessage(`{
		"tool":"Bash",
		"options":[
			{"optionId":"allow","name":"Allow","kind":"allow_once"},
			{"id":"deny","name":"Deny"}
		]
	}`))
	if requested.Request.ID != "native-session:7" || requested.Request.Provider != "acp" {
		t.Fatalf("request identity = %+v", requested.Request)
	}
	if len(requested.Request.Options) != 2 || requested.Request.Options[0].ID != "allow" || requested.Request.Options[1].ID != "deny" {
		t.Fatalf("options = %+v", requested.Request.Options)
	}
	if !json.Valid(requested.Request.Details) {
		t.Fatalf("details = %q, want valid JSON", requested.Request.Details)
	}
}

func TestSetEventSinkPreservesPendingEventOrderWhenLiveEventArrives(t *testing.T) {
	p := &Proc{}
	p.eventSinks.Emit(agentstream.ProducerEmission{Session: agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Code: "test", Message: "pending-1"}})
	p.eventSinks.Emit(agentstream.ProducerEmission{Session: agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Code: "test", Message: "pending-2"}})
	var got []string

	p.SetEventSink(func(emission testProducerEmission) bool {
		diagnostic := emission.Session.(agentstream.SessionDiagnostic)
		got = append(got, diagnostic.Message)
		if diagnostic.Message == "pending-1" {
			p.eventSinks.Emit(agentstream.ProducerEmission{Session: agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Code: "test", Message: "live"}})
		}
		return false
	})

	want := []string{"pending-1", "pending-2", "live"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestInterruptSendsSessionCancelNotification(t *testing.T) {
	stdin := &writeCloserBuffer{}
	p := &Proc{
		stdin:     stdin,
		sessionID: "fake-session",
	}

	if err := p.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	var got struct {
		JSONRPC string `json:"jsonrpc"`
		ID      *int64 `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdin.Bytes()), &got); err != nil {
		t.Fatalf("decode interrupt notification: %v\n%s", err, stdin.String())
	}
	if got.JSONRPC != "2.0" {
		t.Fatalf("jsonrpc = %q, want 2.0", got.JSONRPC)
	}
	if got.ID != nil {
		t.Fatalf("id = %v, want notification without id", *got.ID)
	}
	if got.Method != "session/cancel" {
		t.Fatalf("method = %q, want session/cancel", got.Method)
	}
	if got.Params.SessionID != "fake-session" {
		t.Fatalf("sessionId = %q, want fake-session", got.Params.SessionID)
	}
}

func TestFakeACPServer(t *testing.T) {
	if os.Getenv("AMUX_FAKE_ACP") != "1" {
		return
	}
	if os.Getenv("AMUX_FAKE_ACP_EXIT_BEFORE_INITIALIZE") == "1" {
		fmt.Fprintln(os.Stderr, "fake ACP bootstrap failed")
		os.Exit(42)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if capture := os.Getenv("AMUX_FAKE_ACP_CAPTURE"); capture != "" {
			f, err := os.OpenFile(capture, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err == nil {
				_, _ = f.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
				_ = f.Close()
			}
		}
		switch req.Method {
		case "initialize":
			if result := os.Getenv("AMUX_FAKE_ACP_INITIALIZE_RESULT"); result != "" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", req.ID, result)
			} else if os.Getenv("AMUX_FAKE_ACP_BAD_INITIALIZE") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"agentCapabilities":true}}`+"\n", req.ID)
			} else if os.Getenv("AMUX_FAKE_ACP_RESUME") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"agentCapabilities":{"sessionCapabilities":{"resume":{}}}}}`+"\n", req.ID)
			} else if os.Getenv("AMUX_FAKE_ACP_LOAD") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"agentCapabilities":{"loadSession":true}}}`+"\n", req.ID)
			} else {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
			}
		case "session/new":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"fake-session"}}`+"\n", req.ID)
		case "session/load":
			if os.Getenv("AMUX_FAKE_ACP_LOAD_REPLAY") == "1" {
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"loadable-session","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"old question"}}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"loadable-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"old answer"}}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"loadable-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"old thought"}}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"loadable-session","update":{"sessionUpdate":"tool_call","toolCallId":"old-tool","title":"Old tool","status":"completed"}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"loadable-session","update":{"sessionUpdate":"plan","entries":[{"content":"Old plan","status":"completed"}]}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"loadable-session","update":{"sessionUpdate":"current_mode_update","modeId":"old"}}}`)
			}
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":null}`+"\n", req.ID)
		case "session/resume":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
		case "session/prompt":
			if os.Getenv("AMUX_FAKE_ACP_PROMPT_ERROR") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"prompt exploded"}}`+"\n", req.ID)
				continue
			}
			sessionID := req.Params.SessionID
			if sessionID == "" {
				sessionID = "fake-session"
			}
			if os.Getenv("AMUX_FAKE_ACP_FOREIGN_SESSION") == "1" {
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"foreign-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"child output"}}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"foreign-session","update":{"sessionUpdate":"tool_call","toolCallId":"child-tool","title":"Child tool","status":"pending"}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"foreign-session","update":{"sessionUpdate":"usage_update","used":99,"size":100}}}`)
			}
			if os.Getenv("AMUX_FAKE_ACP_THOUGHT") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"reasoning..."}}}}`+"\n", sessionID)
			}
			if os.Getenv("AMUX_FAKE_ACP_SEMANTIC") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"plan","entries":[{"content":"Inspect","priority":"high","status":"completed"},{"content":"Fix","priority":"high","status":"in_progress"}]}}}`+"\n", sessionID)
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"usage_update","used":1200,"size":8000,"cost":{"amount":0.12,"currency":"USD"}}}}`+"\n", sessionID)
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"tool_call","toolCallId":"tool-1","title":"Edit file","kind":"edit","status":"in_progress","rawInput":{"path":"main.go"}}}}`+"\n", sessionID)
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"tool_call_update","toolCallId":"tool-1","title":"Edit file","kind":"edit","status":"completed","content":[{"type":"diff","path":"main.go","oldText":"old","newText":"new"}],"locations":[{"path":"/repo/main.go","line":12}],"rawOutput":{"changed":true}}}}`+"\n", sessionID)
			}
			fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"pong"}}}}`+"\n", sessionID)
			if os.Getenv("AMUX_FAKE_ACP_AGENT_END") == "1" {
				fmt.Printf(`{"id":"e1","session_id":%q,"agent_end":{"output":{"role":"assistant","content":"pong","response_meta":{"finish_reason":"stop"}}}}`+"\n", sessionID)
			}
			if reason := os.Getenv("AMUX_FAKE_ACP_STOP_REASON"); reason != "" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"stopReason":%q}}`+"\n", req.ID, reason)
			} else {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
			}
			if os.Getenv("AMUX_FAKE_ACP_LATE_CHUNK") == "1" {
				time.Sleep(5 * time.Millisecond)
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":" tail"}}}}`+"\n", sessionID)
			}
		}
	}
	os.Exit(0)
}
