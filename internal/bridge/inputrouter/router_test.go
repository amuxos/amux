package inputrouter

import (
	"testing"

	"github.com/amuxos/amux/internal/bridge/command"
)

func TestDecideDispatchesByThreadState(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		snap Snapshot
		want OutcomeKind
	}{
		{
			name: "fresh thread",
			in:   Input{Text: "hello"},
			snap: Snapshot{ThreadState: ThreadFresh},
			want: OutcomeNoop,
		},
		{
			name: "active thread",
			in:   Input{Text: "@bot hello", Mention: Mention{AtBot: true}},
			snap: Snapshot{ThreadState: ThreadActive, BoundSessionAlive: true, CanUseBot: true},
			want: OutcomeSubmit,
		},
		{
			name: "terminal thread",
			in:   Input{Text: "hello"},
			snap: Snapshot{ThreadState: ThreadTerminal, CanUseBot: true},
			want: OutcomeTerminalNotice,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.in, tt.snap)
			if got.Kind != tt.want {
				t.Fatalf("Kind = %v, want %v", got.Kind, tt.want)
			}
		})
	}
}

func TestFreshThreadDecisions(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want Outcome
	}{
		{
			name: "amux command",
			in:   Input{Text: "/amux"},
			want: Outcome{Kind: OutcomeAmux, Command: command.CommandAmux},
		},
		{
			name: "help command",
			in:   Input{Text: "/help"},
			want: Outcome{Kind: OutcomeHelp, Command: command.CommandHelp},
		},
		{
			name: "short help command",
			in:   Input{Text: "/?"},
			want: Outcome{Kind: OutcomeHelp, Command: command.CommandHelp},
		},
		{
			name: "bind command",
			in:   Input{Text: "/bind runtime/session"},
			want: Outcome{Kind: OutcomeBind, Command: command.CommandBind, Text: "/bind runtime/session"},
		},
		{
			name: "run command",
			in:   Input{Text: "/run codex -n demo"},
			want: Outcome{Kind: OutcomeRun, Command: command.CommandRun, Text: "/run codex -n demo"},
		},
		{
			name: "skill command requires a bound session",
			in:   Input{Text: "/skill"},
			want: Outcome{Kind: OutcomeCommand, Command: command.CommandSkill, Text: "/skill"},
		},
		{
			name: "natural mention entry",
			in:   Input{Text: "@bot please review", Mention: Mention{AtBot: true}},
			want: Outcome{Kind: OutcomeRun, Text: "@bot please review"},
		},
		{
			name: "unmentioned fresh input stays silent",
			in:   Input{Text: "please review"},
			want: Outcome{Kind: OutcomeNoop},
		},
		{
			name: "inaccessible mentioned input is rejected",
			in:   Input{Text: "please review", Mention: Mention{AtBot: true}},
			want: Outcome{Kind: OutcomeDenied},
		},
		{
			name: "automated fresh input creates a task without parsing commands",
			in:   Input{Text: "/stop", Automated: true},
			want: Outcome{Kind: OutcomeRun, Text: "/stop"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canUseBot := tt.name != "inaccessible mentioned input is rejected"
			got := Decide(tt.in, Snapshot{ThreadState: ThreadFresh, CanUseBot: canUseBot})
			if got.Kind != tt.want.Kind || got.Command != tt.want.Command || got.Text != tt.want.Text {
				t.Fatalf("Decide() = %#v, want matching %#v", got, tt.want)
			}
		})
	}
}

