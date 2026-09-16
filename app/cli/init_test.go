package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/system"
	"github.com/spf13/cobra"
)

func TestSetupDefaultsToLocalJournalRequest(t *testing.T) {
	useIsolatedInitConfig(t)
	lifecycle := successfulSetupLifecycle()
	cmd := newSetupCmdWithOpener(fixedSystemLifecycleOpener(lifecycle))
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	want := system.ChangeRequest{
		Action: system.ActionSetup,
		Scope:  system.CurrentScope(),
		Setup: &system.SetupSpec{
			Profile: "local",
		},
	}
	if !reflect.DeepEqual(lifecycle.planRequests, []system.ChangeRequest{want}) {
		t.Fatalf("Plan requests = %#v, want %#v", lifecycle.planRequests, []system.ChangeRequest{want})
	}
	if !reflect.DeepEqual(lifecycle.getIDs, []system.OperationID{"op-setup"}) {
		t.Fatalf("GetOperation IDs = %v", lifecycle.getIDs)
	}
	if !strings.Contains(out.String(), "op-setup") {
		t.Fatalf("setup output missing operation ID:\n%s", out.String())
	}
}

func TestSetupRejectsSmokeWithoutStartingServices(t *testing.T) {
	useIsolatedInitConfig(t)
	lifecycle := successfulSetupLifecycle()
	cmd := newSetupCmdWithOpener(fixedSystemLifecycleOpener(lifecycle))
	cmd.SetArgs([]string{"--smoke", "--no-start"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--smoke cannot be used with --no-start") {
		t.Fatalf("setup --smoke --no-start error = %v", err)
	}
	if len(lifecycle.planRequests) != 0 {
		t.Fatalf("invalid setup created lifecycle plan: %+v", lifecycle.planRequests)
	}
}

func TestSetupValidationFailsBeforeOpeningLifecycleOrCreatingOperation(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantText string
	}{
		{name: "unknown profile", args: []string{"setup", "bogus"}, wantText: `unknown init profile "bogus"`},
		{name: "remote without hub", args: []string{"setup", "remote"}, wantText: "requires --hub"},
		{name: "remote with invalid hub", args: []string{"setup", "remote", "--hub", "hub.example.com"}, wantText: "host:port"},
		{name: "remote with invalid port", args: []string{"setup", "remote", "--hub", "hub.example.com:70000"}, wantText: "port"},
		{name: "unknown feishu local agent", args: []string{"setup", "feishu-local", "--agent", "team/coder"}, wantText: "unknown builtin agent profile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			journal, err := system.OpenJournal(filepath.Join(t.TempDir(), "operations.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = journal.Close() })

			var serviceCalls []string
			hooks := fakeLegacySystemMutationHooks(&serviceCalls, defaultLocalHubAddr)
			lifecycle := system.New(system.Dependencies{
				Journal: journal,
				Inspect: func(context.Context, system.Scope) (system.Snapshot, error) {
					return system.Snapshot{State: system.StateStopped}, nil
				},
				Execute: legacySystemExecutor(io.Discard, hooks),
				IDGen:   func() string { return "op-must-not-exist" },
				Now:     func() time.Time { return time.Unix(100, 0).UTC() },
			})
			opened := false
			opener := func(io.Writer) (system.Lifecycle, func() error, error) {
				opened = true
				return lifecycle, func() error { return nil }, nil
			}

			original := rootCommandBuilder
			rootCommandBuilder = func() *cobra.Command {
				root := &cobra.Command{Use: "amux"}
				root.AddCommand(newSetupCmdWithOpener(opener))
				installCommandValidation(root)
				return root
			}
			t.Cleanup(func() { rootCommandBuilder = original })

			var stderr bytes.Buffer
			if got := run(tt.args, &stderr); got != 2 {
				t.Fatalf("run(%v) exit code = %d, want 2; stderr:\n%s", tt.args, got, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantText) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantText)
			}
			if opened {
				t.Fatal("setup validation opened lifecycle")
			}
			operations, err := journal.List(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(operations) != 0 {
				t.Fatalf("journal operations = %#v, want none", operations)
			}
			if len(serviceCalls) != 0 {
				t.Fatalf("service calls = %v, want none", serviceCalls)
			}
		})
	}
}

