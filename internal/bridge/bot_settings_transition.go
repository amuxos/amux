package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// botSettingsTransitionState owns pending bot-settings confirmations.
type botSettingsTransitionState struct {
	botSettingsMu      sync.Mutex
	pendingBotSettings map[string]pendingBotSettingsConfirmation
}

const (
	BotSettingsStatusSuccess             = "success"
	BotSettingsStatusNeedsConfirmation   = "needs_confirmation"
	BotSettingsStatusNeedsRecoveryChoice = "needs_recovery_choice"
	BotSettingsStatusStateChanged        = "state_changed"

	BotSettingsRecoveryCreateBlank = "create_blank"
)

const botSettingsConfirmationTTL = 10 * time.Minute

var errBotSettingsBlankRecoveryFailed = errors.New("blank singleton recovery failed")

type pendingBotSettingsConfirmation struct {
	Request   BotSettingsRequest
	ExpiresAt time.Time
}

type BotSettingsRequest struct {
	Interaction            string
	DefaultAgent           string
	DefaultRuntimeRef      string
	DefaultModelRef        string
	DefaultReasoningEffort string
	ExpectedVersion        int64
	Confirmed              bool
	RequestID              string
	OpenID                 string
	RecoveryAction         string
	RecoveryAttemptID      string
	// IsolationRequired carries the 每个话题独立(用户隔离) composite option. It
	// is only meaningful with Interaction=thread; singleton commits always
	// clear it.
	IsolationRequired bool
}

type BotSettingsResult struct {
	Status            string
	State             BotProfileState
	ImpactSummary     string
	ImpactMessage     i18n.Text
	ConfirmationToken string
}

// ApplyBotSettings is the single transition boundary for Bot interaction mode
// and default Profile changes initiated by Feishu management surfaces.
func (b *Bridge) ApplyBotSettings(ctx context.Context, req BotSettingsRequest) (BotSettingsResult, error) {
	if b != nil && b.managementPackActions != nil {
		var result BotSettingsResult
		var applyErr error
		err := b.managementPackActions.WithRegistryStableContext(ctx, func() error {
			result, applyErr = b.applyBotSettings(ctx, req)
			return applyErr
		})
		return result, err
	}
	return b.applyBotSettings(ctx, req)
}

