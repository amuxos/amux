package task

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/store"
)

func TestRunnerToolActivityIsTurnScopedRedactedAndReplayable(t *testing.T) {
	const secret = "sk-tool-secret-000111222"
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			change := agentstream.ToolChanged{Phase: agentstream.ToolPhaseStarted, Tool: agentstream.Tool{
				ID: "tool-1", Name: "shell-" + secret, Title: "inspect " + secret,
				Input: json.RawMessage(`{"command":"raw-input"}`), OutputPreview: "raw-preview",
				Error: "raw-error", Content: json.RawMessage(`"raw-content"`), RawOutput: json.RawMessage(`"raw-output"`),
			}}
			c.emit(agentstream.Event{Turn: &agentstream.TurnRef{ID: "old-turn"}, Body: change})
			c.emit(agentstream.Event{Body: change})
			change.Phase = agentstream.ToolPhaseUpdated
			change.Tool.OutputPreview = "changed-raw-preview"
			c.emit(agentstream.Event{Body: change})
			change.Phase = agentstream.ToolPhaseCompleted
			c.emit(agentstream.Event{Body: change})
			change.Tool.ID = "tool-2"
			change.Phase = agentstream.ToolPhaseStarted
			c.emit(agentstream.Event{Body: change})
			change.Phase = agentstream.ToolPhaseFailed
			c.emit(agentstream.Event{Body: change})
			c.emit(agentText(`{"amux_task_result":{"summary":"done"}}`))
			c.emit(turnDone("end_turn"))
		}
	}
	durable := NewSQLStore(db)
	r, err := NewRunner(Options{AmuxDir: t.TempDir(), Store: durable, Events: durable, Control: ctrl, Creds: fixedCreds{env: "TOKEN=" + secret}, DefaultTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	run, err := r.Submit(Spec{Agent: "claude", Task: "inspect"}, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s: %s", final.Status, final.Error)
	}
	stream, unsub, err := r.Subscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	var got []TaskEvent
	for event := range stream {
		if event.Kind == TaskEventTool {
			got = append(got, event)
		}
	}
	if len(got) != 4 {
		t.Fatalf("tool events = %+v, want 4 (no stale/unchanged events)", got)
	}
	for i, want := range []string{"running", "completed", "running", "failed"} {
		ev := got[i]
		if ev.TurnID != final.TurnID || ev.Tool == nil || ev.Tool.Status != want {
			t.Fatalf("event %d = %+v", i, ev)
		}
		data, _ := json.Marshal(ev)
		for _, forbidden := range []string{secret, "raw-input", "raw-preview", "raw-error", "raw-content", "raw-output", "input", "output_preview"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("event leaked %q: %s", forbidden, data)
			}
		}
		if !strings.Contains(ev.Tool.Title, "inspect") || !strings.Contains(ev.Tool.Name, "shell") {
			t.Fatalf("display metadata lost: %+v", ev.Tool)
		}
	}
}
