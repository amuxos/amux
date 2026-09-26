package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/amuxos/amux/app/setup"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:     "Setup",
		Short:   "Amux Windows Setup (binaries + Feishu bind + Windows Service)",
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := setup.Options{OpenBrowser: true}
			opts.SourceDir, _ = cmd.Flags().GetString("source")
			opts.InstallBinDir, _ = cmd.Flags().GetString("bin-dir")
			opts.AmuxHome, _ = cmd.Flags().GetString("amux-home")
			opts.Runtime, _ = cmd.Flags().GetString("runtime")
			opts.AgentProfile, _ = cmd.Flags().GetString("agent-profile")
			opts.Model, _ = cmd.Flags().GetString("model")
			opts.FeishuProfile, _ = cmd.Flags().GetString("feishu-profile")
			opts.InteractionMode, _ = cmd.Flags().GetString("interaction-mode")
			opts.SessionName, _ = cmd.Flags().GetString("session-name")
			opts.SessionCWD, _ = cmd.Flags().GetString("session-cwd")
			noBrowser, _ := cmd.Flags().GetBool("no-browser")
			opts.OpenBrowser = !noBrowser

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			return setup.Run(ctx, opts, cmd.OutOrStdout())
		},
	}
	root.SetVersionTemplate("amux Setup {{.Version}}\n")
	root.Flags().String("source", "", "directory containing amux.exe, amuxd.exe, amuxlet.exe (default: next to Setup.exe)")
	root.Flags().String("bin-dir", "", "install directory for binaries (default: %USERPROFILE%\\.amux\\bin)")
	root.Flags().String("amux-home", "", "amux data directory (default: %USERPROFILE%\\.amux)")
	root.Flags().String("runtime", "", "codex|claude|pi (required)")
	root.Flags().String("agent-profile", "", "builtin/<runtime> (default: automatic)")
	root.Flags().String("model", "", "native model override")
	root.Flags().String("feishu-profile", "default", "Feishu bot profile name")
	root.Flags().String("interaction-mode", "singleton", "singleton (default) or thread")
	root.Flags().String("session-name", "", "singleton session name (default: <profile>-main)")
	root.Flags().String("session-cwd", "", "singleton workspace directory (default: current directory)")
	root.Flags().Bool("no-browser", false, "do not open the Feishu authorization URL in a browser")

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
