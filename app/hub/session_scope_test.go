package hub

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/tunnel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// runScopeHarness is a minimal pb.Amuxd_TunnelServer that lets hubServer.Run
// drive a real tunnel.Hub: it answers Run requests with a canned response and
// records the payload the hub froze into the request.
type runScopeHarness struct {
	pb.Amuxd_TunnelServer
	ctx context.Context
	in  chan *pb.TunnelMsg

	mu         sync.Mutex
	runReq     *pb.RunRequest
	restartReq *pb.RestartRequest
}

func (f *runScopeHarness) Recv() (*pb.TunnelMsg, error) {
	msg, ok := <-f.in
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

func (f *runScopeHarness) Send(m *pb.TunnelMsg) error {
	if m.Kind != "req" {
		return nil
	}
	if m.Method == "Attach" {
		// A stream, not a unary call: answer with one data frame and end it. A
		// "resp" frame would remove the pending stream from the hub before its
		// data frames dispatch, hanging the caller.
		ev, err := json.Marshal(&pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("attach-ok")})
		if err != nil {
			return err
		}
		f.in <- &pb.TunnelMsg{Kind: "data", RequestId: m.RequestId, Payload: ev}
		f.in <- &pb.TunnelMsg{Kind: "end", RequestId: m.RequestId}
		return nil
	}
	var payload json.RawMessage
	switch m.Method {
	case "Run":
		var req pb.RunRequest
		if err := json.Unmarshal(m.Payload, &req); err != nil {
			return err
		}
		f.mu.Lock()
		f.runReq = &req
		f.mu.Unlock()
		b, err := json.Marshal(&pb.RunResponse{Session: &pb.Session{Id: "s-child", Name: req.Name}})
		if err != nil {
			return err
		}
		payload = b
	case "Kill":
		b, err := json.Marshal(&pb.KillResponse{Ok: true})
		if err != nil {
			return err
		}
		payload = b
	case "Restart":
		var req pb.RestartRequest
		if err := json.Unmarshal(m.Payload, &req); err != nil {
			return err
		}
		f.mu.Lock()
		f.restartReq = &req
		f.mu.Unlock()
		b, err := json.Marshal(&pb.RunResponse{Session: &pb.Session{Id: "s-restarted", Name: req.Name}})
		if err != nil {
			return err
		}
		payload = b
	case "ChangeCwd":
		b, err := json.Marshal(&pb.ChangeCwdResponse{EffectiveCwd: "/shared", Changed: true})
		if err != nil {
			return err
		}
		payload = b
	case "QueueList":
		b, err := json.Marshal(&pb.QueueListResponse{Items: []*pb.QueueItem{{Id: "q1", Source: "cli", Text: "ping", CreatedAt: 1}}})
		if err != nil {
			return err
		}
		payload = b
	case "Prune":
		b, err := json.Marshal(&pb.PruneResponse{Count: 1, Names: []string{"archived-1"}})
		if err != nil {
			return err
		}
		payload = b
	default:
		return nil
	}
	f.in <- &pb.TunnelMsg{Kind: "resp", RequestId: m.RequestId, Payload: payload}
	return nil
}

func (f *runScopeHarness) Context() context.Context { return f.ctx }

func (f *runScopeHarness) lastRunRequest() *pb.RunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runReq
}

func (f *runScopeHarness) lastRestartRequest() *pb.RestartRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restartReq
}

// newRunScopeHub builds a hub server backed by a real tunnel.Hub with one fake
// target (t1) connected and hosting the given sessions. The target registers
// without isolation capabilities, so delegated session narrowing stays
// inactive; use newRunScopeHubIsolating for tests that exercise the narrow.
func newRunScopeHub(t *testing.T, principals *principal.Store, sessions []*pb.Session) (*hubServer, *runScopeHarness) {
	t.Helper()
	return newRunScopeHubWithCapabilities(t, principals, sessions, nil)
}

// newRunScopeHubIsolating is newRunScopeHub with the registered target
// declaring an isolation helper capability, which activates the delegated
// session reach narrow hub-side.
func newRunScopeHubIsolating(t *testing.T, principals *principal.Store, sessions []*pb.Session) (*hubServer, *runScopeHarness) {
	t.Helper()
	return newRunScopeHubWithCapabilities(t, principals, sessions, &pb.TargetCapabilities{IsolationHelperProtocol: "1"})
}

