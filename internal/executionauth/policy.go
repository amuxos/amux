package executionauth

import (
	"strings"

	"github.com/amuxos/amux/internal/principal"
)

type DenialKind string

const (
	DenialUnauthenticated DenialKind = "unauthenticated"
	DenialForbidden       DenialKind = "forbidden"
	DenialInvalidArgument DenialKind = "invalid_argument"
)

type Reason string

const (
	ReasonActorRequired              Reason = "actor_required"
	ReasonPrincipalMismatch          Reason = "principal_mismatch"
	ReasonOutsideDelegatedReach      Reason = "outside_delegated_reach"
	ReasonEnrollmentRequired         Reason = "enrollment_required"
	ReasonScheduleOwnerMismatch      Reason = "schedule_owner_mismatch"
	ReasonScheduleOwnerRequired      Reason = "schedule_owner_required"
	ReasonRunAsRequiresActor         Reason = "run_as_requires_actor"
	ReasonIdentityResolution         Reason = "identity_resolution"
	ReasonIdentifiedScheduleRequired Reason = "identified_schedule_required"
	ReasonParentClaimDenied          Reason = "parent_claim_denied"
	ReasonPersistentFollowupDenied   Reason = "persistent_followup_denied"
	ReasonPersistentRunAsChange      Reason = "persistent_run_as_change"
	ReasonInvalidSavedIdentity       Reason = "invalid_saved_identity"
	ReasonScheduledAuthorityInactive Reason = "scheduled_authority_inactive"
)

type Decision struct {
	Allowed bool
	Kind    DenialKind
	Reason  Reason
	Err     error
}

type Policy struct {
	principals *principal.Store
}

func New(principals *principal.Store) *Policy {
	return &Policy{principals: principals}
}

type ExecutionAccessRequest struct {
	Actor           *principal.ActorContext
	Identity        principal.ExecutionIdentity
	ParentSessionID string
	Sessions        []SessionFacts
	IsolationActive bool
}

type SessionFacts struct {
	ID       string
	TargetID string
	Name     string
	LaunchID string
	Identity principal.ExecutionIdentity
}

func (p *Policy) ExecutionAccess(req ExecutionAccessRequest) Decision {
	if req.Identity.IdentityMode == "" || req.Identity.IdentityMode == principal.IdentityModeLegacyHost ||
		p == nil || p.principals == nil || !req.IsolationActive {
		return Decision{Allowed: true}
	}
	actor := req.Actor
	if actor == nil {
		actor = actorFromParentSession(req.ParentSessionID, req.Sessions)
	}
	if actor == nil {
		return Decision{Kind: DenialUnauthenticated, Reason: ReasonActorRequired}
	}
	if err := p.principals.AuthorizeObjectAction(*actor, req.Identity); err != nil {
		return Decision{Kind: DenialForbidden, Reason: ReasonPrincipalMismatch, Err: err}
	}
	return Decision{Allowed: true}
}

func actorFromParentSession(parentSessionID string, sessions []SessionFacts) *principal.ActorContext {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return nil
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.ID) != parentSessionID || !session.Identity.Identified() {
			continue
		}
		return &principal.ActorContext{
			ActorPrincipalID: session.Identity.EffectivePrincipalID,
			Source:           principal.SourceMCP,
			Provider:         principal.ProviderSystem,
			Scope:            "parent:" + parentSessionID,
			Subject:          session.Identity.EffectivePrincipalID,
		}
	}
	return nil
}

type SessionAccessRequest struct {
	Actor           *principal.ActorContext
	Target          SessionFacts
	Sessions        []SessionFacts
	IsolationActive bool
}

func (p *Policy) SessionAccess(req SessionAccessRequest) Decision {
	if req.Target.Identity.IdentityMode == "" || req.Target.Identity.IdentityMode == principal.IdentityModeLegacyHost ||
		p == nil || p.principals == nil || !req.IsolationActive {
		return Decision{Allowed: true}
	}
	if req.Actor == nil {
		return Decision{Kind: DenialUnauthenticated, Reason: ReasonActorRequired}
	}
	if err := p.principals.AuthorizeObjectAction(*req.Actor, req.Target.Identity); err != nil {
		return Decision{Kind: DenialForbidden, Reason: ReasonPrincipalMismatch, Err: err}
	}
	if !delegatedPrincipalAccess(req.Actor, req.Target.Identity) {
		return Decision{Kind: DenialForbidden, Reason: ReasonPrincipalMismatch}
	}
	return Decision{Allowed: true}
}

