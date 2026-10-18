package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/rs/zerolog/log"
)

// larkWSStaleAfterEnv overrides the inbound-silence threshold of the Feishu
// websocket watchdog. It accepts a Go duration ("6m"); "0" or "off" disables
// the watchdog (the supervisor still restarts a client that stopped).
const larkWSStaleAfterEnv = "AMUX_FEISHU_WS_STALE_AFTER"

// larkWSWatchdogConfig controls when a Feishu websocket is considered
// half-dead. A connection can stay ESTABLISHED locally while a NAT, proxy, or
// paused VM has silently dropped the flow: Ping writes keep succeeding into
// the kernel send buffer, but no Pong or event ever comes back.
type larkWSWatchdogConfig struct {
	// Disabled turns off stale and clock-jump detection.
	Disabled bool
	// StaleAfter, when positive, is the fixed inbound-silence threshold.
	// Otherwise the threshold is max(MinStaleAfter, PingMultiplier x the
	// observed ping interval). The same threshold bounds missing pongs,
	// missing ping attempts, and a stuck SDK-internal reconnect.
	StaleAfter          time.Duration
	PingMultiplier      float64
	MinStaleAfter       time.Duration
	DefaultPingInterval time.Duration
	// ClockJumpThreshold is how far wall time may run ahead of monotonic time
	// (or how long the process may go without a watchdog check) before the
	// host is assumed to have been suspended and the connection is recycled.
	ClockJumpThreshold time.Duration
	CheckInterval      time.Duration
	// StopTimeout bounds how long a recycled client may take to stop.
	StopTimeout       time.Duration
	RestartBackoffMin time.Duration
	RestartBackoffMax time.Duration
}

func defaultLarkWSWatchdogConfig() larkWSWatchdogConfig {
	return larkWSWatchdogConfig{
		PingMultiplier:      2.5,
		MinStaleAfter:       4 * time.Minute,
		DefaultPingInterval: 2 * time.Minute,
		ClockJumpThreshold:  2 * time.Minute,
		CheckInterval:       15 * time.Second,
		StopTimeout:         30 * time.Second,
		RestartBackoffMin:   5 * time.Second,
		RestartBackoffMax:   2 * time.Minute,
	}
}

func larkWSWatchdogConfigFromEnv(lookup func(string) (string, bool)) larkWSWatchdogConfig {
	cfg := defaultLarkWSWatchdogConfig()
	raw, ok := lookup(larkWSStaleAfterEnv)
	raw = strings.TrimSpace(raw)
	if !ok || raw == "" {
		return cfg
	}
	if strings.EqualFold(raw, "off") || raw == "0" {
		cfg.Disabled = true
		return cfg
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Warn().Str("env", larkWSStaleAfterEnv).Str("value", raw).
			Msg("bridge: invalid Feishu websocket stale threshold, using default")
		return cfg
	}
	if d < 30*time.Second {
		d = 30 * time.Second
	}
	cfg.StaleAfter = d
	return cfg
}

// wsClockReading pairs a monotonic offset with a wall-clock time so the
// watchdog can notice host suspend: monotonic time does not advance while a
// Linux guest is paused, wall time does once it resyncs.
type wsClockReading struct {
	mono time.Duration
	wall time.Time
}

type wsClock interface {
	Read() wsClockReading
}

type realWSClock struct{ origin time.Time }

func newRealWSClock() realWSClock { return realWSClock{origin: time.Now()} }

func (c realWSClock) Read() wsClockReading {
	now := time.Now()
	return wsClockReading{mono: now.Sub(c.origin), wall: now.Round(0)}
}

// larkWSWatchdog tracks inbound activity for one websocket client generation.
type larkWSWatchdog struct {
	cfg   larkWSWatchdogConfig
	clock wsClock

	mu           sync.Mutex
	connected    bool
	everConn     bool
	connectedAt  wsClockReading
	disconnAt    wsClockReading
	lastInbound  wsClockReading
	lastPong     wsClockReading
	havePong     bool
	lastPing     wsClockReading
	havePing     bool
	pingInterval time.Duration
	lastCheck    wsClockReading
	haveCheck    bool
}

