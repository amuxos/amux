package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) reconfigureExecutionAndRelaunch(ctx context.Context, sess *session.Session, replacement *pb.RunRequest, preserveHistory bool) (*pb.RunResponse, error) {
	s.relaunchMu.Lock()
	defer s.relaunchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if replacement == nil {
		return nil, status.Error(codes.InvalidArgument, "replacement execution is required")
	}
	snap := sess.Snapshot()
	// Callers that only change the model (CLI set-model, MCP session_set_model)
	// do not resolve the profile registry; the replacement carries only the
	// agent type and the requested model args. Re-project through the saved
	// explicit runtime or the profile registry so provider-backed models get
	// their full provider configuration; fall back to backfilling the frozen
	// profile identity when a legacy profile cannot be resolved.
	if replacement.ProfileSpec == nil {
		projected, err := s.reprojectSetModelReplacement(snap, replacement)
		if err != nil {
			return nil, err
		}
		if !projected {
			var saved struct {
				ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
			}
			if decodeLaunchSpec(snap.LaunchSpec, &saved) && saved.ProfileSpec != nil {
				replacement.ProfileSpec = saved.ProfileSpec
				// The caller's args are the authority on a model-only switch; the
				// frozen spec still names the launch-time model.
				if model, ok := agentprofile.AgentArgsModel(replacement.AgentType, replacement.ExtraArgs); ok {
					replacement.ProfileSpec.Model = model
				}
			}
		}
	}
	if strings.TrimSpace(replacement.AgentProfile) == "" {
		replacement.AgentProfile = snap.AgentProfile
	}
	replacement.Name = snap.Name
	replacement.Cwd = snap.Cwd
	replacement.WorkspaceSource = snap.WorkspaceSource
	replacement.WorkspaceScope = snap.WorkspaceScope
	replacement.IdleTimeoutSeconds = int64(snap.IdleTimeoutSeconds)
	replacement.IdleTimeoutSecondsSet = snap.IdleTimeoutSet
	if strings.TrimSpace(replacement.RunAs) != "" {
		return nil, status.Error(codes.Unauthenticated, "replacement run_as must be resolved before runtime dispatch")
	}
	a, err := adapter.Get(replacement.AgentType)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := validateRunPermissionPolicy(replacement); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	materializationCwd := snap.Cwd
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		if err := os.MkdirAll(s.amuxDir, 0o700); err != nil {
			return nil, status.Errorf(codes.Internal, "create reconfigure staging parent: %v", err)
		}
		materializationCwd, err = os.MkdirTemp(s.amuxDir, "isolation-reconfigure-*")
		if err != nil {
			return nil, status.Errorf(codes.Internal, "create reconfigure staging workspace: %v", err)
		}
		defer os.RemoveAll(materializationCwd)
	}

	runExtraArgs := append([]string(nil), replacement.ExtraArgs...)
	storedToken, storedTokenExpiresAt := readSessionActorToken(s.amuxDir, snap.ID)
	if !s.disableMCPAutoInject && !runArgsContainAmuxMCP(replacement.AgentType, runExtraArgs) {
		mcpArgs, err := materializeAmuxMCPForRun(replacement.AgentType, snap.ID, s.amuxDir, materializationCwd, storedToken, memoryprotocol.Binding(snap.Env))
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "reconfigure MCP materialization: %v", err)
		}
		runExtraArgs = append(runExtraArgs, mcpArgs...)
	}
	credentialCtx := sessionCredentialMaterializationContext(ctx, snap.ExecutionIdentity, storedToken)
	providerEnv, err := s.materializeProviderCredentialEnv(credentialCtx, snap.ExecutionIdentity, replacement)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "reconfigure provider credential: %v", err)
	}
	recordEnv := memoryprotocol.SetEnv(ensureLaunchEnvPATH(replacement.Env), memoryprotocol.BindingEnv, memoryprotocol.Binding(snap.Env))
	runEnv := s.launchEnvForIdentity(snap.ExecutionIdentity, append(append([]string{}, providerEnv...), recordEnv...))
	effectiveModel := effectiveRunModel(replacement)
	runExtraArgs, effectiveModel = s.maybeAutoSelectModel(a, replacement.AgentType, snap.Cwd, runEnv, runExtraArgs, effectiveModel)
	replacement.ExtraArgs = append([]string(nil), runExtraArgs...)
	launchSpec := launchSpecSnapshot(replacement)
	if hot, err := s.tryHotReconfigureExecution(sess, snap, replacement, runExtraArgs, recordEnv, effectiveModel, launchSpec); err != nil {
		return nil, err
	} else if hot {
		return &pb.RunResponse{Session: toProtoSession(sess)}, nil
	}

	handoffContext := ""
	fallbackHandoff := ""
	preserveProviderSession := preserveHistory &&
		strings.TrimSpace(snap.AgentType) == strings.TrimSpace(replacement.AgentType) &&
		providerNativeResumeCompatible(snap.LaunchSpec, replacement)
	if preserveHistory {
		fallbackHandoff, err = s.mgr.ConversationHandoff(sess)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "capture conversation history: %v", err)
		}
		if !preserveProviderSession {
			handoffContext = fallbackHandoff
		}
	}

	_ = sess.Kill()
	principalPaths, err := s.prepareRelaunchIsolation(ctx, sess, snap)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "prepare reconfigured launch: %v", err)
	}
	// PrepareRun already created run/<launch-id> for the reconfigured attempt.
	// Reclaim it on every exit that does not attach this attempt's process,
	// including the restore path below: restoring rotates to a fresh launch
	// id, so the captured id can never name the restored directory.
	prepared := sess.Snapshot()
	attached := false
	defer func() {
		if !attached {
			s.reclaimLaunchRun(prepared.ExecutionIdentity, prepared.LaunchID)
		}
	}()
	if err := materializeProfileWorkspace(replacement, materializationCwd, "", s.amuxDir); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "reconfigure profile materialization: %v", err)
	}
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		currentLaunchID := sess.Snapshot().LaunchID
		runEnv, recordEnv, runExtraArgs, err = materializeLinuxDACSessionLaunch(func(sources []isolation.BundleSource) error {
			return s.isolationRuntime.MaterializeExecution(ctx, snap.ExecutionIdentity, currentLaunchID, principalPaths.Workspace, sources)
		}, materializationCwd, profileSessionDir(s.amuxDir, snap.ID), snap.LaunchID, principalPaths,
			isolatedHomeProjectionFor(replacement.AgentType, s.reportLaunchWarning(sess)), runEnv, recordEnv, runExtraArgs)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "materialize reconfigured isolation launch bundle: %v", err)
		}
	}
	runEnv, recordEnv = applyAgentActorToken(runEnv, recordEnv, storedToken, sessionActorTokenPath(s.amuxDir, snap.ID))
	if warning := agentActorTokenExpiryWarning(storedTokenExpiresAt, time.Now()); warning != "" {
		s.reportLaunchWarning(sess)(warning)
	}
	s.mgr.ResetForRestart(sess)
	sess.AppendReplay([]byte("\r\n[system] switching execution runtime/model while retaining conversation\r\n"))
	if err := s.mgr.RecordReconfiguredLaunchParams(sess, replacement.AgentType, runExtraArgs, recordEnv,
		snap.WorkspaceSource, snap.WorkspaceScope, replacement.AgentProfile, effectiveModel, launchSpec,
		handoffContext, preserveProviderSession); err != nil {
		return nil, status.Errorf(codes.Internal, "record reconfigured execution: %v", err)
	}
	launchErr := s.startSessionContext(ctx, a, sess, runExtraArgs, runEnv)
	if launchErr == nil {
		attached = true
		return &pb.RunResponse{Session: toProtoSession(sess)}, nil
	}
	if preserveProviderSession && missingNativeConversationOnResume(replacement.AgentType, launchErr) {
		s.mgr.ResetForRestart(sess)
		if err := s.mgr.RecordReconfiguredLaunchParams(sess, replacement.AgentType, runExtraArgs, recordEnv,
			snap.WorkspaceSource, snap.WorkspaceScope, replacement.AgentProfile, effectiveModel, launchSpec,
			fallbackHandoff, false); err != nil {
			launchErr = fmt.Errorf("native resume failed: %v; prepare fresh provider conversation: %w", launchErr, err)
		} else if retryErr := s.startSessionContext(ctx, a, sess, runExtraArgs, runEnv); retryErr == nil {
			sess.AppendReplay([]byte("\r\n[system] provider history was unavailable; continuing from amux conversation handoff\r\n"))
			attached = true
			return &pb.RunResponse{Session: toProtoSession(sess)}, nil
		} else {
			launchErr = fmt.Errorf("native resume failed: %v; fresh provider conversation failed: %w", launchErr, retryErr)
		}
	}
	restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer restoreCancel()
	if restoreErr := s.restorePreviousExecution(restoreCtx, sess, snap, storedToken, storedTokenExpiresAt, fallbackHandoff); restoreErr != nil {
		return nil, status.Errorf(codes.Internal, "switch execution: %v; restore previous execution: %v", launchErr, restoreErr)
	}
	sess.AppendReplay([]byte("\r\n[system] execution switch failed; previous execution restored\r\n"))
	return nil, status.Errorf(codes.Internal, "switch execution: %v; previous execution restored", launchErr)
}

