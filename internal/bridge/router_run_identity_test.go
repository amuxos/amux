package bridge

import (
	"context"
	"reflect"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestFeishuSessionIsolationSelectionStaysWithinScope(t *testing.T) {
	b, hub, _ := groupRouteTestBridge(t)
	b.SetIsolationSupportChecker(func() (bool, string) { return true, "" })
	global, err := b.currentBotProfileState()
	if err != nil {
		t.Fatal(err)
	}
	original := &pb.ExecutionIdentity{IdentityMode: "identified", EffectivePrincipalId: "principal-1"}
	b.SetExecutionIdentityResolver(func(string, string, string, bool) (LaunchIdentity, error) {
		return LaunchIdentity{Identity: original, LaunchID: "launch-1", ActorToken: "actor-1"}, nil
	})
	isolated := saveGroupRouteTestSettings(t, b, "isolated", config.FeishuInteractionThread)
	isolated.IsolationRequired = true
	if _, err := b.applyGroupSettings(context.Background(), isolated, isolated.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	saveGroupRouteTestSettings(t, b, "ordinary", config.FeishuInteractionThread)
	saveGroupRouteTestSettings(t, b, "singleton", config.FeishuInteractionSingleton)
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), "private", WorkspaceKindPrivate); err != nil {
		t.Fatal(err)
	}
	check := func(chatID, backend string) {
		t.Helper()
		if _, ok, err := b.runFeishuSession(context.Background(), "root-"+chatID, chatID, "owner", "owner", "local", "session-"+chatID, t.TempDir(), agentprofile.LaunchProjection{AgentType: "codex"}, nil); err != nil || !ok {
			t.Fatalf("%s launch ok=%v err=%v", chatID, ok, err)
		}
		runs := groupTestRuns(hub)
		run := runs[len(runs)-1]
		if got := run.ExecutionIdentity.GetIsolationBackend(); got != backend {
			t.Errorf("%s backend=%q, want %q", chatID, got, backend)
		}
		if run.ExecutionIdentity.GetEffectivePrincipalId() != original.EffectivePrincipalId || run.AgentActorToken != "actor-1" {
			t.Fatal("execution authorization identity or credential was lost")
		}
		if original.IsolationBackend != "" {
			t.Fatal("resolver identity was mutated across launches")
		}
	}
	check("isolated", "linux-dac")
	check("ordinary", "passthrough")
	check("singleton", "passthrough")
	check("private", "passthrough")
	check("inherited-group", "passthrough")
	after, err := b.currentBotProfileState()
	if err != nil || !reflect.DeepEqual(global, after) {
		t.Fatalf("group choice modified Bot defaults: before=%+v after=%+v err=%v", global, after, err)
	}
	// A group override also remains ordinary when the Bot default requires isolation.
	if _, err := b.db.CommitThreadBotSettingsWithModel(b.currentBridgeId(), after.Version, after.DefaultAgent, after.DefaultModelRef, true); err != nil {
		t.Fatal(err)
	}
	check("ordinary", "passthrough")
	check("singleton", "passthrough")
	check("private", "linux-dac")
	check("inherited-group", "linux-dac")
}

