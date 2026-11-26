package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

const (
	// modelRecoveryQueueWaitThreshold is how long a turn must remain in
	// upstream retry or queue wait without visible output before the progress
	// card exposes same-engine model switch recovery buttons.
	modelRecoveryQueueWaitThreshold = 60 * time.Second

	modelRecoveryMaxCandidates    = 3
	modelRecoveryMaxMoreOptions   = 20
	modelRecoveryCacheMaxEntries  = 256
	modelRecoveryCacheTTL         = 30 * time.Minute
	modelRecoveryMaxImageBytes    = 4 << 20
	modelRecoveryRankTimeout      = 2 * time.Second
	modelRecoveryInterruptWait    = 3 * time.Second
	modelRecoveryInterruptPoll    = 100 * time.Millisecond
	modelRecoveryActionKey        = "amux_model_recovery"
	modelRecoveryActionSwitch     = "switch_retry"
	modelRecoveryActionSelect     = "switch_retry_select"
	modelRecoveryReasonRateLimit  = "rate_limit"
	modelRecoveryReasonQueued     = "queued"
	modelRecoveryActionsElementID = "model_recovery_actions"
	modelRecoveryMoreElementID    = "model_recovery_more"
)

type modelRecoveryCardPrompt struct {
	TurnID         string
	TargetID       string
	SessionName    string
	RootMsgID      string
	Reason         string
	Candidates     []FallbackModelCandidate
	MoreCandidates []FallbackModelCandidate
}

type turnRecoveryEntry struct {
	TurnID          string
	BridgeID        string
	RootMsgID       string
	ChatID          string
	ThreadID        string
	TargetID        string
	SessionName     string
	AgentProfile    string
	AgentType       string
	EffectiveModel  string
	InitiatorOpenID string

	Text               string
	Parts              []*pb.InputPart
	PartsOmitted       bool
	SystemInstructions string
	Inputs             []turnInputRef

	ToolsExecuted    bool
	FailedRateLimit  bool
	Active           bool
	Claimed          bool
	CandidatesModel  string
	Candidates       []FallbackModelCandidate
	MoreCandidates   []FallbackModelCandidate
	CandidatesLoaded bool
	CreatedAt        time.Time
}

type turnRecoveryState struct {
	mu           sync.Mutex
	entries      map[string]*turnRecoveryEntry
	claimedTurns map[string]time.Time
	order        []string
	nowFn        func() time.Time
	// runAsync allows tests to run the background switch-and-retry synchronously.
	runAsync func(func())
	// sendNotice overrides sendTextToBinding in tests when no Lark client exists.
	sendNotice func(context.Context, *LarkChat, string)
}

func (b *Bridge) sendRecoveryNotice(ctx context.Context, chat *LarkChat, text string) {
	if b == nil || chat == nil || strings.TrimSpace(text) == "" {
		return
	}
	if b.turnRecovery.sendNotice != nil {
		b.turnRecovery.sendNotice(ctx, chat, text)
		return
	}
	if b.client == nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, _, _ = b.sendTextToBinding(sendCtx, chat, text)
}

