package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	if err := RegisterProvider("compatible", ProviderRegistration{InputModalities: []string{"text", "image"}, InputCapabilitySource: "compatible:default"}); err != nil {
		panic(err)
	}
}

func TestCompatibleDefaultsToImageCapabilityWithoutModelDiscovery(t *testing.T) {
	proc := &Proc{providerName: "compatible"}
	proc.discoverInputCapabilities(context.Background(), "any-effective-model")
	modalities, source := proc.InputCapabilities()
	if strings.Join(modalities, ",") != "text,image" || source != "compatible:default" {
		t.Fatalf("modalities=%v source=%q", modalities, source)
	}
}

func TestStartResolvesExecutableFromLaunchEnvPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake provider is unix-only")
	}
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "compatible")
	if err := os.WriteFile(fake, []byte(fakeCodexScript), 0o755); err != nil {
		t.Fatalf("write fake compatible: %v", err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")

	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   "compatible",
		ProviderName: "compatible",
		Env:          append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin"),
	})
	if err != nil {
		t.Fatalf("Start with executable from launch env PATH: %v", err)
	}
	defer proc.Close()
}

func TestStartDoesNotFallbackToServicePATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake provider is unix-only")
	}
	serviceBinDir := t.TempDir()
	fake := filepath.Join(serviceBinDir, "compatible")
	if err := os.WriteFile(fake, []byte(fakeCodexScript), 0o755); err != nil {
		t.Fatalf("write service-path fake compatible: %v", err)
	}
	t.Setenv("PATH", serviceBinDir+string(os.PathListSeparator)+"/usr/bin:/bin")

	_, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   "compatible",
		ProviderName: "compatible",
		Env:          append(os.Environ(), "PATH=/usr/bin:/bin"),
	})
	if err == nil {
		t.Fatal("Start succeeded by falling back to service PATH")
	}
	if !strings.Contains(err.Error(), "launch env PATH") {
		t.Fatalf("Start error = %v, want launch env PATH failure", err)
	}
}

// TestProcThreadStatusIdleEmitsTurnDone covers the compatible/codex app-server path
// where the agent ends a turn by asking a followup and the server emits
// thread/status/changed(status.type=idle) WITHOUT turn/completed. The proc must
// normalize idle to the same turn_done exit so the bridge can finalize the turn.
func TestProcThreadStatusIdleEmitsTurnDone(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexThreadStatusIdleScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:                 t.TempDir(),
		Executable:          fake,
		ProviderName:        "compatible",
		IdleCompletionDelay: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := waitFor(t, out, 3*time.Second); got != "hello from compatible" {
		t.Fatalf("delta = %q, want %q", got, "hello from compatible")
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, time.Second) {
		t.Fatal("did not observe turn_done from thread/status/changed(idle)")
	}
}

func TestProcMapsThreadTokenUsageUpdated(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"codex", "compatible"} {
		for _, withTotal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/total=%t", provider, withTotal), func(t *testing.T) {
				script := fakeCodexTokenUsageScript
				// Usage arrives before the first delta. Make the later timing
				// enrichment deterministic instead of depending on process speed.
				script = strings.Replace(script, "import json, sys", "import json, sys, time", 1)
				script = strings.Replace(script, `        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta"`, `        time.sleep(0.02)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta"`, 1)
				if !withTotal {
					script = strings.Replace(script, `        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated"`, `        usage.pop("total")
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated"`, 1)
				}
				fake := writeFakeCodex(t, script)
				proc, _, err := startTestProc(t, StartOptions{
					Cwd:          t.TempDir(),
					Executable:   fake,
					ProviderName: provider,
				})
				if err != nil {
					t.Fatalf("Start: %v", err)
				}
				defer proc.Close()

				events := make(chan testEmission, 16)
				proc.SetEventSink(captureEventSink(events))
				if _, err := proc.Write([]byte("hello\r")); err != nil {
					t.Fatalf("Write: %v", err)
				}

				ev := waitForEmissionKind(t, events, string(agentstream.KindUsageObserved), 3*time.Second)
				if ev == nil || ev.Body == nil {
					t.Fatal("did not observe usage event")
				}
				if ev.Body.Provider != provider {
					t.Fatalf("usage provider = %q, want %s", ev.Body.Provider, provider)
				}
				var usage map[string]int64
				if err := json.Unmarshal(ev.Body.Usage, &usage); err != nil {
					t.Fatalf("decode normalized usage: %v", err)
				}
				want := map[string]int64{
					"input_tokens":                100,
					"output_tokens":               20,
					"total_tokens":                120,
					"cached_input_tokens":         3,
					"cache_creation_input_tokens": 2,
					"reasoning_output_tokens":     5,
					"context_window":              128000,
				}
				if withTotal {
					for key, value := range map[string]int64{
						"cumulative_input_tokens":                900,
						"cumulative_output_tokens":               99,
						"cumulative_total_tokens":                999,
						"cumulative_cached_input_tokens":         30,
						"cumulative_cache_creation_input_tokens": 20,
						"cumulative_reasoning_output_tokens":     50,
					} {
						want[key] = value
					}
				} else {
					for key := range usage {
						if strings.HasPrefix(key, "cumulative_") {
							t.Fatalf("missing provider total fabricated %s: %v", key, usage)
						}
					}
				}
				for key, value := range want {
					if usage[key] != value {
						t.Fatalf("usage[%q] = %d, want %d; usage=%v", key, usage[key], value, usage)
					}
				}
				result := waitForEmissionKind(t, events, string(agentstream.KindResultObserved), 3*time.Second)
				if result == nil || result.Body == nil || result.Body.Result == nil {
					t.Fatal("did not observe result carrying usage")
				}
				var resultUsage map[string]int64
				if err := json.Unmarshal(result.Body.Result.Usage, &resultUsage); err != nil {
					t.Fatalf("decode result usage: %v", err)
				}
				if resultUsage["ttft_ms"] <= 0 {
					t.Fatalf("result usage lacks first-delta timing: %s", result.Body.Result.Usage)
				}
				// Turn completion may add timing fields after the last provider
				// usage frame. The provider's token snapshot must remain exact.
				delete(resultUsage, "ttft_ms")
				delete(resultUsage, "tokens_per_sec")
				if !maps.Equal(resultUsage, usage) {
					t.Fatalf("result usage = %s, want %s", result.Body.Result.Usage, ev.Body.Usage)
				}
			})
		}
	}
}