// reprojectSetModelReplacement rebuilds a model-only replacement through the
// saved explicit runtime or Agent Profile registry so provider-backed targets
// carry the full provider projection (process-global provider config, profile
// spec snapshot, default env). The requested model comes from the caller's args;
// the session's stored args are the base from which the
// previous execution selection is stripped from. Returns false when the
// registry cannot resolve the session's profile, letting the caller fall back
// to the frozen-snapshot patch behavior; a profile that resolves but rejects
// the model is an InvalidArgument error.
func (s *server) reprojectSetModelReplacement(snap session.Snapshot, replacement *pb.RunRequest) (bool, error) {
	profileRef := refreshProfileRef(snap)
	if profileRef == "" {
		return false, nil
	}
	requested, ok := agentprofile.AgentArgsModel(replacement.AgentType, replacement.ExtraArgs)
	if !ok || strings.TrimSpace(requested) == "" {
		return false, nil
	}
	var saved struct {
		ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
	}
	var profile agentprofile.Profile
	var err error
	if decodeLaunchSpec(snap.LaunchSpec, &saved) && strings.TrimSpace(saved.ProfileSpec.GetRuntimeRef()) != "" {
		// Runtime selection is independent of the source Profile's model policy.
		// Keep its frozen identity even when the Pack changed or is unavailable.
		profile, err = agentprofile.FromPBProfileSpec(saved.ProfileSpec)
		if err != nil {
			return false, status.Errorf(codes.InvalidArgument, "%v", err)
		}
	} else {
		profile, err = agentprofile.ResolveProfile(filepath.Join(s.amuxDir, "agent-packs", "registry.json"), profileRef)
		if err != nil || strings.TrimSpace(profile.Type) != strings.TrimSpace(snap.AgentType) {
			return false, nil
		}
	}
	projection, err := agentprofile.ProjectModelSwitch(profile, snap.ExtraArgs, requested)
	if err != nil {
		return false, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	replacement.ExtraArgs = projection.ExtraArgs
	replacement.ProfileSpec = projection.ProfileSpec
	// set-model never changes the reasoning effort; carry the frozen effort
	// over for native targets so the hot-reconfigure comparison still matches
	// the effort the provider session was launched with.
	if projection.ProfileSpec.Provider == nil {
		if effort := launchSpecReasoningEffort(snap.LaunchSpec); effort != "" {
			replacement.ProfileSpec.ReasoningEffort = effort
		}
	}
	replacement.Env = mergeDefaultEnvByKey(replacement.Env, projection.Env)
	return true, nil
}

// mergeDefaultEnvByKey appends default env entries whose key the recorded env
// does not already set.
func mergeDefaultEnvByKey(env, defaults []string) []string {
	out := append([]string(nil), env...)
	for _, entry := range defaults {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" || envSliceContainsKey(strings.TrimSpace(key), out) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func missingNativeConversationOnResume(agentType string, err error) bool {
	if err == nil {
		return false
	}
	if !adapter.IsCodexCompatible(agentType) {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "thread/resume") {
		return false
	}
	return strings.Contains(message, "no rollout found") ||
		strings.Contains(message, "thread not found") ||
		strings.Contains(message, "no thread found")
}

func (s *server) restorePreviousExecution(ctx context.Context, sess *session.Session, snap session.Snapshot, actorToken string, actorTokenExpiresAt int64, fallbackHandoff string) error {
	s.mgr.ResetForRestart(sess)
	if err := s.mgr.RestoreLaunchSnapshot(sess, snap); err != nil {
		return fmt.Errorf("restore launch snapshot: %w", err)
	}
	a, err := adapter.Get(snap.AgentType)
	if err != nil {
		return err
	}
	req := runRequestFromLaunchSnapshot(snap)
	materializationCwd := snap.Cwd
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		if err := os.MkdirAll(s.amuxDir, 0o700); err != nil {
			return fmt.Errorf("create restore staging parent: %w", err)
		}
		materializationCwd, err = os.MkdirTemp(s.amuxDir, "isolation-restore-*")
		if err != nil {
			return fmt.Errorf("create restore staging workspace: %w", err)
		}
		defer os.RemoveAll(materializationCwd)
	}
	if err := materializeProfileWorkspace(req, materializationCwd, "", s.amuxDir); err != nil {
		return fmt.Errorf("restore profile materialization: %w", err)
	}
	runExtraArgs := append([]string(nil), snap.ExtraArgs...)
	if !s.disableMCPAutoInject && runArgsContainAmuxMCP(snap.AgentType, runExtraArgs) {
		runExtraArgs = stripAmuxMCPArgs(snap.AgentType, runExtraArgs)
		mcpArgs, err := materializeAmuxMCPForRun(snap.AgentType, snap.ID, s.amuxDir, materializationCwd, actorToken, memoryprotocol.Binding(snap.Env))
		if err != nil {
			return fmt.Errorf("restore MCP materialization: %w", err)
		}
		runExtraArgs = append(runExtraArgs, mcpArgs...)
	}
	credentialCtx := sessionCredentialMaterializationContext(ctx, snap.ExecutionIdentity, actorToken)
	providerEnv, err := s.materializeProviderCredentialEnv(credentialCtx, snap.ExecutionIdentity, req)
	if err != nil {
		return fmt.Errorf("restore provider credential: %w", err)
	}
	recordEnv := append([]string(nil), snap.Env...)
	runEnv := s.launchEnvForIdentity(snap.ExecutionIdentity, append(append([]string{}, providerEnv...), recordEnv...))

	principalPaths, err := s.prepareRelaunchIsolation(ctx, sess, snap)
	if err != nil {
		return fmt.Errorf("prepare restored launch: %w", err)
	}
	// PrepareRun already created run/<launch-id>, and nothing removes it
	// unless a process attaches and exits: every exit below that never gets
	// that far reclaims it.
	prepared := sess.Snapshot()
	attached := false
	defer func() {
		if !attached {
			s.reclaimLaunchRun(prepared.ExecutionIdentity, prepared.LaunchID)
		}
	}()
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		currentLaunchID := sess.Snapshot().LaunchID
		runEnv, recordEnv, runExtraArgs, err = materializeLinuxDACSessionLaunch(func(sources []isolation.BundleSource) error {
			return s.isolationRuntime.MaterializeExecution(ctx, snap.ExecutionIdentity, currentLaunchID, principalPaths.Workspace, sources)
		}, materializationCwd, profileSessionDir(s.amuxDir, snap.ID), snap.LaunchID, principalPaths,
			isolatedHomeProjectionFor(snap.AgentType, s.reportLaunchWarning(sess)), runEnv, recordEnv, runExtraArgs)
		if err != nil {
			return fmt.Errorf("materialize restored isolation launch: %w", err)
		}
	}
	runEnv, recordEnv = applyAgentActorToken(runEnv, recordEnv, actorToken, sessionActorTokenPath(s.amuxDir, snap.ID))
	if warning := agentActorTokenExpiryWarning(actorTokenExpiresAt, time.Now()); warning != "" {
		s.reportLaunchWarning(sess)(warning)
	}
	if err := s.startSessionContext(ctx, a, sess, runExtraArgs, runEnv); err == nil {
		attached = true
		return nil
	} else if !missingNativeConversationOnResume(snap.AgentType, err) {
		return err
	}

	s.mgr.ResetForRestart(sess)
	fresh := snap
	fresh.AgentSessionID = ""
	fresh.HandoffContext = fallbackHandoff
	if restoreErr := s.mgr.RestoreLaunchSnapshot(sess, fresh); restoreErr != nil {
		return fmt.Errorf("prepare fresh restored conversation: %w", restoreErr)
	}
	if err := s.startSessionContext(ctx, a, sess, runExtraArgs, runEnv); err != nil {
		return fmt.Errorf("fresh restored conversation: %w", err)
	}
	attached = true
	return nil
}

