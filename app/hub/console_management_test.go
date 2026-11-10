package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/bridge"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type consoleTestController struct {
	calls        int
	save         bridge.ConsoleBotSave
	err          error
	resultStatus string
}

func (c *consoleTestController) ConsoleBot(context.Context) (bridge.ConsoleBot, error) {
	c.calls++
	return bridge.ConsoleBot{Profile: "default", ID: "feishu:test", Version: 7, Actions: []string{"get", "save"}}, c.err
}
func (c *consoleTestController) ConsoleSaveBot(_ context.Context, input bridge.ConsoleBotSave) (bridge.ConsoleBotResult, error) {
	c.calls++
	c.save = input
	state := c.resultStatus
	if state == "" {
		state = "success"
	}
	return bridge.ConsoleBotResult{Status: state, ConfirmationToken: "one-use-token", Bot: bridge.ConsoleBot{Profile: "default", Version: 8}}, c.err
}
func (c *consoleTestController) ConsoleGroups(context.Context) ([]bridge.ConsoleGroup, error) {
	c.calls++
	return []bridge.ConsoleGroup{{Profile: "default", ChatID: "oc_known", Actions: []string{"get", "save"}}}, c.err
}
func (c *consoleTestController) ConsoleGroupSettings(context.Context, string) (bridge.ConsoleGroupsResult, error) {
	c.calls++
	return bridge.ConsoleGroupsResult{Status: "success", Source: "known_bindings", Groups: []bridge.ConsoleGroup{}, Settings: &bridge.GroupSettings{Version: 3}}, c.err
}
func (c *consoleTestController) ConsoleSaveGroup(context.Context, string, *bridge.GroupSettings, int64) (bridge.ConsoleGroupsResult, error) {
	c.calls++
	return bridge.ConsoleGroupsResult{Status: "state_changed", Source: "known_bindings", Groups: []bridge.ConsoleGroup{}, Settings: &bridge.GroupSettings{Version: 4}}, c.err
}

func consoleTestServer(c *consoleTestController) *hubServer {
	return &hubServer{consoleManagementControllers: map[string]consoleManagementController{"default": c}}
}

func consoleTestRequest(resource, action, body string) *pb.ConsoleManagementRequest {
	return &pb.ConsoleManagementRequest{Resource: resource, Action: action, PayloadJson: []byte(body)}
}

func TestConsoleManagementActorDeniedBeforeDispatch(t *testing.T) {
	for _, tokens := range [][]string{{"agent-credential"}, {"enrollment-credential"}, {"", "invalid-credential"}} {
		c := &consoleTestController{}
		s := consoleTestServer(c) // no principal store: metadata must still be rejected
		md := metadata.MD{"x-amux-actor-token": tokens}
		_, err := s.ConsoleManagement(metadata.NewIncomingContext(context.Background(), md), nil)
		if status.Code(err) != codes.PermissionDenied || c.calls != 0 {
			t.Fatalf("tokens %v: err=%v calls=%d", tokens, err, c.calls)
		}
	}
}