// TestProcThreadStatusIdleAfterTurnCompletedNoDup ensures the idle handler does
// not emit a second turn_done when turn/completed already finalized the turn
// (the normal compatible path emits both: turn/completed then a trailing idle).
func TestProcThreadStatusIdleAfterTurnCompletedNoDup(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexTurnCompletedThenIdleScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// First turn_done from turn/completed.
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("did not observe first turn_done from turn/completed")
	}
	// Drain any further events for a short window; there must be no second
	// turn_done from the trailing thread/status/changed(idle).
	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				t.Fatalf("duplicate turn_done observed after turn/completed")
			}
		case <-deadline:
			return
		}
	}
}

// TestProcTurnCompletedAfterThreadStatusIdleNoDup covers an idle notification
// arriving before the authoritative turn/completed. The completion must cancel
// the pending idle fallback so presentation sees only one terminal event.
func TestProcTurnCompletedAfterThreadStatusIdleNoDup(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexIdleThenTurnCompletedScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("did not observe turn_done from turn/completed")
	}
	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				t.Fatal("duplicate turn_done observed after thread/status/changed(idle)")
			}
		case <-deadline:
			return
		}
	}
}

func TestProcThreadStatusIdleThenActiveKeepsSameTurnOpen(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexIdleThenActiveScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:                 t.TempDir(),
		Executable:          fake,
		ProviderName:        "compatible",
		IdleCompletionDelay: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	result := waitForEmissionKind(t, events, string(agentstream.KindResultObserved), 3*time.Second)
	if result == nil || result.Body == nil || result.Body.Result == nil {
		t.Fatal("did not observe native result")
	}
	if got, want := result.Body.Result.Text, "phase one phase two"; got != want {
		t.Fatalf("result text = %q, want %q", got, want)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("did not observe final turn_done")
	}
}

func TestProcIgnoresForeignChildThreadEvents(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexForeignChildThreadScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 64)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var (
		deltas    strings.Builder
		result    string
		terminals int
		tools     int
		usage     int
	)
	deadline := time.After(3 * time.Second)
	for terminals == 0 {
		select {
		case ev := <-events:
			if ev.Body != nil {
				switch ev.Body.Type {
				case string(agentstream.KindAssistantDelta):
					deltas.WriteString(ev.Body.Text)
				case string(agentstream.KindResultObserved):
					if ev.Body.Result != nil {
						result = ev.Body.Result.Text
					}
				case string(agentstream.KindToolChanged):
					tools++
				case string(agentstream.KindUsageObserved):
					usage++
				}
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				terminals++
			}
		case <-deadline:
			t.Fatal("did not observe parent turn_done")
		}
	}

	if got, want := deltas.String(), "parent before parent after"; got != want {
		t.Fatalf("assistant deltas = %q, want %q", got, want)
	}
	if result != "parent before parent after" {
		t.Fatalf("result = %q, want parent-only result", result)
	}
	if tools != 0 || usage != 0 {
		t.Fatalf("foreign child events leaked: tools=%d usage=%d", tools, usage)
	}
}

func TestProcCanLabelCodexCompatibleProvider(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Body != nil && ev.Body.Provider != "compatible" {
				t.Fatalf("provider event = %+v, want provider compatible", ev.Body)
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				return
			}
		case <-timeout:
			t.Fatal("did not observe compatible turn_done")
		}
	}
}

// compatibleStartOptions builds the StartOptions the structured broker uses for a
// `compatible-app-server` provider: same Codex-compatible protocol, but labelled
// `compatible` and with the experimental+initialized handshake compatible requires.
func compatibleStartOptions(t *testing.T, fake string) StartOptions {
	t.Helper()
	return StartOptions{
		Cwd:             t.TempDir(),
		Executable:      fake,
		ProviderName:    "compatible",
		ExperimentalAPI: true,
		SendInitialized: true,
	}
}

// TestCompatibleProcEmitsThreadIDAsAgentSessionID verifies compatible records the
// app-server thread id as AgentSessionID (shared codex protocol, compatible label).
func TestCompatibleProcEmitsThreadIDAsAgentSessionID(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexScript)
	proc, _, err := Start(context.Background(), compatibleStartOptions(t, fake))
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

// TestCompatibleProcResumesThread verifies ResumeSessionID triggers thread/resume
// for compatible and the resumed id is surfaced as AgentSessionID.
func TestCompatibleProcResumesThread(t *testing.T) {
	t.Parallel()
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	fake := writeFakeCodex(t, fakeCodexResumeScript)
	opts := compatibleStartOptions(t, fake)
	opts.ResumeSessionID = "existing-thread"
	opts.Env = append(os.Environ(), "AMUX_FAKE_CODEX_CAPTURE="+capture)
	proc, _, err := Start(context.Background(), opts)
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

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !bytes.Contains(data, []byte(`"method": "thread/resume"`)) && !bytes.Contains(data, []byte(`"method":"thread/resume"`)) {
		t.Fatalf("capture missing thread/resume:\n%s", data)
	}
}

// TestCompatibleProcInterrupt verifies Interrupt() sends turn/interrupt for compatible.
func TestCompatibleProcInterrupt(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexInterruptScript)
	proc, _, err := Start(context.Background(), compatibleStartOptions(t, fake))
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
	go func() {
		_, _ = proc.Write([]byte("again\r"))
	}()
	waitForNativeTurnID(t, proc, 2*time.Second)
	if err := proc.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}

// TestCompatibleProcApprovalRoundTrip verifies the approval request maps to a
// permission event labelled compatible and RespondPermission writes back accept.
func TestCompatibleProcApprovalRoundTrip(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexApprovalScript)
	proc, _, err := Start(context.Background(), compatibleStartOptions(t, fake))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 8)
	proc.SetEventSink(captureEventSink(events))

	go func() { _, _ = proc.Write([]byte("run\r")) }()

	ev := waitForPermissionEvent(t, events, 3*time.Second)
	if ev == nil {
		t.Fatal("did not receive permission event")
	}
	if ev.Permission.ID == "" {
		t.Fatal("permission event missing id")
	}
	// The permission event must carry the compatible provider label.
	if ev.Body == nil || ev.Body.Provider != "compatible" {
		t.Fatalf("permission event provider = %+v, want compatible", ev.Body)
	}
	if err := proc.RespondPermission(ev.Permission.ID, optAccept); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn did not complete after approval")
	}
}

