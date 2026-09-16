package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/system"
	"github.com/spf13/cobra"
)

const defaultLocalHubAddr = "127.0.0.1:7100"

type initOptions struct {
	profile      string
	agent        string
	hubAddr      string
	noStart      bool
	smoke        bool
	smokeTimeout int
}

var runSetupAgentSmoke = func(ctx context.Context, out io.Writer, profile agentprofile.Profile, timeoutSeconds int) error {
	if err := runAgentProfileDoctor(ctx, out, profile); err != nil {
		return err
	}
	return runAgentProfileSmoke(ctx, out, profile, timeoutSeconds)
}

func newSetupCmd() *cobra.Command {
	return newSetupCmdWithOpener(openSystemLifecycle)
}

func newSetupCmdWithOpener(open systemLifecycleOpener) *cobra.Command {
	return newSetupLikeCmd("setup [local|hub|remote|feishu|feishu-local]", "Set up this amux installation", open)
}

func newSetupLikeCmd(use, short string, open systemLifecycleOpener) *cobra.Command {
	opts := initOptions{}
	c := &cobra.Command{
		Use:               use,
		Short:             short,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeInitArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.profile = "local"
			if len(args) > 0 {
				opts.profile = args[0]
			}
			return runSetupChangeWithOpener(cmd, opts, open)
		},
	}
	c.Flags().StringVar(&opts.agent, "agent", agentprofile.DefaultProfileRef, "agent profile for feishu-local")
	c.Flags().StringVar(&opts.hubAddr, "hub", "", "hub address for remote profile")
	c.Flags().BoolVar(&opts.noStart, "no-start", false, "write config only; do not start runtime or hub")
	c.Flags().BoolVar(&opts.smoke, "smoke", false, "verify the default profile with one real task turn after setup")
	c.Flags().IntVar(&opts.smokeTimeout, "smoke-timeout", 120, "setup smoke turn timeout in seconds")
	return c
}

func runSetupChangeWithOpener(cmd *cobra.Command, opts initOptions, open systemLifecycleOpener) error {
	validated, selectedProfile, err := validateInitOptions(opts)
	if err != nil {
		return &system.ValidationError{Err: err}
	}
	opts = validated
	if _, err := prepareInitConfig(opts, selectedProfile); err != nil {
		return err
	}
	setup := &system.SetupSpec{
		Profile: opts.profile,
		NoStart: opts.noStart,
	}
	if opts.profile == "feishu-local" {
		setup.Agent = opts.agent
	}
	if opts.profile == "remote" {
		setup.HubAddress = opts.hubAddr
	}
	request := system.ChangeRequest{
		Action: system.ActionSetup,
		Scope:  system.CurrentScope(),
		Setup:  setup,
	}
	if err := runJournaledSystemChange(cmd, open, request, false); err != nil {
		return err
	}
	if !opts.smoke {
		return nil
	}
	if selectedProfile.Ref() == "" {
		selectedProfile, err = resolveAgentProfile(agentprofile.DefaultProfileRef)
		if err != nil {
			return fmt.Errorf("setup completed but resolve default profile for smoke: %w", err)
		}
	}
	if err := runSetupAgentSmoke(cmd.Context(), cmd.OutOrStdout(), selectedProfile, opts.smokeTimeout); err != nil {
		return fmt.Errorf("setup completed but default profile is not ready: %w", err)
	}
	return nil
}

func completeInitArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return []string{"local", "hub", "remote", "feishu", "feishu-local"}, cobra.ShellCompDirectiveNoFileComp
}

func runInit(out io.Writer, opts initOptions) error {
	return runInitWithHooks(out, opts, defaultLegacySystemMutationHooks())
}

func runInitWithHooks(out io.Writer, opts initOptions, hooks legacySystemMutationHooks) error {
	validated, selectedProfile, err := validateInitOptions(opts)
	if err != nil {
		return &system.ValidationError{Err: err}
	}
	opts = validated
	profile := opts.profile

	if err := config.UpdateConfigFile(defaultConfigPath(), func(raw map[string]any) error {
		return prepareInitConfigValues(cliConfigFile(raw), opts, selectedProfile)
	}); err != nil {
		return err
	}

	if !opts.noStart {
		manageHub := profile != "remote"
		if err := startLegacySystemServices(manageHub, hooks); err != nil {
			return err
		}
	}

	printInitSummary(out, profile, opts.noStart, selectedProfile)
	return nil
}

func prepareInitConfig(opts initOptions, selectedProfile agentprofile.Profile) (cliConfigFile, error) {
	cfg, err := readCLIConfigFile()
	if err != nil {
		return nil, err
	}
	if err := prepareInitConfigValues(cfg, opts, selectedProfile); err != nil {
		return nil, err
	}
	return cfg, nil
}