func (b *Bridge) applyBotSettings(ctx context.Context, req BotSettingsRequest) (BotSettingsResult, error) {
	if b == nil || b.db == nil || b.hub == nil {
		return BotSettingsResult{}, fmt.Errorf("bridge settings transition is unavailable")
	}
	req.Interaction = strings.TrimSpace(req.Interaction)
	req.DefaultAgent = strings.TrimSpace(req.DefaultAgent)
	req.DefaultRuntimeRef = strings.TrimSpace(req.DefaultRuntimeRef)
	req.DefaultModelRef = strings.TrimSpace(req.DefaultModelRef)
	req.DefaultReasoningEffort = strings.TrimSpace(req.DefaultReasoningEffort)
	req.RecoveryAction = strings.TrimSpace(req.RecoveryAction)
	req.RecoveryAttemptID = strings.TrimSpace(req.RecoveryAttemptID)
	if !managementValidInteraction(req.Interaction) {
		return BotSettingsResult{}, fmt.Errorf("unsupported interaction mode %q", req.Interaction)
	}
	if err := b.validateGroupWatchInteractionMode(req.Interaction); err != nil {
		return BotSettingsResult{}, err
	}
	if req.DefaultAgent == "" {
		return BotSettingsResult{}, fmt.Errorf("default agent profile is required")
	}
	if req.RecoveryAction != "" && req.RecoveryAction != BotSettingsRecoveryCreateBlank {
		return BotSettingsResult{}, fmt.Errorf("unsupported settings recovery action %q", req.RecoveryAction)
	}

	current, err := b.currentBotProfileState()
	if err != nil {
		return BotSettingsResult{}, err
	}
	if current.Version != req.ExpectedVersion {
		return BotSettingsResult{Status: BotSettingsStatusStateChanged, State: current}, nil
	}
	if _, err := resolveBridgeAgentLaunchWithSelection(req.DefaultAgent, b.db.workspaceDefaults.Args,
		req.DefaultRuntimeRef, b.effectiveBotLaunchModel(req.DefaultRuntimeRef, req.DefaultModelRef), req.DefaultReasoningEffort); err != nil {
		return BotSettingsResult{}, err
	}

	if current.InteractionMode == config.FeishuInteractionSingleton &&
		req.Interaction == config.FeishuInteractionSingleton &&
		(current.DefaultAgent != req.DefaultAgent || current.DefaultRuntimeRef != req.DefaultRuntimeRef ||
			current.DefaultModelRef != req.DefaultModelRef || current.DefaultReasoningEffort != req.DefaultReasoningEffort) &&
		!req.Confirmed {
		return BotSettingsResult{
			Status:            BotSettingsStatusNeedsConfirmation,
			State:             current,
			ImpactSummary:     managementDefaultText(managementCopy("management.impact.execution")),
			ImpactMessage:     managementCopy("management.impact.execution"),
			ConfirmationToken: b.issueBotSettingsConfirmation(req),
		}, nil
	}

	if req.Interaction == config.FeishuInteractionThread {
		if req.IsolationRequired {
			if ok, reason := b.isolationLaunchSupported(); !ok {
				return BotSettingsResult{}, managementLocalizedError("management.error.isolation", reason)
			}
		}
		return b.applyThreadBotSettings(req)
	}
	return b.applySingletonBotSettings(ctx, current, req)
}

func (b *Bridge) applyThreadBotSettings(req BotSettingsRequest) (BotSettingsResult, error) {
	key := singletonStreamKey(b.currentBridgeId())
	unlock := b.lockRoute(key)
	defer unlock()

	state, err := b.db.CommitThreadBotSettingsWithLaunch(b.currentBridgeId(), req.ExpectedVersion, req.DefaultAgent,
		req.DefaultRuntimeRef, req.DefaultModelRef, req.DefaultReasoningEffort, req.IsolationRequired)
	if err != nil {
		if err == errBotProfileStateChanged {
			return b.botSettingsStateChangedResult()
		}
		return BotSettingsResult{}, err
	}
	b.StopStream(key)
	b.projectLegacyBotSettings(state)
	b.scheduleNativeSlashCommandSync()
	return BotSettingsResult{Status: BotSettingsStatusSuccess, State: state}, nil
}

func (b *Bridge) applySingletonBotSettings(ctx context.Context, current BotProfileState, req BotSettingsRequest) (BotSettingsResult, error) {
	if current.InteractionMode == config.FeishuInteractionSingleton &&
		current.DefaultAgent == req.DefaultAgent && current.DefaultRuntimeRef == req.DefaultRuntimeRef &&
		current.DefaultModelRef == req.DefaultModelRef && current.DefaultReasoningEffort == req.DefaultReasoningEffort {
		binding, err := b.db.GetBotSession(b.currentBridgeId())
		if err != nil {
			return BotSettingsResult{}, err
		}
		if botSessionReady(binding) {
			return BotSettingsResult{Status: BotSettingsStatusSuccess, State: current}, nil
		}
	}
	if current.InteractionMode == config.FeishuInteractionThread {
		return b.restoreOrRebuildSingletonBotSettings(ctx, current, req)
	}
	binding, err := b.db.GetBotSession(b.currentBridgeId())
	if err != nil {
		return BotSettingsResult{}, err
	}
	if binding == nil || (strings.TrimSpace(binding.TargetID) == "" && strings.TrimSpace(binding.TargetName) == "") {
		return BotSettingsResult{}, managementLocalizedError("management.error.target.missing")
	}
	target := findBotSettingsTarget(b.hub.ListTargets(), binding.TargetID, binding.TargetName)
	if target == nil {
		return BotSettingsResult{}, managementLocalizedError("management.error.target.unavailable")
	}
	cwd := strings.TrimSpace(binding.Cwd)
	if retained := findBotSettingsSession(target, binding.SessionName); retained != nil && strings.TrimSpace(retained.Cwd) != "" {
		cwd = strings.TrimSpace(retained.Cwd)
	}
	if cwd == "" {
		return BotSettingsResult{}, managementLocalizedError("management.error.cwd.missing")
	}
	if current.DefaultAgent == req.DefaultAgent {
		return b.reconfigureAndCommitSingletonBotSettings(ctx, req, binding, target, cwd)
	}
	return b.createAndCommitSingletonBotSettings(ctx, current, req, target, cwd)
}

