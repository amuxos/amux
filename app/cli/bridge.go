package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/bridge/threadcontext"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/distribution"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newBridgeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:               "bridge",
		Short:             "Manage external bridge adapters",
		ValidArgsFunction: completeNoFiles,
	}
	c.AddCommand(newFeishuBridgeCmd())
	return c
}

func newFeishuBridgeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:               "feishu",
		Short:             "Manage the Feishu/Lark bridge adapter",
		ValidArgsFunction: completeNoFiles,
	}
	c.AddCommand(newFeishuSetupCmd(), newFeishuStatusCmd(), newFeishuTestCmd(), newFeishuDisableCmd(), newFeishuBindingsCmd(), newFeishuBotsCmd(), newFeishuBotSessionCmd(), newFeishuContextCmd(), newFeishuGroupSettingsCmd())
	return c
}

// newFeishuSetupCmd is an interactive wizard that writes Lark credentials to
// ~/.amux/secrets/feishu/<profile>.json (mode 0600).
func newFeishuSetupCmd() *cobra.Command {
	var profile string
	var noStart bool
	var interactionMode string
	var defaultAgent string
	var defaultRuntime string
	var defaultModel string
	var manual bool
	var noOpenPlatformAuto bool
	var legacyConsoleFallback bool
	c := &cobra.Command{
		Use:               "setup",
		Short:             "Configure Feishu bridge credentials (scan to provision, or --manual)",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile = feishuProfileOrDefault(profile)
			interactionMode = strings.TrimSpace(interactionMode)
			defaultAgent = strings.TrimSpace(defaultAgent)
			defaultRuntime = strings.TrimSpace(defaultRuntime)
			defaultModel = strings.TrimSpace(defaultModel)
			if interactionMode != "" && interactionMode != config.FeishuInteractionThread && interactionMode != config.FeishuInteractionSingleton {
				return fmt.Errorf("--interaction-mode must be thread or singleton")
			}
			if defaultAgent != "" {
				if interactionMode != config.FeishuInteractionThread {
					return fmt.Errorf("--default-agent requires --interaction-mode thread")
				}
				if _, err := resolveAgentProfile(defaultAgent); err != nil {
					return fmt.Errorf("resolve --default-agent %q: %w", defaultAgent, err)
				}
			}
			if defaultRuntime != "" && defaultAgent == "" {
				return fmt.Errorf("--default-runtime requires --default-agent")
			}
			if defaultModel != "" && defaultRuntime == "" {
				return fmt.Errorf("--default-model requires --default-runtime")
			}
			if defaultRuntime != "" {
				profileSpec, err := resolveAgentProfile(defaultAgent)
				if err != nil {
					return fmt.Errorf("resolve --default-agent %q: %w", defaultAgent, err)
				}
				if _, err := agentprofile.ProjectProfileLaunchChecked(profileSpec, nil, nil, agentprofile.LaunchOptions{Runtime: defaultRuntime, Model: defaultModel}); err != nil {
					return fmt.Errorf("validate Feishu default launch: %w", err)
				}
			}
			if interactionMode == config.FeishuInteractionSingleton {
				if err := validateLocalFeishuSingletonCompatibility(profile); err != nil {
					return err
				}
			}
			secretsPath, err := bridge.ProfileSecretsPath(profile)
			if err != nil {
				return err
			}

			// Load existing secrets as defaults.
			existing, _ := bridge.LoadProfileSecrets(profile)
			if existing == nil {
				existing = &bridge.Secrets{}
			}

			fmt.Println("=== amux bridge feishu setup ===")
			fmt.Println()

			stdinTTY := isTerminalFn(int(os.Stdin.Fd()))
			stderrTTY := isTerminalFn(int(os.Stderr.Fd()))

			var s *bridge.Secrets
			switch {
			case manual:
				// Manual paste path. It reads stdin and can therefore accept
				// piped credentials; keep it explicit behind --manual.
				s = readFeishuCredentialsManual(existing)
			case !stdinTTY && !stderrTTY:
				// Fully non-interactive fallback (e.g. curl | sh with stderr
				// redirected): there is nowhere reliable to show a QR. Print
				// guidance and exit 0 (not an error), so installers and reruns
				// stay friendly.
				printFeishuCreateGuide()
				fmt.Println("尚未连接可显示二维码的交互终端。建好应用后重跑 `amux bridge feishu setup --manual` 回填 App ID / App Secret。")
				s = nil
			default:
				// The scan flow renders QR codes to stderr and only polls the
				// network; it never reads stdin. Therefore stdin may be closed or
				// piped (remote shells / installer wrappers) as long as stderr is a
				// terminal where the user can scan the QR.
				s = provisionFeishuViaScan(cmd.Context(), existing, noOpenPlatformAuto, legacyConsoleFallback)
			}
			if s == nil {
				// Empty input, cancelled, or provisioning fell back to guidance;
				// not an error.
				return nil
			}

			return finalizeFeishuSetupWithLaunch(cmd, profile, secretsPath, s, noStart, interactionMode, defaultAgent, defaultRuntime, defaultModel)
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	c.Flags().StringVar(&interactionMode, "interaction-mode", "", "Feishu interaction mode for this profile: thread or singleton")
	c.Flags().StringVar(&defaultAgent, "default-agent", "", "Agent Profile to use for new thread sessions (requires --interaction-mode thread)")
	c.Flags().StringVar(&defaultRuntime, "default-runtime", "", "runtime adapter for new thread sessions (requires --default-agent)")
	c.Flags().StringVar(&defaultModel, "default-model", "", "model within --default-runtime for new thread sessions")
	_ = c.RegisterFlagCompletionFunc("default-runtime", completeRuntimeRefs)
	_ = c.RegisterFlagCompletionFunc("default-model", completeRuntimeModels("default-runtime"))
	c.Flags().BoolVar(&noStart, "no-start", false, "write config and credentials only; do not start/restart local runtime or hub")
	c.Flags().BoolVar(&manual, "manual", false, "skip QR provisioning; paste an existing app's App ID / App Secret")
	c.Flags().BoolVar(&noOpenPlatformAuto, "no-open-platform-auto", false, "select or create a minimal app by QR but skip official Addons configuration")
	if distribution.LegacyConsoleFallback {
		c.Flags().BoolVar(&legacyConsoleFallback, "legacy-console-fallback", false, "after official Addons, explicitly try the fragile legacy console publish fallback")
	}
	return c
}

// printFeishuCreateGuide prints the app-creation entry point, used by the
// manual and non-interactive paths.
func printFeishuCreateGuide() {
	fmt.Println("前往飞书开放平台创建企业自建应用：")
	fmt.Println("  https://open.feishu.cn/app   （Lark 国际版：https://open.larkoffice.com/app）")
	fmt.Println("创建后在「凭证与基础信息」获取 App ID (cli_xxx) 与 App Secret。")
	fmt.Println()
}

// printFeishuScopeChecklist prints the capability / event / permission config
// the bridge bot needs, used when auto-config is skipped or fails.
func printFeishuScopeChecklist() {
	manifest := bridge.RequiredFeishuAppManifest()
	fmt.Println("需要在开放平台完成的配置：")
	fmt.Println("  1. 应用能力 → 添加「机器人」")
	fmt.Println("  2. 事件订阅 → 订阅方式选「长连接」，添加事件：")
	fmt.Printf("       %s\n", strings.Join(manifest.TenantEvents, "、"))
	if len(manifest.UserEvents) > 0 {
		fmt.Printf("       用户身份事件：%s\n", strings.Join(manifest.UserEvents, "、"))
	}
	fmt.Println("  3. 回调配置 → 添加：")
	fmt.Printf("       %s\n", strings.Join(manifest.Callbacks, "、"))
	fmt.Println("  4. 权限管理 → 申请：")
	scopes := append(append([]string{}, manifest.TenantScopes...), manifest.UserScopes...)
	fmt.Printf("       %s\n", strings.Join(scopes, "、"))
	fmt.Println("  5. 版本管理与发布 → 确认已有可用版本；没有则创建并发布")
	fmt.Println()
}

// readFeishuCredentialsManual prompts for App ID / App Secret. Returns nil (not
// an error) when either is left blank, so setup degrades gracefully instead of
// failing. Keeps the v0.25 boundary: manual setup only configures bot
// credentials and never writes owner open_id.
func readFeishuCredentialsManual(existing *bridge.Secrets) *bridge.Secrets {
	printFeishuCreateGuide()
	sc := bufio.NewScanner(os.Stdin)
	read := func(prompt, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", prompt, def)
		} else {
			fmt.Printf("%s: ", prompt)
		}
		if !sc.Scan() {
			return def
		}
		v := strings.TrimSpace(sc.Text())
		if v == "" {
			return def
		}
		return v
	}
	appID := read("App ID     (cli_xxx)", existing.AppId)
	appSecret := read("App Secret (xxx)", existing.AppSecret)
	s := bridge.CarrySecretsForApp(existing, appID)
	s.AppSecret = appSecret
	if existing.AppId != "" && appID != "" && appID != existing.AppId {
		fmt.Println("检测到 App ID 变化：旧应用的负责人/bot 身份已重置（飞书 open_id 按应用隔离，不能跨应用沿用）。")
	}
	fmt.Println()
	if s.AppId == "" || s.AppSecret == "" {
		fmt.Println("尚未填写 App ID / App Secret。建好应用后重跑 `amux bridge feishu setup` 回填。")
		return nil
	}
	return s
}

