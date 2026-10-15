package session

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/agentstream"
)

var (
	ErrTurnSinkActive        = errors.New("turn event sink already active")
	ErrTurnReplayUnsupported = errors.New("turn replay is not supported")
)

type ImageInput struct {
	Data        []byte
	MIMEType    string
	DisplayName string
}

type InputPart struct {
	Text  string
	Image *ImageInput
}

type UserInput struct {
	Parts []InputPart
}

func TextUserInput(text string) UserInput {
	return UserInput{Parts: []InputPart{{Text: text}}}
}

func (i UserInput) Text() string {
	var parts []string
	for _, part := range i.Parts {
		if part.Image == nil && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (i UserInput) ImageCount() int {
	n := 0
	for _, part := range i.Parts {
		if part.Image != nil {
			n++
		}
	}
	return n
}

func (i UserInput) DisplayText() string {
	text := i.Text()
	if n := i.ImageCount(); n > 0 {
		if text != "" {
			text += "\n"
		}
		text += "[" + strconv.Itoa(n) + " images]"
	}
	return text
}

// TurnDispatchRequest carries one queue-admitted turn across a process seam.
// TurnToken is an opaque runtime correlation token: providers and brokers must
// return it unchanged with every emission and must not treat it as a native
// provider turn id.
type TurnDispatchRequest struct {
	TurnToken string
	Input     UserInput
	Control   string
}

// CorrelatedTurnRebinder restores only the event-delivery capability for a
// turn that was already dispatched before the runtime reconnected to a
// surviving broker. RebindTurn must never submit provider input or create a
// second native turn.
type CorrelatedTurnRebinder interface {
	RebindTurn(turnToken string, afterTransportSeq uint64, sink func(agentstream.ProducerEmission) bool) error
}

// EventSinks routes session-scoped observations to the process-wide sink and
// turn-scoped observations to the capability sink installed by DispatchTurn.
// It is shared by in-process drivers so a broker never has to infer ownership
// from whichever Session turn happens to be active when a frame arrives.
// EventSinks itself is intentionally not a temporal correlator: Proc
// implementations must satisfy the terminal-final/native-identity contract.
type EventSinks struct {
	mu        sync.Mutex
	global    func(agentstream.ProducerEmission) bool
	turn      func(agentstream.ProducerEmission) bool
	turnToken string
	pending   []agentstream.ProducerEmission
}

func (s *EventSinks) SetGlobal(fn func(agentstream.ProducerEmission) bool) {
	s.mu.Lock()
	if fn == nil {
		s.global = nil
		s.pending = nil
		s.mu.Unlock()
		return
	}
	s.global = nil
	for {
		pending := append([]agentstream.ProducerEmission(nil), s.pending...)
		s.pending = nil
		if len(pending) == 0 {
			s.global = fn
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		for _, ev := range pending {
			fn(ev)
		}
		s.mu.Lock()
	}
}

func (s *EventSinks) BeginTurn(fn func(agentstream.ProducerEmission) bool) error {
	return s.BeginTurnFor("", fn)
}

// BeginTurnFor installs the sink of the turn identified by token, so input
// aimed at that turn (TurnSteerRequest) can be checked against it.
func (s *EventSinks) BeginTurnFor(token string, fn func(agentstream.ProducerEmission) bool) error {
	if fn == nil {
		return errors.New("turn event sink is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != nil {
		return ErrTurnSinkActive
	}
	s.turn = fn
	s.turnToken = token
	return nil
}

// TurnActiveFor reports whether the turn identified by token is the accepted
// turn that has not emitted its terminal yet.
func (s *EventSinks) TurnActiveFor(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn != nil && token != "" && s.turnToken == token
}

func (s *EventSinks) AbortTurn() {
	s.mu.Lock()
	s.turn = nil
	s.turnToken = ""
	s.mu.Unlock()
}

func (s *EventSinks) Emit(ev agentstream.ProducerEmission) bool {
	cloned, err := agentstream.CloneProducerEmission(ev)
	if err != nil {
		return false
	}
	ev = cloned
	s.mu.Lock()
	var sink func(agentstream.ProducerEmission) bool
	if ev.IsSessionScoped() {
		sink = s.global
	} else if s.turn != nil {
		sink = s.turn
		if ev.IsTerminal() {
			s.turn = nil
			s.turnToken = ""
		}
	} else {
		sink = s.global
	}
	if sink == nil {
		s.pending = append(s.pending, ev)
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	return sink(ev)
}

// InputCapabilityProvider reports modalities confirmed for the current
// effective model. Empty or unknown capabilities are deliberately text-only.
type InputCapabilityProvider interface {
	InputCapabilities() (modalities []string, source string)
}

// Proc abstracts I/O to an agent process behind an ACP or structured broker.
type Proc interface {
	PID() int
	DispatchTurn(context.Context, TurnDispatchRequest, func(agentstream.ProducerEmission) bool) error
	Interrupt() error
	RespondPermission(permissionID, optionID string) error
	// RespondPermissionMulti responds to a multiSelect user-input permission
	// (item/tool/requestUserInput) with multiple selected option labels.
	// Implementations that don't back a structured provider may no-op.
	RespondPermissionMulti(permissionID string, optionIDs []string) error
	SetEventSink(func(agentstream.ProducerEmission) bool)
	Close() error
}

// ExecutionReconfigurer updates provider-native execution settings without
// replacing the process or conversation.
type ExecutionReconfigurer interface {
	ReconfigureExecution(model, reasoningEffort string) error
}
