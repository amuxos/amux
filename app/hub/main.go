package hub

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/amuxos/amux/internal/agentinstance"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/executionauth"
	"github.com/amuxos/amux/internal/extensionstate"
	"github.com/amuxos/amux/internal/hubweb"
	"github.com/amuxos/amux/internal/memory"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/sessionlaunch"
	"github.com/amuxos/amux/internal/transport"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/tunnel"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var version = "dev"

const defaultHubListenAddr = "127.0.0.1:7100"

func Main(buildVersion string) {
	extensionstate.Freeze()
	version = buildVersion
	root := &cobra.Command{
		Use:     "amuxd",
		Short:   "Hub daemon for amux",
		Version: version,
	}
	root.SetVersionTemplate("amuxd {{.Version}}\n")
	root.AddCommand(newStartCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newStartCmd() *cobra.Command {
	var addr string
	var cfgFile string
	var webAddr string
	var webToken string
	var webBaseURL string

	c := &cobra.Command{
		Use:   "start",
		Short: "Start the amux hub daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config (ignore error — config file is optional).
			if cfgFile != "" {
				_ = config.Load(cfgFile)
			} else {
				_ = config.Load(amuxhome.ConfigPath())
			}

			if addr == "" {
				addr = defaultHubListenAddr
			}
			auth := config.GetHubAuth()
			if err := validateHubListenSecurity(addr, auth); err != nil {
				return err
			}
			if err := validateHubWebListenSecurity(webAddr, webToken); err != nil {
				return err
			}
			isolationCfg, err := config.GetIsolation()
			if err != nil {
				return fmt.Errorf("isolation config: %w", err)
			}

			lis, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", addr, err)
			}

			hub := tunnel.NewHub()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Distributed-task control plane: the scheduler places subtasks on
			// runtimes via reserve -> commit admission over the tunnel; the router
			// exposes the AmuxTask service and keeps the task->placement index; the
			// reconciler reclaims committed reservations for terminal/gone tasks.
			// The agent-instance store backs persistent agent sticky routing; a
			// failure to open it is non-fatal (persistent tasks degrade to
			// non-sticky placement, still correct).
			taskSched := registry.NewScheduler(hub.Registry(), newTunnelReserver(hub))
			taskIdx := newTaskIndex()
			var agentInstanceStore *agentinstance.Store
			if store, err := agentinstance.OpenStore(agentinstance.DefaultPath()); err != nil {
				fmt.Fprintf(os.Stderr, "hub: init agent instance store: %v\n", err)
			} else {
				agentInstanceStore = store
				defer store.Close()
			}
			principalStore, err := principal.OpenStore(principal.DefaultPath())
			if err != nil {
				return fmt.Errorf("init principal registry: %w", err)
			}
			defer principalStore.Close()
			var memoryStore *memory.Store
			if config.GetMemory().Enabled {
				memoryStore, err = memory.New(filepath.Join(amuxhome.Dir(), "memory"))
				if err != nil {
					fmt.Fprintf(os.Stderr, "hub: init memory store: %v\n", err)
				}
			}
			// Launch credentials outlive the sessions they were minted for when
			// a host dies mid-session; issuing sweeps too, so this only matters
			// for a daemon that stops launching anything.
			if _, err := principalStore.PruneExpiredAgentCredentials(); err != nil {
				fmt.Fprintf(os.Stderr, "amuxd: prune expired agent credentials: %v\n", err)
			}
			taskRouter := newTaskRouter(hub, taskSched, taskIdx, agentInstanceStore)
			taskRouter.principals = principalStore
			taskRouter.parentNotify = newPersistentParentNotifier(filepath.Join(amuxhome.Dir(), "hub", "parent-notify.json"))
			taskRouter.resumeParentNotify()
			taskRouter.isolationGlobalEnv = config.ActiveIsolationGlobalEnv(isolationCfg)
			subtaskReader := &bridgeSubtaskReader{router: taskRouter}
			go newTaskReconciler(hub, taskSched, taskIdx).Run(ctx)
			if webAddr != "" {
				webLis, err := net.Listen("tcp", webAddr)
				if err != nil {
					return fmt.Errorf("listen web %s: %w", webAddr, err)
				}
				webSrv := &http.Server{
					Handler: hubweb.New(hubWebBackend{hub: hub, subtasks: subtaskReader}, webToken, version).Handler(),
				}
				go func() {
					<-ctx.Done()
					shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
					defer shutdownCancel()
					_ = webSrv.Shutdown(shutdownCtx)
				}()
				go func() {
					if err := webSrv.Serve(webLis); err != nil && err != http.ErrServerClosed {
						fmt.Fprintf(os.Stderr, "hub web: %v\n", err)
					}
				}()
				fmt.Fprintln(os.Stderr, hubWebListeningMessage(webAddr))
			}

			// Enforce registration auth only when configured (open otherwise),
			// preserving existing local/hub setups.
			if auth.SharedToken != "" || len(auth.AllowedTargets) > 0 {
				hub.SetAuthorizer(registry.NewAuthorizer(registry.AuthorizerConfig{
					SharedToken:    auth.SharedToken,
					AllowedTargets: auth.AllowedTargets,
				}))
			}
			if isDistributedHubAddr(addr) && len(auth.AllowedTargets) == 0 {
				fmt.Fprintln(os.Stderr, "hub: warning: distributed listen address without hub.allowed_targets; set an allowlist to restrict runtime registration")
			}

			sessionStarter := sessionlaunch.New(hub)

			// Open the schedule store and start the background scheduler. A
			// failure here is non-fatal: the hub still serves CLI/bridge.
			var scheduleStore *schedule.Store
			if store, err := schedule.OpenStore(schedule.DefaultSchedulePath()); err != nil {
				fmt.Fprintf(os.Stderr, "hub: init scheduler: %v\n", err)
			} else {
				scheduleStore = store
				defer store.Close()
			}

			startedAt := time.Now()
			localExpertAuthoring, err := newLocalExpertAuthoringControl(agentprofile.NewDefaultManagedLibrary())
			if err != nil {
				return err
			}
			go localExpertAuthoring.runEvaluationReconciler(ctx)
			managementActions := newLocalManagementSystemActions()
			go managementActions.RunUpdateMonitor(ctx)
			consoleActions := newLocalManagementConsoleActions(ctx, detectHubIP(lis.Addr()))
			defer consoleActions.Close(context.Background())
			scheduleControl := &bridgeSchedules{server: &hubServer{hub: hub, listTargets: hub.ListTargets, schedule: scheduleStore, principals: principalStore, scheduleRuntime: hub, scheduleTasks: subtaskReader, isolationConfig: config.GetIsolation}, reader: subtaskReader}
			bridges := tryStartBridges(ctx, hub, sessionStarter, subtaskReader, principalStore, scheduleControl, webBaseURL, webToken, managementActions, consoleActions, bridge.ManagementSystemInfo{
				Version:   version,
				StartedAt: startedAt,
				HubIP:     detectHubIP(lis.Addr()),
			})
			var coordinationRT *coordinationRuntime
			if runtime, err := newProductionCoordinationRuntime(ctx, taskRouter, bridges); err != nil {
				fmt.Fprintf(os.Stderr, "hub: init coordination: %v\n", err)
			} else {
				coordinationRT = runtime
				defer runtime.Close()
			}
			feishuBotSessionControllers := make(map[string]feishuBotSessionController, len(bridges))
			consoleManagementControllers := make(map[string]consoleManagementController, len(bridges))
			var feishuMessageSenders []feishuMessageSender
			for _, b := range bridges {
				feishuBotSessionControllers[b.ProfileName()] = b
				consoleManagementControllers[b.ProfileName()] = b
				feishuMessageSenders = append(feishuMessageSenders, b)
			}
			// Register a session event handler that (1) reclaims persistent agent
			// instances whose owner session has archived — the instance follows the
			// owner lifecycle (v0.39) — and (2) pushes DM notifications to bridges.
			// The reclaim path is always wired, independent of whether any bridge
			// is running, so orphaned instances are reclaimed even without Feishu.
			hub.SetSessionEventHandler(func(ev tunnel.SessionEvent) {
				for _, s := range ev.Archived {
					taskRouter.ReclaimInstancesForOwner(s.Id)
				}
				for _, b := range bridges {
					for _, s := range ev.Added {
						b.OnSessionAdded(ev.TargetID, ev.TargetName, s)
					}
				}
			})

			serverOpts := append(transport.GRPCServerOptions(),
				grpc.UnaryInterceptor(hubUnaryAuthInterceptor(auth.SharedToken)),
				grpc.StreamInterceptor(hubStreamAuthInterceptor(auth.SharedToken)),
			)
			hubRPC := &hubServer{
				hub:                          hub,
				listTargets:                  hub.ListTargets,
				feishuBotSessionControllers:  feishuBotSessionControllers,
				consoleManagementControllers: consoleManagementControllers,
				feishuMessageSenders:         feishuMessageSenders,
				schedule:                     scheduleStore,
				scheduleRuntime:              hub,
				scheduleTasks:                subtaskReader,
				principals:                   principalStore,
				memory:                       memoryStore,
				memoryConfig:                 config.GetMemory,
				version:                      version,
				startedAt:                    startedAt,
				localExpertAuthoring:         localExpertAuthoring,
				isolationConfig:              config.GetIsolation,
			}
			hubRPC.dailyCleaner = newDailyCleaner(hubRPC, bridges)
			go hubRPC.dailyCleaner.Run(ctx)
			if scheduleStore != nil {
				scheduler := schedule.NewWithTaskSubmitter(hub, scheduleStore, sessionStarter, taskRouter)
				scheduler.SetSessionPromptAuthorizer(hubRPC.authorizeScheduledSessionPrompt)
				scheduler.SetOutcomeReader(scheduleControl)
				go scheduler.Start(ctx)
			}
			srv := grpc.NewServer(serverOpts...)
			pb.RegisterAmuxdServer(srv, hubRPC)
			pb.RegisterAmuxTaskServer(srv, taskRouter)
			if coordinationRT != nil {
				pb.RegisterCoordinationServiceServer(srv, coordinationRT.server)
			}

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				fmt.Fprintln(os.Stderr, hubShuttingDownMessage())
				cancel()
				shutdownServer(srv, shutdownGracePeriod)
			}()

			fmt.Fprintln(os.Stderr, hubListeningMessage(addr))
			return srv.Serve(lis)
		},
	}
	c.Flags().StringVar(&addr, "addr", "", "TCP listen address (default 127.0.0.1:7100)")
	c.Flags().StringVar(&cfgFile, "config", "", "config file path")
	c.Flags().StringVar(&webAddr, "web-addr", "", "optional read-only web listener address")
	c.Flags().StringVar(&webToken, "web-token", "", "read-only admin token and signing secret for session-scoped viewer links")
	c.Flags().StringVar(&webBaseURL, "web-base-url", "", "public base URL used in Feishu session output links")
	return c
}

