package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/webconsole"
)

var consolePackNamespace = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)
var (
	consoleInstallPack  = agentprofile.AddGitPackWithOptions
	consoleSyncPack     = agentprofile.SyncPack
	consoleRollbackPack = agentprofile.RollbackPack
	consoleRemovePack   = agentprofile.RemovePackIf
)

func consolePackView(pack agentprofile.Pack) map[string]any {
	versions := pack.Versions
	if versions == nil {
		versions = []agentprofile.PackVersion{}
	}
	// URLs can embed userinfo, tokens in query parameters or scp-style usernames.
	// Do not return raw URLs or raw git failures to the browser.
	value := map[string]any{"namespace": pack.Namespace, "source_kind": pack.SourceKind, "display_name": pack.DisplayName, "ref": pack.Ref, "current_revision": pack.CurrentRevision, "auto_sync": pack.AutoSync, "sync_interval": pack.SyncInterval, "last_synced_at": pack.LastSyncedAt, "versions": versions, "development_of": pack.DevelopmentOf, "temporary": pack.Temporary, "evaluation": pack.Evaluation, "managed_by": pack.ManagedBy}
	if pack.LastError != "" {
		value["error_code"] = "pack_sync_failed"
	}
	if parsed, err := url.Parse(pack.GitURL); err == nil && parsed.Host != "" {
		value["source_host"] = parsed.Hostname()
	}
	return value
}
func consolePacks() (map[string]any, error) {
	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		return nil, err
	}
	packs := agentprofile.ListPacks(registry)
	// A missing or unreadable lease registry only hides development details.
	leases, _ := agentprofile.LoadDevelopmentLeaseRegistry(defaultAgentPackRegistryPath())
	views := make([]map[string]any, 0, len(packs))
	for _, pack := range packs {
		view := consolePackView(pack)
		if pack.Temporary {
			view["removable"] = true
		}
		if lease, ok := leases.Leases[pack.Namespace]; ok && pack.Temporary {
			// Host paths stay out of the browser; the branch and lifetime identify it.
			development := map[string]any{"branch": lease.Branch, "created_at": lease.CreatedAt, "expires_at": lease.ExpiresAt}
			if lease.LastError != "" {
				development["cleanup_error_code"] = consoleDevelopmentRetainCode(lease.LastError)
			}
			view["development"] = development
		}
		views = append(views, view)
	}
	return map[string]any{"packs": views}, nil
}

// consolePacksWithExperts adds how many experts each Pack provides. The count
// comes from the Hub's discovered Profiles; when they cannot be listed the Packs
// are still returned, without counts.
func (b *consoleBackend) consolePacksWithExperts(ctx context.Context) (map[string]any, error) {
	value, err := consolePacks()
	if err != nil || b.hub == nil {
		return value, err
	}
	listCtx, cancel := context.WithTimeout(ctx, consoleUsageTimeout)
	defer cancel()
	resp, listErr := b.hub.ListAgentProfiles(listCtx, &pb.ListAgentProfilesRequest{})
	if listErr != nil || resp == nil || resp.GetProfilesError() != "" {
		return value, nil
	}
	profiles, decodeErr := discoveredAgentProfilesFromPB(resp.GetProfiles())
	if decodeErr != nil {
		return value, nil
	}
	counts := map[string]int{}
	for _, profile := range profiles {
		if namespace, _, found := strings.Cut(profile.Profile.Ref(), "/"); found {
			counts[namespace]++
		}
	}
	for _, view := range value["packs"].([]map[string]any) {
		namespace, _ := view["namespace"].(string)
		view["profile_count"] = counts[namespace]
	}
	return value, nil
}

