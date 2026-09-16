package agentstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type encodedEvent struct {
	Version   int             `json:"version"`
	SessionID string          `json:"session_id"`
	Cursor    Cursor          `json:"cursor"`
	Turn      *TurnRef        `json:"turn,omitempty"`
	At        string          `json:"at"`
	Origin    Origin          `json:"origin"`
	Kind      Kind            `json:"kind"`
	Body      json.RawMessage `json:"body"`
}

// MarshalEvent encodes the closed canonical event union for durable storage.
// The kind discriminator is owned by this package; callers cannot inject an
// unrecognized lifecycle payload into the journal.
func MarshalEvent(event Event) ([]byte, error) {
	body, err := validatedBody(event.Body, event.Turn != nil)
	if err != nil {
		return nil, err
	}
	body = durableBody(body)
	if event.SessionID == "" || event.Cursor.Epoch == "" || event.Cursor.Seq == 0 || event.At.IsZero() {
		return nil, ErrInvalidPayload
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal agent event body: %w", err)
	}
	turn := event.Turn
	if turn != nil {
		copy := *turn
		turn = &copy
	}
	return json.Marshal(encodedEvent{
		Version:   1,
		SessionID: event.SessionID,
		Cursor:    event.Cursor,
		Turn:      turn,
		At:        event.At.Format(timeLayout),
		Origin:    event.Origin,
		Kind:      body.Kind(),
		Body:      bodyJSON,
	})
}

func durableBody(body Body) Body {
	switch value := body.(type) {
	case ToolChanged:
		value.Tool.RawOutput = nil
		return value
	case PermissionRequested:
		value.Request.Details = nil
		if value.Request.ToolCall != nil {
			toolCall := *value.Request.ToolCall
			toolCall.RawInput = nil
			value.Request.ToolCall = &toolCall
		}
		return value
	default:
		return body
	}
}

const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

// UnmarshalEvent decodes and validates a durable canonical event. Unknown
// kinds fail closed so a partial replay can never masquerade as complete.
func UnmarshalEvent(data []byte) (Event, error) {
	var encoded encodedEvent
	if err := strictUnmarshal(data, &encoded); err != nil {
		return Event{}, fmt.Errorf("decode agent event: %w", err)
	}
	if encoded.Version != 1 {
		return Event{}, fmt.Errorf("%w: unsupported event version %d", ErrCorruptJournal, encoded.Version)
	}
	body, err := unmarshalBody(encoded.Kind, encoded.Body)
	if err != nil {
		return Event{}, err
	}
	at, err := time.Parse(timeLayout, encoded.At)
	if err != nil {
		return Event{}, fmt.Errorf("%w: invalid event time", ErrCorruptJournal)
	}
	event := Event{
		SessionID: encoded.SessionID,
		Cursor:    encoded.Cursor,
		Turn:      encoded.Turn,
		At:        at,
		Origin:    encoded.Origin,
		Body:      body,
	}
	if event.SessionID == "" || event.Cursor.Epoch == "" || event.Cursor.Seq == 0 {
		return Event{}, fmt.Errorf("%w: incomplete envelope", ErrCorruptJournal)
	}
	if _, err := validatedBody(event.Body, event.Turn != nil); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrCorruptJournal, err)
	}
	return event, nil
}

func validatedBody(body Body, hasTurn bool) (Body, error) {
	if body == nil {
		return nil, ErrInvalidPayload
	}
	switch value := body.(type) {
	case TurnStarted:
		if !hasTurn {
			return nil, ErrInvalidPayload
		}
		return value, nil
	case TurnFinished:
		if !hasTurn || !validOutcome(value.Terminal.Outcome) {
			return nil, ErrInvalidPayload
		}
		return value, nil
	case TurnPayload:
		if !hasTurn {
			return nil, ErrInvalidPayload
		}
		return normalizeTurnPayload(value)
	case SessionPayload:
		if hasTurn {
			return nil, ErrInvalidPayload
		}
		return normalizeSessionPayload(value)
	default:
		return nil, ErrInvalidPayload
	}
}

func unmarshalBody(kind Kind, raw json.RawMessage) (Body, error) {
	var target Body
	switch kind {
	case KindTurnStarted:
		target = &TurnStarted{}
	case KindAssistantDelta:
		target = &AssistantDelta{}
	case KindAssistantMessage:
		target = &AssistantMessage{}
	case KindThinkingDelta:
		target = &ThinkingDelta{}
	case KindPlanReplaced:
		target = &PlanReplaced{}
	case KindToolChanged:
		target = &ToolChanged{}
	case KindPermissionRequested:
		target = &PermissionRequested{}
	case KindPermissionResolved:
		target = &PermissionResolved{}
	case KindUsageObserved:
		target = &UsageObserved{}
	case KindResultObserved:
		target = &ResultObserved{}
	case KindDiagnostic:
		target = &Diagnostic{}
	case KindTurnFinished:
		target = &TurnFinished{}
	case KindSessionMetadataChanged:
		target = &SessionMetadataChanged{}
	case KindCommandsReplaced:
		target = &CommandsReplaced{}
	case KindSessionDiagnostic:
		target = &SessionDiagnostic{}
	case KindExtensionObserved:
		target = &ExtensionObserved{}
	default:
		return nil, fmt.Errorf("%w: unknown event kind %q", ErrCorruptJournal, kind)
	}
	if err := strictUnmarshal(raw, target); err != nil {
		return nil, fmt.Errorf("%w: decode %s body: %v", ErrCorruptJournal, kind, err)
	}
	switch value := target.(type) {
	case *TurnStarted:
		return *value, nil
	case *AssistantDelta:
		return *value, nil
	case *AssistantMessage:
		return *value, nil
	case *ThinkingDelta:
		return *value, nil
	case *PlanReplaced:
		return *value, nil
	case *ToolChanged:
		return *value, nil
	case *PermissionRequested:
		return *value, nil
	case *PermissionResolved:
		return *value, nil
	case *UsageObserved:
		return *value, nil
	case *ResultObserved:
		return *value, nil
	case *Diagnostic:
		return *value, nil
	case *TurnFinished:
		return *value, nil
	case *SessionMetadataChanged:
		return *value, nil
	case *CommandsReplaced:
		return *value, nil
	case *SessionDiagnostic:
		return *value, nil
	case *ExtensionObserved:
		return *value, nil
	default:
		return nil, ErrCorruptJournal
	}
}

func strictUnmarshal(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
