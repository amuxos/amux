package modelservice

import (
	"context"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type relayTestServer struct {
	pb.UnimplementedAmuxdServer
	requests chan *pb.ProxyModelRequest
	tokens   chan string
}

func (s *relayTestServer) ProxyModel(r *pb.ProxyModelRequest, stream grpc.ServerStreamingServer[pb.ProxyModelChunk]) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	s.tokens <- strings.Join(md.Get("x-amux-actor-token"), "")
	s.requests <- r
	if err := stream.Send(&pb.ProxyModelChunk{Status: 200, Headers: map[string]string{"Content-Type": "text/event-stream"}}); err != nil {
		return err
	}
	return stream.Send(&pb.ProxyModelChunk{Body: []byte("data: complete\n\n")})
}
func TestGatewayActorRotationStreamingAndLocalAuthentication(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &relayTestServer{requests: make(chan *pb.ProxyModelRequest, 2), tokens: make(chan string, 2)}
	server := grpc.NewServer()
	pb.RegisterAmuxdServer(server, fake)
	go server.Serve(lis)
	defer server.Stop()
	tokenFile := filepath.Join(t.TempDir(), "actor.json")
	if err = os.WriteFile(tokenFile, []byte(`{"token":"rotated-actor"}`), 0600); err != nil {
		t.Fatal(err)
	}
	base, key, closeGateway, err := StartGateway([]string{"AMUX_HUB_ADDR=" + lis.Addr().String(), "AMUX_ACTOR_TOKEN=launch-actor", "AMUX_ACTOR_TOKEN_FILE=" + tokenFile, "AMUX_MODEL_SERVICE_EFFORT=ultra"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer closeGateway()
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(auth string) *http.Response {
		t.Helper()
		r, _ := http.NewRequestWithContext(context.Background(), "POST", base+"/responses", strings.NewReader(`{"model":"test"}`))
		r.Header.Set("Authorization", auth)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	denied := call("")
	denied.Body.Close()
	if denied.StatusCode != 401 {
		t.Fatal(denied.StatusCode)
	}
	response := call("Bearer " + key)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || string(body) != "data: complete\n\n" {
		t.Fatalf("response %d: %s", response.StatusCode, body)
	}
	if token := <-fake.tokens; token != "rotated-actor" {
		t.Fatal("did not use refreshed actor")
	}
	req := <-fake.requests
	if req.Service != "test" || req.Path != "/v1/responses" || req.Headers["X-Amux-Reasoning-Effort"] != "ultra" {
		t.Fatalf("wrong relay metadata: %+v", req)
	}
	closeGateway()
	if _, err = client.Get(base + "/models"); err == nil {
		t.Fatal("gateway survived broker cleanup")
	}
}
