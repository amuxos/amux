package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/viper"
)

var feishuProfileNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var configWriteMu sync.Mutex

// SetBridgeFeishuInteractionMode persists and activates the interaction mode
// for one Feishu bot profile in the current process.
func SetBridgeFeishuInteractionMode(profile, mode string) error {
	profile = strings.TrimSpace(profile)
	if !feishuProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid Feishu profile %q", profile)
	}
	mode = strings.TrimSpace(mode)
	switch mode {
	case FeishuInteractionThread, FeishuInteractionSingleton:
	default:
		return fmt.Errorf("bridge.feishu.profiles.%s.interaction.mode must be %q or %q", profile, FeishuInteractionThread, FeishuInteractionSingleton)
	}

	path := strings.TrimSpace(viper.ConfigFileUsed())
	if path == "" {
		path = amuxhome.ConfigPath()
	}
	key := "bridge.feishu.profiles." + profile + ".interaction.mode"
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if err := updateConfigFileWithLock(path, func(cfg map[string]any) error {
		setNestedConfigValue(cfg, strings.Split(key, "."), mode)
		return nil
	}); err != nil {
		return err
	}
	viper.Set(key, mode)
	return nil
}

// SetBridgeFeishuThinkingProcess persists and activates the default thinking
// process presentation for one Feishu bot profile.
func SetBridgeFeishuThinkingProcess(profile, mode string) error {
	profile = strings.TrimSpace(profile)
	if !feishuProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid Feishu profile %q", profile)
	}
	mode = strings.TrimSpace(mode)
	if err := ValidateFeishuThinkingProcess(mode); err != nil {
		return fmt.Errorf("bridge.feishu.profiles.%s.presentation.%w", profile, err)
	}
	return setBridgeFeishuProfileString(profile, "presentation.thinking_process", mode)
}

// SetBridgeFeishuDocumentComments persists and activates the document-comment
// override for one Feishu bot profile in the current process.
func SetBridgeFeishuDocumentComments(profile string, enabled bool) error {
	return setBridgeFeishuProfileBool(profile, "document_comments.enabled", enabled)
}

// SetBridgeFeishuBotToBot persists and activates the Bot-to-Bot enabled
// override for one Feishu bot profile in the current process.
func SetBridgeFeishuBotToBot(profile string, enabled bool) error {
	return setBridgeFeishuProfileBool(profile, "bot_to_bot.enabled", enabled)
}

func setBridgeFeishuProfileBool(profile, suffix string, enabled bool) error {
	profile = strings.TrimSpace(profile)
	if !feishuProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid Feishu profile %q", profile)
	}

	path := strings.TrimSpace(viper.ConfigFileUsed())
	if path == "" {
		path = amuxhome.ConfigPath()
	}
	key := "bridge.feishu.profiles." + profile + "." + suffix
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if err := updateConfigFileWithLock(path, func(cfg map[string]any) error {
		setNestedConfigValue(cfg, strings.Split(key, "."), enabled)
		return nil
	}); err != nil {
		return err
	}
	viper.Set(key, enabled)
	return nil
}

func setBridgeFeishuProfileString(profile, suffix, value string) error {
	profile = strings.TrimSpace(profile)
	if !feishuProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid Feishu profile %q", profile)
	}

	path := strings.TrimSpace(viper.ConfigFileUsed())
	if path == "" {
		path = amuxhome.ConfigPath()
	}
	key := "bridge.feishu.profiles." + profile + "." + suffix
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if err := updateConfigFileWithLock(path, func(cfg map[string]any) error {
		setNestedConfigValue(cfg, strings.Split(key, "."), value)
		return nil
	}); err != nil {
		return err
	}
	viper.Set(key, value)
	return nil
}

// SetBridgeFeishuDocumentCommentsExecution persists and activates the optional
// Agent Profile and execution override used by new document-comment sessions.
func SetBridgeFeishuDocumentCommentsExecution(profile, agentProfileRef, runtimeRef, modelRef, reasoningEffort string) error {
	profile = strings.TrimSpace(profile)
	if !feishuProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid Feishu profile %q", profile)
	}
	agentProfileRef = strings.TrimSpace(agentProfileRef)
	runtimeRef = strings.TrimSpace(runtimeRef)
	modelRef = strings.TrimSpace(modelRef)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if agentProfileRef == "" && (runtimeRef != "" || modelRef != "" || reasoningEffort != "") {
		return fmt.Errorf("document comment execution override requires an Agent Profile override")
	}
	if runtimeRef == "" && reasoningEffort != "" {
		return fmt.Errorf("document comment reasoning effort override requires a runtime override")
	}

	path := strings.TrimSpace(viper.ConfigFileUsed())
	if path == "" {
		path = amuxhome.ConfigPath()
	}
	base := "bridge.feishu.profiles." + profile + ".document_comments"
	agentKey := base + ".agent_profile_ref"
	runtimeKey := base + ".runtime_ref"
	modelKey := base + ".model_ref"
	reasoningKey := base + ".reasoning_effort"
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if err := updateConfigFileWithLock(path, func(cfg map[string]any) error {
		setNestedConfigValue(cfg, strings.Split(agentKey, "."), agentProfileRef)
		setNestedConfigValue(cfg, strings.Split(runtimeKey, "."), runtimeRef)
		setNestedConfigValue(cfg, strings.Split(modelKey, "."), modelRef)
		setNestedConfigValue(cfg, strings.Split(reasoningKey, "."), reasoningEffort)
		return nil
	}); err != nil {
		return err
	}
	viper.Set(agentKey, agentProfileRef)
	viper.Set(runtimeKey, runtimeRef)
	viper.Set(modelKey, modelRef)
	viper.Set(reasoningKey, reasoningEffort)
	return nil
}

