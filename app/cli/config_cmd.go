package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/session"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Manage amux configuration",
	}
	c.AddCommand(
		supportsJSONOutput(&cobra.Command{
			Use:               "path",
			Short:             "Print config file path",
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				path := defaultConfigPath()
				if jsonOutputEnabled(cmd) {
					return printJSON(cmd.OutOrStdout(), map[string]any{
						"schema": "amux.system.config.path.v1",
						"path":   path,
					})
				}
				fmt.Fprintln(cmd.OutOrStdout(), path)
				return nil
			},
		}),
		supportsJSONOutput(&cobra.Command{
			Use:               "show",
			Short:             "Print config with shared environment values redacted",
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				if jsonOutputEnabled(cmd) {
					cfg, err := readCLIConfigFile()
					if err != nil {
						return err
					}
					redactIsolationGlobalEnvForDisplay(cfg)
					return printJSON(cmd.OutOrStdout(), map[string]any{
						"schema": "amux.system.config.show.v1",
						"config": map[string]any(cfg),
					})
				}
				data, err := os.ReadFile(defaultConfigPath())
				if err != nil {
					return err
				}
				var raw map[string]any
				if err := toml.Unmarshal(data, &raw); err != nil {
					return commandValidationError(fmt.Errorf("parse config %s: %w", defaultConfigPath(), err))
				}
				if redactIsolationGlobalEnvForDisplay(cliConfigFile(raw)) {
					data, err = toml.Marshal(raw)
					if err != nil {
						return err
					}
				}
				_, err = cmd.OutOrStdout().Write(data)
				return err
			},
		}),
		supportsJSONOutput(&cobra.Command{
			Use:               "get <key>",
			Short:             "Get config value",
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completeConfigGetUnsetArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if args[0] == "isolation.global_env" {
					return commandValidationError(fmt.Errorf("isolation.global_env is sensitive; use `amux isolation global-env ls` to list names without values"))
				}
				if !distributionConfigReadAllowed(args[0]) {
					return commandValidationError(fmt.Errorf("unsupported config key %q", args[0]))
				}
				cfg, err := readCLIConfigFile()
				if err != nil {
					return err
				}
				value, ok := nestedConfigValue(map[string]any(cfg), strings.Split(args[0], "."))
				if !ok {
					return fmt.Errorf("config key %q is not set", args[0])
				}
				if jsonOutputEnabled(cmd) {
					return printJSON(cmd.OutOrStdout(), map[string]any{
						"schema": "amux.system.config.get.v1",
						"key":    args[0],
						"value":  value,
					})
				}
				fmt.Fprintln(cmd.OutOrStdout(), value)
				return nil
			},
		}),
		&cobra.Command{
			Use:               "set <key> <value>",
			Short:             "Set config value",
			Args:              cobra.ExactArgs(2),
			ValidArgsFunction: completeConfigSetArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
					cfg := cliConfigFile(raw)
					if err := setConfigValueLocale(cfg, args[0], args[1], commandLocale(cmd)); err != nil {
						return err
					}
					return validateCLIGroupWatchInteractionCompatibility(cfg, args[0])
				})
			},
		},
		&cobra.Command{
			Use:               "unset <key>",
			Short:             "Unset config value",
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completeConfigGetUnsetArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
					cfg := cliConfigFile(raw)
					if err := unsetConfigValue(cfg, args[0]); err != nil {
						return err
					}
					return validateCLIGroupWatchInteractionCompatibility(cfg, "")
				})
			},
		},
	)
	return c
}

func defaultConfigPath() string {
	return filepath.Join(defaultAmuxDir(), "config.toml")
}

type cliConfigFile map[string]any

var configProfileNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func readCLIConfigFile() (cliConfigFile, error) {
	path := defaultConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cliConfigFile{}, nil
		}
		return nil, err
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, commandValidationError(fmt.Errorf("parse config %s: %w", path, err))
	}
	return cliConfigFile(cfg), nil
}

func configValue(cfg cliConfigFile, key string) (string, bool) {
	value, ok := nestedConfigValue(map[string]any(cfg), strings.Split(key, "."))
	if !ok {
		return "", false
	}
	return fmt.Sprint(value), true
}