// SessionListAccess preserves the fleet inventory contract: enrollment and
// anonymous callers can discover every session. In an isolated fleet, a
// delegated actor sees every session owned by its principal, including sibling
// sessions, but not sessions owned by another principal.
func (p *Policy) SessionListAccess(req SessionAccessRequest) Decision {
	if !req.IsolationActive || req.Actor == nil || !req.Actor.Delegated {
		return Decision{Allowed: true}
	}
	if delegatedPrincipalAccess(req.Actor, req.Target.Identity) {
		return Decision{Allowed: true}
	}
	return Decision{Kind: DenialForbidden, Reason: ReasonPrincipalMismatch}
}

func delegatedPrincipalAccess(actor *principal.ActorContext, identity principal.ExecutionIdentity) bool {
	if actor == nil || !actor.Delegated || !identity.Identified() {
		return true
	}
	principalID := strings.TrimSpace(actor.ActorPrincipalID)
	return principalID != "" &&
		(principalID == identity.OriginActorPrincipalID || principalID == identity.EffectivePrincipalID)
}

func (p *Policy) CallerSessionAccess(actor *principal.ActorContext, target SessionFacts, sessions []SessionFacts) Decision {
	if actor == nil || !actor.Delegated {
		return Decision{Kind: DenialForbidden, Reason: ReasonOutsideDelegatedReach}
	}
	caller, ok := callerSession(actor.Subject, sessions)
	if ok && target.ID == caller.ID {
		return Decision{Allowed: true}
	}
	return Decision{Kind: DenialForbidden, Reason: ReasonOutsideDelegatedReach}
}

func callerSession(subject string, sessions []SessionFacts) (SessionFacts, bool) {
	for _, session := range sessions {
		if sessionMatchesLaunchSubject(session, subject) {
			return session, true
		}
	}
	return SessionFacts{}, false
}

func sessionMatchesLaunchSubject(session SessionFacts, subject string) bool {
	if subject == "" {
		return false
	}
	if session.LaunchID != "" && session.LaunchID == subject {
		return true
	}
	if rest, ok := strings.CutPrefix(subject, "relaunch:"); ok {
		target, name, found := strings.Cut(rest, ":")
		return found && target == session.TargetID && name == session.Name
	}
	return false
}

func (p *Policy) PruneAccess(actor *principal.ActorContext, isolationActive bool) Decision {
	if p == nil || p.principals == nil {
		return Decision{Allowed: true}
	}
	if actor == nil {
		return Decision{Kind: DenialUnauthenticated, Reason: ReasonActorRequired}
	}
	if actor.Delegated && isolationActive {
		return Decision{Kind: DenialForbidden, Reason: ReasonEnrollmentRequired}
	}
	return Decision{Allowed: true}
}

func (p *Policy) ScheduleAccess(actor *principal.ActorContext, ownerPrincipalID string, isolationActive bool) Decision {
	if p == nil || p.principals == nil || !isolationActive {
		return Decision{Allowed: true}
	}
	ownerPrincipalID = strings.TrimSpace(ownerPrincipalID)
	if ownerPrincipalID == "" || (actor != nil && strings.TrimSpace(actor.ActorPrincipalID) == ownerPrincipalID) {
		return Decision{Allowed: true}
	}
	return Decision{Kind: DenialForbidden, Reason: ReasonScheduleOwnerMismatch}
}

func (p *Policy) ScheduleTriggerAccess(ownerPrincipalID string, targetIdentity principal.ExecutionIdentity, isolationActive bool) Decision {
	if targetIdentity.IdentityMode == "" || targetIdentity.IdentityMode == principal.IdentityModeLegacyHost ||
		p == nil || p.principals == nil || !isolationActive {
		return Decision{Allowed: true}
	}
	ownerPrincipalID = strings.TrimSpace(ownerPrincipalID)
	if ownerPrincipalID == "" {
		return Decision{Kind: DenialForbidden, Reason: ReasonScheduleOwnerRequired}
	}
	actor := principal.ActorContext{
		ActorPrincipalID: ownerPrincipalID,
		Source:           principal.SourceSchedule,
		Provider:         principal.ProviderSystem,
		Scope:            "schedule",
		Subject:          ownerPrincipalID,
	}
	if err := p.principals.AuthorizeObjectAction(actor, targetIdentity); err != nil {
		return Decision{Kind: DenialForbidden, Reason: ReasonPrincipalMismatch, Err: err}
	}
	return Decision{Allowed: true}
}

