package bridge

import (
	"context"

	"github.com/amuxos/amux/internal/config"
)

// ManagementConfigActions supplies installation-wide configuration reads and
// mutations (experiments and daily cleanup) owned by the Hub control plane so
// Feishu Bridge card handlers do not directly mutate host config files.
type ManagementConfigActions interface {
	ReadExperimentsSettings() (config.ExperimentsSettings, error)
	UpdateExperimentsSettings(config.ExperimentsSettingsUpdate) (config.ExperimentsSettings, error)
	ReadDailyCleanupSettings(context.Context) (config.DailyCleanupSettings, error)
	SaveDailyCleanupEnabled(context.Context, bool) error
}

// SetManagementConfigActions wires installation-wide configuration operations
// owned by the Hub. It must be called before Start.
func (b *Bridge) SetManagementConfigActions(actions ManagementConfigActions) {
	if b != nil {
		b.managementConfig = actions
	}
}

// ApplyLiveExperimentSettings applies live-reloadable experiment toggles pushed
// by the Hub control plane so all Bots in one installation stay consistent.
func (b *Bridge) ApplyLiveExperimentSettings(saved config.ExperimentsSettings) {
	if b == nil {
		return
	}
	b.setSteerForeground(saved.SteerForeground)
	b.showBackgroundTasks.Store(saved.ShowBackgroundTasks)
	b.mentionTurnInitiator.Store(saved.MentionTurnInitiator)
	b.replyEndNotification.Store(saved.ReplyEndNotification)
	b.setModelRecoveryQueueWaitSeconds(saved.ModelRecoveryQueueWaitSeconds)
}

func (b *Bridge) managementConfigActions() ManagementConfigActions {
	if b != nil && b.managementConfig != nil {
		return b.managementConfig
	}
	return defaultManagementConfigActions{}
}

type defaultManagementConfigActions struct{}

func (defaultManagementConfigActions) ReadExperimentsSettings() (config.ExperimentsSettings, error) {
	return config.ReadExperimentsSettings()
}

func (defaultManagementConfigActions) UpdateExperimentsSettings(update config.ExperimentsSettingsUpdate) (config.ExperimentsSettings, error) {
	return config.UpdateExperimentsSettings(update)
}

func (defaultManagementConfigActions) ReadDailyCleanupSettings(ctx context.Context) (config.DailyCleanupSettings, error) {
	return config.ReadDailyCleanupSettingsContext(ctx)
}

func (defaultManagementConfigActions) SaveDailyCleanupEnabled(ctx context.Context, enabled bool) error {
	return config.SaveDailyCleanupEnabledContext(ctx, enabled)
}
