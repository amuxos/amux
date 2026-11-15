package webconsole

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type testBackend struct{ writes atomic.Int32 }

func (b *testBackend) Snapshot(context.Context, string, url.Values) (any, error) {
	return map[string]any{"sessions": []any{}}, nil
}
func (b *testBackend) Execute(context.Context, string, string, json.RawMessage) (any, error) {
	b.writes.Add(1)
	return map[string]bool{"ok": true}, nil
}
func (b *testBackend) Attach(context.Context, string, string, string) (pb.Amuxlet_AttachClient, error) {
	return nil, errors.New("unavailable")
}
func testServer(t *testing.T) (*Server, *testBackend) {
	t.Helper()
	b := &testBackend{}
	s, err := New(b, Options{Origin: "http://127.0.0.1:7892", PairCode: "one-time-pair"})
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}
func request(s *Server, method, path, body, origin, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:7892"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func requestHost(s *Server, method, path, host string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func pairSession(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	w := request(s, "POST", "/auth/pair", `{"code":"one-time-pair"}`, "http://127.0.0.1:7892", "", nil)
	if w.Code != 200 {
		t.Fatalf("pair: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookies: %v", cookies)
	}
	return cookies[0], response.CSRF
}
func TestConsoleAuthenticationAndControl(t *testing.T) {
	s, b := testServer(t)
	if w := request(s, "GET", "/api/console/sessions", "", "", "", nil); w.Code != 401 {
		t.Fatalf("unauthenticated read: %d", w.Code)
	}
	cookie, csrf := pairSession(t, s)
	if w := request(s, "POST", "/auth/pair", `{"code":"one-time-pair"}`, "http://127.0.0.1:7892", "", nil); w.Code != 401 {
		t.Fatalf("pair reused: %d", w.Code)
	}
	if w := request(s, "GET", "/api/console/bootstrap", "", "", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), csrf) {
		t.Fatalf("bootstrap: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ origin, csrf string }{{"", ""}, {"http://127.0.0.1:7892", "wrong"}, {"https://evil.example", csrf}} {
		w := request(s, "POST", "/api/console/sessions", `{"action":"interrupt","data":{}}`, tc.origin, tc.csrf, cookie)
		if w.Code != 403 {
			t.Fatalf("forged write: %d", w.Code)
		}
	}
	if b.writes.Load() != 0 {
		t.Fatal("rejected request reached backend")
	}
	w := request(s, "POST", "/api/console/sessions", `{"action":"interrupt","data":{}}`, "http://127.0.0.1:7892", csrf, cookie)
	if w.Code != 200 || b.writes.Load() != 1 {
		t.Fatalf("authorized write: %d", w.Code)
	}
	if w = request(s, "POST", "/auth/logout", `{}`, "http://127.0.0.1:7892", csrf, cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request(s, "GET", "/api/console/sessions", "", "", "", cookie); w.Code != 401 {
		t.Fatal("logged out cookie still works")
	}
}
func TestConsolePairExpirationAndEntropyFailure(t *testing.T) {
	now := time.Now()
	s, err := New(&testBackend{}, Options{Origin: "http://127.0.0.1:7892", PairCode: "one-time-pair", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if w := request(s, "POST", "/auth/pair", `{"code":"one-time-pair"}`, "http://127.0.0.1:7892", "", nil); w.Code != 401 {
		t.Fatalf("expired: %d", w.Code)
	}
	s, err = New(&testBackend{}, Options{Origin: "http://127.0.0.1:7892", PairCode: "one-time-pair", Random: bytes.NewReader(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/auth/pair", `{"code":"one-time-pair"}`, "http://127.0.0.1:7892", "", nil); w.Code != 500 {
		t.Fatalf("entropy failed open: %d", w.Code)
	}
}
func TestConsoleOriginHostAndPayloadBoundaries(t *testing.T) {
	s, b := testServer(t)
	cookie, csrf := pairSession(t, s)
	for _, path := range []string{"/", "/app.js", "/styles.css", "/i18n.js", "/favicon.svg"} {
		w := request(s, "GET", path, "", "", "", nil)
		if w.Code != 200 {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
		if path == "/favicon.svg" && w.Header().Get("Content-Type") != "image/svg+xml" {
			t.Fatalf("favicon content type: %q", w.Header().Get("Content-Type"))
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("security headers missing")
		}
	}
	r := httptest.NewRequest("GET", "http://attacker.example/api/console/sessions", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("host rebinding: %d", w.Code)
	}
	for _, body := range []string{`{"action":"x","data":{},"extra":1}`, `{"action":"x","data":{}} {}`, `{"action":"x","data":"` + strings.Repeat("a", 1<<20) + `"}`} {
		w := request(s, "POST", "/api/console/sessions", body, "http://127.0.0.1:7892", csrf, cookie)
		if w.Code != 400 {
			t.Fatalf("bad body: %d", w.Code)
		}
	}
	if b.writes.Load() != 0 {
		t.Fatal("invalid payload reached backend")
	}
}
func TestConsolePairBruteForceLimit(t *testing.T) {
	s, _ := testServer(t)
	for i := 0; i < 10; i++ {
		w := request(s, "POST", "/auth/pair", `{"code":"wrong"}`, "http://127.0.0.1:7892", "", nil)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	w := request(s, "POST", "/auth/pair", `{"code":"one-time-pair"}`, "http://127.0.0.1:7892", "", nil)
	if w.Code != 429 {
		t.Fatalf("not limited: %d", w.Code)
	}
}
func TestConsoleSessionExpires(t *testing.T) {
	now := time.Now()
	s, err := New(&testBackend{}, Options{Origin: "http://127.0.0.1:7892", PairCode: "one-time-pair", Now: func() time.Time { return now }, SessionTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := pairSession(t, s)
	now = now.Add(2 * time.Minute)
	if w := request(s, "GET", "/api/console/bootstrap", "", "", "", cookie); w.Code != 401 {
		t.Fatalf("expired session: %d", w.Code)
	}
}
func TestConsoleNewRejectsUnsafeOrigin(t *testing.T) {
	for _, origin := range []string{"", "ftp://host", "http://user:secret@host", "http://host/path", "http://host?token=x"} {
		if _, err := New(&testBackend{}, Options{Origin: origin, PairCode: "secret"}); err == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
	_, err := randomSecret(io.LimitReader(strings.NewReader("x"), 1))
	if err == nil {
		t.Fatal("short entropy accepted")
	}
}

type fieldError struct{ code, field string }

func (e fieldError) Error() string             { return e.code }
func (e fieldError) Unwrap() error             { return ErrInvalid }
func (e fieldError) ConsoleErrorCode() string  { return e.code }
func (e fieldError) ConsoleErrorField() string { return e.field }

type failingBackend struct{ testBackend }

func (b *failingBackend) Execute(context.Context, string, string, json.RawMessage) (any, error) {
	return nil, fieldError{code: "invalid_field", field: "app_secret"}
}

func TestConsoleErrorsNameTheRejectedField(t *testing.T) {
	s, err := New(&failingBackend{}, Options{Origin: "http://127.0.0.1:7892", PairCode: "one-time-pair"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := pairSession(t, s)
	w := request(s, "POST", "/api/console/bots", `{"action":"create","data":{}}`, "http://127.0.0.1:7892", csrf, cookie)
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 400 || body["code"] != "invalid_field" || body["field"] != "app_secret" {
		t.Fatalf("error = %d %v", w.Code, body)
	}
}
