package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/config"
)

// subtaskCardController owns dedicated progress-card claims across parent turns.
type subtaskCardController struct {
	subtaskCardMu sync.Mutex
	subtaskCards  map[string]struct{}
}

const (
	subtaskCardUpdateInterval = 2 * time.Second
	subtaskCardDefaultTimeout = 35 * time.Minute
)

type subtaskEvent struct {
	TaskID string              `json:"task_id"`
	TurnID string              `json:"turn_id"`
	Tool   *agentui.ToolEvent  `json:"tool,omitempty"`
	Usage  json.RawMessage     `json:"usage,omitempty"`
	Seq    uint64              `json:"seq"`
	Kind   string              `json:"kind"`
	Status string              `json:"status,omitempty"`
	Output []byte              `json:"output,omitempty"`
	Result *subtaskEventResult `json:"result,omitempty"`
	At     time.Time           `json:"at"`
}

type subtaskEventResult struct {
	Summary   string `json:"summary,omitempty"`
	UsageJSON []byte `json:"usage_json,omitempty"`
}

type subtaskCardState struct {
	mu        sync.Mutex
	progress  subtaskProgress
	output    string
	lastSeq   uint64
	lastAt    time.Time
	presenter *agentui.Presenter
	activity  *agentui.ActivityView
}

func newSubtaskCardState(progress subtaskProgress) *subtaskCardState {
	state := &subtaskCardState{progress: progress}
	if !progress.StartedAt.IsZero() {
		state.lastAt = progress.StartedAt
	} else {
		state.lastAt = progress.RequestedAt
	}
	return state
}

func (s *subtaskCardState) applyEvent(event subtaskEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.TaskID != "" && event.TaskID != s.progress.ID {
		return
	}
	if event.Seq <= s.lastSeq {
		return
	}
	wasTerminal := isTerminalActivityItemStatus(s.progress.Status)
	s.lastSeq = event.Seq
	if !event.At.IsZero() {
		s.lastAt = event.At
	}
	if status := strings.TrimSpace(event.Status); status != "" {
		if !wasTerminal || isTerminalActivityItemStatus(status) {
			s.progress.Status = status
		}
	}
	switch event.Kind {
	case "usage":
		if len(event.Usage) > 0 {
			s.progress.UsageJSON = append([]byte(nil), event.Usage...)
		}
	case "tool":
		if event.Tool != nil {
			if s.presenter == nil {
				s.presenter = agentui.NewPresenterWithClock(func() time.Time { return s.lastAt })
			}
			for _, action := range s.presenter.Consume(agentui.Event{
				Kind: agentui.KindToolUpdated,
				Turn: &agentui.TurnEvent{ID: event.TurnID},
				Tool: event.Tool,
			}) {
				if action.Kind == agentui.ActionActivityChanged {
					s.activity = action.Activity
				}
			}
		}
	case "output":
		s.output = truncateAssistantProgress(s.output+string(event.Output), maxAssistantProgressRunes)
	case "permission":
		if !wasTerminal {
			s.progress.Status = "waiting_permission"
		}
	case "result":
		if event.Result != nil {
			s.progress.Summary = strings.TrimSpace(event.Result.Summary)
			if len(event.Result.UsageJSON) > 0 {
				s.progress.UsageJSON = append([]byte(nil), event.Result.UsageJSON...)
			}
			if kept, ok := trimExactResultSuffix(s.output, s.progress.Summary); ok {
				s.output = kept
			}
		}
	}
}

func (s *subtaskCardState) updateProgress(progress subtaskProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if progress.EffectiveModel == "" {
		progress.EffectiveModel = s.progress.EffectiveModel
	}
	if progress.AgentProfile == "" {
		progress.AgentProfile = s.progress.AgentProfile
	}
	if progress.AgentType == "" {
		progress.AgentType = s.progress.AgentType
	}
	if progress.AgentInstanceID == "" {
		progress.AgentInstanceID = s.progress.AgentInstanceID
	}
	if progress.ChildSessionID == "" {
		progress.ChildSessionID = s.progress.ChildSessionID
	}
	if len(progress.UsageJSON) == 0 {
		progress.UsageJSON = s.progress.UsageJSON
	}
	s.progress = progress
	if !progress.FinishedAt.IsZero() {
		s.lastAt = progress.FinishedAt
	}
}

type subtaskChildMetadata struct {
	EffectiveModel  string
	AgentInstanceID string
}

func (s *subtaskCardState) setChildMetadata(metadata subtaskChildMetadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	applySubtaskChildMetadata(&s.progress, metadata)
}