func validateCLIGroupWatchInteractionCompatibility(cfg cliConfigFile, candidateModeKey string) error {
	profiles := map[string]struct{}{feishuProfileOrDefault(""): {}}
	if rawProfiles, ok := nestedConfigValue(map[string]any(cfg), []string{"bridge", "feishu", "profiles"}); ok {
		if profileMap, ok := rawProfiles.(map[string]any); ok {
			for profile := range profileMap {
				if configProfileNamePattern.MatchString(profile) {
					profiles[profile] = struct{}{}
				}
			}
		}
	}
	secretProfiles, err := bridge.ListSecretProfiles()
	if err != nil {
		return commandValidationError(fmt.Errorf("list Feishu profiles: %w", err))
	}
	for _, profile := range secretProfiles {
		profiles[profile] = struct{}{}
	}
	for profile := range profiles {
		key := "bridge.feishu.profiles." + profile + ".interaction.mode"
		mode := cliFeishuInteractionMode(cfg, profile, candidateModeKey == key)
		watch := cliFeishuGroupWatchConfig(cfg, profile)
		err := config.ValidateBridgeFeishuGroupWatchInteraction(watch, mode)
		if fileExists(bridge.DefaultDBPath()) {
			if secrets, loadErr := bridge.LoadProfileSecrets(profile); loadErr == nil && strings.TrimSpace(secrets.AppId) != "" {
				err = bridge.ValidateGroupWatchInteractionInDB(bridge.DefaultDBPath(), profile, bridge.BridgeIdFromAppId(secrets.AppId), watch, mode, configBoolValue(cfg, "bridge.feishu.group_settings.enabled"))
			}
		}
		if err != nil {
			return commandValidationError(fmt.Errorf("Feishu profile %s: %w", profile, err))
		}
	}
	return nil
}

func cliFeishuInteractionMode(cfg cliConfigFile, profile string, preferCandidate bool) string {
	mode := config.FeishuInteractionThread
	key := "bridge.feishu.profiles." + profile + ".interaction.mode"
	if rawMode, ok := nestedConfigValue(map[string]any(cfg), strings.Split(key, ".")); ok {
		mode = strings.TrimSpace(fmt.Sprint(rawMode))
	}
	if preferCandidate {
		return mode
	}
	if !fileExists(bridge.DefaultDBPath()) {
		return mode
	}
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil || strings.TrimSpace(secrets.AppId) == "" {
		return mode
	}
	state, err := bridge.GetBotProfileStateFromDB(bridge.DefaultDBPath(), bridge.BridgeIdFromAppId(secrets.AppId), mode)
	if err != nil || strings.TrimSpace(state.InteractionMode) == "" {
		return mode
	}
	return state.InteractionMode
}

func setConfigValue(cfg cliConfigFile, key, raw string) error {
	return setConfigValueLocale(cfg, key, raw, i18n.English)
}