func (b *Bridge) reconfigureAndCommitSingletonBotSettings(ctx context.Context, req BotSettingsRequest, binding *BotSession, target *pb.Target, cwd string) (BotSettingsResult, error) {
	launch, err := resolveBridgeAgentLaunchWithSelection(req.DefaultAgent, b.db.workspaceDefaults.Args,
		req.DefaultRuntimeRef, b.effectiveBotLaunchModel(req.DefaultRuntimeRef, req.DefaultModelRef), req.DefaultReasoningEffort)
	if err != nil {
		return BotSettingsResult{}, err
	}
	replacement := &pb.RunRequest{
		AgentProfile: launch.AgentProfile,
		ProfileSpec:  launch.ProfileSpec,
		AgentType:    launch.AgentType,
		Name:         binding.SessionName,
		Env:          append([]string(nil), launch.Env...),
		ExtraArgs:    append([]string(nil), launch.ExtraArgs...),
		Cwd:          cwd,
	}
	if config.SupportsAgentExecutable(replacement.AgentType) {
		replacement.AgentExecutable = config.GetAgentExecutable(replacement.AgentType)
	}
	restartCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := b.hub.Route(restartCtx, target.Id, "Restart", &pb.RestartRequest{
		Name: binding.SessionName, Replacement: replacement, PreserveHistory: true,
	})
	if err != nil {
		return BotSettingsResult{}, managementWrapError(err, "management.error.execution.failed")
	}
	if resp == nil {
		return BotSettingsResult{}, managementLocalizedError("management.error.execution.empty")
	}
	var out pb.RunResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return BotSettingsResult{}, managementWrapError(err, "management.error.execution.decode")
	}
	if out.Session == nil || !strings.EqualFold(strings.TrimSpace(out.Session.State), "running") {
		return BotSettingsResult{}, managementLocalizedError("management.error.execution.pending")
	}
	updated := *binding
	updated.Enabled = true
	updated.TargetID = valueOrFallback(strings.TrimSpace(target.Id), strings.TrimSpace(binding.TargetID))
	updated.TargetName = valueOrFallback(strings.TrimSpace(target.Name), strings.TrimSpace(binding.TargetName))
	updated.SessionName = valueOrFallback(strings.TrimSpace(out.Session.Name), strings.TrimSpace(binding.SessionName))
	updated.AgentProfile = valueOrFallback(strings.TrimSpace(out.Session.AgentProfile), launch.AgentProfile)
	updated.AgentType = valueOrFallback(strings.TrimSpace(out.Session.AgentType), launch.AgentType)
	updated.EffectiveModel = valueOrFallback(strings.TrimSpace(out.Session.EffectiveModel), launch.Model)
	updated.Cwd = valueOrFallback(strings.TrimSpace(out.Session.Cwd), cwd)
	if openID := strings.TrimSpace(req.OpenID); openID != "" {
		updated.LastReplyOpenID = openID
	}
	b.StopStream(singletonStreamKey(b.currentBridgeId()))
	return b.commitReadySingletonBotSettings(req, updated)
}

