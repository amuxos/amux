package executionauth_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/executionauth"
	"github.com/amuxos/amux/internal/principal"
)

func TestExecutionAccessFollowsCurrentIsolationBoundary(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	aliceActor := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	bobActor := principal.ActorContext{ActorPrincipalID: bob.ID, Source: principal.SourceCLI}
	identity, err := store.FreezeExecution(aliceActor, "", "", "task:1", nil)
	if err != nil {
		t.Fatal(err)
	}

	policy := executionauth.New(store)
	shared := policy.ExecutionAccess(executionauth.ExecutionAccessRequest{
		Actor:           &bobActor,
		Identity:        identity,
		IsolationActive: false,
	})
	if !shared.Allowed {
		t.Fatalf("non-isolated access = %+v, want allowed", shared)
	}

	isolated := policy.ExecutionAccess(executionauth.ExecutionAccessRequest{
		Actor:           &bobActor,
		Identity:        identity,
		IsolationActive: true,
	})
	if isolated.Allowed || isolated.Kind != executionauth.DenialForbidden || isolated.Reason != executionauth.ReasonPrincipalMismatch {
		t.Fatalf("isolated cross-principal access = %+v, want forbidden principal mismatch", isolated)
	}
}

func TestExecutionAccessCanUseItsIdentifiedParentSession(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	aliceActor := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	parentIdentity, err := store.FreezeExecution(aliceActor, "", "", "session:parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	taskIdentity, err := store.FreezeExecution(aliceActor, "", "", "task:child", &parentIdentity)
	if err != nil {
		t.Fatal(err)
	}

	policy := executionauth.New(store)
	decision := policy.ExecutionAccess(executionauth.ExecutionAccessRequest{
		Identity:        taskIdentity,
		ParentSessionID: "session-parent",
		IsolationActive: true,
		Sessions: []executionauth.SessionFacts{{
			ID:       "session-parent",
			Identity: parentIdentity,
		}},
	})
	if !decision.Allowed {
		t.Fatalf("parent-owned task access = %+v, want allowed", decision)
	}

	missing := policy.ExecutionAccess(executionauth.ExecutionAccessRequest{
		Identity:        taskIdentity,
		ParentSessionID: "missing",
		IsolationActive: true,
	})
	if missing.Allowed || missing.Kind != executionauth.DenialUnauthenticated || missing.Reason != executionauth.ReasonActorRequired {
		t.Fatalf("task access without actor or parent = %+v, want actor required", missing)
	}
}

func TestSessionAccessAllowsSamePrincipalSiblingsAndRejectsForeignPrincipal(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	delegated := enrollment
	delegated.Delegated = true
	delegated.Subject = "launch-main"
	callerIdentity, err := store.FreezeExecution(enrollment, "", "", "session:main", nil)
	if err != nil {
		t.Fatal(err)
	}
	childIdentity, err := store.FreezeExecution(delegated, "", "launch-main", "session:child", nil)
	if err != nil {
		t.Fatal(err)
	}
	siblingIdentity, err := store.FreezeExecution(enrollment, "", "", "session:sibling", nil)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	foreignIdentity, err := store.FreezeExecution(principal.ActorContext{ActorPrincipalID: bob.ID, Source: principal.SourceCLI}, "", "", "session:foreign", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDelegation(alice.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	sessions := []executionauth.SessionFacts{
		{ID: "main", TargetID: "runtime-1", Name: "main", LaunchID: "launch-main", Identity: callerIdentity},
		{ID: "child", TargetID: "runtime-1", Name: "child", LaunchID: "launch-child", Identity: childIdentity},
		{ID: "sibling", TargetID: "runtime-1", Name: "sibling", LaunchID: "launch-sibling", Identity: siblingIdentity},
		{ID: "foreign", TargetID: "runtime-1", Name: "foreign", LaunchID: "launch-foreign", Identity: foreignIdentity},
	}
	policy := executionauth.New(store)

	for _, target := range []executionauth.SessionFacts{sessions[0], sessions[1], sessions[2]} {
		decision := policy.SessionAccess(executionauth.SessionAccessRequest{
			Actor: &delegated, Target: target, Sessions: sessions, IsolationActive: true,
		})
		if !decision.Allowed {
			t.Fatalf("delegated access to %s = %+v, want allowed", target.ID, decision)
		}
	}

	// A delegated agent credential represents only its own principal. Even an
	// active human-level run_as delegation must not widen session access.
	denied := policy.SessionAccess(executionauth.SessionAccessRequest{
		Actor: &delegated, Target: sessions[3], Sessions: sessions, IsolationActive: true,
	})
	if denied.Allowed || denied.Kind != executionauth.DenialForbidden || denied.Reason != executionauth.ReasonPrincipalMismatch {
		t.Fatalf("delegated foreign-principal access = %+v, want principal mismatch", denied)
	}

	allowed := policy.SessionAccess(executionauth.SessionAccessRequest{
		Actor: &enrollment, Target: sessions[2], Sessions: sessions, IsolationActive: true,
	})
	if !allowed.Allowed {
		t.Fatalf("enrollment sibling access = %+v, want allowed", allowed)
	}
}

func TestSessionListAccessScopesDelegatedActorsByPrincipal(t *testing.T) {
	enrollment := principal.ActorContext{ActorPrincipalID: "alice", Source: principal.SourceCLI}
	delegated := enrollment
	delegated.Delegated = true
	delegated.Subject = "launch-main"
	sessions := []executionauth.SessionFacts{
		{ID: "main", LaunchID: "launch-main", Identity: principal.ExecutionIdentity{IdentityMode: principal.IdentityModeIdentified, OriginActorPrincipalID: "alice", EffectivePrincipalID: "alice"}},
		{ID: "sibling", LaunchID: "launch-sibling", Identity: principal.ExecutionIdentity{IdentityMode: principal.IdentityModeIdentified, OriginActorPrincipalID: "alice", EffectivePrincipalID: "alice"}},
		{ID: "foreign", Identity: principal.ExecutionIdentity{IdentityMode: principal.IdentityModeIdentified, OriginActorPrincipalID: "bob", EffectivePrincipalID: "bob"}},
	}
	policy := executionauth.New(nil)

	if decision := policy.SessionListAccess(executionauth.SessionAccessRequest{
		Actor: &enrollment, Target: sessions[2], Sessions: sessions, IsolationActive: true,
	}); !decision.Allowed {
		t.Fatalf("enrollment list access = %+v, want full list", decision)
	}
	if decision := policy.SessionListAccess(executionauth.SessionAccessRequest{
		Actor: &delegated, Target: sessions[1], Sessions: sessions, IsolationActive: true,
	}); !decision.Allowed {
		t.Fatalf("delegated sibling list access = %+v, want allowed", decision)
	}
	denied := policy.SessionListAccess(executionauth.SessionAccessRequest{
		Actor: &delegated, Target: sessions[2], Sessions: sessions, IsolationActive: true,
	})
	if denied.Allowed || denied.Reason != executionauth.ReasonPrincipalMismatch {
		t.Fatalf("delegated foreign-principal list access = %+v, want principal mismatch", denied)
	}
}

func TestCallerSessionAccessRequiresTheCredentialSession(t *testing.T) {
	actor := principal.ActorContext{Delegated: true, Subject: "relaunch:runtime-1:main"}
	sessions := []executionauth.SessionFacts{
		{ID: "main", TargetID: "runtime-1", Name: "main", LaunchID: "rotated"},
		{ID: "child", TargetID: "runtime-1", Name: "child", Identity: principal.ExecutionIdentity{ParentExecutionID: "rotated"}},
	}
	policy := executionauth.New(nil)
	if decision := policy.CallerSessionAccess(&actor, sessions[0], sessions); !decision.Allowed {
		t.Fatalf("caller session = %+v, want allowed", decision)
	}
	decision := policy.CallerSessionAccess(&actor, sessions[1], sessions)
	if decision.Allowed || decision.Reason != executionauth.ReasonOutsideDelegatedReach {
		t.Fatalf("created child as caller session = %+v, want denied", decision)
	}
}

func TestPruneAccessRequiresEnrollmentOnlyWhenIsolationIsActive(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actorPrincipal, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	delegated := principal.ActorContext{ActorPrincipalID: actorPrincipal.ID, Source: principal.SourceMCP, Delegated: true}
	enrollment := delegated
	enrollment.Delegated = false
	policy := executionauth.New(store)

	if decision := policy.PruneAccess(&delegated, false); !decision.Allowed {
		t.Fatalf("non-isolated delegated prune = %+v, want allowed", decision)
	}
	if decision := policy.PruneAccess(&enrollment, true); !decision.Allowed {
		t.Fatalf("isolated enrollment prune = %+v, want allowed", decision)
	}
	decision := policy.PruneAccess(&delegated, true)
	if decision.Allowed || decision.Kind != executionauth.DenialForbidden || decision.Reason != executionauth.ReasonEnrollmentRequired {
		t.Fatalf("isolated delegated prune = %+v, want enrollment required", decision)
	}
	decision = policy.PruneAccess(nil, false)
	if decision.Allowed || decision.Kind != executionauth.DenialUnauthenticated || decision.Reason != executionauth.ReasonActorRequired {
		t.Fatalf("anonymous prune = %+v, want actor required", decision)
	}
}

func TestScheduleAccessUsesFrozenOwnerOnlyWhenIsolationIsActive(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	aliceActor := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	bobActor := principal.ActorContext{ActorPrincipalID: bob.ID, Source: principal.SourceCLI}
	policy := executionauth.New(store)

	if decision := policy.ScheduleAccess(&bobActor, alice.ID, false); !decision.Allowed {
		t.Fatalf("non-isolated schedule access = %+v, want allowed", decision)
	}
	if decision := policy.ScheduleAccess(&bobActor, "", true); !decision.Allowed {
		t.Fatalf("unowned schedule access = %+v, want allowed", decision)
	}
	if decision := policy.ScheduleAccess(&aliceActor, alice.ID, true); !decision.Allowed {
		t.Fatalf("owner schedule access = %+v, want allowed", decision)
	}
	decision := policy.ScheduleAccess(&bobActor, alice.ID, true)
	if decision.Allowed || decision.Kind != executionauth.DenialForbidden || decision.Reason != executionauth.ReasonScheduleOwnerMismatch {
		t.Fatalf("cross-owner schedule access = %+v, want owner mismatch", decision)
	}
}

func TestScheduleTriggerRevalidatesOwnerAgainstTargetSession(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	aliceActor := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	targetIdentity, err := store.FreezeExecution(aliceActor, "", "", "session:target", nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := executionauth.New(store)

	if decision := policy.ScheduleTriggerAccess(alice.ID, targetIdentity, true); !decision.Allowed {
		t.Fatalf("owner trigger = %+v, want allowed", decision)
	}
	missing := policy.ScheduleTriggerAccess("", targetIdentity, true)
	if missing.Allowed || missing.Reason != executionauth.ReasonScheduleOwnerRequired {
		t.Fatalf("ownerless isolated trigger = %+v, want owner required", missing)
	}
	denied := policy.ScheduleTriggerAccess(bob.ID, targetIdentity, true)
	if denied.Allowed || denied.Reason != executionauth.ReasonPrincipalMismatch {
		t.Fatalf("cross-principal trigger = %+v, want principal mismatch", denied)
	}
	if decision := policy.ScheduleTriggerAccess(bob.ID, targetIdentity, false); !decision.Allowed {
		t.Fatalf("non-isolated trigger = %+v, want allowed", decision)
	}
}

func TestFreezeSessionIdentityRecordsDelegatedCreator(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	delegated := principal.ActorContext{
		ActorPrincipalID: alice.ID,
		Source:           principal.SourceMCP,
		Subject:          "launch-parent",
		Delegated:        true,
	}
	policy := executionauth.New(store)
	result := policy.FreezeSessionIdentity(executionauth.FreezeSessionRequest{
		Actor:             &delegated,
		Source:            principal.SourceMCP,
		ExecutionStateRef: "session:child",
		Sessions: []executionauth.SessionFacts{{
			ID: "parent", LaunchID: "launch-parent",
		}},
	})
	if !result.Decision.Allowed {
		t.Fatalf("freeze delegated session = %+v, want allowed", result.Decision)
	}
	if result.Identity.ParentExecutionID != "launch-parent" || result.Identity.EffectivePrincipalID != alice.ID {
		t.Fatalf("frozen identity = %+v, want delegated creator and Alice principal", result.Identity)
	}

	anonymous := policy.FreezeSessionIdentity(executionauth.FreezeSessionRequest{
		Source: principal.SourceCLI,
	})
	if !anonymous.Decision.Allowed || anonymous.Identity.IdentityMode != principal.IdentityModeLegacyHost {
		t.Fatalf("anonymous session identity = %+v, %+v, want legacy-host", anonymous.Identity, anonymous.Decision)
	}
	withRunAs := policy.FreezeSessionIdentity(executionauth.FreezeSessionRequest{
		Source: principal.SourceCLI,
		RunAs:  alice.ID,
	})
	if withRunAs.Decision.Allowed || withRunAs.Decision.Kind != executionauth.DenialUnauthenticated || withRunAs.Decision.Reason != executionauth.ReasonRunAsRequiresActor {
		t.Fatalf("anonymous run_as = %+v, want actor required", withRunAs.Decision)
	}
}

func TestFreezeScheduleIdentityRejectsAnonymousIsolatedDefinitions(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	actor := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceMCP}
	policy := executionauth.New(store)

	identified := policy.FreezeScheduleIdentity(executionauth.FreezeScheduleRequest{
		Actor: &actor, ExecutionStateRef: "schedule-definition:1", IsolationActive: true,
	})
	if !identified.Decision.Allowed || identified.Identity.Source != principal.SourceSchedule || identified.Identity.EffectivePrincipalID != alice.ID {
		t.Fatalf("identified schedule identity = %+v, %+v", identified.Identity, identified.Decision)
	}

	shared := policy.FreezeScheduleIdentity(executionauth.FreezeScheduleRequest{})
	if !shared.Decision.Allowed || shared.Identity.IdentityMode != principal.IdentityModeLegacyHost {
		t.Fatalf("non-isolated anonymous schedule = %+v, %+v, want legacy-host", shared.Identity, shared.Decision)
	}

	denied := policy.FreezeScheduleIdentity(executionauth.FreezeScheduleRequest{IsolationActive: true})
	if denied.Decision.Allowed || denied.Decision.Kind != executionauth.DenialUnauthenticated || denied.Decision.Reason != executionauth.ReasonIdentifiedScheduleRequired {
		t.Fatalf("isolated anonymous schedule = %+v, want identified schedule required", denied.Decision)
	}
}

func TestFreezeTaskIdentityAuthorizesParentClaimOnlyWithIsolation(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	aliceActor := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	bobActor := principal.ActorContext{ActorPrincipalID: bob.ID, Source: principal.SourceCLI}
	parentIdentity, err := store.FreezeExecution(aliceActor, "", "", "session:parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent := executionauth.SessionFacts{ID: "parent", Identity: parentIdentity}
	policy := executionauth.New(store)

	denied := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor: &bobActor, Parent: &parent, IsolationActive: true, ExecutionStateRef: "task:pending",
	})
	if denied.Decision.Allowed || denied.Decision.Kind != executionauth.DenialForbidden || denied.Decision.Reason != executionauth.ReasonParentClaimDenied {
		t.Fatalf("isolated cross-principal parent claim = %+v, want denied", denied.Decision)
	}

	shared := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor: &bobActor, Parent: &parent, IsolationActive: false, ExecutionStateRef: "task:pending",
	})
	if !shared.Decision.Allowed || shared.Identity.EffectivePrincipalID != alice.ID ||
		shared.Identity.OriginActorPrincipalID != alice.ID || shared.Identity.Source != principal.SourceMCP {
		t.Fatalf("non-isolated parent claim = %+v, %+v, want inherited Alice identity", shared.Identity, shared.Decision)
	}

	owned := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor: &aliceActor, Parent: &parent, IsolationActive: true, ExecutionStateRef: "task:pending",
	})
	if !owned.Decision.Allowed || owned.Identity.EffectivePrincipalID != alice.ID {
		t.Fatalf("owned parent claim = %+v, %+v, want allowed", owned.Identity, owned.Decision)
	}
}

