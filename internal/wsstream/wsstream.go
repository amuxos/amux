// Package wsstream holds the WebSocket keepalive plumbing shared by the
// read-only session viewers (desktop gateway and hub web). It owns the
// ping/pong deadline dance and the client-close reader goroutine so the two
// HTTP packages only have to supply the event-forwarding loop.
package wsstream

import (
	"context"
	"time"

	"github.com/gorilla/websocket"
)

// Keepalive deadlines for the viewer WebSocket. These match the values the
// gateway and hub viewers previously hard-coded separately.
const (
	WriteWait  = 10 * time.Second
	PongWait   = 60 * time.Second
	PingPeriod = 25 * time.Second
)

// Conn is the subset of *websocket.Conn the keepalive helper uses.
type Conn interface {
	SetReadDeadline(t time.Time) error
	SetPongHandler(h func(string) error)
	ReadMessage() (int, []byte, error)
	SetWriteDeadline(t time.Time) error
	WriteMessage(messageType int, data []byte) error
	WriteJSON(v any) error
}

// Keepalive wires the ping/pong keepalive loop and a client-close reader onto
// conn. It launches two goroutines (reader + pinger) tied to ctx: when the
// client disconnects or a ping write fails, cancel is invoked. The returned
// write func applies WriteWait before each JSON frame. Callers own conn and
// must close it (and arrange for ctx to be cancelled) when their forward loop
// ends; Keepalive's goroutines exit on ctx.Done or read error.
func Keepalive(conn Conn, ctx context.Context, cancel context.CancelFunc) func(v any) error {
	_ = conn.SetReadDeadline(time.Now().Add(PongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(PongWait))
		return nil
	})
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(PingPeriod)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(WriteWait))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	return func(v any) error {
		_ = conn.SetWriteDeadline(time.Now().Add(WriteWait))
		return conn.WriteJSON(v)
	}
}
