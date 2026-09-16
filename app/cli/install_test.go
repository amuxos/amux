package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/system"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func TestUpdateCopiesBinariesAndWritesMetadata(t *testing.T) {
	const resolvedContractVersion = "0.5.7"
	home := t.TempDir()
	source := writeFakeInstallSource(t, "v1")
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
	previousInstaller := installManagedContractRuntime
	installManagedContractRuntime = func(context.Context) (agentprofile.ManagedContractRuntime, error) {
		return agentprofile.ManagedContractRuntime{
			Version:    resolvedContractVersion,
			Root:       agentprofile.DefaultManagedContractRuntimeRoot(),
			Executable: agentprofile.DefaultManagedContractExecutablePath(),
		}, nil
	}
	t.Cleanup(func() { installManagedContractRuntime = previousInstaller })
	if err := agentprofile.SaveDefaultManagedAuthoringState(agentprofile.ManagedAuthoringState{
		AgentPackContractStatus:    agentprofile.ManagedAuthoringStatusDegraded,
		LocalExpertAuthoringStatus: agentprofile.ManagedAuthoringStatusDegraded,
	}); err != nil {
		t.Fatalf("seed degraded managed authoring state: %v", err)
	}
	oldVersion := version
	version = "v1.2.3"
	t.Cleanup(func() { version = oldVersion })

	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--source", source})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update: %v", err)
	}

	for _, name := range installBinaryNames {
		path := filepath.Join(home, ".amux", "bin", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if got := string(data); got != name+"-v1" {
			t.Fatalf("installed %s content = %q", name, got)
		}
		if info, err := os.Stat(path); err != nil || info.Mode()&0111 == 0 {
			t.Fatalf("installed %s should be executable: info=%v err=%v", name, info, err)
		}
	}

	meta := filepath.Join(home, ".amux", "install.json")
	data, err := os.ReadFile(meta)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"version": "v1.2.3"`, `"bin_dir": "` + filepath.Join(home, ".amux", "bin") + `"`, `"amuxd"`, `"agent_pack_contract_version": "` + resolvedContractVersion + `"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("metadata missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(out.String(), "Updated amux to v1.2.3") {
		t.Fatalf("update output missing version:\n%s", out.String())
	}
	state, err := agentprofile.LoadDefaultManagedAuthoringState()
	if err != nil {
		t.Fatalf("load repaired managed authoring state: %v", err)
	}
	if state.AgentPackContractStatus != agentprofile.ManagedAuthoringStatusReady || state.LocalExpertAuthoringStatus != agentprofile.ManagedAuthoringStatusReady {
		t.Fatalf("managed authoring state after successful update = %q/%q, want ready/ready", state.AgentPackContractStatus, state.LocalExpertAuthoringStatus)
	}
	for _, want := range []string{
		"Updating amux...",
		"source: " + source,
		"destination: " + filepath.Join(home, ".amux", "bin"),
		"Installing binaries to " + filepath.Join(home, ".amux", "bin") + "...",
		"Update summary:",
		"binaries: updated (amux, amuxd, amuxlet)",
		"agent_pack_contract: ready (" + resolvedContractVersion + " at " + agentprofile.DefaultManagedContractExecutablePath() + ")",
		"services: not restarted (use --restart)",
		"sessions: not restarted (use --restart-sessions)",
		"local_services: not restarted (use --restart-local-services)",
		"all local restartable: use --restart-all",
		"Next:",
		"1. Add amux to PATH for this shell:",
		"2. Verify the existing installation:",
		"amux system status",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("update output missing next step %q:\n%s", want, out.String())
		}
	}
	for _, freshOnly := range []string{"amux setup", "amux session run builtin/codex -n work"} {
		if strings.Contains(out.String(), freshOnly) {
			t.Fatalf("update output should not suggest fresh-install step %q:\n%s", freshOnly, out.String())
		}
	}
	for _, legacy := range []string{"amux status", "amux runtime start", "amux run builtin/codex -n work"} {
		if strings.Contains(out.String(), legacy) {
			t.Fatalf("update output should not suggest legacy command %q:\n%s", legacy, out.String())
		}
	}
}

