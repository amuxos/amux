package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/credentialref"
	"github.com/spf13/cobra"
)

func newAgentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "agent",
		Short: "Manage agent profiles",
	}
	show := supportsJSONOutput(&cobra.Command{
		Use:               "show <profile>",
		Short:             "Show an agent profile",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentProfileRefs,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := resolveAgentProfile(args[0])
			if err != nil {
				return err
			}
			if jsonOutputEnabled(cmd) {
				view := profileCLIView(profile)
				view["schema"] = "amux.agent.show.v1"
				return printJSON(cmd.OutOrStdout(), view)
			}
			printAgentProfile(cmd.OutOrStdout(), profile)
			return nil
		},
	})
	c.AddCommand(
		newAgentListCommand("list"),
		show,
		newAgentDoctorCmd(),
		newAgentRenderCmd(),
		newAgentForkCmd(),
		newAgentLocalCmd(),
		newAgentPackCmd(),
		newCredentialCommand(agentCredentialCommandPath),
	)
	return c
}

func newAgentListCommand(use string) *cobra.Command {
	var listAll bool
	var listDevOnly bool
	cmd := supportsJSONOutput(&cobra.Command{
		Use:               use,
		Short:             "List available agent profiles",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			if listAll && listDevOnly {
				return fmt.Errorf("--all and --dev cannot be used together")
			}
			registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
			if err != nil {
				return err
			}
			profiles, profilesErr := agentprofile.DiscoverProfiles(registry, listAll || listDevOnly)
			if listDevOnly {
				development := profiles[:0]
				for _, profile := range profiles {
					if registry.Packs[profile.Namespace].IsDevelopment() {
						development = append(development, profile)
					}
				}
				profiles = development
			}
			if jsonOutputEnabled(cmd) {
				view := map[string]any{"schema": "amux.agent.list.v1", "profiles": profileCLIViews(profiles)}
				if profilesErr != nil {
					view["profiles_error"] = profilesErr.Error()
				}
				return printJSON(cmd.OutOrStdout(), view)
			}
			if profilesErr != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), commandCatalog.Format(commandLocale(cmd), "agent.profiles_warning", profilesErr))
			}
			printAgentProfileList(cmd.OutOrStdout(), profiles)
			return nil
		},
	})
	cmd.Flags().BoolVar(&listAll, "all", false, "include dev profile namespaces")
	cmd.Flags().BoolVar(&listDevOnly, "dev", false, "show only dev profile namespaces")
	return cmd
}

func profileCLIViews(profiles []agentprofile.Profile) []map[string]any {
	views := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		views = append(views, profileCLIView(profile))
	}
	return views
}

func profileCLIView(profile agentprofile.Profile) map[string]any {
	view := profileMCPView(profile)
	placement, _ := view["placement"].(map[string]any)
	placement["preferred_targets"] = nonNilStrings(profile.Placement.PreferredTargets)
	requires, _ := placement["requires"].(map[string]any)
	requires["agent_types"] = nonNilStrings(profile.Placement.Requires.AgentTypes)
	requires["runtime_tools"] = nonNilStrings(profile.Placement.Requires.RuntimeTools)
	requires["features"] = nonNilStrings(profile.Placement.Requires.Features)
	requires["credentials"] = nonNilStrings(profile.Placement.Requires.Credentials)
	return view
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func newAgentDoctorCmd() *cobra.Command {
	var smoke bool
	var smokeTimeout int
	var runtimeRef string
	var model string
	cmd := &cobra.Command{
		Use:               "doctor [profile]",
		Short:             "Diagnose an agent profile",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeAgentProfileRefs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := agentprofile.DefaultProfileRef
			if len(args) > 0 {
				ref = args[0]
			}
			profile, err := resolveAgentProfile(ref)
			if err != nil {
				return err
			}
			projection, err := agentprofile.ProjectProfileLaunchChecked(profile, nil, nil, agentprofile.LaunchOptions{
				Runtime: runtimeRef,
				Model:   model,
			})
			if err != nil {
				return err
			}
			diagnosticProfile, err := agentprofile.FromPBProfileSpec(projection.ProfileSpec)
			if err != nil {
				return err
			}
			if err := runAgentProfileDoctor(cmd.Context(), cmd.OutOrStdout(), diagnosticProfile); err != nil {
				return err
			}
			if smoke {
				return runAgentProfileSmokeWithLaunch(cmd.Context(), cmd.OutOrStdout(), profile, runtimeRef, model, smokeTimeout)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "[READY] %s\n", profile.Ref())
			return nil
		},
	}
	cmd.Flags().BoolVar(&smoke, "smoke", false, "run one real end-to-end task turn after static checks")
	cmd.Flags().IntVar(&smokeTimeout, "smoke-timeout", 120, "smoke turn timeout in seconds")
	cmd.Flags().StringVar(&runtimeRef, "runtime", "", "runtime adapter to diagnose with the Agent Profile")
	cmd.Flags().StringVar(&model, "model", "", "model to diagnose within the selected runtime")
	_ = cmd.RegisterFlagCompletionFunc("runtime", completeRuntimeRefs)
	_ = cmd.RegisterFlagCompletionFunc("model", completeRuntimeModels("runtime"))
	return cmd
}