func (s *turnRecoveryState) now() time.Time {
	if s != nil && s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

func (s *turnRecoveryState) dispatch(fn func()) {
	if s != nil && s.runAsync != nil {
		s.runAsync(fn)
		return
	}
	go fn()
}

func (s *turnRecoveryState) pruneLocked(now time.Time) {
	cutoff := now.Add(-modelRecoveryCacheTTL)
	for id, claimedAt := range s.claimedTurns {
		if claimedAt.Before(cutoff) {
			delete(s.claimedTurns, id)
		}
	}
	if len(s.entries) == 0 {
		return
	}
	kept := s.order[:0]
	for _, id := range s.order {
		entry, ok := s.entries[id]
		if !ok {
			continue
		}
		if entry.CreatedAt.Before(cutoff) {
			delete(s.entries, id)
			continue
		}
		kept = append(kept, id)
	}
	for len(kept) > modelRecoveryCacheMaxEntries {
		evict := kept[0]
		kept = kept[1:]
		delete(s.entries, evict)
	}
	s.order = kept
}

func (b *Bridge) isTurnRecoveryClaimed(turnID string) bool {
	if b == nil {
		return false
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return false
	}
	b.turnRecovery.mu.Lock()
	defer b.turnRecovery.mu.Unlock()
	if claimedAt, ok := b.turnRecovery.claimedTurns[turnID]; ok {
		if b.turnRecovery.now().Sub(claimedAt) <= modelRecoveryCacheTTL {
			return true
		}
		delete(b.turnRecovery.claimedTurns, turnID)
	}
	if entry, ok := b.turnRecovery.entries[turnID]; ok && entry.Claimed {
		return true
	}
	return false
}

func cloneRecoveryParts(parts []*pb.InputPart) ([]*pb.InputPart, bool) {
	if len(parts) == 0 {
		return nil, false
	}
	totalImageBytes := 0
	for _, part := range parts {
		if img := part.GetImage(); img != nil {
			totalImageBytes += len(img.GetData())
			if totalImageBytes > modelRecoveryMaxImageBytes {
				return nil, true
			}
		}
	}
	cloned := make([]*pb.InputPart, 0, len(parts))
	for _, part := range parts {
		if part == nil {
			continue
		}
		switch v := part.Value.(type) {
		case *pb.InputPart_Text:
			cloned = append(cloned, &pb.InputPart{Value: &pb.InputPart_Text{Text: v.Text}})
		case *pb.InputPart_Image:
			if v.Image == nil {
				continue
			}
			data := append([]byte(nil), v.Image.Data...)
			cloned = append(cloned, &pb.InputPart{Value: &pb.InputPart_Image{Image: &pb.ImageInput{
				Data:        data,
				MimeType:    v.Image.MimeType,
				DisplayName: v.Image.DisplayName,
			}}})
		}
	}
	return cloned, false
}

func (b *Bridge) recordTurnRecovery(chat *LarkChat, turnID, text string, parts []*pb.InputPart, systemInstructions string, inputs []turnInputRef, steered bool) {
	if b == nil || chat == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || strings.TrimSpace(chat.SessionName) == "" {
		return
	}
	initiator := strings.TrimSpace(chat.OpenId)
	for _, in := range inputs {
		if id := strings.TrimSpace(in.OpenID); id != "" {
			initiator = id
			break
		}
	}
	clonedParts, omitted := cloneRecoveryParts(parts)
	clonedInputs := append([]turnInputRef(nil), inputs...)
	now := b.turnRecovery.now()

	b.turnRecovery.mu.Lock()
	defer b.turnRecovery.mu.Unlock()
	if b.turnRecovery.entries == nil {
		b.turnRecovery.entries = make(map[string]*turnRecoveryEntry)
	}
	b.turnRecovery.pruneLocked(now)
	if existing, ok := b.turnRecovery.entries[turnID]; ok && steered {
		// Steered follow-up joined an existing running turn; keep original entry
		// but mark parts omitted if the steered follow-up carried oversized images.
		if omitted {
			existing.PartsOmitted = true
			existing.Parts = nil
		}
		return
	}
	entry := &turnRecoveryEntry{
		TurnID:             turnID,
		BridgeID:           strings.TrimSpace(chat.BridgeId),
		RootMsgID:          strings.TrimSpace(chat.RootMsgID),
		ChatID:             strings.TrimSpace(chat.ChatID),
		ThreadID:           strings.TrimSpace(chat.ThreadID),
		TargetID:           strings.TrimSpace(chat.TargetID),
		SessionName:        strings.TrimSpace(chat.SessionName),
		AgentProfile:       strings.TrimSpace(chat.AgentProfile),
		AgentType:          strings.TrimSpace(chat.AgentType),
		EffectiveModel:     strings.TrimSpace(chat.EffectiveModel),
		InitiatorOpenID:    initiator,
		Text:               text,
		Parts:              clonedParts,
		PartsOmitted:       omitted,
		SystemInstructions: systemInstructions,
		Inputs:             clonedInputs,
		Active:             true,
		CreatedAt:          now,
	}
	if _, existed := b.turnRecovery.entries[turnID]; !existed {
		b.turnRecovery.order = append(b.turnRecovery.order, turnID)
	}
	b.turnRecovery.entries[turnID] = entry
	b.turnRecovery.pruneLocked(now)
}

func (b *Bridge) markTurnRecoveryToolUsed(turnID string) {
	if b == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	b.turnRecovery.mu.Lock()
	defer b.turnRecovery.mu.Unlock()
	if entry, ok := b.turnRecovery.entries[turnID]; ok {
		entry.ToolsExecuted = true
	}
}

func (b *Bridge) markTurnRecoveryRateLimited(turnID string, chat *LarkChat, effectiveModel string) {
	if b == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	now := b.turnRecovery.now()
	b.turnRecovery.mu.Lock()
	defer b.turnRecovery.mu.Unlock()
	if b.turnRecovery.entries == nil {
		b.turnRecovery.entries = make(map[string]*turnRecoveryEntry)
	}
	entry, ok := b.turnRecovery.entries[turnID]
	if !ok && chat != nil {
		// Turn started outside bridge (or cache evicted); still allow model
		// switch without automatic replay.
		entry = &turnRecoveryEntry{
			TurnID:          turnID,
			BridgeID:        strings.TrimSpace(chat.BridgeId),
			RootMsgID:       strings.TrimSpace(chat.RootMsgID),
			ChatID:          strings.TrimSpace(chat.ChatID),
			ThreadID:        strings.TrimSpace(chat.ThreadID),
			TargetID:        strings.TrimSpace(chat.TargetID),
			SessionName:     strings.TrimSpace(chat.SessionName),
			AgentProfile:    strings.TrimSpace(chat.AgentProfile),
			AgentType:       strings.TrimSpace(chat.AgentType),
			EffectiveModel:  strings.TrimSpace(effectiveModel),
			InitiatorOpenID: strings.TrimSpace(chat.OpenId),
			PartsOmitted:    true,
			CreatedAt:       now,
		}
		b.turnRecovery.entries[turnID] = entry
		b.turnRecovery.order = append(b.turnRecovery.order, turnID)
	}
	if entry != nil {
		entry.FailedRateLimit = true
		entry.Active = false
		if m := strings.TrimSpace(effectiveModel); m != "" {
			entry.EffectiveModel = m
		}
	}
}

func (b *Bridge) markTurnRecoveryFinished(turnID string, succeeded bool) {
	if b == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	b.turnRecovery.mu.Lock()
	defer b.turnRecovery.mu.Unlock()
	entry, ok := b.turnRecovery.entries[turnID]
	if !ok {
		return
	}
	entry.Active = false
	if succeeded && !entry.FailedRateLimit {
		delete(b.turnRecovery.entries, turnID)
	}
}

func (b *Bridge) defaultSameEngineFallbackCandidates(agentType, currentModel string) []FallbackModelCandidate {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return nil
	}
	catalog, err := agentprofile.RuntimeCatalog()
	if err != nil {
		return nil
	}
	currentLower := strings.ToLower(strings.TrimSpace(currentModel))
	out := make([]FallbackModelCandidate, 0, 8)
	seen := make(map[string]bool)
	for _, rt := range catalog {
		if !strings.EqualFold(strings.TrimSpace(rt.Ref), agentType) {
			continue
		}
		for _, m := range rt.Models {
			ref := strings.TrimSpace(m.Ref)
			if ref == "" {
				continue
			}
			lower := strings.ToLower(ref)
			if lower == currentLower || seen[lower] {
				continue
			}
			seen[lower] = true
			label := strings.TrimSpace(m.DisplayName)
			if label == "" {
				label = ref
			}
			out = append(out, FallbackModelCandidate{Model: ref, Label: label})
		}
	}
	return out
}

