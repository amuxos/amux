// Package hubweb serves the hub-level read-only session output viewer.
package hubweb

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/wsstream"
	"github.com/gorilla/websocket"
)

// Backend is the read-only hub surface used by the viewer.
type Backend interface {
	ListTargets() []*pb.Target
	Attach(ctx context.Context, targetID, name string, fn func(*pb.AttachEvent) error) error
}

// SubtaskReader is an optional backend capability used to link a parent session
// to task-owned child sessions without exposing task mutation APIs.
type SubtaskReader interface {
	SubtaskIDsByParent(parentSessionID string) []string
	GetSubtaskJSON(ctx context.Context, id string) ([]byte, error)
}

type childSessionItem struct {
	TargetID   string      `json:"target_id"`
	TargetName string      `json:"target_name,omitempty"`
	TaskID     string      `json:"task_id"`
	TaskStatus string      `json:"task_status,omitempty"`
	TaskTitle  string      `json:"task_title,omitempty"`
	Session    *pb.Session `json:"session"`
}

// Server serves the read-only hub web UI and stream endpoints.
type Server struct {
	backend  Backend
	token    string
	version  string
	mux      *http.ServeMux
	upgrader websocket.Upgrader
}

// New builds a read-only hub web server. token, when non-empty, is required on
// HTTP requests via Bearer or ?token= and on WebSocket requests via ?token=.
func New(backend Backend, token, version string) *Server {
	s := &Server{
		backend: backend,
		token:   token,
		version: version,
		mux:     http.NewServeMux(),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/health", s.auth(s.handleHealth))
	s.mux.HandleFunc("/api/sessions", s.auth(s.handleSessions))
	s.mux.HandleFunc("/api/session", s.sessionAuth(s.handleSession))
	s.mux.HandleFunc("/api/session/stream", s.handleStream)
	s.mux.HandleFunc("/sessions", s.sessionAuth(s.handlePage))
	s.mux.HandleFunc("/s/", s.sessionAuth(s.handlePage))
	s.mux.HandleFunc("/", s.auth(s.handleIndex))
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.globalTokenOK(requestToken(r)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) sessionAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targetID, name := s.sessionRefFromRequest(r)
		if name == "" || !s.sessionTokenOK(requestToken(r), targetID, name) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) globalTokenOK(got string) bool {
	if s.token == "" {
		return true
	}
	return got == s.token
}

func (s *Server) sessionTokenOK(got, targetID, name string) bool {
	if s.globalTokenOK(got) {
		return true
	}
	now := time.Now()
	if VerifySessionToken(s.token, got, targetID, name, now) == nil {
		return true
	}
	claims, err := SessionTokenScope(s.token, got, now)
	if err != nil {
		return false
	}
	return s.isTaskChildSession(claims.TargetID, claims.Name, targetID, name)
}

func (s *Server) sessionRefFromRequest(r *http.Request) (targetID, name string) {
	q := r.URL.Query()
	targetID = strings.TrimSpace(q.Get("target_id"))
	name = strings.TrimSpace(q.Get("name"))
	if name != "" {
		return targetID, name
	}
	if claims, err := SessionTokenScope(s.token, requestToken(r), time.Now()); err == nil {
		return claims.TargetID, claims.Name
	}
	return targetID, name
}

func requestToken(r *http.Request) string {
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token
	}
	if token, ok := strings.CutPrefix(r.URL.Path, "/s/"); ok {
		return strings.TrimSpace(token)
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if token, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return strings.TrimSpace(token)
	}
	return ""
}

