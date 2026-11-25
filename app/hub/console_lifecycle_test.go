package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/spf13/viper"
)

type fakeConsoleLifecycleProcess struct {
	mu       sync.Mutex
	done     chan struct{}
	once     sync.Once
	signals  int
	kills    int
	stubborn bool
}

func (p *fakeConsoleLifecycleProcess) finish()     { p.once.Do(func() { close(p.done) }) }
func (p *fakeConsoleLifecycleProcess) Wait() error { <-p.done; return nil }
func (p *fakeConsoleLifecycleProcess) Signal(os.Signal) error {
	p.mu.Lock()
	p.signals++
	stubborn := p.stubborn
	p.mu.Unlock()
	if !stubborn {
		p.finish()
	}
	return nil
}
func (p *fakeConsoleLifecycleProcess) Kill() error {
	p.mu.Lock()
	p.kills++
	p.mu.Unlock()
	p.finish()
	return nil
}

func newFakeConsoleLifecycle(t *testing.T) (*localManagementConsoleActions, *fakeConsoleLifecycleProcess) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a := newLocalManagementConsoleActions(ctx, "192.0.2.10")
	p := &fakeConsoleLifecycleProcess{done: make(chan struct{})}
	a.options = func() (int, string) { return 7892, "http://192.0.2.10:7892" }
	a.executable = func() (string, error) { return "/installed/amux", nil }
	a.stopTimeout = 20 * time.Millisecond
	a.startupTimeout = 100 * time.Millisecond
	a.start = func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error) {
		data, _ := json.Marshal(consoleLifecycleReady{Addr: "0.0.0.0:7892", URL: "http://192.0.2.10:7892", ExpiresAt: time.Now().Add(managementConsoleDuration)})
		return p, io.NopCloser(strings.NewReader("ignored child output\nAMUX_CONSOLE_READY " + string(data) + "\n")), nil
	}
	t.Cleanup(func() { cancel(); _ = a.Close(context.Background()); p.finish() })
	return a, p
}

func waitConsoleLifecycle(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("console lifecycle did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagementConsoleLifecycleLazyIdempotentAndPrivatePassword(t *testing.T) {
	a, _ := newFakeConsoleLifecycle(t)
	originalStart := a.start
	var mu sync.Mutex
	starts := 0
	a.start = func(ctx context.Context, executable string, args []string, password string) (consoleLifecycleProcess, io.ReadCloser, error) {
		mu.Lock()
		starts++
		mu.Unlock()
		if executable != "/installed/amux" || len(password) < 16 || strings.ContainsAny(password, " \n\r") {
			t.Errorf("invalid executable or generated password")
		}
		want := []string{"console", "--addr", "0.0.0.0:7892", "--duration", "30m", "--public-url", "http://192.0.2.10:7892", "--password-stdin", "--managed-by-hub"}
		if !reflect.DeepEqual(args, want) || strings.Contains(strings.Join(args, " "), password) {
			t.Errorf("console command does not match the credential-free contract")
		}
		return originalStart(ctx, executable, args, password)
	}
	if got := a.Snapshot(); got != (bridge.ManagementConsoleInfo{}) || starts != 0 {
		t.Fatal("manager construction or snapshot started the console")
	}
	first, err := a.Open(context.Background())
	if err != nil || !first.Enabled || first.Password == "" {
		t.Fatalf("open = enabled %v, error %v", first.Enabled, err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := a.Open(context.Background())
			if err != nil || got != first {
				t.Errorf("repeated open changed the password or expiry: %v", err)
			}
		}()
	}
	wg.Wait()
	if starts != 1 || first.ExpiresAt.After(time.Now().Add(managementConsoleDuration)) {
		t.Fatalf("starts = %d or expiry exceeds 30m", starts)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot() != (bridge.ManagementConsoleInfo{}) {
		t.Fatal("close retained console state/password")
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestManagementConsoleLifecycleNotEnabledUntilReady(t *testing.T) {
	a, p := newFakeConsoleLifecycle(t)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close(); _ = reader.Close() })
	started := make(chan struct{})
	a.start = func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error) {
		close(started)
		return p, reader, nil
	}
	result := make(chan error, 1)
	go func() { _, err := a.Open(context.Background()); result <- err }()
	<-started
	if a.Snapshot().Enabled {
		t.Fatal("console enabled before readiness")
	}
	data, _ := json.Marshal(consoleLifecycleReady{Addr: "0.0.0.0:7892", URL: "http://192.0.2.10:7892", ExpiresAt: time.Now().Add(time.Minute)})
	_, _ = io.WriteString(writer, "AMUX_CONSOLE_READY "+string(data)+"\n")
	if err := <-result; err != nil || !a.Snapshot().Enabled {
		t.Fatalf("ready did not enable console: %v", err)
	}
}

func TestManagementConsoleLifecycleExpiryExitAndHubCancellation(t *testing.T) {
	for _, mode := range []string{"expiry", "exit", "hub_cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, p := newFakeConsoleLifecycle(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.ctx = ctx
			if mode == "expiry" {
				a.start = func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error) {
					data, _ := json.Marshal(consoleLifecycleReady{Addr: "0.0.0.0:7892", URL: "http://192.0.2.10:7892", ExpiresAt: time.Now().Add(40 * time.Millisecond)})
					return p, io.NopCloser(strings.NewReader("AMUX_CONSOLE_READY " + string(data) + "\n")), nil
				}
			}
			if _, err := a.Open(context.Background()); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "exit":
				p.finish()
			case "hub_cancel":
				cancel()
			}
			waitConsoleLifecycle(t, func() bool {
				a.mu.Lock()
				defer a.mu.Unlock()
				return a.run == nil && a.info == (bridge.ManagementConsoleInfo{})
			})
			select {
			case <-p.done:
			default:
				t.Fatal("listener process is still running")
			}
		})
	}
}

