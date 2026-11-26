package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/spf13/viper"
)

func Load(path string) error {
	if path != "" {
		viper.SetConfigFile(path)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("toml")
		viper.AddConfigPath(amuxhome.Dir())
	}
	viper.AutomaticEnv()
	return viper.ReadInConfig()
}

// HubConfig holds connection details for the hub.
type HubConfig struct {
	Addr  string // TCP address, e.g. "host:7000"
	Token string // shared secret (optional)
}

// RuntimeConfig identifies this runtime.
type RuntimeConfig struct {
	ID                 string // stable unique ID (defaults to hostname)
	Name               string // human-readable label (defaults to hostname)
	AutoStart          bool
	IdleTimeoutSeconds int
	// MaxSessions is the whole-machine concurrent session ceiling advertised to
	// the hub registry (0 => default applied by the runtime).
	MaxSessions int
	// MaxConcurrency is the per-agent-type concurrency ceiling (0 => default).
	MaxConcurrency int
	// Labels are operator-defined scheduling labels (pool, repo_cache, ...).
	Labels map[string]string
}

// RuntimeTaskCleanupConfig controls automatic terminal task history cleanup.
type RuntimeTaskCleanupConfig struct {
	Enabled                   bool
	TerminalHistoryTTLSeconds int
	SweepIntervalSeconds      int
}

// RuntimeBrokerReapConfig controls the periodic reconciliation of session
// broker processes against the session database. Brokers are spawned detached
// (Setsid) and are only reaped on the graceful shutdown path, so an ungraceful
// amuxlet exit (SIGKILL, crash) or an `amux update` binary overwrite leaks them
// as orphans. The reaper sweeps the process table and kills any broker whose
// session is gone or no longer running, plus the broker files of sessions the
// DB no longer knows.
type RuntimeBrokerReapConfig struct {
	Enabled              bool
	SweepIntervalSeconds int
}

// CLIConfig holds user-facing CLI defaults.
type CLIConfig struct {
	DefaultTarget string
}

// MCPConfig controls the built-in amux MCP server and automatic run-session
// projection. It does not govern profile-provided third-party MCP configs.
type MCPConfig struct {
	Enabled    bool
	AutoInject bool
}

// MemoryConfig governs Hub-owned, profile-independent Markdown memory.
type MemoryConfig struct {
	Enabled bool
}

const (
	DefaultTurnResultsTTLSeconds            = 7 * 24 * 60 * 60 // 7 days
	MinTurnResultsTTLSeconds                = 60
	MaxTurnResultsTTLSeconds                = 30 * 24 * 60 * 60 // 30 days
	DefaultTurnResultsPendingTimeoutSeconds = 300               // 5 minutes
	MinTurnResultsPendingTimeoutSeconds     = 30
	MaxTurnResultsPendingTimeoutSeconds     = 3600
	DefaultTurnResultsMaxEntries            = 512
	MinTurnResultsMaxEntries                = 16
	MaxTurnResultsMaxEntries                = 10000
	DefaultTurnResultsMaxTotalBytes         = 32 << 20 // 32 MiB
	MinTurnResultsMaxTotalBytes             = 1 << 20
	MaxTurnResultsMaxTotalBytes             = 256 << 20
	DefaultTurnResultsMaxContentBytes       = 1 << 20 // 1 MiB
	MinTurnResultsMaxContentBytes           = 64 << 10
	MaxTurnResultsMaxContentBytes           = 16 << 20
)

// TurnResultsConfig controls Hub single-turn result collection and bounded
// retention. Enabled defaults to true with bounded TTL/count/byte limits and
// can be opted out by setting turn_results.enabled = false.
type TurnResultsConfig struct {
	Enabled               bool
	TTLSeconds            int
	PendingTimeoutSeconds int
	MaxEntries            int
	MaxTotalBytes         int64
	MaxContentBytes       int
}

type IsolationHardeningConfig struct {
	PIDNamespace   bool
	MountNamespace bool
	Seccomp        bool
}

// IsolationConfig controls the optional Linux per-principal UID/DAC backend.
// Enabled defaults to false so existing local runtimes retain passthrough
// behavior until an operator explicitly installs the helper and opts in.
type IsolationConfig struct {
	Enabled       bool
	Backend       string
	UIDRangeStart int
	UIDRangeEnd   int
	GCIdleDays    int
	HelperSocket  string
	MappingDB     string
	PrincipalRoot string
	GlobalEnv     []string
	Hardening     IsolationHardeningConfig
}

var isolationEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ActiveIsolationGlobalEnv returns the installation-wide environment only
// while principal isolation is configured. The helper may temporarily degrade
// to passthrough; identified executions still keep the configured launch
// contract instead of changing credentials with helper readiness.
func ActiveIsolationGlobalEnv(cfg IsolationConfig) []string {
	if !cfg.Enabled || cfg.Backend == "passthrough" {
		return nil
	}
	return append([]string(nil), cfg.GlobalEnv...)
}

type BridgeFeishuPromptConfig struct {
	SharedThreadEnabled      *bool
	TopicRootEnabled         bool
	SharedThreadTemplateFile string
}

type BridgeFeishuDocumentCommentsConfig struct {
	Enabled         bool
	AgentProfileRef string
	RuntimeRef      string
	ModelRef        string
	ReasoningEffort string
}

type BridgeFeishuPresentationConfig struct {
	CompletionNotificationEnabled      bool
	ThinkingProcess                    string
	CompletionNotificationAfterSeconds int
}

const (
	FeishuThinkingProcessExpanded                   = "expanded"
	FeishuThinkingProcessCollapsed                  = "collapsed"
	FeishuThinkingProcessHidden                     = "hidden"
	DefaultFeishuThinkingProcess                    = FeishuThinkingProcessCollapsed
	DefaultFeishuCompletionNotificationAfterSeconds = 60
)

type BridgeFeishuBotE2EConfig struct {
	SenderOpenIDs []string
	// SenderRoles maps a test bot sender open_id to the identity its /e2e
	// request should be projected as. Supported roles: owner (default, same as
	// v0.267), shared. "unauthorized" is deliberately not allowed: letting a
	// trusted test bot impersonate an unauthorized user would turn the test
	// entry point into an identity-bypass channel.
	SenderRoles map[string]string
}

// ValidBotE2ESenderRoles enumerates the identity roles a sender_roles entry
// may project a test bot request onto.
var ValidBotE2ESenderRoles = map[string]bool{"owner": true, "shared": true}

