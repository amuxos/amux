package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// streamController owns live attach-stream lifecycle and ownership indexes.
type streamController struct {
	streamMu        sync.Mutex
	streams         map[string]context.CancelFunc
	streamTokens    map[string]uint64
	streamReady     map[string]chan struct{}
	streamSessions  map[string]sessionStreamRegistration
	streamSessionID map[string]string
	streamFreeze    map[string]streamFreezeHandler
	supersededTurns map[string]bool
	nextStreamToken uint64
}

const (
	streamEventQueueSize         = 4096
	streamAttachReadyTimeout     = 2 * time.Second
	turnStatusPulseInterval      = 5 * time.Second
	subtaskCardDiscoveryInterval = 2 * time.Second
	subtaskProgressPollInterval  = 30 * time.Second
)

type feishuStreamVisibilityGate struct {
	mu                 sync.Mutex
	allowedTurnKey     string
	anonymousInputTurn bool
	pendingInputs      int
	suppressedLogKeys  map[string]struct{}
}

func newFeishuStreamVisibilityGate() *feishuStreamVisibilityGate {
	return &feishuStreamVisibilityGate{suppressedLogKeys: make(map[string]struct{})}
}

func (g *feishuStreamVisibilityGate) Observe(action agentui.Action) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch action.Kind {
	case agentui.ActionTurnStarted:
		key := feishuStreamTurnKey(action.Turn)
		if isInputStartedTurnSource(action.Source) {
			if g.pendingInputs > 0 {
				g.pendingInputs--
			}
			g.allowedTurnKey = key
			g.anonymousInputTurn = key == ""
			return
		}
		// The Bridge accepted a real Feishu input, but the runtime Attach echo
		// can still be absent on restored/provider-backed sessions. Bind that
		// explicit authorization to the next observed turn instead of treating
		// the provider's turn_started frame as startup noise.
		if g.pendingInputs > 0 {
			g.pendingInputs--
			g.allowedTurnKey = key
			g.anonymousInputTurn = key == ""
			return
		}
		if key == "" || key != g.allowedTurnKey {
			g.allowedTurnKey = ""
			g.anonymousInputTurn = false
		}
	case agentui.ActionTurnFinished:
		key := feishuStreamTurnKey(action.Turn)
		if key == "" || key == g.allowedTurnKey {
			g.allowedTurnKey = ""
			g.anonymousInputTurn = false
		}
	}
}

func (g *feishuStreamVisibilityGate) Allow(action agentui.Action) bool {
	if !feishuStreamActionRequiresInputTurn(action.Kind) {
		return true
	}
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := feishuStreamTurnKey(action.Turn)
	if key == "" {
		return g.anonymousInputTurn
	}
	return key == g.allowedTurnKey
}

func (g *feishuStreamVisibilityGate) MarkSuppressed(action agentui.Action) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := string(action.Kind) + ":" + feishuStreamTurnKey(action.Turn)
	if key == string(action.Kind)+":" {
		key += "anonymous"
	}
	if _, ok := g.suppressedLogKeys[key]; ok {
		return false
	}
	g.suppressedLogKeys[key] = struct{}{}
	return true
}

func (g *feishuStreamVisibilityGate) authorizeInput() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.pendingInputs++
	g.mu.Unlock()
}

func (g *feishuStreamVisibilityGate) authorizeTurn(turn agentui.TurnEvent) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.allowedTurnKey = feishuStreamTurnKey(turn)
	g.anonymousInputTurn = g.allowedTurnKey == ""
	g.mu.Unlock()
}

func feishuStreamTurnKey(turn agentui.TurnEvent) string {
	if turn.ID != "" {
		return "id:" + turn.ID
	}
	if turn.Seq != 0 {
		return fmt.Sprintf("seq:%d", turn.Seq)
	}
	return ""
}

func isInputStartedTurnSource(source agentui.Source) bool {
	switch source {
	case agentui.SourceFeishu, agentui.SourceCLI, agentui.SourceDesktop:
		return true
	default:
		return false
	}
}

func feishuStreamActionRequiresInputTurn(kind agentui.ActionKind) bool {
	switch kind {
	case agentui.ActionTurnStarted,
		agentui.ActionReplyAppend,
		agentui.ActionReplyReplace,
		agentui.ActionReplyReconcileFinalMessage,
		agentui.ActionReplyMessageBoundary,
		agentui.ActionReplySegment,
		agentui.ActionActivityChanged,
		agentui.ActionPermissionRequest,
		agentui.ActionError,
		agentui.ActionTurnFinished:
		return true
	default:
		return false
	}
}

// feishuStreamActionCanAuthorizeFromActive reports whether a provider action is
// sufficient evidence that the Bridge-authorized Submit has started producing
// its turn. A terminal frame alone is not sufficient: a newly attached stream
// can replay a stale turn_finished while the fresh Submit is still in flight.
// Treating that replay as the new turn would consume a pending Bot-to-Bot
// handoff before the real answer arrives.
func feishuStreamActionCanAuthorizeFromActive(kind agentui.ActionKind) bool {
	switch kind {
	case agentui.ActionTurnStarted, agentui.ActionTurnFinished:
		return false
	default:
		return feishuStreamActionRequiresInputTurn(kind)
	}
}

// canAuthorizeProviderTurnFromActive keeps the generic provider-start fallback
// for ordinary Feishu submits, but not while a Bot-to-Bot callback is pending.
// Fresh Attach streams can replay a provider-owned turn_started before the real
// Feishu input is observed; consuming the pending callback on that replay would
// drop the first Bot-to-Bot response. A real Feishu turn_started is authorized
// by its source, and providers that omit it are authorized by their first
// non-terminal output instead.
func (b *Bridge) canAuthorizeProviderTurnFromActive(rootMsgID string) bool {
	if b == nil || b.db == nil {
		return true
	}
	pending, err := b.db.PendingBotHandoff(b.currentBridgeId(), rootMsgID)
	if err != nil {
		log.Warn().Err(err).Str("root_msg_id", rootMsgID).
			Msg("bridge: check pending bot handoff before provider turn authorization")
		return true
	}
	return pending == nil
}

func (b *Bridge) startThreadStream(chat *LarkChat, openID string) {
	if chat == nil {
		return
	}
	b.startStream(chat.RootMsgID, chat.TargetID, chat.SessionName, chat.AgentProfile, chat.AgentType, chat.EffectiveModel, openID)
}

// ensureThreadStream guarantees an output stream is attached for a bound thread
// before its next input is submitted. It is the lazy counterpart to
// OnSessionAdded's automatic reattach: when a hub/runtime restart happens while
// the thread is idle past autoReattachRecentBindingWindow, the automatic
// reattach is intentionally skipped, so the user's next message must restore the
// stream here — otherwise the agent runs with no bridge consumer forwarding its
// output back to Feishu. It is a no-op when a stream is already running so an
// in-flight turn is never torn down and re-attached mid-output.
func (b *Bridge) ensureThreadStream(chat *LarkChat) {
	if b == nil || chat == nil {
		return
	}
	rootMsgID := strings.TrimSpace(chat.RootMsgID)
	if rootMsgID == "" {
		return
	}
	b.startThreadStream(chat, chat.OpenId)
}

