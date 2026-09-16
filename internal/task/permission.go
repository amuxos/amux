package task

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/agentstream"
)

// PermissionRequest is the best-effort parsed form of a provider permission
// payload. Providers differ, so every field is optional; the resolver degrades
// gracefully when a field cannot be determined.
type PermissionRequest struct {
	PermissionId string
	Tool         string   // tool name (e.g. "Bash", "Write")
	Command      string   // exec argv0 if identifiable
	Paths        []string // filesystem paths touched if identifiable
	Write        bool     // whether the op writes (best-effort)
	Network      bool     // whether the op touches the network (best-effort)
	options      []permOption
	Raw          []byte
}

type permOption struct {
	OptionId string `json:"optionId"` // provider.PermissionOption shape (Claude/Codex/Acp)
	ID       string `json:"id"`       // generic fallback shape
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// id returns the option identifier regardless of which field the provider used.
func (o permOption) id() string {
	if o.OptionId != "" {
		return o.OptionId
	}
	return o.ID
}

// permToolCall mirrors provider.PermissionToolCall (Claude SDK / Acp) where the
// tool name and arguments live under a nested toolCall object.
type permToolCall struct {
	Name     string          `json:"name"`
	Title    string          `json:"title"`
	RawInput json.RawMessage `json:"rawInput"`
	Input    json.RawMessage `json:"input"`
}

// permPayload is a permissive view over the various provider permission shapes:
// the marshaled provider.Permission emitted by Claude/Codex, the raw Acp
// session/request_permission params, and the generic flat shape.
type permPayload struct {
	Tool     string          `json:"tool"`
	ToolName string          `json:"tool_name"`
	Name     string          `json:"name"`
	Title    string          `json:"title"`
	Command  string          `json:"command"`
	Cmd      string          `json:"cmd"`
	Path     string          `json:"path"`
	FilePath string          `json:"file_path"`
	Paths    []string        `json:"paths"`
	Input    json.RawMessage `json:"input"`
	ToolCall *permToolCall   `json:"toolCall"`
	Raw      json.RawMessage `json:"raw"` // Codex stashes the original approval params here
	Options  []permOption    `json:"options"`
	Kind     string          `json:"kind"`
}

// ParsePermission extracts an actionable PermissionRequest from a raw provider
// permission payload. It never errors; unknown payloads yield an empty (but
// usable) request that the resolver treats conservatively.
func ParsePermission(permissionID string, payload []byte) PermissionRequest {
	req := PermissionRequest{PermissionId: permissionID, Raw: payload}
	var p permPayload
	if err := json.Unmarshal(payload, &p); err == nil {
		req.Tool = firstNonEmpty(p.Tool, p.ToolName, p.Name)
		req.Command = firstNonEmpty(p.Command, p.Cmd)
		req.options = p.Options
		if pth := firstNonEmpty(p.Path, p.FilePath); pth != "" {
			req.Paths = append(req.Paths, pth)
		}
		req.Paths = append(req.Paths, p.Paths...)
		if p.ToolCall != nil {
			if req.Tool == "" {
				req.Tool = firstNonEmpty(p.ToolCall.Name, p.ToolCall.Title)
			}
			req.digInput(firstNonEmpty2(p.ToolCall.RawInput, p.ToolCall.Input))
		}
		// Generic flat input, then Codex's stashed raw params, as fallbacks.
		req.digInput(p.Input)
		req.digInput(p.Raw)
	}
	req.Write = req.Write || inferWrite(req.Tool)
	if isNetworkTool(req.Tool) {
		req.Network = true
	}
	return req
}

// PermissionFromAgentEvent projects the canonical permission model into the
// Task policy model without reconstructing a provider/session event envelope.
func PermissionFromAgentEvent(permission agentstream.PermissionRequest) PermissionRequest {
	req := PermissionRequest{PermissionId: permission.ID, Raw: append([]byte(nil), permission.Details...)}
	if permission.ToolCall != nil {
		req.Tool = firstNonEmpty(permission.ToolCall.Name, permission.ToolCall.Title)
		req.digInput(permission.ToolCall.RawInput)
	}
	for _, option := range permission.Options {
		req.options = append(req.options, permOption{ID: option.ID, Name: option.Name, Kind: option.Kind})
	}
	// Some providers keep the actionable fields only in their raw request.
	// Reuse the permissive parser as a fallback, then keep canonical identity
	// and options authoritative.
	if len(permission.Details) > 0 {
		fallback := ParsePermission(permission.ID, permission.Details)
		if req.Tool == "" {
			req.Tool = fallback.Tool
		}
		if req.Command == "" {
			req.Command = fallback.Command
		}
		req.Paths = append(req.Paths, fallback.Paths...)
		req.Write = req.Write || fallback.Write
		req.Network = req.Network || fallback.Network
		if len(req.options) == 0 {
			req.options = fallback.options
		}
	}
	req.Write = req.Write || inferWrite(req.Tool)
	if isNetworkTool(req.Tool) {
		req.Network = true
	}
	return req
}

// digInput extracts command/path/url hints from a nested provider input object,
// tolerating both a string command (Claude Bash) and an argv array (Codex exec).
func (r *PermissionRequest) digInput(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var in struct {
		Command  json.RawMessage `json:"command"`
		Cmd      json.RawMessage `json:"cmd"`
		Path     string          `json:"path"`
		FilePath string          `json:"file_path"`
		Paths    []string        `json:"paths"`
		URL      string          `json:"url"`
		Changes  json.RawMessage `json:"changes"`
		Patch    json.RawMessage `json:"patch"`
		// Codex/Acp file-change approval variants (best-effort; param schemas
		// differ across providers and versions).
		FileChange  json.RawMessage `json:"fileChange"`
		FileChanges json.RawMessage `json:"file_changes"`
		Diff        json.RawMessage `json:"unified_diff"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return
	}
	if r.Command == "" {
		r.Command = firstNonEmpty(commandString(in.Command), commandString(in.Cmd))
	}
	if pth := firstNonEmpty(in.Path, in.FilePath); pth != "" {
		r.Paths = append(r.Paths, pth)
	}
	r.Paths = append(r.Paths, in.Paths...)
	if in.URL != "" {
		r.Network = true
	}
	// A patch/changes payload (Codex apply_patch, file change approval) is a write.
	if len(in.Changes) > 0 || len(in.Patch) > 0 || len(in.FileChange) > 0 || len(in.FileChanges) > 0 || len(in.Diff) > 0 {
		r.Write = true
		r.Paths = append(r.Paths, changedPaths(in.Changes)...)
		r.Paths = append(r.Paths, changedPaths(in.FileChanges)...)
	}
}

// commandString renders a JSON command field that may be a string or an argv
// array into a single command line.
func commandString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(raw, &argv) == nil {
		return strings.Join(argv, " ")
	}
	return ""
}

// changedPaths extracts file paths from a Codex-style changes object whose keys
// are paths. Best-effort: unknown shapes yield nothing.
func changedPaths(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var byPath map[string]json.RawMessage
	if json.Unmarshal(raw, &byPath) != nil {
		return nil
	}
	out := make([]string, 0, len(byPath))
	for p := range byPath {
		out = append(out, p)
	}
	return out
}

// AllowOptionId returns the provider option id that approves the request, or ""
// if none can be identified (caller should fall back to a sensible default).
func (r PermissionRequest) AllowOptionId() string { return r.pickOption(true) }

// DenyOptionId returns the provider option id that rejects the request.
func (r PermissionRequest) DenyOptionId() string { return r.pickOption(false) }

// FirstOptionId returns the first offered option id, used as a best-effort
// fallback when no option name matches the allow vocabulary.
func (r PermissionRequest) FirstOptionId() string {
	if len(r.options) > 0 {
		return r.options[0].id()
	}
	return ""
}

// LastOptionId returns the last offered option id, used as a best-effort deny
// fallback (providers conventionally list reject/cancel last).
func (r PermissionRequest) LastOptionId() string {
	if n := len(r.options); n > 0 {
		return r.options[n-1].id()
	}
	return ""
}

// ResolveOptionId picks the provider option id for an allow/deny decision,
// applying the best-effort fallbacks: the first offered option for allow and
// the last for deny when no option name matches the allow/deny vocabulary.
func (r PermissionRequest) ResolveOptionId(allow bool) string {
	if allow {
		if id := r.AllowOptionId(); id != "" {
			return id
		}
		return r.FirstOptionId()
	}
	if id := r.DenyOptionId(); id != "" {
		return id
	}
	return r.LastOptionId()
}

func (r PermissionRequest) pickOption(allow bool) string {
	allowWords := []string{"allow", "yes", "approve", "accept", "proceed", "always"}
	denyWords := []string{"deny", "no", "reject", "decline", "cancel", "never"}
	want := allowWords
	if !allow {
		want = denyWords
	}
	for _, o := range r.options {
		hay := strings.ToLower(o.Name + " " + o.Kind + " " + o.id())
		for _, w := range want {
			if strings.Contains(hay, w) {
				return o.id()
			}
		}
	}
	return ""
}

// Whitelist is the policy-allow allow-set for a sandbox. Empty Exec means "no
// exec restriction" (best-effort trusted-mode default). Paths under SandboxRoot
// are always writable.
type Whitelist struct {
	FSWrite     []string // additional allowed write path prefixes
	FSRead      []string // additional allowed read path prefixes
	Exec        []string // allowed argv0 basenames; empty = unrestricted
	Network     []string // allowed network targets; empty = record-only under weak isolation
	SandboxRoot string
	// StrongIsolation indicates an OS-level boundary (container/vm). Under weak
	// isolation the network dimension is record-only (cannot be enforced).
	StrongIsolation bool
}

// DefaultWhitelist returns the Phase 1 private-home baseline: writes confined to
// the sandbox, exec unrestricted, network record-only.
func DefaultWhitelist(sandboxRoot string) Whitelist {
	return Whitelist{
		SandboxRoot: sandboxRoot,
	}
}

// Decision is the resolver verdict.
type Decision struct {
	Allow    bool
	Escalate bool
	Reason   string
	// RecordOnly marks an allow that could not be enforced (e.g. network under
	// weak isolation). The runner surfaces it as an informational diagnostic.
	RecordOnly bool
}

// Resolver applies a permission policy to a parsed request.
type Resolver struct{}

// Resolve decides how to answer a child permission request under the given
// policy and whitelist.
func (Resolver) Resolve(policy PermissionPolicy, wl Whitelist, req PermissionRequest) Decision {
	switch policy {
	case PolicyDenyAll:
		return Decision{Allow: false, Reason: "deny-all policy"}
	case PolicyEscalateToMain:
		return Decision{Escalate: true, Reason: "escalate-to-main"}
	case PolicyAllow, "":
		return resolveAllow(wl, req)
	default:
		// Unknown policy: fail closed.
		return Decision{Allow: false, Reason: "unknown permission policy"}
	}
}

func resolveAllow(wl Whitelist, req PermissionRequest) Decision {
	// Exec restriction (best-effort): if we identified a command and an exec
	// allowlist exists, the effective command's basename must be listed. Shell
	// wrappers (bash -lc "<cmd>") are unwrapped to the inner command so a Codex
	// argv like ["bash","-lc","go build"] is checked against "go", not "bash".
	if req.Command != "" && len(wl.Exec) > 0 {
		base := effectiveExec(req.Command)
		if !contains(wl.Exec, base) {
			return Decision{Allow: false, Reason: "exec not in allowlist: " + base}
		}
	}
	// Write confinement (best-effort): identified write paths must be inside the
	// sandbox or an allowed prefix.
	if req.Write {
		for _, p := range req.Paths {
			if !pathAllowed(p, wl.SandboxRoot, wl.FSWrite) {
				return Decision{Allow: false, Reason: "write outside sandbox: " + p}
			}
		}
	}
	// Network: under weak isolation (no OS boundary) the network dimension cannot
	// be enforced — allow but mark record-only so the limitation is surfaced.
	if req.Network && !wl.StrongIsolation {
		return Decision{Allow: true, Reason: "network allowed but not enforced under weak isolation (record-only)", RecordOnly: true}
	}
	if req.Network && len(wl.Network) > 0 {
		// Strong isolation with an allowlist: Phase 1 cannot reliably extract the
		// target host, so allow and record rather than misclassify.
		return Decision{Allow: true, Reason: "network allowed (target not enforced)", RecordOnly: true}
	}
	return Decision{Allow: true, Reason: "within policy-allow baseline"}
}

// effectiveExec returns the basename of the command that will actually run,
// unwrapping a shell wrapper's -c/-lc script to its first word. Best-effort: a
// complex pipeline inside the script is reduced to its first command.
func effectiveExec(command string) string {
	toks := strings.Fields(command)
	if len(toks) == 0 {
		return ""
	}
	base := filepath.Base(strings.Trim(toks[0], `"'`))
	if isShell(base) {
		for i := 1; i < len(toks); i++ {
			if isShellCommandFlag(toks[i]) {
				if i+1 < len(toks) {
					return filepath.Base(strings.Trim(toks[i+1], `"'`))
				}
				break
			}
		}
	}
	return base
}

func isShell(base string) bool {
	switch base {
	case "bash", "sh", "zsh", "dash", "ksh", "fish":
		return true
	default:
		return false
	}
}

// isShellCommandFlag reports whether a shell argument requests inline command
// execution (-c, -lc, -ic, -lic, ...), i.e. the next token is the script.
func isShellCommandFlag(arg string) bool {
	return len(arg) >= 2 && arg[0] == '-' && strings.Contains(arg, "c")
}

func pathAllowed(p, sandboxRoot string, extra []string) bool {
	if p == "" {
		return true // nothing identifiable
	}
	ap := p
	if !filepath.IsAbs(ap) && sandboxRoot != "" {
		ap = filepath.Join(sandboxRoot, ap)
	}
	ap = filepath.Clean(ap)
	if sandboxRoot != "" && within(ap, sandboxRoot) {
		return true
	}
	for _, pre := range extra {
		if within(ap, pre) {
			return true
		}
	}
	return false
}

func within(path, root string) bool {
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func inferWrite(tool string) bool {
	switch strings.ToLower(tool) {
	case "write", "edit", "multiedit", "notebookedit", "create", "apply_patch", "applypatch", "patch":
		return true
	default:
		return false
	}
}

func isNetworkTool(tool string) bool {
	switch strings.ToLower(tool) {
	case "webfetch", "websearch", "fetch", "curl", "browser":
		return true
	default:
		return false
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmpty2 returns the first non-empty raw JSON message.
func firstNonEmpty2(vals ...json.RawMessage) json.RawMessage {
	for _, v := range vals {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
