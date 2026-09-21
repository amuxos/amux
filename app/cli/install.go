package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/system"
	"github.com/amuxos/amux/internal/updatecheck"
	"github.com/spf13/cobra"
)

var installBinaryNames = []string{"amux", "amuxd", "amuxlet"}

var updateHTTPClient = &http.Client{Timeout: 60 * time.Second}
var updateSystemLifecycleOpener systemLifecycleOpener = openSystemLifecycle
var updatePostInstallRunner = runUpdatePostInstallCommand
var updateAgentPackAutoSyncRefresher = func() error {
	return restartIfRunning(agentPackAutoSyncService())
}
var uninstallServiceStopper = stopServicesForUninstall
var installManagedContractRuntime = agentprofile.EnsureDefaultManagedContractRuntime
var prepareManagedLocalLibrary = agentprofile.PrepareDefaultManagedLibrary

type installMetadata struct {
	Version                    string                              `json:"version"`
	BinDir                     string                              `json:"bin_dir"`
	SourceDir                  string                              `json:"source_dir,omitempty"`
	InstalledAt                string                              `json:"installed_at"`
	Binaries                   []string                            `json:"binaries"`
	AgentPackContractVersion   string                              `json:"agent_pack_contract_version"`
	AgentPackContractPath      string                              `json:"agent_pack_contract_path"`
	AgentPackContractStatus    agentprofile.ManagedAuthoringStatus `json:"agent_pack_contract_status"`
	LocalExpertAuthoringStatus agentprofile.ManagedAuthoringStatus `json:"local_expert_authoring_status"`
}

type installOptions struct {
	sourceDir             string
	binDir                string
	shellProfile          string
	appliedShellProfile   string
	scmLatestURL          string
	installedVersion      string
	expectedVersion       string
	force                 bool
	restart               bool
	restartSessions       bool
	restartLocalServices  bool
	restartAll            bool
	updateShell           bool
	postInstall           bool
	managedAuthoringState agentprofile.ManagedAuthoringState
	installWarnings       []string
}

type installNextStepsContext int

const (
	installNextStepsFresh installNextStepsContext = iota
	installNextStepsConfigured
)

func newInstallCmd() *cobra.Command {
	opts := installOptions{}
	c := &cobra.Command{
		Use:               "install",
		Short:             "Install amux from a local source directory",
		Hidden:            true,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			nextSteps := currentInstallNextStepsContext()
			if err := runInstall(&opts); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Installed amux %s\n", version)
			printInstallWarnings(cmd.OutOrStdout(), &opts)
			printManagedAuthoringSummary(cmd.OutOrStdout(), &opts)
			printInstallNextSteps(cmd.OutOrStdout(), opts.binDir, opts.appliedShellProfile, nextSteps)
			return nil
		},
	}
	addInstallFlags(c, &opts)
	return c
}

