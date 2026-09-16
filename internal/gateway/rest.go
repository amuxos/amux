package gateway

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/amuxos/amux/internal/sourceframe"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// ── request DTOs ─────────────────────────────────────────────────────────────
//
// These embed the pb request types and add the optional target_id used for hub
// routing (ignored by the local backend).

type runReq struct {
	TargetID string `json:"target_id,omitempty"`
	pb.RunRequest
}

type nameReq struct {
	TargetID string `json:"target_id,omitempty"`
	Name     string `json:"name"`
}

type submitReq struct {
	TargetID string `json:"target_id,omitempty"`
	Name     string `json:"name"`
	Source   string `json:"source,omitempty"`
	Text     string `json:"text"`
	Async    bool   `json:"async,omitempty"`
}

type respondPermReq struct {
	TargetID     string `json:"target_id,omitempty"`
	Name         string `json:"name"`
	PermissionId string `json:"permission_id"`
	OptionId     string `json:"option_id"`
}

type queueCancelReq struct {
	TargetID string `json:"target_id,omitempty"`
	Name     string `json:"name"`
	Id       string `json:"id"`
}

type pruneReq struct {
	TargetID string `json:"target_id,omitempty"`
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "version": s.version})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.backend.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if sessions == nil {
		sessions = []*SessionEntry{}
	}
	writeJSON(w, map[string]any{"sessions": sessions})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runReq
	if !decode(w, r, &req) {
		return
	}
	rr := req.RunRequest
	resp, err := s.backend.Run(r.Context(), req.TargetID, &rr)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	s.simpleByName(w, r, s.backend.Kill)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.simpleByName(w, r, s.backend.Restart)
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	s.simpleByName(w, r, s.backend.Interrupt)
}

// simpleByName handles the {target_id,name} -> error endpoints.
func (s *Server) simpleByName(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, targetID, name string) error) {
	var req nameReq
	if !decode(w, r, &req) {
		return
	}
	if err := fn(r.Context(), req.TargetID, req.Name); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req submitReq
	if !decode(w, r, &req) {
		return
	}
	if err := s.backend.Submit(r.Context(), req.TargetID, req.Name, gatewayInputSource(req.Source), req.Text, req.Async); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleRespondPermission(w http.ResponseWriter, r *http.Request) {
	var req respondPermReq
	if !decode(w, r, &req) {
		return
	}
	if err := s.backend.RespondPermission(r.Context(), req.TargetID, req.Name, req.PermissionId, req.OptionId); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleQueueList(w http.ResponseWriter, r *http.Request) {
	var req nameReq
	if !decode(w, r, &req) {
		return
	}
	items, err := s.backend.QueueList(r.Context(), req.TargetID, req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if items == nil {
		items = []*pb.QueueItem{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func (s *Server) handleQueueClear(w http.ResponseWriter, r *http.Request) {
	var req nameReq
	if !decode(w, r, &req) {
		return
	}
	cleared, err := s.backend.QueueClear(r.Context(), req.TargetID, req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"cleared": cleared})
}

func (s *Server) handleQueueCancel(w http.ResponseWriter, r *http.Request) {
	var req queueCancelReq
	if !decode(w, r, &req) {
		return
	}
	if err := s.backend.QueueCancel(r.Context(), req.TargetID, req.Name, req.Id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	var req pruneReq
	if !decode(w, r, &req) {
		return
	}
	count, names, err := s.backend.Prune(r.Context(), req.TargetID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, map[string]any{"count": count, "names": names})
}

func gatewayInputSource(source string) string {
	if source != "" {
		return source
	}
	return sourceframe.SourceDesktop
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// decode reads a JSON request body into v, requiring POST. Returns false (and
// writes an error response) on failure. An empty body decodes to the zero value.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