func setConfigValueLocale(cfg cliConfigFile, key, raw string, locale i18n.Locale) error {
	parts := strings.Split(key, ".")
	if key == config.CodexExecutableKey && !config.ValidCodexExecutable(raw) {
		return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "config.codex_executable.invalid")))
	}
	if !supportedConfigKey(key) {
		return commandValidationError(fmt.Errorf("unsupported config key %q", key))
	}
	documentCommentsField := parseFeishuProfileDocumentCommentsKey(key)
	if documentCommentsField == "agent_profile_ref" || documentCommentsField == "runtime_ref" || documentCommentsField == "model_ref" || documentCommentsField == "reasoning_effort" {
		agentRef := profileCapabilitySiblingValue(cfg, parts, "agent_profile_ref")
		runtimeRef := profileCapabilitySiblingValue(cfg, parts, "runtime_ref")
		modelRef := profileCapabilitySiblingValue(cfg, parts, "model_ref")
		reasoningEffort := profileCapabilitySiblingValue(cfg, parts, "reasoning_effort")
		switch documentCommentsField {
		case "agent_profile_ref":
			agentRef = strings.TrimSpace(raw)
		case "runtime_ref":
			runtimeRef = strings.TrimSpace(raw)
		case "model_ref":
			modelRef = strings.TrimSpace(raw)
		case "reasoning_effort":
			reasoningEffort = strings.TrimSpace(raw)
		}
		if err := validateFeishuCapabilityExecutionSelection(agentRef, runtimeRef, modelRef, reasoningEffort); err != nil {
			return commandValidationError(fmt.Errorf("%s: %w", key, err))
		}
	} else if groupWatchField := parseFeishuProfileGroupWatchKey(key); groupWatchField == "agent_profile_ref" || groupWatchField == "runtime_ref" || groupWatchField == "model_ref" || groupWatchField == "reasoning_effort" {
		agentRef := profileCapabilitySiblingValue(cfg, parts, "agent_profile_ref")
		runtimeRef := profileCapabilitySiblingValue(cfg, parts, "runtime_ref")
		modelRef := profileCapabilitySiblingValue(cfg, parts, "model_ref")
		reasoningEffort := profileCapabilitySiblingValue(cfg, parts, "reasoning_effort")
		switch groupWatchField {
		case "agent_profile_ref":
			agentRef = strings.TrimSpace(raw)
		case "runtime_ref":
			runtimeRef = strings.TrimSpace(raw)
		case "model_ref":
			modelRef = strings.TrimSpace(raw)
		case "reasoning_effort":
			reasoningEffort = strings.TrimSpace(raw)
		}
		if err := validateFeishuCapabilityExecutionSelection(agentRef, runtimeRef, modelRef, reasoningEffort); err != nil {
			return commandValidationError(fmt.Errorf("%s: %w", key, err))
		}
	}
	var value any = raw
	if parsed, handled, err := parseDistributionConfigValue(key, raw); handled {
		if err != nil {
			if localized, ok := err.(interface{ LocalizedMessage(string) string }); ok {
				err = errors.New(localized.LocalizedMessage(string(locale)))
			}
			return commandValidationError(err)
		}
		value = parsed
	}
	if key == config.DailyCleanupEnabledKey {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "config.boolean", key)))
		}
		value = parsed
	}
	if key == "maintenance.daily_cleanup.at" {
		if _, err := time.Parse("15:04", raw); err != nil || len(raw) != 5 {
			return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "cleanup.invalid_time")))
		}
	}
	if key == "maintenance.daily_cleanup.protected_paths" {
		paths := strings.Fields(raw)
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "cleanup.invalid_paths")))
			}
		}
		value = paths
	}
	if key == "hub.addr" {
		if err := validateHubAddress(raw); err != nil {
			return err
		}
	}
	if key == "runtime.auto_start" ||
		key == "bridge.feishu.prompt.shared_thread_enabled" ||
		key == "bridge.feishu.prompt.topic_root_enabled" ||
		key == "bridge.feishu.document_comments.enabled" ||
		parseFeishuProfileDocumentCommentsKey(key) == "enabled" ||
		key == "bridge.feishu.bot_to_bot.enabled" ||
		parseFeishuProfileBotToBotKey(key) == "enabled" ||
		key == "bridge.feishu.group_watch.enabled" ||
		key == "bridge.feishu.group_watch.dedup.enabled" ||
		parseFeishuProfileGroupWatchKey(key) == "enabled" ||
		parseFeishuProfileGroupWatchKey(key) == "dedup.enabled" ||
		key == "mcp.enabled" ||
		key == "memory.enabled" ||
		key == "bridge.feishu.presentation.completion_notification_enabled" ||
		key == "bridge.feishu.group_settings.enabled" ||
		key == "bridge.feishu.experimental.steer_foreground" ||
		key == "mcp.auto_inject" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			if groupWatchConfigField(key) == "dedup.enabled" {
				return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "config.boolean", key)))
			}
			return commandValidationError(fmt.Errorf("%s must be true or false", key))
		}
		value = parsed
	}
	if isIdleTimeoutConfigKey(key) {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return commandValidationError(fmt.Errorf("%s must be an integer number of seconds", key))
		}
		if err := session.ValidateIdleTimeoutSeconds(parsed); err != nil {
			return commandValidationError(err)
		}
		if groupWatchConfigField(key) == "idle_timeout_seconds" && (parsed < 60 || parsed > config.MaxFeishuGroupWatchIdleTimeoutSeconds) {
			return commandValidationError(fmt.Errorf("%s must be an integer from 60 to %d", key, config.MaxFeishuGroupWatchIdleTimeoutSeconds))
		}
		value = parsed
	}
	if key == "runtime.max_sessions" || key == "runtime.max_concurrency" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return commandValidationError(fmt.Errorf("%s must be a non-negative integer", key))
		}
		value = int64(parsed)
	}
	if isCompletionNotificationThresholdConfigKey(key) {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return commandValidationError(fmt.Errorf("%s must be a non-negative integer", key))
		}
		value = int64(parsed)
	}
	if key == "hub.allowed_targets" {
		value = strings.Fields(raw)
	}
	if field := groupWatchConfigField(key); field == "chat_ids" || field == "sender_types" {
		values := strings.Fields(raw)
		if field == "sender_types" {
			for i, senderType := range values {
				values[i] = strings.ToLower(strings.TrimSpace(senderType))
				if values[i] != "user" && values[i] != "app" {
					return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "config.sender_types", key)))
				}
			}
		}
		value = values
	}
	if groupWatchChatSenderTypesKey(key) {
		values := strings.Fields(raw)
		for i, senderType := range values {
			values[i] = strings.ToLower(strings.TrimSpace(senderType))
			if values[i] != "user" && values[i] != "app" {
				return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "config.sender_types", key)))
			}
		}
		value = values
	}
	if parseFeishuProfileBotE2EKey(key) == "sender_open_ids" {
		value = uniqueConfigFields(raw)
	}
	if parseFeishuProfileBotE2EKey(key) == "sender_roles" {
		parsed, err := config.ParseSenderRolesCLI(raw)
		if err != nil {
			return commandValidationError(err)
		}
		value = parsed
	}
	if field := groupWatchConfigField(key); field == "poll_interval_seconds" || field == "lookback_seconds" {
		parsed, err := strconv.Atoi(raw)
		minimum, maximum := 5, 3600
		if field == "poll_interval_seconds" {
			maximum = 300
		}
		if err != nil || parsed < minimum || parsed > maximum {
			return commandValidationError(fmt.Errorf("%s must be an integer from %d to %d", key, minimum, maximum))
		}
		value = int64(parsed)
	}
	if groupWatchConfigField(key) == "dedup.window_seconds" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 60 || parsed > config.MaxFeishuGroupWatchDedupWindowSeconds {
			return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "config.integer_range", key, 60, config.MaxFeishuGroupWatchDedupWindowSeconds)))
		}
		value = int64(parsed)
	}
	if groupWatchConfigField(key) == "max_active_sessions_per_chat" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > config.MaxFeishuGroupWatchActiveSessionsPerChat {
			return commandValidationError(fmt.Errorf("%s must be an integer from 1 to %d", key, config.MaxFeishuGroupWatchActiveSessionsPerChat))
		}
		value = int64(parsed)
	}
	if isWorkspaceDefaultsArgsConfigKey(key) {
		value = strings.Fields(raw)
	}
	if key == "bridge.feishu.bot_to_bot.max_turns_per_thread" || parseFeishuProfileBotToBotKey(key) == "max_turns_per_thread" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return commandValidationError(fmt.Errorf("%s must be a positive integer", key))
		}
		value = int64(parsed)
	}
	if parseFeishuProfileInteractionModeKey(key) {
		mode := strings.TrimSpace(raw)
		if mode != "thread" && mode != "singleton" {
			return commandValidationError(fmt.Errorf("%s must be thread or singleton", key))
		}
		value = mode
	}
	if isThinkingProcessConfigKey(key) {
		mode := strings.TrimSpace(raw)
		if err := config.ValidateFeishuThinkingProcess(mode); err != nil {
			return commandValidationError(fmt.Errorf("%s: %w", key, err))
		}
		value = mode
	}
	m := map[string]any(cfg)
	for _, part := range parts[:len(parts)-1] {
		child, _ := m[part].(map[string]any)
		if child == nil {
			child = map[string]any{}
			m[part] = child
		}
		m = child
	}
	m[parts[len(parts)-1]] = value
	if key == "runtime.id" {
		runtime, _ := map[string]any(cfg)["runtime"].(map[string]any)
		if runtime != nil {
			if _, ok := runtime["name"]; !ok {
				runtime["name"] = raw
			}
		}
	}
	return nil
}