func TestUpdateContinuesWhenManagedAuthoringPreparationFails(t *testing.T) {
	tests := []struct {
		name            string
		suffix          string
		warning         string
		contractSummary string
		state           agentprofile.ManagedAuthoringState
		setup           func(*testing.T, string) func(*testing.T)
	}{
		{
			name:            "Contract runtime unavailable",
			suffix:          "without-contract",
			warning:         "Warning: Agent Pack Contract preparation failed; amux binaries were updated but local expert authoring is degraded: SCM unavailable",
			contractSummary: "agent_pack_contract: degraded",
			state: agentprofile.ManagedAuthoringState{
				AgentPackContractStatus:    agentprofile.ManagedAuthoringStatusDegraded,
				LocalExpertAuthoringStatus: agentprofile.ManagedAuthoringStatusDegraded,
			},
			setup: func(t *testing.T, _ string) func(*testing.T) {
				const previousContractVersion = "0.5.7"
				if err := os.MkdirAll(defaultAmuxDir(), 0o755); err != nil {
					t.Fatalf("create amux home for previous metadata: %v", err)
				}
				if err := writeInstallMetadata(installMetadata{
					AgentPackContractVersion: previousContractVersion,
					AgentPackContractPath:    agentprofile.DefaultManagedContractExecutablePath(),
				}); err != nil {
					t.Fatalf("seed previous install metadata: %v", err)
				}
				marker := filepath.Join(agentprofile.DefaultManagedContractRuntimeRoot(), "previous-runtime")
				if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				previousInstaller := installManagedContractRuntime
				installManagedContractRuntime = func(context.Context) (agentprofile.ManagedContractRuntime, error) {
					return agentprofile.ManagedContractRuntime{}, errors.New("SCM unavailable")
				}
				t.Cleanup(func() { installManagedContractRuntime = previousInstaller })
				localLibraryPrepared := false
				previousLibraryPreparer := prepareManagedLocalLibrary
				prepareManagedLocalLibrary = func(context.Context) error {
					localLibraryPrepared = true
					return nil
				}
				t.Cleanup(func() { prepareManagedLocalLibrary = previousLibraryPreparer })
				return func(t *testing.T) {
					if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
						t.Fatalf("previous Contract runtime marker = %q, err=%v", data, err)
					}
					if localLibraryPrepared {
						t.Fatal("managed local library preparation should be skipped without a ready Contract runtime")
					}
					meta, err := readInstallMetadata()
					if err != nil {
						t.Fatalf("read degraded install metadata: %v", err)
					}
					if meta.AgentPackContractVersion != previousContractVersion {
						t.Fatalf("degraded update Contract version = %q, want preserved %q", meta.AgentPackContractVersion, previousContractVersion)
					}
				}
			},
		},
		{
			name:            "managed local library unavailable",
			suffix:          "without-local-library",
			warning:         "Warning: Managed local expert library preparation failed; amux binaries were updated but local expert authoring is degraded: local Git unavailable",
			contractSummary: "agent_pack_contract: ready",
			state: agentprofile.ManagedAuthoringState{
				AgentPackContractStatus:    agentprofile.ManagedAuthoringStatusReady,
				LocalExpertAuthoringStatus: agentprofile.ManagedAuthoringStatusDegraded,
			},
			setup: func(t *testing.T, amuxHome string) func(*testing.T) {
				marker := filepath.Join(amuxHome, "managed-agent-packs", "local", "previous-history")
				if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				previousLibraryPreparer := prepareManagedLocalLibrary
				prepareManagedLocalLibrary = func(context.Context) error {
					return errors.New("local Git unavailable")
				}
				t.Cleanup(func() { prepareManagedLocalLibrary = previousLibraryPreparer })
				return func(t *testing.T) {
					if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
						t.Fatalf("previous managed local library marker = %q, err=%v", data, err)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			amuxHome := filepath.Join(home, ".amux")
			t.Setenv("HOME", home)
			t.Setenv("AMUX_HOME", amuxHome)
			verifyPreservedState := tt.setup(t, amuxHome)

			cmd := newUpdateCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"--source", writeFakeInstallSource(t, tt.suffix)})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("update with degraded managed authoring: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(amuxHome, "bin", "amux"))
			if err != nil {
				t.Fatalf("read updated amux: %v", err)
			}
			if got, want := string(data), "amux-"+tt.suffix; got != want {
				t.Fatalf("updated amux = %q, want %q", got, want)
			}
			verifyPreservedState(t)
			for _, want := range []string{
				tt.warning,
				tt.contractSummary,
				"local_expert_authoring: degraded (run `amux update` to retry)",
			} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("update output missing %q:\n%s", want, out.String())
				}
			}

			var metadata installMetadata
			metadataBytes, err := os.ReadFile(filepath.Join(amuxHome, "install.json"))
			if err != nil {
				t.Fatalf("read install metadata: %v", err)
			}
			if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
				t.Fatalf("parse install metadata: %v", err)
			}
			if metadata.AgentPackContractStatus != tt.state.AgentPackContractStatus || metadata.LocalExpertAuthoringStatus != tt.state.LocalExpertAuthoringStatus {
				t.Fatalf("install metadata authoring state = %q/%q, want %q/%q", metadata.AgentPackContractStatus, metadata.LocalExpertAuthoringStatus, tt.state.AgentPackContractStatus, tt.state.LocalExpertAuthoringStatus)
			}
			state, err := agentprofile.LoadDefaultManagedAuthoringState()
			if err != nil {
				t.Fatalf("load managed authoring state: %v", err)
			}
			if state.AgentPackContractStatus != tt.state.AgentPackContractStatus || state.LocalExpertAuthoringStatus != tt.state.LocalExpertAuthoringStatus {
				t.Fatalf("managed authoring state = %q/%q, want %q/%q", state.AgentPackContractStatus, state.LocalExpertAuthoringStatus, tt.state.AgentPackContractStatus, tt.state.LocalExpertAuthoringStatus)
			}
		})
	}
}