// ParseSenderRolesCLI parses the CLI/TOML value for
// bridge.feishu.profiles.<profile>.bot_e2e.sender_roles. It accepts either a
// comma-separated list of "open_id=role" entries (e.g.
// "ou_a=shared,ou_b=owner") or a bare map already rendered as
// {"ou_a":"shared"}. Roles are normalized to lowercase; unknown roles are
// preserved verbatim so the bridge can reject them loudly at routing time.
func ParseSenderRolesCLI(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]string{}, nil
	}
	roles := make(map[string]string)
	if strings.HasPrefix(raw, "{") {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("sender_roles must be a JSON map like {\"ou_a\":\"shared\"} or entries like \"ou_a=shared\": %w", err)
		}
		for k, v := range m {
			roles[strings.TrimSpace(k)] = strings.ToLower(strings.TrimSpace(v))
		}
		return roles, nil
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kv := strings.SplitN(entry, "=", 2)
		if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" {
			return nil, fmt.Errorf("sender_roles entry %q must be open_id=role", entry)
		}
		roles[strings.TrimSpace(kv[0])] = strings.ToLower(strings.TrimSpace(kv[1]))
	}
	return roles, nil
}

// BridgeFeishuBotToBotConfig controls Feishu-native Agent @Agent handoffs.
// It defaults on for shared thread workspaces; installations and individual
// Bot Profiles can still disable it explicitly.
type BridgeFeishuBotToBotConfig struct {
	Enabled           bool
	MaxTurnsPerThread int
}

const defaultFeishuBotToBotMaxTurns = 10

type BridgeFeishuGroupWatchConfig struct {
	Enabled                  bool
	ChatIDs                  []string
	SenderTypes              []string
	ChatSenderTypes          map[string][]string
	ChatSettings             map[string]BridgeFeishuGroupWatchChatConfig
	PollIntervalSeconds      int
	LookbackSeconds          int
	DedupEnabled             bool
	DedupWindowSeconds       int
	MaxActiveSessionsPerChat int
	IdleTimeoutSeconds       int
	AgentProfileRef          string
	RuntimeRef               string
	ModelRef                 string
	ReasoningEffort          string
	ThinkingProcess          string
}

const (
	DefaultFeishuGroupWatchMaxActiveSessionsPerChat = 50
	DefaultFeishuGroupWatchIdleTimeoutSeconds       = 3 * 60 * 60
	DefaultFeishuGroupWatchDedupWindowSeconds       = 30 * 60
	MaxFeishuGroupWatchActiveSessionsPerChat        = 500
	MaxFeishuGroupWatchIdleTimeoutSeconds           = 7 * 24 * 60 * 60
	MaxFeishuGroupWatchDedupWindowSeconds           = 24 * 60 * 60
)

// ValidateBridgeFeishuGroupWatchInteraction enforces the product boundary between
// per-root-message group-watch sessions and the one-session singleton mode.
func ValidateBridgeFeishuGroupWatchInteraction(groupWatch BridgeFeishuGroupWatchConfig, mode string) error {
	if !groupWatch.Enabled || strings.TrimSpace(mode) != FeishuInteractionSingleton {
		return nil
	}
	for _, chatID := range groupWatch.ChatIDs {
		if strings.TrimSpace(chatID) != "" && groupWatch.ForChat(chatID).Enabled {
			return fmt.Errorf("群值守与“继续同一对话”不兼容；请先暂停当前智能体的群值守，群值守仅支持“每个话题独立”")
		}
	}
	return nil
}

type BridgeFeishuWorkspaceDefaultsConfig struct {
	IdleTimeoutInherited bool
	Agent                string
	Target               string
	Cwd                  string
	Model                string
	Args                 []string
	IdleTimeoutSeconds   int
}

const (
	FeishuInteractionThread    = "thread"
	FeishuInteractionSingleton = "singleton"
)

func GetHub() HubConfig {
	addr := viper.GetString("hub.addr")
	token := viper.GetString("hub.token")
	return HubConfig{Addr: addr, Token: token}
}

// HubAuthConfig is the amuxd-side registration auth (the values the hub
// requires, vs HubConfig.Token which is what a runtime presents). When both
// fields are empty the hub registers runtimes in open mode.
type HubAuthConfig struct {
	SharedToken    string
	AllowedTargets []string
}

// GetHubAuth returns the hub's required registration auth. SharedToken reuses
// hub.token so a single-machine config matches automatically; AllowedTargets
// restricts which target_ids may register.
func GetHubAuth() HubAuthConfig {
	return HubAuthConfig{
		SharedToken:    viper.GetString("hub.token"),
		AllowedTargets: viper.GetStringSlice("hub.allowed_targets"),
	}
}

func GetRuntime() RuntimeConfig {
	hostname, _ := os.Hostname()
	id := viper.GetString("runtime.id")
	if id == "" {
		id = hostname
	}
	name := viper.GetString("runtime.name")
	if name == "" {
		name = id
	}
	return RuntimeConfig{
		ID:                 id,
		Name:               name,
		AutoStart:          viper.GetBool("runtime.auto_start"),
		IdleTimeoutSeconds: viper.GetInt("runtime.idle_timeout_seconds"),
		MaxSessions:        viper.GetInt("runtime.max_sessions"),
		MaxConcurrency:     viper.GetInt("runtime.max_concurrency"),
		Labels:             viper.GetStringMapString("runtime.labels"),
	}
}

func GetCLI() CLIConfig {
	return CLIConfig{DefaultTarget: viper.GetString("cli.default_target")}
}

func GetRuntimeTaskCleanup() RuntimeTaskCleanupConfig {
	enabled := true
	if viper.IsSet("runtime.task_cleanup.enabled") {
		enabled = viper.GetBool("runtime.task_cleanup.enabled")
	}
	ttl := viper.GetInt("runtime.task_cleanup.terminal_history_ttl_seconds")
	if ttl <= 0 {
		ttl = 7 * 24 * 60 * 60
	}
	interval := viper.GetInt("runtime.task_cleanup.sweep_interval_seconds")
	if interval <= 0 {
		interval = 60 * 60
	}
	return RuntimeTaskCleanupConfig{
		Enabled:                   enabled,
		TerminalHistoryTTLSeconds: ttl,
		SweepIntervalSeconds:      interval,
	}
}

// GetRuntimeBrokerReap reads the periodic orphan broker-process reconciliation
// settings. Default on with a 5-minute sweep.
func GetRuntimeBrokerReap() RuntimeBrokerReapConfig {
	enabled := true
	if viper.IsSet("runtime.broker_reap.enabled") {
		enabled = viper.GetBool("runtime.broker_reap.enabled")
	}
	interval := viper.GetInt("runtime.broker_reap.sweep_interval_seconds")
	if interval <= 0 {
		interval = 300
	}
	return RuntimeBrokerReapConfig{
		Enabled:              enabled,
		SweepIntervalSeconds: interval,
	}
}

func GetMCP() MCPConfig {
	enabled := true
	if viper.IsSet("mcp.enabled") {
		enabled = viper.GetBool("mcp.enabled")
	}
	autoInject := true
	if viper.IsSet("mcp.auto_inject") {
		autoInject = viper.GetBool("mcp.auto_inject")
	}
	return MCPConfig{Enabled: enabled, AutoInject: autoInject}
}

func GetMemory() MemoryConfig {
	return MemoryConfig{Enabled: viper.GetBool("memory.enabled")}
}

