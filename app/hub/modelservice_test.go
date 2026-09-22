package hub

import (
	"context"
	"github.com/amuxos/amux/internal/modelservice"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func init() {
	if err := modelservice.Register(modelservice.Service{ID: "test-authenticated-relay", Name: "Test", Query: func(context.Context, string, string) (modelservice.State, error) { return modelservice.State{}, nil }, Proxy: func(_ context.Context, id string, w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/plain")
		_, err := io.WriteString(w, id)
		return err
	}}); err != nil {
		panic(err)
	}
}
func TestModelServiceTransportBindsActorNotRequestAndRequiresHubAuth(t *testing.T) {
	store := agentTokenTestStore(t)
	alice, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.IssueAgentCredential(alice.ID, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.IssueAgentCredential(bob.ID, "b", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.StreamInterceptor(hubStreamAuthInterceptor("transport-key")))
	pb.RegisterAmuxdServer(server, &hubServer{principals: store})
	go server.Serve(lis)
	defer server.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewAmuxdClient(conn)
	for _, tc := range []struct {
		token, hub, want string
		code             codes.Code
	}{{a.Token, "transport-key", alice.ID, codes.OK}, {b.Token, "transport-key", bob.ID, codes.OK}, {"", "transport-key", "", codes.Unauthenticated}, {a.Token, "", "", codes.Unauthenticated}, {"invalid", "transport-key", "", codes.Unauthenticated}} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ctx = metadata.AppendToOutgoingContext(ctx, "x-amux-actor-token", tc.token, "x-amux-hub-token", tc.hub)
		stream, err := client.ProxyModel(ctx, &pb.ProxyModelRequest{Service: "test-authenticated-relay", Method: "POST", Path: "/v1/responses", Body: []byte(`{"principal":"forged"}`)})
		var body string
		if err == nil {
			for {
				var chunk *pb.ProxyModelChunk
				chunk, err = stream.Recv()
				if err != nil {
					break
				}
				body += string(chunk.Body)
			}
		}
		cancel()
		if err == io.EOF {
			err = nil
		}
		if status.Code(err) != tc.code || body != tc.want {
			t.Fatalf("expected code %v and principal %q; got %v / %q", tc.code, tc.want, err, body)
		}
	}
}
