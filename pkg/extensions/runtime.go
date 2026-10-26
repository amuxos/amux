package extensions

import (
	"context"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/mux"
	"github.com/amuxos/amux/internal/provider"
	"github.com/amuxos/amux/internal/provider/codex"
	"github.com/amuxos/amux/internal/session"
)

type Adapter = adapter.Adapter
type Capabilities = adapter.Capabilities
type Protocol = adapter.Protocol
type ProtocolRegistration = adapter.ProtocolRegistration
type Surface = adapter.Surface
type Placement = adapter.Placement
type CwdSwitchStrategy = adapter.CwdSwitchStrategy
type AutoModelSelectionConfig = adapter.AutoModelSelectionConfig
type IsolatedHomeCache = adapter.IsolatedHomeCache
type WorkspaceTruster = adapter.WorkspaceTruster
type IsolatedHomeProvisioner = adapter.IsolatedHomeProvisioner
type IsolatedHomeReclaimer = adapter.IsolatedHomeReclaimer
type IsolatedHomeCredentialInspector = adapter.IsolatedHomeCredentialInspector
type AutoModelSelector = adapter.AutoModelSelector
type ACPExecutionConfigurator = adapter.ACPExecutionConfigurator
type StructuredLaunchOptions = mux.StructuredLaunchOptions
type StructuredProviderConfig = config.StructuredProviderConfig
type StructuredProviderFactory = mux.StructuredProviderFactory
type ResponsesChatOptions = mux.ResponsesChatOptions
type ResponsesChatCommand = mux.ResponsesChatCommand
type AppServerStartOptions = codex.StartOptions
type AppServerProviderRegistration = codex.ProviderRegistration
type AppServerProc = codex.Proc
type Proc = session.Proc
type ProducerEmission = agentstream.ProducerEmission
type TurnDispatchRequest = session.TurnDispatchRequest
type UserInput = session.UserInput
type InputPart = session.InputPart
type ImageInput = session.ImageInput
type SessionMetadataChanged = agentstream.SessionMetadataChanged
type CommandsReplaced = agentstream.CommandsReplaced
type GoalState = agentstream.GoalState
type GoalChanged = agentstream.GoalChanged
type GoalRequest = session.GoalRequest
type GoalController = session.GoalController
type GoalTurnRequest = session.GoalTurnRequest
type GoalTurnBinding = session.GoalTurnBinding
type GoalTurnAdmitter = session.GoalTurnAdmitter
type NativeGoalTurnProvider = session.NativeGoalTurnProvider
type GoalTurnReadyProvider = session.GoalTurnReadyProvider

var ErrGoalUnsupported = session.ErrGoalUnsupported

const (
	ProtocolACP            = adapter.ProtocolACP
	ProtocolCodexAppServer = adapter.ProtocolCodexAppServer
	InterruptCtrlC         = adapter.InterruptCtrlC
	InterruptACP           = adapter.InterruptACP
	CwdSwitchResume        = adapter.CwdSwitchResume
	CwdSwitchUnsupported   = adapter.CwdSwitchUnsupported
	SurfaceSkills          = adapter.SurfaceSkills
	SurfaceRules           = adapter.SurfaceRules
	SurfaceSystemPrompt    = adapter.SurfaceSystemPrompt
	SurfaceMCP             = adapter.SurfaceMCP
)

func RegisterAdapter(name string, factory func() Adapter) error {
	return adapter.Register(name, factory)
}
func RegisterProtocol(protocol Protocol, registration ProtocolRegistration) error {
	return adapter.RegisterProtocol(protocol, registration)
}
func GetAdapter(name string) (Adapter, error)                  { return adapter.Get(name) }
func AdapterNames() []string                                   { return adapter.Names() }
func AdapterLayout(name string) (map[Surface]Placement, error) { return adapter.Layout(name) }
func RegisterAppServerProvider(name string, registration AppServerProviderRegistration) error {
	return codex.RegisterProvider(name, registration)
}
func RegisterStructuredProvider(id string, factory StructuredProviderFactory) error {
	return mux.RegisterStructuredProvider(id, factory)
}
func StartAppServer(ctx context.Context, opts AppServerStartOptions) (*AppServerProc, <-chan []byte, error) {
	return codex.Start(ctx, opts)
}
func StartStructuredProc(opts StructuredLaunchOptions) (Proc, <-chan []byte, error) {
	return mux.StartStructuredProc(opts)
}
func ResolveExecutableFromEnvPATH(name string, env []string) (string, error) {
	return provider.ResolveExecutableFromEnvPATH(name, env)
}
func TextUserInput(text string) UserInput { return session.TextUserInput(text) }

var RegisterStructuredLaunchPreparation = mux.RegisterStructuredLaunchPreparation

func RegisterResponsesChatCommand(command ResponsesChatCommand) error {
	return mux.RegisterResponsesChatCommand(command)
}
