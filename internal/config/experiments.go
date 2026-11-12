package config

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/spf13/viper"
)

// ExperimentsSettings is the installation-wide experiments form. Saving the
// form is atomic and does not change the running process's startup settings.
type ExperimentsSettings struct {
	AgentExecutables       map[string]string
	CodexExecutable        string
	CompletionNotification CompletionNotificationSettings
	GroupSettingsEnabled   bool
	MemoryEnabled          bool
	// SteerForeground mirrors bridge.feishu.experimental.steer_foreground.
	// Unlike the other fields the bridge applies it live on save.
	SteerForeground bool
	// ShowBackgroundTasks mirrors bridge.feishu.experimental.show_background_tasks
	// (applied live, like SteerForeground).
	ShowBackgroundTasks bool
}

// ExperimentsSettingsUpdate preserves the latest saved value for experiment
// fields omitted by older cards. Omitted fields are resolved under the file lock.
type ExperimentsSettingsUpdate struct {
	AgentExecutables       map[string]string
	CodexExecutable        *string
	CompletionNotification CompletionNotificationSettings
	GroupSettingsEnabled   *bool
	MemoryEnabled          *bool
	SteerForeground        *bool
	ShowBackgroundTasks    *bool
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
		AgentExecutables:       executableSettings(viper.GetString),
		CodexExecutable:        strings.TrimSpace(viper.GetString(CodexExecutableKey)),
		CompletionNotification: CompletionNotificationSettings{Enabled: presentation.CompletionNotificationEnabled, AfterSeconds: presentation.CompletionNotificationAfterSeconds},
		GroupSettingsEnabled:   GetBridgeFeishuGroupSettingsEnabled(),
		MemoryEnabled:          GetMemory().Enabled,
		SteerForeground:        GetBridgeFeishuSteerForeground(),
		ShowBackgroundTasks:    GetBridgeFeishuShowBackgroundTasks(),
	}
}

func experimentsSettingsFromConfig(raw map[string]any) (ExperimentsSettings, error) {
	v := viper.New()
	if err := v.MergeConfigMap(raw); err != nil {
		return ExperimentsSettings{}, err
	}
	return ExperimentsSettings{
		AgentExecutables:       executableSettings(v.GetString),
		CodexExecutable:        strings.TrimSpace(v.GetString(CodexExecutableKey)),
		CompletionNotification: completionNotificationSettings(v),
		GroupSettingsEnabled:   v.GetBool("bridge.feishu.group_settings.enabled"),
		MemoryEnabled:          v.GetBool("memory.enabled"),
		SteerForeground:        v.GetBool(FeishuSteerForegroundKey),
		ShowBackgroundTasks:    v.GetBool(FeishuShowBackgroundTasksKey),
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
		AgentExecutables:       settings.AgentExecutables,
		CodexExecutable:        &settings.CodexExecutable,
		CompletionNotification: settings.CompletionNotification,
		GroupSettingsEnabled:   &settings.GroupSettingsEnabled,
		MemoryEnabled:          &settings.MemoryEnabled,
		SteerForeground:        &settings.SteerForeground,
		ShowBackgroundTasks:    &settings.ShowBackgroundTasks,
	})
	return err
}

// UpdateExperimentsSettings validates the complete update before atomically
// writing the file. It does not mutate the running process's Viper state.
func UpdateExperimentsSettings(update ExperimentsSettingsUpdate) (ExperimentsSettings, error) {
	for agent, value := range update.AgentExecutables {
		if agent == "codex" || !SupportsAgentExecutable(agent) || !ValidAgentExecutable(value) {
			return ExperimentsSettings{}, fmt.Errorf("invalid executable integration %q", agent)
		}
	}
	if update.CodexExecutable != nil && !ValidAgentExecutable(*update.CodexExecutable) {
		return ExperimentsSettings{}, fmt.Errorf("invalid %s", CodexExecutableKey)
	}
	if err := validateCompletionNotificationSettings(update.CompletionNotification); err != nil {
		return ExperimentsSettings{}, err
	}
	var saved ExperimentsSettings
	err := UpdateConfigFile(experimentsConfigPath(), func(raw map[string]any) error {
		for agent, value := range update.AgentExecutables {
			setNestedConfigValue(raw, strings.Split(AgentExecutableKey(agent), "."), strings.TrimSpace(value))
		}
		setCompletionNotificationSettings(raw, update.CompletionNotification)
		if update.CodexExecutable != nil {
			setNestedConfigValue(raw, strings.Split(CodexExecutableKey, "."), strings.TrimSpace(*update.CodexExecutable))
		}
		if update.GroupSettingsEnabled != nil {
			setNestedConfigValue(raw, strings.Split("bridge.feishu.group_settings.enabled", "."), *update.GroupSettingsEnabled)
		}
		if update.MemoryEnabled != nil {
			setNestedConfigValue(raw, strings.Split("memory.enabled", "."), *update.MemoryEnabled)
		}
		if update.SteerForeground != nil {
			setNestedConfigValue(raw, strings.Split(FeishuSteerForegroundKey, "."), *update.SteerForeground)
		}
		if update.ShowBackgroundTasks != nil {
			setNestedConfigValue(raw, strings.Split(FeishuShowBackgroundTasksKey, "."), *update.ShowBackgroundTasks)
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

func executableSettings(get func(string) string) map[string]string {
	var values map[string]string
	for _, agent := range AgentExecutableNames() {
		if agent != "codex" {
			if values == nil {
				values = map[string]string{}
			}
			values[agent] = strings.TrimSpace(get(AgentExecutableKey(agent)))
		}
	}
	return values
}