func TestBotSingletonLaunchSelectsPassthrough(t *testing.T) {
	b, hub, _ := groupRouteTestBridge(t)
	current, err := b.currentBotProfileState()
	if err != nil {
		t.Fatal(err)
	}
	b.SetExecutionIdentityResolver(func(_, _, _ string, shared bool) (LaunchIdentity, error) {
		if !shared {
			t.Fatal("singleton lost shared execution identity")
		}
		return LaunchIdentity{Identity: &pb.ExecutionIdentity{IdentityMode: "identified", EffectivePrincipalId: "service-1"}, LaunchID: "launch-1"}, nil
	})
	_, err = b.createAndCommitSingletonBotSettings(context.Background(), current, BotSettingsRequest{
		Interaction: config.FeishuInteractionSingleton, DefaultAgent: "builtin/codex", OpenID: "owner", ExpectedVersion: current.Version, RequestID: "scope-test",
	}, &pb.Target{Id: "local", Name: "local"}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	runs := groupTestRuns(hub)
	if len(runs) != 1 || runs[0].ExecutionIdentity.GetIsolationBackend() != "passthrough" {
		t.Fatalf("ordinary singleton inherited host isolation: %+v", runs)
	}
}

// A Feishu launch is routed straight at the runtime, so whatever the identity
// resolver produced has to be copied onto this request in full. Dropping the
// credential does not break the session — it breaks whatever that session's
// agent later creates, in a place that names neither.
func TestFeishuSessionRunCarriesTheResolvedLaunchIdentity(t *testing.T) {
	hub := &bridgeTestHub{}
	b := &Bridge{hub: hub, profile: "coder", bridgeID: "feishu:cli_coder", sessionStarter: sessionlaunch.New(hub)}
	want := LaunchIdentity{
		Identity:            &pb.ExecutionIdentity{EffectivePrincipalId: "p-1", IsolationBackend: "linux-dac"},
		LaunchID:            "launch-1",
		ActorToken:          "tok-1",
		ActorTokenExpiresAt: 1893456000,
	}
	b.SetExecutionIdentityResolver(func(openID, _ string, _ string, _ bool) (LaunchIdentity, error) {
		if openID != "ou_sender" {
			t.Fatalf("execution actor = %q, want ordinary message author", openID)
		}
		return want, nil
	})

	if _, ok, err := b.runFeishuSession(
		context.Background(), "msg-1", "chat-1", "ou_sender", "ou_sender", "target-1", "s1", "/tmp",
		agentprofile.LaunchProjection{AgentType: "codex"}, nil,
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
	if run.ExecutionIdentity.GetIsolationBackend() != "linux-dac" {
		t.Fatal("explicitly frozen execution backend was changed")
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
		context.Background(), "msg-iso", "chat-1", "ou_sender", "ou_sender", "target-1", "s1", "/tmp",
		agentprofile.LaunchProjection{AgentType: "codex"}, nil,
	); err != nil || ok {
		t.Fatalf("runFeishuSession ok=%v err=%v, want not-ok refuse (no legacy fallback)", ok, err)
	}
	for _, call := range hub.routes {
		if call.method == "Run" {
			t.Fatalf("Run RPC routed despite isolation-required with empty identity")
		}
	}
}

func TestFeishuSessionRunIsolationRequiredPinsNaturalIdentity(t *testing.T) {
	db, err := openBridgeDB(t.TempDir() + "/bridge.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := &bridgeTestHub{}
	b := &Bridge{hub: hub, db: db, profile: "coder", bridgeID: "feishu:cli_coder", sessionStarter: sessionlaunch.New(hub)}
	current, err := db.GetBotProfileState(b.bridgeID, config.FeishuInteractionThread)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitThreadBotSettingsWithModel(b.bridgeID, current.Version, "builtin/codex", "", true); err != nil {
		t.Fatal(err)
	}
	original := &pb.ExecutionIdentity{IdentityMode: "identified", EffectivePrincipalId: "principal-1"}
	b.SetExecutionIdentityResolver(func(string, string, string, bool) (LaunchIdentity, error) {
		return LaunchIdentity{Identity: original, LaunchID: "launch-1"}, nil
	})

	if _, ok, err := b.runFeishuSession(context.Background(), "msg", "chat", "user", "user", "target", "session", t.TempDir(), agentprofile.LaunchProjection{AgentType: "codex"}, nil); err != nil || !ok {
		t.Fatalf("runFeishuSession ok=%v err=%v", ok, err)
	}
	run := hub.routes[0].payload.(*pb.RunRequest)
	if run.ExecutionIdentity.GetIsolationBackend() != "linux-dac" {
		t.Fatalf("backend = %q, want linux-dac", run.ExecutionIdentity.GetIsolationBackend())
	}
	if original.GetIsolationBackend() != "" {
		t.Fatalf("resolver-owned identity was mutated: %+v", original)
	}
}

func TestFeishuSessionRunIsolationRequiredRejectsExplicitNonisolatedBackend(t *testing.T) {
	for _, backend := range []string{"passthrough", "unknown"} {
		t.Run(backend, func(t *testing.T) {
			db, err := openBridgeDB(t.TempDir() + "/bridge.db")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			hub := &bridgeTestHub{}
			b := &Bridge{hub: hub, db: db, profile: "coder", bridgeID: "feishu:cli_coder", sessionStarter: sessionlaunch.New(hub)}
			current, err := db.GetBotProfileState(b.bridgeID, config.FeishuInteractionThread)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.CommitThreadBotSettingsWithModel(b.bridgeID, current.Version, "builtin/codex", "", true); err != nil {
				t.Fatal(err)
			}
			b.threadNotice = func(context.Context, string, string) error { return nil }
			b.SetExecutionIdentityResolver(func(string, string, string, bool) (LaunchIdentity, error) {
				return LaunchIdentity{Identity: &pb.ExecutionIdentity{IdentityMode: "identified", EffectivePrincipalId: "principal-1", IsolationBackend: backend}, LaunchID: "launch-1"}, nil
			})
			if _, ok, err := b.runFeishuSession(context.Background(), "msg", "chat", "user", "user", "target", "session", t.TempDir(), agentprofile.LaunchProjection{AgentType: "codex"}, nil); err != nil || ok {
				t.Fatalf("runFeishuSession ok=%v err=%v", ok, err)
			}
			if len(hub.routes) != 0 {
				t.Fatalf("nonisolated backend %q reached runtime", backend)
			}
		})
	}
}