func prepareInitConfigValues(cfg cliConfigFile, opts initOptions, selectedProfile agentprofile.Profile) error {
	if err := ensureBaseInitConfig(cfg); err != nil {
		return err
	}
	switch opts.profile {
	case "local":
		if err := setConfigValue(cfg, "hub.addr", defaultLocalHubAddr); err != nil {
			return err
		}
	case "hub", "feishu", "feishu-local":
		if err := setIfConfigUnset(cfg, "hub.addr", defaultLocalHubAddr); err != nil {
			return err
		}
	case "remote":
		if err := setConfigValue(cfg, "hub.addr", opts.hubAddr); err != nil {
			return err
		}
	}
	if opts.profile == "feishu-local" {
		if err := setConfigValue(cfg, "bridge.feishu.workspace_defaults.agent", selectedProfile.Ref()); err != nil {
			return err
		}
	}
	hubAddress, _ := configValue(cfg, "hub.addr")
	if err := validateHubAddress(hubAddress); err != nil {
		return err
	}
	return nil
}

func validateInitOptions(opts initOptions) (initOptions, agentprofile.Profile, error) {
	opts.profile = strings.TrimSpace(opts.profile)
	if opts.profile == "" {
		opts.profile = "local"
	}
	opts.agent = strings.TrimSpace(opts.agent)
	opts.hubAddr = strings.TrimSpace(opts.hubAddr)
	if opts.smoke && opts.noStart {
		return initOptions{}, agentprofile.Profile{}, fmt.Errorf("--smoke cannot be used with --no-start")
	}
	if opts.smoke && opts.smokeTimeout <= 0 {
		return initOptions{}, agentprofile.Profile{}, fmt.Errorf("--smoke-timeout must be greater than zero")
	}

	switch opts.profile {
	case "local", "hub", "feishu":
		return opts, agentprofile.Profile{}, nil
	case "remote":
		if opts.hubAddr == "" {
			return initOptions{}, agentprofile.Profile{}, fmt.Errorf("amux setup remote requires --hub <host:port>")
		}
		if err := validateHubAddress(opts.hubAddr); err != nil {
			return initOptions{}, agentprofile.Profile{}, err
		}
		return opts, agentprofile.Profile{}, nil
	case "feishu-local":
		selectedProfile, err := initBuiltinAgentProfile(opts.agent)
		if err != nil {
			return initOptions{}, agentprofile.Profile{}, err
		}
		return opts, selectedProfile, nil
	default:
		return initOptions{}, agentprofile.Profile{}, fmt.Errorf("unknown init profile %q (expected local|hub|remote|feishu|feishu-local)", opts.profile)
	}
}

func initBuiltinAgentProfile(ref string) (agentprofile.Profile, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = agentprofile.DefaultProfileRef
	}
	profile, ok := agentprofile.ResolveBuiltin(ref)
	if !ok {
		return agentprofile.Profile{}, fmt.Errorf("unknown builtin agent profile %q", ref)
	}
	return profile, nil
}

func ensureBaseInitConfig(cfg cliConfigFile) error {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "local"
	}
	for _, kv := range [][2]string{
		{"runtime.id", hostname},
		{"runtime.name", hostname},
		{"runtime.auto_start", "false"},
		{"cli.default_target", "local"},
		{"bridge.feishu.workspace_defaults.agent", agentprofile.DefaultProfileRef},
		{"bridge.feishu.workspace_defaults.target", ""},
		{"bridge.feishu.workspace_defaults.cwd", ""},
		{"bridge.feishu.workspace_defaults.model", ""},
		{"bridge.feishu.workspace_defaults.args", ""},
	} {
		if err := setIfConfigUnset(cfg, kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

func setIfConfigUnset(cfg cliConfigFile, key, value string) error {
	if current, ok := configValue(cfg, key); ok && strings.TrimSpace(current) != "" {
		return nil
	}
	return setConfigValue(cfg, key, value)
}

func startIfNotRunning(svc managedService) error {
	if _, ok := readRunningPID(svc.PIDPath); ok {
		return nil
	}
	if err := svc.Start(false); err != nil && !isAlreadyRunning(err) {
		return err
	}
	return nil
}

func printInitSummary(out io.Writer, profile string, noStart bool, selectedProfile agentprofile.Profile) {
	fmt.Fprintf(out, "Initialized amux %s profile\n", profile)
	if noStart {
		fmt.Fprintln(out, "Services were not started because --no-start was set.")
		fmt.Fprintln(out, "Next: amux system start")
	}
	fmt.Fprintln(out, "Check: amux system status")
	switch profile {
	case "local":
		fmt.Fprintf(out, "Next: amux session run %s -n work\n", agentprofile.DefaultProfileRef)
		fmt.Fprintf(out, "Verify: amux agent doctor %s --smoke\n", agentprofile.DefaultProfileRef)
	case "hub":
		fmt.Fprintln(out, "Next: on each worker, run `amux setup remote --hub <host:port>`.")
		fmt.Fprintln(out, "Then: amux system target list")
	case "remote":
		fmt.Fprintln(out, "Next on the control: amux system target list")
	case "feishu":
		fmt.Fprintln(out, "Next: amux bridge feishu setup")
	case "feishu-local":
		fmt.Fprintf(out, "Default Feishu agent: %s (%s)\n", selectedProfile.Ref(), selectedProfile.Description)
		fmt.Fprintln(out, "Next: amux bridge feishu setup")
	}
}
