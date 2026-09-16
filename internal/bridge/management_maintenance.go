package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// managementMaintenanceState owns in-flight system-maintenance confirmation state.
type managementMaintenanceState struct {
	maintenanceMu             sync.Mutex
	maintenanceActive         string
	maintenanceConfirmations  map[string]bool
	managementRecoveryRunning bool
}

func (c *managementMaintenanceState) beginManagementMaintenance(action string) bool {
	if c == nil {
		return false
	}
	c.maintenanceMu.Lock()
	defer c.maintenanceMu.Unlock()
	if c.maintenanceActive != "" {
		return false
	}
	c.maintenanceActive = action
	return true
}
func (c *managementMaintenanceState) endManagementMaintenance(action string) {
	if c == nil {
		return
	}
	c.maintenanceMu.Lock()
	defer c.maintenanceMu.Unlock()
	if c.maintenanceActive == action {
		c.maintenanceActive = ""
	}
}
func (c *managementMaintenanceState) consumeManagementRestartConfirmation(state managementState, systemInstance, nonce string) bool {
	systemInstance = strings.TrimSpace(systemInstance)
	nonce = strings.TrimSpace(nonce)
	if systemInstance == "" || nonce == "" || systemInstance != managementSystemInstanceID(state) {
		return false
	}
	key := systemInstance + ":" + nonce
	c.maintenanceMu.Lock()
	defer c.maintenanceMu.Unlock()
	if c.maintenanceConfirmations == nil {
		c.maintenanceConfirmations = make(map[string]bool)
	}
	if c.maintenanceConfirmations[key] {
		return false
	}
	c.maintenanceConfirmations[key] = true
	return true
}
func (c *managementMaintenanceState) consumeManagementUpgradeConfirmation(state managementState, systemInstance, expectedVersion, nonce string) bool {
	systemInstance = strings.TrimSpace(systemInstance)
	expectedVersion = strings.TrimSpace(expectedVersion)
	nonce = strings.TrimSpace(nonce)
	if systemInstance == "" || expectedVersion == "" || nonce == "" || systemInstance != managementSystemInstanceID(state) {
		return false
	}
	key := managementRecoveryUpgrade + ":" + systemInstance + ":" + expectedVersion + ":" + nonce
	c.maintenanceMu.Lock()
	defer c.maintenanceMu.Unlock()
	if c.maintenanceConfirmations == nil {
		c.maintenanceConfirmations = make(map[string]bool)
	}
	if c.maintenanceConfirmations[key] {
		return false
	}
	c.maintenanceConfirmations[key] = true
	return true
}
func (b *Bridge) runManagementSystemRestart(openID, messageID string, surface managementSurface) {
	go func() {
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		card := renderFocusedManagementCardForSurface(renderSystemRestartingCard(), surface)
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, card)
		patchCancel()
		delay := b.managementRestartDelay
		if delay <= 0 {
			delay = 1500 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		if b.managementActions == nil {
			b.endManagementMaintenance("restart")
			b.clearPendingManagementRecovery(messageID)
			b.patchManagementMaintenanceFailure(openID, messageID, "暂时无法重启 amux，请在部署机器上重试。", managementActionSystemRestart, "重试重启", surface)
			return
		}
		restartCtx, restartCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := b.managementActions.Restart(restartCtx)
		restartCancel()
		if err == nil {
			time.AfterFunc(30*time.Second, func() {
				b.endManagementMaintenance("restart")
			})
			return
		}
		b.endManagementMaintenance("restart")
		b.clearPendingManagementRecovery(messageID)
		log.Warn().Err(err).Msg("bridge: failed to start management system restart")
		b.patchManagementMaintenanceFailure(openID, messageID, "未能启动重启，请稍后重试。", managementActionSystemRestart, "重试重启", surface)
	}()
}
func (b *Bridge) runManagementSystemUpgrade(openID, messageID, expectedVersion string, surface managementSurface) {
	go func() {
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		card := renderFocusedManagementCardForSurface(renderSystemUpgradingCard(expectedVersion), surface)
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, card)
		patchCancel()
		delay := b.managementRestartDelay
		if delay <= 0 {
			delay = 1500 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		if b.managementActions == nil {
			b.endManagementMaintenance("upgrade")
			b.clearPendingManagementRecovery(messageID)
			b.patchManagementMaintenanceFailure(openID, messageID, "暂时无法升级 amux，请在部署机器上重试。", managementActionSystemUpdate, "重新检查更新", surface)
			return
		}
		upgradeCtx, upgradeCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		err := b.managementActions.Upgrade(upgradeCtx, expectedVersion)
		upgradeCancel()
		if err == nil {
			time.AfterFunc(30*time.Second, func() {
				b.endManagementMaintenance("upgrade")
			})
			return
		}
		b.endManagementMaintenance("upgrade")
		b.clearPendingManagementRecovery(messageID)
		log.Warn().Err(err).Str("expected_version", expectedVersion).Msg("bridge: management upgrade failed")
		b.patchManagementMaintenanceFailure(openID, messageID, "升级没有完成，请重新检查可用版本后再试。", managementActionSystemUpdate, "重新检查更新", surface)
	}()
}
func (b *Bridge) clearPendingManagementRecovery(messageID string) {
	if b == nil || b.db == nil {
		return
	}
	if err := b.db.DeletePendingManagementRecovery(b.currentBridgeId(), messageID); err != nil {
		log.Warn().Err(err).Str("message_id", messageID).Msg("bridge: clear pending management recovery")
	}
}
func (b *Bridge) startPendingManagementRecovery(ctx context.Context) {
	if b == nil || b.db == nil {
		return
	}
	b.maintenanceMu.Lock()
	if b.managementRecoveryRunning {
		b.maintenanceMu.Unlock()
		return
	}
	b.managementRecoveryRunning = true
	b.maintenanceMu.Unlock()
	go func() {
		defer func() {
			b.maintenanceMu.Lock()
			b.managementRecoveryRunning = false
			b.maintenanceMu.Unlock()
		}()
		deadline := time.Now().Add(managementRecoveryWait)
		for {
			allowAttention := !time.Now().Before(deadline)
			done, err := b.recoverPendingManagementCardOnce(ctx, allowAttention)
			if err != nil {
				log.Warn().Err(err).Msg("bridge: recover pending management card")
			}
			if done || allowAttention {
				return
			}
			wait := min(managementRecoveryPollInterval, time.Until(deadline))
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
func (b *Bridge) recoverPendingManagementCardOnce(ctx context.Context, allowAttention bool) (bool, error) {
	if b == nil || b.db == nil {
		return true, nil
	}
	pending, err := b.db.GetPendingManagementRecovery(b.currentBridgeId())
	if err != nil {
		return false, err
	}
	if pending == nil {
		return true, nil
	}
	if time.Since(pending.CreatedAt) > managementRecoveryTTL {
		return true, b.db.DeletePendingManagementRecovery(pending.BridgeID, pending.MessageID)
	}
	state := b.managementState()
	if state.SystemStatus != managementSystemHealthy && !allowAttention {
		return false, nil
	}
	cardJSON := renderSystemRecoveredCard(state)
	if pending.Operation == managementRecoveryUpgrade {
		cardJSON = renderSystemUpgradeRecoveredCard(state, pending.ExpectedVersion)
	} else if state.SystemStatus != managementSystemHealthy {
		cardJSON = renderSystemRecoveryAttentionCard(state)
	}
	cardJSON = renderManagementCardForSurface(cardJSON, normalizeManagementSurface(pending.Surface), managementViewSystem)
	deliverCtx, deliverCancel := context.WithTimeout(ctx, 10*time.Second)
	delivered := b.deliverManagementCard(deliverCtx, pending.OpenID, pending.MessageID, cardJSON)
	deliverCancel()
	if !delivered {
		return false, nil
	}
	if err := b.db.DeletePendingManagementRecovery(pending.BridgeID, pending.MessageID); err != nil {
		return false, err
	}
	return true, nil
}
func (b *Bridge) runManagementUpdateCheck(openID, messageID string, state managementState, surface managementSurface) {
	go func() {
		defer b.endManagementMaintenance("update_check")
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		checkingCard := renderFocusedManagementCardForSurface(renderSystemUpdateCheckingCard(state), surface)
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, checkingCard)
		patchCancel()
		if b.managementActions == nil {
			b.patchManagementMaintenanceFailure(openID, messageID, "暂时无法检查更新，请在部署机器上重试。", managementActionSystemUpdate, "重试检查更新", surface)
			return
		}
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 60*time.Second)
		result, err := b.managementActions.CheckUpdate(checkCtx)
		checkCancel()
		if err != nil {
			log.Warn().Err(err).Msg("bridge: management update check failed")
			b.patchManagementMaintenanceFailure(openID, messageID, "检查更新失败，请稍后重试。", managementActionSystemUpdate, "重试检查更新", surface)
			return
		}
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer finalCancel()
		resultCard := renderManagementCardForSurface(renderSystemUpdateResultCard(state, result), surface, managementViewSystem)
		b.writeManagementCardFromCallback(finalCtx, openID, messageID, resultCard)
	}()
}
func (b *Bridge) patchManagementMaintenanceFailure(openID, messageID, message, retryAction, retryText string, surface managementSurface) {
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finalCancel()
	card := renderFocusedManagementCardForSurface(renderSystemMaintenanceFailureCard(message, retryAction, retryText), surface)
	b.writeManagementCardFromCallback(finalCtx, openID, messageID, card)
}
func (b *Bridge) updateManagementCardFromCallback(openID, messageID, cardJSON string) {
	if strings.TrimSpace(openID) == "" || strings.TrimSpace(cardJSON) == "" {
		return
	}
	if b == nil {
		return
	}
	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.writeManagementCardFromCallback(sendCtx, openID, messageID, cardJSON)
	}()
}
func (b *Bridge) writeManagementCardFromCallback(ctx context.Context, openID, messageID, cardJSON string) {
	b.writeManagementCardFromCallbackIf(ctx, openID, messageID, cardJSON, nil)
}
func (b *Bridge) writeManagementCardFromCallbackIf(ctx context.Context, openID, messageID, cardJSON string, current func() bool) {
	unlock := b.lockRoute("management-card\x00" + strings.TrimSpace(messageID))
	defer unlock()
	if current != nil && !current() {
		return
	}
	_ = b.deliverManagementCard(ctx, openID, messageID, cardJSON)
}
func (b *Bridge) writeManagementCardFromCallbackWithFallback(ctx context.Context, openID, messageID, cardJSON, fallbackCardJSON string) {
	attempted, err := b.patchManagementCard(ctx, messageID, cardJSON)
	if attempted && err == nil {
		return
	}
	if err != nil {
		log.Warn().Err(err).Str("message_id", messageID).Msg("bridge: failed to patch management callback card")
	}
	if strings.TrimSpace(fallbackCardJSON) == "" {
		return
	}
	fallbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if fallbackCardJSON == cardJSON {
		if b.dmInteractive == nil && b.client == nil {
			return
		}
		if err := b.sendManagementCardToDM(fallbackCtx, openID, fallbackCardJSON); err != nil {
			log.Warn().Err(err).Str("open_id", openID).Msg("bridge: failed to send management callback fallback card")
		}
		return
	}
	_ = b.deliverManagementCard(fallbackCtx, openID, messageID, fallbackCardJSON)
}
func (b *Bridge) deliverManagementCard(ctx context.Context, openID, messageID, cardJSON string) bool {
	canSend := b.dmInteractive != nil || b.client != nil
	attempted, err := b.patchManagementCard(ctx, messageID, cardJSON)
	if !attempted && !canSend {
		return false
	}
	if attempted {
		if err == nil {
			return true
		}
		log.Warn().Err(err).Str("message_id", messageID).Msg("bridge: failed to patch management callback card")
	}
	if !canSend {
		return false
	}
	if err := b.sendManagementCardToDM(ctx, openID, cardJSON); err != nil {
		log.Warn().Err(err).Str("open_id", openID).Msg("bridge: failed to send management callback card")
		return false
	}
	return true
}
func (b *Bridge) patchManagementCard(ctx context.Context, messageID, cardJSON string) (bool, error) {
	if strings.TrimSpace(messageID) == "" || (b.managementCardPatch == nil && b.client == nil) {
		return false, nil
	}
	if b.managementCardPatch != nil {
		return true, b.managementCardPatch(ctx, messageID, cardJSON)
	}
	return true, b.patchInteractiveMessage(ctx, messageID, cardJSON, "management card")
}
func renderSystemStatusCard(state managementState) string {
	draft := managementDraftFromState(state)
	if state.SystemStatus != managementSystemHealthy {
		return marshalNativeManagementCard(managementCardView{Title: "助手状态", Subtitle: "暂时无法开始新任务", Summary: "amux 需要关注", Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementHintElement("当前没有可用的运行环境。请重新检查；仍未恢复时查看系统状态。"), managementHintElement(managementWorkspaceSummary(state))}, Buttons: []managementButton{{Text: "重新检查", Type: "primary", Action: managementActionStatusOpen, Icon: managementIconPtr("refresh_outlined", "blue")}, {Text: "系统状态", Type: "default", Action: managementActionSystemMaintenance, Icon: managementIconPtr("setting_outlined", "grey")}}})
	}
	elements := []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementMessageHandlingSummary, Body: managementModeSummary(draft)}, managementSummaryItem{Title: "默认专家", Body: managementDefaultAgentName(state)}), managementHintElement(managementWorkspaceSummary(state))}
	buttons := []managementButton{{Text: "助手设置", Type: "primary", Action: managementActionSettingsOpen, Icon: managementIconPtr("setting_outlined", "blue")}, {Text: "系统状态", Type: "default", Action: managementActionSystemMaintenance}}
	if state.SystemStatus == managementSystemHealthy && state.Update.ShouldPrompt {
		elements = append(elements, managementUpdateNoticeElement(state.Update))
		buttons[0].Type = "default"
		buttons[1].Text = "查看更新"
		buttons[1].Type = "primary"
	}
	return marshalNativeManagementCard(managementCardView{Title: "助手状态", Subtitle: "运行正常，可直接发送任务", Summary: "amux 运行正常", Theme: "green", Icon: managementIcon("robot_outlined", "green"), Elements: elements, Buttons: buttons})
}
func renderSystemMaintenanceCardForSurface(state managementState, surface managementSurface) string {
	subtitle := "需要关注"
	theme := "orange"
	iconColor := "orange"
	if state.SystemStatus == managementSystemHealthy {
		subtitle = "运行正常"
		theme = "green"
		iconColor = "green"
	}
	restartValues := map[string]string{"system_instance": managementSystemInstanceID(state), "confirmation_nonce": newManagementConfirmationNonce()}
	buttons := []managementButton{{Text: "检查更新", Type: "primary", Action: managementActionSystemUpdate, Icon: managementIconPtr("version_outlined", "blue")}, {Text: "重启 amux", Type: "default", Action: managementActionSystemRestartOK, Values: restartValues, Icon: managementIconPtr("refresh_outlined", "grey"), Confirm: managementConfirm("确认重启 amux？", "飞书智能体会短暂离线，现有会话不会删除。")}}
	elements := []cardElement{managementMaintenanceDetailsElement(state)}
	if state.SystemStatus == managementSystemHealthy && state.Update.ShouldPrompt {
		elements = append(elements, managementUpdateNoticeElement(state.Update))
		buttons[0].Text = "查看更新"
		buttons = []managementButton{buttons[0], {Text: "本版本不再提示", Type: "default", Action: managementActionSystemUpdateDismiss, Values: map[string]string{"version": state.Update.LatestVersion}}, buttons[1]}
	}
	if state.SystemStatus != managementSystemHealthy {
		elements = append(elements, managementHintElement("当前没有可用的运行环境。请先重新检查；仍未恢复时再重启 amux。"))
		buttons = []managementButton{{Text: "重新检查", Type: "primary", Action: managementActionSystemMaintenance, Icon: managementIconPtr("refresh_outlined", "blue")}, buttons[1]}
	}
	if surface != managementSurfaceAmux {
		buttons = append(buttons, managementButton{Text: "返回概览", Type: "default", Action: managementActionStatusOpen})
	}
	return marshalNativeManagementCard(managementCardView{Title: "系统状态", Subtitle: subtitle, Summary: "amux 系统状态", Theme: theme, Icon: managementIcon("setting_outlined", iconColor), Elements: elements, Buttons: buttons})
}
func renderSystemRestartConfirmationCard(state managementState) string {
	values := map[string]string{"system_instance": managementSystemInstanceID(state), "confirmation_nonce": newManagementConfirmationNonce()}
	return marshalNativeManagementCard(managementCardView{Title: "确认重启", Subtitle: "飞书智能体会短暂离线", Summary: "确认重启 amux", Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementHintElement("现有会话不会删除。预计数秒恢复，恢复后可发送 /amux 查看状态。")}, Buttons: []managementButton{{Text: "确认重启", Type: "primary", Action: managementActionSystemRestartOK, Values: values}, {Text: "取消", Type: "default", Action: managementActionSystemMaintenance}}})
}
func managementSystemInstanceID(state managementState) string {
	if state.SystemInfo.StartedAt.IsZero() {
		return ""
	}
	return state.SystemInfo.StartedAt.UTC().Format(time.RFC3339Nano)
}
func newManagementConfirmationNonce() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
func renderSystemRestartingCard() string {
	return marshalNativeManagementCard(managementCardView{Title: "正在重启 amux", Subtitle: "飞书智能体会短暂离线", Summary: "amux 正在重启", Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementHintElement("恢复后可发送 /amux 查看状态。")}})
}
func renderSystemRecoveredCard(state managementState) string {
	draft := managementDraftFromState(state)
	return marshalNativeManagementCard(managementCardView{Title: "amux 已恢复", Subtitle: "运行正常，可继续使用", Summary: "amux 重启完成", Theme: "green", Icon: managementIcon("check_outlined", "green"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementMessageHandlingSummary, Body: managementModeSummary(draft)}, managementSummaryItem{Title: "默认专家", Body: managementDefaultAgentName(state)})}, Buttons: []managementButton{{Text: "助手设置", Type: "primary", Action: managementActionSettingsOpen, Icon: managementIconPtr("setting_outlined", "blue")}, {Text: "系统状态", Type: "default", Action: managementActionSystemMaintenance}}})
}
func renderSystemRecoveryAttentionCard(state managementState) string {
	return marshalNativeManagementCard(managementCardView{Title: "amux 已重启", Subtitle: "运行环境仍需关注", Summary: "amux 重启后需要关注", Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementHintElement("仍没有可用的运行环境，请稍后重新检查。")}, Buttons: []managementButton{{Text: "重新检查", Type: "primary", Action: managementActionStatusOpen, Icon: managementIconPtr("refresh_outlined", "blue")}, {Text: "系统状态", Type: "default", Action: managementActionSystemMaintenance}}})
}
func renderSystemUpdateCheckingCard(state managementState) string {
	return marshalNativeManagementCard(managementCardView{Title: "正在检查更新", Subtitle: "不会自动安装", Summary: "amux 正在检查更新", Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "当前版本", Body: valueOrPlaceholder(state.SystemInfo.Version, "未知")})}})
}
func renderSystemUpdateResultCard(state managementState, result ManagementUpdateInfo) string {
	status := "当前已是最新版本"
	if result.UpdateAvailable {
		status = "发现可用版本"
	}
	theme := "green"
	iconColor := "green"
	if result.UpdateAvailable {
		theme = "orange"
		iconColor = "orange"
	}
	buttons := []managementButton{{Text: "返回系统状态", Type: "default", Action: managementActionSystemMaintenance}}
	hint := "当前已是最新版本。"
	if result.UpdateAvailable {
		hint = "确认后才会安装新版本并重启 amux。"
		buttons = []managementButton{{Text: "升级到 " + result.LatestVersion, Type: "primary", Action: managementActionSystemUpgrade, Values: map[string]string{"system_instance": managementSystemInstanceID(state), "confirmation_nonce": newManagementConfirmationNonce(), "expected_version": result.LatestVersion}, Icon: managementIconPtr("version_outlined", "blue"), Confirm: managementConfirm("确认升级 amux？", "将安装 "+result.LatestVersion+" 并重启 amux，飞书智能体会短暂离线。")}, {Text: "本版本不再提示", Type: "default", Action: managementActionSystemUpdateDismiss, Values: map[string]string{"version": result.LatestVersion}}, {Text: "返回系统状态", Type: "default", Action: managementActionSystemMaintenance}}
	}
	return marshalNativeManagementCard(managementCardView{Title: "更新检查", Subtitle: status, Summary: "amux 更新检查结果", Theme: theme, Icon: managementIcon("version_outlined", iconColor), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "当前版本", Body: valueOrPlaceholder(result.CurrentVersion, "未知")}, managementSummaryItem{Title: "可用版本", Body: valueOrPlaceholder(result.LatestVersion, "未知")}), managementHintElement(hint)}, Buttons: buttons})
}
func renderSystemUpgradingCard(expectedVersion string) string {
	return marshalNativeManagementCard(managementCardView{Title: "正在升级 amux", Subtitle: "飞书智能体会短暂离线", Summary: "amux 正在升级", Theme: "blue", Icon: managementIcon("version_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "目标版本", Body: valueOrPlaceholder(expectedVersion, "未知")}), managementHintElement("恢复连接后自动刷新。")}})
}
func renderSystemUpgradeRecoveredCard(state managementState, expectedVersion string) string {
	currentVersion := strings.TrimSpace(state.SystemInfo.Version)
	expectedVersion = strings.TrimSpace(expectedVersion)
	if state.SystemStatus != managementSystemHealthy || currentVersion == "" || currentVersion != expectedVersion {
		subtitle := "amux 已重新连接，但版本需要确认"
		hint := "当前版本与目标版本不一致，请重新检查更新。"
		if state.SystemStatus != managementSystemHealthy {
			subtitle = "amux 已重新连接，运行环境仍需关注"
			hint = "升级后暂时没有可用的运行环境，可以稍后重新检查。"
		}
		return marshalNativeManagementCard(managementCardView{Title: "更新结果待确认", Subtitle: subtitle, Summary: "amux 更新结果需要确认", Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "当前版本", Body: valueOrPlaceholder(currentVersion, "未知")}, managementSummaryItem{Title: "目标版本", Body: valueOrPlaceholder(expectedVersion, "未知")}), managementHintElement(hint)}, Buttons: []managementButton{{Text: "重新检查更新", Type: "primary", Action: managementActionSystemUpdate, Icon: managementIconPtr("refresh_outlined", "blue")}, {Text: "返回概览", Type: "default", Action: managementActionStatusOpen}}})
	}
	return marshalNativeManagementCard(managementCardView{Title: "amux 升级完成", Subtitle: "运行正常，可继续使用", Summary: "amux 升级完成", Theme: "green", Icon: managementIcon("check_outlined", "green"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "当前版本", Body: currentVersion}, managementSummaryItem{Title: "升级状态", Body: "已完成"})}, Buttons: []managementButton{{Text: "返回概览", Type: "primary", Action: managementActionStatusOpen}, {Text: "系统状态", Type: "default", Action: managementActionSystemMaintenance}}})
}
func renderSystemMaintenanceFailureCard(message, retryAction, retryText string) string {
	return marshalNativeManagementCard(managementCardView{Title: "操作失败", Subtitle: "系统状态未更改", Summary: "amux 系统维护失败", Theme: "red", Icon: managementIcon("warning_outlined", "red"), Elements: []cardElement{managementHintElement(message)}, Buttons: []managementButton{{Text: retryText, Type: "primary", Action: retryAction}, {Text: "返回系统状态", Type: "default", Action: managementActionSystemMaintenance}}})
}
