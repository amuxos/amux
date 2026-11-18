package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

const isolationHelperDir = "/usr/local/libexec/amux"

func newIsolationCmd() *cobra.Command {
	return newIsolationCmdForPlatform(runtime.GOOS)
}

func newIsolationCmdForPlatform(goos string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "isolation",
		Short: "Manage identified principals and Linux UID/DAC isolation",
	}
	cmd.AddCommand(
		newIsolationStatusCmd(),
		newIsolationGlobalEnvCmd(),
		newIsolationPrincipalCmd(),
		newIsolationAliasCmd(),
		newIsolationDelegationCmd(),
		newIsolationServiceMemberCmd(),
		newIsolationEnrollCmd(),
		newIsolationAccountCmd(),
	)
	if goos == "linux" || goos == allCLIPlatforms {
		helper := newIsolationHelperCmd()
		markCommandPlatforms(helper, "linux")
		cmd.AddCommand(helper)
	}
	return cmd
}

func loadIsolationRuntime() (*isolation.Runtime, config.IsolationConfig, error) {
	_ = loadUserConfig()
	cfg, err := config.GetIsolation()
	if err != nil {
		return nil, config.IsolationConfig{}, err
	}
	rt, err := isolation.NewRuntime(cfg)
	return rt, cfg, err
}

func newIsolationStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show isolation configuration, helper readiness, and fallback state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, _, err := loadIsolationRuntime()
			if err != nil {
				return err
			}
			defer rt.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			status := rt.Status(ctx)
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "platform: %s\n", status.Platform)
			fmt.Fprintf(cmd.OutOrStdout(), "enabled: %v\n", status.Enabled)
			fmt.Fprintf(cmd.OutOrStdout(), "configured backend: %s\n", status.Configured)
			fmt.Fprintf(cmd.OutOrStdout(), "selected backend: %s\n", status.Selected)
			fmt.Fprintf(cmd.OutOrStdout(), "helper ready: %v\n", status.HelperReady)
			fmt.Fprintf(cmd.OutOrStdout(), "helper protocol: %d\n", status.ProtocolVersion)
			if status.HelperVersion != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "helper version: %s\n", status.HelperVersion)
			}
			if status.DegradedReason != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "degraded reason: %s\n", status.DegradedReason)
			}
			if status.HelperReady && len(status.Hardening) == 0 {
				// An empty map means "not measured yet", not "every layer off".
				fmt.Fprintln(cmd.OutOrStdout(), "hardening: not probed yet (measured on the first identified launch)")
			}
			for _, name := range slices.Sorted(maps.Keys(status.Hardening)) {
				fmt.Fprintf(cmd.OutOrStdout(), "hardening %s: %v\n", name, status.Hardening[name])
			}
			for _, reason := range status.HardeningDegraded {
				fmt.Fprintf(cmd.OutOrStdout(), "hardening degraded: %s\n", reason)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func newIsolationPrincipalCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "principal", Short: "Manage canonical principals"}
	cmd.AddCommand(newIsolationPrincipalCreateCmd(), newIsolationPrincipalListCmd(), newIsolationPrincipalDisableCmd())
	return cmd
}

func newIsolationPrincipalCreateCmd() *cobra.Command {
	var kind, target string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "create <display-name>",
		Short: "Create a human or service principal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeFn, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.CreatePrincipal(cmd.Context(), &pb.CreatePrincipalRequest{
				Kind: kind, DisplayName: args[0], EnvironmentTargetId: target,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), resp.Principal)
			}
			printPrincipal(cmd, resp.Principal)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "human", "principal kind: human or service")
	cmd.Flags().StringVar(&target, "target", "", "optional fixed environment target id")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func newIsolationPrincipalListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List principals",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, closeFn, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.ListPrincipals(cmd.Context(), &pb.ListPrincipalsRequest{})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), resp.Principals)
			}
			if len(resp.Principals) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no principals")
				return nil
			}
			for _, principal := range resp.Principals {
				printPrincipal(cmd, principal)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func newIsolationPrincipalDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <principal-id>",
		Short: "Disable a principal for new execution and authorization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeFn, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.DisablePrincipal(cmd.Context(), &pb.DisablePrincipalRequest{PrincipalId: args[0]})
			if err != nil {
				return err
			}
			printPrincipal(cmd, resp.Principal)
			return nil
		},
	}
}

