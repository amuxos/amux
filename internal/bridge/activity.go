package bridge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
)

// toolActivityState only owns hub-specific subtask augmentation. Provider
// thinking, plan, tool and usage aggregation belongs to agentui.Presenter.
type toolActivityState struct {
	mu            sync.Mutex
	subtasks      []subtaskProgress
	turnStartedAt time.Time
	nowFn         func() time.Time
}

func (a *toolActivityState) StartTurn(at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.turnStartedAt = at
}

func (a *toolActivityState) TurnStartedAt() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.turnStartedAt
}

func (a *toolActivityState) now() time.Time {
	if a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
}

type subtaskProgress struct {
	ID               string
	AgentType        string
	AgentProfile     string
	AgentInstanceID  string
	EffectiveModel   string
	Status           string
	Title            string
	Summary          string
	UsageJSON        []byte
	Error            string
	TargetID         string
	ChildSessionName string
	ChildSessionID   string
	TimeoutSeconds   int
	RequestedAt      time.Time
	StartedAt        time.Time
	FinishedAt       time.Time
	Elapsed          time.Duration
}

func (a *toolActivityState) SetSubtasks(items []subtaskProgress) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subtasks = append([]subtaskProgress(nil), items...)
}

func (a *toolActivityState) Subtasks() []subtaskProgress {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]subtaskProgress(nil), a.subtasks...)
}

func (a *toolActivityState) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subtasks = nil
	a.turnStartedAt = time.Time{}
}

