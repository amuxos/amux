package hub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/tunnel"
)

func agentTokenTestStore(t *testing.T) *principal.Store {
	t.Helper()
	s, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// An identified launch carries a credential for its own principal, so the agent
// inside it can ask the control plane for a sibling agent without the request
// arriving unauthenticated (and therefore as the service account).
func TestIssueAgentActorTokenIdentifiesTheLaunchPrincipal(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{
		Kind: principal.KindHuman, DisplayName: "feishu user",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &hubServer{principals: principals}
	identity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: p.ID,
		EffectivePrincipalID:   p.ID,
	}
	token, expires := srv.issueAgentActorToken(identity, "launch-1")
	if token == "" {
		t.Fatal("identified launch got no agent actor token")
	}
	// The expiry travels with the token so a relaunch weeks later can warn about
	// a credential the hub no longer tracks per session.
	if expires <= time.Now().Unix() {
		t.Fatalf("expiry = %d, want a future instant", expires)
	}
	actor, err := principals.AuthenticateActor(token, "req-1")
	if err != nil {
		t.Fatalf("agent actor token does not authenticate: %v", err)
	}
	if actor.ActorPrincipalID != p.ID || !actor.Delegated {
		t.Fatalf("agent actor = %+v, want delegated actor for %s", actor, p.ID)
	}
}

func TestIssueAgentActorTokenSkipsLegacyHostLaunches(t *testing.T) {
	srv := &hubServer{principals: agentTokenTestStore(t)}
	if token, _ := srv.issueAgentActorToken(principal.LegacyHostIdentity(principal.SourceCLI), "launch-1"); token != "" {
		t.Fatalf("legacy-host launch got token %q, want none", token)
	}
}

func TestIssueAgentActorTokenWithoutPrincipalStore(t *testing.T) {
	srv := &hubServer{}
	identity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: "11111111-1111-1111-1111-111111111111",
		EffectivePrincipalID:   "11111111-1111-1111-1111-111111111111",
	}
	if token, _ := srv.issueAgentActorToken(identity, "launch-1"); token != "" {
		t.Fatalf("store-less hub minted token %q, want none", token)
	}
}

// Restarting is the documented recovery from an expired agent credential, which
// only works if the restart actually carries a new one.
func TestRelaunchAgentActorTokenMintsForALiveSession(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{
		Kind: principal.KindHuman, DisplayName: "feishu user",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: p.ID,
		EffectivePrincipalID:   p.ID,
	}
	srv := &hubServer{principals: principals, listTargets: func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{
			Id: "s1", Name: "work", ExecutionIdentity: principal.ToProtoIdentity(identity),
		}}}}
	}}
	token, expires := srv.relaunchAgentActorToken("t1", "work")
	if token == "" || expires <= time.Now().Unix() {
		t.Fatalf("relaunch mint = %q / %d, want a live credential", token, expires)
	}
	actor, err := principals.AuthenticateActor(token, "req-1")
	if err != nil || actor.ActorPrincipalID != p.ID || !actor.Delegated {
		t.Fatalf("relaunch credential actor = %+v err = %v", actor, err)
	}
}

// A session the hub cannot see, or one that never had an identity, gets nothing
// — the relaunch then keeps replaying whatever was stored locally.
func TestRelaunchAgentActorTokenSkipsUnknownAndLegacySessions(t *testing.T) {
	principals := agentTokenTestStore(t)
	srv := &hubServer{principals: principals, listTargets: func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{
			Id: "s1", Name: "legacy",
			ExecutionIdentity: principal.ToProtoIdentity(principal.LegacyHostIdentity(principal.SourceCLI)),
		}}}}
	}}
	if token, _ := srv.relaunchAgentActorToken("t1", "legacy"); token != "" {
		t.Fatalf("legacy session minted %q, want none", token)
	}
	if token, _ := srv.relaunchAgentActorToken("t1", "missing"); token != "" {
		t.Fatalf("unknown session minted %q, want none", token)
	}
}

func TestRelaunchAgentActorTokenRotatesLegacyFeishuCredential(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := principals.IssueAgentCredential(p.ID, "launch-legacy", time.Hour); err != nil {
		t.Fatal(err)
	}
	launchID := "launch-legacy"
	srv := &hubServer{principals: principals, listTargets: func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{
			Id: "s1", Name: "legacy", LaunchId: launchID,
			ExecutionIdentity: principal.ToProtoIdentity(principal.LegacyHostIdentity(principal.SourceFeishu)),
		}}}}
	}}
	for _, currentLaunchID := range []string{"launch-legacy", "launch-runtime-rotated"} {
		launchID = currentLaunchID
		token, expires := srv.relaunchAgentActorToken("t1", "legacy")
		if token == "" || expires <= time.Now().Unix() {
			t.Fatalf("relaunch from %s = %q / %d", currentLaunchID, token, expires)
		}
		actor, err := principals.AuthenticateActor(token, "request-1")
		if err != nil || actor.ActorPrincipalID != p.ID || actor.Subject != "relaunch:t1:legacy" {
			t.Fatalf("relaunch actor = %+v err=%v", actor, err)
		}
	}
}