func newUpdateCmd() *cobra.Command {
	opts := installOptions{force: true, scmLatestURL: defaultUpdateSCMLatestURL()}
	c := &cobra.Command{
		Use:               "update",
		Short:             "Update the current user's amux installation",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			nextSteps := installNextStepsConfigured
			normalizeUpdateRestartOptions(&opts)
			out := cmd.OutOrStdout()
			if opts.postInstall {
				return completeUpdatePostInstall(cmd, &opts, nextSteps)
			}
			printUpdateStart(out, &opts)
			if opts.sourceDir == "" {
				fmt.Fprintln(out, "Downloading latest release...")
				sourceDir, releaseVersion, cleanup, err := prepareSCMUpdateSource(opts.scmLatestURL)
				if err != nil {
					return err
				}
				defer cleanup()
				if err := validateExpectedUpdateVersion(opts.expectedVersion, releaseVersion); err != nil {
					return err
				}
				opts.sourceDir = sourceDir
				opts.installedVersion = releaseVersion
			}
			fmt.Fprintf(out, "Installing binaries to %s...\n", effectiveInstallBinDir(opts.binDir))
			if err := runInstall(&opts); err != nil {
				return err
			}
			printInstallWarnings(out, &opts)
			if updatePostInstallRequiresInstalledBinary(&opts) {
				return runInstalledUpdatePostInstall(cmd, &opts)
			}
			return completeUpdatePostInstall(cmd, &opts, nextSteps)
		},
	}
	addInstallFlags(c, &opts)
	c.Flags().StringVar(&opts.scmLatestURL, "release-url", opts.scmLatestURL, "Release manifest or archive URL used when --source is omitted")
	c.Flags().StringVar(&opts.scmLatestURL, "scm-latest-url", opts.scmLatestURL, "SCM latest package URL used when --source is omitted")
	c.Flags().BoolVar(&opts.restart, "restart", false, "restart running amux services after updating")
	c.Flags().BoolVar(&opts.restartSessions, "restart-sessions", false, "restart running local user sessions after updating")
	c.Flags().BoolVar(&opts.restartLocalServices, "restart-local-services", false, "restart registered amux mcp and gui serve local servers after updating")
	c.Flags().BoolVar(&opts.restartAll, "restart-all", false, "restart all locally tracked restartable amux processes after updating")
	c.Flags().BoolVar(&opts.postInstall, "post-install", false, "complete update after binaries have been installed")
	c.Flags().StringVar(&opts.installedVersion, "installed-version", "", "installed version for post-install completion")
	c.Flags().StringVar(&opts.expectedVersion, "expected-version", "", "require the downloaded SCM release to match this version")
	c.Flags().StringVar(&opts.appliedShellProfile, "applied-shell-profile", "", "shell profile updated before post-install completion")
	_ = c.Flags().MarkHidden("post-install")
	_ = c.Flags().MarkHidden("installed-version")
	_ = c.Flags().MarkHidden("expected-version")
	_ = c.Flags().MarkHidden("applied-shell-profile")
	return c
}