type FreezeSessionRequest struct {
	Actor             *principal.ActorContext
	Source            principal.Source
	RunAs             string
	ExecutionStateRef string
	Sessions          []SessionFacts
}

type IdentityResult struct {
	Identity principal.ExecutionIdentity
	Decision Decision
}

func (p *Policy) FreezeSessionIdentity(req FreezeSessionRequest) IdentityResult {
	if req.Actor == nil {
		if strings.TrimSpace(req.RunAs) != "" {
			return IdentityResult{Decision: Decision{
				Kind: DenialUnauthenticated, Reason: ReasonRunAsRequiresActor,
			}}
		}
		return IdentityResult{
			Identity: principal.LegacyHostIdentity(req.Source),
			Decision: Decision{Allowed: true},
		}
	}
	if p == nil || p.principals == nil {
		return IdentityResult{
			Identity: principal.LegacyHostIdentity(req.Source),
			Decision: Decision{Allowed: true},
		}
	}
	parentExecutionID := ""
	if req.Actor.Delegated {
		if caller, ok := callerSession(req.Actor.Subject, req.Sessions); ok {
			parentExecutionID = caller.LaunchID
		}
	}
	identity, err := p.principals.FreezeExecution(
		*req.Actor, req.RunAs, parentExecutionID, req.ExecutionStateRef, nil,
	)
	if err != nil {
		return IdentityResult{Decision: Decision{
			Kind: DenialForbidden, Reason: ReasonIdentityResolution, Err: err,
		}}
	}
	return IdentityResult{Identity: identity, Decision: Decision{Allowed: true}}
}

type FreezeScheduleRequest struct {
	Actor             *principal.ActorContext
	RunAs             string
	ExecutionStateRef string
	IsolationActive   bool
}

func (p *Policy) FreezeScheduleIdentity(req FreezeScheduleRequest) IdentityResult {
	if req.Actor == nil {
		if strings.TrimSpace(req.RunAs) != "" {
			return IdentityResult{Decision: Decision{
				Kind: DenialUnauthenticated, Reason: ReasonRunAsRequiresActor,
			}}
		}
		if req.IsolationActive {
			return IdentityResult{Decision: Decision{
				Kind: DenialUnauthenticated, Reason: ReasonIdentifiedScheduleRequired,
			}}
		}
		return IdentityResult{
			Identity: principal.LegacyHostIdentity(principal.SourceSchedule),
			Decision: Decision{Allowed: true},
		}
	}
	if p == nil || p.principals == nil {
		return IdentityResult{
			Identity: principal.LegacyHostIdentity(principal.SourceSchedule),
			Decision: Decision{Allowed: true},
		}
	}
	actor := *req.Actor
	actor.Source = principal.SourceSchedule
	identity, err := p.principals.FreezeExecution(
		actor, req.RunAs, "", req.ExecutionStateRef, nil,
	)
	if err != nil {
		return IdentityResult{Decision: Decision{
			Kind: DenialForbidden, Reason: ReasonIdentityResolution, Err: err,
		}}
	}
	return IdentityResult{Identity: identity, Decision: Decision{Allowed: true}}
}

type FreezeTaskRequest struct {
	Actor             *principal.ActorContext
	RunAs             string
	ParentTaskID      string
	ExecutionStateRef string
	Parent            *SessionFacts
	SavedIdentity     *principal.ExecutionIdentity
	ScheduledIdentity *principal.ExecutionIdentity
	IsolationActive   bool
}