func (b *Bridge) resolveTurnRecoveryCandidates(ctx context.Context, chat *LarkChat, turnID, currentModel string) ([]FallbackModelCandidate, []FallbackModelCandidate) {
	if b == nil || chat == nil {
		return nil, nil
	}
	turnID = strings.TrimSpace(turnID)
	currentModel = strings.TrimSpace(currentModel)
	agentType := strings.TrimSpace(chat.AgentType)
	if agentType == "" {
		return nil, nil
	}
	var openID string
	if turnID != "" {
		b.turnRecovery.mu.Lock()
		if entry, ok := b.turnRecovery.entries[turnID]; ok {
			openID = entry.InitiatorOpenID
			if entry.CandidatesLoaded && strings.EqualFold(entry.CandidatesModel, currentModel) {
				primary := append([]FallbackModelCandidate(nil), entry.Candidates...)
				more := append([]FallbackModelCandidate(nil), entry.MoreCandidates...)
				b.turnRecovery.mu.Unlock()
				return primary, more
			}
		}
		b.turnRecovery.mu.Unlock()
	}
	if openID == "" {
		openID = strings.TrimSpace(chat.OpenId)
	}

	base := b.defaultSameEngineFallbackCandidates(agentType, currentModel)
	all := make([]FallbackModelCandidate, 0, len(base))
	seen := make(map[string]bool)
	currentLower := strings.ToLower(currentModel)
	appendUnique := func(items []FallbackModelCandidate) {
		for _, c := range items {
			m := strings.TrimSpace(c.Model)
			if m == "" {
				continue
			}
			lower := strings.ToLower(m)
			if lower == currentLower || seen[lower] {
				continue
			}
			seen[lower] = true
			label := strings.TrimSpace(c.Label)
			if label == "" {
				label = m
			}
			all = append(all, FallbackModelCandidate{
				Model: m,
				Label: label,
				Hint:  strings.TrimSpace(c.Hint),
			})
		}
	}
	if hook := bridgeExtensions.snapshot().RankFallbackModels; hook != nil {
		principalID := ""
		if b.resolveModelServicePrincipal != nil && openID != "" {
			if id, err := b.resolveModelServicePrincipal(openID); err == nil {
				principalID = strings.TrimSpace(id)
			}
		}
		rankCtx, cancel := context.WithTimeout(ctx, modelRecoveryRankTimeout)
		ranked, err := hook(rankCtx, FallbackModelRequest{
			AgentType:    agentType,
			AgentProfile: strings.TrimSpace(chat.AgentProfile),
			CurrentModel: currentModel,
			TargetID:     strings.TrimSpace(chat.TargetID),
			SessionName:  strings.TrimSpace(chat.SessionName),
			OpenID:       openID,
			PrincipalID:  principalID,
			Candidates:   append([]FallbackModelCandidate(nil), base...),
		})
		cancel()
		if err == nil && len(ranked) > 0 {
			appendUnique(ranked)
		}
	}
	appendUnique(base)

	var primary, more []FallbackModelCandidate
	if len(all) > modelRecoveryMaxCandidates {
		primary = all[:modelRecoveryMaxCandidates]
		rest := all[modelRecoveryMaxCandidates:]
		if len(rest) > modelRecoveryMaxMoreOptions {
			rest = rest[:modelRecoveryMaxMoreOptions]
		}
		more = rest
	} else {
		primary = all
	}

	if turnID != "" {
		b.turnRecovery.mu.Lock()
		if entry, ok := b.turnRecovery.entries[turnID]; ok {
			entry.CandidatesModel = currentModel
			entry.Candidates = append([]FallbackModelCandidate(nil), primary...)
			entry.MoreCandidates = append([]FallbackModelCandidate(nil), more...)
			entry.CandidatesLoaded = true
		}
		b.turnRecovery.mu.Unlock()
	}
	return primary, more
}