// GetTurnResults reads the Hub single-turn result collection and bounded
// retention settings. Enabled defaults to true; invalid/out-of-range numeric
// values clamp to safe defaults.
func GetTurnResults() TurnResultsConfig {
	enabled := true
	if viper.IsSet("turn_results.enabled") {
		enabled = viper.GetBool("turn_results.enabled")
	}

	ttl := DefaultTurnResultsTTLSeconds
	if viper.IsSet("turn_results.ttl_seconds") {
		raw := viper.GetInt("turn_results.ttl_seconds")
		if raw == 0 || (raw >= MinTurnResultsTTLSeconds && raw <= MaxTurnResultsTTLSeconds) {
			ttl = raw
		}
	}

	pendingTimeout := DefaultTurnResultsPendingTimeoutSeconds
	if viper.IsSet("turn_results.pending_timeout_seconds") {
		raw := viper.GetInt("turn_results.pending_timeout_seconds")
		if raw >= MinTurnResultsPendingTimeoutSeconds && raw <= MaxTurnResultsPendingTimeoutSeconds {
			pendingTimeout = raw
		}
	}

	maxEntries := DefaultTurnResultsMaxEntries
	if viper.IsSet("turn_results.max_entries") {
		raw := viper.GetInt("turn_results.max_entries")
		if raw >= MinTurnResultsMaxEntries && raw <= MaxTurnResultsMaxEntries {
			maxEntries = raw
		}
	}

	maxTotalBytes := int64(DefaultTurnResultsMaxTotalBytes)
	if viper.IsSet("turn_results.max_total_bytes") {
		raw := viper.GetInt64("turn_results.max_total_bytes")
		if raw >= MinTurnResultsMaxTotalBytes && raw <= MaxTurnResultsMaxTotalBytes {
			maxTotalBytes = raw
		}
	}

	maxContentBytes := DefaultTurnResultsMaxContentBytes
	if viper.IsSet("turn_results.max_content_bytes") {
		raw := viper.GetInt("turn_results.max_content_bytes")
		if raw >= MinTurnResultsMaxContentBytes && raw <= MaxTurnResultsMaxContentBytes {
			maxContentBytes = raw
		}
	}

	return TurnResultsConfig{
		Enabled:               enabled,
		TTLSeconds:            ttl,
		PendingTimeoutSeconds: pendingTimeout,
		MaxEntries:            maxEntries,
		MaxTotalBytes:         maxTotalBytes,
		MaxContentBytes:       maxContentBytes,
	}
}

func GetIsolation() (IsolationConfig, error) {
	return isolationFrom(viper.GetViper(), amuxhome.Dir())
}

// LoadIsolationFrom reads the isolation section of an explicit config file
// without disturbing the process-global viper instance. `amux isolation helper
// install` runs as root against another user's AMUX_HOME, so it needs the
// service user's settings rather than the caller's.
func LoadIsolationFrom(path string) (IsolationConfig, error) {
	v := viper.New()
	if _, err := os.Stat(path); err == nil {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return IsolationConfig{}, err
		}
	} else if !os.IsNotExist(err) {
		return IsolationConfig{}, err
	}
	return isolationFrom(v, filepath.Dir(path))
}

func isolationFrom(v *viper.Viper, amuxDir string) (IsolationConfig, error) {
	backend := strings.TrimSpace(v.GetString("isolation.backend"))
	if backend == "" {
		backend = "auto"
	}
	uidStart := v.GetInt("isolation.uid_range_start")
	if uidStart == 0 {
		uidStart = 700000
	}
	uidEnd := v.GetInt("isolation.uid_range_end")
	if uidEnd == 0 {
		uidEnd = 799999
	}
	helperSocket := expandConfigPath(v.GetString("isolation.helper_socket"))
	if helperSocket == "" {
		helperSocket = filepath.Join(amuxDir, "isolation-helper.sock")
	}
	mappingDB := expandConfigPath(v.GetString("isolation.mapping_db"))
	if mappingDB == "" {
		mappingDB = filepath.Join(amuxDir, "isolation-mappings.db")
	}
	principalRoot := expandConfigPath(v.GetString("isolation.principal_root"))
	if principalRoot == "" {
		principalRoot = "/var/lib/amux/principals"
	}
	hardeningDefault := func(key string) bool {
		if v.IsSet(key) {
			return v.GetBool(key)
		}
		return true
	}
	cfg := IsolationConfig{
		Enabled:       v.GetBool("isolation.enabled"),
		Backend:       backend,
		UIDRangeStart: uidStart,
		UIDRangeEnd:   uidEnd,
		GCIdleDays:    v.GetInt("isolation.gc_idle_days"),
		HelperSocket:  helperSocket,
		MappingDB:     mappingDB,
		PrincipalRoot: principalRoot,
		GlobalEnv:     append([]string(nil), v.GetStringSlice("isolation.global_env")...),
		Hardening: IsolationHardeningConfig{
			PIDNamespace:   hardeningDefault("isolation.hardening.pid_namespace"),
			MountNamespace: hardeningDefault("isolation.hardening.mount_namespace"),
			Seccomp:        hardeningDefault("isolation.hardening.seccomp"),
		},
	}
	if err := ValidateIsolation(cfg); err != nil {
		return IsolationConfig{}, err
	}
	return cfg, nil
}

func ValidateIsolation(cfg IsolationConfig) error {
	switch cfg.Backend {
	case "auto", "linux-dac", "passthrough":
	default:
		return fmt.Errorf("isolation.backend must be auto, linux-dac, or passthrough")
	}
	if cfg.GCIdleDays < 0 {
		return fmt.Errorf("isolation.gc_idle_days must be >= 0")
	}
	if err := ValidateIsolationGlobalEnv(cfg.GlobalEnv); err != nil {
		return err
	}
	if cfg.UIDRangeStart < 65536 || cfg.UIDRangeStart > 2147483000 ||
		cfg.UIDRangeEnd < cfg.UIDRangeStart || cfg.UIDRangeEnd > 2147483000 {
		return fmt.Errorf("isolation uid range must be within 65536..2147483000")
	}
	for label, path := range map[string]string{
		"helper_socket":  cfg.HelperSocket,
		"mapping_db":     cfg.MappingDB,
		"principal_root": cfg.PrincipalRoot,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("isolation.%s must be absolute after expansion", label)
		}
	}
	return nil
}

// ValidateIsolationGlobalEnv validates the persisted KEY=VALUE entries without
// including secret values in errors. The CLI uses the same contract before it
// atomically updates one entry.
func ValidateIsolationGlobalEnv(entries []string) error {
	seenEnv := make(map[string]struct{}, len(entries))
	for i, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !isolationEnvNamePattern.MatchString(key) {
			return fmt.Errorf("isolation.global_env entry %d must be KEY=VALUE with a valid environment variable name", i+1)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("isolation.global_env entry %q contains NUL", key)
		}
		if _, duplicate := seenEnv[key]; duplicate {
			return fmt.Errorf("isolation.global_env contains duplicate key %q", key)
		}
		seenEnv[key] = struct{}{}
	}
	return nil
}

func expandConfigPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func GetBridgeFeishuPrompt() BridgeFeishuPromptConfig {
	sharedThreadEnabled := false
	if viper.IsSet("bridge.feishu.prompt.shared_thread_enabled") {
		sharedThreadEnabled = viper.GetBool("bridge.feishu.prompt.shared_thread_enabled")
	}
	topicRootEnabled := true
	if viper.IsSet("bridge.feishu.prompt.topic_root_enabled") {
		topicRootEnabled = viper.GetBool("bridge.feishu.prompt.topic_root_enabled")
	}
	return BridgeFeishuPromptConfig{
		SharedThreadEnabled:      &sharedThreadEnabled,
		TopicRootEnabled:         topicRootEnabled,
		SharedThreadTemplateFile: viper.GetString("bridge.feishu.prompt.shared_thread_template_file"),
	}
}

func GetBridgeFeishuDocumentComments() BridgeFeishuDocumentCommentsConfig {
	return BridgeFeishuDocumentCommentsConfig{
		Enabled: viper.GetBool("bridge.feishu.document_comments.enabled"),
	}
}

func GetBridgeFeishuDocumentCommentsForProfile(profile string) BridgeFeishuDocumentCommentsConfig {
	documentComments := GetBridgeFeishuDocumentComments()
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return documentComments
	}
	base := "bridge.feishu.profiles." + profile + ".document_comments"
	if viper.IsSet(base + ".enabled") {
		documentComments.Enabled = viper.GetBool(base + ".enabled")
	}
	documentComments.AgentProfileRef = strings.TrimSpace(viper.GetString(base + ".agent_profile_ref"))
	documentComments.RuntimeRef = strings.TrimSpace(viper.GetString(base + ".runtime_ref"))
	documentComments.ModelRef = strings.TrimSpace(viper.GetString(base + ".model_ref"))
	documentComments.ReasoningEffort = strings.TrimSpace(viper.GetString(base + ".reasoning_effort"))
	return documentComments
}

// FeishuSteerForegroundKey is the experimental switch that turns on busy-input
// steering into the running turn and the foreground/background coordination
// prompt for Feishu singleton and thread chats. Default off.
const FeishuSteerForegroundKey = "bridge.feishu.experimental.steer_foreground"

// GetBridgeFeishuSteerForeground reports the experimental steer/foreground
// switch. Running bridges read it at startup.
func GetBridgeFeishuSteerForeground() bool {
	return viper.GetBool(FeishuSteerForegroundKey)
}

// FeishuShowBackgroundTasksKey is the experimental switch that shows
// background task (subtask / spawned agent instance) progress cards in Feishu.
// Default off: background progress stays out of the chat; the foreground
// agent reports results itself.
const FeishuShowBackgroundTasksKey = "bridge.feishu.experimental.show_background_tasks"

// GetBridgeFeishuShowBackgroundTasks reports the startup value of the
// show_background_tasks switch. Running bridges apply card edits live.
func GetBridgeFeishuShowBackgroundTasks() bool {
	return viper.GetBool(FeishuShowBackgroundTasksKey)
}

// FeishuMentionTurnInitiatorKey is the experimental switch that prepends
// native Feishu mentions for the human users whose input created an Agent Turn
// in a group topic. Default off. Running bridges apply it live.
const FeishuMentionTurnInitiatorKey = "bridge.feishu.experimental.mention_turn_initiator"

// GetBridgeFeishuMentionTurnInitiator reports the startup value of the
// mention_turn_initiator switch.
func GetBridgeFeishuMentionTurnInitiator() bool {
	return viper.GetBool(FeishuMentionTurnInitiatorKey)
}

// FeishuReplyEndNotificationKey enables a private notification after each
// human-initiated Turn. Default off; running bridges apply card edits live.
const FeishuReplyEndNotificationKey = "bridge.feishu.experimental.reply_end_notification"

func GetBridgeFeishuReplyEndNotification() bool {
	return viper.GetBool(FeishuReplyEndNotificationKey)
}

// FeishuModelRecoveryQueueWaitSecondsKey controls how many seconds a Feishu
// turn must remain in upstream retry or queue wait without visible output
// before the progress card shows same-engine model recovery controls.
// Default is 60s; 0 disables the queued recovery controls while retaining
// rate-limit error recovery cards. Running bridges apply edits live.
const (
	FeishuModelRecoveryQueueWaitSecondsKey     = "bridge.feishu.experimental.model_recovery_queue_wait_seconds"
	DefaultFeishuModelRecoveryQueueWaitSeconds = 60
	MaxFeishuModelRecoveryQueueWaitSeconds     = 3600
)

func ValidateModelRecoveryQueueWaitSeconds(seconds int) error {
	if seconds < 0 || seconds > MaxFeishuModelRecoveryQueueWaitSeconds {
		return fmt.Errorf("%s must be an integer from 0 to %d", FeishuModelRecoveryQueueWaitSecondsKey, MaxFeishuModelRecoveryQueueWaitSeconds)
	}
	return nil
}

func modelRecoveryQueueWaitSecondsFromViper(v *viper.Viper) int {
	if v == nil || !v.IsSet(FeishuModelRecoveryQueueWaitSecondsKey) {
		return DefaultFeishuModelRecoveryQueueWaitSeconds
	}
	seconds := v.GetInt(FeishuModelRecoveryQueueWaitSecondsKey)
	if seconds < 0 || seconds > MaxFeishuModelRecoveryQueueWaitSeconds {
		return DefaultFeishuModelRecoveryQueueWaitSeconds
	}
	return seconds
}

// GetBridgeFeishuModelRecoveryQueueWaitSeconds reports the startup value of the
// queued model recovery wait threshold in seconds.
func GetBridgeFeishuModelRecoveryQueueWaitSeconds() int {
	return modelRecoveryQueueWaitSecondsFromViper(viper.GetViper())
}

// liveSteerForeground mirrors the steer_foreground switch for components
// outside the bridge (the hub task router's parent-completion notifier).
// 0 = not set by a bridge yet (fall back to the config value), 1 = off, 2 = on.
var liveSteerForeground atomic.Int32

// SetLiveSteerForeground records the steer_foreground value a running bridge
// applied (at startup or live from the experiments card).
func SetLiveSteerForeground(on bool) {
	if on {
		liveSteerForeground.Store(2)
	} else {
		liveSteerForeground.Store(1)
	}
}

// LiveSteerForeground reports the running steer_foreground value.
func LiveSteerForeground() bool {
	switch liveSteerForeground.Load() {
	case 2:
		return true
	case 1:
		return false
	default:
		return GetBridgeFeishuSteerForeground()
	}
}

