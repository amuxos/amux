package agentstream

import (
	"encoding/json"
	"fmt"
)

// ProducerEmission is the typed, identity-free seam between provider/ACP
// adapters and a Session-owned Timeline. Producers describe what happened and
// its native provenance; only the Session/Timeline may assign canonical
// SessionID, TurnID, Cursor, timestamp, or lifecycle ordering.
//
// Exactly one of Turn, Terminal, or Session must be set.
type ProducerEmission struct {
	Origin   Origin
	Turn     TurnPayload
	Terminal *Terminal
	Session  SessionPayload
}

type encodedProducerEmission struct {
	Version int             `json:"version"`
	Scope   string          `json:"scope"`
	Origin  Origin          `json:"origin"`
	Kind    Kind            `json:"kind"`
	Body    json.RawMessage `json:"body"`
}

// MarshalProducerEmission encodes the closed producer union for broker
// delivery. The envelope deliberately has no canonical identity fields.
func MarshalProducerEmission(emission ProducerEmission) ([]byte, error) {
	scope, body, err := normalizeProducerEmission(emission)
	if err != nil {
		return nil, err
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal producer emission body: %w", err)
	}
	return json.Marshal(encodedProducerEmission{
		Version: 1,
		Scope:   scope,
		Origin:  emission.Origin,
		Kind:    body.Kind(),
		Body:    bodyJSON,
	})
}

// UnmarshalProducerEmission decodes and validates a broker producer emission.
func UnmarshalProducerEmission(data []byte) (ProducerEmission, error) {
	var encoded encodedProducerEmission
	if err := strictUnmarshal(data, &encoded); err != nil {
		return ProducerEmission{}, fmt.Errorf("decode producer emission: %w", err)
	}
	if encoded.Version != 1 {
		return ProducerEmission{}, fmt.Errorf("%w: unsupported producer emission version %d", ErrInvalidPayload, encoded.Version)
	}
	body, err := unmarshalBody(encoded.Kind, encoded.Body)
	if err != nil {
		return ProducerEmission{}, err
	}
	emission := ProducerEmission{Origin: encoded.Origin}
	switch encoded.Scope {
	case "turn":
		payload, ok := body.(TurnPayload)
		if !ok {
			return ProducerEmission{}, fmt.Errorf("%w: %s is not turn-scoped", ErrInvalidPayload, encoded.Kind)
		}
		emission.Turn = payload
	case "terminal":
		finished, ok := body.(TurnFinished)
		if !ok {
			return ProducerEmission{}, fmt.Errorf("%w: %s is not terminal", ErrInvalidPayload, encoded.Kind)
		}
		terminal := finished.Terminal
		emission.Terminal = &terminal
	case "session":
		payload, ok := body.(SessionPayload)
		if !ok {
			return ProducerEmission{}, fmt.Errorf("%w: %s is not session-scoped", ErrInvalidPayload, encoded.Kind)
		}
		emission.Session = payload
	default:
		return ProducerEmission{}, fmt.Errorf("%w: unknown producer emission scope %q", ErrInvalidPayload, encoded.Scope)
	}
	if _, _, err := normalizeProducerEmission(emission); err != nil {
		return ProducerEmission{}, err
	}
	return emission, nil
}

func normalizeProducerEmission(emission ProducerEmission) (string, Body, error) {
	set := 0
	if emission.Turn != nil {
		set++
	}
	if emission.Terminal != nil {
		set++
	}
	if emission.Session != nil {
		set++
	}
	if set != 1 {
		return "", nil, fmt.Errorf("%w: producer emission must set exactly one scope", ErrInvalidPayload)
	}
	if emission.Turn != nil {
		body, err := normalizeTurnPayload(emission.Turn)
		return "turn", body, err
	}
	if emission.Terminal != nil {
		if !validOutcome(emission.Terminal.Outcome) {
			return "", nil, fmt.Errorf("%w: invalid terminal outcome", ErrInvalidPayload)
		}
		return "terminal", TurnFinished{Terminal: *emission.Terminal}, nil
	}
	body, err := normalizeSessionPayload(emission.Session)
	return "session", body, err
}

// CloneProducerEmission validates and deep-copies a producer value before it
// is retained across an asynchronous boundary.
func CloneProducerEmission(emission ProducerEmission) (ProducerEmission, error) {
	scope, body, err := normalizeProducerEmission(emission)
	if err != nil {
		return ProducerEmission{}, err
	}
	cloned := ProducerEmission{Origin: emission.Origin}
	switch scope {
	case "turn":
		cloned.Turn = body.(TurnPayload)
	case "terminal":
		terminal := body.(TurnFinished).Terminal
		cloned.Terminal = &terminal
	case "session":
		cloned.Session = body.(SessionPayload)
	}
	return cloned, nil
}

// IsTerminal reports whether the emission closes its correlated turn.
func (e ProducerEmission) IsTerminal() bool { return e.Terminal != nil }

// IsSessionScoped reports whether the emission is independent of a turn.
func (e ProducerEmission) IsSessionScoped() bool { return e.Session != nil }
