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
	MemoryEnabled          bool
}

// ExperimentsSettingsUpdate preserves the latest saved value for experiment
// fields omitted by older cards. Omitted fields are resolved under the file lock.
type ExperimentsSettingsUpdate struct {
	CompletionNotification CompletionNotificationSettings
	GroupSettingsEnabled   *bool
	MemoryEnabled          *bool
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
		MemoryEnabled:          GetMemory().Enabled,
	}
}

func experimentsSettingsFromConfig(raw map[string]any) (ExperimentsSettings, error) {
	v := viper.New()
	if err := v.MergeConfigMap(raw); err != nil {
		return ExperimentsSettings{}, err
	}
	return ExperimentsSettings{
		CompletionNotification: completionNotificationSettings(v),
		GroupSettingsEnabled:   v.GetBool("bridge.feishu.group_settings.enabled"),
		MemoryEnabled:          v.GetBool("memory.enabled"),
	}, nil
}

func ReadExperimentsSettings() (ExperimentsSettings, error) {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	raw, err := readConfigFileForWrite(experimentsConfigPath())
	if err != nil {
		return ExperimentsSettings{}, err
	}
	return experimentsSettingsFromConfig(raw)
}

// SaveExperimentsSettings replaces all experiment settings. Form callbacks use
// UpdateExperimentsSettings so omitted fields do not overwrite newer settings.
func SaveExperimentsSettings(settings ExperimentsSettings) error {
	_, err := UpdateExperimentsSettings(ExperimentsSettingsUpdate{
		CompletionNotification: settings.CompletionNotification,
		GroupSettingsEnabled:   &settings.GroupSettingsEnabled,
		MemoryEnabled:          &settings.MemoryEnabled,
	})
	return err
}

// UpdateExperimentsSettings validates the complete update before atomically
// writing the file. It does not mutate the running process's Viper state.
func UpdateExperimentsSettings(update ExperimentsSettingsUpdate) (ExperimentsSettings, error) {
	if err := validateCompletionNotificationSettings(update.CompletionNotification); err != nil {
		return ExperimentsSettings{}, err
	}
	var saved ExperimentsSettings
	err := UpdateConfigFile(experimentsConfigPath(), func(raw map[string]any) error {
		setCompletionNotificationSettings(raw, update.CompletionNotification)
		if update.GroupSettingsEnabled != nil {
			setNestedConfigValue(raw, strings.Split("bridge.feishu.group_settings.enabled", "."), *update.GroupSettingsEnabled)
		}
		if update.MemoryEnabled != nil {
			setNestedConfigValue(raw, strings.Split("memory.enabled", "."), *update.MemoryEnabled)
		}
		var err error
		saved, err = experimentsSettingsFromConfig(raw)
		return err
	})
	if err != nil {
		return ExperimentsSettings{}, err
	}
	return saved, nil
}