// SetBridgeFeishuGroupWatch persists a complete profile override and activates
// it in the current process. Card-driven management uses a complete override
// so subsequent installation-level changes cannot silently change its groups.
func SetBridgeFeishuGroupWatch(profile string, value BridgeFeishuGroupWatchConfig) error {
	profile = strings.TrimSpace(profile)
	if !feishuProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid Feishu profile %q", profile)
	}
	value = normalizeBridgeFeishuGroupWatch(value)
	if value.AgentProfileRef == "" && value.ModelRef != "" {
		return fmt.Errorf("group watch model override requires an Agent Profile override")
	}
	if value.AgentProfileRef == "" && (value.RuntimeRef != "" || value.ReasoningEffort != "") {
		return fmt.Errorf("group watch execution override requires an Agent Profile override")
	}
	if value.RuntimeRef == "" && value.ReasoningEffort != "" {
		return fmt.Errorf("group watch reasoning effort override requires a runtime override")
	}
	base := "bridge.feishu.profiles." + profile + ".group_watch"
	path := strings.TrimSpace(viper.ConfigFileUsed())
	if path == "" {
		path = amuxhome.ConfigPath()
	}

	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if err := updateConfigFileWithLock(path, func(cfg map[string]any) error {
		chatSenderTypes := cloneGroupWatchChatSenderTypes(value.ChatSenderTypes)
		if chatSenderTypes == nil {
			chatSenderTypes = map[string][]string{}
		}
		setNestedConfigValue(cfg, strings.Split(base+".enabled", "."), value.Enabled)
		setNestedConfigValue(cfg, strings.Split(base+".chat_ids", "."), append([]string(nil), value.ChatIDs...))
		setNestedConfigValue(cfg, strings.Split(base+".sender_types", "."), append([]string(nil), value.SenderTypes...))
		setNestedConfigValue(cfg, strings.Split(base+".chat_sender_types", "."), chatSenderTypes)
		setNestedConfigValue(cfg, strings.Split(base+".poll_interval_seconds", "."), value.PollIntervalSeconds)
		setNestedConfigValue(cfg, strings.Split(base+".lookback_seconds", "."), value.LookbackSeconds)
		setNestedConfigValue(cfg, strings.Split(base+".dedup.enabled", "."), value.DedupEnabled)
		setNestedConfigValue(cfg, strings.Split(base+".dedup.window_seconds", "."), value.DedupWindowSeconds)
		setNestedConfigValue(cfg, strings.Split(base+".max_active_sessions_per_chat", "."), value.MaxActiveSessionsPerChat)
		setNestedConfigValue(cfg, strings.Split(base+".idle_timeout_seconds", "."), value.IdleTimeoutSeconds)
		setNestedConfigValue(cfg, strings.Split(base+".agent_profile_ref", "."), value.AgentProfileRef)
		setNestedConfigValue(cfg, strings.Split(base+".runtime_ref", "."), value.RuntimeRef)
		setNestedConfigValue(cfg, strings.Split(base+".model_ref", "."), value.ModelRef)
		setNestedConfigValue(cfg, strings.Split(base+".reasoning_effort", "."), value.ReasoningEffort)
		setNestedConfigValue(cfg, strings.Split(base+".thinking_process", "."), value.ThinkingProcess)
		return nil
	}); err != nil {
		return err
	}
	viper.Set(base+".enabled", value.Enabled)
	viper.Set(base+".chat_ids", append([]string(nil), value.ChatIDs...))
	viper.Set(base+".sender_types", append([]string(nil), value.SenderTypes...))
	chatSenderTypes := cloneGroupWatchChatSenderTypes(value.ChatSenderTypes)
	if chatSenderTypes == nil {
		chatSenderTypes = map[string][]string{}
	}
	viper.Set(base+".chat_sender_types", chatSenderTypes)
	viper.Set(base+".poll_interval_seconds", value.PollIntervalSeconds)
	viper.Set(base+".lookback_seconds", value.LookbackSeconds)
	viper.Set(base+".dedup.enabled", value.DedupEnabled)
	viper.Set(base+".dedup.window_seconds", value.DedupWindowSeconds)
	viper.Set(base+".max_active_sessions_per_chat", value.MaxActiveSessionsPerChat)
	viper.Set(base+".idle_timeout_seconds", value.IdleTimeoutSeconds)
	viper.Set(base+".agent_profile_ref", value.AgentProfileRef)
	viper.Set(base+".runtime_ref", value.RuntimeRef)
	viper.Set(base+".model_ref", value.ModelRef)
	viper.Set(base+".reasoning_effort", value.ReasoningEffort)
	viper.Set(base+".thinking_process", value.ThinkingProcess)
	return nil
}

