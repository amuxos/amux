package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/mux"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/provider"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessioninput"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/tunnel"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type server struct {
	dailyCleanupMu  sync.Mutex
	cleanupLaunchMu sync.RWMutex
	cleanupDB       *store.DB
	cleanupTasks    *task.Runner
	cleanupTarget   string

	maintenance *runtimeMaintenance
	pb.UnimplementedAmuxletServer
	mgr                       *session.Manager
	amuxletBin                string
	amuxDir                   string
	launchInstructions        appliedInstructions
	version                   string
	startedAt                 time.Time
	defaultIdleTimeoutSeconds int
	disableMCPAutoInject      bool
	memoryBindingResolver     func(context.Context, string) (string, error)
	creds                     task.CredentialProvider
	isolationRuntime          *isolation.Runtime
	isolationGlobalEnv        []string
	relaunchMu                sync.Mutex
	startSessionHook          func(adapter.Adapter, *session.Session, []string, []string) error
	startSessionContextHook   func(context.Context, adapter.Adapter, *session.Session, []string, []string) error
	autoModelConfig           func(string) (adapter.AutoModelSelectionConfig, bool)
	providerEndpointSelector  *providerEndpointSelector
	// taskExists reports whether a task record still exists. Injected from main
	// wiring (taskRunner.TaskExists) so the session layer stays unaware of tasks.
	// nil disables orphan reaping (defensive: never reap without a live checker).
	taskExists func(string) (bool, error)
	// shuttingDown is set by the signal handler during graceful shutdown.
	// Shutdown kills every broker so the processes die deliberately, not
	// because the sessions ended: the drain goroutines must not archive those
	// sessions. Keeping them Running (or their recorded state) in the DB is
	// what lets the next amuxlet's LoadFromDB queue them for auto-restart;
	// persisting Archived here would permanently end every live topic on each
	// service update.
	shuttingDown atomic.Bool
}

func (s *server) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{
		Version:   s.version,
		StartedAt: s.startedAt.Unix(),
	}, nil
}

// ownerGuard rejects direct mutating RPCs on a session owned by a distributed
// task or a persistent agent instance. The owner task runner operates on these
// sessions in-process (not via gRPC), so any gRPC caller here is by definition
// not the owner and must go through the task API (amux.* / AmuxTask). A
// persistent-agent session is managed by the agent-level lifecycle
// (spawn/followup/interrupt), so direct session RPCs are rejected the same way.
// Read-only RPCs (List, Attach, QueueList) are intentionally not guarded so a
// session stays diagnosable.
func ownerGuard(sess *session.Session) error {
	if sess.OwnerTaskId != "" {
		return status.Errorf(codes.PermissionDenied,
			"session %q is owned by task %s; use the task API (amux task ...), not direct session RPCs",
			sess.Name, sess.OwnerTaskId)
	}
	if sess.AgentInstanceID != "" {
		return status.Errorf(codes.PermissionDenied,
			"session %q is owned by agent instance %s; use the agent-level tools (amux.instance_spawn/amux.instance_followup/amux.instance_interrupt), not direct session RPCs",
			sess.Name, sess.AgentInstanceID)
	}
	return nil
}

func (s *server) Run(ctx context.Context, req *pb.RunRequest) (*pb.RunResponse, error) {
	s.cleanupLaunchMu.RLock()
	defer s.cleanupLaunchMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, grpcContextError(err)
	}
	if strings.TrimSpace(req.RunAs) != "" {
		return nil, status.Error(codes.Unauthenticated, "run_as must be resolved by amuxd before runtime dispatch")
	}
	a, err := adapter.Get(req.AgentType)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	identity := principal.FromProtoIdentity(req.ExecutionIdentity)
	if identity.IdentityMode == "" {
		identity = principal.LegacyHostIdentity(principal.SourceCLI)
	}
	launchID := strings.TrimSpace(req.LaunchId)
	if launchID == "" {
		launchID = "launch-" + uuid.NewString()
	}
	cwd, runtimeOwnedCwd, identity, principalPaths, err := s.prepareExecution(ctx, identity, launchID, req)
	if err != nil {
		if errors.Is(err, isolation.ErrUnidentifiedExecution) {
			return nil, status.Errorf(codes.PermissionDenied, "%v", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "prepare isolation: %v", err)
	}
	idleTimeoutSeconds, idleTimeoutSet, err := s.effectiveIdleTimeoutSeconds(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := validateRunPermissionPolicy(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	req.ExtraArgs, err = agentprofile.SessionPermissionDefaults(req.AgentType, req.ExtraArgs, req.ProfileSpec)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	sess, err := s.mgr.CreateWithOptions(req.Name, req.AgentType, session.CreateOptions{
		Cwd:                cwd,
		IdleTimeoutSeconds: idleTimeoutSeconds,
		IdleTimeoutSet:     idleTimeoutSet,
	})
	if err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "%v", err)
	}
	if err := s.mgr.SetExecutionIdentity(sess, identity, launchID); err != nil {
		_ = s.mgr.Delete(sess)
		return nil, status.Errorf(codes.InvalidArgument, "execution identity: %v", err)
	}
	materializationCwd := cwd
	stagingCwd := ""
	if identity.IsolationBackend == principal.BackendLinuxDAC {
		if err := os.MkdirAll(s.amuxDir, 0o700); err != nil {
			_ = s.mgr.Delete(sess)
			return nil, status.Errorf(codes.Internal, "create isolation staging parent: %v", err)
		}
		stagingCwd, err = os.MkdirTemp(s.amuxDir, "isolation-stage-*")
		if err != nil {
			_ = s.mgr.Delete(sess)
			return nil, status.Errorf(codes.Internal, "create isolation staging workspace: %v", err)
		}
		materializationCwd = stagingCwd
		if strings.TrimSpace(req.Cwd) != "" {
			if err := isolation.MaterializeWorkspace(req.Cwd, stagingCwd); err != nil {
				_ = os.RemoveAll(stagingCwd)
				_ = s.mgr.Delete(sess)
				return nil, status.Errorf(codes.InvalidArgument, "stage isolation workspace: %v", err)
			}
		}
	}
	cleanupCreatedSession := func() {
		_ = s.mgr.Delete(sess)
		if stagingCwd != "" {
			_ = os.RemoveAll(stagingCwd)
		}
		if runtimeOwnedCwd {
			_ = os.RemoveAll(cwd)
		}
		if s.isolationRuntime != nil {
			_ = s.isolationRuntime.RemoveRun(context.Background(), identity, launchID)
		}
		_ = os.RemoveAll(profileSessionDir(s.amuxDir, sess.ID))
	}
	if err := os.MkdirAll(cwd, 0700); err != nil {
		cleanupCreatedSession()
		return nil, status.Errorf(codes.InvalidArgument, "cwd: %v", err)
	}
	repoPath, err := materializeRepoWorkspace(ctx, req, materializationCwd)
	if err != nil {
		cleanupCreatedSession()
		if ctxErr := grpcContextError(err); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, status.Errorf(codes.InvalidArgument, "repo materialization: %v", err)
	}
	if err := materializeProfileWorkspace(req, materializationCwd, repoPath, s.amuxDir); err != nil {
		cleanupCreatedSession()
		return nil, status.Errorf(codes.InvalidArgument, "profile materialization: %v", err)
	}
	memoryBinding, err := s.resolveMemoryBinding(ctx, req.AgentActorToken, identity, req.AgentType)
	var memoryWarning string
	if err != nil {
		var unavailable *memoryUnavailableError
		if errors.As(err, &unavailable) {
			memoryWarning = err.Error()
		} else {
			cleanupCreatedSession()
			return nil, status.Errorf(codes.FailedPrecondition, "memory binding: %v", err)
		}
	}
	var mcpExtraArgs []string
	if !s.disableMCPAutoInject && !runArgsContainAmuxMCP(req.AgentType, req.ExtraArgs) {
		mcpExtraArgs, err = materializeAmuxMCPForRun(req.AgentType, sess.ID, s.amuxDir, materializationCwd, req.AgentActorToken, memoryBinding)
		if err != nil {
			cleanupCreatedSession()
			return nil, status.Errorf(codes.InvalidArgument, "mcp materialization: %v", err)
		}
	}
	credentialCtx := sessionCredentialMaterializationContext(ctx, identity, req.AgentActorToken)
	providerEnv, err := s.materializeProviderCredentialEnv(credentialCtx, identity, req)
	if err != nil {
		cleanupCreatedSession()
		return nil, status.Errorf(codes.InvalidArgument, "provider credential: %v", err)
	}
	recordEnv := memoryprotocol.SetEnv(ensureLaunchEnvPATH(req.Env), memoryprotocol.BindingEnv, memoryBinding)
	runEnv := s.launchEnvForIdentity(identity, append(append([]string{}, providerEnv...), recordEnv...))
	runExtraArgs := append(append([]string{}, req.ExtraArgs...), mcpExtraArgs...)
	// Non-fatal launch findings (e.g. an expired provider credential) travel back
	// in the response so the caller can show them: the CLI on stderr, the Feishu
	// bridge as a thread notice. Nobody has to read runtime.log to learn that the
	// agent is about to answer nothing.
	var launchWarnings []string
	if memoryWarning != "" {
		launchWarnings = append(launchWarnings, memoryWarning)
	}
	if identity.IsolationBackend == principal.BackendLinuxDAC {
		runEnv, recordEnv, runExtraArgs, err = materializeLinuxDACSessionLaunch(
			func(sources []isolation.BundleSource) error {
				return s.isolationRuntime.MaterializeExecution(ctx, identity, launchID, principalPaths.Workspace, sources)
			},
			materializationCwd, profileSessionDir(s.amuxDir, sess.ID), "", principalPaths,
			isolatedHomeProjectionFor(req.AgentType, func(msg string) {
				launchWarnings = append(launchWarnings, msg)
			}), runEnv, recordEnv, runExtraArgs,
		)
		if err != nil {
			cleanupCreatedSession()
			return nil, status.Errorf(codes.InvalidArgument, "materialize isolation launch bundle: %v", err)
		}
		_ = os.RemoveAll(stagingCwd)
		stagingCwd = ""
	}
	effectiveModel := effectiveRunModel(req)
	// Applied after isolation materialization rewrites the launch env, so the
	// credential survives the rewrite, and kept out of recordEnv below.
	runEnv, recordEnv = applyAgentActorToken(runEnv, recordEnv, req.AgentActorToken, sessionActorTokenPath(s.amuxDir, sess.ID))
	// Relaunches are driven locally and cannot mint a replacement, so the
	// credential has to outlive this process.
	if err := writeSessionActorToken(s.amuxDir, sess.ID, req.AgentActorToken, req.AgentActorTokenExpiresAt); err != nil {
		fmt.Fprintf(os.Stderr, "runtime: persist agent actor token for %s: %v\n", sess.Name, err)
	}
	if warning := agentActorTokenExpiryWarning(req.AgentActorTokenExpiresAt, time.Now()); warning != "" {
		launchWarnings = append(launchWarnings, warning)
	}

	// If no explicit model was resolved, let the adapter auto-select based on
	// runtime conditions (e.g. provider model load). Only for fresh sessions;
	// restart/resume preserves the previously selected model from the snapshot.
	runExtraArgs, effectiveModel = s.maybeAutoSelectModel(a, req.AgentType, cwd, runEnv, runExtraArgs, effectiveModel)

	if err := s.mgr.RecordLaunchParams(sess, runExtraArgs, recordEnv, req.WorkspaceSource, req.WorkspaceScope, req.AgentProfile, effectiveModel, launchSpecSnapshot(req)); err != nil {
		cleanupCreatedSession()
		return nil, status.Errorf(codes.Internal, "record params: %v", err)
	}

	if text := strings.TrimSpace(req.GetSystemInstructions()); text != "" {
		writeSessionInstructions(s.amuxDir, sess.ID, text)
	}
	if err := s.startSession(a, sess, runExtraArgs, runEnv); err != nil {
		// The launch never attached; its run directory would otherwise sit
		// orphaned next to the session parked in restart_failed.
		s.reclaimLaunchRun(identity, launchID)
		return nil, err
	}
	for _, warning := range launchWarnings {
		fmt.Fprintf(os.Stderr, "runtime: %s\n", warning)
		// Also on the session itself, so `amux attach` and any stream consumer
		// that replays history sees it even when the creating caller does not
		// render response warnings.
		sess.AppendReplay([]byte("\r\n[system] " + warning + "\r\n"))
	}
	return &pb.RunResponse{Session: toProtoSession(sess), Warnings: launchWarnings}, nil
}

