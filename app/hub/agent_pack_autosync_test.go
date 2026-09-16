package hub

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
)

type fakeAutoSyncCatalogActions struct {
	snapshot    agentprofile.PackCatalogSnapshot
	order       []string
	installCall int
}

func (f *fakeAutoSyncCatalogActions) Snapshot() (agentprofile.PackCatalogSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakeAutoSyncCatalogActions) InstallIfCurrent(context.Context, string, string) (agentprofile.Pack, error) {
	return agentprofile.Pack{Namespace: "team", AutoSync: true}, nil
}

func (f *fakeAutoSyncCatalogActions) InstallIfCurrentWithAutoSync(
	ctx context.Context,
	_, _ string,
	ensure func(context.Context) error,
) (agentprofile.Pack, error) {
	f.order = append(f.order, "validate")
	if err := ensure(ctx); err != nil {
		return agentprofile.Pack{}, err
	}
	f.order = append(f.order, "install")
	f.installCall++
	return agentprofile.Pack{Namespace: "team", AutoSync: true}, nil
}

func (f *fakeAutoSyncCatalogActions) SyncIfCurrent(context.Context, string, string) (agentprofile.Pack, error) {
	return agentprofile.Pack{Namespace: "team"}, nil
}

func (f *fakeAutoSyncCatalogActions) SyncIfCurrentWithAutoSync(
	ctx context.Context,
	_, _ string,
	ensure func(context.Context) error,
) (agentprofile.Pack, error) {
	f.order = append(f.order, "validate")
	if err := ensure(ctx); err != nil {
		return agentprofile.Pack{}, err
	}
	f.order = append(f.order, "sync")
	return agentprofile.Pack{Namespace: "team"}, nil
}

func (f *fakeAutoSyncCatalogActions) RemoveIfCurrentAndUnreferenced(string, string, func() ([]string, error)) (agentprofile.RemovePackResult, []string, error) {
	return agentprofile.RemovePackResult{}, nil, nil
}

func (f *fakeAutoSyncCatalogActions) WithRegistryStable(commit func() error) error {
	return commit()
}

func (f *fakeAutoSyncCatalogActions) WithRegistryStableContext(ctx context.Context, commit func() error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return commit()
	}
}

func (f *fakeAutoSyncCatalogActions) EnableAutoSyncForInstalledCatalogPacks() error {
	f.order = append(f.order, "enable")
	return nil
}

func TestLocalManagementAgentPackActionsStartsAutoSyncBeforeInstall(t *testing.T) {
	delegate := &fakeAutoSyncCatalogActions{}
	actions := newLocalManagementAgentPackActions(delegate, func(context.Context) error {
		delegate.order = append(delegate.order, "start")
		return nil
	})

	pack, err := actions.InstallIfCurrent(context.Background(), "team", "generation")
	if err != nil {
		t.Fatalf("InstallIfCurrent: %v", err)
	}
	if !pack.AutoSync || !reflect.DeepEqual(delegate.order, []string{"validate", "start", "install"}) {
		t.Fatalf("pack = %+v order = %#v, want auto sync start before install", pack, delegate.order)
	}
}

func TestLocalManagementAgentPackActionsDoesNotInstallWhenAutoSyncCannotStart(t *testing.T) {
	delegate := &fakeAutoSyncCatalogActions{}
	actions := newLocalManagementAgentPackActions(delegate, func(context.Context) error {
		return errors.New("start failed")
	})

	if _, err := actions.InstallIfCurrent(context.Background(), "team", "generation"); err == nil {
		t.Fatal("InstallIfCurrent error = nil, want auto sync start failure")
	}
	if delegate.installCall != 0 {
		t.Fatalf("install calls = %d, want none", delegate.installCall)
	}
}

func TestLocalManagementAgentPackActionsPreparesExistingCatalogPacks(t *testing.T) {
	delegate := &fakeAutoSyncCatalogActions{snapshot: agentprofile.PackCatalogSnapshot{Loaded: []agentprofile.LoadedPackState{{
		Namespace: "team",
		InMarket:  true,
	}}}}
	actions := newLocalManagementAgentPackActions(delegate, func(context.Context) error {
		delegate.order = append(delegate.order, "start")
		return nil
	})

	if err := actions.PrepareExisting(context.Background()); err != nil {
		t.Fatalf("PrepareExisting: %v", err)
	}
	if !reflect.DeepEqual(delegate.order, []string{"start", "enable"}) {
		t.Fatalf("prepare order = %#v, want auto sync start before migration", delegate.order)
	}
}

func TestLocalManagementAgentPackActionsRestartsAutoSyncOnManualUpdate(t *testing.T) {
	delegate := &fakeAutoSyncCatalogActions{}
	actions := newLocalManagementAgentPackActions(delegate, func(context.Context) error {
		delegate.order = append(delegate.order, "start")
		return nil
	})

	if _, err := actions.SyncIfCurrent(context.Background(), "team", "generation"); err != nil {
		t.Fatalf("SyncIfCurrent: %v", err)
	}
	if !reflect.DeepEqual(delegate.order, []string{"validate", "start", "sync", "enable"}) {
		t.Fatalf("sync order = %#v, want daemon recovery before update and policy migration after", delegate.order)
	}
}