func newUninstallCmd() *cobra.Command {
	var yes bool
	var purge bool
	var binDir string
	c := &cobra.Command{
		Use:               "uninstall",
		Short:             "Uninstall amux from the current user",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("refusing to uninstall without --yes")
			}
			if binDir == "" {
				binDir = defaultInstallBinDir()
			}
			if err := runUninstall(binDir, purge); err != nil {
				return err
			}
			if purge {
				fmt.Fprintln(cmd.OutOrStdout(), "Uninstalled amux and removed ~/.amux")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Uninstalled amux binaries; kept ~/.amux data")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "confirm uninstall")
	c.Flags().BoolVar(&purge, "purge", false, "remove ~/.amux data, config, logs, and secrets")
	c.Flags().StringVar(&binDir, "bin-dir", "", "installed binary directory (default: ~/.amux/bin)")
	_ = c.RegisterFlagCompletionFunc("bin-dir", completeDirectories)
	return c
}

func addInstallFlags(c *cobra.Command, opts *installOptions) {
	c.Flags().StringVar(&opts.sourceDir, "source", "", "directory containing amux, amuxd, and runtime helper binaries")
	c.Flags().StringVar(&opts.binDir, "bin-dir", "", "install binary directory (default: ~/.amux/bin)")
	c.Flags().StringVar(&opts.shellProfile, "shell-profile", "", "shell profile to update when --update-shell is set")
	c.Flags().BoolVar(&opts.force, "force", opts.force, "overwrite existing installed binaries")
	c.Flags().BoolVar(&opts.updateShell, "update-shell", false, "add ~/.amux/bin to the current shell profile")
	_ = c.RegisterFlagCompletionFunc("source", completeDirectories)
	_ = c.RegisterFlagCompletionFunc("bin-dir", completeDirectories)
	_ = c.RegisterFlagCompletionFunc("shell-profile", completeFiles)
}

func runInstall(opts *installOptions) error {
	sourceDir := opts.sourceDir
	if sourceDir == "" {
		var err error
		sourceDir, err = currentBinaryDir()
		if err != nil {
			return err
		}
	}
	binDir := opts.binDir
	if binDir == "" {
		binDir = defaultInstallBinDir()
	}
	if err := validateInstallSource(sourceDir); err != nil {
		return err
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(defaultAmuxDir(), 0755); err != nil {
		return err
	}
	if err := validateInstallDestinations(binDir, opts.force); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	contractRuntime := agentprofile.ManagedContractRuntime{
		Root:       agentprofile.DefaultManagedContractRuntimeRoot(),
		Executable: agentprofile.DefaultManagedContractExecutablePath(),
	}
	if previous, previousErr := readInstallMetadata(); previousErr == nil {
		contractRuntime.Version = strings.TrimSpace(previous.AgentPackContractVersion)
		if value := strings.TrimSpace(previous.AgentPackContractPath); value != "" {
			contractRuntime.Executable = value
		}
	}
	opts.managedAuthoringState = agentprofile.ReadyManagedAuthoringState()
	preparedContractRuntime, err := installManagedContractRuntime(ctx)
	if err != nil {
		opts.managedAuthoringState.AgentPackContractStatus = agentprofile.ManagedAuthoringStatusDegraded
		opts.managedAuthoringState.LocalExpertAuthoringStatus = agentprofile.ManagedAuthoringStatusDegraded
		opts.installWarnings = append(opts.installWarnings, fmt.Sprintf(
			"Agent Pack Contract preparation failed; amux binaries were updated but local expert authoring is degraded: %v",
			err,
		))
	} else {
		contractRuntime = preparedContractRuntime
		if err := prepareManagedLocalLibrary(ctx); err != nil {
			opts.managedAuthoringState.LocalExpertAuthoringStatus = agentprofile.ManagedAuthoringStatusDegraded
			opts.installWarnings = append(opts.installWarnings, fmt.Sprintf(
				"Managed local expert library preparation failed; amux binaries were updated but local expert authoring is degraded: %v",
				err,
			))
		}
	}
	if err := agentprofile.SaveDefaultManagedAuthoringState(opts.managedAuthoringState); err != nil {
		return fmt.Errorf("record managed local expert authoring state: %w", err)
	}
	if err := replaceInstallBinaries(sourceDir, binDir, os.Rename); err != nil {
		return err
	}
	meta := installMetadata{
		Version:                    installVersion(opts),
		BinDir:                     binDir,
		SourceDir:                  sourceDir,
		InstalledAt:                time.Now().UTC().Format(time.RFC3339),
		Binaries:                   append([]string(nil), installBinaryNames...),
		AgentPackContractVersion:   contractRuntime.Version,
		AgentPackContractPath:      contractRuntime.Executable,
		AgentPackContractStatus:    opts.managedAuthoringState.AgentPackContractStatus,
		LocalExpertAuthoringStatus: opts.managedAuthoringState.LocalExpertAuthoringStatus,
	}
	if err := writeInstallMetadata(meta); err != nil {
		return err
	}
	if opts.updateShell {
		profile, err := ensureShellPathBlock(opts.shellProfile, binDir)
		if err != nil {
			return err
		}
		opts.appliedShellProfile = profile
	}
	return nil
}

func installVersion(opts *installOptions) string {
	if opts != nil {
		if v := strings.TrimSpace(opts.installedVersion); v != "" {
			return v
		}
	}
	return version
}

func effectiveInstallBinDir(binDir string) string {
	if strings.TrimSpace(binDir) != "" {
		return binDir
	}
	return defaultInstallBinDir()
}

func printUpdateStart(out io.Writer, opts *installOptions) {
	fmt.Fprintln(out, "Updating amux...")
	if opts == nil {
		return
	}
	if strings.TrimSpace(opts.sourceDir) != "" {
		fmt.Fprintf(out, "  source: %s\n", opts.sourceDir)
	} else {
		fmt.Fprintf(out, "  source: release (%s)\n", opts.scmLatestURL)
	}
	fmt.Fprintf(out, "  destination: %s\n", effectiveInstallBinDir(opts.binDir))
}

func printUpdateSummary(out io.Writer, opts *installOptions) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Update summary:")
	fmt.Fprintf(out, "  binaries: updated (%s)\n", strings.Join(installBinaryNames, ", "))
	printManagedAuthoringSummary(out, opts)
	if opts != nil && (opts.restart || opts.restartSessions) {
		fmt.Fprintln(out, "  services: refreshed/started local amux system services")
	} else {
		fmt.Fprintln(out, "  services: not restarted (use --restart)")
	}
	if opts != nil && opts.restartSessions {
		fmt.Fprintln(out, "  sessions: restarted running local user sessions")
	} else {
		fmt.Fprintln(out, "  sessions: not restarted (use --restart-sessions)")
	}
	if opts != nil && opts.restartLocalServices {
		fmt.Fprintln(out, "  local_services: restarted registered mcp/gui local servers")
	} else {
		fmt.Fprintln(out, "  local_services: not restarted (use --restart-local-services)")
	}
	if opts == nil || !opts.restartAll {
		fmt.Fprintln(out, "  all local restartable: use --restart-all")
	}
}

func printInstallWarnings(out io.Writer, opts *installOptions) {
	if opts == nil {
		return
	}
	for _, warning := range opts.installWarnings {
		fmt.Fprintf(out, "Warning: %s\n", warning)
	}
}

func printManagedAuthoringSummary(out io.Writer, opts *installOptions) {
	contractStatus := agentprofile.ManagedAuthoringStatus("")
	authoringStatus := agentprofile.ManagedAuthoringStatus("")
	contractVersion := ""
	contractPath := agentprofile.DefaultManagedContractExecutablePath()
	if opts != nil {
		contractStatus = opts.managedAuthoringState.AgentPackContractStatus
		authoringStatus = opts.managedAuthoringState.LocalExpertAuthoringStatus
	}
	meta, err := readInstallMetadata()
	if err == nil {
		if contractStatus == "" {
			contractStatus = meta.AgentPackContractStatus
		}
		if authoringStatus == "" {
			authoringStatus = meta.LocalExpertAuthoringStatus
		}
		contractVersion = strings.TrimSpace(meta.AgentPackContractVersion)
		if value := strings.TrimSpace(meta.AgentPackContractPath); value != "" {
			contractPath = value
		}
	}
	if contractStatus == "" && err == nil && meta.AgentPackContractVersion != "" && meta.AgentPackContractPath != "" {
		contractStatus = agentprofile.ManagedAuthoringStatusReady
	}
	if authoringStatus == "" && contractStatus == agentprofile.ManagedAuthoringStatusReady {
		authoringStatus = agentprofile.ManagedAuthoringStatusReady
	}

	if contractStatus == agentprofile.ManagedAuthoringStatusDegraded {
		fmt.Fprintln(out, "  agent_pack_contract: degraded")
	} else if contractVersion != "" {
		fmt.Fprintf(out, "  agent_pack_contract: ready (%s at %s)\n", contractVersion, contractPath)
	} else {
		fmt.Fprintf(out, "  agent_pack_contract: ready (compatible >=%s at %s)\n", agentprofile.ManagedContractMinimumVersion, contractPath)
	}
	if authoringStatus == agentprofile.ManagedAuthoringStatusDegraded {
		fmt.Fprintln(out, "  local_expert_authoring: degraded (run `amux update` to retry)")
	} else {
		fmt.Fprintln(out, "  local_expert_authoring: ready")
	}
}

func updatePostInstallRequiresInstalledBinary(opts *installOptions) bool {
	return opts != nil && (opts.restart || opts.restartSessions || opts.restartLocalServices)
}

func completeUpdatePostInstall(cmd *cobra.Command, opts *installOptions, nextSteps installNextStepsContext) error {
	out := cmd.OutOrStdout()
	if opts.restart || opts.restartSessions {
		if err := updateAgentPackAutoSyncRefresher(); err != nil {
			return fmt.Errorf("refresh Agent Pack auto sync: %w", err)
		}
		if err := restartIfRunning(runtimeService()); err != nil {
			return err
		}
		if err := restartIfRunning(hubService()); err != nil {
			return err
		}
		if err := ensureUpdateSystemStarted(cmd); err != nil {
			return err
		}
	}
	if opts.restartSessions {
		result, err := restartLocalUserSessions(cmd.Context())
		if err != nil {
			return err
		}
		printRestartedLocalSessions(out, result)
		if err := result.Err(); err != nil {
			return err
		}
	}
	if opts.restartLocalServices {
		restarted, err := restartTrackedLocalServiceProcesses(5 * time.Second)
		if err != nil {
			return err
		}
		printRestartedLocalServiceProcesses(out, restarted)
	}
	fmt.Fprintf(out, "Updated amux to %s\n", installVersion(opts))
	printUpdateSummary(out, opts)
	printInstallNextSteps(out, opts.binDir, opts.appliedShellProfile, nextSteps)
	return nil
}

func runInstalledUpdatePostInstall(cmd *cobra.Command, opts *installOptions) error {
	binDir := effectiveInstallBinDir(opts.binDir)
	args := []string{
		"update",
		"--post-install",
		"--bin-dir", binDir,
		"--installed-version", installVersion(opts),
	}
	if opts.appliedShellProfile != "" {
		args = append(args, "--applied-shell-profile", opts.appliedShellProfile)
	}
	if opts.restart {
		args = append(args, "--restart")
	}
	if opts.restartSessions {
		args = append(args, "--restart-sessions")
	}
	if opts.restartLocalServices {
		args = append(args, "--restart-local-services")
	}
	if opts.restartAll {
		args = append(args, "--restart-all")
	}
	binary := filepath.Join(binDir, "amux")
	return updatePostInstallRunner(cmd, binary, args)
}

func runUpdatePostInstallCommand(cmd *cobra.Command, binary string, args []string) error {
	child := exec.CommandContext(cmd.Context(), binary, args...)
	child.Stdin = os.Stdin
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	child.Env = os.Environ()
	if err := child.Run(); err != nil {
		return fmt.Errorf("complete update with installed amux %s: %w", binary, err)
	}
	return nil
}

func runUninstall(binDir string, purge bool) error {
	if err := uninstallServiceStopper(); err != nil {
		return fmt.Errorf("stop services before uninstall: %w", err)
	}
	for _, name := range installBinaryNames {
		if err := os.Remove(filepath.Join(binDir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := removeShellPathBlocks(); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(defaultAmuxDir(), "install.json"))
	if purge {
		return os.RemoveAll(defaultAmuxDir())
	}
	if err := agentprofile.RemoveDefaultManagedContractRuntime(); err != nil {
		return fmt.Errorf("remove managed Agent Pack Contract runtime: %w", err)
	}
	return nil
}

func stopServicesForUninstall() error {
	return stopServicesForUninstallWithHooks(defaultLegacySystemMutationHooks(), restartTrackedLocalServiceProcesses)
}

func stopServicesForUninstallWithHooks(hooks legacySystemMutationHooks, stopLocalServices func(time.Duration) ([]trackedProcess, error)) error {
	manageHub, err := resolveLegacySystemTopology(hooks)
	if err != nil {
		return err
	}
	if err := hooks.stopService(hooks.autoSyncService()); err != nil {
		return fmt.Errorf("stop Agent Pack auto sync: %w", err)
	}
	if err := stopLegacySystemServices(manageHub, hooks); err != nil {
		return err
	}
	// This helper asks registered standalone MCP/GUI processes to exit; their
	// external hosts own relaunch. Session-owned processes stop with the runtime.
	if _, err := stopLocalServices(serviceStopGracePeriod); err != nil {
		return fmt.Errorf("stop local services: %w", err)
	}
	return nil
}

func restartIfRunning(svc managedService) error {
	if _, ok := readRunningPID(svc.PIDPath); !ok {
		return nil
	}
	return svc.Restart(false)
}

func ensureUpdateSystemStarted(cmd *cobra.Command) error {
	return runJournaledSystemChange(cmd, updateSystemLifecycleOpener, system.ChangeRequest{
		Action: system.ActionStart,
		Scope:  system.CurrentScope(),
	}, false)
}

func normalizeUpdateRestartOptions(opts *installOptions) {
	if opts == nil || !opts.restartAll {
		return
	}
	opts.restartSessions = true
	opts.restartLocalServices = true
}

func validateInstallSource(sourceDir string) error {
	for _, name := range installBinaryNames {
		path := filepath.Join(sourceDir, name)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("missing %s in %s: %w", name, sourceDir, err)
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", path)
		}
	}
	return nil
}

func defaultUpdateSCMLatestURL() string {
	return updatecheck.LatestURL()
}

func validateExpectedUpdateVersion(expectedVersion, releaseVersion string) error {
	expectedVersion = strings.TrimSpace(expectedVersion)
	if expectedVersion == "" {
		return nil
	}
	releaseVersion = strings.TrimSpace(releaseVersion)
	if releaseVersion != expectedVersion {
		return fmt.Errorf(
			"downloaded amux version changed from expected %q to %q; check for updates again",
			expectedVersion,
			releaseVersion,
		)
	}
	return nil
}

func prepareSCMUpdateSource(latestURL string) (string, string, func(), error) {
	latestURL = strings.TrimSpace(latestURL)
	if latestURL == "" {
		return "", "", nil, fmt.Errorf("SCM latest URL is required")
	}
	tmpDir, err := os.MkdirTemp("", "amux-update-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	fail := func(err error) (string, string, func(), error) {
		cleanup()
		return "", "", nil, err
	}

	latestArchive := filepath.Join(tmpDir, "amux-scm-latest.tar.gz")
	if err := downloadToFile(latestURL, latestArchive); err != nil {
		return fail(err)
	}
	f, err := os.Open(latestArchive)
	if err != nil {
		return fail(err)
	}
	var magic [2]byte
	_, _ = io.ReadFull(f, magic[:])
	_ = f.Close()
	if magic != [2]byte{0x1f, 0x8b} {
		sourceDir, releaseVersion, err := prepareManifestUpdateSource(latestArchive, latestURL, tmpDir)
		if err != nil {
			return fail(err)
		}
		return sourceDir, releaseVersion, cleanup, nil
	}
	latestExtract := filepath.Join(tmpDir, "latest")
	if err := os.MkdirAll(latestExtract, 0755); err != nil {
		return fail(err)
	}
	if err := extractTarGz(latestArchive, latestExtract); err != nil {
		return fail(err)
	}
	platformArchive, releaseVersion, err := findSCMPlatformArchive(latestExtract, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fail(err)
	}
	releaseExtract := filepath.Join(tmpDir, "release")
	if err := os.MkdirAll(releaseExtract, 0755); err != nil {
		return fail(err)
	}
	if err := extractTarGz(platformArchive, releaseExtract); err != nil {
		return fail(err)
	}
	sourceDir, err := findInstallSourceRoot(releaseExtract)
	if err != nil {
		return fail(err)
	}
	if err := validateInstallSource(sourceDir); err != nil {
		return fail(err)
	}
	return sourceDir, releaseVersion, cleanup, nil
}

func downloadToFile(rawURL, dst string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	var in io.ReadCloser
	switch parsed.Scheme {
	case "", "file":
		path := rawURL
		if parsed.Scheme == "file" {
			path = parsed.Path
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		in = f
	case "http", "https":
		resp, err := updateHTTPClient.Get(rawURL)
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return fmt.Errorf("download %s: HTTP %s", rawURL, resp.Status)
		}
		in = resp.Body
	default:
		return fmt.Errorf("unsupported SCM latest URL scheme %q", parsed.Scheme)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func extractTarGz(archivePath, dst string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	cleanDst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." {
			continue
		}
		target := filepath.Join(cleanDst, name)
		if target != cleanDst && !strings.HasPrefix(target, cleanDst+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes destination: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported release archive entry: %s", hdr.Name)
		}
	}
}

func findSCMPlatformArchive(root, goos, goarch string) (string, string, error) {
	key := "_" + goos + "_" + goarch + ".tar.gz"
	var matches []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "amux_") && strings.HasSuffix(base, key) {
			matches = append(matches, path)
		}
		return nil
	}); err != nil {
		return "", "", err
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no amux archive for platform %s_%s", goos, goarch)
	}
	if len(matches) > 1 {
		return "", "", fmt.Errorf("multiple amux archives for platform %s_%s", goos, goarch)
	}
	version := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(matches[0]), "amux_"), key)
	return matches[0], version, nil
}