func TestActiveThreadDecisions(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		snap Snapshot
		want Outcome
	}{
		{
			name: "ordinary input is submitted",
			in:   Input{Text: "please review"},
			snap: Snapshot{ThreadState: ThreadActive, BoundSessionAlive: true, CanUseBot: true},
			want: Outcome{Kind: OutcomeSubmit, Text: "please review", Reason: ReasonActiveSubmit},
		},
		{
			name: "shared discussion without mention becomes context",
			in:   Input{Text: "let us discuss the design"},
			snap: Snapshot{ThreadState: ThreadActive, WorkspaceMode: WorkspaceShared, BoundSessionAlive: true, CanUseBot: true},
			want: Outcome{Kind: OutcomeRecordHumanContext, Text: "let us discuss the design", Reason: ReasonActiveSharedContext},
		},
		{
			name: "shared mentioned input is submitted",
			in:   Input{Text: "please review", Mention: Mention{AtBot: true}},
			snap: Snapshot{ThreadState: ThreadActive, WorkspaceMode: WorkspaceShared, BoundSessionAlive: true, CanUseBot: true},
			want: Outcome{Kind: OutcomeSubmit, Text: "please review", Reason: ReasonActiveSubmit},
		},
		{
			name: "slash command is classified",
			in:   Input{Text: "/interrupt"},
			snap: Snapshot{ThreadState: ThreadActive, WorkspaceMode: WorkspaceShared, BoundSessionAlive: true, CanUseBot: true, CanControlBinding: true},
			want: Outcome{Kind: OutcomeCommand, Command: command.CommandInterrupt, Text: "/interrupt", Reason: ReasonActiveCommand},
		},
		{
			name: "binding control command requires control permission",
			in:   Input{Text: "/stop"},
			snap: Snapshot{ThreadState: ThreadActive, WorkspaceMode: WorkspaceShared, BoundSessionAlive: true, CanUseBot: true},
			want: Outcome{Kind: OutcomeDenied, Reason: ReasonBindingControlDenied},
		},
		{
			name: "natural help command is classified",
			in:   Input{Text: "帮助"},
			snap: Snapshot{ThreadState: ThreadActive, BoundSessionAlive: true, CanUseBot: true},
			want: Outcome{Kind: OutcomeCommand, Command: command.CommandHelp, Text: "帮助", Reason: ReasonActiveCommand},
		},
		{
			name: "provider command bypasses bridge commands",
			in:   Input{Text: "/help", AgentCommand: true},
			snap: Snapshot{ThreadState: ThreadActive, WorkspaceMode: WorkspaceShared, BoundSessionAlive: true, CanUseBot: true},
			want: Outcome{Kind: OutcomeSubmit, Text: "/help", Reason: ReasonActiveAgentCommand},
		},
		{
			name: "inaccessible input is rejected",
			in:   Input{Text: "/status"},
			snap: Snapshot{ThreadState: ThreadActive, BoundSessionAlive: true},
			want: Outcome{Kind: OutcomeDenied, Reason: ReasonBotAccessDenied},
		},
		{
			name: "reclaimed binding becomes terminal",
			in:   Input{Text: "continue"},
			snap: Snapshot{ThreadState: ThreadActive, CanUseBot: true},
			want: Outcome{Kind: OutcomeTerminalNotice, Reason: ReasonInactiveBinding},
		},
		{
			name: "automated input bypasses commands and mention rules",
			in:   Input{Text: "/stop", Automated: true},
			snap: Snapshot{ThreadState: ThreadActive, WorkspaceMode: WorkspaceShared, BoundSessionAlive: true},
			want: Outcome{Kind: OutcomeSubmit, Text: "/stop", Reason: ReasonActiveAutomatedSubmit},
		},
		{
			name: "automated input does not revive a reclaimed binding",
			in:   Input{Text: "new alert", Automated: true},
			snap: Snapshot{ThreadState: ThreadActive, WorkspaceMode: WorkspaceShared},
			want: Outcome{Kind: OutcomeTerminalNotice, Reason: ReasonInactiveBinding},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.in, tt.snap)
			if got.Kind != tt.want.Kind || got.Command != tt.want.Command || got.Text != tt.want.Text || got.Reason != tt.want.Reason {
				t.Fatalf("Decide() = %#v, want matching %#v", got, tt.want)
			}
		})
	}
}
