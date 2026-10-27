package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/distribution"
	"github.com/amuxos/amux/internal/extensionstate"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/mux"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessionfiles"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
	"github.com/amuxos/amux/internal/transport"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/tunnel"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

var version = "dev"

var reconcileDevelopmentLeases = agentprofile.ReconcileDevelopmentLeases

const developmentLeaseSweepInterval = time.Minute

func Main(buildVersion string) {
	extensionstate.Freeze()
	version = buildVersion
	root := &cobra.Command{
		Use:     "amuxlet",
		Short:   "Runtime daemon for amux",
		Version: version,
		RunE:    runServer,
	}

	root.Flags().String("socket", "", "unix socket path (default: ~/.amux/runtime.sock)")
	root.Flags().String("db", "", "SQLite database path (default: ~/.amux/runtime.db)")

	root.SetVersionTemplate("amuxlet {{.Version}}\n")

	acpBrokerCmd := &cobra.Command{
		Use:    "_acp_broker",
		Hidden: true,
		RunE:   runACPBroker,
	}
	acpBrokerCmd.Flags().String("socket", "", "broker unix socket path")
	acpBrokerCmd.Flags().String("pid-file", "", "file to write agent PID into")
	acpBrokerCmd.Flags().String("cwd", "", "Acp working directory")
	acpBrokerCmd.Flags().String("agent-type", "", executionMessage("acp_agent_type"))
	acpBrokerCmd.Flags().String("model", "", executionMessage("acp_model"))
	acpBrokerCmd.Flags().String("reasoning-effort", "", executionMessage("acp_reasoning_effort"))
	acpBrokerCmd.Flags().Int("turn-idle-timeout-seconds", -1, executionMessage("turn_idle"))
	acpBrokerCmd.Flags().Int("turn-hard-cap-seconds", -1, executionMessage("turn_hard_cap"))
	acpBrokerCmd.Flags().Int("tool-idle-timeout-seconds", -1, executionMessage("tool_idle"))
	acpBrokerCmd.Flags().Int("repeat-tool-call-limit", -1, executionMessage("repeat_tool"))
	acpBrokerCmd.Flags().String("resume-session-id", "", "Acp session id to resume or load (if supported)")
	acpBrokerCmd.Flags().String("resume-session-id-file", "", "file containing Acp session id to resume or load (if supported)")
	acpBrokerCmd.Flags().String("launch-context-file", "", "one-shot resolved launch context file")
	acpBrokerCmd.Flags().Duration("prompt-tail-drain", 0, "quiet window for late Acp prompt events")
	acpBrokerCmd.Flags().StringArray("env", nil, "env vars for agent (KEY=VAL)")
	root.AddCommand(acpBrokerCmd)

	structuredBrokerCmd := &cobra.Command{
		Use:    "_structured_broker",
		Hidden: true,
		RunE:   runStructuredBroker,
	}
	structuredBrokerCmd.Flags().String("provider", "", "structured provider id")
	structuredBrokerCmd.Flags().String("session-id", "", "amux session id")
	structuredBrokerCmd.Flags().String("resume-session-id", "", "provider session id to resume (if supported)")
	structuredBrokerCmd.Flags().String("resume-session-id-file", "", "file containing provider session id to resume (if supported)")
	structuredBrokerCmd.Flags().String("resume-model", "", "model override for a resumed provider conversation")
	structuredBrokerCmd.Flags().String("resume-reasoning-effort", "", "reasoning effort override for a resumed provider conversation")
	structuredBrokerCmd.Flags().String("launch-context-file", "", "one-shot resolved launch context file")
	structuredBrokerCmd.Flags().String("socket", "", "broker unix socket path")
	structuredBrokerCmd.Flags().String("pid-file", "", "file to write agent PID into")
	structuredBrokerCmd.Flags().String("cwd", "", "provider working directory")
	structuredBrokerCmd.Flags().StringArray("env", nil, "env vars for provider (KEY=VAL)")
	structuredBrokerCmd.Flags().StringArray("excluded-skill-root", nil, "skill root excluded from provider discovery")
	structuredBrokerCmd.Flags().Int("turn-idle-timeout-seconds", -1, "turn idle watchdog timeout in seconds (-1=default, 0=disabled)")
	structuredBrokerCmd.Flags().Int("turn-hard-cap-seconds", -1, "turn hard-cap watchdog timeout in seconds (-1=default, 0=disabled)")
	structuredBrokerCmd.Flags().Int("tool-idle-timeout-seconds", -1, "tool idle watchdog timeout in seconds (-1=default, 0=disabled)")
	structuredBrokerCmd.Flags().Int("repeat-tool-call-limit", -1, "fail a turn after N consecutive identical tool calls (-1=default, 0=disabled)")
	structuredBrokerCmd.Flags().String("responses-chat-upstream-base-url", "", "Chat Completions upstream base URL")
	structuredBrokerCmd.Flags().String("responses-chat-api-key-env", "", "environment variable containing the upstream API key")
	structuredBrokerCmd.Flags().String("responses-chat-credential-file", "", "one-shot file containing the upstream API key")
	root.AddCommand(structuredBrokerCmd)

	addDistributionCommands(root)

	materializeCmd := &cobra.Command{
		Use:    "_isolation-materialize <workspace> <run> <home>",
		Hidden: true,
		Args:   cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			return isolation.ApplyMaterializeBundle(os.Stdin, args[0], args[1], args[2])
		},
	}
	root.AddCommand(materializeCmd)
	root.AddCommand(&cobra.Command{
		Use: "_session-file <workspace> <action> <name>", Hidden: true, Args: cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			return sessionfiles.Exchange(args[0], args[1], args[2], os.Stdin, os.Stdout)
		},
	})

	root.AddCommand(&cobra.Command{
		Use: "_read-home-files <home> <relative-file>...", Hidden: true, Args: cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return isolation.ReadHomeFilesWorker(args[0], args[1:], os.Stdout)
		},
	})

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runACPBroker(cmd *cobra.Command, args []string) error {
	sockPath, _ := cmd.Flags().GetString("socket")
	pidFile, _ := cmd.Flags().GetString("pid-file")
	cwd, _ := cmd.Flags().GetString("cwd")
	agentType, _ := cmd.Flags().GetString("agent-type")
	model, _ := cmd.Flags().GetString("model")
	reasoningEffort, _ := cmd.Flags().GetString("reasoning-effort")
	turnIdle, _ := cmd.Flags().GetInt("turn-idle-timeout-seconds")
	turnHardCap, _ := cmd.Flags().GetInt("turn-hard-cap-seconds")
	toolIdle, _ := cmd.Flags().GetInt("tool-idle-timeout-seconds")
	repeatTool, _ := cmd.Flags().GetInt("repeat-tool-call-limit")
	resumeID, _ := cmd.Flags().GetString("resume-session-id")
	resumeIDFile, _ := cmd.Flags().GetString("resume-session-id-file")
	launchContextFile, _ := cmd.Flags().GetString("launch-context-file")
	promptTailDrain, _ := cmd.Flags().GetDuration("prompt-tail-drain")
	envVars, _ := cmd.Flags().GetStringArray("env")
	if sockPath == "" || pidFile == "" || len(args) == 0 {
		return fmt.Errorf("usage: amuxlet _acp_broker --socket S --pid-file P --cwd C -- cmd [args...]")
	}
	var err error
	resumeID, err = readResumeSessionID(resumeID, resumeIDFile)
	if err != nil {
		return err
	}
	if cwd == "" {
		cwd = mustGetwd()
	}
	launchContext, err := mux.ReadBrokerLaunchContextFile(launchContextFile)
	if err != nil {
		return err
	}
	backend, isolationRuntime, err := brokerLaunchBackend(launchContext)
	if err != nil {
		return err
	}
	defer isolationRuntime.Close()
	agentCmd := exec.Command(args[0], args[1:]...)
	agentCmd.Dir = cwd
	if len(envVars) > 0 {
		agentCmd.Env = envVars
	}
	return mux.RunACPBrokerWithOptions(sockPath, pidFile, mux.ACPBrokerOptions{
		Cwd:             cwd,
		Cmd:             agentCmd,
		AgentType:       agentType,
		Model:           model,
		ReasoningEffort: reasoningEffort,
		Watchdog: &config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: turnIdle,
			TurnHardCapSeconds:     turnHardCap,
			ToolIdleTimeoutSeconds: toolIdle,
			RepeatToolCallLimit:    repeatTool,
		},
		ResumeSessionID: resumeID,
		PromptTailDrain: promptTailDrain,
		LaunchContext:   launchContext,
		Backend:         backend,
	})
}