func newAgentRenderCmd() *cobra.Command {
	var outDir string
	c := supportsJSONOutput(&cobra.Command{
		Use:               "render <profile>",
		Short:             "Render a profile workspace projection",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentProfileRefs,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := resolveAgentProfile(args[0])
			if err != nil {
				return err
			}
			root := strings.TrimSpace(outDir)
			if root == "" {
				root = filepath.Join(defaultAmuxDir(), "rendered-profiles", profile.Namespace, profile.Name)
			}
			result, err := agentprofile.RenderProfileWorkspace(profile, root)
			if err != nil {
				return err
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":      "amux.agent.render.v1",
					"profile_ref": result.ProfileRef,
					"agent_type":  result.AgentType,
					"root":        result.Root,
					"files":       nonNilStrings(result.Files),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rendered %s to %s\n", result.ProfileRef, result.Root)
			for _, file := range result.Files {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", file)
			}
			return nil
		},
	})
	c.Flags().StringVar(&outDir, "out", "", "output workspace directory")
	_ = c.RegisterFlagCompletionFunc("out", completeDirectories)
	return c
}

func newAgentPackCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pack",
		Short: "Manage agent profile packs",
	}
	list := supportsJSONOutput(&cobra.Command{
		Use:               "ls",
		Aliases:           []string{"list"},
		Short:             "List agent packs",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
			if err != nil {
				return err
			}
			packs := agentprofile.ListPacks(registry)
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema": "amux.agent.pack.list.v1",
					"packs":  packs,
				})
			}
			printAgentPacks(cmd.OutOrStdout(), packs)
			return nil
		},
	})
	versions := supportsJSONOutput(&cobra.Command{
		Use:               "versions <namespace>",
		Short:             "List recorded pack revisions",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentPackNamespaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
			if err != nil {
				return err
			}
			pack, ok := registry.Packs[strings.TrimSpace(args[0])]
			if !ok {
				return fmt.Errorf("unknown agent pack namespace %q", args[0])
			}
			if jsonOutputEnabled(cmd) {
				versions := pack.Versions
				if versions == nil {
					versions = []agentprofile.PackVersion{}
				}
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":    "amux.agent.pack.versions.v1",
					"namespace": pack.Namespace,
					"versions":  versions,
				})
			}
			printAgentPackVersions(cmd.OutOrStdout(), pack)
			return nil
		},
	})
	c.AddCommand(
		newAgentPackAddCmd(),
		list,
		newAgentPackSyncCmd(),
		newAgentPackWatchCmd(),
		newAgentPackAutoSyncCmd(),
		newAgentPackLeaseCmd(),
		newAgentPackRemoveCmd(),
		versions,
		&cobra.Command{
			Use:               "rollback <namespace> <revision>",
			Short:             "Roll back a pack to a recorded revision",
			Args:              cobra.ExactArgs(2),
			ValidArgsFunction: completeAgentPackNamespaces,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
				defer cancel()
				pack, err := agentprofile.RollbackPack(ctx, defaultAgentPackRegistryPath(), args[0], args[1])
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Rolled back pack %s to %s\n", pack.Namespace, shortRevision(pack.CurrentRevision))
				return nil
			},
		},
	)
	return c
}

func newAgentPackLeaseCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "lease",
		Short:  "Manage temporary development leases",
		Hidden: true,
	}
	var resolveField string
	resolve := &cobra.Command{
		Use:               "resolve <profile-ref>",
		Short:             "Resolve the formal development source for a profile",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			context, err := agentprofile.ResolvePackDevelopmentContext(defaultAgentPackRegistryPath(), args[0])
			if err != nil {
				return err
			}
			var value string
			switch strings.TrimSpace(resolveField) {
			case "formal-namespace":
				value = context.FormalNamespace
			case "git-url":
				value = context.GitURL
			case "ref":
				value = context.Ref
			default:
				return fmt.Errorf("unknown development context field %q", resolveField)
			}
			fmt.Fprintln(cmd.OutOrStdout(), value)
			return nil
		},
	}
	resolve.Flags().StringVar(&resolveField, "field", "", "field to print: formal-namespace, git-url, or ref")
	_ = resolve.MarkFlagRequired("field")
	var developmentOf string
	var repositoryPath string
	var worktreePath string
	var branch string
	var ownerSessionID string
	var ttlText string
	register := &cobra.Command{
		Use:               "register <temporary-namespace>",
		Short:             "Register a temporary development lease",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ttl, err := time.ParseDuration(strings.TrimSpace(ttlText))
			if err != nil || ttl <= 0 {
				return fmt.Errorf("invalid development lease TTL %q", ttlText)
			}
			lease, err := agentprofile.RegisterDevelopmentLease(
				defaultAgentPackRegistryPath(),
				agentprofile.RegisterDevelopmentLeaseOptions{
					Namespace:      args[0],
					DevelopmentOf:  developmentOf,
					RepositoryPath: repositoryPath,
					WorktreePath:   worktreePath,
					Branch:         branch,
					OwnerSessionID: ownerSessionID,
					TTL:            ttl,
				},
			)
			if err != nil {
				return err
			}
			owner := lease.OwnerSessionID
			if owner == "" {
				owner = "none"
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"Registered development lease %s (owner: %s, expires: %s)\n",
				lease.Namespace,
				owner,
				lease.ExpiresAt.Format(time.RFC3339),
			)
			return nil
		},
	}
	register.Flags().StringVar(&developmentOf, "dev-of", "", "formal pack namespace that owns this development task")
	register.Flags().StringVar(&repositoryPath, "repo", "", "base Git repository path")
	register.Flags().StringVar(&worktreePath, "worktree", "", "task Git worktree path")
	register.Flags().StringVar(&branch, "branch", "", "task Git branch")
	register.Flags().StringVar(&ownerSessionID, "owner-session", os.Getenv("AMUX_SESSION_ID"), "owner amux session ID")
	register.Flags().StringVar(&ttlText, "ttl", agentprofile.DefaultDevelopmentLeaseTTL.String(), "ownerless lease lifetime")
	_ = register.MarkFlagRequired("dev-of")
	_ = register.MarkFlagRequired("repo")
	_ = register.MarkFlagRequired("worktree")
	_ = register.MarkFlagRequired("branch")

	cleanup := &cobra.Command{
		Use:               "cleanup <temporary-namespace>",
		Short:             "Clean a temporary development lease",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := agentprofile.CleanupDevelopmentLease(defaultAgentPackRegistryPath(), args[0])
			if err != nil {
				return err
			}
			if result.Status == agentprofile.DevelopmentLeaseRetained {
				return fmt.Errorf("development lease %s retained: %s", result.Lease.Namespace, result.Reason)
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"Cleaned development lease %s; branch %s was retained\n",
				result.Lease.Namespace,
				result.Lease.Branch,
			)
			return nil
		},
	}
	c.AddCommand(resolve, register, cleanup)
	return c
}

