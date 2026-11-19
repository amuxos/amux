package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
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
			b.patchManagementMaintenanceFailure(openID, messageID, managementCopy("management.cannot.restart.amux.right.now.retry.on.the.deployment"), managementActionSystemRestart, managementCopy("management.retry.restart"), surface)
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
		b.patchManagementMaintenanceFailure(openID, messageID, managementCopy("management.restart.could.not.be.started.try.again.later"), managementActionSystemRestart, managementCopy("management.retry.restart"), surface)
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
			b.patchManagementMaintenanceFailure(openID, messageID, managementCopy("management.cannot.upgrade.amux.right.now.retry.on.the.deployment"), managementActionSystemUpdate, managementCopy("management.check.for.updates.again"), surface)
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
		b.patchManagementMaintenanceFailure(openID, messageID, managementCopy("management.upgrade.did.not.finish.check.available.versions.and.try"), managementActionSystemUpdate, managementCopy("management.check.for.updates.again"), surface)
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

func (b *Bridge) runManagementUpdateCheck(openID, messageID string, state managementState, surface managementSurface, version uint64) {
	go func() {
		defer b.endManagementMaintenance("update_check")
		current := func() bool { return b.modelServiceEditorCurrent(messageID, version) }
		write := func(ctx context.Context, card string) {
			b.writeManagementCardFromCallbackIf(ctx, openID, messageID, card, current)
		}
		fail := func(message any) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			card := renderFocusedManagementCardForSurface(renderSystemMaintenanceFailureCard(message, managementActionSystemUpdate, managementCopy("management.retry.update.check")), surface)
			write(ctx, card)
		}
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		checkingCard := renderFocusedManagementCardForSurface(renderSystemUpdateCheckingCard(state), surface)
		write(patchCtx, checkingCard)
		patchCancel()
		if b.managementActions == nil {
			fail(managementCopy("management.cannot.check.for.updates.right.now.retry.on.the"))
			return
		}
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 60*time.Second)
		result, err := b.managementActions.CheckUpdate(checkCtx)
		checkCancel()
		if err != nil {
			log.Warn().Err(err).Msg("bridge: management update check failed")
			fail(managementCopy("management.update.check.failed.try.again.later"))
			return
		}
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer finalCancel()
		resultCard := renderManagementCardForSurface(renderSystemUpdateResultCard(state, result), surface, managementViewSystem)
		write(finalCtx, resultCard)
	}()
}

func (b *Bridge) patchManagementMaintenanceFailure(openID, messageID string, message any, retryAction string, retryText any, surface managementSurface) {
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
	b.settingsMu.Lock()
	version := b.modelServiceVersions[messageID]
	b.settingsMu.Unlock()
	b.updateManagementCardAtVersion(openID, messageID, cardJSON, version)
}

func (b *Bridge) managementCardResponse(openID, messageID, cardJSON string, version uint64, toast *callback.CardActionTriggerResponse) *callback.CardActionTriggerResponse {
	resp := &callback.CardActionTriggerResponse{}
	if toast != nil {
		resp.Toast = toast.Toast
	}
	if b != nil {
		localized := b.managementCardForPatch(openID, messageID, cardJSON)
		if strings.TrimSpace(localized) != "" {
			resp.Card = &callback.Card{Type: "raw", Data: json.RawMessage(localized)}
		}
		if b.managementCardPatch != nil {
			b.updateManagementCardAtVersion(openID, messageID, cardJSON, version)
		}
	}
	return resp
}

func (b *Bridge) updateManagementCardAtVersion(openID, messageID, card string, version uint64) {
	go b.writeManagementCardAtVersion(context.Background(), openID, messageID, card, version)
}

// Write in the editor goroutine so its loading and completed views cannot race
// each other. Recheck after acquiring the per-card delivery lock.
func (b *Bridge) writeManagementCardAtVersion(parent context.Context, openID, messageID, card string, version uint64) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	b.writeManagementCardFromCallbackIf(ctx, openID, messageID, card, func() bool {
		return b.modelServiceEditorCurrent(messageID, version)
	})
}

func (b *Bridge) writeManagementCardFromCallback(ctx context.Context, openID, messageID, cardJSON string) {
	b.writeManagementCardFromCallbackIf(ctx, openID, messageID, cardJSON, nil)
}

