package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/gorilla/websocket"
)

// fakeBackend is a minimal in-memory Backend for handler tests.
type fakeBackend struct {
	sessions    []*SessionEntry
	events      []*pb.AttachEvent
	lastRun     *pb.RunRequest
	pruneTarget string
	submits     []string
	lastSource  string
}

func (f *fakeBackend) List(context.Context) ([]*SessionEntry, error) { return f.sessions, nil }
func (f *fakeBackend) Run(_ context.Context, _ string, req *pb.RunRequest) (*pb.RunResponse, error) {
	f.lastRun = req
	return &pb.RunResponse{Session: &pb.Session{Name: req.Name, AgentType: req.AgentType, State: "created"}}, nil
}
func (f *fakeBackend) Kill(context.Context, string, string) error      { return nil }
func (f *fakeBackend) Restart(context.Context, string, string) error   { return nil }
func (f *fakeBackend) Interrupt(context.Context, string, string) error { return nil }
func (f *fakeBackend) Attach(context.Context, string, string) (pb.Amuxlet_AttachClient, error) {
	return &fakeAttachClient{events: f.events}, nil
}
func (f *fakeBackend) Submit(_ context.Context, _, _, source, text string, _ bool) error {
	f.lastSource = source
	f.submits = append(f.submits, text)
	return nil
}
func (f *fakeBackend) QueueList(context.Context, string, string) ([]*pb.QueueItem, error) {
	return nil, nil
}
func (f *fakeBackend) QueueClear(context.Context, string, string) (int32, error) { return 0, nil }
func (f *fakeBackend) QueueCancel(context.Context, string, string, string) error { return nil }
func (f *fakeBackend) RespondPermission(context.Context, string, string, string, string) error {
	return nil
}
func (f *fakeBackend) Prune(_ context.Context, targetID string) (int32, []string, error) {
	f.pruneTarget = targetID
	return 2, []string{"dead1", "dead2"}, nil
}

// fakeAttachClient replays a fixed slice of events then EOFs.
type fakeAttachClient struct {
	pb.Amuxlet_AttachClient
	events []*pb.AttachEvent
	i      int
}

func (c *fakeAttachClient) Recv() (*pb.AttachEvent, error) {
	if c.i >= len(c.events) {
		return nil, io.EOF
	}
	ev := c.events[c.i]
	c.i++
	return ev, nil
}

func newTestServer(b Backend, token string) *httptest.Server {
	return httptest.NewServer(New(b, token, "test").Handler())
}

func TestAuthRequired(t *testing.T) {
	srv := newTestServer(&fakeBackend{}, "secret")
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestCORSPreflightAndHeaders(t *testing.T) {
	srv := newTestServer(&fakeBackend{}, "secret")
	defer srv.Close()

	// Preflight must succeed without a token and advertise the needed headers.
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/run", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight want 204, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("missing ACAO header")
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("ACAH missing Authorization: %q", resp.Header.Get("Access-Control-Allow-Headers"))
	}

	// Actual GET response should also carry ACAO so the renderer can read it.
	body := doJSON(t, srv.URL+"/api/health", "secret", http.MethodGet, nil)
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("health body: %s", body)
	}
}

func TestSessionsAndRun(t *testing.T) {
	b := &fakeBackend{sessions: []*SessionEntry{{Session: &pb.Session{Name: "a", AgentType: "claude", State: "idle"}}}}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	// List
	body := doJSON(t, srv.URL+"/api/sessions", "secret", http.MethodGet, nil)
	if !strings.Contains(body, `"name":"a"`) || !strings.Contains(body, `"agent_type":"claude"`) {
		t.Fatalf("sessions body missing fields: %s", body)
	}

	// Run
	body = doJSON(t, srv.URL+"/api/run", "secret", http.MethodPost, map[string]any{"agent_type": "claude", "name": "t1"})
	if b.lastRun == nil || b.lastRun.Name != "t1" || b.lastRun.AgentType != "claude" {
		t.Fatalf("run not forwarded: %+v", b.lastRun)
	}
	if !strings.Contains(body, `"state":"created"`) {
		t.Fatalf("run resp missing session: %s", body)
	}
}

func TestAttachWebSocketStreamsBase64Data(t *testing.T) {
	// Includes multibyte/UTF-8 to confirm base64 round-trips binary cleanly.
	payload := []byte("héllo 世界\x00\xff")
	b := &fakeBackend{events: []*pb.AttachEvent{
		{Kind: pb.AttachEvent_OUTPUT, Data: payload, Source: "agent"},
		{Kind: pb.AttachEvent_STATE_CHANGE, State: "turn_done", StopReason: "end_turn"},
	}}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/attach?name=t1&token=secret"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var first pb.AttachEvent
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("read1: %v", err)
	}
	if first.Kind != pb.AttachEvent_OUTPUT || !bytes.Equal(first.Data, payload) {
		t.Fatalf("event1 mismatch: kind=%d data=%q", first.Kind, first.Data)
	}

	var second pb.AttachEvent
	if err := conn.ReadJSON(&second); err != nil {
		t.Fatalf("read2: %v", err)
	}
	if second.Kind != pb.AttachEvent_STATE_CHANGE || second.State != "turn_done" {
		t.Fatalf("event2 mismatch: %+v", second)
	}
}

func TestPruneForwardsTargetID(t *testing.T) {
	b := &fakeBackend{}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	body := doJSON(t, srv.URL+"/api/prune", "secret", http.MethodPost, map[string]any{"target_id": "target-1"})
	if b.pruneTarget != "target-1" {
		t.Fatalf("prune target = %q, want target-1", b.pruneTarget)
	}
	if !strings.Contains(body, `"count":2`) || !strings.Contains(body, `"dead1"`) {
		t.Fatalf("prune body missing response: %s", body)
	}
}

func TestInputEndpointsDefaultToDesktopSource(t *testing.T) {
	b := &fakeBackend{}
	srv := newTestServer(b, "secret")
	defer srv.Close()

	doJSON(t, srv.URL+"/api/submit", "secret", http.MethodPost, map[string]any{"name": "t1", "text": "hello"})
	if b.lastSource != "desktop" {
		t.Fatalf("submit source = %q, want desktop", b.lastSource)
	}

}

// doJSON performs a request with the bearer token and returns the response body.
func doJSON(t *testing.T, url, token, method string, payload any) string {
	t.Helper()
	var body io.Reader
	if payload != nil {
		buf, _ := json.Marshal(payload)
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return string(out)
}