func (s *server) prepareExecution(ctx context.Context, identity principal.ExecutionIdentity, launchID string, req *pb.RunRequest) (string, bool, principal.ExecutionIdentity, isolation.PrincipalPaths, error) {
	if identity.IdentityMode == "" {
		identity = principal.LegacyHostIdentity(principal.SourceSystem)
	}
	// Creation is the only place this is checked. Relaunching an execution
	// created before the host opted into isolation keeps working: its identity
	// was frozen under the old rules and cannot be escalated by replaying it.
	if err := s.isolationRuntime.RequireIdentifiedExecution(identity); err != nil {
		return "", false, identity, isolation.PrincipalPaths{}, err
	}
	backend := identity.IsolationBackend
	if s.isolationRuntime == nil {
		if backend == principal.BackendLinuxDAC {
			return "", false, identity, isolation.PrincipalPaths{}, fmt.Errorf("linux-dac isolation runtime is unavailable")
		}
		if backend == "" {
			backend = principal.BackendPassthrough
		}
	} else {
		paths, selected, err := s.isolationRuntime.PrepareExecution(ctx, identity, launchID, "")
		if err != nil {
			return "", false, identity, isolation.PrincipalPaths{}, err
		}
		backend = selected
		if selected == principal.BackendLinuxDAC {
			finalized, err := principal.FinalizeBackend(identity, selected)
			if err != nil {
				return "", false, identity, isolation.PrincipalPaths{}, err
			}
			return paths.Workspace, false, finalized, paths, nil
		}
	}
	finalized, err := principal.FinalizeBackend(identity, backend)
	if err != nil {
		return "", false, identity, isolation.PrincipalPaths{}, err
	}
	cwd, runtimeOwned, err := sessionCwdPath(s.amuxDir, req)
	return cwd, runtimeOwned, finalized, isolation.PrincipalPaths{}, err
}

func effectiveRunModel(req *pb.RunRequest) string {
	if req == nil {
		return ""
	}
	if req.ProfileSpec != nil {
		if model := strings.TrimSpace(req.ProfileSpec.Model); model != "" {
			return model
		}
	}
	model, _ := agentprofile.AgentArgsModel(req.AgentType, req.ExtraArgs)
	return strings.TrimSpace(model)
}

// maybeAutoSelectModel asks the adapter to auto-select a model based on runtime
// conditions (e.g. provider model load) when no explicit --model is present in
// extraArgs and effectiveModel is empty. If a model is selected, it is injected
// into extraArgs as ["--model", <model>] and returned as effectiveModel.
// Best-effort: errors are logged and do not block launch.
func (s *server) maybeAutoSelectModel(a adapter.Adapter, agentType, cwd string, env, extraArgs []string, effectiveModel string) ([]string, string) {
	sel, ok := a.(adapter.AutoModelSelector)
	if !ok {
		return extraArgs, effectiveModel
	}
	// If an explicit model is already set (either in args or as effectiveModel),
	// respect it and skip auto-selection.
	if effectiveModel != "" {
		return extraArgs, effectiveModel
	}
	if _, hasModel := agentprofile.AgentArgsModel(agentType, extraArgs); hasModel {
		return extraArgs, effectiveModel
	}
	if s.autoModelConfig == nil {
		return extraArgs, effectiveModel
	}
	cfg, enabled := s.autoModelConfig(agentType)
	if !enabled {
		return extraArgs, effectiveModel
	}
	model, err := sel.SelectAutoModel(cwd, env, cfg)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "runtime: auto-select model for %s: %v\n", agentType, err)
		}
		return extraArgs, effectiveModel
	}
	if model == "" {
		return extraArgs, effectiveModel
	}
	injected := append([]string{}, extraArgs...)
	injected = append(injected, "--model", model)
	return injected, model
}

