package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/doclang"
	"github.com/amuxos/amux/internal/webconsole"
)

// The settings page is generated from the configuration reference, the same
// metadata as `amux system config` and the generated documentation. The browser
// never sees secrets (hub.token, isolation.global_env), executable or file paths,
// raw TOML, or per-Bot and per-group keys, which have their own pages. Keys that
// change identity, connectivity, admission or host paths are shown read-only.
var consoleSettingsReadOnly = map[string]bool{
	"hub.addr":                                  true,
	"hub.allowed_targets":                       true,
	"runtime.id":                                true,
	"bridge.feishu.workspace_defaults.args":     true,
	"maintenance.daily_cleanup.protected_paths": true,
}

var (
	consoleLeadingInteger = regexp.MustCompile(`^-?\d+`)
	consoleClockTime      = regexp.MustCompile(`^\d{2}:\d{2}$`)
)

func consoleSettingHidden(option config.ConfigOption) bool {
	return option.Sensitive || strings.ContainsAny(option.Key, "<>") || config.IsAgentExecutableKey(option.Key) ||
		option.Type == "path" || strings.HasPrefix(option.Type, "map") || strings.Contains(option.Type, "map entry")
}

func consoleSettingOption(key string) (config.ConfigOption, bool) {
	for _, option := range config.ConfigOptions() {
		if option.Key == key && !consoleSettingHidden(option) {
			return option, true
		}
	}
	return config.ConfigOption{}, false
}

func consoleSettingWritable(option config.ConfigOption) bool {
	return option.Managed && !consoleSettingsReadOnly[option.Key]
}

// consoleSettingKind is the control the browser renders for a setting.
func consoleSettingKind(option config.ConfigOption) string {
	switch {
	case option.Type == "boolean":
		return "boolean"
	case strings.HasPrefix(option.Type, "integer"):
		return "integer"
	case option.Type == "enum" && len(consoleSettingChoices(option)) > 0:
		return "enum"
	case strings.Contains(option.Type, "array"):
		return "list"
	case option.Constraints == "HH:MM":
		return "time"
	}
	return "text"
}

func consoleSettingChoices(option config.ConfigOption) []string {
	if option.Type != "enum" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(option.Constraints, func(r rune) bool { return r == '、' || r == ',' || r == '，' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// consoleSettingDefault is the value an inherited setting has, in the form its
// control edits. Descriptive defaults ("empty", "inherits ...") give no value.
func consoleSettingDefault(option config.ConfigOption) string {
	value := strings.TrimSpace(option.Default)
	switch consoleSettingKind(option) {
	case "boolean":
		if value == "true" || value == "false" {
			return value
		}
	case "integer":
		return consoleLeadingInteger.FindString(value)
	case "enum":
		for _, choice := range consoleSettingChoices(option) {
			if choice == value {
				return value
			}
		}
	case "time":
		if consoleClockTime.MatchString(value) {
			return value
		}
	}
	return ""
}

// consoleSettingText offers one metadata string in both console languages. The
// documentation catalogue translates the public reference; distribution
// metadata may carry both languages as "中文 / English".
func consoleSettingText(text string, heading bool) map[string]string {
	text = strings.TrimSpace(text)
	out := map[string]string{"en": text, "zh-CN": text}
	if text == "" {
		return out
	}
	key := text
	if heading {
		key = "## " + text
	}
	if english, ok := doclang.Text(key, "en"); ok {
		out["en"] = strings.TrimPrefix(english, "## ")
		if chinese, ok := doclang.Text(key, "zh-CN"); ok {
			out["zh-CN"] = strings.TrimPrefix(chinese, "## ")
		}
		return out
	}
	if chinese, english, found := strings.Cut(text, " / "); found && strings.TrimSpace(chinese) != "" && strings.TrimSpace(english) != "" {
		out["zh-CN"], out["en"] = strings.TrimSpace(chinese), strings.TrimSpace(english)
	}
	return out
}

func consoleSettingValue(value any) string {
	switch typed := value.(type) {
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, fmt.Sprint(item))
		}
		return strings.Join(parts, " ")
	case []string:
		return strings.Join(typed, " ")
	case nil:
		return ""
	}
	return fmt.Sprint(value)
}

func consoleSettingsVersion(raw map[string]any) string {
	// An empty or missing file reads as a nil map here but as an empty map
	// inside UpdateConfigFile; both are the same configuration.
	if raw == nil {
		raw = map[string]any{}
	}
	data, _ := json.Marshal(raw)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (b *consoleBackend) snapshotSettings() (any, error) {
	b.localMu.Lock()
	defer b.localMu.Unlock()
	cfg, err := readCLIConfigFile()
	if err != nil {
		return nil, consoleOperationError(err)
	}
	settings := make([]map[string]any, 0)
	for _, option := range config.ConfigOptions() {
		if consoleSettingHidden(option) {
			continue
		}
		raw, exists := nestedConfigValue(map[string]any(cfg), strings.Split(option.Key, "."))
		value := consoleSettingDefault(option)
		if exists {
			value = consoleSettingValue(raw)
		}
		row := map[string]any{"key": option.Key, "value": value, "kind": consoleSettingKind(option), "type": option.Type,
			"writable": consoleSettingWritable(option), "inherited": !exists, "restart_required": true,
			"section": option.Section, "section_label": consoleSettingText(option.Section, true),
			"description": consoleSettingText(option.Description, false), "default": consoleSettingText(option.Default, false)}
		if option.Constraints != "" {
			row["constraints"] = consoleSettingText(option.Constraints, false)
		}
		if choices := consoleSettingChoices(option); len(choices) > 0 {
			row["choices"] = choices
		}
		if consoleSettingExperimental(option) {
			row["experimental"] = true
		}
		if !consoleSettingWritable(option) {
			row["read_only_reason"] = "cli_only"
		}
		settings = append(settings, row)
	}
	return map[string]any{"settings": settings, "version": consoleSettingsVersion(map[string]any(cfg)), "actions": []string{"save"}, "scope": "local_installation", "hub_address": hubAddr()}, nil
}

func consoleSettingExperimental(option config.ConfigOption) bool {
	return strings.Contains(option.Section, "实验") || strings.Contains(strings.ToLower(option.Section), "experiment") || strings.Contains(option.Key, ".experimental.")
}

// consoleNormalizeSetting checks a value against its control before the same
// key-specific validation `amux system config set` applies.
func consoleNormalizeSetting(option config.ConfigOption, value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return "", consoleInvalid("invalid_field", option.Key)
	}
	switch consoleSettingKind(option) {
	case "boolean":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return "", consoleInvalid("invalid_field", option.Key)
		}
		return strconv.FormatBool(parsed), nil
	case "integer":
		if _, err := strconv.Atoi(value); err != nil {
			return "", consoleInvalid("invalid_field", option.Key)
		}
	case "enum":
		for _, choice := range consoleSettingChoices(option) {
			if value == choice {
				return value, nil
			}
		}
		return "", consoleInvalid("invalid_field", option.Key)
	}
	return value, nil
}

