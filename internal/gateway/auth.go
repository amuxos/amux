package gateway

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// auth wraps an http.HandlerFunc with bearer-token enforcement. When the server
// token is empty, auth is disabled (dev mode).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenOK(bearerToken(r)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// tokenOK reports whether got matches the configured token (constant time).
// Always true when no token is configured.
func (s *Server) tokenOK(got string) bool {
	if s.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// bearerToken extracts the token from the Authorization header
// ("Bearer <tok>") or, for WebSocket upgrades, the ?token= query param.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	return r.URL.Query().Get("token")
}
