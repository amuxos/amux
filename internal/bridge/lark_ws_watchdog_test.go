package bridge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type fakeWSClock struct {
	mu   sync.Mutex
	mono time.Duration
	wall time.Time
}

func newFakeWSClock() *fakeWSClock {
	return &fakeWSClock{wall: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeWSClock) Read() wsClockReading {
	c.mu.Lock()
	defer c.mu.Unlock()
	return wsClockReading{mono: c.mono, wall: c.wall}
}

// Advance moves both clocks, as during normal running.
func (c *fakeWSClock) Advance(d time.Duration) { c.Jump(d, d) }

// Jump moves monotonic and wall time independently, as after a VM pause.
func (c *fakeWSClock) Jump(mono, wall time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mono += mono
	c.wall = c.wall.Add(wall)
}

type nopLarkLogger struct{}

func (nopLarkLogger) Debug(context.Context, ...interface{}) {}
func (nopLarkLogger) Info(context.Context, ...interface{})  {}
func (nopLarkLogger) Warn(context.Context, ...interface{})  {}
func (nopLarkLogger) Error(context.Context, ...interface{}) {}

type fakeWSClient struct {
	hooks   larkWSHooks
	h       *fakeWSHarness
	started chan struct{}
	stopped chan struct{}
	result  chan error
}

func (c *fakeWSClient) Start(ctx context.Context) error {
	if n := c.h.running.Add(1); n > 1 {
		c.h.overlap.Store(true)
	}
	defer c.h.running.Add(-1)
	defer close(c.stopped)
	close(c.started)
	select {
	case <-ctx.Done():
		return nil
	case err := <-c.result:
		return err
	}
}

func (c *fakeWSClient) ready() { c.hooks.OnReady() }
func (c *fakeWSClient) pong() {
	c.hooks.Logger.Debug(context.Background(), "receive pong", "[conn_id=1]")
}
func (c *fakeWSClient) ping() {
	c.hooks.Logger.Debug(context.Background(), "ping success", "[conn_id=1]")
}
func (c *fakeWSClient) event() {
	c.hooks.Logger.Debug(context.Background(), "receive message, message_type: event")
}
func (c *fakeWSClient) sdkReconnected() { c.hooks.OnReconnected() }

type fakeWSHarness struct {
	t         *testing.T
	clock     *fakeWSClock
	tick      chan time.Time
	checked   chan larkWSStaleVerdict
	clients   chan *fakeWSClient
	running   atomic.Int32
	overlap   atomic.Bool
	connects  atomic.Int32
	reconnect atomic.Int32
	cancel    context.CancelFunc
	runErr    chan error
}

func startFakeWSSupervisor(t *testing.T, cfg larkWSWatchdogConfig) *fakeWSHarness {
	t.Helper()
	h := &fakeWSHarness{
		t:       t,
		clock:   newFakeWSClock(),
		tick:    make(chan time.Time),
		checked: make(chan larkWSStaleVerdict, 1),
		clients: make(chan *fakeWSClient, 16),
		runErr:  make(chan error, 1),
	}
	s := newLarkWSSupervisor(cfg, func(hooks larkWSHooks) larkWSClient {
		c := &fakeWSClient{hooks: hooks, h: h, started: make(chan struct{}), stopped: make(chan struct{}), result: make(chan error, 1)}
		h.clients <- c
		return c
	})
	s.clock = h.clock
	s.sdkLogger = nopLarkLogger{}
	s.newTicker = func(time.Duration) (<-chan time.Time, func()) { return h.tick, func() {} }
	s.after = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	s.checked = func(v larkWSStaleVerdict) { h.checked <- v }
	s.onConnected = func() { h.connects.Add(1) }
	s.onReconnected = func() { h.reconnect.Add(1) }
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.runErr <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.runErr:
		case <-time.After(5 * time.Second):
			t.Error("supervisor did not stop after cancel")
		}
	})
	return h
}

func (h *fakeWSHarness) nextClient() *fakeWSClient {
	h.t.Helper()
	select {
	case c := <-h.clients:
		<-c.started
		return c
	case <-time.After(5 * time.Second):
		h.t.Fatal("no websocket client was started")
		return nil
	}
}

func (h *fakeWSHarness) noNewClient() {
	h.t.Helper()
	h.sendTick()
	select {
	case <-h.clients:
		h.t.Fatal("unexpected websocket client restart")
	default:
	}
}