func TestFreezeTaskIdentityRejectsDelegatedRunAsWithParent(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := store.Create(principal.Principal{Kind: principal.KindService, DisplayName: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.GrantServiceMember(service.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	enrollment := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	parentIdentity, err := store.FreezeExecution(enrollment, "", "", "session:parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	delegated := principal.ActorContext{
		ActorPrincipalID: alice.ID,
		Source:           principal.SourceMCP,
		Subject:          "launch-parent",
		Delegated:        true,
	}
	parent := executionauth.SessionFacts{ID: "parent", Identity: parentIdentity}

	result := executionauth.New(store).FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor:             &delegated,
		RunAs:             service.ID,
		Parent:            &parent,
		IsolationActive:   true,
		ExecutionStateRef: "task:pending",
	})
	if result.Decision.Allowed || result.Decision.Kind != executionauth.DenialForbidden ||
		result.Decision.Reason != executionauth.ReasonIdentityResolution ||
		!errors.Is(result.Decision.Err, principal.ErrUnauthorizedRunAs) {
		t.Fatalf("delegated parent run_as = %+v, want unauthorized run_as", result.Decision)
	}

	inherited := executionauth.New(store).FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor:             &delegated,
		Parent:            &parent,
		IsolationActive:   true,
		ExecutionStateRef: "task:pending",
	})
	if !inherited.Decision.Allowed || inherited.Identity.EffectivePrincipalID != alice.ID ||
		inherited.Identity.OriginActorPrincipalID != alice.ID || inherited.Identity.Source != principal.SourceMCP {
		t.Fatalf("delegated parent inheritance = %+v, %+v, want parent provenance", inherited.Identity, inherited.Decision)
	}
}