// SetBridgeFeishuGroupWatchExecution persists and activates the optional expert,
// runtime, model, and reasoning override used by new group-watch root sessions,
// together with the presentation policy applied to all group-watch turns.
func SetBridgeFeishuGroupWatchExecution(profile, agentProfileRef, runtimeRef, modelRef, reasoningEffort, thinkingProcess string) error {
	profile = strings.TrimSpace(profile)
	if !feishuProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid Feishu profile %q", profile)
	}
	agentProfileRef = strings.TrimSpace(agentProfileRef)
	runtimeRef = strings.TrimSpace(runtimeRef)
	modelRef = strings.TrimSpace(modelRef)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if agentProfileRef == "" && (runtimeRef != "" || modelRef != "" || reasoningEffort != "") {
		return fmt.Errorf("group watch execution override requires an Agent Profile override")
	}
	if agentProfileRef != "" && runtimeRef == "" {
		return fmt.Errorf("group watch custom execution requires a runtime override")
	}
	thinkingProcess = strings.TrimSpace(thinkingProcess)
	if err := ValidateFeishuThinkingProcess(thinkingProcess); err != nil {
		return fmt.Errorf("bridge.feishu.profiles.%s.group_watch.%w", profile, err)
	}

	path := strings.TrimSpace(viper.ConfigFileUsed())
	if path == "" {
		path = amuxhome.ConfigPath()
	}
	base := "bridge.feishu.profiles." + profile + ".group_watch"
	agentKey := base + ".agent_profile_ref"
	runtimeKey := base + ".runtime_ref"
	modelKey := base + ".model_ref"
	reasoningKey := base + ".reasoning_effort"
	thinkingKey := base + ".thinking_process"
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if err := updateConfigFileWithLock(path, func(cfg map[string]any) error {
		setNestedConfigValue(cfg, strings.Split(agentKey, "."), agentProfileRef)
		setNestedConfigValue(cfg, strings.Split(runtimeKey, "."), runtimeRef)
		setNestedConfigValue(cfg, strings.Split(modelKey, "."), modelRef)
		setNestedConfigValue(cfg, strings.Split(reasoningKey, "."), reasoningEffort)
		setNestedConfigValue(cfg, strings.Split(thinkingKey, "."), thinkingProcess)
		return nil
	}); err != nil {
		return err
	}
	viper.Set(agentKey, agentProfileRef)
	viper.Set(runtimeKey, runtimeRef)
	viper.Set(modelKey, modelRef)
	viper.Set(reasoningKey, reasoningEffort)
	viper.Set(thinkingKey, thinkingProcess)
	return nil
}

// UpdateConfigFile serializes a complete config read-modify-write transaction
// across goroutines and amux processes that share the same config path.
func UpdateConfigFile(path string, mutate func(map[string]any) error) error {
	return UpdateConfigFileContext(context.Background(), path, mutate)
}

// UpdateConfigFileContext is UpdateConfigFile with cancellable lock acquisition.
// Cancellation is also checked before mutation and before committing the file.
func UpdateConfigFileContext(ctx context.Context, path string, mutate func(map[string]any) error) error {
	if err := lockConfigWriteContext(ctx); err != nil {
		return err
	}
	defer configWriteMu.Unlock()
	return updateConfigFileWithLockContext(ctx, path, mutate)
}

func lockConfigWriteContext(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if configWriteMu.TryLock() {
			if err := ctx.Err(); err != nil {
				configWriteMu.Unlock()
				return err
			}
			return nil
		}
		if err := waitConfigLockRetry(ctx); err != nil {
			return err
		}
	}
}

func waitConfigLockRetry(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func updateConfigFileWithLock(path string, mutate func(map[string]any) error) error {
	return updateConfigFileWithLockContext(context.Background(), path, mutate)
}

func updateConfigFileWithLockContext(ctx context.Context, path string, mutate func(map[string]any) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("config path is required")
	}
	if mutate == nil {
		return fmt.Errorf("config mutation is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lock.Chmod(0600); err != nil {
		return err
	}
	if err := lockConfigFileContext(ctx, lock); err != nil {
		return err
	}
	defer unlockConfigFile(lock) //nolint:errcheck

	cfg, err := readConfigFileForWrite(path)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mutate(cfg); err != nil {
		return err
	}
	return writeConfigFileForWriteContext(ctx, path, cfg)
}

func readConfigFileForWrite(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

func writeConfigFileForWrite(path string, cfg map[string]any) error {
	return writeConfigFileForWriteContext(context.Background(), path, cfg)
}

func writeConfigFileForWriteContext(ctx context.Context, path string, cfg map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func setNestedConfigValue(cfg map[string]any, parts []string, value any) {
	m := cfg
	for _, part := range parts[:len(parts)-1] {
		child, _ := m[part].(map[string]any)
		if child == nil {
			child = map[string]any{}
			m[part] = child
		}
		m = child
	}
	m[parts[len(parts)-1]] = value
}