func printPrincipal(cmd *cobra.Command, principal *pb.Principal) {
	if principal == nil {
		return
	}
	target := principal.EnvironmentTargetId
	if target == "" {
		target = "any"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\ttarget=%s\n",
		principal.PrincipalId, principal.Kind, principal.Status, principal.DisplayName, target)
}

func newIsolationAliasCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "alias", Short: "Manage scoped principal aliases"}
	var provider, scope, subject, principalID string
	bind := &cobra.Command{
		Use:   "bind",
		Short: "Bind provider/scope/subject to a principal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, closeFn, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeFn()
			_, err = client.BindPrincipalAlias(cmd.Context(), &pb.BindPrincipalAliasRequest{
				Provider: provider, Scope: scope, Subject: subject, PrincipalId: principalID,
			})
			if err == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "alias bound")
			}
			return err
		},
	}
	bind.Flags().StringVar(&provider, "provider", "cli", "alias provider")
	bind.Flags().StringVar(&scope, "scope", "local", "provider-local scope")
	bind.Flags().StringVar(&subject, "subject", "", "provider subject")
	bind.Flags().StringVar(&principalID, "principal", "", "principal UUID")
	_ = bind.MarkFlagRequired("subject")
	_ = bind.MarkFlagRequired("principal")
	list := &cobra.Command{
		Use:   "list",
		Short: "List scoped principal aliases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, closeFn, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.ListPrincipalAliases(cmd.Context(), &pb.ListPrincipalAliasesRequest{})
			if err != nil {
				return err
			}
			for _, alias := range resp.Aliases {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n",
					alias.Provider, alias.Scope, alias.Subject, alias.PrincipalId)
			}
			return nil
		},
	}
	cmd.AddCommand(bind, list)
	return cmd
}

func newIsolationDelegationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "delegation", Short: "Manage run_as delegations"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "grant <actor-principal-id> <effective-principal-id>",
			Short: "Grant an actor permission to run as another principal",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				client, closeFn, err := newAmuxdClient()
				if err != nil {
					return err
				}
				defer closeFn()
				resp, err := client.GrantPrincipalDelegation(cmd.Context(), &pb.GrantPrincipalDelegationRequest{
					ActorPrincipalId: args[0], EffectivePrincipalId: args[1],
				})
				if err == nil {
					fmt.Fprintln(cmd.OutOrStdout(), resp.DelegationId)
				}
				return err
			},
		},
		&cobra.Command{
			Use:   "revoke <delegation-id>",
			Short: "Revoke a run_as delegation",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				client, closeFn, err := newAmuxdClient()
				if err != nil {
					return err
				}
				defer closeFn()
				_, err = client.RevokePrincipalDelegation(cmd.Context(), &pb.RevokePrincipalDelegationRequest{DelegationId: args[0]})
				if err == nil {
					fmt.Fprintln(cmd.OutOrStdout(), "delegation revoked")
				}
				return err
			},
		},
	)
	return cmd
}

func newIsolationServiceMemberCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "service-member", Short: "Manage service-principal membership"}
	var serviceID, memberID string
	grant := &cobra.Command{
		Use:   "grant",
		Short: "Allow a human principal to run as a service principal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, closeFn, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeFn()
			_, err = client.GrantServicePrincipalMember(cmd.Context(), &pb.ServicePrincipalMemberRequest{
				ServicePrincipalId: serviceID, MemberPrincipalId: memberID,
			})
			if err == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "service principal member granted")
			}
			return err
		},
	}
	revoke := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke service-principal membership and active service launches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, closeFn, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeFn()
			_, err = client.RevokeServicePrincipalMember(cmd.Context(), &pb.ServicePrincipalMemberRequest{
				ServicePrincipalId: serviceID, MemberPrincipalId: memberID,
			})
			if err == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "service principal member revoked")
			}
			return err
		},
	}
	for _, subcommand := range []*cobra.Command{grant, revoke} {
		subcommand.Flags().StringVar(&serviceID, "service", "", "service principal UUID")
		subcommand.Flags().StringVar(&memberID, "member", "", "member principal UUID")
		_ = subcommand.MarkFlagRequired("service")
		_ = subcommand.MarkFlagRequired("member")
	}
	cmd.AddCommand(grant, revoke)
	return cmd
}