func GetBridgeFeishuPresentation() BridgeFeishuPresentationConfig {
	completionAfter := DefaultFeishuCompletionNotificationAfterSeconds
	if viper.IsSet("bridge.feishu.presentation.completion_notification_after_seconds") {
		completionAfter = viper.GetInt("bridge.feishu.presentation.completion_notification_after_seconds")
		if completionAfter < 0 {
			completionAfter = DefaultFeishuCompletionNotificationAfterSeconds
		}
	}
	return BridgeFeishuPresentationConfig{
		CompletionNotificationEnabled: viper.GetBool("bridge.feishu.presentation.completion_notification_enabled"),
		ThinkingProcess: normalizeFeishuThinkingProcess(
			viper.GetString("bridge.feishu.presentation.thinking_process"),
		),
		CompletionNotificationAfterSeconds: completionAfter,
	}
}

func GetBridgeFeishuPresentationForProfile(profile string) BridgeFeishuPresentationConfig {
	presentation := GetBridgeFeishuPresentation()
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return presentation
	}
	base := "bridge.feishu.profiles." + profile + ".presentation"
	if key := base + ".thinking_process"; viper.IsSet(key) {
		presentation.ThinkingProcess = normalizeFeishuThinkingProcess(viper.GetString(key))
	}
	if key := base + ".completion_notification_after_seconds"; viper.IsSet(key) {
		seconds := viper.GetInt(key)
		if seconds >= 0 {
			presentation.CompletionNotificationAfterSeconds = seconds
		}
	}
	return presentation
}

func ValidateFeishuThinkingProcess(value string) error {
	switch strings.TrimSpace(value) {
	case FeishuThinkingProcessExpanded, FeishuThinkingProcessCollapsed, FeishuThinkingProcessHidden:
		return nil
	default:
		return fmt.Errorf("thinking_process must be %q, %q, or %q", FeishuThinkingProcessExpanded, FeishuThinkingProcessCollapsed, FeishuThinkingProcessHidden)
	}
}

func normalizeFeishuThinkingProcess(value string) string {
	value = strings.TrimSpace(value)
	if ValidateFeishuThinkingProcess(value) != nil {
		return DefaultFeishuThinkingProcess
	}
	return value
}

func GetBridgeFeishuBotE2EForProfile(profile string) BridgeFeishuBotE2EConfig {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return BridgeFeishuBotE2EConfig{}
	}
	base := "bridge.feishu.profiles." + profile + ".bot_e2e"
	cfg := BridgeFeishuBotE2EConfig{
		SenderOpenIDs: cleanUniqueStrings(viper.GetStringSlice(base+".sender_open_ids"), false, nil),
	}
	if raw := viper.GetStringMapString(base + ".sender_roles"); len(raw) > 0 {
		roles := make(map[string]string, len(raw))
		for sender, role := range raw {
			sender = strings.TrimSpace(sender)
			role = strings.ToLower(strings.TrimSpace(role))
			if sender == "" {
				continue
			}
			if !ValidBotE2ESenderRoles[role] {
				// Keep the entry so the bridge can reject it verbosely at
				// routing time instead of silently treating it as owner.
				roles[sender] = role
				continue
			}
			roles[sender] = role
		}
		cfg.SenderRoles = roles
	}
	return cfg
}

func GetBridgeFeishuBotToBot() BridgeFeishuBotToBotConfig {
	enabled := false
	if viper.IsSet("bridge.feishu.bot_to_bot.enabled") {
		enabled = viper.GetBool("bridge.feishu.bot_to_bot.enabled")
	}
	maxTurns := viper.GetInt("bridge.feishu.bot_to_bot.max_turns_per_thread")
	if maxTurns <= 0 {
		maxTurns = defaultFeishuBotToBotMaxTurns
	}
	return BridgeFeishuBotToBotConfig{
		Enabled:           enabled,
		MaxTurnsPerThread: maxTurns,
	}
}

func GetBridgeFeishuBotToBotForProfile(profile string) BridgeFeishuBotToBotConfig {
	botToBot := GetBridgeFeishuBotToBot()
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return botToBot
	}
	base := "bridge.feishu.profiles." + profile + ".bot_to_bot"
	if viper.IsSet(base + ".enabled") {
		botToBot.Enabled = viper.GetBool(base + ".enabled")
	}
	if viper.IsSet(base + ".max_turns_per_thread") {
		if maxTurns := viper.GetInt(base + ".max_turns_per_thread"); maxTurns > 0 {
			botToBot.MaxTurnsPerThread = maxTurns
		} else {
			botToBot.MaxTurnsPerThread = defaultFeishuBotToBotMaxTurns
		}
	}
	return botToBot
}

func GetBridgeFeishuGroupWatch() BridgeFeishuGroupWatchConfig {
	cfg := BridgeFeishuGroupWatchConfig{
		Enabled:                  viper.GetBool("bridge.feishu.group_watch.enabled"),
		ChatIDs:                  viper.GetStringSlice("bridge.feishu.group_watch.chat_ids"),
		SenderTypes:              []string{"user", "app"},
		ChatSenderTypes:          readGroupWatchChatSenderTypes("bridge.feishu.group_watch.chat_sender_types"),
		PollIntervalSeconds:      15,
		LookbackSeconds:          60,
		DedupEnabled:             false,
		DedupWindowSeconds:       DefaultFeishuGroupWatchDedupWindowSeconds,
		MaxActiveSessionsPerChat: DefaultFeishuGroupWatchMaxActiveSessionsPerChat,
		IdleTimeoutSeconds:       DefaultFeishuGroupWatchIdleTimeoutSeconds,
		ThinkingProcess:          DefaultFeishuThinkingProcess,
	}
	if viper.IsSet("bridge.feishu.group_watch.sender_types") {
		cfg.SenderTypes = viper.GetStringSlice("bridge.feishu.group_watch.sender_types")
	}
	if viper.IsSet("bridge.feishu.group_watch.poll_interval_seconds") {
		cfg.PollIntervalSeconds = viper.GetInt("bridge.feishu.group_watch.poll_interval_seconds")
	}
	if viper.IsSet("bridge.feishu.group_watch.lookback_seconds") {
		cfg.LookbackSeconds = viper.GetInt("bridge.feishu.group_watch.lookback_seconds")
	}
	if viper.IsSet("bridge.feishu.group_watch.dedup.enabled") {
		cfg.DedupEnabled = viper.GetBool("bridge.feishu.group_watch.dedup.enabled")
	}
	if viper.IsSet("bridge.feishu.group_watch.dedup.window_seconds") {
		cfg.DedupWindowSeconds = viper.GetInt("bridge.feishu.group_watch.dedup.window_seconds")
	}
	if viper.IsSet("bridge.feishu.group_watch.max_active_sessions_per_chat") {
		cfg.MaxActiveSessionsPerChat = viper.GetInt("bridge.feishu.group_watch.max_active_sessions_per_chat")
	}
	if viper.IsSet("bridge.feishu.group_watch.idle_timeout_seconds") {
		cfg.IdleTimeoutSeconds = viper.GetInt("bridge.feishu.group_watch.idle_timeout_seconds")
	}
	if viper.IsSet("bridge.feishu.group_watch.thinking_process") {
		cfg.ThinkingProcess = viper.GetString("bridge.feishu.group_watch.thinking_process")
	}
	return normalizeBridgeFeishuGroupWatch(cfg)
}