func (b *consoleBackend) executeSettings(action string, data json.RawMessage) (any, error) {
	if action != "save" {
		return nil, consoleInvalid("unsupported_action", "")
	}
	var request struct {
		ExpectedVersion string            `json:"expected_version"`
		Values          map[string]string `json:"values"`
		Reset           []string          `json:"reset,omitempty"`
	}
	if err := consoleDecode(data, &request); err != nil {
		return nil, err
	}
	if request.ExpectedVersion == "" || len(request.Values) == 0 && len(request.Reset) == 0 {
		return nil, consoleInvalid("field_required", "expected_version")
	}
	if len(request.Values)+len(request.Reset) > 200 {
		return nil, consoleInvalid("invalid_field", "values")
	}
	values := map[string]string{}
	for key, value := range request.Values {
		option, ok := consoleSettingOption(key)
		if !ok {
			return nil, consoleInvalid("setting_not_writable", "")
		}
		if !consoleSettingWritable(option) {
			return nil, consoleInvalid("setting_not_writable", key)
		}
		normalized, err := consoleNormalizeSetting(option, value)
		if err != nil {
			return nil, err
		}
		values[key] = normalized
	}
	for _, key := range request.Reset {
		option, ok := consoleSettingOption(key)
		if !ok {
			return nil, consoleInvalid("setting_not_writable", "")
		}
		if !consoleSettingWritable(option) {
			return nil, consoleInvalid("setting_not_writable", key)
		}
		if _, both := values[key]; both {
			return nil, consoleInvalid("invalid_field", key)
		}
	}
	b.localMu.Lock()
	err := config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
		if consoleSettingsVersion(raw) != request.ExpectedVersion {
			return webconsole.ErrConflict
		}
		reset := append([]string(nil), request.Reset...)
		sort.Strings(reset)
		for _, key := range reset {
			if err := unsetConfigValue(cliConfigFile(raw), key); err != nil {
				return consoleInvalid("invalid_field", key)
			}
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := setConfigValue(cliConfigFile(raw), key, values[key]); err != nil {
				return consoleInvalid("invalid_field", key)
			}
		}
		if err := config.ValidateSessionReclaimSettings(raw); err != nil {
			return consoleInvalid("invalid_field", "runtime.session_reclaim")
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
