package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/system"
	"github.com/spf13/cobra"
)

func TestRunReturnsTypedExitCodesThroughWrappedAndJoinedErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantText string
	}{
		{
			name:     "execution failure",
			err:      fmt.Errorf("system operation op-failed failed: %w", errors.New("runtime start failed")),
			wantCode: 1,
			wantText: "op-failed",
		},
		{
			name: "validation",
			err: errors.Join(
				errors.New("close journal failed"),
				fmt.Errorf("plan system change: %w", &system.ValidationError{Err: errors.New("invalid setup")}),
			),
			wantCode: 2,
			wantText: "invalid setup",
		},
		{
			name:     "active operation conflict",
			err:      fmt.Errorf("apply system change: %w", &system.ConflictError{ExistingOperationID: "op-active"}),
			wantCode: 3,
			wantText: "op-active",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := rootCommandBuilder
			rootCommandBuilder = func() *cobra.Command {
				return &cobra.Command{
					Use:  "amux <trigger>",
					Args: cobra.ExactArgs(1),
					RunE: func(cmd *cobra.Command, args []string) error {
						if args[0] != "trigger" {
							t.Fatalf("command args = %v, want [trigger]", args)
						}
						fmt.Fprintln(cmd.ErrOrStderr(), "command diagnostic")
						return tt.err
					},
				}
			}
			t.Cleanup(func() { rootCommandBuilder = original })

			var stderr bytes.Buffer
			if got := run([]string{"trigger"}, &stderr); got != tt.wantCode {
				t.Fatalf("run() exit code = %d, want %d; stderr:\n%s", got, tt.wantCode, stderr.String())
			}
			for _, want := range []string{"command diagnostic", tt.wantText} {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("stderr = %q, want %q", stderr.String(), want)
				}
			}
			if strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr contains usage noise:\n%s", stderr.String())
			}
		})
	}
}

func TestRunReturnsValidationExitCodeForRealArgumentAndFlagErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantText string
	}{
		{name: "extra argument", args: []string{"system", "status", "extra"}, wantText: "unknown command"},
		{name: "unknown flag", args: []string{"system", "start", "--unknown"}, wantText: "unknown flag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := run(tt.args, &stderr); got != 2 {
				t.Fatalf("run(%v) exit code = %d, want 2; stderr:\n%s", tt.args, got, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantText) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantText)
			}
			if strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr contains usage noise:\n%s", stderr.String())
			}
		})
	}
}

func TestRunReturnsValidationExitCodeForInvalidConfigValues(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		configText string
		wantText   string
	}{
		{name: "unsupported key", args: []string{"system", "config", "set", "unknown.key", "value"}, wantText: "unsupported config key"},
		{name: "invalid bool", args: []string{"system", "config", "set", "runtime.auto_start", "sometimes"}, wantText: "must be true or false"},
		{name: "invalid hub address", args: []string{"system", "config", "set", "hub.addr", "missing-port"}, wantText: "host:port"},
		{name: "invalid hub port", args: []string{"system", "config", "set", "hub.addr", "hub.example.com:70000"}, wantText: "port"},
		{name: "malformed TOML", args: []string{"system", "config", "get", "hub.addr"}, configText: "[hub\naddr = broken", wantText: "parse config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
			if tt.configText != "" {
				if err := os.MkdirAll(filepath.Join(home, ".amux"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, ".amux", "config.toml"), []byte(tt.configText), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var stderr bytes.Buffer
			if got := run(tt.args, &stderr); got != 2 {
				t.Fatalf("run(%v) exit code = %d, want 2; stderr:\n%s", tt.args, got, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantText) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantText)
			}
		})
	}
}

func TestRunReturnsValidationExitCodeForUnknownCommandsAndHelpTopics(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "root command", args: []string{"bogus"}},
		{name: "help topic", args: []string{"help", "bogus"}},
		{name: "nested group command", args: []string{"agent", "bogus"}},
		{name: "unsupported system upgrade", args: []string{"system", "upgrade"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := run(tt.args, &stderr); got != 2 {
				t.Fatalf("run(%v) exit code = %d, want 2; stderr:\n%s", tt.args, got, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want command resolution error")
			}
			if strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr contains usage noise:\n%s", stderr.String())
			}
		})
	}
}