func readResumeSessionID(raw, filePath string) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return strings.TrimSpace(raw), nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read resume session id file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func runStructuredBroker(cmd *cobra.Command, args []string) error {
	sockPath, _ := cmd.Flags().GetString("socket")
	pidFile, _ := cmd.Flags().GetString("pid-file")
	providerID, _ := cmd.Flags().GetString("provider")
	sessionID, _ := cmd.Flags().GetString("session-id")
	resumeID, _ := cmd.Flags().GetString("resume-session-id")
	resumeIDFile, _ := cmd.Flags().GetString("resume-session-id-file")
	resumeModel, _ := cmd.Flags().GetString("resume-model")
	resumeReasoningEffort, _ := cmd.Flags().GetString("resume-reasoning-effort")
	launchContextFile, _ := cmd.Flags().GetString("launch-context-file")
	cwd, _ := cmd.Flags().GetString("cwd")
	envVars, _ := cmd.Flags().GetStringArray("env")
	excludedSkillRoots, _ := cmd.Flags().GetStringArray("excluded-skill-root")
	turnIdleTimeout, _ := cmd.Flags().GetInt("turn-idle-timeout-seconds")
	turnHardCap, _ := cmd.Flags().GetInt("turn-hard-cap-seconds")
	toolIdleTimeout, _ := cmd.Flags().GetInt("tool-idle-timeout-seconds")
	repeatToolCallLimit, _ := cmd.Flags().GetInt("repeat-tool-call-limit")
	responsesChatUpstream, _ := cmd.Flags().GetString("responses-chat-upstream-base-url")
	responsesChatAPIKeyEnv, _ := cmd.Flags().GetString("responses-chat-api-key-env")
	responsesChatCredentialFile, _ := cmd.Flags().GetString("responses-chat-credential-file")
	if sockPath == "" || pidFile == "" || providerID == "" || sessionID == "" {
		return fmt.Errorf("usage: amuxlet _structured_broker --provider P --session-id ID --socket S --pid-file P --cwd C")
	}
	var err error
	resumeID, err = readResumeSessionID(resumeID, resumeIDFile)
	if err != nil {
		return err
	}
	if cwd == "" {
		cwd = mustGetwd()
	}
	launchContext, err := mux.ReadBrokerLaunchContextFile(launchContextFile)
	if err != nil {
		return err
	}
	backend, isolationRuntime, err := brokerLaunchBackend(launchContext)
	if err != nil {
		return err
	}
	defer isolationRuntime.Close()
	opts := mux.StructuredLaunchOptions{
		Provider:               providerID,
		SessionID:              sessionID,
		ResumeSessionID:        resumeID,
		ResumeModel:            resumeModel,
		ResumeReasoningEffort:  resumeReasoningEffort,
		Cwd:                    cwd,
		Env:                    envVars,
		ExtraArgs:              args,
		ExcludedSkillRoots:     excludedSkillRoots,
		TurnIdleTimeoutSeconds: turnIdleTimeout,
		TurnHardCapSeconds:     turnHardCap,
		ToolIdleTimeoutSeconds: toolIdleTimeout,
		RepeatToolCallLimit:    repeatToolCallLimit,
		LaunchContext:          launchContext,
		Backend:                backend,
	}
	if responsesChatUpstream != "" || responsesChatAPIKeyEnv != "" {
		if err := distribution.RequireResponsesChat(); err != nil {
			return err
		}
		if responsesChatUpstream == "" || responsesChatAPIKeyEnv == "" {
			return fmt.Errorf("responses-chat upstream base URL and API key env must be set together")
		}
		credential, err := readOneShotSecretFile(responsesChatCredentialFile)
		if err != nil {
			return err
		}
		envVars = append(envVars, responsesChatAPIKeyEnv+"="+credential)
		opts.Env = envVars
		opts.ResponsesChat = &mux.ResponsesChatOptions{UpstreamBaseURL: responsesChatUpstream, APIKeyEnv: responsesChatAPIKeyEnv}
	}
	return mux.RunStructuredBroker(sockPath, pidFile, opts)
}

func readOneShotSecretFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("responses-chat credential file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read responses-chat credential file: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove responses-chat credential file: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("responses-chat credential file is empty")
	}
	return string(data), nil
}

func brokerLaunchBackend(launchContext isolation.LaunchContext) (isolation.LaunchBackend, *isolation.Runtime, error) {
	_ = config.Load(filepath.Join(amuxhome.Dir(), "config.toml"))
	isolationCfg, err := config.GetIsolation()
	if err != nil {
		return nil, nil, err
	}
	isolationRuntime, err := isolation.NewRuntime(isolationCfg)
	if err != nil {
		return nil, nil, err
	}
	backend, err := isolationRuntime.BackendForContext(launchContext)
	if err != nil {
		_ = isolationRuntime.Close()
		return nil, nil, err
	}
	return backend, isolationRuntime, nil
}

func runServer(cmd *cobra.Command, _ []string) error {
	amuxDir := amuxhome.Dir()

	// Load config (ignore error — config file is optional).
	_ = config.Load(filepath.Join(amuxDir, "config.toml"))
	runtimeCfg := config.GetRuntime()
	if err := session.ValidateIdleTimeoutSeconds(runtimeCfg.IdleTimeoutSeconds); err != nil {
		return fmt.Errorf("runtime idle timeout: %w", err)
	}
	isolationCfg, err := config.GetIsolation()
	if err != nil {
		return fmt.Errorf("isolation config: %w", err)
	}
	isolationRuntime, err := isolation.NewRuntime(isolationCfg)
	if err != nil {
		return fmt.Errorf("isolation runtime: %w", err)
	}
	defer isolationRuntime.Close()

	sockPath, _ := cmd.Flags().GetString("socket")
	if sockPath == "" {
		sockPath = filepath.Join(amuxDir, "runtime.sock")
	}

	// Enforce a single amuxlet per socket. The PID-file check in the CLI is not
	// atomic with spawning, and the socket bind below unlinks+rebinds rather than
	// failing on an in-use address, so without this lock a concurrent auto-start
	// (or a restart that misses a stale pid file) would silently orphan the
	// previous amuxlet — it keeps Serve()ing on an unlinked socket inode forever.
	// Holding the flock for the process lifetime turns that into a clean refusal.
	lockFile, err := acquireSingletonLock(sockPath + ".lock")
	if err != nil {
		return err
	}
	defer lockFile.Close()

	dbPath, _ := cmd.Flags().GetString("db")
	if dbPath == "" {
		dbPath = filepath.Join(amuxDir, "runtime.db")
	}

	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	if err := mgr.LoadFromDB(); err != nil {
		return fmt.Errorf("load sessions: %w", err)
	}
	amuxletBin, _ := os.Executable()

	// Remove stale socket file.
	_ = os.Remove(sockPath)

	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sockPath, err)
	}
	defer lis.Close()

	mcpCfg := config.GetMCP()
	hubCfg := config.GetHub()
	creds := task.CredentialProvider(task.FileCredentials{Dir: filepath.Join(amuxDir, "secrets")})
	if hubCfg.Addr != "" {
		creds = newHubCredentialProvider(hubCfg.Addr, hubCfg.Token)
	}
	localSrv := &server{
		cleanupDB: db, cleanupTarget: runtimeCfg.ID,
		mgr:                       mgr,
		amuxletBin:                amuxletBin,
		amuxDir:                   amuxDir,
		version:                   version,
		startedAt:                 time.Now(),
		defaultIdleTimeoutSeconds: runtimeCfg.IdleTimeoutSeconds,
		disableMCPAutoInject:      !mcpCfg.Enabled || !mcpCfg.AutoInject,
		creds:                     creds,
		memoryBindingResolver:     newHubMemoryBindingResolver(hubCfg.Addr, hubCfg.Token),
		isolationRuntime:          isolationRuntime,
		isolationGlobalEnv:        config.ActiveIsolationGlobalEnv(isolationCfg),
		providerEndpointSelector:  newProviderEndpointSelector(),
		autoModelConfig:           distributionAutoModelConfig,
	}
	maintenance, err := newRuntimeMaintenance(localSrv, nil)
	if err != nil {
		return fmt.Errorf("load maintenance journal: %w", err)
	}
	// Reconnect to broker sockets for sessions that survived the restart.
	if err := restoreRunningSessions(mgr); err != nil {
		return fmt.Errorf("restore running sessions: %w", err)
	}

	grpcSrv := grpc.NewServer(transport.GRPCServerOptions()...)
	// Registration follows maintenance journal recovery below.

	// runtimeCfg identifies this runtime; its ID is both the hub registration id
	// and the task SessionRef target id, so a routed task's session_ref matches
	// the target it was scheduled onto.

	// Phase 1 distributed-task control plane: an in-process runner driving child
	// sessions through the local session manager, exposed via the AmuxTask service.
	taskCtrl := newTaskController(localSrv, runtimeCfg.ID)
	taskRunner, err := task.NewRunner(task.Options{
		AmuxDir:   amuxDir,
		Store:     task.NewSQLStore(db),
		Control:   taskCtrl,
		Artifacts: taskArtifactPruner{srv: localSrv},
		Creds:     creds,
		// Agents whose provider emits a native turn result skip the fallback
		// envelope injection; the runner records the summary from the native event.
		NativeResult: func(agentType string) bool {
			a, gerr := adapter.Get(agentType)
			return gerr == nil && a.Capabilities().NativeResult
		},
		// Materialize a resolved Agent Profile's content into the per-run sandbox
		// before the child starts.
		MaterializeProfile: func(profile *pb.AgentProfileSpec, sb *task.Sandbox) ([]string, error) {
			return agentprofile.MaterializeIntoSandbox(profile, sb, adapter.Layout, amuxDir)
		},
		LaunchEnv: func(identity principal.ExecutionIdentity) []string {
			return localSrv.launchEnvForIdentity(identity, nil)
		},
	})
	if err != nil {
		return fmt.Errorf("init task runner: %w", err)
	}
	// instance_wait joins both the active TaskRun and the child-session lifecycle.
	// Wiring the runner watch here closes the instance_followup -> instance_wait race
	// where the next task is pending but the reusable child is still sleeping.
	taskCtrl.watchActiveInstance = taskRunner.WatchActiveInstance
	// Let session-prune and the cleanup sweeper detect orphaned task child
	// sessions (owner task record gone) without coupling the session layer to
	// the task store.
	localSrv.taskExists = taskRunner.TaskExists
	localSrv.cleanupTasks = taskRunner
	if recovered, rerr := taskRunner.RecoverOrphans(); rerr != nil {
		fmt.Fprintf(os.Stderr, "runtime: recover orphan tasks: %v\n", rerr)
	} else if len(recovered) > 0 {
		fmt.Fprintf(os.Stderr, "runtime: marked %d orphan task(s) failed after restart\n", len(recovered))
	}
	// Reap child sessions stranded by a task record that no longer exists (e.g.
	// pruned before the child archived in an older build).
	if reaped, rerr := localSrv.reapOrphanChildSessions(); rerr != nil {
		fmt.Fprintf(os.Stderr, "runtime: reap orphan child sessions: %v\n", rerr)
	} else if len(reaped) > 0 {
		fmt.Fprintf(os.Stderr, "runtime: reaped %d orphan child session(s)\n", len(reaped))
	}
	taskSrv := newTaskServer(taskRunner, taskCtrl)
	taskSrv.resolveParentTurn = localSrv.GetActiveTurn
	taskSrv.resolveProfile = func(spec *task.Spec) error {
		return agentprofile.ResolveTaskSpec(spec, agentprofile.DefaultPackRegistryPath())
	}
	maintenance.tasks = taskRunner.ActiveCount
	sessionRPC := &maintenanceSessionServer{AmuxletServer: localSrv, maintenance: maintenance}
	taskRPC := &maintenanceTaskServer{AmuxTaskServer: taskSrv, maintenance: maintenance}
	pb.RegisterAmuxletServer(grpcSrv, sessionRPC)
	pb.RegisterAmuxTaskServer(grpcSrv, taskRPC)
	pb.RegisterRuntimeMaintenanceServer(grpcSrv, maintenance)
	if err := os.Chmod(sockPath, 0600); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Drain any sessions LoadFromDB queued for restart (process died while
	// amuxlet was down). Best-effort: log failures and move on.
	go func() {
		for sess := range mgr.PendingRestart() {
			fmt.Fprintf(os.Stderr, "runtime: auto-restarting session %s\n", sess.Name)
			for {
				done, err := maintenance.enter(true)
				if err == nil {
					if sess.StateSnapshot() == session.StateCreated {
						_, err = localSrv.Restart(context.Background(), &pb.RestartRequest{Name: sess.Name})
					}
					done()
					if err != nil {
						fmt.Fprintf(os.Stderr, "runtime: auto-restart %s: %v\n", sess.Name, err)
					}
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}
	}()

	reclaimCfg, err := config.GetSessionReclaim()
	if err != nil {
		return fmt.Errorf("session_reclaim config: %w", err)
	}
	go maintenance.watch(ctx)
	startSessionReclaimWatcher(ctx, localSrv, reclaimCfg)
	startTaskCleanupWatcher(ctx, taskRunner, localSrv, config.GetRuntimeTaskCleanup())
	startDistributionStateWatchers(ctx)
	startBrokerReapWatcher(ctx, localSrv, config.GetRuntimeBrokerReap())
	startDevelopmentLeaseWatcher(
		ctx,
		mgr,
		agentprofile.DefaultPackRegistryPath(),
		developmentLeaseSweepInterval,
	)

	// Start tunnel client if hub.addr is configured.
	if hubCfg.Addr != "" {
		tc := tunnel.NewClient(hubCfg.Addr, runtimeCfg.ID, runtimeCfg.Name, mgr, sessionRPC)
		tc.SetVersion(version)
		tc.SetCapabilities(buildCapabilities(runtimeCfg, isolationRuntime), hubCfg.Token)
		tc.SetTaskServer(taskRPC)
		go tc.Run(ctx)
	}

	// Graceful shutdown on signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, runtimeShuttingDownMessage())
		// Kill all active session broker processes before stopping the server.
		// Without this, broker processes become orphans (reparented to init)
		// because they block waiting on a socket that will never connect.
		if localSrv != nil {
			// Mark shutdown before killing so the per-session drain goroutines
			// do not persist Archived for processes we are killing ourselves.
			// The DB must keep these sessions Running so the next amuxlet's
			// LoadFromDB queues them for auto-restart (service update flow).
			localSrv.shuttingDown.Store(true)
			for _, sess := range localSrv.mgr.List() {
				_ = sess.Kill()
			}
		}
		cancel()
		grpcSrv.GracefulStop()
		os.Remove(sockPath)
	}()

	fmt.Fprintln(os.Stderr, runtimeListeningMessage(sockPath))
	return grpcSrv.Serve(lis)
}

// acquireSingletonLock takes an exclusive, non-blocking advisory lock on path so
// that only one amuxlet can own a given socket at a time. The returned file must
// be kept open for the lifetime of the process; closing it (on shutdown) releases
// the lock. A held lock yields a clear error instead of a second daemon silently
// taking over the socket.
func acquireSingletonLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create runtime dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open runtime lock: %w", err)
	}
	acquired, err := trySingletonFileLock(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquire runtime lock %s: %w", path, err)
	}
	if !acquired {
		_ = f.Close()
		return nil, fmt.Errorf("another amuxlet already owns %s; refusing to start a second runtime", path)
	}
	return f, nil
}

