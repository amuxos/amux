package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/extensionstate"
)

// Extension supplies distribution-specific Bridge behavior. Install it before
// invoking Bridge operations. Nil hooks retain the public defaults.
// OnboardingOptions supplies distribution-specific authoring resources.
// PromptGuidance is Agent content, not product UI. ResolveUsername must only
// return an authenticated account name; a typed name is never proof of access.
type OnboardingOptions struct {
	GitURLTemplate  string
	SkillCatalogURL string
	PromptGuidance  string
	ResolveUsername func(context.Context) (string, error)
}

// TextCommandRequest carries verified sender identity for distribution-specific
// conversational control commands (for example quota-pool opt-in/out).
type TextCommandRequest struct {
	OpenID      string
	PrincipalID string
	DisplayName string
	Text        string
	IsOwner     bool
	InGroup     bool
	Locale      string
}

// ExperimentToggle declares a distribution-provided boolean toggle rendered
// inside the management Experiments card.
type ExperimentToggle struct {
	ID         string
	FieldName  string
	TitleZH    string
	TitleEN    string
	SummaryZH  string
	SummaryEN  string
	GetEnabled func() bool
	SetEnabled func(bool) error
}

// FallbackModelCandidate describes one same-engine model candidate shown on a
// turn recovery card.
type FallbackModelCandidate struct {
	Model string
	Label string
	Hint  string
}

// FallbackModelRequest carries context for distribution-specific fallback
// ranking (for example filtering depleted or overloaded models).
type FallbackModelRequest struct {
	AgentType    string
	AgentProfile string
	CurrentModel string
	TargetID     string
	SessionName  string
	OpenID       string
	PrincipalID  string
	Candidates   []FallbackModelCandidate
}

type Extension struct {
	Onboarding             OnboardingOptions
	SessionCredential      *CredentialAdmissionOptions
	ObserveConversations   func(context.Context, ConversationServices) (ConversationObserver, error)
	ExperimentEntries      func() []json.RawMessage
	ExperimentToggles      []ExperimentToggle
	ActivityPossiblyQueued func(string, *agentui.ActivityView) bool
	ProviderErrorNotice    func(string) string
	ReplyCardAgent         func(string) bool
	AgentTypeName          func(string) string
	CoordinationProfile    func(string) bool
	HandleTextCommand      func(context.Context, TextCommandRequest) (string, bool, error)
	RankFallbackModels     func(context.Context, FallbackModelRequest) ([]FallbackModelCandidate, error)
	// DefaultProfileReplacements maps retired default refs to replacements.
	// Targets cannot also be sources, so repeated migrations are idempotent.
	DefaultProfileReplacements map[string]string
	ConsoleSetup               func(context.Context, string, ConsoleOptions) ConsoleResult
}

type extensionRegistry struct {
	mu         sync.Mutex
	extension  Extension
	registered bool
	used       bool
}

var bridgeExtensions extensionRegistry

// RegisterExtension installs the process's Bridge customization exactly once.
func RegisterExtension(extension Extension) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	return bridgeExtensions.register(extension)
}

func (r *extensionRegistry) register(extension Extension) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registered {
		return fmt.Errorf("bridge extension already registered")
	}
	if r.used {
		return fmt.Errorf("bridge extension registration must precede first use")
	}
	// Clone and validate before publication; callers cannot mutate migration policy.
	replacements := make(map[string]string, len(extension.DefaultProfileReplacements))
	for old, next := range extension.DefaultProfileReplacements {
		if strings.TrimSpace(old) == "" || strings.TrimSpace(next) == "" {
			return fmt.Errorf("default profile replacements require non-empty references")
		}
		if _, chains := extension.DefaultProfileReplacements[next]; chains {
			return fmt.Errorf("default profile replacement target %q is also a source", next)
		}
		replacements[old] = next
	}
	extension.DefaultProfileReplacements = replacements
	extension.ExperimentToggles = append([]ExperimentToggle(nil), extension.ExperimentToggles...)
	r.extension = extension
	r.registered = true
	return nil
}

func (r *extensionRegistry) snapshot() Extension {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.used = true
	return r.extension
}

// RenderQRCode shares the official provisioning renderer with extensions.
func RenderQRCode(writer io.Writer, payload string) { renderQRCode(writer, payload) }