func (b *Bridge) setModelRecoveryQueueWaitSeconds(seconds int) {
	if b == nil {
		return
	}
	if seconds < 0 || seconds > config.MaxFeishuModelRecoveryQueueWaitSeconds {
		seconds = config.DefaultFeishuModelRecoveryQueueWaitSeconds
	}
	b.modelRecoveryQueueWaitSeconds.Store(int64(seconds))
	b.modelRecoveryQueueWaitSet.Store(true)
}

func (b *Bridge) currentModelRecoveryQueueWaitSeconds() int {
	if b == nil || !b.modelRecoveryQueueWaitSet.Load() {
		return config.DefaultFeishuModelRecoveryQueueWaitSeconds
	}
	return int(b.modelRecoveryQueueWaitSeconds.Load())
}

func (b *Bridge) modelRecoveryQueueWaitThreshold() time.Duration {
	seconds := b.currentModelRecoveryQueueWaitSeconds()
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// shouldOfferQueuedModelRecovery reports whether a running turn has been
// waiting in upstream retry or queue wait without visible output for at least
// threshold (default 60s; <=0 disables queued recovery controls).
func shouldOfferQueuedModelRecovery(meta toolActivityCardMeta, threshold time.Duration) bool {
	if threshold <= 0 {
		return false
	}
	if meta.SubtaskReport {
		return false
	}
	if meta.Status != "running" && meta.Status != "" {
		return false
	}
	if meta.Activity == nil || activityViewHasOutput(meta.Activity) {
		return false
	}
	now := meta.Now
	if now.IsZero() {
		now = time.Now()
	}
	if meta.Activity.StartedAt.IsZero() {
		return false
	}
	elapsed := now.Sub(meta.Activity.StartedAt)
	if elapsed < threshold {
		return false
	}
	if meta.Activity.RetryCount > 0 {
		return true
	}
	return distributionActivityPossiblyQueued(meta.AgentType, meta.Activity)
}

func (b *Bridge) modelRecoveryPromptForActivity(chat *LarkChat, turnID string, meta toolActivityCardMeta) *modelRecoveryCardPrompt {
	if b == nil || chat == nil || meta.SubtaskReport {
		return nil
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || strings.TrimSpace(chat.SessionName) == "" {
		return nil
	}
	b.turnRecovery.mu.Lock()
	entry := b.turnRecovery.entries[turnID]
	var failedRateLimit, claimed, toolsExecuted bool
	if entry != nil {
		failedRateLimit = entry.FailedRateLimit
		claimed = entry.Claimed
		toolsExecuted = entry.ToolsExecuted
	}
	b.turnRecovery.mu.Unlock()
	if claimed {
		return nil
	}

	reason := ""
	switch {
	case meta.Status == "failed" && failedRateLimit:
		reason = modelRecoveryReasonRateLimit
	case !toolsExecuted && shouldOfferQueuedModelRecovery(meta, b.modelRecoveryQueueWaitThreshold()):
		reason = modelRecoveryReasonQueued
	default:
		return nil
	}

	// Ensure entry exists for queued recovery so clicking the button can validate
	// candidate membership and authorize the initiator.
	b.turnRecovery.mu.Lock()
	if b.turnRecovery.entries == nil {
		b.turnRecovery.entries = make(map[string]*turnRecoveryEntry)
	}
	if _, ok := b.turnRecovery.entries[turnID]; !ok {
		b.turnRecovery.entries[turnID] = &turnRecoveryEntry{
			TurnID:          turnID,
			BridgeID:        strings.TrimSpace(chat.BridgeId),
			RootMsgID:       strings.TrimSpace(chat.RootMsgID),
			ChatID:          strings.TrimSpace(chat.ChatID),
			ThreadID:        strings.TrimSpace(chat.ThreadID),
			TargetID:        strings.TrimSpace(chat.TargetID),
			SessionName:     strings.TrimSpace(chat.SessionName),
			AgentProfile:    strings.TrimSpace(chat.AgentProfile),
			AgentType:       strings.TrimSpace(chat.AgentType),
			EffectiveModel:  strings.TrimSpace(meta.EffectiveModel),
			InitiatorOpenID: strings.TrimSpace(chat.OpenId),
			PartsOmitted:    true,
			Active:          reason == modelRecoveryReasonQueued,
			FailedRateLimit: reason == modelRecoveryReasonRateLimit,
			CreatedAt:       b.turnRecovery.now(),
		}
		b.turnRecovery.order = append(b.turnRecovery.order, turnID)
	}
	b.turnRecovery.mu.Unlock()

	candidates, more := b.resolveTurnRecoveryCandidates(context.Background(), chat, turnID, meta.EffectiveModel)
	if len(candidates) == 0 {
		return nil
	}
	return &modelRecoveryCardPrompt{
		TurnID:         turnID,
		TargetID:       strings.TrimSpace(chat.TargetID),
		SessionName:    strings.TrimSpace(chat.SessionName),
		RootMsgID:      strings.TrimSpace(chat.RootMsgID),
		Reason:         reason,
		Candidates:     candidates,
		MoreCandidates: more,
	}
}

func renderModelRecoveryElements(prompt *modelRecoveryCardPrompt) []cardElement {
	if prompt == nil || len(prompt.Candidates) == 0 {
		return nil
	}
	promptKey := "recovery.prompt.rate_limit"
	if prompt.Reason == modelRecoveryReasonQueued {
		promptKey = "recovery.prompt.queued"
	}
	note := managementTextElement(sessionMessage(promptKey), "notation", "grey", nil)
	note.ElementID = "model_recovery_prompt"

	columns := make([]cardColumn, 0, len(prompt.Candidates))
	for i, cand := range prompt.Candidates {
		label := truncateCardText(cand.Label, 36)
		var text i18n.Text
		if cand.Hint != "" {
			text = sessionMessage("recovery.button.switch_hint", label, truncateCardText(cand.Hint, 20))
		} else {
			text = sessionMessage("recovery.button.switch", label)
		}
		buttonType := "default"
		if i == 0 {
			buttonType = "primary"
		}
		value := map[string]string{
			modelRecoveryActionKey: modelRecoveryActionSwitch,
			"turn_id":              prompt.TurnID,
			"target_id":            prompt.TargetID,
			"session_name":         prompt.SessionName,
			"root_msg_id":          prompt.RootMsgID,
			"model":                cand.Model,
		}
		btn := cardElement{
			Tag:       "button",
			Name:      fmt.Sprintf("model_recovery_%d", i),
			Text:      managementPlainTextPtr(text),
			Type:      buttonType,
			Size:      "small",
			Behaviors: []cardBehavior{{Type: "callback", Value: value}},
		}
		columns = append(columns, cardColumn{
			Tag:           "column",
			Width:         "auto",
			VerticalAlign: "top",
			Elements:      []cardElement{btn},
		})
	}
	row := cardElement{
		Tag:               "column_set",
		ElementID:         modelRecoveryActionsElementID,
		FlexMode:          "flow",
		HorizontalSpacing: "8px",
		Columns:           columns,
	}
	out := []cardElement{note, row}
	if len(prompt.MoreCandidates) > 0 {
		options := make([]cardOption, 0, len(prompt.MoreCandidates))
		for _, cand := range prompt.MoreCandidates {
			label := truncateCardText(cand.Label, 48)
			if cand.Hint != "" {
				label = fmt.Sprintf("%s (%s)", label, truncateCardText(cand.Hint, 20))
			}
			options = append(options, cardOption{
				Text:  cardTitle{Tag: "plain_text", Content: label},
				Value: cand.Model,
			})
		}
		selectValue := map[string]string{
			modelRecoveryActionKey: modelRecoveryActionSelect,
			"turn_id":              prompt.TurnID,
			"target_id":            prompt.TargetID,
			"session_name":         prompt.SessionName,
			"root_msg_id":          prompt.RootMsgID,
		}
		selectEl := cardElement{
			Tag:         "select_static",
			ElementID:   modelRecoveryMoreElementID,
			Name:        modelRecoveryMoreElementID,
			Placeholder: managementPlainTextPtr(sessionMessage("recovery.select.placeholder")),
			Width:       "default",
			Options:     options,
			Behaviors:   []cardBehavior{{Type: "callback", Value: selectValue}},
		}
		out = append(out, selectEl)
	}
	return out
}

func renderModelRecoveryErrorCard(errorText string, prompt *modelRecoveryCardPrompt) string {
	elements := []cardElement{
		{Tag: "markdown", Content: strings.TrimSpace(errorText)},
	}
	elements = append(elements, renderModelRecoveryElements(prompt)...)
	doc := newCardDoc(elements)
	bs, _ := json.Marshal(doc)
	return string(bs)
}

func (b *Bridge) handleModelRecoveryCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, false, nil
	}
	value := event.Event.Action.Value
	action, _ := value[modelRecoveryActionKey].(string)
	if strings.TrimSpace(action) == "" {
		return nil, false, nil
	}
	operator := ""
	if event.Event.Operator != nil {
		operator = strings.TrimSpace(event.Event.Operator.OpenID)
	}
	locale := b.managementLocale(operator)
	if action != modelRecoveryActionSwitch && action != modelRecoveryActionSelect {
		return cardToast("warning", sessionCopy([]i18n.Locale{locale}, "recovery.toast.invalid")), true, nil
	}
	turnID, _ := value["turn_id"].(string)
	targetID, _ := value["target_id"].(string)
	sessionName, _ := value["session_name"].(string)
	rootMsgID, _ := value["root_msg_id"].(string)
	targetModel, _ := value["model"].(string)
	if action == modelRecoveryActionSelect {
		targetModel = event.Event.Action.Option
	}
	turnID = strings.TrimSpace(turnID)
	targetID = strings.TrimSpace(targetID)
	sessionName = strings.TrimSpace(sessionName)
	rootMsgID = strings.TrimSpace(rootMsgID)
	targetModel = strings.TrimSpace(targetModel)
	if turnID == "" || sessionName == "" || targetModel == "" {
		return cardToast("warning", sessionCopy([]i18n.Locale{locale}, "recovery.toast.invalid")), true, nil
	}

	b.turnRecovery.mu.Lock()
	entry, ok := b.turnRecovery.entries[turnID]
	if !ok || entry == nil || b.turnRecovery.now().Sub(entry.CreatedAt) > modelRecoveryCacheTTL {
		delete(b.turnRecovery.entries, turnID)
		b.turnRecovery.mu.Unlock()
		return cardToast("warning", sessionCopy([]i18n.Locale{locale}, "recovery.toast.expired")), true, nil
	}
	if entry.SessionName != sessionName || (targetID != "" && entry.TargetID != "" && entry.TargetID != targetID) {
		b.turnRecovery.mu.Unlock()
		return cardToast("warning", sessionCopy([]i18n.Locale{locale}, "recovery.toast.invalid")), true, nil
	}
	if !b.isBotOwner(operator) && (entry.InitiatorOpenID == "" || operator == "" || operator != entry.InitiatorOpenID) {
		b.turnRecovery.mu.Unlock()
		return cardToast("error", sessionCopy([]i18n.Locale{locale}, "recovery.toast.unauthorized")), true, nil
	}
	if entry.Claimed {
		b.turnRecovery.mu.Unlock()
		return cardToast("warning", sessionCopy([]i18n.Locale{locale}, "recovery.toast.processing")), true, nil
	}
	// Validate targetModel is in the offered candidates (buttons or "other models"
	// dropdown) to prevent arbitrary callback tampering.
	allowedModel := false
	displayLabel := targetModel
	for _, list := range [][]FallbackModelCandidate{entry.Candidates, entry.MoreCandidates} {
		for _, cand := range list {
			if strings.EqualFold(strings.TrimSpace(cand.Model), targetModel) {
				allowedModel = true
				targetModel = strings.TrimSpace(cand.Model)
				if strings.TrimSpace(cand.Label) != "" {
					displayLabel = strings.TrimSpace(cand.Label)
				}
				break
			}
		}
		if allowedModel {
			break
		}
	}
	if !allowedModel {
		b.turnRecovery.mu.Unlock()
		return cardToast("warning", sessionCopy([]i18n.Locale{locale}, "recovery.toast.invalid")), true, nil
	}
	entry.Claimed = true
	if b.turnRecovery.claimedTurns == nil {
		b.turnRecovery.claimedTurns = make(map[string]time.Time)
	}
	b.turnRecovery.claimedTurns[turnID] = b.turnRecovery.now()
	snapshot := *entry
	snapshot.Parts = append([]*pb.InputPart(nil), entry.Parts...)
	snapshot.Inputs = append([]turnInputRef(nil), entry.Inputs...)
	if rootMsgID != "" && snapshot.RootMsgID == "" {
		snapshot.RootMsgID = rootMsgID
	}
	b.turnRecovery.mu.Unlock()

	b.turnRecovery.dispatch(func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		b.executeModelRecoverySwitchRetry(bgCtx, snapshot, targetModel, displayLabel, locale)
	})
	return cardToast("info", sessionCopy([]i18n.Locale{locale}, "recovery.toast.switching", displayLabel)), true, nil
}