func newAgentPackRemoveCmd() *cobra.Command {
	var keepFiles bool
	c := &cobra.Command{
		Use:               "remove <namespace>",
		Aliases:           []string{"rm", "delete"},
		Short:             "Remove an agent pack",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentPackNamespaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := agentprofile.RemovePack(defaultAgentPackRegistryPath(), args[0], agentprofile.RemovePackOptions{
				KeepFiles: keepFiles,
			})
			if err != nil {
				return err
			}
			printRemovedAgentPack(cmd.OutOrStdout(), result)
			return nil
		},
	}
	c.Flags().BoolVar(&keepFiles, "keep-files", false, "unregister the pack without deleting its local materialized files")
	return c
}

func newAgentPackAddCmd() *cobra.Command {
	var ref string
	var devOf string
	var temporary bool
	var noAutoSync bool
	var syncInterval string
	c := &cobra.Command{
		Use:               "add <namespace> <git-url>",
		Short:             "Add a Git-backed agent pack",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			interval, err := parsePackSyncInterval(syncInterval)
			if err != nil {
				return err
			}
			devOf = strings.TrimSpace(devOf)
			if temporary && devOf == "" {
				return fmt.Errorf("--temporary requires --dev-of")
			}
			if temporary {
				noAutoSync = true
			}
			opts := agentprofile.AddGitPackOptions{
				Ref:           ref,
				DevelopmentOf: devOf,
				Temporary:     temporary,
				AutoSync:      !noAutoSync,
				SyncInterval:  interval.String(),
			}
			pack, err := agentprofile.AddGitPackWithOptions(ctx, defaultAgentPackRegistryPath(), args[0], args[1], opts)
			if err != nil {
				return err
			}
			printSyncedAgentPack(cmd.OutOrStdout(), pack)
			if !noAutoSync {
				printAgentPackAutoSyncStartResult(cmd.OutOrStdout(), startAgentPackAutoSyncService())
			}
			return nil
		},
	}
	c.Flags().StringVar(&ref, "ref", "", "Git ref to sync, such as stable, main, or a tag")
	c.Flags().StringVar(&devOf, "dev-of", "", "formal pack namespace that owns this development pack")
	c.Flags().BoolVar(&temporary, "temporary", false, "mark a --dev-of pack as temporary and disable auto sync")
	c.Flags().BoolVar(&noAutoSync, "no-auto-sync", false, "record the pack with auto sync disabled")
	c.Flags().StringVar(&syncInterval, "sync-interval", defaultPackWatchInterval.String(), "default auto sync interval recorded for the pack")
	_ = c.RegisterFlagCompletionFunc("ref", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("dev-of", completeAgentPackNamespaces)
	_ = c.RegisterFlagCompletionFunc("sync-interval", completeNoFiles)
	return c
}

func newAgentPackSyncCmd() *cobra.Command {
	c := &cobra.Command{
		Use:               "sync [namespace]",
		Short:             "Sync one or all agent packs",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeAgentPackNamespaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			if len(args) == 0 {
				if err := syncAllAgentPacksAndPrint(ctx, cmd.OutOrStdout(), defaultAgentPackRegistryPath()); err != nil {
					return err
				}
				startAgentPackAutoSyncForRegisteredPacks(cmd.OutOrStdout(), defaultAgentPackRegistryPath())
				return nil
			}
			if err := syncAgentPackAndPrint(ctx, cmd.OutOrStdout(), defaultAgentPackRegistryPath(), args[0]); err != nil {
				return err
			}
			startAgentPackAutoSyncForRegisteredPacks(cmd.OutOrStdout(), defaultAgentPackRegistryPath())
			return nil
		},
	}
	return c
}

func newAgentPackWatchCmd() *cobra.Command {
	var intervalText string
	c := &cobra.Command{
		Use:               "watch <namespace>",
		Short:             "Continuously sync an agent pack",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAgentPackNamespaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			interval, err := parsePackSyncInterval(intervalText)
			if err != nil {
				return err
			}
			return watchAgentPack(cmd.Context(), cmd.OutOrStdout(), defaultAgentPackRegistryPath(), args[0], interval)
		},
	}
	c.Flags().StringVar(&intervalText, "interval", defaultPackWatchInterval.String(), "sync interval")
	_ = c.RegisterFlagCompletionFunc("interval", completeNoFiles)
	return c
}

func newAgentPackAutoSyncCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "autosync",
		Short: "Manage background agent pack auto sync",
	}
	c.AddCommand(
		&cobra.Command{
			Use:               "start",
			Short:             "Start background agent pack auto sync",
			Args:              cobra.NoArgs,
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				err := startAgentPackAutoSyncService()
				if err != nil {
					return err
				}
				svc := agentPackAutoSyncService()
				fmt.Fprintf(cmd.OutOrStdout(), "agent pack autosync: started\npid: %s\nlog: %s\n", svc.PIDPath, svc.LogPath)
				return nil
			},
		},
		&cobra.Command{
			Use:               "stop",
			Short:             "Stop background agent pack auto sync",
			Args:              cobra.NoArgs,
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := agentPackAutoSyncService().Stop(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "agent pack autosync: stopped")
				return nil
			},
		},
		&cobra.Command{
			Use:               "status",
			Short:             "Show background agent pack auto sync status",
			Args:              cobra.NoArgs,
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				report := inspectService(agentPackAutoSyncService(), "", "")
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "state: %s\n", report.State)
				if report.PID > 0 {
					fmt.Fprintf(out, "pid: %d\n", report.PID)
				}
				if !report.StartedAt.IsZero() {
					fmt.Fprintf(out, "started_at: %s\n", formatStatusTime(report.StartedAt))
				}
				fmt.Fprintf(out, "pid_file: %s\n", report.PIDPath)
				fmt.Fprintf(out, "log: %s\n", report.LogPath)
				if report.Error != "" {
					fmt.Fprintf(out, "error: %s\n", report.Error)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:               "logs",
			Short:             "Show background agent pack auto sync log path",
			Args:              cobra.NoArgs,
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Fprintln(cmd.OutOrStdout(), agentPackAutoSyncService().LogPath)
				return nil
			},
		},
		&cobra.Command{
			Use:               "run",
			Short:             "Run background agent pack auto sync loop",
			Args:              cobra.NoArgs,
			Hidden:            true,
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
				defer stop()
				return runAgentPackAutoSyncDaemon(ctx, cmd.OutOrStdout(), defaultAgentPackRegistryPath())
			},
		},
	)
	return c
}

var defaultPackWatchInterval = 30 * time.Second

func parsePackSyncInterval(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultPackWatchInterval, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid sync interval %q: %w", value, err)
	}
	if interval <= 0 {
		return 0, fmt.Errorf("sync interval must be greater than zero")
	}
	return interval, nil
}

func syncAgentPackAndPrint(ctx context.Context, out io.Writer, registryPath, namespace string) error {
	namespace = strings.TrimSpace(namespace)
	pack, err := agentprofile.SyncPack(ctx, registryPath, namespace)
	if err != nil {
		fmt.Fprintf(out, "Failed to sync pack %s: %v\n", namespace, err)
		return err
	}
	printSyncedAgentPack(out, pack)
	return nil
}

func syncAllAgentPacksAndPrint(ctx context.Context, out io.Writer, registryPath string) error {
	registry, err := agentprofile.LoadPackRegistry(registryPath)
	if err != nil {
		return err
	}
	for _, pack := range agentprofile.ListPacks(registry) {
		if err := syncAgentPackAndPrint(ctx, out, registryPath, pack.Namespace); err != nil {
			return err
		}
	}
	return nil
}

func agentPackAutoSyncService() managedService {
	return managedService{
		Name:    "agent-pack-autosync",
		Binary:  "amux",
		Args:    []string{"agent", "pack", "autosync", "run"},
		PIDPath: filepath.Join(defaultAmuxDir(), "agent-pack-autosync.pid"),
		LogPath: filepath.Join(defaultAmuxDir(), "agent-pack-autosync.log"),
	}
}

var startAgentPackAutoSyncService = func() error {
	err := agentPackAutoSyncService().Start(false)
	if isAlreadyRunning(err) {
		return nil
	}
	return err
}

var (
	agentPackAutoSyncTimeout = 90 * time.Second
	syncPackForAutoSync      = agentprofile.SyncPack
)

func startAgentPackAutoSyncForRegisteredPacks(out io.Writer, registryPath string) {
	registry, err := agentprofile.LoadPackRegistry(registryPath)
	if err != nil {
		fmt.Fprintf(out, "Warning: agent pack auto sync not started: %v\n", err)
		return
	}
	for _, pack := range agentprofile.ListPacks(registry) {
		if pack.AutoSync {
			printAgentPackAutoSyncStartResult(out, startAgentPackAutoSyncService())
			return
		}
	}
}

func printAgentPackAutoSyncStartResult(out io.Writer, err error) {
	if err == nil {
		svc := agentPackAutoSyncService()
		fmt.Fprintf(out, "Agent pack auto sync started: pid %s, log %s\n", svc.PIDPath, svc.LogPath)
		return
	}
	fmt.Fprintf(out, "Warning: agent pack auto sync not started: %v\n", err)
}