// provisionFeishuViaScan creates a Feishu app via the official device-code (QR)
// flow. Unless skipped, registration Addons configures the exact bridge
// manifest on the same confirmation page. The legacy console publisher runs
// only when explicitly requested. Registration failure prints the manual
// checklist and returns nil so setup degrades gracefully rather than aborting.
func provisionFeishuViaScan(ctx context.Context, existing *bridge.Secrets, skipAddons, legacyConsoleFallback bool) *bridge.Secrets {
	if skipAddons {
		fmt.Println("将通过扫码选择已有应用或创建最小飞书智能体应用，自动配置已关闭（也可用 --manual 手动粘贴已有应用凭证）。")
	} else {
		fmt.Println("将通过扫码选择已有应用或创建智能体应用，并用飞书官方 registration Addons 配置（也可用 --manual 手动粘贴已有应用凭证）。")
	}
	res, err := provisionFeishuAppFn(ctx, bridge.ProvisionOptions{
		QRWriter:   os.Stderr,
		OnStatus:   func(msg string) { fmt.Fprintln(os.Stderr, msg) },
		SkipAddons: skipAddons,
		AppID:      existing.AppId,
	})
	if err != nil {
		fmt.Printf("扫码配置应用失败：%v\n\n", err)
		printFeishuCreateGuide()
		printFeishuScopeChecklist()
		fmt.Println("可稍后重试 `amux bridge feishu setup`，或用 `--manual` 手动粘贴已有应用凭证。")
		return nil
	}
	fmt.Printf("\n✅ 已选择或创建应用：App ID=%s\n", res.AppID)

	s := bridge.CarrySecretsForApp(existing, res.AppID)
	s.AppSecret = res.AppSecret
	if existing.AppId != "" && existing.AppId != res.AppID {
		fmt.Println("检测到 App ID 变化：旧应用的负责人/bot 身份已重置（飞书 open_id 按应用隔离，不能跨应用沿用）。")
	}
	// The scanning user is the operator, so seed owner from the device-flow
	// identity — but never override an owner already established for this same
	// app (rerun keeps the existing owner, consistent with the product model).
	if s.OpenId == "" && res.OwnerOpenID != "" {
		s.OpenId = res.OwnerOpenID
		fmt.Printf("已将扫码用户设为负责人 (open_id=%s)\n", res.OwnerOpenID)
	}

	if skipAddons {
		fmt.Println("\n已跳过开放平台自动配置 (--no-open-platform-auto)。请到开放平台手动完成：")
		printFeishuScopeChecklist()
		return s
	}

	fmt.Println("\n✅ 已通过飞书官方 registration Addons 提交最小权限、事件与回调配置。")
	fmt.Println("提示：官方资料未承诺应用版本已在所有租户自动发布；首次使用前请核对应用可用性和版本状态。")
	if !legacyConsoleFallback {
		return s
	}

	fmt.Println("\n正在显式尝试 legacy console fallback（可能需要扫码登录开放平台）…")
	cres := automateOpenPlatformSetupFn(ctx, res.AppID, bridge.ConsoleOptions{
		Manifest: bridge.RequiredScopeManifest(),
		OnQRCode: func(qrText, _ string) {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "请用飞书 App 扫码登录开放平台（用于自动配置权限）：")
			fmt.Fprintln(os.Stderr, qrText)
		},
		OnStatus: func(msg string) { fmt.Fprintln(os.Stderr, msg) },
	})
	if cres.OK {
		fmt.Printf("✅ legacy console fallback 已导入 %d 个权限，跳过 %d 个", cres.ScopeCount, cres.SkippedScopeCount)
		if cres.VersionID != "" {
			fmt.Printf("，已提交发布版本 %s", cres.VersionID)
		}
		fmt.Println("。")
		if cres.ScopeWarning != "" {
			fmt.Printf("提示：部分权限自动配置未完成（%s）；若收发消息异常请到开放平台补齐。\n", cres.ScopeWarning)
		}
	} else {
		fmt.Printf("legacy console fallback 未完成（%s）。应用已创建，请到开放平台手动核对剩余配置：\n", cres.Reason)
		printFeishuScopeChecklist()
	}
	return s
}