func validateHubListenSecurity(addr string, auth config.HubAuthConfig) error {
	if !isDistributedHubAddr(addr) {
		return nil
	}
	if auth.SharedToken == "" {
		return fmt.Errorf("distributed hub listen address %s requires hub.token; set `amux config set hub.token <secret>` or bind to %s for local-only use", addr, defaultHubListenAddr)
	}
	return nil
}

func validateHubWebListenSecurity(addr, token string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" || !isDistributedHubAddr(addr) {
		return nil
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("distributed hub web listen address %s requires --web-token; bind to 127.0.0.1 for local-only use or pass a read-only admin token/signing secret", addr)
	}
	return nil
}

func hubUnaryAuthInterceptor(token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := authorizeHubRPC(ctx, token); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func hubStreamAuthInterceptor(token string) grpc.StreamServerInterceptor {
	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := authorizeHubRPC(stream.Context(), token); err != nil {
			return err
		}
		return handler(srv, stream)
	}
}

func authorizeHubRPC(ctx context.Context, want string) error {
	if want == "" {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(incomingHubToken(ctx)), []byte(want)) == 1 {
		return nil
	}
	return status.Error(codes.Unauthenticated, "invalid or missing hub.token")
}

func incomingHubToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, value := range md.Get("x-amux-hub-token") {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	for _, value := range md.Get("authorization") {
		if token, ok := strings.CutPrefix(value, "Bearer "); ok {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

func isDistributedHubAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return true
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// tryStartBridges loads all configured Feishu bot profiles and starts one
// bridge instance per valid profile. Missing credentials are treated as absent.
func tryStartBridges(
	ctx context.Context,
	hub *tunnel.Hub,
	sessionStarter sessionlaunch.Starter,
	subtasks bridge.SubtaskReader,
	principals *principal.Store,
	schedules *bridgeSchedules,
	webBaseURL,
	webToken string,
	managementActions bridge.ManagementSystemActions,
	consoleActions bridge.ManagementConsoleActions,
	systemInfo bridge.ManagementSystemInfo,
) []*bridge.Bridge {
	profiles, err := bridge.ListSecretProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hub: list lark profiles: %v\n", err)
		return nil
	}
	type pendingBridge struct {
		profile string
		appID   string
		bridge  *bridge.Bridge
	}
	packActions := newLocalManagementAgentPackActions(agentprofile.NewDefaultPackCatalogManager(), startLocalAgentPackAutoSync)
	identifiedFeishuLaunches := false
	if isolationCfg, cfgErr := config.GetIsolation(); cfgErr == nil {
		identifiedFeishuLaunches = isolationCfg.Enabled
	}
	var pending []pendingBridge
	for _, profile := range profiles {
		secrets, err := bridge.LoadProfileSecrets(profile)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "hub: load lark profile %s: %v\n", profile, err)
			}
			continue
		}
		if msg := hubBridgeMissingCredentialsMessage(profile, secrets); msg != "" {
			// A profile file without credentials cannot start a Bridge. Say so
			// instead of silently running without the bot.
			fmt.Fprintln(os.Stderr, msg)
			continue
		}

		b, err := bridge.NewWithProfile(hub, profile, secrets, sessionStarter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hub: init lark bridge profile %s: %v\n", profile, err)
			continue
		}
		b.SetSubtaskReader(subtasks)
		if schedules != nil && schedules.server.schedule != nil {
			b.SetScheduleControl(schedules)
		}
		if principals != nil {
			bridgeID := bridge.BridgeIdFromAppId(secrets.AppId)
			b.SetExecutionIdentityResolver(newFeishuLaunchIdentityResolver(principals, bridgeID, profile, identifiedFeishuLaunches))
			b.SetModelServicePrincipalResolver(func(openID string) (string, error) {
				actor, err := resolveFeishuLaunchActor(principals, bridgeID, openID, true)
				return actor.ActorPrincipalID, err
			})
		}
		b.SetSessionViewer(webBaseURL, webToken)
		b.SetManagementSystemActions(managementActions)
		b.SetManagementConsoleActions(consoleActions)
		pending = append(pending, pendingBridge{profile: profile, appID: secrets.AppId, bridge: b})
	}
	if len(pending) > 0 {
		prepareCtx, prepareCancel := context.WithTimeout(ctx, 10*time.Second)
		if err := packActions.PrepareExisting(prepareCtx); err != nil {
			fmt.Fprintf(os.Stderr, "hub: prepare Agent Pack auto sync: %v\n", err)
		}
		prepareCancel()
	}
	for _, item := range pending {
		item.bridge.SetManagementAgentPackActions(packActions)
	}

	out := make([]*bridge.Bridge, 0, len(pending))
	systemInfo.BotCount = len(pending)
	for _, item := range pending {
		item.bridge.SetManagementSystemInfo(systemInfo)
		go func(profile string, b *bridge.Bridge) {
			if err := b.Start(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "hub: lark bridge profile %s error: %v\n", profile, err)
			}
		}(item.profile, item.bridge)

		fmt.Fprintln(os.Stderr, hubBridgeStartingMessage(item.profile, item.appID))
		out = append(out, item.bridge)
	}
	return out
}