func (h *fakeWSHarness) sendTick() {
	h.t.Helper()
	select {
	case h.tick <- time.Time{}:
	case <-time.After(5 * time.Second):
		h.t.Fatal("supervisor is not consuming watchdog ticks")
	}
	select {
	case <-h.checked:
	case <-time.After(5 * time.Second):
		h.t.Fatal("watchdog check did not finish")
	}
}

func testWSConfig() larkWSWatchdogConfig {
	cfg := defaultLarkWSWatchdogConfig()
	cfg.StopTimeout = 5 * time.Second
	return cfg
}

func TestLarkWSSupervisorRestartsStaleConnectionOnce(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	first := h.nextClient()
	first.ready()
	h.sendTick()
	for i := 0; i < 2; i++ { // 3m: pings keep "succeeding" but nothing comes back
		h.clock.Advance(90 * time.Second)
		first.ping()
		h.sendTick()
	}
	select {
	case <-h.clients:
		t.Fatal("restarted before the stale threshold")
	default:
	}
	h.clock.Advance(90 * time.Second) // 4.5m silent > 4m floor
	first.ping()
	h.sendTick()
	second := h.nextClient()
	select {
	case <-first.stopped:
	default:
		t.Fatal("stale client was not stopped before its replacement started")
	}
	second.ready()
	h.noNewClient()
	h.noNewClient()
	if h.overlap.Load() {
		t.Fatal("two websocket clients ran at the same time")
	}
	if got, want := h.connects.Load(), int32(1); got != want {
		t.Fatalf("onConnected calls = %d, want %d", got, want)
	}
	if got, want := h.reconnect.Load(), int32(1); got != want {
		t.Fatalf("onReconnected calls = %d, want %d", got, want)
	}
}

func TestLarkWSSupervisorPongsAndEventsKeepConnection(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	for i := 0; i < 40; i++ { // one hour
		h.clock.Advance(90 * time.Second)
		c.ping()
		if i%2 == 0 {
			c.pong()
		} else {
			c.event()
		}
		h.sendTick()
	}
	h.noNewClient()
	if h.reconnect.Load() != 0 {
		t.Fatal("unexpected reconnect callback")
	}
}

func TestLarkWSSupervisorWaitsForShortSDKReconnect(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	c.hooks.OnDisconnected()
	c.hooks.OnReconnecting()
	h.clock.Advance(3 * time.Minute)
	h.noNewClient()
	c.sdkReconnected()
	h.clock.Advance(time.Minute)
	h.noNewClient()
	if got := h.reconnect.Load(); got != 1 {
		t.Fatalf("onReconnected calls = %d, want 1", got)
	}
}

func TestLarkWSSupervisorRecyclesStuckSDKReconnect(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	c.hooks.OnDisconnected()
	h.clock.Advance(2 * time.Minute)
	c.hooks.OnReconnecting() // repeated callbacks keep the first disconnect time
	h.clock.Advance(4 * time.Minute) // 6m disconnected > 5m threshold
	h.sendTick()
	next := h.nextClient()
	<-c.stopped
	next.ready()
	h.noNewClient()
}

func TestLarkWSSupervisorRecyclesWhenPongsStopDespiteEvents(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	c.ping()
	c.pong()
	for i := 0; i < 2; i++ {
		h.clock.Advance(2 * time.Minute)
		c.ping()
		c.event()
		h.sendTick()
	}
	h.clock.Advance(90 * time.Second) // 5.5m since last pong
	c.event()
	h.sendTick()
	h.nextClient()
}

func TestLarkWSSupervisorRecyclesWhenPingsStop(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	c.ping()
	c.pong()
	for i := 0; i < 6; i++ { // events flow, but the SDK pinger went quiet
		h.clock.Advance(time.Minute)
		c.event()
		h.sendTick()
		if i < 4 {
			select {
			case <-h.clients:
				t.Fatal("restarted before the stale threshold")
			default:
			}
		}
	}
	h.nextClient()
}

func TestLarkWSSupervisorRecyclesAfterHostSuspend(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	h.sendTick()
	h.clock.Advance(15 * time.Second)
	c.pong()
	h.sendTick()
	// VM paused for 40 minutes: monotonic barely moved, wall clock jumped.
	h.clock.Jump(15*time.Second, 40*time.Minute)
	h.sendTick()
	next := h.nextClient()
	<-c.stopped
	next.ready()
	h.noNewClient()
}

func TestLarkWSSupervisorKeepsConnectionProvenAliveAfterSuspend(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	h.sendTick()
	h.clock.Jump(15*time.Second, 40*time.Minute)
	c.pong() // Feishu answered after resume: the flow survived
	h.sendTick()
	h.noNewClient()
}