func activityViewFingerprint(view *agentui.ActivityView) string {
	if view == nil {
		return ""
	}
	type visibleTool struct {
		ID            string
		Name          string
		Title         string
		Status        string
		OutputPreview string
		StartedAt     time.Time
	}
	projection := struct {
		TurnID      string
		TurnSeq     int64
		Status      string
		CurrentStep string
		Plan        []agentui.PlanEntry
		Tools       []visibleTool
		UsageJSON   json.RawMessage
	}{
		TurnID: view.TurnID, TurnSeq: view.TurnSeq, Status: view.Status,
		CurrentStep: view.CurrentStep, Plan: view.Plan, UsageJSON: view.UsageJSON,
	}
	projection.Tools = make([]visibleTool, 0, len(view.Tools))
	for _, tool := range view.Tools {
		projection.Tools = append(projection.Tools, visibleTool{
			ID: tool.ID, Name: tool.Name, Title: tool.Title, Status: tool.Status,
			OutputPreview: tool.OutputPreview, StartedAt: tool.StartedAt,
		})
	}
	data, err := json.Marshal(projection)
	if err != nil {
		data = []byte(fmt.Sprintf("%#v", projection))
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("activity:%x", sum[:16])
}

func activityViewWithCurrentStep(view *agentui.ActivityView, step string) *agentui.ActivityView {
	if view == nil {
		return nil
	}
	clone := *view
	clone.CurrentStep = strings.TrimSpace(step)
	clone.Plan = append([]agentui.PlanEntry(nil), view.Plan...)
	clone.Tools = append([]agentui.ToolEvent(nil), view.Tools...)
	clone.UsageJSON = append(json.RawMessage(nil), view.UsageJSON...)
	return &clone
}

func formatElapsed(d time.Duration, locales ...i18n.Locale) string {
	if d < 0 {
		d = 0
	}
	secs := int(d.Seconds())
	if secs < 60 {
		return sessionCopy(locales, "elapsed.seconds", secs)
	}
	return sessionCopy(locales, "elapsed.minutes", secs/60, secs%60)
}

type toolActivitySender struct {
	mu          sync.Mutex
	minInterval time.Duration
	send        func(string) (string, error)
	patch       func(string, string) error
	messageID   string
	lastText    string
	lastSent    time.Time
	pendingText string
	timer       *time.Timer
}

const mainTurnActivityMessageID = "__amux_cardkit_main_turn__"

type activitySendKind int

const (
	activitySendMaybe activitySendKind = iota
	activitySendForce
	activitySendFlush
	activitySendReset
	activitySendClose
)

type activitySendCommand struct {
	kind activitySendKind
	text string
	done chan struct{}
}

type mainTurnActivitySender struct {
	mu        sync.Mutex
	commands  chan activitySendCommand
	wake      chan struct{}
	pending   *activitySendCommand
	done      chan struct{}
	closeOnce sync.Once
}

const (
	turnStatusSilentDelay     = 10 * time.Second
	turnStatusRefreshInterval = 15 * time.Second
)

// turnStatusRefreshPolicy owns only UI heartbeat cadence. Provider activity
// timestamps come from agentui; task-plane polling has a separate schedule.
type turnStatusRefreshPolicy struct {
	lastActivity time.Time
	lastRefresh  time.Time
}

func (p *turnStatusRefreshPolicy) Due(now time.Time, view *agentui.ActivityView) bool {
	if view == nil || view.Status != "running" || view.LastActivityAt.IsZero() {
		return false
	}
	if !view.LastActivityAt.Equal(p.lastActivity) {
		p.lastActivity = view.LastActivityAt
		p.lastRefresh = time.Time{}
	}
	if now.Sub(view.LastActivityAt) < turnStatusSilentDelay {
		return false
	}
	if !p.lastRefresh.IsZero() && now.Sub(p.lastRefresh) < turnStatusRefreshInterval {
		return false
	}
	p.lastRefresh = now
	return true
}

func (p *turnStatusRefreshPolicy) MarkRefreshed(now time.Time, view *agentui.ActivityView) {
	if view == nil {
		return
	}
	p.lastActivity = view.LastActivityAt
	p.lastRefresh = now
}

func (p *turnStatusRefreshPolicy) Reset() {
	p.lastActivity = time.Time{}
	p.lastRefresh = time.Time{}
}

func newMainTurnActivitySender(
	minInterval time.Duration,
	reply *streamingReplySender,
	render func() turnCardActivity,
	legacySend func(string) (string, error),
	legacyPatch func(string, string) error,
	onFallback func(error),
) *mainTurnActivitySender {
	inner := newToolActivitySender(minInterval, func(text string) (string, error) {
		if reply != nil && render != nil {
			handled, err := reply.UpdateActivity(render())
			if handled {
				return mainTurnActivityMessageID, err
			}
			if err != nil && onFallback != nil {
				onFallback(err)
			}
		}
		if legacySend == nil {
			return "", fmt.Errorf("legacy activity send unavailable")
		}
		return legacySend(text)
	}, func(messageID, text string) error {
		if messageID == mainTurnActivityMessageID {
			if reply == nil || render == nil {
				return fmt.Errorf("main turn activity updater unavailable")
			}
			handled, err := reply.UpdateActivity(render())
			if err != nil {
				return err
			}
			if !handled {
				return fmt.Errorf("main turn activity backend unavailable after CardKit send")
			}
			return nil
		}
		if legacyPatch == nil {
			return fmt.Errorf("legacy activity patch unavailable")
		}
		return legacyPatch(messageID, text)
	})
	sender := &mainTurnActivitySender{
		commands: make(chan activitySendCommand),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go sender.run(inner)
	return sender
}

func (s *mainTurnActivitySender) run(inner *toolActivitySender) {
	defer close(s.done)
	for {
		var cmd activitySendCommand
		select {
		case cmd = <-s.commands:
			s.runPending(inner)
		case <-s.wake:
			if !s.runPending(inner) {
				continue
			}
			continue
		}
		switch cmd.kind {
		case activitySendFlush:
			inner.Flush(cmd.text)
		case activitySendReset:
			inner.Reset()
		case activitySendClose:
			inner.Close()
		}
		if cmd.done != nil {
			close(cmd.done)
		}
		if cmd.kind == activitySendClose {
			return
		}
	}
}

func (s *mainTurnActivitySender) runPending(inner *toolActivitySender) bool {
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	if pending == nil {
		return false
	}
	if pending.kind == activitySendForce {
		inner.ForceSend(pending.text)
	} else {
		inner.MaybeSend(pending.text)
	}
	return true
}

func (s *mainTurnActivitySender) MaybeSend(text string) {
	s.enqueue(activitySendCommand{kind: activitySendMaybe, text: text})
}

func (s *mainTurnActivitySender) ForceSend(text string) {
	s.enqueue(activitySendCommand{kind: activitySendForce, text: text})
}

func (s *mainTurnActivitySender) Flush(text string) {
	s.runSync(activitySendFlush, text)
}

func (s *mainTurnActivitySender) Reset() {
	s.runSync(activitySendReset, "")
}

func (s *mainTurnActivitySender) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { s.runSync(activitySendClose, "") })
}

func (s *mainTurnActivitySender) enqueue(cmd activitySendCommand) {
	if s == nil || strings.TrimSpace(cmd.text) == "" {
		return
	}
	select {
	case <-s.done:
		return
	default:
	}
	s.mu.Lock()
	s.pending = &cmd
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *mainTurnActivitySender) runSync(kind activitySendKind, text string) {
	if s == nil {
		return
	}
	done := make(chan struct{})
	select {
	case s.commands <- activitySendCommand{kind: kind, text: text, done: done}:
	case <-s.done:
		return
	}
	select {
	case <-done:
	case <-s.done:
	}
}

