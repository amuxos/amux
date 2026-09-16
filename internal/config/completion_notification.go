package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// CompletionNotificationSettings is the saved installation-wide experiment.
// Running bridges read their startup config until the operator restarts amux.
type CompletionNotificationSettings struct {
	Enabled      bool
	AfterSeconds int
}

func ReadCompletionNotificationSettings() (CompletionNotificationSettings, error) {
	settings, err := ReadExperimentsSettings()
	return settings.CompletionNotification, err
}

func completionNotificationSettings(v *viper.Viper) CompletionNotificationSettings {
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
	}
}

func validateCompletionNotificationSettings(settings CompletionNotificationSettings) error {
	if settings.AfterSeconds < 0 || settings.AfterSeconds > 604800 {
		return fmt.Errorf("通知阈值须为 0 到 604800 秒的整数")
	}
	return nil
}

func setCompletionNotificationSettings(raw map[string]any, settings CompletionNotificationSettings) {
	setNestedConfigValue(raw, strings.Split("bridge.feishu.presentation.completion_notification_enabled", "."), settings.Enabled)
	setNestedConfigValue(raw, strings.Split("bridge.feishu.presentation.completion_notification_after_seconds", "."), settings.AfterSeconds)
}

func SaveCompletionNotificationSettings(settings CompletionNotificationSettings) error {
	if err := validateCompletionNotificationSettings(settings); err != nil {
		return err
	}
	return UpdateConfigFile(experimentsConfigPath(), func(raw map[string]any) error {
		setCompletionNotificationSettings(raw, settings)
		return nil
	})
}