func runRequestFromLaunchSnapshot(snap session.Snapshot) *pb.RunRequest {
	var saved struct {
		Repo                  string               `json:"repo"`
		RepoRef               string               `json:"repo_ref"`
		IdleTimeoutSeconds    int64                `json:"idle_timeout_seconds"`
		IdleTimeoutSecondsSet bool                 `json:"idle_timeout_seconds_set"`
		ProfileSpec           *pb.AgentProfileSpec `json:"profile_spec"`
	}
	_ = decodeLaunchSpec(snap.LaunchSpec, &saved)
	return &pb.RunRequest{
		AgentProfile: snap.AgentProfile, ProfileSpec: saved.ProfileSpec, AgentType: snap.AgentType,
		Name: snap.Name, Env: append([]string(nil), snap.Env...), ExtraArgs: append([]string(nil), snap.ExtraArgs...),
		Cwd: snap.Cwd, Repo: saved.Repo, RepoRef: saved.RepoRef,
		IdleTimeoutSeconds: saved.IdleTimeoutSeconds, IdleTimeoutSecondsSet: saved.IdleTimeoutSecondsSet,
		WorkspaceSource: snap.WorkspaceSource, WorkspaceScope: snap.WorkspaceScope,
	}
}

func (s *server) tryHotReconfigureExecution(sess *session.Session, snap session.Snapshot, replacement *pb.RunRequest, runExtraArgs, recordEnv []string, effectiveModel, launchSpec string) (bool, error) {
	agentType := strings.TrimSpace(replacement.AgentType)
	if snap.State != session.StateRunning || strings.TrimSpace(snap.AgentSessionID) == "" ||
		strings.TrimSpace(snap.AgentType) != agentType ||
		strings.TrimSpace(snap.AgentProfile) != strings.TrimSpace(replacement.AgentProfile) ||
		launchSpecUsesProvider(snap.LaunchSpec) || replacement.ProfileSpec == nil || replacement.ProfileSpec.Provider != nil {
		return false, nil
	}
	oldModel, oldModelOK := agentprofile.AgentArgsModel(snap.AgentType, snap.ExtraArgs)
	newModel, newModelOK := agentprofile.AgentArgsModel(agentType, runExtraArgs)
	oldEffort := launchSpecReasoningEffort(snap.LaunchSpec)
	newEffort := launchSpecReasoningEffort(launchSpec)
	if adapter.IsCodexCompatible(agentType) {
		if !oldModelOK || !newModelOK || oldEffort == "" || newEffort == "" {
			return false, nil
		}
	} else {
		a, err := adapter.Get(agentType)
		if err != nil || a.Capabilities().Protocol != adapter.ProtocolACP {
			return false, nil
		}
		mapper, ok := a.(adapter.ACPExecutionConfigurator)
		if !ok {
			return false, nil
		}
		oldModel, newModel = launchSpecModel(snap.LaunchSpec), launchSpecModel(launchSpec)
		// Rollback requires an explicit previous model. Native defaults are
		// provider-owned and cannot be reconstructed from a live display label.
		if oldModel == "" || newModel == "" {
			return false, nil
		}
		for _, choice := range [][2]string{{oldModel, oldEffort}, {newModel, newEffort}} {
			values, err := mapper.ACPExecutionConfig(choice[0], choice[1])
			if err != nil || len(values) == 0 {
				return false, nil
			}
		}
	}
	unlock, busy, pending := sess.TryLockIdleQueue()
	if busy || pending > 0 {
		return false, nil
	}
	defer unlock()
	if err := sess.ReconfigureExecution(newModel, newEffort); err != nil {
		return false, nil
	}
	if err := s.mgr.RecordReconfiguredLaunchParams(sess, agentType, runExtraArgs, recordEnv,
		snap.WorkspaceSource, snap.WorkspaceScope, replacement.AgentProfile, effectiveModel, launchSpec,
		"", true); err != nil {
		rollbackErr := sess.ReconfigureExecution(oldModel, oldEffort)
		if rollbackErr != nil {
			return false, status.Errorf(codes.Internal, "record hot execution reconfiguration: %v (provider rollback failed: %v)", err, rollbackErr)
		}
		return false, status.Errorf(codes.Internal, "record hot execution reconfiguration: %v", err)
	}
	sess.AppendReplay([]byte("\r\n[system] " + executionMessage("updated") + "\r\n"))
	return true, nil
}

