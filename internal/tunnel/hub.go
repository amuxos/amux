// Package tunnel manages the reverse-tunnel connections between runtimes and hub.
package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
)

// streamBufferSize bounds the per-stream backlog the hub buffers between the
// shared target reader (producer) and a single RouteStream consumer. The reader
// pushes data frames non-blockingly so one slow stream can never stall unary
// responses, heartbeats, or other streams sharing the target's read loop; when
// this backlog fills, that stream is failed with an explicit overflow error.
// It is a var (not const) so tests can shrink it to force overflow.
var streamBufferSize = 512

// SessionEvent carries information about sessions that appeared or disappeared
// on a target after a sessions-update message from a runtime.
type SessionEvent struct {
	TargetID   string
	TargetName string
	Added      []*pb.Session // sessions that are new since the last update
	Removed    []*pb.Session // sessions that disappeared since the last update
	// Archived holds sessions whose state transitioned to "archived" since the
	// last update. amuxd uses this to reclaim persistent agent instances whose
	// owner session has ended (the instance follows the owner lifecycle).
	Archived []*pb.Session
}

// Hub tracks all connected runtimes and routes RPCs through them.
type Hub struct {
	mu      sync.RWMutex
	targets map[string]*targetEntry

	evMu      sync.RWMutex
	evHandler func(SessionEvent)

	// reg holds Phase 2 registry state (capabilities + heartbeat load) for
	// connected runtimes. Tunnel presence is the authoritative liveness signal
	// (a target is removed on disconnect), so registry staleness is disabled;
	// heartbeat load is advisory ranking input for the scheduler.
	reg *registry.Registry
	// auth, when set, gates runtime registration (shared token + allowlist).
	auth *registry.Authorizer
}

func NewHub() *Hub {
	return &Hub{
		targets: make(map[string]*targetEntry),
		reg:     registry.New(registry.Options{}),
	}
}

// Registry exposes the hub's runtime registry for the scheduler/reconciler.
func (h *Hub) Registry() *registry.Registry { return h.reg }

// SetAuthorizer installs a registration authorizer. Passing nil (or never
// calling this) leaves registration open, preserving local/hub behavior.
func (h *Hub) SetAuthorizer(a *registry.Authorizer) { h.auth = a }

// SetSessionEventHandler registers a callback that is invoked whenever the
// set of sessions on any target changes. The callback runs in the tunnel's
// receive goroutine — keep it non-blocking.
func (h *Hub) SetSessionEventHandler(fn func(SessionEvent)) {
	h.evMu.Lock()
	h.evHandler = fn
	h.evMu.Unlock()
}

type targetEntry struct {
	id           string
	name         string
	version      string
	capabilities *registry.Capabilities
	mu           sync.Mutex
	sessions     []*pb.Session
	sendCh       chan *pb.TunnelMsg
	pending      map[string]*pendingCall
}

type pendingCall struct {
	respCh chan *pb.TunnelMsg
	dataCh chan *pb.TunnelMsg // non-nil for streaming (Attach)
	doneCh chan error         // streaming transport termination (disconnect)
	stopCh chan struct{}      // RouteStream stopped consuming
}

// ListTargets returns a snapshot of all connected targets, joined with any
// Phase 2 registry capabilities/load reported by the runtime.
func (h *Hub) ListTargets() []*pb.Target {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*pb.Target, 0, len(h.targets))
	for _, e := range h.targets {
		e.mu.Lock()
		sb := &pb.Target{Id: e.id, Name: e.name, Version: e.version, Capabilities: registryCapabilitiesToPB(e.capabilities), Sessions: e.sessions}
		e.mu.Unlock()
		if rt, ok := h.reg.Get(e.id); ok {
			caps := rt.Caps
			load := rt.Load
			sb.Capabilities = registryCapabilitiesToPB(&caps)
			sb.Load = registryLoadToPB(load)
			sb.Status = string(rt.Status)
		}
		out = append(out, sb)
	}
	return out
}