func (s *server) materializeProviderCredentialEnv(ctx context.Context, identity principal.ExecutionIdentity, req *pb.RunRequest) ([]string, error) {
	if req == nil || req.ProfileSpec == nil {
		return nil, nil
	}
	plan, err := task.ResolveCredentialPlan(task.Spec{
		Agent:           req.AgentType,
		ResolvedProfile: req.ProfileSpec,
	})
	if err != nil {
		return nil, err
	}
	existingEnv := append(os.Environ(), s.launchEnvForIdentity(identity, req.Env)...)
	plan = task.FilterCredentialPlanForEnv(plan, existingEnv)
	if len(plan.Requests) == 0 {
		return nil, nil
	}
	mat, err := task.MaterializeCredentialPlan(ctx, s.credentialProvider(), plan, req.AgentType)
	if err != nil {
		return nil, err
	}
	return mat.Env, nil
}

// launchEnvForIdentity applies installation-wide isolation defaults only to
// identified executions. The field is populated only when isolation is enabled
// with a non-passthrough backend, so helper readiness does not make credentials
// appear or disappear across restarts.
func (s *server) launchEnvForIdentity(identity principal.ExecutionIdentity, env []string) []string {
	if s == nil || !identity.Identified() || len(s.isolationGlobalEnv) == 0 {
		return append([]string(nil), env...)
	}
	return mergeDefaultEnvByKey(env, s.isolationGlobalEnv)
}

// sessionCredentialMaterializationContext prevents a legacy-host session's
// control-plane actor token from changing its provider credential scope.
// Identified sessions retain their existing token-based materialization path.
func sessionCredentialMaterializationContext(ctx context.Context, identity principal.ExecutionIdentity, actorToken string) context.Context {
	if !identity.Identified() {
		return task.WithoutCredentialActorToken(ctx)
	}
	return task.WithCredentialActorToken(ctx, actorToken)
}

func (s *server) credentialProvider() task.CredentialProvider {
	if s != nil && s.creds != nil {
		return s.creds
	}
	amuxDir := ""
	if s != nil {
		amuxDir = s.amuxDir
	}
	return task.FileCredentials{Dir: filepath.Join(amuxDir, "secrets")}
}

func envSliceContainsKey(envName string, env []string) bool {
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if ok && key == envName {
			return true
		}
	}
	return false
}

func ensureLaunchEnvPATH(env []string) []string {
	out := append([]string(nil), env...)
	if envSliceContainsKey("PATH", out) {
		return out
	}

	pathEntries := filepath.SplitList(os.Getenv("PATH"))
	pathEntries = appendStandardUserBinPaths(pathEntries)
	if len(pathEntries) == 0 {
		pathEntries = []string{"/usr/local/bin", "/usr/bin", "/bin"}
	}
	return append(out, "PATH="+strings.Join(uniquePathEntries(pathEntries), string(os.PathListSeparator)))
}

func ensureLegacyLaunchEnvPATH(env []string) []string {
	out := append([]string(nil), env...)
	pathIndex := -1
	var pathEntries []string
	for i := len(out) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(out[i], "=")
		if ok && key == "PATH" {
			pathIndex = i
			pathEntries = filepath.SplitList(value)
			break
		}
	}
	if pathIndex < 0 {
		return ensureLaunchEnvPATH(out)
	}
	pathEntries = appendStandardUserBinPaths(pathEntries)
	out[pathIndex] = "PATH=" + strings.Join(uniquePathEntries(pathEntries), string(os.PathListSeparator))
	return out
}

func appendStandardUserBinPaths(pathEntries []string) []string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		pathEntries = append(pathEntries, filepath.Join(home, ".local", "bin"))
		pathEntries = appendDistributionUserBinPaths(home, pathEntries)
		pathEntries = append(pathEntries,
			filepath.Join(home, ".amux", "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
		)
	}
	return pathEntries
}

func uniquePathEntries(pathEntries []string) []string {
	seen := make(map[string]bool, len(pathEntries))
	unique := make([]string, 0, len(pathEntries))
	for _, entry := range pathEntries {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		unique = append(unique, entry)
	}
	return unique
}

func (s *server) effectiveIdleTimeoutSeconds(req *pb.RunRequest) (int, bool, error) {
	seconds := s.defaultIdleTimeoutSeconds
	set := false
	if req != nil && req.IdleTimeoutSecondsSet {
		seconds = int(req.IdleTimeoutSeconds)
		set = true
	}
	if err := session.ValidateIdleTimeoutSeconds(seconds); err != nil {
		return 0, false, err
	}
	return seconds, set, nil
}

func grpcContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	return nil
}

// startSession launches the agent process for an existing session row and
// wires up output forwarding. Shared between Run and Restart so the launch
// path stays single-sourced.
func (s *server) startSession(a adapter.Adapter, sess *session.Session, extraArgs, extraEnv []string) error {
	return s.startSessionContext(context.Background(), a, sess, extraArgs, extraEnv)
}

func (s *server) startSessionContext(ctx context.Context, a adapter.Adapter, sess *session.Session, extraArgs, extraEnv []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	snap := sess.Snapshot()
	if err := validateMemoryBinding(memoryprotocol.Binding(snap.Env), snap.ExecutionIdentity); err != nil {
		return err
	}
	extraEnv = s.launchEnvForIdentity(snap.ExecutionIdentity, extraEnv)
	// The agent binary is resolved against the last PATH in the launch env, so a
	// launch that names none silently falls back to the environment amuxlet
	// itself was started from — which for a daemon started from a non-login
	// shell has no ~/.local/bin and cannot find a user-installed agent. The
	// session RPCs repair their env before calling in; task child launches carry
	// only TMPDIR and credentials, so hold the invariant here where every launch
	// passes. Callers that already named a PATH (including a deliberately
	// restricted one) keep it.
	extraEnv = ensureLaunchEnvPATH(extraEnv)
	var memoryErr error
	extraArgs, extraEnv, memoryErr = s.configureSessionMemory(ctx, sess, extraArgs, extraEnv)
	if memoryErr != nil {
		return memoryErr
	}
	var capabilityErr error
	extraArgs, extraEnv, capabilityErr = s.configureSessionCapability(ctx, sess, extraArgs, extraEnv)
	if capabilityErr != nil {
		return capabilityErr
	}
	var instructionsApplied string
	extraArgs, extraEnv, instructionsApplied = s.configureSessionInstructions(snap, extraArgs, extraEnv)
	extraArgs = nativeWorkspaceTrustArgs(snap, extraArgs)
	if s.startSessionContextHook != nil {
		err := s.startSessionContextHook(ctx, a, sess, extraArgs, extraEnv)
		if err == nil {
			s.launchInstructions.set(snap.ID, sess.ProcessGeneration(), instructionsApplied)
		}
		return err
	}
	if s.startSessionHook != nil {
		err := s.startSessionHook(a, sess, extraArgs, extraEnv)
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			s.launchInstructions.set(snap.ID, sess.ProcessGeneration(), instructionsApplied)
		}
		return err
	}
	cmd := a.Cmd(extraArgs)
	env := append(os.Environ(), extraEnv...)
	env = memoryprotocol.SetEnv(env, memoryprotocol.BindingEnv, memoryprotocol.Binding(extraEnv))
	// Let in-session tooling (notably `amux mcp`) attribute work to this
	// session: subtask submission fills Spec.ParentSessionID from this.
	env = memoryprotocol.SetEnv(env, "AMUX_SESSION_ID", snap.ID)
	if value := launchEnvValue(env, "CODEX_HOME"); value != "" {
		env = memoryprotocol.SetEnv(env, "CODEX_HOME", value)
	}
	cmd.Env = env
	cmd.Dir = snap.Cwd

	if t, ok := a.(adapter.WorkspaceTruster); ok && hostWorkspaceTrust(snap) {
		if err := t.EnsureWorkspaceTrusted(snap.Cwd, env); err != nil {
			fmt.Fprintf(os.Stderr, "runtime: ensure workspace trust: %v\n", err)
		}
	}

	proc, procDone, sockPath, err := s.launchProcContext(ctx, a, sess, cmd, snap.Cwd, env)
	if err != nil {
		_ = s.mgr.SetSleeping(sess, session.SleepReasonRestartFailed)
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = proc.Close()
		_ = s.mgr.SetSleeping(sess, session.SleepReasonRestartFailed)
		return err
	}

	s.wireSessionEventTap(sess)
	sess.SetProc(proc)
	procGeneration := sess.ProcessGeneration()
	s.launchInstructions.set(snap.ID, procGeneration, instructionsApplied)
	sess.SetBrokerSocket(sockPath)
	if err := s.mgr.SetRunning(sess); err != nil {
		return status.Errorf(codes.Internal, "set state: %v", err)
	}

	// Captured now, for this process. Reading them back after the process ends
	// is what let a concurrent restart — which rotates the launch id and
	// prepares a new run directory before this goroutine wakes — have its fresh
	// directory deleted and its live session archived. See processExitCleanup.
	startedSnap := sess.Snapshot()
	go s.waitForSessionProcess(sess, procDone, startedSnap.LaunchID, startedSnap.ExecutionIdentity, procGeneration)
	return nil
}

