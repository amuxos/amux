package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/winsvc"
)

// Run executes the phase-1 Windows Setup flow. Feishu provisioning is required;
// scan/timeout/network failure aborts with a non-zero error and does not claim
// success. Partial binary copies may remain; the Windows Service is rolled
// back when service install happens after a later failure.
func Run(ctx context.Context, opts Options, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	if err := opts.Normalize(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return fmt.Errorf("Setup.exe targets Windows; current OS is %s", runtime.GOOS)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sourceDir, err := resolveSourceDir(opts.SourceDir)
	if err != nil {
		return err
	}
	amuxHome := opts.AmuxHome
	if amuxHome == "" {
		amuxHome = amuxhome.Dir()
	}
	amuxHome, err = filepath.Abs(amuxHome)
	if err != nil {
		return err
	}
	_ = os.Setenv("AMUX_HOME", amuxHome)
	binDir := opts.InstallBinDir
	if binDir == "" {
		binDir = filepath.Join(amuxHome, "bin")
	}
	binDir, err = filepath.Abs(binDir)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "=== amux Windows Setup (phase 1) ===")
	fmt.Fprintf(out, "runtime: %s\nprofile: %s\ninteraction: %s\n", opts.Runtime, opts.FeishuProfile, opts.InteractionMode)
	fmt.Fprintf(out, "install: %s\namux_home: %s\n\n", binDir, amuxHome)

	fmt.Fprintln(out, "==> Installing binaries")
	if err := installBinaries(sourceDir, binDir); err != nil {
		return err
	}
	amuxExe := filepath.Join(binDir, exeName("amux"))
	if err := runAmux(amuxExe, amuxHome, out, "install", "--source", sourceDir, "--force"); err != nil {
		return fmt.Errorf("amux install: %w", err)
	}

	fmt.Fprintln(out, "\n==> Feishu bind (QR / browser authorization)")
	fmt.Fprintln(out, "Phase 1 requires a successful Feishu device-code bind. There is no App ID/Secret manual fallback.")
	if err := provisionAndSaveFeishu(ctx, opts, out); err != nil {
		return fmt.Errorf("Feishu bind failed — setup aborted: %w", err)
	}

	fmt.Fprintln(out, "\n==> Writing local defaults")
	if err := runAmux(amuxExe, amuxHome, out, "setup", "local", "--no-start"); err != nil {
		fmt.Fprintf(out, "warning: setup local: %v\n", err)
	}
	if err := runAmux(amuxExe, amuxHome, out, "system", "config", "set", "bridge.feishu.workspace_defaults.agent", opts.AgentProfile); err != nil {
		return fmt.Errorf("set workspace default agent: %w", err)
	}
	if opts.InteractionMode == "singleton" || opts.InteractionMode == "thread" {
		key := fmt.Sprintf("bridge.feishu.profiles.%s.interaction.mode", opts.FeishuProfile)
		if err := config.UpdateConfigFile(amuxhome.ConfigPath(), func(raw map[string]any) error {
			return setNested(raw, strings.Split(key, "."), opts.InteractionMode)
		}); err != nil {
			return fmt.Errorf("set interaction mode: %w", err)
		}
	}

	fmt.Fprintln(out, "\n==> Registering Windows Service (Automatic / LocalSystem + user AMUX_HOME)")
	if err := winsvc.GrantSystemAccess(amuxHome); err != nil {
		return err
	}
	cfg := winsvc.Config{
		BinDir:      binDir,
		AmuxHome:    amuxHome,
		UserProfile: strings.TrimSpace(os.Getenv("USERPROFILE")),
	}
	if home, err := os.UserHomeDir(); err == nil && cfg.UserProfile == "" {
		cfg.UserProfile = home
	}
	if err := winsvc.Install(amuxExe, cfg); err != nil {
		// Allow reruns: uninstall then reinstall.
		_ = winsvc.Uninstall()
		if err := winsvc.Install(amuxExe, cfg); err != nil {
			return fmt.Errorf("install Windows Service: %w", err)
		}
	}
	if err := winsvc.Start(); err != nil {
		_ = winsvc.Uninstall()
		return fmt.Errorf("start Windows Service: %w", err)
	}
	fmt.Fprintf(out, "Windows Service %q installed and started\n", winsvc.ServiceName)

	if opts.InteractionMode == "singleton" {
		fmt.Fprintln(out, "\n==> Binding Feishu singleton bot-session")
		if err := bindSingleton(ctx, amuxExe, amuxHome, opts, out); err != nil {
			return fmt.Errorf("bot-session bind failed: %w", err)
		}
	}

	fmt.Fprintln(out, "\n✅ Setup complete.")
	fmt.Fprintln(out, "Next: open Feishu, message the bot, and confirm the owner if prompted.")
	fmt.Fprintf(out, "Logs: %s\\hub.log and %s\\runtime.log\n", amuxHome, amuxHome)
	return nil
}