func newIsolationEnrollCmd() *cobra.Command {
	var provider, scope, subject, principalID string
	var rotate bool
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Create and store a CLI actor credential",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tokenPath := filepath.Join(defaultAmuxDir(), "secrets", "principal-enrollment.token")
			if !rotate {
				if _, err := os.Stat(tokenPath); err == nil {
					return fmt.Errorf("enrollment already exists; pass --rotate to replace it")
				}
			}
			result, err := createAndStorePrincipalEnrollment(cmd.Context(), principalEnrollmentOptions{
				Provider: provider, Scope: scope, Subject: subject, PrincipalID: principalID, Rotate: rotate,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enrolled credential %s; token stored at %s\n", result.CredentialID, result.TokenPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "cli", "actor provider")
	cmd.Flags().StringVar(&scope, "scope", "local", "provider-local scope")
	cmd.Flags().StringVar(&subject, "subject", "", "provider subject (default current user)")
	cmd.Flags().StringVar(&principalID, "principal", "", "principal UUID")
	cmd.Flags().BoolVar(&rotate, "rotate", false, "replace an existing local enrollment credential")
	_ = cmd.MarkFlagRequired("principal")
	return cmd
}

type principalEnrollmentOptions struct {
	Provider    string
	Scope       string
	Subject     string
	PrincipalID string
	Rotate      bool
}

type principalEnrollmentResult struct {
	CredentialID string
	TokenPath    string
}

func createAndStorePrincipalEnrollment(ctx context.Context, opts principalEnrollmentOptions) (principalEnrollmentResult, error) {
	if opts.Subject == "" {
		if current, err := user.Current(); err == nil {
			opts.Subject = current.Username
		}
	}
	client, closeFn, err := newAmuxdClient()
	if err != nil {
		return principalEnrollmentResult{}, err
	}
	defer closeFn()
	resp, err := client.CreatePrincipalEnrollment(ctx, &pb.CreatePrincipalEnrollmentRequest{
		Provider: opts.Provider, Scope: opts.Scope, Subject: opts.Subject, PrincipalId: opts.PrincipalID, Rotate: opts.Rotate,
	})
	if err != nil {
		return principalEnrollmentResult{}, err
	}
	tokenPath := filepath.Join(defaultAmuxDir(), "secrets", "principal-enrollment.token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		return principalEnrollmentResult{}, err
	}
	if err := os.WriteFile(tokenPath, []byte(resp.Token+"\n"), 0o600); err != nil {
		return principalEnrollmentResult{}, err
	}
	return principalEnrollmentResult{CredentialID: resp.CredentialId, TokenPath: tokenPath}, nil
}

func newIsolationAccountCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Inspect target-local principal account mappings"}
	var jsonOut bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List mappings known to this runtime",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, cfg, err := loadIsolationRuntime()
			if err != nil {
				return err
			}
			defer rt.Close()
			store, err := isolation.OpenMappingStoreRange(cfg.MappingDB, cfg.UIDRangeStart, cfg.UIDRangeEnd)
			if err != nil {
				return err
			}
			defer store.Close()
			mappings, err := store.List()
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), mappings)
			}
			for _, mapping := range mappings {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tuid=%d\tlast-used=%s\n",
					mapping.PrincipalID, mapping.Account, mapping.UID, mapping.LastUsedAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	list.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	var execute bool
	var confirmPrincipal string
	var olderThanDays int
	gc := &cobra.Command{
		Use:   "gc",
		Short: "Preview or explicitly remove idle principal accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, cfg, err := loadIsolationRuntime()
			if err != nil {
				return err
			}
			defer rt.Close()
			store, err := isolation.OpenMappingStoreRange(cfg.MappingDB, cfg.UIDRangeStart, cfg.UIDRangeEnd)
			if err != nil {
				return err
			}
			defer store.Close()
			if olderThanDays < 0 {
				return fmt.Errorf("--older-than-days must be >= 0")
			}
			if olderThanDays == 0 {
				olderThanDays = cfg.GCIdleDays
			}
			if olderThanDays == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "automatic account GC is disabled (gc_idle_days=0); no accounts were removed")
				return nil
			}
			cutoff := time.Now().Add(-time.Duration(olderThanDays) * 24 * time.Hour)
			mappings, err := store.List()
			if err != nil {
				return err
			}
			var candidates []isolation.AccountMapping
			for _, mapping := range mappings {
				if !mapping.LastUsedAt.After(cutoff) {
					candidates = append(candidates, mapping)
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tuid=%d\tlast-used=%s\n",
						mapping.PrincipalID, mapping.Account, mapping.UID, mapping.LastUsedAt.Format(time.RFC3339))
				}
			}
			if !execute {
				fmt.Fprintf(cmd.OutOrStdout(), "dry-run: %d account(s) eligible; no data was removed\n", len(candidates))
				return nil
			}
			if confirmPrincipal == "" {
				return fmt.Errorf("--execute requires --confirm-principal with the exact principal UUID")
			}
			for _, mapping := range candidates {
				if mapping.PrincipalID != confirmPrincipal {
					continue
				}
				client := isolation.NewHelperClient(cfg.HelperSocket)
				if err := client.RemoveAccount(cmd.Context(), mapping); err != nil {
					return err
				}
				if err := store.Delete(mapping.PrincipalID); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "permanently removed account, HOME, and workspace for principal %s\n", mapping.PrincipalID)
				return nil
			}
			return fmt.Errorf("confirmed principal is not eligible for GC")
		},
	}
	gc.Flags().BoolVar(&execute, "execute", false, "perform irreversible account/HOME/workspace removal")
	gc.Flags().StringVar(&confirmPrincipal, "confirm-principal", "", "exact principal UUID to remove")
	gc.Flags().IntVar(&olderThanDays, "older-than-days", 0, "idle age threshold (default isolation.gc_idle_days)")
	cmd.AddCommand(list, gc)
	return cmd
}

func newIsolationHelperCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "helper", Short: "Install and manage the privileged Linux helper"}
	cmd.AddCommand(newIsolationHelperDrainProbeCmd())
	cmd.AddCommand(newIsolationHelperUpgradeCmd())
	cmd.AddCommand(newIsolationHelperStatusCmd(), newIsolationHelperInstallCmd(), newIsolationHelperUninstallCmd())
	return cmd
}

func newIsolationHelperStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check the local helper handshake",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, _, err := loadIsolationRuntime()
			if err != nil {
				return err
			}
			defer rt.Close()
			status := rt.Status(cmd.Context())
			fmt.Fprintf(cmd.OutOrStdout(), "ready=%v protocol=%d version=%s reason=%s\n",
				status.HelperReady, status.ProtocolVersion, status.HelperVersion, status.DegradedReason)
			return nil
		},
	}
}

func helperServiceDefaults() (int, int, string) {
	uid, gid := os.Getuid(), os.Getgid()
	if raw := os.Getenv("SUDO_UID"); raw != "" {
		uid, _ = strconv.Atoi(raw)
	}
	if raw := os.Getenv("SUDO_GID"); raw != "" {
		gid, _ = strconv.Atoi(raw)
	}
	return uid, gid, defaultAmuxDir()
}

func newIsolationHelperInstallCmd() *cobra.Command {
	defaultUID, defaultGID, defaultHome := helperServiceDefaults()
	opts := isolationHelperInstallOptions{
		ServiceUID:     defaultUID,
		ServiceGID:     defaultGID,
		AmuxHome:       defaultHome,
		UIDRangeStart:  700000,
		UIDRangeEnd:    799999,
		PrincipalRoot:  "/var/lib/amux/principals",
		PIDNamespace:   true,
		MountNamespace: true,
		Seccomp:        true,
	}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Explicitly install and start the root helper systemd unit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Socket == "" {
				opts.Socket = filepath.Join(opts.AmuxHome, "isolation-helper.sock")
			}
			if err := applyServiceHardeningDefaults(cmd, &opts); err != nil {
				return err
			}
			if os.Geteuid() != 0 {
				exe, _ := os.Executable()
				fmt.Fprintf(cmd.OutOrStdout(), "root is required; run:\nsudo %s isolation helper install --service-uid %d --service-gid %d --amux-home %s --socket %s\n",
					exe, opts.ServiceUID, opts.ServiceGID, opts.AmuxHome, opts.Socket)
				return fmt.Errorf("helper installation requires root")
			}
			err := installIsolationHelper(cmd.Context(), cmd.OutOrStdout(), opts)
			if errors.Is(err, helperUpgradeProblem("invalid_target")) {
				return errors.New(helperUpgradeText(cmd, "invalid_target"))
			}
			return err
		},
	}
	cmd.Flags().IntVar(&opts.ServiceUID, "service-uid", defaultUID, "amuxlet service uid allowed to connect")
	cmd.Flags().IntVar(&opts.ServiceGID, "service-gid", defaultGID, "amuxlet service gid")
	cmd.Flags().StringVar(&opts.AmuxHome, "amux-home", defaultHome, "service user's AMUX_HOME")
	cmd.Flags().StringVar(&opts.Socket, "socket", "", "absolute helper socket path (default <amux-home>/isolation-helper.sock)")
	cmd.Flags().IntVar(&opts.UIDRangeStart, "uid-range-start", 700000, "first dedicated principal uid")
	cmd.Flags().IntVar(&opts.UIDRangeEnd, "uid-range-end", 799999, "last dedicated principal uid")
	cmd.Flags().StringVar(&opts.PrincipalRoot, "principal-root", "/var/lib/amux/principals", "principal data root")
	cmd.Flags().BoolVar(&opts.PIDNamespace, "pid-namespace", true, "enable PID namespace hardening")
	cmd.Flags().BoolVar(&opts.MountNamespace, "mount-namespace", true, "enable mount namespace hardening")
	cmd.Flags().BoolVar(&opts.Seccomp, "seccomp", true, "enable provider seccomp hardening")
	markFlagDefaultRuntimeDependent(cmd, "service-uid", "service-gid", "amux-home")
	return cmd
}