func runAgentPackAutoSyncDaemon(ctx context.Context, out io.Writer, registryPath string) error {
	cleanup := registerTrackedProcess(trackedProcessRolePackAutoSync)
	defer cleanup()
	for {
		if err := runAgentPackAutoSyncOnce(ctx, out, registryPath); err != nil {
			fmt.Fprintf(out, "warning: auto sync cycle failed: %v\n", err)
		}
		interval := nextAgentPackAutoSyncInterval(registryPath)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func runAgentPackAutoSyncOnce(ctx context.Context, out io.Writer, registryPath string) error {
	registry, err := agentprofile.LoadPackRegistry(registryPath)
	if err != nil {
		return err
	}
	for _, pack := range agentprofile.ListPacks(registry) {
		if !pack.AutoSync {
			continue
		}
		syncCtx, cancel := context.WithTimeout(ctx, agentPackAutoSyncTimeout)
		next, err := syncPackForAutoSync(syncCtx, registryPath, pack.Namespace)
		cancel()
		if err != nil {
			fmt.Fprintf(out, "Warning: failed to auto sync pack %s: %v\n", pack.Namespace, err)
			continue
		}
		printSyncedAgentPack(out, next)
	}
	return nil
}

func nextAgentPackAutoSyncInterval(registryPath string) time.Duration {
	registry, err := agentprofile.LoadPackRegistry(registryPath)
	if err != nil {
		return defaultPackWatchInterval
	}
	interval := time.Duration(0)
	for _, pack := range agentprofile.ListPacks(registry) {
		if !pack.AutoSync {
			continue
		}
		d, err := parsePackSyncInterval(pack.SyncInterval)
		if err != nil {
			d = defaultPackWatchInterval
		}
		if interval == 0 || d < interval {
			interval = d
		}
	}
	if interval <= 0 {
		return defaultPackWatchInterval
	}
	return interval
}

func watchAgentPack(ctx context.Context, out io.Writer, registryPath, namespace string, interval time.Duration) error {
	for {
		if err := syncAgentPackAndPrint(ctx, out, registryPath, namespace); err != nil {
			fmt.Fprintf(out, "warning: sync failed; keeping last usable revision\n")
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func resolveAgentProfile(ref string) (agentprofile.Profile, error) {
	return agentprofile.ResolveProfile(defaultAgentPackRegistryPath(), ref)
}

func availableAgentProfiles() ([]agentprofile.Profile, error) {
	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		return agentprofile.Builtins(), err
	}
	return agentprofile.DiscoverProfiles(registry, false)
}

func defaultAgentPackRegistryPath() string {
	return agentprofile.DefaultPackRegistryPath()
}

func printAgentProfileList(out io.Writer, profiles []agentprofile.Profile) {
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Ref() < profiles[j].Ref()
	})
	fmt.Fprintln(out, "REF             TYPE    SOURCE   DESCRIPTION")
	for _, profile := range profiles {
		fmt.Fprintf(out, "%-15s %-7s %-8s %s\n", profile.Ref(), profile.Type, profile.Source, profile.Description)
	}
}

func printAgentProfile(out io.Writer, profile agentprofile.Profile) {
	fmt.Fprintf(out, "ref: %s\n", profile.Ref())
	if profile.PinnedRef() != profile.Ref() {
		fmt.Fprintf(out, "pinned_ref: %s\n", profile.PinnedRef())
	}
	fmt.Fprintf(out, "type: %s\n", profile.Type)
	if profile.Model != "" {
		fmt.Fprintf(out, "model: %s\n", profile.Model)
	}
	printAgentProfileStringList(out, "supported_models", profile.SupportedModels)
	fmt.Fprintf(out, "source: %s\n", profile.Source)
	if profile.Description != "" {
		fmt.Fprintf(out, "description: %s\n", profile.Description)
	}
	if profile.Agents != "" {
		fmt.Fprintf(out, "agents: %s\n", profile.Agents)
	}
	printAgentProfileStringList(out, "skills", profile.Skills)
	printAgentProfileStringList(out, "subagents", profile.Subagents)
	printAgentProfileStringList(out, "skill_dirs", profile.SkillDirs)
	printAgentProfileStringList(out, "default_args", profile.DefaultArgs)
	printAgentProfileStringList(out, "default_env", profile.DefaultEnv)
	printAgentProfileStringList(out, "envs", profile.Envs)
	printAgentProfileStringList(out, "env_refs", profile.EnvRefs)
	if profile.MCPConfig != "" {
		fmt.Fprintf(out, "mcp_config: %s\n", profile.MCPConfig)
	}
	if profile.PermissionPolicy != "" {
		fmt.Fprintf(out, "permission_policy: %s\n", profile.PermissionPolicy)
	}
	printAgentProfileCredentialEnvs(out, profile)
	printAgentProfileCredentialEnvRefs(out, profile)
	printAgentProfileProviderCredential(out, profile)
	printAgentProfileStringList(out, "preferred_targets", profile.Placement.PreferredTargets)
	printAgentProfileStringList(out, "agent_types", profile.Placement.Requires.AgentTypes)
	printAgentProfileStringList(out, "runtime_tools", profile.Placement.Requires.RuntimeTools)
	printAgentProfileStringList(out, "features", profile.Placement.Requires.Features)
	printAgentProfileStringList(out, "credentials", profile.Placement.Requires.Credentials)
}

func printAgentProfileStringList(out io.Writer, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(out, "%s: %s\n", label, strings.Join(values, ", "))
}

func runAgentProfileDoctor(ctx context.Context, out io.Writer, profile agentprofile.Profile) error {
	fmt.Fprintln(out, "Agent profile doctor")
	fmt.Fprintf(out, "[OK] profile: %s\n", profile.Ref())
	_, err := adapter.Get(profile.Type)
	if err != nil {
		fmt.Fprintf(out, "[FAIL] adapter: %v\n", err)
		return err
	}
	fmt.Fprintf(out, "[OK] adapter: %s\n", profile.Type)
	var findings []error
	executables := map[string]string{}
	checkExecutable := func(agentType string) {
		if _, checked := executables[agentType]; checked {
			return
		}
		a, getErr := adapter.Get(agentType)
		if getErr != nil {
			findings = append(findings, getErr)
			return
		}
		path, lookupErr := exec.LookPath(a.Cmd(nil).Path)
		if lookupErr != nil {
			fmt.Fprintf(out, "[FAIL] executable: %s (not found in PATH)\n", a.Cmd(nil).Path)
			findings = append(findings, fmt.Errorf("%s executable is not available in PATH", a.Cmd(nil).Path))
			return
		}
		executables[agentType] = path
		fmt.Fprintf(out, "[OK] executable: %s\n", path)
	}
	checkExecutable(profile.Type)
	for _, required := range profile.Placement.Requires.AgentTypes {
		if required == profile.Type {
			fmt.Fprintf(out, "[OK] placement.agent_types: %s\n", required)
			continue
		}
		if _, err := adapter.Get(required); err != nil {
			fmt.Fprintf(out, "[FAIL] placement.agent_types: %s (%v)\n", required, err)
			return err
		}
		fmt.Fprintf(out, "[OK] placement.agent_types: %s\n", required)
		checkExecutable(required)
	}
	for _, tool := range profile.Placement.Requires.RuntimeTools {
		path, lookupErr := exec.LookPath(tool)
		if lookupErr != nil {
			fmt.Fprintf(out, "[FAIL] runtime_tool: %s (not found in PATH)\n", tool)
			findings = append(findings, fmt.Errorf("runtime tool %s is not available in PATH", tool))
			continue
		}
		fmt.Fprintf(out, "[OK] runtime_tool: %s (%s)\n", tool, path)
	}
	if executable := executables[profile.Type]; executable != "" {
		if authErr := runAgentAuthenticationPreflight(ctx, profile.Type, executable); authErr != nil {
			fmt.Fprintf(out, "[FAIL] authentication: %v\n", authErr)
			findings = append(findings, authErr)
		} else if probe := distributionAuthenticationProbeLabel(profile.Type); probe != "" {
			fmt.Fprintf(out, "[OK] authentication: %s\n", probe)
		} else {
			fmt.Fprintf(out, "[INFO] authentication: %s has no stable static login probe; use --smoke\n", profile.Type)
		}
	}
	if err := runAgentCredentialEnvDoctor(out, profile); err != nil {
		findings = append(findings, err)
	}
	if err := runAgentCredentialEnvRefDoctor(out, profile); err != nil {
		findings = append(findings, err)
	}
	if err := runAgentProviderCredentialDoctor(out, profile); err != nil {
		findings = append(findings, err)
	}
	return errors.Join(findings...)
}

func printAgentProfileCredentialEnvs(out io.Writer, profile agentprofile.Profile) {
	for _, envName := range profile.Envs {
		envName = strings.TrimSpace(envName)
		if envName == "" {
			continue
		}
		resolution := globalCredentialEnvResolution(envName)
		fmt.Fprintf(out, "credential_env: %s\n", envName)
		if resolution.sourceProfile != "" {
			fmt.Fprintf(out, "credential_source: %s\n", resolution.sourceProfile)
		}
		if resolution.detail != "" {
			fmt.Fprintf(out, "credential: %s (%s)\n", resolution.status, resolution.detail)
		} else {
			fmt.Fprintf(out, "credential: %s\n", resolution.status)
		}
		setupProfile := resolution.sourceProfile
		if setupProfile == "" && resolution.status == credentialEnvMissing {
			setupProfile = agentprofile.SuggestedCredentialProfileForEnv(profile, envName)
		}
		if setupProfile != "" && (resolution.sourceProfile != "" || resolution.status == credentialEnvMissing) {
			label := "credential_setup"
			if resolution.sourceProfile == "" {
				label = "credential_setup_suggestion"
			}
			fmt.Fprintf(out, "%s: %s\n", label, credentialSetupCommand(agentCredentialCommandPath, setupProfile, envName))
		}
	}
}

func runAgentCredentialEnvDoctor(out io.Writer, profile agentprofile.Profile) error {
	var findings []error
	for _, envName := range profile.Envs {
		envName = strings.TrimSpace(envName)
		if envName == "" {
			continue
		}
		resolution := globalCredentialEnvResolution(envName)
		if resolution.status == credentialEnvConfigured {
			fmt.Fprintf(out, "[OK] credential_env: profile %s uniquely defines %s\n", resolution.sourceProfile, envName)
			continue
		}
		if resolution.detail != "" {
			fmt.Fprintf(out, "[FAIL] credential_env: %s (%s)\n", resolution.status, resolution.detail)
		} else {
			fmt.Fprintf(out, "[FAIL] credential_env: %s\n", resolution.status)
		}
		findings = append(findings, fmt.Errorf("required credential env %s is %s", envName, resolution.status))
		if suggestedProfile := agentprofile.SuggestedCredentialProfileForEnv(profile, envName); resolution.status == credentialEnvMissing && suggestedProfile != "" {
			fmt.Fprintf(out, "[INFO] suggested credential setup: %s\n", credentialSetupCommand(agentCredentialCommandPath, suggestedProfile, envName))
		}
	}
	return errors.Join(findings...)
}

type credentialEnvStatus string

const (
	credentialEnvConfigured credentialEnvStatus = "configured"
	credentialEnvMissing    credentialEnvStatus = "missing"
	credentialEnvAmbiguous  credentialEnvStatus = "ambiguous"
	credentialEnvInvalid    credentialEnvStatus = "invalid"
	credentialEnvUnresolved credentialEnvStatus = "unresolved"
)

type agentCredentialEnvResolution struct {
	status        credentialEnvStatus
	sourceProfile string
	detail        string
}

func globalCredentialEnvResolution(envName string) agentCredentialEnvResolution {
	envName = strings.TrimSpace(envName)
	if err := validateCredentialEnvName(envName); err != nil {
		return agentCredentialEnvResolution{status: credentialEnvInvalid, detail: err.Error()}
	}
	profiles, err := listCredentialMetadata(context.Background())
	if err != nil {
		return agentCredentialEnvResolution{status: credentialEnvUnresolved, detail: err.Error()}
	}
	var found *credentialProfileMetadata
	for i := range profiles {
		if !slices.Contains(profiles[i].EnvNames, envName) {
			continue
		}
		if found != nil && found.Profile != profiles[i].Profile {
			return agentCredentialEnvResolution{
				status: credentialEnvAmbiguous,
				detail: fmt.Sprintf("credential env %s is provided by multiple profiles: %q and %q", envName, found.Profile, profiles[i].Profile),
			}
		}
		found = &profiles[i]
	}
	if found == nil {
		return agentCredentialEnvResolution{status: credentialEnvMissing, detail: fmt.Sprintf("credential store does not define env %s", envName)}
	}
	return agentCredentialEnvResolution{
		status:        credentialEnvConfigured,
		sourceProfile: found.Profile,
		detail:        found.Path,
	}
}

func printAgentProfileCredentialEnvRefs(out io.Writer, profile agentprofile.Profile) {
	refs, err := credentialref.ParseEnvRefs(profile.EnvRefs)
	if err != nil {
		fmt.Fprintf(out, "credential_ref_error: %v\n", err)
		return
	}
	for _, ref := range refs {
		fmt.Fprintf(out, "credential_ref: %s:%s\n", ref.Profile, ref.Env)
		state, detail := credentialEnvRefState(ref)
		if detail != "" {
			fmt.Fprintf(out, "credential: %s (%s)\n", state, detail)
		} else {
			fmt.Fprintf(out, "credential: %s\n", state)
		}
		fmt.Fprintf(out, "credential_setup: %s\n", credentialSetupCommand(agentCredentialCommandPath, ref.Profile, ref.Env))
	}
}

func runAgentCredentialEnvRefDoctor(out io.Writer, profile agentprofile.Profile) error {
	refs, err := credentialref.ParseEnvRefs(profile.EnvRefs)
	if err != nil {
		fmt.Fprintf(out, "[FAIL] credential_ref: %v\n", err)
		return err
	}
	var findings []error
	for _, ref := range refs {
		state, detail := credentialEnvRefState(ref)
		switch state {
		case "configured":
			fmt.Fprintf(out, "[OK] credential_ref: profile %s defines %s\n", ref.Profile, ref.Env)
		case "missing":
			if detail != "" {
				fmt.Fprintf(out, "[FAIL] credential_ref: missing env %s in profile %s (%s)\n", ref.Env, ref.Profile, detail)
			} else {
				fmt.Fprintf(out, "[FAIL] credential_ref: missing env %s in profile %s\n", ref.Env, ref.Profile)
			}
			fmt.Fprintf(out, "[INFO] credential setup: %s\n", credentialSetupCommand(agentCredentialCommandPath, ref.Profile, ref.Env))
			findings = append(findings, fmt.Errorf("credential profile %s does not define required env %s", ref.Profile, ref.Env))
		default:
			if detail != "" {
				fmt.Fprintf(out, "[FAIL] credential_ref: %s profile %s (%s)\n", state, ref.Profile, detail)
			} else {
				fmt.Fprintf(out, "[FAIL] credential_ref: %s profile %s\n", state, ref.Profile)
			}
			fmt.Fprintf(out, "[INFO] credential setup: %s\n", credentialSetupCommand(agentCredentialCommandPath, ref.Profile, ref.Env))
			findings = append(findings, fmt.Errorf("credential profile %s is %s for required env %s", ref.Profile, state, ref.Env))
		}
	}
	return errors.Join(findings...)
}

func credentialEnvRefState(ref credentialref.EnvRef) (string, string) {
	return credentialEnvState(ref.Profile, ref.Env)
}

func credentialEnvState(profile, envName string) (string, string) {
	profile = strings.TrimSpace(profile)
	envName = strings.TrimSpace(envName)
	if err := validateCredentialProfileName(profile); err != nil {
		return "invalid", err.Error()
	}
	if err := validateCredentialEnvName(envName); err != nil {
		return "invalid", err.Error()
	}
	meta, err := getCredentialMetadata(context.Background(), profile)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", credentialMissingLocation(profile)
		}
		return "unreadable", err.Error()
	}
	if !slices.Contains(meta.EnvNames, envName) {
		return "missing", meta.Path
	}
	return "configured", meta.Path
}

