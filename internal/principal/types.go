// Package principal owns the control-plane identity model used by identified
// executions. Principal IDs are the only security keys; aliases and display
// names are lookup/display data and never become filesystem paths or OS UIDs.
package principal

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Kind string

const (
	KindHuman   Kind = "human"
	KindService Kind = "service"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type Provider string

const (
	ProviderCLI    Provider = "cli"
	ProviderFeishu Provider = "feishu"
	ProviderSystem Provider = "system"
)

type Source string

const (
	SourceCLI      Source = "cli"
	SourceFeishu   Source = "feishu"
	SourceMCP      Source = "mcp"
	SourceSchedule Source = "schedule"
	SourceSystem   Source = "system"
)

type IdentityMode string

const (
	IdentityModeIdentified IdentityMode = "identified"
	IdentityModeLegacyHost IdentityMode = "legacy-host"
)

const (
	BackendPassthrough = "passthrough"
	BackendLinuxDAC    = "linux-dac"
)

var (
	ErrNotFound          = errors.New("principal not found")
	ErrAliasNotFound     = errors.New("principal alias not found")
	ErrDisabled          = errors.New("principal disabled")
	ErrUnauthorizedRunAs = errors.New("run_as is not authorized")
	ErrIdentityMismatch  = errors.New("execution identity mismatch")
)

type Principal struct {
	ID                  string            `json:"principal_id"`
	Kind                Kind              `json:"kind"`
	DisplayName         string            `json:"display_name"`
	Status              Status            `json:"status"`
	EnvironmentTargetID string            `json:"environment_target_id,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

type Alias struct {
	Provider    Provider `json:"provider"`
	Scope       string   `json:"scope"`
	Subject     string   `json:"subject"`
	PrincipalID string   `json:"principal_id"`
}

type ActorContext struct {
	ActorPrincipalID        string   `json:"actor_principal_id"`
	Source                  Source   `json:"source"`
	Provider                Provider `json:"provider,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	Subject                 string   `json:"subject,omitempty"`
	AuthenticationContextID string   `json:"authentication_context_id,omitempty"`
	RequestID               string   `json:"request_id,omitempty"`
	// Delegated marks an actor that authenticated with an agent launch
	// credential rather than a human enrollment. Such an actor holds exactly
	// its own principal's authority and may never act as another principal.
	Delegated bool `json:"delegated,omitempty"`
}

// ExecutionIdentity is frozen at object creation. IsolationBackend is empty
// until the runtime selects an actual backend and may then be finalized once.
type ExecutionIdentity struct {
	IdentityMode                    IdentityMode `json:"identity_mode"`
	OriginActorPrincipalID          string       `json:"origin_actor_principal_id,omitempty"`
	EffectivePrincipalID            string       `json:"effective_principal_id,omitempty"`
	RequestedRunAs                  string       `json:"requested_run_as,omitempty"`
	Source                          Source       `json:"source,omitempty"`
	ParentExecutionID               string       `json:"parent_execution_id,omitempty"`
	DelegationID                    string       `json:"delegation_id,omitempty"`
	CreationAuthorizationDecisionID string       `json:"creation_authorization_decision_id,omitempty"`
	CreationPolicyRevision          string       `json:"creation_policy_revision,omitempty"`
	PrincipalEnvironmentRef         string       `json:"principal_environment_ref,omitempty"`
	ExecutionStateRef               string       `json:"execution_state_ref,omitempty"`
	IsolationBackend                string       `json:"isolation_backend,omitempty"`
}

func LegacyHostIdentity(source Source) ExecutionIdentity {
	return ExecutionIdentity{IdentityMode: IdentityModeLegacyHost, Source: source}
}

func (i ExecutionIdentity) Identified() bool {
	return i.IdentityMode == IdentityModeIdentified && strings.TrimSpace(i.EffectivePrincipalID) != ""
}

func (i ExecutionIdentity) Validate() error {
	switch i.IdentityMode {
	case IdentityModeLegacyHost:
		if i.EffectivePrincipalID != "" || i.OriginActorPrincipalID != "" || i.RequestedRunAs != "" {
			return fmt.Errorf("legacy-host identity cannot carry principal or run_as")
		}
	case IdentityModeIdentified:
		if _, err := uuid.Parse(i.OriginActorPrincipalID); err != nil {
			return fmt.Errorf("invalid origin actor principal id: %w", err)
		}
		if _, err := uuid.Parse(i.EffectivePrincipalID); err != nil {
			return fmt.Errorf("invalid effective principal id: %w", err)
		}
	default:
		return fmt.Errorf("invalid identity mode %q", i.IdentityMode)
	}
	switch i.IsolationBackend {
	case "", BackendPassthrough, BackendLinuxDAC:
	default:
		return fmt.Errorf("invalid isolation backend %q", i.IsolationBackend)
	}
	return nil
}

func validatePrincipal(p Principal) error {
	if _, err := uuid.Parse(p.ID); err != nil {
		return fmt.Errorf("invalid principal id: %w", err)
	}
	switch p.Kind {
	case KindHuman, KindService:
	default:
		return fmt.Errorf("invalid principal kind %q", p.Kind)
	}
	switch p.Status {
	case StatusActive, StatusDisabled:
	default:
		return fmt.Errorf("invalid principal status %q", p.Status)
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		return errors.New("principal display name is required")
	}
	return nil
}