func TestInstallPreparesManagedLocalLibraryBeforeReplacingBinaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
	called := false
	previous := prepareManagedLocalLibrary
	prepareManagedLocalLibrary = func(context.Context) error {
		called = true
		return nil
	}
	t.Cleanup(func() { prepareManagedLocalLibrary = previous })
	opts := &installOptions{sourceDir: writeFakeInstallSource(t, "local"), binDir: filepath.Join(t.TempDir(), "bin")}
	if err := runInstall(opts); err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if !called {
		t.Fatal("managed local library was not prepared")
	}
}

func TestUpdateWithExistingConfigDoesNotRecommendDefaultSetup(t *testing.T) {
	home := t.TempDir()
	amuxHome := filepath.Join(home, ".amux")
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", amuxHome)
	if err := os.MkdirAll(amuxHome, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[hub]\naddr = 'hub.example.com:7100'\n"
	if err := os.WriteFile(filepath.Join(amuxHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--source", writeFakeInstallSource(t, "existing")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update existing installation: %v", err)
	}
	if strings.Contains(out.String(), "amux setup") {
		t.Fatalf("existing installation update suggested topology-resetting default setup:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "amux system status") {
		t.Fatalf("existing installation update omitted canonical verification:\n%s", out.String())
	}
	data, err := os.ReadFile(filepath.Join(amuxHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != config {
		t.Fatalf("update changed existing topology config:\n%s", data)
	}
}

func TestUpdateWithEnvOnlyRemoteDoesNotRecommendDefaultSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
	t.Setenv("AMUX_HUB_ADDR", "remote.example.com:7100")

	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--source", writeFakeInstallSource(t, "remote")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update env-only remote installation: %v", err)
	}
	if strings.Contains(out.String(), "amux setup") {
		t.Fatalf("env-only remote update suggested topology-resetting default setup:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "amux system status") {
		t.Fatalf("env-only remote update omitted canonical verification:\n%s", out.String())
	}
}

func TestReplaceInstallBinariesRollsBackWholeSetOnActivationFailure(t *testing.T) {
	source := writeFakeInstallSource(t, "new")
	binDir := t.TempDir()
	for _, name := range installBinaryNames {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(name+"-old"), 0o755); err != nil {
			t.Fatalf("write old %s: %v", name, err)
		}
	}

	rename := func(oldPath, newPath string) error {
		if strings.Contains(filepath.Dir(oldPath), ".amux-stage-") && filepath.Base(newPath) == "amuxd" {
			return errors.New("injected activation failure")
		}
		return os.Rename(oldPath, newPath)
	}
	err := replaceInstallBinaries(source, binDir, rename)
	if err == nil || !strings.Contains(err.Error(), "injected activation failure") {
		t.Fatalf("replace error = %v, want injected activation failure", err)
	}
	for _, name := range installBinaryNames {
		data, readErr := os.ReadFile(filepath.Join(binDir, name))
		if readErr != nil {
			t.Fatalf("read rolled-back %s: %v", name, readErr)
		}
		if got, want := string(data), name+"-old"; got != want {
			t.Fatalf("rolled-back %s = %q, want %q", name, got, want)
		}
	}
	for _, pattern := range []string{".amux-stage-*", ".amux-backup-*"} {
		matches, globErr := filepath.Glob(filepath.Join(binDir, pattern))
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("temporary install directories remain after rollback: %v", matches)
		}
	}
}

