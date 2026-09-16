package config

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/spf13/viper"
)

// CompletionNotificationSettings is the saved installation-wide experiment.
// Running bridges read their startup config until the operator restarts amux.
type CompletionNotificationSettings struct {
	Enabled      bool
	AfterSeconds int
}

func completionNotificationConfigPath() string {
	if path := strings.TrimSpace(viper.ConfigFileUsed()); path != "" {
		return path
	}
	return amuxhome.ConfigPath()
}

func ReadCompletionNotificationSettings() (CompletionNotificationSettings, error) {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	raw, err := readConfigFileForWrite(completionNotificationConfigPath())
	if err != nil {
		return CompletionNotificationSettings{}, err
	}
	v := viper.New()
	if err := v.MergeConfigMap(raw); err != nil {
		return CompletionNotificationSettings{}, err
	}
	seconds := DefaultFeishuCompletionNotificationAfterSeconds
	if v.IsSet("bridge.feishu.presentation.completion_notification_after_seconds") {
		seconds = v.GetInt("bridge.feishu.presentation.completion_notification_after_seconds")
		if seconds < 0 {
			seconds = DefaultFeishuCompletionNotificationAfterSeconds
		}
	}
	return CompletionNotificationSettings{
		Enabled:      v.GetBool("bridge.feishu.presentation.completion_notification_enabled"),
		AfterSeconds: seconds,
	}, nil
}

func SaveCompletionNotificationSettings(settings CompletionNotificationSettings) error {
	if settings.AfterSeconds < 0 || settings.AfterSeconds > 604800 {
		return fmt.Errorf("通知阈值须为 0 到 604800 秒的整数")
	}
	return UpdateConfigFile(completionNotificationConfigPath(), func(raw map[string]any) error {
		setNestedConfigValue(raw, strings.Split("bridge.feishu.presentation.completion_notification_enabled", "."), settings.Enabled)
		setNestedConfigValue(raw, strings.Split("bridge.feishu.presentation.completion_notification_after_seconds", "."), settings.AfterSeconds)
		return nil
	})
}
