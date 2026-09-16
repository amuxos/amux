package tunnel

import "context"

type trustedSubmitContextKey struct{}
type requestIDContextKey struct{}

// withTrustedSubmit marks a Submit dispatched by the runtime's reverse-tunnel
// client. The marker is process-local context state and cannot be supplied by
// an ordinary gRPC caller that invokes amuxlet directly.
func withTrustedSubmit(ctx context.Context, requestID string) context.Context {
	ctx = context.WithValue(ctx, trustedSubmitContextKey{}, true)
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// IsTrustedSubmit reports whether a Submit entered amuxlet through the
// reverse-tunnel dispatch path owned by amuxd.
func IsTrustedSubmit(ctx context.Context) bool {
	trusted, _ := ctx.Value(trustedSubmitContextKey{}).(bool)
	return trusted
}

// RequestID returns the hop-local reverse-tunnel request identifier attached
// to a trusted runtime request. It is diagnostic only and must not be used as
// durable business identity.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