// finalizeFeishuSetup fetches bot identity, persists credentials, writes the
// base config, and starts local services. Shared by the scan and manual paths.
func finalizeFeishuSetup(cmd *cobra.Command, profile, secretsPath string, s *bridge.Secrets, noStart bool, interactionMode, defaultAgent string) error {
	return finalizeFeishuSetupWithLaunch(cmd, profile, secretsPath, s, noStart, interactionMode, defaultAgent, "", "")
}

func finalizeFeishuSetupWithLaunch(cmd *cobra.Command, profile, secretsPath string, s *bridge.Secrets, noStart bool, interactionMode, defaultAgent, defaultRuntime, defaultModel string) error {
	if s.BotName == "" || s.BotOpenId == "" {
		fmt.Println("正在自动获取 bot identity...")
		lookupCtx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		identity, err := bridge.FetchBotIdentityWithCredentials(lookupCtx, s.AppId, s.AppSecret)
		cancel()
		if err == nil {
			if s.BotName == "" {
				s.BotName = identity.Name
			}
			if s.BotOpenId == "" {
				s.BotOpenId = identity.OpenId
			}
			fmt.Printf("已识别智能体: name=%s open_id=%s\n", s.BotName, s.BotOpenId)
		} else {
			fmt.Printf("自动获取 bot identity 失败：%v\n", err)
			fmt.Println("bridge 收到消息时会再次尝试自动获取。")
		}
	}

	if err := bridge.SaveProfileSecrets(profile, s); err != nil {
		return fmt.Errorf("保存凭证失败: %w", err)
	}
	if strings.TrimSpace(defaultRuntime) != "" {
		if err := configureFeishuSetupLaunchDefaults(cmd.Context(), profile, s.AppId, defaultAgent, defaultRuntime, defaultModel); err != nil {
			return fmt.Errorf("写入智能体默认执行组合失败: %w", err)
		}
	} else if strings.TrimSpace(defaultAgent) != "" {
		if err := configureFeishuSetupDefaultAgent(cmd.Context(), profile, s.AppId, defaultAgent); err != nil {
			return fmt.Errorf("写入智能体默认专家失败: %w", err)
		}
	}
	if err := ensureLocalConfigForFeishuSetupWithInteraction(profile, interactionMode); err != nil {
		return fmt.Errorf("写入本机默认配置失败: %w", err)
	}
	fmt.Printf("\n✅ profile %s 凭证已保存到 %s\n", profile, secretsPath)
	fmt.Println()
	if noStart {
		fmt.Println("服务未启动，因为设置了 --no-start。后续请执行：")
		fmt.Println("  amux system start")
		fmt.Println("然后在飞书里私聊智能体确认负责人。")
	} else {
		fmt.Println("正在协调本机 amux system，并刷新 Feishu bridge...")
		if err := ensureFeishuSetupServices(os.Stdout); err != nil {
			return fmt.Errorf("启动本机服务失败: %w", err)
		}
		fmt.Println()
		fmt.Println("下一步：把智能体拉进群并 @智能体，或私聊智能体。")
	}
	fmt.Println()
	fmt.Println("注意：请在承载飞书智能体的 amux 控制机上执行 setup。")
	return nil
}

func configureFeishuSetupDefaultAgent(ctx context.Context, profile, appID, defaultAgent string) error {
	defaultAgent = strings.TrimSpace(defaultAgent)
	return agentprofile.WithPackRegistryLockContext(ctx, defaultAgentPackRegistryPath(), func() error {
		if _, err := resolveAgentProfile(defaultAgent); err != nil {
			return fmt.Errorf("resolve default agent %q: %w", defaultAgent, err)
		}
		bridgeID := bridge.BridgeIdFromAppId(appID)
		_, err := bridge.ConfigureThreadBotDefaultAgentInDB(bridge.DefaultDBPath(), profile, bridgeID, defaultAgent)
		return err
	})
}

