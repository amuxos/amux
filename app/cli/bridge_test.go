package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/bridge/threadcontext"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

func TestBridgeFeishuCommandSurfaceAndFlags(t *testing.T) {
	cmd := newBridgeCmd()
	for _, args := range [][]string{{"feishu", "setup"}, {"feishu", "status"}, {"feishu", "test"}, {"feishu", "disable"}, {"feishu", "bindings"}, {"feishu", "bots"}, {"feishu", "bot-session", "status"}, {"feishu", "bot-session", "bind", "local/main"}, {"feishu", "bot-session", "off"}, {"feishu", "context", "explain", "root-1"}} {
		found, _, err := cmd.Find(args)
		if err != nil {
			t.Fatalf("Find(%v): %v", args, err)
		}
		want := args[1]
		if len(args) > 2 {
			want = args[2]
		}
		if found.Name() != want {
			t.Fatalf("Find(%v) = %s", args, found.Name())
		}
	}
	for _, args := range [][]string{{"feishu", "setup"}, {"feishu", "status"}, {"feishu", "test"}, {"feishu", "disable"}, {"feishu", "bindings"}, {"feishu", "bot-session", "status"}, {"feishu", "bot-session", "bind", "local/main"}, {"feishu", "bot-session", "off"}} {
		found, _, err := cmd.Find(args)
		if err != nil {
			t.Fatalf("Find(%v): %v", args, err)
		}
		if found.Flag("profile") == nil {
			t.Fatalf("%s missing --profile flag", found.CommandPath())
		}
	}
	found, _, err := cmd.Find([]string{"feishu", "setup"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	for _, name := range []string{"manual", "no-open-platform-auto", "no-start", "default-agent", "default-runtime", "default-model"} {
		if found.Flag(name) == nil {
			t.Fatalf("%s missing --%s flag", found.CommandPath(), name)
		}
	}
}

func TestBridgeFeishuSetupDefaultRuntimeRequiresAgentAndThreadMode(t *testing.T) {
	cmd := newFeishuSetupCmd()
	cmd.SetArgs([]string{"--default-runtime", "codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --default-agent") {
		t.Fatalf("setup error = %v, want default-agent requirement", err)
	}
}

func TestBridgeFeishuSetupDefaultModelRequiresRuntime(t *testing.T) {
	cmd := newFeishuSetupCmd()
	cmd.SetArgs([]string{"--interaction-mode", "thread", "--default-agent", "builtin/claude", "--default-model", "gpt-5.4"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --default-runtime") {
		t.Fatalf("setup error = %v, want default-runtime requirement", err)
	}
}

func TestConfigureFeishuSetupLaunchDefaultsPersistsComposite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := configureFeishuSetupLaunchDefaults(context.Background(), "default", "cli_bot_composite", "builtin/claude", "codex", "gpt-5.4"); err != nil {
		t.Fatalf("configureFeishuSetupLaunchDefaults: %v", err)
	}
	state, err := bridge.GetBotProfileStateFromDB(bridge.DefaultDBPath(), "feishu:cli_bot_composite", config.FeishuInteractionThread)
	if err != nil {
		t.Fatal(err)
	}
	if state.DefaultAgent != "builtin/claude" || state.DefaultRuntimeRef != "codex" || state.DefaultModelRef != "gpt-5.4" {
		t.Fatalf("state = %+v, want Claude/Codex/gpt-5.4", state)
	}
}

func TestLocalFeishuBotSessionStatusShowsBinding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{AppId: "cli_bot_a", AppSecret: "secret"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	if err := bridge.SetBotSessionInDB(bridge.DefaultDBPath(), bridge.BotSession{
		BridgeId:     "feishu:cli_bot_a",
		Enabled:      true,
		TargetID:     "target-1",
		TargetName:   "local",
		SessionName:  "bot-a-main",
		AgentProfile: "builtin/codex",
		AgentType:    "codex",
		Cwd:          "/repo/amux",
	}); err != nil {
		t.Fatalf("SetBotSessionInDB: %v", err)
	}

	status, err := localFeishuBotSessionStatus("bot_a")
	if err != nil {
		t.Fatalf("localFeishuBotSessionStatus: %v", err)
	}
	if status.Profile != "bot_a" || status.BridgeId != "feishu:cli_bot_a" || status.BotSession == nil || !status.BotSession.Enabled {
		t.Fatalf("status = %+v", status)
	}
	if status.DefaultAgent != "builtin/codex" {
		t.Fatalf("default agent = %q, want builtin/codex", status.DefaultAgent)
	}

	var out bytes.Buffer
	printFeishuBotSessionStatus(&out, status)
	text := out.String()
	for _, want := range []string{"bot-session:", "profile:      bot_a", "mode:", "default_agent: builtin/codex", "enabled:      yes", "session:      local/bot-a-main", "agent:        builtin/codex", "agent_type:   codex", "cwd:          /repo/amux"} {
		if !strings.Contains(text, want) {
			t.Fatalf("bot-session status output missing %q:\n%s", want, text)
		}
	}
}

func TestBridgeFeishuSetupDefaultAgentRequiresThreadMode(t *testing.T) {
	cmd := newFeishuSetupCmd()
	cmd.SetArgs([]string{"--default-agent", "builtin/claude"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --interaction-mode thread") {
		t.Fatalf("setup error = %v, want thread-mode requirement", err)
	}
}

func TestFinalizeFeishuSetupPersistsAuthoritativeDefaultAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	profile := "bot_a"
	secretsPath, err := bridge.ProfileSecretsPath(profile)
	if err != nil {
		t.Fatalf("ProfileSecretsPath: %v", err)
	}
	s := &bridge.Secrets{
		AppId:     "cli_bot_a",
		AppSecret: "secret",
		OpenId:    "ou_owner",
		BotName:   "amux",
		BotOpenId: "ou_bot",
	}
	if err := finalizeFeishuSetup(&cobra.Command{}, profile, secretsPath, s, true, config.FeishuInteractionThread, "builtin/claude"); err != nil {
		t.Fatalf("finalizeFeishuSetup: %v", err)
	}
	status, err := localFeishuBotSessionStatus(profile)
	if err != nil {
		t.Fatalf("localFeishuBotSessionStatus: %v", err)
	}
	if status.InteractionMode != config.FeishuInteractionThread || status.DefaultAgent != "builtin/claude" {
		t.Fatalf("status = %+v, want thread/builtin-claude", status)
	}
}

func TestConfigureFeishuSetupDefaultAgentWaitsForRegistryLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	registryPath := defaultAgentPackRegistryPath()
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		t.Fatalf("mkdir registry dir: %v", err)
	}
	lockFile, err := os.OpenFile(registryPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open registry lock: %v", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("hold registry lock: %v", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = configureFeishuSetupDefaultAgent(ctx, "default", "cli_bot_a", "builtin/claude")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("configureFeishuSetupDefaultAgent error = %v, want context deadline exceeded", err)
	}
}

func TestBridgeFeishuSetupNonInteractiveEmptyIsGraceful(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()

	oldStdin := os.Stdin
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin }()

	oldStderr := os.Stderr
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stderr = stderrW
	defer func() { os.Stderr = oldStderr }()
	defer stderrR.Close()

	cmd := newFeishuSetupCmd()
	cmd.SetArgs([]string{})
	// Non-interactive with empty input must NOT be a hard error: installers
	// (curl | sh) reach this step, and an error there aborts the install.
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup with empty stdin/stderr should be graceful, got error: %v", err)
	}
	if err := stderrW.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	data, err := io.ReadAll(stdoutR)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	text := string(data)
	// New guidance points at the domestic Feishu create-app page and tells the
	// user to rerun once they have credentials, instead of hard-erroring.
	for _, want := range []string{
		"https://open.feishu.cn/app",
		"重跑 `amux bridge feishu setup --manual` 回填 App ID / App Secret",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("setup output missing %q:\n%s", want, text)
		}
	}
	// The old hard-error guidance must be gone.
	if strings.Contains(text, "app_id 和 app_secret 不能为空") {
		t.Fatalf("setup still emits the old hard-error guidance:\n%s", text)
	}
}

