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
// The Runtime reads the execution identity from its own Session registry.
type PersonalRequest struct {
	SessionID   string
	PrincipalID string
	Message     Message
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