func configureFeishuSetupLaunchDefaults(ctx context.Context, profile, appID, defaultAgent, defaultRuntime, defaultModel string) error {
	defaultAgent = strings.TrimSpace(defaultAgent)
	defaultRuntime = strings.TrimSpace(defaultRuntime)
	defaultModel = strings.TrimSpace(defaultModel)
	return agentprofile.WithPackRegistryLockContext(ctx, defaultAgentPackRegistryPath(), func() error {
		resolved, err := resolveAgentProfile(defaultAgent)
		if err != nil {
			return fmt.Errorf("resolve default agent %q: %w", defaultAgent, err)
		}
		if _, err := agentprofile.ProjectProfileLaunchChecked(resolved, nil, nil, agentprofile.LaunchOptions{Runtime: defaultRuntime, Model: defaultModel}); err != nil {
			return err
		}
		bridgeID := bridge.BridgeIdFromAppId(appID)
		_, err = bridge.ConfigureThreadBotLaunchDefaultsInDB(bridge.DefaultDBPath(), profile, bridgeID, defaultAgent, defaultRuntime, defaultModel)
		return err
	})
}

var isTerminalFn = term.IsTerminal

var provisionFeishuAppFn = bridge.ProvisionFeishuApp

var automateOpenPlatformSetupFn = bridge.AutomateOpenPlatformSetup

var ensureFeishuSetupServices = ensureLocalFeishuSetupServices

func ensureLocalFeishuSetupServices(out io.Writer) error {
	runtime := runtimeService()
	if _, ok := readRunningPID(runtime.PIDPath); !ok {
		if err := runtime.Start(false); err != nil {
			return fmt.Errorf("start local runtime: %w", err)
		}
	}

	hub := hubService()
	if _, ok := readRunningPID(hub.PIDPath); ok {
		if err := hub.Restart(false); err != nil {
			return fmt.Errorf("refresh Feishu bridge service: %w", err)
		}
	} else {
		if err := hub.Start(false); err != nil {
			return fmt.Errorf("start Feishu bridge service: %w", err)
		}
	}
	fmt.Fprintln(out, "amux system: local services ready")
	fmt.Fprintln(out, "Feishu bridge: refreshed")
	return nil
}

func ensureLocalConfigForFeishuSetupWithInteraction(profile, interactionMode string) error {
	return config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
		cfg := cliConfigFile(raw)
		if err := ensureBaseInitConfig(cfg); err != nil {
			return err
		}
		if err := setIfConfigUnset(cfg, "hub.addr", defaultLocalHubAddr); err != nil {
			return err
		}
		if strings.TrimSpace(interactionMode) != "" {
			profile = feishuProfileOrDefault(profile)
			key := fmt.Sprintf("bridge.feishu.profiles.%s.interaction.mode", profile)
			if err := setConfigValue(cfg, key, interactionMode); err != nil {
				return err
			}
		}
		return nil
	})
}

// newFeishuStatusCmd shows whether Lark credentials are configured.
func newFeishuStatusCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:               "status",
		Short:             "Show Feishu bridge configuration status",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			status, err := feishuStatus(ctx, profile)
			if err != nil {
				return err
			}
			printFeishuStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	return c
}

func newFeishuTestCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:               "test",
		Short:             "Validate Feishu/Lark bridge credentials locally",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			status, err := feishuStatus(ctx, profile)
			if err != nil {
				return err
			}
			if !status.Configured {
				return fmt.Errorf("Feishu credentials are incomplete; run `amux bridge feishu setup`")
			}
			if !status.BridgeDbHealthy {
				return fmt.Errorf("Feishu bridge DB is not healthy: %s", status.Error)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Feishu bridge looks valid\n")
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	return c
}

func newFeishuDisableCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:               "disable",
		Short:             "Disable the Feishu/Lark bridge adapter",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile = feishuProfileOrDefault(profile)
			disabledPath, err := bridge.DisableProfileSecrets(profile)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "Feishu bridge is already disabled")
					return nil
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Feishu bridge profile %s disabled; credentials moved to %s\n", profile, disabledPath)
			fmt.Fprintln(cmd.OutOrStdout(), "Note: this changes hub-local secrets. Run disable on the hub host.")
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	return c
}

func newFeishuBindingsCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:               "bindings",
		Short:             "List Feishu topic bindings",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			bindings, err := feishuBindings(ctx, profile)
			if err != nil {
				return err
			}
			return printFeishuBindings(cmd.OutOrStdout(), bindings)
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	return c
}

func newFeishuBotsCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "bots",
		Short:             "List Feishu bot profiles",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			bots, err := feishuBots(ctx)
			if err != nil {
				return err
			}
			printFeishuBots(cmd.OutOrStdout(), bots)
			return nil
		},
	}
}

func newFeishuBotSessionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:               "bot-session",
		Short:             "Manage Feishu singleton bot-session binding",
		ValidArgsFunction: completeNoFiles,
	}
	c.AddCommand(newFeishuBotSessionStatusCmd(), newFeishuBotSessionBindCmd(), newFeishuBotSessionOffCmd())
	return c
}

func newFeishuBotSessionStatusCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:               "status",
		Short:             "Show Feishu singleton bot-session binding",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			status, err := feishuBotSessionStatus(ctx, profile)
			if err != nil {
				return err
			}
			printFeishuBotSessionStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	return c
}

func newFeishuBotSessionBindCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:               "bind <target/session>",
		Short:             "Bind a Feishu bot profile to an existing amux session",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			status, err := feishuBotSessionBind(ctx, profile, args[0])
			if err != nil {
				return err
			}
			printFeishuBotSessionStatus(cmd.OutOrStdout(), status)
			if status.Error != "" {
				return fmt.Errorf("%s", status.Error)
			}
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	return c
}

func newFeishuBotSessionOffCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:               "off",
		Short:             "Disable Feishu singleton bot-session routing",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			status, err := feishuBotSessionOff(ctx, profile)
			if err != nil {
				return err
			}
			printFeishuBotSessionStatus(cmd.OutOrStdout(), status)
			if status.Error != "" {
				return fmt.Errorf("%s", status.Error)
			}
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	return c
}

func newFeishuContextCmd() *cobra.Command {
	c := &cobra.Command{
		Use:               "context",
		Short:             "Inspect Feishu thread context decisions",
		ValidArgsFunction: completeNoFiles,
	}
	c.AddCommand(newFeishuContextExplainCmd())
	return c
}