func startSessionReclaimWatcher(ctx context.Context, srv *server, cfg config.SessionReclaimConfig) {
	if srv == nil || !cfg.Enabled || cfg.SweepIntervalSeconds <= 0 {
		return
	}
	interval := time.Duration(cfg.SweepIntervalSeconds) * time.Second
	toPolicy := func(s config.SessionReclaimStage) session.ReclaimPolicy {
		return session.ReclaimPolicy{
			SleepAfter:   time.Duration(s.SleepAfterSeconds) * time.Second,
			ArchiveAfter: time.Duration(s.ArchiveAfterSeconds) * time.Second,
			PruneAfter:   time.Duration(s.PruneAfterSeconds) * time.Second,
		}
	}
	localPol, feishuPol := toPolicy(cfg.Local), toPolicy(cfg.Feishu)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		firstTick := true
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if firstTick {
					firstTick = false
					continue
				}
				srv.reclaimSessions(now, localPol, feishuPol)
			}
		}
	}()
}

func startDevelopmentLeaseWatcher(
	ctx context.Context,
	mgr *session.Manager,
	packRegistryPath string,
	interval time.Duration,
) {
	if mgr == nil || strings.TrimSpace(packRegistryPath) == "" || interval <= 0 {
		return
	}
	sweep := func() {
		liveOwnerSessionIDs := make(map[string]struct{})
		for _, sess := range mgr.List() {
			if sess.StateSnapshot() != session.StateArchived {
				liveOwnerSessionIDs[sess.ID] = struct{}{}
			}
		}
		results, err := reconcileDevelopmentLeases(packRegistryPath, time.Now(), liveOwnerSessionIDs)
		for _, result := range results {
			switch result.Status {
			case agentprofile.DevelopmentLeaseCleaned:
				fmt.Fprintf(
					os.Stderr,
					"runtime: cleaned development lease %s; branch %s retained\n",
					result.Lease.Namespace,
					result.Lease.Branch,
				)
			case agentprofile.DevelopmentLeaseRetained:
				fmt.Fprintf(
					os.Stderr,
					"runtime: retained development lease %s: %s\n",
					result.Lease.Namespace,
					result.Reason,
				)
			}
		}
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "runtime: development lease cleanup: %v\n", err)
		}
	}
	go func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		sessionChanges := mgr.WatchSessions()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				sweep()
				timer.Reset(interval)
			case <-sessionChanges:
				sessionChanges = mgr.WatchSessions()
				sweep()
			}
		}
	}()
}

