package bridge

import "strings"

const maxAssistantProgressRunes = 6000

const omittedAssistantProgress = "…中间处理动态已省略…"

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