func printAgentProfileProviderCredential(out io.Writer, profile agentprofile.Profile) {
	credentialProfile := agentprofile.EffectiveCredentialProfile(profile.Provider)
	if credentialProfile == "" || strings.TrimSpace(profile.Provider.APIKeyEnv) == "" {
		return
	}
	fmt.Fprintf(out, "provider_platform: %s\n", strings.TrimSpace(profile.Provider.Platform))
	fmt.Fprintf(out, "provider_api_key_env: %s\n", strings.TrimSpace(profile.Provider.APIKeyEnv))
	if literal := strings.TrimSpace(profile.Provider.APIKeyLiteral); literal != "" {
		// Literal key is self-contained: no store lookup, no setup suggestion.
		fmt.Fprintf(out, "provider_api_key_literal: %s\n", literal)
		return
	}
	if providerCredentialCoveredByProfileEnvs(profile) {
		return
	}
	state, detail := agentProviderCredentialState(profile)
	if detail != "" {
		fmt.Fprintf(out, "credential: %s (%s)\n", state, detail)
	} else {
		fmt.Fprintf(out, "credential: %s\n", state)
	}
	fmt.Fprintf(out, "credential_setup: %s\n", credentialSetupCommand(agentCredentialCommandPath, credentialProfile, profile.Provider.APIKeyEnv))
}