// TestCompatibleProcRequestUserInputRoundTrip verifies item/tool/requestUserInput
// (AskUserQuestion) splits into N permissions, aggregates answers, and responds
// with {answers:{questionId:{answers:[label]}}} so the turn completes.
func TestCompatibleProcRequestUserInputRoundTrip(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexRequestUserInputScript)
	proc, _, err := Start(context.Background(), compatibleStartOptions(t, fake))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	go func() { _, _ = proc.Write([]byte("run\r")) }()

	// The fake sends one requestUserInput with two questions -> two permissions.
	permIDs := waitForPermissions(t, events, 2, 5*time.Second)
	if len(permIDs) != 2 {
		t.Fatalf("got %d permission events, want 2", len(permIDs))
	}
	// Permission ids must be "<reqID>:<questionId>".
	var q1Perm, q2Perm string
	for _, pid := range permIDs {
		switch pid {
		case "9001:q1":
			q1Perm = pid
		case "9001:q2":
			q2Perm = pid
		}
	}
	if q1Perm == "" || q2Perm == "" {
		t.Fatalf("permission ids = %v, want 9001:q1 and 9001:q2", permIDs)
	}
	// Answer q1=A, q2=Y. The proc must not respond until both are answered.
	if err := proc.RespondPermission(q1Perm, "A"); err != nil {
		t.Fatalf("RespondPermission q1: %v", err)
	}
	// Give the broker a moment; the turn must NOT complete yet (q2 unanswered).
	if waitForOutcome(t, events, agentstream.OutcomeSuccess, 300*time.Millisecond) {
		t.Fatal("turn completed before all questions answered")
	}
	if err := proc.RespondPermission(q2Perm, "Y"); err != nil {
		t.Fatalf("RespondPermission q2: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn did not complete after all answers submitted")
	}
}

// TestCompatibleProcRequestUserInputMultiSelect verifies RespondPermissionMulti
// carries multiple labels for a multiSelect question.
func TestCompatibleProcRequestUserInputMultiSelect(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexRequestUserInputMultiScript)
	proc, _, err := Start(context.Background(), compatibleStartOptions(t, fake))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	go func() { _, _ = proc.Write([]byte("run\r")) }()

	permID := waitForPermission(t, events, 5*time.Second)
	if permID != "9001:q1" {
		t.Fatalf("permission id = %q, want 9001:q1", permID)
	}
	if err := proc.RespondPermissionMulti(permID, []string{"A", "B"}); err != nil {
		t.Fatalf("RespondPermissionMulti: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn did not complete after multi-select answer")
	}
}

// the compatible proc emits the native EventResult (provider=compatible) BEFORE the
// terminal turn_done state change.
func TestCompatibleProcTurnCompletedEmitsResultThenDone(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexScript)
	proc, out, err := Start(context.Background(), compatibleStartOptions(t, fake))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if gotDelta := waitFor(t, out, 3*time.Second); gotDelta != "hello from fake" {
		t.Fatalf("delta = %q, want %q", gotDelta, "hello from fake")
	}

	// Collect events until turn_done; record the order of result vs turn_done.
	var order []string
	var resultProvider string
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == agentstream.KindResultObserved && ev.Body != nil {
				order = append(order, "result")
				resultProvider = ev.Body.Provider
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				order = append(order, "turn_done")
				if len(order) < 2 || order[0] != "result" || order[1] != "turn_done" {
					t.Fatalf("event order = %#v, want [result turn_done]", order)
				}
				if resultProvider != "compatible" {
					t.Fatalf("result provider = %q, want compatible", resultProvider)
				}
				return
			}
		case <-deadline:
			t.Fatalf("did not observe turn_done; order so far = %#v", order)
		}
	}
}

// TestProcEmitsStructuredToolEventsForCommandAndFileChange asserts the codex
// proc maps item/started, item/completed, item/commandExecution/outputDelta,
// item/mcpToolCall/progress and item/fileChange/patchUpdated notifications to
// canonical ToolChanged payloads (v0.1.32/v0.1.33). This is what makes
// codex/compatible tool execution visible in the Feishu activity card and the CLI
// attach tool timeline.
func TestProcEmitsStructuredToolEventsForCommandAndFileChange(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexToolEventsScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("run a command, call an mcp tool, and edit a file\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Collect tool events until turn_done. Expect at least:
	//   command tool_started (item/started, running)
	//   command tool_started (outputDelta, running, with preview)
	//   command tool_completed (item/completed)
	//   ask_user MCP tool_started (item/started, running)
	//   ask_user MCP tool_updated (progress, running, with preview)
	//   ask_user MCP tool_completed (item/completed)
	//   file_change tool_started (item/started, running)
	//   file_change tool_completed (item/completed)
	var started, completed, failed int
	var sawCommandOutputDelta, sawCommandRawOutput, sawMcpStarted, sawMcpProgress, sawMcpCompleted, sawFileChangeStarted, sawFileChangeCompleted, sawFileChangeContent bool
	var trace []string
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				trace = append(trace, "turn_done")
				break loop
			}
			pe := ev.Body
			if pe.Tool == nil {
				trace = append(trace, string(pe.Type))
				break
			}
			trace = append(trace, string(pe.Type)+":"+pe.Tool.Name+":"+string(pe.ToolPhase))
			switch pe.ToolPhase {
			case agentstream.ToolPhaseStarted:
				started++
				if pe.Tool.Name == "command" && pe.Tool.OutputPreview != "" {
					sawCommandOutputDelta = true
				}
				if pe.Tool.Name == "ask_user" {
					sawMcpStarted = true
					if pe.Tool.OutputPreview != "" {
						sawMcpProgress = true
					}
				}
				if pe.Tool.Name == "file_change" {
					sawFileChangeStarted = true
				}
			case agentstream.ToolPhaseUpdated:
				if pe.Tool.Name == "command" && pe.Tool.OutputPreview != "" {
					sawCommandOutputDelta = true
				}
				if pe.Tool.ID == "m1" && pe.Tool.OutputPreview != "" {
					sawMcpProgress = true
				}
			case agentstream.ToolPhaseCompleted:
				completed++
				if pe.Tool.Name == "command" && len(pe.Tool.RawOutput) > 0 {
					sawCommandRawOutput = true
				}
				if pe.Tool.Name == "ask_user" {
					sawMcpCompleted = true
				}
				if pe.Tool.Name == "file_change" {
					sawFileChangeCompleted = true
					sawFileChangeContent = len(pe.Tool.Content) > 0 && len(pe.Tool.Locations) > 0
				}
			case agentstream.ToolPhaseFailed:
				failed++
			}
		case <-deadline:
			t.Fatalf("timed out before turn_done; started=%d completed=%d failed=%d; trace=%v", started, completed, failed, trace)
		}
	}
	if started < 3 || completed < 3 {
		t.Fatalf("expected >=3 tool_started and >=3 tool_completed; got started=%d completed=%d failed=%d", started, completed, failed)
	}
	if failed != 0 {
		t.Fatalf("expected 0 tool_failed, got %d", failed)
	}
	if !sawCommandOutputDelta {
		t.Error("did not observe command outputDelta as a tool_started with OutputPreview")
	}
	if !sawCommandRawOutput {
		t.Error("did not preserve completed command raw output")
	}
	if !sawMcpStarted || !sawMcpProgress || !sawMcpCompleted {
		t.Errorf("ask_user MCP lifecycle not observed: started=%v progress=%v completed=%v", sawMcpStarted, sawMcpProgress, sawMcpCompleted)
	}
	if !sawFileChangeStarted || !sawFileChangeCompleted {
		t.Errorf("file_change lifecycle not observed: started=%v completed=%v", sawFileChangeStarted, sawFileChangeCompleted)
	}
	if !sawFileChangeContent {
		t.Error("did not preserve file change content and locations")
	}
}

