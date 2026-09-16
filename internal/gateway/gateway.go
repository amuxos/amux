// Package gateway exposes the amux session API over HTTP/WebSocket using JSON,
// so non-gRPC clients (e.g. the Electron desktop app) can drive sessions.
//
// Unary RPCs map to JSON REST endpoints under /api; the streaming Attach RPC is
// served over a WebSocket at /attach. The gateway is transport-agnostic: it
// talks to a Backend, which the amux CLI wires to either the local runtime or a
// hub (see cmd/amux/gui.go). This package only depends on internal/transport/pbgen,
// matching the layering rules in AGENTS.md.
package gateway

import (
	"context"
	"net/http"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/gorilla/websocket"
)

// SessionEntry is a session plus optional hub routing info. The embedded
// *pb.Session fields are promoted when marshaled to JSON.
type SessionEntry struct {
	*pb.Session
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

// Backend is the subset of session operations the gateway exposes. It mirrors
// cmd/amux's sessionClient but lives here (over pb types) so the gateway stays
// testable and free of the cmd/amux/main package.
type Backend interface {
	List(ctx context.Context) ([]*SessionEntry, error)
	Run(ctx context.Context, targetID string, req *pb.RunRequest) (*pb.RunResponse, error)
	Kill(ctx context.Context, targetID, name string) error
	Restart(ctx context.Context, targetID, name string) error
	Interrupt(ctx context.Context, targetID, name string) error
	Attach(ctx context.Context, targetID, name string) (pb.Amuxlet_AttachClient, error)
	Submit(ctx context.Context, targetID, name, source, text string, async bool) error
	QueueList(ctx context.Context, targetID, name string) ([]*pb.QueueItem, error)
	QueueClear(ctx context.Context, targetID, name string) (int32, error)
	QueueCancel(ctx context.Context, targetID, name, id string) error
	RespondPermission(ctx context.Context, targetID, name, permissionID, optionID string) error
	// Prune removes archived sessions from the selected target/runtime.
	Prune(ctx context.Context, targetID string) (count int32, names []string, err error)
}

// Server serves the gateway HTTP/WebSocket API.
type Server struct {
	backend  Backend
	token    string
	version  string
	mux      *http.ServeMux
	upgrader websocket.Upgrader
}

// New builds a gateway server. token, when non-empty, is required on every
// request (Bearer header for REST, ?token= for the WebSocket). An empty token
// disables auth and is intended for local development only.
func New(backend Backend, token, version string) *Server {
	s := &Server{
		backend: backend,
		token:   token,
		version: version,
		mux:     http.NewServeMux(),
		upgrader: websocket.Upgrader{
			// The gateway binds loopback only and gates on a bearer token, so we
			// accept any Origin (Electron's file:// origin would otherwise fail).
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	s.routes()
	return s
}

// Handler returns the root http.Handler. CORS is applied first (so preflight
// OPTIONS short-circuit before auth); bearer-token auth is applied per-route.
func (s *Server) Handler() http.Handler {
	return corsMiddleware(s.mux)
}

// corsMiddleware permits cross-origin requests from the desktop renderer, whose
// page origin is file:// (packaged) or http://localhost:<vite> (dev) — both
// distinct from the gateway's 127.0.0.1:<port> origin. The gateway binds
// loopback only and gates on a bearer token, so a permissive ACAO is safe.
// Requests carry an Authorization header, which makes them non-simple and
// triggers an OPTIONS preflight that must be answered without auth.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	// REST (unary). All gated by bearer token.
	s.mux.HandleFunc("/api/health", s.auth(s.handleHealth))
	s.mux.HandleFunc("/api/sessions", s.auth(s.handleSessions))
	s.mux.HandleFunc("/api/run", s.auth(s.handleRun))
	s.mux.HandleFunc("/api/kill", s.auth(s.handleKill))
	s.mux.HandleFunc("/api/restart", s.auth(s.handleRestart))
	s.mux.HandleFunc("/api/interrupt", s.auth(s.handleInterrupt))
	s.mux.HandleFunc("/api/submit", s.auth(s.handleSubmit))
	s.mux.HandleFunc("/api/respond-permission", s.auth(s.handleRespondPermission))
	s.mux.HandleFunc("/api/queue/list", s.auth(s.handleQueueList))
	s.mux.HandleFunc("/api/queue/clear", s.auth(s.handleQueueClear))
	s.mux.HandleFunc("/api/queue/cancel", s.auth(s.handleQueueCancel))
	s.mux.HandleFunc("/api/prune", s.auth(s.handlePrune))

	// Streaming attach over WebSocket (token via ?token=).
	s.mux.HandleFunc("/attach", s.handleAttach)
}
