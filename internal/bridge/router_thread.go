package bridge

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/bridge/threadcontext"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/proto"
)

func (b *Bridge) bindThread(ctx context.Context, chatID, rootMsgID, openID, text string) error {
	ok, err := b.canCreateOrBindInChat(chatID, openID)
	if err != nil {
		return err
	}
	if !ok {
		return b.notifyThread(ctx, rootMsgID, memberBlockedInOwnerModeText())
	}
	ref := bindSessionRef(text)
	if ref == "" {
		return b.notifyThread(ctx, rootMsgID, "用法：/bind <target/session>")
	}
	resolved, ok := b.resolveSessionRef(ref)
	if !ok {
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("找不到唯一 session: %s", ref))
	}
	if err := b.checkGroupBindingIsolation(ctx, chatID, resolved); err != nil {
		return b.notifyThread(ctx, rootMsgID, err.Error())
	}
	return b.bindResolvedThread(ctx, chatID, rootMsgID, openID, resolved, "已绑定到 session")
}
func (b *Bridge) stopCurrentSessionAndBindThread(ctx context.Context, chatID, rootMsgID, openID, text string, current *LarkChat) error {
	if current == nil {
		return b.bindThread(ctx, chatID, rootMsgID, openID, text)
	}
	if !b.canControlBinding(openID, current) {
		return b.notifyThread(ctx, rootMsgID, "你没有权限操作此 amux session。")
	}
	ref := bindSessionRef(text)
	if ref == "" {
		return b.notifyThread(ctx, rootMsgID, "用法：/bind <target/session>")
	}
	resolved, ok := b.resolveSessionRef(ref)
	if !ok {
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("找不到唯一 session: %s", ref))
	}
	if current.TargetID == resolved.TargetID && current.SessionName == resolved.SessionName {
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("已经绑定到 session: %s/%s", resolved.TargetName, resolved.SessionName))
	}
	if err := b.checkGroupBindingIsolation(ctx, chatID, resolved); err != nil {
		return b.notifyThread(ctx, rootMsgID, err.Error())
	}
	existing, err := b.db.FindBySession(resolved.TargetID, resolved.SessionName)
	if err != nil {
		return err
	}
	if existing != nil && existing.RootMsgID != rootMsgID && !b.canTransferBinding(openID, existing) {
		return b.notifyThread(ctx, rootMsgID, "你没有权限接管此 amux session 的 Feishu 绑定。")
	}
	log.Info().Str("current_session", current.SessionName).Str("target_session", resolved.SessionName).Msg("bridge: /bind — stopping current session before rebinding thread")
	killCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := b.hub.Route(killCtx, current.TargetID, "Kill", &pb.KillRequest{Name: current.SessionName}); err != nil {
		return b.notifyThread(ctx, rootMsgID, "⚠️ 停止当前 session 失败："+err.Error())
	}
	b.StopStream(rootMsgID)
	if err := b.db.TerminateChat(b.currentBridgeId(), rootMsgID); err != nil {
		return err
	}
	return b.bindResolvedThread(ctx, chatID, rootMsgID, openID, resolved, "已绑定到 session")
}
func (b *Bridge) bindResolvedThread(ctx context.Context, chatID, rootMsgID, openID string, resolved resolvedBridgeSession, action string) error {
	return b.bindResolvedThreadWithOptions(ctx, chatID, rootMsgID, openID, resolved, bindThreadOptions{Action: action})
}
func (b *Bridge) bindResolvedThreadWithOptions(ctx context.Context, chatID, rootMsgID, openID string, resolved resolvedBridgeSession, opts bindThreadOptions) error {
	action := strings.TrimSpace(opts.Action)
	if action == "" {
		action = "已绑定到 session"
	}
	existing, err := b.db.FindBySession(resolved.TargetID, resolved.SessionName)
	if err != nil {
		return err
	}
	if existing != nil && existing.RootMsgID != rootMsgID {
		if !b.canTransferBinding(openID, existing) {
			return b.notifyThread(ctx, rootMsgID, "你没有权限接管此 amux session 的 Feishu 绑定。")
		}
		action = "已接管 session"
	}
	if existing != nil && existing.RootMsgID == rootMsgID {
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("已经绑定到 session: %s/%s", resolved.TargetName, resolved.SessionName))
	}
	if err := b.db.UpsertChat(LarkChat{BridgeId: b.currentBridgeId(), ChatID: chatID, RootMsgID: rootMsgID, ThreadID: rootMsgID, OpenId: openID, TargetID: resolved.TargetID, TargetName: resolved.TargetName, SessionName: resolved.SessionName, AgentProfile: resolved.AgentProfile, AgentType: resolved.AgentType, EffectiveModel: resolved.EffectiveModel, DisplayTitle: opts.DisplayTitle, EntrySource: opts.EntrySource}); err != nil {
		return err
	}
	if existing != nil && existing.RootMsgID != rootMsgID {
		b.StopStream(existing.RootMsgID)
	}
	chat, err := b.db.FindByRootMsg(b.currentBridgeId(), rootMsgID)
	if err == nil {
		b.startThreadStream(chat, openID)
	}
	if opts.SkipNotice {
		return nil
	}
	if notice := strings.TrimSpace(opts.NoticeText); notice != "" {
		return b.notifyThread(ctx, rootMsgID, notice)
	}
	return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("%s: %s/%s", action, resolved.TargetName, resolved.SessionName))
}
func (b *Bridge) canTransferBinding(openID string, existing *LarkChat) bool {
	if existing == nil {
		return true
	}
	if existing.BridgeId == b.currentBridgeId() {
		return b.canControlBinding(openID, existing)
	}
	return existing.OpenId != "" && openID == existing.OpenId
}
func parseTopicRun(text string) (topicRunRequest, bool) {
	text, ok := slashCommandText(text)
	if !ok {
		return topicRunRequest{}, false
	}
	parts := strings.Fields(text)
	if len(parts) < 4 || strings.ToLower(parts[0]) != "run" {
		return topicRunRequest{}, false
	}
	req := topicRunRequest{AgentRef: parts[1]}
	for i := 2; i < len(parts); i++ {
		part := parts[i]
		switch {
		case part == "--":
			req.ExtraArgs = append(req.ExtraArgs, parts[i+1:]...)
			i = len(parts)
		case part == "-n" || part == "--name":
			if i+1 >= len(parts) {
				return topicRunRequest{}, false
			}
			req.Name = parts[i+1]
			i++
		case part == "--on":
			if i+1 >= len(parts) {
				return topicRunRequest{}, false
			}
			req.Target = parts[i+1]
			i++
		case strings.HasPrefix(part, "--on="):
			req.Target = strings.TrimPrefix(part, "--on=")
		case part == "--cwd":
			if i+1 >= len(parts) {
				return topicRunRequest{}, false
			}
			req.Cwd = parts[i+1]
			i++
		case strings.HasPrefix(part, "--cwd="):
			req.Cwd = strings.TrimPrefix(part, "--cwd=")
		case part == "--model":
			if i+1 >= len(parts) {
				return topicRunRequest{}, false
			}
			req.Model = parts[i+1]
			i++
		case strings.HasPrefix(part, "--model="):
			req.Model = strings.TrimPrefix(part, "--model=")
		default:
			return topicRunRequest{}, false
		}
	}
	if req.AgentRef == "" || req.Name == "" {
		return topicRunRequest{}, false
	}
	return req, true
}
func resolveBridgeAgentLaunch(ref string, explicitArgs []string, model string) (agentprofile.LaunchProjection, error) {
	return resolveBridgeAgentLaunchWithSelection(ref, explicitArgs, "", model, "")
}
func resolveBridgeAgentLaunchWithSelection(ref string, explicitArgs []string, runtimeRef, modelRef, reasoningEffort string) (agentprofile.LaunchProjection, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return agentprofile.LaunchProjection{}, fmt.Errorf("agent is required")
	}
	resolvedRef, err := resolveBridgeAgentRef(ref)
	if err != nil {
		return agentprofile.LaunchProjection{}, err
	}
	ref = resolvedRef
	if profile, err := agentprofile.ResolveProfile(agentprofile.DefaultPackRegistryPath(), ref); err == nil {
		return agentprofile.ProjectProfileLaunchChecked(profile, explicitArgs, nil, agentprofile.LaunchOptions{
			Runtime: strings.TrimSpace(runtimeRef), Model: strings.TrimSpace(modelRef),
			ReasoningEffort: strings.TrimSpace(reasoningEffort),
		})
	} else if strings.Contains(ref, "/") || !agentprofile.IsProfileNotFound(err) {
		return agentprofile.LaunchProjection{}, err
	}
	if strings.TrimSpace(runtimeRef) != "" || strings.TrimSpace(reasoningEffort) != "" {
		return agentprofile.LaunchProjection{}, fmt.Errorf("runtime and reasoning selection require an Agent Profile, got legacy agent %q", ref)
	}
	return agentprofile.ProjectLegacyLaunch(ref, explicitArgs, agentprofile.LaunchOptions{Model: modelRef}), nil
}
func resolveBridgeAgentRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("agent is required")
	}
	if strings.Contains(ref, "/") {
		profile, err := agentprofile.ResolveProfile(agentprofile.DefaultPackRegistryPath(), ref)
		if err != nil {
			return "", err
		}
		return profile.Ref(), nil
	}
	agentType := strings.ToLower(ref)
	if _, err := adapter.Get(agentType); err == nil {
		return agentType, nil
	}
	if profile, err := agentprofile.ResolveProfile(agentprofile.DefaultPackRegistryPath(), ref); err == nil {
		return profile.Ref(), nil
	} else if !agentprofile.IsProfileNotFound(err) {
		return "", err
	}
	if profile, err := resolveUniqueBridgeProfileShortName(ref); err == nil {
		return profile.Ref(), nil
	} else if !agentprofile.IsProfileNotFound(err) {
		return "", err
	}
	return "", fmt.Errorf("没找到专家“%s”。请私聊助手发送 /amux，在“助手”中选择。", ref)
}
func resolveUniqueBridgeProfileShortName(name string) (agentprofile.Profile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return agentprofile.Profile{}, agentprofile.ErrProfileNotFound
	}
	registry, err := agentprofile.LoadPackRegistry(agentprofile.DefaultPackRegistryPath())
	if err != nil {
		return agentprofile.Profile{}, err
	}
	profiles, err := agentprofile.ListPackProfiles(registry)
	if err != nil {
		return agentprofile.Profile{}, err
	}
	var matches []agentprofile.Profile
	for _, profile := range profiles {
		if strings.EqualFold(profile.Name, name) {
			matches = append(matches, profile)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		refs := make([]string, 0, len(matches))
		for _, profile := range matches {
			refs = append(refs, profile.Ref())
		}
		sort.Strings(refs)
		return agentprofile.Profile{}, fmt.Errorf("找到多个 %s：%s。请说完整名称。", name, strings.Join(refs, "、"))
	}
	return agentprofile.Profile{}, agentprofile.ErrProfileNotFound
}

