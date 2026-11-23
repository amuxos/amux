package hub

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/modelservice"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

func TestPassthroughGatewayForwardsCustomPathAndAuthorization(t *testing.T) {
	const svcID = "test-passthrough-relay"
	var gotPath, gotAuth, gotSession string
	if _, ok := modelservice.Get(svcID); !ok {
		if err := modelservice.Register(modelservice.Service{
			ID:           svcID,
			Name:         "Passthrough",
			AllowedPaths: []string{"/api/custom/chat"},
			Query:        func(context.Context, string, string) (modelservice.State, error) { return modelservice.State{}, nil },
			Proxy: func(_ context.Context, principalID string, w http.ResponseWriter, r *http.Request) error {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				gotSession = r.Header.Get("X-Session-Id")
				w.Header().Set("Content-Type", "text/plain")
				_, err := io.WriteString(w, "ok:"+principalID)
				return err
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	store := agentTokenTestStore(t)
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := store.IssueAgentCredential(alice.ID, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterAmuxdServer(server, &hubServer{principals: store})
	go server.Serve(lis)
	defer server.Stop()

	base, closeGW, err := modelservice.StartPassthroughGateway([]string{
		"AMUX_ACTOR_TOKEN=" + cred.Token,
		"AMUX_HUB_ADDR=" + lis.Addr().String(),
		"AMUX_SESSION_ID=sess-99",
	}, svcID)
	if err != nil {
		t.Fatalf("StartPassthroughGateway: %v", err)
	}
	defer closeGW()

	req, _ := http.NewRequest(http.MethodPost, base+"/api/custom/chat", http.NoBody)
	req.Header.Set("Authorization", "Custom-JWT token-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST passthrough: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok:"+alice.ID {
		t.Fatalf("resp = %d %q, want 200 ok:%s", resp.StatusCode, body, alice.ID)
	}
	if gotPath != "/api/custom/chat" || gotAuth != "Custom-JWT token-123" || gotSession != "sess-99" {
		t.Fatalf("forwarded = (%q, %q, %q)", gotPath, gotAuth, gotSession)
	}

	// Disallowed path must be rejected.
	badReq, _ := http.NewRequest(http.MethodPost, base+"/api/other/endpoint", http.NoBody)
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatalf("POST disallowed: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("disallowed path status = %d, want 400", badResp.StatusCode)
	}
}

func TestPassthroughGatewayAllowsLegacyHostWhenOptedIn(t *testing.T) {
	const svcID = "test-passthrough-legacy-host"
	if _, ok := modelservice.Get(svcID); !ok {
		if err := modelservice.Register(modelservice.Service{
			ID:              svcID,
			Name:            "Passthrough Legacy",
			AllowedPaths:    []string{"/api/custom/chat"},
			AllowLegacyHost: true,
			Query:           func(context.Context, string, string) (modelservice.State, error) { return modelservice.State{}, nil },
			Proxy: func(_ context.Context, principalID string, w http.ResponseWriter, r *http.Request) error {
				_, err := io.WriteString(w, "ok:"+principalID+":"+r.Header.Get("Authorization"))
				return err
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterAmuxdServer(server, &hubServer{principals: agentTokenTestStore(t)})
	go server.Serve(lis)
	defer server.Stop()

	// Legacy-host launches set AMUX_ACTOR_TOKEN_FILE to a non-existent path and no AMUX_ACTOR_TOKEN.
	base, closeGW, err := modelservice.StartPassthroughGateway([]string{
		"AMUX_ACTOR_TOKEN_FILE=/nonexistent/actor-token",
		"AMUX_HUB_ADDR=" + lis.Addr().String(),
		"AMUX_SESSION_ID=sess-legacy",
	}, svcID)
	if err != nil {
		t.Fatalf("StartPassthroughGateway: %v", err)
	}
	defer closeGW()

	req, _ := http.NewRequest(http.MethodPost, base+"/api/custom/chat", http.NoBody)
	req.Header.Set("Authorization", "Cloud-CLI-JWT legacy-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST legacy passthrough: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok:legacy-host:Cloud-CLI-JWT legacy-tok" {
		t.Fatalf("resp = %d %q, want 200 ok:legacy-host:Cloud-CLI-JWT legacy-tok", resp.StatusCode, body)
	}
}
