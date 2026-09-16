package hub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/updatecheck"
)

func TestLocalManagementSystemActionsCheckUpdate(t *testing.T) {
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tw := tar.NewWriter(gz)
	manifest := []byte(`{"version":"v1.2.0","assets":{"linux_amd64":{}}}`)
	if err := tw.WriteHeader(&tar.Header{Name: "latest.json", Mode: 0600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload.Bytes())
	}))
	defer server.Close()

	actions := &localManagementSystemActions{
		httpClient: server.Client(),
		latestURL:  server.URL,
		goos:       "linux",
		goarch:     "amd64",
	}
	actions.monitor = newTestUpdateMonitor(t, updatecheck.MonitorOptions{
		CurrentVersion: "v1.1.0",
		Source:         server.URL,
		StatePath:      filepath.Join(t.TempDir(), "update-state.json"),
		FetchLatest: func(ctx context.Context) (string, error) {
			return updatecheck.LatestVersion(ctx, actions.httpClient, actions.latestURL, actions.goos, actions.goarch)
		},
	})
	result, err := actions.CheckUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckUpdate: %v", err)
	}
	if result.CurrentVersion != "v1.1.0" || result.LatestVersion != "v1.2.0" || !result.UpdateAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestLocalManagementSystemActionsSharesCachedUpdateWithManagementSurfaces(t *testing.T) {
	actions := &localManagementSystemActions{}
	actions.monitor = newTestUpdateMonitor(t, updatecheck.MonitorOptions{
		CurrentVersion: "1.0.0.1457",
		Source:         "https://example.test/latest",
		StatePath:      filepath.Join(t.TempDir(), "update-state.json"),
		Interval:       12 * time.Hour,
		FetchLatest: func(context.Context) (string, error) {
			return "1.0.0.1458", nil
		},
	})

	result, err := actions.CheckUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "1.0.0.1458" {
		t.Fatalf("checked result = %+v", result)
	}
	if cached := actions.UpdateSnapshot(); !cached.UpdateAvailable || !cached.ShouldPrompt {
		t.Fatalf("cached result = %+v", cached)
	}
	if err := actions.DismissUpdate("1.0.0.1458"); err != nil {
		t.Fatal(err)
	}
	if cached := actions.UpdateSnapshot(); !cached.UpdateAvailable || cached.ShouldPrompt {
		t.Fatalf("dismissed result = %+v", cached)
	}
}

func TestLocalManagementSystemActionsDoesNotAdvertiseDowngrade(t *testing.T) {
	actions := &localManagementSystemActions{}
	actions.monitor = newTestUpdateMonitor(t, updatecheck.MonitorOptions{
		CurrentVersion: "1.0.0.1459",
		Source:         "https://example.test/latest",
		StatePath:      filepath.Join(t.TempDir(), "update-state.json"),
		FetchLatest: func(context.Context) (string, error) {
			return "1.0.0.1458", nil
		},
	})
	result, err := actions.CheckUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("downgrade advertised as update: %+v", result)
	}
}

func newTestUpdateMonitor(t *testing.T, options updatecheck.MonitorOptions) *updatecheck.Monitor {
	t.Helper()
	monitor, err := updatecheck.NewMonitor(options)
	if err != nil {
		t.Fatal(err)
	}
	return monitor
}

func TestLocalManagementSystemActionsRejectsConcurrentMaintenance(t *testing.T) {
	actions := &localManagementSystemActions{}
	if !actions.begin("restart") {
		t.Fatal("first maintenance begin = false")
	}
	if actions.begin("update_check") {
		t.Fatal("concurrent maintenance begin = true")
	}
	actions.end("restart")
	if !actions.begin("update_check") {
		t.Fatal("maintenance begin after release = false")
	}
}

func TestLocalManagementSystemActionsReportsFastRestartFailure(t *testing.T) {
	binDir := t.TempDir()
	amuxPath := filepath.Join(binDir, "amux")
	if err := os.WriteFile(amuxPath, []byte("#!/bin/sh\nexit 23\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	actions := &localManagementSystemActions{executable: amuxPath}
	if err := actions.Restart(context.Background()); err == nil {
		t.Fatal("Restart fast failure = nil, want error")
	}
	if actions.active != "" {
		t.Fatalf("active = %q after restart failure, want empty", actions.active)
	}
}

func TestLocalManagementSystemActionsUpgradeRunsCheckedVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Artifact-Version", "1.2.3")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	binDir := t.TempDir()
	argsPath := filepath.Join(binDir, "args")
	amuxPath := filepath.Join(binDir, "amux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n"
	if err := os.WriteFile(amuxPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	actions := &localManagementSystemActions{
		httpClient: server.Client(),
		latestURL:  server.URL,
		goos:       "linux",
		goarch:     "amd64",
		executable: amuxPath,
	}
	if err := actions.Upgrade(context.Background(), "1.2.3"); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "update\n--restart\n--expected-version\n1.2.3\n--scm-latest-url\n"+server.URL+"\n" {
		t.Fatalf("args = %q", args)
	}
	if actions.active != "" {
		t.Fatalf("active = %q after upgrade, want empty", actions.active)
	}
}

func TestLocalManagementSystemActionsUpgradeRejectsChangedLatestVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Artifact-Version", "1.2.4")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	actions := &localManagementSystemActions{
		httpClient: server.Client(),
		latestURL:  server.URL,
		goos:       "linux",
		goarch:     "amd64",
		executable: filepath.Join(t.TempDir(), "must-not-run"),
	}
	if err := actions.Upgrade(context.Background(), "1.2.3"); err == nil {
		t.Fatal("Upgrade changed latest version = nil, want error")
	}
}