func TestUpdateOverwritesInstalledBinaries(t *testing.T) {
	home := t.TempDir()
	sourceV1 := writeFakeInstallSource(t, "v1")
	sourceV2 := writeFakeInstallSource(t, "v2")
	t.Setenv("HOME", home)

	initialUpdate := newUpdateCmd()
	initialUpdate.SetArgs([]string{"--source", sourceV1})
	if err := initialUpdate.Execute(); err != nil {
		t.Fatalf("initial update: %v", err)
	}

	oldVersion := version
	version = "v2.0.0"
	t.Cleanup(func() { version = oldVersion })
	update := newUpdateCmd()
	var out bytes.Buffer
	update.SetOut(&out)
	update.SetArgs([]string{"--source", sourceV2})
	if err := update.Execute(); err != nil {
		t.Fatalf("update: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "bin", "amux"))
	if err != nil {
		t.Fatalf("read upgraded amux: %v", err)
	}
	if string(data) != "amux-v2" {
		t.Fatalf("update did not overwrite amux: %q", string(data))
	}
	if !strings.Contains(out.String(), "Updated amux to v2.0.0") {
		t.Fatalf("update output missing version:\n%s", out.String())
	}
}

func TestRestartableUpdateSessionOnlyIncludesRunningUserSessions(t *testing.T) {
	tests := []struct {
		name string
		sess *sessionEntry
		want bool
	}{
		{name: "nil", sess: nil},
		{name: "running user session", sess: &sessionEntry{Session: pb.Session{Name: "work", State: "running"}}, want: true},
		{name: "running task child", sess: &sessionEntry{Session: pb.Session{Name: "subtask", State: "running", OwnerTaskId: "task-1"}}},
		{name: "sleeping user session", sess: &sessionEntry{Session: pb.Session{Name: "sleepy", State: "sleeping"}}},
		{name: "archived user session", sess: &sessionEntry{Session: pb.Session{Name: "done", State: "archived"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restartableUpdateSession(tt.sess); got != tt.want {
				t.Fatalf("restartableUpdateSession() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPrintRestartedLocalSessionsIncludesFailures(t *testing.T) {
	result := restartLocalSessionsResult{
		Restarted: []string{"ok-1", "ok-2"},
		Failed: []restartSessionFailure{
			{Name: "bad-1", Error: errors.New("agent binary not found")},
			{Name: "bad-2", Error: errors.New("permission denied")},
		},
	}
	err := result.Err()
	if err == nil {
		t.Fatal("Err() = nil, want failure")
	}
	for _, want := range []string{"failed to restart 2 local session(s)", "bad-1: agent binary not found", "bad-2: permission denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Err() = %v, missing %q", err, want)
		}
	}
	var out bytes.Buffer
	printRestartedLocalSessions(&out, result)
	text := out.String()
	for _, want := range []string{
		"Restarted local session(s): ok-1, ok-2",
		"Failed local session restart(s):",
		"- bad-1: agent binary not found",
		"- bad-2: permission denied",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("restart summary missing %q:\n%s", want, text)
		}
	}
}

func TestNormalizeUpdateRestartOptionsExpandsRestartAll(t *testing.T) {
	opts := installOptions{restartAll: true}
	normalizeUpdateRestartOptions(&opts)
	if !opts.restartSessions {
		t.Fatal("restartAll should enable restartSessions")
	}
	if !opts.restartLocalServices {
		t.Fatal("restartAll should enable restartLocalServices")
	}
	if opts.restart {
		t.Fatal("restartAll should rely on restartSessions for managed service restart instead of setting restart directly")
	}
}

func TestPrintUpdateSummaryForRestartAll(t *testing.T) {
	var out bytes.Buffer
	printUpdateSummary(&out, &installOptions{restartAll: true, restartSessions: true, restartLocalServices: true})
	text := out.String()
	for _, want := range []string{
		"services: refreshed/started local amux system services",
		"sessions: restarted running local user sessions",
		"local_services: restarted registered mcp/gui local servers",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "all local restartable: use --restart-all") {
		t.Fatalf("restart-all summary should not suggest itself:\n%s", text)
	}
}

func TestUpdateRestartEnsuresSystemStartedWhenServicesWereStopped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))

	lifecycle := &fakeSystemLifecycle{
		applyID:   "op-update-start",
		operation: testSystemOperation("op-update-start", system.OperationSucceeded, time.Unix(100, 0)),
	}
	oldOpen := updateSystemLifecycleOpener
	updateSystemLifecycleOpener = func(io.Writer) (system.Lifecycle, func() error, error) {
		return lifecycle, func() error { return nil }, nil
	}
	t.Cleanup(func() { updateSystemLifecycleOpener = oldOpen })
	refreshedAutoSync := 0
	oldAutoSyncRefresher := updateAgentPackAutoSyncRefresher
	updateAgentPackAutoSyncRefresher = func() error {
		refreshedAutoSync++
		return nil
	}
	t.Cleanup(func() { updateAgentPackAutoSyncRefresher = oldAutoSyncRefresher })

	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--post-install", "--restart", "--installed-version", "v-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update --post-install --restart: %v", err)
	}

	want := system.ChangeRequest{Action: system.ActionStart, Scope: system.CurrentScope()}
	if !reflect.DeepEqual(lifecycle.planRequests, []system.ChangeRequest{want}) {
		t.Fatalf("system lifecycle requests = %#v, want %#v", lifecycle.planRequests, []system.ChangeRequest{want})
	}
	if !strings.Contains(out.String(), "system start: complete (operation op-update-start)") {
		t.Fatalf("update output missing system start operation:\n%s", out.String())
	}
	if refreshedAutoSync != 1 {
		t.Fatalf("Agent Pack auto sync refreshes = %d, want 1", refreshedAutoSync)
	}
}

func TestUpdateWithoutRestartLeavesAgentPackAutoSyncAlone(t *testing.T) {
	refreshedAutoSync := 0
	oldAutoSyncRefresher := updateAgentPackAutoSyncRefresher
	updateAgentPackAutoSyncRefresher = func() error {
		refreshedAutoSync++
		return nil
	}
	t.Cleanup(func() { updateAgentPackAutoSyncRefresher = oldAutoSyncRefresher })

	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--post-install", "--installed-version", "v-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if refreshedAutoSync != 0 {
		t.Fatalf("Agent Pack auto sync refreshes = %d, want 0", refreshedAutoSync)
	}
}

func TestUpdateRestartStopsWhenAgentPackAutoSyncRefreshFails(t *testing.T) {
	refreshErr := errors.New("autosync restart failed")
	oldAutoSyncRefresher := updateAgentPackAutoSyncRefresher
	updateAgentPackAutoSyncRefresher = func() error { return refreshErr }
	t.Cleanup(func() { updateAgentPackAutoSyncRefresher = oldAutoSyncRefresher })

	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--post-install", "--restart", "--installed-version", "v-test"})
	err := cmd.Execute()
	if !errors.Is(err, refreshErr) || !strings.Contains(err.Error(), "refresh Agent Pack auto sync") {
		t.Fatalf("error = %v, want autosync refresh failure", err)
	}
}

func TestUpdateRestartHandoffsPostInstallToInstalledAmux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
	source := writeFakeInstallSource(t, "handoff")

	var gotBinary string
	var gotArgs []string
	oldRunner := updatePostInstallRunner
	updatePostInstallRunner = func(cmd *cobra.Command, binary string, args []string) error {
		gotBinary = binary
		gotArgs = append([]string(nil), args...)
		return nil
	}
	t.Cleanup(func() { updatePostInstallRunner = oldRunner })

	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--source", source, "--restart"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update --restart: %v", err)
	}

	if want := filepath.Join(home, ".amux", "bin", "amux"); gotBinary != want {
		t.Fatalf("post-install binary = %q, want %q", gotBinary, want)
	}
	wantArgs := []string{
		"update",
		"--post-install",
		"--bin-dir", filepath.Join(home, ".amux", "bin"),
		"--installed-version", version,
		"--restart",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("post-install args = %#v, want %#v", gotArgs, wantArgs)
	}
	if strings.Contains(out.String(), "Update summary:") {
		t.Fatalf("parent updater should leave summary to installed amux:\n%s", out.String())
	}
}