func newFeishuLaunchIdentityResolver(
	principals *principal.Store,
	bridgeID,
	profile string,
	identified bool,
) func(openID, executionRef, targetID string, shared bool) (bridge.LaunchIdentity, error) {
	return func(openID, executionRef, targetID string, shared bool) (bridge.LaunchIdentity, error) {
		credentialSubject, err := feishuSessionCredentialSubject(targetID, executionRef)
		if err != nil {
			return bridge.LaunchIdentity{}, err
		}
		if !identified {
			actor, err := resolveFeishuLaunchActor(principals, bridgeID, openID, true)
			if err != nil {
				fmt.Fprintf(os.Stderr, "bridge: local expert authoring credential unavailable for %s: %v\n", openID, err)
				return bridge.LaunchIdentity{}, nil
			}
			launchID := "launch-" + uuid.NewString()
			token, expiresAt := mintAgentActorTokenForPrincipal(principals, actor.ActorPrincipalID, credentialSubject)
			return bridge.LaunchIdentity{
				LaunchID: launchID, ActorToken: token, ActorTokenExpiresAt: expiresAt,
			}, nil
		}
		actor, err := resolveFeishuLaunchActor(principals, bridgeID, openID, !shared)
		if err != nil {
			// An unbound sender is the expected first-run state, and the operator
			// cannot guess the scope/subject to bind from a bare "alias not
			// found". Name the exact coordinates so the fix is self-service.
			if errors.Is(err, principal.ErrAliasNotFound) {
				return bridge.LaunchIdentity{}, fmt.Errorf(
					"%w; bind this sender with: amux isolation alias bind --provider feishu --scope %q --subject %q --principal <principal-id>",
					err, bridgeID, openID)
			}
			return bridge.LaunchIdentity{}, err
		}
		launchID := "launch-" + uuid.NewString()
		runAs := ""
		if shared {
			runAs = "service:" + profile
		}
		identityResult := executionauth.New(principals).FreezeSessionIdentity(executionauth.FreezeSessionRequest{
			Actor:             &actor,
			Source:            principal.SourceFeishu,
			RunAs:             runAs,
			ExecutionStateRef: executionRef,
		})
		if !identityResult.Decision.Allowed {
			if shared {
				return bridge.LaunchIdentity{}, fmt.Errorf(
					"singleton requires service principal alias %q and active sender membership: %w",
					runAs, identityResult.Decision.Err,
				)
			}
			return bridge.LaunchIdentity{}, identityResult.Decision.Err
		}
		identity := identityResult.Identity
		effective, err := principals.Get(identity.EffectivePrincipalID)
		if err != nil {
			return bridge.LaunchIdentity{}, err
		}
		if effective.EnvironmentTargetID != "" && effective.EnvironmentTargetID != strings.TrimSpace(targetID) {
			return bridge.LaunchIdentity{}, fmt.Errorf(
				"principal environment is fixed to target %s", effective.EnvironmentTargetID,
			)
		}
		// A Feishu launch is routed straight at the runtime, so this is the only
		// place its agent credential can be minted. Without one the agent's own
		// calls back into the hub arrive unauthenticated, and anything it creates
		// — a sibling session, a scheduled subtask — has no principal to run as.
		token, expiresAt := mintAgentActorToken(principals, identity, credentialSubject)
		return bridge.LaunchIdentity{
			Identity:            principal.ToProtoIdentity(identity),
			LaunchID:            launchID,
			ActorToken:          token,
			ActorTokenExpiresAt: expiresAt,
		}, nil
	}
}