func (s *server) refreshProfileAndRelaunch(ctx context.Context, sess *session.Session) (*pb.RunResponse, error) {
	s.relaunchMu.Lock()
	defer s.relaunchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sess.Snapshot()
	profileRef := refreshProfileRef(snap)
	if profileRef == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q was not created from an Agent Profile; --refresh-profile is unavailable", snap.Name)
	}
	profile, err := agentprofile.ResolveProfile(filepath.Join(s.amuxDir, "agent-packs", "registry.json"), profileRef)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "refresh Agent Profile %q: %v", profileRef, err)
	}
	launch, err := projectRefreshedProfileLaunch(snap, profile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(launch.AgentType) != strings.TrimSpace(snap.AgentType) {
		return nil, status.Errorf(codes.FailedPrecondition, "refresh Agent Profile %q changes agent type from %s to %s; cross-type refresh is unsupported", profileRef, snap.AgentType, launch.AgentType)
	}
	a, err := adapter.Get(launch.AgentType)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	launch.Env = append([]string(nil), snap.Env...)
	refreshReq := refreshedRunRequest(snap, launch)
	if err := validateRunPermissionPolicy(refreshReq); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "refresh Agent Profile %q: %v", profileRef, err)
	}
	refreshCwd := snap.Cwd
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		if err := os.MkdirAll(s.amuxDir, 0o700); err != nil {
			return nil, status.Errorf(codes.Internal, "create refresh staging parent: %v", err)
		}
		refreshCwd, err = os.MkdirTemp(s.amuxDir, "isolation-refresh-*")
		if err != nil {
			return nil, status.Errorf(codes.Internal, "create refresh staging workspace: %v", err)
		}
		defer os.RemoveAll(refreshCwd)
	}
	runExtraArgs := append([]string(nil), launch.ExtraArgs...)
	storedToken, _ := readSessionActorToken(s.amuxDir, snap.ID)
	if !s.disableMCPAutoInject && !runArgsContainAmuxMCP(launch.AgentType, runExtraArgs) {
		mcpExtraArgs, err := materializeAmuxMCPForRun(launch.AgentType, snap.ID, s.amuxDir, refreshCwd, storedToken, memoryprotocol.Binding(snap.Env))
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "refresh MCP materialization: %v", err)
		}
		runExtraArgs = append(runExtraArgs, mcpExtraArgs...)
	}
	refreshReq.ExtraArgs = append([]string(nil), runExtraArgs...)
	credentialCtx := sessionCredentialMaterializationContext(ctx, snap.ExecutionIdentity, storedToken)
	providerEnv, err := s.materializeProviderCredentialEnv(credentialCtx, snap.ExecutionIdentity, refreshReq)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "refresh provider credential: %v", err)
	}
	recordEnv := memoryprotocol.SetEnv(ensureLaunchEnvPATH(launch.Env), memoryprotocol.BindingEnv, memoryprotocol.Binding(snap.Env))
	runEnv := s.launchEnvForIdentity(snap.ExecutionIdentity, append(append([]string{}, providerEnv...), recordEnv...))
	launchSpec := launchSpecSnapshot(refreshReq)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runExtraArgs, launch.Model = s.maybeAutoSelectModel(a, launch.AgentType, snap.Cwd, runEnv, runExtraArgs, launch.Model)
	_ = sess.Kill()
	principalPaths, err := s.prepareRelaunchIsolation(ctx, sess, snap)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "prepare refreshed launch: %v", err)
	}
	// PrepareRun already created run/<launch-id>, and nothing removes it
	// unless a process attaches and exits: every exit below that never gets
	// that far reclaims it.
	prepared := sess.Snapshot()
	attached := false
	defer func() {
		if !attached {
			s.reclaimLaunchRun(prepared.ExecutionIdentity, prepared.LaunchID)
		}
	}()
	if err := materializeProfileWorkspace(refreshReq, refreshCwd, "", s.amuxDir); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "refresh profile materialization: %v", err)
	}
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		currentLaunchID := sess.Snapshot().LaunchID
		runEnv, recordEnv, runExtraArgs, err = materializeLinuxDACSessionLaunch(func(sources []isolation.BundleSource) error {
			return s.isolationRuntime.MaterializeExecution(ctx, snap.ExecutionIdentity, currentLaunchID, principalPaths.Workspace, sources)
		}, refreshCwd, profileSessionDir(s.amuxDir, snap.ID), snap.LaunchID, principalPaths, isolatedHomeProjectionFor(snap.AgentType, s.reportLaunchWarning(sess)), runEnv, recordEnv, runExtraArgs)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "refresh isolation launch bundle: %v", err)
		}
	}
	s.mgr.ResetForRestart(sess)
	sess.AppendReplay([]byte("\r\n[system] refreshing Agent Profile and restarting session\r\n"))
	if err := s.mgr.RecordRefreshedLaunchParams(sess, runExtraArgs, recordEnv, snap.WorkspaceSource, snap.WorkspaceScope, launch.AgentProfile, launch.Model, launchSpec); err != nil {
		return nil, status.Errorf(codes.Internal, "record refreshed profile: %v", err)
	}
	if err := s.startSessionContext(ctx, a, sess, runExtraArgs, runEnv); err != nil {
		return nil, err
	}
	attached = true
	return &pb.RunResponse{Session: toProtoSession(sess)}, nil
}

