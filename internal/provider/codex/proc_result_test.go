package codex

import (
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestEmitTurnProducesTypedTurnPayload(t *testing.T) {
	proc := &Proc{}
	events := make(chan agentstream.ProducerEmission, 1)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		events <- emission
		return false
	})

	proc.emitTurn(agentstream.AssistantDelta{Text: "hello"}, agentstream.ActorAgent, "agent", "assistant_text_delta", "")
	emission := <-events
	delta, ok := emission.Turn.(agentstream.AssistantDelta)
	if !ok || delta.Text != "hello" || emission.Terminal != nil || emission.Session != nil {
		t.Fatalf("producer emission = %#v", emission)
	}
}

// Codex has no native "final message" in turn/completed, so the proc must
// aggregate the agentMessage deltas of the turn and emit a native EventResult
// (carrying the accumulated text) BEFORE turn_done. The result event must not be
// agent-sourced so it never pollutes the task transcript.
func TestProcEmitsNativeResultBeforeTurnDone(t *testing.T) {
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

	events := make(chan testEmission, 16)
	proc.SetEventSink(captureEventSink(events))
	go func() { // drain the raw delta channel so it never blocks
		for range out {
		}
	}()

	if _, err := proc.Write([]byte("hello\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var sawResult bool
	var resultText string
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("did not observe turn_done")
		case ev := <-events:
			if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) {
				if ev.Origin.Channel == "agent" {
					t.Fatal("native result must not be agent-sourced")
				}
				sawResult = true
				if ev.Body.Result != nil {
					resultText = ev.Body.Result.Text
				}
				continue
			}
			if ev.Terminal != nil && ev.Terminal.Outcome == agentstream.OutcomeSuccess {
				if !sawResult {
					t.Fatal("turn_done arrived before the native result event")
				}
				if resultText != "hello from fake" {
					t.Fatalf("result text = %q, want %q", resultText, "hello from fake")
				}
				return
			}
		}
	}
}