func (b *Bridge) executeModelRecoverySwitchRetry(ctx context.Context, entry turnRecoveryEntry, targetModel, displayLabel string, locale i18n.Locale) {
	chat := b.recoveryBindingChat(entry)
	locales := []i18n.Locale{locale}
	releaseClaim := func() {
		b.turnRecovery.mu.Lock()
		if cur, ok := b.turnRecovery.entries[entry.TurnID]; ok {
			cur.Claimed = false
		}
		delete(b.turnRecovery.claimedTurns, entry.TurnID)
		b.turnRecovery.mu.Unlock()
	}

	// If the turn is still running (queued/retrying escape), interrupt and wait
	// briefly for idle so Restart(PreserveHistory: true) hits in-place hot
	// reconfiguration instead of killing the process.
	if entry.Active || b.isTurnActive(entry.RootMsgID) {
		intCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _ = b.hub.Route(intCtx, entry.TargetID, "Interrupt", &pb.InterruptRequest{
			Name:         entry.SessionName,
			PreserveGoal: true,
		})
		cancel()
		deadline := time.Now().Add(modelRecoveryInterruptWait)
		for time.Now().Before(deadline) {
			if !b.isTurnActive(entry.RootMsgID) {
				break
			}
			select {
			case <-ctx.Done():
				break
			case <-time.After(modelRecoveryInterruptPoll):
			}
		}
	}

	newEffectiveModel, err := b.switchSessionModelInPlace(ctx, entry.TargetID, entry.SessionName, targetModel)
	if err != nil {
		releaseClaim()
		log.Warn().Err(err).Str("session", entry.SessionName).Str("model", targetModel).Msg("bridge: turn model recovery switch failed")
		msg := sessionCopy(locales, "recovery.notice.failed", displayLabel, sanitizeProviderErrorSummary(err.Error()))
		b.sendRecoveryNotice(context.Background(), chat, msg)
		return
	}
	if newEffectiveModel == "" {
		newEffectiveModel = targetModel
	}
	b.persistRecoveredSessionModel(entry, newEffectiveModel)
	streamTargetID := strings.TrimSpace(entry.TargetID)
	if streamTargetID == "" && chat != nil {
		streamTargetID = strings.TrimSpace(chat.TargetID)
	}
	b.updateLiveStreamEffectiveModel(streamTargetID, entry.SessionName, newEffectiveModel)
	if chat != nil {
		chat.EffectiveModel = newEffectiveModel
	}

	// Re-check whether tools ran while interrupting (race window before idle).
	b.turnRecovery.mu.Lock()
	toolsUsed := entry.ToolsExecuted
	if cur, ok := b.turnRecovery.entries[entry.TurnID]; ok {
		if cur.ToolsExecuted {
			toolsUsed = true
		}
		delete(b.turnRecovery.entries, entry.TurnID)
	}
	b.turnRecovery.mu.Unlock()

	canReplay := !toolsUsed && !entry.PartsOmitted && (strings.TrimSpace(entry.Text) != "" || len(entry.Parts) > 0)
	if !canReplay {
		key := "recovery.notice.switched_manual"
		if toolsUsed {
			key = "recovery.notice.switched_tools_used"
		}
		b.sendRecoveryNotice(context.Background(), chat, sessionCopy(locales, key, newEffectiveModel))
		return
	}

	b.sendRecoveryNotice(context.Background(), chat, sessionCopy(locales, "recovery.notice.switched_retrying", newEffectiveModel))
	if err := b.resubmitRecoveredTurn(ctx, chat, entry); err != nil {
		log.Warn().Err(err).Str("session", entry.SessionName).Str("model", newEffectiveModel).Msg("bridge: turn model recovery resubmit failed")
		b.sendRecoveryNotice(context.Background(), chat, sessionCopy(locales, "recovery.notice.resubmit_failed", newEffectiveModel))
	}
}