func TestUpdateDownloadsSCMLatestWhenSourceOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	latestArchive := writeFakeSCMLatestPackage(t, "v9.9.9", runtime.GOOS, runtime.GOARCH, "scm")
	t.Setenv("AMUX_SCM_LATEST_URL", "file://"+latestArchive)
	oldVersion := version
	version = "v0.0.1-old"
	t.Cleanup(func() { version = oldVersion })

	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update from scm latest: %v", err)
	}

	for _, name := range installBinaryNames {
		path := filepath.Join(home, ".amux", "bin", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if got := string(data); got != name+"-scm" {
			t.Fatalf("installed %s content = %q", name, got)
		}
	}
	if !strings.Contains(out.String(), "Updated amux to v9.9.9") {
		t.Fatalf("update output should use downloaded release version:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Downloading latest SCM package...") {
		t.Fatalf("update output should show SCM download progress:\n%s", out.String())
	}
	meta, err := os.ReadFile(filepath.Join(home, ".amux", "install.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if !strings.Contains(string(meta), `"version": "v9.9.9"`) {
		t.Fatalf("metadata should use downloaded release version:\n%s", meta)
	}
}

func TestValidateExpectedUpdateVersion(t *testing.T) {
	if err := validateExpectedUpdateVersion("1.2.3", "1.2.3"); err != nil {
		t.Fatalf("matching version: %v", err)
	}
	if err := validateExpectedUpdateVersion("", "1.2.4"); err != nil {
		t.Fatalf("empty expected version: %v", err)
	}
	if err := validateExpectedUpdateVersion("1.2.3", "1.2.4"); err == nil {
		t.Fatal("mismatched version = nil, want error")
	}
}

func TestUpdateDownloadHTTPClientHasTimeout(t *testing.T) {
	if updateHTTPClient == nil {
		t.Fatal("update HTTP client is nil")
	}
	if updateHTTPClient.Timeout <= 0 {
		t.Fatalf("update HTTP client should have a timeout, got %s", updateHTTPClient.Timeout)
	}
}

func TestUpdateShellWritesPathBlockOnce(t *testing.T) {
	home := t.TempDir()
	source := writeFakeInstallSource(t, "v1")
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")

	for i := 0; i < 2; i++ {
		cmd := newUpdateCmd()
		cmd.SetArgs([]string{"--source", source, "--force", "--update-shell"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("update --update-shell run %d: %v", i+1, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatalf("read zshrc: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"# >>> amux path >>>",
		`export PATH="$HOME/.amux/bin:$PATH"`,
		"# <<< amux path <<<",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("zshrc missing %q:\n%s", want, text)
		}
	}
	if count := strings.Count(text, "# >>> amux path >>>"); count != 1 {
		t.Fatalf("amux path block should be written once, got %d:\n%s", count, text)
	}
}

func TestUpdateShellWritesCommonInteractiveProfilesWhenShellIsAmbiguous(t *testing.T) {
	home := t.TempDir()
	source := writeFakeInstallSource(t, "v1")
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")

	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--source", source, "--force", "--update-shell"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update --update-shell: %v", err)
	}

	for _, name := range []string{".zshrc", ".bashrc", ".profile"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), `export PATH="$HOME/.amux/bin:$PATH"`) {
			t.Fatalf("%s missing amux PATH block:\n%s", name, data)
		}
	}
}

func TestUpdateShellSkipsExistingPathLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	zshrc := filepath.Join(home, ".zshrc")
	existing := `export PATH="$HOME/.amux/bin:$PATH"` + "\n"
	if err := os.WriteFile(zshrc, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ensureShellPathBlockInFile(zshrc, filepath.Join(home, ".amux", "bin")); err != nil {
		t.Fatalf("ensure shell path: %v", err)
	}

	data, err := os.ReadFile(zshrc)
	if err != nil {
		t.Fatalf("read zshrc: %v", err)
	}
	text := string(data)
	if text != existing {
		t.Fatalf("existing path line should be left untouched:\n%s", text)
	}
}

func TestUpdateShellUsesExplicitShellProfile(t *testing.T) {
	home := t.TempDir()
	source := writeFakeInstallSource(t, "v1")
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")

	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--source", source, "--force", "--update-shell", "--shell-profile", "~/.zshrc"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update --shell-profile: %v", err)
	}

	zshrcData, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatalf("read zshrc: %v", err)
	}
	if !strings.Contains(string(zshrcData), "# >>> amux path >>>") {
		t.Fatalf("zshrc missing amux block:\n%s", zshrcData)
	}
	if _, err := os.Stat(filepath.Join(home, ".profile")); !os.IsNotExist(err) {
		t.Fatalf("explicit shell profile should not write .profile, err=%v", err)
	}
}

