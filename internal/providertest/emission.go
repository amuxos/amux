// Package providertest provides canonical producer-emission assertions for
// native provider and broker protocol tests.
package providertest

import (
	"fmt"

	"github.com/amuxos/amux/internal/agentstream"
)

// Emission is a validated canonical assertion view. It retains the exact
// ProducerEmission (including scope and origin) while exposing the concrete
// closed-union body without rebuilding the retired provider/session event
// envelope.
type Emission struct {
	agentstream.ProducerEmission
	Kind       agentstream.Kind
	Body       *Body
	Text       string
	Plan       *agentstream.PlanReplaced
	Tool       *agentstream.Tool
	ToolPhase  agentstream.ToolPhase
	Permission *agentstream.PermissionRequest
	Usage      []byte
	Result     *agentstream.ResultObserved
	Diagnostic *agentstream.Diagnostic
	Metadata   *agentstream.SessionMetadataChanged
	Commands   []agentstream.Command
}

// Body is a convenience view over the concrete canonical union member. Type
// is the canonical agentstream.Kind string; all other fields point at canonical
// payload values.
type Body struct {
	Type              string
	Provider          string
	ID                string
	Seq               int64
	Text              string
	Tool              *agentstream.Tool
	ToolPhase         agentstream.ToolPhase
	Permission        *agentstream.PermissionRequest
	Usage             []byte
	Result            *agentstream.ResultObserved
	Error             *agentstream.Diagnostic
	Plan              *agentstream.PlanReplaced
	Terminal          *agentstream.Terminal
	AvailableCommands []agentstream.Command
}

// Capture validates and inspects a producer emission for test assertions.
// Invalid or multi-scope emissions panic because accepting one is always a
// test bug.
func Capture(emission agentstream.ProducerEmission) Emission {
	cloned, err := agentstream.CloneProducerEmission(emission)
	if err != nil {
		panic(fmt.Sprintf("capture producer emission: %v", err))
	}
	view := Emission{ProducerEmission: cloned}
	switch body := cloned.Turn.(type) {
	case agentstream.AssistantDelta:
		view.Kind, view.Text = body.Kind(), body.Text
	case agentstream.AssistantMessage:
		view.Kind, view.Text = body.Kind(), body.Text
	case agentstream.ThinkingDelta:
		view.Kind, view.Text = body.Kind(), body.Text
	case agentstream.PlanReplaced:
		view.Kind, view.Plan = body.Kind(), &body
	case agentstream.ToolChanged:
		view.Kind, view.Tool, view.ToolPhase = body.Kind(), &body.Tool, body.Phase
	case agentstream.PermissionRequested:
		view.Kind, view.Permission = body.Kind(), &body.Request
	case agentstream.PermissionResolved:
		view.Kind = body.Kind()
	case agentstream.UsageObserved:
		view.Kind, view.Usage = body.Kind(), body.Usage
	case agentstream.ResultObserved:
		view.Kind, view.Result, view.Text, view.Usage = body.Kind(), &body, body.Text, body.Usage
	case agentstream.Diagnostic:
		view.Kind, view.Text, view.Diagnostic = body.Kind(), body.Message, &body
	}
	if cloned.Terminal != nil {
		view.Kind = agentstream.KindTurnFinished
		if cloned.Terminal.Outcome == agentstream.OutcomeError {
			view.Diagnostic = &agentstream.Diagnostic{Severity: agentstream.SeverityError, Code: cloned.Terminal.Code, Message: cloned.Terminal.Message}
		}
	}
	switch body := cloned.Session.(type) {
	case agentstream.SessionMetadataChanged:
		view.Kind, view.Metadata = body.Kind(), &body
	case agentstream.CommandsReplaced:
		view.Kind, view.Commands = body.Kind(), body.Commands
	case agentstream.SessionDiagnostic:
		view.Kind, view.Text = body.Kind(), body.Message
		view.Diagnostic = &agentstream.Diagnostic{Severity: body.Severity, Code: body.Code, Message: body.Message}
	}
	view.Body = &Body{
		Type:              string(view.Kind),
		Provider:          cloned.Origin.Provider,
		ID:                cloned.Origin.NativeID,
		Seq:               cloned.Origin.NativeSeq,
		Text:              view.Text,
		Tool:              view.Tool,
		ToolPhase:         view.ToolPhase,
		Permission:        view.Permission,
		Usage:             view.Usage,
		Result:            view.Result,
		Error:             view.Diagnostic,
		Plan:              view.Plan,
		Terminal:          cloned.Terminal,
		AvailableCommands: view.Commands,
	}
	return view
}