func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	writeJSON(w, map[string]any{"ok": true, "version": s.version, "mode": "read-only"})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	targets := s.backend.ListTargets()
	if targets == nil {
		targets = []*pb.Target{}
	}
	type sessionItem struct {
		*pb.Session
		TargetID   string `json:"target_id"`
		TargetName string `json:"target_name,omitempty"`
	}
	type targetItem struct {
		TargetID   string         `json:"target_id"`
		TargetName string         `json:"target_name,omitempty"`
		Sessions   []*sessionItem `json:"sessions"`
	}
	out := make([]*targetItem, 0, len(targets))
	for _, target := range targets {
		item := &targetItem{TargetID: target.GetId(), TargetName: target.GetName()}
		for _, sess := range target.GetSessions() {
			if sess == nil {
				continue
			}
			item.Sessions = append(item.Sessions, &sessionItem{
				Session:    sess,
				TargetID:   target.GetId(),
				TargetName: target.GetName(),
			})
		}
		if item.Sessions == nil {
			item.Sessions = []*sessionItem{}
		}
		out = append(out, item)
	}
	writeJSON(w, map[string]any{"targets": out})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	targetID, name := s.sessionRefFromRequest(r)
	target, sess := s.findSession(targetID, name)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"target_id":   target.GetId(),
		"target_name": target.GetName(),
		"session":     sess,
		"children":    s.childSessions(r.Context(), sess),
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !requireGET(w, r) {
		return
	}
	http.Redirect(w, r, "/sessions?token="+html.EscapeString(r.URL.Query().Get("token")), http.StatusFound)
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, viewerHTML)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	targetID, name := s.sessionRefFromRequest(r)
	if name == "" || !s.sessionTokenOK(requestToken(r), targetID, name) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !requireGET(w, r) {
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	write := wsstream.Keepalive(conn, ctx, cancel)

	err = s.backend.Attach(ctx, targetID, name, func(ev *pb.AttachEvent) error {
		return write(ev)
	})
	if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		_ = conn.WriteJSON(map[string]string{"error": err.Error()})
	}
}

func (s *Server) findSession(targetID, name string) (*pb.Target, *pb.Session) {
	for _, target := range s.backend.ListTargets() {
		if target == nil || target.GetId() != targetID {
			continue
		}
		for _, sess := range target.GetSessions() {
			if sess != nil && sess.GetName() == name {
				return target, sess
			}
		}
	}
	return nil, nil
}

func (s *Server) isTaskChildSession(parentTargetID, parentName, childTargetID, childName string) bool {
	_, parent := s.findSession(parentTargetID, parentName)
	if parent == nil {
		return false
	}
	reader, ok := s.backend.(SubtaskReader)
	if !ok || parent.GetId() == "" {
		return false
	}
	taskIDs := reader.SubtaskIDsByParent(parent.GetId())
	if len(taskIDs) == 0 {
		return false
	}
	taskSet := make(map[string]struct{}, len(taskIDs))
	for _, id := range taskIDs {
		taskSet[id] = struct{}{}
	}
	_, child := s.findSession(childTargetID, childName)
	if child == nil {
		return false
	}
	_, ok = taskSet[child.GetOwnerTaskId()]
	return ok
}

func (s *Server) subtaskReader() (SubtaskReader, bool) {
	reader, ok := s.backend.(SubtaskReader)
	if !ok || reader == nil {
		return nil, false
	}
	return reader, true
}

