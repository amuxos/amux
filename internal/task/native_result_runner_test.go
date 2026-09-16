package task

import (
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

// nativeResultEvent is the canonical final result emitted before TurnFinished.
func nativeResultEvent(text string) agentstream.Event {
	return agentstream.Event{Body: agentstream.ResultObserved{Text: text}}
}

func TestChildPromptEnvelopeToggle(t *testing.T) {
	withEnv := childPrompt("do it", true)
	if !strings.Contains(withEnv, "do it") || !strings.Contains(withEnv, "amux_task_result") {
		t.Fatalf("injected prompt missing task or envelope: %q", withEnv)
	}
	noEnv := childPrompt("do it", false)
	if noEnv != "do it" {
		t.Fatalf("non-injected prompt should be the bare task, got %q", noEnv)
	}
}

// A native-result agent must NOT get the envelope instruction, and the runner
// must record the summary from the provider's native result event.
func TestRunnerNativeResultSkipsEnvelope(t *testing.T) {
	ctrl := &fakeController{target: "local"}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(nativeResultEvent("did the thing natively"))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		DefaultTimeout: 2 * time.Second,
		NativeResult:   func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, _ := r.Submit(Spec{Agent: "claude", Task: "summarize the dir"}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s err=%s", final.Status, final.Error)
	}
	if final.Result == nil || final.Result.Summary != "did the thing natively" {
		t.Fatalf("summary = %+v", final.Result)
	}
	got := ctrl.last().submittedText(0)
	if strings.Contains(got, "amux_task_result") {
		t.Fatalf("native-result agent should not get envelope injection: %q", got)
	}
	if !strings.Contains(got, "summarize the dir") {
		t.Fatalf("user task dropped: %q", got)
	}
}

// When an agent volunteers an envelope as well, its summary overrides the native
// final message (richer, more concise).
func TestRunnerEnvelopeOverridesNativeSummary(t *testing.T) {
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentText(`prose... {"amux_task_result":{"summary":"envelope wins"}}`))
			c.emit(nativeResultEvent("native text loses"))
			c.emit(turnDone("end_turn"))
		}
	}
	r, err := NewRunner(Options{
		AmuxDir:        t.TempDir(),
		Store:          NewMemStore(),
		Control:        ctrl,
		DefaultTimeout: 2 * time.Second,
		NativeResult:   func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	run, _ := r.Submit(Spec{Agent: "claude", Task: "x"}, "", "", 0)
	final := waitTerminal(t, r, run.ID)
	if final.Result == nil || final.Result.Summary != "envelope wins" {
		t.Fatalf("envelope should override native summary, got %+v", final.Result)
	}
}