func newRunScopeHubWithCapabilities(t *testing.T, principals *principal.Store, sessions []*pb.Session, caps *pb.TargetCapabilities) (*hubServer, *runScopeHarness) {
	t.Helper()
	hub := tunnel.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream := &runScopeHarness{ctx: ctx, in: make(chan *pb.TunnelMsg, 8)}
	go func() { _ = hub.Serve(stream) }()
	t.Cleanup(func() { close(stream.in) })
	stream.in <- &pb.TunnelMsg{Kind: "register", Payload: mustSessionScopeJSON(t, &pb.RegisterPayload{TargetId: "t1", TargetName: "box", Capabilities: caps})}
	waitForHubState(t, func() bool { return len(hub.ListTargets()) == 1 })
	stream.in <- &pb.TunnelMsg{Kind: "sessions", Payload: mustSessionScopeJSON(t, &pb.SessionsPayload{Sessions: sessions})}
	waitForHubState(t, func() bool {
		targets := hub.ListTargets()
		return len(targets) == 1 && len(targets[0].Sessions) == len(sessions)
	})
	return &hubServer{hub: hub, principals: principals}, stream
}

func mustSessionScopeJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func waitForHubState(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("hub state not reached within 2s")
}

// A delegated actor is an agent running inside a session; when it creates a
// session, the new session's frozen identity records the creator session's
// current launch id. That creation-time snapshot is what the delegated scoping
// rule later reads to recognize "this agent created that session".
func TestRunRecordsCreatorLaunchIDForDelegatedActor(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := principals.IssueAgentCredential(p.ID, "launch-parent", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	parent := &pb.Session{Id: "s-parent", Name: "parent", LaunchId: "launch-parent"}
	srv, stream := newRunScopeHub(t, principals, []*pb.Session{parent})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-amux-actor-token", cred.Token,
		"x-amux-request-id", "run-child-1",
	))
	resp, err := srv.Run(ctx, &pb.AmuxdRunRequest{TargetId: "t1", Name: "child"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.Session == nil || resp.Session.Id != "s-child" {
		t.Fatalf("Run response = %+v, want the canned child session", resp)
	}
	frozen := stream.lastRunRequest()
	if frozen == nil || frozen.ExecutionIdentity == nil {
		t.Fatalf("routed RunRequest = %+v, want a frozen execution identity", frozen)
	}
	if got := frozen.ExecutionIdentity.ParentExecutionId; got != "launch-parent" {
		t.Fatalf("ParentExecutionId = %q, want the creator session launch id %q", got, "launch-parent")
	}
}

// An enrollment actor is a human CLI caller rather than an agent inside a
// session, so nothing records a creator on the sessions it launches.
func TestRunLeavesParentExecutionIDEmptyForEnrollmentActor(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := principals.CreateEnrollment(principal.ProviderCLI, "local", "cli-caller", p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	parent := &pb.Session{Id: "s-parent", Name: "parent", LaunchId: "launch-parent"}
	srv, stream := newRunScopeHub(t, principals, []*pb.Session{parent})

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-amux-actor-token", enrollment.Token,
		"x-amux-request-id", "run-child-2",
	))
	if _, err := srv.Run(ctx, &pb.AmuxdRunRequest{TargetId: "t1", Name: "child"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	frozen := stream.lastRunRequest()
	if frozen == nil || frozen.ExecutionIdentity == nil {
		t.Fatalf("routed RunRequest = %+v, want a frozen execution identity", frozen)
	}
	if got := frozen.ExecutionIdentity.ParentExecutionId; got != "" {
		t.Fatalf("ParentExecutionId = %q, want empty for an enrollment actor", got)
	}
}

func TestRunMintsSessionCredentialThatSurvivesLaunchRotation(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := principals.CreateEnrollment(principal.ProviderCLI, "local", "cli-caller", p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	srv, stream := newRunScopeHub(t, principals, nil)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-amux-actor-token", enrollment.Token,
		"x-amux-request-id", "run-stable-credential",
	))
	if _, err := srv.Run(ctx, &pb.AmuxdRunRequest{TargetId: "t1", Name: "work"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	routed := stream.lastRunRequest()
	if routed == nil || routed.AgentActorToken == "" {
		t.Fatalf("routed RunRequest = %+v, want an agent actor credential", routed)
	}
	actor, err := principals.AuthenticateActor(routed.AgentActorToken, "after-local-relaunch")
	if err != nil {
		t.Fatalf("authenticate session credential: %v", err)
	}
	if actor.Subject != "relaunch:t1:work" {
		t.Fatalf("credential subject = %q, want stable session subject", actor.Subject)
	}
	rotated := []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{
		Id: "s-work", Name: "work", LaunchId: "launch-after-service-restart",
	}}}}
	visible := filterTargetsBySessionAccess(rotated, &actor, true)
	if len(visible) != 1 || len(visible[0].Sessions) != 1 || visible[0].Sessions[0].Id != "s-work" {
		t.Fatalf("visible after launch rotation = %+v, want s-work", visible)
	}
}

