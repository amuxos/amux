package agentui

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ActionKind string

const (
	ActionTurnStarted                ActionKind = "turn_started"
	ActionReplyAppend                ActionKind = "reply_append"
	ActionReplyReplace               ActionKind = "reply_replace"
	ActionReplyReconcileFinalMessage ActionKind = "reply_reconcile_final_message"
	ActionReplyMessageBoundary       ActionKind = "reply_message_boundary"
	ActionReplySegment               ActionKind = "reply_segment"
	ActionActivityChanged            ActionKind = "activity_changed"
	ActionPermissionRequest          ActionKind = "permission_request"
	ActionPermissionResolved         ActionKind = "permission_resolved"
	ActionInputEcho                  ActionKind = "input_echo"
	ActionTurnFinished               ActionKind = "turn_finished"
	ActionError                      ActionKind = "error"
)

const longRunningActivityDelay = 10 * time.Second

type Action struct {
	Kind       ActionKind
	Turn       TurnEvent
	Source     Source
	Text       string
	Activity   *ActivityView
	Permission *PermissionEvent
	Error      *ErrorEvent
	Immediate  bool
}

type ActivityView struct {
	TurnID         string
	TurnSeq        int64
	Status         string
	CurrentStep    string
	Plan           []PlanEntry
	Tools          []ToolEvent
	UsageJSON      json.RawMessage
	StartedAt      time.Time
	LastActivityAt time.Time
	FinishedAt     time.Time
}

type Presenter struct {
	turn           TurnEvent
	activity       bool
	status         string
	finished       bool
	thinking       strings.Builder
	currentStep    string
	plan           []PlanEntry
	tools          map[string]*ToolEvent
	toolOrder      map[string]int
	nextToolOrder  int
	legacyToolSeq  int
	usage          json.RawMessage
	startedAt      time.Time
	lastActivityAt time.Time
	finishedAt     time.Time
	nowFn          func() time.Time
}

func NewPresenter() *Presenter {
	return NewPresenterWithClock(time.Now)
}

func NewPresenterWithClock(nowFn func() time.Time) *Presenter {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Presenter{nowFn: nowFn}
}

func (p *Presenter) Consume(ev Event) []Action {
	if olderTurn(p.turn, ev.Turn) {
		return nil
	}
	switch ev.Kind {
	case KindInputSubmitted:
		if sameTurn(p.turn, ev.Turn) && p.status == "running" {
			p.adoptTurn(ev.Turn)
			if ev.Source != SourceFeishu && ev.Source != SourceGoal {
				return []Action{{Kind: ActionInputEcho, Turn: p.turn, Text: ev.Text}}
			}
			return nil
		}
		p.startTurn(ev.Turn)
		actions := []Action{{Kind: ActionTurnStarted, Turn: p.turn, Source: ev.Source, Text: ev.Text}}
		if ev.Source != SourceFeishu && ev.Source != SourceGoal {
			actions = append(actions, Action{Kind: ActionInputEcho, Turn: p.turn, Text: ev.Text})
		}
		return actions
	case KindTurnStarted:
		if sameTurn(p.turn, ev.Turn) && p.status == "running" {
			p.adoptTurn(ev.Turn)
			return nil
		}
		p.startTurn(ev.Turn)
		return []Action{{Kind: ActionTurnStarted, Turn: p.turn}}
	default:
		if ev.Turn != nil && (p.status == "" || !sameTurn(p.turn, ev.Turn)) {
			p.startTurn(ev.Turn)
		} else {
			p.adoptTurn(ev.Turn)
		}
	}

	switch ev.Kind {
	case KindAssistantTextDelta:
		if ev.Text == "" {
			return nil
		}
		p.markActivity()
		return []Action{{Kind: ActionReplyAppend, Turn: p.turn, Source: ev.Source, Text: ev.Text}}
	case KindAssistantText:
		p.markActivity()
		actions := []Action{{Kind: ActionReplyMessageBoundary, Turn: p.turn}}
		if ev.Text != "" {
			actions = append(actions, Action{Kind: ActionReplyAppend, Turn: p.turn, Source: ev.Source, Text: ev.Text})
		}
		return actions
	case KindResult:
		p.markActivity()
		if len(ev.UsageJSON) > 0 {
			p.usage = cloneBytes(ev.UsageJSON)
		}
		if strings.TrimSpace(ev.Text) == "" {
			return nil
		}
		return []Action{{Kind: ActionReplyReconcileFinalMessage, Turn: p.turn, Text: ev.Text}}
	case KindThinkingDelta:
		if !p.updateThinking(ev.Text) {
			return nil
		}
		p.markActivity()
		p.activity = true
		return p.activityAction(false)
	case KindPlan:
		if ev.Plan == nil {
			return nil
		}
		p.markActivity()
		p.activity = true
		p.plan = append(p.plan[:0], ev.Plan.Entries...)
		return p.activityAction(false)
	case KindToolStarted, KindToolUpdated, KindToolCompleted, KindToolFailed:
		if ev.Tool == nil {
			return nil
		}
		p.markActivity()
		p.activity = true
		p.updateTool(ev.Kind, ev.Tool)
		return p.activityAction(ev.Kind != KindToolUpdated)
	case KindUsage:
		if len(ev.UsageJSON) > 0 {
			p.markActivity()
		}
		p.usage = cloneBytes(ev.UsageJSON)
		if len(ev.UsageJSON) > 0 {
			p.activity = true
		}
		return nil
	case KindHeartbeat:
		if p.status == "running" && !p.activity && !p.startedAt.IsZero() && p.nowFn().Sub(p.startedAt) >= longRunningActivityDelay {
			p.activity = true
			return p.activityAction(true)
		}
		return nil
	case KindPermissionRequested:
		p.markActivity()
		return []Action{{Kind: ActionPermissionRequest, Turn: p.turn, Permission: ev.Permission}}
	case KindPermissionResolved:
		return []Action{{Kind: ActionPermissionResolved, Turn: p.turn, Permission: ev.Permission}}
	case KindError:
		p.status = "failed"
		actions := []Action{{Kind: ActionError, Turn: p.turn, Text: ev.Text, Error: ev.Error}}
		if p.activity {
			actions = append(p.activityAction(true), actions...)
		}
		return actions
	case KindInterrupted, KindTurnDone:
		if p.finished {
			return nil
		}
		p.finished = true
		p.finishedAt = p.nowFn()
		if ev.Kind == KindInterrupted {
			p.status = "stopped"
		} else if ev.Turn != nil && ev.Turn.State == "failed" {
			p.status = "failed"
		} else if p.status != "failed" {
			p.status = "done"
		}
		actions := make([]Action, 0, 2)
		if p.activity {
			actions = append(actions, p.activityAction(true)...)
		}
		actions = append(actions, Action{Kind: ActionTurnFinished, Turn: p.turn})
		return actions
	default:
		return nil
	}
}