func (b *Bridge) runFeishuSession(ctx context.Context, rootMsgID, chatID, messageAuthorOpenID, executionActorOpenID, targetID, sessionName, cwd string, launch agentprofile.LaunchProjection, idleTimeoutSeconds int) (sessionlaunch.SessionRef, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var res sessionlaunch.SessionRef
	policy, err := b.newSessionPolicy(ctx, chatID)
	if err != nil {
		return res, false, err
	}
	identity, err := b.executionIdentity(executionActorOpenID, "feishu-session:"+sessionName, targetID, policy.shared, policy.isolationRequired)
	if err != nil {
		return res, false, b.notifyThread(ctx, rootMsgID, "⚠️ 无法解析执行身份："+err.Error())
	}
	if policy.isolationRequired {
		if identity.Identity == nil || identity.Identity.IdentityMode != string(principal.IdentityModeIdentified) || strings.TrimSpace(identity.Identity.EffectivePrincipalId) == "" || strings.TrimSpace(identity.LaunchID) == "" {
			return res, false, b.notifyThread(ctx, rootMsgID, "⚠️ 当前设置为「每个话题独立（用户隔离）」，但本环境无法以隔离用户身份创建会话（可能是已启用用户隔离的 Linux 环境不可用或已降级）。请改回「每个话题独立」后重试。")
		}
		backend := strings.TrimSpace(identity.Identity.IsolationBackend)
		if backend != "" && backend != principal.BackendLinuxDAC {
			return res, false, b.notifyThread(ctx, rootMsgID, "⚠️ 当前设置为「每个话题独立（用户隔离）」，但解析到的执行身份不是用户隔离身份。请检查运行环境的隔离配置后重试。")
		}
		pinned := proto.Clone(identity.Identity).(*pb.ExecutionIdentity)
		pinned.IsolationBackend = principal.BackendLinuxDAC
		identity.Identity = pinned
	}
	idleTimeout := int64(idleTimeoutSeconds)
	res, err = b.sessionStarter.Start(runCtx, sessionlaunch.StartRequest{TargetID: targetID, Name: sessionName, Launch: launch, Workspace: sessionlaunch.WorkspaceRequest{Cwd: cwd, Source: "feishu", Scope: b.feishuWorkspaceScope(chatID, messageAuthorOpenID)}, IdleTimeoutSeconds: &idleTimeout, ExecutionIdentity: identity.Identity, LaunchID: identity.LaunchID, AgentActorToken: identity.ActorToken, AgentActorTokenExpiresAt: identity.ActorTokenExpiresAt})
	if err != nil {
		principalID := ""
		if identity.Identity != nil {
			principalID = strings.TrimSpace(identity.Identity.EffectivePrincipalId)
		}
		return res, false, b.notifyThread(ctx, rootMsgID, feishuSessionStartFailureNotice(err, principalID))
	}
	for _, warning := range // runFeishuSession delegates ordinary session creation to the shared starter.
	// Feishu-specific deadline and failure presentation remain at this boundary.
	res.Warnings {
		if err := b.notifyThread(ctx, rootMsgID, "⚠️ "+warning); err != nil {
			return res, false, err
		}
	}
	return res, true, nil
}
func feishuSessionStartFailureNotice(err error, principalID string) string {
	var startErr *sessionlaunch.StartError
	if errors.As(err, &startErr) && startErr.Kind == sessionlaunch.ErrorInvalidResponse {
		return "⚠️ 解析创建结果失败：" + startErr.Err.Error()
	}
	return feishuRunFailureNotice(err, principalID)
}
func feishuRunFailureNotice(err error, principalID string) string {
	if setup, ok := providerCredentialSetupFromError(err); ok {
		principalID = strings.TrimSpace(principalID)
		if _, parseErr := uuid.Parse(principalID); parseErr != nil {
			return "⚠️ 创建 session 失败：缺少模型凭据\n\n请在运行 amux 的机器上执行：\n" + setup.Command
		}
		return fmt.Sprintf("⚠️ 创建 session 失败：当前飞书用户缺少模型凭据\n\n凭据：%s / %s\n作用域：Principal %s\n\n请在运行 amux 的机器上执行：\n\namux agent credential setup %s %s --principal %s\n\n检测到已有 legacy-host 凭据时会询问是否复用，无需重新输入。配置成功后请重新创建 session。", setup.Profile, setup.EnvName, principalID, setup.Profile, setup.EnvName, principalID)
	}
	return "⚠️ 创建 session 失败：" + err.Error()
}