func (b *Bridge) writeManagementCardFromCallbackIf(ctx context.Context, openID, messageID, cardJSON string, current func() bool) {
	queued := time.Now()
	unlock, err := b.lockGroupSettingsRouteContext(ctx, "management-card\x00"+strings.TrimSpace(messageID))
	if err != nil {
		log.Warn().Err(err).Str("message_id", messageID).Msg("bridge: management card delivery lock timed out")
		return
	}
	defer unlock()
	waited := time.Since(queued)
	if current != nil && !current() {
		return
	}
	started := time.Now()
	delivered := b.deliverManagementCardIf(ctx, openID, messageID, cardJSON, current)
	log.Debug().Str("message_id", messageID).Dur("queue_ms", waited).Dur("delivery_ms", time.Since(started)).Bool("delivered", delivered).Msg("bridge: management card delivery completed")
}

func (b *Bridge) writeManagementCardFromCallbackWithFallback(ctx context.Context, openID, messageID, cardJSON, fallbackCardJSON string) {
	attempted, err := b.patchManagementCard(ctx, messageID, b.managementCardForPatch(openID, messageID, cardJSON))
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
	return b.deliverManagementCardIf(ctx, openID, messageID, cardJSON, nil)
}

func (b *Bridge) deliverManagementCardIf(ctx context.Context, openID, messageID, cardJSON string, current func() bool) bool {
	canSend := b.dmInteractive != nil || b.client != nil
	attempted, err := b.patchManagementCard(ctx, messageID, b.managementCardForPatch(openID, messageID, cardJSON))
	if !attempted && !canSend {
		return false
	}
	if attempted {
		if err == nil {
			return true
		}
		log.Warn().Err(err).Str("message_id", messageID).Msg("bridge: failed to patch management callback card")
	}
	if !canSend || (current != nil && !current()) {
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
		return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.assistant.status"), Subtitle: managementCopy("management.cannot.start.new.tasks.right.now"), Summary: managementCopy("management.amux.needs.attention"), Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementHintElement(managementCopy("management.no.runtimes.are.available.check.again.then.open.system")), managementHintElement(managementWorkspaceSummary(state))}, Buttons: []managementButton{{Text: managementCopy("management.check.again"), Type: "primary", Action: managementActionStatusOpen, Icon: managementIconPtr("refresh_outlined", "blue")}, {Text: managementCopy("management.system.status"), Type: "default", Action: managementActionSystemMaintenance, Icon: managementIconPtr("setting_outlined", "grey")}}})
	}
	elements := []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.message.handling.summary"), Body: managementModeSummary(draft)}, managementSummaryItem{Title: managementCopy("management.default.expert"), Body: managementDefaultAgentName(state)}), managementHintElement(managementWorkspaceSummary(state))}
	buttons := []managementButton{{Text: managementCopy("management.assistant.settings"), Type: "primary", Action: managementActionSettingsOpen, Icon: managementIconPtr("setting_outlined", "blue")}, {Text: managementCopy("management.system.status"), Type: "default", Action: managementActionSystemMaintenance}}
	if state.SystemStatus == managementSystemHealthy && state.Update.ShouldPrompt {
		elements = append(elements, managementUpdateNoticeElement(state.Update))
		buttons[0].Type = "default"
		buttons[1].Text = managementCopy("management.view.update")
		buttons[1].Type = "primary"
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.assistant.status"), Subtitle: managementCopy("management.running.normally.send.a.task.to.begin"), Summary: managementCopy("management.amux.running.normally"), Theme: "green", Icon: managementIcon("robot_outlined", "green"), Elements: elements, Buttons: buttons})
}

func renderSystemMaintenanceCardForSurface(state managementState, surface managementSurface) string {
	subtitle := managementCopy("management.needs.attention")
	theme := "orange"
	iconToken := "warning_outlined"
	iconColor := "orange"
	tag := cardTextTag{Tag: "text_tag", Text: managementPlainText(managementCopy("management.status.tag.attention")), Color: "orange"}
	if state.SystemStatus == managementSystemHealthy {
		subtitle = managementCopy("management.running.normally")
		theme = "wathet"
		iconToken = "check_outlined"
		iconColor = "green"
		tag = cardTextTag{Tag: "text_tag", Text: managementPlainText(managementCopy("management.status.tag.healthy")), Color: "green"}
	}
	restartValues := map[string]string{"system_instance": managementSystemInstanceID(state), "confirmation_nonce": newManagementConfirmationNonce()}
	buttons := []managementButton{{Text: managementCopy("management.check.for.updates"), Type: "primary", Action: managementActionSystemUpdate, Icon: managementIconPtr("version_outlined", "blue")}, {Text: managementCopy("management.restart.amux"), Type: "default", Action: managementActionSystemRestartOK, Values: restartValues, Icon: managementIconPtr("refresh_outlined", "grey"), Confirm: managementConfirm(managementCopy("management.restart.amux.409"), managementCopy("management.the.feishu.assistant.will.briefly.go.offline.existing.sessions"))}}
	elements := []cardElement{managementMaintenanceDetailsElement(state)}
	if state.SystemStatus == managementSystemHealthy && state.Update.ShouldPrompt {
		elements = append(elements, managementUpdateNoticeElement(state.Update))
		buttons[0].Text = managementCopy("management.view.update")
		buttons = []managementButton{buttons[0], {Text: managementCopy("management.dismiss.this.version"), Type: "default", Action: managementActionSystemUpdateDismiss, Values: map[string]string{"version": state.Update.LatestVersion}}, buttons[1]}
	}
	if state.SystemStatus != managementSystemHealthy {
		elements = append(elements, managementHintElement(managementCopy("management.no.runtimes.are.available.check.again.before.restarting.amux")))
		buttons = []managementButton{{Text: managementCopy("management.check.again"), Type: "primary", Action: managementActionSystemMaintenance, Icon: managementIconPtr("refresh_outlined", "blue")}, buttons[1]}
	}
	if surface != managementSurfaceAmux {
		buttons = append(buttons, managementButton{Text: managementCopy("management.back.to.overview"), Type: "default", Action: managementActionStatusOpen})
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.system.status"), Subtitle: subtitle, Summary: managementCopy("management.amux.system.status"), Theme: theme, Icon: managementIcon(iconToken, iconColor), Tags: []cardTextTag{tag}, Elements: elements, Buttons: buttons})
}

func renderSystemRestartConfirmationCard(state managementState) string {
	values := map[string]string{"system_instance": managementSystemInstanceID(state), "confirmation_nonce": newManagementConfirmationNonce()}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.confirm.restart"), Subtitle: managementCopy("management.the.feishu.assistant.will.briefly.go.offline"), Summary: managementCopy("management.confirm.amux.restart"), Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementHintElement(managementCopy("management.existing.sessions.will.not.be.deleted.recovery.normally.takes"))}, Buttons: []managementButton{{Text: managementCopy("management.confirm.restart"), Type: "primary", Action: managementActionSystemRestartOK, Values: values}, {Text: managementCopy("management.cancel"), Type: "default", Action: managementActionSystemMaintenance}}})
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
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.restarting.amux"), Subtitle: managementCopy("management.the.feishu.assistant.will.briefly.go.offline"), Summary: managementCopy("management.amux.restarting"), Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementHintElement(managementCopy("management.send.amux.after.recovery.to.check.status"))}})
}

func renderSystemRecoveredCard(state managementState) string {
	draft := managementDraftFromState(state)
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.amux.recovered"), Subtitle: managementCopy("management.running.normally.ready.to.continue"), Summary: managementCopy("management.amux.restart.complete"), Theme: "green", Icon: managementIcon("check_outlined", "green"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.message.handling.summary"), Body: managementModeSummary(draft)}, managementSummaryItem{Title: managementCopy("management.default.expert"), Body: managementDefaultAgentName(state)})}, Buttons: []managementButton{{Text: managementCopy("management.assistant.settings"), Type: "primary", Action: managementActionSettingsOpen, Icon: managementIconPtr("setting_outlined", "blue")}, {Text: managementCopy("management.system.status"), Type: "default", Action: managementActionSystemMaintenance}}})
}

