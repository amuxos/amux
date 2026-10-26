package principal

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func (s *Store) ResolveActor(provider Provider, scope, subject, authenticationContextID, requestID string, source Source) (ActorContext, error) {
	p, err := s.ResolveAlias(provider, scope, subject)
	if err != nil {
		return ActorContext{}, err
	}
	return ActorContext{
		ActorPrincipalID:        p.ID,
		Source:                  source,
		Provider:                provider,
		Scope:                   strings.TrimSpace(scope),
		Subject:                 strings.TrimSpace(subject),
		AuthenticationContextID: authenticationContextID,
		RequestID:               requestID,
	}, nil
}

// FreezeExecution resolves runAs once and records the authorization decision.
// An empty runAs means self unless a parent identity is supplied, in which case
// MCP inheritance uses the parent's effective principal.
func (s *Store) FreezeExecution(actor ActorContext, runAs, parentExecutionID, executionStateRef string, parent *ExecutionIdentity) (ExecutionIdentity, error) {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	actorP, err := s.requireActive(actor.ActorPrincipalID)
	if err != nil {
		return ExecutionIdentity{}, err
	}
	effective := actorP
	delegationID := ""
	runAs = strings.TrimSpace(runAs)
	if parent != nil && runAs == "" {
		if err := parent.Validate(); err != nil {
			return ExecutionIdentity{}, fmt.Errorf("parent identity: %w", err)
		}
		if !parent.Identified() {
			return ExecutionIdentity{}, errors.New("legacy parent cannot create identified child")
		}
		effective, err = s.requireActive(parent.EffectivePrincipalID)
		if err != nil {
			return ExecutionIdentity{}, err
		}
	} else if runAs != "" {
		// A delegated actor already carries a frozen principal identity. Letting
		// it submit any run_as intent would turn a sandboxed process into an
		// identity-selection oracle, so the request is refused rather than
		// resolved even when it names the same principal.
		if actor.Delegated {
			err := fmt.Errorf("%w: agent credentials cannot use run_as", ErrUnauthorizedRunAs)
			_, _ = s.recordDecision(actor, actorP.ID, "", false, err.Error())
			return ExecutionIdentity{}, err
		}
		effective, err = s.resolveRunAs(actor, runAs)
		if err != nil {
			_, _ = s.recordDecision(actor, actorP.ID, "", false, err.Error())
			return ExecutionIdentity{}, err
		}
		if effective.ID != actorP.ID {
			delegationID, err = s.authorizeRunAs(actorP.ID, effective)
			if err != nil {
				_, _ = s.recordDecision(actor, effective.ID, "", false, err.Error())
				return ExecutionIdentity{}, err
			}
		}
	}
	decisionID, err := s.recordDecision(actor, effective.ID, delegationID, true, "allowed")
	if err != nil {
		return ExecutionIdentity{}, err
	}
	identity := ExecutionIdentity{
		IdentityMode:                    IdentityModeIdentified,
		OriginActorPrincipalID:          actorP.ID,
		EffectivePrincipalID:            effective.ID,
		RequestedRunAs:                  runAs,
		Source:                          actor.Source,
		ParentExecutionID:               parentExecutionID,
		DelegationID:                    delegationID,
		CreationAuthorizationDecisionID: decisionID,
		CreationPolicyRevision:          s.policyRevision(),
		PrincipalEnvironmentRef:         "principal:" + effective.ID,
		ExecutionStateRef:               executionStateRef,
	}
	return identity, identity.Validate()
}

func (s *Store) resolveRunAs(actor ActorContext, runAs string) (Principal, error) {
	if p, err := s.Get(runAs); err == nil {
		if p.Status != StatusActive {
			return Principal{}, ErrDisabled
		}
		return p, nil
	}
	// A run_as alias is always resolved within the authenticated provider/scope.
	return s.resolveAlias(actor.Provider, actor.Scope, runAs)
}