func GetBridgeFeishuGroupWatchForProfile(profile string) BridgeFeishuGroupWatchConfig {
	cfg := GetBridgeFeishuGroupWatch()
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return cfg
	}
	base := "bridge.feishu.profiles." + profile + ".group_watch"
	dedupBase := base + ".dedup"
	if viper.IsSet(base + ".enabled") {
		cfg.Enabled = viper.GetBool(base + ".enabled")
	}
	if viper.IsSet(base + ".chat_ids") {
		cfg.ChatIDs = viper.GetStringSlice(base + ".chat_ids")
	}
	if viper.IsSet(base + ".sender_types") {
		cfg.SenderTypes = viper.GetStringSlice(base + ".sender_types")
	}
	if viper.IsSet(base + ".chat_sender_types") {
		cfg.ChatSenderTypes = readGroupWatchChatSenderTypes(base + ".chat_sender_types")
	}
	if viper.IsSet(base + ".poll_interval_seconds") {
		cfg.PollIntervalSeconds = viper.GetInt(base + ".poll_interval_seconds")
	}
	if viper.IsSet(base + ".lookback_seconds") {
		cfg.LookbackSeconds = viper.GetInt(base + ".lookback_seconds")
	}
	if viper.IsSet(dedupBase + ".enabled") {
		cfg.DedupEnabled = viper.GetBool(dedupBase + ".enabled")
	}
	if viper.IsSet(dedupBase + ".window_seconds") {
		cfg.DedupWindowSeconds = viper.GetInt(dedupBase + ".window_seconds")
	}
	if viper.IsSet(base + ".max_active_sessions_per_chat") {
		cfg.MaxActiveSessionsPerChat = viper.GetInt(base + ".max_active_sessions_per_chat")
	}
	if viper.IsSet(base + ".idle_timeout_seconds") {
		cfg.IdleTimeoutSeconds = viper.GetInt(base + ".idle_timeout_seconds")
	}
	cfg.AgentProfileRef = strings.TrimSpace(viper.GetString(base + ".agent_profile_ref"))
	cfg.RuntimeRef = strings.TrimSpace(viper.GetString(base + ".runtime_ref"))
	cfg.ModelRef = strings.TrimSpace(viper.GetString(base + ".model_ref"))
	cfg.ReasoningEffort = strings.TrimSpace(viper.GetString(base + ".reasoning_effort"))
	if viper.IsSet(base + ".thinking_process") {
		cfg.ThinkingProcess = viper.GetString(base + ".thinking_process")
	}
	cfg.ChatSettings = readGroupWatchChatSettings(base+".chat_settings", cfg)
	return normalizeBridgeFeishuGroupWatch(cfg)
}

func normalizeBridgeFeishuGroupWatch(cfg BridgeFeishuGroupWatchConfig) BridgeFeishuGroupWatchConfig {
	cfg.AgentProfileRef = strings.TrimSpace(cfg.AgentProfileRef)
	cfg.RuntimeRef = strings.TrimSpace(cfg.RuntimeRef)
	cfg.ModelRef = strings.TrimSpace(cfg.ModelRef)
	cfg.ReasoningEffort = strings.TrimSpace(cfg.ReasoningEffort)
	cfg.ThinkingProcess = normalizeFeishuThinkingProcess(cfg.ThinkingProcess)
	cfg.ChatIDs = cleanUniqueStrings(cfg.ChatIDs, false, nil)
	cfg.SenderTypes = cleanUniqueStrings(cfg.SenderTypes, true, map[string]bool{
		"user": true,
		"app":  true,
	})
	cfg.ChatSenderTypes = normalizeGroupWatchChatSenderTypes(cfg.ChatSenderTypes)
	if cfg.PollIntervalSeconds < 5 || cfg.PollIntervalSeconds > 300 {
		cfg.PollIntervalSeconds = 15
	}
	if cfg.LookbackSeconds < cfg.PollIntervalSeconds || cfg.LookbackSeconds > 3600 {
		cfg.LookbackSeconds = max(60, cfg.PollIntervalSeconds)
	}
	if cfg.DedupWindowSeconds < 60 || cfg.DedupWindowSeconds > MaxFeishuGroupWatchDedupWindowSeconds {
		cfg.DedupWindowSeconds = DefaultFeishuGroupWatchDedupWindowSeconds
	}
	if cfg.MaxActiveSessionsPerChat < 1 || cfg.MaxActiveSessionsPerChat > MaxFeishuGroupWatchActiveSessionsPerChat {
		cfg.MaxActiveSessionsPerChat = DefaultFeishuGroupWatchMaxActiveSessionsPerChat
	}
	if cfg.IdleTimeoutSeconds < 60 || cfg.IdleTimeoutSeconds > MaxFeishuGroupWatchIdleTimeoutSeconds {
		cfg.IdleTimeoutSeconds = DefaultFeishuGroupWatchIdleTimeoutSeconds
	}
	return cfg
}

func readGroupWatchChatSenderTypes(key string) map[string][]string {
	raw := viper.GetStringMapStringSlice(key)
	out := make(map[string][]string, len(raw))
	for chatID, value := range raw {
		out[chatID] = append([]string(nil), value...)
	}
	return out
}

func normalizeGroupWatchChatSenderTypes(values map[string][]string) map[string][]string {
	out := make(map[string][]string, len(values))
	for chatID, senderTypes := range values {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" {
			continue
		}
		senderTypes = cleanUniqueStrings(senderTypes, true, map[string]bool{
			"user": true,
			"app":  true,
		})
		if len(senderTypes) == 0 {
			continue
		}
		out[chatID] = senderTypes
	}
	return out
}

func cloneGroupWatchChatSenderTypes(values map[string][]string) map[string][]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string][]string, len(values))
	for chatID, senderTypes := range values {
		out[chatID] = append([]string(nil), senderTypes...)
	}
	return out
}