func newLarkWSWatchdog(cfg larkWSWatchdogConfig, clock wsClock) *larkWSWatchdog {
	return &larkWSWatchdog{cfg: cfg, clock: clock, pingInterval: cfg.DefaultPingInterval}
}

func (w *larkWSWatchdog) markConnected() {
	now := w.clock.Read()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.connected = true
	w.everConn = true
	w.connectedAt = now
	w.lastInbound = now
	w.havePing = false
	w.havePong = false
}

func (w *larkWSWatchdog) markDisconnected() {
	now := w.clock.Read()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.connected || !w.everConn {
		w.disconnAt = now
	}
	w.connected = false
}

// observeInbound records a data frame received from Feishu.
func (w *larkWSWatchdog) observeInbound() {
	now := w.clock.Read()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastInbound = now
}

// observePong records a Pong; it also counts as inbound activity.
func (w *larkWSWatchdog) observePong() {
	now := w.clock.Read()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastInbound = now
	w.lastPong = now
	w.havePong = true
}

// observePing records a successful Ping write and learns the server-provided
// ping interval from consecutive pings.
func (w *larkWSWatchdog) observePing() {
	now := w.clock.Read()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.havePing {
		if d := now.mono - w.lastPing.mono; d >= 5*time.Second && d <= 10*time.Minute {
			w.pingInterval = d
		}
	}
	w.lastPing = now
	w.havePing = true
}

func (w *larkWSWatchdog) staleAfterLocked() time.Duration {
	if w.cfg.StaleAfter > 0 {
		return w.cfg.StaleAfter
	}
	d := time.Duration(w.cfg.PingMultiplier * float64(w.pingInterval))
	if d < w.cfg.MinStaleAfter {
		d = w.cfg.MinStaleAfter
	}
	return d
}

type larkWSStaleVerdict struct {
	stale      bool
	reason     string
	silence    time.Duration
	staleAfter time.Duration
}

// check reports whether the current connection should be recycled.
func (w *larkWSWatchdog) check() larkWSStaleVerdict {
	now := w.clock.Read()
	w.mu.Lock()
	defer w.mu.Unlock()
	prev, hadPrev := w.lastCheck, w.haveCheck
	w.lastCheck, w.haveCheck = now, true
	if w.cfg.Disabled || !w.everConn {
		return larkWSStaleVerdict{}
	}
	staleAfter := w.staleAfterLocked()
	if !w.connected {
		// The SDK is reconnecting on its own. Give it one stale window; its
		// reconnect loop can hang after a host suspend and then logs nothing.
		if gap := now.mono - w.disconnAt.mono; gap > staleAfter {
			return larkWSStaleVerdict{stale: true, reason: "sdk reconnect did not complete", silence: gap, staleAfter: staleAfter}
		}
		return larkWSStaleVerdict{}
	}
	silence := now.mono - w.lastInbound.mono
	if hadPrev && w.lastInbound.mono <= prev.mono {
		monoGap := now.mono - prev.mono
		wallGap := now.wall.Sub(prev.wall)
		if wallGap-monoGap > w.cfg.ClockJumpThreshold || monoGap > w.cfg.ClockJumpThreshold {
			return larkWSStaleVerdict{
				stale:      true,
				reason:     fmt.Sprintf("host suspend suspected (wall clock advanced %s, monotonic %s since last check)", wallGap.Round(time.Second), monoGap.Round(time.Second)),
				silence:    silence,
				staleAfter: staleAfter,
			}
		}
	}
	if silence > staleAfter {
		return larkWSStaleVerdict{stale: true, reason: "no pong or event received", silence: silence, staleAfter: staleAfter}
	}
	// Events alone do not prove the heartbeat works: require a pong after
	// pings were sent, and require the SDK to keep attempting pings at all.
	pongRef := w.connectedAt
	if w.havePong {
		pongRef = w.lastPong
	}
	if gap := now.mono - pongRef.mono; w.havePing && w.lastPing.mono > pongRef.mono && gap > staleAfter {
		return larkWSStaleVerdict{stale: true, reason: "pings sent but no pong received", silence: gap, staleAfter: staleAfter}
	}
	pingRef := w.connectedAt
	if w.havePing {
		pingRef = w.lastPing
	}
	if gap := now.mono - pingRef.mono; gap > staleAfter {
		return larkWSStaleVerdict{stale: true, reason: "no ping attempted", silence: gap, staleAfter: staleAfter}
	}
	return larkWSStaleVerdict{}
}