func TestRunAllowsValidHelpAndRootInformationFlags(t *testing.T) {
	original := rootCommandBuilder
	rootCommandBuilder = func() *cobra.Command {
		root := newRootCmd()
		root.SetOut(io.Discard)
		return root
	}
	t.Cleanup(func() { rootCommandBuilder = original })

	for _, args := range [][]string{
		{"help", "session", "run"},
		{"--help"},
		{"--version"},
	} {
		var stderr bytes.Buffer
		if got := run(args, &stderr); got != 0 {
			t.Fatalf("run(%v) exit code = %d, want 0; stderr:\n%s", args, got, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("run(%v) stderr = %q, want empty", args, stderr.String())
		}
	}
}

func TestRunAllowsCobraCompletionCommands(t *testing.T) {
	original := rootCommandBuilder
	rootCommandBuilder = func() *cobra.Command {
		root := newRootCmd()
		root.PersistentFlags().String("profile", "", "test profile")
		root.SetOut(io.Discard)
		return root
	}
	t.Cleanup(func() { rootCommandBuilder = original })

	for _, args := range [][]string{
		{"completion", "bash"},
		{"completion", "zsh"},
		{cobra.ShellCompRequestCmd, "system", "st"},
		{cobra.ShellCompNoDescRequestCmd, "system", "st"},
		{"--profile", "local", cobra.ShellCompRequestCmd, "system", "st"},
		{"--profile=local", cobra.ShellCompNoDescRequestCmd, "system", "st"},
	} {
		var stderr bytes.Buffer
		if got := run(args, &stderr); got != 0 {
			t.Fatalf("run(%v) exit code = %d, want 0; stderr:\n%s", args, got, stderr.String())
		}
	}

	for _, shell := range []string{"bash", "zsh"} {
		args := []string{"completion", shell, "extra"}
		var stderr bytes.Buffer
		if got := run(args, &stderr); got != 2 {
			t.Fatalf("run(%v) exit code = %d, want 2; stderr:\n%s", args, got, stderr.String())
		}
	}
}

func TestCommandResolutionAllowsLeafPositionals(t *testing.T) {
	root := newRootCmd()
	args := []string{"session", "run", "builtin/codex", "--name", "work"}
	if err := commandResolutionError(root, args); err != nil {
		t.Fatalf("commandResolutionError(%v) = %v, want nil", args, err)
	}
}

func TestRootCommandUsesPhaseOneGroups(t *testing.T) {
	cmd := newRootCmd()
	if got, want := cmd.Groups(), []*cobra.Group{
		{ID: "work", Title: "Work:"},
		{ID: "control", Title: "Control:"},
	}; len(got) != len(want) {
		t.Fatalf("root groups = %#v, want %#v", got, want)
	} else {
		for i := range want {
			if *got[i] != *want[i] {
				t.Fatalf("root group %d = %#v, want %#v", i, got[i], want[i])
			}
		}
	}

	canonical := map[string]string{
		"session":  "work",
		"task":     "work",
		"agent":    "work",
		"schedule": "work",
		"memory":   "work",
		"setup":    "control",
		"system":   "control",
	}
	for name, group := range canonical {
		found, _, err := cmd.Find([]string{name})
		if err != nil || found.Name() != name {
			t.Fatalf("missing canonical command %s: found=%v err=%v", name, found, err)
		}
		if found.Hidden || found.GroupID != group {
			t.Errorf("canonical command %s hidden/group = %t/%q, want false/%q", name, found.Hidden, found.GroupID, group)
		}
	}
}

func TestRootJSONFlagIsPersistentAndUnsupportedCommandsRejectIt(t *testing.T) {
	root := newRootCmd()
	flag := root.PersistentFlags().Lookup("json")
	if flag == nil {
		t.Fatal("root persistent --json flag is missing")
	}
	if flag.Usage != "output stable JSON" {
		t.Fatalf("--json usage = %q", flag.Usage)
	}
	supported, _, err := root.Find([]string{"agent", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if local := supported.LocalNonPersistentFlags().Lookup("json"); local != nil {
		t.Fatalf("agent list unexpectedly defines a local --json flag: %#v", local)
	}

	root.SetArgs([]string{"session", "attach", "work", "--json"})
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "does not support --json") {
		t.Fatalf("session attach --json error = %v, want unsupported JSON error", err)
	}
}

func newJSONCommandTestRoot(child *cobra.Command) *cobra.Command {
	root := &cobra.Command{
		Use:               "amux",
		PersistentPreRunE: validateJSONOutputSupport,
		SilenceErrors:     true,
		SilenceUsage:      true,
	}
	root.PersistentFlags().Bool("json", false, "output stable JSON")
	root.AddCommand(child)
	return root
}

func TestRemovedLegacyRootCommandSurface(t *testing.T) {
	cmd := newRootCmd()
	for _, name := range []string{
		"runtime", "hub", "service", "client",
		"status", "doctor", "uninstall", "config", "target", "credential", "init",
		"run", "ls", "attach", "send", "interrupt", "sleep", "resume", "stop", "restart",
		"bot", "sandbox", "hook", "kill",
	} {
		assertRootCommandNotRegistered(t, cmd, name)
	}

	for _, name := range []string{"bridge", "mcp", "gui", "install", "update"} {
		found, _, err := cmd.Find([]string{name})
		if err != nil || found.Name() != name {
			t.Fatalf("hidden command %s should remain findable: found=%v err=%v", name, found, err)
		}
		if !found.Hidden {
			t.Errorf("internal command %s should be hidden", name)
		}
	}
}
func TestSystemRejectsUnsupportedAndUnknownChildren(t *testing.T) {
	for _, child := range []string{"upgrade", "unknown-child"} {
		t.Run(child, func(t *testing.T) {
			root := newRootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"system", child})

			err := root.Execute()
			if err == nil {
				t.Fatalf("amux system %s returned nil, want unknown-command error; output:\n%s", child, out.String())
			}
			if !strings.Contains(err.Error(), "unknown command") || !strings.Contains(err.Error(), child) {
				t.Fatalf("amux system %s error = %q, want explicit unknown-command error", child, err)
			}
		})
	}
}

