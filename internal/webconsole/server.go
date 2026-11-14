// Package webconsole serves the same-origin, operator-only browser console.
// It deliberately does not reuse the desktop Gateway's permissive CORS policy.
package webconsole

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

//go:embed assets/index.html assets/app.js assets/styles.css assets/i18n.js assets/favicon.svg
var assets embed.FS

var (
	ErrNotFound = errors.New("console resource not found")
	ErrConflict = errors.New("console resource changed")
	ErrInvalid  = errors.New("invalid console request")
)

type Backend interface {
	Snapshot(context.Context, string, url.Values) (any, error)
	Execute(context.Context, string, string, json.RawMessage) (any, error)
	// Attach follows a Session Timeline. A non-empty cursor is exclusive: the
	// stream continues after the last event a reconnecting browser received.
	Attach(ctx context.Context, target, name, afterCursor string) (pb.Amuxlet_AttachClient, error)
}

type Options struct {
	Version string
	// Origin is the exact browser-facing origin. A temporary remote listener
	// may use HTTP on a trusted network; public deployments should use HTTPS.
	Origin string
	// Password enables repeatable temporary-password login until ExpiresAt.
	// PairCode remains the optional local one-time bootstrap mode.
	Password   string
	ExpiresAt  time.Time
	PairCode   string
	PairTTL    time.Duration
	SessionTTL time.Duration
	Now        func() time.Time
	Random     io.Reader
}

type browserSession struct {
	csrf    string
	expires time.Time
}
type Server struct {
	backend       Backend
	options       Options
	origin        *url.URL
	mu            sync.Mutex
	pairHash      [32]byte
	pairExpires   time.Time
	pairUsed      bool
	sessions      map[[32]byte]browserSession
	failures      map[string]int
	loginFailures int
	passwordAuth  bool
	closed        bool
}

func New(backend Backend, options Options) (*Server, error) {
	origin, err := url.Parse(options.Origin)
	if err != nil || origin.Host == "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, fmt.Errorf("%w: origin", ErrInvalid)
	}
	if options.PairCode == "" && options.Password == "" {
		return nil, fmt.Errorf("%w: authentication secret", ErrInvalid)
	}
	if options.Password != "" && (len(options.Password) < 16 || options.ExpiresAt.IsZero()) {
		return nil, fmt.Errorf("%w: temporary password", ErrInvalid)
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.PairTTL == 0 {
		options.PairTTL = 5 * time.Minute
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = 8 * time.Hour
	}
	if options.PairTTL <= 0 || options.SessionTTL <= 0 {
		return nil, fmt.Errorf("%w: expiration", ErrInvalid)
	}
	secret := options.PairCode
	if options.Password != "" {
		secret = options.Password
	}
	hash := sha256.Sum256([]byte(secret))
	passwordAuth := options.Password != ""
	options.PairCode, options.Password = "", ""
	return &Server{backend: backend, options: options, origin: origin, pairHash: hash, pairExpires: options.Now().Add(options.PairTTL), passwordAuth: passwordAuth, sessions: make(map[[32]byte]browserSession), failures: make(map[string]int)}, nil
}