func (b *Bridge) restoreOrRebuildSingletonBotSettings(ctx context.Context, current BotProfileState, req BotSettingsRequest) (BotSettingsResult, error) {
	binding, err := b.db.GetBotSession(b.currentBridgeId())
	if err != nil {
		return BotSettingsResult{}, err
	}
	if req.RecoveryAction == BotSettingsRecoveryCreateBlank {
		if !req.Confirmed {
			return BotSettingsResult{}, fmt.Errorf("blank singleton recovery requires confirmation")
		}
		var retainedTarget *pb.Target
		if binding != nil {
			retainedTarget = findBotSettingsTarget(b.hub.ListTargets(), binding.TargetID, binding.TargetName)
		}
		result, createErr := b.createAndCommitSingletonBotSettings(ctx, current, req, retainedTarget, "")
		if createErr != nil {
			return BotSettingsResult{}, fmt.Errorf("%w: %v", errBotSettingsBlankRecoveryFailed, createErr)
		}
		return result, nil
	}
	if binding == nil || (strings.TrimSpace(binding.TargetID) == "" && strings.TrimSpace(binding.TargetName) == "") {
		return b.issueBotSettingsRecoveryChoice(current, req), nil
	}
	target := findBotSettingsTarget(b.hub.ListTargets(), binding.TargetID, binding.TargetName)
	retained := findBotSettingsSession(target, binding.SessionName)
	if retained != nil && !strings.EqualFold(strings.TrimSpace(retained.State), "archived") && botSettingsSessionMatchesAgent(retained, binding, req.DefaultAgent) {
		cwd := valueOrFallback(strings.TrimSpace(retained.Cwd), strings.TrimSpace(binding.Cwd))
		return b.reconfigureAndCommitSingletonBotSettings(ctx, req, binding, target, cwd)
	}

	cwd := strings.TrimSpace(binding.Cwd)
	if cwd == "" {
		return b.issueBotSettingsRecoveryChoice(current, req), nil
	}
	if target == nil {
		return BotSettingsResult{}, managementLocalizedError("management.error.original.target")
	}
	if !req.Confirmed {
		return BotSettingsResult{
			Status:            BotSettingsStatusNeedsConfirmation,
			State:             current,
			ImpactSummary:     managementDefaultText(managementCopy("management.impact.recreate", cwd)),
			ImpactMessage:     managementCopy("management.impact.recreate", cwd),
			ConfirmationToken: b.issueBotSettingsConfirmation(req),
		}, nil
	}
	result, err := b.createAndCommitSingletonBotSettings(ctx, current, req, target, cwd)
	if err != nil {
		if botSettingsCwdUnavailable(err) {
			return b.issueBotSettingsRecoveryChoice(current, req), nil
		}
		return BotSettingsResult{}, fmt.Errorf("create singleton in retained workspace: %w", err)
	}
	return result, nil
}

func botSettingsCwdUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if rpcStatus, ok := status.FromError(err); ok && rpcStatus.Code() == codes.InvalidArgument {
		if strings.HasPrefix(strings.TrimSpace(rpcStatus.Message()), "cwd:") {
			return true
		}
	}
	// The reverse tunnel currently transports unary errors as strings. Match the
	// exact amuxlet status prefix so other InvalidArgument failures (credentials,
	// profiles, permission policy) remain ordinary retryable failures.
	return strings.Contains(err.Error(), "code = InvalidArgument desc = cwd:")
}

func (b *Bridge) issueBotSettingsRecoveryChoice(current BotProfileState, req BotSettingsRequest) BotSettingsResult {
	return botSettingsRecoveryChoice(current, b.issueBotSettingsConfirmation(req))
}

func botSettingsRecoveryChoice(current BotProfileState, confirmationToken string) BotSettingsResult {
	return BotSettingsResult{
		Status:            BotSettingsStatusNeedsRecoveryChoice,
		State:             current,
		ImpactSummary:     managementDefaultText(managementCopy("management.impact.recovery")),
		ImpactMessage:     managementCopy("management.impact.recovery"),
		ConfirmationToken: confirmationToken,
	}
}