func TestConsoleManagementTransportAuthAndUnknownMethod(t *testing.T) {
	c := &consoleTestController{}
	server := grpc.NewServer(grpc.UnaryInterceptor(hubUnaryAuthInterceptor("hub-secret")))
	pb.RegisterAmuxdServer(server, consoleTestServer(c))
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///console-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewAmuxdClient(conn)
	req := consoleTestRequest("bots", "list", "{}")
	if _, err := client.ConsoleManagement(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no hub token: %v", err)
	}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-amux-hub-token", "hub-secret", "x-amux-actor-token", "agent-token"))
	if _, err := client.ConsoleManagement(ctx, req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Agent with shared token: %v", err)
	}
	if c.calls != 0 {
		t.Fatal("denied caller reached Bridge")
	}
	if err := conn.Invoke(ctx, "/amux.v1.Amuxd/ConsoleExecute", req, &pb.ConsoleManagementResponse{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unknown RPC: %v", err)
	}
	ctx = metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-amux-hub-token", "hub-secret"))
	out, err := client.ConsoleManagement(ctx, req)
	if err != nil || out.Status != "success" || c.calls != 1 {
		t.Fatalf("operator result=%v err=%v calls=%d", out, err, c.calls)
	}
}

func TestConsoleManagementStrictWhitelistAndPayload(t *testing.T) {
	cases := []struct {
		resource, action, body string
		code                   codes.Code
	}{
		{"runtimes", "list", "{}", codes.InvalidArgument},
		{"bots", "execute", "{}", codes.InvalidArgument},
		{"bots", "list", `{"method":"Run"}`, codes.InvalidArgument},
		{"bots", "list", `{"profile":"default","profile":"other"}`, codes.InvalidArgument},
		{"bots", "list", `{} {}`, codes.InvalidArgument},
		{"bots", "list", `null`, codes.InvalidArgument},
		{"bots", "list", `[]`, codes.InvalidArgument},
		{"bots", "get", `{"profile":"../default"}`, codes.InvalidArgument},
		{"groups", "get", `{"profile":"default","chat_id":"oc_\u0000bad"}`, codes.InvalidArgument},
		{"bots", "save", `{"profile":"default","settings":{}}`, codes.InvalidArgument},
		{"bots", "save", `{"profile":"default","expected_version":0,"settings":{},"open_id":"owner"}`, codes.InvalidArgument},
		{"groups", "save", `{"profile":"default","chat_id":"oc_known","settings":{}}`, codes.InvalidArgument},
		{"bots", "list", strings.Repeat(" ", consoleManagementPayloadLimit+1), codes.ResourceExhausted},
		{"bots", "list", `{"profile":"missing"}`, codes.NotFound},
	}
	for _, tc := range cases {
		t.Run(tc.resource+"/"+tc.action+"/"+tc.body[:min(len(tc.body), 60)], func(t *testing.T) {
			c := &consoleTestController{}
			_, err := consoleTestServer(c).ConsoleManagement(context.Background(), consoleTestRequest(tc.resource, tc.action, tc.body))
			if status.Code(err) != tc.code || c.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, c.calls)
			}
		})
	}
}

func TestConsoleManagementBotTransitionStatusesAndFields(t *testing.T) {
	body := `{"profile":"default","expected_version":7,"settings":{"default_agent":"builtin/codex","default_runtime_ref":"codex","default_model_ref":"model","default_reasoning_effort":"high","interaction_mode":"thread","isolation_required":true},"confirmed":true,"confirmation_token":"token","request_id":"request","recovery_action":"create_blank","recovery_attempt_id":"attempt"}`
	for _, state := range []string{"success", "needs_confirmation", "needs_recovery_choice", "state_changed"} {
		c := &consoleTestController{resultStatus: state}
		out, err := consoleTestServer(c).ConsoleManagement(context.Background(), consoleTestRequest("bots", "save", body))
		if err != nil || out.Status != state {
			t.Fatalf("%s: out=%v err=%v", state, out, err)
		}
		var value bridge.ConsoleBotResult
		if json.Unmarshal(out.PayloadJson, &value) != nil || value.Status != state || value.ConfirmationToken != "one-use-token" {
			t.Fatalf("discarded status/token: %s", out.PayloadJson)
		}
		if c.save.ExpectedVersion != 7 || !c.save.Confirmed || c.save.RequestID != "request" || c.save.Settings.DefaultRuntimeRef != "codex" || !c.save.Settings.IsolationRequired || c.save.RecoveryAction != "create_blank" {
			t.Fatalf("lost typed fields: %+v", c.save)
		}
	}
}

func TestConsoleManagementGroupConflictAndSafeErrors(t *testing.T) {
	settings := bridge.GroupSettings{ChatID: "oc_known", InteractionMode: "thread", Mode: "shared", DefaultArgs: []string{}, Version: 3}
	body, _ := json.Marshal(consoleGroupSave{Profile: "default", ChatID: "oc_known", Settings: &settings})
	c := &consoleTestController{}
	out, err := consoleTestServer(c).ConsoleManagement(context.Background(), consoleTestRequest("groups", "save", string(body)))
	if err != nil || out.Status != "state_changed" || !strings.Contains(string(out.PayloadJson), `"version":4`) {
		t.Fatalf("conflict out=%v err=%v", out, err)
	}
	c.err = errors.New("secret-token internal /private/path environment-data")
	_, err = consoleTestServer(c).ConsoleManagement(context.Background(), consoleTestRequest("bots", "list", "{}"))
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != "console_management_failed" {
		t.Fatalf("unsafe error: %v", err)
	}
	c.err = status.Error(codes.Internal, "console_secret: credential-value")
	_, err = consoleTestServer(c).ConsoleManagement(context.Background(), consoleTestRequest("bots", "list", "{}"))
	if status.Convert(err).Message() != "console_management_failed" {
		t.Fatalf("unsafe prefixed error: %v", err)
	}
}