func RandomSecret() (string, error) { return randomSecret(rand.Reader) }
func randomSecret(reader io.Reader) (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(reader, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

const cookieName = "amux_console_session"

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	// Exact host checks also prevent DNS rebinding. Never trust X-Forwarded-* to
	// choose an identity, cookie policy or origin.
	if !strings.EqualFold(r.Host, s.origin.Host) {
		s.error(w, http.StatusForbidden, "origin_rejected")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != strings.TrimSuffix(s.options.Origin, "/") {
		s.error(w, http.StatusForbidden, "origin_rejected")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
		s.error(w, http.StatusForbidden, "origin_rejected")
		return
	}
	s.mu.Lock()
	closed := s.closed || !s.options.ExpiresAt.IsZero() && !s.options.Now().Before(s.options.ExpiresAt)
	s.mu.Unlock()
	if closed {
		s.error(w, http.StatusUnauthorized, "console_expired")
		return
	}
	if r.URL.Path == "/auth/config" {
		if !s.method(w, r, http.MethodGet) {
			return
		}
		s.json(w, map[string]any{"mode": s.authMode(), "expires_at": s.expiration(), "insecure_transport": s.insecureTransport()})
		return
	}
	if r.URL.Path == "/auth/pair" {
		if s.passwordAuth {
			s.error(w, http.StatusNotFound, "not_found")
			return
		}
		s.pair(w, r)
		return
	}
	if r.URL.Path == "/auth/login" {
		s.login(w, r)
		return
	}
	session, key, ok := s.session(r)
	if r.URL.Path == "/auth/logout" {
		if !ok {
			s.error(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		if !s.writeAllowed(w, r, session) {
			return
		}
		s.mu.Lock()
		delete(s.sessions, key)
		s.mu.Unlock()
		s.cookie(w, "", time.Unix(1, 0), -1)
		s.json(w, map[string]bool{"ok": true})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !ok {
			s.error(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		if r.URL.Path == "/api/console/bootstrap" {
			if !s.method(w, r, http.MethodGet) {
				return
			}
			s.json(w, map[string]any{"version": s.options.Version, "csrf_token": session.csrf, "authenticated": true, "operator": true, "expires_at": s.expiration(), "auth_mode": s.authMode(), "insecure_transport": s.insecureTransport(), "resources": []string{"overview", "sessions", "tasks", "instances", "schedules", "profiles", "packs", "usage", "credentials", "settings", "bots", "groups", "catalogs"}})
			return
		}
		if r.URL.Path == "/api/console/events" {
			if !s.method(w, r, http.MethodGet) {
				return
			}
			s.events(w, r, session)
			return
		}
		resource := strings.TrimPrefix(r.URL.Path, "/api/console/")
		if resource == r.URL.Path || resource == "" || strings.Contains(resource, "/") {
			s.error(w, http.StatusNotFound, "not_found")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var value any
		var err error
		switch r.Method {
		case http.MethodGet:
			value, err = s.backend.Snapshot(ctx, resource, r.URL.Query())
		case http.MethodPost:
			if !s.writeAllowed(w, r, session) {
				return
			}
			var request struct {
				Action string          `json:"action"`
				Data   json.RawMessage `json:"data"`
			}
			if !s.decode(w, r, &request) {
				return
			}
			if request.Action == "" || len(request.Action) > 64 {
				s.error(w, http.StatusBadRequest, "invalid_request")
				return
			}
			value, err = s.backend.Execute(ctx, resource, request.Action, request.Data)
		default:
			w.Header().Set("Allow", "GET, POST")
			s.error(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if err != nil {
			s.backendError(w, err)
			return
		}
		s.json(w, value)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.error(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	files, _ := fs.Sub(assets, "assets")
	switch r.URL.Path {
	case "/":
		r.URL.Path = "/index.html"
	case "/index.html", "/app.js", "/styles.css", "/i18n.js", "/favicon.svg":
	default:
		s.error(w, http.StatusNotFound, "not_found")
		return
	}
	data, err := fs.ReadFile(files, strings.TrimPrefix(r.URL.Path, "/"))
	if err != nil {
		s.error(w, http.StatusNotFound, "not_found")
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case strings.HasSuffix(r.URL.Path, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(r.URL.Path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(r.URL.Path, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	if !s.method(w, r, http.MethodPost) {
		return
	}
	// Pairing requires the browser's exact origin too, including the first
	// request that does not yet possess a session or CSRF token.
	if r.Header.Get("Origin") != strings.TrimSuffix(s.options.Origin, "/") {
		s.error(w, http.StatusForbidden, "origin_rejected")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	digest := sha256.Sum256([]byte(req.Code))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.options.Now()
	if s.failures[ip] >= 10 {
		s.error(w, http.StatusTooManyRequests, "pairing_rate_limited")
		return
	}
	if s.pairUsed || !now.Before(s.pairExpires) || subtle.ConstantTimeCompare(digest[:], s.pairHash[:]) != 1 {
		s.failures[ip]++
		s.error(w, http.StatusUnauthorized, "pairing_expired_or_invalid")
		return
	}
	token, err := randomSecret(s.options.Random)
	if err != nil {
		s.error(w, http.StatusInternalServerError, "entropy_unavailable")
		return
	}
	csrf, err := randomSecret(s.options.Random)
	if err != nil {
		s.error(w, http.StatusInternalServerError, "entropy_unavailable")
		return
	}
	expires := now.Add(s.options.SessionTTL)
	if !s.options.ExpiresAt.IsZero() && s.options.ExpiresAt.Before(expires) {
		expires = s.options.ExpiresAt
	}
	s.pairUsed = true
	s.sessions[sha256.Sum256([]byte(token))] = browserSession{csrf: csrf, expires: expires}
	s.cookie(w, token, expires, int(s.options.SessionTTL.Seconds()))
	s.json(w, map[string]any{"authenticated": true, "csrf_token": csrf, "expires_at": expires.Unix()})
}

// insecureTransport reports whether browser traffic can cross a network in
// plaintext. Loopback origins (local use or an SSH tunnel) never leave the host
// and browsers treat them as potentially trustworthy, so they are not flagged.
func (s *Server) insecureTransport() bool {
	if s.origin.Scheme == "https" {
		return false
	}
	host := s.origin.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}
func (s *Server) authMode() string {
	if s.passwordAuth {
		return "password"
	}
	return "pair"
}
func (s *Server) expiration() int64 {
	if s.options.ExpiresAt.IsZero() {
		return 0
	}
	return s.options.ExpiresAt.Unix()
}

// Close invalidates all sessions before the listener and event streams stop.
func (s *Server) Close() { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true; clear(s.sessions) }
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.method(w, r, http.MethodPost) {
		return
	}
	if !s.passwordAuth {
		s.error(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Header.Get("Origin") != strings.TrimSuffix(s.options.Origin, "/") {
		s.error(w, http.StatusForbidden, "origin_rejected")
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if !s.decode(w, r, &request) {
		return
	}
	digest := sha256.Sum256([]byte(request.Password))
	request.Password = ""
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.options.Now()
	if s.closed || !now.Before(s.options.ExpiresAt) {
		s.error(w, http.StatusUnauthorized, "console_expired")
		return
	}
	if s.failures[ip] >= 10 || s.loginFailures >= 100 || len(s.failures) >= 1024 {
		s.error(w, http.StatusTooManyRequests, "authentication_rate_limited")
		return
	}
	if subtle.ConstantTimeCompare(digest[:], s.pairHash[:]) != 1 {
		s.failures[ip]++
		s.loginFailures++
		s.error(w, http.StatusUnauthorized, "invalid_password")
		return
	}
	for key, session := range s.sessions {
		if !now.Before(session.expires) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 64 {
		s.error(w, http.StatusTooManyRequests, "authentication_rate_limited")
		return
	}
	token, err := randomSecret(s.options.Random)
	if err != nil {
		s.error(w, http.StatusInternalServerError, "entropy_unavailable")
		return
	}
	csrf, err := randomSecret(s.options.Random)
	if err != nil {
		s.error(w, http.StatusInternalServerError, "entropy_unavailable")
		return
	}
	expires := now.Add(s.options.SessionTTL)
	if s.options.ExpiresAt.Before(expires) {
		expires = s.options.ExpiresAt
	}
	s.sessions[sha256.Sum256([]byte(token))] = browserSession{csrf: csrf, expires: expires}
	s.cookie(w, token, expires, int(expires.Sub(now).Seconds()))
	s.json(w, map[string]any{"authenticated": true, "csrf_token": csrf, "expires_at": expires.Unix()})
}
func (s *Server) cookie(w http.ResponseWriter, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true, Secure: s.origin.Scheme == "https", SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}
func (s *Server) session(r *http.Request) (browserSession, [32]byte, bool) {
	var empty [32]byte
	cookie, err := r.Cookie(cookieName)
	if err != nil || len(cookie.Value) != 64 {
		return browserSession{}, empty, false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[key]
	if ok && (s.closed || !s.options.Now().Before(session.expires)) {
		delete(s.sessions, key)
		ok = false
	}
	return session, key, ok
}
func (s *Server) writeAllowed(w http.ResponseWriter, r *http.Request, session browserSession) bool {
	if !s.method(w, r, http.MethodPost) {
		return false
	}
	if r.Header.Get("Origin") != strings.TrimSuffix(s.options.Origin, "/") || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.csrf)) != 1 {
		s.error(w, http.StatusForbidden, "csrf_rejected")
		return false
	}
	return true
}
func (s *Server) method(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		w.Header().Set("Allow", method)
		s.error(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return false
	}
	return true
}
func (s *Server) decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		s.error(w, http.StatusUnsupportedMediaType, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		s.error(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		s.error(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	return true
}
func (s *Server) json(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "code": code})
}
func (s *Server) backendError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	code := "backend_error"
	switch {
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
		code = "not_found"
	case errors.Is(err, ErrConflict):
		status = http.StatusConflict
		code = "version_conflict"
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
		code = "invalid_request"
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
		code = "backend_timeout"
	}
	var coded interface{ ConsoleErrorCode() string }
	if errors.As(err, &coded) {
		code = coded.ConsoleErrorCode()
		if code == "permission_denied" || code == "operator_required" {
			status = http.StatusForbidden
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "code": code})
}

// resumeCursor returns the cursor a reconnecting browser last saw (EventSource
// sends it as Last-Event-ID). It is opaque and untrusted: the Hub validates it.
func resumeCursor(r *http.Request) string {
	id := r.Header.Get("Last-Event-ID")
	if len(id) > 512 || strings.ContainsAny(id, "\r\n\x00") {
		return ""
	}
	return id
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, session browserSession) {
	name, target := r.URL.Query().Get("name"), r.URL.Query().Get("target_id")
	if name == "" {
		s.error(w, http.StatusBadRequest, "session_required")
		return
	}
	expires := session.expires
	if !s.options.ExpiresAt.IsZero() && s.options.ExpiresAt.Before(expires) {
		expires = s.options.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(r.Context(), expires)
	defer cancel()
	after := resumeCursor(r)
	stream, err := s.backend.Attach(ctx, target, name, after)
	if err != nil && after != "" {
		// An unusable cursor must not strand the browser: replay everything and
		// let it de-duplicate.
		after = ""
		stream, err = s.backend.Attach(ctx, target, name, "")
	}
	if err != nil {
		s.backendError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.error(w, http.StatusInternalServerError, "stream_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	// A long history is replayed in several connections (the Hub can cut one off), so
	// ask the browser to resume after a second rather than its default.
	_, _ = io.WriteString(w, "retry: 1000\n: connected\n\n")
	flusher.Flush()
	events := make(chan *pb.AttachEvent, 8)
	errs := make(chan error, 1)
	go func() {
		resumed, received := after != "", false
		for {
			event, err := stream.Recv()
			if err != nil {
				if resumed && !received && err != io.EOF {
					// The Hub rejected the cursor (new epoch, rotated journal):
					// replay everything instead of failing every reconnect.
					resumed = false
					if next, attachErr := s.backend.Attach(ctx, target, name, ""); attachErr == nil {
						stream = next
						continue
					}
				}
				select {
				case errs <- err:
				case <-ctx.Done():
				}
				return
			}
			received = true
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-errs:
			_, _ = io.WriteString(w, "event: disconnected\ndata: {}\n\n")
			flusher.Flush()
			return
		case <-heartbeat.C:
			if _, _, valid := s.session(r); !valid {
				return
			}
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			flusher.Flush()
		case event := <-events:
			if _, _, valid := s.session(r); !valid {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", strings.NewReplacer("\n", "", "\r", "").Replace(event.Cursor), data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
