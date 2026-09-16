package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/amuxos/amux/internal/wsstream"
)

// handleAttach upgrades to a WebSocket and streams a session's AttachEvents as
// JSON text frames. The client is read-only on this socket; input/control is
// done via the REST endpoints (submit/send/resize/respond-permission).
//
// Each AttachEvent is written verbatim as JSON — []byte fields (Data,
// PermissionPayload, *JSON) become base64 strings, which the frontend decodes.
func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(bearerToken(r)) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	name := q.Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	targetID := q.Get("target_id")

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Open the backend attach stream (replay is always on so reconnects catch up
	// from the ring buffer).
	stream, err := s.backend.Attach(ctx, targetID, name)
	if err != nil {
		_ = conn.WriteJSON(map[string]string{"error": err.Error()})
		return
	}

	write := wsstream.Keepalive(conn, ctx, cancel)

	// Forward AttachEvents until the stream ends or the client disconnects.
	for {
		ev, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				_ = conn.WriteJSON(map[string]string{"error": err.Error()})
			}
			return
		}
		if err := write(ev); err != nil {
			return
		}
	}
}