func TestBridgeFeishuSetupScanWhenStderrIsTTY(t *testing.T) {
	oldStdin := os.Stdin
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin }()

	oldStdout := os.Stdout
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()
	defer stdoutR.Close()

	oldIsTerminal := isTerminalFn
	stdinFD := int(os.Stdin.Fd())
	stderrFD := int(os.Stderr.Fd())
	isTerminalFn = func(fd int) bool { return fd == stderrFD && fd != stdinFD }
	defer func() { isTerminalFn = oldIsTerminal }()

	oldProvision := provisionFeishuAppFn
	called := false
	provisionFeishuAppFn = func(ctx context.Context, opts bridge.ProvisionOptions) (*bridge.ProvisionResult, error) {
		called = true
		if opts.QRWriter == nil {
			t.Fatalf("ProvisionFeishuApp QRWriter is nil")
		}
		if opts.SkipAddons {
			t.Fatal("default setup unexpectedly skipped official registration Addons")
		}
		return nil, io.ErrUnexpectedEOF
	}
	defer func() { provisionFeishuAppFn = oldProvision }()

	cmd := newFeishuSetupCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup should degrade gracefully after fake provision error: %v", err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	if !called {
		t.Fatal("stdin was non-TTY and stderr was TTY, but scan provisioning was not attempted")
	}
}

