// Package bridge connects the hub to the Lark (Feishu) messaging platform via
// a WebSocket long connection. It routes Lark thread replies to sessions and
// streams agent output back to Lark.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larknormalize "github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkapplication "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/proto"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/bridge/command"
	bridgeprompt "github.com/amuxos/amux/internal/bridge/prompt"
	"github.com/amuxos/amux/internal/bridge/threadcontext"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// Hub is the subset of tunnel.Hub that Bridge needs.
type Hub interface {
	ListTargets() []*pb.Target
	Route(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error)
	RouteStream(ctx context.Context, targetID, method string, payload interface{}, fn func(*pb.TunnelMsg) error) error
}

// SubtaskReader lets the bridge surface distributed-task progress for a bound
// session. Implemented by the amuxd task control plane; nil when the hub has
// no task plane (progress block is simply absent, e.g. local CLI-only).
type SubtaskReader interface {
	// SubtaskIDsByParent lists known task ids whose parent session matches.
	SubtaskIDsByParent(parentSessionID string) []string
	// GetSubtaskJSON returns the TaskRun JSON for one task id.
	GetSubtaskJSON(ctx context.Context, id string) ([]byte, error)
}

// SubtaskEventReader is the optional live-event extension implemented by the
// hub task plane. Snapshot-only readers retain the existing summary behavior.
type SubtaskEventReader interface {
	// LiveSubtaskIDsByParent lists tasks already known to this hub without a
	// fleet-wide refresh. It is used by the low-latency card discovery loop.
	LiveSubtaskIDsByParent(parentSessionID string) []string
	// StreamSubtaskEvents replays events after fromSeq and tails live progress
	// until the task reaches a terminal state or ctx is cancelled.
	StreamSubtaskEvents(ctx context.Context, id string, fromSeq uint64, handle func([]byte) error) error
}

// ManagementSystemInfo is amuxd-level metadata shown by Feishu management
// cards. Health and maintenance state are deliberately computed separately.
type ManagementSystemInfo struct {
	Version   string
	StartedAt time.Time
	BotCount  int
	HubIP     string
}

// ManagementUpdateInfo is the user-facing result of checking the configured
// amux release source. Checking never installs the returned version.
type ManagementUpdateInfo struct {
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	ShouldPrompt    bool
	CheckedAt       time.Time
}

// ManagementSystemActions supplies host-level maintenance operations to the
// Feishu bridge without leaking process or release-source details into it.
type ManagementSystemActions interface {
	Restart(context.Context) error
	CheckUpdate(context.Context) (ManagementUpdateInfo, error)
	UpdateSnapshot() ManagementUpdateInfo
	DismissUpdate(string) error
	Upgrade(context.Context, string) error
}

// ManagementAgentPackActions supplies serialized Agent Pack catalog operations
// without leaking Feishu card payloads into the Agent Profile runtime.
type ManagementAgentPackActions interface {
	Snapshot() (agentprofile.PackCatalogSnapshot, error)
	InstallIfCurrent(context.Context, string, string) (agentprofile.Pack, error)
	SyncIfCurrent(context.Context, string, string) (agentprofile.Pack, error)
	RemoveIfCurrentAndUnreferenced(string, string, func() ([]string, error)) (agentprofile.RemovePackResult, []string, error)
	WithRegistryStable(func() error) error
	WithRegistryStableContext(context.Context, func() error) error
}

// Bridge connects the hub to Lark.
type Bridge struct {
	// turnSources maps turns to the Feishu chat of their first input.
	turnSources    turnSourceChats
	hub            Hub
	sessionStarter sessionlaunch.Starter
	db             *bridgeDB
	client         *lark.Client
	secrets        *Secrets
	profile        string
	bridgeID       string
	identityPort
	isolationSupport isolationSupportChecker

	documentCommentController
	documentCommentAPI
	botE2EConfig config.BridgeFeishuBotE2EConfig

	streamController
	inputRouter
	turnCoalescer
	// showBackgroundTasks is the experimental show_background_tasks switch
	// (default off): when off, background task progress cards are not posted.
	// Applied live from the experiments card.
	showBackgroundTasks atomic.Bool
	queueNotice         queueNoticeState
	steerObservers      steerObserverState
	subtaskCardController
	subtaskCardPort

	subtasks SubtaskReader // hub-side task plane; nil when absent (local CLI-only)

	sessionViewerBaseURL string
	sessionViewerToken   string

	managementSystemInfo   ManagementSystemInfo
	managementActions      ManagementSystemActions
	managementPackActions  ManagementAgentPackActions
	managementRestartDelay time.Duration

	threadContext  *threadcontext.Store
	promptRenderer *bridgeprompt.Renderer
	lookupPort

	groupWatchState
	groupWatchAPI
	capabilityPermissionAuditor
	accessScopePort
	botCoordinationState
	externalBotPort

	permissionGateway
	skillPickerStore

	larkLookupCache
	messagingPort

	botIdentityResolver
	nativeSlashController

	managementSettingsState
	managementMaintenanceState
	managementPackState
	botSettingsTransitionState

	artifactPort
	materialDelivery
}

// LaunchIdentity is everything a Feishu launch needs from the principal
// registry: who it runs as, and the credential the agent inside it presents
// when it calls back into the control plane. amuxd owns identity minting;
// the bridge selects process isolation from the effective conversation policy.
type LaunchIdentity struct {
	Identity            *pb.ExecutionIdentity
	LaunchID            string
	ActorToken          string
	ActorTokenExpiresAt int64
}

// SetExecutionIdentityResolver enables identified Feishu launches without
// coupling the bridge to the amuxd principal registry implementation.
func (b *Bridge) SetExecutionIdentityResolver(fn func(openID, executionRef, targetID string, shared bool) (LaunchIdentity, error)) {
	b.resolveExecutionIdentity = fn
}

func (b *Bridge) executionIdentity(openID, executionRef, targetID string, shared, isolationRequired bool) (LaunchIdentity, error) {
	if b.resolveExecutionIdentity == nil {
		return LaunchIdentity{}, nil
	}
	identity, err := b.resolveExecutionIdentity(strings.TrimSpace(openID), executionRef, strings.TrimSpace(targetID), shared)
	if err != nil {
		return LaunchIdentity{}, err
	}
	// Host isolation availability must not override a conversation's ordinary
	// mode. Keep the principal and credential for authorization, while pinning
	// fresh executions to the selected environment. Never rewrite a frozen backend.
	if !isolationRequired && identity.Identity != nil && identity.Identity.IsolationBackend == "" {
		pinned := proto.Clone(identity.Identity).(*pb.ExecutionIdentity)
		pinned.IsolationBackend = principal.BackendPassthrough
		identity.Identity = pinned
	}
	return identity, nil
}

// currentBotIsolationRequired reports whether the current Bot Profile has the
// 每个话题独立(用户隔离) flag set. It is read fresh on each thread-session
// launch so granting flags post-save takes effect immediately; a read failure
// (no db) is treated as not-required.
func (b *Bridge) currentBotIsolationRequired() bool {
	if b == nil || b.db == nil {
		return false
	}
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), config.FeishuInteractionThread)
	if err != nil {
		return false
	}
	return state.InteractionMode == config.FeishuInteractionThread && state.IsolationRequired
}

// isolationSupportChecker reports whether the environment can launch
// identified (per-principal isolated) executions, with a human-readable
// reason when it cannot. Tests replace it; production uses the default below.
type isolationSupportChecker func() (supported bool, reason string)

// SetIsolationSupportChecker overrides the environment probe used by the
// 每个话题独立(用户隔离) gating. Nil restores the default probe.
func (b *Bridge) SetIsolationSupportChecker(fn isolationSupportChecker) {
	b.isolationSupport = fn
}