func TestBareSystemShowsHelp(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"system"})

	if err := root.Execute(); err != nil {
		t.Fatalf("amux system: %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "Available Commands:") {
		t.Fatalf("amux system did not show help:\n%s", out.String())
	}
}

func TestSessionCommandsDoNotExposeSandboxFlag(t *testing.T) {
	for name, cmd := range map[string]func() *cobra.Command{
		"attach":    newAttachCmd,
		"interrupt": newInterruptCmd,
		"stop":      newStopCmd,
	} {
		if flag := cmd().Flag("sandbox"); flag != nil {
			t.Fatalf("%s should not expose --sandbox", name)
		}
	}
}

func TestRootHelpIncludesCommandGroups(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	text := out.String()
	for _, want := range []string{"Work:", "Control:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Compatibility:") {
		t.Fatalf("help exposed removed compatibility group:\n%s", text)
	}
	for _, want := range []string{
		"  session ", "  task ", "  agent ", "  schedule ", "  setup ", "  system ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing visible command %q:\n%s", want, text)
		}
	}
	for _, hidden := range []string{
		"  bridge ", "  mcp ", "  gui ", "  install ", "  update ",
		"  runtime ", "  hub ", "  service ", "  status ", "  doctor ", "  uninstall ", "  config ", "  target ", "  credential ", "  init ",
		"  run ", "  ls ", "  attach ", "  send ", "  interrupt ", "  sleep ", "  resume ", "  stop ", "  restart ",
	} {
		if strings.Contains(text, hidden) {
			t.Fatalf("help exposed hidden command %q:\n%s", hidden, text)
		}
	}
}

func TestRootCompletionMatchesVisibleCommandSurface(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"__complete", ""})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("root completion: %v", err)
	}
	text := out.String()
	for _, visible := range []string{
		"session", "task", "agent", "schedule", "memory", "setup", "system",
	} {
		if !rootCompletionContains(text, visible) {
			t.Fatalf("completion missing %q:\n%s", visible, text)
		}
	}
	for _, hidden := range []string{
		"bridge", "mcp", "gui", "install", "update",
		"runtime", "hub", "service", "status", "doctor", "uninstall", "config", "target", "credential", "init",
		"run", "ls", "attach", "send", "interrupt", "sleep", "resume", "stop", "restart",
	} {
		if rootCompletionContains(text, hidden) {
			t.Fatalf("completion exposed hidden command %q:\n%s", hidden, text)
		}
	}
}

func rootCompletionContains(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		command, _, ok := strings.Cut(line, "\t")
		if ok && command == name {
			return true
		}
	}
	return false
}

func TestRootKeepsVersionHelpAndCompletion(t *testing.T) {
	cmd := newRootCmd()
	if cmd.Version != version {
		t.Fatalf("root version = %q, want %q", cmd.Version, version)
	}
	cmd.InitDefaultHelpCmd()
	cmd.InitDefaultCompletionCmd()
	for _, name := range []string{"help", "completion"} {
		found, _, err := cmd.Find([]string{name})
		if err != nil || found.Name() != name {
			t.Fatalf("missing built-in command %s: found=%v err=%v", name, found, err)
		}
		if found.Hidden {
			t.Fatalf("built-in command %s should remain visible", name)
		}
	}
}

