package modelservice

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/transport"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// StartGateway is owned by the session broker, so Runtime restarts do not break
// a running agent's connection. The key is its existing actor credential.
func StartGateway(env []string, service string) (baseURL, key string, closeGateway func(), err error) {
	values := map[string]string{}
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			values[k] = v
		}
	}
	key = values["AMUX_ACTOR_TOKEN"]
	if key == "" || values["AMUX_HUB_ADDR"] == "" {
		return "", "", nil, fmt.Errorf("model service requires an AMUX principal and Hub address")
	}
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, transport.GRPCClientDialOptions()...)
	conn, err := grpc.NewClient(values["AMUX_HUB_ADDR"], opts...)
	if err != nil {
		return "", "", nil, err
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		conn.Close()
		return "", "", nil, err
	}
	client := pb.NewAmuxdClient(conn)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == "" {
			provided = r.Header.Get("X-Api-Key")
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(provided)) != 1 {
			gatewayError(w, 401, "AMUX authentication required")
			return
		}
		if r.Header.Get("Origin") != "" {
			gatewayError(w, 403, "browser requests are not supported")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
		if err != nil {
			gatewayError(w, 413, "model request too large")
			return
		}
		actor := key
		if path := values["AMUX_ACTOR_TOKEN_FILE"]; path != "" {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				gatewayError(w, 401, "AMUX session credential unavailable")
				return
			}
			actor = strings.TrimSpace(string(data))
			var stored struct {
				Token string `json:"token"`
			}
			if json.Unmarshal(data, &stored) == nil {
				actor = strings.TrimSpace(stored.Token)
			}
			if actor == "" {
				gatewayError(w, 401, "AMUX session credential unavailable")
				return
			}
		}
		ctx := metadata.AppendToOutgoingContext(r.Context(), "x-amux-actor-token", actor)
		if token := values["AMUX_HUB_TOKEN"]; token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "x-amux-hub-token", token)
		}
		headers := map[string]string{}
		for _, name := range []string{"User-Agent", "Anthropic-Version", "Anthropic-Beta", "X-Session-Id", "X-Amux-Reasoning-Effort"} {
			headers[name] = r.Header.Get(name)
		}
		headers["X-Amux-Reasoning-Effort"] = values["AMUX_MODEL_SERVICE_EFFORT"]
		if headers["X-Session-Id"] == "" {
			headers["X-Session-Id"] = values["AMUX_SESSION_ID"]
		}
		stream, err := client.ProxyModel(ctx, &pb.ProxyModelRequest{Service: service, Method: r.Method, Path: r.URL.Path, Headers: headers, Body: body})
		if err != nil {
			gatewayGRPCError(w, err)
			return
		}
		started := false
		for {
			chunk, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				if !started {
					gatewayGRPCError(w, err)
				} else {
					panic(http.ErrAbortHandler)
				}
				return
			}
			if !started {
				if chunk.Status < 100 || chunk.Status > 599 {
					gatewayError(w, 502, "invalid model response")
					return
				}
				for name, value := range chunk.Headers {
					w.Header().Set(name, value)
				}
				w.WriteHeader(int(chunk.Status))
				started = true
			}
			if len(chunk.Body) > 0 {
				if _, err = w.Write(chunk.Body); err != nil {
					return
				}
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	})
	go func() { _ = srv.Serve(lis) }()
	closeGateway = func() { _ = srv.Close(); _ = conn.Close() }
	return "http://" + lis.Addr().String() + "/v1", key, closeGateway, nil
}

// PassthroughHeaderAllowlist lists HTTP request headers forwarded across a
// transparent passthrough gateway to Hub ProxyModel.
var PassthroughHeaderAllowlist = []string{
	"Authorization",
	"X-Jwt-Token",
	"Content-Type",
	"Accept",
	"User-Agent",
	"Originator",
	"Version",
	"X-App-Id",
	"X-Ide-Function",
	"X-Ide-Version-Code",
	"X-Session-Id",
	"X-Request-Id",
	"X-Tt-Logid",
	"Anthropic-Version",
	"Anthropic-Beta",
}