func feishuSessionCredentialSubject(targetID, executionRef string) (string, error) {
	kind, name, found := strings.Cut(strings.TrimSpace(executionRef), ":")
	if !found || strings.TrimSpace(name) == "" || (kind != "feishu-session" && kind != "feishu-singleton") {
		return "", fmt.Errorf("invalid Feishu session execution reference %q", executionRef)
	}
	subject := sessionAgentCredentialSubject(targetID, name)
	if subject == "" {
		return "", fmt.Errorf("Feishu session credential requires target and session name")
	}
	return subject, nil
}

func resolveFeishuLaunchActor(
	principals *principal.Store, bridgeID, openID string, autoEnroll bool,
) (principal.ActorContext, error) {
	actor, err := principals.ResolveActor(
		principal.ProviderFeishu, bridgeID, openID, bridgeID, uuid.NewString(), principal.SourceFeishu,
	)
	if !errors.Is(err, principal.ErrAliasNotFound) || !autoEnroll {
		return actor, err
	}
	created, provisionErr := principals.EnsureAliasPrincipal(principal.Alias{
		Provider: principal.ProviderFeishu, Scope: bridgeID, Subject: openID,
	}, openID)
	if provisionErr != nil {
		return principal.ActorContext{}, fmt.Errorf("auto-enroll feishu sender %s: %w", openID, provisionErr)
	}
	fmt.Fprintf(os.Stderr, "bridge: auto-enrolled feishu sender %s as principal %s\n", openID, created.ID)
	return principals.ResolveActor(
		principal.ProviderFeishu, bridgeID, openID, bridgeID, uuid.NewString(), principal.SourceFeishu,
	)
}