func resolveSourceDir(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	for _, name := range []string{"amux", "amuxd", "amuxlet"} {
		if _, err := os.Stat(filepath.Join(dir, exeName(name))); err != nil {
			return "", fmt.Errorf("missing %s next to Setup.exe (looked in %s); pass --source", exeName(name), dir)
		}
	}
	return dir, nil
}

func installBinaries(sourceDir, binDir string) error {
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}
	for _, name := range []string{"amux", "amuxd", "amuxlet"} {
		src := filepath.Join(sourceDir, exeName(name))
		dst := filepath.Join(binDir, exeName(name))
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0755); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}
	return nil
}

func provisionAndSaveFeishu(ctx context.Context, opts Options, out io.Writer) error {
	existing, _ := bridge.LoadProfileSecrets(opts.FeishuProfile)
	if existing == nil {
		existing = &bridge.Secrets{}
	}
	openBrowser := opts.OpenBrowser
	provCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	res, err := bridge.ProvisionFeishuApp(provCtx, bridge.ProvisionOptions{
		QRWriter: out,
		OnStatus: func(msg string) { fmt.Fprintln(out, msg) },
		OnAuthorizationURL: func(url string, expireIn int) {
			fmt.Fprintf(out, "Authorization URL (expire ~%ds):\n  %s\n", expireIn, url)
			if openBrowser {
				if err := OpenURL(url); err != nil {
					fmt.Fprintf(out, "Could not open browser automatically: %v\nOpen the URL above manually.\n", err)
				} else {
					fmt.Fprintln(out, "Opened the authorization URL in your default browser.")
				}
			}
		},
		AppID: existing.AppId,
	})
	if err != nil {
		return err
	}
	if res == nil || strings.TrimSpace(res.AppID) == "" || strings.TrimSpace(res.AppSecret) == "" {
		return fmt.Errorf("device-code flow returned empty credentials")
	}
	s := bridge.CarrySecretsForApp(existing, res.AppID)
	s.AppSecret = res.AppSecret
	if s.OpenId == "" && res.OwnerOpenID != "" {
		s.OpenId = res.OwnerOpenID
	}
	lookupCtx, lookupCancel := context.WithTimeout(ctx, 15*time.Second)
	identity, idErr := bridge.FetchBotIdentityWithCredentials(lookupCtx, s.AppId, s.AppSecret)
	lookupCancel()
	if idErr == nil {
		if s.BotName == "" {
			s.BotName = identity.Name
		}
		if s.BotOpenId == "" {
			s.BotOpenId = identity.OpenId
		}
	}
	if err := bridge.SaveProfileSecrets(opts.FeishuProfile, s); err != nil {
		return err
	}
	fmt.Fprintf(out, "✅ Feishu profile %q saved (App ID=%s)\n", opts.FeishuProfile, s.AppId)
	return nil
}

func bindSingleton(ctx context.Context, amuxExe, amuxHome string, opts Options, out io.Writer) error {
	cwd := opts.SessionCWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	args := []string{"session", "run", opts.AgentProfile, "-n", opts.SessionName, "--on", "local", "--runtime", opts.Runtime, "--cwd", cwd}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	// Best-effort session create; bind may attach to an existing one.
	_ = runAmux(amuxExe, amuxHome, out, args...)
	target := "local/" + opts.SessionName
	return runAmux(amuxExe, amuxHome, out, "bridge", "feishu", "bot-session", "bind", "--profile", opts.FeishuProfile, target)
}

func runAmux(amuxExe, amuxHome string, out io.Writer, args ...string) error {
	cmd := exec.Command(amuxExe, args...)
	cmd.Env = append(os.Environ(), "AMUX_HOME="+amuxHome)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func exeName(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

func setNested(root map[string]any, path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("empty config path")
	}
	cur := root
	for i, key := range path[:len(path)-1] {
		next, ok := cur[key]
		if !ok || next == nil {
			child := map[string]any{}
			cur[key] = child
			cur = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("config path %s is not a table", strings.Join(path[:i+1], "."))
		}
		cur = child
	}
	cur[path[len(path)-1]] = value
	return nil
}