func TestManagementConsoleLifecycleBoundedCloseKillsOnlyOwnedChild(t *testing.T) {
	a, p := newFakeConsoleLifecycle(t)
	p.stubborn = true
	if _, err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.signals != 1 || p.kills != 1 || time.Since(start) > time.Second || a.Snapshot().Password != "" {
		t.Fatalf("signals %d kills %d; close did not converge", p.signals, p.kills)
	}
}

func TestManagementConsoleLifecycleStartupFailuresAreSanitizedAndReaped(t *testing.T) {
	for _, mode := range []string{"spawn", "malformed", "wrong_origin", "expired", "no_ready", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, p := newFakeConsoleLifecycle(t)
			a.start = func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error) {
				if mode == "spawn" {
					return nil, nil, errors.New("secret-from-diagnostic")
				}
				data := "secret-from-diagnostic\n"
				switch mode {
				case "malformed":
					data += "AMUX_CONSOLE_READY invalid-secret-from-diagnostic\n"
				case "wrong_origin", "expired":
					ready := consoleLifecycleReady{Addr: "0.0.0.0:7892", URL: "http://192.0.2.10:7892", ExpiresAt: time.Now().Add(time.Minute)}
					if mode == "wrong_origin" {
						ready.URL += "?password=secret-from-diagnostic"
					} else {
						ready.ExpiresAt = time.Now().Add(-time.Minute)
					}
					encoded, _ := json.Marshal(ready)
					data += "AMUX_CONSOLE_READY " + string(encoded) + "\n"
				}
				return p, io.NopCloser(strings.NewReader(data)), nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			info, err := a.Open(ctx)
			if err == nil || strings.Contains(err.Error(), "secret-from-diagnostic") || info != (bridge.ManagementConsoleInfo{}) || a.Snapshot().Enabled {
				t.Fatal("startup failure was not sanitized or left an enabled console")
			}
			if mode != "spawn" && mode != "cancel" {
				select {
				case <-p.done:
				default:
					t.Fatal("failed startup leaked process")
				}
			}
		})
	}
}