// consolePackExperts lists the refs of the experts a Pack provides, for the removal
// dialog. When the Hub cannot enumerate Profiles it falls back to the experts that
// something refers to.
func (b *consoleBackend) consolePackExperts(ctx context.Context, namespace string, referenced map[string]consoleUsageCounts) []string {
	refs := map[string]bool{}
	for ref := range referenced {
		refs[ref] = true
	}
	if b.hub != nil {
		listCtx, cancel := context.WithTimeout(ctx, consoleUsageTimeout)
		defer cancel()
		if resp, err := b.hub.ListAgentProfiles(listCtx, &pb.ListAgentProfilesRequest{}); err == nil && resp != nil && resp.GetProfilesError() == "" {
			if profiles, decodeErr := discoveredAgentProfilesFromPB(resp.GetProfiles()); decodeErr == nil {
				for _, profile := range profiles {
					if ref := profile.Profile.Ref(); strings.HasPrefix(ref, namespace+"/") {
						refs[ref] = true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// consolePackRemovalGuard refuses to remove a Pack that something starts new work
// from: a Bot or group default expert, a Schedule or a persistent instance would
// fail once the Pack is gone. Sessions keep their frozen launch snapshot and do not
// block. When use cannot be read the answer is unknown, which blocks as well; only
// an explicit force passes.
func (b *consoleBackend) consolePackRemovalGuard(ctx context.Context, namespace string, force bool) error {
	if force {
		return nil
	}
	index := b.consoleUsageIndex(ctx)
	if counts, _, _ := index.forNamespace(namespace); counts.starting() > 0 {
		return &consoleResourceError{code: "pack_in_use", field: "namespace", cause: webconsole.ErrConflict}
	}
	for _, source := range index.unavailable {
		if source != "sessions" {
			return &consoleResourceError{code: "usage_unavailable", field: "namespace", cause: webconsole.ErrConflict}
		}
	}
	return nil
}
func consoleValidatePackNamespace(namespace string) error {
	if !consolePackNamespace.MatchString(namespace) || namespace == "builtin" || namespace == agentprofile.ManagedLocalPackNamespace {
		return consoleInvalid("invalid_field", "namespace")
	}
	return nil
}
func consoleValidateRemotePackURL(raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n\t ") || strings.HasPrefix(raw, "-") {
		return consoleInvalid("invalid_field", "url")
	}
	var host string
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || parsed.Path == "" || parsed.Path == "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return consoleInvalid("invalid_field", "url")
		}
		switch parsed.Scheme {
		case "https":
			if parsed.User != nil {
				return consoleInvalid("invalid_field", "url")
			}
		case "ssh":
			if parsed.User != nil {
				if _, ok := parsed.User.Password(); ok {
					return consoleInvalid("invalid_field", "url")
				}
			}
		default:
			return consoleInvalid("invalid_field", "url")
		}
		host = parsed.Hostname()
	} else {
		// Accept conventional git@host:repo.git, but not file paths, remote helpers,
		// arbitrary commands, or git's ext:: protocol.
		at := strings.Index(raw, "@")
		colon := strings.Index(raw, ":")
		if at <= 0 || colon <= at+1 || colon == len(raw)-1 || strings.Contains(raw[:colon], "/") || strings.Contains(raw, "::") {
			return consoleInvalid("invalid_field", "url")
		}
		host = raw[at+1 : colon]
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "" || strings.HasPrefix(host, "-") || ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return consoleInvalid("invalid_field", "url")
	}
	return nil
}
func (b *consoleBackend) consoleExecutePack(ctx context.Context, action string, data json.RawMessage) (any, error) {
	var req struct {
		Namespace string `json:"namespace"`
		URL       string `json:"url,omitempty"`
		Ref       string `json:"ref,omitempty"`
		Revision  string `json:"revision,omitempty"`
		Confirm   bool   `json:"confirm,omitempty"`
		KeepFiles bool   `json:"keep_files,omitempty"`
		Force     bool   `json:"force,omitempty"`
		// KeepWorktree removes a temporary development Pack while leaving its
		// development worktree and branch, e.g. with uncommitted work.
		KeepWorktree bool `json:"keep_worktree,omitempty"`
	}
	if err := consoleDecode(data, &req); err != nil {
		return nil, err
	}
	req.Namespace = strings.TrimSpace(req.Namespace)
	if err := consoleValidatePackNamespace(req.Namespace); err != nil {
		return nil, err
	}
	if action == "install" || action == "rollback" || action == "remove" {
		if !req.Confirm {
			return nil, consoleInvalid("confirmation_required", "confirm")
		}
	}
	if strings.HasPrefix(req.Ref, "-") || strings.ContainsAny(req.Ref, "\x00\r\n") || strings.HasPrefix(req.Revision, "-") || strings.ContainsAny(req.Revision, "\x00\r\n") {
		return nil, consoleInvalid("invalid_field", "ref")
	}
	if action == "install" {
		if err := consoleValidateRemotePackURL(req.URL); err != nil {
			return nil, err
		}
	}
	if action == "rollback" {
		if err := consoleRequired(req.Revision, "revision"); err != nil {
			return nil, err
		}
	}
	registryPath := defaultAgentPackRegistryPath()
	if action != "install" {
		registry, err := agentprofile.LoadPackRegistry(registryPath)
		if err != nil {
			return nil, err
		}
		pack, ok := registry.Packs[req.Namespace]
		if !ok {
			return nil, consoleMissing()
		}
		// Temporary development and evaluation Packs can only be removed; other
		// amux-managed Packs are read-only.
		if pack.Temporary && action != "remove" || !pack.Temporary && (pack.ManagedBy != "" || pack.Evaluation) {
			return nil, consoleInvalid("managed_pack_read_only", "namespace")
		}
		if req.KeepWorktree && !pack.Temporary {
			return nil, consoleInvalid("invalid_field", "keep_worktree")
		}
		if action == "remove" && pack.Temporary {
			return b.consoleRemoveTemporaryPack(ctx, registryPath, req.Namespace, req.KeepFiles, req.KeepWorktree, req.Force)
		}
		// Removal runs no git command, so any registered Pack can be removed; only
		// sync and rollback need a source the console accepts.
		if action != "remove" {
			if err := consoleValidateRemotePackURL(pack.GitURL); err != nil {
				return nil, err
			}
		}
		if action == "remove" {
			// Fail fast, without waiting for the registry lock; the same check runs
			// again under the lock before anything is removed.
			if err := b.consolePackRemovalGuard(ctx, req.Namespace, req.Force); err != nil {
				return nil, err
			}
		}
		if action == "rollback" {
			recorded := false
			for _, version := range pack.Versions {
				if version.Revision == req.Revision {
					recorded = true
					break
				}
			}
			if !recorded {
				return nil, consoleInvalid("invalid_field", "revision")
			}
		}
	}
	opCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var pack agentprofile.Pack
	var err error
	switch action {
	case "install":
		// Register only; never start/restart an OS service from a browser request.
		// Auto-sync can be enabled through the owning CLI after trust is established.
		pack, err = consoleInstallPack(opCtx, registryPath, req.Namespace, req.URL, agentprofile.AddGitPackOptions{Ref: req.Ref, AutoSync: false})
	case "sync":
		pack, err = consoleSyncPack(opCtx, registryPath, req.Namespace)
	case "rollback":
		pack, err = consoleRollbackPack(opCtx, registryPath, req.Namespace, req.Revision)
	case "remove":
		var result agentprofile.RemovePackResult
		result, err = consoleRemovePack(opCtx, registryPath, req.Namespace, agentprofile.RemovePackOptions{KeepFiles: req.KeepFiles}, func(current agentprofile.Pack) error {
			// Re-check after acquiring the Pack lock. References in other stores
			// can still change concurrently; this is a point-in-time check.
			if current.ManagedBy != "" || current.Temporary || current.Evaluation {
				return consoleInvalid("managed_pack_read_only", "namespace")
			}
			return b.consolePackRemovalGuard(opCtx, req.Namespace, req.Force)
		})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				// Typically an auto-sync holding the registry lock.
				return nil, &consoleResourceError{code: "pack_busy", cause: webconsole.ErrConflict}
			}
			// RemovePackIf returns a removal path only after successfully saving the
			// unregistered state. Do not infer partial success from a later registry
			// read, which could race with a different operator removing the Pack.
			if result.RemovedPath != "" && !result.FilesRemoved {
				return map[string]any{"ok": true, "removed": map[string]any{"namespace": req.Namespace, "files_removed": false}, "warning_code": "pack_files_not_removed"}, nil
			}
			return nil, err
		}
		// Never return the materialized path: it is a host filesystem location.
		return map[string]any{"ok": true, "removed": map[string]any{"namespace": req.Namespace, "files_removed": result.FilesRemoved}}, nil
	default:
		return nil, consoleInvalid("unsupported_action", "")
	}
	if err != nil {
		if err == agentprofile.ErrPackRegistryStateChanged {
			return nil, &consoleResourceError{code: "operation_conflict", cause: webconsole.ErrConflict}
		}
		return nil, err
	}
	return map[string]any{"ok": true, "pack": consolePackView(pack)}, nil
}

// consoleRemoveTemporaryPack removes a temporary development or evaluation Pack.
// A Pack with a development lease is cleaned up like the lease's own cleanup: a
// clean worktree is removed with the Pack. A worktree with changes is retained
// and reported, unless the operator explicitly keeps the worktree.
func (b *consoleBackend) consoleRemoveTemporaryPack(ctx context.Context, registryPath, namespace string, keepFiles, keepWorktree, force bool) (any, error) {
	if err := b.consolePackRemovalGuard(ctx, namespace, force); err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	guard := func(current agentprofile.Pack) error {
		if !current.Temporary {
			return consoleInvalid("managed_pack_read_only", "namespace")
		}
		return b.consolePackRemovalGuard(opCtx, namespace, force)
	}
	busy := func(err error) error {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &consoleResourceError{code: "pack_busy", cause: webconsole.ErrConflict}
		}
		return err
	}
	leases, err := agentprofile.LoadDevelopmentLeaseRegistry(registryPath)
	if err != nil {
		return nil, err
	}
	if _, leased := leases.Leases[namespace]; !leased {
		if keepWorktree {
			return nil, consoleInvalid("invalid_field", "keep_worktree")
		}
		result, err := consoleRemovePack(opCtx, registryPath, namespace, agentprofile.RemovePackOptions{KeepFiles: keepFiles}, guard)
		if err != nil {
			if result.RemovedPath != "" && !result.FilesRemoved {
				return map[string]any{"ok": true, "removed": map[string]any{"namespace": namespace, "files_removed": false}, "warning_code": "pack_files_not_removed"}, nil
			}
			return nil, busy(err)
		}
		return map[string]any{"ok": true, "removed": map[string]any{"namespace": namespace, "files_removed": result.FilesRemoved}}, nil
	}
	if keepWorktree {
		result, err := consoleReleaseDevelopmentPack(opCtx, registryPath, namespace, guard)
		if err != nil {
			return nil, busy(err)
		}
		return map[string]any{"ok": true, "removed": map[string]any{"namespace": namespace, "files_removed": result.FilesRemoved, "worktree_kept": true}}, nil
	}
	result, err := consoleCleanupDevelopmentPack(opCtx, registryPath, namespace, guard)
	if err != nil {
		return nil, busy(err)
	}
	if result.Status != agentprofile.DevelopmentLeaseCleaned {
		return nil, &consoleResourceError{code: "development_worktree_retained", field: consoleDevelopmentRetainCode(result.Reason), cause: webconsole.ErrConflict}
	}
	return map[string]any{"ok": true, "removed": map[string]any{"namespace": namespace, "files_removed": true, "worktree_removed": true}}, nil
}

var (
	consoleCleanupDevelopmentPack = agentprofile.CleanupDevelopmentLeaseIf
	consoleReleaseDevelopmentPack = agentprofile.ReleaseDevelopmentLeaseIf
)

// consoleDevelopmentRetainCode maps a lease cleanup diagnostic, which can name
// host paths and Git remotes, to a stable code the console can explain.
func consoleDevelopmentRetainCode(reason string) string {
	switch {
	case strings.Contains(reason, "uncommitted changes"):
		return "worktree_has_changes"
	case strings.Contains(reason, "does not match its development lease"):
		return "pack_identity_changed"
	case strings.Contains(reason, "origin"):
		return "repository_origin_changed"
	}
	return "cleanup_blocked"
}