// StartPassthroughGateway starts a loopback reverse-proxy gateway owned by the
// session broker for native clients that supply their own upstream
// Authorization header (rather than AMUX_ACTOR_TOKEN). The gateway binds to
// 127.0.0.1:0, authenticates to Hub with the broker's actor credential, and
// forwards raw HTTP path, headers, and body verbatim through ProxyModel.
func StartPassthroughGateway(env []string, service string) (baseURL string, closeGateway func(), err error) {
	values := map[string]string{}
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			values[k] = v
		}
	}
	key := values["AMUX_ACTOR_TOKEN"]
	if values["AMUX_HUB_ADDR"] == "" {
		return "", nil, fmt.Errorf("model service requires a Hub address")
	}
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, transport.GRPCClientDialOptions()...)
	conn, err := grpc.NewClient(values["AMUX_HUB_ADDR"], opts...)
	if err != nil {
		return "", nil, err
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		conn.Close()
		return "", nil, err
	}
	client := pb.NewAmuxdClient(conn)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			gatewayError(w, 403, "browser requests are not supported")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
		if err != nil {
			gatewayError(w, 413, "model request too large")
			return
		}
		actor := key
		if path := values["AMUX_ACTOR_TOKEN_FILE"]; path != "" {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				if key == "" && !os.IsNotExist(readErr) {
					gatewayError(w, 401, "AMUX session credential unavailable")
					return
				}
			} else {
				parsed := strings.TrimSpace(string(data))
				var stored struct {
					Token string `json:"token"`
				}
				if json.Unmarshal(data, &stored) == nil {
					parsed = strings.TrimSpace(stored.Token)
				}
				if parsed != "" {
					actor = parsed
				}
			}
		}
		ctx := r.Context()
		if actor != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "x-amux-actor-token", actor)
		}
		if token := values["AMUX_HUB_TOKEN"]; token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "x-amux-hub-token", token)
		}
		headers := map[string]string{}
		for _, name := range PassthroughHeaderAllowlist {
			if v := r.Header.Get(name); v != "" {
				headers[name] = v
			}
		}
		if headers["X-Session-Id"] == "" {
			headers["X-Session-Id"] = values["AMUX_SESSION_ID"]
		}
		reqPath := r.URL.Path
		if r.URL.RawQuery != "" {
			reqPath += "?" + r.URL.RawQuery
		}
		stream, err := client.ProxyModel(ctx, &pb.ProxyModelRequest{
			Service: service,
			Method:  r.Method,
			Path:    reqPath,
			Headers: headers,
			Body:    body,
		})
		if err != nil {
			gatewayGRPCError(w, err)
			return
		}
		started := false
		for {
			chunk, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				if !started {
					gatewayGRPCError(w, err)
				} else {
					panic(http.ErrAbortHandler)
				}
				return
			}
			if !started {
				if chunk.Status < 100 || chunk.Status > 599 {
					gatewayError(w, 502, "invalid model response")
					return
				}
				for name, value := range chunk.Headers {
					w.Header().Set(name, value)
				}
				w.WriteHeader(int(chunk.Status))
				started = true
			}
			if len(chunk.Body) > 0 {
				if _, err = w.Write(chunk.Body); err != nil {
					return
				}
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	})
	go func() { _ = srv.Serve(lis) }()
	closeGateway = func() { _ = srv.Close(); _ = conn.Close() }
	return "http://" + lis.Addr().String(), closeGateway, nil
}

func gatewayGRPCError(w http.ResponseWriter, err error) {
	code, key := http.StatusServiceUnavailable, "unavailable"
	switch status.Code(err) {
	case codes.Unauthenticated:
		code, key = http.StatusUnauthorized, "login_required"
	case codes.PermissionDenied:
		code, key = http.StatusForbidden, "access_denied"
	case codes.InvalidArgument:
		code, key = http.StatusBadRequest, "invalid_request"
	case codes.NotFound:
		code, key = http.StatusNotFound, "not_found"
	case codes.ResourceExhausted:
		code, key = http.StatusTooManyRequests, "rate_limited"
	case codes.DeadlineExceeded:
		code, key = http.StatusGatewayTimeout, "timed_out"
	}
	gatewayError(w, code, errorMessage(key))
}

func gatewayError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "model_service_error", "message": message}})
}
