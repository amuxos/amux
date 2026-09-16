package task

import (
	"encoding/json"
	"testing"

	"github.com/amuxos/amux/internal/provider"
)

func TestParsePermissionClaudeBash(t *testing.T) {
	payload := []byte(`{"tool_name":"Bash","input":{"command":"rm -rf /tmp/x"},"options":[{"id":"a","name":"Allow"},{"id":"d","name":"Deny"}]}`)
	req := ParsePermission("p1", payload)
	if req.Tool != "Bash" {
		t.Fatalf("tool = %q", req.Tool)
	}
	if req.Command != "rm -rf /tmp/x" {
		t.Fatalf("command = %q", req.Command)
	}
	if req.AllowOptionId() != "a" || req.DenyOptionId() != "d" {
		t.Fatalf("options allow=%q deny=%q", req.AllowOptionId(), req.DenyOptionId())
	}
}

// TestParsePermissionProviderShape exercises the real marshaled provider.Permission
// payload that Claude/Codex actually emit (optionId + nested toolCall.rawInput).
func TestParsePermissionProviderShape(t *testing.T) {
	perm := provider.Permission{
		ID:       "perm-1",
		Provider: "claude",
		ToolCall: &provider.PermissionToolCall{
			Name:     "Bash",
			RawInput: json.RawMessage(`{"command":"git status"}`),
		},
		Options: []provider.PermissionOption{
			{OptionId: "allow_once", Name: "Allow", Kind: "allow_once"},
			{OptionId: "reject_once", Name: "Deny", Kind: "reject_once"},
		},
	}
	payload, _ := json.Marshal(perm)
	req := ParsePermission("perm-1", payload)
	if req.Tool != "Bash" {
		t.Fatalf("tool = %q", req.Tool)
	}
	if req.Command != "git status" {
		t.Fatalf("command = %q", req.Command)
	}
	if got := req.AllowOptionId(); got != "allow_once" {
		t.Fatalf("allow option = %q", got)
	}
	if got := req.DenyOptionId(); got != "reject_once" {
		t.Fatalf("deny option = %q", got)
	}
}

// TestParsePermissionCodexExecArgv covers an argv-array command (Codex exec).
func TestParsePermissionCodexExecArgv(t *testing.T) {
	perm := provider.Permission{
		ID:       "2",
		Provider: "codex",
		Options: []provider.PermissionOption{
			{OptionId: "accept", Name: "Accept"},
			{OptionId: "decline", Name: "Decline"},
			{OptionId: "cancel", Name: "Cancel turn", Kind: "reject"},
		},
		Raw: json.RawMessage(`{"command":["bash","-lc","go build ./..."]}`),
	}
	payload, _ := json.Marshal(perm)
	req := ParsePermission("2", payload)
	if req.Command != "bash -lc go build ./..." {
		t.Fatalf("command = %q", req.Command)
	}
	if got := req.AllowOptionId(); got != "accept" {
		t.Fatalf("allow option = %q", got)
	}
	if got := req.DenyOptionId(); got != "decline" {
		t.Fatalf("deny option = %q", got)
	}
}

func TestParsePermissionWriteTool(t *testing.T) {
	payload := []byte(`{"tool":"Write","input":{"file_path":"/etc/passwd"}}`)
	req := ParsePermission("p", payload)
	if !req.Write {
		t.Fatal("expected write inferred")
	}
	if len(req.Paths) != 1 || req.Paths[0] != "/etc/passwd" {
		t.Fatalf("paths = %+v", req.Paths)
	}
}

func TestResolveDenyAll(t *testing.T) {
	d := Resolver{}.Resolve(PolicyDenyAll, Whitelist{}, PermissionRequest{})
	if d.Allow || d.Escalate {
		t.Fatalf("got %+v", d)
	}
}