func TestUpdateShellPrintsSourceNextStep(t *testing.T) {
	home := t.TempDir()
	source := writeFakeInstallSource(t, "v1")
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
	t.Setenv("PATH", "/usr/bin")

	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--source", source, "--force", "--update-shell", "--shell-profile", "~/.zshrc"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update --update-shell: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "source "+filepath.Join(home, ".zshrc")) {
		t.Fatalf("update output should suggest source profile:\n%s", text)
	}
	if !strings.Contains(text, "amux system status") {
		t.Fatalf("update output should suggest verification:\n%s", text)
	}
	if strings.Contains(text, "amux setup") || strings.Contains(text, "amux session run builtin/codex -n work") {
		t.Fatalf("update output should not suggest fresh-install steps:\n%s", text)
	}
	if strings.Contains(text, "export PATH=") {
		t.Fatalf("update output should not suggest export after writing shell profile:\n%s", text)
	}
}

func TestUninstallRemovesBinariesButKeepsDataByDefault(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".amux", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range installBinaryNames {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := isolateUninstallTestHome(t, home)

	cmd := newUninstallCmd()
	cmd.SetArgs([]string{"--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	for _, name := range installBinaryNames {
		if _, err := os.Stat(filepath.Join(binDir, name)); !os.IsNotExist(err) {
			t.Fatalf("installed binary %s should be removed, err=%v", name, err)
		}
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config should be kept by default: %v", err)
	}
}

func TestUninstallRemovesManagedShellPathBlock(t *testing.T) {
	home := t.TempDir()
	isolateUninstallTestHome(t, home)
	zshrc := filepath.Join(home, ".zshrc")
	original := strings.Join([]string{
		"before",
		"# >>> amux path >>>",
		`export PATH="$HOME/.amux/bin:$PATH"`,
		"# <<< amux path <<<",
		"after",
		"",
	}, "\n")
	if err := os.WriteFile(zshrc, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newUninstallCmd()
	cmd.SetArgs([]string{"--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	data, err := os.ReadFile(zshrc)
	if err != nil {
		t.Fatalf("read zshrc: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "amux path") || strings.Contains(text, ".amux/bin") {
		t.Fatalf("uninstall should remove managed amux path block:\n%s", text)
	}
	for _, want := range []string{"before", "after"} {
		if !strings.Contains(text, want) {
			t.Fatalf("uninstall should keep user content %q:\n%s", want, text)
		}
	}
}

func TestUninstallPurgeRemovesAmuxDirectory(t *testing.T) {
	home := t.TempDir()
	amuxDir := filepath.Join(home, ".amux")
	if err := os.MkdirAll(filepath.Join(amuxDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	isolateUninstallTestHome(t, home)

	cmd := newUninstallCmd()
	cmd.SetArgs([]string{"--yes", "--purge"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("uninstall --purge: %v", err)
	}

	if _, err := os.Stat(amuxDir); !os.IsNotExist(err) {
		t.Fatalf("amux dir should be removed on purge, err=%v", err)
	}
}

func TestSystemUninstallPurgesWithoutOpeningOperationJournal(t *testing.T) {
	home := t.TempDir()
	amuxHome := filepath.Join(home, ".amux")
	binDir := filepath.Join(amuxHome, "bin")
	if err := os.MkdirAll(filepath.Join(amuxHome, "system"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range installBinaryNames {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(amuxHome, "system", "operations.db"), []byte("existing journal"), 0600); err != nil {
		t.Fatal(err)
	}
	isolateUninstallTestHome(t, home)

	opened := false
	cmd := newSystemCmdWithOpener(func(io.Writer) (system.Lifecycle, func() error, error) {
		opened = true
		return nil, nil, errors.New("uninstall should not open lifecycle")
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"uninstall", "--yes", "--purge"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("system uninstall --purge: %v", err)
	}
	if opened {
		t.Fatal("system uninstall opened the operation journal lifecycle")
	}
	if _, err := os.Stat(amuxHome); !os.IsNotExist(err) {
		t.Fatalf("amux home should remain removed after purge, err=%v", err)
	}
	if !strings.Contains(out.String(), "Uninstalled amux and removed") {
		t.Fatalf("system uninstall output = %q", out.String())
	}
}

// isolateUninstallTestHome keeps the real uninstall path under test without
// allowing managedService.Stop's orphan-listener reclamation to discover a
// developer's live hub on the default 127.0.0.1:7100 address.
func isolateUninstallTestHome(t *testing.T, home string) string {
	t.Helper()
	// File-removal behavior is under test here; process signaling is not. Never
	// let an in-process uninstall test reach the production service stopper,
	// regardless of HOME, Viper, PID-file, or listener-discovery state.
	previousStopper := uninstallServiceStopper
	uninstallServiceStopper = func() {}
	t.Cleanup(func() { uninstallServiceStopper = previousStopper })
	// config.Load uses the package-global Viper instance. Reset both before and
	// after each uninstall test so a previous test's hub.addr cannot override
	// the isolated config below and point orphan reclamation at a live service.
	viper.Reset()
	t.Cleanup(viper.Reset)
	amuxHome := filepath.Join(home, ".amux")
	if err := os.MkdirAll(amuxHome, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(amuxHome, "config.toml")
	hubAddr := freeLoopbackAddr(t)
	config := "[hub]\naddr = '" + hubAddr + "'\n"
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", amuxHome)
	return configPath
}

func TestUpdateCommandDoesNotExposeDaemonsInHelp(t *testing.T) {
	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update help: %v", err)
	}
	text := out.String()
	if strings.Contains(text, "amuxlet") {
		t.Fatalf("update help should not expose amuxlet:\n%s", text)
	}
}

func TestHiddenUpdateRemainsExecutableWithoutUpgradeWarning(t *testing.T) {
	root := newRootCmd()
	update, _, err := root.Find([]string{"update"})
	if err != nil || update.Name() != "update" {
		t.Fatalf("find hidden update: found=%v err=%v", update, err)
	}
	if !update.Hidden {
		t.Fatal("update should be hidden before system upgrade exists")
	}
	update.RunE = func(*cobra.Command, []string) error { return nil }
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"update"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute hidden update: %v", err)
	}
	for stream, got := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if strings.Contains(got, "Warning:") || strings.Contains(got, "system upgrade") {
			t.Fatalf("update wrote nonexistent upgrade replacement to %s: %q", stream, got)
		}
	}
}

func writeFakeInstallSource(t *testing.T, suffix string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range installBinaryNames {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"-"+suffix), 0755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	return dir
}

func writeFakeSCMLatestPackage(t *testing.T, releaseVersion, goos, goarch, suffix string) string {
	t.Helper()
	payloadRoot := filepath.Join(t.TempDir(), "amux_"+releaseVersion+"_"+goos+"_"+goarch)
	if err := os.MkdirAll(payloadRoot, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range installBinaryNames {
		if err := os.WriteFile(filepath.Join(payloadRoot, name), []byte(name+"-"+suffix), 0755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	platformArchive := filepath.Join(t.TempDir(), filepath.Base(payloadRoot)+".tar.gz")
	writeTarGz(t, platformArchive, filepath.Dir(payloadRoot), []string{filepath.Base(payloadRoot)})

	latestRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(latestRoot, "install.sh"), []byte("#!/usr/bin/env bash\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := copyFileForInstallTest(platformArchive, filepath.Join(latestRoot, filepath.Base(platformArchive)), 0644); err != nil {
		t.Fatal(err)
	}
	latestArchive := filepath.Join(t.TempDir(), "amux-scm-latest.tar.gz")
	writeTarGz(t, latestArchive, latestRoot, []string{"install.sh", filepath.Base(platformArchive)})
	return latestArchive
}

func writeTarGz(t *testing.T, dst, root string, names []string) {
	t.Helper()
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()
	for _, name := range names {
		path := filepath.Join(root, name)
		if err := filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			_, err = io.Copy(tw, in)
			return err
		}); err != nil {
			t.Fatalf("write tar %s: %v", dst, err)
		}
	}
}

func copyFileForInstallTest(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}
