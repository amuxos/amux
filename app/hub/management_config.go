package hub

import (
	"context"
	"fmt"
	"sync"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
)

type liveExperimentConsumer interface {
	ApplyLiveExperimentSettings(config.ExperimentsSettings)
}

// localManagementConfigActions owns installation-wide configuration reads and
// writes initiated from management surfaces, keeping host config mutations in
// the Hub control plane and broadcasting live-reloadable flags to all Bots.
type localManagementConfigActions struct {
	mu         sync.Mutex
	targets    []liveExperimentConsumer
	turnViewer *managedTurnViewer
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
	defer a.mu.Unlock()
	startedViewer := false
	if update.TurnProcessMode != nil && *update.TurnProcessMode == config.FeishuTurnProcessSidebar {
		if a.turnViewer == nil {
			return config.ExperimentsSettings{}, bridge.ErrTurnProcessViewerUnavailable
		}
		previousURL, _ := a.turnViewer.settings()
		if err := a.turnViewer.setEnabled(true); err != nil {
			return config.ExperimentsSettings{}, fmt.Errorf("%w: %v", bridge.ErrTurnProcessViewerUnavailable, err)
		}
		startedViewer = previousURL == ""
	}
	saved, err := config.UpdateExperimentsSettings(update)
	if err != nil {
		if startedViewer {
			_ = a.turnViewer.setEnabled(false)
		}
		return config.ExperimentsSettings{}, err
	}
	if a.turnViewer != nil && saved.TurnProcessMode != config.FeishuTurnProcessSidebar {
		_ = a.turnViewer.setEnabled(false)
	}
	for _, target := range a.targets {
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