func (p *Policy) FreezeTaskIdentity(req FreezeTaskRequest) IdentityResult {
	if req.ScheduledIdentity != nil {
		identity := *req.ScheduledIdentity
		if err := identity.Validate(); err != nil {
			return IdentityResult{Decision: Decision{
				Kind: DenialInvalidArgument, Reason: ReasonInvalidSavedIdentity, Err: err,
			}}
		}
		if identity.Identified() && p != nil && p.principals != nil {
			refreshed, err := p.principals.RefreshScheduledExecution(identity, req.ExecutionStateRef)
			if err != nil {
				return IdentityResult{Decision: Decision{
					Kind: DenialForbidden, Reason: ReasonScheduledAuthorityInactive, Err: err,
				}}
			}
			identity = refreshed
		}
		return IdentityResult{Identity: identity, Decision: Decision{Allowed: true}}
	}

	var parentActor *principal.ActorContext
	if req.Parent != nil && req.Parent.Identity.Identified() {
		if req.Actor == nil && strings.TrimSpace(req.RunAs) != "" {
			return IdentityResult{Decision: Decision{
				Kind: DenialUnauthenticated, Reason: ReasonRunAsRequiresActor,
			}}
		}
		if req.IsolationActive && p != nil && p.principals != nil {
			if req.Actor == nil {
				return IdentityResult{Decision: Decision{
					Kind: DenialUnauthenticated, Reason: ReasonActorRequired,
				}}
			}
			if err := p.principals.AuthorizeObjectAction(*req.Actor, req.Parent.Identity); err != nil {
				return IdentityResult{Decision: Decision{
					Kind: DenialForbidden, Reason: ReasonParentClaimDenied, Err: err,
				}}
			}
		}
		resolvedActor := principal.ActorContext{
			ActorPrincipalID: req.Parent.Identity.EffectivePrincipalID,
			Source:           principal.SourceMCP,
			Provider:         principal.ProviderSystem,
			Scope:            "parent:" + req.Parent.ID,
			Subject:          req.Parent.Identity.EffectivePrincipalID,
		}
		parentActor = &resolvedActor
	}

	if req.SavedIdentity != nil {
		if strings.TrimSpace(req.RunAs) != "" {
			return IdentityResult{Decision: Decision{
				Kind: DenialForbidden, Reason: ReasonPersistentRunAsChange,
			}}
		}
		authorizingActor := req.Actor
		if parentActor != nil {
			authorizingActor = parentActor
		}
		if req.SavedIdentity.Identified() && req.IsolationActive && p != nil && p.principals != nil {
			if authorizingActor == nil {
				return IdentityResult{Decision: Decision{
					Kind: DenialUnauthenticated, Reason: ReasonActorRequired,
				}}
			}
			if err := p.principals.AuthorizeObjectAction(*authorizingActor, *req.SavedIdentity); err != nil {
				return IdentityResult{Decision: Decision{
					Kind: DenialForbidden, Reason: ReasonPersistentFollowupDenied, Err: err,
				}}
			}
		}
		return IdentityResult{Identity: *req.SavedIdentity, Decision: Decision{Allowed: true}}
	}

	if parentActor != nil {
		if p == nil || p.principals == nil {
			return IdentityResult{
				Identity: principal.LegacyHostIdentity(principal.SourceMCP),
				Decision: Decision{Allowed: true},
			}
		}
		actor := parentActor
		if strings.TrimSpace(req.RunAs) != "" && req.Actor != nil {
			actor = req.Actor
		}
		identity, err := p.principals.FreezeExecution(
			*actor, req.RunAs, req.ParentTaskID, req.ExecutionStateRef, &req.Parent.Identity,
		)
		if err != nil {
			return IdentityResult{Decision: Decision{
				Kind: DenialForbidden, Reason: ReasonIdentityResolution, Err: err,
			}}
		}
		return IdentityResult{Identity: identity, Decision: Decision{Allowed: true}}
	}

	if req.Parent != nil || req.Actor == nil || p == nil || p.principals == nil {
		if strings.TrimSpace(req.RunAs) != "" {
			return IdentityResult{Decision: Decision{
				Kind: DenialUnauthenticated, Reason: ReasonRunAsRequiresActor,
			}}
		}
		return IdentityResult{
			Identity: principal.LegacyHostIdentity(principal.SourceMCP),
			Decision: Decision{Allowed: true},
		}
	}
	identity, err := p.principals.FreezeExecution(
		*req.Actor, req.RunAs, req.ParentTaskID, req.ExecutionStateRef, nil,
	)
	if err != nil {
		return IdentityResult{Decision: Decision{
			Kind: DenialForbidden, Reason: ReasonIdentityResolution, Err: err,
		}}
	}
	return IdentityResult{Identity: identity, Decision: Decision{Allowed: true}}
}