func uniqueConfigFields(raw string) []string {
	values := make([]string, 0)
	seen := make(map[string]bool)
	for _, value := range strings.Fields(raw) {
		if seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	return values
}

func unsetConfigValue(cfg cliConfigFile, key string) error {
	if !supportedConfigKey(key) {
		return commandValidationError(fmt.Errorf("unsupported config key %q", key))
	}
	parts := strings.Split(key, ".")
	if parseFeishuProfileExecutionKey(key) == "agent_profile_ref" {
		if profileCapabilitySiblingValue(cfg, parts, "runtime_ref") != "" ||
			profileCapabilitySiblingValue(cfg, parts, "model_ref") != "" ||
			profileCapabilitySiblingValue(cfg, parts, "reasoning_effort") != "" {
			return commandValidationError(fmt.Errorf("unset execution fields first before unsetting %s", key))
		}
	}
	m := map[string]any(cfg)
	for _, part := range parts[:len(parts)-1] {
		child, _ := m[part].(map[string]any)
		if child == nil {
			return nil
		}
		m = child
	}
	delete(m, parts[len(parts)-1])
	return nil
}

func profileCapabilitySiblingValue(cfg cliConfigFile, keyParts []string, field string) string {
	parts := append([]string(nil), keyParts...)
	parts[len(parts)-1] = field
	value, ok := nestedConfigValue(map[string]any(cfg), parts)
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func validateFeishuCapabilityExecutionConfig(agentRef, modelRef string) error {
	agentRef = strings.TrimSpace(agentRef)
	modelRef = strings.TrimSpace(modelRef)
	if agentRef == "" {
		if modelRef != "" {
			return fmt.Errorf("model_ref requires agent_profile_ref to be set first")
		}
		return nil
	}
	profile, err := resolveAgentProfile(agentRef)
	if err != nil {
		return fmt.Errorf("agent_profile_ref %q is unavailable: %w", agentRef, err)
	}
	if modelRef == "" {
		return nil
	}
	if _, err := agentprofile.ResolveModelSelection(profile, modelRef); err != nil {
		return fmt.Errorf("model_ref %q is not supported by agent_profile_ref %q: %w", modelRef, agentRef, err)
	}
	return nil
}

func validateFeishuCapabilityExecutionSelection(agentRef, runtimeRef, modelRef, reasoningEffort string) error {
	agentRef = strings.TrimSpace(agentRef)
	runtimeRef = strings.TrimSpace(runtimeRef)
	modelRef = strings.TrimSpace(modelRef)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if agentRef == "" {
		if runtimeRef != "" || modelRef != "" || reasoningEffort != "" {
			return fmt.Errorf("execution override requires agent_profile_ref to be set first")
		}
		return nil
	}
	if _, err := resolveAgentProfile(agentRef); err != nil {
		return fmt.Errorf("agent_profile_ref %q is unavailable: %w", agentRef, err)
	}
	if runtimeRef == "" {
		if reasoningEffort != "" {
			return fmt.Errorf("reasoning_effort requires runtime_ref to be set first")
		}
		return validateFeishuCapabilityExecutionConfig(agentRef, modelRef)
	}
	runtime, err := agentprofile.ResolveRuntimeOption(runtimeRef)
	if err != nil {
		return err
	}
	model, err := agentprofile.ResolveRuntimeModel(runtime, modelRef)
	if err != nil {
		return err
	}
	return agentprofile.ValidateRuntimeReasoningEffort(runtime, model, reasoningEffort)
}

func validateHubAddress(raw string) error {
	address := strings.TrimSpace(raw)
	if address == "" {
		return nil
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return commandValidationError(fmt.Errorf("hub.addr must be a valid host:port address: %q", raw))
	}
	if strings.ContainsAny(host, " \t\r\n/") {
		return commandValidationError(fmt.Errorf("hub.addr host is invalid in %q", raw))
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return commandValidationError(fmt.Errorf("hub.addr port must be an integer from 1 to 65535: %q", portText))
	}
	return nil
}

func validatedConfiguredHubAddress() (string, error) {
	cfg, err := readCLIConfigFile()
	if err != nil {
		return "", err
	}
	address, ok := configValue(cfg, "hub.addr")
	if !ok || strings.TrimSpace(address) == "" {
		address = strings.TrimSpace(os.Getenv("AMUX_HUB_ADDR"))
	}
	if address == "" {
		address = defaultLocalHubAddr
	}
	if err := validateHubAddress(address); err != nil {
		return "", err
	}
	return strings.TrimSpace(address), nil
}

func nestedConfigValue(m map[string]any, parts []string) (any, bool) {
	if len(parts) == 0 {
		return nil, false
	}
	for _, part := range parts[:len(parts)-1] {
		child, _ := m[part].(map[string]any)
		if child == nil {
			return nil, false
		}
		m = child
	}
	value, ok := m[parts[len(parts)-1]]
	return value, ok
}

func supportedConfigKeys() []string {
	return config.ManagedConfigKeys()
}

func supportedConfigKey(key string) bool {
	for _, supported := range supportedConfigKeys() {
		if strings.Contains(supported, "<profile>") || strings.Contains(supported, "<label>") || strings.Contains(supported, "<chat_id>") {
			continue
		}
		if key == supported {
			return true
		}
	}
	if isRuntimeLabelKey(key) {
		return true
	}
	if groupWatchChatSenderTypesKey(key) {
		return true
	}
	return parseFeishuProfileWorkspaceDefaultsKey(key) != "" ||
		parseFeishuProfileInteractionModeKey(key) ||
		parseFeishuProfilePresentationKey(key) != "" ||
		parseFeishuProfileBotE2EKey(key) != "" ||
		parseFeishuProfileDocumentCommentsKey(key) != "" ||
		parseFeishuProfileBotToBotKey(key) != "" ||
		parseFeishuProfileGroupWatchKey(key) != ""
}

// isRuntimeLabelKey reports whether key is a runtime scheduling label
// (runtime.labels.<name>), the nested-map config the registry advertises.
func isRuntimeLabelKey(key string) bool {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "runtime" || parts[1] != "labels" {
		return false
	}
	return configProfileNamePattern.MatchString(parts[2])
}

func parseFeishuProfileWorkspaceDefaultsKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 6 {
		return ""
	}
	if parts[0] != "bridge" || parts[1] != "feishu" || parts[2] != "profiles" || parts[4] != "workspace_defaults" {
		return ""
	}
	if !configProfileNamePattern.MatchString(parts[3]) {
		return ""
	}
	switch parts[5] {
	case "agent", "target", "cwd", "model", "args", "idle_timeout_seconds":
		return parts[5]
	default:
		return ""
	}
}

func parseFeishuProfileInteractionModeKey(key string) bool {
	parts := strings.Split(key, ".")
	if len(parts) != 6 {
		return false
	}
	return parts[0] == "bridge" &&
		parts[1] == "feishu" &&
		parts[2] == "profiles" &&
		configProfileNamePattern.MatchString(parts[3]) &&
		parts[4] == "interaction" &&
		parts[5] == "mode"
}

func parseFeishuProfilePresentationKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 6 {
		return ""
	}
	if parts[0] != "bridge" || parts[1] != "feishu" || parts[2] != "profiles" ||
		!configProfileNamePattern.MatchString(parts[3]) || parts[4] != "presentation" {
		return ""
	}
	if parts[5] == "thinking_process" || parts[5] == "completion_notification_after_seconds" {
		return parts[5]
	}
	return ""
}