// EnsureActivity lets an amux surface add non-provider activity, such as hub
// subtasks, while preserving the presenter's turn identity and timing.
func (p *Presenter) EnsureActivity() *ActivityView {
	if p == nil || p.status == "" {
		return nil
	}
	p.activity = true
	return p.activityView()
}

func olderTurn(current TurnEvent, incoming *TurnEvent) bool {
	return incoming != nil && current.Seq > 0 && incoming.Seq > 0 && incoming.Seq < current.Seq
}

func sameTurn(current TurnEvent, incoming *TurnEvent) bool {
	if incoming == nil {
		return false
	}
	if incoming.ID != "" && current.ID != "" {
		return incoming.ID == current.ID
	}
	return incoming.Seq != 0 && incoming.Seq == current.Seq
}

func (p *Presenter) startTurn(turn *TurnEvent) {
	p.reset()
	if turn != nil {
		p.turn = *turn
	}
	p.status = "running"
	p.startedAt = p.nowFn()
	p.lastActivityAt = p.startedAt
}

func (p *Presenter) adoptTurn(turn *TurnEvent) {
	if turn == nil {
		return
	}
	if turn.ID != "" {
		p.turn.ID = turn.ID
	}
	if turn.Seq != 0 {
		p.turn.Seq = turn.Seq
	}
	if turn.State != "" {
		p.turn.State = turn.State
	}
	if turn.StopReason != "" {
		p.turn.StopReason = turn.StopReason
	}
}

func (p *Presenter) reset() {
	p.turn = TurnEvent{}
	p.activity = false
	p.status = ""
	p.finished = false
	p.thinking.Reset()
	p.currentStep = ""
	p.plan = nil
	p.tools = nil
	p.toolOrder = nil
	p.nextToolOrder = 0
	p.legacyToolSeq = 0
	p.usage = nil
	p.startedAt = time.Time{}
	p.lastActivityAt = time.Time{}
	p.finishedAt = time.Time{}
}

func (p *Presenter) markActivity() {
	if p != nil {
		p.lastActivityAt = p.nowFn()
	}
}