// waitForSessionProcess waits until the broker dies, then runs exit cleanup.
// During graceful shutdown (shuttingDown) the process
// died because amuxlet killed it, not because the session ended: keep the DB
// state as-is so the next amuxlet's LoadFromDB queues the session for
// auto-restart instead of permanently archiving every live topic.
func (s *server) waitForSessionProcess(sess *session.Session, procDone <-chan struct{}, startedLaunchID string, startedIdentity principal.ExecutionIdentity, procGeneration uint64) {
	<-procDone
	cleanup := processExitCleanup(startedLaunchID, sess.Snapshot().LaunchID)
	if cleanup.Archive && !s.shuttingDown.Load() {
		sess.FailActiveTurnForProcess(procGeneration, "process_exit")
		_ = s.mgr.SetArchivedAfterProcessExit(sess)
	}
	if s.isolationRuntime != nil {
		// The broker is gone. Kill whatever it left behind before the run
		// directory disappears: a broker that died without shutting down
		// (crash, SIGKILL during a restart storm) leaves its provider running
		// inside the helper, holding provider-native locks that then fail
		// every future resume of the session.
		ctx := context.Background()
		_ = s.isolationRuntime.Kill(ctx, startedIdentity, cleanup.RemoveLaunch)
		_ = s.isolationRuntime.RemoveRun(ctx, startedIdentity, cleanup.RemoveLaunch)
	}
}

// reclaimLaunchRun abandons a launch attempt that will never attach a process:
// it terminates anything the helper already started for it and removes the run
// directory PrepareRun created. Only waitForSessionProcess removes that
// directory once a process owns it; without this, every failed attempt (a
// broker that never comes up, a provider that refuses to start, an aborted
// launch) abandons a run tree of agent config and HOME projection on disk, and
// recovery retries leak one tree per attempt until the volume fills.
func (s *server) reclaimLaunchRun(identity principal.ExecutionIdentity, launchID string) {
	if s == nil || s.isolationRuntime == nil {
		return
	}
	ctx := context.Background()
	_ = s.isolationRuntime.Kill(ctx, identity, launchID)
	_ = s.isolationRuntime.RemoveRun(ctx, identity, launchID)
}

func (s *server) wireSessionEventTap(sess *session.Session) {
	sess.SetEventTap(func(ev agentstream.Event) bool {
		if err := s.mgr.RecordAgentEvent(sess, ev); err != nil {
			fmt.Fprintf(os.Stderr, "runtime: record session event: %v\n", err)
		}
		return handleRunSessionPermission(sess, ev)
	})
}

func validateRunPermissionPolicy(req *pb.RunRequest) error {
	policy := ""
	if req != nil && req.ProfileSpec != nil {
		policy = strings.TrimSpace(req.ProfileSpec.PermissionPolicy)
	}
	switch task.PermissionPolicy(policy) {
	case "", task.PolicyAllow, task.PolicyEscalateToMain, task.PolicyDenyAll:
		return nil
	default:
		return fmt.Errorf("permission_policy must be one of %s, %s, %s (got %q)", task.PolicyAllow, task.PolicyEscalateToMain, task.PolicyDenyAll, policy)
	}
}

func handleRunSessionPermission(sess *session.Session, ev agentstream.Event) bool {
	requested, ok := ev.Body.(agentstream.PermissionRequested)
	if !ok {
		return false
	}
	if requested.Request.IsUserInput() {
		return false
	}
	policy := launchSpecPermissionPolicy(sess.Snapshot().LaunchSpec)
	if policy == "" {
		return false
	}
	switch task.PermissionPolicy(policy) {
	case task.PolicyAllow, task.PolicyDenyAll:
	case task.PolicyEscalateToMain:
		return false
	default:
		return false
	}
	req := task.PermissionFromAgentEvent(requested.Request)
	snap := sess.Snapshot()
	wl := task.Whitelist{
		SandboxRoot: snap.Cwd,
		FSWrite:     []string{snap.Cwd},
	}
	d := task.Resolver{}.Resolve(task.PermissionPolicy(policy), wl, req)
	optID := req.ResolveOptionId(d.Allow)
	if optID == "" {
		return false
	}
	if err := sess.RespondPermission(requested.Request.ID, optID); err != nil {
		fmt.Fprintf(os.Stderr, "runtime: run permission policy %s response: %v\n", policy, err)
		return false
	}
	return true
}

// stripAmuxMCPArgs removes a previously injected amux MCP server entry from
// launch args so a relaunch can re-materialize it with the current credential.
// It removes only the entries our own injection writes (the app-server `-c
// mcp_servers.amux.*` triplets and the claude `--mcp-config` pair); every other
// arg is preserved verbatim.

// drop the config path that follows

// drop the mcp_servers.amux.* value that follows

func (s *server) launchProcContext(ctx context.Context, a adapter.Adapter, sess *session.Session, cmd *exec.Cmd, cwd string, env []string) (session.Proc, <-chan struct{}, string, error) {
	// Broker-owned model relays use the same trusted Hub connection as MCP.
	hubConfig := config.GetHub()
	env = memoryprotocol.SetEnv(env, "AMUX_HUB_ADDR", hubConfig.Addr)
	env = memoryprotocol.SetEnv(env, "AMUX_HUB_TOKEN", hubConfig.Token)
	snap := sess.Snapshot()
	identity := snap.ExecutionIdentity
	launchContext := isolation.LaunchContext{LaunchID: snap.LaunchID, Identity: &identity, Backend: identity.IsolationBackend}
	caps := a.Capabilities()
	providerID, watchdogKey := caps.Protocol.StructuredProvider()
	switch {
	case providerID != "":
		// Only protocols that implement watchdogs select their configuration.
		wdCfg := config.StructuredProviderConfig{
			TurnIdleTimeoutSeconds: -1,
			TurnHardCapSeconds:     -1,
			ToolIdleTimeoutSeconds: -1,
			RepeatToolCallLimit:    -1,
		}
		if watchdogKey != "" {
			wdCfg = config.GetStructuredProviderForProvider(watchdogKey)
		}
		launchArgs := cmd.Args[1:]
		launchArgs, selectedBaseURL, err := s.resolveProviderEndpoint(ctx, snap.LaunchSpec, providerID, launchArgs)
		if err != nil {
			return nil, nil, "", status.Error(codes.FailedPrecondition, err.Error())
		}
		logSelectedProviderEndpoint(snap.ID, providerID, selectedBaseURL)
		responsesChat, err := responsesChatOptionsFromLaunchSpec(snap.LaunchSpec, providerID, selectedBaseURL)
		if err != nil {
			return nil, nil, "", status.Errorf(codes.FailedPrecondition, "responses-chat provider: %v", err)
		}
		resumeModel, _ := agentprofile.AgentArgsModel(snap.AgentType, launchArgs)
		agentPID, sockPath, err := mux.LaunchStructuredContext(ctx, s.amuxletBin, snap.ID, s.amuxDir, mux.StructuredLaunchOptions{
			Provider:               providerID,
			SessionID:              snap.ID,
			ResumeSessionID:        snap.AgentSessionID,
			ResumeModel:            resumeModel,
			ResumeReasoningEffort:  launchSpecReasoningEffort(snap.LaunchSpec),
			Cwd:                    cwd,
			Env:                    env,
			ExtraArgs:              launchArgs,
			ExcludedSkillRoots:     launchSpecProviderProfileSkillRoots(snap.LaunchSpec, cwd),
			TurnIdleTimeoutSeconds: wdCfg.TurnIdleTimeoutSeconds,
			TurnHardCapSeconds:     wdCfg.TurnHardCapSeconds,
			ToolIdleTimeoutSeconds: wdCfg.ToolIdleTimeoutSeconds,
			RepeatToolCallLimit:    wdCfg.RepeatToolCallLimit,
			LaunchContext:          launchContext,
			ResponsesChat:          responsesChat,
		})
		if err != nil {
			return nil, nil, "", status.Errorf(codes.Internal, "launch structured: %v", err)
		}
		proc, procDone, err := mux.ConnectStructuredContext(ctx, sockPath, agentPID)
		if err != nil {
			_ = mux.ShutdownStructuredBroker(sockPath)
			return nil, nil, "", status.Errorf(codes.Internal, "connect structured broker: %v", err)
		}
		return proc, procDone, sockPath, nil
	case caps.Protocol == adapter.ProtocolACP:
		if err := resolveACPCommandPath(cmd, env); err != nil {
			return nil, nil, "", err
		}
		watchdog := config.GetStructuredProviderForProvider(snap.AgentType)
		model, effort := "", ""
		if _, ok := a.(adapter.ACPExecutionConfigurator); ok {
			model = launchSpecModel(snap.LaunchSpec)
			effort = launchSpecReasoningEffort(snap.LaunchSpec)
		}
		agentPID, sockPath, err := mux.LaunchACPContext(ctx, s.amuxletBin, snap.ID, s.amuxDir, mux.ACPLaunchOptions{
			Cmd:             cmd,
			Cwd:             cwd,
			AgentType:       snap.AgentType,
			Model:           model,
			ReasoningEffort: effort,
			Watchdog:        &watchdog,
			ResumeSessionID: snap.AgentSessionID,
			PromptTailDrain: caps.ACPPromptTailDrain,
			LaunchContext:   launchContext,
		})
		if err != nil {
			return nil, nil, "", status.Errorf(codes.Internal, "launch acp: %v", err)
		}
		proc, procDone, err := mux.ConnectACPContext(ctx, sockPath, agentPID)
		if err != nil {
			_ = mux.ShutdownACPBroker(sockPath)
			return nil, nil, "", status.Errorf(codes.Internal, "connect acp broker: %v", err)
		}
		return proc, procDone, sockPath, nil
	default:
		return nil, nil, "", status.Errorf(codes.FailedPrecondition, "agent type %q declares unsupported protocol %q", snap.AgentType, caps.Protocol)
	}
}

