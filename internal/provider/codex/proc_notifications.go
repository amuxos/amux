package codex

import providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"

// Restored goals can emit during thread/resume, before the Session owner has
// installed its admission callback. Defer startup events, preserving their
// order; after handover, notifications retain the normal synchronous ordering
// relative to turn/start responses.
func (p *Proc) orderedHandler(h providerjsonrpc.Handler) providerjsonrpc.Handler {
	return func(message providerjsonrpc.Message) {
		if !p.notificationsInitialized {
			h(message)
			return
		}
		p.notificationMu.Lock()
		defer p.notificationMu.Unlock()
		if !p.notificationsReady {
			if len(p.notificationQueue) >= 4096 {
				go p.Close() // Fail closed on an unbounded startup event flood.
				return
			}
			p.notificationQueue = append(p.notificationQueue, func() { h(message) })
			return
		}
		h(message)
	}
}
func (p *Proc) onNotification(method string, h providerjsonrpc.Handler) {
	p.client.OnNotification(method, p.orderedHandler(h))
}
func (p *Proc) onRequest(method string, h providerjsonrpc.Handler) {
	p.client.OnRequest(method, p.orderedHandler(h))
}
func (p *Proc) EnableGoalTurns() {
	// SetProc must return before an admission needs its restored queue/sinks.
	go p.enableGoalTurns()
}

func (p *Proc) enableGoalTurns() {
	p.notificationMu.Lock()
	defer p.notificationMu.Unlock()
	if p.notificationsReady {
		return
	}
	p.notificationsReady = true
	queue := p.notificationQueue
	p.notificationQueue = nil
	for _, fn := range queue {
		fn()
	}
}