func cleanUniqueStrings(values []string, lower bool, allowed map[string]bool) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if lower {
			value = strings.ToLower(value)
		}
		if value == "" || seen[value] || (allowed != nil && !allowed[value]) {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func GetBridgeFeishuWorkspaceDefaults() BridgeFeishuWorkspaceDefaultsConfig {
	return BridgeFeishuWorkspaceDefaultsConfig{
		Agent:                viper.GetString("bridge.feishu.workspace_defaults.agent"),
		Target:               viper.GetString("bridge.feishu.workspace_defaults.target"),
		Cwd:                  viper.GetString("bridge.feishu.workspace_defaults.cwd"),
		Model:                viper.GetString("bridge.feishu.workspace_defaults.model"),
		Args:                 viper.GetStringSlice("bridge.feishu.workspace_defaults.args"),
		IdleTimeoutSeconds:   viper.GetInt("bridge.feishu.workspace_defaults.idle_timeout_seconds"),
		IdleTimeoutInherited: !viper.IsSet("bridge.feishu.workspace_defaults.idle_timeout_seconds"),
	}
}

func GetBridgeFeishuWorkspaceDefaultsForProfile(profile string) BridgeFeishuWorkspaceDefaultsConfig {
	defaults := GetBridgeFeishuWorkspaceDefaults()
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return defaults
	}
	base := "bridge.feishu.profiles." + profile + ".workspace_defaults"
	if viper.IsSet(base + ".agent") {
		defaults.Agent = viper.GetString(base + ".agent")
	}
	if viper.IsSet(base + ".target") {
		defaults.Target = viper.GetString(base + ".target")
	}
	if viper.IsSet(base + ".cwd") {
		defaults.Cwd = viper.GetString(base + ".cwd")
	}
	if viper.IsSet(base + ".model") {
		defaults.Model = viper.GetString(base + ".model")
	}
	if viper.IsSet(base + ".args") {
		defaults.Args = viper.GetStringSlice(base + ".args")
	}
	if viper.IsSet(base + ".idle_timeout_seconds") {
		defaults.IdleTimeoutSeconds = viper.GetInt(base + ".idle_timeout_seconds")
		defaults.IdleTimeoutInherited = false
	}
	return defaults
}

// GetBridgeFeishuClaudeModels returns operator-declared Claude model refs that
// the Feishu model picker offers in addition to the built-in opus/sonnet/haiku.
// Entries are trimmed and de-duplicated, preserving declaration order.
func GetBridgeFeishuClaudeModels() []string {
	return cleanUniqueStrings(viper.GetStringSlice("bridge.feishu.claude_models"), false, nil)
}

func GetBridgeFeishuInteractionMode(profile string) (string, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return FeishuInteractionThread, nil
	}
	key := "bridge.feishu.profiles." + profile + ".interaction.mode"
	mode := strings.TrimSpace(viper.GetString(key))
	if mode == "" {
		return FeishuInteractionThread, nil
	}
	switch mode {
	case FeishuInteractionThread, FeishuInteractionSingleton:
		return mode, nil
	default:
		return "", fmt.Errorf("%s must be %q or %q", key, FeishuInteractionThread, FeishuInteractionSingleton)
	}
}

// SessionReclaimStage holds one source's three staged idle thresholds (seconds).
// 0 disables that stage.
type SessionReclaimStage struct {
	SleepAfterSeconds   int
	ArchiveAfterSeconds int
	PruneAfterSeconds   int
}

// SessionReclaimConfig controls staged idle reclamation of sessions.
type SessionReclaimConfig struct {
	Enabled              bool
	SweepIntervalSeconds int
	Local                SessionReclaimStage
	Feishu               SessionReclaimStage
}

const (
	defaultReclaimSweepIntervalSeconds = 600
	defaultFeishuSleepAfterSeconds     = 172800 // 48h
	defaultFeishuArchiveAfterSeconds   = 0
	defaultFeishuPruneAfterSeconds     = 0
	minReclaimStageSeconds             = 60 // mirrors session.MinIdleTimeoutSeconds
)

func validateReclaimStage(name string, s SessionReclaimStage) error {
	for label, v := range map[string]int{"sleep": s.SleepAfterSeconds, "archive": s.ArchiveAfterSeconds, "prune": s.PruneAfterSeconds} {
		if v != 0 && v < minReclaimStageSeconds {
			return fmt.Errorf("session_reclaim.%s.%s_after_seconds must be 0 or >= %d", name, label, minReclaimStageSeconds)
		}
	}
	if s.SleepAfterSeconds > 0 && s.ArchiveAfterSeconds > 0 && s.SleepAfterSeconds > s.ArchiveAfterSeconds {
		return fmt.Errorf("session_reclaim.%s: sleep_after_seconds must be <= archive_after_seconds", name)
	}
	if s.ArchiveAfterSeconds > 0 && s.PruneAfterSeconds > 0 && s.ArchiveAfterSeconds > s.PruneAfterSeconds {
		return fmt.Errorf("session_reclaim.%s: archive_after_seconds must be <= prune_after_seconds", name)
	}
	if s.SleepAfterSeconds > 0 && s.PruneAfterSeconds > 0 && s.SleepAfterSeconds > s.PruneAfterSeconds {
		return fmt.Errorf("session_reclaim.%s: sleep_after_seconds must be <= prune_after_seconds", name)
	}
	return nil
}

// GetSessionReclaim reads runtime.session_reclaim with defaults, validation, and
// legacy runtime.idle_timeout_seconds compatibility mapping.
func GetSessionReclaim() (SessionReclaimConfig, error) {
	return sessionReclaimFrom(viper.GetViper())
}

// ValidateSessionReclaimSettings applies GetSessionReclaim's validation to a
// configuration file map before it is saved, so a management surface cannot
// write stage thresholds that would stop Session reclamation from loading.
func ValidateSessionReclaimSettings(raw map[string]any) error {
	v := viper.New()
	if err := v.MergeConfigMap(raw); err != nil {
		return err
	}
	_, err := sessionReclaimFrom(v)
	return err
}

func sessionReclaimFrom(viper *viper.Viper) (SessionReclaimConfig, error) {
	enabled := true
	if viper.IsSet("runtime.session_reclaim.enabled") {
		enabled = viper.GetBool("runtime.session_reclaim.enabled")
	}
	sweep := viper.GetInt("runtime.session_reclaim.sweep_interval_seconds")
	if sweep <= 0 {
		sweep = defaultReclaimSweepIntervalSeconds
	}

	local := SessionReclaimStage{
		SleepAfterSeconds:   viper.GetInt("runtime.session_reclaim.local.sleep_after_seconds"),
		ArchiveAfterSeconds: viper.GetInt("runtime.session_reclaim.local.archive_after_seconds"),
		PruneAfterSeconds:   viper.GetInt("runtime.session_reclaim.local.prune_after_seconds"),
	}
	feishu := SessionReclaimStage{
		SleepAfterSeconds:   viper.GetInt("runtime.session_reclaim.feishu.sleep_after_seconds"),
		ArchiveAfterSeconds: viper.GetInt("runtime.session_reclaim.feishu.archive_after_seconds"),
		PruneAfterSeconds:   viper.GetInt("runtime.session_reclaim.feishu.prune_after_seconds"),
	}
	if !viper.IsSet("runtime.session_reclaim.feishu") {
		feishu = SessionReclaimStage{defaultFeishuSleepAfterSeconds, defaultFeishuArchiveAfterSeconds, defaultFeishuPruneAfterSeconds}
	}
	if !viper.IsSet("runtime.session_reclaim.local") {
		if legacy := viper.GetInt("runtime.idle_timeout_seconds"); legacy > 0 {
			local.SleepAfterSeconds = legacy
		}
	}

	cfg := SessionReclaimConfig{Enabled: enabled, SweepIntervalSeconds: sweep, Local: local, Feishu: feishu}
	if err := validateReclaimStage("local", cfg.Local); err != nil {
		return SessionReclaimConfig{}, err
	}
	if err := validateReclaimStage("feishu", cfg.Feishu); err != nil {
		return SessionReclaimConfig{}, err
	}
	return cfg, nil
}