// isolationLaunchSupported gates the 用户隔离 mode: the hub must have isolation
// enabled (otherwise amuxd never wires the identity resolver and every launch
// would be anonymous) and at least one registered runtime must report a ready
// isolation helper (helper protocol is only advertised on Linux when the
// helper handshake succeeds).
func (b *Bridge) isolationLaunchSupported() (bool, string) {
	if b.isolationSupport != nil {
		return b.isolationSupport()
	}
	cfg, err := config.GetIsolation()
	if err != nil {
		return false, "读取隔离配置失败"
	}
	if !cfg.Enabled {
		return false, "hub 未启用用户隔离（isolation.enabled = false）"
	}
	if b.hub == nil {
		return false, "hub 不可用"
	}
	for _, target := range b.hub.ListTargets() {
		if target == nil || target.Capabilities == nil {
			continue
		}
		if strings.TrimSpace(target.Capabilities.IsolationHelperProtocol) != "" {
			return true, ""
		}
	}
	return false, "没有隔离 helper 就绪的 Linux 运行环境"
}

type streamFreezeHandler struct {
	token uint64
	fn    func()
}

type sessionStreamRegistration struct {
	rootMsgID string
	token     uint64
	ctx       context.Context
	sessionID string
	launchID  string
}

type managementPackLibraryCardState struct {
	operationActive bool
	viewVersion     uint64
	viewLock        sync.Mutex
}

// BotIdentity identifies the current Feishu bot for structured @ mention matching.
// botIdentityResolver owns cached bot identity freshness state.
type botIdentityResolver struct {
	botIdentityMu        sync.Mutex
	botIdentityCheckedAt time.Time
}

type BotIdentity struct {
	OpenId string
	Name   string
}

type chatBotCountCacheEntry struct {
	Count     int
	FetchedAt time.Time
}

const (
	botIdentityRefreshTTL           = 5 * time.Minute
	maxFeishuMessageEventAge        = 24 * time.Hour
	autoReattachRecentBindingWindow = 30 * time.Minute
)

// DefaultDBPath returns the default bridge DB path.
func DefaultDBPath() string {
	return filepath.Join(amuxhome.Dir(), "bridge.db")
}

func NewWithProfile(hub Hub, profile string, secrets *Secrets, starter sessionlaunch.Starter) (*Bridge, error) {
	if strings.TrimSpace(profile) == "" {
		profile = DefaultProfileName
	}
	db, err := openBridgeDBForProfile(DefaultDBPath(), profile)
	if err != nil {
		return nil, fmt.Errorf("bridge: open db: %w", err)
	}
	client := lark.NewClient(secrets.AppId, secrets.AppSecret,
		larkClientOptions(lark.WithLogLevel(larkcore.LogLevelInfo))...,
	)
	bridgeID := BridgeIdFromAppId(secrets.AppId)
	if bridgeID == "" {
		return nil, fmt.Errorf("bridge: app_id is required")
	}
	if err := db.RecoverExternalBotExchanges(bridgeID, time.Now().UTC()); err != nil {
		// External coordination is optional. Its recovery failure must not take
		// down this profile's existing inbound/session Bridge behavior; adapter
		// construction will retry recovery and degrade only that optional backend.
		log.Warn().Err(err).Str("bridge_id", bridgeID).Msg("bridge: external Bot exchange recovery deferred")
	}
	threadContext, err := threadcontext.OpenStore(threadcontext.DefaultDBPath())
	if err != nil {
		return nil, fmt.Errorf("bridge: open thread context db: %w", err)
	}
	documentComments := config.GetBridgeFeishuDocumentCommentsForProfile(profile)
	b := &Bridge{
		hub:            hub,
		sessionStarter: starter,
		db:             db,
		client:         client,
		secrets:        secrets,
		profile:        profile,
		bridgeID:       bridgeID,
		threadContext:  threadContext,
		promptRenderer: newPromptRendererFromConfig(),
		botE2EConfig:   config.GetBridgeFeishuBotE2EForProfile(profile),
		streamController: streamController{
			streams:      make(map[string]context.CancelFunc),
			streamTokens: make(map[string]uint64),
			streamReady:  make(map[string]chan struct{}),
		},
		inputRouter: inputRouter{
			routeLocks: make(map[string]*sync.Mutex),
			seenMsgs:   make(map[string]struct{}),
		},
		larkLookupCache: larkLookupCache{
			chatBotCountCache: make(map[string]chatBotCountCacheEntry),
		},
		documentCommentController: documentCommentController{
			documentCommentsEnabled:    documentComments.Enabled,
			documentCommentsConfigured: documentComments.Enabled,
			documentStreams:            make(map[string]context.CancelFunc),
			documentStreamReady:        make(map[string]chan struct{}),
			documentTurns:              make(map[string]map[string]*documentTurnDelivery),
			documentCommentSemaphore:   make(chan struct{}, 3),
		},
		groupWatchState: groupWatchState{
			groupWatchConfig:        config.GetBridgeFeishuGroupWatchForProfile(profile),
			groupWatchConfigChanged: make(chan struct{}, 1),
		},
		botCoordinationState: botCoordinationState{
			botToBot: config.GetBridgeFeishuBotToBotForProfile(profile),
		},
	}
	// A process restart loses the in-memory provider-turn correlation. Preserve
	// dedupe and loop counters, but discard in-flight return addresses rather
	// than risk sending a later unrelated human turn to the previous caller Bot.
	if err := db.ClearPendingBotHandoffs(bridgeID); err != nil {
		_ = threadContext.Close()
		_ = db.Close()
		return nil, fmt.Errorf("bridge: clear stale bot handoffs: %w", err)
	}
	if err := b.reconcileBotSettingsOnStartup(); err != nil {
		_ = threadContext.Close()
		_ = db.Close()
		return nil, fmt.Errorf("bridge: reconcile bot settings: %w", err)
	}
	b.steerForeground.Store(config.GetBridgeFeishuSteerForeground())
	config.SetLiveSteerForeground(b.steerForeground.Load())
	b.showBackgroundTasks.Store(config.GetBridgeFeishuShowBackgroundTasks())
	return b, nil
}

func (b *Bridge) reconcileBotSettingsOnStartup() error {
	if b == nil || b.db == nil {
		return nil
	}
	state, err := b.currentBotProfileState()
	if err != nil {
		return err
	}
	binding, err := b.db.GetBotSession(b.currentBridgeId())
	if err != nil {
		return err
	}
	switch {
	case state.InteractionMode == config.FeishuInteractionSingleton && !botSessionReady(binding):
		state, err = b.db.CommitThreadBotSettings(b.currentBridgeId(), state.Version, state.DefaultAgent)
		if err != nil {
			return err
		}
		log.Warn().Str("bridge_id", b.currentBridgeId()).
			Msg("bridge: singleton binding missing at startup; fell back to thread mode")
		b.projectLegacyBotSettings(state)
	case state.InteractionMode == config.FeishuInteractionThread && binding != nil && binding.Enabled:
		state, err = b.db.CommitThreadBotSettings(b.currentBridgeId(), state.Version, state.DefaultAgent)
		if err != nil {
			return err
		}
		log.Warn().Str("bridge_id", b.currentBridgeId()).
			Msg("bridge: disabled stale singleton binding for thread mode")
		b.projectLegacyBotSettings(state)
	}
	return nil
}

// SetSubtaskReader wires the hub-side task control plane for progress display.
// Safe to leave unset (nil): the activity card just omits the subtask block.
func (b *Bridge) SetSubtaskReader(r SubtaskReader) { b.subtasks = r }

// SetManagementSystemInfo supplies amuxd-level metadata used by Feishu
// management cards. It must be called before Start.
func (b *Bridge) SetManagementSystemInfo(info ManagementSystemInfo) {
	if b == nil {
		return
	}
	info.Version = strings.TrimSpace(info.Version)
	info.HubIP = strings.TrimSpace(info.HubIP)
	info.BotCount = max(info.BotCount, 0)
	b.managementSystemInfo = info
}

