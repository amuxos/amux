package hub

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestSelectHubIPUsesRoutedHostForLoopbackListener(t *testing.T) {
	got := selectHubIP("127.0.0.1:7100", net.ParseIP("192.0.2.19"))
	if got != "192.0.2.19" {
		t.Fatalf("selectHubIP = %q, want routed host IP", got)
	}
}

func TestSelectHubIPPrefersExplicitListenerAndRejectsLocalOnlyCandidates(t *testing.T) {
	if got := selectHubIP("10.0.0.8:7100", net.ParseIP("192.0.2.19")); got != "10.0.0.8" {
		t.Fatalf("selectHubIP explicit listener = %q, want listener IP", got)
	}
	if got := selectHubIP("0.0.0.0:7100", net.ParseIP("127.0.0.1"), net.ParseIP("192.0.2.19")); got != "192.0.2.19" {
		t.Fatalf("selectHubIP candidate fallback = %q, want usable host IP", got)
	}
	if got := selectHubIP("[::1]:7100", net.ParseIP("127.0.0.1"), net.IPv4zero); got != "" {
		t.Fatalf("selectHubIP local-only candidates = %q, want empty", got)
	}
}

func TestStartCommandUsesHubTerminology(t *testing.T) {
	cmd := newStartCmd()
	short := strings.ToLower(cmd.Short)
	if !strings.Contains(short, "hub") {
		t.Fatalf("start short should mention hub: %q", cmd.Short)
	}
	if strings.Contains(short, "orchestrator") {
		t.Fatalf("start short should not mention orchestrator: %q", cmd.Short)
	}
}

func TestHubLogMessagesUseHubTerminology(t *testing.T) {
	for _, text := range []string{
		hubListeningMessage("127.0.0.1:7000"),
		hubShuttingDownMessage(),
		hubBridgeStartingMessage("default", "cli_xxx"),
	} {
		if !strings.Contains(strings.ToLower(text), "hub") {
			t.Fatalf("message should mention hub: %q", text)
		}
		if strings.Contains(strings.ToLower(text), "amuxd") || strings.Contains(strings.ToLower(text), "orchestrator") {
			t.Fatalf("message should not expose old terminology: %q", text)
		}
	}
}

func TestHubBridgeMissingCredentialsMessageNamesEmptyFields(t *testing.T) {
	t.Setenv("AMUX_HOME", filepath.Join(t.TempDir(), "amux-home"))
	if got := hubBridgeMissingCredentialsMessage("default", &bridge.Secrets{AppId: "cli_x", AppSecret: "secret"}); got != "" {
		t.Fatalf("complete credentials message = %q, want empty", got)
	}
	wantPath := filepath.Join(os.Getenv("AMUX_HOME"), "secrets", "feishu", "default.json")
	for _, tc := range []struct {
		name    string
		secrets *bridge.Secrets
		want    string
	}{
		{name: "both", secrets: &bridge.Secrets{OpenId: "ou_owner"}, want: "empty app_id, app_secret"},
		{name: "app_secret", secrets: &bridge.Secrets{AppId: "cli_x"}, want: "empty app_secret"},
		{name: "nil", secrets: nil, want: "empty app_id, app_secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hubBridgeMissingCredentialsMessage("default", tc.secrets)
			for _, part := range []string{"hub:", "profile default", "skipped", tc.want, wantPath} {
				if !strings.Contains(got, part) {
					t.Fatalf("message %q does not contain %q", got, part)
				}
			}
		})
	}
}

