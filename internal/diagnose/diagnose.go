// Package diagnose correlates the identifiers that span bridge messages,
// session turns, distributed tasks, and child sessions.
package diagnose

import (
	"context"
	"sort"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/task"
)

type QueryKind string

const (
	QueryMessageID QueryKind = "message_id"
	QueryTurnID    QueryKind = "turn_id"
	QueryTaskID    QueryKind = "task_id"
	QuerySessionID QueryKind = "session_id"
)

type Query struct {
	Kind  QueryKind `json:"kind"`
	Value string    `json:"value"`
}

type Session struct {
	TargetID    string `json:"target_id"`
	TargetName  string `json:"target_name,omitempty"`
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
	LaunchID    string `json:"launch_id,omitempty"`
	OwnerTaskID string `json:"owner_task_id,omitempty"`
	State       string `json:"state,omitempty"`
}

type Report struct {
	Schema   string            `json:"schema"`
	Query    Query             `json:"query"`
	Links    []bridge.TurnLink `json:"message_turn_links,omitempty"`
	Sessions []Session         `json:"sessions,omitempty"`
	Tasks    []*task.TaskRun   `json:"tasks,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}

// Source keeps storage and transport concerns outside the correlation graph.
type Source interface {
	LoadControlPlane(context.Context) ([]Session, []*task.TaskRun, []string)
	LookupTurnLinks(context.Context, bridge.TurnLinkQuery) ([]bridge.TurnLink, error)
}

func Gather(ctx context.Context, source Source, query Query) Report {
	report := Report{Schema: "amux.system.diagnose.v1", Query: query}
	allSessions, allTasks, warnings := source.LoadControlPlane(ctx)
	report.Warnings = append(report.Warnings, warnings...)

	messageIDs := map[string]bool{}
	turnIDs := map[string]bool{}
	taskIDs := map[string]bool{}
	sessionIDs := map[string]bool{}
	switch query.Kind {
	case QueryMessageID:
		messageIDs[query.Value] = true
	case QueryTurnID:
		turnIDs[query.Value] = true
	case QueryTaskID:
		taskIDs[query.Value] = true
	case QuerySessionID:
		sessionIDs[query.Value] = true
	}

	linkKeys := map[string]bool{}
	queried := map[string]bool{}
	lookup := func(q bridge.TurnLinkQuery) bool {
		key := q.MessageID + "\x00" + q.TurnID + "\x00" + q.SessionID
		if queried[key] {
			return false
		}
		queried[key] = true
		links, err := source.LookupTurnLinks(ctx, q)
		if err != nil {
			warning := "read bridge turn links: " + err.Error()
			if !containsString(report.Warnings, warning) {
				report.Warnings = append(report.Warnings, warning)
			}
			return false
		}
		changed := false
		for _, link := range links {
			linkKey := link.BridgeID + "\x00" + link.MessageID
			if !linkKeys[linkKey] {
				linkKeys[linkKey] = true
				report.Links = append(report.Links, link)
				changed = true
			}
			messageIDs[link.MessageID] = true
			turnIDs[link.TurnID] = true
			sessionIDs[link.SessionID] = true
		}
		return changed
	}
	for messageID := range messageIDs {
		lookup(bridge.TurnLinkQuery{MessageID: messageID})
	}

	selectedTasks := map[string]*task.TaskRun{}
	selectedSessions := map[string]Session{}
	// Every set grows monotonically from finite source snapshots, so iterate to
	// a fixed point and preserve arbitrarily deep task parent chains.
	for {
		changed := false
		for _, run := range allTasks {
			if run == nil {
				continue
			}
			match := taskIDs[run.ID] || taskIDs[run.ParentTaskID] || turnIDs[run.TurnID] || turnIDs[run.ParentTurnID] ||
				sessionIDs[run.ParentSessionID] || sessionIDs[run.ChildSessionId]
			if !match {
				continue
			}
			if selectedTasks[run.ID] == nil {
				selectedTasks[run.ID] = run
				changed = true
			}
			taskIDs[run.ID] = true
			if run.ParentTaskID != "" {
				taskIDs[run.ParentTaskID] = true
			}
			if run.TurnID != "" {
				turnIDs[run.TurnID] = true
			}
			if run.ParentTurnID != "" {
				turnIDs[run.ParentTurnID] = true
			}
			if run.ParentSessionID != "" {
				sessionIDs[run.ParentSessionID] = true
			}
			if run.ChildSessionId != "" {
				sessionIDs[run.ChildSessionId] = true
			}
		}
		for _, session := range allSessions {
			if !sessionIDs[session.SessionID] && !taskIDs[session.OwnerTaskID] {
				continue
			}
			if _, ok := selectedSessions[session.SessionID]; !ok {
				selectedSessions[session.SessionID] = session
				changed = true
			}
			sessionIDs[session.SessionID] = true
			if session.OwnerTaskID != "" {
				taskIDs[session.OwnerTaskID] = true
			}
		}
		for turnID := range turnIDs {
			changed = lookup(bridge.TurnLinkQuery{TurnID: turnID}) || changed
		}
		for sessionID := range sessionIDs {
			changed = lookup(bridge.TurnLinkQuery{SessionID: sessionID}) || changed
		}
		if !changed {
			break
		}
	}
	for _, run := range selectedTasks {
		report.Tasks = append(report.Tasks, run)
	}
	for _, session := range selectedSessions {
		report.Sessions = append(report.Sessions, session)
	}
	sort.Slice(report.Tasks, func(i, j int) bool { return report.Tasks[i].RequestedAt.Before(report.Tasks[j].RequestedAt) })
	sort.Slice(report.Sessions, func(i, j int) bool { return report.Sessions[i].SessionID < report.Sessions[j].SessionID })
	sort.Slice(report.Links, func(i, j int) bool { return report.Links[i].SubmittedAt.Before(report.Links[j].SubmittedAt) })
	sort.Strings(report.Warnings)
	return report
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