func startTaskCleanupWatcher(ctx context.Context, runner *task.Runner, srv *server, cfg config.RuntimeTaskCleanupConfig) {
	if runner == nil || !cfg.Enabled || cfg.TerminalHistoryTTLSeconds <= 0 || cfg.SweepIntervalSeconds <= 0 {
		return
	}
	interval := time.Duration(cfg.SweepIntervalSeconds) * time.Second
	ttl := time.Duration(cfg.TerminalHistoryTTLSeconds) * time.Second
	sweep := func() {
		var maintenance *runtimeMaintenance
		if srv != nil {
			maintenance = srv.maintenance
		}
		done, err := maintenance.enter(true)
		if err != nil {
			return
		}
		defer done()
		if _, err := runner.Prune(ctx, task.PruneRequest{OlderThan: ttl}); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "runtime: task cleanup: %v\n", err)
		}
		// Self-heal child sessions whose owner task record is gone. Runs after
		// Prune so a task pruned this tick (with its child already archived) is
		// handled by Prune's own DeleteArchivedOwned, and only true orphans fall
		// through to here.
		if srv != nil {
			if reaped, err := srv.reapOrphanChildSessions(); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "runtime: reap orphan child sessions: %v\n", err)
			} else if len(reaped) > 0 {
				fmt.Fprintf(os.Stderr, "runtime: reaped %d orphan child session(s)\n", len(reaped))
			}
		}
	}
	go func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				sweep()
				timer.Reset(interval)
			}
		}
	}()
}

