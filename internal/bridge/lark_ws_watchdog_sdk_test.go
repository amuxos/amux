package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// fakeLarkGateway imitates the Feishu websocket gateway. The first connection
// answers a few pings and then goes half-dead: it keeps the TCP flow open and
// keeps reading pings but never answers again, like the 2026-10-10 incident.
type fakeLarkGateway struct {
	t           *testing.T
	server      *httptest.Server
	healthyPong int

	mu        sync.Mutex
	connects  []time.Time
	closedOld chan struct{}
	closeOnce sync.Once
}

func newFakeLarkGateway(t *testing.T, healthyPongs int) *fakeLarkGateway {
	g := &fakeLarkGateway{t: t, healthyPong: healthyPongs, closedOld: make(chan struct{})}
	conf := &larkws.ClientConfig{ReconnectCount: -1, ReconnectInterval: 1, PingInterval: 1}
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc(larkws.GenEndpointUri, func(w http.ResponseWriter, r *http.Request) {
		wsURL := "ws" + strings.TrimPrefix(g.server.URL, "http") + "/ws?device_id=test-device&service_id=1"
		_ = json.NewEncoder(w).Encode(larkws.EndpointResp{Code: larkws.OK, Data: &larkws.Endpoint{Url: wsURL, ClientConfig: conf}})
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		g.mu.Lock()
		g.connects = append(g.connects, time.Now())
		index := len(g.connects)
		g.mu.Unlock()
		answered := 0
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				if index == 1 {
					g.closeOnce.Do(func() { close(g.closedOld) })
				}
				return
			}
			if index == 1 && answered >= g.healthyPong {
				continue // half-dead: swallow pings
			}
			answered++
			payload, _ := json.Marshal(conf)
			pong := larkws.Frame{Service: 1, Method: int32(larkws.FrameTypeControl), Headers: []larkws.Header{{Key: larkws.HeaderType, Value: string(larkws.MessageTypePong)}}, Payload: payload}
			bs, _ := pong.Marshal()
			if err := conn.WriteMessage(websocket.BinaryMessage, bs); err != nil {
				return
			}
		}
	})
	g.server = httptest.NewServer(mux)
	return g
}

func (g *fakeLarkGateway) connections() []time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]time.Time(nil), g.connects...)
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLarkWSWatchdogObservesRealSDKFrames drives the real SDK client so an SDK
// upgrade that renames its Ping/Pong debug lines, or stops honoring
// cancellation, breaks this test instead of silently disabling the watchdog.
func TestLarkWSWatchdogObservesRealSDKFrames(t *testing.T) {
	baseline := runtime.NumGoroutine()
	gateway := newFakeLarkGateway(t, 3)

	cfg := defaultLarkWSWatchdogConfig()
	cfg.StaleAfter = 1500 * time.Millisecond // below the SDK's own 7s read deadline
	cfg.CheckInterval = 50 * time.Millisecond
	cfg.ClockJumpThreshold = time.Hour
	cfg.StopTimeout = 5 * time.Second

	var connected, reconnected atomic.Int32
	s := newLarkWSSupervisor(cfg, func(hooks larkWSHooks) larkWSClient {
		return larkws.NewClient("cli_test", "secret",
			larkws.WithDomain(gateway.server.URL),
			larkws.WithAutoReconnect(true),
			larkws.WithLogLevel(larkcore.LogLevelDebug),
			larkws.WithLogger(hooks.Logger),
			larkws.WithOnReady(hooks.OnReady),
			larkws.WithOnReconnected(hooks.OnReconnected),
			larkws.WithOnReconnecting(hooks.OnReconnecting),
			larkws.WithOnDisconnected(hooks.OnDisconnected),
		)
	})
	s.sdkLogger = nopLarkLogger{}
	s.onConnected = func() { connected.Add(1) }
	s.onReconnected = func() { reconnected.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(ctx) }()

	waitFor(t, 15*time.Second, "watchdog reconnect", func() bool { return len(gateway.connections()) >= 2 })
	select {
	case <-gateway.closedOld:
	case <-time.After(5 * time.Second):
		t.Fatal("half-dead connection was not closed by the client")
	}
	conns := gateway.connections()
	if gap := conns[1].Sub(conns[0]); gap < 2500*time.Millisecond {
		t.Fatalf("reconnected after %s although pongs were flowing for ~3s; pong hook not observed", gap)
	}
	waitFor(t, 5*time.Second, "reconnect callback", func() bool { return reconnected.Load() == 1 })

	// The healthy connection keeps answering pings, so it must survive well
	// past the stale threshold.
	time.Sleep(3 * time.Second)
	if n := len(gateway.connections()); n != 2 {
		t.Fatalf("connections = %d, want 2 (healthy connection was recycled)", n)
	}
	if connected.Load() != 1 || reconnected.Load() != 1 {
		t.Fatalf("callbacks connected=%d reconnected=%d, want 1/1", connected.Load(), reconnected.Load())
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned %v after cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	gateway.server.Close()
	waitFor(t, 5*time.Second, "websocket goroutines to exit", func() bool {
		return runtime.NumGoroutine() <= baseline+2
	})
}

// TestLarkSDKReadDeadlineRecoversHalfDeadConnection pins the SDK (>= v3.12)
// behavior the watchdog backs up: with the watchdog off, the SDK's own read
// deadline (2 x ping interval + 5s) must still recycle a silent connection.
func TestLarkSDKReadDeadlineRecoversHalfDeadConnection(t *testing.T) {
	t.Parallel()
	gateway := newFakeLarkGateway(t, 1)
	defer gateway.server.Close()
	cfg := defaultLarkWSWatchdogConfig()
	cfg.Disabled = true
	var reconnected atomic.Int32
	s := newLarkWSSupervisor(cfg, func(hooks larkWSHooks) larkWSClient {
		return larkws.NewClient("cli_test", "secret",
			larkws.WithDomain(gateway.server.URL),
			larkws.WithLogger(hooks.Logger),
			larkws.WithOnReady(hooks.OnReady),
			larkws.WithOnReconnected(hooks.OnReconnected),
		)
	})
	s.sdkLogger = nopLarkLogger{}
	s.onReconnected = func() { reconnected.Add(1) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitFor(t, 20*time.Second, "SDK read-deadline reconnect", func() bool {
		return len(gateway.connections()) >= 2 && reconnected.Load() == 1
	})
	conns := gateway.connections()
	if gap := conns[1].Sub(conns[0]); gap < 5*time.Second {
		t.Fatalf("SDK reconnected after %s, expected its ~7s read deadline", gap)
	}
}