func TestSetupRejectsMalformedConfigBeforeOpeningLifecycle(t *testing.T) {
	home := t.TempDir()
	amuxHome := filepath.Join(home, ".amux")
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", amuxHome)
	if err := os.MkdirAll(amuxHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(amuxHome, "config.toml"), []byte("[hub\naddr = broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	opened := false
	cmd := newSetupCmdWithOpener(func(io.Writer) (system.Lifecycle, func() error, error) {
		opened = true
		return successfulSetupLifecycle(), func() error { return nil }, nil
	})
	err := cmd.Execute()
	var validation *system.ValidationError
	if !errors.As(err, &validation) || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("setup error = %v, want malformed-config validation", err)
	}
	if opened {
		t.Fatal("malformed config opened setup lifecycle")
	}
	if _, err := os.Stat(filepath.Join(amuxHome, "system", "operations.db")); !os.IsNotExist(err) {
		t.Fatalf("malformed config created operation journal, err=%v", err)
	}
}

func TestInitLocalReplacesExternalHubAddress(t *testing.T) {
	home := useIsolatedInitConfig(t)
	cfg, err := readCLIConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := setConfigValue(cfg, "hub.addr", "hub.example.com:7100"); err != nil {
		t.Fatal(err)
	}
	if err := replaceCLIConfigFileForTest(cfg); err != nil {
		t.Fatal(err)
	}

	cmd := newTestInitCompatCmd()
	cmd.SetArgs([]string{"local", "--no-start"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init local: %v", err)
	}
	config := readTestConfig(t, home)
	if !strings.Contains(config, "addr = '127.0.0.1:7100'") || strings.Contains(config, "hub.example.com:7100") {
		t.Fatalf("local setup did not replace external hub address:\n%s", config)
	}
}

func TestSetupNoStartIsFrozenInJournalRequest(t *testing.T) {
	useIsolatedInitConfig(t)
	lifecycle := successfulSetupLifecycle()
	cmd := newSetupCmdWithOpener(fixedSystemLifecycleOpener(lifecycle))
	cmd.SetArgs([]string{"--no-start"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup --no-start: %v", err)
	}
	if len(lifecycle.planRequests) != 1 || lifecycle.planRequests[0].Setup == nil || !lifecycle.planRequests[0].Setup.NoStart {
		t.Fatalf("Plan requests = %#v, want no_start setup", lifecycle.planRequests)
	}
}

func TestInitCompatAndSetupProduceEquivalentRequests(t *testing.T) {
	useIsolatedInitConfig(t)
	args := []string{"remote", "--hub", "hub.example.com:7100", "--agent", "claude", "--no-start"}
	setupLifecycle := successfulSetupLifecycle()
	setup := newSetupCmdWithOpener(fixedSystemLifecycleOpener(setupLifecycle))
	setup.SetArgs(args)
	if err := setup.Execute(); err != nil {
		t.Fatalf("setup remote: %v", err)
	}

	initLifecycle := successfulSetupLifecycle()
	init := newSetupLikeCmd("init [local|hub|remote|feishu|feishu-local]", "Initialize amux for a local profile or integration", fixedSystemLifecycleOpener(initLifecycle))
	init.SetArgs(args)
	if err := init.Execute(); err != nil {
		t.Fatalf("init remote: %v", err)
	}

	if !reflect.DeepEqual(initLifecycle.planRequests, setupLifecycle.planRequests) {
		t.Fatalf("init requests = %#v, setup requests = %#v", initLifecycle.planRequests, setupLifecycle.planRequests)
	}
}

func TestSetupExecutorFailureIncludesOperationID(t *testing.T) {
	useIsolatedInitConfig(t)
	lifecycle := successfulSetupLifecycle()
	lifecycle.applyErr = errors.New("write config failed")
	cmd := newSetupCmdWithOpener(fixedSystemLifecycleOpener(lifecycle))
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "op-setup") || !strings.Contains(err.Error(), "write config failed") {
		t.Fatalf("setup error = %v, want executor failure and operation ID", err)
	}
}

func TestInitHubWritesDefaultHubAddr(t *testing.T) {
	home := useIsolatedInitConfig(t)

	cmd := newTestInitCompatCmd()
	cmd.SetArgs([]string{"hub", "--no-start"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init hub: %v", err)
	}

	config := readTestConfig(t, home)
	for _, want := range []string{
		"[hub]",
		"addr = '127.0.0.1:7100'",
		"[runtime]",
		"[cli]",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q:\n%s", want, config)
		}
	}
}

func TestInitRemoteRequiresHubAddrAndDoesNotUseDefault(t *testing.T) {
	home := useIsolatedInitConfig(t)

	cmd := newTestInitCompatCmd()
	cmd.SetArgs([]string{"remote", "--no-start"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --hub") {
		t.Fatalf("init remote without --hub error = %v, want requires --hub", err)
	}

	cmd = newTestInitCompatCmd()
	cmd.SetArgs([]string{"remote", "--hub", "10.0.0.12:7100", "--no-start"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init remote: %v", err)
	}

	config := readTestConfig(t, home)
	if !strings.Contains(config, "addr = '10.0.0.12:7100'") {
		t.Fatalf("remote init should write explicit hub addr:\n%s", config)
	}
	if strings.Contains(config, "127.0.0.1:7100") {
		t.Fatalf("remote init should not use local hub default:\n%s", config)
	}
}

func TestInitFeishuPreparesHubAndPointsToBridgeSetup(t *testing.T) {
	home := useIsolatedInitConfig(t)

	cmd := newTestInitCompatCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"feishu", "--no-start"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init feishu: %v", err)
	}

	config := readTestConfig(t, home)
	if !strings.Contains(config, "addr = '127.0.0.1:7100'") {
		t.Fatalf("feishu init should configure local hub default:\n%s", config)
	}
	if !strings.Contains(out.String(), "amux bridge feishu setup") {
		t.Fatalf("feishu init should print bridge setup next step:\n%s", out.String())
	}
}

func TestInitFeishuLocalAgentFlagOverridesDefaultProfile(t *testing.T) {
	home := useIsolatedInitConfig(t)

	cmd := newTestInitCompatCmd()
	cmd.SetArgs([]string{"feishu-local", "--agent", "claude", "--no-start"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init feishu-local: %v", err)
	}

	config := readTestConfig(t, home)
	if !strings.Contains(config, "agent = 'builtin/claude'") {
		t.Fatalf("config should use explicit claude agent:\n%s", config)
	}
}

func TestInitFeishuLocalRejectsUnknownProfile(t *testing.T) {
	useIsolatedInitConfig(t)

	cmd := newTestInitCompatCmd()
	cmd.SetArgs([]string{"feishu-local", "--agent", "team/coder", "--no-start"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown builtin agent profile") {
		t.Fatalf("init feishu-local error = %v, want unknown builtin agent profile", err)
	}
}

func readTestConfig(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}

func useIsolatedInitConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
	return home
}

func successfulSetupLifecycle() *fakeSystemLifecycle {
	return &fakeSystemLifecycle{
		applyID:   "op-setup",
		operation: testSystemOperation("op-setup", system.OperationSucceeded, time.Unix(100, 0)),
	}
}

func fixedSystemLifecycleOpener(lifecycle system.Lifecycle) systemLifecycleOpener {
	return func(io.Writer) (system.Lifecycle, func() error, error) {
		return lifecycle, func() error { return nil }, nil
	}
}

func newTestInitCompatCmd() *cobra.Command {
	return newSetupLikeCmd("init [local|hub|remote|feishu|feishu-local]", "Initialize amux for a local profile or integration", func(out io.Writer) (system.Lifecycle, func() error, error) {
		lifecycle := successfulSetupLifecycle()
		var calls []string
		hooks := fakeLegacySystemMutationHooks(&calls, defaultLocalHubAddr)
		lifecycle.applyFunc = func(ctx context.Context, plan system.ChangePlan) (system.OperationID, error) {
			err := legacySystemExecutor(out, hooks)(ctx, plan)
			return lifecycle.applyID, err
		}
		return lifecycle, func() error { return nil }, nil
	})
}