func (b *Bridge) switchSessionModelInPlace(ctx context.Context, targetID, sessionName, targetModel string) (string, error) {
	if b == nil || b.hub == nil {
		return "", errors.New("hub unavailable")
	}
	var matchedTarget *pb.Target
	var matchedSess *pb.Session
	for _, t := range b.hub.ListTargets() {
		if targetID != "" && t.GetId() != targetID {
			continue
		}
		for _, s := range t.GetSessions() {
			if s.GetName() == sessionName {
				matchedTarget, matchedSess = t, s
				break
			}
		}
		if matchedSess != nil {
			break
		}
	}
	if matchedSess == nil || matchedTarget == nil {
		return "", fmt.Errorf("session %q not found", sessionName)
	}
	if matchedSess.GetOwnerTaskId() != "" || matchedSess.GetAgentInstanceId() != "" {
		return "", errors.New("cannot switch model on an owner-held session")
	}
	replacement := &pb.RunRequest{
		AgentType:    matchedSess.GetAgentType(),
		AgentProfile: matchedSess.GetAgentProfile(),
		ExtraArgs:    agentprofile.ReplaceArgsModel(matchedSess.GetAgentType(), matchedSess.GetExtraArgs(), targetModel),
		Env:          append([]string(nil), matchedSess.GetEnv()...),
	}
	if config.SupportsAgentExecutable(replacement.AgentType) {
		replacement.AgentExecutable = config.GetAgentExecutable(replacement.AgentType)
	}
	restartCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := b.hub.Route(restartCtx, matchedTarget.GetId(), "Restart", &pb.RestartRequest{
		Name:            matchedSess.GetName(),
		Replacement:     replacement,
		PreserveHistory: true,
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return targetModel, nil
	}
	var out pb.RunResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return targetModel, nil
	}
	if out.Session != nil && strings.TrimSpace(out.Session.EffectiveModel) != "" {
		return strings.TrimSpace(out.Session.EffectiveModel), nil
	}
	return targetModel, nil
}