// startBrokerReapWatcher periodically reaps orphan session-broker processes and
// their artifact files. Brokers are spawned detached (Setsid) and are only
// reaped on the graceful SIGINT/SIGTERM path, so an ungraceful amuxlet exit
// (SIGKILL, crash) or an `amux update` binary overwrite leaks them — reparented
// to init and invisible to the DB-row-driven recovery (which tracks the inner
// agent pid, not the broker pid). The sweep runs once at startup to clear any
// accumulated backlog, then every interval. It is conservative and safe
// alongside live sessions: a live session's current broker is never signalled,
// only brokers whose session is gone or no longer running (or stale duplicates
// of a live session's newest broker) are reaped, and each is re-verified
// against the process table immediately before signalling to defeat PID reuse.
func startBrokerReapWatcher(ctx context.Context, srv *server, cfg config.RuntimeBrokerReapConfig) {
	if !cfg.Enabled || cfg.SweepIntervalSeconds <= 0 {
		return
	}
	interval := time.Duration(cfg.SweepIntervalSeconds) * time.Second
	sweep := func() {
		reaped, err := srv.reapOrphanBrokers()
		switch {
		case err != nil:
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "runtime: broker reap: %v\n", err)
			}
		case reaped > 0:
			fmt.Fprintf(os.Stderr, "runtime: broker reap: reaped %d orphan brokers/session-artifacts\n", reaped)
		}
	}
	go func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				sweep()
				timer.Reset(interval)
			}
		}
	}()
}