// waitForStreamReady gives a newly launched Attach stream a bounded window to
// install its runtime subscription before Submit broadcasts the input event.
// Older runtimes do not send the initial handshake, so timeout remains a
// compatibility fallback instead of failing the user's input.
func (c *streamController) waitForStreamReady(ctx context.Context, rootMsgID string) {
	if c == nil || strings.TrimSpace(rootMsgID) == "" {
		return
	}
	c.streamMu.Lock()
	ready := c.streamReady[rootMsgID]
	c.streamMu.Unlock()
	if ready == nil {
		return
	}
	timer := time.NewTimer(streamAttachReadyTimeout)
	defer timer.Stop()
	select {
	case <-ready:
		return
	case <-ctx.Done():
		return
	case <-timer.C:
		log.Warn().Str("root_msg_id", rootMsgID).
			Msg("bridge: timed out waiting for Attach stream readiness; submitting with compatibility fallback")
	}
}

func singletonStreamKey(bridgeID string) string {
	return "singleton:" + bridgeID
}

func (b *Bridge) startSingletonStream(bs *BotSession, openID string) {
	if !botSessionReady(bs) {
		return
	}
	b.startStream(singletonStreamKey(bs.BridgeId), bs.TargetID, bs.SessionName, bs.AgentProfile, bs.AgentType, bs.EffectiveModel, openID)
}

// reattachSingletonStreamIfBoundLocked re-establishes the persistent singleton
// bot-session attach stream when the added session matches the bound target.
// Returns true if a reattach was attempted. Callers must have already
// determined interaction mode == singleton and must hold the singleton route
// lock; this helper only checks whether the bot-session record actually points
// at (targetID, sessionName).
//
// Unlike thread-mode reattach, this does NOT gate on recent activity: singleton
// output routes to bs.LastReply* (never to a dormant thread), so reattaching a
// long-idle singleton binding cannot wake up an old thread. Skipping the reattach
// would silently drop provider output that a resumed session produces before the
// next user message arrives.
func (b *Bridge) reattachSingletonStreamIfBoundLocked(targetID, sessionName string) bool {
	if b == nil || b.db == nil {
		return false
	}
	bs, err := b.db.GetBotSession(b.currentBridgeId())
	if err != nil || !botSessionReady(bs) {
		return false
	}
	if strings.TrimSpace(bs.TargetID) != strings.TrimSpace(targetID) ||
		strings.TrimSpace(bs.SessionName) != strings.TrimSpace(sessionName) {
		return false
	}
	openID := strings.TrimSpace(bs.LastReplyOpenID)
	log.Debug().Str("session", sessionName).Str("target_id", targetID).
		Str("bridge_id", bs.BridgeId).Bool("has_last_reply_open_id", openID != "").
		Msg("bridge: ensuring singleton bot-session stream")
	b.startSingletonStream(bs, openID)
	// Identity metadata can change the static menu even when the live Attach
	// is reused; reconcile it without discarding that stream's discovery.
	b.scheduleNativeSlashCommandSync()
	return true
}

