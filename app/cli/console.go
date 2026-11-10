package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/consoleentry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/webconsole"
	"github.com/spf13/cobra"
)

var errConsoleResourceNotFound = webconsole.ErrNotFound

type consoleBackend struct {
	sessions        *sessionClientBackend
	hub             pb.AmuxdClient
	tasks           pb.AmuxTaskClient
	managedOperator bool // set only after verified direct Hub ancestry
	localMu         sync.Mutex
}

type consoleServeOptions struct {
	Addr, PublicURL string
	Duration        time.Duration
	PasswordStdin   bool
	ManagedByHub    bool
}

func newConsoleCmd() *cobra.Command {
	options := consoleServeOptions{}
	c := &cobra.Command{Use: "console", Short: commandCatalog.Format(commandLocale(nil), "console.short"), Args: cobra.NoArgs, ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error { return runConsoleWithOptions(cmd, options) }}
	c.Flags().StringVar(&options.Addr, "addr", "127.0.0.1:7892", commandCatalog.Format(commandLocale(nil), "console.addr"))
	c.Flags().StringVar(&options.PublicURL, "public-url", "", commandCatalog.Format(commandLocale(nil), "console.public_url"))
	c.Flags().DurationVar(&options.Duration, "duration", 30*time.Minute, commandCatalog.Format(commandLocale(nil), "console.duration"))
	c.Flags().BoolVar(&options.PasswordStdin, "password-stdin", false, commandCatalog.Format(commandLocale(nil), "console.password_stdin"))
	c.Flags().BoolVar(&options.ManagedByHub, "managed-by-hub", false, "")
	_ = c.Flags().MarkHidden("managed-by-hub")
	return c
}

func runConsole(cmd *cobra.Command, addr string) error {
	return runConsoleWithOptions(cmd, consoleServeOptions{Addr: addr, Duration: 30 * time.Minute})
}
func runConsoleWithOptions(cmd *cobra.Command, options consoleServeOptions) error {
	addr := options.Addr
	if err := loadUserConfig(); err != nil && !os.IsNotExist(err) {
		return err
	}
	managedOperator := options.ManagedByHub && consoleManagedParentVerified()
	if options.ManagedByHub && !managedOperator || consoleHasAgentContext() || !managedOperator && actorToken() != "" {
		return fmt.Errorf("%s", commandCatalog.Format(commandLocale(cmd), "console.operator_required"))
	}
	host, _, err := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil {
		return fmt.Errorf("%s", commandCatalog.Format(commandLocale(cmd), "console.listen_invalid"))
	}
	if options.Duration <= 0 || options.Duration > 30*time.Minute {
		return fmt.Errorf("%s", commandCatalog.Format(commandLocale(cmd), "console.duration_invalid"))
	}
	publicURL := options.PublicURL
	if publicURL == "" {
		publicURL = config.GetConsolePublicURL()
	}
	if publicURL != "" {
		publicURL, err = consoleentry.ValidateURL(publicURL)
		if err != nil {
			return fmt.Errorf("%s", commandCatalog.Format(commandLocale(cmd), "console.invalid_public_url"))
		}
	}
	if !ip.IsLoopback() && (!options.PasswordStdin || publicURL == "" || consoleentry.LocalURL(publicURL)) {
		return fmt.Errorf("%s", commandCatalog.Format(commandLocale(cmd), "console.remote_auth_required"))
	}
	password := ""
	if options.PasswordStdin {
		reader := bufio.NewReader(io.LimitReader(cmd.InOrStdin(), 4097))
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("%s", commandCatalog.Format(commandLocale(cmd), "console.password_invalid"))
		}
		password = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if len(password) < 16 || len(password) > 256 || strings.ContainsAny(password, "\x00\r\n\t ") {
			return fmt.Errorf("%s", commandCatalog.Format(commandLocale(cmd), "console.password_invalid"))
		}
	}
	var hc *hubClient
	if managedOperator {
		hc, err = newConsoleOperatorHub(5 * time.Second)
	} else {
		hc, err = newHubClientWithTimeout(hubAddr(), 5*time.Second)
	}
	if err != nil {
		return err
	}
	defer hc.Close()
	backend := &consoleBackend{sessions: &sessionClientBackend{sc: hc}, hub: hc.c, tasks: pb.NewAmuxTaskClient(hc.conn), managedOperator: managedOperator}
	network := "tcp6"
	if ip.To4() != nil {
		network = "tcp4"
	}
	listener, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	code := ""
	if password == "" {
		code, err = webconsole.RandomSecret()
		if err != nil {
			return err
		}
	}
	origin := publicURL
	if origin == "" {
		origin = "http://" + listener.Addr().String()
	}
	expiresAt := time.Now().Add(options.Duration).UTC()
	server, err := webconsole.New(backend, webconsole.Options{Version: version, Origin: origin, PairCode: code, Password: password, ExpiresAt: expiresAt, SessionTTL: options.Duration})
	password = ""
	if err != nil {
		return err
	}
	defer server.Close()
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10}
	readyInfo := map[string]any{"addr": listener.Addr().String(), "url": origin, "expires_at": expiresAt}
	if code != "" {
		readyInfo["pair_url"] = origin + "/#pair=" + code
		readyInfo["expires_in_seconds"] = 300
	}
	ready, _ := json.Marshal(readyInfo)
	fmt.Fprintf(cmd.OutOrStdout(), "AMUX_CONSOLE_READY %s\n", ready)
	if code != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), commandCatalog.Format(commandLocale(cmd), "console.ready", origin+"/#pair="+code))
	} else {
		fmt.Fprintln(cmd.ErrOrStderr(), commandCatalog.Format(commandLocale(cmd), "console.password_ready", origin))
	}
	cleanup := registerTrackedProcess(trackedProcessRoleGUI)
	defer cleanup()
	signalCtx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, expire := context.WithDeadline(signalCtx, expiresAt)
	defer expire()
	httpServer.BaseContext = func(net.Listener) context.Context { return ctx }
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(listener) }()
	select {
	case <-ctx.Done():
		server.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (b *consoleBackend) Snapshot(ctx context.Context, resource string, query url.Values) (any, error) {
	if query.Get("target") == "" && query.Get("target_id") != "" {
		copied := make(url.Values, len(query))
		for k, v := range query {
			copied[k] = append([]string(nil), v...)
		}
		query = copied
		query.Set("target", query.Get("target_id"))
	}
	switch resource {
	case "bots", "groups":
		return b.snapshotBridge(ctx, resource, query)
	case "settings":
		return b.snapshotSettings()
	}
	return b.snapshotResource(ctx, resource, query)
}
func (b *consoleBackend) Execute(ctx context.Context, resource, action string, data json.RawMessage) (any, error) {
	switch resource {
	case "bots", "groups":
		return b.executeBridge(ctx, resource, action, data)
	case "settings":
		return b.executeSettings(action, data)
	}
	return b.executeResource(ctx, resource, action, data)
}
func (b *consoleBackend) Attach(ctx context.Context, target, name string) (pb.Amuxlet_AttachClient, error) {
	return b.sessions.Attach(ctx, target, name)
}

// URL and numeric identity validation are shared by local management adapters.
func consolePositiveID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, webconsole.ErrInvalid
	}
	return id, nil
}
