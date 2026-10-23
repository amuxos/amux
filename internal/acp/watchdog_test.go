package acp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
)

func TestWatchdogPolicyNormalization(t *testing.T) {
	zero := newWatchdogPolicy(&config.StructuredProviderConfig{}, time.Second)
	if zero != (watchdogPolicy{}) {
		t.Fatalf("explicit zero=%+v", zero)
	}
	defaults := newWatchdogPolicy(nil, time.Second)
	negative := newWatchdogPolicy(&config.StructuredProviderConfig{-1, -1, -1, -1}, time.Second)
	if defaults != negative {
		t.Fatalf("default=%+v negative=%+v", defaults, negative)
	}
	clamped := newWatchdogPolicy(&config.StructuredProviderConfig{int(^uint(0) >> 1), int(^uint(0) >> 1), 1, 5000}, time.Second)
	if clamped.idle != 7*24*time.Hour || clamped.hardCap != 7*24*time.Hour || clamped.repeatLimit != 1000 {
		t.Fatalf("clamped=%+v", clamped)
	}
}

func TestWatchdogPolicyActivityToolsAndPermissions(t *testing.T) {
	start := time.Now()
	w := newTurnWatchdog(1, watchdogPolicy{idle: time.Second, hardCap: 10 * time.Second}, start)
	w.activity = start.Add(time.Second)
	if reason := w.violation(start.Add(1500 * time.Millisecond)); reason != "" {
		t.Fatal(reason)
	}
	w.observeTool(toolUpdate{Update: "tool_call", ID: "tool", Status: "in_progress"}, start)
	if reason := w.violation(start.Add(2 * time.Second)); reason != "" {
		t.Fatal("unverified tool progress caused idle timeout", reason)
	}
	if reason := w.violation(start.Add(10 * time.Second)); reason != "turn_hard_cap" {
		t.Fatalf("hard cap=%q", reason)
	}
	w.observeTool(toolUpdate{Update: "tool_call_update", ID: "tool", Status: "completed"}, start.Add(3*time.Second))
	w.permissions["permission"] = true
	if reason := w.violation(start.Add(5 * time.Second)); reason != "" {
		t.Fatal("permission wait caused idle timeout", reason)
	}
	delete(w.permissions, "permission")
	w.activity = start.Add(5 * time.Second)
	if reason := w.violation(start.Add(6 * time.Second)); reason != "turn_idle_timeout" {
		t.Fatalf("idle=%q", reason)
	}
}

func TestWatchdogRepeatedCallsUseNativeInputsAndDistinctIDs(t *testing.T) {
	now := time.Now()
	w := newTurnWatchdog(1, watchdogPolicy{repeatLimit: 2}, now)
	first := toolUpdate{Update: "tool_call", Status: "completed", ID: "a", Kind: "execute", Title: "Run", RawInput: json.RawMessage(`{"b":2,"a":1}`)}
	w.observeTool(first, now)
	w.observeTool(first, now)
	if w.repeated != 1 {
		t.Fatalf("duplicate native ID counted twice: %d", w.repeated)
	}
	second := first
	second.ID = "b"
	second.RawInput = json.RawMessage(`{"a":1,"b":2}`)
	w.observeTool(second, now)
	if reason := w.violation(now); reason != "repeat_tool_loop" {
		t.Fatalf("repeat=%q", reason)
	}
	third := second
	third.ID = "c"
	third.RawInput = nil
	w.observeTool(third, now)
	if reason := w.violation(now); reason != "" {
		t.Fatalf("missing native input inferred as repeat: %q", reason)
	}
}

func TestWatchdogPendingInputsDoNotCreateFalseRepeat(t *testing.T) {
	now := time.Now()
	w := newTurnWatchdog(1, watchdogPolicy{repeatLimit: 2}, now)
	for _, id := range []string{"a", "b"} {
		w.observeTool(toolUpdate{Update: "tool_call", ID: id, Title: "Run", Kind: "execute", Status: "pending", RawInput: json.RawMessage(`{}`)}, now)
	}
	if reason := w.violation(now); reason != "" {
		t.Fatalf("pending placeholders counted as repeated calls: %q", reason)
	}
	w.observeTool(toolUpdate{Update: "tool_call_update", ID: "a", Status: "completed", RawInput: json.RawMessage(`{"command":"first"}`)}, now)
	w.observeTool(toolUpdate{Update: "tool_call_update", ID: "b", Status: "completed", RawInput: json.RawMessage(`{"command":"second"}`)}, now)
	if reason := w.violation(now); reason != "" {
		t.Fatalf("different completed inputs counted as repeated calls: %q", reason)
	}
	w.observeTool(toolUpdate{Update: "tool_call", ID: "c", Title: "Run", Kind: "execute", Status: "pending", RawInput: json.RawMessage(`{}`)}, now)
	w.observeTool(toolUpdate{Update: "tool_call_update", ID: "c", Status: "completed", RawInput: json.RawMessage(`{"command":"second"}`)}, now)
	if reason := w.violation(now); reason != "repeat_tool_loop" {
		t.Fatalf("identical final inputs not counted: %q", reason)
	}
}

func TestWatchdogToolFingerprintPreservesLargeIntegerInputs(t *testing.T) {
	first := toolUpdate{Kind: "execute", Title: "Lookup", RawInput: json.RawMessage(`{"id":9007199254740992}`)}
	second := first
	second.RawInput = json.RawMessage(`{"id":9007199254740993}`)
	if toolCallFingerprint(first) == toolCallFingerprint(second) {
		t.Fatal("different large integer inputs collapsed")
	}
}

func TestWatchdogNullToolInputRetainsAccumulatedArguments(t *testing.T) {
	now := time.Now()
	w := newTurnWatchdog(1, watchdogPolicy{repeatLimit: 2}, now)
	for _, id := range []string{"a", "b"} {
		w.observeTool(toolUpdate{Update: "tool_call", ID: id, Title: "Run", Kind: "execute", Status: "in_progress", RawInput: json.RawMessage(`{"command":"same"}`)}, now)
		w.observeTool(toolUpdate{Update: "tool_call_update", ID: id, Status: "completed", RawInput: json.RawMessage(`null`)}, now)
	}
	if reason := w.violation(now); reason != "repeat_tool_loop" {
		t.Fatalf("null replaced accumulated tool input: %q", reason)
	}
}