// startStream atomically reuses a live attachment to the same session launch,
// or launches one that forwards agent output to rootMsgID. All startup paths
// share this check so a delayed session-added callback cannot split a turn's
// waiting/activity card from its result by replacing the reply sender.
func (b *Bridge) startStream(rootMsgID, targetID, sessionName, agentProfile, agentType, effectiveModel, openID string) {
	bridgeID := b.currentBridgeId()
	streamChat := &LarkChat{
		BridgeId:       bridgeID,
		RootMsgID:      rootMsgID,
		OpenId:         openID,
		TargetID:       targetID,
		SessionName:    sessionName,
		AgentProfile:   agentProfile,
		AgentType:      agentType,
		EffectiveModel: effectiveModel,
		Status:         ChatStatusActive,
	}
	if b.db != nil {
		if chat, err := b.db.FindByRootMsg(bridgeID, rootMsgID); err == nil && chat != nil {
			streamChat = chat
		}
	}
	groupWatchStream := false
	if b.db != nil {
		entrySource, err := b.db.FindChatEntrySource(bridgeID, rootMsgID)
		if err != nil {
			log.Warn().Err(err).Str("root_msg_id", rootMsgID).
				Msg("bridge: resolve Feishu thinking process presentation")
		} else if entrySource == ChatEntrySourceGroupWatch {
			groupWatchStream = true
		}
	}
	thinkingProcess := func() string {
		return b.streamThinkingProcess(groupWatchStream)
	}
	b.streamMu.Lock()
	if b.streamSessions == nil {
		b.streamSessions = make(map[string]sessionStreamRegistration)
	}
	if b.streamSessionID == nil {
		b.streamSessionID = make(map[string]string)
	}
	sessionKey := targetID + "\x00" + sessionName
	// Read the cached Hub snapshot while serializing registration. The Hub
	// updates it before dispatching session-added callbacks; using the current
	// snapshot also prevents a delayed callback from restoring an older launch.
	session := b.boundSession(targetID, sessionName)
	if previous, ok := b.streamSessions[sessionKey]; ok {
		if previous.rootMsgID == rootMsgID && previous.ctx.Err() == nil &&
			(previous.sessionID == "" || session.GetId() == "" || previous.sessionID == session.GetId()) &&
			(previous.launchID == "" || session.GetLaunchId() == "" || previous.launchID == session.GetLaunchId()) {
			// Run can return before the first session snapshot arrives. Fill in
			// its identity without discarding the card already opened by input.
			if previous.sessionID == "" {
				previous.sessionID = session.GetId()
			}
			if previous.launchID == "" {
				previous.launchID = session.GetLaunchId()
			}
			b.streamSessions[sessionKey] = previous
			b.streamMu.Unlock()
			return
		}
		if cancel := b.removeStreamLocked(previous.rootMsgID); cancel != nil {
			cancel()
		}
	}
	if cancel := b.removeStreamLocked(rootMsgID); cancel != nil {
		cancel()
	}
	if b.streamTokens == nil {
		b.streamTokens = make(map[string]uint64)
	}
	if b.streamReady == nil {
		b.streamReady = make(map[string]chan struct{})
	}
	if b.streams == nil {
		b.streams = make(map[string]context.CancelFunc)
	}
	// Only a new attachment invalidates advertised commands. Reusing a live
	// stream must retain its discovery state as well as its presentation state.
	b.clearAdvertisedAgentCommands(targetID, sessionName)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	b.nextStreamToken++
	token := b.nextStreamToken
	b.streams[rootMsgID] = cancel
	b.streamTokens[rootMsgID] = token
	b.streamReady[rootMsgID] = ready
	b.streamSessions[sessionKey] = sessionStreamRegistration{
		rootMsgID: rootMsgID, token: token, ctx: ctx,
		sessionID: session.GetId(), launchID: session.GetLaunchId(),
	}
	b.streamSessionID[rootMsgID] = sessionKey
	b.streamMu.Unlock()
	log.Info().Str("session", sessionName).Str("target_id", targetID).
		Str("root_msg_id", rootMsgID).Str("session_id", session.GetId()).
		Str("launch_id", session.GetLaunchId()).Uint64("stream_token", token).
		Msg("bridge: attaching session output stream")

	go func() {
		defer cancel()
		var readyOnce sync.Once
		markReady := func() { readyOnce.Do(func() { close(ready) }) }
		defer markReady()
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).
					Str("session", sessionName).
					Msg("bridge: startStream goroutine panicked")
			}
			b.streamMu.Lock()
			ownsPresentation := false
			if b.streamTokens[rootMsgID] == token {
				ownsPresentation = true
				delete(b.streams, rootMsgID)
				delete(b.streamTokens, rootMsgID)
				delete(b.streamReady, rootMsgID)
				delete(b.streamSessionID, rootMsgID)
			}
			if current, ok := b.streamSessions[sessionKey]; ok && current.token == token {
				delete(b.streamSessions, sessionKey)
			}
			b.streamMu.Unlock()
			// A replaced stream exits after the replacement is registered. It
			// must not clear the topic-level authorization/turn state owned by
			// that newer stream.
			if ownsPresentation {
				b.setTurnActive(rootMsgID, false)
			}
		}()

		turn := &toolActivityTurn{}
		presenter := agentui.NewPresenter()
		visibilityGate := newFeishuStreamVisibilityGate()
		logSuppressedVisibleAction := func(action agentui.Action) {
			if !visibilityGate.MarkSuppressed(action) {
				return
			}
			log.Info().
				Str("session", sessionName).
				Str("target_id", targetID).
				Str("root_msg_id", rootMsgID).
				Str("action", string(action.Kind)).
				Str("source", string(action.Source)).
				Str("turn_id", action.Turn.ID).
				Int64("turn_seq", action.Turn.Seq).
				Str("text_preview", truncateCardText(action.Text, 160)).
				Msg("bridge: suppressed Feishu stream output without input-started turn")
		}

		agg := NewAggregator(30*time.Second, 50000, func(text string) error {
			_ = b.db.SetLatestAgentOutput(bridgeID, rootMsgID, text)
			sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			msgID, _, err := b.sendAgentReplyToBinding(sendCtx, streamChat, text)
			if err != nil {
				log.Warn().Err(err).Str("session", sessionName).Msg("bridge: sendAgentReplyToBinding error")
			}
			if err == nil {
				b.recordAgentThreadContext(sendCtx, streamChat, msgID, text)
			}
			if err == nil && isFollowupPrompt(text) {
				turn.MarkWaiting()
			}
			return err
		})
		defer agg.Close()

		activity := &toolActivityState{}
		subtaskChat := &LarkChat{
			BridgeId:    streamChat.BridgeId,
			RootMsgID:   streamChat.RootMsgID,
			TargetID:    streamChat.TargetID,
			SessionName: streamChat.SessionName,
		}
		presentationGate := newTurnActivityPresentationGate(distributionCoordinationProfile(agentProfile))
		assistantUpdates := &assistantProgress{}
		hasAuthoritativeResult := false
		var activityViewMu sync.RWMutex
		var currentActivity *agentui.ActivityView
		var statusRefresh turnStatusRefreshPolicy
		setActivityView := func(view *agentui.ActivityView) {
			activityViewMu.Lock()
			currentActivity = view
			activityViewMu.Unlock()
		}
		getActivityView := func() *agentui.ActivityView {
			activityViewMu.RLock()
			defer activityViewMu.RUnlock()
			return currentActivity
		}
		activityMeta := func() toolActivityCardMeta {
			meta := turn.Meta()
			meta.AgentProfile = streamChat.AgentProfile
			meta.AgentType = streamChat.AgentType
			meta.EffectiveModel = streamChat.EffectiveModel
			meta.Activity = getActivityView()
			meta.Subtasks = activity.Subtasks()
			meta.ThinkingProcess = thinkingProcess()
			if meta.Activity != nil {
				if meta.Activity.TurnSeq > 0 {
					meta.TurnSeq = int(meta.Activity.TurnSeq)
				}
				if meta.Status != "waiting" && meta.Activity.Status != "" {
					meta.Status = meta.Activity.Status
				}
			}
			meta.ViewURL = b.sessionViewerURL(targetID, sessionName)
			meta.Now = time.Now()
			return meta
		}
		legacyActivitySend := func(text string) (string, error) {
			sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			msgID, _, err := b.sendToolActivityToBinding(sendCtx, streamChat, activityMeta(), text)
			if err != nil {
				log.Warn().Err(err).Str("session", sessionName).Msg("bridge: sendToolActivityToBinding error")
			}
			return msgID, err
		}
		legacyActivityPatch := func(msgID, text string) error {
			sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err := b.patchToolActivityBinding(sendCtx, streamChat, msgID, activityMeta(), text)
			if err != nil {
				log.Warn().Err(err).Str("session", sessionName).Str("msg_id", msgID).Msg("bridge: patch tool activity error")
			}
			return err
		}

		var finalAnswerMu sync.Mutex
		finalAnswer := ""
		setFinalAnswer := func(text string) {
			finalAnswerMu.Lock()
			finalAnswer = text
			finalAnswerMu.Unlock()
		}
		getFinalAnswer := func() string {
			finalAnswerMu.Lock()
			defer finalAnswerMu.Unlock()
			return finalAnswer
		}
		replyStream := newStreamingReplySender(100*time.Millisecond,
			newBindingReplyCardBackend(b, streamChat),
			func(text string) {
				_ = b.db.SetLatestAgentOutput(bridgeID, rootMsgID, text)
				b.recordAgentThreadContext(context.Background(), streamChat, "", text)
				setFinalAnswer(text)
				if isFollowupPrompt(text) {
					turn.MarkWaiting()
				}
			},
		)
		activitySender := newMainTurnActivitySender(
			2*time.Second,
			replyStream,
			func() turnCardActivity { return renderTurnActivity(activityMeta()) },
			legacyActivitySend,
			legacyActivityPatch,
			func(err error) {
				log.Warn().Err(err).Str("session", sessionName).
					Msg("bridge: CardKit activity unavailable before send; using legacy activity card")
			},
		)
		publishActivity := func(view *agentui.ActivityView, force bool) {
			if view == nil || !presentationGate.ShouldPublishActivity(view, len(activity.Subtasks()) > 0) {
				return
			}
			fingerprint := activityViewFingerprint(view)
			if force {
				activitySender.ForceSend(fingerprint)
			} else {
				activitySender.MaybeSend(fingerprint)
			}
		}
		b.registerStreamFreeze(rootMsgID, token, replyStream.Supersede)
		defer b.unregisterStreamFreeze(rootMsgID, token)
		// Defers are LIFO: Flush publishes the final text before Close performs
		// its idempotent lifecycle cleanup.
		defer replyStream.Close()
		defer replyStream.Flush()
		defer activitySender.Close()
		var (
			streamsReplyCardsOnce        sync.Once
			streamsReplyCards            bool
			providerFailureNoticePending bool
			failureNoticeDelivered       bool
		)
		getStreamsReplyCards := func() bool {
			streamsReplyCardsOnce.Do(func() {
				streamsReplyCards = streamOutputAsReplyCardForWorkspace(agentType, b.workspaceKindForChat(streamChat.ChatID))
			})
			return streamsReplyCards
		}

		handleAttachEvent := func(ev *pb.AttachEvent) {
			if ev == nil {
				return
			}
			if model := strings.TrimSpace(ev.EffectiveModel); model != "" && strings.TrimSpace(streamChat.EffectiveModel) == "" {
				streamChat.EffectiveModel = model
				if err := b.db.RefreshSessionIdentity(bridgeID, targetID, sessionName, "", "", model); err != nil {
					log.Warn().Err(err).Str("session", sessionName).Str("effective_model", model).
						Msg("bridge: persist provider-confirmed effective model")
				}
				if view := getActivityView(); view != nil {
					publishActivity(view, true)
				}
			}
			if ev.EventType == "available_commands" {
				b.observeAvailableCommands(targetID, sessionName, ev.StructuredJson)
				return
			}
			for _, uiEvent := range agentui.FromAttachEvent(ev) {
				for _, action := range presenter.Consume(uiEvent) {
					// Some providers omit an explicit turn_started frame and
					// begin with assistant/result/activity output carrying the
					// turn identity. A successful Feishu Submit already marked
					// the topic active, so bind that first visible action to the
					// authenticated turn instead of suppressing the whole answer.
					if feishuStreamActionCanAuthorizeFromActive(action.Kind) &&
						!visibilityGate.Allow(action) && b.isTurnActive(rootMsgID) {
						visibilityGate.authorizeTurn(action.Turn)
						log.Debug().Str("session", sessionName).Str("root_msg_id", rootMsgID).
							Str("action", string(action.Kind)).Str("turn_id", action.Turn.ID).
							Int64("turn_seq", action.Turn.Seq).
							Msg("bridge: authorized first provider action from active Feishu submit")
					}
					switch action.Kind {
					case agentui.ActionTurnStarted:
						// submitToSession marks the topic turn active before the
						// runtime RPC. This topic-scoped authorization survives an
						// OnSessionAdded stream replacement. The normal lark input
						// echo remains the preferred turn-start signal when present.
						if !isInputStartedTurnSource(action.Source) && b.isTurnActive(rootMsgID) &&
							b.canAuthorizeProviderTurnFromActive(rootMsgID) {
							visibilityGate.authorizeInput()
							log.Debug().Str("session", sessionName).Str("root_msg_id", rootMsgID).
								Str("turn_id", action.Turn.ID).Int64("turn_seq", action.Turn.Seq).
								Msg("bridge: authorized provider turn from active Feishu submit")
						}
						visibilityGate.Observe(action)
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						oldSeq := turn.Meta().TurnSeq
						question := ""
						if action.Source == agentui.SourceFeishu {
							question = toolActivityQuestionTitle(action.Text)
						}
						meta := turn.Start(int(action.Turn.Seq), question)
						providerFailureNoticePending = false
						failureNoticeDelivered = false
						b.setTurnActive(rootMsgID, true)
						if meta.TurnSeq != oldSeq {
							agg.Flush()
							replyStream.Flush()
							replyStream.Reset()
							setFinalAnswer("")
							activity.Reset()
							assistantUpdates.Reset()
							hasAuthoritativeResult = false
							setActivityView(nil)
							activitySender.Reset()
							presentationGate.Reset()
							statusRefresh.Reset()
							activity.StartTurn(activity.now())
							// Some providers do not expose a native queue event. Reply as soon as
							// the accepted turn starts so model-side queueing is visible
							// even for coordination turns whose later activity stays behind the
							// external-coordination presentation gate.
							view := presenter.EnsureActivity()
							if distributionActivityPossiblyQueued(streamChat.AgentType, view) {
								setActivityView(view)
								activitySender.ForceSend(activityViewFingerprint(view))
								presentationGate.ObserveForcedActivity()
								statusRefresh.MarkRefreshed(time.Now(), view)
							}
						}
						b.clearSupersededTurn(rootMsgID)
					case agentui.ActionInputEcho:
						sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
						_, _, err := b.sendTextToBinding(sendCtx, streamChat, action.Text)
						cancel()
						if err != nil {
							log.Warn().Err(err).Str("session", sessionName).Msg("bridge: sendTextToBinding (input) error")
						}
					case agentui.ActionReplyAppend:
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						if b.isTurnSuperseded(rootMsgID) {
							continue
						}
						if action.Source == agentui.SourceSystem {
							if text, ok := bridgeProviderFailureNotice(
								sessionName,
								strings.TrimSpace(streamChat.AgentProfile) != "",
								action.Text,
							); ok {
								if providerFailureNoticePending {
									continue
								}
								agg.Flush()
								replyStream.Flush()
								sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
								_, _, err := b.sendTextToBinding(sendCtx, streamChat, text)
								cancel()
								if err != nil {
									log.Warn().Err(err).Str("session", sessionName).
										Msg("bridge: sendTextToBinding (provider failure) error")
								} else {
									failureNoticeDelivered = true
								}
								providerFailureNoticePending = true
								continue
							}
						}
						if summary := assistantUpdates.Append(action.Text); summary != "" {
							view := getActivityView()
							if view == nil && !assistantUpdates.HasCompletedMessage() {
								continue
							}
							if view == nil {
								view = presenter.EnsureActivity()
							}
							view = activityViewWithCurrentStep(view, summary)
							setActivityView(view)
							publishActivity(view, false)
						}
					case agentui.ActionReplyMessageBoundary:
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						if b.isTurnSuperseded(rootMsgID) {
							continue
						}
						if assistantUpdates.Boundary() {
							view := getActivityView()
							if view == nil {
								view = presenter.EnsureActivity()
							}
							if view != nil {
								view = activityViewWithCurrentStep(view, assistantUpdates.Summary())
								setActivityView(view)
								presentationGate.ObserveCompletedAssistantMessage()
								publishActivity(view, false)
							}
						}
					case agentui.ActionReplyReplace:
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						if b.isTurnSuperseded(rootMsgID) {
							continue
						}
						if summary := assistantUpdates.Replace(action.Text); summary != "" {
							view := getActivityView()
							if view == nil && !assistantUpdates.HasCompletedMessage() {
								continue
							}
							if view == nil {
								view = presenter.EnsureActivity()
							}
							view = activityViewWithCurrentStep(view, summary)
							setActivityView(view)
							publishActivity(view, false)
						}
					case agentui.ActionReplyReconcileFinalMessage:
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						if b.isTurnSuperseded(rootMsgID) {
							continue
						}
						if isFollowupPrompt(action.Text) {
							turn.MarkWaiting()
						}
						hasAuthoritativeResult = true
						before := assistantUpdates.Summary()
						after := assistantUpdates.ReconcileResult(action.Text)
						if before != after {
							if view := getActivityView(); view != nil {
								view = activityViewWithCurrentStep(view, after)
								setActivityView(view)
								publishActivity(view, true)
							}
						}
						if getStreamsReplyCards() {
							replyStream.ReplaceCurrent(action.Text)
						} else {
							agg.Add([]byte(action.Text))
						}
					case agentui.ActionReplySegment:
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						replyStream.Segment()
					case agentui.ActionActivityChanged:
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						if b.isTurnSuperseded(rootMsgID) && action.Activity != nil && action.Activity.Status == "running" {
							continue
						}
						view := action.Activity
						if progress := assistantUpdates.Summary(); view != nil && progress != "" {
							// Assistant narration is the chronological Feishu-facing
							// progress history. Structured thinking remains the current
							// step only until narration is available.
							view = activityViewWithCurrentStep(view, progress)
						}
						setActivityView(view)
						publishActivity(view, action.Immediate)
					case agentui.ActionPermissionRequest:
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						if action.Permission == nil {
							continue
						}
						sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
						err := b.handlePermissionEvent(sendCtx, rootMsgID, targetID, sessionName, openID, action.Permission)
						cancel()
						if err != nil {
							log.Warn().Err(err).Str("session", sessionName).Str("permission_id", action.Permission.RequestID).Msg("bridge: send permission request card error")
						}
					case agentui.ActionError:
						if !visibilityGate.Allow(action) {
							logSuppressedVisibleAction(action)
							continue
						}
						turn.Fail()
						replyStream.Flush()
						if providerFailureNoticePending {
							providerFailureNoticePending = false
							continue
						}
						if text := bridgeErrorNotice(
							sessionName,
							action.Text,
							strings.TrimSpace(streamChat.AgentProfile) != "",
						); text != "" {
							sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
							_, _, sendErr := b.sendTextToBinding(sendCtx, streamChat, text)
							cancel()
							failureNoticeDelivered = sendErr == nil
						}
					case agentui.ActionTurnFinished:
						allowed := visibilityGate.Allow(action)
						visibilityGate.Observe(action)
						providerFailureNoticePending = false
						if !allowed {
							logSuppressedVisibleAction(action)
							b.setTurnActive(rootMsgID, false)
							continue
						}
						b.setTurnActive(rootMsgID, false)
						b.refreshSubtaskProgress(activity, targetID, sessionName)
						b.startSubtaskCards(subtaskChat, activity.Subtasks())
						completionMeta := turn.Complete(action.Turn.State == "failed")
						turnElapsed := time.Duration(0)
						if startedAt := activity.TurnStartedAt(); !startedAt.IsZero() {
							turnElapsed = activity.now().Sub(startedAt)
						}
						if !hasAuthoritativeResult && shouldUseAssistantResultFallback(action.Turn) {
							if fallback := assistantUpdates.LastMessage(); fallback != "" {
								if getStreamsReplyCards() {
									replyStream.ReplaceCurrent(fallback)
								} else {
									agg.Add([]byte(fallback))
								}
							}
						}
						view := getActivityView()
						if view != nil && presentationGate.ShouldPublishActivity(view, len(activity.Subtasks()) > 0) {
							activitySender.Flush(activityViewFingerprint(view))
						}
						agg.Flush()
						superseded := b.isTurnSuperseded(rootMsgID)
						handoffEligible := !superseded && shouldUseAssistantResultFallback(action.Turn)
						var pendingCardHandoff *PendingBotHandoff
						cardHandoffDelivered := false
						if handoffEligible && getStreamsReplyCards() {
							pending, err := b.db.PendingBotHandoff(bridgeID, rootMsgID)
							if err != nil {
								log.Warn().Err(err).Str("root_msg_id", rootMsgID).
									Msg("bridge: load pending bot handoff for reply card")
							} else if pending != nil {
								pendingCardHandoff = pending
								cardHandoffDelivered = replyStream.FinalizeForBot(pending.BotOpenID)
							}
						}
						if pendingCardHandoff == nil {
							replyStream.Flush()
						}
						if handoffEligible {
							if text := strings.TrimSpace(getFinalAnswer()); text != "" {
								if cardHandoffDelivered {
									if err := b.completePendingBotHandoff(rootMsgID, pendingCardHandoff.MessageID); err != nil {
										log.Warn().Err(err).Str("root_msg_id", rootMsgID).
											Msg("bridge: clear same-card bot-to-bot handoff")
									}
								} else {
									handoffCtx, handoffCancel := context.WithTimeout(context.Background(), 15*time.Second)
									if err := b.deliverPendingBotHandoff(handoffCtx, rootMsgID, text); err != nil {
										log.Warn().Err(err).Str("root_msg_id", rootMsgID).Msg("bridge: deliver bot-to-bot handoff")
									}
									handoffCancel()
								}
							} else {
								b.discardPendingBotHandoff(rootMsgID)
							}
						} else {
							b.discardPendingBotHandoff(rootMsgID)
						}
						if !superseded {
							replyStream.Reset()
						}
						if handoffEligible {
							b.dispatchArtifactDelivery(streamChat, action.Turn.ID, getFinalAnswer(), ev.CanonicalJson)
						}
						setFinalAnswer("")
						if !superseded && (completionMeta.Status != "failed" || !failureNoticeDelivered) {
							b.dispatchTurnCompletionNotification(streamChat, action.Turn.ID, completionMeta.Status, action.Turn.StopReason, turnElapsed)
						}
						if text := turnDoneNotice(action.Turn.StopReason, view != nil); text != "" {
							sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
							_, _, err := b.sendTextToBinding(sendCtx, streamChat, text)
							cancel()
							if err != nil {
								log.Warn().Err(err).Str("session", sessionName).Msg("bridge: sendTextToBinding (turn done) error")
							}
						}
						activity.Reset()
						assistantUpdates.Reset()
						hasAuthoritativeResult = false
						setActivityView(nil)
						statusRefresh.Reset()
					}
				}
			}
		}

		eventCh := make(chan *pb.AttachEvent, streamEventQueueSize)
		processorDone := make(chan struct{})
		subtaskDiscoveryDone := make(chan struct{})
		subtaskDiscoveryCtx, stopSubtaskDiscovery := context.WithCancel(ctx)
		go func() {
			defer close(subtaskDiscoveryDone)
			ticker := time.NewTicker(subtaskCardDiscoveryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-subtaskDiscoveryCtx.Done():
					return
				case <-ticker.C:
					if !turn.Running() {
						continue
					}
					subtasks := b.liveSubtaskProgressForTurn(
						subtaskDiscoveryCtx,
						targetID,
						sessionName,
						activity.TurnStartedAt(),
						time.Now(),
					)
					b.startSubtaskCards(subtaskChat, subtasks)
				}
			}
		}()
		go func() {
			defer close(processorDone)
			statusTicker := time.NewTicker(turnStatusPulseInterval)
			subtaskTicker := time.NewTicker(subtaskProgressPollInterval)
			defer statusTicker.Stop()
			defer subtaskTicker.Stop()
			for {
				select {
				case ev, ok := <-eventCh:
					if !ok {
						return
					}
					handleAttachEvent(ev)
				case now := <-statusTicker.C:
					before := getActivityView()
					handleAttachEvent(&pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT})
					view := getActivityView()
					if before == nil && view != nil {
						// The heartbeat action already created the status surface.
						statusRefresh.MarkRefreshed(now, view)
						continue
					}
					if statusRefresh.Due(now, view) {
						publishActivity(view, true)
					}
				case now := <-subtaskTicker.C:
					if !turn.Running() {
						continue
					}
					b.refreshSubtaskProgress(activity, targetID, sessionName)
					b.startSubtaskCards(subtaskChat, activity.Subtasks())
					view := getActivityView()
					if len(activity.Subtasks()) > 0 && view == nil {
						view = presenter.EnsureActivity()
						setActivityView(view)
					}
					if view != nil {
						publishActivity(view, true)
						statusRefresh.MarkRefreshed(now, view)
					}
				}
			}
		}()

		err := b.hub.RouteStream(ctx, targetID, "Attach",
			&pb.AttachRequest{Name: sessionName, TimelineMode: pb.AttachRequest_TAIL},
			func(msg *pb.TunnelMsg) error {
				var ev pb.AttachEvent
				if err := json.Unmarshal(msg.Payload, &ev); err != nil {
					return nil
				}
				markReady()
				select {
				case eventCh <- &ev:
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			},
		)
		close(eventCh)
		<-processorDone
		stopSubtaskDiscovery()
		<-subtaskDiscoveryDone
		if err != nil && ctx.Err() == nil {
			log.Warn().Err(err).Str("session", sessionName).Msg("bridge: stream ended")
			if text, ok := bridgeStreamFailureNotice(sessionName, err); ok {
				sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_, _, sendErr := b.sendTextToBinding(sendCtx, streamChat, text)
				cancel()
				if sendErr != nil {
					log.Warn().Err(sendErr).Str("session", sessionName).
						Msg("bridge: sendTextToBinding (missing session) error")
				}
			}
		}
	}()
}