func renderSystemRecoveryAttentionCard(state managementState) string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.amux.restarted"), Subtitle: managementCopy("management.runtimes.still.need.attention"), Summary: managementCopy("management.amux.needs.attention.after.restart"), Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementHintElement(managementCopy("management.no.runtimes.are.available.yet.check.again.later"))}, Buttons: []managementButton{{Text: managementCopy("management.check.again"), Type: "primary", Action: managementActionStatusOpen, Icon: managementIconPtr("refresh_outlined", "blue")}, {Text: managementCopy("management.system.status"), Type: "default", Action: managementActionSystemMaintenance}}})
}

func renderSystemUpdateCheckingCard(state managementState) string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.checking.for.updates"), Subtitle: managementCopy("management.updates.will.not.install.automatically"), Summary: managementCopy("management.amux.checking.for.updates"), Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.current.version"), Body: managementValueOrPlaceholder(state.SystemInfo.Version, managementCopy("management.unknown"))})}})
}

func renderSystemUpdateResultCard(state managementState, result ManagementUpdateInfo) string {
	status := managementCopy("management.already.on.the.latest.version")
	if result.UpdateAvailable {
		status = managementCopy("management.an.update.is.available")
	}
	theme := "green"
	iconColor := "green"
	if result.UpdateAvailable {
		theme = "orange"
		iconColor = "orange"
	}
	buttons := []managementButton{{Text: managementCopy("management.back.to.system.status"), Type: "default", Action: managementActionSystemMaintenance}}
	hint := managementCopy("management.already.on.the.latest.version.432")
	if result.UpdateAvailable {
		hint = managementCopy("management.the.new.version.will.install.and.restart.amux.only")
		buttons = []managementButton{{Text: managementConcat(managementCopy("management.upgrade.to"), result.LatestVersion), Type: "primary", Action: managementActionSystemUpgrade, Values: map[string]string{"system_instance": managementSystemInstanceID(state), "confirmation_nonce": newManagementConfirmationNonce(), "expected_version": result.LatestVersion}, Icon: managementIconPtr("version_outlined", "blue"), Confirm: managementConfirm(managementCopy("management.upgrade.amux"), managementConcat(managementCopy("management.this.will.install"), result.LatestVersion, managementCopy("management.and.restart.amux.the.feishu.assistant.will.briefly.go")))}, {Text: managementCopy("management.dismiss.this.version"), Type: "default", Action: managementActionSystemUpdateDismiss, Values: map[string]string{"version": result.LatestVersion}}, {Text: managementCopy("management.back.to.system.status"), Type: "default", Action: managementActionSystemMaintenance}}
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.update.check"), Subtitle: status, Summary: managementCopy("management.amux.update.check.results"), Theme: theme, Icon: managementIcon("version_outlined", iconColor), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.current.version"), Body: managementValueOrPlaceholder(result.CurrentVersion, managementCopy("management.unknown"))}, managementSummaryItem{Title: managementCopy("management.available.version"), Body: managementValueOrPlaceholder(result.LatestVersion, managementCopy("management.unknown"))}), managementHintElement(hint)}, Buttons: buttons})
}