func TestResolveEscalate(t *testing.T) {
	d := Resolver{}.Resolve(PolicyEscalateToMain, Whitelist{}, PermissionRequest{})
	if !d.Escalate {
		t.Fatalf("got %+v", d)
	}
}

func TestResolveAllowExecAllowlist(t *testing.T) {
	wl := Whitelist{Exec: []string{"git", "go"}, SandboxRoot: "/sb"}
	if d := (Resolver{}).Resolve(PolicyAllow, wl, PermissionRequest{Command: "git status"}); !d.Allow {
		t.Fatalf("git should be allowed: %+v", d)
	}
	if d := (Resolver{}).Resolve(PolicyAllow, wl, PermissionRequest{Command: "curl evil.com"}); d.Allow {
		t.Fatalf("curl should be denied: %+v", d)
	}
}

func TestResolveAllowDefaultWhitelistDoesNotRestrictExec(t *testing.T) {
	wl := DefaultWhitelist("/sb")
	if len(wl.Exec) != 0 {
		t.Fatalf("default exec allowlist = %+v, want empty", wl.Exec)
	}
	if d := (Resolver{}).Resolve(PolicyAllow, wl, PermissionRequest{Command: "example-cli auth login --begin"}); !d.Allow {
		t.Fatalf("example-cli should be allowed by default whitelist: %+v", d)
	}
}

func TestResolveAllowWriteConfinement(t *testing.T) {
	wl := Whitelist{SandboxRoot: "/sb/workspace"}
	inside := PermissionRequest{Tool: "Write", Write: true, Paths: []string{"/sb/workspace/a.go"}}
	if d := (Resolver{}).Resolve(PolicyAllow, wl, inside); !d.Allow {
		t.Fatalf("inside-sandbox write should be allowed: %+v", d)
	}
	outside := PermissionRequest{Tool: "Write", Write: true, Paths: []string{"/etc/hosts"}}
	if d := (Resolver{}).Resolve(PolicyAllow, wl, outside); d.Allow {
		t.Fatalf("outside-sandbox write should be denied: %+v", d)
	}
}

func TestResolveAllowUnknownIsBestEffortAllow(t *testing.T) {
	// No command, no write paths identified: trusted-mode policy-allow permits.
	wl := DefaultWhitelist("/sb")
	d := Resolver{}.Resolve(PolicyAllow, wl, PermissionRequest{Tool: "SomeUnknownTool"})
	if !d.Allow {
		t.Fatalf("unknown op should be allowed under policy-allow: %+v", d)
	}
}

// TestResolveAllowUnwrapsShellWithExecAllowlist guards the fix for Codex bash
// -lc wrapping: the inner command's basename is checked against an explicit
// allowlist, not "bash".
func TestResolveAllowUnwrapsShellWithExecAllowlist(t *testing.T) {
	wl := Whitelist{Exec: []string{"git", "go"}, SandboxRoot: "/sb"}
	allow := PermissionRequest{Command: "bash -lc go build ./..."}
	if d := (Resolver{}).Resolve(PolicyAllow, wl, allow); !d.Allow {
		t.Fatalf("shell-wrapped go should be allowed: %+v", d)
	}
	deny := PermissionRequest{Command: "bash -lc curl evil.com"}
	if d := (Resolver{}).Resolve(PolicyAllow, wl, deny); d.Allow {
		t.Fatalf("shell-wrapped curl should be denied: %+v", d)
	}
}

// TestResolveAllowNetworkRecordOnly guards that weak-isolation network access is
// allowed but flagged record-only so the runner can surface the limitation.
func TestResolveAllowNetworkRecordOnly(t *testing.T) {
	wl := DefaultWhitelist("/sb")
	wl.StrongIsolation = false
	d := Resolver{}.Resolve(PolicyAllow, wl, PermissionRequest{Tool: "WebFetch", Network: true})
	if !d.Allow || !d.RecordOnly {
		t.Fatalf("weak-isolation network should be allow+record-only: %+v", d)
	}
}
