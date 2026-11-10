package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/webconsole"
)

var consolePackNamespace = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)
var (
	consoleInstallPack  = agentprofile.AddGitPackWithOptions
	consoleSyncPack     = agentprofile.SyncPack
	consoleRollbackPack = agentprofile.RollbackPack
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
	views := make([]map[string]any, 0, len(packs))
	for _, pack := range packs {
		views = append(views, consolePackView(pack))
	}
	return map[string]any{"packs": views}, nil
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
func consoleExecutePack(ctx context.Context, action string, data json.RawMessage) (any, error) {
	var req struct {
		Namespace string `json:"namespace"`
		URL       string `json:"url,omitempty"`
		Ref       string `json:"ref,omitempty"`
		Revision  string `json:"revision,omitempty"`
		Confirm   bool   `json:"confirm,omitempty"`
	}
	if err := consoleDecode(data, &req); err != nil {
		return nil, err
	}
	req.Namespace = strings.TrimSpace(req.Namespace)
	if err := consoleValidatePackNamespace(req.Namespace); err != nil {
		return nil, err
	}
	if action == "install" || action == "rollback" {
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
		if pack.ManagedBy != "" || pack.Temporary || pack.Evaluation {
			return nil, consoleInvalid("managed_pack_read_only", "namespace")
		}
		if err := consoleValidateRemotePackURL(pack.GitURL); err != nil {
			return nil, err
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
