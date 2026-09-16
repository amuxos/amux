package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// debugDrops reports whether verbose jsonrpc drop logging is enabled via
// AMUX_DEBUG_JSONRPC=1. When enabled, the client logs notifications/requests/
// responses it would otherwise silently drop (no registered handler, no pending
// call, or unparseable frame), so a missing handler is observable instead of
// silent (e.g. an unhandled provider notification stalling a turn).
func debugDrops() bool { return os.Getenv("AMUX_DEBUG_JSONRPC") == "1" }

// debugDrop logs a dropped jsonrpc notification or server->client request.
// params is truncated to keep output readable; it is only stringified when
// drop logging is enabled.
func debugDrop(kind, method string, id int64, params json.RawMessage) {
	if !debugDrops() {
		return
	}
	ps := string(params)
	if len(ps) > 200 {
		ps = ps[:200] + "...(truncated)"
	}
	fmt.Fprintf(os.Stderr, "amux jsonrpc drop: kind=%s method=%q id=%d params=%s\n", kind, method, id, ps)
}

// debugDropID logs a dropped response with no matching pending call.
func debugDropID(id int64) {
	if !debugDrops() {
		return
	}
	fmt.Fprintf(os.Stderr, "amux jsonrpc drop: kind=response id=%d (no pending call)\n", id)
}

// debugDropParse logs an unparseable jsonrpc frame.
func debugDropParse(line string, err error) {
	if !debugDrops() {
		return
	}
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 200 {
		trimmed = trimmed[:200] + "...(truncated)"
	}
	fmt.Fprintf(os.Stderr, "amux jsonrpc drop: kind=unparseable err=%v line=%s\n", err, trimmed)
}

type Message struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

type Handler func(Message)

type Client struct {
	r io.Reader
	w io.Writer

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan Message

	notifyMu sync.RWMutex
	notify   map[string]Handler
	request  map[string]Handler

	// onUnknownRequest is invoked for a server->client request that has no
	// registered handler, just before the client replies with a -32601 error.
	// It lets the proc observe (and surface) the unhandled method instead of
	// the failure being silent. Optional; nil means no callback.
	onUnknownRequest func(method string, id int64, params json.RawMessage)

	closed  chan struct{}
	onClose sync.Once
}

// OnUnknownRequest registers a callback fired when a server->client request
// arrives for which no handler is registered. The client still replies with a
// -32601 error so the provider does not block waiting. The callback is for
// observation/surfacing only.
func (c *Client) OnUnknownRequest(fn func(method string, id int64, params json.RawMessage)) {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	c.onUnknownRequest = fn
}

func NewClient(r io.Reader, w io.Writer) *Client {
	return &Client{
		r:       r,
		w:       w,
		pending: make(map[int64]chan Message),
		notify:  make(map[string]Handler),
		request: make(map[string]Handler),
		closed:  make(chan struct{}),
	}
}

func (c *Client) OnNotification(method string, h Handler) {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	c.notify[method] = h
}

func (c *Client) OnRequest(method string, h Handler) {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	c.request[method] = h
}

func (c *Client) Run() error {
	defer c.closePending()
	scanner := bufio.NewScanner(c.r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			debugDropParse(scanner.Text(), err)
			continue
		}
		c.dispatch(msg)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (c *Client) Call(ctx context.Context, method string, params any) (Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id := c.nextID.Add(1)
	ch := make(chan Message, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	if err := c.write(Message{JSONRPC: "2.0", ID: id, Method: method, Params: mustRaw(params)}); err != nil {
		c.removePending(id)
		return Message{}, err
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return Message{}, io.ErrClosedPipe
		}
		if msg.Error != nil {
			return msg, msg.Error
		}
		return msg, nil
	case <-ctx.Done():
		c.removePending(id)
		return Message{}, ctx.Err()
	}
}

func (c *Client) Notify(method string, params any) error {
	return c.write(Message{JSONRPC: "2.0", Method: method, Params: mustRaw(params)})
}

func (c *Client) Respond(id int64, result any) error {
	return c.write(Message{JSONRPC: "2.0", ID: id, Result: mustRaw(result)})
}

func (c *Client) RespondError(id int64, code int, message string) error {
	return c.write(Message{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: message}})
}

func (c *Client) dispatch(msg Message) {
	if msg.ID != 0 && msg.Method == "" {
		c.pendingMu.Lock()
		ch := c.pending[msg.ID]
		delete(c.pending, msg.ID)
		c.pendingMu.Unlock()
		if ch != nil {
			ch <- msg
			close(ch)
		} else {
			// Response with no matching pending call (e.g. late reply after
			// context cancellation, or a server response we never requested).
			debugDropID(msg.ID)
		}
		return
	}

	c.notifyMu.RLock()
	var h Handler
	if msg.ID != 0 {
		h = c.request[msg.Method]
	} else {
		h = c.notify[msg.Method]
	}
	c.notifyMu.RUnlock()
	if h != nil {
		h(msg)
		return
	}
	// No registered handler. For a server->client request we MUST reply with a
	// JSON-RPC error, otherwise the provider blocks forever waiting for a
	// response. For a notification there is no id
	// to reply to; just log.
	kind := "notification"
	if msg.ID != 0 {
		kind = "server-request"
	}
	debugDrop(kind, msg.Method, msg.ID, msg.Params)
	if msg.ID != 0 {
		c.notifyMu.RLock()
		fn := c.onUnknownRequest
		c.notifyMu.RUnlock()
		if fn != nil {
			fn(msg.Method, msg.ID, msg.Params)
		}
		// Best-effort: if the client is closing, the write will fail and we
		// just drop the response.
		_ = c.RespondError(msg.ID, -32601, "method not found: "+msg.Method)
	}
}

// maxWriteDuration bounds a single frame write. A wedged provider that stops
// reading stdin fills the OS pipe buffer, and an unbounded Write would block
// here holding writeMu — deadlocking every later Call/Respond (resubmissions,
// permission responses, watchdog turn/interrupt) behind it and re-wedging the
// session a client-side watchdog just freed. 10s matches the proc Interrupt
// bound; the provider stdin buffer is large enough that any healthy provider
// drains a frame far faster.
const maxWriteDuration = 10 * time.Second

func (c *Client) write(msg Message) error {
	select {
	case <-c.closed:
		return io.ErrClosedPipe
	default:
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// Cap the write at the OS level when the writer is an *os.File (an exec
	// StdinPipe), so a full pipe fails fast instead of blocking writeMu forever.
	// The ctx passed to Call only governs the response wait, not this write, so
	// without the deadline the caller's timeout cannot fire on the write path.
	if f, ok := c.w.(*os.File); ok {
		_ = f.SetWriteDeadline(time.Now().Add(maxWriteDuration))
		defer func() { _ = f.SetWriteDeadline(time.Time{}) }()
	}
	_, err = c.w.Write(data)
	return err
}

func (c *Client) removePending(id int64) {
	c.pendingMu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (c *Client) closePending() {
	c.onClose.Do(func() {
		close(c.closed)
		c.pendingMu.Lock()
		pending := c.pending
		c.pending = make(map[int64]chan Message)
		c.pendingMu.Unlock()
		for _, ch := range pending {
			close(ch)
		}
	})
}

func mustRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(errors.New("jsonrpc: marshal params: " + err.Error()))
	}
	return data
}
