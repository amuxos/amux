package inputrouter

import (
	"strings"

	"github.com/amuxos/amux/internal/bridge/command"
)

type Input struct {
	Text    string
	Mention Mention

	// AgentCommand marks a slash command advertised by the bound agent. It must
	// reach the agent instead of being interpreted as a Bridge command.
	AgentCommand bool
	// Automated marks trusted non-human inputs such as group watch. They bypass
	// human mention and command routing while retaining thread lifecycle checks.
	Automated bool
}

type Mention struct {
	AtBot bool
}

type ThreadState int

const (
	ThreadFresh ThreadState = iota
	ThreadActive
	ThreadTerminal
)

type WorkspaceMode int

const (
	WorkspaceUnknown WorkspaceMode = iota
	WorkspaceOwnerOnly
	WorkspaceShared
)

type Snapshot struct {
	ThreadState       ThreadState
	WorkspaceMode     WorkspaceMode
	CanControlBinding bool
	BoundSessionAlive bool
	CanUseBot         bool
}

type OutcomeKind int

const (
	OutcomeNoop OutcomeKind = iota
	OutcomeAmux
	OutcomeHelp
	OutcomeBind
	OutcomeRun
	OutcomeSubmit
	OutcomeRecordHumanContext
	OutcomeTerminalNotice
	OutcomeCommand
	OutcomeDenied
)

// Reason identifies why Decide selected an outcome. It is intentionally not a
// string so callers cannot introduce new control-flow reasons by typo.
type Reason uint8

const (
	ReasonNone Reason = iota
	ReasonBotAccessDenied
	ReasonTerminalThread
	ReasonUnknownThreadState
	ReasonFreshAutomatedRun
	ReasonFreshAmux
	ReasonFreshHelp
	ReasonFreshBind
	ReasonFreshRun
	ReasonFreshUnboundCommand
	ReasonFreshNaturalRun
	ReasonFreshUnmentionedText
	ReasonInactiveBinding
	ReasonActiveAutomatedSubmit
	ReasonActiveAgentCommand
	ReasonBindingControlDenied
	ReasonActiveCommand
	ReasonActiveSharedContext
	ReasonActiveSubmit
)

type Outcome struct {
	Kind    OutcomeKind
	Command command.Command
	Text    string
	Reason  Reason
}

func Decide(in Input, snapshot Snapshot) Outcome {
	switch snapshot.ThreadState {
	case ThreadFresh:
		outcome := decideFreshThread(in, snapshot)
		if outcome.Kind != OutcomeNoop && !in.Automated && !snapshot.CanUseBot {
			return Outcome{Kind: OutcomeDenied, Reason: ReasonBotAccessDenied}
		}
		return outcome
	case ThreadActive:
		return decideActiveThread(in, snapshot)
	case ThreadTerminal:
		if !in.Automated && !snapshot.CanUseBot {
			return Outcome{Kind: OutcomeDenied, Reason: ReasonBotAccessDenied}
		}
		return Outcome{Kind: OutcomeTerminalNotice, Reason: ReasonTerminalThread}
	default:
		return Outcome{Kind: OutcomeNoop, Reason: ReasonUnknownThreadState}
	}
}

func decideFreshThread(in Input, _ Snapshot) Outcome {
	text := normalizeText(in.Text)
	if in.Automated {
		return Outcome{Kind: OutcomeRun, Text: text, Reason: ReasonFreshAutomatedRun}
	}
	switch cmd := command.Classify(command.ScopeFreshTopic, text); cmd {
	case command.CommandAmux:
		return Outcome{Kind: OutcomeAmux, Command: cmd, Reason: ReasonFreshAmux}
	case command.CommandHelp:
		return Outcome{Kind: OutcomeHelp, Command: cmd, Reason: ReasonFreshHelp}
	case command.CommandBind:
		return Outcome{Kind: OutcomeBind, Command: cmd, Text: text, Reason: ReasonFreshBind}
	case command.CommandRun:
		return Outcome{Kind: OutcomeRun, Command: cmd, Text: text, Reason: ReasonFreshRun}
	case command.CommandSkill:
		return Outcome{Kind: OutcomeCommand, Command: cmd, Text: text, Reason: ReasonFreshUnboundCommand}
	}
	if in.Mention.AtBot {
		return Outcome{Kind: OutcomeRun, Text: text, Reason: ReasonFreshNaturalRun}
	}
	return Outcome{Kind: OutcomeNoop, Reason: ReasonFreshUnmentionedText}
}

func decideActiveThread(in Input, snapshot Snapshot) Outcome {
	if !in.Automated && !snapshot.CanUseBot {
		return Outcome{Kind: OutcomeDenied, Reason: ReasonBotAccessDenied}
	}
	if !snapshot.BoundSessionAlive {
		return Outcome{Kind: OutcomeTerminalNotice, Reason: ReasonInactiveBinding}
	}
	if in.Automated {
		return Outcome{Kind: OutcomeSubmit, Text: normalizeText(in.Text), Reason: ReasonActiveAutomatedSubmit}
	}
	text := normalizeText(in.Text)
	if in.AgentCommand {
		return Outcome{Kind: OutcomeSubmit, Text: text, Reason: ReasonActiveAgentCommand}
	}
	if cmd := command.Classify(command.ScopeActiveTopic, text); cmd != command.CommandNone {
		if cmd.RequiresBindingControl() && !snapshot.CanControlBinding {
			return Outcome{Kind: OutcomeDenied, Reason: ReasonBindingControlDenied}
		}
		return Outcome{Kind: OutcomeCommand, Command: cmd, Text: text, Reason: ReasonActiveCommand}
	}
	if snapshot.WorkspaceMode == WorkspaceShared && !in.Mention.AtBot {
		return Outcome{Kind: OutcomeRecordHumanContext, Text: text, Reason: ReasonActiveSharedContext}
	}
	return Outcome{Kind: OutcomeSubmit, Text: text, Reason: ReasonActiveSubmit}
}

func normalizeText(text string) string {
	return strings.TrimSpace(text)
}
