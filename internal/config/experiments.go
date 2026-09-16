package config

import (
	"strings"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/spf13/viper"
)

// ExperimentsSettings is the installation-wide experiments form. Saving the
// form is atomic and does not change the running process's startup settings.
type ExperimentsSettings struct {
	CompletionNotification CompletionNotificationSettings
	GroupSettingsEnabled   bool
}

func experimentsConfigPath() string {
	if path := strings.TrimSpace(viper.ConfigFileUsed()); path != "" {
		return path
	}
	return amuxhome.ConfigPath()
}

func GetBridgeFeishuGroupSettingsEnabled() bool {
	return viper.GetBool("bridge.feishu.group_settings.enabled")
}

func GetExperimentsSettings() ExperimentsSettings {
	presentation := GetBridgeFeishuPresentation()
	return ExperimentsSettings{
		CompletionNotification: CompletionNotificationSettings{Enabled: presentation.CompletionNotificationEnabled, AfterSeconds: presentation.CompletionNotificationAfterSeconds},
		GroupSettingsEnabled:   GetBridgeFeishuGroupSettingsEnabled(),
	}
}

func ReadExperimentsSettings() (ExperimentsSettings, error) {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	raw, err := readConfigFileForWrite(experimentsConfigPath())
	if err != nil {
		return ExperimentsSettings{}, err
	}
	v := viper.New()
	if err := v.MergeConfigMap(raw); err != nil {
		return ExperimentsSettings{}, err
	}
	return ExperimentsSettings{
		CompletionNotification: completionNotificationSettings(v),
		GroupSettingsEnabled:   v.GetBool("bridge.feishu.group_settings.enabled"),
	}, nil
}

func SaveExperimentsSettings(settings ExperimentsSettings) error {
	if err := validateCompletionNotificationSettings(settings.CompletionNotification); err != nil {
		return err
	}
	return UpdateConfigFile(experimentsConfigPath(), func(raw map[string]any) error {
		setCompletionNotificationSettings(raw, settings.CompletionNotification)
		setNestedConfigValue(raw, strings.Split("bridge.feishu.group_settings.enabled", "."), settings.GroupSettingsEnabled)
		return nil
	})
}