func parseFeishuProfileBotE2EKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 6 {
		return ""
	}
	if parts[0] != "bridge" || parts[1] != "feishu" || parts[2] != "profiles" ||
		!configProfileNamePattern.MatchString(parts[3]) || parts[4] != "bot_e2e" {
		return ""
	}
	switch parts[5] {
	case "sender_open_ids", "sender_roles":
		return parts[5]
	default:
		return ""
	}
}

func parseFeishuProfileDocumentCommentsKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 6 {
		return ""
	}
	if parts[0] != "bridge" || parts[1] != "feishu" || parts[2] != "profiles" ||
		!configProfileNamePattern.MatchString(parts[3]) || parts[4] != "document_comments" {
		return ""
	}
	switch parts[5] {
	case "enabled", "agent_profile_ref", "runtime_ref", "model_ref", "reasoning_effort":
		return parts[5]
	default:
		return ""
	}
}

func parseFeishuProfileBotToBotKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 6 {
		return ""
	}
	if parts[0] != "bridge" || parts[1] != "feishu" || parts[2] != "profiles" ||
		!configProfileNamePattern.MatchString(parts[3]) || parts[4] != "bot_to_bot" {
		return ""
	}
	switch parts[5] {
	case "enabled", "max_turns_per_thread":
		return parts[5]
	default:
		return ""
	}
}

func parseFeishuProfileGroupWatchKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 6 && len(parts) != 7 {
		return ""
	}
	if parts[0] != "bridge" ||
		parts[1] != "feishu" ||
		parts[2] != "profiles" ||
		!configProfileNamePattern.MatchString(parts[3]) ||
		parts[4] != "group_watch" {
		return ""
	}
	if len(parts) == 7 {
		if parts[5] == "dedup" && (parts[6] == "enabled" || parts[6] == "window_seconds") {
			return "dedup." + parts[6]
		}
		if parts[5] == "chat_sender_types" && configProfileNamePattern.MatchString(parts[6]) {
			return "chat_sender_types"
		}
		return ""
	}
	switch parts[5] {
	case "enabled", "chat_ids", "sender_types", "poll_interval_seconds", "lookback_seconds", "max_active_sessions_per_chat", "idle_timeout_seconds", "agent_profile_ref", "runtime_ref", "model_ref", "reasoning_effort", "thinking_process":
		return parts[5]
	default:
		return ""
	}
}

func isThinkingProcessConfigKey(key string) bool {
	return key == "bridge.feishu.presentation.thinking_process" ||
		key == "bridge.feishu.group_watch.thinking_process" ||
		parseFeishuProfilePresentationKey(key) == "thinking_process" ||
		parseFeishuProfileGroupWatchKey(key) == "thinking_process"
}

