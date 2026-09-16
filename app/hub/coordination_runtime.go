package hub

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/coordination"
	"github.com/amuxos/amux/internal/coordination/amuxadapter"
	"github.com/rs/zerolog/log"
)

type coordinationRuntime struct {
	store  *coordination.SQLiteStore
	server *coordinationServer
}

func newProductionCoordinationRuntime(ctx context.Context, router coordinationTaskRouter, bridges []*bridge.Bridge) (*coordinationRuntime, error) {
	tasks, err := newCoordinationTaskControl(router)
	if err != nil {
		return nil, err
	}
	external, err := bridge.NewExternalBotCoordinationAdapter(bridges)
	if err != nil {
		log.Warn().Err(err).Msg("hub: external Bot coordination adapter unavailable; continuing with internal collaborators")
		return openCoordinationRuntime(ctx, filepath.Join(amuxhome.Dir(), "coordination.db"), tasks, loadCoordinationProfiles)
	}
	return openCoordinationRuntime(ctx, filepath.Join(amuxhome.Dir(), "coordination.db"), tasks, loadCoordinationProfiles, external)
}

type coordinationCatalogGroup struct {
	required coordination.Catalog
	optional []coordination.Catalog
}

func (g coordinationCatalogGroup) List(ctx context.Context, scope coordination.CatalogScope) ([]coordination.CollaboratorDescriptor, error) {
	var result []coordination.CollaboratorDescriptor
	seen := make(map[string]struct{})
	catalogs := append([]coordination.Catalog{g.required}, g.optional...)
	for index, catalog := range catalogs {
		items, err := catalog.List(ctx, scope)
		if err != nil {
			if index == 0 {
				return nil, err
			}
			log.Warn().Err(err).Msg("hub: optional coordination catalog unavailable")
			continue
		}
		for _, item := range items {
			if _, duplicate := seen[item.ID]; duplicate {
				return nil, fmt.Errorf("duplicate collaborator id %q", item.ID)
			}
			seen[item.ID] = struct{}{}
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (g coordinationCatalogGroup) ResolveTarget(ctx context.Context, id string, scope coordination.CatalogScope) (coordination.ResolvedCollaborator, error) {
	catalogs := append([]coordination.Catalog{g.required}, g.optional...)
	for index, catalog := range catalogs {
		items, err := catalog.List(ctx, scope)
		if err != nil {
			if index == 0 {
				return coordination.ResolvedCollaborator{}, err
			}
			continue
		}
		for _, item := range items {
			if item.ID == id {
				return catalog.ResolveTarget(ctx, id, scope)
			}
		}
	}
	return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown collaborator %q", id)
}

func openCoordinationRuntime(_ context.Context, path string, tasks amuxadapter.TaskControl, loadProfiles amuxadapter.ProfileLoader, extras ...coordination.Adapter) (*coordinationRuntime, error) {
	store, err := coordination.OpenSQLiteStore(path)
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*coordinationRuntime, error) { _ = store.Close(); return nil, cause }
	catalog, err := amuxadapter.NewProfileCatalog(loadProfiles)
	if err != nil {
		return fail(err)
	}
	backend, err := amuxadapter.New(amuxadapter.Options{Tasks: tasks, BuildSpec: catalog})
	if err != nil {
		return fail(err)
	}
	optionalCatalogs := make([]coordination.Catalog, 0, len(extras))
	backends := map[string]coordination.Backend{coordination.BackendKindAmuxTask: backend}
	for _, adapter := range extras {
		if adapter == nil {
			continue
		}
		kind := adapter.BackendKind()
		if kind == "" || backends[kind] != nil {
			return fail(fmt.Errorf("duplicate or empty coordination backend kind %q", kind))
		}
		optionalCatalogs = append(optionalCatalogs, adapter)
		backends[kind] = adapter
	}
	server, err := newCoordinationServer(store, coordinationCatalogGroup{required: catalog, optional: optionalCatalogs}, backends)
	if err != nil {
		return fail(err)
	}
	return &coordinationRuntime{store: store, server: server}, nil
}

func (r *coordinationRuntime) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

func loadCoordinationProfiles(context.Context) ([]agentprofile.Profile, error) {
	profiles := agentprofile.Builtins()
	registry, err := agentprofile.LoadPackRegistry(agentprofile.DefaultPackRegistryPath())
	if err != nil {
		return nil, err
	}
	packProfiles, err := agentprofile.ListPackProfiles(registry)
	if err != nil {
		return nil, err
	}
	profiles = append(profiles, packProfiles...)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].PinnedRef() < profiles[j].PinnedRef() })
	return profiles, nil
}