func runAgentProviderCredentialDoctor(out io.Writer, profile agentprofile.Profile) error {
	credentialProfile := agentprofile.EffectiveCredentialProfile(profile.Provider)
	if credentialProfile == "" || strings.TrimSpace(profile.Provider.APIKeyEnv) == "" {
		return nil
	}
	if literal := strings.TrimSpace(profile.Provider.APIKeyLiteral); literal != "" {
		// Literal key is self-contained: report OK without a store lookup.
		fmt.Fprintf(out, "[OK] credential: %s uses literal api key (api_key_literal)\n", strings.TrimSpace(profile.Provider.APIKeyEnv))
		return nil
	}
	if providerCredentialCoveredByProfileEnvs(profile) {
		return nil
	}
	state, detail := agentProviderCredentialState(profile)
	switch state {
	case "configured":
		fmt.Fprintf(out, "[OK] credential: %s defines %s\n", credentialProfile, strings.TrimSpace(profile.Provider.APIKeyEnv))
		return nil
	default:
		if detail != "" {
			fmt.Fprintf(out, "[FAIL] credential: %s profile %s (%s)\n", state, credentialProfile, detail)
		} else {
			fmt.Fprintf(out, "[FAIL] credential: %s profile %s\n", state, credentialProfile)
		}
		fmt.Fprintf(out, "[INFO] credential setup: %s\n", credentialSetupCommand(agentCredentialCommandPath, credentialProfile, profile.Provider.APIKeyEnv))
		return fmt.Errorf("provider credential profile %s is %s for required env %s", credentialProfile, state, strings.TrimSpace(profile.Provider.APIKeyEnv))
	}
	return nil
}

