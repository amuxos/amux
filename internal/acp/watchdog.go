package acp

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/amuxos/amux/internal/config"
)

const maxACPWatchdogSeconds = 7 * 24 * 3600

type watchdogPolicy struct {
	idle        time.Duration
	hardCap     time.Duration
	repeatLimit int
}

func newWatchdogPolicy(settings *config.StructuredProviderConfig, unit time.Duration) watchdogPolicy {
	defaults := config.DefaultStructuredProviderTimeouts()
	selected := defaults
	if settings != nil {
		selected = *settings
	}
	if unit <= 0 {
		unit = time.Second
	}
	duration := func(value, fallback int) time.Duration {
		if value < 0 {
			value = fallback
		}
		if value > maxACPWatchdogSeconds {
			value = maxACPWatchdogSeconds
		}
		return time.Duration(value) * unit
	}
	repeat := selected.RepeatToolCallLimit
	if repeat < 0 {
		repeat = defaults.RepeatToolCallLimit
	}
	if repeat > 1000 {
		repeat = 1000
	}
	return watchdogPolicy{
		idle:        duration(selected.TurnIdleTimeoutSeconds, defaults.TurnIdleTimeoutSeconds),
		hardCap:     duration(selected.TurnHardCapSeconds, defaults.TurnHardCapSeconds),
		repeatLimit: repeat,
	}
}

type turnWatchdog struct {
	generation  uint64
	policy      watchdogPolicy
	started     time.Time
	activity    time.Time
	tools       map[string]bool
	permissions map[string]bool
	seenTools   map[string]bool
	toolDetails map[string]toolUpdate
	lastTool    string
	repeated    int
}

func newTurnWatchdog(generation uint64, policy watchdogPolicy, now time.Time) *turnWatchdog {
	return &turnWatchdog{generation: generation, policy: policy, started: now, activity: now, tools: map[string]bool{}, permissions: map[string]bool{}, seenTools: map[string]bool{}, toolDetails: map[string]toolUpdate{}}
}

func (w *turnWatchdog) observeTool(update toolUpdate, now time.Time) {
	w.activity = now
	switch update.Status {
	case "completed", "failed":
		delete(w.tools, update.ID)
	case "pending", "in_progress":
		w.tools[update.ID] = true
	default:
		if update.Update == "tool_call" {
			w.tools[update.ID] = true
		}
	}

	details := w.toolDetails[update.ID]
	if update.Kind != "" {
		details.Kind = update.Kind
	}
	if update.Title != "" {
		details.Title = update.Title
	}
	if len(update.RawInput) > 0 && string(bytes.TrimSpace(update.RawInput)) != "null" {
		details.RawInput = append(json.RawMessage(nil), update.RawInput...)
	}
	w.toolDetails[update.ID] = details
	// Initial pending calls may carry placeholder inputs. Count a distinct call
	// only once its final update confirms the accumulated call-defining input.
	if (update.Status != "completed" && update.Status != "failed") || w.seenTools[update.ID] {
		return
	}
	w.seenTools[update.ID] = true

	fingerprint := toolCallFingerprint(details)
	if fingerprint == "" || fingerprint != w.lastTool {
		w.lastTool = fingerprint
		w.repeated = 1
		return
	}
	w.repeated++
}

// Only explicit call-defining input is comparable. Missing inputs and updates
// are never guessed to be repeated calls; tool call IDs are only deduplicators.
func toolCallFingerprint(update toolUpdate) string {
	if len(update.RawInput) == 0 || (update.Kind == "" && update.Title == "") {
		return ""
	}
	var input any
	decoder := json.NewDecoder(bytes.NewReader(update.RawInput))
	decoder.UseNumber()
	if decoder.Decode(&input) != nil || input == nil {
		return ""
	}
	data, err := json.Marshal([]any{update.Kind, update.Title, input})
	if err != nil {
		return ""
	}
	return string(data)
}

func (w *turnWatchdog) violation(now time.Time) string {
	if w.policy.repeatLimit > 0 && w.lastTool != "" && w.repeated >= w.policy.repeatLimit {
		return "repeat_tool_loop"
	}
	if w.policy.hardCap > 0 && now.Sub(w.started) >= w.policy.hardCap {
		return "turn_hard_cap"
	}
	// ACP tools and permission requests have no verified progress heartbeat.
	// The hard cap remains authoritative while either is active.
	if len(w.tools) == 0 && len(w.permissions) == 0 && w.policy.idle > 0 && now.Sub(w.activity) >= w.policy.idle {
		return "turn_idle_timeout"
	}
	return ""
}

func (w *turnWatchdog) nextCheck(now time.Time) time.Duration {
	delay := time.Hour
	if w.policy.hardCap > 0 {
		delay = w.started.Add(w.policy.hardCap).Sub(now)
	}
	if len(w.tools) == 0 && len(w.permissions) == 0 && w.policy.idle > 0 {
		idle := w.activity.Add(w.policy.idle).Sub(now)
		if idle < delay {
			delay = idle
		}
	}
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	return delay
}
