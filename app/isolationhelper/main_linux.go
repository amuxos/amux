//go:build linux

package isolationhelper

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/amuxos/amux/internal/extensionstate"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/sessionfiles"
	"github.com/spf13/cobra"
)

var version = "dev"

func Main(buildVersion string) {
	extensionstate.Freeze()
	version = buildVersion
	if len(os.Args) == 5 && os.Args[1] == "_session-file" {
		if err := sessionfiles.Exchange(os.Args[2], os.Args[3], os.Args[4], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		return
	}
	if len(os.Args) == 5 && os.Args[1] == "_isolation-materialize" {
		if err := isolation.ApplyMaterializeBundle(os.Stdin, os.Args[2], os.Args[3], os.Args[4]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		return
	}

	if len(os.Args) >= 4 && os.Args[1] == "_read-home-files" {
		if err := isolation.ReadHomeFilesWorker(os.Args[2], os.Args[3:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "_probe_mount" {
		if err := isolation.ApplyPrincipalMountView(
			os.Getenv(isolation.MountViewRootEnv),
			os.Getenv(isolation.MountViewAccountEnv),
		); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "_probe_pid_namespace" {
		if err := isolation.ApplyPIDNamespaceProcView(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		os.Exit(0)
	}
	if len(os.Args) >= 3 && os.Args[1] == "_exec" {
		if os.Getenv(isolation.MountViewEnabledEnv) == "1" {
			if err := isolation.ApplyPrincipalMountView(
				os.Getenv(isolation.MountViewRootEnv),
				os.Getenv(isolation.MountViewAccountEnv),
			); err != nil {
				fmt.Fprintln(os.Stderr, "amux mount hardening degraded:", err)
			}
		}
		if os.Getenv(isolation.PIDNamespaceEnabledEnv) == "1" {
			if err := isolation.ApplyPIDNamespaceProcView(); err != nil {
				fmt.Fprintln(os.Stderr, "amux PID namespace hardening failed:", err)
				os.Exit(125)
			}
		}
		_ = os.Unsetenv(isolation.MountViewRootEnv)
		_ = os.Unsetenv(isolation.MountViewAccountEnv)
		_ = os.Unsetenv(isolation.MountViewEnabledEnv)
		_ = os.Unsetenv(isolation.PIDNamespaceEnabledEnv)
		seccompEnabled := os.Getenv(isolation.SeccompEnabledEnv) == "1"
		_ = os.Unsetenv(isolation.SeccompEnabledEnv)
		if seccompEnabled {
			if err := isolation.ApplySeccompDenylist(); err != nil {
				// Seccomp is defense in depth. UID/DAC remains the security
				// invariant, so a kernel-specific filter failure is degraded rather
				// than turning the whole launch into an availability failure.
				fmt.Fprintln(os.Stderr, "amux hardening degraded:", err)
			}
		}
		if err := syscall.Exec(os.Args[2], os.Args[2:], os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, "amux-isolation-helper:", isolation.DescribeExecFailure(os.Args[2], err))
			os.Exit(126)
		}
	}
	root := &cobra.Command{
		Use:           "amux-isolation-helper",
		Short:         "Privileged Linux UID/DAC process helper for amux",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.Version = version
	root.SetVersionTemplate("amux-isolation-helper {{.Version}}\n")
	serve := &cobra.Command{Use: "serve", Short: "Serve the restricted helper protocol", RunE: runServe}
	serve.Flags().String("socket", "", "absolute unixpacket socket path")
	serve.Flags().String("state-dir", "", "absolute root-owned helper state directory")
	serve.Flags().String("principal-root", "/var/lib/amux/principals", "absolute principal data root")
	serve.Flags().Int("service-uid", -1, "only uid allowed to connect")
	serve.Flags().Int("service-gid", -1, "trusted service gid used for control-plane traversal")
	serve.Flags().Int("uid-range-start", 700000, "first dedicated principal uid")
	serve.Flags().Int("uid-range-end", 799999, "last dedicated principal uid")
	serve.Flags().Bool("pid-namespace", true, "enable best-effort user/PID namespace hardening")
	serve.Flags().Bool("mount-namespace", true, "enable best-effort user/mount namespace hardening")
	serve.Flags().Bool("seccomp", true, "enable best-effort provider seccomp hardening")
	serve.Flags().String("materializer-path", "", "absolute path to the fixed target-uid materializer binary")
	root.AddCommand(serve)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	socketPath, _ := cmd.Flags().GetString("socket")
	stateDir, _ := cmd.Flags().GetString("state-dir")
	principalRoot, _ := cmd.Flags().GetString("principal-root")
	serviceUID, _ := cmd.Flags().GetInt("service-uid")
	serviceGID, _ := cmd.Flags().GetInt("service-gid")
	uidStart, _ := cmd.Flags().GetInt("uid-range-start")
	uidEnd, _ := cmd.Flags().GetInt("uid-range-end")
	pidNamespace, _ := cmd.Flags().GetBool("pid-namespace")
	mountNamespace, _ := cmd.Flags().GetBool("mount-namespace")
	seccomp, _ := cmd.Flags().GetBool("seccomp")
	materializerPath, _ := cmd.Flags().GetString("materializer-path")
	if socketPath == "" || stateDir == "" || serviceUID < 0 || serviceGID < 0 {
		return fmt.Errorf("--socket, --state-dir, --service-uid and --service-gid are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return isolation.ServeHelper(ctx, isolation.HelperConfig{
		SocketPath:       socketPath,
		StateDir:         stateDir,
		PrincipalRoot:    principalRoot,
		ServiceUID:       serviceUID,
		ServiceGID:       serviceGID,
		UIDRangeStart:    uidStart,
		UIDRangeEnd:      uidEnd,
		PIDNamespace:     pidNamespace,
		MountNamespace:   mountNamespace,
		Seccomp:          seccomp,
		ExecWrapperPath:  executable,
		MaterializerPath: materializerPath,
	})
}