func applySubtaskChildMetadata(progress *subtaskProgress, metadata subtaskChildMetadata) {
	if metadata.EffectiveModel != "" {
		progress.EffectiveModel = metadata.EffectiveModel
	}
	if progress.AgentInstanceID == "" {
		progress.AgentInstanceID = metadata.AgentInstanceID
	}
}

// subtaskChildMetadata consumes the runtime's structured child-session metadata.
// A task's requested model, its parent model and display text are not substitutes.
func (b *Bridge) subtaskChildMetadata(targetID, sessionID, sessionName string) subtaskChildMetadata {
	if b.hub == nil || targetID == "" || (sessionID == "" && sessionName == "") {
		return subtaskChildMetadata{}
	}
	for _, target := range b.hub.ListTargets() {
		if target.GetId() != targetID {
			continue
		}
		for _, child := range target.GetSessions() {
			if sessionID != "" {
				if child.GetId() != sessionID {
					continue
				}
			} else if child.GetName() != sessionName {
				continue
			}
			return subtaskChildMetadata{
				EffectiveModel:  strings.TrimSpace(child.GetEffectiveModel()),
				AgentInstanceID: strings.TrimSpace(child.GetAgentInstanceId()),
			}
		}
	}
	return subtaskChildMetadata{}
}

func (b *Bridge) subtaskCardSnapshot(state *subtaskCardState, now time.Time) subtaskCardSnapshot {
	snapshot := state.snapshot(now)
	progress := snapshot.Progress
	metadata := b.subtaskChildMetadata(progress.TargetID, progress.ChildSessionID, progress.ChildSessionName)
	state.setChildMetadata(metadata)
	applySubtaskChildMetadata(&snapshot.Progress, metadata)
	return snapshot
}

func (s *subtaskCardState) snapshot(now time.Time) subtaskCardSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	progress := s.progress
	start := progress.StartedAt
	if start.IsZero() {
		start = progress.RequestedAt
	}
	end := now
	if !progress.FinishedAt.IsZero() {
		end = progress.FinishedAt
	}
	if !start.IsZero() && end.After(start) {
		progress.Elapsed = end.Sub(start)
	}
	return subtaskCardSnapshot{
		Progress: progress,
		Activity: s.activity,
		Output:   s.output,
		LastSeq:  s.lastSeq,
		LastAt:   s.lastAt,
	}
}

type subtaskCardSnapshot struct {
	Progress subtaskProgress
	Activity *agentui.ActivityView
	Output   string
	LastSeq  uint64
	LastAt   time.Time
}

func (s subtaskCardSnapshot) fingerprint() string {
	encoded, err := json.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%s:%d:%s", s.Progress.ID, s.LastSeq, s.Progress.Status)
	}
	return string(encoded)
}

func (s subtaskCardSnapshot) terminal() bool {
	return isTerminalActivityItemStatus(s.Progress.Status)
}

func (s subtaskCardSnapshot) activity(viewURL string, now time.Time) turnCardActivity {
	meta := subtaskCardMeta(s, viewURL, now)
	rendered := renderTurnActivity(meta)
	if result := subtaskCardFinalText(s.Progress); result != "" && len(rendered.Element.Columns) > 0 {
		rendered.Element.Columns[0].Elements = append(
			rendered.Element.Columns[0].Elements,
			cardElement{Tag: "hr", ElementID: cardKitAnswerDividerElementID},
			cardElement{
				Tag:       "markdown",
				ElementID: cardKitReplyElementID,
				Content:   renderFinalResultText(result),
			},
		)
	}
	rendered.Trailing = append(rendered.Trailing, renderAmuxBrandFooterElements()...)
	rendered.JSON, _ = json.Marshal(rendered.Element)
	rendered.Terminal = s.terminal()
	return rendered
}

func subtaskCardStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success", "completed", "done":
		return "done"
	case "failed", "error", "timed_out", "timeout":
		return "failed"
	case "cancelled", "canceled", "stopped":
		return "stopped"
	case "waiting_permission":
		return "waiting"
	default:
		return "running"
	}
}

type subtaskCardDelivery struct {
	reply    *streamingReplySender
	activity *mainTurnActivitySender
}

func newSubtaskCardDelivery(
	backend replyCardBackend,
	render func() turnCardActivity,
	legacySend func(string) (string, error),
	legacyPatch func(string, string) error,
	onFallback func(error),
) *subtaskCardDelivery {
	reply := newStreamingReplySender(subtaskCardUpdateInterval, backend, nil)
	return &subtaskCardDelivery{
		reply: reply,
		activity: newMainTurnActivitySender(
			subtaskCardUpdateInterval,
			reply,
			render,
			legacySend,
			legacyPatch,
			onFallback,
		),
	}
}