// Portable content refresh preserves the frozen execution, including native
// permission arguments and provider bindings. It must not re-resolve a changed
// runtime catalog or acquire defaults from the new content revision.
func projectRefreshedProfileLaunch(snap session.Snapshot, profile agentprofile.Profile) (agentprofile.LaunchProjection, error) {
	var previous struct {
		ProfileSpec *pb.AgentProfileSpec `json:"profile_spec"`
	}
	decodeLaunchSpec(snap.LaunchSpec, &previous)
	frozen, err := agentprofile.FromPBProfileSpec(previous.ProfileSpec)
	if err != nil {
		return agentprofile.LaunchProjection{}, err
	}
	profile.Type = frozen.Type
	profile.Model = frozen.Model
	profile.ConfiguredModel = frozen.ConfiguredModel
	profile.Provider = frozen.Provider
	profile.DefaultArgs = frozen.DefaultArgs
	profile.SupportedModels = frozen.SupportedModels
	profile.ExecutionRuntimeRef = frozen.ExecutionRuntimeRef
	profile.ReasoningEffort = frozen.ReasoningEffort
	profile.PermissionPolicy = frozen.PermissionPolicy
	profile.Placement.Requires.AgentTypes = []string{frozen.Type}
	return agentprofile.LaunchProjection{
		AgentType: frozen.Type, AgentProfile: profile.PinnedRef(),
		ProfileSpec: agentprofile.ToPBProfileSpec(profile),
		ExtraArgs:   append([]string(nil), snap.ExtraArgs...),
		Env:         append([]string(nil), snap.Env...), Model: snap.EffectiveModel,
		Runtime: frozen.ExecutionRuntimeRef, ReasoningEffort: frozen.ReasoningEffort,
	}, nil
}