func detectHubIP(listenerAddress net.Addr) string {
	candidates := make([]net.IP, 0, 4)
	if routed := routedHubIP(); routed != nil {
		candidates = append(candidates, routed)
	}
	candidates = append(candidates, interfaceHubIPs()...)
	if listenerAddress == nil {
		return selectHubIP("", candidates...)
	}
	return selectHubIP(listenerAddress.String(), candidates...)
}

func selectHubIP(listenerAddress string, candidates ...net.IP) string {
	if host, _, err := net.SplitHostPort(strings.TrimSpace(listenerAddress)); err == nil {
		if ip := net.ParseIP(host); usableHubIP(ip) {
			return ip.String()
		}
	}
	for _, candidate := range candidates {
		if usableHubIP(candidate) {
			return candidate.String()
		}
	}
	return ""
}

func routedHubIP() net.IP {
	conn, err := net.Dial("udp4", "192.0.2.1:80")
	if err != nil {
		return nil
	}
	defer conn.Close()
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

func interfaceHubIPs() []net.IP {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ipv4, ipv6 []net.IP
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || !usableHubIP(ip) {
				continue
			}
			if ip.To4() != nil {
				ipv4 = append(ipv4, ip)
			} else {
				ipv6 = append(ipv6, ip)
			}
		}
	}
	return append(ipv4, ipv6...)
}

func usableHubIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsUnspecified()
}

func hubListeningMessage(addr string) string {
	return fmt.Sprintf("hub listening on %s", addr)
}

func hubWebListeningMessage(addr string) string {
	return fmt.Sprintf("hub web listening on %s", addr)
}

func hubShuttingDownMessage() string {
	return "hub: shutting down"
}

func hubBridgeStartingMessage(profile, appID string) string {
	return "hub: lark bridge starting (profile=" + profile + ", app_id=" + appID + ")"
}

// hubBridgeMissingCredentialsMessage returns the warning logged when a Feishu
// profile is skipped because app_id or app_secret is empty, or "" when the
// profile has both.
func hubBridgeMissingCredentialsMessage(profile string, secrets *bridge.Secrets) string {
	var missing []string
	if secrets == nil || secrets.AppId == "" {
		missing = append(missing, "app_id")
	}
	if secrets == nil || secrets.AppSecret == "" {
		missing = append(missing, "app_secret")
	}
	if len(missing) == 0 {
		return ""
	}
	location := ""
	if path, err := bridge.ProfileSecretsPath(profile); err == nil {
		location = " in " + path
	}
	return "hub: lark bridge profile " + profile + " skipped: empty " + strings.Join(missing, ", ") + location
}

// shutdownGracePeriod bounds how long SIGTERM waits for in-flight RPCs and
// streams to drain before forcing a hard stop. The hub holds long-lived streams
// (reverse tunnels from amuxlets, attach streams) that never close on their own,
// so an unbounded GracefulStop would hang and keep the listen port held — the
// CLI would then SIGKILL the daemon, leaving streams torn mid-flight.
const shutdownGracePeriod = 3 * time.Second

// grpcServer is the subset of *grpc.Server shutdownServer needs, so the
// graceful-then-hard sequencing can be unit-tested with a fake.
type grpcServer interface {
	GracefulStop()
	Stop()
}

// shutdownServer drains the server gracefully, but falls back to a hard Stop if
// GracefulStop has not returned within grace. This guarantees the listen port is
// released promptly on SIGTERM instead of hanging on long-lived streams.
func shutdownServer(srv grpcServer, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		srv.Stop()
		<-done
	}
}