func (d *subtaskCardDelivery) Update(fingerprint string, force bool) {
	if d == nil || d.activity == nil {
		return
	}
	if force {
		d.activity.ForceSend(fingerprint)
		return
	}
	d.activity.MaybeSend(fingerprint)
}

func (d *subtaskCardDelivery) Finish(fingerprint string) {
	if d == nil {
		return
	}
	if d.activity != nil {
		d.activity.Flush(fingerprint)
	}
	if d.reply != nil {
		d.reply.Flush()
	}
	d.Close()
}

func (d *subtaskCardDelivery) Close() {
	if d == nil {
		return
	}
	if d.activity != nil {
		d.activity.Close()
	}
	if d.reply != nil {
		d.reply.Close()
	}
}

func (b *Bridge) startSubtaskCards(chat *LarkChat, subtasks []subtaskProgress) {
	if b == nil || b.subtasks == nil || chat == nil {
		return
	}
	if _, ok := b.subtasks.(SubtaskEventReader); !ok {
		return
	}
	for _, progress := range subtasks {
		if strings.TrimSpace(progress.ID) == "" || !b.claimSubtaskCard(progress.ID) {
			continue
		}
		progress := progress
		binding := *chat
		go b.watchSubtaskCard(context.Background(), &binding, progress)
	}
}

func (b *subtaskCardController) claimSubtaskCard(taskID string) bool {
	b.subtaskCardMu.Lock()
	defer b.subtaskCardMu.Unlock()
	if b.subtaskCards == nil {
		b.subtaskCards = make(map[string]struct{})
	}
	if _, exists := b.subtaskCards[taskID]; exists {
		return false
	}
	b.subtaskCards[taskID] = struct{}{}
	return true
}

func (b *Bridge) watchSubtaskCard(ctx context.Context, chat *LarkChat, initial subtaskProgress) {
	eventReader, ok := b.subtasks.(SubtaskEventReader)
	if !ok {
		return
	}
	state := newSubtaskCardState(initial)
	var viewURLMu sync.RWMutex
	viewURL := b.sessionViewerURL(initial.TargetID, initial.ChildSessionName)
	currentViewURL := func() string {
		viewURLMu.RLock()
		defer viewURLMu.RUnlock()
		return viewURL
	}
	render := func() turnCardActivity {
		now := time.Now()
		return b.subtaskCardSnapshot(state, now).activity(currentViewURL(), now)
	}
	legacySend := func(string) (string, error) {
		sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		now := time.Now()
		msgID, _, err := b.sendInteractiveToBinding(
			sendCtx,
			chat,
			renderSubtaskActivityCard(b.subtaskCardSnapshot(state, now), currentViewURL(), now),
			"subtask activity card",
		)
		return msgID, err
	}
	legacyPatch := func(messageID, _ string) error {
		sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		now := time.Now()
		return b.patchInteractiveMessage(
			sendCtx,
			messageID,
			renderSubtaskActivityCard(b.subtaskCardSnapshot(state, now), currentViewURL(), now),
			"subtask activity card",
		)
	}
	backend := newBindingReplyCardBackend(b, chat)
	if b.subtaskCardBackend != nil {
		backend = b.subtaskCardBackend(chat)
	}
	delivery := newSubtaskCardDelivery(
		backend,
		render,
		legacySend,
		legacyPatch,
		func(err error) {
			log.Warn().Err(err).Str("task_id", initial.ID).
				Msg("bridge: CardKit subtask activity unavailable before send; using legacy activity card")
		},
	)
	defer delivery.Close()
	initialSnapshot := state.snapshot(time.Now())
	delivery.Update(initialSnapshot.fingerprint(), true)

	timeout := subtaskCardDefaultTimeout
	if initial.TimeoutSeconds > 0 {
		timeout = time.Duration(initial.TimeoutSeconds)*time.Second + 5*time.Minute
	}
	watchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fromSeq := uint64(0)
	for {
		err := eventReader.StreamSubtaskEvents(watchCtx, initial.ID, fromSeq, func(raw []byte) error {
			var event subtaskEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				return fmt.Errorf("decode subtask event: %w", err)
			}
			if event.Seq <= fromSeq {
				return nil
			}
			fromSeq = event.Seq
			// The card can be discovered while the TaskRun is still pending,
			// before it has a child identity. Resolve that identity when execution
			// starts so the live title and viewer link can follow child metadata.
			if event.Kind == "status" && event.Status == "running" {
				readCtx, readCancel := context.WithTimeout(watchCtx, 2*time.Second)
				progress, ok := b.readSubtaskProgress(readCtx, initial.ID, time.Now())
				readCancel()
				if ok {
					state.updateProgress(progress)
					if progress.TargetID != "" && progress.ChildSessionName != "" {
						viewURLMu.Lock()
						viewURL = b.sessionViewerURL(progress.TargetID, progress.ChildSessionName)
						viewURLMu.Unlock()
					}
				}
			}
			state.applyEvent(event)
			snapshot := state.snapshot(time.Now())
			force := event.Kind == "status" || event.Kind == "permission" || event.Kind == "result"
			delivery.Update(snapshot.fingerprint(), force)
			return nil
		})

		progress, ok := b.readSubtaskProgress(watchCtx, initial.ID, time.Now())
		if ok {
			state.updateProgress(progress)
			if progress.TargetID != "" && progress.ChildSessionName != "" {
				viewURLMu.Lock()
				viewURL = b.sessionViewerURL(progress.TargetID, progress.ChildSessionName)
				viewURLMu.Unlock()
			}
		}
		snapshot := state.snapshot(time.Now())
		if snapshot.terminal() || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			delivery.Finish(snapshot.fingerprint())
			return
		}
		logEvent := log.Warn().Str("task_id", initial.ID).Uint64("from_seq", fromSeq)
		if err != nil {
			logEvent = logEvent.Err(err)
		}
		logEvent.Msg("bridge: subtask event stream interrupted before terminal state; retrying")
		select {
		case <-watchCtx.Done():
			delivery.Finish(snapshot.fingerprint())
			return
		case <-time.After(time.Second):
		}
	}
}

