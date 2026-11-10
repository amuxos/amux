package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
)

// Deployment integration: compile this test binary as amuxd beside the actual
// amux CLI, then run with AMUX_TEST_CONSOLE_EXECUTABLE=<absolute CLI path>.
// The real Hub runs in this parent process; no user HOME or Feishu is used.
func TestManagementConsoleInstalledProcess(t *testing.T) {
	cli := os.Getenv("AMUX_TEST_CONSOLE_EXECUTABLE")
	if cli == "" {
		t.Skip("requires an assembled CLI beside this test executable named amuxd")
	}
	self, err := os.Executable()
	if err != nil || filepath.Base(self) != "amuxd" || filepath.Dir(self) != filepath.Dir(cli) {
		t.Fatal("integration test must be named amuxd and installed beside the CLI")
	}
	for _, key := range []string{"AMUX_ACTOR_TOKEN", "AMUX_ACTOR_TOKEN_FILE", "AMUX_SESSION_ID", "AMUX_TASK_ID", "AMUX_PRINCIPAL_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("AMUX_HOME", t.TempDir())
	freeAddr := func() string {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().String()
	}
	hubAddr := freeAddr()
	configPath := amuxhome.ConfigPath()
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[hub]\naddr=%q\ntoken='console-test-hub-token'\n", hubAddr)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(amuxhome.Path("hub.pid"), []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	command := newStartCmd()
	command.SetArgs([]string{"--addr", hubAddr, "--config", configPath})
	go func() { _ = command.Execute() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", hubAddr, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Hub did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Reproduce the deployed host without sending this enrollment as RPC auth.
	if err := os.MkdirAll(amuxhome.SecretsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(amuxhome.Path("secrets", "principal-enrollment.token"), []byte("host-enrollment-must-not-be-used"), 0600); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(freeAddr())
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		t.Fatal(err)
	}
	origin := "http://192.0.2.10:" + port
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newLocalManagementConsoleActions(ctx, "192.0.2.10")
	a.options = func() (int, string) { return portNumber, origin }
	a.executable = func() (string, error) { return cli, nil }
	defer a.Close(context.Background())
	info, err := a.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Enabled || len(info.Password) != 32 || time.Until(info.ExpiresAt) < 29*time.Minute {
		t.Fatal("invalid managed window")
	}
	again, err := a.Open(ctx)
	if err != nil || again.Password != info.Password || !again.ExpiresAt.Equal(info.ExpiresAt) {
		t.Fatal("reopen changed the active window")
	}
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp4", "127.0.0.1:"+port)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 10 * time.Second}
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		var input io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			input = bytes.NewReader(data)
		}
		req, _ := http.NewRequest(method, origin+path, input)
		req.Header.Set("Origin", origin)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var value map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", path, resp.StatusCode, want)
		}
		return value
	}
	call("GET", "/api/console/bootstrap", nil, 401)
	call("POST", "/auth/login", map[string]string{"password": "wrong-password"}, 401)
	call("POST", "/auth/login", map[string]string{"password": info.Password}, 200)
	for _, resource := range []string{"bootstrap", "sessions", "bots", "groups", "credentials", "schedules", "tasks", "instances", "profiles", "packs", "settings", "catalogs"} {
		call("GET", "/api/console/"+resource, nil, 200)
	}
	call("POST", "/api/console/sessions", map[string]any{"action": "interrupt", "data": map[string]string{"name": "missing"}}, 403)
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if connection, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second); err == nil {
		connection.Close()
		t.Fatal("closed console still listens")
	}
	// Even a genuine Hub child must fail when carrying explicit Agent identity.
	for _, marker := range []string{"AMUX_ACTOR_TOKEN", "AMUX_ACTOR_TOKEN_FILE", "AMUX_SESSION_ID", "AMUX_TASK_ID", "AMUX_PRINCIPAL_ID"} {
		probe := exec.CommandContext(ctx, cli, "console", "--managed-by-hub", "--duration", "1s")
		probe.Env = append(os.Environ(), marker+"=agent-context")
		if output, err := probe.CombinedOutput(); err == nil || strings.Contains(string(output), "AMUX_CONSOLE_READY") {
			t.Fatalf("managed child accepted %s", marker)
		}
	}
	// A private flag alone is insufficient, even without an enrollment file.
	if err := os.WriteFile(amuxhome.Path("hub.pid"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(amuxhome.Path("secrets", "principal-enrollment.token")); err != nil {
		t.Fatal(err)
	}
	probe := exec.CommandContext(ctx, cli, "console", "--managed-by-hub", "--duration", "1s")
	if output, err := probe.CombinedOutput(); err == nil || strings.Contains(string(output), "AMUX_CONSOLE_READY") {
		t.Fatal("unverified parent started a console")
	}
	t.Log("real managed Hub child: enrollment isolation, password login, resources, CSRF, shared lifetime and port closure passed")
}
