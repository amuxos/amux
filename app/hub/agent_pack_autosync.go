package hub

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/bridge"
)

type autoSyncCatalogActions interface {
	bridge.ManagementAgentPackActions
	InstallIfCurrentWithAutoSync(context.Context, string, string, func(context.Context) error) (agentprofile.Pack, error)
	SyncIfCurrentWithAutoSync(context.Context, string, string, func(context.Context) error) (agentprofile.Pack, error)
	EnableAutoSyncForInstalledCatalogPacks() error
}

type localManagementAgentPackActions struct {
	delegate      autoSyncCatalogActions
	startAutoSync func(context.Context) error
}

func newLocalManagementAgentPackActions(
	delegate autoSyncCatalogActions,
	startAutoSync func(context.Context) error,
) *localManagementAgentPackActions {
	return &localManagementAgentPackActions{delegate: delegate, startAutoSync: startAutoSync}
}

func (a *localManagementAgentPackActions) PrepareExisting(ctx context.Context) error {
	snapshot, err := a.delegate.Snapshot()
	if err != nil {
		return err
	}
	for _, pack := range snapshot.Loaded {
		if !pack.InMarket {
			continue
		}
		if err := a.ensureAutoSync(ctx); err != nil {
			return err
		}
		return a.delegate.EnableAutoSyncForInstalledCatalogPacks()
	}
	return nil
}

func (a *localManagementAgentPackActions) Snapshot() (agentprofile.PackCatalogSnapshot, error) {
	return a.delegate.Snapshot()
}

func (a *localManagementAgentPackActions) InstallIfCurrent(ctx context.Context, catalogID, expectedGeneration string) (agentprofile.Pack, error) {
	return a.delegate.InstallIfCurrentWithAutoSync(ctx, catalogID, expectedGeneration, a.ensureAutoSync)
}

func (a *localManagementAgentPackActions) SyncIfCurrent(ctx context.Context, namespace, expectedGeneration string) (agentprofile.Pack, error) {
	pack, err := a.delegate.SyncIfCurrentWithAutoSync(ctx, namespace, expectedGeneration, a.ensureAutoSync)
	if err != nil {
		return pack, err
	}
	err = a.delegate.EnableAutoSyncForInstalledCatalogPacks()
	return pack, err
}

func (a *localManagementAgentPackActions) RemoveIfCurrentAndUnreferenced(
	namespace, expectedGeneration string,
	references func() ([]string, error),
) (agentprofile.RemovePackResult, []string, error) {
	return a.delegate.RemoveIfCurrentAndUnreferenced(namespace, expectedGeneration, references)
}

func (a *localManagementAgentPackActions) WithRegistryStable(commit func() error) error {
	return a.delegate.WithRegistryStable(commit)
}

func (a *localManagementAgentPackActions) WithRegistryStableContext(ctx context.Context, commit func() error) error {
	return a.delegate.WithRegistryStableContext(ctx, commit)
}

func (a *localManagementAgentPackActions) ensureAutoSync(ctx context.Context) error {
	if a == nil || a.delegate == nil || a.startAutoSync == nil {
		return fmt.Errorf("agent pack auto sync is unavailable")
	}
	if err := a.startAutoSync(ctx); err != nil {
		return fmt.Errorf("start agent pack auto sync: %w", err)
	}
	return nil
}

func startLocalAgentPackAutoSync(ctx context.Context) error {
	amuxPath, err := localAmuxExecutable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, amuxPath, "agent", "pack", "autosync", "start")
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("amux agent pack autosync start: %w", err)
	}
	return nil
}
