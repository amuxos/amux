package cli

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestConsoleExplicitAgentIdentityAlwaysRejected(t *testing.T) {
	consoleTestOperator(t)
	for _, key := range []string{"AMUX_ACTOR_TOKEN", "AMUX_ACTOR_TOKEN_FILE", "AMUX_SESSION_ID", "AMUX_TASK_ID", "AMUX_PRINCIPAL_ID"} {
		t.Setenv(key, "")
	}
	for _, key := range []string{"AMUX_ACTOR_TOKEN", "AMUX_ACTOR_TOKEN_FILE", "AMUX_SESSION_ID", "AMUX_TASK_ID", "AMUX_PRINCIPAL_ID"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "agent-context")
			if !consoleHasAgentContext() || consoleManagedParentVerified() {
				t.Fatal("Agent context accepted as managed operator")
			}
			b := &consoleBackend{managedOperator: true}
			_, err := b.snapshotResource(t.Context(), "credentials", nil)
			consoleTestCode(t, err, "operator_required")
			_, err = b.executeResource(t.Context(), "packs", "sync", json.RawMessage(`{"namespace":"team"}`))
			consoleTestCode(t, err, "operator_required")
		})
	}
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	if err := runConsoleWithOptions(cmd, consoleServeOptions{Addr: "127.0.0.1:0", Duration: time.Second, ManagedByHub: true}); err == nil {
		t.Fatal("private flag bypassed parent check")
	}
	if err := os.MkdirAll(filepath.Join(defaultAmuxDir(), "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultAmuxDir(), "secrets", "principal-enrollment.token"), []byte("enrolled-agent"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runConsole(cmd, "127.0.0.1:0"); err == nil {
		t.Fatal("ordinary enrollment caller started operator surface")
	}
}

type consoleHostIdentityHub struct {
	pb.UnimplementedAmuxdServer
	t *testing.T
}

func (s *consoleHostIdentityHub) ListTargets(ctx context.Context, _ *pb.ListTargetsRequest) (*pb.ListTargetsResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("x-amux-actor-token")) != 0 {
		s.t.Error("host enrollment leaked into operator RPC")
	}
	if got := md.Get("x-amux-hub-token"); len(got) != 1 || got[0] != "test-hub-auth" {
		s.t.Error("Hub authentication missing")
	}
	return &pb.ListTargetsResponse{}, nil
}

func TestConsoleOperatorConnectionDoesNotInheritEnrollment(t *testing.T) {
	consoleTestOperator(t)
	for _, key := range []string{"AMUX_SESSION_ID", "AMUX_TASK_ID", "AMUX_PRINCIPAL_ID"} {
		t.Setenv(key, "")
	}
	viper.Reset()
	t.Cleanup(viper.Reset)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterAmuxdServer(server, &consoleHostIdentityHub{t: t})
	go server.Serve(listener)
	defer server.Stop()
	t.Setenv("AMUX_HUB_ADDR", listener.Addr().String())
	t.Setenv("AMUX_HUB_TOKEN", "test-hub-auth")
	if err := os.MkdirAll(filepath.Join(defaultAmuxDir(), "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultAmuxDir(), "secrets", "principal-enrollment.token"), []byte("host-enrollment"), 0600); err != nil {
		t.Fatal(err)
	}
	if actorToken() != "host-enrollment" {
		t.Fatal("test did not reproduce enrollment fallback")
	}
	client, err := newConsoleOperatorHub(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.c.ListTargets(t.Context(), &pb.ListTargetsRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentialProfile("host-profile", cliCredentialFile{Kind: "long-lived", Env: map[string]string{"API_KEY": "private-host-secret"}}); err != nil {
		t.Fatal(err)
	}
	b := &consoleBackend{hub: client.c, managedOperator: true}
	value, err := b.snapshotResource(t.Context(), "credentials", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if !strings.Contains(string(raw), "host-profile") || strings.Contains(string(raw), "private-host-secret") || strings.Contains(string(raw), "path") {
		t.Fatal("credential metadata contract changed")
	}
	t.Setenv("AMUX_HUB_ADDR", "192.0.2.10:7100")
	if _, err := newConsoleOperatorHub(time.Second); err == nil {
		t.Fatal("managed operator dialed a remote Hub")
	}
}
