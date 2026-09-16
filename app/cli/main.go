package cli

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/amuxos/amux/internal/system"
	"github.com/spf13/cobra"
)

var version = "dev"

var rootCommandBuilder = newRootCmd

type ExitCoder interface {
	ExitCode() int
}

func run(args []string, stderr io.Writer) int {
	root := rootCommandBuilder()
	root.SetArgs(args)
	root.SetErr(stderr)
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.InitDefaultCompletionCmd()
	if !isShellCompletionRequest(root, args) {
		if err := commandResolutionError(root, args); err != nil {
			fmt.Fprintln(stderr, err)
			return exitCodeFor(err)
		}
	}
	if err := root.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		return exitCodeFor(err)
	}
	return 0
}

func isShellCompletionRequest(root *cobra.Command, args []string) bool {
	token, ok := firstCobraCommandToken(root, args)
	return ok && (token == cobra.ShellCompRequestCmd || token == cobra.ShellCompNoDescRequestCmd)
}

// firstCobraCommandToken mirrors Cobra's root-command flag stripping closely
// enough to recognize its lazily installed shell-completion request command.
func firstCobraCommandToken(root *cobra.Command, args []string) (string, bool) {
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()
	flags := root.LocalFlags()

	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			return "", false
		case strings.HasPrefix(arg, "--"):
			if !strings.Contains(arg, "=") {
				flag := flags.Lookup(strings.TrimPrefix(arg, "--"))
				if flag == nil || flag.NoOptDefVal == "" {
					index++
				}
			}
		case strings.HasPrefix(arg, "-"):
			if !strings.Contains(arg, "=") && len(arg) == 2 {
				flag := flags.ShorthandLookup(arg[1:])
				if flag == nil || flag.NoOptDefVal == "" {
					index++
				}
			}
		case arg != "":
			return arg, true
		}
	}
	return "", false
}

func commandResolutionError(root *cobra.Command, args []string) error {
	root.InitDefaultHelpCmd()
	helpCandidate, helpRemaining, helpErr := root.Find([]string{"help"})
	if helpErr != nil {
		return commandValidationError(helpErr)
	}
	var helpCmd *cobra.Command
	if helpCandidate != root && len(helpRemaining) == 0 {
		helpCmd = helpCandidate
	}

	cmd, remaining, err := root.Find(args)
	if err != nil {
		return commandValidationError(err)
	}
	if helpCmd != nil && cmd == helpCmd {
		return helpTopicResolutionError(root, remaining)
	}
	if cmd != nil && !cmd.Runnable() && cmd.HasSubCommands() {
		return unresolvedCommandArgumentError(cmd, remaining)
	}
	return nil
}

func helpTopicResolutionError(root *cobra.Command, topic []string) error {
	if _, ok := firstNonFlagArgument(topic); !ok {
		return nil
	}
	cmd, remaining, err := root.Find(topic)
	if err != nil {
		return commandValidationError(err)
	}
	return unresolvedCommandArgumentError(cmd, remaining)
}

func unresolvedCommandArgumentError(cmd *cobra.Command, remaining []string) error {
	argument, ok := firstNonFlagArgument(remaining)
	if !ok {
		return nil
	}
	return commandValidationError(cobra.NoArgs(cmd, []string{argument}))
}

func firstNonFlagArgument(args []string) (string, bool) {
	positional := false
	for _, arg := range args {
		if positional || !strings.HasPrefix(arg, "-") {
			return arg, true
		}
		if arg == "--" {
			positional = true
		}
	}
	return "", false
}

func exitCodeFor(err error) int {
	var coded ExitCoder
	if errors.As(err, &coded) {
		if code := coded.ExitCode(); code > 0 {
			return code
		}
	}
	return 1
}

func newRootCmd() *cobra.Command {
	return newRootCmdForPlatform(runtime.GOOS)
}

func newRootCmdForPlatform(goos string) *cobra.Command {
	root := &cobra.Command{
		Use:               "amux",
		Short:             "Unified manager for code agents across runtimes",
		Version:           version,
		PersistentPreRunE: validateJSONOutputSupport,
	}
	root.PersistentFlags().Bool("json", false, "output stable JSON")

	root.SetVersionTemplate("amux {{.Version}}\n")
	root.AddGroup(
		&cobra.Group{ID: "work", Title: "Work:"},
		&cobra.Group{ID: "control", Title: "Control:"},
	)
	root.SetHelpCommandGroupID("control")
	root.SetCompletionCommandGroupID("control")

	setupCmd := newSetupCmd()
	setupCmd.GroupID = "control"
	systemCmd := newSystemCmd()
	systemCmd.GroupID = "control"

	installCmd := newInstallCmd()
	updateCmd := newUpdateCmd()
	updateCmd.Hidden = true
	agentCmd := newAgentCmd()
	agentCmd.GroupID = "work"
	sessionCmd := newSessionCmd()
	sessionCmd.GroupID = "work"
	scheduleCmd := newScheduleCmd()
	scheduleCmd.GroupID = "work"
	bridgeCmd := newBridgeCmd()
	bridgeCmd.Hidden = true
	guiCmd := newGuiCmd()
	guiCmd.Hidden = true
	taskCmd := newTaskCmd()
	taskCmd.GroupID = "work"
	mcpCmd := newMcpCmd()
	mcpCmd.Hidden = true
	isolationCmd := newIsolationCmdForPlatform(goos)
	isolationCmd.GroupID = "control"

	root.AddCommand(
		setupCmd,
		systemCmd,
		installCmd,
		updateCmd,
		agentCmd,
		sessionCmd,
		scheduleCmd,
		taskCmd,
		mcpCmd,
		isolationCmd,
		bridgeCmd,
		guiCmd,
	)
	root.InitDefaultCompletionCmd()
	installCommandValidation(root)
	return root
}

func installCommandValidation(cmd *cobra.Command) {
	if cmd.Args != nil {
		validateArgs := cmd.Args
		cmd.Args = func(cmd *cobra.Command, args []string) error {
			return commandValidationError(validateArgs(cmd, args))
		}
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return commandValidationError(err)
	})
	for _, child := range cmd.Commands() {
		installCommandValidation(child)
	}
}

func commandValidationError(err error) error {
	if err == nil {
		return nil
	}
	var validation *system.ValidationError
	if errors.As(err, &validation) {
		return err
	}
	return &system.ValidationError{Err: err}
}