func newFeishuContextExplainCmd() *cobra.Command {
	var profile string
	var messageID string
	var decisionID int64
	var verbose bool
	c := &cobra.Command{
		Use:               "explain <root_msg_id>",
		Short:             "Explain the latest Feishu thread context decision",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			resp, err := feishuContextExplain(ctx, profile, args[0], messageID, decisionID)
			if err != nil {
				return err
			}
			printFeishuContextExplain(cmd.OutOrStdout(), resp, verbose)
			if resp.Error != "" {
				return fmt.Errorf("%s", resp.Error)
			}
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", bridge.DefaultProfileName, "Feishu bot profile name")
	c.Flags().StringVar(&messageID, "message-id", "", "Feishu message_id for a specific user request")
	c.Flags().Int64Var(&decisionID, "decision-id", 0, "context decision id")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "show full thread context and prompt preview")
	return c
}

func feishuStatus(ctx context.Context, profile string) (*pb.FeishuStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	hc, configured, hubErr := feishuHubClient(ctx)
	if hubErr == nil && hc != nil {
		defer hc.Close()
		return hc.c.FeishuStatus(ctx, &pb.FeishuStatusRequest{Profile: profile})
	}
	if configured && !localFeishuStateExists(profile) {
		return nil, hubErr
	}
	return localFeishuStatus(profile)
}

func localFeishuStatus(profile string) (*pb.FeishuStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	out := &pb.FeishuStatusResponse{Profile: profile}
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err == nil {
		out.Configured = secrets.AppId != "" && secrets.AppSecret != ""
		out.BridgeId = bridge.BridgeIdFromAppId(secrets.AppId)
		out.AppId = secrets.AppId
		out.OpenId = secrets.OpenId
		out.NotifyChatId = secrets.NotifyChatId
		out.BotName = secrets.BotName
		out.BotOpenId = secrets.BotOpenId
	} else if !os.IsNotExist(err) {
		out.Error = err.Error()
	}
	dbPath := bridge.DefaultDBPath()
	if !fileExists(dbPath) {
		out.BridgeDbHealthy = true
		return out, nil
	}
	bindings, err := bridge.ListBindingsForBridgeId(dbPath, out.BridgeId)
	if err != nil {
		out.BridgeDbHealthy = false
		if out.Error == "" {
			out.Error = err.Error()
		}
		return out, nil
	}
	out.BridgeDbHealthy = true
	out.BindingCount = int32(len(bindings))
	return out, nil
}

func printFeishuStatus(out interface{ Write([]byte) (int, error) }, status *pb.FeishuStatusResponse) {
	st := styleFor(out)
	profile := feishuProfileOrDefault(status.Profile)
	secretsPath, _ := bridge.ProfileSecretsPath(profile)
	configured := yesNo(status.Configured)
	dbHealthy := yesNo(status.BridgeDbHealthy)
	fmt.Fprintf(out, "%s\n", st.heading("Feishu bridge:"))
	fmt.Fprintf(out, "  profile:        %s\n", profile)
	fmt.Fprintf(out, "  configured:     %s\n", st.state(configured))
	fmt.Fprintf(out, "  secrets:        %s (%s)\n", secretsPath, st.state(existsWord(fileExists(secretsPath))))
	fmt.Fprintf(out, "  bridge_db:      %s (%s)\n", bridge.DefaultDBPath(), st.state(dbHealthy))
	fmt.Fprintf(out, "  bridge_id:      %s\n", status.BridgeId)
	fmt.Fprintf(out, "  app_id:         %s\n", status.AppId)
	fmt.Fprintf(out, "  open_id:        %s\n", status.OpenId)
	fmt.Fprintf(out, "  notify_chat_id: %s\n", status.NotifyChatId)
	fmt.Fprintf(out, "  bot_name:       %s\n", status.BotName)
	fmt.Fprintf(out, "  bot_open_id:    %s\n", status.BotOpenId)
	fmt.Fprintf(out, "  bindings:       %d\n", status.BindingCount)
	if status.Error != "" {
		fmt.Fprintf(out, "  error:          %s\n", st.fail(status.Error))
	}
}

func feishuBindings(ctx context.Context, profile string) ([]*pb.FeishuBinding, error) {
	profile = feishuProfileOrDefault(profile)
	hc, configured, hubErr := feishuHubClient(ctx)
	if hubErr == nil && hc != nil {
		defer hc.Close()
		resp, err := hc.c.FeishuBindings(ctx, &pb.FeishuBindingsRequest{Profile: profile})
		if err != nil {
			return nil, err
		}
		return resp.Bindings, nil
	}
	if configured && !localFeishuStateExists(profile) {
		return nil, hubErr
	}
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	local, err := bridge.ListBindingsForBridgeId(bridge.DefaultDBPath(), bridge.BridgeIdFromAppId(secrets.AppId))
	if err != nil {
		return nil, err
	}
	out := make([]*pb.FeishuBinding, 0, len(local))
	for _, b := range local {
		out = append(out, &pb.FeishuBinding{
			BridgeId:    b.BridgeId,
			RootMsgId:   b.RootMsgID,
			ThreadId:    b.ThreadID,
			OpenId:      b.OpenId,
			TargetId:    b.TargetID,
			TargetName:  b.TargetName,
			SessionName: b.SessionName,
			AgentType:   b.AgentType,
			Status:      b.Status,
		})
	}
	return out, nil
}

func feishuBotSessionStatus(ctx context.Context, profile string) (*pb.FeishuBotSessionStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	hc, configured, hubErr := feishuHubClient(ctx)
	if hubErr == nil && hc != nil {
		defer hc.Close()
		return hc.c.FeishuBotSessionStatus(ctx, &pb.FeishuBotSessionStatusRequest{Profile: profile})
	}
	if configured && !localFeishuStateExists(profile) {
		return nil, hubErr
	}
	return localFeishuBotSessionStatus(profile)
}