// ── Session principal authorization for mutations and listing ───────────────

// scopedIdentifiedSession builds a live session carrying a frozen identified
// identity of principalID, optionally recording parentExecutionID as its
// creator launch id.
func scopedIdentifiedSession(id, name, launchID, principalID, parentExecutionID string) *pb.Session {
	return &pb.Session{
		Id: id, Name: name, LaunchId: launchID,
		ExecutionIdentity: principal.ToProtoIdentity(principal.ExecutionIdentity{
			IdentityMode:           principal.IdentityModeIdentified,
			OriginActorPrincipalID: principalID,
			EffectivePrincipalID:   principalID,
			ParentExecutionID:      parentExecutionID,
		}),
	}
}

func actorTokenCtx(token, requestID string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-amux-actor-token", token,
		"x-amux-request-id", requestID,
	))
}

// newScopeMutationFixture builds a hub with one target hosting three sessions
// of one principal: the session the delegated actor runs in (own, launch id
// launch-own), a session that actor created (created, parent launch-own), and
// a same-principal session the actor neither owns nor created (other). The
// registered target declares an isolation helper capability, so principal
// authorization is active.
func newScopeMutationFixture(t *testing.T) (*hubServer, *principal.Store, string, principal.AgentCredential) {
	t.Helper()
	return newScopeMutationFixtureOn(t, newRunScopeHubIsolating)
}

// newScopeMutationFixtureNoIsolation builds the same sessions on a target
// without isolation capabilities, where the control plane remains shared.
func newScopeMutationFixtureNoIsolation(t *testing.T) (*hubServer, *principal.Store, string, principal.AgentCredential) {
	t.Helper()
	return newScopeMutationFixtureOn(t, newRunScopeHub)
}