func refreshProfileRef(snap session.Snapshot) string {
	var decoded struct {
		ProfileSpec *struct {
			Ref string `json:"ref"`
		} `json:"profile_spec"`
	}
	if decodeLaunchSpec(snap.LaunchSpec, &decoded) && decoded.ProfileSpec != nil {
		if ref := strings.TrimSpace(decoded.ProfileSpec.Ref); ref != "" {
			return ref
		}
	}
	ref := strings.TrimSpace(snap.AgentProfile)
	if ref == "" {
		return ""
	}
	if base, _, ok := strings.Cut(ref, "@"); ok {
		return strings.TrimSpace(base)
	}
	return ref
}
func refreshedRunRequest(snap session.Snapshot, launch agentprofile.LaunchProjection) *pb.RunRequest {
	var previous struct {
		Repo                  string `json:"repo"`
		RepoRef               string `json:"repo_ref"`
		IdleTimeoutSeconds    int64  `json:"idle_timeout_seconds"`
		IdleTimeoutSecondsSet bool   `json:"idle_timeout_seconds_set"`
	}
	_ = decodeLaunchSpec(snap.LaunchSpec, &previous)
	return &pb.RunRequest{AgentProfile: launch.AgentProfile, ProfileSpec: launch.ProfileSpec, AgentType: launch.AgentType, Name: snap.Name, Env: append([]string(nil), launch.Env...), Cwd: snap.Cwd, Repo: previous.Repo, RepoRef: previous.RepoRef, IdleTimeoutSeconds: previous.IdleTimeoutSeconds, IdleTimeoutSecondsSet: previous.IdleTimeoutSecondsSet, WorkspaceSource: snap.WorkspaceSource, WorkspaceScope: snap.WorkspaceScope}
}

