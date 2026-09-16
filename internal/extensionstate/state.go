// Package extensionstate closes static extension registration at application startup.
package extensionstate

import (
	"errors"
	"sync"
)

var ErrFrozen = errors.New("extension registration is closed after application startup")

type gate struct {
	mu     sync.RWMutex
	closed bool
}

var startup gate

// BeginRegistration holds startup open until the returned release is called.
// A pending Freeze also rejects registration: blocking a nested reader behind
// that writer could deadlock an outer registration that has not yet returned.
func BeginRegistration() (func(), error) { return startup.beginRegistration() }

func (g *gate) beginRegistration() (func(), error) {
	if !g.mu.TryRLock() {
		return nil, ErrFrozen
	}
	if g.closed {
		g.mu.RUnlock()
		return nil, ErrFrozen
	}
	return g.mu.RUnlock, nil
}

// Freeze waits for admitted registrations and permanently closes registration.
// It must be called outside a registration, before serving application work.
func Freeze() { startup.freeze() }

func (g *gate) freeze() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
}
