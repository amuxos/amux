package modelservice

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type relayTestServer struct {
	pb.UnimplementedAmuxdServer
	requests chan *pb.ProxyModelRequest
	tokens   chan string
}

func TestPassthroughRequestFilterAppliesBeforeHubRelay(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &relayTestServer{requests: make(chan *pb.ProxyModelRequest, 1), tokens: make(chan string, 1)}
	server := grpc.NewServer()
	pb.RegisterAmuxdServer(server, fake)
	go server.Serve(lis)
	defer server.Stop()
	base, closeGateway, err := StartFilteredPassthroughGateway([]string{"AMUX_HUB_ADDR=" + lis.Addr().String()}, "test", func(r *http.Request) error {
		if r.Header.Get("Authorization") != "native" {
			return NewProxyError(http.StatusUnauthorized, "request rejected")
		}
		r.Header.Set("Authorization", "prepared")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGateway()
	client := &http.Client{Timeout: 5 * time.Second}
	for _, tc := range []struct {
		value string
		code  int
	}{{"invalid", 401}, {"native", 200}} {
		req, err := http.NewRequest(http.MethodGet, base+"/test", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", tc.value)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Fatalf("status = %d, want %d", resp.StatusCode, tc.code)
		}
	}
	if len(fake.requests) != 1 {
		t.Fatalf("Hub received %d requests", len(fake.requests))
	}
	if req := <-fake.requests; req.Headers["Authorization"] != "prepared" {
		t.Fatal("Hub did not receive the filtered headers")
	}
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
	base, key, closeGateway, err := StartGateway([]string{"AMUX_HUB_ADDR=" + lis.Addr().String(), "AMUX_ACTOR_TOKEN=launch-actor", "AMUX_ACTOR_TOKEN_FILE=" + tokenFile, "AMUX_MODEL_SERVICE_EFFORT=ultra", "AMUX_SESSION_ID=session"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer closeGateway()
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(auth string) *http.Response {
		t.Helper()
		r, _ := http.NewRequestWithContext(context.Background(), "POST", base+"/responses", strings.NewReader(`{"model":"test"}`))
		r.Header.Set("Authorization", auth)
		r.Header.Set("X-Session-Id", "client-requested")
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
	if req.Service != "test" || req.Path != "/v1/responses" || req.Headers["X-Amux-Reasoning-Effort"] != "ultra" || req.Headers["X-Session-Id"] != "session" {
		t.Fatalf("wrong relay metadata: %+v", req)
	}
	closeGateway()
	if _, err = client.Get(base + "/models"); err == nil {
		t.Fatal("gateway survived broker cleanup")
	}
}

type relayErrorTestServer struct {
	pb.UnimplementedAmuxdServer
	code codes.Code
}

func (s *relayErrorTestServer) ProxyModel(*pb.ProxyModelRequest, grpc.ServerStreamingServer[pb.ProxyModelChunk]) error {
	return status.Error(s.code, "private transport diagnostic")
}

func TestGatewayMapsModelServiceFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		grpcCode   codes.Code
		httpStatus int
		key        string
	}{
		{name: "login", grpcCode: codes.Unauthenticated, httpStatus: http.StatusUnauthorized, key: "login_required"},
		{name: "permission", grpcCode: codes.PermissionDenied, httpStatus: http.StatusForbidden, key: "access_denied"},
		{name: "invalid", grpcCode: codes.InvalidArgument, httpStatus: http.StatusBadRequest, key: "invalid_request"},
		{name: "missing", grpcCode: codes.NotFound, httpStatus: http.StatusNotFound, key: "not_found"},
		{name: "limit", grpcCode: codes.ResourceExhausted, httpStatus: http.StatusTooManyRequests, key: "rate_limited"},
		{name: "timeout", grpcCode: codes.DeadlineExceeded, httpStatus: http.StatusGatewayTimeout, key: "timed_out"},
		{name: "upstream", grpcCode: codes.Unavailable, httpStatus: http.StatusServiceUnavailable, key: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			pb.RegisterAmuxdServer(server, &relayErrorTestServer{code: tc.grpcCode})
			go server.Serve(lis)
			defer server.Stop()

			base, key, closeGateway, err := StartGateway([]string{
				"AMUX_HUB_ADDR=" + lis.Addr().String(),
				"AMUX_ACTOR_TOKEN=actor",
			}, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer closeGateway()
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/responses", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+key)
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			var payload struct{ Error struct{ Message string } }
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tc.httpStatus || strings.Contains(payload.Error.Message, "private") {
				t.Fatalf("response = %d %s, want %d with a safe diagnostic", response.StatusCode, body, tc.httpStatus)
			}
			for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese, "unsupported"} {
				message := errorCatalog.Format(locale, tc.key)
				if message == tc.key || !strings.Contains(payload.Error.Message, message) {
					t.Fatalf("missing %s diagnostic in %q", locale, payload.Error.Message)
				}
			}
		})
	}
}
