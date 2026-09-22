package hub

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/amuxos/amux/internal/modelservice"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *hubServer) ProxyModel(req *pb.ProxyModelRequest, stream grpc.ServerStreamingServer[pb.ProxyModelChunk]) error {
	actor, err := authenticatedActor(stream.Context(), s.principals)
	if err != nil {
		return err
	}
	if actor == nil || actor.ActorPrincipalID == "" {
		return status.Error(codes.Unauthenticated, "model service requires an authenticated principal")
	}
	service, ok := modelservice.Get(req.Service)
	if !ok {
		return status.Error(codes.NotFound, "unknown model service")
	}
	if len(req.Body) > 32<<20 {
		return status.Error(codes.ResourceExhausted, "model request too large")
	}
	if req.Method != http.MethodPost && req.Method != http.MethodGet {
		return status.Error(codes.InvalidArgument, "unsupported method")
	}
	// Fixed local origin: distributions choose upstream URLs, never callers.
	if req.Path != "/v1/responses" && req.Path != "/v1/messages" && req.Path != "/v1/messages/count_tokens" && req.Path != "/v1/chat/completions" && req.Path != "/v1/models" {
		return status.Error(codes.InvalidArgument, "unsupported model endpoint")
	}
	r, err := http.NewRequestWithContext(stream.Context(), req.Method, "http://localhost"+req.Path, bytes.NewReader(req.Body))
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid request")
	}
	r.Header.Set("Content-Type", "application/json")
	for _, name := range []string{"User-Agent", "Anthropic-Version", "Anthropic-Beta", "X-Session-Id", "X-Amux-Reasoning-Effort"} {
		if value := req.Headers[name]; value != "" {
			r.Header.Set(name, value)
		}
	}
	w := &modelStreamWriter{stream: stream, header: http.Header{}}
	if err := service.Proxy(stream.Context(), actor.ActorPrincipalID, w, r); err != nil {
		return status.Error(codes.Unavailable, "model service request failed")
	}
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
	return w.err
}

type modelStreamWriter struct {
	stream grpc.ServerStreamingServer[pb.ProxyModelChunk]
	header http.Header
	sent   bool
	err    error
}

func (w *modelStreamWriter) Header() http.Header { return w.header }
func (w *modelStreamWriter) WriteHeader(code int) {
	if w.sent {
		return
	}
	w.sent = true
	if code < 100 || code > 599 {
		code = 502
	}
	headers := map[string]string{}
	for _, name := range []string{"Content-Type", "Cache-Control", "Retry-After", "X-Request-Id"} {
		if v := w.header.Get(name); v != "" {
			headers[name] = v
		}
	}
	w.err = w.stream.Send(&pb.ProxyModelChunk{Status: int32(code), Headers: headers})
}
func (w *modelStreamWriter) Write(p []byte) (int, error) {
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
	}
	n := 0
	for len(p) > 0 {
		size := min(len(p), 64<<10)
		if err := w.stream.Send(&pb.ProxyModelChunk{Body: p[:size]}); err != nil {
			w.err = err
			return n, fmt.Errorf("write model stream: %w", err)
		}
		n += size
		p = p[size:]
	}
	return n, nil
}
func (w *modelStreamWriter) Flush() {
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
}