func newToolActivitySender(minInterval time.Duration, send func(string) (string, error), patch func(string, string) error) *toolActivitySender {
	return &toolActivitySender{minInterval: minInterval, send: send, patch: patch}
}

func (s *toolActivitySender) MaybeSend(text string) {
	if s == nil || strings.TrimSpace(text) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == s.lastText {
		return
	}
	if !s.lastSent.IsZero() && time.Since(s.lastSent) < s.minInterval {
		s.pendingText = text
		if s.timer == nil {
			remaining := s.minInterval - time.Since(s.lastSent)
			s.timer = time.AfterFunc(remaining, s.sendPending)
		}
		return
	}
	s.stopTimerLocked()
	s.pendingText = ""
	s.sendNowLocked(text)
}

func (s *toolActivitySender) Flush(text string) {
	if s == nil || strings.TrimSpace(text) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopTimerLocked()
	s.pendingText = ""
	s.sendNowLocked(text)
}

func (s *toolActivitySender) ForceSend(text string) {
	if s == nil || strings.TrimSpace(text) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopTimerLocked()
	s.pendingText = ""
	s.sendNowLocked(text)
}

func (s *toolActivitySender) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopTimerLocked()
	s.pendingText = ""
	s.messageID = ""
	s.lastText = ""
	s.lastSent = time.Time{}
}

func (s *toolActivitySender) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopTimerLocked()
	s.pendingText = ""
}

func (s *toolActivitySender) sendPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	text := s.pendingText
	s.pendingText = ""
	if strings.TrimSpace(text) == "" || text == s.lastText {
		return
	}
	s.sendNowLocked(text)
}

func (s *toolActivitySender) stopTimerLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *toolActivitySender) sendNowLocked(text string) {
	if s.messageID != "" && s.patch != nil {
		if err := s.patch(s.messageID, text); err != nil {
			return
		}
		s.lastText = text
		s.lastSent = time.Now()
		return
	}
	if s.send != nil {
		msgID, err := s.send(text)
		if err != nil {
			// Keep lastText at the last delivered activity: the failed update
			// must stay re-sendable, or the card freezes on stale content (e.g.
			// the final usage/speed title) because later sends dedupe against a
			// fingerprint that never reached Feishu.
			return
		}
		s.messageID = msgID
	}
	s.lastText = text
	s.lastSent = time.Now()
}

type toolActivityTurn struct {
	mu       sync.Mutex
	seq      int
	active   bool
	question string
	followup string
	status   string
}

func (t *toolActivityTurn) Start(seq int, question string) toolActivityCardMeta {
	t.mu.Lock()
	defer t.mu.Unlock()
	question = strings.TrimSpace(question)
	if seq <= 0 {
		if !t.active {
			t.seq++
		}
		seq = t.seq
	}
	if seq != t.seq || !t.active {
		t.seq = seq
		t.active = true
		t.question = question
		t.followup = ""
		t.status = "running"
		return t.metaLocked()
	}
	if question != "" {
		if t.question == "" {
			t.question = question
		} else if question != t.question {
			t.followup = question
		}
	}
	t.status = "running"
	return t.metaLocked()
}

func (t *toolActivityTurn) Complete(failed bool) toolActivityCardMeta {
	t.mu.Lock()
	defer t.mu.Unlock()
	if failed {
		t.status = "failed"
	} else if t.status != "waiting" {
		t.status = "done"
	}
	t.active = false
	return t.metaLocked()
}

func (t *toolActivityTurn) Fail() toolActivityCardMeta {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = "failed"
	return t.metaLocked()
}

func (t *toolActivityTurn) MarkWaiting() toolActivityCardMeta {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seq > 0 {
		t.active = true
		t.status = "waiting"
	}
	return t.metaLocked()
}

func (t *toolActivityTurn) Meta() toolActivityCardMeta {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.metaLocked()
}

func (t *toolActivityTurn) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active && t.status == "running"
}

func (t *toolActivityTurn) metaLocked() toolActivityCardMeta {
	return toolActivityCardMeta{TurnSeq: t.seq, Status: t.status, Question: t.question, Followup: t.followup}
}

func isFollowupPrompt(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？") {
		return true
	}
	for _, marker := range []string{"请确认", "请提供", "请选择", "是否继续"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