func feishuBotSessionBind(ctx context.Context, profile, sessionRef string) (*pb.FeishuBotSessionStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	hc, configured, hubErr := feishuHubClient(ctx)
	if hubErr == nil && hc != nil {
		defer hc.Close()
		return hc.c.FeishuBotSessionBind(ctx, &pb.FeishuBotSessionBindRequest{Profile: profile, SessionRef: sessionRef})
	}
	if configured {
		return nil, hubErr
	}
	return localFeishuBotSessionBind(ctx, profile, sessionRef)
}

func feishuBotSessionOff(ctx context.Context, profile string) (*pb.FeishuBotSessionStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	hc, configured, hubErr := feishuHubClient(ctx)
	if hubErr == nil && hc != nil {
		defer hc.Close()
		return hc.c.FeishuBotSessionOff(ctx, &pb.FeishuBotSessionOffRequest{Profile: profile})
	}
	if configured {
		return nil, hubErr
	}
	return localFeishuBotSessionOff(profile)
}

func localFeishuBotSessionStatus(profile string) (*pb.FeishuBotSessionStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	fallbackMode, err := config.GetBridgeFeishuInteractionMode(profile)
	out := &pb.FeishuBotSessionStatusResponse{Profile: profile, InteractionMode: fallbackMode}
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.BridgeId = bridge.BridgeIdFromAppId(secrets.AppId)
	if out.BridgeId == "" || !fileExists(bridge.DefaultDBPath()) {
		return out, nil
	}
	state, err := bridge.GetBotProfileStateFromDB(bridge.DefaultDBPath(), out.BridgeId, fallbackMode)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.InteractionMode = state.InteractionMode
	out.DefaultAgent = state.DefaultAgent
	bs, err := bridge.GetBotSessionFromDB(bridge.DefaultDBPath(), out.BridgeId)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.BotSession = localFeishuBotSessionToPB(bs)
	return out, nil
}

func localFeishuBotSessionBind(ctx context.Context, profile, sessionRef string) (*pb.FeishuBotSessionStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	if err := validateLocalFeishuSingletonCompatibility(profile); err != nil {
		return nil, err
	}
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		return nil, err
	}
	bridgeID := bridge.BridgeIdFromAppId(secrets.AppId)
	if bridgeID == "" {
		return nil, fmt.Errorf("Feishu profile %s missing app_id", profile)
	}
	lc, err := newLocalRuntimeClient()
	if err != nil {
		return nil, err
	}
	defer lc.Close()
	sessions, err := lc.List(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := (&sessionResolver{
		listTargets: func(context.Context) ([]targetSessions, error) {
			return []targetSessions{{
				target:   targetRef{ID: "local", Name: "local"},
				sessions: sessions,
			}}, nil
		},
	}).ResolveSession(ctx, sessionRef)
	if err != nil {
		return nil, err
	}
	targetID := strings.TrimSpace(resolved.Target.ID)
	if targetID == "" {
		targetID = strings.TrimSpace(resolved.Target.Name)
	}
	targetName := strings.TrimSpace(resolved.Target.Name)
	if targetName == "" {
		targetName = targetID
	}
	if err := bridge.SetBotSessionInDB(bridge.DefaultDBPath(), bridge.BotSession{
		BridgeId:     bridgeID,
		Enabled:      true,
		TargetID:     targetID,
		TargetName:   targetName,
		SessionName:  resolved.Session.Name,
		AgentProfile: resolved.Session.AgentProfile,
		AgentType:    resolved.Session.AgentType,
		Cwd:          resolved.Session.Cwd,
	}); err != nil {
		return nil, err
	}
	_ = config.SetBridgeFeishuInteractionMode(profile, config.FeishuInteractionSingleton)
	return localFeishuBotSessionStatus(profile)
}

func validateLocalFeishuSingletonCompatibility(profile string) error {
	cfg, err := readCLIConfigFile()
	if err != nil {
		return err
	}
	return config.ValidateBridgeFeishuGroupWatchInteraction(cliFeishuGroupWatchConfig(cfg, profile), config.FeishuInteractionSingleton)
}

func cliFeishuGroupWatchConfig(cfg cliConfigFile, profile string) config.BridgeFeishuGroupWatchConfig {
	groupWatch := config.BridgeFeishuGroupWatchConfig{
		Enabled: configBoolValue(cfg, "bridge.feishu.group_watch.enabled"),
		ChatIDs: configStringSliceValue(cfg, "bridge.feishu.group_watch.chat_ids"),
	}
	base := "bridge.feishu.profiles." + feishuProfileOrDefault(profile) + ".group_watch."
	if _, ok := nestedConfigValue(map[string]any(cfg), strings.Split(base+"enabled", ".")); ok {
		groupWatch.Enabled = configBoolValue(cfg, base+"enabled")
	}
	if _, ok := nestedConfigValue(map[string]any(cfg), strings.Split(base+"chat_ids", ".")); ok {
		groupWatch.ChatIDs = configStringSliceValue(cfg, base+"chat_ids")
	}
	return groupWatch
}

func configBoolValue(cfg cliConfigFile, key string) bool {
	value, ok := nestedConfigValue(map[string]any(cfg), strings.Split(key, "."))
	if !ok {
		return false
	}
	parsed, _ := strconv.ParseBool(strings.TrimSpace(fmt.Sprint(value)))
	return parsed
}

func configStringSliceValue(cfg cliConfigFile, key string) []string {
	value, ok := nestedConfigValue(map[string]any(cfg), strings.Split(key, "."))
	if !ok {
		return nil
	}
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return strings.Fields(fmt.Sprint(value))
	}
}

func localFeishuBotSessionOff(profile string) (*pb.FeishuBotSessionStatusResponse, error) {
	profile = feishuProfileOrDefault(profile)
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		return nil, err
	}
	bridgeID := bridge.BridgeIdFromAppId(secrets.AppId)
	if bridgeID == "" {
		return nil, fmt.Errorf("Feishu profile %s missing app_id", profile)
	}
	if err := bridge.DisableBotSessionInDB(bridge.DefaultDBPath(), bridgeID); err != nil {
		return nil, err
	}
	_ = config.SetBridgeFeishuInteractionMode(profile, config.FeishuInteractionThread)
	return localFeishuBotSessionStatus(profile)
}

