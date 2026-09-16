package coordination

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrNotFound          = errors.New("delegation route not found")
	ErrRouteCreating     = errors.New("delegation route is still being created")
	ErrRouteAlreadyBound = errors.New("delegation route is already bound")
)

// RouteStore persists only the stable mapping from a public Delegation ID to
// the backend that owns its lifecycle. It deliberately has no status/event API.
type RouteStore interface {
	Claim(context.Context, *Route) (*Route, bool, error)
	Bind(context.Context, string, string) (*Route, error)
	Get(context.Context, string) (*Route, error)
	DeleteUnbound(context.Context, string) error
}

// MemRouteStore is the in-memory RouteStore used by service tests.
type MemRouteStore struct {
	mu          sync.RWMutex
	routes      map[string]*Route
	idempotency map[string]string
}

func NewMemRouteStore() *MemRouteStore {
	return &MemRouteStore{routes: make(map[string]*Route), idempotency: make(map[string]string)}
}

func (m *MemRouteStore) Claim(_ context.Context, route *Route) (*Route, bool, error) {
	if err := validateRoute(route); err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if route.IdempotencyKey != "" {
		if id, ok := m.idempotency[route.IdempotencyKey]; ok {
			return cloneRoute(m.routes[id]), false, nil
		}
	}
	if _, ok := m.routes[route.ID]; ok {
		return nil, false, fmt.Errorf("delegation route %q already exists", route.ID)
	}
	m.routes[route.ID] = cloneRoute(route)
	if route.IdempotencyKey != "" {
		m.idempotency[route.IdempotencyKey] = route.ID
	}
	return cloneRoute(route), true, nil
}

func (m *MemRouteStore) Bind(_ context.Context, id, backendRef string) (*Route, error) {
	backendRef = strings.TrimSpace(backendRef)
	if backendRef == "" {
		return nil, errors.New("backend ref is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	route, ok := m.routes[id]
	if !ok {
		return nil, fmt.Errorf("delegation route %q: %w", id, ErrNotFound)
	}
	if route.BackendRef != "" {
		if route.BackendRef == backendRef {
			return cloneRoute(route), nil
		}
		return nil, fmt.Errorf("delegation route %q: %w", id, ErrRouteAlreadyBound)
	}
	route.BackendRef = backendRef
	return cloneRoute(route), nil
}

func (m *MemRouteStore) Get(_ context.Context, id string) (*Route, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	route, ok := m.routes[id]
	if !ok {
		return nil, fmt.Errorf("delegation route %q: %w", id, ErrNotFound)
	}
	return cloneRoute(route), nil
}

func (m *MemRouteStore) DeleteUnbound(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	route, ok := m.routes[id]
	if !ok {
		return nil
	}
	if route.BackendRef != "" {
		return fmt.Errorf("delegation route %q: %w", id, ErrRouteAlreadyBound)
	}
	delete(m.routes, id)
	if route.IdempotencyKey != "" {
		delete(m.idempotency, route.IdempotencyKey)
	}
	return nil
}

func validateRoute(route *Route) error {
	if route == nil {
		return errors.New("delegation route is required")
	}
	if strings.TrimSpace(route.ID) == "" {
		return errors.New("delegation route id is required")
	}
	if strings.TrimSpace(route.BackendRef) != "" {
		return errors.New("new delegation route must not be pre-bound")
	}
	return nil
}

func cloneRoute(route *Route) *Route {
	if route == nil {
		return nil
	}
	copy := *route
	return &copy
}
