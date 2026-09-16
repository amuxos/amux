package hubweb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/gorilla/websocket"
)

type fakeBackend struct {
	targets      []*pb.Target
	events       []*pb.AttachEvent
	attachTarget string
	attachName   string
	parentTasks  map[string][]string
	taskJSON     map[string][]byte
}

func (f *fakeBackend) ListTargets() []*pb.Target {
	return f.targets
}

func (f *fakeBackend) Attach(ctx context.Context, targetID, name string, fn func(*pb.AttachEvent) error) error {
	f.attachTarget = targetID
	f.attachName = name
	for _, ev := range f.events {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeBackend) SubtaskIDsByParent(parentSessionID string) []string {
	return f.parentTasks[parentSessionID]
}

func (f *fakeBackend) GetSubtaskJSON(_ context.Context, id string) ([]byte, error) {
	return f.taskJSON[id], nil
}

func TestAuthRequiredForAPISessionsAndPage(t *testing.T) {
	srv := httptest.NewServer(New(&fakeBackend{}, "secret", "test").Handler())
	defer srv.Close()

	for _, path := range []string{"/api/sessions", "/sessions?target_id=t1&name=s1"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestSessionsListsTargetsAndSessions(t *testing.T) {
	backend := &fakeBackend{targets: []*pb.Target{{
		Id:   "target-1",
		Name: "remote-dev",
		Sessions: []*pb.Session{{
			Name:         "feishu-a1",
			AgentType:    "codex",
			AgentProfile: "team/coder",
			State:        "running",
		}},
	}}}
	srv := httptest.NewServer(New(backend, "secret", "test").Handler())
	defer srv.Close()

	body := doReq(t, srv.URL+"/api/sessions", "secret", http.MethodGet, nil)
	if !strings.Contains(body, `"target_id":"target-1"`) ||
		!strings.Contains(body, `"name":"feishu-a1"`) ||
		!strings.Contains(body, `"agent_profile":"team/coder"`) {
		t.Fatalf("sessions body missing expected fields: %s", body)
	}
}

func TestPageIsServedWithTokenQuery(t *testing.T) {
	srv := httptest.NewServer(New(&fakeBackend{}, "secret", "test").Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions?target_id=target-1&name=feishu-a1&token=secret")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, out)
	}
	if !strings.Contains(string(out), "Session Output") ||
		!strings.Contains(string(out), "recent replay + live") {
		t.Fatalf("page body missing viewer copy: %s", out)
	}
	if strings.Contains(string(out), "<textarea") || strings.Contains(string(out), "Send") {
		t.Fatalf("page should not render input controls: %s", out)
	}
}

func TestViewerHTMLCoalescesStreamingAgentText(t *testing.T) {
	for _, want := range []string{
		"let lastMergeKey = \"\";",
		"function mergeKey(ev, text)",
		"inputContinuationText(rawText)",
		"inputDisplayText(ev, text)",
		"content.className = \"content\"",
	} {
		if !strings.Contains(viewerHTML, want) {
			t.Fatalf("viewer HTML missing streaming coalescing marker %q", want)
		}
	}
	if strings.Contains(viewerHTML, "pre {\n  margin: 0;\n  white-space: pre-wrap;\n  overflow-wrap: anywhere;") {
		t.Fatal("viewer should not render all transcript text through global pre with overflow-wrap:anywhere")
	}
}

func TestViewerHTMLExposesStructuredToolDetailsReadOnly(t *testing.T) {
	for _, want := range []string{
		"function structuredDetails(ev)",
		"ev.structured_json",
		"details.className = \"structured-details\"",
		"完整结构化输出",
	} {
		if !strings.Contains(viewerHTML, want) {
			t.Fatalf("viewer HTML missing structured detail marker %q", want)
		}
	}
}

func TestWriteRoutesAreNotExposed(t *testing.T) {
	srv := httptest.NewServer(New(&fakeBackend{}, "secret", "test").Handler())
	defer srv.Close()

	for _, path := range []string{"/api/submit", "/api/interrupt", "/api/restart", "/api/respond-permission"} {
		resp, err := doRawReq(t, srv.URL+path, "secret", http.MethodPost, map[string]string{"name": "s1"})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 404 or 405", path, resp.StatusCode)
		}
	}
}

func TestSessionStreamForwardsAttachEventsReadOnly(t *testing.T) {
	payload := []byte("hello from agent")
	backend := &fakeBackend{events: []*pb.AttachEvent{
		{Kind: pb.AttachEvent_OUTPUT, Data: payload, Source: "agent"},
		{Kind: pb.AttachEvent_STATE_CHANGE, State: "turn_done", StopReason: "end_turn"},
	}}
	srv := httptest.NewServer(New(backend, "secret", "test").Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/session/stream?target_id=target-1&name=feishu-a1&token=secret"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var first pb.AttachEvent
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("read first event: %v", err)
	}
	if first.Kind != pb.AttachEvent_OUTPUT || !bytes.Equal(first.Data, payload) {
		t.Fatalf("first event = %+v data=%q", first, first.Data)
	}

	var second pb.AttachEvent
	if err := conn.ReadJSON(&second); err != nil {
		t.Fatalf("read second event: %v", err)
	}
	if second.Kind != pb.AttachEvent_STATE_CHANGE || second.State != "turn_done" {
		t.Fatalf("second event = %+v", second)
	}
	if backend.attachTarget != "target-1" || backend.attachName != "feishu-a1" {
		t.Fatalf("attach target/name = %q/%q", backend.attachTarget, backend.attachName)
	}
}

func TestSignedSessionTokenCanViewOnlyScopedSession(t *testing.T) {
	token := SignSessionToken("viewer-secret", "target-1", "feishu-a1", time.Now().Add(time.Hour))
	backend := &fakeBackend{targets: []*pb.Target{{
		Id:   "target-1",
		Name: "remote-dev",
		Sessions: []*pb.Session{{
			Name:      "feishu-a1",
			AgentType: "codex",
			State:     "running",
		}},
	}}}
	srv := httptest.NewServer(New(backend, "viewer-secret", "test").Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions?target_id=target-1&name=feishu-a1&token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scoped page status = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/api/sessions?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("scoped token list status = %d, want 401", resp.StatusCode)
	}

	body := doReq(t, srv.URL+"/api/session?target_id=target-1&name=feishu-a1&token="+token, "", http.MethodGet, nil)
	if !strings.Contains(body, `"name":"feishu-a1"`) || strings.Contains(body, `"targets"`) {
		t.Fatalf("session body = %s, want one scoped session", body)
	}
}

func TestSignedSessionTokenShortURLServesScopedPageAndAPI(t *testing.T) {
	token := SignSessionToken("viewer-secret", "target-1", "feishu-a1", time.Now().Add(time.Hour))
	backend := &fakeBackend{targets: []*pb.Target{{
		Id:   "target-1",
		Name: "remote-dev",
		Sessions: []*pb.Session{{
			Name:      "feishu-a1",
			AgentType: "codex",
			State:     "running",
		}},
	}}}
	srv := httptest.NewServer(New(backend, "viewer-secret", "test").Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/s/" + token)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("short page status = %d body=%s", resp.StatusCode, page)
	}
	if !strings.Contains(string(page), "Session Output") {
		t.Fatalf("short page body missing viewer copy: %s", page)
	}

	body := doReq(t, srv.URL+"/api/session?token="+token, "", http.MethodGet, nil)
	if !strings.Contains(body, `"target_id":"target-1"`) || !strings.Contains(body, `"name":"feishu-a1"`) {
		t.Fatalf("session body = %s, want scoped session", body)
	}
}

func TestSignedSessionTokenCanStreamWithoutQueryRef(t *testing.T) {
	token := SignSessionToken("viewer-secret", "target-1", "feishu-a1", time.Now().Add(time.Hour))
	backend := &fakeBackend{events: []*pb.AttachEvent{
		{Kind: pb.AttachEvent_OUTPUT, Data: []byte("scoped stream"), Source: "agent"},
	}}
	srv := httptest.NewServer(New(backend, "viewer-secret", "test").Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/session/stream?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial scoped stream: %v", err)
	}
	defer conn.Close()
	var first pb.AttachEvent
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("read scoped event: %v", err)
	}
	if string(first.Data) != "scoped stream" {
		t.Fatalf("event data = %q", first.Data)
	}
	if backend.attachTarget != "target-1" || backend.attachName != "feishu-a1" {
		t.Fatalf("attach target/name = %q/%q", backend.attachTarget, backend.attachName)
	}
}

func TestSignedSessionTokenCannotStreamOtherSession(t *testing.T) {
	token := SignSessionToken("viewer-secret", "target-1", "feishu-a1", time.Now().Add(time.Hour))
	srv := httptest.NewServer(New(&fakeBackend{}, "viewer-secret", "test").Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/session/stream?target_id=target-1&name=other&token=" + token
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("dial other session with scoped token succeeded, want failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		if resp == nil {
			t.Fatalf("dial response nil, err=%v", err)
		}
		t.Fatalf("dial status = %d, want 401", resp.StatusCode)
	}
}

func TestSignedParentSessionTokenCanViewTaskChildSession(t *testing.T) {
	token := SignSessionToken("viewer-secret", "target-1", "main", time.Now().Add(time.Hour))
	backend := &fakeBackend{
		targets: []*pb.Target{
			{
				Id:   "target-1",
				Name: "remote-dev-a",
				Sessions: []*pb.Session{{
					Id:        "sess-parent",
					Name:      "main",
					AgentType: "codex",
					State:     "running",
				}},
			},
			{
				Id:   "target-2",
				Name: "remote-dev-b",
				Sessions: []*pb.Session{{
					Id:          "sess-child",
					Name:        "child-1",
					AgentType:   "coco",
					State:       "running",
					OwnerTaskId: "task-child",
				}},
			},
		},
		parentTasks: map[string][]string{"sess-parent": {"task-child"}},
		taskJSON: map[string][]byte{
			"task-child": []byte(`{"id":"task-child","status":"running","task":"inspect logs","child_session_name":"child-1","target_id":"target-2"}`),
		},
		events: []*pb.AttachEvent{{Kind: pb.AttachEvent_OUTPUT, Data: []byte("child output"), Source: "agent"}},
	}
	srv := httptest.NewServer(New(backend, "viewer-secret", "test").Handler())
	defer srv.Close()

	body := doReq(t, srv.URL+"/api/session?target_id=target-1&name=main&token="+token, "", http.MethodGet, nil)
	for _, want := range []string{`"children"`, `"task_id":"task-child"`, `"target_id":"target-2"`, `"name":"child-1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("session body missing %s: %s", want, body)
		}
	}

	body = doReq(t, srv.URL+"/api/session?target_id=target-2&name=child-1&token="+token, "", http.MethodGet, nil)
	if !strings.Contains(body, `"name":"child-1"`) {
		t.Fatalf("child session body = %s, want child session", body)
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/session/stream?target_id=target-2&name=child-1&token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial child stream with parent token: %v", err)
	}
	defer conn.Close()
	var first pb.AttachEvent
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("read child event: %v", err)
	}
	if string(first.Data) != "child output" || backend.attachTarget != "target-2" || backend.attachName != "child-1" {
		t.Fatalf("child stream event=%q attach=%q/%q", first.Data, backend.attachTarget, backend.attachName)
	}
}

func TestSignedParentSessionTokenCannotViewUnrelatedTaskChildSession(t *testing.T) {
	token := SignSessionToken("viewer-secret", "target-1", "main", time.Now().Add(time.Hour))
	backend := &fakeBackend{
		targets: []*pb.Target{
			{Id: "target-1", Sessions: []*pb.Session{{Id: "sess-parent", Name: "main"}}},
			{Id: "target-2", Sessions: []*pb.Session{{Id: "sess-child", Name: "child-1", OwnerTaskId: "task-child"}}},
		},
		parentTasks: map[string][]string{"sess-parent": {"other-task"}},
	}
	srv := httptest.NewServer(New(backend, "viewer-secret", "test").Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/session?target_id=target-2&name=child-1&token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unrelated child status = %d, want 401", resp.StatusCode)
	}
}

func doReq(t *testing.T, url, token, method string, payload any) string {
	t.Helper()
	resp, err := doRawReq(t, url, token, method, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("%s %s status=%d body=%s", method, url, resp.StatusCode, out)
	}
	return string(out)
}

func doRawReq(t *testing.T, url, token, method string, payload any) (*http.Response, error) {
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
	return http.DefaultClient.Do(req)
}
