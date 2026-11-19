package agentui

import (
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestPresenterTracksAndClearsProviderRetry(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	p := NewPresenterWithClock(func() time.Time { return now })

	p.Consume(Event{Kind: KindTurnStarted, Turn: &TurnEvent{ID: "turn-1", Seq: 1}})

	ev := FromAgentEvent(agentstream.Event{
		Turn: &agentstream.TurnRef{ID: "turn-1", Seq: 1},
		Body: agentstream.Diagnostic{
			Severity:  agentstream.SeverityWarning,
			Code:      "rate_limit_retry",
			Message:   "429 Too Many Requests - retrying (1/5)",
			Retryable: true,
		},
	})
	if len(ev) != 1 || ev[0].Kind != KindRetry {
		t.Fatalf("mapped retry event = %+v, want KindRetry", ev)
	}

	now = now.Add(5 * time.Second)
	actions := p.Consume(ev[0])
	if len(actions) != 1 || actions[0].Kind != ActionActivityChanged || !actions[0].Immediate {
		t.Fatalf("retry actions = %+v, want immediate ActionActivityChanged", actions)
	}
	if actions[0].Activity.RetryCount != 1 || actions[0].Activity.RetryCode != "rate_limit_retry" {
		t.Fatalf("retry view = %+v, want count=1 code=rate_limit_retry", actions[0].Activity)
	}

	// Second retry increments count.
	now = now.Add(5 * time.Second)
	actions = p.Consume(ev[0])
	if actions[0].Activity.RetryCount != 2 {
		t.Fatalf("second retry count = %d, want 2", actions[0].Activity.RetryCount)
	}

	// First token clears retry state.
	now = now.Add(2 * time.Second)
	p.Consume(Event{Kind: KindAssistantTextDelta, Turn: &TurnEvent{ID: "turn-1", Seq: 1}, Text: "hello"})
	view := p.EnsureActivity()
	if view.RetryCount != 0 || view.RetryCode != "" {
		t.Fatalf("after first token, retry state = count:%d code:%q, want cleared", view.RetryCount, view.RetryCode)
	}
}
