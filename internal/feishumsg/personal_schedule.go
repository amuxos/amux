package feishumsg

import (
	"context"
	"encoding/json"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/sessioncredential"
)

const (
	PersonalScheduleEventsRoute = "PersonalScheduleEvents"
	PersonalScheduleCardRoute   = "PersonalScheduleCard"
)

type PersonalScheduleIdentity struct {
	Execution    principal.ExecutionIdentity
	Actor        sessioncredential.FeishuActor
	SourceOpenID string
}

func (i PersonalScheduleIdentity) Valid() bool {
	return i.SourceOpenID != "" && i.Execution.Validate() == nil && i.Actor.Identified() &&
		IsPersonalSession(i.Execution, i.Execution.OriginActorPrincipalID)
}

type PersonalScheduleEvent struct {
	EventID   string
	MessageID string
	ChatID    string
	Account   PersonalScheduleAccount
	Token     string
	Action    map[string]any
}

type PersonalScheduleAccount struct {
	Profile string
	OpenID  string
}

// PersonalScheduleCard only supports delivery to, or update for, the verified
// owner of the personal app profile.
type PersonalScheduleCard struct {
	Binding PersonalScheduleIdentity
	Account PersonalScheduleAccount
	Card    json.RawMessage `json:",omitempty"`
	Text    string
	Token   string
	UUID    string
}

type RunPersonalEventCommand func(context.Context, string, []string, []string, func([]byte) error) error
type PersonalScheduleObserver func(context.Context, PersonalScheduleIdentity, RunPersonalCommand, RunPersonalEventCommand, func(PersonalScheduleEvent) error) error
type PersonalScheduleWriter func(context.Context, PersonalScheduleCard, RunPersonalCommand) (PersonalResult, error)