func findBotSettingsTarget(targets []*pb.Target, targetID, targetName string) *pb.Target {
	targetID = strings.TrimSpace(targetID)
	targetName = strings.TrimSpace(targetName)
	for _, target := range targets {
		if target != nil && targetID != "" && strings.TrimSpace(target.Id) == targetID {
			return target
		}
	}
	for _, target := range targets {
		if target != nil && targetName != "" && strings.TrimSpace(target.Name) == targetName {
			return target
		}
	}
	return nil
}

func findBotSettingsSession(target *pb.Target, sessionName string) *pb.Session {
	sessionName = strings.TrimSpace(sessionName)
	if target == nil || sessionName == "" {
		return nil
	}
	for _, candidate := range target.Sessions {
		if candidate != nil && strings.TrimSpace(candidate.Name) == sessionName {
			return candidate
		}
	}
	return nil
}

func botSettingsSessionMatchesAgent(sess *pb.Session, binding *BotSession, agent string) bool {
	want := strings.TrimSpace(agent)
	if want == "" {
		return true
	}
	actual := strings.TrimSpace(sess.AgentProfile)
	if actual == "" && binding != nil {
		actual = strings.TrimSpace(binding.AgentProfile)
	}
	return actual == "" || actual == want
}

func (b *Bridge) createAndCommitSingletonBotSettings(ctx context.Context, current BotProfileState, req BotSettingsRequest, retainedTarget *pb.Target, cwd string) (BotSettingsResult, error) {
	var target resolvedBridgeTarget
	if retainedTarget != nil {
		target = resolvedBridgeTarget{ID: strings.TrimSpace(retainedTarget.Id), Name: strings.TrimSpace(retainedTarget.Name)}
		if target.ID == "" {
			target.ID = target.Name
		}
		if target.Name == "" {
			target.Name = target.ID
		}
	} else {
		var targetErr string
		target, targetErr = b.resolveRunTarget(b.db.workspaceDefaults.Target)
		if targetErr != "" {
			return BotSettingsResult{}, fmt.Errorf("%s", targetErr)
		}
	}

	launch, err := resolveBridgeAgentLaunchWithSelection(req.DefaultAgent, b.db.workspaceDefaults.Args,
		req.DefaultRuntimeRef, b.effectiveBotLaunchModel(req.DefaultRuntimeRef, req.DefaultModelRef), req.DefaultReasoningEffort)
	if err != nil {
		return BotSettingsResult{}, err
	}
	requestID := req.RequestID
	if req.RecoveryAction == BotSettingsRecoveryCreateBlank {
		requestID += ":" + valueOrFallback(req.RecoveryAttemptID, "blank")
	}
	sessionName := managedSingletonSessionName(b.ProfileName(), requestID, current.Version, req.DefaultAgent)
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	identity, err := b.executionIdentity(req.OpenID, "feishu-singleton:"+sessionName, target.ID, true, false)
	if err != nil {
		return BotSettingsResult{}, fmt.Errorf("resolve singleton execution identity: %w", err)
	}
	idleTimeout := int64(0)
	session, err := b.sessionStarter.Start(runCtx, sessionlaunch.StartRequest{
		TargetID: target.ID,
		Name:     sessionName,
		Launch:   launch,
		Workspace: sessionlaunch.WorkspaceRequest{
			Cwd:    strings.TrimSpace(cwd),
			Source: "feishu",
			Scope:  b.currentBridgeId(),
		},
		IdleTimeoutSeconds:       &idleTimeout,
		ExecutionIdentity:        identity.Identity,
		LaunchID:                 identity.LaunchID,
		AgentActorToken:          identity.ActorToken,
		AgentActorTokenExpiresAt: identity.ActorTokenExpiresAt,
		SystemInstructions:       b.sessionStartInstructions(),
	})
	if err != nil {
		return BotSettingsResult{}, fmt.Errorf("create singleton session: %w", err)
	}
	if !strings.EqualFold(session.State, "running") {
		return BotSettingsResult{}, fmt.Errorf("singleton session is not ready")
	}
	if strings.TrimSpace(cwd) == "" && strings.TrimSpace(session.Cwd) == "" {
		return BotSettingsResult{}, fmt.Errorf("singleton workspace is not ready")
	}
	binding := BotSession{
		BridgeId:        b.currentBridgeId(),
		Enabled:         true,
		TargetID:        target.ID,
		TargetName:      target.Name,
		SessionName:     session.Name,
		AgentProfile:    session.AgentProfile,
		AgentType:       session.AgentType,
		EffectiveModel:  session.EffectiveModel,
		Cwd:             session.Cwd,
		LastReplyOpenID: strings.TrimSpace(req.OpenID),
	}
	return b.commitReadySingletonBotSettings(req, binding)
}

