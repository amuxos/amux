package bridge

import "strings"

const maxAssistantProgressRunes = 6000

// Progress is bounded before a viewer's locale is known. Use a language-neutral
// omission marker instead of embedding product prose into Agent-owned content.
const omittedAssistantProgress = "…"

// assistantProgress keeps provider narrative updates out of the final answer
// while retaining a bounded, chronological user-facing progress history and
// one complete message for providers that do not emit a native result event.
type assistantProgress struct {
	current strings.Builder
	// history contains completed messages before last. Keeping the latest
	// completed message separate lets a later native result remove only its
	// duplicate suffix without touching earlier progress.
	history string
	last    string
}

func (p *assistantProgress) Append(delta string) string {
	if p == nil || delta == "" {
		return ""
	}
	p.current.WriteString(delta)
	return p.Summary()
}

func (p *assistantProgress) Replace(text string) string {
	if p == nil {
		return ""
	}
	p.current.Reset()
	p.current.WriteString(text)
	return p.Summary()
}

// Boundary completes the current assistant message and reports whether it was
// non-empty. agentui emits this after the provider closes a message and before
// any following message starts.
func (p *assistantProgress) Boundary() bool {
	if p == nil {
		return false
	}
	if text := strings.TrimSpace(p.current.String()); text != "" {
		if p.last != "" {
			p.history = joinAssistantProgress(p.history, p.last)
			p.history = truncateAssistantProgress(p.history, maxAssistantProgressRunes)
		}
		p.last = text
		p.current.Reset()
		return true
	}
	p.current.Reset()
	return false
}

// ReconcileResult removes an authoritative provider result only when it is an
// exact suffix of the most recent assistant message. Earlier progress is never
// searched or rewritten. An empty result is a no-op so LastMessage remains
// available to successful providers that do not emit a native result event.
func (p *assistantProgress) ReconcileResult(result string) string {
	if p == nil {
		return ""
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return p.Summary()
	}
	if current := strings.TrimSpace(p.current.String()); current != "" {
		if kept, ok := trimExactResultSuffix(current, result); ok {
			p.current.Reset()
			p.current.WriteString(kept)
			return p.Summary()
		}
	}
	if kept, ok := trimExactResultSuffix(p.last, result); ok {
		p.last = kept
	}
	return p.Summary()
}

func trimExactResultSuffix(message, result string) (string, bool) {
	message = strings.TrimSpace(message)
	result = strings.TrimSpace(result)
	if message == "" || result == "" || !strings.HasSuffix(message, result) {
		return message, false
	}
	return strings.TrimSpace(strings.TrimSuffix(message, result)), true
}

func (p *assistantProgress) LastMessage() string {
	if p == nil {
		return ""
	}
	if text := strings.TrimSpace(p.current.String()); text != "" {
		return text
	}
	return p.last
}

func (p *assistantProgress) HasCompletedMessage() bool {
	return p != nil && strings.TrimSpace(p.last) != ""
}

func (p *assistantProgress) Summary() string {
	if p == nil {
		return ""
	}
	return truncateAssistantProgress(
		joinAssistantProgress(joinAssistantProgress(p.history, p.last), p.current.String()),
		maxAssistantProgressRunes,
	)
}

func (p *assistantProgress) Reset() {
	if p == nil {
		return
	}
	p.current.Reset()
	p.history = ""
	p.last = ""
}

func joinAssistantProgress(left, right string) string {
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

// truncateAssistantProgress keeps both the initial context and the newest
// progress when a long-running turn exceeds the in-card activity budget.
func truncateAssistantProgress(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	marker := []rune("\n\n" + omittedAssistantProgress + "\n\n")
	if maxRunes <= len(marker)+2 {
		return string(runes[len(runes)-maxRunes:])
	}
	contentBudget := maxRunes - len(marker)
	headRunes := contentBudget / 4
	tailRunes := contentBudget - headRunes
	return strings.TrimSpace(string(runes[:headRunes])) + string(marker) + strings.TrimSpace(string(runes[len(runes)-tailRunes:]))
}

// mergeSteeredFinalAnswer builds the final answer of a turn that received
// steered input: every assistant message produced since the first steer, in
// order, followed by the provider result, with exact duplicates dropped (and
// the result dropped when a kept message already ends with it). A provider's
// native result only repeats the last message, so without this merge the
// answers to earlier steers would only survive in the collapsed process panel.
// collapseRepeatedText reduces text made of one block repeated back to back
// (e.g. "XX" or "X\n\nX") to a single block. A steered Claude turn spans
// several CLI runs that can each re-emit the same answer, and without message
// boundaries their text arrives as one concatenated message.
func collapseRepeatedText(text string) string {
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

func mergeSteeredFinalAnswer(messages []string, result string) string {
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
	result = collapseRepeatedText(strings.TrimSpace(result))
	gluedResult := false
	for _, m := range messages {
		m = collapseRepeatedText(strings.TrimSpace(m))
		// A runtime without per-message boundaries (Claude SDK) can glue the
		// last answer of a steered turn onto earlier text of the same message;
		// the result is appended below, so keep only what precedes it.
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
	kept = filtered
	add(result)
	return strings.Join(kept, "\n\n")
}