func (b *Bridge) recoveryBindingChat(entry turnRecoveryEntry) *LarkChat {
	if b != nil && b.db != nil && entry.BridgeID != "" && entry.RootMsgID != "" {
		if chat, err := b.db.FindByRootMsg(entry.BridgeID, entry.RootMsgID); err == nil && chat != nil {
			return chat
		}
	}
	return &LarkChat{
		BridgeId:       entry.BridgeID,
		ChatID:         entry.ChatID,
		RootMsgID:      entry.RootMsgID,
		ThreadID:       entry.ThreadID,
		OpenId:         entry.InitiatorOpenID,
		TargetID:       entry.TargetID,
		SessionName:    entry.SessionName,
		AgentProfile:   entry.AgentProfile,
		AgentType:      entry.AgentType,
		EffectiveModel: entry.EffectiveModel,
	}
}

func (b *Bridge) persistRecoveredSessionModel(entry turnRecoveryEntry, effectiveModel string) {
	if b == nil || b.db == nil || strings.TrimSpace(effectiveModel) == "" {
		return
	}
	if entry.BridgeID != "" && entry.TargetID != "" && entry.SessionName != "" {
		_ = b.db.RefreshSessionIdentity(entry.BridgeID, entry.TargetID, entry.SessionName, entry.AgentProfile, entry.AgentType, effectiveModel)
	}
	if entry.BridgeID != "" && entry.RootMsgID == singletonStreamKey(entry.BridgeID) {
		if bs, err := b.db.GetBotSession(entry.BridgeID); err == nil && bs != nil && bs.SessionName == entry.SessionName {
			bs.EffectiveModel = effectiveModel
			_ = b.db.SetBotSession(*bs)
		}
	}
}