// TestProcThreadStatusSystemErrorEmitsFailure covers compatible
// thread/status/changed(status.type=systemError) being normalized to a
// terminal failed turn with a visible [system] frame (v0.1.38), instead of
// being silently ignored and leaving the turn hanging.
func TestProcThreadStatusSystemErrorEmitsFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexThreadStatusSystemErrorScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var sawSystemFrame, sawFailed bool
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Diagnostic != nil && bytes.Contains([]byte(ev.Text), []byte("systemError")) {
				sawSystemFrame = true
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				sawFailed = true
			}
			if sawSystemFrame && sawFailed {
				return
			}
		case <-deadline:
			t.Fatalf("did not observe systemError failure; systemFrame=%v failed=%v", sawSystemFrame, sawFailed)
		}
	}
}

// TestProcTurnFailureHintAnnotatesSystemError covers the TurnFailureHint hook
// (v0.1.148): when injected, a compatible systemError failure carries the hint text
// and the marker code in both the [system] frame and the failed state event,
// so downstream UIs (CLI attach, Feishu bridge) can classify it.
func TestProcTurnFailureHintAnnotatesSystemError(t *testing.T) {
	t.Parallel()
	const hint = "The provider credential has expired. Run the provider login command."
	fake := writeFakeCodex(t, fakeCodexThreadStatusSystemErrorScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
		TurnFailureHint: func(reason, errorCode string) (string, string) {
			return hint, "auth_expired"
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var frame []byte
	var failed *agentstream.Diagnostic
	deadline := time.After(3 * time.Second)
	for frame == nil || failed == nil {
		select {
		case ev := <-events:
			if ev.Diagnostic != nil && bytes.Contains([]byte(ev.Text), []byte("systemError")) {
				frame = append([]byte(nil), []byte(ev.Text)...)
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError && ev.Body != nil {
				failed = ev.Body.Error
			}
		case <-deadline:
			t.Fatalf("did not observe systemError failure; frame=%q failed=%+v", frame, failed)
		}
	}
	if !bytes.Contains(frame, []byte(hint)) {
		t.Fatalf("[system] frame missing re-login hint: %q", frame)
	}
	if !bytes.Contains(frame, []byte("(code=auth_expired)")) {
		t.Fatalf("[system] frame missing auth_expired marker: %q", frame)
	}
	if failed == nil || failed.Code != "auth_expired" || !strings.Contains(failed.Message, hint) {
		t.Fatalf("failed event error = %+v, want code auth_expired with hint", failed)
	}
}

// TestProcTurnFailureHintKeepsExistingCode covers the hook not clobbering a
// code the provider already reported: the hint is appended to the reason but
// the original error code wins.
func TestProcTurnFailureHintKeepsExistingCode(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexTurnFailedScript)
	var hookCode atomic.Value
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
		TurnFailureHint: func(reason, errorCode string) (string, string) {
			hookCode.Store(errorCode)
			return "some hint", "auth_expired"
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError && ev.Body != nil && ev.Body.Error != nil {
				if got, _ := hookCode.Load().(string); got != "internal" {
					t.Fatalf("hint hook saw code %q, want the provider-reported \"internal\"", got)
				}
				if ev.Body.Error.Code != "internal" {
					t.Fatalf("hook code clobbered provider-reported code: got %q", ev.Body.Error.Code)
				}
				return
			}
		case <-deadline:
			t.Fatal("did not observe turn/failed failure")
		}
	}
}

// TestProcTurnFailedEmitsFailure covers turn/failed producing a terminal
// failed turn with a visible [system] frame (v0.1.38).
func TestProcTurnFailedEmitsFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexTurnFailedScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var sawSystemFrame, sawFailed bool
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Diagnostic != nil && bytes.Contains([]byte(ev.Text), []byte("boom")) {
				sawSystemFrame = true
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				sawFailed = true
			}
			if sawSystemFrame && sawFailed {
				return
			}
		case <-deadline:
			t.Fatalf("did not observe turn/failed failure; systemFrame=%v failed=%v", sawSystemFrame, sawFailed)
		}
	}
}

// TestProcTopLevelErrorEmitsFailure covers a top-level "error" notification
// during an active turn being routed through emitTurnFailure (v0.1.38).
func TestProcTopLevelErrorEmitsFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexTopLevelErrorScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var sawSpecificError bool
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError &&
				ev.Body != nil && ev.Body.Error != nil {
				sawSpecificError = ev.Body.Error.Message == " model unavailable " &&
					ev.Body.Error.Code == " no_models "
			}
			if sawSpecificError {
				return
			}
		case <-deadline:
			t.Fatal("did not observe specific top-level provider failure")
		}
	}
}

func TestProcRetryableTopLevelErrorsWaitForAuthoritativeCompletion(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexRetryableTopLevelErrorScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var result string
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Diagnostic != nil {
				t.Fatalf("retryable error emitted system output: %q", []byte(ev.Text))
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				t.Fatal("retryable error terminated the active turn")
			}
			if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) && ev.Body.Result != nil {
				result = ev.Body.Result.Text
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				if result != "recovered response" {
					t.Fatalf("result = %q, want recovered response", result)
				}
				return
			}
		case <-deadline:
			t.Fatal("did not observe turn completion after retryable errors")
		}
	}
}

func TestProcRetryableErrorAfterCompletedMessageWaitsForTerminal(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexRetryAfterCompletedMessageScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var result string
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				t.Fatal("retryable error terminated the active turn")
			}
			if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) && ev.Body.Result != nil {
				result = ev.Body.Result.Text
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				if result != "partial recovered" {
					t.Fatalf("turn completed before retry recovery: result=%q", result)
				}
				return
			}
		case <-deadline:
			t.Fatal("did not observe authoritative completion after retry")
		}
	}
}