func newScopeMutationFixtureOn(t *testing.T, hubFn func(*testing.T, *principal.Store, []*pb.Session) (*hubServer, *runScopeHarness)) (*hubServer, *principal.Store, string, principal.AgentCredential) {
	t.Helper()
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := principals.IssueAgentCredential(p.ID, "launch-own", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sessions := []*pb.Session{
		scopedIdentifiedSession("s-own", "own", "launch-own", p.ID, ""),
		scopedIdentifiedSession("s-created", "created", "launch-created", p.ID, "launch-own"),
		scopedIdentifiedSession("s-other", "other", "launch-other", p.ID, ""),
	}
	srv, _ := hubFn(t, principals, sessions)
	return srv, principals, p.ID, cred
}

func newCrossPrincipalSessionFixture(t *testing.T, hubFn func(*testing.T, *principal.Store, []*pb.Session) (*hubServer, *runScopeHarness)) (*hubServer, principal.AgentCredential) {
	t.Helper()
	principals := agentTokenTestStore(t)
	caller, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "caller"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := principals.IssueAgentCredential(caller.ID, "launch-caller", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := hubFn(t, principals, []*pb.Session{
		scopedIdentifiedSession("s-caller", "caller", "launch-caller", caller.ID, ""),
		scopedIdentifiedSession("s-foreign", "foreign", "launch-foreign", foreign.ID, ""),
	})
	return srv, cred
}

// Linux DAC isolates principals, not sibling sessions. A delegated actor can
// therefore operate another session owned by the same principal.
func TestDelegatedActorCanKillSamePrincipalSiblingWithIsolation(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	resp, err := srv.Kill(actorTokenCtx(cred.Token, "kill-other"), &pb.AmuxdKillRequest{TargetId: "t1", Name: "other"})
	if err != nil {
		t.Fatalf("Kill same-principal sibling: %v", err)
	}
	if resp == nil || !resp.Ok {
		t.Fatalf("Kill same-principal sibling response = %+v, want Ok", resp)
	}
}

// Without an isolating runtime the control plane remains shared.
func TestDelegatedActorCanKillOthersSessionWithoutIsolation(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixtureNoIsolation(t)
	resp, err := srv.Kill(actorTokenCtx(cred.Token, "kill-other"), &pb.AmuxdKillRequest{TargetId: "t1", Name: "other"})
	if err != nil {
		t.Fatalf("Kill other without isolation: %v", err)
	}
	if resp == nil || !resp.Ok {
		t.Fatalf("Kill other without isolation response = %+v, want Ok", resp)
	}
}

func TestDelegatedActorCanKillForeignPrincipalSessionWithoutIsolation(t *testing.T) {
	srv, cred := newCrossPrincipalSessionFixture(t, newRunScopeHub)

	resp, err := srv.Kill(actorTokenCtx(cred.Token, "kill-foreign-no-isolation"), &pb.AmuxdKillRequest{TargetId: "t1", Name: "foreign"})
	if err != nil {
		t.Fatalf("Kill foreign principal session without isolation: %v", err)
	}
	if resp == nil || !resp.Ok {
		t.Fatalf("Kill foreign principal session response = %+v, want Ok", resp)
	}
}

func TestDelegatedActorCannotKillForeignPrincipalSessionWithIsolation(t *testing.T) {
	srv, cred := newCrossPrincipalSessionFixture(t, newRunScopeHubIsolating)

	_, err := srv.Kill(actorTokenCtx(cred.Token, "kill-foreign-isolation"), &pb.AmuxdKillRequest{TargetId: "t1", Name: "foreign"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Kill foreign principal session with isolation = %v, want PermissionDenied", err)
	}
}

func TestDelegatedActorCanChangeForeignPrincipalCwdWithoutIsolation(t *testing.T) {
	srv, cred := newCrossPrincipalSessionFixture(t, newRunScopeHub)
	resp, err := srv.ChangeCwd(actorTokenCtx(cred.Token, "cwd-foreign-no-isolation"), &pb.AmuxdChangeCwdRequest{
		TargetId: "t1", Name: "foreign", Cwd: "/shared",
	})
	if err != nil {
		t.Fatalf("ChangeCwd foreign principal session without isolation: %v", err)
	}
	if resp == nil || !resp.Changed || resp.EffectiveCwd != "/shared" {
		t.Fatalf("ChangeCwd foreign principal session response = %+v", resp)
	}
}

func TestDelegatedActorCannotChangeForeignPrincipalCwdWithIsolation(t *testing.T) {
	srv, cred := newCrossPrincipalSessionFixture(t, newRunScopeHubIsolating)
	_, err := srv.ChangeCwd(actorTokenCtx(cred.Token, "cwd-foreign-isolation"), &pb.AmuxdChangeCwdRequest{
		TargetId: "t1", Name: "foreign", Cwd: "/shared",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ChangeCwd foreign principal session with isolation = %v, want PermissionDenied", err)
	}
}

func TestSessionActionRejectsInvalidCredentialWithoutIsolation(t *testing.T) {
	srv, _, _, _ := newScopeMutationFixtureNoIsolation(t)
	_, err := srv.Kill(actorTokenCtx("invalid-token", "kill-invalid-token"), &pb.AmuxdKillRequest{TargetId: "t1", Name: "other"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Kill with invalid credential without isolation = %v, want Unauthenticated", err)
	}
}

func TestDelegatedActorCanKillSessionItCreated(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	resp, err := srv.Kill(actorTokenCtx(cred.Token, "kill-created"), &pb.AmuxdKillRequest{TargetId: "t1", Name: "created"})
	if err != nil {
		t.Fatalf("Kill created: %v", err)
	}
	if resp == nil || !resp.Ok {
		t.Fatalf("Kill created response = %+v, want Ok", resp)
	}
}

func TestDelegatedActorCanKillItsOwnSession(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	resp, err := srv.Kill(actorTokenCtx(cred.Token, "kill-own"), &pb.AmuxdKillRequest{TargetId: "t1", Name: "own"})
	if err != nil {
		t.Fatalf("Kill own: %v", err)
	}
	if resp == nil || !resp.Ok {
		t.Fatalf("Kill own response = %+v, want Ok", resp)
	}
}

// Enrollment actors can operate identified sessions authorized by the
// principal policy.
func TestEnrollmentActorCanKillAnyIdentifiedSession(t *testing.T) {
	srv, principals, pid, _ := newScopeMutationFixture(t)
	enrollment, err := principals.CreateEnrollment(principal.ProviderCLI, "local", "cli-caller", pid, false)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Kill(actorTokenCtx(enrollment.Token, "kill-other"), &pb.AmuxdKillRequest{TargetId: "t1", Name: "other"})
	if err != nil {
		t.Fatalf("Kill as enrollment: %v", err)
	}
	if resp == nil || !resp.Ok {
		t.Fatalf("Kill as enrollment response = %+v, want Ok", resp)
	}
}

// The session list is filtered hub-side by principal. Same-principal siblings
// remain visible across targets; foreign-principal sessions do not.
func TestListTargetsFiltersDelegatedActorSessionsByPrincipal(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := principals.IssueAgentCredential(p.ID, "launch-own", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	targets := []*pb.Target{
		{Id: "t1", Name: "box", Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"}, Sessions: []*pb.Session{
			scopedIdentifiedSession("s-own", "own", "launch-own", p.ID, ""),
			scopedIdentifiedSession("s-created", "created", "launch-created", p.ID, "launch-own"),
			scopedIdentifiedSession("s-other", "other", "launch-other", p.ID, ""),
			scopedIdentifiedSession("s-foreign", "foreign", "launch-foreign", foreign.ID, ""),
		}},
		{Id: "t2", Name: "elsewhere", Sessions: []*pb.Session{
			scopedIdentifiedSession("s-remote", "remote", "launch-remote", p.ID, ""),
		}},
	}
	srv := &hubServer{principals: principals, listTargets: func() []*pb.Target { return targets }}

	resp, err := srv.ListTargets(actorTokenCtx(cred.Token, "list-1"), &pb.ListTargetsRequest{})
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(resp.Targets) != 2 {
		t.Fatalf("ListTargets targets = %d, want 2 (both targets stay discoverable)", len(resp.Targets))
	}
	got := map[string][]string{}
	for _, target := range resp.Targets {
		for _, sess := range target.Sessions {
			got[target.Id] = append(got[target.Id], sess.Name)
		}
	}
	if len(got["t1"]) != 3 || got["t1"][0] != "own" || got["t1"][1] != "created" || got["t1"][2] != "other" {
		t.Fatalf("t1 sessions = %v, want [own created other]", got["t1"])
	}
	if len(got["t2"]) != 1 || got["t2"][0] != "remote" {
		t.Fatalf("t2 sessions = %v, want [remote]", got["t2"])
	}
}

// On a fleet with no isolating runtime the narrow is inactive: a delegated
// actor (for example an agent inside a Feishu session listing the fleet's
// sessions) gets the full list, exactly like an enrollment actor.
func TestListTargetsReturnsEverythingForDelegatedActorWithoutIsolation(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := principals.IssueAgentCredential(p.ID, "launch-own", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	targets := []*pb.Target{
		{Id: "t1", Name: "box", Sessions: []*pb.Session{
			scopedIdentifiedSession("s-own", "own", "launch-own", p.ID, ""),
			scopedIdentifiedSession("s-other", "other", "launch-other", p.ID, ""),
		}},
		{Id: "t2", Name: "elsewhere", Sessions: []*pb.Session{
			scopedIdentifiedSession("s-remote", "remote", "launch-remote", p.ID, ""),
		}},
	}
	srv := &hubServer{principals: principals, listTargets: func() []*pb.Target { return targets }}

	resp, err := srv.ListTargets(actorTokenCtx(cred.Token, "list-no-isolation"), &pb.ListTargetsRequest{})
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(resp.Targets) != 2 {
		t.Fatalf("ListTargets targets = %d, want 2", len(resp.Targets))
	}
	got := map[string][]string{}
	for _, target := range resp.Targets {
		for _, sess := range target.Sessions {
			got[target.Id] = append(got[target.Id], sess.Name)
		}
	}
	if len(got["t1"]) != 2 || len(got["t2"]) != 1 {
		t.Fatalf("sessions = %v, want the full list on both targets", got)
	}
}

func TestListTargetsReturnsEverythingForEnrollmentActor(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := principals.CreateEnrollment(principal.ProviderCLI, "local", "cli-caller", p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	targets := []*pb.Target{{Id: "t1", Sessions: []*pb.Session{
		scopedIdentifiedSession("s-own", "own", "launch-own", p.ID, ""),
		scopedIdentifiedSession("s-other", "other", "launch-other", p.ID, ""),
	}}}
	srv := &hubServer{principals: principals, listTargets: func() []*pb.Target { return targets }}

	resp, err := srv.ListTargets(actorTokenCtx(enrollment.Token, "list-2"), &pb.ListTargetsRequest{})
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(resp.Targets) != 1 || len(resp.Targets[0].Sessions) != 2 {
		t.Fatalf("ListTargets = %+v, want both sessions for an enrollment actor", resp.Targets)
	}
}

// A caller with no actor token at all (the legacy local CLI path) keeps the
// pre-scoping behavior: the full list, no filter.
func TestListTargetsReturnsEverythingWithoutActor(t *testing.T) {
	principals := agentTokenTestStore(t)
	p, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	targets := []*pb.Target{{Id: "t1", Sessions: []*pb.Session{
		scopedIdentifiedSession("s-own", "own", "launch-own", p.ID, ""),
		scopedIdentifiedSession("s-other", "other", "launch-other", p.ID, ""),
	}}}
	srv := &hubServer{principals: principals, listTargets: func() []*pb.Target { return targets }}

	resp, err := srv.ListTargets(context.Background(), &pb.ListTargetsRequest{})
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(resp.Targets) != 1 || len(resp.Targets[0].Sessions) != 2 {
		t.Fatalf("ListTargets = %+v, want both sessions without an actor", resp.Targets)
	}
}

// ── Reads and prune get the same scoping as mutations ────────────────────────

// attachStreamFake is a minimal pb.Amuxd_AttachServer for hub-side Attach
// tests: it records delivered events and serves a fixed context.
type attachStreamFake struct {
	grpc.ServerStream
	ctx context.Context

	mu     sync.Mutex
	events []*pb.AttachEvent
}

func (f *attachStreamFake) Context() context.Context { return f.ctx }

func (f *attachStreamFake) Send(ev *pb.AttachEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *attachStreamFake) sentEvents() []*pb.AttachEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.AttachEvent(nil), f.events...)
}

// Attach uses the same principal authorization as mutations.
func TestAttachRequiresTimelineMode(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	stream := &attachStreamFake{ctx: actorTokenCtx(cred.Token, "attach-unspecified")}
	err := srv.Attach(&pb.AmuxdAttachRequest{TargetId: "t1", Name: "own"}, stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Attach unspecified timeline mode error = %v, want InvalidArgument", err)
	}
}

func TestDelegatedActorCanAttachSamePrincipalSibling(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	stream := &attachStreamFake{ctx: actorTokenCtx(cred.Token, "attach-other")}
	if err := srv.Attach(&pb.AmuxdAttachRequest{TargetId: "t1", Name: "other", TimelineMode: pb.AttachRequest_FOLLOW}, stream); err != nil {
		t.Fatalf("Attach same-principal sibling: %v", err)
	}
	events := stream.sentEvents()
	if len(events) != 1 || string(events[0].Data) != "attach-ok" {
		t.Fatalf("Attach sibling events = %+v, want the one canned frame", events)
	}
}

func TestDelegatedActorCanAttachOwnSession(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	stream := &attachStreamFake{ctx: actorTokenCtx(cred.Token, "attach-own")}
	if err := srv.Attach(&pb.AmuxdAttachRequest{TargetId: "t1", Name: "own", TimelineMode: pb.AttachRequest_FOLLOW}, stream); err != nil {
		t.Fatalf("Attach own: %v", err)
	}
	events := stream.sentEvents()
	if len(events) != 1 || string(events[0].Data) != "attach-ok" {
		t.Fatalf("Attach events = %+v, want the one canned frame", events)
	}
}

// QueueList reads one identified session's input queue and uses the same
// principal boundary as Attach.
func TestDelegatedActorCanQueueListSamePrincipalSibling(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	resp, err := srv.QueueList(actorTokenCtx(cred.Token, "queue-other"), &pb.AmuxdQueueListRequest{TargetId: "t1", Name: "other"})
	if err != nil {
		t.Fatalf("QueueList same-principal sibling: %v", err)
	}
	if resp == nil || len(resp.Items) != 1 || resp.Items[0].Id != "q1" {
		t.Fatalf("QueueList sibling = %+v, want the one canned item", resp)
	}
}

func TestDelegatedActorCanQueueListOwnSession(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	resp, err := srv.QueueList(actorTokenCtx(cred.Token, "queue-own"), &pb.AmuxdQueueListRequest{TargetId: "t1", Name: "own"})
	if err != nil {
		t.Fatalf("QueueList own: %v", err)
	}
	if resp == nil || len(resp.Items) != 1 || resp.Items[0].Id != "q1" {
		t.Fatalf("QueueList own = %+v, want the one canned item", resp)
	}
}

// An isolating fleet keeps fan-out prune enrollment-only because the runtime
// cannot attribute each deleted archived session to the delegated principal.
func TestDelegatedActorCannotPruneSessionsWithIsolation(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixture(t)
	_, err := srv.Prune(actorTokenCtx(cred.Token, "prune-delegated"), &pb.AmuxdPruneRequest{TargetId: "t1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Prune as delegated = %v, want PermissionDenied", err)
	}
}

func TestDelegatedActorCanPruneSessionsWithoutIsolation(t *testing.T) {
	srv, _, _, cred := newScopeMutationFixtureNoIsolation(t)
	resp, err := srv.Prune(actorTokenCtx(cred.Token, "prune-delegated-no-isolation"), &pb.AmuxdPruneRequest{TargetId: "t1"})
	if err != nil {
		t.Fatalf("Prune as delegated without isolation: %v", err)
	}
	if resp == nil || resp.Count != 1 || len(resp.Names) != 1 || resp.Names[0] != "archived-1" {
		t.Fatalf("Prune response = %+v, want the canned archived session", resp)
	}
}

func TestSessionPruneRequiresEnrollmentWithoutActor(t *testing.T) {
	srv, _, _, _ := newScopeMutationFixture(t)
	_, err := srv.Prune(context.Background(), &pb.AmuxdPruneRequest{TargetId: "t1"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Prune without actor = %v, want Unauthenticated", err)
	}
}

func TestSessionPruneRequiresAuthenticatedActorWithoutIsolation(t *testing.T) {
	srv, _, _, _ := newScopeMutationFixtureNoIsolation(t)
	_, err := srv.Prune(context.Background(), &pb.AmuxdPruneRequest{TargetId: "t1"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Prune without actor and without isolation = %v, want Unauthenticated", err)
	}
}

func TestEnrollmentActorCanPruneSessions(t *testing.T) {
	srv, principals, pid, _ := newScopeMutationFixture(t)
	enrollment, err := principals.CreateEnrollment(principal.ProviderCLI, "local", "cli-caller", pid, false)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Prune(actorTokenCtx(enrollment.Token, "prune-enrollment"), &pb.AmuxdPruneRequest{TargetId: "t1"})
	if err != nil {
		t.Fatalf("Prune as enrollment: %v", err)
	}
	if resp == nil || resp.Count != 1 || len(resp.Names) != 1 || resp.Names[0] != "archived-1" {
		t.Fatalf("Prune response = %+v, want the canned archived session", resp)
	}
}
