package distribution

import (
	"fmt"

	"github.com/amuxos/amux/internal/extensionstate"
)

// Options describes distribution defaults installed before application startup.
type Options struct {
	LegacyConsoleFallback bool
	CodexCompatibleLabel  string
	CodexCompatibleChoice string
	CodexCompatibleGroup  string
	SupportsResponsesChat bool
	LatestReleaseURL      string
	ContractReleaseURL    string
	BrandURL              string
	BrandFooterMarkdown   string
	LegacyBrandFooter     string
	DelegationProtocol    string
}

var installed bool

func Register(opts Options) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if installed {
		return fmt.Errorf("distribution defaults already registered")
	}
	if opts.CodexCompatibleLabel == "" || opts.DelegationProtocol == "" {
		return fmt.Errorf("distribution label and delegation protocol are required")
	}
	installed = true
	LegacyConsoleFallback = opts.LegacyConsoleFallback
	CodexCompatibleLabel = opts.CodexCompatibleLabel
	CodexCompatibleChoice = opts.CodexCompatibleChoice
	CodexCompatibleGroup = opts.CodexCompatibleGroup
	SupportsResponsesChat = opts.SupportsResponsesChat
	LatestReleaseURL = opts.LatestReleaseURL
	ContractReleaseURL = opts.ContractReleaseURL
	BrandURL = opts.BrandURL
	BrandFooterMarkdown = opts.BrandFooterMarkdown
	LegacyBrandFooter = opts.LegacyBrandFooter
	DelegationProtocol = opts.DelegationProtocol
	return nil
}
