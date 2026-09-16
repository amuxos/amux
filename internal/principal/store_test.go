package principal

import (
	"errors"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestScopedAliasDoesNotCrossBridge(t *testing.T) {
	s := openTestStore(t)
	a, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "A"})
	b, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "B"})
	if err := s.BindAlias(Alias{Provider: ProviderFeishu, Scope: "bridge-a", Subject: "ou-1", PrincipalID: a.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAlias(Alias{Provider: ProviderFeishu, Scope: "bridge-b", Subject: "ou-1", PrincipalID: b.ID}); err != nil {
		t.Fatal(err)
	}
	gotA, _ := s.ResolveAlias(ProviderFeishu, "bridge-a", "ou-1")
	gotB, _ := s.ResolveAlias(ProviderFeishu, "bridge-b", "ou-1")
	if gotA.ID != a.ID || gotB.ID != b.ID {
		t.Fatalf("scoped aliases drifted: %s %s", gotA.ID, gotB.ID)
	}
}

func TestFreezeExecutionRunAsAuthorizationAndAliasRebind(t *testing.T) {
	s := openTestStore(t)
	actor, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "actor"})
	service, _ := s.Create(Principal{Kind: KindService, DisplayName: "team"})
	if err := s.BindAlias(Alias{Provider: ProviderCLI, Scope: "install-1", Subject: "actor", PrincipalID: actor.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindAlias(Alias{Provider: ProviderCLI, Scope: "install-1", Subject: "team", PrincipalID: service.ID}); err != nil {
		t.Fatal(err)
	}
	ctx, _ := s.ResolveActor(ProviderCLI, "install-1", "actor", "auth-1", "req-1", SourceCLI)
	if _, err := s.FreezeExecution(ctx, "team", "", "exec-1", nil); !errors.Is(err, ErrUnauthorizedRunAs) {
		t.Fatalf("unauthorized run_as error = %v", err)
	}
	if err := s.GrantServiceMember(service.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	id, err := s.FreezeExecution(ctx, "team", "", "exec-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id.EffectivePrincipalID != service.ID {
		t.Fatalf("effective = %s, want %s", id.EffectivePrincipalID, service.ID)
	}
	other, _ := s.Create(Principal{Kind: KindService, DisplayName: "other"})
	if err := s.BindAlias(Alias{Provider: ProviderCLI, Scope: "install-1", Subject: "team", PrincipalID: other.ID}); err != nil {
		t.Fatal(err)
	}
	if id.EffectivePrincipalID != service.ID {
		t.Fatal("frozen identity changed after alias rebind")
	}
}

func TestDisabledPrincipalRejectedAndLegacyNotUpgraded(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "actor"})
	_ = s.BindAlias(Alias{Provider: ProviderCLI, Scope: "i", Subject: "a", PrincipalID: p.ID})
	if err := s.Disable(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAlias(ProviderCLI, "i", "a"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("resolve disabled error = %v", err)
	}
	legacy := LegacyHostIdentity(SourceCLI)
	if legacy.Identified() || legacy.EffectivePrincipalID != "" {
		t.Fatal("legacy identity was inferred")
	}
}

func TestEnrollmentRequiresExplicitRotationAndInvalidatesOldToken(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "actor"})
	first, err := s.CreateEnrollment(ProviderCLI, "local", "actor", p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEnrollment(ProviderCLI, "local", "actor", p.ID, false); err == nil {
		t.Fatal("duplicate enrollment did not require explicit rotation")
	}
	second, err := s.CreateEnrollment(ProviderCLI, "local", "actor", p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateEnrollment(first.Token, "old"); err == nil {
		t.Fatal("rotated enrollment left old token active")
	}
	if actor, err := s.AuthenticateEnrollment(second.Token, "new"); err != nil || actor.ActorPrincipalID != p.ID {
		t.Fatalf("new enrollment actor=%+v err=%v", actor, err)
	}
}

func TestPolicyMutationsCommitWithSingleRevisionIncrement(t *testing.T) {
	s := openTestStore(t)
	if got := s.policyRevision(); got != "1" {
		t.Fatalf("initial revision = %s, want 1", got)
	}
	p, err := s.Create(Principal{Kind: KindHuman, DisplayName: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.policyRevision(); got != "2" {
		t.Fatalf("revision after create = %s, want 2", got)
	}
	if err := s.BindAlias(Alias{Provider: ProviderCLI, Scope: "local", Subject: "actor", PrincipalID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if got := s.policyRevision(); got != "3" {
		t.Fatalf("revision after alias = %s, want 3", got)
	}
	if _, err := s.CreateEnrollment(ProviderCLI, "local", "actor", p.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := s.policyRevision(); got != "4" {
		t.Fatalf("enrollment and alias transaction revision = %s, want 4", got)
	}
}

func TestRevokeServiceMemberBlocksFutureRunAs(t *testing.T) {
	s := openTestStore(t)
	actor, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "actor"})
	service, _ := s.Create(Principal{Kind: KindService, DisplayName: "service"})
	_ = s.BindAlias(Alias{Provider: ProviderCLI, Scope: "local", Subject: "actor", PrincipalID: actor.ID})
	_ = s.BindAlias(Alias{Provider: ProviderCLI, Scope: "local", Subject: "service", PrincipalID: service.ID})
	if err := s.GrantServiceMember(service.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	actorContext, _ := s.ResolveActor(ProviderCLI, "local", "actor", "auth", "req", SourceCLI)
	if _, err := s.FreezeExecution(actorContext, "service", "", "before", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeServiceMember(service.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FreezeExecution(actorContext, "service", "", "after", nil); !errors.Is(err, ErrUnauthorizedRunAs) {
		t.Fatalf("revoked service membership error = %v", err)
	}
}

func TestScheduledExecutionRechecksFrozenAuthorityWithoutResolvingAlias(t *testing.T) {
	s := openTestStore(t)
	actor, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "actor"})
	service, _ := s.Create(Principal{Kind: KindService, DisplayName: "service"})
	other, _ := s.Create(Principal{Kind: KindService, DisplayName: "other"})
	_ = s.BindAlias(Alias{Provider: ProviderCLI, Scope: "local", Subject: "actor", PrincipalID: actor.ID})
	_ = s.BindAlias(Alias{Provider: ProviderCLI, Scope: "local", Subject: "team", PrincipalID: service.ID})
	if err := s.GrantServiceMember(service.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	actorContext, _ := s.ResolveActor(ProviderCLI, "local", "actor", "auth", "req", SourceSchedule)
	identity, err := s.FreezeExecution(actorContext, "team", "", "schedule:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindAlias(Alias{Provider: ProviderCLI, Scope: "local", Subject: "team", PrincipalID: other.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeScheduledExecution(identity); err != nil {
		t.Fatalf("alias rebind invalidated frozen schedule: %v", err)
	}
	triggered, err := s.RefreshScheduledExecution(identity, "task:trigger-1")
	if err != nil {
		t.Fatalf("refresh scheduled execution: %v", err)
	}
	if triggered.OriginActorPrincipalID != identity.OriginActorPrincipalID ||
		triggered.EffectivePrincipalID != identity.EffectivePrincipalID ||
		triggered.DelegationID != identity.DelegationID {
		t.Fatalf("trigger identity drifted: saved=%+v trigger=%+v", identity, triggered)
	}
	if triggered.CreationAuthorizationDecisionID == identity.CreationAuthorizationDecisionID ||
		triggered.ExecutionStateRef != "task:trigger-1" {
		t.Fatalf("trigger did not record a fresh decision/state ref: saved=%+v trigger=%+v", identity, triggered)
	}
	if err := s.RevokeServiceMember(service.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeScheduledExecution(identity); !errors.Is(err, ErrUnauthorizedRunAs) {
		t.Fatalf("revoked membership still authorized schedule: %v", err)
	}
}

// Operators can bind aliases but had no way to read them back: the store knew
// who was bound to whom and nothing surfaced it.
func TestListAliasesReturnsEveryBinding(t *testing.T) {
	store := openTestStore(t)
	alice, err := store.Create(Principal{Kind: KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create(Principal{Kind: KindHuman, DisplayName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Alias{
		{Provider: ProviderFeishu, Scope: "feishu:app-1", Subject: "ou_bob", PrincipalID: bob.ID},
		{Provider: ProviderCLI, Scope: "local", Subject: "alice", PrincipalID: alice.ID},
	} {
		if err := store.BindAlias(a); err != nil {
			t.Fatal(err)
		}
	}

	aliases, err := store.ListAliases()
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 2 {
		t.Fatalf("aliases = %+v, want 2", aliases)
	}
	// Stable ordering keeps `alias list` output diffable across runs.
	if aliases[0].Provider != ProviderCLI || aliases[1].Provider != ProviderFeishu {
		t.Fatalf("aliases are not ordered by provider/scope/subject: %+v", aliases)
	}
	if aliases[0].Subject != "alice" || aliases[0].PrincipalID != alice.ID {
		t.Fatalf("cli alias = %+v", aliases[0])
	}
	if aliases[1].Scope != "feishu:app-1" || aliases[1].PrincipalID != bob.ID {
		t.Fatalf("feishu alias = %+v", aliases[1])
	}
}

// A Feishu sender who has never talked to the bot must get a principal without
// an operator in the loop. Two messages arriving at once must still produce one
// principal, or the same person ends up with two OS accounts.
func TestEnsureAliasPrincipalCreatesOnceAndIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	alias := Alias{Provider: ProviderFeishu, Scope: "feishu:app-1", Subject: "ou_newcomer"}

	first, err := store.EnsureAliasPrincipal(alias, "ou_newcomer")
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != KindHuman || first.Status != StatusActive {
		t.Fatalf("principal = %+v, want an active human", first)
	}

	second, err := store.EnsureAliasPrincipal(alias, "ou_newcomer")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("second call created a new principal %s (first %s)", second.ID, first.ID)
	}
	principals, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(principals) != 1 {
		t.Fatalf("principals = %d, want exactly one", len(principals))
	}
	// The binding must be readable afterwards, or the operator cannot audit it.
	aliases, err := store.ListAliases()
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].PrincipalID != first.ID {
		t.Fatalf("aliases = %+v, want one bound to %s", aliases, first.ID)
	}
}

// An existing binding is authoritative: auto-provisioning must never re-point a
// sender at a fresh principal and silently strip them of their history.
func TestEnsureAliasPrincipalKeepsExistingBinding(t *testing.T) {
	store := openTestStore(t)
	existing, err := store.Create(Principal{Kind: KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	alias := Alias{Provider: ProviderFeishu, Scope: "feishu:app-1", Subject: "ou_alice", PrincipalID: existing.ID}
	if err := store.BindAlias(alias); err != nil {
		t.Fatal(err)
	}

	got, err := store.EnsureAliasPrincipal(Alias{Provider: alias.Provider, Scope: alias.Scope, Subject: alias.Subject}, "ou_alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != existing.ID || got.DisplayName != "Alice" {
		t.Fatalf("got = %+v, want the already bound principal %s", got, existing.ID)
	}
}
