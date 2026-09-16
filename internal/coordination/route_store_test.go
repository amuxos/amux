package coordination

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemRouteStoreClaimsIdempotentlyAndBindsBackend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemRouteStore()
	route := &Route{ID: "delegation-1", IdempotencyKey: "request-1", TargetRef: "team/reviewer@abc", CreatedAt: time.Now().UTC()}

	claimed, created, err := store.Claim(ctx, route)
	if err != nil || !created || claimed.ID != route.ID {
		t.Fatalf("Claim = %#v, %v, %v", claimed, created, err)
	}
	replayed, created, err := store.Claim(ctx, &Route{ID: "delegation-2", IdempotencyKey: "request-1"})
	if err != nil || created || replayed.ID != route.ID {
		t.Fatalf("replayed Claim = %#v, %v, %v", replayed, created, err)
	}
	bound, err := store.Bind(ctx, route.ID, "amux-task:task-1")
	if err != nil || bound.BackendRef != "amux-task:task-1" {
		t.Fatalf("Bind = %#v, %v", bound, err)
	}
	if _, err := store.Bind(ctx, route.ID, "amux-task:task-2"); !errors.Is(err, ErrRouteAlreadyBound) {
		t.Fatalf("second Bind error = %v", err)
	}
}

func TestMemRouteStoreDeletesOnlyUnboundClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemRouteStore()
	_, _, _ = store.Claim(ctx, &Route{ID: "unbound", IdempotencyKey: "retry"})
	if err := store.DeleteUnbound(ctx, "unbound"); err != nil {
		t.Fatalf("DeleteUnbound: %v", err)
	}
	if _, err := store.Get(ctx, "unbound"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted error = %v", err)
	}
	_, _, _ = store.Claim(ctx, &Route{ID: "bound"})
	_, _ = store.Bind(ctx, "bound", "amux-task:task-1")
	if err := store.DeleteUnbound(ctx, "bound"); !errors.Is(err, ErrRouteAlreadyBound) {
		t.Fatalf("DeleteUnbound bound error = %v", err)
	}
}
