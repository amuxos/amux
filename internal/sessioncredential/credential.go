package sessioncredential

import (
	"context"

	"github.com/amuxos/amux/internal/principal"
)

const Route = "IsolatedSessionCredential"

type Action string

const (
	Probe  Action = "probe"
	Begin  Action = "begin"
	Poll   Action = "poll"
	Reauth Action = "reauth"
)

type State string

const (
	Ready         State = "ready"
	LoginNeeded   State = "login_needed"
	SetupNeeded   State = "setup_needed"
	Pending       State = "pending"
	Expired       State = "expired"
	Denied        State = "denied"
	Mismatch      State = "mismatch"
	Unavailable   State = "unavailable"
	MissingTool   State = "missing_tool"
	AccountError  State = "account_error"
	Continuing    State = "continuing"
	DeliveryError State = "delivery_error"
)

type Request struct {
	Identity principal.ExecutionIdentity
	Actor    FeishuActor
	Provider string
	Action   Action
}

// FeishuActor contains identifiers for the human sender of the original event.
type FeishuActor struct {
	TenantKey string
	UserID    string
	UnionID   string
}

func (a FeishuActor) Identified() bool {
	return a.UnionID != "" || (a.TenantKey != "" && a.UserID != "")
}

type Result struct {
	State    State
	URL      string
	Provider string
}

// RunCommand is provided only to a registered distribution policy. The
// Runtime resolves and runs the command as the checked execution principal.
type CommandOutput struct {
	Stdout, Stderr []byte
	ExitCode       int
}

type RunCommand func(ctx context.Context, name string, args, env []string, observeLine func(string)) (CommandOutput, error)
type Policy func(ctx, lifetime context.Context, request Request, run RunCommand) Result
