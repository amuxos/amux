package task

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/store"
)

func TestRunnerUsageIsNativeTurnScopedAndReplayable(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	live := json.RawMessage(`{"input_tokens":100,"total_tokens":120,"cumulative_input_tokens":343000,"cumulative_cached_input_tokens":300000,"cumulative_output_tokens":12000,"provider_secret":"do-not-expose","output_tokens":"invalid","context_window":null,"ttft_ms":-1}`)
	finalUsage := json.RawMessage(`{"input_tokens":110,"output_tokens":20,"total_tokens":130,"cumulative_input_tokens":343110,"cumulative_cached_input_tokens":300100,"cumulative_output_tokens":12020}`)
	ctrl := &fakeController{}
	ctrl.onCreate = func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			c.emit(agentstream.Event{Turn: &agentstream.TurnRef{ID: "other-turn"}, Body: agentstream.UsageObserved{Usage: finalUsage}})
			c.emit(agentstream.Event{Body: agentstream.UsageObserved{Usage: live}})
			c.emit(agentstream.Event{Body: agentstream.UsageObserved{Usage: live}})
			c.emit(agentstream.Event{Body: agentstream.ResultObserved{Text: "done", Usage: finalUsage}})
			c.emit(turnDone("end_turn"))
		}
	}
	durable := NewSQLStore(db)
	r, err := NewRunner(Options{AmuxDir: t.TempDir(), Store: durable, Events: durable, Control: ctrl, DefaultTimeout: 2 * time.Second, NativeResult: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	run, err := r.Submit(Spec{Agent: "codex", Task: "inspect"}, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, r, run.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("status = %s: %s", final.Status, final.Error)
	}
	if final.Result == nil || !bytes.Equal(final.Result.UsageJson, finalUsage) {
		t.Fatalf("final usage lost: %+v", final.Result)
	}
	stream, unsub, err := r.Subscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	var got []TaskEvent
	var terminalSeq uint64
	for event := range stream {
		if event.Kind == TaskEventUsage {
			got = append(got, event)
		}
		if event.Kind == TaskEventResult {
			terminalSeq = event.Seq
		}
	}
	if len(got) != 2 {
		t.Fatalf("usage events = %+v, want two distinct native snapshots", got)
	}
	wants := []map[string]int64{
		{"input_tokens": 100, "total_tokens": 120, "cumulative_input_tokens": 343000, "cumulative_cached_input_tokens": 300000, "cumulative_output_tokens": 12000},
		{"input_tokens": 110, "output_tokens": 20, "total_tokens": 130, "cumulative_input_tokens": 343110, "cumulative_cached_input_tokens": 300100, "cumulative_output_tokens": 12020},
	}
	for i, event := range got {
		if event.TurnID != final.TurnID || event.TaskID != final.ID || event.Seq >= terminalSeq {
			t.Fatalf("usage scope/order: %+v", event)
		}
		want, _ := json.Marshal(wants[i])
		if !bytes.Equal(event.Usage, want) {
			t.Fatalf("usage = %s, want %s", event.Usage, want)
		}
	}
}
