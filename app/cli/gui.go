package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/amuxos/amux/internal/gateway"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

func newGuiCmd() *cobra.Command {
	c := &cobra.Command{
		Use:               "gui",
		Short:             "Desktop GUI gateway",
		ValidArgsFunction: completeNoFiles,
	}
	c.AddCommand(newGuiServeCmd())
	return c
}

func newGuiServeCmd() *cobra.Command {
	var addr, token string
	c := &cobra.Command{
		Use:   "serve",
		Short: "Serve the HTTP/WebSocket gateway for the desktop client",
		Long: "Exposes the amux session API over JSON (REST + WebSocket) on a loopback\n" +
			"TCP port so the desktop client can drive sessions. Prints a single\n" +
			"`AMUX_GUI_READY {json}` line to stdout once listening.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGuiServe(cmd, addr, token)
		},
	}
	c.Flags().StringVar(&addr, "addr", "127.0.0.1:0", "listen address (port 0 = OS-assigned)")
	c.Flags().StringVar(&token, "token", "", "bearer token required by clients (random if empty)")
	return c
}

func runGuiServe(cmd *cobra.Command, addr, token string) error {
	cleanup := registerTrackedProcess(trackedProcessRoleGUI)
	defer cleanup()

	if token == "" {
		token = randomToken()
	}

	_ = loadUserConfig()

	backend, closeBackend, err := newGatewayBackend()
	if err != nil {
		return err
	}
	defer closeBackend()

	srv := gateway.New(backend, token, version)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	httpSrv := &http.Server{Handler: srv.Handler()}

	// Announce readiness on stdout for the desktop launcher to parse.
	ready, _ := json.Marshal(map[string]string{"addr": lis.Addr().String(), "token": token})
	fmt.Fprintf(cmd.OutOrStdout(), "AMUX_GUI_READY %s\n", ready)
	fmt.Fprintf(cmd.ErrOrStderr(), "amux gui: listening on http://%s\n", lis.Addr().String())

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(lis) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is not expected; fall back to a constant so the
		// gateway still starts (loopback-only).
		return "amux-dev-token"
	}
	return hex.EncodeToString(b)
}

// ── Backend adapter: sessionClient -> gateway.Backend ────────────────────────

func newGatewayBackend() (gateway.Backend, func(), error) {
	sc, err := newSessionClient()
	if err != nil {
		return nil, nil, err
	}
	return &sessionClientBackend{sc: sc}, func() { _ = sc.Close() }, nil
}

type sessionClientBackend struct {
	sc sessionClient
}

type sourceSessionClient interface {
	SubmitFrom(ctx context.Context, targetID, name, text, source string) error
	SubmitAsyncFrom(ctx context.Context, targetID, name, text, source string) error
}

func (b *sessionClientBackend) List(ctx context.Context) ([]*gateway.SessionEntry, error) {
	entries, err := b.sc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*gateway.SessionEntry, len(entries))
	for i, e := range entries {
		sess := e.Session // copy; take address of the copy
		out[i] = &gateway.SessionEntry{
			Session:    &sess,
			TargetID:   e.TargetID,
			TargetName: e.TargetName,
		}
	}
	return out, nil
}

func (b *sessionClientBackend) Run(ctx context.Context, targetID string, req *pb.RunRequest) (*pb.RunResponse, error) {
	return b.sc.Run(ctx, targetID, req)
}
func (b *sessionClientBackend) Kill(ctx context.Context, targetID, name string) error {
	return b.sc.Kill(ctx, targetID, name)
}
func (b *sessionClientBackend) Restart(ctx context.Context, targetID, name string) error {
	return b.sc.Restart(ctx, targetID, name, false)
}
func (b *sessionClientBackend) Sleep(ctx context.Context, targetID, name string) error {
	return b.sc.Sleep(ctx, targetID, name)
}
func (b *sessionClientBackend) Resume(ctx context.Context, targetID, name string) error {
	return b.sc.Resume(ctx, targetID, name)
}
func (b *sessionClientBackend) Interrupt(ctx context.Context, targetID, name string) error {
	return b.sc.Interrupt(ctx, targetID, name)
}
func (b *sessionClientBackend) Attach(ctx context.Context, targetID, name string) (pb.Amuxlet_AttachClient, error) {
	return b.sc.Attach(ctx, targetID, name)
}
func (b *sessionClientBackend) Submit(ctx context.Context, targetID, name, source, text string, async bool) error {
	if sc, ok := b.sc.(sourceSessionClient); ok {
		if async {
			return sc.SubmitAsyncFrom(ctx, targetID, name, text, source)
		}
		return sc.SubmitFrom(ctx, targetID, name, text, source)
	}
	if async {
		return b.sc.SubmitAsync(ctx, targetID, name, text)
	}
	return b.sc.Submit(ctx, targetID, name, text)
}
func (b *sessionClientBackend) QueueList(ctx context.Context, targetID, name string) ([]*pb.QueueItem, error) {
	return b.sc.QueueList(ctx, targetID, name)
}
func (b *sessionClientBackend) QueueClear(ctx context.Context, targetID, name string) (int32, error) {
	return b.sc.QueueClear(ctx, targetID, name)
}
func (b *sessionClientBackend) QueueCancel(ctx context.Context, targetID, name, id string) error {
	return b.sc.QueueCancel(ctx, targetID, name, id)
}
func (b *sessionClientBackend) RespondPermission(ctx context.Context, targetID, name, permissionID, optionID string) error {
	return b.sc.RespondPermission(ctx, targetID, name, permissionID, optionID)
}

// Prune removes archived sessions from the selected runtime.
func (b *sessionClientBackend) Prune(ctx context.Context, targetID string) (int32, []string, error) {
	return b.sc.Prune(ctx, targetID)
}