func TestLarkWSSupervisorRecyclesWhenProcessWasFrozen(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	h.sendTick()
	h.clock.Advance(3 * time.Minute) // no check for 3m; nothing received
	h.sendTick()
	h.nextClient()
}

func TestLarkWSSupervisorRestartsClientThatStopped(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	c.result <- errors.New("unable to connect to server after 3 retries")
	next := h.nextClient()
	next.ready()
	if got := h.reconnect.Load(); got != 1 {
		t.Fatalf("onReconnected calls = %d, want 1", got)
	}
}

func TestLarkWSSupervisorReturnsInitialClientError(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	want := larkws.NewClientError(19001, "invalid credential")
	c.result <- want
	select {
	case err := <-h.runErr:
		h.runErr <- err // let cleanup observe exit
		var ce *larkws.ClientError
		if !errors.As(err, &ce) {
			t.Fatalf("Run error = %v, want ClientError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return the initial client error")
	}
}

func TestLarkWSSupervisorBacksOffWhenNeverConnected(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	for i := 0; i < 3; i++ {
		c := h.nextClient()
		c.result <- errors.New("dial tcp: i/o timeout")
	}
	h.nextClient().ready()
	if got := h.connects.Load(); got != 1 {
		t.Fatalf("onConnected calls = %d, want 1", got)
	}
}

func TestLarkWSSupervisorCancelStopsClient(t *testing.T) {
	h := startFakeWSSupervisor(t, testWSConfig())
	c := h.nextClient()
	c.ready()
	h.cancel()
	select {
	case err := <-h.runErr:
		h.runErr <- err
		if err != nil {
			t.Fatalf("Run error after cancel = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	<-c.stopped
	if h.running.Load() != 0 {
		t.Fatal("client still running after supervisor exit")
	}
}

func TestLarkWSWatchdogStaleThreshold(t *testing.T) {
	clock := newFakeWSClock()
	wd := newLarkWSWatchdog(defaultLarkWSWatchdogConfig(), clock)
	wd.markConnected()
	if got := wd.staleAfterLocked(); got != 5*time.Minute {
		t.Fatalf("default stale threshold = %s, want 5m (2.5 x 2m default ping)", got)
	}
	wd.observePing()
	clock.Advance(90 * time.Second)
	wd.observePing()
	if got := wd.staleAfterLocked(); got != 4*time.Minute {
		t.Fatalf("stale threshold with 90s pings = %s, want 4m floor", got)
	}
	clock.Advance(4 * time.Minute)
	wd.observePing()
	if got := wd.staleAfterLocked(); got != 10*time.Minute {
		t.Fatalf("stale threshold with 4m pings = %s, want 10m", got)
	}
}

func TestLarkWSWatchdogConfigFromEnv(t *testing.T) {
	env := func(v string) func(string) (string, bool) {
		return func(string) (string, bool) { return v, true }
	}
	if cfg := larkWSWatchdogConfigFromEnv(func(string) (string, bool) { return "", false }); cfg.Disabled || cfg.StaleAfter != 0 {
		t.Fatalf("unset env config = %+v", cfg)
	}
	if cfg := larkWSWatchdogConfigFromEnv(env("8m")); cfg.StaleAfter != 8*time.Minute {
		t.Fatalf("StaleAfter = %s, want 8m", cfg.StaleAfter)
	}
	if cfg := larkWSWatchdogConfigFromEnv(env("5s")); cfg.StaleAfter != 30*time.Second {
		t.Fatalf("StaleAfter = %s, want 30s floor", cfg.StaleAfter)
	}
	for _, v := range []string{"off", "0"} {
		if cfg := larkWSWatchdogConfigFromEnv(env(v)); !cfg.Disabled {
			t.Fatalf("%q did not disable the watchdog", v)
		}
	}
	if cfg := larkWSWatchdogConfigFromEnv(env("soon")); cfg.Disabled || cfg.StaleAfter != 0 {
		t.Fatalf("invalid value changed config: %+v", cfg)
	}
}

func TestLarkWSWatchdogDisabled(t *testing.T) {
	cfg := defaultLarkWSWatchdogConfig()
	cfg.Disabled = true
	clock := newFakeWSClock()
	wd := newLarkWSWatchdog(cfg, clock)
	wd.markConnected()
	wd.check()
	clock.Jump(time.Hour, 2*time.Hour)
	if v := wd.check(); v.stale {
		t.Fatalf("disabled watchdog reported stale: %+v", v)
	}
}

var _ larkcore.Logger = larkWSLogger{}
