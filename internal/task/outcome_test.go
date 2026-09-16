package task

import (
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestOutcomeFromTerminal(t *testing.T) {
	tests := []struct {
		name string
		in   agentstream.Terminal
		want TerminalOutcome
	}{
		{
			name: "success",
			in:   agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed", Message: "end_turn"},
			want: TerminalOutcome{Category: OutcomeSuccess, Code: "completed"},
		},
		{
			name: "cancelled",
			in:   agentstream.Terminal{Outcome: agentstream.OutcomeCancelled, Code: "cancelled", Message: "cancelled by user"},
			want: TerminalOutcome{Category: OutcomeCancelled, Code: "cancelled"},
		},
		{
			name: "interrupted",
			in:   agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "interrupted", Message: "preempted"},
			want: TerminalOutcome{Category: OutcomeInterrupted, Code: "interrupted"},
		},
		{
			name: "error",
			in:   agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "rate_limited", Message: "429 rate limit"},
			want: TerminalOutcome{Category: OutcomeError, Code: "rate_limited", RawError: "429 rate limit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcomeFromTerminal(tt.in); got != tt.want {
				t.Fatalf("outcomeFromTerminal(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}
