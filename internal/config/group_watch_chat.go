package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/spf13/viper"
)

// ErrGroupWatchChatSettingsChanged indicates a conflict with the saved version.
var ErrGroupWatchChatSettingsChanged = errors.New("group watch settings changed; reopen the editor")

// A saved record is a complete group policy. Empty execution fields explicitly
// inherit the group's conversation defaults, not the Bot-wide watch override.
type BridgeFeishuGroupWatchChatConfig struct {
	Enabled                  bool
	AgentProfileRef          string
	RuntimeRef               string
	ModelRef                 string
	ReasoningEffort          string
	ThinkingProcess          string
	MaxActiveSessionsPerChat int
	IdleTimeoutSeconds       int
	Version                  int64
}

func (cfg BridgeFeishuGroupWatchConfig) ForChat(chatID string) BridgeFeishuGroupWatchConfig {
	if setting, ok := cfg.ChatSettings[strings.TrimSpace(chatID)]; ok {
		cfg.Enabled = cfg.Enabled && setting.Enabled
		cfg.AgentProfileRef, cfg.RuntimeRef = setting.AgentProfileRef, setting.RuntimeRef
		cfg.ModelRef, cfg.ReasoningEffort = setting.ModelRef, setting.ReasoningEffort
		cfg.ThinkingProcess = setting.ThinkingProcess
		cfg.MaxActiveSessionsPerChat, cfg.IdleTimeoutSeconds = setting.MaxActiveSessionsPerChat, setting.IdleTimeoutSeconds
	}
	return cfg
}

func readGroupWatchChatSettings(key string, defaults BridgeFeishuGroupWatchConfig) map[string]BridgeFeishuGroupWatchChatConfig {
	result := make(map[string]BridgeFeishuGroupWatchChatConfig)
	for chatID := range viper.GetStringMap(key) {
		base := key + "." + chatID
		setting := BridgeFeishuGroupWatchChatConfig{
			Enabled: true, ThinkingProcess: defaults.ThinkingProcess,
			MaxActiveSessionsPerChat: defaults.MaxActiveSessionsPerChat, IdleTimeoutSeconds: defaults.IdleTimeoutSeconds,
			AgentProfileRef: strings.TrimSpace(viper.GetString(base + ".agent_profile_ref")),
			RuntimeRef:      strings.TrimSpace(viper.GetString(base + ".runtime_ref")),
			ModelRef:        strings.TrimSpace(viper.GetString(base + ".model_ref")),
			ReasoningEffort: strings.TrimSpace(viper.GetString(base + ".reasoning_effort")),
			Version:         viper.GetInt64(base + ".version"),
		}
		if viper.IsSet(base + ".enabled") {
			setting.Enabled = viper.GetBool(base + ".enabled")
		}
		if viper.IsSet(base + ".thinking_process") {
			setting.ThinkingProcess = viper.GetString(base + ".thinking_process")
		}
		if viper.IsSet(base + ".max_active_sessions_per_chat") {
			setting.MaxActiveSessionsPerChat = viper.GetInt(base + ".max_active_sessions_per_chat")
		}
		if viper.IsSet(base + ".idle_timeout_seconds") {
			setting.IdleTimeoutSeconds = viper.GetInt(base + ".idle_timeout_seconds")
		}
		result[chatID] = setting
	}
	return result
}

func ValidateGroupWatchChatSettings(value BridgeFeishuGroupWatchChatConfig) error {
	if value.AgentProfileRef == "" && (value.RuntimeRef != "" || value.ModelRef != "" || value.ReasoningEffort != "") {
		return fmt.Errorf("group watch execution override requires an Agent Profile")
	}
	if value.AgentProfileRef != "" && value.RuntimeRef == "" {
		return fmt.Errorf("group watch custom execution requires a runtime")
	}
	if value.MaxActiveSessionsPerChat < 1 || value.MaxActiveSessionsPerChat > MaxFeishuGroupWatchActiveSessionsPerChat {
		return fmt.Errorf("invalid group watch active session limit")
	}
	if value.IdleTimeoutSeconds < 60 || value.IdleTimeoutSeconds > MaxFeishuGroupWatchIdleTimeoutSeconds {
		return fmt.Errorf("invalid group watch idle timeout")
	}
	return ValidateFeishuThinkingProcess(value.ThinkingProcess)
}

// Update only this group under the same file lock used by other configuration
// writers; another group's record and capability-wide controls are preserved.
func SetBridgeFeishuGroupWatchChat(profile, chatID string, value BridgeFeishuGroupWatchChatConfig, expectedVersion int64) error {
	if !feishuProfileNamePattern.MatchString(profile) || !feishuProfileNamePattern.MatchString(chatID) {
		return fmt.Errorf("invalid profile or chat_id")
	}
	if err := ValidateGroupWatchChatSettings(value); err != nil {
		return err
	}
	path := strings.TrimSpace(viper.ConfigFileUsed())
	if path == "" {
		path = amuxhome.ConfigPath()
	}
	keys := []string{"bridge", "feishu", "profiles", profile, "group_watch", "chat_settings"}
	values := map[string]any{
		"enabled": value.Enabled, "agent_profile_ref": value.AgentProfileRef, "runtime_ref": value.RuntimeRef,
		"model_ref": value.ModelRef, "reasoning_effort": value.ReasoningEffort, "thinking_process": value.ThinkingProcess,
		"max_active_sessions_per_chat": value.MaxActiveSessionsPerChat, "idle_timeout_seconds": value.IdleTimeoutSeconds,
		"version": expectedVersion + 1,
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	var chatSettings map[string]any
	err := updateConfigFileWithLock(path, func(cfg map[string]any) error {
		chatSettings = cfg
		for _, key := range keys {
			chatSettings, _ = chatSettings[key].(map[string]any)
		}
		current, _ := chatSettings[chatID].(map[string]any)
		version := int64(0)
		if current != nil {
			switch v := current["version"].(type) {
			case int64:
				version = v
			case int:
				version = int64(v)
			}
		}
		if version != expectedVersion {
			return ErrGroupWatchChatSettingsChanged
		}
		if chatSettings == nil {
			chatSettings = make(map[string]any)
			setNestedConfigValue(cfg, keys, chatSettings)
		}
		chatSettings[chatID] = values
		return nil
	})
	if err == nil || errors.Is(err, ErrGroupWatchChatSettingsChanged) {
		// Viper replaces a table with its override when enumerating keys. Publish
		// the entire table so a saved group cannot hide its persisted siblings.
		viper.Set(strings.Join(keys, "."), chatSettings)
	}
	return err
}