// TestProcNestedTopLevelErrorEmitsSpecificFailure covers the current Codex/
// Compatible app-server ErrorNotification shape:
// params.error.message + params.error.codexErrorInfo. The diagnostic must not
// collapse to the generic "provider error" fallback.
func TestProcNestedTopLevelErrorEmitsSpecificFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		errorInfo string
		wantCode  string
	}{
		{name: "string category", errorInfo: `"serverOverloaded"`, wantCode: "serverOverloaded"},
		{name: "object variant", errorInfo: `{"httpConnectionFailed":{"httpStatusCode":503}}`, wantCode: "httpConnectionFailed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script := strings.Replace(fakeCodexNestedTopLevelErrorScript, "__CODEX_ERROR_INFO__", tc.errorInfo, 1)
			fake := writeFakeCodex(t, script)
			proc, _, err := startTestProc(t, StartOptions{
				Cwd:          t.TempDir(),
				Executable:   fake,
				ProviderName: "compatible",
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer proc.Close()

			events := make(chan testEmission, 16)
			proc.SetEventSink(captureEventSink(events))

			if _, err := proc.Write([]byte("hello\r")); err != nil {
				t.Fatalf("Write: %v", err)
			}

			var sawSpecificError bool
			deadline := time.After(3 * time.Second)
			for {
				select {
				case ev := <-events:
					if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError &&
						ev.Body != nil && ev.Body.Error != nil {
						sawSpecificError = ev.Body.Error.Message == "model service overloaded" &&
							ev.Body.Error.Code == tc.wantCode
					}
					if sawSpecificError {
						return
					}
				case <-deadline:
					t.Fatal("did not observe specific nested provider failure")
				}
			}
		})
	}
}

// TestProcProcessExitMidTurnEmitsFailure covers the provider process exiting
// mid-turn producing a terminal failure (v0.1.38) instead of a silent hang.
func TestProcProcessExitMidTurnEmitsFailure(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexExitMidTurnScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	// The fake exits mid-turn, which can close the pipe before/during Write;
	// that is expected and is exactly the failure we're testing. Don't treat
	// a Write error as fatal.
	_, _ = proc.Write([]byte("hello\r"))

	var sawSystemFrame, sawFailed bool
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Diagnostic != nil && bytes.Contains([]byte(ev.Text), []byte("process exited")) {
				sawSystemFrame = true
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				sawFailed = true
			}
			if sawSystemFrame && sawFailed {
				return
			}
		case <-deadline:
			t.Fatalf("did not observe process-exit failure; systemFrame=%v failed=%v", sawSystemFrame, sawFailed)
		}
	}
}

// TestProcCloseSuppressesProcessExitFailure covers intentional teardown: Close
// kills the provider mid-turn during session stop/restart/reconfigure, and the
// exit watcher must not turn that into a spurious "provider process exited"
// failure — the session side ends the turn itself (Kill -> abortTurn). The
// counterpart self-exit case is TestProcProcessExitMidTurnEmitsFailure.
func TestProcCloseSuppressesProcessExitFailure(t *testing.T) {
	t.Parallel()
	// The zombie script arms a turn then hangs with no terminal event; the
	// watchdog is not configured, so only the process exit can end the turn.
	fake := writeFakeCodex(t, fakeCodexZombieTurnScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))

	// Write returns after the turn/start response, which arms the turn.
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Intentional teardown with a live turn.
	if err := proc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The exit watcher closes out after deciding whether to emit a failure,
	// so once out is closed any failure event would already be queued.
	for range out {
	}

	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError {
				t.Fatalf("intentional Close emitted a turn failure: %+v", ev)
			}
			if ev.Diagnostic != nil && bytes.Contains([]byte(ev.Text), []byte("process exited")) {
				t.Fatalf("intentional Close emitted a process-exit system frame: %q", []byte(ev.Text))
			}
		default:
			return
		}
	}
}

const fakeCodexThreadStatusIdleScript = `#!/usr/bin/env python3
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
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"hello from compatible","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":thread_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexTurnCompletedThenIdleScript = `#!/usr/bin/env python3
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
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"hello from compatible","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":thread_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

