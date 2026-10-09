package claude

import (
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/provider"
)

func TestClaudeTerminalClassifiesSidecarTurnDone(t *testing.T) {
	tests := []struct {
		name        string
		event       provider.Event
		wantOutcome agentstream.Outcome
		wantCode    string
	}{
		{
			name:        "interrupted turn is not an error",
			event:       provider.Event{Type: provider.EventTurnDone, State: &provider.StateChange{State: "interrupted", StopReason: "interrupted"}},
			wantOutcome: agentstream.OutcomeInterrupted,
			wantCode:    "interrupted",
		},
		{
			name: "failed turn keeps the provider error code",
			event: provider.Event{
				Type:  provider.EventTurnDone,
				State: &provider.StateChange{State: "failed", StopReason: "error"},
				Error: &provider.ProviderError{Code: "result_error", Message: "boom"},
			},
			wantOutcome: agentstream.OutcomeError,
			wantCode:    "result_error",
		},
		{
			name:        "completed turn succeeds",
			event:       provider.Event{Type: provider.EventTurnDone, State: &provider.StateChange{State: "turn_done", StopReason: "end_turn"}},
			wantOutcome: agentstream.OutcomeSuccess,
			wantCode:    "completed",
		},
		{
			name:        "cancelled turn stays cancelled",
			event:       provider.Event{Type: provider.EventTurnDone, State: &provider.StateChange{State: "turn_done", StopReason: "cancelled"}},
			wantOutcome: agentstream.OutcomeCancelled,
			wantCode:    "cancelled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := claudeTerminal(tt.event)
			if got.Outcome != tt.wantOutcome || got.Code != tt.wantCode {
				t.Fatalf("claudeTerminal = {%s %s}, want {%s %s}", got.Outcome, got.Code, tt.wantOutcome, tt.wantCode)
			}
		})
	}
}