func TestBridgeFeishuSetupWritesInteractionMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{
		AppId:     "cli_bot_a",
		AppSecret: "secret",
		OpenId:    "ou_owner",
		BotName:   "amux",
		BotOpenId: "ou_bot",
	}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}

	oldStdout := os.Stdout
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()

	oldStdin := os.Stdin
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	if _, err := stdinW.WriteString("\n\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin }()

	cmd := newFeishuSetupCmd()
	cmd.SetArgs([]string{"--manual", "--profile", "bot_a", "--interaction-mode", "singleton", "--no-start"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	_, _ = io.ReadAll(stdoutR)

	configText := readTestConfig(t, home)
	for _, want := range []string{
		"[bridge.feishu.profiles.bot_a.interaction]",
		"mode = 'singleton'",
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("config missing %q:\n%s", want, configText)
		}
	}
}

func TestBridgeFeishuSetupRejectsSingletonWhenGroupWatchConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
		return setConfigValue(cliConfigFile(raw), "bridge.feishu.profiles.bot_a.group_watch.enabled", "true")
	}); err != nil {
		t.Fatalf("enable groupWatch: %v", err)
	}
	if err := config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
		return setConfigValue(cliConfigFile(raw), "bridge.feishu.profiles.bot_a.group_watch.chat_ids", "oc_alarm")
	}); err != nil {
		t.Fatalf("set groupWatch chats: %v", err)
	}
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{
		AppId: "cli_bot_a", AppSecret: "secret", OpenId: "ou_owner", BotName: "amux", BotOpenId: "ou_bot",
	}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}

	oldStdin := os.Stdin
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	if _, err := stdinW.WriteString("\n\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = stdinW.Close()
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin }()

	cmd := newFeishuSetupCmd()
	cmd.SetArgs([]string{"--manual", "--profile", "bot_a", "--interaction-mode", "singleton", "--no-start"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "群值守") || !strings.Contains(err.Error(), "每个话题独立") {
		t.Fatalf("setup error = %v, want actionable 群值守 incompatibility", err)
	}
	if text := readTestConfig(t, home); strings.Contains(text, "mode = 'singleton'") || strings.Contains(text, "mode = \"singleton\"") {
		t.Fatalf("setup persisted singleton despite incompatibility:\n%s", text)
	}
}