// larkWSLogger forwards SDK logs and feeds Ping/Pong/event activity to the
// watchdog. The v3 ws.Client has no frame hooks; its Debug log lines are the
// only per-frame signal. TestLarkWSWatchdogObservesRealSDKFrames pins the
// strings so an SDK upgrade that changes them fails loudly.
type larkWSLogger struct {
	next larkcore.Logger
	wd   *larkWSWatchdog
}

func (l larkWSLogger) Debug(ctx context.Context, args ...interface{}) {
	if len(args) > 0 {
		if msg, ok := args[0].(string); ok {
			switch {
			case strings.HasPrefix(msg, "receive pong"):
				l.wd.observePong()
			case strings.HasPrefix(msg, "receive message"):
				l.wd.observeInbound()
			case strings.HasPrefix(msg, "ping success"):
				l.wd.observePing()
			}
		}
	}
	l.next.Debug(ctx, args...)
}

func (l larkWSLogger) Info(ctx context.Context, args ...interface{})  { l.next.Info(ctx, args...) }
func (l larkWSLogger) Warn(ctx context.Context, args ...interface{})  { l.next.Warn(ctx, args...) }
func (l larkWSLogger) Error(ctx context.Context, args ...interface{}) { l.next.Error(ctx, args...) }

// larkWSClient is the part of *larkws.Client the supervisor drives. Start must
// block until ctx is cancelled or the client stops for good, and must have
// stopped its workers when it returns (SDK >= v3.12 guarantees this).
type larkWSClient interface {
	Start(ctx context.Context) error
}

type larkWSHooks struct {
	Logger         larkcore.Logger
	OnReady        func()
	OnReconnected  func()
	OnReconnecting func()
	OnDisconnected func()
}

type larkWSClientFactory func(hooks larkWSHooks) larkWSClient

// larkWSSupervisor owns one Feishu websocket client at a time. It replaces the
// client when the watchdog finds the connection stale, or when the client
// stops on its own, and reports connects to the bridge exactly once per
// established connection.
type larkWSSupervisor struct {
	cfg       larkWSWatchdogConfig
	clock     wsClock
	newClient larkWSClientFactory
	sdkLogger larkcore.Logger
	newTicker func(time.Duration) (<-chan time.Time, func())
	// after paces restarts of clients that never connected.
	after   func(time.Duration) <-chan time.Time
	profile string

	// onConnected runs on the first established connection, onReconnected on
	// every later one (SDK reconnect or supervisor restart).
	onConnected   func()
	onReconnected func()

	// checked, when set (tests), observes every finished watchdog check.
	checked func(larkWSStaleVerdict)

	mu            sync.Mutex
	everConnected bool
}

func newLarkWSSupervisor(cfg larkWSWatchdogConfig, factory larkWSClientFactory) *larkWSSupervisor {
	return &larkWSSupervisor{
		cfg:       cfg,
		clock:     newRealWSClock(),
		newClient: factory,
		sdkLogger: larkcore.NewDefaultLogger(larkcore.LogLevelDebug),
		newTicker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		after: time.After,
	}
}

