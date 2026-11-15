package cli

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/gateway"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// The console links records the Hub keeps separately: a Task to the Schedule
// that started it (from each Schedule's retained run history), and a persistent
// instance to its owner Session and the Tasks it ran. Links are best effort: a
// source that cannot be read leaves the records unlinked rather than failing.
const (
	consoleScheduleIndexLimit = 200
	consoleInstanceTaskLimit  = 20
	consolePurposeRunes       = 160
)

type consoleScheduleRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

type consoleScheduleIndex struct {
	byTask    map[string]consoleScheduleRef
	schedules []map[string]any
	complete  bool
}

func consoleScheduleTitle(item *pb.ScheduleItem) string {
	if name := strings.TrimSpace(item.GetName()); name != "" {
		return name
	}
	return "#" + strconv.FormatInt(item.GetId(), 10)
}

func (b *consoleBackend) consoleScheduleIndex(ctx context.Context) consoleScheduleIndex {
	index := consoleScheduleIndex{byTask: map[string]consoleScheduleRef{}, schedules: []map[string]any{}}
	if b.hub == nil {
		return index
	}
	items, err := clientop.ScheduleList(ctx, b.hub)
	if err != nil {
		return index
	}
	index.complete = len(items) <= consoleScheduleIndexLimit
	counts := map[int64]int{}
	for i, item := range items {
		if item == nil {
			continue
		}
		ref := consoleScheduleRef{ID: item.GetId(), Name: consoleScheduleTitle(item), Kind: item.GetKind()}
		if i < consoleScheduleIndexLimit {
			detail, err := clientop.ScheduleGet(ctx, b.hub, clientop.ScheduleIDRequest{ID: item.GetId(), IncludeRuns: true})
			if err != nil {
				index.complete = false
			} else {
				for _, run := range detail.GetRuns() {
					if id := strings.TrimSpace(run.GetTaskId()); id != "" {
						index.byTask[id] = ref
						counts[ref.ID]++
					}
				}
			}
		}
		index.schedules = append(index.schedules, map[string]any{"id": ref.ID, "name": ref.Name, "kind": ref.Kind, "enabled": item.GetEnabled()})
	}
	for _, schedule := range index.schedules {
		schedule["task_count"] = counts[schedule["id"].(int64)]
	}
	return index
}

// consoleTaskSource says what started a Task: a Schedule, a persistent instance,
// an Agent Session (a subtask), or a direct operator/CLI request.
func consoleAnnotateTask(view map[string]any, run *task.TaskRun, index consoleScheduleIndex) {
	if ref, ok := index.byTask[run.ID]; ok {
		view["source"], view["schedule"] = "schedule", ref
		view["schedule_id"], view["schedule_name"] = ref.ID, ref.Name
		return
	}
	switch {
	case run.AgentInstanceID != "":
		view["source"] = "instance"
	case run.ParentSessionID != "":
		view["source"] = "session"
	default:
		view["source"] = "direct"
	}
}

// consolePurpose is a one-line excerpt of the task text that created an
// instance: what the instance was started to do.
func consolePurpose(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= consolePurposeRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:consolePurposeRunes]) + "…"
}

func consoleSessionOwner(entry *gateway.SessionEntry) map[string]any {
	owner := map[string]any{"session_id": entry.GetId(), "name": entry.GetName(), "target_id": entry.TargetID, "target_name": entry.TargetName, "state": entry.GetState()}
	if session := entry.Session; session != nil {
		owner["agent_profile"] = session.GetAgentProfile()
		owner["workspace_source"] = session.GetWorkspaceSource()
	}
	return owner
}

// consoleInstanceLinks adds ownership and purpose to instance views: the owner
// Session, the first task (purpose), the latest task and the number of tasks.
func (b *consoleBackend) consoleInstanceLinks(ctx context.Context, views []map[string]any, withTasks bool) {
	if len(views) == 0 {
		return
	}
	sessions := map[string]*gateway.SessionEntry{}
	if entries, err := b.consoleSessionEntries(ctx); err == nil {
		for _, entry := range entries {
			if entry.GetId() != "" {
				sessions[entry.GetId()] = entry
			}
		}
	}
	runs, err := b.consoleTaskRuns(ctx)
	if err != nil {
		runs = nil
	}
	byInstance := map[string][]*task.TaskRun{}
	for _, run := range runs {
		if run.AgentInstanceID != "" {
			byInstance[run.AgentInstanceID] = append(byInstance[run.AgentInstanceID], run)
		}
	}
	var index consoleScheduleIndex
	if withTasks {
		index = b.consoleScheduleIndex(ctx)
	}
	for _, view := range views {
		id, _ := view["agent_instance_id"].(string)
		if ownerID, _ := view["owner_session_id"].(string); ownerID != "" {
			if entry := sessions[ownerID]; entry != nil {
				view["owner"] = consoleSessionOwner(entry)
			} else {
				view["owner"] = map[string]any{"session_id": ownerID, "missing": true}
			}
		}
		instanceRuns := byInstance[id]
		// Runs are newest first; the oldest one created the instance.
		sort.SliceStable(instanceRuns, func(i, j int) bool { return instanceRuns[i].RequestedAt.After(instanceRuns[j].RequestedAt) })
		view["task_count"] = len(instanceRuns)
		if runs == nil {
			view["tasks_unavailable"] = true
		}
		if len(instanceRuns) == 0 {
			continue
		}
		first, latest := instanceRuns[len(instanceRuns)-1], instanceRuns[0]
		view["purpose"] = consolePurpose(first.Task)
		view["latest_task"] = map[string]any{"task_id": latest.ID, "status": latest.Status, "requested_at": latest.RequestedAt, "task": consolePurpose(latest.Task)}
		if withTasks {
			tasks := make([]map[string]any, 0, consoleInstanceTaskLimit)
			for _, run := range instanceRuns {
				if len(tasks) == consoleInstanceTaskLimit {
					break
				}
				task := consoleTaskView(run)
				consoleAnnotateTask(task, run, index)
				tasks = append(tasks, task)
			}
			view["tasks"] = tasks
		}
	}
}