func renderSystemUpgradingCard(expectedVersion string) string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.upgrading.amux"), Subtitle: managementCopy("management.the.feishu.assistant.will.briefly.go.offline"), Summary: managementCopy("management.amux.upgrading"), Theme: "blue", Icon: managementIcon("version_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.target.version"), Body: managementValueOrPlaceholder(expectedVersion, managementCopy("management.unknown"))}), managementHintElement(managementCopy("management.refreshes.automatically.after.reconnecting"))}})
}

func renderSystemUpgradeRecoveredCard(state managementState, expectedVersion string) string {
	currentVersion := strings.TrimSpace(state.SystemInfo.Version)
	expectedVersion = strings.TrimSpace(expectedVersion)
	if state.SystemStatus != managementSystemHealthy || currentVersion == "" || currentVersion != expectedVersion {
		subtitle := managementCopy("management.amux.reconnected.version.needs.verification")
		hint := managementCopy("management.the.current.version.differs.from.the.target.check.for")
		if state.SystemStatus != managementSystemHealthy {
			subtitle = managementCopy("management.amux.reconnected.runtimes.still.need.attention")
			hint = managementCopy("management.no.runtimes.are.available.after.the.upgrade.yet.check")
		}
		return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.update.result.needs.verification"), Subtitle: subtitle, Summary: managementCopy("management.amux.update.result.needs.verification"), Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.current.version"), Body: managementValueOrPlaceholder(currentVersion, managementCopy("management.unknown"))}, managementSummaryItem{Title: managementCopy("management.target.version"), Body: managementValueOrPlaceholder(expectedVersion, managementCopy("management.unknown"))}), managementHintElement(hint)}, Buttons: []managementButton{{Text: managementCopy("management.check.for.updates.again"), Type: "primary", Action: managementActionSystemUpdate, Icon: managementIconPtr("refresh_outlined", "blue")}, {Text: managementCopy("management.back.to.overview"), Type: "default", Action: managementActionStatusOpen}}})
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.amux.upgrade.complete"), Subtitle: managementCopy("management.running.normally.ready.to.continue"), Summary: managementCopy("management.amux.upgrade.complete"), Theme: "green", Icon: managementIcon("check_outlined", "green"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.current.version"), Body: currentVersion}, managementSummaryItem{Title: managementCopy("management.upgrade.status"), Body: managementCopy("management.complete")})}, Buttons: []managementButton{{Text: managementCopy("management.back.to.overview"), Type: "primary", Action: managementActionStatusOpen}, {Text: managementCopy("management.system.status"), Type: "default", Action: managementActionSystemMaintenance}}})
}

func renderSystemMaintenanceFailureCard(message any, retryAction string, retryText any) string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.operation.failed"), Subtitle: managementCopy("management.system.state.unchanged"), Summary: managementCopy("management.amux.system.maintenance.failed"), Theme: "red", Icon: managementIcon("warning_outlined", "red"), Elements: []cardElement{managementHintElement(message)}, Buttons: []managementButton{{Text: retryText, Type: "primary", Action: retryAction}, {Text: managementCopy("management.back.to.system.status"), Type: "default", Action: managementActionSystemMaintenance}}})
}
