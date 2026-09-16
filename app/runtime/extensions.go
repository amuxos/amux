package runtime

import (
	"context"
	"fmt"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/extensionstate"
	"github.com/spf13/cobra"
)

type EndpointSelector interface {
	Resolve(context.Context, string, string, []string) ([]string, string, error)
}

type ArchivedSession struct {
	AgentType       string
	Env             []string
	HasRunningAgent bool
	Isolated        bool
	ReclaimHome     func(context.Context) error
}

// Hooks are installed once by a distribution before starting the runtime.
type Hooks struct {
	AutoModelConfig        func(string) (adapter.AutoModelSelectionConfig, bool)
	StartStateWatchers     func(context.Context)
	CredentialLoginCommand func(string) string
	AppendUserBinPaths     func(string, []string) []string
	SessionArchived        func(ArchivedSession)
	NewEndpointSelector    func() EndpointSelector
	LogSelectedEndpoint    func(string, string, string)
	AddCommands            func(*cobra.Command)
}

var distributionHooks Hooks
var hooksRegistered bool

func RegisterHooks(hooks Hooks) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if hooksRegistered {
		return fmt.Errorf("runtime hooks already registered")
	}
	distributionHooks = hooks
	hooksRegistered = true
	return nil
}
