package principal

import (
	"errors"
	"testing"
	"time"
)

func TestAgentCredentialAuthenticatesAsItsOwnPrincipal(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "feishu user"})
	cred, err := s.IssueAgentCredential(p.ID, "launch-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.AuthenticateAgentCredential(cred.Token, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if actor.ActorPrincipalID != p.ID {
		t.Fatalf("actor principal = %s, want %s", actor.ActorPrincipalID, p.ID)
	}
	if !actor.Delegated {
		t.Fatal("agent credential actor must be marked delegated")
	}
	if actor.Subject != "launch-1" {
		t.Fatalf("actor subject = %q, want the launch id", actor.Subject)
	}
}

// An agent credential carries exactly its own principal's frozen authority.
// Letting it name any run_as target would turn sandbox escape into an identity
// selection oracle, even when the requested target happens to be itself.
func TestAgentCredentialCannotUseRunAs(t *testing.T) {
	s := openTestStore(t)
	owner, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "owner"})
	service, _ := s.Create(Principal{Kind: KindService, DisplayName: "team"})
	if err := s.GrantServiceMember(service.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	cred, err := s.IssueAgentCredential(owner.ID, "launch-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.AuthenticateAgentCredential(cred.Token, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FreezeExecution(actor, service.ID, "", "exec-1", nil); !errors.Is(err, ErrUnauthorizedRunAs) {
		t.Fatalf("delegated run_as error = %v, want ErrUnauthorizedRunAs", err)
	}
	if _, err := s.FreezeExecution(actor, owner.ID, "", "exec-1", nil); !errors.Is(err, ErrUnauthorizedRunAs) {
		t.Fatalf("delegated self run_as error = %v, want ErrUnauthorizedRunAs", err)
	}
	if _, err := s.FreezeExecution(actor, "", "", "exec-1", nil); err != nil {
		t.Fatalf("delegated self execution error = %v, want nil", err)
	}
}

func TestAgentCredentialExpires(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "user"})
	cred, err := s.IssueAgentCredential(p.ID, "launch-1", -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAgentCredential(cred.Token, "req-1"); err == nil {
		t.Fatal("expired agent credential authenticated")
	}
}

func TestAgentCredentialRevokedWithItsLaunch(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "user"})
	cred, err := s.IssueAgentCredential(p.ID, "launch-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAgentCredentials("launch-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAgentCredential(cred.Token, "req-1"); err == nil {
		t.Fatal("revoked agent credential authenticated")
	}
}

// Reissuing for the same launch (a session relaunch) must not leave the old
// token usable.
func TestAgentCredentialReissueSupersedesTheOldToken(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "user"})
	first, err := s.IssueAgentCredential(p.ID, "launch-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.IssueAgentCredential(p.ID, "launch-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAgentCredential(first.Token, "req-1"); err == nil {
		t.Fatal("superseded agent credential still authenticates")
	}
	if _, err := s.AuthenticateAgentCredential(second.Token, "req-1"); err != nil {
		t.Fatalf("reissued agent credential error = %v", err)
	}
}

// A disabled principal must not keep acting through a credential minted while
// it was still active.
func TestAgentCredentialFollowsPrincipalStatus(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "user"})
	cred, err := s.IssueAgentCredential(p.ID, "launch-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Disable(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAgentCredential(cred.Token, "req-1"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled principal agent credential error = %v, want ErrDisabled", err)
	}
}

// AuthenticateActor is the single entry point the control plane uses; it must
// accept both a human's enrollment token and an agent's launch credential.
func TestAuthenticateActorAcceptsEnrollmentAndAgentCredentials(t *testing.T) {
	s := openTestStore(t)
	p, _ := s.Create(Principal{Kind: KindHuman, DisplayName: "user"})
	enrollment, err := s.CreateEnrollment(ProviderCLI, "local", "user", p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.IssueAgentCredential(p.ID, "launch-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	human, err := s.AuthenticateActor(enrollment.Token, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if human.Delegated {
		t.Fatal("enrollment actor must not be delegated")
	}
	delegated, err := s.AuthenticateActor(agent.Token, "req-2")
	if err != nil {
		t.Fatal(err)
	}
	if !delegated.Delegated || delegated.ActorPrincipalID != p.ID {
		t.Fatalf("agent actor = %+v", delegated)
	}
	if _, err := s.AuthenticateActor("not-a-token", "req-3"); err == nil {
		t.Fatal("unknown token authenticated")
	}
}
