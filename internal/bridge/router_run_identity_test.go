package bridge

import (
	"context"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// A Feishu launch is routed straight at the runtime, so whatever the identity
// resolver produced has to be copied onto this request in full. Dropping the
// credential does not break the session — it breaks whatever that session's
// agent later creates, in a place that names neither.
func TestFeishuSessionRunCarriesTheResolvedLaunchIdentity(t *testing.T) {
	hub := &bridgeTestHub{}
	b := &Bridge{hub: hub, profile: "coder", bridgeID: "feishu:cli_coder", sessionStarter: sessionlaunch.New(hub)}
	want := LaunchIdentity{
		Identity:            &pb.ExecutionIdentity{EffectivePrincipalId: "p-1"},
		LaunchID:            "launch-1",
		ActorToken:          "tok-1",
		ActorTokenExpiresAt: 1893456000,
	}
	b.SetExecutionIdentityResolver(func(string, string, string, bool) (LaunchIdentity, error) {
		return want, nil
	})

	if _, ok, err := b.runFeishuSession(
		context.Background(), "msg-1", "chat-1", "ou_sender", "target-1", "s1", "/tmp",
		agentprofile.LaunchProjection{AgentType: "codex"}, 0,
	); err != nil || !ok {
		t.Fatalf("runFeishuSession ok=%v err=%v", ok, err)
	}

	var run *pb.RunRequest
	for _, call := range hub.routes {
		if call.method == "Run" {
			run = call.payload.(*pb.RunRequest)
		}
	}
	if run == nil {
		t.Fatal("no Run RPC was routed")
	}
	if run.LaunchId != want.LaunchID || run.ExecutionIdentity.GetEffectivePrincipalId() != "p-1" {
		t.Fatalf("identity = %q/%+v, want %q/p-1", run.LaunchId, run.ExecutionIdentity, want.LaunchID)
	}
	if run.AgentActorToken != want.ActorToken {
		t.Fatalf("agent actor token = %q, want %q", run.AgentActorToken, want.ActorToken)
	}
	if run.AgentActorTokenExpiresAt != want.ActorTokenExpiresAt {
		t.Fatalf("token expiry = %d, want %d", run.AgentActorTokenExpiresAt, want.ActorTokenExpiresAt)
	}
}

// TestFeishuSessionRunIsolationRequiredRefusesAnonymousLaunch verifies that in
// 每个话题独立(用户隔离) mode an empty resolved identity (no isolation helper /
// resolver degraded) is refused instead of falling back to a legacy-host run.
func TestFeishuSessionRunIsolationRequiredRefusesAnonymousLaunch(t *testing.T) {
	db, err := openBridgeDB(t.TempDir() + "/bridge.db")
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	hub := &bridgeTestHub{}
	b := &Bridge{hub: hub, db: db, profile: "coder", bridgeID: "feishu:cli_coder", sessionStarter: sessionlaunch.New(hub)}
	current, err := db.GetBotProfileState(b.bridgeID, config.FeishuInteractionThread)
	if err != nil {
		t.Fatalf("GetBotProfileState: %v", err)
	}
	if _, err := db.CommitThreadBotSettingsWithModel(b.bridgeID, current.Version, "builtin/codex", "", true); err != nil {
		t.Fatalf("set isolation required: %v", err)
	}
	b.threadNotice = func(context.Context, string, string) error { return nil }
	// No identity resolver → identity is empty, which must be refused.
	b.SetIsolationSupportChecker(func() (bool, string) { return true, "" })

	if _, ok, err := b.runFeishuSession(
		context.Background(), "msg-iso", "chat-1", "ou_sender", "target-1", "s1", "/tmp",
		agentprofile.LaunchProjection{AgentType: "codex"}, 0,
	); err != nil || ok {
		t.Fatalf("runFeishuSession ok=%v err=%v, want not-ok refuse (no legacy fallback)", ok, err)
	}
	for _, call := range hub.routes {
		if call.method == "Run" {
			t.Fatalf("Run RPC routed despite isolation-required with empty identity")
		}
	}
}