func restoreRunningSessions(mgr *session.Manager) error {
	for _, sess := range mgr.List() {
		if sess.State != session.StateRunning {
			continue
		}
		if sess.BrokerSocket == "" {
			if err := sess.FinishRecoveredTurn("runtime_reconnect_failed", "running session had no broker socket during recovery"); err != nil {
				return fmt.Errorf("finish recovered turn for session %q: %w", sess.Name, err)
			}
			if err := mgr.SetSleeping(sess, session.SleepReasonRestartFailed); err != nil {
				return fmt.Errorf("sleep recovered session %q: %w", sess.Name, err)
			}
			continue
		}
		if err := reconnectSessionBroker(mgr, sess); err != nil {
			return err
		}
	}
	return nil
}

func reconnectSessionBroker(mgr *session.Manager, sess *session.Session) error {
	proc, procDone, err := reconnectProc(sess)
	if err != nil {
		if finishErr := sess.FinishRecoveredTurn("runtime_reconnect_failed", err.Error()); finishErr != nil {
			return fmt.Errorf("finish recovered turn for session %q: %w", sess.Name, finishErr)
		}
		if sleepErr := mgr.SetSleeping(sess, session.SleepReasonRestartFailed); sleepErr != nil {
			return fmt.Errorf("sleep recovered session %q: %w", sess.Name, sleepErr)
		}
		fmt.Fprintln(os.Stderr, runtimeReconnectSleepMessage(sess.Name, err))
		return nil
	}
	sess.SetEventTap(func(ev agentstream.Event) bool {
		if err := mgr.RecordAgentEvent(sess, ev); err != nil {
			fmt.Fprintf(os.Stderr, "runtime: record session event: %v\n", err)
		}
		return handleRunSessionPermission(sess, ev)
	})
	sess.SetProc(proc)
	procGeneration := sess.ProcessGeneration()
	if err := sess.RebindRecoveredTurn(); err != nil {
		_ = proc.Close()
		if errors.Is(err, agentstream.ErrJournalFault) {
			return fmt.Errorf("rebind recovered turn for session %q: %w", sess.Name, err)
		}
		if sleepErr := mgr.SetSleeping(sess, session.SleepReasonRestartFailed); sleepErr != nil {
			return fmt.Errorf("sleep recovered session %q: %w", sess.Name, sleepErr)
		}
		fmt.Fprintln(os.Stderr, runtimeReconnectSleepMessage(sess.Name, err))
		return nil
	}
	go func() {
		<-procDone
		sess.FailActiveTurnForProcess(procGeneration, "process_exit")
		if err := mgr.SetState(sess, session.StateArchived); err != nil {
			fmt.Fprintf(os.Stderr, "runtime: archive disconnected session %s: %v\n", sess.Name, err)
		}
	}()
	fmt.Fprintln(os.Stderr, runtimeReconnectedMessage(sess.Name, sess.PID))
	return nil
}

func runtimeListeningMessage(sockPath string) string {
	return fmt.Sprintf("runtime listening on %s", sockPath)
}

func runtimeShuttingDownMessage() string {
	return "runtime: shutting down"
}

func runtimeReconnectSleepMessage(name string, err error) string {
	return fmt.Sprintf("runtime: reconnect session %s: %v — sleeping", name, err)
}

func runtimeReconnectedMessage(name string, pid int) string {
	return fmt.Sprintf("runtime: reconnected session %s (pid %d)", name, pid)
}

func reconnectProc(sess *session.Session) (session.Proc, <-chan struct{}, error) {
	a, err := adapter.Get(sess.AgentType)
	if err != nil {
		return nil, nil, err
	}
	protocol := a.Capabilities().Protocol
	providerID, _ := protocol.StructuredProvider()
	switch {
	case protocol == adapter.ProtocolACP:
		return mux.ConnectACP(sess.BrokerSocket, sess.PID)
	case providerID != "":
		return mux.ConnectStructured(sess.BrokerSocket, sess.PID)
	default:
		return nil, nil, fmt.Errorf("unsupported protocol %q for agent type %q", protocol, sess.AgentType)
	}
}