func findInstallSourceRoot(root string) (string, error) {
	if err := validateInstallSource(root); err == nil {
		return root, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(root, entry.Name()))
		}
	}
	if len(dirs) == 1 {
		return dirs[0], nil
	}
	return root, nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode()|0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, info.Mode()|0755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

type installBinarySwap struct {
	destination   string
	backup        string
	staged        string
	hadOriginal   bool
	originalMoved bool
	newInstalled  bool
}

func validateInstallDestinations(binDir string, force bool) error {
	for _, name := range installBinaryNames {
		dst := filepath.Join(binDir, name)
		info, err := os.Lstat(dst)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", dst)
		}
		if !force {
			return fmt.Errorf("%s already exists; use --force or `amux update`", dst)
		}
	}
	return nil
}

func replaceInstallBinaries(sourceDir, binDir string, rename func(string, string) error) error {
	stageDir, err := os.MkdirTemp(binDir, ".amux-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)

	backupDir, err := os.MkdirTemp(binDir, ".amux-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(backupDir)

	for _, name := range installBinaryNames {
		if err := copyExecutable(filepath.Join(sourceDir, name), filepath.Join(stageDir, name)); err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
	}

	swaps := make([]installBinarySwap, 0, len(installBinaryNames))
	for _, name := range installBinaryNames {
		destination := filepath.Join(binDir, name)
		info, statErr := os.Lstat(destination)
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		if statErr == nil && info.IsDir() {
			return fmt.Errorf("%s is a directory", destination)
		}
		swaps = append(swaps, installBinarySwap{
			destination: destination,
			backup:      filepath.Join(backupDir, name),
			staged:      filepath.Join(stageDir, name),
			hadOriginal: statErr == nil,
		})
	}
	// Public releases carry a platform-matched SDK. Local binary-only updates
	// keep the existing sidecar. Stage it before replacing any installed file.
	sdkSource := filepath.Join(sourceDir, "sidecars", "claude-sdk")
	if _, err := os.Stat(sdkSource); err == nil {
		for _, required := range []string{"dist/index.js", "package.json", "node_modules/@anthropic-ai/claude-agent-sdk"} {
			if _, err := os.Stat(filepath.Join(sdkSource, required)); err != nil {
				return fmt.Errorf("incomplete Claude SDK: %w", err)
			}
		}
		staged := filepath.Join(stageDir, "claude-sdk")
		if err := os.CopyFS(staged, os.DirFS(sdkSource)); err != nil {
			return err
		}
		destination := filepath.Join(defaultAmuxDir(), "sidecars", "claude-sdk")
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		_, statErr := os.Lstat(destination)
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		swaps = append(swaps, installBinarySwap{
			destination: destination, backup: filepath.Join(backupDir, "claude-sdk"),
			staged: staged, hadOriginal: statErr == nil,
		})
	} else if !os.IsNotExist(err) {
		return err
	}

	for i := range swaps {
		swap := &swaps[i]
		if swap.hadOriginal {
			if err := rename(swap.destination, swap.backup); err != nil {
				rollbackErr := rollbackInstallBinaries(swaps[:i], rename)
				return errors.Join(fmt.Errorf("back up %s: %w", filepath.Base(swap.destination), err), rollbackErr)
			}
			swap.originalMoved = true
		}
		if err := rename(swap.staged, swap.destination); err != nil {
			rollbackErr := rollbackInstallBinaries(swaps[:i+1], rename)
			return errors.Join(fmt.Errorf("activate %s: %w", filepath.Base(swap.destination), err), rollbackErr)
		}
		swap.newInstalled = true
	}
	return nil
}

func rollbackInstallBinaries(swaps []installBinarySwap, rename func(string, string) error) error {
	var rollbackErrors []error
	for i := len(swaps) - 1; i >= 0; i-- {
		swap := swaps[i]
		if swap.newInstalled {
			if err := os.RemoveAll(swap.destination); err != nil && !os.IsNotExist(err) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new %s: %w", filepath.Base(swap.destination), err))
			}
		}
		if swap.originalMoved {
			if err := rename(swap.backup, swap.destination); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s: %w", filepath.Base(swap.destination), err))
			}
		}
	}
	return errors.Join(rollbackErrors...)
}