func TestLocalFeishuBotSessionBindRejectsSingletonWhenGroupWatchConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
		cfg := cliConfigFile(raw)
		if err := setConfigValue(cfg, "bridge.feishu.profiles.bot_a.group_watch.enabled", "true"); err != nil {
			return err
		}
		return setConfigValue(cfg, "bridge.feishu.profiles.bot_a.group_watch.chat_ids", "oc_alarm")
	}); err != nil {
		t.Fatalf("configure groupWatch: %v", err)
	}

	_, err := localFeishuBotSessionBind(context.Background(), "bot_a", "missing-session")
	if err == nil || !strings.Contains(err.Error(), "群值守") || !strings.Contains(err.Error(), "每个话题独立") {
		t.Fatalf("local bind error = %v, want 群值守 incompatibility before session lookup", err)
	}
}

func TestBridgeFeishuSetupDoesNotPromptForOwnerOpenId(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets(bridge.DefaultProfileName, &bridge.Secrets{
		OpenId:    "ou_existing_owner",
		BotName:   "amux",
		BotOpenId: "ou_bot",
	}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}

	oldStdout := os.Stdout
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()

	oldStdin := os.Stdin
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	if _, err := stdinW.WriteString("cli_test\nsecret\nou_should_not_be_used\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin }()

	cmd := newFeishuSetupCmd()
	cmd.SetArgs([]string{"--manual", "--no-start"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	data, err := io.ReadAll(stdoutR)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	text := string(data)
	for _, notWant := range []string{
		"Open ID 说明",
		"你的 Open ID",
		"Open ID 未填写",
		"amux runtime start",
		"amux hub restart",
		"Feishu bridge 运行在 hub 上",
	} {
		if strings.Contains(text, notWant) {
			t.Fatalf("setup should not prompt for owner open_id %q:\n%s", notWant, text)
		}
	}
	if !strings.Contains(text, "amux system start") {
		t.Fatalf("setup --no-start should guide users through system start:\n%s", text)
	}
	if !strings.Contains(text, "私聊智能体确认负责人") {
		t.Fatalf("setup should guide owner confirmation by private chat:\n%s", text)
	}

	secrets, err := bridge.LoadProfileSecrets(bridge.DefaultProfileName)
	if err != nil {
		t.Fatalf("LoadProfileSecrets: %v", err)
	}
	if secrets.OpenId != "ou_existing_owner" {
		t.Fatalf("OpenId = %q, want preserved existing owner", secrets.OpenId)
	}
	if secrets.AppId != "cli_test" || secrets.AppSecret != "secret" {
		t.Fatalf("credentials = %+v, want updated app credentials", secrets)
	}
}

// Feishu open_id / bot identity are scoped to one app: switching the profile to
// a different App ID must reset them so owner re-establishes under the new app
// (feishu-bridge-product-model v0.44) instead of locking the operator out.
func TestReadFeishuCredentialsManualAppChangeResetsAppScopedIdentity(t *testing.T) {
	oldStdout := os.Stdout
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = stdoutW
	defer func() { os.Stdout = oldStdout }()

	oldStdin := os.Stdin
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	if _, err := stdinW.WriteString("cli_new\nnew_secret\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin }()

	s := readFeishuCredentialsManual(&bridge.Secrets{
		AppId:        "cli_old",
		AppSecret:    "old_secret",
		OpenId:       "ou_old_app_owner",
		NotifyChatId: "oc_notify",
		BotName:      "old-bot",
		BotOpenId:    "ou_old_bot",
	})
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	out, _ := io.ReadAll(stdoutR)
	if s == nil {
		t.Fatalf("readFeishuCredentialsManual returned nil:\n%s", out)
	}
	if s.AppId != "cli_new" || s.AppSecret != "new_secret" {
		t.Fatalf("credentials = %+v, want new app credentials", s)
	}
	if s.OpenId != "" || s.BotName != "" || s.BotOpenId != "" {
		t.Fatalf("app change must reset app-scoped identity (open_id is per-app), got %+v", s)
	}
	if s.NotifyChatId != "oc_notify" {
		t.Fatalf("NotifyChatId = %q, want tenant-scoped field preserved", s.NotifyChatId)
	}
	if !strings.Contains(string(out), "App ID 变化") {
		t.Fatalf("expected app-change reset notice in output:\n%s", out)
	}
}

func TestBridgeFeishuBindingsListsBridgeDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	t.Setenv("AMUX_HUB_ADDR", freeStatusTCPAddr(t))
	dbPath := filepath.Join(home, ".amux", "bridge.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE lark_chats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bridge_id TEXT NOT NULL DEFAULT '',
			root_msg_id TEXT NOT NULL,
			thread_id TEXT NOT NULL DEFAULT '',
			open_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			target_name TEXT NOT NULL,
			session_name TEXT NOT NULL,
			agent_type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			latest_agent_output TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO lark_chats (bridge_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_type, status)
		VALUES ('feishu:cli_test', 'root-1', 'thread-1', 'open-1', 'target-1', 'devbox', 'c1', 'codex', 'active');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.SaveProfileSecrets(bridge.DefaultProfileName, &bridge.Secrets{AppId: "cli_test", AppSecret: "secret"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}

	cmd := newBridgeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"feishu", "bindings"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("bindings: %v", err)
	}

	text := out.String()
	for _, want := range []string{"devbox", "c1", "codex", "active", "root-1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("bindings output missing %q:\n%s", want, text)
		}
	}
}

func TestLocalFeishuContextExplainShowsDecision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("coder", &bridge.Secrets{AppId: "cli_coder", AppSecret: "secret"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	store, err := threadcontext.OpenStore(threadcontext.DefaultDBPath())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.RecordEvent(ctx, threadcontext.ThreadEvent{
		ChatID:      "oc-workspace",
		RootMsgID:   "root-ctx",
		MessageID:   "m-planner",
		EventType:   threadcontext.EventTypeAgentOutput,
		ActorType:   "agent",
		ActorID:     "planner",
		BridgeId:    "feishu:cli_planner",
		TargetName:  "planner",
		SessionName: "planner-h5",
		Text:        "# H5 贪吃蛇方案",
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	pkg, err := store.BuildPackage(ctx, threadcontext.BuildRequest{
		ChatID:             "oc-workspace",
		RootMsgID:          "root-ctx",
		MessageID:          "m-request",
		BridgeId:           "feishu:cli_coder",
		BotProfile:         "coder",
		WorkspaceMode:      threadcontext.WorkspaceModeShared,
		RequestText:        "根据上述方案实现",
		ThreadContextLimit: 20,
	})
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if _, err := store.RecordDecision(ctx, pkg, "full prompt preview body"); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	resp, err := localFeishuContextExplain(ctx, "coder", "root-ctx", "", 0)
	if err != nil {
		t.Fatalf("localFeishuContextExplain: %v", err)
	}
	var out bytes.Buffer
	printFeishuContextExplain(&out, resp, false)
	text := out.String()
	for _, want := range []string{"Feishu context decision", "profile:   coder", "selected items", "planner/planner-h5", "# H5 贪吃蛇方案", "Use --verbose"} {
		if !strings.Contains(text, want) {
			t.Fatalf("context explain output missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Thread context:", "Prompt preview:", "full prompt preview body"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("compact context explain output contains %q:\n%s", unwanted, text)
		}
	}

	out.Reset()
	printFeishuContextExplain(&out, resp, true)
	text = out.String()
	for _, want := range []string{"Thread context:", "Prompt preview:", "full prompt preview body"} {
		if !strings.Contains(text, want) {
			t.Fatalf("verbose context explain output missing %q:\n%s", want, text)
		}
	}
}

func TestLocalFeishuStatusIncludesBotIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("planner", &bridge.Secrets{
		AppId:     "cli_test",
		AppSecret: "secret",
		BotName:   "build-bot",
		BotOpenId: "ou_bot",
	}); err != nil {
		t.Fatalf("SaveSecrets: %v", err)
	}

	status, err := localFeishuStatus("planner")
	if err != nil {
		t.Fatalf("localFeishuStatus: %v", err)
	}

	if status.BotName != "build-bot" || status.BotOpenId != "ou_bot" {
		t.Fatalf("status = %+v, want bot identity", status)
	}
	var out bytes.Buffer
	printFeishuStatus(&out, status)
	for _, want := range []string{"bot_name:       build-bot", "bot_open_id:    ou_bot"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status output missing %q:\n%s", want, out.String())
		}
	}
}

func TestLocalFeishuStatusDefaultsToDefaultProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets(bridge.DefaultProfileName, &bridge.Secrets{
		AppId:     "cli_default",
		AppSecret: "secret",
		OpenId:    "ou_default",
	}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}

	status, err := localFeishuStatus("")
	if err != nil {
		t.Fatalf("localFeishuStatus: %v", err)
	}
	if status.Profile != bridge.DefaultProfileName || status.BridgeId != "feishu:cli_default" || status.AppId != "cli_default" {
		t.Fatalf("status = %+v, want default profile", status)
	}
}

func TestLocalFeishuBotsListsProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("planner", &bridge.Secrets{AppId: "cli_planner", AppSecret: "secret", OpenId: "ou_planner"}); err != nil {
		t.Fatalf("SaveProfileSecrets planner: %v", err)
	}
	if err := bridge.SaveProfileSecrets("coder", &bridge.Secrets{AppId: "cli_coder", AppSecret: "secret", OpenId: "ou_coder"}); err != nil {
		t.Fatalf("SaveProfileSecrets coder: %v", err)
	}

	bots, err := localFeishuBots()
	if err != nil {
		t.Fatalf("localFeishuBots: %v", err)
	}
	if len(bots) != 2 || bots[0].Profile != "coder" || bots[1].Profile != "planner" {
		t.Fatalf("bots = %+v, want coder/planner", bots)
	}
	var out bytes.Buffer
	printFeishuBots(&out, bots)
	for _, want := range []string{"coder", "feishu:cli_coder", "planner", "feishu:cli_planner"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("bots output missing %q:\n%s", want, out.String())
		}
	}
}

func TestPrintFeishuStatusColorizesWhenForced(t *testing.T) {
	t.Setenv("AMUX_COLOR", "always")
	t.Setenv("NO_COLOR", "")
	var out bytes.Buffer
	printFeishuStatus(&out, &pb.FeishuStatusResponse{
		Configured:      true,
		BridgeDbHealthy: false,
		BotName:         "innie",
	})
	text := out.String()
	for _, want := range []string{
		"\x1b[1mFeishu bridge:\x1b[22m",
		"configured:     \x1b[32myes\x1b[0m",
		"bridge_db:",
		"\x1b[33mno\x1b[0m",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("colored Feishu status output missing %q:\n%q", want, text)
		}
	}
}

func TestEnsureLocalConfigForFeishuSetupPreservesExistingHubAddr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cfg := cliConfigFile{}
	if err := setConfigValue(cfg, "hub.addr", "10.0.0.1:7100"); err != nil {
		t.Fatalf("set hub.addr: %v", err)
	}
	if err := replaceCLIConfigFileForTest(cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := ensureLocalConfigForFeishuSetupWithInteraction("", ""); err != nil {
		t.Fatalf("ensureLocalConfigForFeishuSetupWithInteraction: %v", err)
	}

	cfg, err := readCLIConfigFile()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got, ok := configValue(cfg, "hub.addr")
	if !ok {
		t.Fatalf("hub.addr missing")
	}
	if got != "10.0.0.1:7100" {
		t.Fatalf("hub.addr = %q, want existing value", got)
	}
}
