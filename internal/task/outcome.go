package task

import "github.com/amuxos/amux/internal/agentstream"

// OutcomeCategory is the normalized terminal category used by TaskRun.
type OutcomeCategory string

const (
	OutcomeSuccess     OutcomeCategory = "success"
	OutcomeCancelled   OutcomeCategory = "cancelled"
	OutcomeInterrupted OutcomeCategory = "interrupted"
	OutcomeError       OutcomeCategory = "error"
	OutcomeUnknown     OutcomeCategory = "unknown"
)

// TerminalOutcome is Task's projection of the canonical Agent terminal.
// Provider-specific terminal inference belongs at the Timeline producer edge.
type TerminalOutcome struct {
	Category OutcomeCategory `json:"category"`
	Code     string          `json:"code,omitempty"`
	RawError string          `json:"raw_error,omitempty"`
}

func outcomeFromTerminal(terminal agentstream.Terminal) TerminalOutcome {
	out := TerminalOutcome{Code: terminal.Code}
	switch terminal.Outcome {
	case agentstream.OutcomeSuccess:
		out.Category = OutcomeSuccess
	case agentstream.OutcomeCancelled:
		out.Category = OutcomeCancelled
	case agentstream.OutcomeInterrupted:
		out.Category = OutcomeInterrupted
	case agentstream.OutcomeError:
		out.Category = OutcomeError
		out.RawError = terminal.Message
	default:
		out.Category = OutcomeUnknown
		out.RawError = terminal.Message
	}
	return out
}