func resolveACPCommandPath(cmd *exec.Cmd, env []string) error {
	if cmd == nil || len(cmd.Args) == 0 {
		return status.Error(codes.InvalidArgument, "agent binary is empty")
	}
	bin, err := provider.ResolveExecutableFromEnvPATH(cmd.Args[0], env)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "agent binary %q not found in PATH", cmd.Args[0])
	}
	cmd.Path = bin
	return nil
}

func (s *server) List(_ context.Context, _ *pb.ListRequest) (*pb.ListResponse, error) {
	sessions := s.mgr.List()
	resp := &pb.ListResponse{Sessions: make([]*pb.Session, len(sessions))}
	for i, sess := range sessions {
		resp.Sessions[i] = toProtoSession(sess)
	}
	return resp, nil
}

func (s *server) Kill(_ context.Context, req *pb.KillRequest) (*pb.KillResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	if err := sess.Kill(); err != nil {
		return nil, status.Errorf(codes.Internal, "kill: %v", err)
	}
	if err := s.mgr.SetState(sess, session.StateArchived); err != nil {
		return nil, status.Errorf(codes.Internal, "set state: %v", err)
	}
	// Clean up adapter workspace trust so the global config does not grow
	// unbounded (same as the archive/reclaim/prune paths).
	s.cleanupSessionArtifacts(sess)
	return &pb.KillResponse{Ok: true}, nil
}

func (s *server) RevokePrincipalLaunches(ctx context.Context, req *pb.RevokePrincipalLaunchesRequest) (*pb.RevokePrincipalLaunchesResponse, error) {
	principalID := strings.TrimSpace(req.PrincipalId)
	if principalID == "" {
		return nil, status.Error(codes.InvalidArgument, "principal_id is required")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "principal_revoked"
	}
	var affected int32
	for _, sess := range s.mgr.List() {
		snap := sess.Snapshot()
		if snap.ExecutionIdentity.EffectivePrincipalID != principalID {
			continue
		}
		if req.DelegationId != "" && snap.ExecutionIdentity.DelegationID != req.DelegationId {
			continue
		}
		_ = sess.Kill()
		if s.isolationRuntime != nil {
			_ = s.isolationRuntime.RemoveRun(context.Background(), snap.ExecutionIdentity, snap.LaunchID)
		}
		sess.AppendReplay([]byte("\r\n[system] execution revoked: " + reason + "\r\n"))
		_ = s.mgr.SetArchived(sess, reason)
		affected++
	}
	if req.DelegationId == "" && s.isolationRuntime != nil {
		if err := s.isolationRuntime.KillPrincipal(ctx, principalID); err != nil {
			return nil, status.Errorf(codes.Internal, "kill principal: %v", err)
		}
	}
	return &pb.RevokePrincipalLaunchesResponse{Affected: affected}, nil
}

func (s *server) Restart(ctx context.Context, req *pb.RestartRequest) (*pb.RunResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	s.adoptAgentActorToken(sess, req.AgentActorToken, req.AgentActorTokenExpiresAt)
	if req.Replacement != nil {
		return s.reconfigureExecutionAndRelaunch(ctx, sess, req.Replacement, req.PreserveHistory)
	}
	if req.RefreshProfile {
		return s.refreshProfileAndRelaunch(ctx, sess)
	}
	return s.relaunchSession(sess, "restarting")
}

// Same rule as a relaunch: the rewritten MCP config must carry the
// session credential, or the cwd switch would silently downgrade the
// MCP server to anonymous.

// The prior ExtraArgs are a flattened launch artifact: they mix defaults,
// model/provider projection, MCP injection, and explicit user args without
// provenance. Reusing them would let an old --model/-c override the refreshed
// profile (the bug this operation is meant to fix), so refresh rebuilds args
// from the current profile. Env is already a durable session snapshot with no
// equivalent provenance boundary, so preserve it byte-for-byte and refresh
// only credential env through the new ProfileSpec.

// Auto-select model for the refreshed profile if no explicit model is set.

// All validation/materialization above happens while the old proc is still
// available; from here on the operation commits the refreshed snapshot.
// Workspace mutation happens only after the old launch is stopped and a new
// run has been prepared, so linux-dac never writes through the service
// identity into a principal-owned live workspace.

func (s *server) Sleep(_ context.Context, req *pb.SleepRequest) (*pb.SleepResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	state := sess.StateSnapshot()
	if state == session.StateArchived {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q is archived; cannot sleep", req.Name)
	}
	if state == session.StateSleeping {
		return &pb.SleepResponse{Ok: true}, nil
	}
	s.relaunchMu.Lock()
	defer s.relaunchMu.Unlock()
	state = sess.StateSnapshot()
	if state == session.StateArchived {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q is archived; cannot sleep", req.Name)
	}
	if state == session.StateSleeping {
		return &pb.SleepResponse{Ok: true}, nil
	}
	if err := s.sleepSession(sess, session.SleepReasonManual); err != nil {
		return nil, status.Errorf(codes.Internal, "set sleeping: %v", err)
	}
	return &pb.SleepResponse{Ok: true}, nil
}

func (s *server) sleepSession(sess *session.Session, reason string) error {
	if err := sess.Kill(); err != nil {
		return fmt.Errorf("sleep: %w", err)
	}
	msg := "\r\n[system] sleeping session\r\n"
	if reason == session.SleepReasonIdleTimeout {
		msg = "\r\n[system] sleeping session (idle_timeout)\r\n"
	}
	sess.RecordReplayEvent(session.Event{Kind: session.EventOutput, Data: []byte(msg), Source: "system"})
	return s.mgr.SetSleeping(sess, reason)
}

// archiveSession kills the session process (best-effort), records the archived
// state, and cleans up runtime-owned artifacts. The system notice is emitted via
// Source "system" so it does not bump the reclaim clock.
func (s *server) archiveSession(sess *session.Session, reason string) error {
	_ = sess.Kill() // best-effort; may already be dead
	msg := "\r\n[system] archived (idle reclaim)\r\n"
	sess.RecordReplayEvent(session.Event{Kind: session.EventOutput, Data: []byte(msg), Source: "system"})
	if err := s.mgr.SetArchived(sess, reason); err != nil {
		return err
	}
	s.cleanupSessionArtifacts(sess)
	return nil
}

