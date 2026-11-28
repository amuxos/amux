package hubweb

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/wsstream"
)

//go:embed turn_process.html
var turnProcessHTML string

//go:embed locales/en.json
var turnProcessEnglish []byte

//go:embed locales/zh-CN.json
var turnProcessChinese []byte

var turnProcessTemplate = template.Must(template.New("turn process").Parse(turnProcessHTML))

var turnProcessCatalog = func() *i18n.Catalog {
	catalog, err := i18n.New(turnProcessEnglish, turnProcessChinese)
	if err != nil {
		panic(err)
	}
	return catalog
}()

func turnProcessLocale(r *http.Request) i18n.Locale {
	for _, raw := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(raw), ";")
		if locale := i18n.Normalize(tag); locale != "" {
			return locale
		}
	}
	return i18n.English
}

func (s *Server) checkTurnProcessToken(w http.ResponseWriter, r *http.Request, token string) (sessionTokenClaims, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !requireGET(w, r) {
		return sessionTokenClaims{}, false
	}
	claims, err := SessionTokenScope(s.token, token, time.Now())
	if err != nil || claims.TurnID == "" || claims.TargetID == "" || claims.Name == "" {
		http.Error(w, turnProcessCatalog.Format(turnProcessLocale(r), "unauthorized"), http.StatusUnauthorized)
		return sessionTokenClaims{}, false
	}
	return claims, true
}

func (s *Server) handleTurnProcessPage(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.checkTurnProcessToken(w, r, strings.TrimPrefix(r.URL.Path, "/t/"))
	if !ok {
		return
	}
	locale := turnProcessLocale(r)
	messages := make(map[string]string)
	for _, key := range []string{
		"title", "connecting", "waiting", "running", "done", "failed", "stopped", "unavailable",
		"empty", "empty_done", "latest", "retry", "progress", "progress_empty", "progress_empty_done",
		"tools", "tool", "tool_failed",
	} {
		messages[key] = turnProcessCatalog.Format(locale, key)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = turnProcessTemplate.Execute(w, struct {
		Locale         i18n.Locale
		Messages       map[string]string
		ShowProgress   bool
		ExpandProgress bool
	}{locale, messages, !claims.ToolsOnly, claims.ExpandProgress})
}

type turnProcessUpdate struct {
	Status   string               `json:"status,omitempty"`
	Progress *turnProcessProgress `json:"progress,omitempty"`
	Tool     *turnProcessTool     `json:"tool,omitempty"`
}

type turnProcessProgress struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type turnProcessThinking struct {
	presenter *agentui.Presenter
	index     int
	lastStep  string
}

func (t *turnProcessThinking) update(delta string) *turnProcessProgress {
	if strings.TrimSpace(delta) == "" {
		return nil
	}
	if t.presenter == nil {
		t.presenter = agentui.NewPresenter()
		t.index++
		t.lastStep = ""
	}
	// Use the card's bounded, user-visible progress projection for each segment.
	for _, action := range t.presenter.Consume(agentui.Event{Kind: agentui.KindThinkingDelta, Text: delta}) {
		if action.Activity != nil && action.Activity.CurrentStep != "" && action.Activity.CurrentStep != t.lastStep {
			t.lastStep = action.Activity.CurrentStep
			return &turnProcessProgress{Index: t.index, Text: t.lastStep}
		}
	}
	return nil
}

type turnProcessTool struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`
	Phase string `json:"phase"`
	At    int64  `json:"at,omitempty"`
}

var errTurnProcessFinished = errors.New("turn process finished")

func (s *Server) handleTurnProcessStream(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.checkTurnProcessToken(w, r, requestToken(r))
	if !ok {
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithDeadline(r.Context(), time.Unix(claims.Exp, 0))
	defer cancel()
	send := wsstream.Keepalive(conn, ctx, cancel)
	var thinking *turnProcessThinking
	if !claims.ToolsOnly {
		thinking = &turnProcessThinking{}
	}
	started := false
	err = s.backend.Attach(ctx, claims.TargetID, claims.Name, func(ev *pb.AttachEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ev.GetTurnId() != claims.TurnID {
			if ev.GetCaughtUp() && !started {
				return send(turnProcessUpdate{Status: "waiting"})
			}
			return nil
		}
		if !started {
			started = true
			if err := send(turnProcessUpdate{Status: "running"}); err != nil {
				return err
			}
		}
		return forwardTurnProcessEvent(ev, thinking, send)
	})
	if !errors.Is(err, errTurnProcessFinished) && ctx.Err() == nil {
		_ = send(turnProcessUpdate{Status: "unavailable"})
	}
}

func forwardTurnProcessEvent(ev *pb.AttachEvent, thinking *turnProcessThinking, send func(any) error) error {
	switch ev.GetEventType() {
	case string(agentstream.KindTurnFinished):
		status := "done"
		switch {
		case ev.GetState() == "failed":
			status = "failed"
		case ev.GetState() == "interrupted" || ev.GetStopReason() == "cancelled":
			status = "stopped"
		}
		if err := send(turnProcessUpdate{Status: status}); err != nil {
			return err
		}
		return errTurnProcessFinished
	case string(agentstream.KindThinkingDelta):
		if thinking != nil {
			if progress := thinking.update(ev.GetText()); progress != nil {
				return send(turnProcessUpdate{Progress: progress})
			}
		}
	case "tool_started", "tool_updated", "tool_completed", "tool_failed":
		if thinking != nil && ev.GetEventType() == "tool_started" {
			thinking.presenter = nil
		}
		// Project display fields only. Attach events may carry tool inputs,
		// raw output and permission payloads which must not reach this viewer.
		var timestamp struct{ At time.Time }
		var at int64
		if json.Unmarshal(ev.GetCanonicalJson(), &timestamp) == nil && !timestamp.At.IsZero() {
			at = timestamp.At.UnixMilli()
		}
		return send(turnProcessUpdate{Tool: &turnProcessTool{
			ID: ev.GetToolId(), Name: ev.GetToolName(), Title: ev.GetToolTitle(),
			Phase: strings.TrimPrefix(ev.GetEventType(), "tool_"), At: at,
		}})
	}
	return nil
}