type isolationHelperInstallOptions struct {
	ServiceUID     int
	ServiceGID     int
	AmuxHome       string
	Socket         string
	UIDRangeStart  int
	UIDRangeEnd    int
	PrincipalRoot  string
	PIDNamespace   bool
	MountNamespace bool
	Seccomp        bool
}

// applyServiceHardeningDefaults resolves the removable hardening layer from the
// service user's `[isolation.hardening]` section. The helper is installed as
// root against another user's AMUX_HOME, and it is the only process that can
// apply namespaces/seccomp, so install time is where that config takes effect.
// An explicit flag always wins over the file.
func applyServiceHardeningDefaults(cmd *cobra.Command, opts *isolationHelperInstallOptions) error {
	cfg, err := config.LoadIsolationFrom(filepath.Join(opts.AmuxHome, "config.toml"))
	if err != nil {
		return fmt.Errorf("read service isolation config: %w", err)
	}
	for _, item := range []struct {
		flag   string
		target *bool
		value  bool
	}{
		{"pid-namespace", &opts.PIDNamespace, cfg.Hardening.PIDNamespace},
		{"mount-namespace", &opts.MountNamespace, cfg.Hardening.MountNamespace},
		{"seccomp", &opts.Seccomp, cfg.Hardening.Seccomp},
	} {
		if !cmd.Flags().Changed(item.flag) {
			*item.target = item.value
		}
	}
	return nil
}

