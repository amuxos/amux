package cli

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/task"
)

const (
	consoleOverviewRecentTasks  = 6
	consoleOverviewTaskTextSize = 280
	consoleOverviewTaskWindow   = 24 * time.Hour
)

// consoleOverviewTaskTimeout bounds the task index lookup so a slow task
// service cannot hold back the authoritative Session snapshot. It is a variable
// only so tests can shorten it.
var consoleOverviewTaskTimeout = 5 * time.Second

// consoleOverview adds bounded task and bot summaries to the session snapshot.
// Sessions remain authoritative: an unavailable task index is reported as such
// and an unavailable or unconfigured bot bridge is simply omitted.
func (b *consoleBackend) consoleOverview(ctx context.Context) (map[string]any, error) {
	var (
		wg       sync.WaitGroup
		tasks    map[string]any
		tasksErr error
		bots     []any
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		tasks, tasksErr = b.consoleTaskSummary(ctx)
	}()
	go func() {
		defer wg.Done()
		bots = b.consoleBotSummary(ctx)
	}()
	value, err := b.consoleSessions(ctx, "overview", nil)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	if tasksErr != nil {
		value["unavailable"] = []string{"tasks"}
	} else {
		for key, item := range tasks {
			value[key] = item
		}
	}
	if bots != nil {
		value["bots"] = bots
	}
	return value, nil
}

func (b *consoleBackend) consoleTaskSummary(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, consoleOverviewTaskTimeout)
	defer cancel()
	runs, err := b.consoleTaskRuns(ctx)
	if err != nil {
		return nil, err
	}
	since := time.Now().Add(-consoleOverviewTaskWindow)
	counts := map[string]int{"active": 0, "succeeded_24h": 0, "failed_24h": 0}
	for _, run := range runs {
		switch {
		case !run.Status.IsTerminal():
			counts["active"]++
		case run.FinishedAt.Before(since):
		case run.Status == task.StatusSucceeded:
			counts["succeeded_24h"]++
		case run.Status == task.StatusFailed || run.Status == task.StatusTimedOut:
			counts["failed_24h"]++
		}
	}
	recent := make([]map[string]any, 0, consoleOverviewRecentTasks)
	for _, run := range runs {
		if len(recent) == consoleOverviewRecentTasks {
			break
		}
		view := consoleTaskView(run)
		view["task"] = consoleTruncateRunes(run.Task, consoleOverviewTaskTextSize)
		if summary, ok := view["summary"].(string); ok {
			view["summary"] = consoleTruncateRunes(summary, consoleOverviewTaskTextSize)
		}
		// The overview links to task detail; full result payloads stay there.
		delete(view, "result")
		recent = append(recent, view)
	}
	return map[string]any{"recent_tasks": recent, "task_counts": counts}, nil
}

// consoleBotSummary returns the live bot list, or nil when no bridge answers.
func (b *consoleBackend) consoleBotSummary(ctx context.Context) []any {
	if b.hub == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := b.bridgeManagement(ctx, "bots", "list", json.RawMessage(`{}`))
	if err != nil {
		return nil
	}
	data, ok := raw.(json.RawMessage)
	if !ok {
		return nil
	}
	var response struct {
		Bots []any `json:"bots"`
	}
	if json.Unmarshal(data, &response) != nil || response.Bots == nil {
		return nil
	}
	return response.Bots
}

func consoleTruncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
