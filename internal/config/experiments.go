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
	// MentionTurnInitiator mirrors
	// bridge.feishu.experimental.mention_turn_initiator and applies live.
	MentionTurnInitiator bool
	// TurnProcessMode selects how new Feishu turns display processing details.
	TurnProcessMode string
	// ReplyEndNotification applies live and bypasses the long-turn threshold.
	ReplyEndNotification bool
	// ModelRecoveryQueueWaitSeconds mirrors
	// bridge.feishu.experimental.model_recovery_queue_wait_seconds and applies live.
	ModelRecoveryQueueWaitSeconds int
}

// ExperimentsSettingsUpdate preserves the latest saved value for experiment
// fields omitted by older cards. Omitted fields are resolved under the file lock.
type ExperimentsSettingsUpdate struct {
	AgentExecutables              map[string]string
	CodexExecutable               *string
	CompletionNotification        *CompletionNotificationSettings
	GroupSettingsEnabled          *bool
	MemoryEnabled                 *bool
	SteerForeground               *bool
	ShowBackgroundTasks           *bool
	MentionTurnInitiator          *bool
	TurnProcessMode               *string
	ReplyEndNotification          *bool
	ModelRecoveryQueueWaitSeconds *int
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
		AgentExecutables:              executableSettings(viper.GetString),
		CodexExecutable:               strings.TrimSpace(viper.GetString(CodexExecutableKey)),
		CompletionNotification:        CompletionNotificationSettings{Enabled: presentation.CompletionNotificationEnabled, AfterSeconds: presentation.CompletionNotificationAfterSeconds},
		GroupSettingsEnabled:          GetBridgeFeishuGroupSettingsEnabled(),
		MemoryEnabled:                 GetMemory().Enabled,
		SteerForeground:               GetBridgeFeishuSteerForeground(),
		ShowBackgroundTasks:           GetBridgeFeishuShowBackgroundTasks(),
		MentionTurnInitiator:          GetBridgeFeishuMentionTurnInitiator(),
		TurnProcessMode:               GetFeishuTurnProcessMode(),
		ReplyEndNotification:          GetBridgeFeishuReplyEndNotification(),
		ModelRecoveryQueueWaitSeconds: GetBridgeFeishuModelRecoveryQueueWaitSeconds(),
	}
}

func experimentsSettingsFromConfig(raw map[string]any) (ExperimentsSettings, error) {
	v := viper.New()
	if err := v.MergeConfigMap(raw); err != nil {
		return ExperimentsSettings{}, err
	}
	return ExperimentsSettings{
		AgentExecutables:              executableSettings(v.GetString),
		CodexExecutable:               strings.TrimSpace(v.GetString(CodexExecutableKey)),
		CompletionNotification:        completionNotificationSettings(v),
		GroupSettingsEnabled:          v.GetBool("bridge.feishu.group_settings.enabled"),
		MemoryEnabled:                 v.GetBool("memory.enabled"),
		SteerForeground:               v.GetBool(FeishuSteerForegroundKey),
		ShowBackgroundTasks:           v.GetBool(FeishuShowBackgroundTasksKey),
		MentionTurnInitiator:          v.GetBool(FeishuMentionTurnInitiatorKey),
		TurnProcessMode:               NormalizeFeishuTurnProcessMode(v.GetString(FeishuTurnProcessModeKey)),
		ReplyEndNotification:          v.GetBool(FeishuReplyEndNotificationKey),
		ModelRecoveryQueueWaitSeconds: modelRecoveryQueueWaitSecondsFromViper(v),
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
	if settings.TurnProcessMode == "" {
		settings.TurnProcessMode = FeishuTurnProcessInline
	}
	_, err := UpdateExperimentsSettings(ExperimentsSettingsUpdate{
		AgentExecutables:              settings.AgentExecutables,
		CodexExecutable:               &settings.CodexExecutable,
		CompletionNotification:        &settings.CompletionNotification,
		GroupSettingsEnabled:          &settings.GroupSettingsEnabled,
		MemoryEnabled:                 &settings.MemoryEnabled,
		SteerForeground:               &settings.SteerForeground,
		ShowBackgroundTasks:           &settings.ShowBackgroundTasks,
		MentionTurnInitiator:          &settings.MentionTurnInitiator,
		TurnProcessMode:               &settings.TurnProcessMode,
		ReplyEndNotification:          &settings.ReplyEndNotification,
		ModelRecoveryQueueWaitSeconds: &settings.ModelRecoveryQueueWaitSeconds,
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
	if update.CompletionNotification != nil {
		if err := validateCompletionNotificationSettings(*update.CompletionNotification); err != nil {
			return ExperimentsSettings{}, err
		}
	}
	if update.ModelRecoveryQueueWaitSeconds != nil {
		if err := ValidateModelRecoveryQueueWaitSeconds(*update.ModelRecoveryQueueWaitSeconds); err != nil {
			return ExperimentsSettings{}, err
		}
	}
	if update.TurnProcessMode != nil && !ValidFeishuTurnProcessMode(*update.TurnProcessMode) {
		return ExperimentsSettings{}, fmt.Errorf("invalid %s", FeishuTurnProcessModeKey)
	}
	var saved ExperimentsSettings
	err := UpdateConfigFile(experimentsConfigPath(), func(raw map[string]any) error {
		for agent, value := range update.AgentExecutables {
			setNestedConfigValue(raw, strings.Split(AgentExecutableKey(agent), "."), strings.TrimSpace(value))
		}
		if update.CompletionNotification != nil {
			setCompletionNotificationSettings(raw, *update.CompletionNotification)
		}
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
		if update.MentionTurnInitiator != nil {
			setNestedConfigValue(raw, strings.Split(FeishuMentionTurnInitiatorKey, "."), *update.MentionTurnInitiator)
		}
		if update.TurnProcessMode != nil {
			setNestedConfigValue(raw, strings.Split(FeishuTurnProcessModeKey, "."), *update.TurnProcessMode)
		}
		if update.ReplyEndNotification != nil {
			setNestedConfigValue(raw, strings.Split(FeishuReplyEndNotificationKey, "."), *update.ReplyEndNotification)
		}
		if update.ModelRecoveryQueueWaitSeconds != nil {
			setNestedConfigValue(raw, strings.Split(FeishuModelRecoveryQueueWaitSecondsKey, "."), *update.ModelRecoveryQueueWaitSeconds)
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