func installIsolationHelper(ctx context.Context, out interface{ Write([]byte) (int, error) }, opts isolationHelperInstallOptions) error {
	if opts.ServiceUID < 0 || opts.ServiceGID < 0 {
		return fmt.Errorf("service uid/gid must be non-negative")
	}
	if !filepath.IsAbs(opts.AmuxHome) || !filepath.IsAbs(opts.Socket) || !filepath.IsAbs(opts.PrincipalRoot) {
		return fmt.Errorf("amux home, socket, and principal root must be absolute")
	}
	if opts.UIDRangeStart < 65536 || opts.UIDRangeEnd < opts.UIDRangeStart || opts.UIDRangeEnd > 2147483000 {
		return fmt.Errorf("invalid dedicated uid range")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	source := filepath.Join(filepath.Dir(exe), "amux-isolation-helper")
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read helper binary next to amux: %w", err)
	}
	materializerSource := filepath.Join(filepath.Dir(exe), "amuxlet")
	materializerData, err := os.ReadFile(materializerSource)
	if err != nil {
		return fmt.Errorf("read amuxlet materializer binary next to amux: %w", err)
	}
	if _, err := os.Lstat(opts.Socket); err == nil {
		active, drainErr := drainHelper(ctx, opts.Socket, false)
		if drainErr != nil {
			return fmt.Errorf("drain existing helper before install: %w", drainErr)
		}
		if active != 0 {
			return fmt.Errorf("existing helper has %d active launch(es); retry after they finish", active)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect existing helper socket: %w", err)
	}
	target := filepath.Join(isolationHelperDir, "amux-isolation-helper")
	materializerTarget := filepath.Join(isolationHelperDir, "amuxlet-materializer")
	if err := prepareIsolationHelperDir("/", true); err != nil {
		return err
	}
	if err := installFileAtomically(target, data, 0o755); err != nil {
		return err
	}
	if err := installFileAtomically(materializerTarget, materializerData, 0o755); err != nil {
		return err
	}
	state := filepath.Join("/var/lib/amux/isolation", strconv.Itoa(opts.ServiceUID))
	execArgs := []string{
		target, "serve",
		"--socket", opts.Socket,
		"--state-dir", state,
		"--principal-root", opts.PrincipalRoot,
		"--service-uid", strconv.Itoa(opts.ServiceUID),
		"--service-gid", strconv.Itoa(opts.ServiceGID),
		"--uid-range-start", strconv.Itoa(opts.UIDRangeStart),
		"--uid-range-end", strconv.Itoa(opts.UIDRangeEnd),
		"--materializer-path", materializerTarget,
		"--pid-namespace=" + strconv.FormatBool(opts.PIDNamespace),
		"--mount-namespace=" + strconv.FormatBool(opts.MountNamespace),
		"--seccomp=" + strconv.FormatBool(opts.Seccomp),
	}
	for i, arg := range execArgs {
		quoted, err := quoteSystemdExecArg(arg)
		if err != nil {
			return err
		}
		execArgs[i] = quoted
	}
	unit := `[Unit]
Description=amux Linux UID/DAC isolation helper
After=local-fs.target

[Service]
Type=simple
ExecStart=` + strings.Join(execArgs, " ") + `
Restart=on-failure
KillMode=control-group
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
	`
	unitPath := "/etc/systemd/system/amux-isolation-helper.service"
	if err := installFileAtomically(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", "amux-isolation-helper.service"},
		{"restart", "amux-isolation-helper.service"},
	} {
		command := exec.CommandContext(ctx, "systemctl", args...)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	fmt.Fprintf(out, "installed %s and started amux-isolation-helper.service\n", target)
	return nil
}

func installFileAtomically(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func quoteSystemdExecArg(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\n\r") {
		return "", fmt.Errorf("systemd argument contains a control character")
	}
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`, nil
}

func newIsolationHelperUninstallCmd() *cobra.Command {
	var purge bool
	var force bool
	var amuxHome string
	var socket string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the helper; preserve account data by default",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("helper uninstall requires root")
			}
			if socket == "" {
				socket = filepath.Join(amuxHome, "isolation-helper.sock")
			}
			if !filepath.IsAbs(socket) {
				return fmt.Errorf("helper socket must be absolute")
			}
			active, err := drainHelper(cmd.Context(), socket, force)
			if err != nil {
				return fmt.Errorf("drain helper before uninstall: %w", err)
			}
			if active != 0 {
				return fmt.Errorf("helper has %d active launch(es); stop them or pass --force", active)
			}
			_ = exec.CommandContext(cmd.Context(), "systemctl", "disable", "--now", "amux-isolation-helper.service").Run()
			_ = os.Remove("/etc/systemd/system/amux-isolation-helper.service")
			_ = os.Remove(filepath.Join(isolationHelperDir, "amux-isolation-helper"))
			_ = os.Remove(filepath.Join(isolationHelperDir, "amuxlet-materializer"))
			_ = exec.CommandContext(cmd.Context(), "systemctl", "daemon-reload").Run()
			if purge {
				return fmt.Errorf("--purge is intentionally unsupported; remove retained mappings/accounts only after manual review")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "helper uninstalled; principal mappings, accounts, HOME, and workspaces were preserved")
			return nil
		},
	}
	_, _, amuxHome = helperServiceDefaults()
	cmd.Flags().BoolVar(&purge, "purge", false, "request destructive data purge (requires separate manual review)")
	cmd.Flags().BoolVar(&force, "force", false, "kill active isolated launches before uninstall")
	cmd.Flags().StringVar(&amuxHome, "amux-home", amuxHome, "service user's AMUX_HOME")
	cmd.Flags().StringVar(&socket, "socket", "", "absolute helper socket path (default <amux-home>/isolation-helper.sock)")
	markFlagDefaultRuntimeDependent(cmd, "amux-home")
	return cmd
}

// drainProbe is the seam between deciding *who* may ask the helper to drain and
// actually asking it.
type drainProbe struct {
	direct  func(ctx context.Context) (int, error)
	dropped func(ctx context.Context, uid, gid int) (int, error)
}

// drainHelperForControl reports the running helper's active launch count. The
// helper chowns its control socket to the service user and rejects every other
// peer — root included — so a root caller (which is every install/uninstall)
// must ask from the socket owner's credentials rather than its own.
func drainHelperForControl(ctx context.Context, socket string, callerUID int, probe drainProbe) (int, error) {
	ownerUID, ownerGID, err := helperSocketOwner(socket)
	if err != nil {
		return 0, err
	}
	if callerUID == ownerUID {
		return probe.direct(ctx)
	}
	return probe.dropped(ctx, ownerUID, ownerGID)
}

// drainHelper is the production wiring of drainHelperForControl: the dropped
// probe re-executes this binary as the socket owner, since credentials can only
// be dropped for a whole process.
func drainHelper(ctx context.Context, socket string, force bool) (int, error) {
	return probeHelper(ctx, socket, force, nil)
}

func probeHelper(ctx context.Context, socket string, force bool, lease *os.File) (int, error) {
	return drainHelperForControl(ctx, socket, os.Geteuid(), drainProbe{
		direct: func(ctx context.Context) (int, error) {
			if lease != nil {
				return isolation.NewHelperClient(socket).AcquireUpgradeGuard(ctx, lease)
			}
			return isolation.NewHelperClient(socket).DrainStatus(ctx, force)
		},
		dropped: func(ctx context.Context, uid, gid int) (int, error) {
			self, err := os.Executable()
			if err != nil {
				return 0, err
			}
			args := []string{"isolation", "helper", "_drain-probe", "--socket", socket}
			if force {
				args = append(args, "--force")
			}
			if lease != nil {
				args = append(args, "--upgrade-guard")
			}
			cmd := exec.CommandContext(ctx, self, args...)
			if lease != nil {
				cmd.Stdin = lease
			}
			setProcessCredential(cmd, uid, gid)
			output, err := cmd.Output()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
					return 0, fmt.Errorf("%s", strings.TrimSpace(string(exitErr.Stderr)))
				}
				return 0, err
			}
			active, convErr := strconv.Atoi(strings.TrimSpace(string(output)))
			if convErr != nil {
				return 0, fmt.Errorf("unexpected drain probe output %q", strings.TrimSpace(string(output)))
			}
			return active, nil
		},
	})
}

func newIsolationHelperDrainProbeCmd() *cobra.Command {
	var upgradeGuard bool
	var socket string
	var force bool
	cmd := &cobra.Command{
		Use:    "_drain-probe",
		Short:  "Report the helper's active launch count (internal)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !filepath.IsAbs(socket) {
				return fmt.Errorf("helper socket must be absolute")
			}
			var lease *os.File
			if upgradeGuard {
				lease = os.Stdin
			}
			active, err := probeHelper(cmd.Context(), socket, force, lease)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), active)
			return nil
		},
	}
	cmd.Flags().BoolVar(&upgradeGuard, "upgrade-guard", false, "")
	cmd.Flags().StringVar(&socket, "socket", "", "absolute helper socket path")
	cmd.Flags().BoolVar(&force, "force", false, "kill active isolated launches")
	return cmd
}