// reclaimSessions runs one staged-reclaim sweep, advancing at most one stage per
// session per call. Task-owned sessions are never reclaimed here. Sleep/archive
// transitions re-check eligibility under relaunchMu (mirroring relaunch
// serialization). Prune is a batch DB op applied per workspace source so local
// and feishu can use different TTLs.
func (s *server) reclaimSessions(now time.Time, localPol, feishuPol session.ReclaimPolicy) int {
	done, err := s.maintenance.enter(true)
	if err != nil {
		return 0
	}
	defer done()
	acted := 0
	for _, sess := range s.mgr.List() {
		snap := sess.Snapshot()
		if snap.OwnerTaskId != "" {
			continue
		}
		pol := session.ResolveReclaimPolicy(snap.WorkspaceSource, localPol, feishuPol)
		switch sess.ReclaimAction(now, pol) {
		case session.ReclaimSleep:
			s.relaunchMu.Lock()
			if sess.ReclaimAction(now, pol) == session.ReclaimSleep { // re-check under lock
				if err := s.sleepSession(sess, session.SleepReasonIdleTimeout); err != nil {
					fmt.Fprintf(os.Stderr, "runtime: reclaim sleep %s: %v\n", sess.Name, err)
				} else {
					acted++
				}
			}
			s.relaunchMu.Unlock()
		case session.ReclaimArchive:
			// A long-dormant session (idle already past PruneAfter, e.g. after
			// amuxlet downtime) would otherwise emit a "[system] archived"
			// replay+broadcast here and then be deleted by the batch prune below in
			// the same tick — a wasted, confusing notice on a session removed
			// milliseconds later. When the session is already prune-eligible, archive
			// it silently (state transition + artifact cleanup, no replay/broadcast)
			// so the batch prune still matches and deletes it this same tick.
			if pol.PruneAfter > 0 && now.Sub(sess.InteractionAt()) >= pol.PruneAfter {
				s.relaunchMu.Lock()
				if sess.ReclaimAction(now, pol) == session.ReclaimArchive { // re-check under lock
					_ = sess.Kill() // best-effort; prune deletes the record but won't reap the process
					if err := s.mgr.SetArchived(sess, session.SleepReasonIdleTimeout); err != nil {
						fmt.Fprintf(os.Stderr, "runtime: reclaim archive %s: %v\n", sess.Name, err)
					}
				}
				s.relaunchMu.Unlock()
				break
			}
			s.relaunchMu.Lock()
			if sess.ReclaimAction(now, pol) == session.ReclaimArchive { // re-check under lock
				if err := s.archiveSession(sess, session.SleepReasonIdleTimeout); err != nil {
					fmt.Fprintf(os.Stderr, "runtime: reclaim archive %s: %v\n", sess.Name, err)
				} else {
					acted++
				}
			}
			s.relaunchMu.Unlock()
		case session.ReclaimPrune, session.ReclaimNone:
			// prune handled in batch below; none = skip
		}
	}
	// Prune is a batch DB op; local and feishu may use different TTLs, so prune
	// each source independently via the match predicate.
	if feishuPol.PruneAfter > 0 {
		pruned, err := s.mgr.PruneArchivedOlderThan(now, feishuPol.PruneAfter, func(se *session.Session) bool {
			return se.Snapshot().WorkspaceSource == "feishu"
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "runtime: reclaim prune (feishu): %v\n", err)
		}
		// PruneArchivedOlderThan returns the partial set deleted before a mid-batch
		// DB failure, so clean up artifacts for whatever was pruned even on error.
		for _, se := range pruned {
			s.cleanupSessionArtifacts(se)
			acted++
		}
	}
	if localPol.PruneAfter > 0 {
		pruned, err := s.mgr.PruneArchivedOlderThan(now, localPol.PruneAfter, func(se *session.Session) bool {
			return se.Snapshot().WorkspaceSource != "feishu"
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "runtime: reclaim prune (local): %v\n", err)
		}
		for _, se := range pruned {
			s.cleanupSessionArtifacts(se)
			acted++
		}
	}
	return acted
}

func (s *server) Resume(_ context.Context, req *pb.ResumeRequest) (*pb.RunResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	s.adoptAgentActorToken(sess, req.AgentActorToken, req.AgentActorTokenExpiresAt)
	return s.relaunchSession(sess, "resuming")
}

// adoptAgentActorToken replaces the stored credential when the hub minted a
// fresh one for this relaunch. Relaunch otherwise replays what creation stored,
// so this is what makes "restart the session" an actual recovery from an
// expired credential rather than a re-run of the same dead one. A hub that did
// not mint (unidentified session, no principal store, a direct runtime call)
// leaves the stored credential untouched.

// Relaunch credential materialization must use the same principal scope as
// creation. Resolve the stored token before any provider lookup.

// Tear down the previous proc if still alive; ignore errors (it may
// already be dead, which is exactly the case we want to restart from).

// A relaunch replays the creation-time MCP server config, which carries
// whatever credential the creation build wrote — none for sessions created
// before the token was projected into mcp_servers.amux.env. app servers do
// not inherit the agent env into MCP subprocesses, so a tokenless MCP server
// is permanent until the args are refreshed. Replace our own injected entry
// with one carrying the relaunch credential (or the stored one), leaving any
// non-amux args untouched.

// A relaunch reuses the credential minted when the session was created: the
// principal it acts as has not changed, and only the hub can mint a new one.
// A caller that already holds a fresh credential (a task's follow-up resume)
// passes it explicitly; an empty one falls back to the credential stored at
// creation, which is what an ordinary session relaunch replays.

// Prune removes all archived (dead) sessions from the runtime, deleting their
// DB records and cleaning up runtime-owned broker/profile/workspace artifacts.
func (s *server) Prune(_ context.Context, _ *pb.PruneRequest) (*pb.PruneResponse, error) {
	pruned, err := s.mgr.PruneArchived()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "prune: %v", err)
	}
	names := make([]string, 0, len(pruned))
	for _, sess := range pruned {
		names = append(names, sess.Name)
		s.cleanupSessionArtifacts(sess)
	}
	// Also reap orphaned task child sessions: archived sessions whose owning task
	// record no longer exists. PruneArchived deliberately skips all owned
	// sessions, so without this they would never be collected.
	orphans, oerr := s.reapOrphanChildSessions()
	if oerr != nil {
		return nil, status.Errorf(codes.Internal, "prune orphans: %v", oerr)
	}
	names = append(names, orphans...)
	// Also reap orphan broker processes left over from ungraceful restarts, and
	// any broker files for sessions no longer in the DB. Best-effort: a reaper
	// error must not fail the prune that already succeeded on the DB side.
	if n, rerr := s.reapOrphanBrokers(); rerr != nil {
		fmt.Fprintf(os.Stderr, "runtime: broker reap on prune: %v\n", rerr)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "runtime: broker reap on prune: reaped %d orphans\n", n)
	}
	return &pb.PruneResponse{Count: int32(len(names)), Names: names}, nil
}

// reapOrphanChildSessions deletes archived task-owned child sessions whose owner
// task record no longer exists. Live task children (owner task still present)
// are left untouched — the task lifecycle owns those. With taskExists unset it
// is a no-op so a child session is never reaped without a live existence check.
func (s *server) reapOrphanChildSessions() ([]string, error) {
	if s.taskExists == nil {
		return nil, nil
	}
	var reaped []string
	for _, sess := range s.mgr.List() {
		snap := sess.Snapshot()
		if snap.OwnerTaskId == "" || snap.State != session.StateArchived {
			continue
		}
		exists, err := s.taskExists(snap.OwnerTaskId)
		if err != nil {
			// Conservative: a transient store read failure must not delete a
			// session. Skip and let a later sweep retry.
			fmt.Fprintf(os.Stderr, "runtime: reap orphan %s: check owner task %s: %v\n", snap.Name, snap.OwnerTaskId, err)
			continue
		}
		if exists {
			continue
		}
		deleted, ok, derr := s.mgr.DeleteArchivedOwned(snap.OwnerTaskId, snap.ID)
		if derr != nil {
			return reaped, derr
		}
		if !ok || deleted == nil {
			continue
		}
		s.cleanupSessionArtifacts(deleted)
		_ = os.RemoveAll(filepath.Join(task.SandboxesDir(s.amuxDir), snap.OwnerTaskId))
		reaped = append(reaped, deleted.Name)
	}
	return reaped, nil
}

