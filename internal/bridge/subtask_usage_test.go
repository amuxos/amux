package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentui"
)

func TestSubtaskCardUsageRemainsIndependentAndSurvivesFinalization(t *testing.T) {
	now := time.Now()
	live := json.RawMessage(`{"total_tokens":120,"cumulative_input_tokens":343000,"cumulative_cached_input_tokens":300000,"cumulative_output_tokens":12000}`)
	final := json.RawMessage(`{"total_tokens":130,"cumulative_input_tokens":343100,"cumulative_cached_input_tokens":300000,"cumulative_output_tokens":12020}`)
	a := newSubtaskCardState(subtaskProgress{ID: "task-a", Status: "running", StartedAt: now})
	b := newSubtaskCardState(subtaskProgress{ID: "task-b", Status: "running", StartedAt: now})
	a.applyEvent(subtaskEvent{TaskID: "task-a", Seq: 1, Kind: "usage", Usage: live})
	a.applyEvent(subtaskEvent{TaskID: "task-b", Seq: 99, Kind: "usage", Usage: final})
	a.applyEvent(subtaskEvent{TaskID: "task-a", Seq: 1, Kind: "usage", Usage: final})
	a.applyEvent(subtaskEvent{TaskID: "task-a", Seq: 2, Kind: "tool", Tool: &agentui.ToolEvent{ID: "tool-1", Name: "shell", Status: "running"}})
	if !bytes.Equal(a.snapshot(now).Progress.UsageJSON, live) || len(b.snapshot(now).Progress.UsageJSON) != 0 {
		t.Fatal("tool/duplicate/foreign events changed the wrong usage")
	}
	assertUsage := func(state *subtaskCardState, want string) {
		t.Helper()
		snapshot := state.snapshot(now)
		for _, card := range []string{renderSubtaskActivityCard(snapshot, "", now), string(snapshot.activity("", now).JSON)} {
			if !strings.Contains(card, want) {
				t.Fatalf("card missing %q: %s", want, card)
			}
		}
	}
	assertUsage(a, "120 tokens · 累计输入 300k/343k（缓存/总量） · 累计输出 12k")
	b.applyEvent(subtaskEvent{TaskID: "task-b", Seq: 1, Kind: "usage", Usage: []byte(`{"cumulative_input_tokens":80,"cumulative_cached_input_tokens":0,"cumulative_output_tokens":10}`)})
	a.applyEvent(subtaskEvent{TaskID: "task-a", Seq: 3, Kind: "result", Status: "succeeded", Result: &subtaskEventResult{Summary: "done", UsageJSON: final}})
	a.updateProgress(subtaskProgress{ID: "task-a", Status: "succeeded", Summary: "done"})
	if !bytes.Equal(a.snapshot(now).Progress.UsageJSON, final) {
		t.Fatal("sparse final snapshot erased result usage")
	}
	assertUsage(a, "130 tokens · 累计输入 300k/343.1k（缓存/总量） · 累计输出 12k")
	assertUsage(b, "累计输入 0/80（缓存/总量） · 累计输出 10")
}

// A long-running child accumulates past k; the child card scales magnitude
// units the same way the main card does.
func TestSubtaskCardUsageAdaptsMagnitudeUnit(t *testing.T) {
	now := time.Now()
	state := newSubtaskCardState(subtaskProgress{ID: "task-a", Status: "running", StartedAt: now})
	state.applyEvent(subtaskEvent{TaskID: "task-a", Seq: 1, Kind: "usage", Usage: []byte(`{"cumulative_input_tokens":128400000,"cumulative_cached_input_tokens":300000,"cumulative_output_tokens":10500000}`)})
	want := "累计输入 300k/128.4M（缓存/总量） · 累计输出 10.5M"
	snapshot := state.snapshot(now)
	for _, card := range []string{renderSubtaskActivityCard(snapshot, "", now), string(snapshot.activity("", now).JSON)} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q: %s", want, card)
		}
	}
}

func TestReadSubtaskProgressRestoresFinalUsage(t *testing.T) {
	usage := []byte(`{"cumulative_input_tokens":343000,"cumulative_cached_input_tokens":300000,"cumulative_output_tokens":12000}`)
	raw, err := json.Marshal(map[string]any{
		"id": "task-1", "status": "succeeded", "result": map[string]any{"summary": "done", "usage_json": usage},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := &Bridge{subtasks: &streamingSubtaskReader{currentJSON: string(raw)}}
	progress, ok := b.readSubtaskProgress(context.Background(), "task-1", time.Now())
	if !ok || !bytes.Equal(progress.UsageJSON, usage) {
		t.Fatalf("final snapshot usage lost: %+v", progress)
	}
	card := renderSubtaskActivityCard(newSubtaskCardState(progress).snapshot(time.Now()), "", time.Now())
	if !strings.Contains(card, "累计输入 300k/343k（缓存/总量） · 累计输出 12k") {
		t.Fatalf("restored title: %s", card)
	}
}