func TestFreezeTaskIdentityRejectsAnonymousRunAsWithParent(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	parentIdentity, err := store.FreezeExecution(enrollment, "", "", "session:parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent := executionauth.SessionFacts{ID: "parent", Identity: parentIdentity}
	policy := executionauth.New(store)

	for _, isolationActive := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "isolated"}[isolationActive], func(t *testing.T) {
			result := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
				RunAs:             alice.ID,
				Parent:            &parent,
				IsolationActive:   isolationActive,
				ExecutionStateRef: "task:pending",
			})
			if result.Decision.Allowed || result.Decision.Kind != executionauth.DenialUnauthenticated ||
				result.Decision.Reason != executionauth.ReasonRunAsRequiresActor {
				t.Fatalf("anonymous parent run_as = %+v, want actor required", result.Decision)
			}
		})
	}
}

func TestFreezeTaskIdentityUsesEnrollmentActorForParentRunAs(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := store.Create(principal.Principal{Kind: principal.KindService, DisplayName: "Service"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.GrantServiceMember(service.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.BindAlias(principal.Alias{
		Provider:    principal.ProviderCLI,
		Scope:       "install-1",
		Subject:     "service",
		PrincipalID: service.ID,
	}); err != nil {
		t.Fatal(err)
	}
	enrollment := principal.ActorContext{
		ActorPrincipalID: alice.ID,
		Source:           principal.SourceCLI,
		Provider:         principal.ProviderCLI,
		Scope:            "install-1",
		Subject:          "enrollment",
	}
	parentIdentity, err := store.FreezeExecution(enrollment, "", "", "session:parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent := executionauth.SessionFacts{ID: "parent", Identity: parentIdentity}
	policy := executionauth.New(store)

	for _, isolationActive := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "isolated"}[isolationActive], func(t *testing.T) {
			result := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
				Actor:             &enrollment,
				RunAs:             "service",
				ParentTaskID:      "parent-task",
				Parent:            &parent,
				IsolationActive:   isolationActive,
				ExecutionStateRef: "task:pending",
			})
			wantDelegation := principal.ServiceMembershipAuthorityID(service.ID, alice.ID)
			if !result.Decision.Allowed || result.Identity.EffectivePrincipalID != service.ID ||
				result.Identity.OriginActorPrincipalID != alice.ID || result.Identity.Source != principal.SourceCLI ||
				result.Identity.RequestedRunAs != "service" || result.Identity.DelegationID != wantDelegation ||
				result.Identity.ParentExecutionID != "parent-task" {
				t.Fatalf("enrollment parent run_as = %+v, %+v, want authorized service identity", result.Identity, result.Decision)
			}
		})
	}
}