func localFeishuBotSessionToPB(bs *bridge.BotSession) *pb.FeishuBotSession {
	if bs == nil {
		return nil
	}
	return &pb.FeishuBotSession{
		BridgeId:           bs.BridgeId,
		Enabled:            bs.Enabled,
		TargetId:           bs.TargetID,
		TargetName:         bs.TargetName,
		SessionName:        bs.SessionName,
		AgentProfile:       bs.AgentProfile,
		AgentType:          bs.AgentType,
		LastReplyKind:      bs.LastReplyKind,
		LastReplyChatId:    bs.LastReplyChatID,
		LastReplyOpenId:    bs.LastReplyOpenID,
		LastInputMessageId: bs.LastInputMessageID,
		Cwd:                bs.Cwd,
	}
}

func feishuBots(ctx context.Context) ([]*pb.FeishuBot, error) {
	hc, _, hubErr := feishuHubClient(ctx)
	if hubErr == nil && hc != nil {
		defer hc.Close()
		resp, err := hc.c.FeishuBots(ctx, &pb.FeishuBotsRequest{})
		if err != nil {
			return nil, err
		}
		return resp.Bots, nil
	}
	return localFeishuBots()
}

func feishuContextExplain(ctx context.Context, profile, rootMsgID, messageID string, decisionID int64) (*pb.FeishuContextExplainResponse, error) {
	profile = feishuProfileOrDefault(profile)
	hc, configured, hubErr := feishuHubClient(ctx)
	if hubErr == nil && hc != nil {
		defer hc.Close()
		return hc.c.FeishuContextExplain(ctx, &pb.FeishuContextExplainRequest{
			Profile:    profile,
			RootMsgId:  rootMsgID,
			MessageId:  messageID,
			DecisionId: decisionID,
		})
	}
	if configured && !localFeishuStateExists(profile) {
		return nil, hubErr
	}
	return localFeishuContextExplain(ctx, profile, rootMsgID, messageID, decisionID)
}

func localFeishuContextExplain(ctx context.Context, profile, rootMsgID, messageID string, decisionID int64) (*pb.FeishuContextExplainResponse, error) {
	profile = feishuProfileOrDefault(profile)
	out := &pb.FeishuContextExplainResponse{Profile: profile}
	secrets, err := bridge.LoadProfileSecrets(profile)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.BridgeId = bridge.BridgeIdFromAppId(secrets.AppId)
	store, err := threadcontext.OpenStore(threadcontext.DefaultDBPath())
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	defer store.Close()
	explain, err := store.Explain(ctx, threadcontext.ExplainRequest{
		RootMsgID:  rootMsgID,
		MessageID:  messageID,
		BridgeId:   out.BridgeId,
		BotProfile: profile,
		DecisionID: decisionID,
	})
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.Decision = feishuContextDecisionToPB(explain.Decision)
	out.SelectedItems = make([]*pb.FeishuContextItem, 0, len(explain.SelectedItems))
	for _, item := range explain.SelectedItems {
		out.SelectedItems = append(out.SelectedItems, feishuContextItemToPB(item))
	}
	return out, nil
}

func feishuHubClient(_ context.Context) (*hubClient, bool, error) {
	_ = loadUserConfig()
	addr := hubAddr()
	if addr == "" {
		return nil, false, nil
	}
	hc, err := newHubClient(addr)
	if err != nil {
		return nil, true, err
	}
	return hc, true, nil
}

func localFeishuStateExists(profile string) bool {
	secretsPath, _ := bridge.ProfileSecretsPath(feishuProfileOrDefault(profile))
	for _, path := range []string{bridge.DefaultDBPath(), threadcontext.DefaultDBPath(), secretsPath} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func localFeishuBots() ([]*pb.FeishuBot, error) {
	profiles, err := bridge.ListSecretProfiles()
	if err != nil {
		return nil, err
	}
	out := make([]*pb.FeishuBot, 0, len(profiles))
	for _, profile := range profiles {
		bot := &pb.FeishuBot{Profile: profile}
		secrets, err := bridge.LoadProfileSecrets(profile)
		if err != nil {
			bot.Error = err.Error()
			out = append(out, bot)
			continue
		}
		bot.Configured = secrets.AppId != "" && secrets.AppSecret != ""
		bot.AppId = secrets.AppId
		bot.OpenId = secrets.OpenId
		bot.BotName = secrets.BotName
		bot.BotOpenId = secrets.BotOpenId
		bot.BridgeId = bridge.BridgeIdFromAppId(secrets.AppId)
		if bindings, err := bridge.ListBindingsForBridgeId(bridge.DefaultDBPath(), bot.BridgeId); err == nil {
			bot.BindingCount = int32(len(bindings))
		} else {
			bot.Error = err.Error()
		}
		out = append(out, bot)
	}
	return out, nil
}

func printFeishuBotSessionStatus(out interface{ Write([]byte) (int, error) }, status *pb.FeishuBotSessionStatusResponse) {
	st := styleFor(out)
	if status == nil {
		fmt.Fprintf(out, "%s\n", st.heading("Feishu bot-session:"))
		fmt.Fprintln(out, "  enabled:      no")
		return
	}
	profile := feishuProfileOrDefault(status.Profile)
	fmt.Fprintf(out, "%s\n", st.heading("Feishu bot-session:"))
	fmt.Fprintf(out, "  profile:      %s\n", profile)
	fmt.Fprintf(out, "  mode:         %s\n", status.InteractionMode)
	fmt.Fprintf(out, "  default_agent: %s\n", status.DefaultAgent)
	fmt.Fprintf(out, "  bridge_id:    %s\n", status.BridgeId)
	bs := status.BotSession
	enabled := "no"
	if bs != nil && bs.Enabled {
		enabled = "yes"
	}
	fmt.Fprintf(out, "  enabled:      %s\n", enabled)
	if bs != nil {
		targetName := strings.TrimSpace(bs.TargetName)
		if targetName == "" {
			targetName = strings.TrimSpace(bs.TargetId)
		}
		session := strings.TrimSpace(bs.SessionName)
		if targetName != "" && session != "" {
			fmt.Fprintf(out, "  session:      %s/%s\n", targetName, session)
		} else {
			fmt.Fprintln(out, "  session:      ")
		}
		fmt.Fprintf(out, "  agent:        %s\n", bs.AgentProfile)
		fmt.Fprintf(out, "  agent_type:   %s\n", bs.AgentType)
		fmt.Fprintf(out, "  cwd:          %s\n", bs.Cwd)
	} else {
		fmt.Fprintln(out, "  session:      ")
	}
	if status.Error != "" {
		fmt.Fprintf(out, "  error:        %s\n", st.fail(status.Error))
	}
}

func printFeishuBindings(out interface{ Write([]byte) (int, error) }, bindings []*pb.FeishuBinding) error {
	if len(bindings) == 0 {
		fmt.Fprintln(out, "No Feishu topic bindings")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ROOT\tTHREAD\tTARGET\tSESSION\tAGENT\tSTATUS")
	for _, b := range bindings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			b.RootMsgId,
			b.ThreadId,
			b.TargetName,
			b.SessionName,
			b.AgentType,
			b.Status,
		)
	}
	return tw.Flush()
}