func (s *Store) authorizeRunAs(actorID string, effective Principal) (string, error) {
	if effective.Kind == KindService {
		var active int
		err := s.db.QueryRow(`SELECT active FROM service_principal_members
		  WHERE service_principal_id=? AND member_principal_id=?`, effective.ID, actorID).Scan(&active)
		if err == nil && active == 1 {
			return ServiceMembershipAuthorityID(effective.ID, actorID), nil
		}
	}
	var id string
	err := s.db.QueryRow(`SELECT delegation_id FROM principal_delegations
	  WHERE actor_principal_id=? AND effective_principal_id=? AND active=1`,
		actorID, effective.ID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if errors.Is(err, ErrNotFound) {
		return "", ErrUnauthorizedRunAs
	}
	return "", ErrUnauthorizedRunAs
}

func ServiceMembershipAuthorityID(serviceID, memberID string) string {
	return "service-member:" + strings.TrimSpace(serviceID) + ":" + strings.TrimSpace(memberID)
}

func (s *Store) AuthorizeObjectAction(actor ActorContext, identity ExecutionIdentity) error {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	if identity.IdentityMode == IdentityModeLegacyHost {
		if strings.TrimSpace(actor.ActorPrincipalID) != "" {
			return nil
		}
		return nil
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	if _, err := s.requireActive(actor.ActorPrincipalID); err != nil {
		return err
	}
	effective, err := s.requireActive(identity.EffectivePrincipalID)
	if err != nil {
		return err
	}
	if actor.ActorPrincipalID == identity.OriginActorPrincipalID ||
		actor.ActorPrincipalID == identity.EffectivePrincipalID {
		return nil
	}
	_, err = s.authorizeRunAs(actor.ActorPrincipalID, effective)
	return err
}

// AuthorizeScheduledExecution revalidates the authority that created a frozen
// schedule identity without resolving its original alias again. Alias
// rebinding cannot move the schedule, while principal disable, delegation
// revoke, or service membership revoke prevents future triggers.
func (s *Store) AuthorizeScheduledExecution(identity ExecutionIdentity) error {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	return s.authorizeScheduledExecution(identity)
}

func (s *Store) authorizeScheduledExecution(identity ExecutionIdentity) error {
	if identity.IdentityMode == IdentityModeLegacyHost {
		return nil
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	origin, err := s.requireActive(identity.OriginActorPrincipalID)
	if err != nil {
		return err
	}
	effective, err := s.requireActive(identity.EffectivePrincipalID)
	if err != nil {
		return err
	}
	if origin.ID == effective.ID {
		return nil
	}
	activeAuthority, err := s.authorizeRunAs(origin.ID, effective)
	if err != nil {
		return err
	}
	if activeAuthority != identity.DelegationID {
		return ErrUnauthorizedRunAs
	}
	return nil
}

// RefreshScheduledExecution creates the per-trigger execution identity from a
// frozen schedule authority. It deliberately preserves the resolved
// origin/effective principals and authority id instead of resolving the
// original alias again, while recording a fresh authorization decision and
// policy revision for this trigger.
func (s *Store) RefreshScheduledExecution(identity ExecutionIdentity, executionStateRef string) (ExecutionIdentity, error) {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	if err := s.authorizeScheduledExecution(identity); err != nil {
		return ExecutionIdentity{}, err
	}
	if identity.IdentityMode == IdentityModeLegacyHost {
		return identity, nil
	}
	actor := ActorContext{
		ActorPrincipalID: identity.OriginActorPrincipalID,
		Source:           SourceSchedule,
		Provider:         ProviderSystem,
		Scope:            "schedule",
		Subject:          identity.OriginActorPrincipalID,
		RequestID:        strings.TrimSpace(executionStateRef),
	}
	identity.Source = SourceSchedule
	identity.ParentExecutionID = ""
	identity.ExecutionStateRef = strings.TrimSpace(executionStateRef)
	identity.IsolationBackend = ""
	decisionID, err := s.recordDecision(
		actor, identity.EffectivePrincipalID, identity.DelegationID, true, "scheduled trigger allowed",
	)
	if err != nil {
		return ExecutionIdentity{}, err
	}
	identity.CreationAuthorizationDecisionID = decisionID
	identity.CreationPolicyRevision = s.policyRevision()
	return identity, identity.Validate()
}

func (s *Store) recordDecision(actor ActorContext, effectiveID, delegationID string, allowed bool, reason string) (string, error) {
	id := uuid.NewString()
	allowedInt := 0
	if allowed {
		allowedInt = 1
	}
	_, err := s.db.Exec(`INSERT INTO authorization_decisions
	  (decision_id, actor_principal_id, effective_principal_id, delegation_id,
	   policy_revision, request_id, allowed, reason, created_at)
	  VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, actor.ActorPrincipalID, effectiveID, delegationID, s.policyRevision(),
		actor.RequestID, allowedInt, reason, s.now().Unix())
	if err != nil {
		return "", fmt.Errorf("record authorization decision: %w", err)
	}
	return id, nil
}

type Enrollment struct {
	CredentialID string
	Token        string
	Provider     Provider
	Scope        string
	Subject      string
	PrincipalID  string
}

func (s *Store) CreateEnrollment(provider Provider, scope, subject, principalID string, rotate bool) (Enrollment, error) {
	scope = strings.TrimSpace(scope)
	subject = strings.TrimSpace(subject)
	if provider == "" || scope == "" || subject == "" {
		return Enrollment{}, errors.New("provider, scope and subject are required")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Enrollment{}, fmt.Errorf("generate enrollment token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	id := uuid.NewString()
	now := s.now().Unix()
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Enrollment{}, err
	}
	defer tx.Rollback()
	if _, err := requireActiveTx(tx, principalID); err != nil {
		return Enrollment{}, err
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM enrollment_credentials
	  WHERE provider=? AND scope=? AND subject=? AND active=1`,
		provider, scope, subject).Scan(&active); err != nil {
		return Enrollment{}, err
	}
	if active != 0 && !rotate {
		return Enrollment{}, fmt.Errorf("active enrollment already exists; explicit rotation is required")
	}
	if rotate {
		if _, err := tx.Exec(`UPDATE enrollment_credentials SET active=0, updated_at=?
		  WHERE provider=? AND scope=? AND subject=? AND active=1`,
			now, provider, scope, subject); err != nil {
			return Enrollment{}, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO enrollment_credentials
	  (credential_id, token_hash, provider, scope, subject, principal_id, active, created_at, updated_at)
	  VALUES(?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, hashEnrollmentToken(token), provider, scope, subject, principalID, now, now); err != nil {
		return Enrollment{}, fmt.Errorf("create enrollment: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO principal_aliases(provider, scope, subject, principal_id)
	  VALUES(?, ?, ?, ?)
	  ON CONFLICT(provider, scope, subject) DO UPDATE SET principal_id=excluded.principal_id`,
		provider, scope, subject, principalID); err != nil {
		return Enrollment{}, fmt.Errorf("bind enrollment alias: %w", err)
	}
	if err := bumpRevisionTx(tx); err != nil {
		return Enrollment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{CredentialID: id, Token: token, Provider: provider, Scope: scope, Subject: subject, PrincipalID: principalID}, nil
}

func (s *Store) AuthenticateEnrollment(token, requestID string) (ActorContext, error) {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	var id, provider, scope, subject, principalID string
	var active int
	err := s.db.QueryRow(`SELECT credential_id, provider, scope, subject, principal_id, active
	  FROM enrollment_credentials WHERE token_hash=?`, hashEnrollmentToken(strings.TrimSpace(token))).
		Scan(&id, &provider, &scope, &subject, &principalID, &active)
	if err != nil || active != 1 {
		return ActorContext{}, errors.New("invalid enrollment credential")
	}
	p, err := s.requireActive(principalID)
	if err != nil {
		return ActorContext{}, err
	}
	return ActorContext{
		ActorPrincipalID:        p.ID,
		Source:                  SourceCLI,
		Provider:                Provider(provider),
		Scope:                   scope,
		Subject:                 subject,
		AuthenticationContextID: id,
		RequestID:               requestID,
	}, nil
}

func FinalizeBackend(identity ExecutionIdentity, backend string) (ExecutionIdentity, error) {
	if backend != BackendPassthrough && backend != BackendLinuxDAC {
		return ExecutionIdentity{}, fmt.Errorf("invalid isolation backend %q", backend)
	}
	if identity.IsolationBackend != "" && identity.IsolationBackend != backend {
		return ExecutionIdentity{}, ErrIdentityMismatch
	}
	identity.IsolationBackend = backend
	return identity, identity.Validate()
}

// AuthorizeEffectivePrincipal checks present authority to use an already
// resolved principal. Unlike ownership of a historical object, a retained
// creator identity alone does not preserve revoked delegation or membership.
func (s *Store) AuthorizeEffectivePrincipal(actorID, effectiveID string) error {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	if _, err := s.requireActive(actorID); err != nil {
		return err
	}
	effective, err := s.requireActive(effectiveID)
	if err != nil {
		return err
	}
	if actorID == effectiveID {
		return nil
	}
	_, err = s.authorizeRunAs(actorID, effective)
	return err
}