const fakeCodexIdleThenTurnCompletedScript = `#!/usr/bin/env python3
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
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"hello from compatible","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":thread_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexToolEventsScript emits a command execution (item/started +
// outputDelta + item/completed), an MCP tool call (item/started + progress +
// item/completed) and a file change (item/started + item/completed) between
// turn/started and turn/completed, exercising the v0.1.32/v0.1.33 tool event
// mapping. Field names follow the compatible app-server schema (camelCase).
const fakeCodexToolEventsScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
cmd_item = {"id":"c1","type":"commandExecution","command":"ls -la","status":"inProgress","cwd":"/tmp","aggregatedOutput":None,"exitCode":None}
mcp_item = {"id":"m1","type":"mcpToolCall","tool":"ask_user","server":"amux","status":"inProgress","arguments":{"question":"which?"}}
fc_item = {"id":"f1","type":"fileChange","status":"inProgress","changes":[{"path":"src/a.go","diff":"--- a\n+++ b\n","kind":{"type":"update"}}]}
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
        # command execution lifecycle
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"item":cmd_item,"threadId":thread_id,"turnId":turn_id,"startedAtMs":1}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/commandExecution/outputDelta","params":{"delta":"total 0\ndrwxr-xr-x src\n","itemId":"c1","threadId":thread_id,"turnId":turn_id}}), flush=True)
        cmd_done = dict(cmd_item, status="completed", aggregatedOutput="total 0\ndrwxr-xr-x src\n", exitCode=0)
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"item":cmd_done,"threadId":thread_id,"turnId":turn_id,"completedAtMs":2}}), flush=True)
        # mcp tool call lifecycle
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"item":mcp_item,"threadId":thread_id,"turnId":turn_id,"startedAtMs":3}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/mcpToolCall/progress","params":{"itemId":"m1","message":"waiting on user","threadId":thread_id,"turnId":turn_id}}), flush=True)
        mcp_done = dict(mcp_item, status="completed")
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"item":mcp_done,"threadId":thread_id,"turnId":turn_id,"completedAtMs":4}}), flush=True)
        # file change lifecycle
        print(json.dumps({"jsonrpc":"2.0","method":"item/started","params":{"item":fc_item,"threadId":thread_id,"turnId":turn_id,"startedAtMs":5}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/fileChange/patchUpdated","params":{"changes":[{"path":"src/a.go","diff":"--- a\n+++ b\n","kind":{"type":"update"}}],"itemId":"f1","threadId":thread_id,"turnId":turn_id}}), flush=True)
        fc_done = dict(fc_item, status="completed")
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"item":fc_done,"threadId":thread_id,"turnId":turn_id,"completedAtMs":6}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"done","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexRequestUserInputScript drives a compatible-style item/tool/requestUserInput
// round trip. It sends one request with two questions; the test answers each
// via RespondPermission and the script asserts the aggregated answers payload.
const fakeCodexRequestUserInputScript = `#!/usr/bin/env python3
import json, sys
thread_id = "t1"
turn_id = "u1"
pending_turn_id = None
got_answers = None
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
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/tool/requestUserInput","params":{"itemId":"it1","threadId":thread_id,"turnId":turn_id,"questions":[{"id":"q1","header":"更新类型","question":"选哪个？","multiSelect":False,"options":[{"label":"A","description":"a"},{"label":"B","description":"b"}]},{"id":"q2","header":"范围","question":"多大？","multiSelect":False,"options":[{"label":"X","description":"x"},{"label":"Y","description":"y"}]}]}}), flush=True)
    elif mid == 9001 and "result" in msg:
        got_answers = msg["result"]["answers"]
        assert got_answers["q1"]["answers"] == ["A"], got_answers
        assert got_answers["q2"]["answers"] == ["Y"], got_answers
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":pending_turn_id,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCodexNestedTopLevelErrorScript emits the current v2 ErrorNotification
// shape used by Codex/Compatible app-server.
const fakeCodexNestedTopLevelErrorScript = `#!/usr/bin/env python3
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
        print(json.dumps({"jsonrpc":"2.0","method":"error","params":{"error":{"message":"model service overloaded","codexErrorInfo":__CODEX_ERROR_INFO__},"threadId":thread_id,"turnId":turn_id,"willRetry":False}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCompatibleCompletedMessageWithoutTerminalScript reproduces a Compatible turn that
// completes a full assistant message but never emits turn/completed or
// thread/status/changed(idle).
const fakeCompatibleCompletedMessageWithoutTerminalScript = `#!/usr/bin/env python3
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
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"完整回答","threadId":thread_id,"turnId":turn_id,"itemId":"message-1"}}), flush=True)
        item = {"id":"message-1","type":"agentMessage","text":"完整回答"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCompatibleIntermediateMessageThenReasoningScript keeps reasoning after a
// completed progress message without ever completing the turn.
const fakeCompatibleIntermediateMessageThenReasoningScript = `#!/usr/bin/env python3
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
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"我先继续分析","threadId":thread_id,"turnId":turn_id,"itemId":"message-1"}}), flush=True)
        item = {"id":"message-1","type":"agentMessage","text":"我先继续分析"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        time.sleep(0.02)
        print(json.dumps({"jsonrpc":"2.0","method":"item/reasoning/textDelta","params":{"delta":"仍在推理","threadId":thread_id,"turnId":turn_id}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// fakeCompatibleIntermediateMessageThenOrphanToolCompletionScript covers a dropped
// item/started notification: item/completed still proves that execution
// continued after the assistant message.
const fakeCompatibleIntermediateMessageThenOrphanToolCompletionScript = `#!/usr/bin/env python3
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
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"我先执行工具","threadId":thread_id,"turnId":turn_id,"itemId":"message-1"}}), flush=True)
        item = {"id":"message-1","type":"agentMessage","text":"我先执行工具"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":item}}), flush=True)
        time.sleep(0.02)
        tool = {"id":"tool-1","type":"commandExecution","status":"completed","aggregatedOutput":"done"}
        print(json.dumps({"jsonrpc":"2.0","method":"item/completed","params":{"threadId":thread_id,"turnId":turn_id,"item":tool}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcCompletedMessageWithoutTerminalFailsByWatchdog(t *testing.T) {
	t.Parallel()
	for _, providerName := range testAppServerProviders() {
		for _, tc := range []struct {
			name     string
			watchdog config.StructuredProviderConfig
			want     string
		}{
			{name: "idle", watchdog: config.StructuredProviderConfig{TurnIdleTimeoutSeconds: 1}, want: "turn idle timeout"},
			{name: "hard_cap", watchdog: config.StructuredProviderConfig{TurnHardCapSeconds: 1}, want: "turn hard cap exceeded"},
		} {
			t.Run(providerName+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				fake := writeFakeCodex(t, fakeCompatibleCompletedMessageWithoutTerminalScript)
				proc, _, err := startTestProc(t, StartOptions{
					Cwd:             t.TempDir(),
					CodexExecutable: fake,
					ProviderName:    providerName,
					Watchdog:        tc.watchdog,
				})
				if err != nil {
					t.Fatalf("Start: %v", err)
				}
				defer proc.Close()
				events := make(chan testEmission, 16)
				proc.SetEventSink(captureEventSink(events))
				if _, err := proc.Write([]byte("hello\r")); err != nil {
					t.Fatalf("Write: %v", err)
				}
				deadline := time.After(3 * time.Second)
				for {
					select {
					case ev := <-events:
						if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) {
							t.Fatal("message was promoted to a result without provider completion")
						}
						if ev.Terminal != nil {
							if ev.Terminal.Outcome != agentstream.OutcomeError || ev.Terminal.Message != tc.want {
								t.Fatalf("terminal = %+v, want error %q", ev.Terminal, tc.want)
							}
							return
						}
					case <-deadline:
						t.Fatal("missing terminal left the turn stuck past its watchdog budget")
					}
				}
			})
		}
	}
}

func TestProcCompatibleReasoningAfterCompletedMessageWaitsForTerminal(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCompatibleIntermediateMessageThenReasoningScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		ProviderName:    "compatible",
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
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	quietWindow := time.NewTimer(50 * time.Millisecond)
	defer quietWindow.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				t.Fatal("intermediate completed message ended the turn after later reasoning")
			}
		case <-quietWindow.C:
			if !waitForOutcome(t, events, agentstream.OutcomeError, 2*time.Second) {
				t.Fatal("turn did not fall back to the configured idle failure")
			}
			return
		}
	}
}

func TestProcCompatibleOrphanToolAfterCompletedMessageWaitsForTerminal(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCompatibleIntermediateMessageThenOrphanToolCompletionScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		ProviderName:    "compatible",
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
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	quietWindow := time.NewTimer(50 * time.Millisecond)
	defer quietWindow.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				t.Fatal("orphan tool completion ended the turn without a provider terminal")
			}
		case <-quietWindow.C:
			if !waitForOutcome(t, events, agentstream.OutcomeError, 2*time.Second) {
				t.Fatal("turn did not fall back to the configured idle failure")
			}
			return
		}
	}
}

