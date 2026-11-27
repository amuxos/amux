package agentui

import (
	"strings"
	"sync"
)

const (
	// MaxProgressRunes bounds the chronological assistant narration retained
	// for in-progress UI panels.
	MaxProgressRunes = 6000
	// OmittedProgressMarker is a language-neutral omission marker inserted
	// when long-running turn narration exceeds MaxProgressRunes.
	OmittedProgressMarker = "…"
)

// ReplyProjection maintains the canonical separation between in-progress
// assistant narration, completed turn messages, steered-turn merge boundaries,
// and the final reconciled answer for a single running turn.
//
// Its zero value is ready for use and safe for concurrent steer notifications.
type ReplyProjection struct {
	mu                     sync.Mutex
	turnID                 string
	current                strings.Builder
	history                string
	last                   string
	turnMessages           []string
	steered                bool
	steerStart             int
	hasAuthoritativeResult bool
}

// NewReplyProjection creates an empty ReplyProjection.
func NewReplyProjection() *ReplyProjection {
	return &ReplyProjection{}
}

// BeginTurn resets all turn-scoped narration, steer markers, and result state
// for a newly started turn.
func (p *ReplyProjection) BeginTurn(turnID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resetLocked(turnID)
}

// Reset clears narration, steer markers, and authoritative result state.
func (p *ReplyProjection) Reset() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resetLocked("")
}

func (p *ReplyProjection) resetLocked(turnID string) {
	p.turnID = turnID
	p.current.Reset()
	p.history = ""
	p.last = ""
	p.turnMessages = nil
	p.steered = false
	p.steerStart = 0
	p.hasAuthoritativeResult = false
}

// TurnID returns the turn identity currently bound to this projection.
func (p *ReplyProjection) TurnID() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnID
}

// Append adds streaming text to the active assistant message and returns the
// updated bounded progress summary.
func (p *ReplyProjection) Append(delta string) string {
	if p == nil || delta == "" {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current.WriteString(delta)
	return p.summaryLocked()
}

// Replace overwrites the active assistant message text and returns the updated
// bounded progress summary.
func (p *ReplyProjection) Replace(text string) string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current.Reset()
	p.current.WriteString(text)
	return p.summaryLocked()
}

// Boundary completes the current assistant message, records it for steered-turn
// merging, and reports whether the completed message was non-empty.
func (p *ReplyProjection) Boundary() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if text := strings.TrimSpace(p.current.String()); text != "" {
		if p.last != "" {
			p.history = joinProgress(p.history, p.last)
			p.history = truncateProgress(p.history, MaxProgressRunes)
		}
		p.last = text
		p.current.Reset()
		p.turnMessages = append(p.turnMessages, text)
		return true
	}
	p.current.Reset()
	return false
}

// MarkSteered records the first steered input accepted during the active turn
// so that subsequent assistant messages are preserved in the merged final
// answer. It is safe to call from steer observer goroutines.
func (p *ReplyProjection) MarkSteered(turnID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.steered || (turnID != "" && p.turnID != "" && turnID != p.turnID) {
		return
	}
	p.steered = true
	p.steerStart = len(p.turnMessages)
}

// ReconcileResult removes an authoritative provider result only when it is an
// exact suffix of the most recent assistant message. Earlier progress is never
// searched or rewritten. An empty result is a no-op so LastMessage remains
// available to providers that do not emit a native result event.
func (p *ReplyProjection) ReconcileResult(result string) string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reconcileResultLocked(result)
}

func (p *ReplyProjection) reconcileResultLocked(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return p.summaryLocked()
	}
	if current := strings.TrimSpace(p.current.String()); current != "" {
		if kept, ok := trimExactResultSuffix(current, result); ok {
			p.current.Reset()
			p.current.WriteString(kept)
			return p.summaryLocked()
		}
	}
	if kept, ok := trimExactResultSuffix(p.last, result); ok {
		p.last = kept
	}
	return p.summaryLocked()
}

// ReconcileFinalAnswer records an authoritative native result, strips its
// duplicated suffix from in-progress narration, and produces the canonical
// final answer (merging all steered-turn messages when the turn was steered).
func (p *ReplyProjection) ReconcileFinalAnswer(result string) (progressBefore, progressAfter, finalAnswer string) {
	if p == nil {
		return "", "", result
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hasAuthoritativeResult = true
	if last := strings.TrimSpace(p.lastMessageLocked()); last != "" {
		p.turnMessages = append(p.turnMessages, last)
	}
	progressBefore = p.summaryLocked()
	progressAfter = p.reconcileResultLocked(result)
	finalAnswer = result
	if p.steered {
		finalAnswer = MergeSteeredFinalAnswer(p.turnMessages[p.steerStart:], result)
	}
	return progressBefore, progressAfter, finalAnswer
}

// FallbackFinalAnswer returns the synthesized final answer when a turn
// completes without an authoritative ResultObserved event. Returns ("", false)
// if an authoritative result was already reconciled.
func (p *ReplyProjection) FallbackFinalAnswer() (string, bool) {
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hasAuthoritativeResult {
		return "", false
	}
	last := p.lastMessageLocked()
	if p.steered {
		return MergeSteeredFinalAnswer(p.turnMessages[p.steerStart:], last), true
	}
	return last, true
}

// HasAuthoritativeResult reports whether an authoritative provider result has
// been reconciled for the current turn.
func (p *ReplyProjection) HasAuthoritativeResult() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hasAuthoritativeResult
}

