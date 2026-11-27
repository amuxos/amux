package feishumsg

import (
	"context"
	"errors"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/sessioncredential"
)

const Self = "self"
const PersonalRoute = "SendPersonalFeishuMessage"

var (
	ErrPersonalLogin = errors.New(Copy("personal_login"))
	ErrPersonalBot   = errors.New(Copy("personal_bot"))
	ErrEmptyResponse = errors.New(Copy("empty_response"))
)

// PersonalRequest is sent by the trusted Hub after authenticating the caller.
// For a Session send, the Runtime reads the identity from its Session registry.
// For a schedule result, the Hub supplies the frozen creator identity from the
// authorized schedule definition because its execution may have already exited.
type PersonalRequest struct {
	SessionID        string
	PrincipalID      string
	Message          Message
	ScheduleIdentity *principal.ExecutionIdentity `json:",omitempty"`
}

type PersonalResult struct {
	Result
	BotProfile string
}

// RunPersonalCommand makes nonempty input available at ./input.json in the
// command's private working directory for the duration of the call.
type RunPersonalCommand func(ctx context.Context, name string, args, env []string, input string) (sessioncredential.CommandOutput, error)

type PersonalSender func(context.Context, Message, RunPersonalCommand) (PersonalResult, error)

// IsPersonalSession requires a Feishu identity that runs as its original user.
func IsPersonalSession(identity principal.ExecutionIdentity, principalID string) bool {
	return identity.Source == principal.SourceFeishu && identity.IsolationBackend == principal.BackendLinuxDAC &&
		identity.Identified() && identity.OriginActorPrincipalID == principalID && identity.EffectivePrincipalID == principalID
}

// IsPersonalSchedule accepts only a creator's own frozen execution identity.
func IsPersonalSchedule(identity principal.ExecutionIdentity, principalID string) bool {
	if !identity.Identified() || identity.OriginActorPrincipalID != principalID || identity.EffectivePrincipalID != principalID || identity.Validate() != nil {
		return false
	}
	switch identity.Source {
	case principal.SourceFeishu:
		return identity.IsolationBackend == principal.BackendLinuxDAC
	case principal.SourceSchedule:
		return identity.CreationAuthorizationDecisionID != "" && identity.IsolationBackend != principal.BackendPassthrough
	default:
		return false
	}
}