func (s *server) cleanupSessionArtifacts(sess *session.Session) {
	if sess == nil {
		return
	}
	snap := sess.Snapshot()
	if snap.BrokerSocket != "" {
		_ = os.Remove(snap.BrokerSocket)
		// The broker pid file is always <sid>.pid regardless of whether the
		// socket is <sid>.acp.sock or <sid>.structured.sock. Deriving it from the
		// socket extension produced a non-existent <sid>.acp.pid and left the
		// real pid file behind.
		_ = os.Remove(filepath.Join(s.amuxDir, "sessions", snap.ID+".pid"))
	}
	_ = os.RemoveAll(profileSessionDir(s.amuxDir, snap.ID))
	removeSessionActorToken(s.amuxDir, snap.ID)
	removeSessionInstructions(s.amuxDir, snap.ID)
	// Clean up adapter-specific workspace trust entries so the global config
	// does not grow unbounded.
	// This must run before the workspace dir removal below, because the cleanup
	// resolves symlinks on cwd and needs the directory to still exist for a
	// stable key matching what EnsureWorkspaceTrusted wrote.
	if a, err := adapter.Get(snap.AgentType); err == nil {
		if t, ok := a.(adapter.WorkspaceTruster); ok && hostWorkspaceTrust(snap) {
			_ = t.CleanupWorkspaceTrust(snap.Cwd, snap.Env)
		}
		s.cleanupDistributionSessionState(snap)
	}
	if isRuntimeOwnedWorkspace(s.amuxDir, snap) {
		_ = os.RemoveAll(snap.Cwd)
		return
	}
	// The isolated equivalent: the principal workspace is runtime-created too,
	// but owned by the principal at 0700, so only the helper can reclaim it.
	if s.isolationRuntime != nil && !principalWorkspaceInUse(snap, s.sessionSnapshots()) {
		_ = s.isolationRuntime.RemoveWorkspace(context.Background(), snap.ExecutionIdentity, snap.Cwd)
	}
}

// reportLaunchWarning surfaces a non-fatal launch finding on a relaunch path,
// where there is no creation response to carry it. The replay line is what an
// attached client (including the Feishu bridge) can still see.
func (s *server) reportLaunchWarning(sess *session.Session) func(string) {
	return func(msg string) {
		fmt.Fprintf(os.Stderr, "runtime: %s\n", msg)
		if sess != nil {
			sess.AppendReplay([]byte("\r\n[system] " + msg + "\r\n"))
		}
	}
}

func (s *server) sessionSnapshots() []session.Snapshot {
	sessions := s.mgr.List()
	snaps := make([]session.Snapshot, 0, len(sessions))
	for _, sess := range sessions {
		snaps = append(snaps, sess.Snapshot())
	}
	return snaps
}

func (s *server) hasRunningAgent(agentType string) bool {
	for _, se := range s.mgr.List() {
		sn := se.Snapshot()
		if sn.AgentType == agentType && sn.State == session.StateRunning {
			return true
		}
	}
	return false
}

func (s *server) Interrupt(_ context.Context, req *pb.InterruptRequest) (*pb.InterruptResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	snap := sess.Snapshot()
	caps := adapterCapabilities(snap.AgentType)
	if !caps.Interrupt {
		return nil, status.Errorf(codes.FailedPrecondition, "interrupt unsupported for agent %s", snap.AgentType)
	}
	interrupt := sess.Interrupt
	if req.PreserveGoal {
		interrupt = sess.InterruptForInput
	}
	if err := interrupt(); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "interrupt: %v", err)
	}
	return &pb.InterruptResponse{Ok: true}, nil
}

// Install the live subscription atomically with the structured snapshot.
// Non-replay surfaces still need the latest turn-neutral capability state
// (notably available_commands), but must not receive transcript/tool/result
// history as a side effect.

// Non-replay consumers such as the Feishu bridge need an explicit Attach
// handshake before they submit the first input. Without it, asynchronous
// stream startup can miss the input event and later suppress the otherwise
// valid provider turn as non-user-initiated output.

// Send replay buffer first.

// A typed provider error with no output payload (e.g. a bare
// EventError) becomes a first-class ERROR attach event so attach and
// the Feishu bridge can render it as an error rather than dropping it
// (empty-Data OUTPUT frames are dropped by consumers and not
// replayed). The visible [system] frame is carried separately as an
// OUTPUT frame, so replay/line-mode rendering still works.

func (s *server) Submit(ctx context.Context, req *pb.SubmitRequest) (_ *pb.SubmitResponse, retErr error) {
	requestID := tunnel.RequestID(ctx)
	var sessionID, launchID string
	defer func() {
		if retErr != nil {
			log.Warn().Err(retErr).Str("session_id", sessionID).Str("session", req.GetName()).
				Str("launch_id", launchID).Str("transport_request_id", requestID).Msg("session.submit.failed")
		}
	}()
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	snap := sess.Snapshot()
	sessionID, launchID = snap.ID, snap.LaunchID
	if (req.ExpectedSessionId != "" && req.ExpectedSessionId != snap.ID) || (req.ExpectedLaunchId != "" && req.ExpectedLaunchId != snap.LaunchID) {
		return nil, status.Error(codes.FailedPrecondition, "session execution changed after file materialization")
	}
	if req.GetLocalExpertAuthoring() && !tunnel.IsTrustedSubmit(ctx) {
		return nil, status.Error(codes.PermissionDenied, "local expert authoring grant requires trusted tunnel provenance")
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	if req.GetManageSystemInstructions() {
		// Persist before waking the session so a launch triggered by this
		// submit already carries the instructions out of band.
		writeSessionInstructions(s.amuxDir, snap.ID, req.GetSystemInstructions())
	}
	if err := s.ensureSessionActive(sess); err != nil {
		return nil, err
	}
	input, err := sessioninput.FromProto(req.Parts, req.Text)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "submit input: %v", err)
	}
	if err := sessioninput.Validate(input, sessioninput.SupportsImage(snap.InputModalities)); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "submit input: %v", err)
	}
	var item session.QueueItem
	authorization := session.TurnAuthorization{LocalExpertAuthoring: req.LocalExpertAuthoring}
	delivery, steerFallback := "", ""
	if req.GetSteer() {
		if strings.TrimSpace(req.Control) != "" || req.Preempt {
			return nil, status.Error(codes.InvalidArgument, "steer cannot be combined with preempt or session control")
		}
		steerSource := session.SteerNoteSourceUser
		if req.Source == "coordinator" {
			steerSource = session.SteerNoteSourceCoordinator
		}
		turnID, steerErr := sess.SteerInput(ctx, req.Source, session.FrameSteerInput(steerSource, input), req.GetSteerExpectedTurnId(), authorization)
		if steerErr == nil {
			if err := s.mgr.RecordActivity(sess, time.Now(), true); err != nil {
				return nil, status.Errorf(codes.Internal, "record activity: %v", err)
			}
			log.Info().Str("session_id", snap.ID).Str("session", snap.Name).Str("launch_id", snap.LaunchID).
				Str("turn_id", turnID).Str("transport_request_id", requestID).Msg("session.submit.steered")
			return &pb.SubmitResponse{Ok: true, TurnId: turnID, SessionId: snap.ID, LaunchId: snap.LaunchID, Delivery: "steered"}, nil
		}
		if errors.Is(steerErr, session.ErrSteerOutcomeUnknown) {
			// The turn may already have the input; queueing it as well could
			// run it twice.
			log.Warn().Err(steerErr).Str("session", snap.Name).Str("transport_request_id", requestID).Msg("session.submit.steer_outcome_unknown")
			return nil, status.Errorf(codes.Unknown, "steer outcome unknown; the input was not queued again to avoid running it twice: %v", steerErr)
		}
		// Fall back to ordinary queued input: it runs after the active turn.
		delivery, steerFallback = "queued", steerErr.Error()
		log.Info().Err(steerErr).Str("session", snap.Name).Str("transport_request_id", requestID).Msg("session.submit.steer_fallback_queue")
		req.Async = true
	}
	instructionsDelivery := ""
	if req.GetManageSystemInstructions() && strings.TrimSpace(req.Control) == "" {
		var inline string
		instructionsDelivery, inline = s.sessionInstructionsDelivery(sess, req.GetSystemInstructions())
		input = appendInstructionsInline(input, inline)
	}
	switch {
	case strings.TrimSpace(req.Control) != "":
		control := strings.TrimSpace(req.Control)
		if control != "compact" && control != "clear" {
			return nil, status.Errorf(codes.InvalidArgument, "unknown session control %q", control)
		}
		agentType := strings.TrimSpace(sess.Snapshot().AgentType)
		if !adapter.IsCodexCompatible(agentType) {
			return nil, status.Errorf(codes.FailedPrecondition, "session control %q is not supported by agent %q", control, agentType)
		}
		if req.Preempt {
			return nil, status.Error(codes.InvalidArgument, "session controls cannot preempt an active turn")
		}
		if !req.Async {
			return nil, status.Error(codes.InvalidArgument, "session controls must be submitted asynchronously")
		}
		err = sess.SubmitControlAsync(req.Source, req.Text, control)
	case req.Preempt && req.PreemptAlreadyInterrupted:
		item, err = sess.SubmitPreemptInputWaitWithAuthorization(ctx, req.Source, input, false, authorization)
	case req.Preempt:
		item, err = sess.SubmitPreemptInputWaitWithAuthorization(ctx, req.Source, input, true, authorization)
	case req.Async:
		item, err = sess.SubmitInputAsyncWithAuthorization(req.Source, input, authorization)
	default:
		item, err = sess.SubmitInputWithAuthorization(ctx, req.Source, input, authorization)
	}
	if err != nil {
		if errors.Is(err, session.ErrQueueItemCancelled) {
			return nil, status.Errorf(codes.Canceled, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "submit: %v", err)
	}
	if err := s.mgr.RecordActivity(sess, time.Now(), true); err != nil {
		return nil, status.Errorf(codes.Internal, "record activity: %v", err)
	}
	snap = sess.Snapshot()
	sessionID, launchID = snap.ID, snap.LaunchID
	log.Info().Str("session_id", snap.ID).Str("session", snap.Name).Str("launch_id", snap.LaunchID).
		Str("turn_id", item.TurnID).Str("transport_request_id", requestID).Msg("session.submit.accepted")
	return &pb.SubmitResponse{Ok: true, TurnId: item.TurnID, SessionId: snap.ID, LaunchId: snap.LaunchID, Delivery: delivery, SteerFallbackReason: steerFallback, InstructionsDelivery: instructionsDelivery}, nil
}

