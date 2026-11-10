package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/webconsole"
)

func (b *consoleBackend) snapshotBridge(ctx context.Context, resource string, query url.Values) (any, error) {
	action := "list"
	if query.Get("chat_id") != "" || query.Get("profile") != "" && resource == "bots" {
		action = "get"
	}
	params := map[string]string{}
	for _, key := range []string{"profile", "chat_id"} {
		if value := query.Get(key); value != "" {
			params[key] = value
		}
	}
	data, _ := json.Marshal(params)
	return b.bridgeManagement(ctx, resource, action, data)
}
func (b *consoleBackend) executeBridge(ctx context.Context, resource, action string, data json.RawMessage) (any, error) {
	return b.bridgeManagement(ctx, resource, action, data)
}
func (b *consoleBackend) bridgeManagement(ctx context.Context, resource, action string, data json.RawMessage) (any, error) {
	response, err := b.hub.ConsoleManagement(ctx, &pb.ConsoleManagementRequest{Resource: resource, Action: action, PayloadJson: data})
	if err != nil {
		return nil, consoleOperationError(err)
	}
	if len(response.GetPayloadJson()) == 0 || !json.Valid(response.GetPayloadJson()) {
		return nil, consoleInvalid("backend_response_invalid", "")
	}
	return json.RawMessage(response.GetPayloadJson()), nil
}

// This explicit allowlist is intentionally smaller than CLI config. The
// browser never sees hub.token, global_env, executable paths or raw TOML.
var consoleSettingsKeys = []string{
	"bridge.feishu.group_settings.enabled",
	"bridge.feishu.presentation.thinking_process",
	"bridge.feishu.presentation.completion_notification_enabled",
	"bridge.feishu.presentation.completion_notification_after_seconds",
	"maintenance.daily_cleanup.enabled",
	"maintenance.daily_cleanup.at",
}

func consoleSettingsVersion(raw map[string]any) string {
	data, _ := json.Marshal(raw)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func consoleSettingAllowed(key string) bool {
	for _, allowed := range consoleSettingsKeys {
		if key == allowed {
			return true
		}
	}
	return false
}
func (b *consoleBackend) snapshotSettings() (any, error) {
	b.localMu.Lock()
	defer b.localMu.Unlock()
	cfg, err := readCLIConfigFile()
	if err != nil {
		return nil, consoleOperationError(err)
	}
	settings := make([]map[string]any, 0, len(consoleSettingsKeys))
	defaults := map[string]string{"bridge.feishu.group_settings.enabled": "false", "bridge.feishu.presentation.thinking_process": "collapsed", "bridge.feishu.presentation.completion_notification_enabled": "false", "bridge.feishu.presentation.completion_notification_after_seconds": fmt.Sprint(config.DefaultFeishuCompletionNotificationAfterSeconds), "maintenance.daily_cleanup.enabled": "true", "maintenance.daily_cleanup.at": "06:17"}
	for _, key := range consoleSettingsKeys {
		value, exists := nestedConfigValue(map[string]any(cfg), strings.Split(key, "."))
		text := defaults[key]
		if exists {
			text = fmt.Sprint(value)
		}
		settings = append(settings, map[string]any{"key": key, "value": text, "writable": true, "inherited": !exists, "restart_required": true})
	}
	return map[string]any{"settings": settings, "version": consoleSettingsVersion(map[string]any(cfg)), "actions": []string{"save"}, "scope": "local_installation", "hub_address": hubAddr()}, nil
}
func (b *consoleBackend) executeSettings(action string, data json.RawMessage) (any, error) {
	if action != "save" {
		return nil, consoleInvalid("unsupported_action", "")
	}
	var request struct {
		ExpectedVersion string            `json:"expected_version"`
		Values          map[string]string `json:"values"`
	}
	if err := consoleDecode(data, &request); err != nil {
		return nil, err
	}
	if request.ExpectedVersion == "" || len(request.Values) == 0 {
		return nil, consoleInvalid("field_required", "expected_version")
	}
	for key, value := range request.Values {
		if !consoleSettingAllowed(key) || len(value) > 4096 {
			return nil, consoleInvalid("invalid_field", "key")
		}
	}
	b.localMu.Lock()
	err := config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
		if consoleSettingsVersion(raw) != request.ExpectedVersion {
			return webconsole.ErrConflict
		}
		keys := make([]string, 0, len(request.Values))
		for key := range request.Values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := setConfigValue(cliConfigFile(raw), key, request.Values[key]); err != nil {
				return consoleInvalid("invalid_field", key)
			}
		}
		if err := validateCLIGroupWatchInteractionCompatibility(cliConfigFile(raw), ""); err != nil {
			return consoleInvalid("invalid_field", "interaction")
		}
		return nil
	})
	b.localMu.Unlock()
	if err != nil {
		return nil, consoleOperationError(err)
	}
	value, err := b.snapshotSettings()
	if err != nil {
		return nil, err
	}
	out := value.(map[string]any)
	out["restart_required"] = true
	out["status"] = "saved"
	return out, nil
}
