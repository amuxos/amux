package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/modelservice"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func init() {
	services := []modelservice.Service{
		{ID: "test-authenticated-relay", Name: "Test", Query: func(context.Context, string, string) (modelservice.State, error) { return modelservice.State{}, nil }, Proxy: func(_ context.Context, id string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/plain")
			_, err := io.WriteString(w, id)
			return err
		}},
		{ID: "test-unknown-model-error", Name: "Unknown", Query: func(context.Context, string, string) (modelservice.State, error) { return modelservice.State{}, nil }, Proxy: func(context.Context, string, http.ResponseWriter, *http.Request) error {
			return errors.New("private upstream diagnostic")
		}},
		{ID: "test-stream-model-error", Name: "Stream", Query: func(context.Context, string, string) (modelservice.State, error) { return modelservice.State{}, nil }, Proxy: func(_ context.Context, _ string, w http.ResponseWriter, _ *http.Request) error {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: partial\n\n")
			return modelservice.NewProxyError(401, "safe model failure\n安全模型错误")
		}},
	}
	for _, code := range []int{400, 401, 403, 404, 413, 429, 500, 502, 503, 504} {
		services = append(services, modelservice.Service{
			ID: fmt.Sprintf("test-classified-model-%d", code), Name: "Classified",
			Query: func(context.Context, string, string) (modelservice.State, error) { return modelservice.State{}, nil },
			Proxy: func(context.Context, string, http.ResponseWriter, *http.Request) error {
				return fmt.Errorf("private wrapper diagnostic: %w", modelservice.NewProxyError(code, "safe model failure\n安全模型错误"))
			},
		})
	}

	for _, service := range services {
		if err := modelservice.Register(service); err != nil {
			panic(err)
		}
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

func TestModelServiceGatewayPreservesClassifiedFailures(t *testing.T) {
	store := agentTokenTestStore(t)
	actor, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.IssueAgentCredential(actor.ID, "model-service-test", time.Hour)
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
	call := func(t *testing.T, service string) *http.Response {
		t.Helper()
		base, key, closeGateway, err := modelservice.StartGateway([]string{
			"AMUX_HUB_ADDR=" + lis.Addr().String(), "AMUX_HUB_TOKEN=transport-key", "AMUX_ACTOR_TOKEN=" + credential.Token,
		}, service)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeGateway)
		req, err := http.NewRequest(http.MethodPost, base+"/responses", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	for _, code := range []int{400, 401, 403, 404, 413, 429, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			response := call(t, fmt.Sprintf("test-classified-model-%d", code))
			defer response.Body.Close()
			var body struct {
				Error struct{ Type, Message string }
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != code || body.Error.Type != "model_service_error" || body.Error.Message != "safe model failure\n安全模型错误" {
				t.Fatalf("response = %d %+v", response.StatusCode, body)
			}
		})
	}
	t.Run("unclassified", func(t *testing.T) {
		response := call(t, "test-unknown-model-error")
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 503 || strings.Contains(string(body), "private") || !strings.Contains(string(body), "model service unavailable") || !strings.Contains(string(body), "模型服务暂时不可用") {
			t.Fatalf("response = %d %s", response.StatusCode, body)
		}
	})
	t.Run("stream-failure", func(t *testing.T) {
		response := call(t, "test-stream-model-error")
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err == nil || response.StatusCode != 200 || strings.Contains(string(body), "model_service_error") {
			t.Fatalf("response = %d %s, error = %v; want aborted stream", response.StatusCode, body, err)
		}
	})
}