func providerCredentialCoveredByProfileEnvs(profile agentprofile.Profile) bool {
	if profile.Provider == nil {
		return false
	}
	credentialProfile := agentprofile.EffectiveCredentialProfile(profile.Provider)
	apiKeyEnv := strings.TrimSpace(profile.Provider.APIKeyEnv)
	if credentialProfile == "" || apiKeyEnv == "" {
		return false
	}
	for _, envName := range profile.Envs {
		if strings.TrimSpace(envName) == apiKeyEnv {
			return true
		}
	}
	return false
}

func agentProviderCredentialState(profile agentprofile.Profile) (string, string) {
	provider := profile.Provider
	if provider == nil {
		return "", ""
	}
	credentialProfile := agentprofile.EffectiveCredentialProfile(provider)
	apiKeyEnv := strings.TrimSpace(provider.APIKeyEnv)
	meta, err := getCredentialMetadata(context.Background(), credentialProfile)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", credentialMissingLocation(credentialProfile)
		}
		return "unreadable", err.Error()
	}
	if !slices.Contains(meta.EnvNames, apiKeyEnv) {
		return "missing env " + apiKeyEnv, meta.Path
	}
	return "configured", meta.Path
}

func printSyncedAgentPack(out io.Writer, pack agentprofile.Pack) {
	fmt.Fprintf(out, "Synced pack %s at %s\n", pack.Namespace, shortRevision(pack.CurrentRevision))
}

func printRemovedAgentPack(out io.Writer, result agentprofile.RemovePackResult) {
	fmt.Fprintf(out, "Removed pack %s\n", result.Pack.Namespace)
	if result.FilesRemoved {
		fmt.Fprintf(out, "Removed files %s\n", result.RemovedPath)
	}
}

func printAgentPacks(out io.Writer, packs []agentprofile.Pack) {
	fmt.Fprintln(out, "NAMESPACE SOURCE REF    DEV_OF TEMPORARY REVISION     AUTO_SYNC INTERVAL LAST_SYNCED_AT        LAST_ERROR PATH")
	for _, pack := range packs {
		lastError := displayPackLastError(pack.LastError)
		fmt.Fprintf(out, "%-9s %-6s %-6s %-6s %-9v %-12s %-9v %-8s %-21s %-10s %s\n",
			pack.Namespace,
			pack.SourceKind,
			defaultDisplay(pack.Ref, "-"),
			defaultDisplay(pack.DevelopmentOf, "-"),
			pack.Temporary,
			shortRevision(pack.CurrentRevision),
			pack.AutoSync,
			defaultDisplay(pack.SyncInterval, "-"),
			defaultDisplay(pack.LastSyncedAt, "-"),
			lastError,
			pack.MaterializedPath,
		)
	}
}

func displayPackLastError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.Join(strings.Fields(value), " ")
	const maxDisplayPackLastErrorLength = 80
	if len(value) > maxDisplayPackLastErrorLength {
		return value[:maxDisplayPackLastErrorLength-3] + "..."
	}
	return value
}

func printAgentPackVersions(out io.Writer, pack agentprofile.Pack) {
	fmt.Fprintf(out, "Versions for pack %s\n", pack.Namespace)
	fmt.Fprintln(out, "REVISION     REF    SYNCED_AT")
	for _, version := range pack.Versions {
		fmt.Fprintf(out, "%-12s %-6s %s\n", shortRevision(version.Revision), defaultDisplay(version.Ref, "-"), version.SyncedAt)
	}
}

func shortRevision(revision string) string {
	if len(revision) <= 12 {
		return revision
	}
	return revision[:12]
}

func defaultDisplay(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func completeAgentProfileRefs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	refs := make([]string, 0)
	profiles, _ := availableAgentProfiles()
	for _, profile := range profiles {
		refs = append(refs, profile.Ref()+"\t"+profile.Description)
	}
	return filterCompletionValues(refs, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeAgentPackNamespaces(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	values := make([]string, 0, len(registry.Packs))
	for _, pack := range agentprofile.ListPacks(registry) {
		values = append(values, pack.Namespace+"\t"+pack.SourceKind)
	}
	return filterCompletionValues(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}