func (s *larkWSSupervisor) connected(runCtx context.Context, wd *larkWSWatchdog) {
	if runCtx.Err() != nil {
		return
	}
	wd.markConnected()
	s.mu.Lock()
	first := !s.everConnected
	s.everConnected = true
	s.mu.Unlock()
	if first {
		if s.onConnected != nil {
			s.onConnected()
		}
		return
	}
	if s.onReconnected != nil {
		s.onReconnected()
	}
}

func (s *larkWSSupervisor) hasConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.everConnected
}

// Run blocks until ctx is cancelled. It returns an error only when the very
// first client fails with a non-retryable SDK ClientError (for example bad
// credentials), matching the SDK's own Start behavior.
func (s *larkWSSupervisor) Run(ctx context.Context) error {
	checkEvery := s.cfg.CheckInterval
	if checkEvery <= 0 {
		checkEvery = 15 * time.Second
	}
	tick, stopTick := s.newTicker(checkEvery)
	defer stopTick()

	backoff := s.cfg.RestartBackoffMin
	for {
		if ctx.Err() != nil {
			return nil
		}
		wd := newLarkWSWatchdog(s.cfg, s.clock)
		runCtx, cancel := context.WithCancel(ctx)
		client := s.newClient(larkWSHooks{
			Logger:         larkWSLogger{next: s.sdkLogger, wd: wd},
			OnReady:        func() { s.connected(runCtx, wd) },
			OnReconnected:  func() { s.connected(runCtx, wd) },
			OnReconnecting: wd.markDisconnected,
			OnDisconnected: wd.markDisconnected,
		})
		done := make(chan error, 1)
		go func() { done <- client.Start(runCtx) }()

		restart, err := s.supervise(ctx, cancel, done, tick, wd)
		cancel()
		if !restart {
			return err
		}
		if wd.wasEverConnected() {
			backoff = s.cfg.RestartBackoffMin
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.after(backoff):
		}
		backoff *= 2
		if backoff > s.cfg.RestartBackoffMax {
			backoff = s.cfg.RestartBackoffMax
		}
	}
}

// supervise waits on one client generation. restart=false means Run returns err.
func (s *larkWSSupervisor) supervise(ctx context.Context, cancel context.CancelFunc, done <-chan error, tick <-chan time.Time, wd *larkWSWatchdog) (restart bool, err error) {
	for {
		select {
		case <-ctx.Done():
			cancel()
			s.awaitStop(done)
			return false, nil
		case err := <-done:
			if ctx.Err() != nil {
				return false, nil
			}
			var clientErr *larkws.ClientError
			if errors.As(err, &clientErr) && !s.hasConnected() {
				return false, err
			}
			log.Warn().Err(err).Str("profile", s.profile).
				Msg("bridge: lark websocket client stopped; restarting")
			return true, nil
		case <-tick:
			v := wd.check()
			if !v.stale {
				s.notifyChecked(v)
				continue
			}
			log.Warn().Str("profile", s.profile).Str("reason", v.reason).
				Dur("silence", v.silence).Dur("stale_after", v.staleAfter).
				Msgf("bridge: lark websocket stale (%s) for %s; reconnecting", v.reason, v.silence.Round(time.Second))
			cancel()
			s.awaitStop(done)
			s.notifyChecked(v)
			return true, nil
		}
	}
}

func (s *larkWSSupervisor) notifyChecked(v larkWSStaleVerdict) {
	if s.checked != nil {
		s.checked(v)
	}
}

func (s *larkWSSupervisor) awaitStop(done <-chan error) {
	timeout := s.cfg.StopTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// Start ignores cancellation only if an event handler is stuck; the old
		// client can no longer read or dispatch new frames, so move on.
		log.Warn().Str("profile", s.profile).Dur("timeout", timeout).
			Msg("bridge: previous lark websocket client did not stop in time")
	}
}

func (w *larkWSWatchdog) wasEverConnected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.everConn
}

func larkWSEnvLookup(key string) (string, bool) { return os.LookupEnv(key) }