func TestFreezeTaskIdentityKeepsPersistentInstanceAuthority(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	aliceActor := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceCLI}
	bobActor := principal.ActorContext{ActorPrincipalID: bob.ID, Source: principal.SourceCLI}
	saved, err := store.FreezeExecution(aliceActor, "", "", "task:original", nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := executionauth.New(store)

	denied := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor: &bobActor, SavedIdentity: &saved, IsolationActive: true,
	})
	if denied.Decision.Allowed || denied.Decision.Reason != executionauth.ReasonPersistentFollowupDenied {
		t.Fatalf("isolated cross-principal followup = %+v, want denied", denied.Decision)
	}

	shared := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor: &bobActor, SavedIdentity: &saved,
	})
	if !shared.Decision.Allowed || shared.Identity != saved {
		t.Fatalf("shared followup = %+v, %+v, want saved identity", shared.Identity, shared.Decision)
	}

	changed := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor: &aliceActor, SavedIdentity: &saved, RunAs: alice.ID,
	})
	if changed.Decision.Allowed || changed.Decision.Reason != executionauth.ReasonPersistentRunAsChange {
		t.Fatalf("persistent run_as change = %+v, want denied", changed.Decision)
	}
}

func TestFreezeTaskIdentityRefreshesScheduledAuthority(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	actor := principal.ActorContext{ActorPrincipalID: alice.ID, Source: principal.SourceSchedule}
	saved, err := store.FreezeExecution(actor, "", "", "schedule-definition:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := executionauth.New(store)

	refreshed := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		ScheduledIdentity: &saved, ExecutionStateRef: "scheduled-task:1",
	})
	if !refreshed.Decision.Allowed || refreshed.Identity.ExecutionStateRef != "scheduled-task:1" || refreshed.Identity.Source != principal.SourceSchedule {
		t.Fatalf("scheduled refresh = %+v, %+v", refreshed.Identity, refreshed.Decision)
	}

	invalidIdentity := saved
	invalidIdentity.EffectivePrincipalID = ""
	invalid := policy.FreezeTaskIdentity(executionauth.FreezeTaskRequest{ScheduledIdentity: &invalidIdentity})
	if invalid.Decision.Allowed || invalid.Decision.Kind != executionauth.DenialInvalidArgument || invalid.Decision.Reason != executionauth.ReasonInvalidSavedIdentity {
		t.Fatalf("invalid scheduled identity = %+v, want invalid argument", invalid.Decision)
	}
}