func (s *server) GetActiveTurn(_ context.Context, req *pb.ActiveTurnRequest) (*pb.ActiveTurnResponse, error) {
	sess, err := s.mgr.Get(req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	turnID, _, active := sess.ActiveTurnAuthorization()
	snap := sess.Snapshot()
	return &pb.ActiveTurnResponse{
		Active:          active,
		TurnId:          turnID,
		SessionId:       snap.ID,
		LaunchId:        snap.LaunchID,
		EffectiveModel:  strings.TrimSpace(snap.EffectiveModel),
		ReasoningEffort: launchSpecReasoningEffort(snap.LaunchSpec),
	}, nil
}

func (s *server) GetLocalExpertAuthoringTurn(_ context.Context, req *pb.LocalExpertAuthoringTurnRequest) (*pb.LocalExpertAuthoringTurnResponse, error) {
	sess, err := s.mgr.Get(req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	turnID, authorization, active := sess.ActiveTurnAuthorization()
	return &pb.LocalExpertAuthoringTurnResponse{
		Active: active, TurnId: turnID, Allowed: active && authorization.LocalExpertAuthoring,
	}, nil
}

func (s *server) RespondPermission(_ context.Context, req *pb.RespondPermissionRequest) (*pb.RespondPermissionResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	if req.TurnId != "" {
		turnID, _, active := sess.ActiveTurnAuthorization()
		if !active || turnID != req.TurnId {
			return nil, status.Error(codes.FailedPrecondition, "user_input_expired")
		}
	}
	if err := s.ensureSessionActive(sess); err != nil {
		return nil, err
	}
	if req.Cancel {
		if req.OptionId != "" || len(req.OptionIds) > 0 {
			return nil, status.Error(codes.InvalidArgument, "cancellation cannot include answers")
		}
		if err := sess.CancelUserInput(req.PermissionId); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "cancel user input: %v", err)
		}
	} else if len(req.OptionIds) > 0 {
		if err := sess.RespondPermissionMulti(req.PermissionId, req.OptionIds); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "permission response: %v", err)
		}
	} else if err := sess.RespondPermission(req.PermissionId, req.OptionId); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "permission response: %v", err)
	}
	if err := s.mgr.RecordActivity(sess, time.Now(), true); err != nil {
		return nil, status.Errorf(codes.Internal, "record activity: %v", err)
	}
	return &pb.RespondPermissionResponse{Ok: true}, nil
}

func (s *server) QueueList(_ context.Context, req *pb.QueueListRequest) (*pb.QueueListResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	items := sess.QueueSnapshot()
	resp := &pb.QueueListResponse{Items: make([]*pb.QueueItem, len(items))}
	for i, item := range items {
		resp.Items[i] = toProtoQueueItem(item)
	}
	return resp, nil
}

func (s *server) QueueClear(_ context.Context, req *pb.QueueClearRequest) (*pb.QueueClearResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	return &pb.QueueClearResponse{Cleared: int32(sess.ClearQueue())}, nil
}

func (s *server) QueueCancel(_ context.Context, req *pb.QueueCancelRequest) (*pb.QueueCancelResponse, error) {
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if err := ownerGuard(sess); err != nil {
		return nil, err
	}
	if !sess.CancelQueueItem(req.Id) {
		return nil, status.Errorf(codes.NotFound, "queue item %q not found", req.Id)
	}
	return &pb.QueueCancelResponse{Ok: true}, nil
}

func toProtoSession(s *session.Session) *pb.Session {
	snap := s.Snapshot()
	caps := adapterCapabilities(snap.AgentType)
	return &pb.Session{
		Id:                    snap.ID,
		Name:                  snap.Name,
		AgentProfile:          snap.AgentProfile,
		EffectiveModel:        snap.EffectiveModel,
		InputModalities:       append([]string(nil), snap.InputModalities...),
		InputCapabilitySource: snap.InputCapabilitySource,
		GoalSupported:         s.GoalSupported(),
		AgentType:             snap.AgentType,
		CwdSwitch:             string(caps.CwdSwitch),
		State:                 string(snap.State),
		Pid:                   int32(snap.PID),
		Cwd:                   snap.Cwd,
		ExtraArgs:             append([]string(nil), snap.ExtraArgs...),
		Env:                   append([]string(nil), snap.Env...),
		WorkspaceSource:       snap.WorkspaceSource,
		WorkspaceScope:        snap.WorkspaceScope,
		OwnerTaskId:           snap.OwnerTaskId,
		AgentInstanceId:       snap.AgentInstanceID,
		ExecutionIdentity:     principal.ToProtoIdentity(snap.ExecutionIdentity),
		LaunchId:              snap.LaunchID,
		CreatedAt:             snap.CreatedAt.Unix(),
		UpdatedAt:             snap.UpdatedAt.Unix(),
		Protocol:              string(caps.Protocol),
		Acp:                   caps.Acp,
		Interrupt:             caps.Interrupt,
		IdleTimeoutSeconds:    int64(snap.IdleTimeoutSeconds),
		LastActivityAt:        sessionUnix(snap.LastActivityAt),
		LastSleepReason:       snap.LastSleepReason,
		AutoSleepEligible:     s.AutoSleepEligible(time.Now()),
	}
}

func sessionUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func toProtoQueueItem(item session.QueueItem) *pb.QueueItem {
	return &pb.QueueItem{
		Id:        item.ID,
		Source:    item.Source,
		Text:      item.Text,
		CreatedAt: item.CreatedAt.Unix(),
	}
}

func adapterCapabilities(agentType string) adapter.Capabilities {
	a, err := adapter.Get(agentType)
	if err != nil {
		return adapter.Capabilities{}
	}
	return a.Capabilities()
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