// LastMessage returns the latest non-empty active or completed assistant message.
func (p *ReplyProjection) LastMessage() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastMessageLocked()
}

func (p *ReplyProjection) lastMessageLocked() string {
	if text := strings.TrimSpace(p.current.String()); text != "" {
		return text
	}
	return p.last
}

// HasCompletedMessage reports whether at least one assistant message boundary
// has completed in the current turn.
func (p *ReplyProjection) HasCompletedMessage() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(p.last) != ""
}

// Summary returns the bounded chronological progress narration for the current turn.
func (p *ReplyProjection) Summary() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.summaryLocked()
}

func (p *ReplyProjection) summaryLocked() string {
	return truncateProgress(
		joinProgress(joinProgress(p.history, p.last), p.current.String()),
		MaxProgressRunes,
	)
}

// TrimExactResultSuffix removes result when it is an exact suffix of message.
func TrimExactResultSuffix(message, result string) (string, bool) {
	return trimExactResultSuffix(message, result)
}

func trimExactResultSuffix(message, result string) (string, bool) {
	message = strings.TrimSpace(message)
	result = strings.TrimSpace(result)
	if message == "" || result == "" || !strings.HasSuffix(message, result) {
		return message, false
	}
	return strings.TrimSpace(strings.TrimSuffix(message, result)), true
}

func joinProgress(left, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "":
		return left
	default:
		return left + "\n\n" + right
	}
}

// TruncateProgress keeps both the initial context and the newest progress when
// a long-running turn exceeds the UI budget.
func TruncateProgress(text string, maxRunes int) string {
	return truncateProgress(text, maxRunes)
}

func truncateProgress(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	marker := []rune("\n\n" + OmittedProgressMarker + "\n\n")
	if maxRunes <= len(marker)+2 {
		return string(runes[len(runes)-maxRunes:])
	}
	contentBudget := maxRunes - len(marker)
	headRunes := contentBudget / 4
	tailRunes := contentBudget - headRunes
	return strings.TrimSpace(string(runes[:headRunes])) + string(marker) + strings.TrimSpace(string(runes[len(runes)-tailRunes:]))
}

// CollapseRepeatedText reduces text made of one block repeated back to back
// (e.g. "XX" or "X\n\nX") to a single block. A steered turn spanning multiple
// provider runs without per-message boundaries can otherwise concatenate
// repeated answers into one message.
func CollapseRepeatedText(text string) string {
	// Short units ("88", "haha") are legitimate answers, not glued repeats.
	const minUnit = 12
	n := len(text)
	for size := minUnit; size <= n/2; size++ {
		if n%size != 0 {
			continue
		}
		unit := text[:size]
		if strings.TrimSpace(unit) == "" {
			continue
		}
		if strings.Repeat(unit, n/size) == text {
			return strings.TrimSpace(unit)
		}
	}
	// Repeats separated by blank lines ("X\n\nX\n\nX").
	parts := strings.Split(text, "\n\n")
	if len(parts) > 1 {
		for size := 1; size <= len(parts)/2; size++ {
			if len(parts)%size != 0 {
				continue
			}
			unit := strings.Join(parts[:size], "\n\n")
			if len(strings.TrimSpace(unit)) < minUnit {
				continue
			}
			same := true
			for i := size; i < len(parts); i += size {
				if strings.Join(parts[i:i+size], "\n\n") != unit {
					same = false
					break
				}
			}
			if same {
				return strings.TrimSpace(unit)
			}
		}
	}
	return text
}

// MergeSteeredFinalAnswer builds the final answer of a turn that received
// steered input: every assistant message produced since the first steer, in
// order, followed by the provider result, with exact duplicates dropped.
func MergeSteeredFinalAnswer(messages []string, result string) string {
	var kept []string
	seen := map[string]struct{}{}
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if _, ok := seen[text]; ok {
			return
		}
		seen[text] = struct{}{}
		kept = append(kept, text)
	}
	result = CollapseRepeatedText(strings.TrimSpace(result))
	gluedResult := false
	for _, m := range messages {
		m = CollapseRepeatedText(strings.TrimSpace(m))
		if result != "" && m != result && strings.HasSuffix(m, result) {
			m = strings.TrimSpace(strings.TrimSuffix(m, result))
			gluedResult = true
		}
		add(m)
	}
	if result == "" {
		return strings.Join(kept, "\n\n")
	}
	matchesPrior := gluedResult
	for _, m := range kept {
		if m == result || strings.HasSuffix(m, result) || strings.Contains(result, m) {
			matchesPrior = true
			break
		}
	}
	if !matchesPrior && len(kept) > 0 {
		// Fresh comprehensive result: keep progress chatter out of the answer.
		return result
	}
	filtered := make([]string, 0, len(kept))
	seen = map[string]struct{}{}
	for _, m := range kept {
		if m == result || strings.Contains(result, m) {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		filtered = append(filtered, m)
	}
	filtered = append(filtered, result)
	return strings.Join(filtered, "\n\n")
}
