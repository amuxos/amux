package hub

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/consoleentry"
)

const managementConsoleDuration = 30 * time.Minute

// Only this manager's child is signalled. No PID file, process search, detached
// process, password argument, or child diagnostic is used by this lifecycle.
type consoleLifecycleProcess interface {
	Wait() error
	Signal(os.Signal) error
	Kill() error
}

type consoleLifecycleCommand struct{ *exec.Cmd }

func (p consoleLifecycleCommand) Signal(signal os.Signal) error { return p.Process.Signal(signal) }
func (p consoleLifecycleCommand) Kill() error                   { return p.Process.Kill() }

type consoleLifecycleRun struct {
	process consoleLifecycleProcess
	done    chan struct{}
}

type localManagementConsoleActions struct {
	ctx context.Context
	// gate serializes the entire start/readiness/stop transition across Bots.
	gate chan struct{}
	mu   sync.Mutex
	run  *consoleLifecycleRun
	info bridge.ManagementConsoleInfo

	options          func() (int, string)
	hasDynamicOrigin func() bool
	executable       func() (string, error)
	start            func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error)
	random           io.Reader
	now              func() time.Time
	startupTimeout   time.Duration
	stopTimeout      time.Duration
}

func newLocalManagementConsoleActions(ctx context.Context, hubIP string) *localManagementConsoleActions {
	return newLocalManagementConsoleActionsWithConfig(ctx, hubIP, "")
}

func newLocalManagementConsoleActionsWithConfig(ctx context.Context, hubIP, configPath string) *localManagementConsoleActions {
	a := &localManagementConsoleActions{
		ctx: ctx, gate: make(chan struct{}, 1),
		hasDynamicOrigin: consoleentry.HasActivePublicOriginResolver,
		executable:       localAmuxExecutable, start: startConsoleLifecycleProcess,
		random: rand.Reader, now: time.Now,
		startupTimeout: 12 * time.Second, stopTimeout: 2 * time.Second,
	}
	a.options = func() (int, string) {
		port := config.GetConsolePort()
		origin := config.GetConsolePublicURL()
		if strings.TrimSpace(configPath) != "" {
			if cfg, err := config.LoadConsoleFrom(configPath); err == nil {
				port = cfg.Port
				origin = cfg.PublicURL
			}
		}
		if origin == "" && (a.hasDynamicOrigin == nil || !a.hasDynamicOrigin()) && strings.TrimSpace(hubIP) != "" {
			origin = "http://" + net.JoinHostPort(hubIP, strconv.Itoa(port))
		}
		return port, origin
	}
	return a
}

func startConsoleLifecycleProcess(ctx context.Context, executable string, args []string, password string) (consoleLifecycleProcess, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	// Password input is a single line on an anonymous pipe, never a flag or env.
	cmd.Stdin = strings.NewReader(password + "\n")
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, errors.New("console output unavailable")
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, nil, errors.New("console process could not start")
	}
	return consoleLifecycleCommand{cmd}, stdout, nil
}

func (a *localManagementConsoleActions) Snapshot() bridge.ManagementConsoleInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.info.ExpiresAt.After(a.now()) {
		return bridge.ManagementConsoleInfo{}
	}
	return a.info
}