func (b *Bridge) streamThinkingProcess(groupWatch bool) string {
	if groupWatch {
		return config.GetBridgeFeishuGroupWatchForProfile(b.ProfileName()).ThinkingProcess
	}
	return config.GetBridgeFeishuPresentationForProfile(b.ProfileName()).ThinkingProcess
}

func turnDoneNotice(stopReason string, hasActivityCard bool) string {
	switch stopReason {
	case "cancelled", "canceled":
		if !hasActivityCard {
			return "已停止本次回答"
		}
		return ""
	default:
		return ""
	}
}

func shouldUseAssistantResultFallback(turn agentui.TurnEvent) bool {
	state := strings.ToLower(strings.TrimSpace(turn.State))
	if state == "failed" || state == "stopped" || state == "interrupted" || state == "cancelled" || state == "canceled" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(turn.StopReason)) {
	case "cancelled", "canceled", "interrupted", "killed", "stopped":
		return false
	default:
		return true
	}
}

// bridgeErrorNotice is the sanitized Feishu prompt for a top-level provider
// error event. It uses providerError only for bounded classification and never
// forwards it verbatim because it may contain local paths, secrets, or internal
// refs.
func bridgeErrorNotice(sessionName, providerError string, profileBacked bool) string {
	if notice := distributionProviderErrorNotice(providerError); notice != "" {
		return notice
	}
	if providerEmptyResponse(providerError) {
		return "模型本轮未返回内容，请重新发送上一条消息；若连续出现，请切换模型后再试。"
	}
	if providerRepeatedToolLoop(providerError) {
		return "⚠️ 模型反复调用同一工具未正常结束，本轮已自动终止。实际工作可能已完成，请检查结果后直接继续对话；若连续出现请切换模型。"
	}
	if providerModelUnavailable(providerError) {
		if sessionName == "" || !profileBacked {
			return "⚠️ 当前模型服务不可用，可能是模型已下线或服务暂时异常。请打开 `/amux` →「助手设置」，切换其他模型后重新发送本条消息。"
		}
		return fmt.Sprintf(
			"⚠️ 当前模型服务不可用，可能是模型已下线或服务暂时异常。请打开 `/amux` →「助手设置」，切换其他模型后重新发送本条消息。若切换后当前长期会话仍使用旧配置，请在运行 amux 的机器上执行 `amux session restart %s --refresh-profile`。",
			sessionName,
		)
	}
	if providerConversationMissing(providerError) {
		if sessionName == "" || !profileBacked {
			return "⚠️ Agent 的历史对话上下文已失效，无法继续当前会话。请新建话题。"
		}
		return fmt.Sprintf(
			"⚠️ Agent 的历史对话上下文已失效，无法继续当前会话。请新建话题；若需恢复当前由专家创建的长期会话，请在运行 amux 的机器上执行 `amux session restart %s --refresh-profile`。",
			sessionName,
		)
	}
	if sessionName == "" {
		return "⚠️ Agent 执行出错，请稍后重试或新建话题。"
	}
	return fmt.Sprintf("⚠️ Agent 执行出错，请稍后重试或 `amux session restart %s`。", sessionName)
}

