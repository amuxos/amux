package hub

import (
	"context"
	"sync"

	"github.com/amuxos/amux/internal/config"
)

type liveExperimentConsumer interface {
	ApplyLiveExperimentSettings(config.ExperimentsSettings)
}

// localManagementConfigActions owns installation-wide configuration reads and
// writes initiated from management surfaces, keeping host config mutations in
// the Hub control plane and broadcasting live-reloadable flags to all Bots.
type localManagementConfigActions struct {
	mu      sync.Mutex
	targets []liveExperimentConsumer
}

func newLocalManagementConfigActions() *localManagementConfigActions {
	return &localManagementConfigActions{}
}

func (a *localManagementConfigActions) RegisterTarget(target liveExperimentConsumer) {
	if a == nil || target == nil {
		return
	}
	a.mu.Lock()
	a.targets = append(a.targets, target)
	a.mu.Unlock()
}

func (a *localManagementConfigActions) ReadExperimentsSettings() (config.ExperimentsSettings, error) {
	return config.ReadExperimentsSettings()
}

func (a *localManagementConfigActions) UpdateExperimentsSettings(update config.ExperimentsSettingsUpdate) (config.ExperimentsSettings, error) {
	a.mu.Lock()
	saved, err := config.UpdateExperimentsSettings(update)
	targets := append([]liveExperimentConsumer(nil), a.targets...)
	a.mu.Unlock()
	if err != nil {
		return config.ExperimentsSettings{}, err
	}
	for _, target := range targets {
		target.ApplyLiveExperimentSettings(saved)
	}
	return saved, nil
}

func (a *localManagementConfigActions) ReadDailyCleanupSettings(ctx context.Context) (config.DailyCleanupSettings, error) {
	return config.ReadDailyCleanupSettingsContext(ctx)
}

func (a *localManagementConfigActions) SaveDailyCleanupEnabled(ctx context.Context, enabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return config.SaveDailyCleanupEnabledContext(ctx, enabled)
}