func (s *server) adoptAgentActorToken(sess *session.Session, token string, expiresAt int64) {
	if sess == nil || strings.TrimSpace(token) == "" {
		return
	}
	if err := writeSessionActorToken(s.amuxDir, sess.ID, token, expiresAt); err != nil {
		fmt.Fprintf(os.Stderr, "runtime: persist agent actor token for %s: %v\n", sess.Name, err)
	}
}
func (s *server) relaunchSession(sess *session.Session, action string) (*pb.RunResponse, error) {
	s.cleanupLaunchMu.RLock()
	defer s.cleanupLaunchMu.RUnlock()
	s.relaunchMu.Lock()
	defer s.relaunchMu.Unlock()
	return s.relaunchSessionLocked(sess, action)
}
func (s *server) relaunchSessionWithEnvContext(ctx context.Context, sess *session.Session, action string, extraArgs, launchEnv, recordEnv []string, // adoptAgentActorToken replaces the stored credential when the hub minted a
	// fresh one for this relaunch. Relaunch otherwise replays what creation stored,
	// so this is what makes "restart the session" an actual recovery from an
	// expired credential rather than a re-run of the same dead one. A hub that did
	// not mint (unidentified session, no principal store, a direct runtime call)
	// leaves the stored credential untouched.
	actorToken string, actorTokenExpiresAt int64) (*pb.RunResponse, error) {
	for !s.relaunchMu.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer s.relaunchMu.Unlock()
	return s.relaunchSessionLockedWithEnvContext(ctx, sess, action, extraArgs, launchEnv, recordEnv, true, actorToken, actorTokenExpiresAt)
}
func (s *server) relaunchSessionLocked(sess *session.Session, action string) (*pb.RunResponse, error) {
	return s.relaunchSessionLockedWithEnv(sess, action, nil, nil, nil, false)
}
func (s *server) relaunchSessionLockedWithEnv(sess *session.Session, action string, extraArgs, launchEnv, recordEnv []string, explicitEnv bool) (*pb.RunResponse, error) {
	return s.relaunchSessionLockedWithEnvContext(context.Background(), sess, action, extraArgs, launchEnv, recordEnv, explicitEnv, "", 0)
}
func (s *server) relaunchSessionLockedWithEnvContext(ctx context.Context, sess *session.Session, action string, extraArgs, launchEnv, recordEnv []string, explicitEnv bool, actorToken string, actorTokenExpiresAt int64) (*pb.RunResponse, error) {
	return s.relaunchSessionLockedWithEnvContextAndQueue(ctx, sess, action, extraArgs, launchEnv, recordEnv, explicitEnv, actorToken, actorTokenExpiresAt, false)
}
func (s *server) relaunchSessionLockedPreservingQueue(sess *session.Session, action string) (*pb.RunResponse, error) {
	return s.relaunchSessionLockedWithEnvContextAndQueue(context.Background(), sess, action, nil, nil, nil, false, "", 0, true)
}
func (s *server) relaunchSessionLockedWithEnvContextAndQueue(ctx context.Context, sess *session.Session, action string, extraArgs, launchEnv, recordEnv []string, explicitEnv bool, actorToken string, actorTokenExpiresAt int64, queueLocked bool) (*pb.RunResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sess.Snapshot()
	if action == "resuming" && snap.State != session.StateSleeping {
		return nil, status.Errorf(codes.FailedPrecondition, "session %q is %s; only sleeping sessions can resume", snap.Name, snap.State)
	}
	original := snap
	snap, fallbackReq, err := s.recoverRestartDefaults(snap)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	a, err := adapter.Get(snap.AgentType)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	if actorToken == "" {
		actorToken, actorTokenExpiresAt = readSessionActorToken(s.amuxDir, snap.ID)
	}
	if recordedRestartEngine(original) == "" {
		if snap.HandoffContext == "" {
			snap.HandoffContext, err = s.mgr.ConversationHandoff(sess)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "%v", err)
			}
		}
		snap.AgentSessionID = ""
	}
	materializationCwd := ""
	if fallbackReq != nil {
		materializationCwd = snap.Cwd
		if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
			materializationCwd, err = os.MkdirTemp(s.amuxDir, "isolation-restart-*")
			if err != nil {
				return nil, status.Errorf(codes.Internal, "%v", err)
			}
			defer os.RemoveAll(materializationCwd)
		}
	}
	_ = sess.Kill()
	principalPaths, err := s.prepareRelaunchIsolation(ctx, sess, snap)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "prepare %s launch: %v", action, err)
	}
	// PrepareRun already created run/<launch-id>, and nothing removes it
	// unless a process attaches and exits: every exit below that never gets
	// that far reclaims it.
	prepared := sess.Snapshot()
	attached := false
	defer func() {
		if !attached {
			s.reclaimLaunchRun(prepared.ExecutionIdentity, prepared.LaunchID)
		}
	}()
	if fallbackReq != nil {
		if err := materializeProfileWorkspace(fallbackReq, materializationCwd, "", s.amuxDir); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
	}
	if snap.AgentType != original.AgentType || snap.AgentProfile != original.AgentProfile || snap.LaunchSpec != original.LaunchSpec {
		if err := s.mgr.RestoreLaunchSnapshot(sess, snap); err != nil {
			return nil, status.Errorf(codes.Internal, "%v", err)
		}
	}
	wasSleeping := snap.State == session.StateSleeping
	if queueLocked {
		s.mgr.ResetForRestartPreservingQueue(sess)
	} else {
		s.mgr.ResetForRestart(sess)
	}
	sess.AppendReplay([]byte("\r\n[system] " + action + " session\r\n"))
	runExtraArgs := snap.ExtraArgs
	if explicitEnv {
		runExtraArgs = append([]string(nil), extraArgs...)
	}
	if !s.disableMCPAutoInject && runArgsContainAmuxMCP(snap.AgentType, runExtraArgs) {
		mcpToken := actorToken
		if mcpToken == "" {
			mcpToken, _ = readSessionActorToken(s.amuxDir, snap.ID)
		}
		stripped := stripAmuxMCPArgs(snap.AgentType, runExtraArgs)
		mcpExtraArgs, mcpErr := materializeAmuxMCPForRun(snap.AgentType, snap.ID, s.amuxDir, principalPaths.Workspace, mcpToken, memoryprotocol.Binding(snap.Env))
		if mcpErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%s MCP materialization: %v", action, mcpErr)
		}
		runExtraArgs = append(stripped, mcpExtraArgs...)
	}
	relaunchEnv := append([]string{}, snap.Env...)
	if explicitEnv {
		relaunchEnv = ensureLaunchEnvPATH(launchEnv)
		recordEnv = memoryprotocol.SetEnv(ensureLaunchEnvPATH(recordEnv), memoryprotocol.BindingEnv, memoryprotocol.Binding(snap.Env))
		if err := s.mgr.RecordLaunchParams(sess, runExtraArgs, recordEnv, snap.WorkspaceSource, snap.WorkspaceScope, snap.AgentProfile, snap.EffectiveModel, snap.LaunchSpec); err != nil {
			return nil, status.Errorf(codes.Internal, "record params: %v", err)
		}
	} else {
		recordEnv = ensureLegacyLaunchEnvPATH(snap.Env)
		credentialCtx := sessionCredentialMaterializationContext(ctx, snap.ExecutionIdentity, actorToken)
		providerEnv, err := s.materializeProviderCredentialEnvFromLaunchSpec(credentialCtx, snap.ExecutionIdentity, snap.LaunchSpec, snap.AgentType, recordEnv)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "provider credential: %v", err)
		}
		relaunchEnv = s.launchEnvForIdentity(snap.ExecutionIdentity, append(append([]string{}, providerEnv...), recordEnv...))
		if !slices.Equal(snap.Env, recordEnv) {
			if err := s.mgr.RecordLaunchParams(sess, runExtraArgs, recordEnv, snap.WorkspaceSource, snap.WorkspaceScope, snap.AgentProfile, snap.EffectiveModel, snap.LaunchSpec); err != nil {
				return nil, status.Errorf(codes.Internal, "record migrated PATH: %v", err)
			}
		}
	}
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		currentLaunchID := sess.Snapshot().LaunchID
		relaunchEnv, recordEnv, runExtraArgs, err = materializeLinuxDACSessionLaunch(func(sources []isolation.BundleSource) error {
			return s.isolationRuntime.MaterializeExecution(ctx, snap.ExecutionIdentity, currentLaunchID, principalPaths.Workspace, sources)
		}, materializationCwd, profileSessionDir(s.amuxDir, snap.ID), snap.LaunchID, principalPaths, isolatedHomeProjectionFor(snap.AgentType, s.reportLaunchWarning(sess)), relaunchEnv, recordEnv, runExtraArgs)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "materialize %s isolation bundle: %v", action, err)
		}
	}
	relaunchEnv, recordEnv = applyAgentActorToken(relaunchEnv, recordEnv, actorToken, sessionActorTokenPath(s.amuxDir, snap.ID))
	if warning := agentActorTokenExpiryWarning(actorTokenExpiresAt, time.Now()); warning != "" {
		s.reportLaunchWarning(sess)(warning)
	}
	launchErr := s.startSessionContext(ctx, a, sess, runExtraArgs, relaunchEnv)
	if launchErr != nil && strings.TrimSpace(snap.AgentSessionID) != "" &&
		ctx.Err() == nil && missingNativeConversationOnResume(snap.AgentType, launchErr) {
		// A provider may allocate a thread ID before persisting its first turn.
		// Keep any undelivered handoff from an earlier model switch; otherwise
		// rebuild it from amux's transcript, which survives provider restarts.
		fresh := sess.Snapshot()
		handoff := fresh.HandoffContext
		var handoffErr error
		if strings.TrimSpace(handoff) == "" {
			handoff, handoffErr = s.mgr.ConversationHandoff(sess)
		}
		if handoffErr != nil {
			launchErr = fmt.Errorf("native resume failed: %v; capture conversation history: %w", launchErr, handoffErr)
		} else {
			fresh.AgentSessionID = ""
			fresh.HandoffContext = handoff
			// The caller already applied its queue policy. Retrying a launch
			// must not clear input retained by a queued lifecycle operation.
			s.mgr.ResetForRestartPreservingQueue(sess)
			if err := s.mgr.RestoreLaunchSnapshot(sess, fresh); err != nil {
				launchErr = fmt.Errorf("native resume failed: %v; prepare conversation handoff: %w", launchErr, err)
			} else if err := s.startSessionContext(ctx, a, sess, runExtraArgs, relaunchEnv); err != nil {
				launchErr = fmt.Errorf("native resume failed: %v; fresh provider conversation failed: %w", launchErr, err)
			} else {
				launchErr = nil
				sess.AppendReplay([]byte("\r\n[system] provider history was unavailable; continuing from amux conversation handoff\r\n"))
			}
		}
	}
	if launchErr != nil {
		if wasSleeping {
			sess.AppendReplay([]byte("\r\n[system] resume failed; session remains sleeping: " + launchErr.Error() + "\r\n"))
			_ = s.mgr.SetSleeping(sess)
		}
		return nil, launchErr
	}
	attached = true
	return &pb.RunResponse{Session: toProtoSession(sess)}, nil
}
func (s *server) prepareRelaunchIsolation(ctx context.Context, sess *session.Session, snap session.Snapshot) (isolation.PrincipalPaths, error) {
	launchID, err := s.mgr.RotateLaunchID(sess)
	if err != nil {
		return isolation.PrincipalPaths{}, err
	}
	identity := snap.ExecutionIdentity
	if identity.IdentityMode == "" {
		identity = principal.LegacyHostIdentity(principal.SourceSystem)
	}
	var paths isolation.PrincipalPaths
	if s.isolationRuntime != nil {
		var selected string
		paths, selected, err = s.isolationRuntime.PrepareExecution(ctx, identity, launchID, snap.Cwd)
		if err != nil {
			return isolation.PrincipalPaths{}, err
		}
		identity, err = principal.FinalizeBackend(identity, selected)
		if err != nil {
			return isolation.PrincipalPaths{}, err
		}
		if selected == principal.BackendLinuxDAC && filepath.Clean(paths.Workspace) != filepath.Clean(snap.Cwd) {
			return isolation.PrincipalPaths{}, fmt.Errorf("saved linux-dac workspace changed across relaunch")
		}
	} else if identity.IsolationBackend == "" {
		identity, err = principal.FinalizeBackend(identity, principal.BackendPassthrough)
		if err != nil {
			return isolation.PrincipalPaths{}, err
		}
	}
	return paths, s.mgr.SetExecutionIdentity(sess, identity, launchID)
}
func (s *server) ensureSessionActive(sess *session.Session) error {
	s.relaunchMu.Lock()
	defer s.relaunchMu.Unlock()
	switch state := sess.StateSnapshot(); state {
	case session.StateRunning:
		if !sess.HasProcess() {
			return status.Errorf(codes.Unavailable, "session %q process is not connected yet", sess.Name)
		}
		return nil
	case session.StateSleeping:
		_, err := s.relaunchSessionLocked(sess, "resuming")
		return err
	case session.StateCreated:
		if sess.HasProcess() {
			return nil
		}
		_, err := s.relaunchSessionLocked(sess, "restarting")
		return err
	case session.StateArchived:
		return status.Errorf(codes.FailedPrecondition, "session %q is archived", sess.Name)
	default:
		return status.Errorf(codes.FailedPrecondition, "session %q is not available (state=%s)", sess.Name, state)
	}
}