func TestFeishuExecutionIdentityResolverScopesActorAndEnforcesSharedMembership(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	human, _ := store.Create(principal.Principal{
		Kind: principal.KindHuman, DisplayName: "human", EnvironmentTargetID: "target-a",
	})
	service, _ := store.Create(principal.Principal{
		Kind: principal.KindService, DisplayName: "shared bot", EnvironmentTargetID: "target-a",
	})
	bridgeID := "feishu:cli_test"
	if err := store.BindAlias(principal.Alias{
		Provider: principal.ProviderFeishu, Scope: bridgeID, Subject: "ou_sender", PrincipalID: human.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.BindAlias(principal.Alias{
		Provider: principal.ProviderFeishu, Scope: bridgeID, Subject: "service:default", PrincipalID: service.ID,
	}); err != nil {
		t.Fatal(err)
	}
	resolve := newFeishuLaunchIdentityResolver(store, bridgeID, "default", true)
	personal, err := resolve("ou_sender", "feishu-session:1", "target-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if personal.Identity.EffectivePrincipalId != human.ID {
		t.Fatalf("personal effective principal = %q, want %q", personal.Identity.EffectivePrincipalId, human.ID)
	}
	if _, err := resolve("ou_sender", "feishu-singleton:1", "target-a", true); err == nil {
		t.Fatal("shared identity bypassed service membership")
	}
	if err := store.GrantServiceMember(service.ID, human.ID); err != nil {
		t.Fatal(err)
	}
	shared, err := resolve("ou_sender", "feishu-singleton:2", "target-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if shared.Identity.OriginActorPrincipalId != human.ID || shared.Identity.EffectivePrincipalId != service.ID {
		t.Fatalf("shared identity = %+v", shared.Identity)
	}
	if _, err := resolve("ou_sender", "feishu-session:wrong-target", "target-b", false); err == nil {
		t.Fatal("Feishu identified launch ignored principal hard placement")
	}
	// The same open_id under another bot is a different actor: it must not
	// inherit this scope's principal. Auto-enrollment mints a separate one
	// rather than crossing the scope boundary.
	otherScope := newFeishuLaunchIdentityResolver(store, "feishu:other_app", "default", true)
	crossed, err := otherScope("ou_sender", "feishu-session:other", "target-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if crossed.Identity.EffectivePrincipalId == human.ID {
		t.Fatal("Feishu actor alias crossed bridge scope")
	}
}

func TestValidateHubListenSecurityAllowsLocalWithoutToken(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7100", "localhost:7100", "[::1]:7100"} {
		if err := validateHubListenSecurity(addr, config.HubAuthConfig{}); err != nil {
			t.Fatalf("validateHubListenSecurity(%q) = %v, want nil", addr, err)
		}
	}
}

func TestValidateHubListenSecurityRequiresTokenForDistributedAddr(t *testing.T) {
	for _, addr := range []string{":7100", "0.0.0.0:7100", "[::]:7100", "10.0.0.12:7100", "hub.internal:7100"} {
		err := validateHubListenSecurity(addr, config.HubAuthConfig{})
		if err == nil {
			t.Fatalf("validateHubListenSecurity(%q) = nil, want token error", addr)
		}
		if !strings.Contains(err.Error(), "requires hub.token") {
			t.Fatalf("validateHubListenSecurity(%q) error = %v, want hub.token guidance", addr, err)
		}
	}
}

func TestValidateHubListenSecurityAllowsDistributedAddrWithToken(t *testing.T) {
	if err := validateHubListenSecurity("0.0.0.0:7100", config.HubAuthConfig{SharedToken: "secret"}); err != nil {
		t.Fatalf("validateHubListenSecurity with token = %v", err)
	}
}

func TestValidateHubWebListenSecurity(t *testing.T) {
	if err := validateHubWebListenSecurity("127.0.0.1:7200", ""); err != nil {
		t.Fatalf("local web listener without token = %v", err)
	}
	err := validateHubWebListenSecurity("0.0.0.0:7200", "")
	if err == nil {
		t.Fatal("distributed web listener without token = nil, want error")
	}
	if !strings.Contains(err.Error(), "requires --web-token") {
		t.Fatalf("error = %v, want --web-token guidance", err)
	}
	if err := validateHubWebListenSecurity("0.0.0.0:7200", "viewer-secret"); err != nil {
		t.Fatalf("distributed web listener with token = %v", err)
	}
}

func TestAuthorizeHubRPCAllowsOpenLocalMode(t *testing.T) {
	if err := authorizeHubRPC(context.Background(), ""); err != nil {
		t.Fatalf("authorizeHubRPC open = %v", err)
	}
}

func TestAuthorizeHubRPCRequiresMetadataToken(t *testing.T) {
	if err := authorizeHubRPC(context.Background(), "secret"); err == nil {
		t.Fatal("authorizeHubRPC without token = nil, want error")
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-hub-token", "secret"))
	if err := authorizeHubRPC(ctx, "secret"); err != nil {
		t.Fatalf("authorizeHubRPC with metadata token = %v", err)
	}
	bearer := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer secret"))
	if err := authorizeHubRPC(bearer, "secret"); err != nil {
		t.Fatalf("authorizeHubRPC with bearer token = %v", err)
	}
}

func TestHubUnaryAuthInterceptor(t *testing.T) {
	interceptor := hubUnaryAuthInterceptor("secret")
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-amux-hub-token", "secret"))
	called := false
	_, err := interceptor(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/amux.test/Method"}, func(context.Context, interface{}) (interface{}, error) {
		called = true
		return "ok", nil
	})
	if err != nil || !called {
		t.Fatalf("interceptor err=%v called=%v, want success", err, called)
	}
}

// fakeGRPCServer lets us drive shutdownServer's graceful-then-hard sequencing.
// GracefulStop blocks until release is closed — either by finishGraceful (the
// drain completed) or by Stop (the hard-stop fallback fired).
type fakeGRPCServer struct {
	mu         sync.Mutex
	stopCalled bool
	released   bool
	release    chan struct{}
}

func newFakeGRPCServer() *fakeGRPCServer {
	return &fakeGRPCServer{release: make(chan struct{})}
}

func (f *fakeGRPCServer) GracefulStop() { <-f.release }

func (f *fakeGRPCServer) unblock() {
	if !f.released {
		f.released = true
		close(f.release)
	}
}

// finishGraceful models GracefulStop draining on its own (no hard stop).
func (f *fakeGRPCServer) finishGraceful() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unblock()
}