func (b *Bridge) effectiveBotSettingsModel(defaultModelRef string) string {
	workspaceModel := ""
	if b != nil && b.db != nil {
		workspaceModel = b.db.workspaceDefaults.Model
	}
	return effectiveNewSessionModelRef(workspaceModel, defaultModelRef)
}

func (b *Bridge) effectiveBotLaunchModel(runtimeRef, defaultModelRef string) string {
	if strings.TrimSpace(runtimeRef) != "" {
		return strings.TrimSpace(defaultModelRef)
	}
	return b.effectiveBotSettingsModel(defaultModelRef)
}

func effectiveNewSessionModelRef(workspaceModel, botDefaultModel string) string {
	if workspaceModel = strings.TrimSpace(workspaceModel); workspaceModel != "" {
		return workspaceModel
	}
	return strings.TrimSpace(botDefaultModel)
}

func (b *Bridge) commitReadySingletonBotSettings(req BotSettingsRequest, binding BotSession) (BotSettingsResult, error) {
	key := singletonStreamKey(b.currentBridgeId())
	unlock := b.lockRoute(key)
	defer unlock()
	state, err := b.db.CommitSingletonBotSettingsWithLaunch(b.currentBridgeId(), req.ExpectedVersion, req.DefaultAgent,
		req.DefaultRuntimeRef, req.DefaultModelRef, req.DefaultReasoningEffort, binding)
	if err != nil {
		if err == errBotProfileStateChanged {
			return b.botSettingsStateChangedResult()
		}
		return BotSettingsResult{}, err
	}
	stored, err := b.db.GetBotSession(b.currentBridgeId())
	if err != nil {
		return BotSettingsResult{}, err
	}
	if !botSessionReady(stored) {
		return BotSettingsResult{}, fmt.Errorf("singleton binding is not ready")
	}
	b.startSingletonStream(stored, req.OpenID)
	b.projectLegacyBotSettings(state)
	b.scheduleNativeSlashCommandSync()
	return BotSettingsResult{Status: BotSettingsStatusSuccess, State: state}, nil
}

func (b *Bridge) botSettingsStateChangedResult() (BotSettingsResult, error) {
	state, err := b.currentBotProfileState()
	if err != nil {
		return BotSettingsResult{}, err
	}
	return BotSettingsResult{Status: BotSettingsStatusStateChanged, State: state}, nil
}

func (b *Bridge) currentBotProfileState() (BotProfileState, error) {
	fallback := config.FeishuInteractionThread
	if mode, err := b.legacyInteractionMode(); err == nil && managementValidInteraction(mode) {
		fallback = mode
	}
	return b.db.GetBotProfileState(b.currentBridgeId(), fallback)
}