// fakeCodexIdleDuringApprovalScript reports thread/status idle while blocked
// on an approval, then completes the turn only after the approval is
// answered.
const fakeCodexIdleDuringApprovalScript = `#!/usr/bin/env python3
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
        print(json.dumps({"jsonrpc":"2.0","id":9001,"method":"item/commandExecution/requestApproval","params":{"command":"ls","threadId":thread_id,"turnId":turn_id}}), flush=True)
        # Compatible reports thread idle while waiting for the user.
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":thread_id}}), flush=True)
    elif mid == 9001 and "result" in msg:
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":pending_turn_id,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcIdleCompletionDeferredDuringApproval verifies the 5s stable-idle
// fallback does NOT complete the turn while the provider is blocked on a user
// response — otherwise everything after the user's answer is silently dropped.
func TestProcIdleCompletionDeferredDuringApproval(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexIdleDuringApprovalScript)
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
	// The fake answers turn/start only after the approval round-trip, so the
	// Write would block until then.
	go func() {
		_, _ = proc.Write([]byte("go\r"))
	}()
	permID := waitForPermission(t, events, 3*time.Second)
	if permID == "" {
		t.Fatal("did not receive permission event")
	}
	// Past the 5s stable-idle delay the turn must still be open: no turn_done.
	deadline := time.After(80 * time.Millisecond)
	for done := false; !done; {
		select {
		case ev := <-events:
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				t.Fatal("idle fallback completed the turn while the provider was waiting on the user")
			}
		case <-deadline:
			done = true
		}
	}
	if err := proc.RespondPermission(permID, "accept"); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn did not complete after the approval was answered")
	}
}

// fakeCodexZeroOutputIdleScript emits turn/started then thread idle with NO
// output/tool/approval — the Compatible empty-response completion pattern.
const fakeCodexZeroOutputIdleScript = `#!/usr/bin/env python3
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
        usage = {"last":{"inputTokens":59150,"outputTokens":1,"totalTokens":59151},"total":{"inputTokens":59150,"outputTokens":1,"totalTokens":59151}}
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":thread_id,"turnId":turn_id,"tokenUsage":usage}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"},"threadId":thread_id}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        if mid is not None:
            print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcZeroOutputTurnIdleFails visibly rejects an idle terminal that has
// neither assistant output nor tool activity, instead of silently completing.
func TestProcZeroOutputTurnIdleFails(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexZeroOutputIdleScript)
	proc, out, err := startTestProc(t, StartOptions{
		Cwd:             t.TempDir(),
		CodexExecutable: fake,
		Watchdog: config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: 30, // long — a spurious idle-timeout would NOT be why it completes
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
	got, ok := collectUntilOutcome(t, events, agentstream.OutcomeError, 8*time.Second)
	if !ok {
		t.Fatal("zero-output idle turn was not failed visibly")
	}
	sawFrame, sawFailure := false, false
	for _, ev := range got {
		if ev.Diagnostic != nil && strings.Contains(ev.Diagnostic.Message, "provider completed without assistant output") && ev.Diagnostic.Code == "empty_response" {
			sawFrame = true
		}
		if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError && ev.Body != nil && ev.Body.Error != nil && ev.Body.Error.Code == "empty_response" {
			sawFailure = true
		}
		if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) {
			t.Fatal("zero-output turn emitted an empty result")
		}
	}
	if !sawFrame || !sawFailure {
		t.Fatalf("empty-response failure incomplete: frame=%v failure=%v events=%+v", sawFrame, sawFailure, got)
	}
}

