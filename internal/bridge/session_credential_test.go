package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/sessioncredential"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type credentialTestHub struct {
	*bridgeTestHub
	checks []sessioncredential.Action
}

func (h *credentialTestHub) Route(ctx context.Context, target, method string, args any) (*pb.TunnelMsg, error) {
	if method != sessioncredential.Route {
		return h.bridgeTestHub.Route(ctx, target, method, args)
	}
	request := args.(sessioncredential.Request)
	if request.Actor != (sessioncredential.FeishuActor{TenantKey: "tenant", UnionID: "on_owner"}) {
		return nil, errors.New("credential request missing original Feishu actor")
	}
	h.mu.Lock()
	h.checks = append(h.checks, request.Action)
	h.mu.Unlock()
	result := sessioncredential.Result{State: sessioncredential.LoginNeeded}
	if request.Action == sessioncredential.Begin {
		result.State, result.URL = sessioncredential.Pending, "https://auth.example.test/one-time"
	} else if request.Action == sessioncredential.Poll {
		result.State = sessioncredential.Ready
	}
	raw, _ := json.Marshal(result)
	return &pb.TunnelMsg{Payload: raw}, nil
}

func TestSessionCredentialGateIsIsolatedAndResumesOnce(t *testing.T) {
	b, base, _ := groupRouteTestBridge(t)
	hub := &credentialTestHub{bridgeTestHub: base}
	b.hub, b.sessionStarter = hub, sessionlaunch.New(hub)
	b.SetExecutionIdentityResolver(func(_, _, _ string, _ bool) (LaunchIdentity, error) {
		return LaunchIdentity{Identity: &pb.ExecutionIdentity{
			IdentityMode: "identified", EffectivePrincipalId: "p-1", OriginActorPrincipalId: "p-1", Source: "feishu",
		}, LaunchID: "launch-1"}, nil
	})
	isolated := saveGroupRouteTestSettings(t, b, "isolated", config.FeishuInteractionThread)
	isolated.IsolationRequired = true
	b.SetIsolationSupportChecker(func() (bool, string) { return true, "" })
	if _, err := b.applyGroupSettings(context.Background(), isolated, isolated.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	saveGroupRouteTestSettings(t, b, "ordinary", config.FeishuInteractionThread)

	render := func(view CredentialCardView) string {
		raw, _ := json.Marshal(view)
		return string(raw)
	}
	bridgeExtensions.mu.Lock()
	stored := bridgeExtensions.extension.SessionCredential
	bridgeExtensions.extension.SessionCredential = &CredentialAdmissionOptions{
		Render: render,
		Notice: func(_, key string) string { return key },
	}
	bridgeExtensions.mu.Unlock()
	t.Cleanup(func() {
		b.stopCredentialAdmissions()
		bridgeExtensions.mu.Lock()
		bridgeExtensions.extension.SessionCredential = stored
		bridgeExtensions.mu.Unlock()
	})

	updates := make(chan string, 4)
	replies := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"fake","expire":7200}`))
		case strings.HasSuffix(r.URL.Path, "/reply"):
			var payload struct{ Content string }
			_ = json.NewDecoder(r.Body).Decode(&payload)
			replies <- payload.Content
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"credential-card"}}`))
		case strings.HasSuffix(r.URL.Path, "/card/update"):
			raw := json.RawMessage{}
			_ = json.NewDecoder(r.Body).Decode(&raw)
			updates <- string(raw)
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	defer server.Close()
	defer b.stopCredentialAdmissions()
	b.client = lark.NewClient("test-isolated-admission", "test-secret", lark.WithOpenBaseUrl(server.URL))

	ctx := withCredentialSender(context.Background(), "owner", "tenant", "", "on_owner")
	if err := b.routeToSessionInChatWithMention(ctx, "ordinary", "ordinary-root", "ordinary-msg", "owner", "ordinary question", botMentionInfo{Mentioned: true}); err != nil {
		t.Fatal(err)
	}
	if len(hub.checks) != 0 || len(groupTestRuns(base)) != 1 {
		t.Fatalf("ordinary route used the gate: %#v", hub.checks)
	}

	if err := b.routeToSessionInChatWithMention(ctx, "isolated", "isolated-root", "isolated-msg", "owner", "original question", botMentionInfo{Mentioned: true}); err != nil {
		t.Fatal(err)
	}
	if len(hub.checks) != 1 || hub.checks[0] != sessioncredential.Probe || len(groupTestRuns(base)) != 1 {
		t.Fatal("isolated Session launched before credentials were checked")
	}
	select {
	case raw := <-replies:
		if strings.Contains(raw, "auth.example.test") {
			t.Fatal("initial card exposed a login URL")
		}
	default:
		t.Fatal("original topic has no login card")
	}
	b.credentialAdmissions.mu.Lock()
	pending := b.credentialAdmissions.byRoot["isolated-root"]
	b.credentialAdmissions.mu.Unlock()
	if pending == nil {
		t.Fatal("no pending request")
	}

	cardAction := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Token:    "exclusive-token",
		Operator: &callback.Operator{OpenID: "someone-else"},
		Context:  &callback.Context{OpenMessageID: "credential-card"},
		Action: &callback.CallBackAction{Value: map[string]any{
			"session_credential_id":     pending.id,
			"session_credential_action": "sign_in",
		}},
	}}
	if resp, _ := b.handleCredentialAdmissionAction(cardAction); resp == nil || resp.Toast.Content != "unauthorized" {
		t.Fatalf("another actor operated the login card: %+v", resp)
	}
	cardAction.Event.Operator.OpenID = "owner"
	if resp, _ := b.handleCredentialAdmissionAction(cardAction); resp == nil || resp.Toast.Content != "starting" {
		t.Fatalf("sign-in click was not acknowledged: %+v", resp)
	}
	cardAction.Event.Action.Value["session_credential_action"] = "check"
	if resp, _ := b.handleCredentialAdmissionAction(cardAction); resp == nil || resp.Toast.Content != "processing" {
		t.Fatalf("check during sign-in was not acknowledged: %+v", resp)
	}
	receive := func() string {
		t.Helper()
		select {
		case got := <-updates:
			return got
		case <-time.After(8 * time.Second):
			t.Fatal("no exclusive update")
			return ""
		}
	}
	first, second := receive(), receive()
	if !strings.Contains(first, `"open_ids":["owner"]`) || !strings.Contains(first, "auth.example.test/one-time") ||
		!strings.Contains(second, `"State":"continuing"`) || strings.Contains(second, "auth.example.test") {
		t.Fatalf("unexpected exclusive updates: %s / %s", first, second)
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	var started, submitted int
	for _, call := range base.routes {
		if call.method == "Run" && call.payload.(*pb.RunRequest).ExecutionIdentity.GetIsolationBackend() == "linux-dac" {
			started++
		}
		if call.method == "Submit" && strings.Contains(call.payload.(*pb.SubmitRequest).Text, "original question") {
			submitted++
		}
	}
	if started != 1 || submitted != 1 || len(hub.checks) != 3 {
		t.Fatalf("started=%d submitted=%d actions=%v", started, submitted, hub.checks)
	}
}