func (s *Server) childSessions(ctx context.Context, parent *pb.Session) []*childSessionItem {
	reader, ok := s.subtaskReader()
	if !ok || parent == nil || parent.GetId() == "" {
		return []*childSessionItem{}
	}
	taskIDs := reader.SubtaskIDsByParent(parent.GetId())
	if len(taskIDs) == 0 {
		return []*childSessionItem{}
	}
	taskSet := make(map[string]struct{}, len(taskIDs))
	meta := make(map[string]struct {
		status string
		title  string
	}, len(taskIDs))
	for _, id := range taskIDs {
		if id == "" {
			continue
		}
		taskSet[id] = struct{}{}
		if raw, err := reader.GetSubtaskJSON(ctx, id); err == nil && len(raw) > 0 {
			var run struct {
				Status string `json:"status"`
				Task   string `json:"task"`
			}
			if json.Unmarshal(raw, &run) == nil {
				meta[id] = struct {
					status string
					title  string
				}{status: run.Status, title: run.Task}
			}
		}
	}
	var out []*childSessionItem
	for _, target := range s.backend.ListTargets() {
		if target == nil {
			continue
		}
		for _, sess := range target.GetSessions() {
			if sess == nil {
				continue
			}
			taskID := sess.GetOwnerTaskId()
			if _, ok := taskSet[taskID]; !ok {
				continue
			}
			m := meta[taskID]
			out = append(out, &childSessionItem{
				TargetID:   target.GetId(),
				TargetName: target.GetName(),
				TaskID:     taskID,
				TaskStatus: m.status,
				TaskTitle:  m.title,
				Session:    sess,
			})
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

const viewerHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Session Output</title>
<style>
:root {
  color-scheme: light;
  --ink: #0F1720;
  --paper: #F7F8F5;
  --panel: #FFFFFF;
  --line: #CBD5D1;
  --agent: #2E6F6B;
  --input: #8A5A28;
  --error: #C63D32;
  --muted: #5E6C67;
}
* { box-sizing: border-box; }
body {
  margin: 0;
  min-height: 100vh;
  color: var(--ink);
  background: var(--paper);
  font: 14px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}
.shell {
  display: grid;
  grid-template-columns: minmax(220px, 280px) 1fr;
  min-height: 100vh;
}
.rail {
  border-right: 1px solid var(--line);
  padding: 24px 18px;
  background: rgba(255,255,255,.56);
}
.brand {
  font-size: 12px;
  font-weight: 700;
  letter-spacing: .08em;
  text-transform: uppercase;
  color: var(--agent);
}
h1 {
  margin: 10px 0 8px;
  font-size: 24px;
  line-height: 1.1;
  letter-spacing: 0;
}
.hint { color: var(--muted); margin: 0 0 24px; }
.kv { display: grid; gap: 12px; }
.kv label {
  display: block;
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: .06em;
}
.kv div {
  margin-top: 3px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  overflow-wrap: anywhere;
}
.children {
  display: none;
  margin-top: 24px;
}
.children h2 {
  margin: 0 0 9px;
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: .06em;
}
.child-list {
  display: grid;
  gap: 8px;
}
.child {
  display: grid;
  gap: 3px;
  padding: 9px 10px;
  border: 1px solid var(--line);
  border-radius: 8px;
  color: var(--ink);
  background: var(--panel);
  text-decoration: none;
}
.child:hover { border-color: var(--agent); }
.child strong {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}
.child span {
  color: var(--muted);
  font-size: 12px;
  overflow-wrap: anywhere;
}
.main { padding: 22px min(4vw, 42px); }
.toolbar {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: center;
  padding: 10px 0 16px;
  background: linear-gradient(var(--paper) 74%, rgba(247,248,245,0));
}
.status {
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 7px 10px;
  background: var(--panel);
  color: var(--muted);
  font-size: 12px;
}
.events {
  display: grid;
  gap: 10px;
  padding-bottom: 36px;
}
.event {
  position: relative;
  display: grid;
  grid-template-columns: 4px 1fr;
  gap: 12px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel);
  overflow: hidden;
}
.mark { background: var(--agent); }
.event.input .mark { background: var(--input); }
.event.error .mark { background: var(--error); }
.body { padding: 11px 13px 12px 0; min-width: 0; }
.meta {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 6px;
  color: var(--muted);
  font-size: 12px;
}
.chip {
  border: 1px solid var(--line);
  border-radius: 999px;
  padding: 1px 7px;
  background: #FAFBF8;
}
.content {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: break-word;
  word-break: normal;
  font: 14px/1.65 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}
.content.raw {
  font: 13px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.structured-details {
  margin-top: 10px;
  border-top: 1px solid var(--line);
  padding-top: 8px;
  color: var(--muted);
}
.structured-details summary { cursor: pointer; font-size: 12px; }
.structured-details pre {
  max-height: 420px;
  overflow: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  color: var(--ink);
  font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.empty {
  border: 1px dashed var(--line);
  border-radius: 8px;
  padding: 32px;
  color: var(--muted);
  background: rgba(255,255,255,.48);
}
@media (max-width: 780px) {
  .shell { grid-template-columns: 1fr; }
  .rail { border-right: 0; border-bottom: 1px solid var(--line); }
}
</style>
</head>
<body>
<div class="shell">
  <aside class="rail">
    <div class="brand">amux read-only</div>
    <h1>Session Output</h1>
    <p class="hint">recent replay + live stream. Controls stay in Feishu and CLI.</p>
    <div class="kv">
      <section><label>target</label><div id="target">-</div></section>
      <section><label>session</label><div id="session">-</div></section>
      <section><label>state</label><div id="state">-</div></section>
      <section><label>agent</label><div id="agent">-</div></section>
    </div>
    <section class="children" id="childrenBlock">
      <h2>Child sessions</h2>
      <div class="child-list" id="children"></div>
    </section>
  </aside>
  <main class="main">
    <div class="toolbar">
      <strong>Transcript</strong>
      <span class="status" id="status">connecting</span>
    </div>
    <div class="events" id="events"><div class="empty">Waiting for output...</div></div>
  </main>
</div>
<script>
const params = new URLSearchParams(location.search);
const pathToken = location.pathname.startsWith("/s/") ? decodeURIComponent(location.pathname.slice(3)) : "";
const token = params.get("token") || pathToken || "";
let targetID = params.get("target_id") || "";
let name = params.get("name") || "";
const auth = token ? {"Authorization": "Bearer " + token} : {};
document.getElementById("target").textContent = targetID || "-";
document.getElementById("session").textContent = name || "-";
let lastMergeKey = "";
let lastContent = null;

function textFromEvent(ev) {
  if (ev.text) return ev.text;
  if (ev.error_message) return ev.error_message;
  if (ev.tool_title || ev.tool_output_preview) return [ev.tool_title, ev.tool_output_preview].filter(Boolean).join("\n");
  if (!ev.data) return ev.state || ev.event_type || ev.kind || "";
  try {
    const raw = atob(ev.data);
    const bytes = Uint8Array.from(raw, c => c.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  } catch {
    return ev.data;
  }
}

function decodeJSONBytes(encoded) {
  if (!encoded) return null;
  try {
    const raw = atob(encoded);
    const bytes = Uint8Array.from(raw, c => c.charCodeAt(0));
    return JSON.parse(new TextDecoder().decode(bytes));
  } catch {
    return null;
  }
}

function structuredDetails(ev) {
  const details = {};
  for (const [label, value] of [
    ["event", ev.structured_json],
    ["tool_input", ev.tool_input_json],
    ["usage", ev.usage_json],
    ["permission", ev.permission_payload],
  ]) {
    const decoded = decodeJSONBytes(value);
    if (decoded !== null) details[label] = decoded;
  }
  return Object.keys(details).length ? details : null;
}

function eventKind(ev) {
  if (ev.error_message || ev.kind === "ERROR") return "error";
  if (ev.input) return "input";
  return "output";
}

function trimInputPrefix(line) {
  return line.replace(/^\s*(?:💻|🖥️|📱)?\s*来自(?:终端|桌面|飞书)\s*[：:]\s*/, "");
}

function inputDisplayText(ev, text) {
  if (!ev.input) return text;
  const prefix = ev.source === "lark" ? "来自飞书：" : "来自终端：";
  const lines = text.replace(/\r\n/g, "\n").split("\n").map(trimInputPrefix).filter(line => line !== "");
  return prefix + lines.join("\n");
}

function inputContinuationText(text) {
  return text.replace(/\r\n/g, "\n").split("\n").map(trimInputPrefix).filter(line => line !== "").join("\n");
}

function isPlainAgentText(ev, text) {
  if (!text || eventKind(ev) !== "output") return false;
  if (ev.tool_name || ev.tool_title || ev.tool_output_preview || ev.state || ev.event_type) return false;
  return !ev.source || ev.source === "agent";
}

function mergeKey(ev, text) {
  if (ev.input && text) return [eventKind(ev), ev.source || "input"].join(":");
  if (!isPlainAgentText(ev, text)) return "";
  return [eventKind(ev), ev.source || "agent"].join(":");
}

function appendText(target, text) {
  target.textContent += text;
}

function addEvent(ev) {
  const rawText = textFromEvent(ev);
  const text = ev.input ? inputDisplayText(ev, rawText) : rawText;
  const key = mergeKey(ev, text);
  if (key && key === lastMergeKey && lastContent) {
    appendText(lastContent, ev.input ? "\n" + inputContinuationText(rawText) : text);
    lastContent.parentElement.parentElement.scrollIntoView({block: "end"});
    return;
  }
  const list = document.getElementById("events");
  if (list.firstElementChild && list.firstElementChild.className === "empty") list.innerHTML = "";
  const row = document.createElement("article");
  row.className = "event " + eventKind(ev);
  const mark = document.createElement("div");
  mark.className = "mark";
  const body = document.createElement("div");
  body.className = "body";
  const meta = document.createElement("div");
  meta.className = "meta";
  for (const value of [ev.source, ev.event_type, ev.tool_name, ev.state].filter(Boolean)) {
    const chip = document.createElement("span");
    chip.className = "chip";
    chip.textContent = value;
    meta.appendChild(chip);
  }
  const content = document.createElement("div");
  content.className = "content";
  if (!key) content.classList.add("raw");
  content.textContent = text;
  body.appendChild(meta);
  body.appendChild(content);
  const structured = structuredDetails(ev);
  if (structured) {
    const details = document.createElement("details");
    details.className = "structured-details";
    const summary = document.createElement("summary");
    summary.textContent = "完整结构化输出";
    const raw = document.createElement("pre");
    raw.textContent = JSON.stringify(structured, null, 2);
    details.appendChild(summary);
    details.appendChild(raw);
    body.appendChild(details);
  }
  row.appendChild(mark);
  row.appendChild(body);
  list.appendChild(row);
  lastMergeKey = key;
  lastContent = key ? content : null;
  row.scrollIntoView({block: "end"});
}

function sessionQuery() {
  const q = new URLSearchParams({token});
  if (targetID) q.set("target_id", targetID);
  if (name) q.set("name", name);
  return q.toString();
}

function childHref(child) {
  const q = new URLSearchParams({token});
  q.set("target_id", child.target_id || "");
  q.set("name", (child.session || {}).name || "");
  return "/sessions?" + q.toString();
}

function renderChildren(children) {
  const block = document.getElementById("childrenBlock");
  const list = document.getElementById("children");
  list.innerHTML = "";
  if (!children || children.length === 0) {
    block.style.display = "none";
    return;
  }
  block.style.display = "block";
  for (const child of children) {
    const sess = child.session || {};
    const link = document.createElement("a");
    link.className = "child";
    link.href = childHref(child);
    const title = document.createElement("strong");
    title.textContent = sess.name || child.task_id || "child";
    const meta = document.createElement("span");
    meta.textContent = [child.target_name || child.target_id, child.task_status, child.task_title].filter(Boolean).join(" | ");
    link.appendChild(title);
    link.appendChild(meta);
    list.appendChild(link);
  }
}

fetch("/api/session?" + sessionQuery(), {headers: auth}).then(r => r.json()).then(data => {
  const s = data.session || {};
  targetID = data.target_id || targetID;
  name = s.name || name;
  document.getElementById("target").textContent = targetID || "-";
  document.getElementById("session").textContent = name || "-";
  document.getElementById("state").textContent = s.state || "-";
  document.getElementById("agent").textContent = s.agent_profile || s.agent_type || "-";
  renderChildren(data.children || []);
}).catch(() => {});

if (!token && !name) {
  document.getElementById("status").textContent = "missing session";
} else {
  const wsProto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(wsProto + "//" + location.host + "/api/session/stream?" + sessionQuery());
  ws.onopen = () => document.getElementById("status").textContent = "live";
  ws.onmessage = (msg) => {
    try {
      const ev = JSON.parse(msg.data);
      if (ev.error) {
        document.getElementById("status").textContent = ev.error;
      } else {
        addEvent(ev);
      }
    } catch {}
  };
  ws.onclose = () => document.getElementById("status").textContent = "closed";
  ws.onerror = () => document.getElementById("status").textContent = "stream error";
}
</script>
</body>
</html>`