func providerEmptyResponse(text string) bool {
	return strings.Contains(strings.ToLower(text), "(code=empty_response)")
}

func providerRepeatedToolLoop(text string) bool {
	return strings.Contains(strings.ToLower(text), "(code=repeat_tool_loop)")
}

var providerModelUnavailablePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bthe\s+model\s+["'\x60]?[a-z0-9][a-z0-9._:/-]*["'\x60]?\s+(?:does\s+not\s+exist|was\s+not\s+found)\b`),
	regexp.MustCompile(`(?i)\bmodel\s+(?:is\s+)?(?:not\s+available|unavailable)\b`),
	regexp.MustCompile(`(?i)\bmodel(?:\s+id)?\s+["'\x60][^"'\x60\r\n]+["'\x60]\s+(?:is\s+)?(?:not\s+available|unavailable)\b`),
	regexp.MustCompile(`(?i)\bcode[=:"'\s]+(?:model_not_found|model_not_available|model_unavailable)\b`),
}

func providerModelUnavailable(text string) bool {
	for _, pattern := range providerModelUnavailablePatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// bridgeProviderFailureNotice recognizes only visible provider system failure
// frames. Their details are classified and sanitized by bridgeErrorNotice.
func bridgeProviderFailureNotice(sessionName string, profileBacked bool, text string) (string, bool) {
	if !providerFailureFrame(text) {
		return "", false
	}
	return bridgeErrorNotice(sessionName, text, profileBacked), true
}

func providerFailureFrame(text string) bool {
	frame := strings.ToLower(strings.TrimSpace(text))
	if !strings.HasPrefix(frame, "[system]") {
		return false
	}
	body := strings.TrimSpace(strings.TrimPrefix(frame, "[system]"))
	providerName, _, ok := strings.Cut(body, ":")
	if !ok {
		return false
	}
	return adapter.IsCodexCompatible(providerName)
}

var providerConversationMissingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:thread|conversation)\b(?:\s+(?:with\s+id\s+)?["']?[a-z._/-]*[0-9][a-z0-9._/-]*["']?)?\s+(?:was\s+)?not\s+found\b`),
	regexp.MustCompile(`(?i)\b(?:thread|conversation)\b(?:\s+(?:with\s+id\s+)?["']?[a-z._/-]*[0-9][a-z0-9._/-]*["']?)?\s+(?:does\s+not|no\s+longer)\s+exist\b`),
	regexp.MustCompile(`(?i)\b(?:thread|conversation)\b(?:\s+(?:with\s+id\s+)?["']?[a-z._/-]*[0-9][a-z0-9._/-]*["']?)?\s+is\s+missing\b`),
	regexp.MustCompile(`(?i)\bunknown\s+(?:thread|conversation)\b`),
}

func providerConversationMissing(text string) bool {
	for _, pattern := range providerConversationMissingPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// bridgeStreamFailureNotice handles failures of the amux Attach operation,
// which are distinct from provider-side conversation errors.
func bridgeStreamFailureNotice(sessionName string, err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message := strings.ToLower(err.Error())
	message = strings.TrimSpace(message)
	const grpcNotFoundPrefix = "rpc error: code = notfound desc = "
	if strings.HasPrefix(message, grpcNotFoundPrefix) {
		message = strings.TrimSpace(strings.TrimPrefix(message, grpcNotFoundPrefix))
	}
	if !strings.HasPrefix(message, "session ") {
		return "", false
	}
	notFound := strings.Index(message, " not found")
	if notFound < 0 || strings.ContainsAny(message[len("session "):notFound], ":;\r\n") {
		return "", false
	}
	suffix := strings.TrimSpace(message[notFound+len(" not found"):])
	if suffix != "" && !strings.HasPrefix(suffix, "on target ") {
		return "", false
	}
	if sessionName == "" {
		return "⚠️ 当前话题关联的会话已不存在，请新建话题重新开始。", true
	}
	return fmt.Sprintf(
		"⚠️ 当前话题关联的会话已不存在（`%s`），请新建话题重新开始。",
		sessionName,
	), true
}

// StopStream cancels the stream for the given root message thread.
func (c *streamController) StopStream(rootMsgID string) {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	if cancel := c.removeStreamLocked(rootMsgID); cancel != nil {
		cancel()
	}
}

// removeStreamLocked unregisters one stream from both its presentation root
// and target/session ownership indexes. The caller must hold b.streamMu.
func (c *streamController) removeStreamLocked(rootMsgID string) context.CancelFunc {
	cancel, ok := c.streams[rootMsgID]
	if !ok {
		return nil
	}
	token := c.streamTokens[rootMsgID]
	delete(c.streams, rootMsgID)
	delete(c.streamTokens, rootMsgID)
	delete(c.streamReady, rootMsgID)
	if sessionKey := c.streamSessionID[rootMsgID]; sessionKey != "" {
		if current, ok := c.streamSessions[sessionKey]; ok && current.token == token {
			delete(c.streamSessions, sessionKey)
		}
		delete(c.streamSessionID, rootMsgID)
	}
	return cancel
}

func (c *streamController) registerStreamFreeze(rootMsgID string, token uint64, fn func()) {
	c.streamMu.Lock()
	if c.streamTokens[rootMsgID] != token {
		c.streamMu.Unlock()
		return
	}
	if c.streamFreeze == nil {
		c.streamFreeze = make(map[string]streamFreezeHandler)
	}
	c.streamFreeze[rootMsgID] = streamFreezeHandler{token: token, fn: fn}
	pending := c.supersededTurns[rootMsgID]
	c.streamMu.Unlock()
	if pending && fn != nil {
		fn()
	}
}

func (c *streamController) unregisterStreamFreeze(rootMsgID string, token uint64) {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	if current, ok := c.streamFreeze[rootMsgID]; ok && current.token == token {
		delete(c.streamFreeze, rootMsgID)
	}
}

func (c *streamController) markTurnSuperseded(rootMsgID string) func() {
	c.streamMu.Lock()
	if c.supersededTurns == nil {
		c.supersededTurns = make(map[string]bool)
	}
	c.supersededTurns[rootMsgID] = true
	freeze := c.streamFreeze[rootMsgID].fn
	c.streamMu.Unlock()
	return freeze
}

func (c *streamController) clearSupersededTurn(rootMsgID string) {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	delete(c.supersededTurns, rootMsgID)
}

func (c *streamController) isTurnSuperseded(rootMsgID string) bool {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	return c.supersededTurns[rootMsgID]
}

type attachTextKind int

const (
	attachTextAgentOutput attachTextKind = iota
	attachTextCLIInput
	attachTextLarkInput
)

func streamOutputAsReplyCardForWorkspace(agentType, workspaceKind string) bool {
	switch agentType {
	case "claude", "codex", "pi":
		return true
	default:
		return distributionReplyCardAgent(agentType)
	}
}

func (b *Bridge) workspaceKindForChat(chatID string) string {
	if b == nil || b.db == nil || chatID == "" {
		return WorkspaceKindGroup
	}
	kind, err := b.db.WorkspaceKind(b.currentBridgeId(), chatID)
	if err != nil || kind == "" {
		return WorkspaceKindGroup
	}
	return kind
}

func (b *Bridge) sendTextToBinding(ctx context.Context, chat *LarkChat, text string) (string, string, error) {
	if bs, ok, err := b.botSessionForBinding(chat); ok || err != nil {
		if err != nil {
			return "", "", err
		}
		return b.sendTextToBotSessionTarget(ctx, bs, text)
	}
	return b.sendTextToThread(ctx, chat.RootMsgID, text)
}

func (b *Bridge) sendAgentReplyToBinding(ctx context.Context, chat *LarkChat, text string) (string, string, error) {
	if bs, ok, err := b.botSessionForBinding(chat); ok || err != nil {
		if err != nil {
			return "", "", err
		}
		return b.sendAgentReplyCardToBotSessionTarget(ctx, bs, text)
	}
	return b.sendAgentReplyToThread(ctx, chat.RootMsgID, text)
}

func (b *Bridge) sendToolActivityToBinding(ctx context.Context, chat *LarkChat, meta toolActivityCardMeta, text string) (string, string, error) {
	if bs, ok, err := b.botSessionForBinding(chat); ok || err != nil {
		if err != nil {
			return "", "", err
		}
		return b.sendToolActivityCardToBotSessionTarget(ctx, bs, meta, text)
	}
	return b.sendToolActivityToThread(ctx, chat.RootMsgID, meta, text)
}

func (b *Bridge) patchToolActivityBinding(ctx context.Context, chat *LarkChat, msgID string, meta toolActivityCardMeta, text string) error {
	if _, ok, err := b.botSessionForBinding(chat); ok || err != nil {
		if err != nil {
			return err
		}
		return b.patchToolActivityMessage(ctx, msgID, meta, text)
	}
	return b.patchToolActivityMessage(ctx, msgID, meta, text)
}

func (b *Bridge) sendAgentReplyCardToBinding(ctx context.Context, chat *LarkChat, text string) (string, string, error) {
	if bs, ok, err := b.botSessionForBinding(chat); ok || err != nil {
		if err != nil {
			return "", "", err
		}
		return b.sendAgentReplyCardToBotSessionTarget(ctx, bs, text)
	}
	return b.sendAgentReplyCardToThread(ctx, chat.RootMsgID, text)
}

func (b *Bridge) sendInteractiveToBinding(ctx context.Context, chat *LarkChat, content, label string) (string, string, error) {
	if bs, ok, err := b.botSessionForBinding(chat); ok || err != nil {
		if err != nil {
			return "", "", err
		}
		return b.sendInteractiveToBotSessionTarget(ctx, bs, content, label)
	}
	if chat == nil {
		return "", "", fmt.Errorf("nil lark chat")
	}
	return b.sendInteractiveToThread(ctx, chat.RootMsgID, content, label)
}

func (b *Bridge) patchAgentReplyBinding(ctx context.Context, chat *LarkChat, msgID, text string) error {
	if _, ok, err := b.botSessionForBinding(chat); ok || err != nil {
		if err != nil {
			return err
		}
		return b.patchAgentReplyCard(ctx, msgID, text)
	}
	return b.patchAgentReplyCard(ctx, msgID, text)
}

func (b *Bridge) botSessionForBinding(chat *LarkChat) (*BotSession, bool, error) {
	bridgeID, ok := singletonBridgeIDFromRoot(chat)
	if !ok {
		return nil, false, nil
	}
	if b == nil || b.db == nil {
		return nil, true, fmt.Errorf("singleton bot-session output requires bridge db")
	}
	bs, err := b.db.GetBotSession(bridgeID)
	if err != nil {
		return nil, true, err
	}
	if !botSessionReady(bs) {
		return nil, true, fmt.Errorf("singleton bot-session %s is not enabled", bridgeID)
	}
	// A singleton stream key identifies the bot, so verify that the live stream
	// still belongs to the bot's current target/session binding. This prevents a
	// stale stream from routing old-session output through a newly rebound bot's
	// LastReply* destination.
	chatTargetID := strings.TrimSpace(chat.TargetID)
	chatSessionName := strings.TrimSpace(chat.SessionName)
	if (chatTargetID != "" && chatTargetID != strings.TrimSpace(bs.TargetID)) ||
		(chatSessionName != "" && chatSessionName != strings.TrimSpace(bs.SessionName)) {
		return nil, true, fmt.Errorf(
			"stale singleton bot-session stream for %s: stream=%s/%s binding=%s/%s",
			bridgeID,
			chatTargetID,
			chatSessionName,
			strings.TrimSpace(bs.TargetID),
			strings.TrimSpace(bs.SessionName),
		)
	}
	return bs, true, nil
}

func singletonBridgeIDFromRoot(chat *LarkChat) (string, bool) {
	if chat == nil {
		return "", false
	}
	root := strings.TrimSpace(chat.RootMsgID)
	const prefix = "singleton:"
	if !strings.HasPrefix(root, prefix) {
		return "", false
	}
	bridgeID := strings.TrimSpace(strings.TrimPrefix(root, prefix))
	return bridgeID, bridgeID != ""
}

func (b *Bridge) sendTextToBotSessionTarget(ctx context.Context, bs *BotSession, text string) (string, string, error) {
	if bs == nil {
		return "", "", fmt.Errorf("nil bot session")
	}
	switch strings.TrimSpace(bs.LastReplyKind) {
	case botSessionReplyDM:
		if strings.TrimSpace(bs.LastReplyOpenID) == "" {
			return "", "", fmt.Errorf("bot-session dm output target is empty")
		}
		return b.notifyDM(ctx, bs.LastReplyOpenID, text)
	case botSessionReplyGroup:
		if strings.TrimSpace(bs.LastReplyChatID) == "" {
			return "", "", fmt.Errorf("bot-session group output target is empty")
		}
		return b.notifyChat(ctx, bs.LastReplyChatID, text)
	default:
		return "", "", fmt.Errorf("bot-session output target is unknown")
	}
}

func (b *Bridge) sendAgentReplyCardToBotSessionTarget(ctx context.Context, bs *BotSession, text string) (string, string, error) {
	return b.sendInteractiveToBotSessionTarget(ctx, bs, renderAgentReplyCard(text), "agent reply card")
}

func (b *Bridge) sendToolActivityCardToBotSessionTarget(ctx context.Context, bs *BotSession, meta toolActivityCardMeta, text string) (string, string, error) {
	return b.sendInteractiveToBotSessionTarget(ctx, bs, renderToolActivityCard(meta, text), "tool activity card")
}

func (b *Bridge) sendInteractiveToBotSessionTarget(ctx context.Context, bs *BotSession, content, label string) (string, string, error) {
	if bs == nil {
		return "", "", fmt.Errorf("nil bot session")
	}
	switch strings.TrimSpace(bs.LastReplyKind) {
	case botSessionReplyDM:
		if strings.TrimSpace(bs.LastReplyOpenID) == "" {
			return "", "", fmt.Errorf("bot-session dm output target is empty")
		}
		return b.sendInteractiveToDM(ctx, bs.LastReplyOpenID, content, label)
	case botSessionReplyGroup:
		if strings.TrimSpace(bs.LastReplyChatID) == "" {
			return "", "", fmt.Errorf("bot-session group output target is empty")
		}
		return b.sendInteractiveToChat(ctx, bs.LastReplyChatID, content, label)
	default:
		return "", "", fmt.Errorf("bot-session output target is unknown")
	}
}

// refreshSubtaskProgress polls the hub task plane for subtasks spawned by the
// bound session and pushes them onto the activity card. No-op without a
// SubtaskReader (local CLI-only deployments).
func (b *Bridge) refreshSubtaskProgress(activity *toolActivityState, targetID, sessionName string) {
	if b.subtasks == nil {
		return
	}
	activity.SetSubtasks(b.subtaskProgressForTurn(
		context.Background(),
		targetID,
		sessionName,
		activity.TurnStartedAt(),
		activity.now(),
	))
}

func (b *Bridge) subtaskProgressForTurn(
	ctx context.Context,
	targetID string,
	sessionName string,
	turnStartedAt time.Time,
	now time.Time,
) []subtaskProgress {
	if b.subtasks == nil {
		return nil
	}
	parent := b.boundSessionID(targetID, sessionName)
	if parent == "" {
		return nil
	}
	ids := b.subtasks.SubtaskIDsByParent(parent)
	return b.subtaskProgressForIDs(ctx, ids, turnStartedAt, now)
}

func (b *Bridge) liveSubtaskProgressForTurn(
	ctx context.Context,
	targetID string,
	sessionName string,
	turnStartedAt time.Time,
	now time.Time,
) []subtaskProgress {
	reader, ok := b.subtasks.(SubtaskEventReader)
	if !ok {
		return nil
	}
	parent := b.boundSessionID(targetID, sessionName)
	if parent == "" {
		return nil
	}
	return b.subtaskProgressForIDs(ctx, reader.LiveSubtaskIDsByParent(parent), turnStartedAt, now)
}

func (b *Bridge) subtaskProgressForIDs(
	ctx context.Context,
	ids []string,
	turnStartedAt time.Time,
	now time.Time,
) []subtaskProgress {
	if len(ids) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rows []subtaskProgress
	for _, id := range ids {
		progress, ok := b.readSubtaskProgress(ctx, id, now)
		if !ok {
			continue
		}
		if turnStartedAt.IsZero() || progress.RequestedAt.IsZero() || progress.RequestedAt.Before(turnStartedAt) {
			continue
		}
		rows = append(rows, progress)
	}
	return rows
}

func (b *Bridge) readSubtaskProgress(ctx context.Context, id string, now time.Time) (subtaskProgress, bool) {
	raw, err := b.subtasks.GetSubtaskJSON(ctx, id)
	if err != nil {
		return subtaskProgress{}, false
	}
	var run struct {
		ID               string `json:"id"`
		AgentType        string `json:"agent_type"`
		AgentProfile     string `json:"agent_profile"`
		AgentInstanceID  string `json:"agent_instance_id"`
		Status           string `json:"status"`
		Task             string `json:"task"`
		TargetID         string `json:"target_id"`
		ChildSessionName string `json:"child_session_name"`
		ChildSessionID   string `json:"child_session_id"`
		TimeoutSeconds   int    `json:"timeout_seconds"`
		Result           struct {
			Summary   string `json:"summary"`
			UsageJSON []byte `json:"usage_json,omitempty"`
		} `json:"result"`
		Error       string    `json:"error"`
		RequestedAt time.Time `json:"requested_at"`
		StartedAt   time.Time `json:"started_at"`
		FinishedAt  time.Time `json:"finished_at"`
	}
	if json.Unmarshal(raw, &run) != nil || run.ID == "" {
		return subtaskProgress{}, false
	}
	start := run.StartedAt
	if start.IsZero() {
		start = run.RequestedAt
	}
	end := now
	if !run.FinishedAt.IsZero() {
		end = run.FinishedAt
	}
	var elapsed time.Duration
	if !start.IsZero() && end.After(start) {
		elapsed = end.Sub(start)
	}
	progress := subtaskProgress{
		ID:               run.ID,
		AgentType:        run.AgentType,
		AgentProfile:     run.AgentProfile,
		AgentInstanceID:  run.AgentInstanceID,
		Status:           run.Status,
		Title:            truncateForNotice(run.Task, 80),
		Summary:          run.Result.Summary,
		UsageJSON:        run.Result.UsageJSON,
		Error:            run.Error,
		TargetID:         run.TargetID,
		ChildSessionName: run.ChildSessionName,
		ChildSessionID:   run.ChildSessionID,
		TimeoutSeconds:   run.TimeoutSeconds,
		RequestedAt:      run.RequestedAt,
		StartedAt:        run.StartedAt,
		FinishedAt:       run.FinishedAt,
		Elapsed:          elapsed,
	}
	applySubtaskChildMetadata(&progress, b.subtaskChildMetadata(run.TargetID, run.ChildSessionID, run.ChildSessionName))
	return progress, true
}

// boundSessionID resolves the session id of (targetID, sessionName) from the
// hub's target snapshot; empty when unknown.
func (b *Bridge) boundSessionID(targetID, sessionName string) string {
	return b.boundSession(targetID, sessionName).GetId()
}

func (b *Bridge) boundSession(targetID, sessionName string) *pb.Session {
	for _, t := range b.hub.ListTargets() {
		if t.Id != targetID {
			continue
		}
		for _, s := range t.Sessions {
			if s.Name == sessionName {
				return s
			}
		}
	}
	return nil
}