func (f *fakeGRPCServer) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalled = true
	f.unblock()
}

func (f *fakeGRPCServer) hardStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalled
}

func TestShutdownServerForcesHardStopWhenGracefulHangs(t *testing.T) {
	srv := newFakeGRPCServer()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		shutdownServer(srv, 50*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdownServer did not return after grace period")
	}
	if !srv.hardStopped() {
		t.Fatal("expected hard Stop fallback when GracefulStop hangs")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("returned before grace period: %s", elapsed)
	}
}

func TestShutdownServerReturnsOnGracefulCompletion(t *testing.T) {
	srv := newFakeGRPCServer()
	srv.finishGraceful() // drain completes promptly
	done := make(chan struct{})
	go func() {
		shutdownServer(srv, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdownServer did not return when GracefulStop completed")
	}
	if srv.hardStopped() {
		t.Fatal("hard Stop should not fire when GracefulStop completes within grace")
	}
}

// A Feishu sender nobody has bound yet gets their own principal on first
// contact. Refusing would mean every new colleague waits for an operator, and
// there is nothing safe to fall back to — reusing someone else's principal
// would hand them that person's HOME.
func TestFeishuExecutionIdentityResolverAutoEnrollsUnknownSender(t *testing.T) {
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bridgeID := "feishu:cli_test"
	resolve := newFeishuLaunchIdentityResolver(store, bridgeID, "default", true)

	first, err := resolve("ou_newcomer", "feishu-session:1", "target-a", false)
	if err != nil {
		t.Fatalf("unknown sender was refused: %v", err)
	}
	if first.Identity.EffectivePrincipalId == "" || first.Identity.IdentityMode != string(principal.IdentityModeIdentified) {
		t.Fatalf("identity = %+v, want an identified principal", first.Identity)
	}

	second, err := resolve("ou_newcomer", "feishu-session:2", "target-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Identity.EffectivePrincipalId != first.Identity.EffectivePrincipalId {
		t.Fatalf("second message minted a second principal (%s vs %s)",
			second.Identity.EffectivePrincipalId, first.Identity.EffectivePrincipalId)
	}
	principals, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(principals) != 1 {
		t.Fatalf("principals = %d, want exactly one for one sender", len(principals))
	}

	// The shared bot is one environment everybody uses; joining it stays an
	// explicit grant, so an unknown sender must still be refused there.
	if _, err := resolve("ou_stranger", "feishu-singleton:1", "target-a", true); err == nil {
		t.Fatal("singleton accepted a sender with no service principal configured")
	}
}