func (b *Bridge) currentBotProfileStateReadOnly() (BotProfileState, error) {
	fallback := config.FeishuInteractionThread
	if mode, err := b.legacyInteractionMode(); err == nil && managementValidInteraction(mode) {
		fallback = mode
	}
	if b == nil || b.db == nil {
		return BotProfileState{InteractionMode: fallback}, nil
	}
	return b.db.ReadBotProfileState(b.currentBridgeId(), fallback)
}

func effectiveBotDefaultAgentRef(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return agentprofile.DefaultProfileRef
}

func (b *Bridge) projectLegacyBotSettings(state BotProfileState) {
	if err := b.db.SetProfileDefaultAgent(b.currentBridgeId(), state.DefaultAgent); err != nil {
		log.Warn().Err(err).Msg("bridge: project legacy default agent")
	}
	if err := config.SetBridgeFeishuInteractionMode(b.ProfileName(), state.InteractionMode); err != nil {
		log.Warn().Err(err).Msg("bridge: project legacy interaction mode")
	}
}

func (b *Bridge) issueBotSettingsConfirmation(req BotSettingsRequest) string {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s:%d",
			b.currentBridgeId(), req.ExpectedVersion, req.DefaultAgent, req.RequestID, time.Now().UnixNano())))
		copy(raw[:], sum[:len(raw)])
	}
	token := hex.EncodeToString(raw[:])
	b.botSettingsMu.Lock()
	if b.pendingBotSettings == nil {
		b.pendingBotSettings = make(map[string]pendingBotSettingsConfirmation)
	}
	now := time.Now()
	for existing, pending := range b.pendingBotSettings {
		if !pending.ExpiresAt.After(now) {
			delete(b.pendingBotSettings, existing)
		}
	}
	b.pendingBotSettings[token] = pendingBotSettingsConfirmation{
		Request:   req,
		ExpiresAt: now.Add(botSettingsConfirmationTTL),
	}
	b.botSettingsMu.Unlock()
	return token
}

func (c *botSettingsTransitionState) consumeBotSettingsConfirmation(token string, req BotSettingsRequest) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	c.botSettingsMu.Lock()
	defer c.botSettingsMu.Unlock()
	pending, ok := c.pendingBotSettings[token]
	if ok {
		delete(c.pendingBotSettings, token)
	}
	if !ok || !pending.ExpiresAt.After(time.Now()) {
		return false
	}
	return botSettingsConfirmationMatches(pending.Request, req)
}

func (c *botSettingsTransitionState) validateBotSettingsConfirmation(token string, req BotSettingsRequest) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	c.botSettingsMu.Lock()
	defer c.botSettingsMu.Unlock()
	pending, ok := c.pendingBotSettings[token]
	return ok && pending.ExpiresAt.After(time.Now()) && botSettingsConfirmationMatches(pending.Request, req)
}

func botSettingsConfirmationMatches(pending, req BotSettingsRequest) bool {
	return pending.Interaction == req.Interaction &&
		pending.DefaultAgent == req.DefaultAgent &&
		pending.DefaultRuntimeRef == req.DefaultRuntimeRef &&
		pending.DefaultModelRef == req.DefaultModelRef &&
		pending.DefaultReasoningEffort == req.DefaultReasoningEffort &&
		pending.ExpectedVersion == req.ExpectedVersion &&
		pending.RequestID == req.RequestID &&
		pending.OpenID == req.OpenID
}

func managedSingletonSessionName(profile, requestID string, version int64, agent string) string {
	base := sanitizeManagedSessionName(profile)
	if base == "" {
		base = "bot"
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = fmt.Sprintf("%d:%s", version, strings.TrimSpace(agent))
	}
	sum := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("feishu-singleton-%s-%s", base, hex.EncodeToString(sum[:5]))
}

func sanitizeManagedSessionName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
			continue
		}
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "-") {
			out.WriteByte('-')
		}
		if out.Len() >= 24 {
			break
		}
	}
	return strings.Trim(out.String(), "-")
}

func valueOrFallback(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}
