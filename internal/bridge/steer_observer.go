package bridge

import (
	"strings"
	"sync"
)

// steerObserverState lets the running stream of a Feishu root learn that a
// steered input was accepted into one of its turns, so the final answer can
// keep every steer's answer (see mergeSteeredFinalAnswer).
type steerObserverState struct {
	mu  sync.Mutex
	fns map[string]steerObserver
}

type steerObserver struct {
	token uint64
	fn    func(turnID string)
}

func (b *Bridge) registerSteerObserver(rootMsgID string, token uint64, fn func(turnID string)) {
	if b == nil || strings.TrimSpace(rootMsgID) == "" || fn == nil {
		return
	}
	b.steerObservers.mu.Lock()
	defer b.steerObservers.mu.Unlock()
	if b.steerObservers.fns == nil {
		b.steerObservers.fns = make(map[string]steerObserver)
	}
	b.steerObservers.fns[rootMsgID] = steerObserver{token: token, fn: fn}
}

func (b *Bridge) unregisterSteerObserver(rootMsgID string, token uint64) {
	if b == nil {
		return
	}
	b.steerObservers.mu.Lock()
	defer b.steerObservers.mu.Unlock()
	if cur, ok := b.steerObservers.fns[rootMsgID]; ok && cur.token == token {
		delete(b.steerObservers.fns, rootMsgID)
	}
}

// noteTurnSteered reports an accepted steer to the root's running stream.
func (b *Bridge) noteTurnSteered(rootMsgID, turnID string) {
	if b == nil {
		return
	}
	b.steerObservers.mu.Lock()
	obs, ok := b.steerObservers.fns[rootMsgID]
	b.steerObservers.mu.Unlock()
	if ok && obs.fn != nil {
		obs.fn(strings.TrimSpace(turnID))
	}
}