// StructuredProviderConfig controls client-side turn watchdogs for structured
// providers. A value of 0 disables the
// corresponding watchdog layer.
type StructuredProviderConfig struct {
	TurnIdleTimeoutSeconds int
	TurnHardCapSeconds     int
	ToolIdleTimeoutSeconds int
	// RepeatToolCallLimit fails a turn after this many consecutive identical
	// tool calls (same item type and call-defining payload) within one turn. It
	// is a count, not seconds, so it is NOT clamped by
	// maxStructuredTimeoutSeconds. Zero disables the layer.
	RepeatToolCallLimit int
}

const (
	defaultTurnIdleTimeoutSeconds = 900   // 15m
	defaultTurnHardCapSeconds     = 21600 // 6h
	defaultToolIdleTimeoutSeconds = 3600  // 1h
	// defaultRepeatToolCallLimit leaves room for normal same-argument polling
	// of long-running jobs; the previous limit of 5 interrupted healthy SQL
	// status checks. A stuck model still stops at 50 consecutive identical
	// calls (observed: 1101 update_plan calls in a single production turn).
	defaultRepeatToolCallLimit = 50
	// maxRepeatToolCallLimit bounds the count override independently of the
	// seconds-based maxStructuredTimeoutSeconds clamp.
	maxRepeatToolCallLimit = 1000
	// maxStructuredTimeoutSeconds caps configured watchdog timeouts so the
	// time.Duration(seconds)*time.Second conversion cannot overflow int64
	// (which would make the timer fire immediately).
	maxStructuredTimeoutSeconds = 7 * 24 * 3600 // 7d
)

// DefaultStructuredProviderTimeouts returns the global/Codex package defaults.
func DefaultStructuredProviderTimeouts() StructuredProviderConfig {
	return StructuredProviderConfig{
		TurnIdleTimeoutSeconds: defaultTurnIdleTimeoutSeconds,
		TurnHardCapSeconds:     defaultTurnHardCapSeconds,
		ToolIdleTimeoutSeconds: defaultToolIdleTimeoutSeconds,
		RepeatToolCallLimit:    defaultRepeatToolCallLimit,
	}
}

// DefaultStructuredProviderTimeoutsForProvider returns distribution defaults
// for one structured provider.
func DefaultStructuredProviderTimeoutsForProvider(name string) StructuredProviderConfig {
	return distributionStructuredDefaults(name, DefaultStructuredProviderTimeouts())
}

// GetStructuredProvider reads [provider.structured] with defaults.
func GetStructuredProvider() StructuredProviderConfig {
	return StructuredProviderConfig{
		TurnIdleTimeoutSeconds: intWithDefault(
			"provider.structured.turn_idle_timeout_seconds",
			defaultTurnIdleTimeoutSeconds,
		),
		TurnHardCapSeconds: intWithDefault(
			"provider.structured.turn_hard_cap_seconds",
			defaultTurnHardCapSeconds,
		),
		ToolIdleTimeoutSeconds: intWithDefault(
			"provider.structured.tool_idle_timeout_seconds",
			defaultToolIdleTimeoutSeconds,
		),
		RepeatToolCallLimit: repeatToolCallLimitWithDefault(
			"provider.structured.repeat_tool_call_limit",
			defaultRepeatToolCallLimit,
		),
	}
}

// GetStructuredProviderForProvider applies supported per-provider overrides.
// An unset key inherits the global value, or the provider default if the global
// is unset. Negative overrides reset to the provider default.
func GetStructuredProviderForProvider(name string) StructuredProviderConfig {
	base := GetStructuredProvider()
	name = strings.ToLower(strings.TrimSpace(name))
	if !supportsStructuredConfig(name) {
		return base
	}
	defaults := DefaultStructuredProviderTimeoutsForProvider(name)
	if !viper.IsSet("provider.structured.turn_idle_timeout_seconds") {
		base.TurnIdleTimeoutSeconds = defaults.TurnIdleTimeoutSeconds
	}
	prefix := "provider.structured." + name + "."
	applyStructuredOverride(&base.TurnIdleTimeoutSeconds, prefix+"turn_idle_timeout_seconds", defaults.TurnIdleTimeoutSeconds)
	applyStructuredOverride(&base.TurnHardCapSeconds, prefix+"turn_hard_cap_seconds", defaults.TurnHardCapSeconds)
	applyStructuredOverride(&base.ToolIdleTimeoutSeconds, prefix+"tool_idle_timeout_seconds", defaults.ToolIdleTimeoutSeconds)
	applyRepeatLimitOverride(&base.RepeatToolCallLimit, prefix+"repeat_tool_call_limit", defaults.RepeatToolCallLimit)
	return base
}

// applyRepeatLimitOverride applies the per-provider repeated-identical-tool-call
// limit override. It mirrors applyStructuredOverride's unset/negative
// semantics but clamps to the count bound rather than the seconds bound, and
// preserves an explicit 0 (disabled).
func applyRepeatLimitOverride(dst *int, key string, def int) {
	if !viper.IsSet(key) {
		return
	}
	v := viper.GetInt(key)
	switch {
	case v < 0:
		v = def
	case v > maxRepeatToolCallLimit:
		v = maxRepeatToolCallLimit
	}
	*dst = v
}

func repeatToolCallLimitWithDefault(key string, def int) int {
	if !viper.IsSet(key) {
		return def
	}
	v := viper.GetInt(key)
	switch {
	case v < 0:
		return def
	case v > maxRepeatToolCallLimit:
		return maxRepeatToolCallLimit
	default:
		return v
	}
}

// applyStructuredOverride applies a per-provider watchdog override. Unset keys
// leave *dst untouched; a negative value resets to the supplied provider
// default; values above the cap are clamped.
func applyStructuredOverride(dst *int, key string, def int) {
	if !viper.IsSet(key) {
		return
	}
	v := viper.GetInt(key)
	switch {
	case v < 0:
		v = def
	case v > maxStructuredTimeoutSeconds:
		v = maxStructuredTimeoutSeconds
	}
	*dst = v
}

// intWithDefault returns the configured integer if the key is set, otherwise
// the default. Negative values mean "use the default" — matching the
// _structured_broker flag convention (-1=default, 0=disabled) — and values
// above maxStructuredTimeoutSeconds are clamped.
func intWithDefault(key string, def int) int {
	if v, ok := intFromConfig(key); ok {
		return v
	}
	return def
}

// intFromConfig returns the configured integer and true if the key is set to
// a non-negative value. Negative values are treated as unset (use default /
// inherit the global section), and values above maxStructuredTimeoutSeconds
// are clamped.
func intFromConfig(key string) (int, bool) {
	if !viper.IsSet(key) {
		return 0, false
	}
	v := viper.GetInt(key)
	if v < 0 {
		return 0, false
	}
	if v > maxStructuredTimeoutSeconds {
		v = maxStructuredTimeoutSeconds
	}
	return v, true
}