func (a *localManagementConsoleActions) lock(ctx context.Context) error {
	select {
	case a.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *localManagementConsoleActions) Open(ctx context.Context) (bridge.ManagementConsoleInfo, error) {
	if err := a.lock(ctx); err != nil {
		return bridge.ManagementConsoleInfo{}, err
	}
	defer func() { <-a.gate }()
	if err := ctx.Err(); err != nil {
		return bridge.ManagementConsoleInfo{}, err
	}
	if err := a.ctx.Err(); err != nil {
		return bridge.ManagementConsoleInfo{}, err
	}
	a.mu.Lock()
	run, info := a.run, a.info
	a.mu.Unlock()
	if run != nil {
		if info.Enabled && info.ExpiresAt.After(a.now()) {
			return info, nil // Reopening never extends the original expiry.
		}
		if err := a.stopLocked(context.Background(), run); err != nil {
			return bridge.ManagementConsoleInfo{}, err
		}
	}
	port, origin := a.options()
	dynamicOrigin := origin == "" && a.hasDynamicOrigin != nil && a.hasDynamicOrigin()
	if dynamicOrigin {
		if port < 1 || port > 65535 {
			return bridge.ManagementConsoleInfo{}, errors.New("console requires a reachable public origin")
		}
	} else {
		var err error
		origin, err = managementConsoleOrigin(port, origin)
		if err != nil {
			return bridge.ManagementConsoleInfo{}, err
		}
	}
	executable, err := a.executable()
	if err != nil {
		return bridge.ManagementConsoleInfo{}, errors.New("console executable unavailable")
	}
	secret := make([]byte, 24)
	if _, err := io.ReadFull(a.random, secret); err != nil {
		return bridge.ManagementConsoleInfo{}, errors.New("console password generation failed")
	}
	password := base64.RawURLEncoding.EncodeToString(secret)
	addr := consoleListenAddr(port, origin)
	startedAt := a.now()
	args := []string{"console", "--addr", addr, "--duration", "30m"}
	if origin != "" {
		args = append(args, "--public-url", origin)
	}
	args = append(args, "--password-stdin", "--managed-by-hub")
	process, stdout, err := a.start(a.ctx, executable, args, password)
	if err != nil {
		return bridge.ManagementConsoleInfo{}, errors.New("console process could not start")
	}
	run = &consoleLifecycleRun{process: process, done: make(chan struct{})}
	a.mu.Lock()
	a.run = run
	a.mu.Unlock()
	go func() {
		_ = process.Wait() // Never retain or print child output or errors.
		_ = stdout.Close()
		a.mu.Lock()
		if a.run == run {
			a.run, a.info = nil, bridge.ManagementConsoleInfo{}
		}
		close(run.done)
		a.mu.Unlock()
	}()
	ready := make(chan consoleLifecycleReady, 1)
	go readConsoleLifecycleReady(stdout, ready)
	startupCtx, cancel := context.WithTimeout(ctx, a.startupTimeout)
	defer cancel()
	var result consoleLifecycleReady
	select {
	case result = <-ready:
	case <-run.done:
	case <-a.ctx.Done():
	case <-startupCtx.Done():
	}
	expiresAt := result.ExpiresAt
	if maximum := startedAt.Add(managementConsoleDuration); expiresAt.After(maximum) {
		expiresAt = maximum
	}
	resolvedOrigin := origin
	if dynamicOrigin {
		resolvedOrigin, err = managementConsoleOrigin(port, result.URL)
		if err != nil {
			_ = a.stopLocked(context.Background(), run)
			return bridge.ManagementConsoleInfo{}, errors.New("console did not become ready")
		}
	}
	if result.Addr != addr || strings.TrimRight(result.URL, "/") != resolvedOrigin || !expiresAt.After(a.now()) || startupCtx.Err() != nil || a.ctx.Err() != nil {
		_ = a.stopLocked(context.Background(), run)
		return bridge.ManagementConsoleInfo{}, errors.New("console did not become ready")
	}
	info = bridge.ManagementConsoleInfo{Enabled: true, URL: resolvedOrigin, Password: password, ExpiresAt: expiresAt}
	a.mu.Lock()
	if a.run != run {
		a.mu.Unlock()
		return bridge.ManagementConsoleInfo{}, errors.New("console exited before becoming ready")
	}
	a.info = info
	a.mu.Unlock()
	go a.expire(run, expiresAt)
	return info, nil
}

type consoleLifecycleReady struct {
	Addr      string    `json:"addr"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func readConsoleLifecycleReady(stdout io.Reader, ready chan<- consoleLifecycleReady) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "AMUX_CONSOLE_READY ") {
			var result consoleLifecycleReady
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "AMUX_CONSOLE_READY ")), &result) != nil {
				result = consoleLifecycleReady{}
			}
			ready <- result
			_, _ = io.Copy(io.Discard, stdout)
			return
		}
	}
	ready <- consoleLifecycleReady{}
}

func (a *localManagementConsoleActions) expire(run *consoleLifecycleRun, expiresAt time.Time) {
	timer := time.NewTimer(expiresAt.Sub(a.now()))
	defer timer.Stop()
	select {
	case <-run.done:
		return
	case <-timer.C:
	case <-a.ctx.Done():
	}
	// Check the identity under the same gate so a late expiry cannot close a
	// replacement instance opened by another Bot.
	_ = a.lock(context.Background())
	defer func() { <-a.gate }()
	a.mu.Lock()
	current := a.run == run
	a.mu.Unlock()
	if current {
		_ = a.stopLocked(context.Background(), run)
	}
}

func (a *localManagementConsoleActions) Close(ctx context.Context) error {
	if err := a.lock(ctx); err != nil {
		return err
	}
	defer func() { <-a.gate }()
	a.mu.Lock()
	run := a.run
	a.mu.Unlock()
	return a.stopLocked(ctx, run)
}

func (a *localManagementConsoleActions) stopLocked(ctx context.Context, run *consoleLifecycleRun) error {
	if run == nil {
		return nil
	}
	a.mu.Lock()
	if a.run == run {
		a.info = bridge.ManagementConsoleInfo{} // Revoke the in-memory credential now.
	}
	a.mu.Unlock()
	select {
	case <-run.done:
		return nil
	default:
	}
	_ = run.process.Signal(os.Interrupt)
	timer := time.NewTimer(a.stopTimeout)
	defer timer.Stop()
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
	case <-timer.C:
	}
	_ = run.process.Kill()
	// Still reap after a cancelled callback, but never wait without a bound.
	timer.Reset(a.stopTimeout)
	select {
	case <-run.done:
		return nil
	case <-timer.C:
		return errors.New("console process did not stop")
	}
}

func consoleListenAddr(port int, origin string) string {
	host := "0.0.0.0"
	if u, err := url.Parse(strings.TrimSpace(origin)); err == nil {
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.To4() == nil {
			host = "::"
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func managementConsoleOrigin(port int, origin string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if port < 1 || port > 65535 || err != nil || u.Host == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		strings.EqualFold(u.Hostname(), "localhost") || (net.ParseIP(u.Hostname()) != nil && !usableHubIP(net.ParseIP(u.Hostname()))) {
		return "", errors.New("console requires a reachable public origin")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