func TestRelaunchAgentActorTokenUsesCanonicalNameWhenRestartedBySessionID(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := principals.IssueAgentCredential(p.ID, "relaunch:t1:work", time.Hour); err != nil {
		t.Fatal(err)
	}
	srv := &hubServer{principals: principals, listTargets: func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{
			Id: "session-1", Name: "work", LaunchId: "launch-after-service-restart",
			ExecutionIdentity: principal.ToProtoIdentity(principal.LegacyHostIdentity(principal.SourceFeishu)),
		}}}}
	}}
	token, expires := srv.relaunchAgentActorToken("t1", "session-1")
	if token == "" || expires <= time.Now().Unix() {
		t.Fatalf("relaunch mint = %q / %d, want a live credential", token, expires)
	}
	actor, err := principals.AuthenticateActor(token, "request-1")
	if err != nil || actor.Subject != "relaunch:t1:work" {
		t.Fatalf("relaunch actor = %+v err=%v, want canonical session name subject", actor, err)
	}
}

// listTargets is a test-injection hook that production never sets. Guards that
// key on it (`if s.listTargets == nil { return }`) therefore disable themselves
// in production — which is how session-mutation authorization, launch
// revocation and relaunch minting all became silent no-ops. Everything that
// needs the target list must go through currentTargets.
func TestCurrentTargetsFallsBackToTheHub(t *testing.T) {
	injected := []*pb.Target{{Id: "injected"}}
	srv := &hubServer{listTargets: func() []*pb.Target { return injected }}
	if got := srv.currentTargets(); len(got) != 1 || got[0].Id != "injected" {
		t.Fatalf("with an override, currentTargets = %+v, want the injected list", got)
	}
	srv = &hubServer{hub: tunnel.NewHub()}
	if got := srv.currentTargets(); got == nil {
		t.Fatal("without an override, currentTargets ignored the hub")
	}
}

// A Feishu launch never passes through hubServer.Run — the bridge routes it
// straight at the runtime — so its credential has to be minted where the
// identity is resolved. Without it the agent inside the session reaches the
// control plane anonymously, which is how a subtask scheduled from a Feishu
// thread ended up stored with no identity to run under.
func TestFeishuExecutionIdentityResolverMintsAnAgentActorToken(t *testing.T) {
	principals := agentTokenTestStore(t)
	resolve := newFeishuLaunchIdentityResolver(principals, "feishu:app-1", "default", true)

	launch, err := resolve("ou_sender_1", "feishu-session:demo", "t1", false)
	if err != nil {
		t.Fatalf("resolve feishu identity: %v", err)
	}
	if launch.Identity == nil || launch.LaunchID == "" {
		t.Fatalf("launch = %+v, want an identity and a launch id", launch)
	}
	if launch.ActorToken == "" {
		t.Fatal("feishu launch carries no agent actor token")
	}
	if launch.ActorTokenExpiresAt <= time.Now().Unix() {
		t.Fatalf("expiry = %d, want a future instant", launch.ActorTokenExpiresAt)
	}
	actor, err := principals.AuthenticateActor(launch.ActorToken, "req-1")
	if err != nil {
		t.Fatalf("minted token does not authenticate: %v", err)
	}
	if actor.ActorPrincipalID != launch.Identity.EffectivePrincipalId || !actor.Delegated {
		t.Fatalf("actor = %+v, want a delegated actor for %s", actor, launch.Identity.EffectivePrincipalId)
	}
	if actor.Subject != "relaunch:t1:demo" {
		t.Fatalf("actor subject = %q, want stable Feishu session subject", actor.Subject)
	}
}

func TestFeishuLegacyHostLaunchMintsAScopedAgentCredential(t *testing.T) {
	principals := agentTokenTestStore(t)
	resolve := newFeishuLaunchIdentityResolver(principals, "feishu:app-1", "default", false)

	launch, err := resolve("ou-owner", "feishu-singleton:work", "target-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Identity != nil {
		t.Fatalf("legacy-host launch identity = %+v, want nil", launch.Identity)
	}
	if launch.LaunchID == "" || launch.ActorToken == "" || launch.ActorTokenExpiresAt <= time.Now().Unix() {
		t.Fatalf("legacy-host launch credential = %+v", launch)
	}
	actor, err := principals.AuthenticateActor(launch.ActorToken, "request-1")
	if err != nil {
		t.Fatalf("authenticate launch credential: %v", err)
	}
	if !actor.Delegated || actor.Subject != "relaunch:target-1:work" {
		t.Fatalf("actor = %+v, want stable legacy Feishu session subject", actor)
	}
	rotated := []*pb.Target{{Id: "target-1", Sessions: []*pb.Session{{
		Id: "session-1", Name: "work", LaunchId: "launch-after-service-restart",
	}}}}
	visible := filterTargetsBySessionAccess(rotated, &actor, true)
	if len(visible) != 1 || len(visible[0].Sessions) != 1 || visible[0].Sessions[0].Id != "session-1" {
		t.Fatalf("visible after launch rotation = %+v, want session-1", visible)
	}
}

func TestFeishuLegacyHostLaunchDoesNotFailWhenCredentialRegistryIsUnavailable(t *testing.T) {
	principals, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	resolve := newFeishuLaunchIdentityResolver(principals, "feishu:app-1", "default", false)
	if err := principals.Close(); err != nil {
		t.Fatal(err)
	}
	launch, err := resolve("ou-owner", "feishu-session:work", "target-1", false)
	if err != nil {
		t.Fatalf("legacy-host launch must remain available: %v", err)
	}
	if launch.Identity != nil || launch.ActorToken != "" {
		t.Fatalf("degraded legacy-host launch = %+v", launch)
	}
}