// RemoveSessions removes named sessions from a target cache immediately after a
// routed mutation succeeds. The runtime still sends the authoritative session
// snapshot asynchronously; this keeps immediate ListTargets calls from serving
// stale entries.
func (h *Hub) RemoveSessions(targetID string, names []string) {
	if len(names) == 0 {
		return
	}
	entry, err := h.find(targetID)
	if err != nil {
		return
	}
	remove := make(map[string]struct{}, len(names))
	for _, name := range names {
		remove[name] = struct{}{}
	}
	entry.mu.Lock()
	prev := entry.sessions
	next := make([]*pb.Session, 0, len(prev))
	for _, sess := range prev {
		if _, ok := remove[sess.Name]; ok {
			continue
		}
		next = append(next, sess)
	}
	if len(next) == len(prev) {
		entry.mu.Unlock()
		return
	}
	entry.sessions = next
	entry.mu.Unlock()
	h.fireSessionEvent(entry.id, entry.name, prev, next)
}

// Serve handles a single runtime tunnel connection until it disconnects.
// The first TunnelMsg must be kind="register".
func (h *Hub) Serve(stream pb.Amuxd_TunnelServer) error {
	// Read registration message.
	regMsg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("tunnel: read register: %w", err)
	}
	if regMsg.Kind != "register" {
		return fmt.Errorf("tunnel: expected register, got %q", regMsg.Kind)
	}
	var reg pb.RegisterPayload
	if err := json.Unmarshal(regMsg.Payload, &reg); err != nil {
		return fmt.Errorf("tunnel: decode register: %w", err)
	}

	// Phase 2 registration auth: gate on shared token / target allowlist /
	// label allowlist when an authorizer is configured (open otherwise).
	var caps registry.Capabilities
	caps = pbCapabilitiesToRegistry(reg.Capabilities)
	if h.auth != nil {
		if err := h.auth.Authorize(reg.TargetId, reg.Token, caps); err != nil {
			return fmt.Errorf("tunnel: register denied for %q: %w", reg.TargetId, err)
		}
	}

	entry := &targetEntry{
		id:           reg.TargetId,
		name:         reg.TargetName,
		version:      reg.Version,
		capabilities: cloneTargetCapabilities(&caps),
		sendCh:       make(chan *pb.TunnelMsg, 64),
		pending:      make(map[string]*pendingCall),
	}

	h.mu.Lock()
	h.targets[entry.id] = entry
	h.mu.Unlock()

	// Record static capabilities in the registry (no-op caps for Phase 1 runtimes).
	h.reg.Register(reg.TargetId, reg.TargetName, caps)

	defer func() {
		h.mu.Lock()
		delete(h.targets, entry.id)
		h.mu.Unlock()
		h.reg.Remove(entry.id)
		// Fail all pending calls.
		entry.mu.Lock()
		for _, p := range entry.pending {
			if p.dataCh != nil {
				p.doneCh <- fmt.Errorf("target %q disconnected", entry.id)
			} else {
				close(p.respCh)
			}
		}
		entry.pending = make(map[string]*pendingCall)
		entry.mu.Unlock()
	}()

	ctx := stream.Context()

	// Writer goroutine: drain sendCh → stream.
	go func() {
		for {
			select {
			case msg := <-entry.sendCh:
				_ = stream.Send(msg)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Reader loop: dispatch responses from amuxlet.
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		switch msg.Kind {
		case "sessions":
			var sp pb.SessionsPayload
			if err := json.Unmarshal(msg.Payload, &sp); err == nil {
				entry.mu.Lock()
				prev := entry.sessions
				entry.sessions = sp.Sessions
				entry.mu.Unlock()
				h.fireSessionEvent(entry.id, entry.name, prev, sp.Sessions)
			}
		case "heartbeat":
			var hp pb.HeartbeatPayload
			if err := json.Unmarshal(msg.Payload, &hp); err == nil {
				h.reg.Heartbeat(entry.id, pbLoadToRegistry(hp.Load))
			}
		case "resp":
			entry.mu.Lock()
			p, ok := entry.pending[msg.RequestId]
			if ok {
				delete(entry.pending, msg.RequestId)
			}
			entry.mu.Unlock()
			if ok {
				p.respCh <- msg
				close(p.respCh)
			}
		case "data":
			entry.mu.Lock()
			p, ok := entry.pending[msg.RequestId]
			if ok && p.dataCh != nil {
				select {
				case p.dataCh <- msg:
					entry.mu.Unlock()
				case <-p.stopCh:
					entry.mu.Unlock()
				default:
					delete(entry.pending, msg.RequestId)
					entry.mu.Unlock()
					select {
					case p.doneCh <- fmt.Errorf("stream %s buffer overflow", msg.RequestId):
					default:
					}
				}
			} else {
				entry.mu.Unlock()
			}
		case "end":
			entry.mu.Lock()
			p, ok := entry.pending[msg.RequestId]
			if ok {
				delete(entry.pending, msg.RequestId)
			}
			entry.mu.Unlock()
			if ok && p.dataCh != nil {
				if msg.Error != "" {
					p.dataCh <- msg // carry error
				}
				close(p.dataCh)
			}
		}
	}
}

// Route sends a unary RPC through the tunnel to the named target.
func (h *Hub) Route(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
	entry, err := h.find(targetID)
	if err != nil {
		return nil, err
	}
	reqID := uuid.New().String()
	p := &pendingCall{respCh: make(chan *pb.TunnelMsg, 1)}
	entry.mu.Lock()
	entry.pending[reqID] = p
	entry.mu.Unlock()

	if err := h.send(ctx, entry, reqID, method, payload); err != nil {
		entry.mu.Lock()
		delete(entry.pending, reqID)
		entry.mu.Unlock()
		return nil, err
	}

	select {
	case resp, ok := <-p.respCh:
		if !ok {
			return nil, fmt.Errorf("target %q disconnected", targetID)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		return resp, nil
	case <-ctx.Done():
		entry.mu.Lock()
		delete(entry.pending, reqID)
		entry.mu.Unlock()
		return nil, ctx.Err()
	}
}

// RouteStream sends a streaming RPC through the tunnel, calling fn for each data chunk.
func (h *Hub) RouteStream(ctx context.Context, targetID, method string, payload interface{}, fn func(*pb.TunnelMsg) error) error {
	entry, err := h.find(targetID)
	if err != nil {
		return err
	}
	reqID := uuid.New().String()
	p := &pendingCall{
		respCh: make(chan *pb.TunnelMsg, 1),
		dataCh: make(chan *pb.TunnelMsg, streamBufferSize),
		doneCh: make(chan error, 1),
		stopCh: make(chan struct{}),
	}
	entry.mu.Lock()
	entry.pending[reqID] = p
	entry.mu.Unlock()

	if err := h.send(ctx, entry, reqID, method, payload); err != nil {
		entry.mu.Lock()
		delete(entry.pending, reqID)
		entry.mu.Unlock()
		return err
	}

	defer func() {
		close(p.stopCh)
		// Send cancel so amuxlet can tear down the Attach subscription.
		entry.mu.Lock()
		_, stillPending := entry.pending[reqID]
		if stillPending {
			delete(entry.pending, reqID)
		}
		entry.mu.Unlock()
		if stillPending {
			select {
			case entry.sendCh <- &pb.TunnelMsg{Kind: "cancel", RequestId: reqID}:
			default:
			}
		}
	}()

	// drain delivers everything still buffered in dataCh (in order) through fn,
	// used after doneCh fires. The reader deletes this pending call from its map
	// before signaling an overflow, and has already exited before a disconnect
	// signal, so no new frames arrive once doneCh fires: a non-blocking receive
	// that finds the channel empty means fully drained. Returns fn's error (or a
	// carried frame error) so drain failures surface instead of the done error.
	drain := func() error {
		for {
			select {
			case msg, ok := <-p.dataCh:
				if !ok {
					return nil
				}
				if msg.Error != "" {
					return fmt.Errorf("%s", msg.Error)
				}
				if err := fn(msg); err != nil {
					return err
				}
			default:
				return nil
			}
		}
	}

	for {
		select {
		case msg, ok := <-p.dataCh:
			if !ok {
				return nil
			}
			if msg.Error != "" {
				return fmt.Errorf("%s", msg.Error)
			}
			if err := fn(msg); err != nil {
				return err
			}
		case err := <-p.doneCh:
			// Deliver frames buffered before the transport failed so an overflow
			// or disconnect never discards data the hub already received; only
			// the frame that could not fit (overflow) or arrive (disconnect) is
			// lost. A drain-time fn error takes precedence over the done error.
			if derr := drain(); derr != nil {
				return derr
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *Hub) find(targetID string) (*targetEntry, error) {
	h.mu.RLock()
	entry, ok := h.targets[targetID]
	h.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("target %q not connected", targetID)
	}
	return entry, nil
}

func (h *Hub) send(ctx context.Context, entry *targetEntry, reqID, method string, payload interface{}) error {
	raw, err := marshalTunnelPayload(payload)
	if err != nil {
		return err
	}
	msg := &pb.TunnelMsg{Kind: "req", RequestId: reqID, Method: method, Payload: raw}
	select {
	case entry.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fireSessionEvent computes the diff between prev and next session lists and
// calls the registered session event handler.
func (h *Hub) fireSessionEvent(targetID, targetName string, prev, next []*pb.Session) {
	h.evMu.RLock()
	fn := h.evHandler
	h.evMu.RUnlock()
	if fn == nil {
		return
	}

	prevMap := make(map[string]*pb.Session, len(prev))
	for _, s := range prev {
		prevMap[s.Id] = s
	}
	nextMap := make(map[string]*pb.Session, len(next))
	for _, s := range next {
		nextMap[s.Id] = s
	}

	ev := SessionEvent{TargetID: targetID, TargetName: targetName}
	for _, s := range next {
		prevSession, existed := prevMap[s.Id]
		if !existed || becameRunning(prevSession, s) {
			ev.Added = append(ev.Added, s)
		}
		if existed && becameArchived(prevSession, s) {
			ev.Archived = append(ev.Archived, s)
		}
	}
	for _, s := range prev {
		if _, stillHere := nextMap[s.Id]; !stillHere {
			ev.Removed = append(ev.Removed, s)
		}
	}
	if len(ev.Added) > 0 || len(ev.Removed) > 0 || len(ev.Archived) > 0 {
		// Run in a separate goroutine so we don't block the tunnel reader
		// goroutine — the handler may do network I/O (e.g. send a Lark DM).
		go func() {
			defer func() {
				if r := recover(); r != nil {
					// Log but don't crash the hub on a handler panic.
					_ = r
				}
			}()
			fn(ev)
		}()
	}
}

func becameRunning(prev, next *pb.Session) bool {
	if prev == nil || next == nil {
		return false
	}
	return prev.State != "running" && next.State == "running"
}

// becameArchived reports whether a session transitioned into the archived
// state since the previous update. A freshly-appeared archived session (no
// prev) is not treated as a transition — it was never alive on this target in
// the watcher's view, so there is no owner-lifecycle event to act on.
func becameArchived(prev, next *pb.Session) bool {
	if prev == nil || next == nil {
		return false
	}
	return prev.State != "archived" && next.State == "archived"
}

func cloneTargetCapabilities(caps *registry.Capabilities) *registry.Capabilities {
	if caps == nil {
		return nil
	}
	return &registry.Capabilities{
		AgentTypes:     cloneStringSlice(caps.AgentTypes),
		Features:       cloneStringSlice(caps.Features),
		RuntimeTools:   cloneStringSlice(caps.RuntimeTools),
		WorkspaceRoots: cloneStringSlice(caps.WorkspaceRoots),
		Shells:         cloneStringSlice(caps.Shells),
		PathStyle:      caps.PathStyle,
		NetworkProfile: caps.NetworkProfile,
		CredentialRefs: cloneStringSlice(caps.CredentialRefs),
		Labels:         cloneStringMap(caps.Labels),
	}
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}