// fakeCodexZeroOutputCompletedScript matches the authoritative terminal shape
// observed from Compatible: a turn/completed notification without an agent message.
const fakeCodexZeroOutputCompletedScript = `#!/usr/bin/env python3
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
        usage = {"last":{"inputTokens":59150,"outputTokens":1,"totalTokens":59151},"total":{"inputTokens":59150,"outputTokens":1,"totalTokens":59151}}
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":thread_id,"turnId":turn_id,"tokenUsage":usage}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    elif mid is not None:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

func TestProcZeroOutputTurnCompletedFails(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexZeroOutputCompletedScript)
	proc, out, err := startTestProc(t, StartOptions{Cwd: t.TempDir(), CodexExecutable: fake})
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
	got, ok := collectUntilOutcome(t, events, agentstream.OutcomeError, 3*time.Second)
	if !ok {
		t.Fatal("zero-output turn/completed was not failed visibly")
	}
	for _, ev := range got {
		if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeError && ev.Body != nil && ev.Body.Error != nil && ev.Body.Error.Code == "empty_response" {
			return
		}
	}
	t.Fatalf("missing empty_response terminal: %+v", got)
}

// TestProcTokenUsageCarriesSpeedMetrics asserts the proc measures TTFT (from
// turn/start send to the first streamed delta) and output tokens per second,
// attaching both to the normalized EventUsage payload when derivable.
func TestProcTokenUsageCarriesSpeedMetrics(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexSpeedMetricsScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ev := waitForEmissionKind(t, events, string(agentstream.KindUsageObserved), 3*time.Second)
	if ev == nil || ev.Body == nil {
		t.Fatal("did not observe usage event")
	}
	var usage map[string]int64
	if err := json.Unmarshal(ev.Body.Usage, &usage); err != nil {
		t.Fatalf("decode normalized usage: %v", err)
	}
	if ttft, ok := usage["ttft_ms"]; !ok || ttft <= 0 {
		t.Fatalf("usage[ttft_ms] = %d (ok=%t), want positive after first delta; usage=%v", ttft, ok, usage)
	}
	if _, ok := usage["context_window"]; !ok {
		t.Fatalf("usage missing context_window; usage=%v", usage)
	}
	if tps, ok := usage["tokens_per_sec"]; !ok || tps <= 0 {
		t.Fatalf("usage[tokens_per_sec] = %d (ok=%t), want positive for 20 output tokens over >=1s; usage=%v", tps, ok, usage)
	}
}

// TestProcTokenUsageBeforeFirstDeltaOmitsSpeedMetrics asserts usage events
// that arrive before any streamed delta do not carry bogus TTFT/speed values.
func TestProcTokenUsageBeforeFirstDeltaOmitsSpeedMetrics(t *testing.T) {
	t.Parallel()
	// Reuse the standard tokenUsage script: it emits tokenUsage/updated
	// before any agentMessage delta, so neither metric is derivable yet.
	fake := writeFakeCodex(t, fakeCodexTokenUsageScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ev := waitForEmissionKind(t, events, string(agentstream.KindUsageObserved), 3*time.Second)
	if ev == nil || ev.Body == nil {
		t.Fatal("did not observe usage event")
	}
	var usage map[string]int64
	if err := json.Unmarshal(ev.Body.Usage, &usage); err != nil {
		t.Fatalf("decode normalized usage: %v", err)
	}
	if _, ok := usage["ttft_ms"]; ok {
		t.Fatalf("usage[ttft_ms] present before first delta; usage=%v", usage)
	}
	if _, ok := usage["tokens_per_sec"]; ok {
		t.Fatalf("usage[tokens_per_sec] present before first delta; usage=%v", usage)
	}
}

// TestProcTurnDoneBackfillsSpeedMetrics reproduces the production shape where
// the provider emits thread/tokenUsage/updated only once, before the 0.5s
// generation window elapses: the mid-turn usage payload carries ttft_ms but no
// tokens_per_sec. The turn boundary must backfill the missing speed fields on
// the final usage (EventResult/EventTurnDone), matching what the collapsed
// card title renders.
func TestProcTurnDoneBackfillsSpeedMetrics(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexEarlyUsageScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Mid-turn usage arrives before the 0.5s window: no tokens_per_sec yet.
	mid := waitForEmissionKind(t, events, string(agentstream.KindUsageObserved), 3*time.Second)
	if mid == nil || mid.Body == nil {
		t.Fatal("did not observe mid-turn usage event")
	}
	var midUsage map[string]int64
	if err := json.Unmarshal(mid.Body.Usage, &midUsage); err != nil {
		t.Fatalf("decode mid-turn usage: %v", err)
	}
	if _, ok := midUsage["ttft_ms"]; !ok {
		t.Fatalf("mid-turn usage missing ttft_ms; usage=%v", midUsage)
	}
	if _, ok := midUsage["tokens_per_sec"]; ok {
		t.Fatalf("mid-turn usage unexpectedly carries tokens_per_sec before the generation window; usage=%v", midUsage)
	}

	// The final result usage must carry both speed fields.
	res := waitForEmissionKind(t, events, string(agentstream.KindResultObserved), 3*time.Second)
	if res == nil || res.Body == nil || res.Body.Result == nil {
		t.Fatal("did not observe result event")
	}
	var finalUsage map[string]int64
	if err := json.Unmarshal(res.Body.Result.Usage, &finalUsage); err != nil {
		t.Fatalf("decode final usage: %v", err)
	}
	if ttft, ok := finalUsage["ttft_ms"]; !ok || ttft <= 0 {
		t.Fatalf("final usage ttft_ms = %d (ok=%t), want positive; usage=%v", ttft, ok, finalUsage)
	}
	if tps, ok := finalUsage["tokens_per_sec"]; !ok || tps <= 0 {
		t.Fatalf("final usage tokens_per_sec = %d (ok=%t), want backfilled at turn end; usage=%v", tps, ok, finalUsage)
	}
}

// fakeCodexEarlyUsageScript mirrors the observed glm-compatible behavior: one
// tokenUsage notification ~0.3s after the first delta (inside the 0.5s
// minimum generation window), no later usage frames, then turn/completed
// ~0.8s after the first delta.
const fakeCodexEarlyUsageScript = `#!/usr/bin/env python3
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
        time.sleep(0.3)
        usage = {
            "last": {"cachedInputTokens":0,"cacheCreationInputTokens":0,"inputTokens":100,"outputTokens":15,"reasoningOutputTokens":0,"totalTokens":115},
            "total": {"cachedInputTokens":0,"cacheCreationInputTokens":0,"inputTokens":100,"outputTokens":15,"reasoningOutputTokens":0,"totalTokens":115},
            "modelContextWindow": 128000,
            "autoCompactTokenLimit": 100000
        }
        print(json.dumps({"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":thread_id,"turnId":turn_id,"tokenUsage":usage}}), flush=True)
        time.sleep(0.5)
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"lo","threadId":thread_id,"turnId":turn_id,"itemId":"i1"}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}), flush=True)
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}), flush=True)
    else:
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}), flush=True)
`

// TestProcSpeedMetricsAcrossSequentialTurns asserts TTFT is re-measured on
// every turn, not only the first: two sequential writes must each produce a
// usage event carrying a fresh ttft_ms.
func TestProcSpeedMetricsAcrossSequentialTurns(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexTwoTurnSpeedScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("one\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	first := waitForEmissionKind(t, events, string(agentstream.KindUsageObserved), 3*time.Second)
	if first == nil || first.Body == nil {
		t.Fatal("no usage event for first turn")
	}
	var usage1 map[string]int64
	if err := json.Unmarshal(first.Body.Usage, &usage1); err != nil {
		t.Fatalf("decode usage1: %v", err)
	}
	if ttft, ok := usage1["ttft_ms"]; !ok || ttft <= 0 {
		t.Fatalf("turn 1 usage ttft_ms = %d (ok=%t), want positive; usage=%v", ttft, ok, usage1)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("turn 1 did not finish")
	}

	if _, err := proc.Write([]byte("two\r")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	second := waitForEmissionKind(t, events, string(agentstream.KindUsageObserved), 3*time.Second)
	if second == nil || second.Body == nil {
		t.Fatal("no usage event for second turn")
	}
	var usage2 map[string]int64
	if err := json.Unmarshal(second.Body.Usage, &usage2); err != nil {
		t.Fatalf("decode usage2: %v", err)
	}
	if ttft, ok := usage2["ttft_ms"]; !ok || ttft <= 0 {
		t.Fatalf("turn 2 usage ttft_ms = %d (ok=%t), want positive on every turn; usage=%v", ttft, ok, usage2)
	}
}

// TestProcToolTurnSpeedExcludesToolTime asserts the generation rate excludes
// tool execution time. The fake turn generates for ~2s before a tool, runs the
// tool for ~6s, then generates for ~2s after it: 120 output tokens over ~4s of
// active generation is ~30 tok/s, while wall-clock division would report ~14
// or less. The assertion floor of 20 tok/s separates the two models robustly.
func TestProcToolTurnSpeedExcludesToolTime(t *testing.T) {
	t.Parallel()
	fake := writeFakeCodex(t, fakeCodexToolTurnScript)
	proc, _, err := startTestProc(t, StartOptions{
		Cwd:          t.TempDir(),
		Executable:   fake,
		ProviderName: "compatible",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 64)
	proc.SetEventSink(captureEventSink(events))
	if _, err := proc.Write([]byte("run the tool\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res := waitForEmissionKind(t, events, string(agentstream.KindResultObserved), 20*time.Second)
	if res == nil || res.Body == nil || res.Body.Result == nil {
		t.Fatal("did not observe result event")
	}
	var usage map[string]int64
	if err := json.Unmarshal(res.Body.Result.Usage, &usage); err != nil {
		t.Fatalf("decode result usage: %v", err)
	}
	if ttft, ok := usage["ttft_ms"]; !ok || ttft <= 0 {
		t.Fatalf("usage[ttft_ms] = %d (ok=%t), want positive; usage=%v", ttft, ok, usage)
	}
	tps, ok := usage["tokens_per_sec"]
	if !ok || tps <= 0 {
		t.Fatalf("usage[tokens_per_sec] = %d (ok=%t), want positive; usage=%v", tps, ok, usage)
	}
	if tps < 20 {
		t.Fatalf("usage[tokens_per_sec] = %d, want >= 20 (tool time excluded); usage=%v", tps, usage)
	}
}