// SetManagementSystemActions wires owner-only host maintenance actions.
// It must be called before Start.
func (b *Bridge) SetManagementSystemActions(actions ManagementSystemActions) {
	if b == nil {
		return
	}
	b.managementActions = actions
}

// SetManagementAgentPackActions wires the installation-wide Pack catalog
// manager shared by all Feishu Bot profiles.
func (b *Bridge) SetManagementAgentPackActions(actions ManagementAgentPackActions) {
	if b == nil {
		return
	}
	b.managementPackActions = actions
}

func newPromptRendererFromConfig() *bridgeprompt.Renderer {
	cfg := config.GetBridgeFeishuPrompt()
	topicRootEnabled := cfg.TopicRootEnabled
	renderer, err := bridgeprompt.NewRendererFromOptions(bridgeprompt.Options{
		SharedThreadEnabled:      cfg.SharedThreadEnabled,
		TopicRootEnabled:         &topicRootEnabled,
		SharedThreadTemplateFile: cfg.SharedThreadTemplateFile,
	})
	if err != nil {
		log.Warn().Err(err).Str("template_file", cfg.SharedThreadTemplateFile).Msg("bridge: failed to load Feishu prompt template, using default")
		return bridgeprompt.NewRenderer(bridgeprompt.Options{
			SharedThreadEnabled: cfg.SharedThreadEnabled,
			TopicRootEnabled:    &topicRootEnabled,
		})
	}
	return renderer
}

func (b *Bridge) currentBridgeId() string {
	if b == nil {
		return ""
	}
	if strings.TrimSpace(b.bridgeID) != "" {
		return b.bridgeID
	}
	if b.secrets != nil {
		return BridgeIdFromAppId(b.secrets.AppId)
	}
	return ""
}

func (b *Bridge) saveProfileSecrets() error {
	if b == nil || b.secrets == nil {
		return nil
	}
	profile := strings.TrimSpace(b.profile)
	if profile == "" {
		profile = DefaultProfileName
	}
	return SaveProfileSecrets(profile, b.secrets)
}

