package extensions

import (
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/sessioncredential"
)

type CredentialAdmissionOptions = bridge.CredentialAdmissionOptions
type CredentialCardView = bridge.CredentialCardView
type IsolatedSessionCredentialRequest = sessioncredential.Request
type IsolatedSessionCredentialActor = sessioncredential.FeishuActor
type IsolatedSessionCredentialAction = sessioncredential.Action
type IsolatedSessionCredentialResult = sessioncredential.Result
type IsolatedSessionCredentialState = sessioncredential.State
type RunIsolatedCredentialCommand = sessioncredential.RunCommand
type IsolatedCredentialCommandOutput = sessioncredential.CommandOutput

const (
	IsolatedCredentialProbe         = sessioncredential.Probe
	IsolatedCredentialBegin         = sessioncredential.Begin
	IsolatedCredentialPoll          = sessioncredential.Poll
	IsolatedCredentialReauth        = sessioncredential.Reauth
	IsolatedCredentialReady         = sessioncredential.Ready
	IsolatedCredentialLoginNeeded   = sessioncredential.LoginNeeded
	IsolatedCredentialSetupNeeded   = sessioncredential.SetupNeeded
	IsolatedCredentialPending       = sessioncredential.Pending
	IsolatedCredentialExpired       = sessioncredential.Expired
	IsolatedCredentialDenied        = sessioncredential.Denied
	IsolatedCredentialMismatch      = sessioncredential.Mismatch
	IsolatedCredentialUnavailable   = sessioncredential.Unavailable
	IsolatedCredentialMissingTool   = sessioncredential.MissingTool
	IsolatedCredentialAccountError  = sessioncredential.AccountError
	IsolatedCredentialContinuing    = sessioncredential.Continuing
	IsolatedCredentialDeliveryError = sessioncredential.DeliveryError
)