func (b *Bridge) resubmitRecoveredTurn(ctx context.Context, chat *LarkChat, entry turnRecoveryEntry) error {
	if b == nil || b.hub == nil || chat == nil {
		return errors.New("bridge unavailable")
	}
	b.waitForStreamReady(ctx, chat.RootMsgID)
	b.setTurnActive(chat.RootMsgID, true)
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	routed, err := b.hub.Route(sendCtx, chat.TargetID, "Submit", &pb.SubmitRequest{
		Name:                     chat.SessionName,
		Text:                     entry.Text,
		Source:                   "lark",
		Async:                    true,
		Parts:                    entry.Parts,
		SystemInstructions:       entry.SystemInstructions,
		ManageSystemInstructions: true,
	})
	if err != nil {
		b.setTurnActive(chat.RootMsgID, false)
		return err
	}
	var response pb.SubmitResponse
	if err := json.Unmarshal(routed.Payload, &response); err != nil {
		b.setTurnActive(chat.RootMsgID, false)
		return fmt.Errorf("decode submit receipt: %w", err)
	}
	newTurnID := response.GetTurnId()
	b.recordTurnSenders(chat.RootMsgID, newTurnID, entry.Inputs)
	b.recordTurnSourceChat(chat.RootMsgID, newTurnID, chat.ChatID, chat.ThreadID)
	b.recordTurnRecovery(chat, newTurnID, entry.Text, entry.Parts, entry.SystemInstructions, entry.Inputs, false)
	if b.db != nil {
		_ = b.db.TouchChat(chat.BridgeId, chat.RootMsgID)
		if newTurnID != "" && len(entry.Inputs) > 0 {
			links := make([]TurnLink, 0, len(entry.Inputs))
			for _, input := range entry.Inputs {
				if strings.TrimSpace(input.MessageID) == "" {
					continue
				}
				links = append(links, TurnLink{
					BridgeID:    chat.BridgeId,
					RootMsgID:   chat.RootMsgID,
					MessageID:   input.MessageID,
					OpenID:      input.OpenID,
					TargetID:    chat.TargetID,
					SessionID:   response.GetSessionId(),
					SessionName: chat.SessionName,
					LaunchID:    response.GetLaunchId(),
					TurnID:      newTurnID,
					SubmittedAt: time.Now(),
				})
			}
			_ = b.db.RecordTurnLinks(links)
		}
	}
	return nil
}