func writeInstallMetadata(meta installMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(defaultAmuxDir(), "install.json"), data, 0644)
}

func readInstallMetadata() (installMetadata, error) {
	data, err := os.ReadFile(filepath.Join(defaultAmuxDir(), "install.json"))
	if err != nil {
		return installMetadata{}, err
	}
	var meta installMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return installMetadata{}, err
	}
	return meta, nil
}

func defaultInstallBinDir() string {
	return filepath.Join(defaultAmuxDir(), "bin")
}

func currentBinaryDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func currentInstallNextStepsContext() installNextStepsContext {
	if fileExists(defaultConfigPath()) {
		return installNextStepsConfigured
	}
	return installNextStepsFresh
}

func printInstallNextSteps(out io.Writer, binDir, shellProfile string, nextSteps installNextStepsContext) {
	if binDir == "" {
		binDir = defaultInstallBinDir()
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Next:")
	if shellProfile != "" {
		fmt.Fprintln(out, "  1. Reload your shell profile:")
		fmt.Fprintf(out, "     source %s\n", shellProfile)
	} else if !pathContains(binDir, os.Getenv("PATH")) {
		fmt.Fprintln(out, "  1. Add amux to PATH for this shell:")
		fmt.Fprintf(out, "     export PATH=%q:$PATH\n", binDir)
	} else {
		fmt.Fprintln(out, "  1. amux is already on PATH.")
	}
	if nextSteps == installNextStepsConfigured {
		fmt.Fprintln(out, "  2. Verify the existing installation:")
		fmt.Fprintln(out, "     amux system status")
		return
	}
	fmt.Fprintln(out, "  2. Set up the local installation:")
	fmt.Fprintln(out, "     amux setup")
	fmt.Fprintln(out, "  3. Verify the installation:")
	fmt.Fprintln(out, "     amux system status")
	fmt.Fprintln(out, "  4. Start your first session:")
	fmt.Fprintf(out, "     amux session run %s -n work\n", agentprofile.DefaultProfileRef)
}

func pathContains(dir, pathValue string) bool {
	for _, entry := range filepath.SplitList(pathValue) {
		if entry == dir {
			return true
		}
	}
	return false
}

const (
	shellPathStart = "# >>> amux path >>>"
	shellPathEnd   = "# <<< amux path <<<"
)

func ensureShellPathBlock(profile, binDir string) (string, error) {
	if profile != "" {
		profile = expandHomePath(profile)
		return profile, ensureShellPathBlockInFile(profile, binDir)
	}
	profiles, err := defaultShellProfiles()
	if err != nil {
		return "", err
	}
	for _, candidate := range profiles {
		if err := ensureShellPathBlockInFile(candidate, binDir); err != nil {
			return "", err
		}
	}
	if len(profiles) == 0 {
		return "", nil
	}
	return profiles[0], nil
}

func ensureShellPathBlockInFile(path, binDir string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(data)
	if strings.Contains(text, shellPathStart) || strings.Contains(text, shellPathLine(binDir)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(text) > 0 && !strings.HasSuffix(text, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	if len(text) > 0 {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(shellPathBlock(binDir))
	return err
}

func removeShellPathBlocks() error {
	for _, profile := range candidateShellProfiles() {
		if err := removeShellPathBlockFromFile(profile); err != nil {
			return err
		}
	}
	return nil
}

func removeShellPathBlockFromFile(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	text := string(data)
	changed := false
	for {
		start := strings.Index(text, shellPathStart)
		if start < 0 {
			break
		}
		end := strings.Index(text[start:], shellPathEnd)
		if end < 0 {
			break
		}
		end += start + len(shellPathEnd)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		text = text[:start] + text[end:]
		changed = true
	}
	if !changed {
		return nil
	}
	text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	return os.WriteFile(path, []byte(text), 0644)
}

func defaultShellProfiles() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	shell := filepath.Base(os.Getenv("SHELL"))
	preferred := filepath.Join(home, ".zshrc")
	switch shell {
	case "bash":
		preferred = filepath.Join(home, ".bashrc")
	case "zsh", "":
		preferred = filepath.Join(home, ".zshrc")
	}
	profiles := []string{preferred}
	seen := map[string]bool{preferred: true}
	for _, candidate := range candidateShellProfiles() {
		if candidate == "" || seen[candidate] {
			continue
		}
		profiles = append(profiles, candidate)
		seen[candidate] = true
	}
	return profiles, nil
}

func candidateShellProfiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".profile"),
	}
}

func shellPathBlock(binDir string) string {
	return fmt.Sprintf("%s\n%s\n%s\n", shellPathStart, shellPathLine(binDir), shellPathEnd)
}

func shellPathLine(binDir string) string {
	if binDir == defaultInstallBinDir() {
		return `export PATH="$HOME/.amux/bin:$PATH"`
	}
	return fmt.Sprintf("export PATH=%q:$PATH", binDir)
}

func expandHomePath(path string) string {
	if path == "~" {
		return homeDirOr(path)
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDirOr("~"), path[2:])
	}
	return path
}

func homeDirOr(fallback string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback
	}
	return home
}
