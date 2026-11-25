package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/updatecheck"
)

type localManagementSystemActions struct {
	httpClient *http.Client
	latestURL  string
	goos       string
	goarch     string
	executable string
	monitor    *updatecheck.Monitor
	mu         sync.Mutex
	active     string
}

func newLocalManagementSystemActions() *localManagementSystemActions {
	actions := &localManagementSystemActions{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		latestURL:  updatecheck.LatestURL(),
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
	}
	monitor, err := updatecheck.NewMonitor(updatecheck.MonitorOptions{
		CurrentVersion: version,
		Source:         actions.latestURL,
		FetchLatest: func(ctx context.Context) (string, error) {
			return updatecheck.LatestVersion(ctx, actions.httpClient, actions.latestURL, actions.goos, actions.goarch)
		},
	})
	actions.monitor = monitor
	if err != nil {
		fmt.Fprintf(os.Stderr, "hub: initialize update checker: %v\n", err)
	}
	return actions
}

func (a *localManagementSystemActions) Restart(ctx context.Context) error {
	if !a.begin("restart") {
		return fmt.Errorf("system maintenance already in progress")
	}
	if err := ctx.Err(); err != nil {
		a.end("restart")
		return err
	}
	amuxPath, err := a.amuxExecutable()
	if err != nil {
		a.end("restart")
		return err
	}
	cmd := exec.Command(amuxPath, "system", "restart", "--force")
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Env = stripHubAgentContextEnv(os.Environ())
	// managedService.Stop terminates the amuxd process group. Start the
	// lifecycle command in its own session so it survives long enough to bring
	// the system back up.
	configureDetachedCommand(cmd)
	if err := cmd.Start(); err != nil {
		a.end("restart")
		return fmt.Errorf("start amux system restart: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			time.AfterFunc(30*time.Second, func() { a.end("restart") })
			return nil
		}
		a.end("restart")
		return fmt.Errorf("amux system restart failed: %w", err)
	case <-time.After(5 * time.Second):
		// A normal restart stops this amuxd before the lifecycle command
		// returns. If it is still running, leave the detached command in charge.
		time.AfterFunc(30*time.Second, func() { a.end("restart") })
		return nil
	}
}

func (a *localManagementSystemActions) CheckUpdate(ctx context.Context) (bridge.ManagementUpdateInfo, error) {
	if !a.begin("update_check") {
		return bridge.ManagementUpdateInfo{}, fmt.Errorf("system maintenance already in progress")
	}
	defer a.end("update_check")
	if a.monitor == nil {
		return bridge.ManagementUpdateInfo{}, fmt.Errorf("update checker is not configured")
	}
	state, err := a.monitor.Refresh(ctx)
	if err != nil {
		return bridge.ManagementUpdateInfo{}, err
	}
	return managementUpdateInfoFromState(state), nil
}

func (a *localManagementSystemActions) UpdateSnapshot() bridge.ManagementUpdateInfo {
	if a == nil || a.monitor == nil {
		return bridge.ManagementUpdateInfo{}
	}
	return managementUpdateInfoFromState(a.monitor.Snapshot())
}

func (a *localManagementSystemActions) DismissUpdate(version string) error {
	if a == nil || a.monitor == nil {
		return fmt.Errorf("update checker is not configured")
	}
	return a.monitor.Dismiss(version)
}

func (a *localManagementSystemActions) RunUpdateMonitor(ctx context.Context) {
	if a != nil && a.monitor != nil {
		a.monitor.Run(ctx)
	}
}

func managementUpdateInfoFromState(state updatecheck.State) bridge.ManagementUpdateInfo {
	return bridge.ManagementUpdateInfo{
		CurrentVersion:  state.CurrentVersion,
		LatestVersion:   state.LatestVersion,
		UpdateAvailable: state.UpdateAvailable(),
		ShouldPrompt:    state.ShouldPrompt(),
		CheckedAt:       state.CheckedAt,
	}
}