func TestManagementConsoleOriginAndAutomaticAddress(t *testing.T) {
	for _, origin := range []string{"", "http://0.0.0.0:7892", "http://127.0.0.1:7892", "http://localhost:7892", "https://host.test/?password=secret", "https://user:secret@host.test", "https://host.test/path", "ftp://host.test", "https://[::]:7892"} {
		if _, err := managementConsoleOrigin(7892, origin); err == nil {
			t.Errorf("accepted unsafe/unreachable origin %q", origin)
		}
	}
	for _, origin := range []string{"http://192.0.2.10:7892", "https://console.example.com", "http://[2001:db8::10]:7892"} {
		if got, err := managementConsoleOrigin(7892, origin); err != nil || got != origin {
			t.Errorf("valid origin %q = %q %v", origin, got, err)
		}
	}
	if _, err := managementConsoleOrigin(0, "https://console.example.com"); err == nil {
		t.Fatal("accepted random port")
	}
}

func TestManagementConsoleLifecycleStartupTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"timeout", "callback_cancel", "hub_cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, p := newFakeConsoleLifecycle(t)
			a.startupTimeout = 30 * time.Millisecond
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			started := make(chan struct{})
			a.start = func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error) {
				close(started)
				return p, reader, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hubCtx, hubCancel := context.WithCancel(context.Background())
			defer hubCancel()
			a.ctx = hubCtx
			result := make(chan error, 1)
			go func() { _, err := a.Open(ctx); result <- err }()
			<-started
			switch mode {
			case "callback_cancel":
				cancel()
			case "hub_cancel":
				hubCancel()
			}
			select {
			case err := <-result:
				if err == nil || a.Snapshot().Enabled {
					t.Fatal("failed startup retained enabled console")
				}
			case <-time.After(time.Second):
				t.Fatal("startup did not converge")
			}
			select {
			case <-p.done:
			default:
				t.Fatal("startup leaked a child")
			}
		})
	}
}

func TestManagementConsoleLifecycleFailedPreflightDoesNotSpawn(t *testing.T) {
	for _, mode := range []string{"executable", "random", "missing_ip"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := newFakeConsoleLifecycle(t)
			a.start = func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error) {
				t.Fatal("preflight failure spawned a process")
				return nil, nil, errors.New("unexpected")
			}
			switch mode {
			case "executable":
				a.executable = func() (string, error) { return "", errors.New("secret diagnostic") }
			case "random":
				a.random = strings.NewReader("")
			case "missing_ip":
				a.options = func() (int, string) { return 7892, "" }
			}
			if _, err := a.Open(context.Background()); err == nil || strings.Contains(err.Error(), "secret diagnostic") {
				t.Fatal("preflight failed to reject/sanitize error")
			}
		})
	}
}

func TestManagementConsoleLifecycleReopenRotatesPassword(t *testing.T) {
	a, _ := newFakeConsoleLifecycle(t)
	a.start = func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error) {
		p := &fakeConsoleLifecycleProcess{done: make(chan struct{})}
		t.Cleanup(p.finish)
		data, _ := json.Marshal(consoleLifecycleReady{Addr: "0.0.0.0:7892", URL: "http://192.0.2.10:7892", ExpiresAt: time.Now().Add(managementConsoleDuration)})
		return p, io.NopCloser(strings.NewReader("AMUX_CONSOLE_READY " + string(data) + "\n")), nil
	}
	first, err := a.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := a.Open(context.Background())
	if err != nil || second.Password == first.Password {
		t.Fatal("reopen reused a revoked password")
	}
}

func TestManagementConsoleLifecycleConfiguredAndAutomaticOrigins(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("console.port", 7892)
	for _, test := range []struct{ ip, configured, want string }{
		{"192.0.2.10", "", "http://192.0.2.10:7892"},
		{"", "", ""},
		{"192.0.2.10", "https://console.example.com", "https://console.example.com"},
	} {
		viper.Set("console.public_url", test.configured)
		a := newLocalManagementConsoleActions(context.Background(), test.ip)
		port, origin := a.options()
		if port != 7892 || origin != test.want {
			t.Fatalf("options = %d %q", port, origin)
		}
	}
}