func printFeishuBots(out interface{ Write([]byte) (int, error) }, bots []*pb.FeishuBot) {
	if len(bots) == 0 {
		fmt.Fprintln(out, "No Feishu bot profiles")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROFILE\tBRIDGE_ID\tAPP_ID\tOWNER\tBOT\tBINDINGS\tSTATUS")
	for _, b := range bots {
		status := "configured"
		if !b.Configured {
			status = "incomplete"
		}
		if b.Error != "" {
			status = b.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			b.Profile,
			b.BridgeId,
			b.AppId,
			b.OpenId,
			b.BotName,
			b.BindingCount,
			status,
		)
	}
	_ = tw.Flush()
}

func printFeishuContextExplain(out interface{ Write([]byte) (int, error) }, resp *pb.FeishuContextExplainResponse, verbose bool) {
	if resp == nil {
		fmt.Fprintln(out, "No Feishu context decision")
		return
	}
	fmt.Fprintf(out, "Feishu context decision\n")
	fmt.Fprintf(out, "  profile:   %s\n", feishuProfileOrDefault(resp.Profile))
	fmt.Fprintf(out, "  bridge_id: %s\n", resp.BridgeId)
	if resp.Error != "" {
		fmt.Fprintf(out, "  error:     %s\n", resp.Error)
		return
	}
	if resp.Decision == nil {
		fmt.Fprintln(out, "  decision:  none")
		return
	}
	d := resp.Decision
	fmt.Fprintf(out, "  decision:  %d\n", d.Id)
	fmt.Fprintf(out, "  root:      %s\n", d.RootMsgId)
	if d.MessageId != "" {
		fmt.Fprintf(out, "  message:   %s\n", d.MessageId)
	}
	fmt.Fprintf(out, "  mode:      %s\n", d.WorkspaceMode)
	if d.RequestText != "" {
		fmt.Fprintf(out, "  request:   %s\n", d.RequestText)
	}
	fmt.Fprintf(out, "  context:   %d chars\n", len(d.ThreadContext))
	fmt.Fprintf(out, "  prompt:    %d chars\n", len(d.PromptPreview))
	if len(resp.SelectedItems) == 0 {
		fmt.Fprintln(out, "\nselected items: none")
	} else {
		fmt.Fprintln(out, "\nselected items:")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tTYPE\tSOURCE\tCHARS\tPREVIEW")
		for _, item := range resp.SelectedItems {
			itemType := item.ItemType
			if item.Truncated {
				itemType += "*"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n",
				item.Id,
				itemType,
				item.AuthorLabel,
				len(item.Text),
				feishuContextOneLine(item.Text, 96),
			)
		}
		_ = tw.Flush()
	}
	if !verbose {
		if d.ThreadContext != "" || d.PromptPreview != "" {
			fmt.Fprintln(out, "\nUse --verbose to show full thread context and prompt preview.")
		}
		return
	}
	if d.ThreadContext != "" {
		fmt.Fprintf(out, "\nThread context:\n%s\n", d.ThreadContext)
	}
	if d.PromptPreview != "" {
		fmt.Fprintf(out, "\nPrompt preview:\n%s\n", d.PromptPreview)
	}
}

func feishuContextDecisionToPB(d threadcontext.Decision) *pb.FeishuContextDecision {
	return &pb.FeishuContextDecision{
		Id:            d.ID,
		RootMsgId:     d.RootMsgID,
		MessageId:     d.MessageID,
		BridgeId:      d.BridgeId,
		BotProfile:    d.BotProfile,
		WorkspaceMode: d.WorkspaceMode,
		RequestText:   d.RequestText,
		ThreadContext: d.ThreadContext,
		PromptPreview: d.PromptPreview,
		CreatedAt:     d.CreatedAt,
	}
}

func feishuContextItemToPB(item threadcontext.ContextItem) *pb.FeishuContextItem {
	return &pb.FeishuContextItem{
		Id:          item.ID,
		ItemType:    item.ItemType,
		AuthorLabel: item.AuthorLabel,
		Text:        item.Text,
		BridgeId:    item.BridgeId,
		BotProfile:  item.BotProfile,
		TargetName:  item.TargetName,
		SessionName: item.SessionName,
		MessageId:   item.MessageID,
		CreatedAt:   item.CreatedAt,
		Truncated:   item.Truncated,
	}
}

func feishuContextOneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func feishuProfileOrDefault(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return bridge.DefaultProfileName
	}
	return profile
}