func subtaskCardFinalText(progress subtaskProgress) string {
	if detail := subtaskTerminalDetail(progress); detail != "" {
		return detail
	}
	switch strings.ToLower(strings.TrimSpace(progress.Status)) {
	case "succeeded", "success", "completed", "done":
		return "子 Agent 已完成任务。"
	case "failed", "error":
		return "子 Agent 执行失败。"
	case "timed_out", "timeout":
		return "子 Agent 执行超时。"
	case "cancelled", "canceled", "stopped":
		return "子 Agent 任务已取消。"
	default:
		return ""
	}
}

// subtaskAgentName uses the durable instance identity across followups. Ephemeral
// agents use their child session identity once the task has started.
func subtaskAgentName(progress subtaskProgress) string {
	profile, _, _ := strings.Cut(strings.TrimSpace(progress.AgentProfile), "@")
	name := profile[strings.LastIndex(profile, "/")+1:]
	if name == "" {
		name = strings.TrimSpace(progress.AgentType)
	}
	if name == "" {
		name = "agent"
	}
	id := strings.TrimPrefix(strings.TrimSpace(progress.AgentInstanceID), "agent-")
	if id == "" {
		id = strings.TrimSpace(progress.ChildSessionID)
	}
	if id == "" {
		id = strings.TrimPrefix(strings.TrimSpace(progress.ID), "task-")
	}
	if id == "" {
		return name
	}
	shortID := []rune(id)
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return name + "-" + string(shortID)
}

func subtaskCardMeta(snapshot subtaskCardSnapshot, viewURL string, now time.Time) toolActivityCardMeta {
	progressText := strings.TrimSpace(snapshot.Output)
	startedAt := snapshot.Progress.StartedAt
	if startedAt.IsZero() {
		startedAt = snapshot.Progress.RequestedAt
	}
	finishedAt := snapshot.Progress.FinishedAt
	if finishedAt.IsZero() && snapshot.terminal() {
		finishedAt = snapshot.LastAt
	}
	lastAt := snapshot.LastAt
	if lastAt.IsZero() {
		lastAt = startedAt
	}
	activity := &agentui.ActivityView{}
	if snapshot.Activity != nil {
		*activity = *snapshot.Activity
	}
	activity.Status = subtaskCardStatus(snapshot.Progress.Status)
	activity.UsageJSON = snapshot.Progress.UsageJSON
	activity.CurrentStep = progressText
	activity.StartedAt = startedAt
	activity.FinishedAt = finishedAt
	activity.LastActivityAt = lastAt
	return toolActivityCardMeta{
		Status:          subtaskCardStatus(snapshot.Progress.Status),
		TitlePrefix:     subtaskAgentName(snapshot.Progress) + " · ",
		Question:        snapshot.Progress.Title,
		AgentProfile:    snapshot.Progress.AgentProfile,
		EffectiveModel:  snapshot.Progress.EffectiveModel,
		AgentType:       snapshot.Progress.AgentType,
		ViewURL:         viewURL,
		Activity:        activity,
		ThinkingProcess: config.FeishuThinkingProcessExpanded,
		Now:             now,
	}
}

func renderSubtaskActivityCard(snapshot subtaskCardSnapshot, viewURL string, now time.Time) string {
	activity := snapshot.activity(viewURL, now)
	doc := newCardDoc(activity.elements())
	return marshalActivityCard(doc, subtaskCardMeta(snapshot, viewURL, now))
}
