package bridge

import (
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentui"
)

// turnPlaceholderRefreshInterval paces the elapsed-time refresh of a waiting
// card. One CardKit update per 10s per card stays far below Feishu's
// per-message update limits.
const turnPlaceholderRefreshInterval = 10 * time.Second

// turnPlaceholder tracks the immediate-feedback state of one stream's current
// turn. Immediate feedback is always on (not gated by steer_foreground): the
// main reply card is posted the moment the user's turn starts and is then
// updated in place by streaming output and the final result.
//
// Lifecycle: Begin (turn started, card posted) -> End (first visible output)
// or Fail (error before output) -> Reset (turn finished / next turn).
type turnPlaceholder struct {
	mu          sync.Mutex
	shown       bool
	active      bool
	errText     string
	lastRefresh time.Time
	now         func() time.Time
}

func (p *turnPlaceholder) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *turnPlaceholder) Begin() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shown, p.active, p.errText = true, true, ""
	p.lastRefresh = p.clock()
}

// End drops the waiting line; it reports whether the line was visible.
func (p *turnPlaceholder) End() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	was := p.active
	p.active = false
	return was
}

// Fail records an error for a turn that produced no output yet. It reports
// whether the waiting card will show it (false: deliver the notice normally).
func (p *turnPlaceholder) Fail(text string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active || strings.TrimSpace(text) == "" {
		return false
	}
	p.errText = strings.TrimSpace(text)
	return true
}

func (p *turnPlaceholder) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shown, p.active, p.errText = false, false, ""
	p.lastRefresh = time.Time{}
}

// Shown reports whether this turn's card was posted as a placeholder.
func (p *turnPlaceholder) Shown() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shown
}

// State is what the card renderer needs.
func (p *turnPlaceholder) State() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active, p.errText
}

// RefreshDue reports (and records) a due elapsed-time refresh.
func (p *turnPlaceholder) RefreshDue(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active || p.errText != "" || now.Sub(p.lastRefresh) < turnPlaceholderRefreshInterval {
		return false
	}
	p.lastRefresh = now
	return true
}

// shouldShowTurnPlaceholder: background-task completion turns stay silent
// until the agent produces text; every other started turn gets immediate
// feedback.
func shouldShowTurnPlaceholder(source agentui.Source) bool {
	return source != agentui.SourceSubtask
}

// activityViewHasOutput reports whether a running activity view carries
// anything worth showing instead of the waiting line.
func activityViewHasOutput(view *agentui.ActivityView) bool {
	if view == nil {
		return false
	}
	return strings.TrimSpace(view.CurrentStep) != "" || len(view.Plan) > 0 || len(view.Tools) > 0
}
