package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/winsvc"
	"github.com/spf13/cobra"
)

func newSystemServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "service",
		Short:             "Manage the Amux Windows Service (hub + runtime)",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newSystemServiceInstallCmd(),
		newSystemServiceUninstallCmd(),
		newSystemServiceStartCmd(),
		newSystemServiceStopCmd(),
		newSystemServiceStatusCmd(),
		newSystemServiceRunCmd(),
	)
	return cmd
}

func newSystemServiceInstallCmd() *cobra.Command {
	var binDir string
	var amuxHome string
	c := &cobra.Command{
		Use:               "install",
		Short:             "Install the Amux Windows Service (Automatic start)",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("Windows Service install is only available on Windows")
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if strings.TrimSpace(binDir) == "" {
				binDir = filepath.Dir(exe)
			}
			cfg, err := buildWindowsServiceConfig(binDir, amuxHome)
			if err != nil {
				return err
			}
			if err := winsvc.GrantSystemAccess(cfg.AmuxHome); err != nil {
				return err
			}
			amuxExe := filepath.Join(cfg.BinDir, exeName("amux"))
			if _, err := os.Stat(amuxExe); err != nil {
				return fmt.Errorf("amux binary not found at %s: %w", amuxExe, err)
			}
			if err := winsvc.Install(amuxExe, *cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Installed Windows Service %q\n", winsvc.ServiceName)
			fmt.Fprintf(cmd.OutOrStdout(), "  bin_dir:   %s\n", cfg.BinDir)
			fmt.Fprintf(cmd.OutOrStdout(), "  amux_home: %s\n", cfg.AmuxHome)
			fmt.Fprintf(cmd.OutOrStdout(), "  account:   LocalSystem (AMUX_HOME points at the installing user profile)\n")
			fmt.Fprintf(cmd.OutOrStdout(), "Start with: amux system service start\n")
			return nil
		},
	}
	c.Flags().StringVar(&binDir, "bin-dir", "", "directory containing amux.exe / amuxd.exe / amuxlet.exe (default: directory of this amux)")
	c.Flags().StringVar(&amuxHome, "amux-home", "", "amux data directory (default: current AMUX_HOME or %USERPROFILE%\\.amux)")
	return c
}

func newSystemServiceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "uninstall",
		Short:             "Stop and remove the Amux Windows Service",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("Windows Service uninstall is only available on Windows")
			}
			if err := winsvc.Uninstall(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed Windows Service %q\n", winsvc.ServiceName)
			return nil
		},
	}
}

func newSystemServiceStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "start",
		Short:             "Start the Amux Windows Service",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := winsvc.Start(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Started Windows Service %q\n", winsvc.ServiceName)
			return nil
		},
	}
}

func newSystemServiceStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop",
		Short:             "Stop the Amux Windows Service",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := winsvc.Stop(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stopped Windows Service %q\n", winsvc.ServiceName)
			return nil
		},
	}
}

func newSystemServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "status",
		Short:             "Show Amux Windows Service status",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := winsvc.Status()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "service: %s\nstatus: %s\n", winsvc.ServiceName, status)
			if cfg, err := winsvc.LoadConfig(); err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "bin_dir: %s\namux_home: %s\n", cfg.BinDir, cfg.AmuxHome)
			}
			return nil
		},
	}
}

func newSystemServiceRunCmd() *cobra.Command {
	c := &cobra.Command{
		Use:               "run",
		Short:             "Run as the Windows Service process (SCM entrypoint)",
		Hidden:            true,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return winsvc.RunService(&windowsServiceHost{})
		},
	}
	return c
}

func buildWindowsServiceConfig(binDir, amuxHome string) (*winsvc.Config, error) {
	binDir = strings.TrimSpace(binDir)
	if binDir == "" {
		return nil, fmt.Errorf("bin-dir is required")
	}
	absBin, err := filepath.Abs(binDir)
	if err != nil {
		return nil, err
	}
	amuxHome = strings.TrimSpace(amuxHome)
	if amuxHome == "" {
		amuxHome = amuxhome.Dir()
	}
	absHome, err := filepath.Abs(amuxHome)
	if err != nil {
		return nil, err
	}
	profile := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if profile == "" {
		if home, err := os.UserHomeDir(); err == nil {
			profile = home
		}
	}
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	return &winsvc.Config{
		BinDir:      absBin,
		AmuxHome:    absHome,
		UserProfile: profile,
		Username:    username,
	}, nil
}

type windowsServiceHost struct{}

func (h *windowsServiceHost) Start() error {
	cfg, err := winsvc.LoadConfig()
	if err != nil {
		return err
	}
	applyWindowsServiceEnv(cfg)
	// Prefer binaries from the installed bin dir.
	if err := os.Setenv("PATH", cfg.BinDir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return err
	}
	if err := startIfNotRunning(hubService()); err != nil {
		return fmt.Errorf("start hub: %w", err)
	}
	if err := startIfNotRunning(runtimeService()); err != nil {
		_ = hubService().Stop()
		return fmt.Errorf("start runtime: %w", err)
	}
	return nil
}

func (h *windowsServiceHost) Stop() error {
	cfg, err := winsvc.LoadConfig()
	if err == nil {
		applyWindowsServiceEnv(cfg)
	}
	var first error
	if err := runtimeService().Stop(); err != nil && first == nil {
		first = err
	}
	if err := hubService().Stop(); err != nil && first == nil {
		first = err
	}
	return first
}

func applyWindowsServiceEnv(cfg *winsvc.Config) {
	if cfg == nil {
		return
	}
	_ = os.Setenv("AMUX_HOME", cfg.AmuxHome)
	if cfg.UserProfile != "" {
		_ = os.Setenv("USERPROFILE", cfg.UserProfile)
		_ = os.Setenv("HOME", cfg.UserProfile)
		_ = os.Setenv("HOMEPATH", strings.TrimPrefix(cfg.UserProfile, filepath.VolumeName(cfg.UserProfile)))
		appData := filepath.Join(cfg.UserProfile, "AppData", "Roaming")
		localAppData := filepath.Join(cfg.UserProfile, "AppData", "Local")
		_ = os.Setenv("APPDATA", appData)
		_ = os.Setenv("LOCALAPPDATA", localAppData)
	}
}