func TestSystemLogsShowsHubAndRuntimePathsWithoutOpeningLifecycle(t *testing.T) {
	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	opened := false
	cmd := newSystemCmdWithOpener(func(io.Writer) (system.Lifecycle, func() error, error) {
		opened = true
		return nil, nil, errors.New("logs should not open lifecycle")
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"logs"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("system logs: %v", err)
	}
	if opened {
		t.Fatal("system logs opened the operation journal lifecycle")
	}
	for _, want := range []string{
		"hub: " + filepath.Join(amuxHome, "hub.log"),
		"runtime: " + filepath.Join(amuxHome, "runtime.log"),
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("system logs output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSystemPhaseOneCommandSurfaceHelpAndCompletion(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{
		{"system", "config"},
		{"system", "target", "list"},
		{"system", "logs"},
		{"system", "uninstall"},
	} {
		found, _, err := root.Find(path)
		if err != nil || found.Name() != path[len(path)-1] {
			t.Fatalf("missing %v: found=%v err=%v", path, found, err)
		}
	}
	systemCmd, _, err := root.Find([]string{"system"})
	if err != nil {
		t.Fatalf("find system: %v", err)
	}
	for _, child := range systemCmd.Commands() {
		if child.Name() == "upgrade" {
			t.Fatal("system upgrade should not be registered before the safe upgrade engine")
		}
	}

	var help bytes.Buffer
	root.SetOut(&help)
	root.SetErr(&help)
	root.SetArgs([]string{"system", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("system help: %v", err)
	}
	for _, want := range []string{"  config ", "  target ", "  logs ", "  uninstall "} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("system help missing %q:\n%s", want, help.String())
		}
	}
	if strings.Contains(help.String(), "  upgrade ") {
		t.Fatalf("system help exposed upgrade:\n%s", help.String())
	}

	root = newRootCmd()
	var completion bytes.Buffer
	root.SetOut(&completion)
	root.SetErr(&completion)
	root.SetArgs([]string{"__complete", "system", ""})
	if err := root.Execute(); err != nil {
		t.Fatalf("system completion: %v", err)
	}
	for _, want := range []string{"config", "target", "logs", "uninstall"} {
		if !rootCompletionContains(completion.String(), want) {
			t.Fatalf("system completion missing %q:\n%s", want, completion.String())
		}
	}
	if rootCompletionContains(completion.String(), "upgrade") {
		t.Fatalf("system completion exposed upgrade:\n%s", completion.String())
	}
}

func TestSessionCommandSurface(t *testing.T) {
	cmd := newRootCmd()
	session, _, err := cmd.Find([]string{"session"})
	if err != nil {
		t.Fatalf("find session: %v", err)
	}
	for _, name := range []string{"run", "list", "attach", "send", "interrupt", "sleep", "resume", "stop", "restart", "queue", "prune"} {
		found, _, err := cmd.Find([]string{"session", name})
		if err != nil || found.Name() != name {
			t.Fatalf("missing session command %s: found=%v err=%v", name, found, err)
		}
		if found.Parent() != session {
			t.Fatalf("session command %s parent = %v, want session", name, found.Parent())
		}
		if name == "restart" && found.Flag("running") == nil {
			t.Fatal("session restart should expose --running")
		}
	}

	nestedList, _, err := cmd.Find([]string{"session", "list"})
	if err != nil {
		t.Fatalf("find session list: %v", err)
	}
	if got := nestedList.Use; got != "list [target]" {
		t.Fatalf("session list Use = %q, want %q", got, "list [target]")
	}
}

func TestCanonicalSessionCommandsUseSessionReferences(t *testing.T) {
	cmd := newRootCmd()
	canonicalUses := map[string]string{
		"attach":    "attach <session-ref>",
		"send":      "send <session-ref> <text>",
		"interrupt": "interrupt <session-ref>",
		"sleep":     "sleep <session-ref>",
		"resume":    "resume <session-ref>",
		"stop":      "stop <session-ref>",
		"restart":   "restart <session-ref>",
		"queue":     "queue <session-ref>",
	}
	for name, want := range canonicalUses {
		found, _, err := cmd.Find([]string{"session", name})
		if err != nil {
			t.Fatalf("find session %s: %v", name, err)
		}
		if got := found.Use; got != want {
			t.Errorf("session %s Use = %q, want %q", name, got, want)
		}
	}

}

func assertRootCommandNotRegistered(t *testing.T, root *cobra.Command, name string) {
	t.Helper()
	found, _, err := root.Find([]string{name})
	if err == nil && found != nil && found.Name() == name && found.Parent() == root {
		t.Fatalf("root command %q should not be registered", name)
	}
}

func TestRootHelpUsesRuntimeTerminology(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	text := out.String()
	for _, old := range []string{"sandbox", "orchestrator"} {
		if strings.Contains(strings.ToLower(text), old) {
			t.Fatalf("root help should not mention %q:\n%s", old, text)
		}
	}
}
