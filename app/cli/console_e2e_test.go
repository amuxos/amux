package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real CLI -> HTTP -> authenticated Hub -> Runtime round-trip. The provider is
// a deterministic sidecar fixture: no user installation or paid model is used.
func TestConsoleManagementE2E(t *testing.T) {
	requireLongTest(t)
	e := newCLIE2E(t)
	e.env = append(e.env, "AMUX_SESSION_ID=", "AMUX_MEMORY_BINDING=", "AMUX_ACTOR_TOKEN=", "AMUX_ACTOR_TOKEN_FILE=")
	e.writeFakeClaude(t)
	e.writeFakeClaudeSidecar(t)
	e.run(t, "setup", "--no-start")
	e.config(t, "runtime.id", "console-e2e")
	e.config(t, "runtime.name", "console-e2e")
	e.startSystemOnFreePort(t)
	e.waitForCommandOutput(t, "console target", "console-e2e", "system", "target", "list")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, e.amux, "console", "--addr", "127.0.0.1:0")
	command.Env = e.env
	command.Dir = e.repoRoot
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(e.amuxHome, "console.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
		}
	}()
	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "AMUX_CONSOLE_READY ") {
				lines <- scanner.Text()
				return
			}
		}
	}()
	var ready struct {
		URL     string `json:"url"`
		PairURL string `json:"pair_url"`
	}
	select {
	case line := <-lines:
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "AMUX_CONSOLE_READY ")), &ready); err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("console exited early: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("console not ready")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	call := func(method, path string, body any, csrf string) (int, map[string]any) {
		t.Helper()
		var data io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.NewReader(encoded)
		}
		req, err := http.NewRequest(method, ready.URL+path, data)
		if err != nil {
			t.Fatal(err)
		}
		if method == "POST" {
			req.Header.Set("Origin", ready.URL)
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("%s %s: decode status %d: %v", method, path, resp.StatusCode, err)
		}
		return resp.StatusCode, result
	}
	status, _ := call("GET", "/api/console/sessions", nil, "")
	if status != 401 {
		t.Fatalf("unauthenticated read: %d", status)
	}
	code := strings.SplitN(ready.PairURL, "#pair=", 2)[1]
	status, pair := call("POST", "/auth/pair", map[string]any{"code": code}, "")
	if status != 200 {
		t.Fatalf("pair: %d %v", status, pair)
	}
	csrf := pair["csrf_token"].(string)
	post := func(resource, action string, data any) map[string]any {
		t.Helper()
		status, result := call("POST", "/api/console/"+resource, map[string]any{"action": action, "data": data}, csrf)
		if status != 200 {
			t.Fatalf("%s/%s: %d %v", resource, action, status, result)
		}
		return result
	}
	for _, resource := range []string{"bootstrap", "overview", "sessions", "tasks", "instances", "schedules", "profiles", "packs", "credentials", "catalogs", "settings", "bots", "groups"} {
		status, result := call("GET", "/api/console/"+resource, nil, "")
		if status != 200 {
			t.Fatalf("GET %s: %d %v", resource, status, result)
		}
	}
	post("sessions", "create", map[string]any{"profile": "builtin/claude", "runtime": "claude", "name": "console-session", "target": "console-e2e", "cwd": e.amuxHome})
	status, session := call("GET", "/api/console/session?name=console-session&target=console-e2e", nil, "")
	if status != 200 || session["session"] == nil {
		t.Fatalf("session: %d %v", status, session)
	}
	post("sessions", "submit", map[string]any{"name": "console-session", "target": "console-e2e", "text": "console fixture input"})
	post("sessions", "queue-list", map[string]any{"name": "console-session", "target": "console-e2e"})
	post("sessions", "interrupt", map[string]any{"name": "console-session", "target": "console-e2e"})
	post("sessions", "sleep", map[string]any{"name": "console-session", "target": "console-e2e"})
	post("sessions", "resume", map[string]any{"name": "console-session", "target": "console-e2e"})
	post("sessions", "restart", map[string]any{"name": "console-session", "target": "console-e2e"})
	schedule := post("schedules", "create", map[string]any{"schedule_type": "session-prompt", "spec": "every 24h", "name": "console-schedule", "target_id": "console-e2e", "session_name": "console-session", "prompt": "fixture schedule prompt", "run_mode": "reuse-session"})
	id := schedule["schedule_id"]
	if id == nil {
		t.Fatalf("no schedule id: %v", schedule)
	}
	post("schedules", "disable", map[string]any{"schedule_id": id})
	post("schedules", "enable", map[string]any{"schedule_id": id})
	post("schedules", "update", map[string]any{"schedule_id": id, "name": "console-schedule-updated"})
	post("schedules", "delete", map[string]any{"schedule_id": id})
	_, settings := call("GET", "/api/console/settings", nil, "")
	post("settings", "save", map[string]any{"expected_version": settings["version"], "values": map[string]string{"maintenance.daily_cleanup.at": "06:18"}})
	status, _ = call("POST", "/api/console/settings", map[string]any{"action": "save", "data": map[string]any{"expected_version": settings["version"], "values": map[string]string{"maintenance.daily_cleanup.at": "06:19"}}}, csrf)
	if status != 409 {
		t.Fatalf("stale config not rejected: %d", status)
	}
	post("sessions", "archive", map[string]any{"name": "console-session", "target": "console-e2e"})
	status, _ = call("POST", "/auth/logout", map[string]any{}, csrf)
	if status != 200 {
		t.Fatal(status)
	}
	status, _ = call("GET", "/api/console/bootstrap", nil, "")
	if status != 401 {
		t.Fatal("logout did not invalidate session")
	}
	// Exercise the password listener's real clock and TCP shutdown, without
	// waiting thirty minutes or reusing the test runner's own installation.
	passwordCommand := exec.CommandContext(ctx, e.amux, "console", "--addr", "0.0.0.0:0", "--public-url", "http://192.0.2.20:7892", "--password-stdin", "--duration", "2s")
	passwordCommand.Env = e.env
	passwordCommand.Dir = e.repoRoot
	passwordCommand.Stdin = strings.NewReader("temporary-e2e-password-1234567890\n")
	passwordCommand.Stderr = stderr
	passwordStdout, err := passwordCommand.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := passwordCommand.Start(); err != nil {
		t.Fatal(err)
	}
	passwordDone := make(chan error, 1)
	go func() { passwordDone <- passwordCommand.Wait() }()
	defer func() { _ = passwordCommand.Process.Kill() }()
	scanner := bufio.NewScanner(passwordStdout)
	if !scanner.Scan() {
		t.Fatal("temporary password console had no readiness")
	}
	var temporary struct {
		Addr      string    `json:"addr"`
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "AMUX_CONSOLE_READY ")), &temporary); err != nil {
		t.Fatal(err)
	}
	listenHost, port, err := net.SplitHostPort(temporary.Addr)
	if err != nil {
		t.Fatal(err)
	}
	if listenHost != "0.0.0.0" {
		t.Fatalf("temporary listener READY host = %q; want exact global IPv4 for lifecycle", listenHost)
	}
	login := func() int {
		req, err := http.NewRequest("POST", "http://127.0.0.1:"+port+"/auth/login", strings.NewReader(`{"password":"temporary-e2e-password-1234567890"}`))
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(temporary.URL)
		req.Host = u.Host
		req.Header.Set("Origin", temporary.URL)
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if temporary.ExpiresAt.IsZero() || login() != 200 {
		t.Fatal("temporary listener failed password login")
	}
	select {
	case err := <-passwordDone:
		if err != nil {
			t.Fatalf("temporary listener exit: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("temporary listener did not expire")
	}
	if connection, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second); err == nil {
		connection.Close()
		t.Fatal("expired temporary port is still listening")
	}
}
