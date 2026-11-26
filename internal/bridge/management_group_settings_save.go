package bridge

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

const groupSettingsSaveTimeout = 15 * time.Second

// Reserve before advancing the editor version: duplicate clicks must not make
// the first accepted save's result stale. The reservation is per group, not card.
func (b *Bridge) beginGroupSettingsSave(chatID, messageID string) (uint64, bool) {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	if b.groupSettingsSaves[chatID] {
		return 0, false
	}
	if b.groupSettingsSaves == nil {
		b.groupSettingsSaves = make(map[string]bool)
	}
	b.groupSettingsSaves[chatID] = true
	if b.modelServiceVersions == nil {
		b.modelServiceVersions = make(map[string]uint64)
	}
	b.modelServiceVersions[messageID]++
	return b.modelServiceVersions[messageID], true
}

func (b *Bridge) finishGroupSettingsSave(chatID string) {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	delete(b.groupSettingsSaves, chatID)
}

func (b *Bridge) runGroupSettingsSave(openID, messageID, chatName string, scope *GroupSettings, action *callback.CallBackAction, version uint64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), groupSettingsSaveTimeout)
		defer cancel()
		finish := sync.OnceFunc(func() { b.finishGroupSettingsSave(scope.ChatID) })
		defer finish()
		started := time.Now()
		var saveErr error
		stage := "scope"
		committed := false
		defer func() {
			event := log.Info()
			if saveErr != nil {
				event = log.Warn().Err(saveErr)
			}
			event.Str("chat_id", scope.ChatID).Str("message_id", messageID).
				Str("stage", stage).Bool("committed", committed).Dur("elapsed_ms", time.Since(started)).
				Msg("bridge: group settings save completed")
		}()
		// Loading and completion use the same worker and versioned delivery lock.
		// Loading delivery shares the mutation budget.
		loadingCtx, loadingCancel := context.WithTimeout(ctx, time.Second)
		b.writeManagementCardAtVersion(loadingCtx, openID, messageID,
			renderGroupSettingsSaveStatusCard(scope, chatName, managementCopy("management.group_settings.saving"), "blue"), version)
		loadingCancel()

		settings, _, err := b.groupSettingsFromCardScopeContext(ctx, action)
		if err != nil {
			saveErr = err
			finish()
			b.writeGroupSettingsSaveFailure(openID, messageID, chatName, scope, nil, err, version)
			return
		}
		draft, err := groupSettingsDraftFromCallback(settings, action)
		if err == nil && draft["daily_cleanup"] != "" && draft["daily_cleanup"] != "true" && draft["daily_cleanup"] != "false" {
			err = managementLocalizedError("cleanup.invalid")
		}
		var submitted *GroupSettings
		if err == nil {
			submitted, err = draft.settings(settings)
		}
		stage = "apply"
		applyStarted := time.Now()
		if err == nil {
			settings, err = b.applyGroupSettings(ctx, submitted, scope.Version, openID)
		}
		log.Debug().Str("chat_id", scope.ChatID).Dur("apply_ms", time.Since(applyStarted)).Msg("bridge: group settings save apply completed")
		if err != nil {
			saveErr = err
			finish()
			b.writeGroupSettingsSaveFailure(openID, messageID, chatName, scope, draft, err, version)
			return
		}
		committed = true
		stage = "cleanup"
		cleanupStarted := time.Now()
		if managementUsesThreadRouting(submitted.InteractionMode) && draft["daily_cleanup"] != "" {
			err = b.managementConfigActions().SaveDailyCleanupEnabled(ctx, draft["daily_cleanup"] == "true")
		}
		log.Debug().Str("chat_id", scope.ChatID).Dur("cleanup_ms", time.Since(cleanupStarted)).Msg("bridge: group settings save cleanup completed")
		if err != nil {
			saveErr = err
			// Group policy is already committed. Do not tell the owner that the
			// entire save failed or invite a stale-version retry of that policy.
			finish()
			b.writeManagementCardAtVersion(context.Background(), openID, messageID,
				renderGroupSettingsSaveStatusCard(settings, chatName, managementCopy("management.group_settings.cleanup_partial"), "orange"), version)
			return
		}
		stage = "result"
		card, err := b.currentGroupSettingsCardWithNoticeContext(ctx, settings, chatName, managementCopy("management.group.settings.saved.new.execution.options.apply.only.to"))
		if err != nil {
			saveErr = err
			card = renderGroupSettingsSaveStatusCard(settings, chatName, managementCopy("management.group_settings.saved_refresh_failed"), "orange")
		}
		finish()
		b.writeManagementCardAtVersion(context.Background(), openID, messageID, card, version)
	}()
}

func (b *Bridge) writeGroupSettingsSaveFailure(openID, messageID, chatName string, scope *GroupSettings, draft managementGroupSettingsDraft, err error, version uint64) {
	message := managementCopy("management.group_settings.save_failed")
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		message = managementCopy("management.group_settings.save_timeout")
	} else if errors.Is(err, errGroupSettingsChanged) {
		message = managementCopy("management.group.settings.changed.send.amux.again.in.the.target")
	}
	card := renderGroupSettingsSaveStatusCard(scope, chatName, message, "red")
	if draft != nil && !errors.Is(err, errGroupSettingsChanged) {
		// A compact retry button carries the entire draft without rendering the
		// large catalog again after an error or timing out on more shared I/O.
		values := b.groupSettingsCardScope(scope, chatName)
		for key, value := range draft {
			values[key] = value
		}
		card = marshalNativeManagementCard(managementCardView{
			Title: managementCopy("management.save.failed"), Subtitle: managementConcat(chatName, " · ", scope.ChatID), Theme: "red",
			Elements: []cardElement{managementHintElement(message), managementHintElement(groupSettingsSaveErrorReason(err))},
			Buttons: []managementButton{
				groupSettingsScopedButton(managementActionGroupSettingsEdit, managementCopy("management.edit.group.settings"), "primary", values),
				groupSettingsScopedButton(managementActionGroupSettingsOpen, managementCopy("management.current.group.settings"), "default", b.groupSettingsCardScope(scope, chatName)),
			},
		})
	}
	b.writeManagementCardAtVersion(context.Background(), openID, messageID, card, version)
}

func groupSettingsSaveErrorReason(err error) any {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return managementCopy("management.group_settings.retry_after_timeout")
	}
	// Reuse the existing localized diagnostic sanitization; it is only used
	// here before any group transaction was committed.
	return managementSettingsFailureMessage(err)
}

func renderGroupSettingsSaveStatusCard(settings *GroupSettings, chatName string, message any, theme string) string {
	return marshalNativeManagementCard(managementCardView{
		Title: managementCopy("management.current.group.settings"), Subtitle: managementConcat(chatName, " · ", settings.ChatID),
		Theme: theme, Elements: []cardElement{managementHintElement(message)},
		Buttons: []managementButton{{Text: managementCopy("management.back.to.overview"), Type: "default", Action: managementActionStatusOpen}},
	})
}