func isCompletionNotificationThresholdConfigKey(key string) bool {
	return key == "bridge.feishu.presentation.completion_notification_after_seconds" ||
		parseFeishuProfilePresentationKey(key) == "completion_notification_after_seconds"
}

func parseFeishuProfileExecutionKey(key string) string {
	if field := parseFeishuProfileDocumentCommentsKey(key); field == "agent_profile_ref" || field == "runtime_ref" || field == "model_ref" || field == "reasoning_effort" {
		return field
	}
	if field := parseFeishuProfileGroupWatchKey(key); field == "agent_profile_ref" || field == "runtime_ref" || field == "model_ref" || field == "reasoning_effort" {
		return field
	}
	return ""
}

func groupWatchConfigField(key string) string {
	const prefix = "bridge.feishu.group_watch."
	if strings.HasPrefix(key, prefix) {
		if globalGroupWatchChatSenderTypesKey(key) {
			return "chat_sender_types"
		}
		switch field := strings.TrimPrefix(key, prefix); field {
		case "enabled", "chat_ids", "sender_types", "poll_interval_seconds", "lookback_seconds", "dedup.enabled", "dedup.window_seconds", "max_active_sessions_per_chat", "idle_timeout_seconds", "thinking_process":
			return field
		}
	}
	field := parseFeishuProfileGroupWatchKey(key)
	if field == "agent_profile_ref" || field == "runtime_ref" || field == "model_ref" || field == "reasoning_effort" {
		return ""
	}
	return field
}

func groupWatchChatSenderTypesKey(key string) bool {
	return globalGroupWatchChatSenderTypesKey(key) || parseFeishuProfileGroupWatchKey(key) == "chat_sender_types"
}

func globalGroupWatchChatSenderTypesKey(key string) bool {
	const prefix = "bridge.feishu.group_watch.chat_sender_types."
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	chatID := strings.TrimPrefix(key, prefix)
	return configProfileNamePattern.MatchString(chatID)
}

func isWorkspaceDefaultsArgsConfigKey(key string) bool {
	if key == "bridge.feishu.workspace_defaults.args" {
		return true
	}
	return parseFeishuProfileWorkspaceDefaultsKey(key) == "args"
}

func isIdleTimeoutConfigKey(key string) bool {
	return key == "runtime.idle_timeout_seconds" ||
		key == "bridge.feishu.workspace_defaults.idle_timeout_seconds" ||
		groupWatchConfigField(key) == "idle_timeout_seconds" ||
		parseFeishuProfileWorkspaceDefaultsKey(key) == "idle_timeout_seconds"
}
