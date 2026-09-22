package cli

import (
	"context"
	"fmt"

	"github.com/amuxos/amux/internal/extensionstate"
	"github.com/spf13/cobra"
)

// Hooks customize distribution-specific command metadata and execution checks.
type Hooks struct {
	AuthenticationPreflight  func(context.Context, string, string) error
	AuthenticationProbeLabel func(string) string
	ParseConfigValue         func(string, string) (any, bool, error)
	// CredentialSetExamples appends examples using the supplied invocation path.
	CredentialSetExamples func(string) string
	// SetupCommands returns fresh optional-component commands. It must not
	// install anything while the command tree is being built.
	SetupCommands func() []*cobra.Command
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
		return fmt.Errorf("CLI hooks already registered")
	}
	distributionHooks = hooks
	hooksRegistered = true
	return nil
}