type providerCredentialSetup struct {
	Profile string
	EnvName string
	Command string
}

func providerCredentialSetupFromError(err error) (providerCredentialSetup, bool) {
	if err == nil {
		return providerCredentialSetup{}, false
	}
	const marker = "setup: "
	text := err.Error()
	idx := strings.LastIndex(text, marker)
	if idx < 0 {
		return providerCredentialSetup{}, false
	}
	setup := strings.TrimSpace(text[idx+len(marker):])
	if cut := strings.IndexAny(setup, "\r\n;"); cut >= 0 {
		setup = strings.TrimSpace(setup[:cut])
	}
	fields := strings.Fields(setup)
	if len(fields) != 6 {
		return providerCredentialSetup{}, false
	}
	if fields[0] != "amux" || fields[1] != "agent" || fields[2] != "credential" || fields[3] != "set" {
		return providerCredentialSetup{}, false
	}
	return providerCredentialSetup{
		Profile: fields[4],
		EnvName: fields[5],
		Command: strings.Join(fields, " "),
	}, true
}
func (b *Bridge) runAndBindThread(ctx context.Context, chatID, rootMsgID, openID, text string) error {
	ok, err := b.canCreateOrBindInChat(chatID, openID)
	if err != nil {
		return err
	}
	if !ok {
		return b.notifyThread(ctx, rootMsgID, memberBlockedInOwnerModeText())
	}
	req, ok := parseTopicRun(text)
	if !ok {
		return b.notifyThread(ctx, rootMsgID, "用法：/run <agent> -n <name> [--on <target>] [--model <model>] [--cwd <dir>] [-- <agent-args>...]")
	}
	policy, err := b.newSessionPolicy(ctx, chatID)
	if err != nil {
		return err
	}
	ctx = withGroupLaunchPolicy(ctx, policy.settings, policy.isolationRequired, false)
	runtimeRef, reasoningEffort := "", ""
	if group := policy.settings; group != nil {
		if req.Target == "" {
			req.Target = group.DefaultTarget
		}
		if req.Cwd == "" {
			req.Cwd = group.DefaultCwd
		}
		if req.Model == "" {
			req.Model = group.DefaultModelRef
		}
		if len(req.ExtraArgs) == 0 {
			req.ExtraArgs = append([]string(nil), group.DefaultArgs...)
		}
		runtimeRef, reasoningEffort = group.DefaultRuntimeRef, group.DefaultReasoningEffort
	}
	target, targetErr := b.resolveRunTarget(req.Target)
	if targetErr != "" {
		return b.notifyThread(ctx, rootMsgID, targetErr)
	}
	if existing, err := b.db.FindBySession(target.ID, req.Name); err != nil {
		return err
	} else if existing != nil && existing.RootMsgID != rootMsgID {
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("session 已绑定到另一个话题: %s/%s", target.Name, req.Name))
	}
	launch, agentErr := resolveBridgeAgentLaunchWithSelection(req.AgentRef, req.ExtraArgs, runtimeRef, req.Model, reasoningEffort)
	if agentErr != nil {
		return b.notifyThread(ctx, rootMsgID, agentErr.Error())
	}
	ws, err := b.currentWorkspace(chatID)
	if err != nil {
		return err
	}
	ws = groupSettingsWorkspace(ws, policy.settings)
	blocked, err := b.enforceRunQuota(ctx, rootMsgID, chatID, openID, ws)
	if err != nil {
		return err
	}
	if blocked {
		return nil
	}
	res, ok, err := b.runFeishuSession(ctx, rootMsgID, chatID, openID, openID, target.ID, req.Name, req.Cwd, launch, ws.IdleTimeoutSeconds)
	if !ok {
		return err
	}
	return b.bindResolvedThread(ctx, chatID, rootMsgID, openID, resolvedBridgeSession{TargetID: target.ID, TargetName: target.Name, SessionName: res.Name, AgentProfile: res.AgentProfile, AgentType: res.AgentType, EffectiveModel: res.EffectiveModel}, "已创建并绑定 session")
}
func (b *Bridge) runAndBindNaturalEntry(ctx context.Context, chatID, rootMsgID, msgID, openID, text string, mention botMentionInfo) error {
	return b.runAndBindNaturalEntryWithOptions(ctx, chatID, rootMsgID, msgID, openID, text, mention, naturalEntryOptions{})
}
func (b *Bridge) runAndBindNaturalEntryWithOptions(ctx context.Context, chatID, rootMsgID, msgID, openID, text string, mention botMentionInfo, opts naturalEntryOptions) error {
	policy, err := b.newSessionPolicy(ctx, chatID)
	if err != nil {
		return err
	}
	ctx = withGroupLaunchPolicy(ctx, policy.settings, policy.isolationRequired, policy.shared)
	if opts.source != routeInputSourceGroupWatch {
		ok, err := b.canCreateOrBindInChat(chatID, openID)
		if err != nil {
			return err
		}
		if !ok {
			return b.notifyThread(ctx, rootMsgID, memberBlockedInOwnerModeText())
		}
	}
	routedText := b.stripCurrentBotMention(text, mention)
	quoteRef := feishuQuotedMessageFromContext(ctx)
	hasQuotedMessage := quoteRef.messageID != "" && !mention.AmuxDelegation
	rootText := ""
	if !mention.AmuxDelegation {
		rootText = routedText
		if strings.TrimSpace(rootMsgID) != strings.TrimSpace(msgID) && quoteRef.messageID != strings.TrimSpace(rootMsgID) {
			rootText = b.rootThreadRequestText(ctx, chatID, rootMsgID)
		}
	}
	usedRootRequest := false
	if routedText == "" && !hasQuotedMessage {
		routedText = rootText
		if routedText == "" {
			return b.notifyThreadHelpCard(ctx, rootMsgID, renderSessionEntryHelpCard())
		}
		usedRootRequest = true
	}
	if strings.HasPrefix(routedText, "/") {
		return b.notifyThreadHelpCard(ctx, rootMsgID, renderSessionEntryHelpCard())
	}
	if cmd := freshThreadCommand(routedText); cmd != command.CommandNone {
		return b.notifyThreadHelpCard(ctx, rootMsgID, renderSessionEntryHelpCard())
	}
	ws, err := b.currentWorkspace(chatID)
	if err != nil {
		return err
	}
	ws = groupSettingsWorkspace(ws, policy.settings)
	target, targetErr := b.resolveRunTarget(ws.DefaultTarget)
	if targetErr != "" {
		return b.notifyThread(ctx, rootMsgID, targetErr)
	}
	sessionName := naturalEntrySessionName(b.currentBridgeId(), chatID, rootMsgID)
	if existing, err := b.db.FindBySession(target.ID, sessionName); err != nil {
		return err
	} else if existing != nil && existing.RootMsgID != rootMsgID {
		return b.notifyThread(ctx, rootMsgID, fmt.Sprintf("session 已绑定到另一个话题: %s/%s", target.Name, sessionName))
	}
	if opts.alertDedup != nil {
		unlock := b.lockRoute("group-watch-alert:" + opts.alertDedup.lockKey())
		defer unlock()
		claimed, err := b.claimGroupWatchAlert(ctx, opts.alertDedup, msgID, target.ID, sessionName)
		if err != nil || !claimed {
			return err
		}
	}
	claimMessageID := msgID
	if opts.alertDedup != nil && opts.alertDedup.ResumeClaim != nil {
		prior := opts.alertDedup.ResumeClaim
		claimMessageID, rootMsgID, sessionName = prior.MessageID, prior.MessageID, prior.SessionName
		// Continue the original topic and Runtime; the new matching alert supplies input.
		target.ID, target.Name = prior.TargetID, prior.TargetID
	}
	finishAlert := opts.alertDedup != nil && opts.alertDedup.Resume == nil
	defer func() {
		if finishAlert {
			if finishErr := b.db.FinishGroupWatchAlert(*opts.alertDedup, claimMessageID, false, time.Now()); finishErr != nil {
				log.Error().Err(finishErr).Msg("bridge: release unstarted group watch alert")
			}
		}
	}()
	if opts.source == routeInputSourceGroupWatch && (opts.alertDedup == nil || opts.alertDedup.Resume == nil) {
		capacityUnlock := b.lockRoute("group-watch-capacity:" + b.currentBridgeId() + ":" + chatID)
		defer capacityUnlock()
		blocked, err := b.enforceGroupWatchActiveSessionLimit(ctx, rootMsgID, chatID, opts.maxActiveSessionsPerChat)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
		blocked, err = b.enforceWorkspaceTotalQuota(ctx, rootMsgID, chatID, ws)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
	}
	if opts.source != routeInputSourceGroupWatch {
		blocked, err := b.enforceRunQuota(ctx, rootMsgID, chatID, openID, ws)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
	}
	var quoted feishuQuotedMessage
	quotedPresent := false
	if !mention.AmuxDelegation {
		quoted, quotedPresent, err = b.resolveFeishuQuotedMessage(ctx)
		if err != nil {
			log.Warn().Err(err).Str("message_id", msgID).Msg("bridge: resolve quoted Feishu message")
			return b.notifyThread(ctx, rootMsgID, quotedMessageUnavailableText())
		}
	}
	if routedText == "" && quotedPresent {
		routedText = defaultQuotedMessageRequest()
	}
	quoteSuppliesRoot := !mention.AmuxDelegation && quotedPresent && b.topicRootPromptEnabled() && quoteRef.messageID == strings.TrimSpace(rootMsgID)
	state, err := b.currentBotProfileState()
	if err != nil {
		return err
	}
	agentRef := strings.TrimSpace(state.DefaultAgent)
	if agentRef == "" {
		agentRef = agentprofile.DefaultProfileRef
	}
	modelRef := strings.TrimSpace(ws.DefaultModel)
	runtimeRef := strings.TrimSpace(state.DefaultRuntimeRef)
	reasoningEffort := strings.TrimSpace(state.DefaultReasoningEffort)
	if runtimeRef != "" {
		modelRef = strings.TrimSpace(state.DefaultModelRef)
	} else if modelRef == "" {
		modelRef = strings.TrimSpace(state.DefaultModelRef)
	}
	if policy.settings != nil {
		agentRef, err = b.groupSettingsAgent(policy.settings)
		if err != nil {
			return err
		}
		runtimeRef = policy.settings.DefaultRuntimeRef
		modelRef = policy.settings.DefaultModelRef
		reasoningEffort = policy.settings.DefaultReasoningEffort
	}
	if opts.source == routeInputSourceGroupWatch {
		if err := validateGroupWatchExecution(opts.agentProfileRef, opts.runtimeRef, opts.modelRef, opts.reasoningEffort); err != nil {
			return b.notifyThread(ctx, rootMsgID, err.Error())
		}
		if strings.TrimSpace(opts.agentProfileRef) != "" {
			agentRef = strings.TrimSpace(opts.agentProfileRef)
			modelRef = strings.TrimSpace(opts.modelRef)
			if strings.TrimSpace(opts.runtimeRef) != "" {
				runtimeRef = strings.TrimSpace(opts.runtimeRef)
				reasoningEffort = strings.TrimSpace(opts.reasoningEffort)
			} else {
				// Compatibility for existing capability overrides created before
				// runtime and reasoning became independent execution choices.
				runtimeRef = ""
				reasoningEffort = ""
			}
		}
	}
	launch, agentErr := resolveBridgeAgentLaunchWithSelection(agentRef, ws.DefaultArgs,
		runtimeRef, modelRef, reasoningEffort)
	if agentErr != nil {
		return b.notifyThread(ctx, rootMsgID, agentErr.Error())
	}
	idleTimeoutSeconds := ws.IdleTimeoutSeconds
	entrySource := ""
	if opts.source == routeInputSourceGroupWatch {
		idleTimeoutSeconds = opts.idleTimeoutSeconds
		if idleTimeoutSeconds < 60 {
			idleTimeoutSeconds = config.DefaultFeishuGroupWatchIdleTimeoutSeconds
		}
		entrySource = ChatEntrySourceGroupWatch
	}
	cwd := ""
	if policy.settings != nil {
		cwd = policy.settings.DefaultCwd
	}

	var res sessionlaunch.SessionRef
	ok := false
	if opts.alertDedup != nil && opts.alertDedup.Resume != nil {
		recovered := opts.alertDedup.Resume
		res = sessionlaunch.SessionRef{TargetID: target.ID, Name: recovered.Name, AgentProfile: recovered.AgentProfile, AgentType: recovered.AgentType, EffectiveModel: recovered.EffectiveModel}
		ok = true
	} else {
		executionActorOpenID := openID
		if opts.source == routeInputSourceGroupWatch {
			executionActorOpenID = strings.TrimSpace(opts.executionActorOpenID)
			if executionActorOpenID == "" && b.resolveExecutionIdentity != nil {
				return b.notifyThread(ctx, rootMsgID, b.groupWatchOwnerUnavailableText())
			}
		}
		res, ok, err = b.runFeishuSession(ctx, rootMsgID, chatID, openID, executionActorOpenID, target.ID, sessionName, cwd, launch, idleTimeoutSeconds)
	}
	if opts.alertDedup != nil {
		finishAlert = false // Keep creation/Submit uncertainty durable until resolved.
		if !ok && res.Name == "" {
			recovered, probeErr := b.groupWatchSession(ctx, target.ID, sessionName)
			if probeErr != nil {
				return fmt.Errorf("resolve group watch launch outcome: %w", probeErr)
			}
			if recovered == nil {
				if finishErr := b.db.FinishGroupWatchAlert(*opts.alertDedup, claimMessageID, false, time.Now()); finishErr != nil {
					return finishErr
				}
				return err
			}
			res = sessionlaunch.SessionRef{TargetID: target.ID, Name: recovered.Name, AgentProfile: recovered.AgentProfile, AgentType: recovered.AgentType, EffectiveModel: recovered.EffectiveModel}
		}
		ok = res.Name != ""
	}
	if !ok {
		return err
	}

	alertAcceptedAt := time.Now()
	if err := b.bindResolvedThreadWithOptions(ctx, chatID, rootMsgID, openID, resolvedBridgeSession{TargetID: target.ID, TargetName: target.Name, SessionName: res.Name, AgentProfile: res.AgentProfile, AgentType: res.AgentType, EffectiveModel: res.EffectiveModel}, bindThreadOptions{DisplayTitle: displayTitleFromRequest(routedText), SkipNotice: true, EntrySource: entrySource}); err != nil {
		return err
	}
	chat, err := b.db.FindByRootMsg(b.currentBridgeId(), rootMsgID)
	if err != nil {
		return err
	}
	if chat == nil {
		return nil
	}
	if err := b.bindNewGroupConversation(ctx, chatID, rootMsgID); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = b.hub.Route(cleanupCtx, target.ID, "Kill", &pb.KillRequest{Name: res.Name})
		b.StopStream(rootMsgID)
		_ = b.db.TerminateChat(b.currentBridgeId(), rootMsgID)
		return err
	}
	chat.localExpertAuthoring = opts.source != routeInputSourceGroupWatch && b.isBotOwner(openID)
	b.recordUserRequestContext(ctx, chat, msgID, openID, routedText)
	input := routedText
	var contextPackage *threadcontext.ContextPackage
	if mention.AmuxDelegation {
		input = routedText
	} else if !usedRootRequest {
		input, contextPackage = b.withThreadContext(ctx, chat, msgID, routedText, ws)
		if rootText != "" {
			input, chat.topicRootPromptPending = b.ensureTopicRootInInput(rootText, routedText, input)
		}
	} else {
		chat.topicRootPromptPending = rootText != ""
	}
	if quoteSuppliesRoot {
		chat.topicRootPromptPending = true
	}
	input, err = b.renderFeishuQuotedMessage(input, quoted, quotedPresent)
	if err != nil {
		log.Warn().Err(err).Str("message_id", msgID).Msg("bridge: render quoted Feishu message")
		return b.notifyThread(ctx, rootMsgID, quotedMessageUnavailableText())
	}
	input, err = b.withBotHandoffInstructions(chat.RootMsgID, msgID, openID, input)
	if err != nil {
		return err
	}
	b.recordThreadContextDecision(ctx, contextPackage, input)
	notificationOpenID := openID
	if opts.source == routeInputSourceGroupWatch || mention.ExternalBot {
		notificationOpenID = ""
	}
	if opts.alertDedup != nil {
		if err := b.db.StartGroupWatchAlertSubmission(*opts.alertDedup, claimMessageID, time.Now()); err != nil {
			return err
		}
	}
	if _, err := b.submitToSessionForMessages(ctx, chat, input, compactTurnInputRefs(notificationOpenID, msgID)); err != nil {
		if notifyErr := b.notifyThread(ctx, rootMsgID, taskSubmitFailureText()); notifyErr != nil {
			return notifyErr
		}
		return err
	}
	if opts.alertDedup != nil {
		if finishErr := b.db.FinishGroupWatchAlert(*opts.alertDedup, claimMessageID, true, alertAcceptedAt); finishErr != nil {
			return finishErr
		}
	}
	if b.canAddReaction() && msgID != "" {
		b.addReactionAsync(msgID, "OneSecond")
	}
	return nil
}
func (b *Bridge) stripCurrentBotMention(text string, mention botMentionInfo) string {
	if mention.PreserveText {
		return strings.TrimSpace(text)
	}
	if mention.Mentioned && len(mention.Tokens) > 0 {
		return strings.TrimSpace(stripMentionTokens(text, mention.Tokens))
	}
	return strings.TrimSpace(stripMention(text))
}
func naturalEntrySessionName(bridgeID, chatID, rootMsgID string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(bridgeID) + "\n" + strings.TrimSpace(chatID) + "\n" + strings.TrimSpace(rootMsgID)))
	return fmt.Sprintf("feishu-%x", sum[:])[:19]
}
func displayTitleFromRequest(text string) string {
	return truncateForNotice(strings.TrimSpace(text), 48)
}
func slashCommandText(text string) (string, bool) {
	text = strings.TrimSpace(stripMention(text))
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "/"))
	return text, text != ""
}
func (b *Bridge) enforceRunQuota(ctx context.Context, rootMsgID, chatID, openID string, ws *FeishuWorkspace) (bool, error) {
	if b == nil || b.db == nil || ws == nil || ws.Mode != WorkspaceModeShared {
		return false, nil
	}
	blocked, err := b.enforceWorkspaceTotalQuota(ctx, rootMsgID, chatID, ws)
	if err != nil || blocked {
		return blocked, err
	}
	if ws.MaxSessionsPerUser > 0 {
		total, err := b.db.CountActiveBindingsByChatIDAndOpenId(b.currentBridgeId(), chatID, openID)
		if err != nil {
			return false, err
		}
		if total >= ws.MaxSessionsPerUser {
			return true, b.notifyThread(ctx, rootMsgID, fmt.Sprintf("你的 session 数量已达上限（%d）。请先 /stop 或 /unbind 旧 session。", ws.MaxSessionsPerUser))
		}
	}
	return false, nil
}
func (b *Bridge) enforceWorkspaceTotalQuota(ctx context.Context, rootMsgID, chatID string, ws *FeishuWorkspace) (bool, error) {
	if b == nil || b.db == nil || ws == nil || ws.MaxSessionsTotal <= 0 {
		return false, nil
	}
	bindings, err := b.db.ListWorkspaceBindingsByChatID(b.currentBridgeId(), chatID)
	if err != nil {
		return false, err
	}
	if len(bindings) < ws.MaxSessionsTotal {
		return false, nil
	}
	sessionsByTarget := make(map[string]map[string]string)
	countedSessions := make(map[string]struct{})
	total := 0
	for _, binding := range bindings {
		states := sessionsByTarget[binding.TargetID]
		if states == nil {
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			resp, routeErr := b.hub.Route(probeCtx, binding.TargetID, "List", &pb.ListRequest{})
			cancel()
			if routeErr != nil {
				log.Warn().Err(routeErr).Str("chat_id", chatID).Str("target_id", binding.TargetID).Msg("bridge: workspace quota probe failed")
				return true, b.notifyThread(ctx, rootMsgID, "暂时无法确认 workspace Session 容量，已停止创建；请稍后重试。")
			}
			var list pb.ListResponse
			if err := json.Unmarshal(resp.Payload, &list); err != nil {
				log.Warn().Err(err).Str("chat_id", chatID).Str("target_id", binding.TargetID).Msg("bridge: workspace quota response invalid")
				return true, b.notifyThread(ctx, rootMsgID, "暂时无法确认 workspace Session 容量，已停止创建；请稍后重试。")
			}
			states = make(map[string]string, len(list.Sessions))
			for _, item := range list.Sessions {
				if item != nil {
					states[item.Name] = item.State
				}
			}
			sessionsByTarget[binding.TargetID] = states
		}
		state, found := states[binding.SessionName]
		if !found || session.State(state) == session.StateArchived {
			if err := b.db.TerminateChat(b.currentBridgeId(), binding.RootMsgID); err != nil {
				return false, err
			}
			continue
		}
		sessionKey := binding.TargetID + "\x00" + binding.SessionName
		if _, counted := countedSessions[sessionKey]; counted {
			continue
		}
		countedSessions[sessionKey] = struct{}{}
		total++
	}
	if total >= ws.MaxSessionsTotal {
		return true, b.notifyThread(ctx, rootMsgID, fmt.Sprintf("workspace session 数量已达上限（%d）。请先 /stop 旧 session，或等待其归档。", ws.MaxSessionsTotal))
	}
	return false, nil
}
func (b *Bridge) resolveRunTarget(ref string) (resolvedBridgeTarget, string) {
	if strings.TrimSpace(ref) != "" {
		target, ok := b.resolveTarget(ref)
		if !ok {
			return resolvedBridgeTarget{}, fmt.Sprintf("找不到唯一 target: %s", ref)
		}
		return target, ""
	}
	targets := b.hub.ListTargets()
	switch len(targets) {
	case 0:
		return resolvedBridgeTarget{}, "暂无可用 target。请先启动 amux runtime。"
	case 1:
		return resolvedBridgeTarget{ID: targets[0].Id, Name: targets[0].Name}, ""
	default:
		names := make([]string, 0, len(targets))
		for _, target := range targets {
			name := target.Name
			if name == "" {
				name = target.Id
			}
			names = append(names, name)
		}
		return resolvedBridgeTarget{}, fmt.Sprintf("有多个 target，请使用 --on <target> 指定：%s", strings.Join(names, ", "))
	}
}
func (b *Bridge) feishuWorkspaceScope(chatID, openID string) string {
	if strings.TrimSpace(chatID) != "" {
		return strings.TrimSpace(chatID)
	}
	if openID != "" {
		return openID
	}
	return "default"
}
func (c *inputRouter) lockRoute(rootMsgID string) func() {
	c.routeMu.Lock()
	if c.routeLocks == nil {
		c.routeLocks = make(map[string]*sync.Mutex)
	}
	l := c.routeLocks[rootMsgID]
	if l == nil {
		l = &sync.Mutex{}
		c.routeLocks[rootMsgID] = l
	}
	c.routeMu.Unlock()
	l.Lock()
	return l.Unlock
}