// FetchBotIdentity fetches the current Feishu bot identity using the official
// bot/v3/info endpoint. The generated SDK does not expose this endpoint.
func FetchBotIdentity(ctx context.Context, client *lark.Client) (BotIdentity, error) {
	if client == nil {
		return BotIdentity{}, fmt.Errorf("nil lark client")
	}
	resp, err := client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return BotIdentity{}, err
	}
	if resp == nil {
		return BotIdentity{}, fmt.Errorf("empty response from bot/v3/info")
	}
	if resp.StatusCode != 200 {
		return BotIdentity{}, fmt.Errorf("bot/v3/info status %d", resp.StatusCode)
	}
	var out struct {
		Code int `json:"code"`
		Bot  struct {
			OpenId  string `json:"open_id"`
			AppName string `json:"app_name"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &out); err != nil {
		return BotIdentity{}, fmt.Errorf("parse bot/v3/info: %w", err)
	}
	if out.Code != 0 {
		return BotIdentity{}, fmt.Errorf("bot/v3/info code %d", out.Code)
	}
	return BotIdentity{
		OpenId: strings.TrimSpace(out.Bot.OpenId),
		Name:   strings.TrimSpace(out.Bot.AppName),
	}, nil
}

func FetchBotIdentityWithCredentials(ctx context.Context, appID, appSecret string) (BotIdentity, error) {
	return FetchBotIdentity(ctx, lark.NewClient(appID, appSecret, larkClientOptions(lark.WithLogLevel(larkcore.LogLevelInfo))...))
}

func (b *Bridge) newEventDispatcher() *dispatcher.EventDispatcher {
	disp := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			return b.handleMessage(ctx, event)
		}).
		// Feishu echoes reactions written by the app even when the application
		// did not subscribe to reaction events. Acknowledge that delivery so the
		// SDK does not fail and redeliver the surrounding event batch. This is
		// transport hygiene, not a bridge capability: the provisioning manifest
		// intentionally remains free of reaction events and reaction-read scope.
		OnP2MessageReactionCreatedV1(func(context.Context, *larkim.P2MessageReactionCreatedV1) error {
			return nil
		}).
		OnP2ChatAccessEventBotP2pChatEnteredV1(func(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
			return b.handleP2PChatEntered(ctx, event)
		}).
		OnP2BotMenuV6(func(ctx context.Context, event *larkapplication.P2BotMenuV6) error {
			return b.handleBotMenu(ctx, event)
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return b.handleCardAction(ctx, event)
		})
	disp.OnP2NoticeCommentAddV1(func(ctx context.Context, event *larkdrive.P2NoticeCommentAddV1) error {
		if event == nil {
			return nil
		}
		return b.handleDocumentComment(ctx, larknormalize.ParseComment(event.EventReq))
	})
	return disp
}

// Start connects to Lark via WebSocket and blocks until ctx is cancelled.
func (b *Bridge) Start(ctx context.Context) error {
	identityCtx, identityCancel := context.WithTimeout(ctx, 10*time.Second)
	if b.ensureBotIdentity(identityCtx) {
		_ = b.saveProfileSecrets()
	}
	identityCancel()
	if b.groupWatchConfigured() {
		if mode, err := b.interactionMode(); err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: failed to validate group_watch interaction mode")
		} else if err := b.validateGroupWatchInteractionMode(mode); err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: group_watch polling paused")
		}
	}
	go b.runGroupWatchPoller(ctx)
	go b.runExternalBotCatalogRefresher(ctx)
	if b.documentCommentsActive() {
		subscribeCtx, subscribeCancel := context.WithTimeout(ctx, 10*time.Second)
		if err := b.prepareDocumentCommentSource(subscribeCtx); err != nil {
			log.Warn().Err(err).Msg("bridge: document comment subscription unavailable")
		}
		subscribeCancel()
	}
	disp := b.newEventDispatcher()

	appID, appSecret := b.secrets.AppId, b.secrets.AppSecret
	supervisor := newLarkWSSupervisor(larkWSWatchdogConfigFromEnv(larkWSEnvLookup), func(hooks larkWSHooks) larkWSClient {
		return larkws.NewClient(appID, appSecret,
			larkws.WithEventHandler(disp),
			larkws.WithAutoReconnect(true),
			larkws.WithLogLevel(larkcore.LogLevelDebug),
			larkws.WithLogger(hooks.Logger),
			larkws.WithOnReady(hooks.OnReady),
			larkws.WithOnReconnected(hooks.OnReconnected),
			larkws.WithOnReconnecting(hooks.OnReconnecting),
			larkws.WithOnDisconnected(hooks.OnDisconnected),
		)
	})
	supervisor.profile = b.ProfileName()
	// Cached tenant tokens may have expired during the suspend.
	supervisor.onSuspend = func() { sharedLarkTokenCache.Invalidate("host suspend") }
	supervisor.onConnected = func() {
		log.Info().Msg("bridge: connected to Lark")
		b.startFeishuPermissionAudit(ctx)
		b.startNativeSlashCommandSync(ctx)
		b.startPendingManagementRecovery(ctx)
	}
	// Both SDK-internal reconnects and watchdog-driven client restarts land
	// here, so pending management recovery reruns after either.
	supervisor.onReconnected = func() {
		log.Info().Msg("bridge: reconnected to Lark")
		b.startPendingManagementRecovery(ctx)
	}
	return supervisor.Run(ctx)
}

// OnSessionAdded is called by the hub session event handler when a new session
// appears on any target.
func (b *Bridge) OnSessionAdded(targetID, targetName string, session *pb.Session) {
	if session == nil {
		return
	}
	// Serialize restart reconciliation with singleton input and binding changes.
	// Otherwise an async session-added callback can revive a stale binding after
	// A card, CLI, or hub transition has already installed the new state.
	unlock := b.lockRoute(singletonStreamKey(b.currentBridgeId()))
	defer unlock()
	effectiveModel := strings.TrimSpace(session.EffectiveModel)
	if effectiveModel == "" {
		effectiveModel, _ = agentprofile.AgentArgsModel(session.AgentType, session.ExtraArgs)
	}
	if err := b.db.RefreshSessionIdentity(b.currentBridgeId(), targetID, session.Name, session.AgentProfile, session.AgentType, effectiveModel); err != nil {
		log.Warn().Err(err).Str("session", session.Name).Str("target_id", targetID).
			Msg("bridge: refresh binding session identity")
	} else if session.State != "running" {
		// Running sessions clear discovery only when startStream installs a
		// new attachment. Duplicate Added notifications retain its commands.
		b.resetSingletonNativeSlashCommands(targetID, session.Name)
	}
	// Runtime registration snapshots include sleeping and archived sessions too.
	// Refresh their presentation identity so a bridge restart can repair older
	// bindings, but never reattach output unless the session is running.
	if session.State != "running" {
		return
	}

	// Singleton mode owns the reattach: a stale lark_chats row for the same
	// (target, session) must not divert the stream to a thread root — singleton
	// output routes to LastReply* on the bot-session record, not to any thread.
	mode, err := b.interactionMode()
	if err != nil {
		log.Warn().Err(err).Str("session", session.Name).Str("target_id", targetID).
			Msg("bridge: invalid Feishu interaction mode, skip automatic stream reattach")
		return
	}
	if mode == config.FeishuInteractionSingleton {
		if b.reattachSingletonStreamIfBoundLocked(targetID, session.Name) {
			return
		}
		log.Debug().Str("session", session.Name).Str("target_id", targetID).Str("target_name", targetName).
			Msg("bridge: singleton mode has no matching bot-session binding, checking recent thread binding")
	}
	if b.reattachGroupSessionStream(targetID, session) {
		return
	}
	existing, err := b.db.FindRecentBridgeSession(
		b.currentBridgeId(),
		targetID,
		session.Name,
		time.Now().Add(-autoReattachRecentBindingWindow).Unix(),
	)
	if err != nil {
		log.Warn().Err(err).Str("session", session.Name).Str("target_id", targetID).
			Msg("bridge: find recent Feishu binding for automatic stream reattach")
		return
	}
	if existing != nil && existing.Status == "active" {
		log.Debug().Str("session", session.Name).Str("root_msg_id", existing.RootMsgID).
			Msg("bridge: ensuring stream for recent active thread")
		b.startStream(existing.RootMsgID, targetID, session.Name, existing.AgentProfile, session.AgentType, session.EffectiveModel, existing.OpenId)
		return
	}
	log.Debug().Str("session", session.Name).Str("target_id", targetID).Str("target_name", targetName).
		Msg("bridge: session has no recent active Feishu binding, skip automatic stream reattach")
}

// ProfileName returns the local Feishu profile managed by this bridge.
func (b *Bridge) ProfileName() string {
	if b == nil || strings.TrimSpace(b.profile) == "" {
		return DefaultProfileName
	}
	return strings.TrimSpace(b.profile)
}

func (b *Bridge) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil {
		return nil
	}
	msg := event.Event.Message
	sender := event.Event.Sender

	log.Info().
		Str("chat_type", stringPtr(msg.ChatType)).
		Str("root_id", stringPtr(msg.RootId)).
		Str("parent_id", stringPtr(msg.ParentId)).
		Str("thread_id", stringPtr(msg.ThreadId)).
		Str("message_id", stringPtr(msg.MessageId)).
		Str("sender_type", stringPtr(sender.SenderType)).
		Str("text_preview", textPreview(extractText(msg.Content), 40)).
		Msg("bridge: incoming message")

	if isStaleFeishuMessage(msg, time.Now()) {
		log.Warn().
			Str("message_id", stringPtr(msg.MessageId)).
			Str("create_time", stringPtr(msg.CreateTime)).
			Msg("bridge: stale Feishu message event ignored")
		return nil
	}

	chatType := stringPtr(msg.ChatType)
	openID := ""
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
		openID = *sender.SenderId.OpenId
	}
	msgID := stringPtr(msg.MessageId)
	text := strings.TrimSpace(extractText(msg.Content))
	imageRefs := parseFeishuImageRefs(msgID, msg.Content)
	isGroupMessage := chatType == "group" || chatType == "topic_group"
	isGroupWatchCandidate := isGroupMessage && b.groupWatchActive() && b.groupWatchHasChat(stringPtr(msg.ChatId))
	isBotE2ECandidate := b.isBotE2ECandidate(isGroupMessage, stringPtr(sender.SenderType), openID)
	isBotToBotCandidate := isGroupMessage && b.botToBotSnapshot().Enabled
	senderType := strings.ToLower(strings.TrimSpace(stringPtr(sender.SenderType)))
	isExternalBotReplyCandidate := isGroupMessage && (senderType == "app" || senderType == "bot")
	if sender.SenderType != nil && *sender.SenderType != "user" && !isGroupWatchCandidate && !isBotE2ECandidate && !isBotToBotCandidate && !isExternalBotReplyCandidate {
		return nil
	}
	identityCtx, identityCancel := context.WithTimeout(ctx, 10*time.Second)
	identityChanged := b.ensureBotIdentity(identityCtx)
	identityCancel()
	if identityChanged {
		if err := b.saveProfileSecrets(); err == nil {
			log.Info().Str("bot_open_id", b.secrets.BotOpenId).Str("bot_name", b.secrets.BotName).Msg("bridge: auto-captured bot identity")
		}
	}

	if isGroupMessage {
		if handled, err := b.handleGroupWatchEvent(ctx, event); handled || err != nil {
			return err
		}
	}
	if isExternalBotReplyCandidate {
		// Exchange correlation precedes /e2e parsing. An allowlisted test Bot
		// replying to an outbound delegation must remain untrusted result data;
		// it must never be projected into an owner/shared user identity.
		if handled, err := b.tryCompleteExternalBotExchange(msg, openID, text); handled || err != nil {
			return err
		}
	}
	ctx = withFeishuImages(ctx, feishuImageContext{
		chatID: stringPtr(msg.ChatId), openID: openID,
	})

	materialText := collectMessageInput(ctx, msgID, stringPtr(msg.MessageType), msg.Content)
	if text == "" && (len(materialsFromContext(ctx).files) > 0 || stringPtr(msg.MessageType) == "merge_forward") {
		text = materialText
	}
	botE2ERequest, isBotE2E := b.botE2ERequest(msg, text, openID, isBotE2ECandidate)
	if !isBotE2E && senderType != "" && senderType != "user" {
		// Not a trusted /e2e test input: route external app/bot senders to the
		// native bot-to-bot handoff and ignore every other non-user sender.
		if senderType == "app" || senderType == "bot" {
			return b.handleExternalBotMessage(ctx, msg, openID, text)
		}
		return nil
	}
	if isBotE2E {
		ownerOpenID := ""
		if b.secrets != nil {
			ownerOpenID = strings.TrimSpace(b.secrets.OpenId)
		}
		role := b.botE2ERoleForSender(openID)
		switch role {
		case "":
			// No explicit mapping: v0.267 default, project as owner.
			if ownerOpenID == "" {
				log.Warn().Str("test_bot_open_id", openID).Msg("bridge: bot E2E request ignored because profile owner is not confirmed")
				return nil
			}
			log.Info().
				Str("test_bot_open_id", openID).
				Str("owner_open_id", ownerOpenID).
				Str("message_id", msgID).
				Msg("bridge: accepted bot E2E request")
			openID = ownerOpenID
			text = botE2ERequest
		case botE2ERoleShared:
			// Map to the test bot itself as the shared user; whether it may
			// enter is decided downstream by the target chat workspace mode
			// (same as a real shared user). ownerOpenID staying empty only
			// matters for owner-coded diagnostics; shared path never needs it.
			log.Info().
				Str("test_bot_open_id", openID).
				Str("projected_role", role).
				Str("message_id", msgID).
				Msg("bridge: accepted bot E2E request as shared role")
			text = botE2ERequest
		default:
			// Unknown / unauthorized role must fail closed instead of silently
			// projecting to owner (the test entry point must not become an
			// identity-bypass channel).
			log.Warn().
				Str("test_bot_open_id", openID).
				Str("projected_role", role).
				Msg("bridge: bot E2E request ignored because sender_roles value is invalid")
			return nil
		}
	}
	ctx = withFeishuQuotedMessage(ctx, feishuQuotedMessageRef{
		messageID: strings.TrimSpace(stringPtr(msg.ParentId)),
		chatID:    strings.TrimSpace(stringPtr(msg.ChatId)),
	})
	if text == "" && len(imageRefs) == 0 {
		return nil
	}

	if isGroupMessage {
		chatID := stringPtr(msg.ChatId)
		hasImages := len(imageRefs) > 0
		hasQuotedMessage := strings.TrimSpace(stringPtr(msg.ParentId)) != ""
		mention := b.messageBotMentionInfo(msg)
		cmdText := b.stripCurrentBotMention(text, mention)
		text = normalizeMentionTokens(text, mention.Replacements)
		if blocked, err := b.blockUnmentionedGroupMessage(ctx, chatID, msg, text, mention); err != nil || blocked {
			return err
		}
		publicScope := command.ScopeGroup
		if isTopicMessage(msg) {
			if messageRootID(msg) == "" {
				publicScope = command.ScopeFreshTopic
			} else {
				publicScope = command.ScopeActiveTopic
			}
		}
		if agentRef, ok := parseNaturalWorkspaceDefaultAgent(cmdText); ok {
			return b.handleNaturalWorkspaceDefaultAgent(ctx, chatID, msgID, openID, agentRef)
		}
		publicIntent := command.Resolve(publicScope, cmdText)
		groupIntent := command.Resolve(command.ScopeGroup, cmdText)
		isPublicCommand := publicIntent != command.CommandNone ||
			groupIntent == command.CommandCollaborationOn ||
			groupIntent == command.CommandCollaborationOff ||
			isGroupWatchIntent(groupIntent)
		if mention.Mentioned && strings.HasPrefix(cmdText, "/") &&
			(isSingletonSessionCommand(cmdText) || isSkillSlashCommand(cmdText) || (!isPublicCommand && threadCommand(cmdText) == command.CommandNone)) {
			settings, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID)
			if err != nil {
				return err
			}
			if settings != nil && settings.InteractionMode == config.FeishuInteractionSingleton {
				rootID := messageRootID(msg)
				if rootID == "" {
					rootID = msgID
				}
				return b.routeToSessionInChatWithMention(ctx, chatID, rootID, msgID, openID, text, mention)
			}
		}
		if routedText := b.stripCurrentBotMention(text, mention); mention.Mentioned && isSingletonSessionCommand(routedText) {
			if handled, err := b.routeBotSessionInput(ctx, botSessionInput{
				ChatID:    chatID,
				MessageID: msgID,
				OpenID:    openID,
				Text:      routedText,
				ReplyKind: botSessionReplyGroup,
			}); handled || err != nil {
				return err
			}
		}
		if routedText := b.stripCurrentBotMention(text, mention); routedText != "" && isSkillSlashCommand(routedText) {
			if handled, err := b.routeBotSessionInput(ctx, botSessionInput{
				ChatID:    chatID,
				MessageID: msgID,
				OpenID:    openID,
				Text:      routedText,
				ReplyKind: botSessionReplyGroup,
			}); handled || err != nil {
				return err
			}
		}
		if routedText := b.stripCurrentBotMention(text, mention); !isPublicCommand && mention.Mentioned && (routedText != "" || hasImages || hasQuotedMessage) &&
			(!strings.HasPrefix(routedText, "/") || b.hasSingletonAdvertisedAgentCommand(routedText)) {
			if handled, err := b.routeBotSessionInput(ctx, botSessionInput{
				ChatID:    chatID,
				MessageID: msgID,
				OpenID:    openID,
				Text:      routedText,
				ReplyKind: botSessionReplyGroup,
			}); handled || err != nil {
				return err
			}
		}

		if !isTopicMessage(msg) {
			if intent := command.Resolve(command.ScopeGroup, cmdText); intent != command.CommandNone {
				return b.handleGroupPublicCommand(ctx, chatID, msgID, openID, intent, mention.Mentioned)
			}
			switch threadCommand(cmdText) {
			case command.CommandWorkspace:
				return b.handleWorkspaceRootCommandReply(ctx, chatID, msgID, openID, cmdText)
			case command.CommandRun, command.CommandBind:
				if msgID == "" {
					return nil
				}
				return b.routeToSessionInChatWithMention(ctx, chatID, msgID, msgID, openID, cmdText, mention)
			case command.CommandStatus, command.CommandUnbind, command.CommandStop, command.CommandRestart,
				command.CommandInterrupt, command.CommandQueue, command.CommandQueueClear, command.CommandQueueCancel:
				return nil
			default:
				if agentRef, ok := parseNaturalWorkspaceDefaultAgent(cmdText); ok {
					return b.handleNaturalWorkspaceDefaultAgent(ctx, chatID, msgID, openID, agentRef)
				}
				routedText := b.stripCurrentBotMention(text, mention)
				if mention.Mentioned && msgID != "" && routedText == "" && !hasImages && !hasQuotedMessage {
					if err := b.ensureWorkspaceForChat(chatID); err != nil {
						return err
					}
					if !b.ownerConfirmed() {
						return b.notifyThread(ctx, msgID, ownerConfirmationRequiredText())
					}
					return b.notifyThreadHelpCard(ctx, msgID, renderSessionEntryHelpCard())
				} else if mention.Mentioned && msgID != "" && (routedText != "" || hasImages || hasQuotedMessage) && !strings.HasPrefix(routedText, "/") {
					if err := b.ensureWorkspaceForChat(chatID); err != nil {
						return err
					}
					if !b.ownerConfirmed() {
						return b.notifyThread(ctx, msgID, ownerConfirmationRequiredText())
					}
					return b.routeToSessionInChatWithMention(ctx, chatID, msgID, msgID, openID, text, mention)
				} else if strings.HasPrefix(cmdText, "/setup") || cmdText == "setup" {
					return b.notifyRootCommand(ctx, chatID, msgID, "⚠️ /setup 已移除。请在群里发送 /workspace init。")
				} else if isAmuxManagementCommand(cmdText) {
					return b.notifyRootCommand(ctx, chatID, msgID, "请私聊智能体管理 amux。")
				} else if strings.HasPrefix(cmdText, "/") {
					return nil
				}
			}
			return nil
		}

		rootID := messageRootID(msg)
		if rootID == "" {
			if intent := command.Resolve(command.ScopeFreshTopic, cmdText); intent != command.CommandNone {
				if intent == command.CommandAmux {
					return b.handleGroupPublicCommand(ctx, chatID, msgID, openID, intent, mention.Mentioned)
				}
				if intent == command.CommandHelp {
					if err := b.ensureWorkspaceForChat(chatID); err != nil {
						return err
					}
					if !b.ownerConfirmed() {
						return b.notifyRootCommand(ctx, chatID, msgID, ownerConfirmationRequiredText())
					}
					allowed, err := b.canUseBotInGroup(chatID, openID)
					if err != nil {
						return err
					}
					if !allowed {
						return b.notifyRootCommand(ctx, chatID, msgID, memberBlockedInOwnerModeText())
					}
					return b.notifyRootHelpCard(ctx, chatID, msgID, renderSessionEntryHelpCard())
				}
			}
			if intent := command.Resolve(command.ScopeGroup, cmdText); intent == command.CommandCollaborationOn ||
				intent == command.CommandCollaborationOff || isGroupWatchIntent(intent) {
				return b.handleGroupPublicCommand(ctx, chatID, msgID, openID, intent, mention.Mentioned)
			}
			switch threadCommand(cmdText) {
			case command.CommandWorkspace:
				return b.handleWorkspaceRootCommandReply(ctx, chatID, msgID, openID, cmdText)
			case command.CommandRun, command.CommandBind:
				if msgID == "" {
					return nil
				}
				return b.routeToSessionInChatWithMention(ctx, chatID, msgID, msgID, openID, cmdText, mention)
			default:
				if strings.HasPrefix(cmdText, "/setup") || cmdText == "setup" {
					return b.notifyRootCommand(ctx, chatID, msgID, "⚠️ /setup 已移除。请在群里发送 /workspace init。")
				} else if isAmuxManagementCommand(cmdText) {
					return b.notifyRootCommand(ctx, chatID, msgID, "请私聊智能体管理 amux。")
				} else if agentRef, ok := parseNaturalWorkspaceDefaultAgent(cmdText); ok {
					return b.handleNaturalWorkspaceDefaultAgent(ctx, chatID, msgID, openID, agentRef)
				} else {
					if strings.HasPrefix(cmdText, "/") {
						return nil
					}
					isWorkspace, err := b.isWorkspaceChat(chatID)
					if err != nil {
						return err
					}
					routedText := b.stripCurrentBotMention(text, mention)
					if msgID != "" && mention.Mentioned && routedText == "" && !hasImages && !hasQuotedMessage {
						if err := b.ensureWorkspaceForChat(chatID); err != nil {
							return err
						}
						if !b.ownerConfirmed() {
							return b.notifyThread(ctx, msgID, ownerConfirmationRequiredText())
						}
						return b.notifyThreadHelpCard(ctx, msgID, renderSessionEntryHelpCard())
					} else if msgID != "" && mention.Mentioned && (routedText != "" || hasImages || hasQuotedMessage) && !strings.HasPrefix(routedText, "/") {
						if err := b.ensureWorkspaceForChat(chatID); err != nil {
							return err
						}
						if !b.ownerConfirmed() {
							return b.notifyThread(ctx, msgID, ownerConfirmationRequiredText())
						}
						return b.routeToSessionInChatWithMention(ctx, chatID, msgID, msgID, openID, text, mention)
					} else if isWorkspace && msgID != "" {
						b.recordRootThreadContext(ctx, chatID, msgID, "user", openID, text)
					}
				}
			}
			return nil
		}
		if isGroupWatchIntent(groupIntent) {
			return b.notifyRootCommand(ctx, chatID, msgID, "群值守命令仅支持在群主聊天或话题根消息中 @当前智能体 使用。")
		}

		isWorkspace, err := b.isWorkspaceChat(chatID)
		if err != nil {
			return err
		}
		if !isWorkspace {
			return nil
		}
		return b.routeToSessionInChatWithMention(ctx, chatID, rootID, msgID, openID, text, mention)
	}

	if chatType != "p2p" {
		return nil
	}

	return b.handlePrivateMessage(ctx, msg, openID, text)
}

func (b *Bridge) handlePrivateMessage(ctx context.Context, msg *larkim.EventMessage, openID, text string) error {
	if b.secrets.OpenId == "" && openID != "" {
		b.secrets.OpenId = openID
		if err := b.saveProfileSecrets(); err == nil {
			log.Info().Str("open_id", openID).Msg("bridge: auto-captured owner open_id")
		}
		b.triggerFeishuPermissionAudit(ctx)
		_, _, _ = b.notifyDM(ctx, openID,
			fmt.Sprintf("✅ 已记录你的 Open ID：%s\n以后可直接使用 /help 查看命令。", openID))
		return nil
	}

	if !b.ownerConfirmed() || !b.canUseBridge(openID) {
		_, _, err := b.notifyDM(ctx, openID, privateWorkspaceBlockedText())
		return err
	}

	mention := b.messageBotMentionInfo(msg)
	cmdText := b.stripCurrentBotMention(text, mention)
	text = normalizeMentionTokens(text, mention.Replacements)
	if b.isPrivateManagementCommand(cmdText) {
		return b.handleCommand(ctx, openID, cmdText)
	}

	chatID := strings.TrimSpace(stringPtr(msg.ChatId))
	if chatID == "" {
		return nil
	}
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), chatID, WorkspaceKindPrivate); err != nil {
		return err
	}
	rootID := messageRootID(msg)
	msgID := stringPtr(msg.MessageId)
	if rootID == "" {
		rootID = msgID
	}
	if rootID == "" {
		return nil
	}

	if cmd := threadCommand(cmdText); cmd == command.CommandWorkspace {
		return b.handleWorkspaceRootCommandReply(ctx, chatID, rootID, openID, cmdText)
	}
	if agentRef, ok := parseNaturalWorkspaceDefaultAgent(cmdText); ok {
		return b.handleNaturalWorkspaceDefaultAgent(ctx, chatID, rootID, openID, agentRef)
	}
	if strings.HasPrefix(cmdText, "/") && threadCommand(cmdText) == command.CommandNone && !b.hasSingletonAdvertisedAgentCommand(cmdText) {
		return b.handleCommand(ctx, openID, cmdText)
	}
	if handled, err := b.routeBotSessionInput(ctx, botSessionInput{
		ChatID:    chatID,
		MessageID: msgID,
		OpenID:    openID,
		Text:      strings.TrimSpace(text),
		ReplyKind: botSessionReplyDM,
	}); handled || err != nil {
		return err
	}

	mention.Mentioned = true
	return b.routeToSessionInChatWithMention(ctx, chatID, rootID, msgID, openID, text, mention)
}

func (b *Bridge) isPrivateManagementCommand(text string) bool {
	if command.Resolve(command.ScopePrivate, text) != command.CommandNone {
		return true
	}
	if _, ok := parseNaturalProfileDefaultAgent(text); ok {
		return true
	}
	cmdText, ok := slashCommandText(text)
	if !ok {
		return false
	}
	parts := strings.Fields(strings.ToLower(cmdText))
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "help", "h", "?":
		return true
	default:
		return false
	}
}

func (b *Bridge) blockUnmentionedGroupMessage(ctx context.Context, chatID string, msg *larkim.EventMessage, text string, mention botMentionInfo) (bool, error) {
	if mention.Mentioned {
		return false, nil
	}
	if mention.MentionedOtherBot {
		return true, nil
	}
	count, err := b.chatBotCount(ctx, chatID)
	if err == nil && count == 1 {
		return false, nil
	}
	if err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("bridge: failed to determine Feishu chat bot count; requiring explicit @bot")
	}
	topicCommand := freshThreadCommand(text)
	if isTopicMessage(msg) || strings.HasPrefix(strings.TrimSpace(text), "/") {
		topicCommand = threadCommand(text)
	}
	if topicCommand == command.CommandNone && command.Resolve(command.ScopeGroup, text) == command.CommandNone {
		if b.shouldAllowUnmentionedSharedThreadContext(chatID, messageRootID(msg)) {
			return false, nil
		}
		return true, nil
	}
	guidance := multiBotMentionRequiredText()
	if rootID := messageRootID(msg); rootID != "" {
		return true, b.notifyThread(ctx, rootID, guidance)
	}
	if msgID := strings.TrimSpace(stringPtr(msg.MessageId)); msgID != "" {
		return true, b.notifyThread(ctx, msgID, guidance)
	}
	_, _, notifyErr := b.notifyChat(ctx, chatID, guidance)
	return true, notifyErr
}

func (b *Bridge) shouldAllowUnmentionedSharedThreadContext(chatID, rootMsgID string) bool {
	if b == nil || b.db == nil || strings.TrimSpace(chatID) == "" || strings.TrimSpace(rootMsgID) == "" {
		return false
	}
	chat, err := b.db.FindByRootMsg(b.currentBridgeId(), rootMsgID)
	if err != nil || chat == nil || chat.Status != ChatStatusActive {
		return false
	}
	ws, err := b.currentWorkspace(chatID)
	return err == nil && ws != nil && ws.Mode == WorkspaceModeShared
}

func multiBotMentionRequiredText() string {
	return "当前群可能有多个智能体，请显式 @目标智能体 后再发送 amux 命令。"
}

func isStaleFeishuMessage(msg *larkim.EventMessage, now time.Time) bool {
	if msg == nil || msg.CreateTime == nil || strings.TrimSpace(*msg.CreateTime) == "" {
		return false
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(*msg.CreateTime), 10, 64)
	if err != nil || ms <= 0 {
		return false
	}
	createdAt := time.UnixMilli(ms)
	return now.Sub(createdAt) > maxFeishuMessageEventAge
}

func (b *Bridge) configureWorkspace(chatID string) {
	if b.secrets == nil {
		b.secrets = &Secrets{}
	}
	b.secrets.NotifyChatId = chatID
}

func (b *Bridge) notifyChat(ctx context.Context, chatID, text string) (string, string, error) {
	if b.chatNotice != nil {
		return b.chatNotice(ctx, chatID, text)
	}
	return b.sendToChat(ctx, chatID, text)
}

func (b *Bridge) notifyDM(ctx context.Context, openID, text string) (string, string, error) {
	if b.dmNotice != nil {
		return b.dmNotice(ctx, openID, text)
	}
	return b.sendDM(ctx, openID, text)
}

func (b *Bridge) ensureBotIdentity(ctx context.Context) bool {
	if b == nil || b.secrets == nil {
		return false
	}
	if b.secrets.AppId == "" || b.secrets.AppSecret == "" {
		return false
	}
	if strings.TrimSpace(b.secrets.BotOpenId) != "" && strings.TrimSpace(b.secrets.BotName) != "" {
		b.botIdentityMu.Lock()
		recent := !b.botIdentityCheckedAt.IsZero() && time.Since(b.botIdentityCheckedAt) < botIdentityRefreshTTL
		b.botIdentityMu.Unlock()
		if recent {
			return false
		}
	}
	lookup := b.botIdentityLookup
	if lookup == nil {
		lookup = func(ctx context.Context) (BotIdentity, error) {
			return FetchBotIdentity(ctx, b.client)
		}
	}
	identity, err := lookup(ctx)
	if err != nil {
		log.Debug().Err(err).Msg("bridge: bot identity lookup failed")
		return false
	}
	b.botIdentityMu.Lock()
	b.botIdentityCheckedAt = time.Now()
	b.botIdentityMu.Unlock()
	changed := false
	if openID := strings.TrimSpace(identity.OpenId); openID != "" && strings.TrimSpace(b.secrets.BotOpenId) != openID {
		b.secrets.BotOpenId = openID
		changed = true
	}
	if name := strings.TrimSpace(identity.Name); name != "" && strings.TrimSpace(b.secrets.BotName) != name {
		b.secrets.BotName = name
		changed = true
	}
	return changed
}

func messageRootID(msg *larkim.EventMessage) string {
	if msg == nil {
		return ""
	}
	if msg.RootId != nil && *msg.RootId != "" {
		return *msg.RootId
	}
	if msg.ParentId != nil && *msg.ParentId != "" {
		return *msg.ParentId
	}
	return ""
}

func isTopicMessage(msg *larkim.EventMessage) bool {
	if msg == nil {
		return false
	}
	chatType := stringPtr(msg.ChatType)
	if chatType == "topic_group" {
		return true
	}
	return chatType == "group" && strings.TrimSpace(stringPtr(msg.ThreadId)) != ""
}

type botMentionInfo struct {
	Mentioned         bool
	MentionedOtherBot bool
	PreserveText      bool
	AmuxDelegation    bool
	Tokens            []string
	Replacements      map[string]string
	ExternalBot       bool
}

func (b *Bridge) textBotMentionInfo(text string) botMentionInfo {
	token := b.configuredBotMentionToken(text)
	if token == "" {
		return botMentionInfo{}
	}
	return botMentionInfo{Mentioned: true, Tokens: []string{token}}
}

func (b *Bridge) messageBotMentionInfo(msg *larkim.EventMessage) botMentionInfo {
	if msg == nil {
		return botMentionInfo{}
	}
	if len(msg.Mentions) == 0 {
		return b.textBotMentionInfo(strings.TrimSpace(extractText(msg.Content)))
	}
	var info botMentionInfo
	for _, mention := range msg.Mentions {
		if mention == nil {
			continue
		}
		info.addReplacement(mention)
		mentionedType := ""
		if mention.MentionedType != nil {
			mentionedType = strings.ToLower(strings.TrimSpace(*mention.MentionedType))
		}
		isAppMention := mentionedType == "app" || mentionedType == "bot"
		mentionedBot := false
		if mentionHasAnyID(mention.Id) && b.mentionIDMatchesBot(mention.Id) {
			mentionedBot = true
		} else if mentionedType != "" && !isAppMention {
			continue
		} else if isAppMention {
			mentionedBot = b.isConfiguredBotMention(mention)
		} else {
			if mention.Name != nil && b.isBotMentionName(*mention.Name) {
				mentionedBot = true
			}
			if mention.Key != nil && b.isBotMentionToken(*mention.Key) {
				mentionedBot = true
			}
		}
		if !mentionedBot {
			if isAppMention {
				info.MentionedOtherBot = true
			}
			continue
		}
		info.Mentioned = true
		if mention.Key != nil && strings.TrimSpace(*mention.Key) != "" {
			info.Tokens = append(info.Tokens, strings.TrimSpace(*mention.Key))
		}
		if mention.Name != nil && strings.TrimSpace(*mention.Name) != "" {
			info.Tokens = append(info.Tokens, "@"+strings.TrimSpace(*mention.Name))
		}
	}
	return info
}

func (info *botMentionInfo) addReplacement(mention *larkim.MentionEvent) {
	if info == nil || mention == nil || mention.Key == nil || mention.Name == nil {
		return
	}
	key := normalizeMentionToken(*mention.Key)
	name := strings.TrimSpace(*mention.Name)
	if key == "" || name == "" {
		return
	}
	if info.Replacements == nil {
		info.Replacements = make(map[string]string)
	}
	info.Replacements[key] = "@" + name
}

func normalizeMentionTokens(text string, replacements map[string]string) string {
	if len(replacements) == 0 || strings.TrimSpace(text) == "" {
		return text
	}
	parts := strings.Fields(text)
	for i, part := range parts {
		normalized := normalizeMentionToken(part)
		replacement, ok := replacements[normalized]
		if !ok {
			continue
		}
		suffix := strings.TrimPrefix(part, normalized)
		parts[i] = replacement + suffix
	}
	return strings.Join(parts, " ")
}

// extractText parses a Feishu message content JSON and returns the text.
func extractText(content *string) string {
	if content == nil {
		return ""
	}
	raw := *content
	var m struct {
		Title   string          `json:"title"`
		Text    string          `json:"text"`
		Content [][]postContent `json:"content"`
		UserDSL string          `json:"user_dsl"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	if text := extractCardKitExternalBotDelegationVisibleText(m.UserDSL); text != "" {
		return text
	}
	if text := extractCardKitAnswerText(m.UserDSL); text != "" {
		return text
	}
	if len(m.Content) == 0 {
		return ""
	}
	var lines []string
	if title := strings.TrimSpace(m.Title); title != "" {
		lines = append(lines, title)
	}
	for _, line := range m.Content {
		var b strings.Builder
		for _, item := range line {
			if item.Tag == "a" && strings.TrimSpace(item.Href) == amuxExternalBotDelegationProtocolURL() {
				continue
			}
			if item.Text != "" {
				b.WriteString(item.Text)
			} else if item.Tag == "at" && strings.TrimSpace(item.UserName) != "" {
				b.WriteString("@")
				b.WriteString(strings.TrimSpace(item.UserName))
			}
			if item.Tag == "a" {
				href := strings.TrimSpace(item.Href)
				label := strings.TrimSpace(item.Text)
				if href != "" && href != label {
					if label == "" {
						b.WriteString(href)
					} else {
						b.WriteString(" (" + href + ")")
					}
				}
			}
		}
		if text := strings.TrimSpace(b.String()); text != "" {
			lines = append(lines, text)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

var (
	cardKitAtTagPattern        = regexp.MustCompile(`(?is)<at\b[^>]*>\s*</at>`)
	cardKitMentionKeyPattern   = regexp.MustCompile(`(?i)\bmention_key\s*=\s*(?:"([^"]+)"|'([^']+)'|([^\s>]+))`)
	cardKitMentionIDPattern    = regexp.MustCompile(`(?i)(?:^|\s)id\s*=\s*(?:"([^"]+)"|'([^']+)'|([^\s>]+))`)
	cardKitMarkdownLinkPattern = regexp.MustCompile(`\[((?:\\.|[^]])*)\]\(([^)\s]+)\)`)
)

func extractCardKitAnswerText(userDSL string) string {
	userDSL = strings.TrimSpace(userDSL)
	if userDSL == "" {
		return ""
	}
	var card cardDoc
	if err := json.Unmarshal([]byte(userDSL), &card); err != nil {
		return ""
	}
	text := strings.TrimSpace(findCardElementContent(card.Body.Elements, cardKitReplyElementID))
	// Older cards embedded the product heading in the answer. New cards keep it
	// separate, so an identically worded Agent heading must remain literal.
	if _, hasHeading := findCardElementByID(card.Body.Elements, cardKitAnswerHeadingID); !hasHeading {
		text = strings.TrimSpace(strings.TrimPrefix(text, "**最终结果**"))
	}
	return strings.TrimSpace(cardKitAtTagPattern.ReplaceAllStringFunc(text, func(tag string) string {
		match := cardKitMentionKeyPattern.FindStringSubmatch(tag)
		if len(match) > 1 {
			for _, value := range match[1:] {
				if value = strings.TrimSpace(value); value != "" {
					return value
				}
			}
		}
		return tag
	}))
}

func findCardElementContent(elements []cardElement, elementID string) string {
	for _, element := range elements {
		if element.ElementID == elementID && strings.TrimSpace(element.Content) != "" {
			return element.Content
		}
		if text := findCardElementContent(element.Elements, elementID); text != "" {
			return text
		}
		for _, column := range element.Columns {
			if text := findCardElementContent(column.Elements, elementID); text != "" {
				return text
			}
		}
	}
	return ""
}

type postContent struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Href     string `json:"href"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	Language string `json:"language"`
	ImageKey string `json:"image_key"`
}

// stripMention removes leading @mention tokens from text.
func stripMention(text string) string {
	return stripBotMention(text)
}

func stripBotMention(text string) string {
	return stripMentionTokens(text, nil)
}

func stripMentionTokens(text string, tokens []string) string {
	text = strings.TrimSpace(text)
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" || !strings.HasPrefix(text, token) {
			continue
		}
		rest := strings.TrimPrefix(text, token)
		if rest == "" {
			return ""
		}
		if strings.TrimSpace(rest) != rest {
			return strings.TrimSpace(rest)
		}
		if trimmed := strings.TrimLeft(rest, "，,。.!！?？:：;；"); trimmed != rest {
			return strings.TrimSpace(trimmed)
		}
	}
	parts := strings.Fields(text)
	out := parts[:0]
	removed := false
	for _, part := range parts {
		if !removed && shouldStripMentionToken(part, tokens) {
			removed = true
			continue
		}
		out = append(out, part)
	}
	return strings.Join(out, " ")
}

func shouldStripMentionToken(part string, tokens []string) bool {
	if len(tokens) == 0 {
		return strings.HasPrefix(normalizeMentionToken(part), "@")
	}
	normalizedPart := normalizeMentionToken(part)
	for _, token := range tokens {
		if normalizedPart == normalizeMentionToken(token) {
			return true
		}
	}
	return false
}

func (b *Bridge) configuredBotMentionToken(text string) string {
	if b == nil || b.secrets == nil {
		return ""
	}
	name := strings.TrimSpace(b.secrets.BotName)
	if name == "" {
		return ""
	}
	token := "@" + name
	searchFrom := 0
	for searchFrom < len(text) {
		offset := strings.Index(text[searchFrom:], token)
		if offset < 0 {
			return ""
		}
		start := searchFrom + offset
		end := start + len(token)
		if mentionBoundaryBefore(text[:start]) && mentionBoundaryAfter(text[end:]) {
			return token
		}
		searchFrom = end
	}
	return ""
}

func mentionBoundaryBefore(text string) bool {
	if text == "" {
		return true
	}
	var last rune
	for _, r := range text {
		last = r
	}
	return unicode.IsSpace(last)
}

func mentionBoundaryAfter(text string) bool {
	if text == "" {
		return true
	}
	for _, r := range text {
		return unicode.IsSpace(r) || strings.ContainsRune("，,。.!！?？:：;；", r)
	}
	return true
}

func (b *Bridge) isBotMentionToken(token string) bool {
	token = normalizeMentionToken(token)
	if !strings.HasPrefix(token, "@") {
		return false
	}
	return b.isBotMentionName(strings.TrimPrefix(token, "@"))
}

func normalizeMentionToken(token string) string {
	token = strings.TrimSpace(token)
	return strings.Trim(token, "，,。.!！?？:：;；")
}

func (b *Bridge) isBotMentionName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || b == nil || b.secrets == nil || strings.TrimSpace(b.secrets.BotName) == "" {
		return false
	}
	return strings.EqualFold(name, strings.TrimSpace(b.secrets.BotName))
}

func (b *Bridge) isConfiguredBotMention(mention *larkim.MentionEvent) bool {
	if mention == nil || b == nil || b.secrets == nil {
		return false
	}
	if mentionHasAnyID(mention.Id) {
		return b.mentionIDMatchesBot(mention.Id)
	}
	if mention.Name != nil && b.isBotMentionName(*mention.Name) {
		return true
	}
	if mention.Key != nil && b.isBotMentionToken(*mention.Key) {
		return true
	}
	return false
}

func mentionHasAnyID(id *larkim.UserId) bool {
	if id == nil {
		return false
	}
	for _, got := range []*string{id.OpenId, id.UserId, id.UnionId} {
		if got != nil && strings.TrimSpace(*got) != "" {
			return true
		}
	}
	return false
}

func (b *Bridge) mentionIDMatchesBot(id *larkim.UserId) bool {
	if id == nil || b == nil || b.secrets == nil {
		return false
	}
	want := make(map[string]struct{})
	for _, v := range []string{b.secrets.BotOpenId, b.secrets.AppId} {
		v = strings.TrimSpace(v)
		if v != "" {
			want[v] = struct{}{}
		}
	}
	if len(want) == 0 {
		return false
	}
	for _, got := range []*string{id.OpenId, id.UserId, id.UnionId} {
		if got == nil {
			continue
		}
		if _, ok := want[strings.TrimSpace(*got)]; ok {
			return true
		}
	}
	return false
}

func stringPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func textPreview(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max]
}