func (p *Presenter) updateThinking(delta string) bool {
	if strings.TrimSpace(delta) == "" {
		return false
	}
	p.thinking.WriteString(delta)
	text := p.thinking.String()
	if runes := []rune(text); len(runes) > 1024 {
		text = string(runes[len(runes)-1024:])
		p.thinking.Reset()
		p.thinking.WriteString(text)
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if step := strings.TrimSpace(lines[i]); step != "" {
			runes := []rune(step)
			if len(runes) > 80 {
				step = string(runes[len(runes)-80:]) + "..."
			}
			p.currentStep = step
			return true
		}
	}
	return false
}

func (p *Presenter) updateTool(kind EventKind, incoming *ToolEvent) {
	if p.tools == nil {
		p.tools = make(map[string]*ToolEvent)
		p.toolOrder = make(map[string]int)
	}
	key := strings.TrimSpace(incoming.ID)
	if key == "" && kind != KindToolStarted {
		key = p.latestLegacyToolKey(incoming)
	}
	if key == "" {
		p.legacyToolSeq++
		key = "legacy:" + incoming.Name + ":" + incoming.Title + ":" + strconv.Itoa(p.legacyToolSeq)
	}
	current := p.tools[key]
	if current == nil {
		current = &ToolEvent{ID: incoming.ID, StartedAt: p.nowFn()}
		p.tools[key] = current
		p.toolOrder[key] = p.nextToolOrder
		p.nextToolOrder++
	}
	mergeToolEvent(current, incoming)
	switch kind {
	case KindToolStarted, KindToolUpdated:
		if current.Status == "" {
			current.Status = "running"
		}
	case KindToolCompleted:
		current.Status = "completed"
	case KindToolFailed:
		current.Status = "failed"
	}
}

func (p *Presenter) latestLegacyToolKey(incoming *ToolEvent) string {
	bestKey := ""
	bestOrder := -1
	for key, tool := range p.tools {
		if tool.ID != "" || tool.Status == "completed" || tool.Status == "failed" {
			continue
		}
		if incoming.Name != "" && tool.Name != incoming.Name {
			continue
		}
		if incoming.Title != "" && tool.Title != incoming.Title {
			continue
		}
		if p.toolOrder[key] > bestOrder {
			bestKey, bestOrder = key, p.toolOrder[key]
		}
	}
	return bestKey
}

func mergeToolEvent(dst, src *ToolEvent) {
	if src.ID != "" {
		dst.ID = src.ID
	}
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Title != "" {
		dst.Title = src.Title
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if len(src.InputJSON) > 0 {
		dst.InputJSON = cloneBytes(src.InputJSON)
	}
	if src.OutputPreview != "" {
		dst.OutputPreview = src.OutputPreview
	}
	if src.Error != "" {
		dst.Error = src.Error
	}
	if len(src.ContentJSON) > 0 {
		dst.ContentJSON = cloneBytes(src.ContentJSON)
	}
	if len(src.LocationsJSON) > 0 {
		dst.LocationsJSON = cloneBytes(src.LocationsJSON)
	}
	if len(src.RawOutputJSON) > 0 {
		dst.RawOutputJSON = cloneBytes(src.RawOutputJSON)
	}
	if !src.StartedAt.IsZero() {
		dst.StartedAt = src.StartedAt
	}
	dst.Legacy = dst.Legacy || src.Legacy
}

func (p *Presenter) activityAction(immediate bool) []Action {
	return []Action{{Kind: ActionActivityChanged, Turn: p.turn, Activity: p.activityView(), Immediate: immediate}}
}

func (p *Presenter) activityView() *ActivityView {
	view := &ActivityView{
		TurnID:         p.turn.ID,
		TurnSeq:        p.turn.Seq,
		Status:         p.status,
		CurrentStep:    p.currentStep,
		Plan:           append([]PlanEntry(nil), p.plan...),
		UsageJSON:      cloneBytes(p.usage),
		StartedAt:      p.startedAt,
		LastActivityAt: p.lastActivityAt,
		FinishedAt:     p.finishedAt,
	}
	keys := make([]string, 0, len(p.tools))
	for key := range p.tools {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return p.toolOrder[keys[i]] < p.toolOrder[keys[j]] })
	for _, key := range keys {
		tool := *p.tools[key]
		tool.InputJSON = cloneBytes(tool.InputJSON)
		tool.ContentJSON = cloneBytes(tool.ContentJSON)
		tool.LocationsJSON = cloneBytes(tool.LocationsJSON)
		tool.RawOutputJSON = cloneBytes(tool.RawOutputJSON)
		view.Tools = append(view.Tools, tool)
	}
	return view
}