func (a *localManagementSystemActions) Upgrade(ctx context.Context, expectedVersion string) error {
	if !a.begin("upgrade") {
		return fmt.Errorf("system maintenance already in progress")
	}
	defer a.end("upgrade")
	if err := ctx.Err(); err != nil {
		return err
	}
	expectedVersion = strings.TrimSpace(expectedVersion)
	if expectedVersion == "" {
		return fmt.Errorf("expected upgrade version is required")
	}
	latestVersion, err := updatecheck.LatestVersion(ctx, a.httpClient, a.latestURL, a.goos, a.goarch)
	if err != nil {
		return fmt.Errorf("recheck latest amux version: %w", err)
	}
	latestVersion = strings.TrimSpace(latestVersion)
	if latestVersion != expectedVersion {
		return fmt.Errorf("available amux version changed from %q to %q", expectedVersion, latestVersion)
	}
	amuxPath, err := a.amuxExecutable()
	if err != nil {
		return err
	}
	cmd := exec.Command(
		amuxPath,
		"update",
		"--restart",
		"--expected-version", expectedVersion,
		"--scm-latest-url", a.latestURL,
	)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	var diagnostic maintenanceStderr
	cmd.Stderr = &diagnostic
	cmd.Env = stripHubAgentContextEnv(os.Environ())
	// The installed update command restarts amuxd. Keep it outside the amuxd
	// process group so it survives the service stop and can finish the handoff.
	configureDetachedCommand(cmd)
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(string(diagnostic.data)); detail != "" {
			return fmt.Errorf("amux update --restart failed: %w: %s", err, detail)
		}
		return fmt.Errorf("amux update --restart failed: %w", err)
	}
	return nil
}

func (a *localManagementSystemActions) begin(action string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active != "" {
		return false
	}
	a.active = action
	return true
}

func (a *localManagementSystemActions) end(action string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active == action {
		a.active = ""
	}
}

func localAmuxExecutable() (string, error) {
	executable, _ := os.Executable()
	return localAmuxExecutableFrom(executable, exec.LookPath)
}

func localAmuxExecutableFrom(executable string, lookPath func(string) (string, error)) (string, error) {
	name := "amux"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var candidates []string
	if executable != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), name))
	}
	// A running executable may have been renamed into a backup directory that
	// was removed after activation. Resolve the current installation as well.
	var metadata struct {
		BinDir string `json:"bin_dir"`
	}
	if data, err := os.ReadFile(amuxhome.Path("install.json")); err == nil && json.Unmarshal(data, &metadata) == nil && filepath.IsAbs(metadata.BinDir) {
		candidates = append(candidates, filepath.Join(metadata.BinDir, name))
	}
	candidates = append(candidates, amuxhome.Path("bin", name))
	for _, candidate := range candidates {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if candidate, err := lookPath(name); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("amux executable not found")
}

// Keep the last error lines without buffering an entire installer transcript.
type maintenanceStderr struct{ data []byte }

func (w *maintenanceStderr) Write(p []byte) (int, error) {
	const limit = 8 * 1024
	n := len(p)
	if n >= limit {
		w.data = append(w.data[:0], p[n-limit:]...)
		return n, nil
	}
	if len(w.data)+n > limit {
		w.data = w.data[len(w.data)+n-limit:]
	}
	w.data = append(w.data, p...)
	return n, nil
}

func (a *localManagementSystemActions) amuxExecutable() (string, error) {
	if a != nil && strings.TrimSpace(a.executable) != "" {
		return strings.TrimSpace(a.executable), nil
	}
	return localAmuxExecutable()
}

func stripHubAgentContextEnv(env []string) []string {
	blocked := []string{
		"AMUX_ACTOR_TOKEN",
		"AMUX_ACTOR_TOKEN_FILE",
		"AMUX_SESSION_ID",
		"AMUX_TASK_ID",
		"AMUX_PRINCIPAL_ID",
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		skip := false
		for _, name := range blocked {
			if strings.EqualFold(key, name) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