func TestManagementConsoleLifecycleReloadsConfigAndSupportsDynamicOriginAndIPv6(t *testing.T) {
	t.Run("reloads config file without restarting hub", func(t *testing.T) {
		cfgPath := t.TempDir() + "/config.toml"
		if err := os.WriteFile(cfgPath, []byte("[console]\nport = 7892\n"), 0600); err != nil {
			t.Fatal(err)
		}
		a := newLocalManagementConsoleActionsWithConfig(context.Background(), "192.0.2.10", cfgPath)
		a.hasDynamicOrigin = func() bool { return false }
		port, origin := a.options()
		if port != 7892 || origin != "http://192.0.2.10:7892" {
			t.Fatalf("initial options = %d %q", port, origin)
		}
		if err := os.WriteFile(cfgPath, []byte("[console]\nport = 8899\npublic_url = 'https://console.example.com'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		port, origin = a.options()
		if port != 8899 || origin != "https://console.example.com" {
			t.Fatalf("reloaded options = %d %q", port, origin)
		}
	})

	t.Run("dynamic origin resolver omits public-url flag and validates ready origin", func(t *testing.T) {
		a, p := newFakeConsoleLifecycle(t)
		a.hasDynamicOrigin = func() bool { return true }
		a.options = func() (int, string) { return 7892, "" }
		var seenArgs []string
		a.start = func(_ context.Context, _ string, args []string, _ string) (consoleLifecycleProcess, io.ReadCloser, error) {
			seenArgs = append([]string(nil), args...)
			data, _ := json.Marshal(consoleLifecycleReady{Addr: "0.0.0.0:7892", URL: "https://dynamic.example.com", ExpiresAt: time.Now().Add(managementConsoleDuration)})
			return p, io.NopCloser(strings.NewReader("AMUX_CONSOLE_READY " + string(data) + "\n")), nil
		}
		info, err := a.Open(context.Background())
		if err != nil {
			t.Fatalf("dynamic open: %v", err)
		}
		wantArgs := []string{"console", "--addr", "0.0.0.0:7892", "--duration", "30m", "--password-stdin", "--managed-by-hub"}
		if !reflect.DeepEqual(seenArgs, wantArgs) {
			t.Fatalf("args = %v, want %v", seenArgs, wantArgs)
		}
		if info.URL != "https://dynamic.example.com" || !info.Enabled {
			t.Fatalf("info = %+v", info)
		}
	})

	t.Run("dynamic origin rejects unsafe loopback ready url", func(t *testing.T) {
		a, p := newFakeConsoleLifecycle(t)
		a.hasDynamicOrigin = func() bool { return true }
		a.options = func() (int, string) { return 7892, "" }
		a.start = func(context.Context, string, []string, string) (consoleLifecycleProcess, io.ReadCloser, error) {
			data, _ := json.Marshal(consoleLifecycleReady{Addr: "0.0.0.0:7892", URL: "http://127.0.0.1:7892", ExpiresAt: time.Now().Add(managementConsoleDuration)})
			return p, io.NopCloser(strings.NewReader("AMUX_CONSOLE_READY " + string(data) + "\n")), nil
		}
		if _, err := a.Open(context.Background()); err == nil {
			t.Fatal("accepted loopback url in dynamic mode")
		}
	})

	t.Run("ipv6 literal origin listens on wildcard ipv6", func(t *testing.T) {
		a, p := newFakeConsoleLifecycle(t)
		a.options = func() (int, string) { return 7892, "http://[2001:db8::10]:7892" }
		var seenArgs []string
		a.start = func(_ context.Context, _ string, args []string, _ string) (consoleLifecycleProcess, io.ReadCloser, error) {
			seenArgs = append([]string(nil), args...)
			data, _ := json.Marshal(consoleLifecycleReady{Addr: "[::]:7892", URL: "http://[2001:db8::10]:7892", ExpiresAt: time.Now().Add(managementConsoleDuration)})
			return p, io.NopCloser(strings.NewReader("AMUX_CONSOLE_READY " + string(data) + "\n")), nil
		}
		info, err := a.Open(context.Background())
		if err != nil {
			t.Fatalf("ipv6 open: %v", err)
		}
		wantArgs := []string{"console", "--addr", "[::]:7892", "--duration", "30m", "--public-url", "http://[2001:db8::10]:7892", "--password-stdin", "--managed-by-hub"}
		if !reflect.DeepEqual(seenArgs, wantArgs) {
			t.Fatalf("args = %v, want %v", seenArgs, wantArgs)
		}
		if info.URL != "http://[2001:db8::10]:7892" {
			t.Fatalf("info = %+v", info)
		}
	})
}
