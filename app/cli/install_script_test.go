//go:build darwin || linux

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/amuxhome/amuxhometest"
)

// Run the public installer against command doubles, with no native agents,
// network tools or service managers on PATH. HOME and AMUX_HOME deliberately
// differ so an accidental hard-coded ~/.amux does not pass these tests.
type publicInstallFixture struct {
	t                   *testing.T
	dir, home, amuxHome string
	source, path, log   string
	state               publicInstallState
	downloadBase        string
}

type publicInstallState struct {
	Configured, CancelSetup, StatusError                             bool
	IgnoreBind                                                       bool
	RejectProfile, RejectDoctor                                      bool
	SystemRunning, AutoSyncRunning                                   bool
	MissingRuntimeID, EmptyRuntimeID                                 bool
	RejectAutoSyncStatus, RejectAutoSyncStop, RejectSystemStop       bool
	AutoSyncStatusError                                              bool
	InstallSource, DoctorExecutable, StopExecutable, PurgeExecutable string
	Mode, Binding, Profile, Runtime, Cwd                             string
	Sessions                                                         []publicInstallSession
}

type publicInstallSession struct{ Name, Runtime, Profile, Cwd, State, Target string }

func newPublicInstallFixture(t *testing.T, runtimes ...string) *publicInstallFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &publicInstallFixture{
		t: t, dir: dir, home: filepath.Join(dir, "home"), amuxHome: filepath.Join(dir, "amux data"),
		source: filepath.Join(dir, "release binaries"), path: filepath.Join(dir, "tools"),
		log: filepath.Join(dir, "commands.jsonl"),
	}
	for _, path := range []string{f.home, f.source, f.path} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// An allowlist avoids discovering a developer's real codex/claude/pi, and
	// ensures a regression cannot invoke curl, npm or a native service manager.
	for _, name := range []string{"bash", "dirname", "basename", "mkdir", "cp", "chmod", "rm", "mv", "mktemp", "cat", "awk", "sed", "grep", "tr", "uname", "readlink", "sort", "head", "cut", "test"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("test utility %s: %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(f.path, name)); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	stub := "#!/bin/bash\nexec " + quote(executable) + " -test.run=^TestPublicInstallScriptHelperProcess$ -- \"$0\" \"$@\"\n"
	for _, name := range []string{"amux", "amuxd", "amuxlet", "amux-isolation-helper"} {
		f.write(filepath.Join(f.source, name), stub, 0o755)
	}
	for _, name := range runtimes {
		f.write(filepath.Join(f.path, name), "#!/bin/bash\necho 'native agents must not run in installer tests' >&2\nexit 97\n", 0o755)
	}
	return f
}

func (f *publicInstallFixture) write(path, text string, mode os.FileMode) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *publicInstallFixture) sidecar() string {
	f.t.Helper()
	dir := filepath.Join(f.dir, "prepared SDK")
	f.write(filepath.Join(dir, "dist", "index.js"), "// compiled SDK\n", 0o644)
	f.write(filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-agent-sdk", "index.js"), "// dependency\n", 0o644)
	f.write(filepath.Join(dir, "package.json"), "{\"type\":\"module\"}\n", 0o644)
	// Claude's runtime preflight may check for node without starting the SDK.
	f.write(filepath.Join(f.path, "node"), "#!/bin/bash\nexit 0\n", 0o755)
	return dir
}

func (f *publicInstallFixture) run(input string, args ...string) (string, [][]string, error) {
	f.t.Helper()
	data, err := json.Marshal(f.state)
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(filepath.Join(f.dir, "state.json"), string(data), 0o600)
	f.write(f.log, "", 0o600)
	script, err := filepath.Abs("../../scripts/install.sh")
	if err != nil {
		f.t.Fatal(err)
	}
	// Bash reads scripts incrementally. Snapshot it so an edit during the test
	// run cannot splice different installer revisions into one subprocess.
	scriptBytes, err := os.ReadFile(script)
	if err != nil {
		f.t.Fatal(err)
	}
	script = filepath.Join(f.dir, "install.sh")
	f.write(script, string(scriptBytes), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseArgs := []string{script, "--source", f.source}
	if f.downloadBase != "" {
		baseArgs = []string{script}
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", append(baseArgs, args...)...)
	cmd.Dir = f.home
	cmd.Stdin = strings.NewReader(input)
	// Detach the controlling terminal too: the interactive installer must not
	// read from the developer's /dev/tty when the test supplies empty stdin.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = []string{
		"HOME=" + f.home, "AMUX_HOME=" + f.amuxHome, "PATH=" + f.path, "SHELL=/bin/bash",
		"LC_ALL=C", "TERM=dumb", "NO_COLOR=1", "PUBLIC_INSTALL_HELPER=1",
		"PUBLIC_INSTALL_TEST_DIR=" + f.dir,
		"PUBLIC_INSTALL_BINARY_SOURCE=" + f.source, "TMPDIR=" + f.dir,
		"PUBLIC_INSTALL_REAL_AMUX_HOME=" + f.amuxHome,
		// The helper process re-execs this test binary; keep the fixture's
		// AMUX_HOME instead of TestMain's isolated one.
		amuxhometest.PreserveEnvVar + "=1",
	}
	if f.downloadBase != "" {
		cmd.Env = append(cmd.Env, "AMUX_RELEASE_BASE_URL="+f.downloadBase)
	}
	output, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		f.t.Fatalf("installer did not finish: %v\n%s", ctx.Err(), output)
	}
	log, err := os.ReadFile(f.log)
	if err != nil {
		f.t.Fatal(err)
	}
	var commands [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if line == "" {
			continue
		}
		var command []string
		if err := json.Unmarshal([]byte(line), &command); err != nil {
			f.t.Fatalf("decode command %q: %v", line, err)
		}
		commands = append(commands, command)
	}
	data, err = os.ReadFile(filepath.Join(f.dir, "state.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	if err := json.Unmarshal(data, &f.state); err != nil {
		f.t.Fatal(err)
	}
	return string(output), commands, runErr
}

func TestPublicInstallScriptDownloadsAndVerifiesRelease(t *testing.T) {
	t.Parallel()
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt=%v", corrupt), func(t *testing.T) {
			t.Parallel()
			f := newPublicInstallFixture(t, "codex")
			for _, name := range []string{"curl", "tar", "gzip", "shasum"} {
				utility, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(utility, filepath.Join(f.path, name)); err != nil {
					t.Fatal(err)
				}
			}
			root := filepath.Join(f.dir, "downloads")
			f.downloadBase = "file://" + root
			f.write(filepath.Join(root, "latest", "download", "version.txt"), "v1.2.3\n", 0644)
			name := "amux_v1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH
			payload := filepath.Join(f.dir, name)
			if err := os.MkdirAll(payload, 0755); err != nil {
				t.Fatal(err)
			}
			for _, binary := range installBinaryNames {
				if err := copyFileForInstallTest(filepath.Join(f.source, binary), filepath.Join(payload, binary), 0755); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(root, "download", "v1.2.3", name+".tar.gz")
			if err := os.MkdirAll(filepath.Dir(archive), 0755); err != nil {
				t.Fatal(err)
			}
			writeTarGz(t, archive, f.dir, []string{name})
			data, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			if corrupt {
				digest[0] ^= 1
			}
			f.write(archive+".sha256", fmt.Sprintf("%x  %s.tar.gz\n", digest, name), 0644)
			output, commands, err := f.run("", "--runtime", "codex", "--no-modify-path")
			if corrupt {
				if err == nil || !strings.Contains(output, "checksum mismatch") || len(commands) != 0 {
					t.Fatalf("corrupt release: err=%v output=%s commands=%v", err, output, commands)
				}
			} else {
				if err != nil {
					t.Fatalf("release install: %v\n%s", err, output)
				}
				f.requireStagedInstall(commands, false)
			}
		})
	}
}

func publicInstallCommandIndex(commands [][]string, prefix ...string) int {
	for i, command := range commands {
		if len(command) >= len(prefix) && reflect.DeepEqual(command[:len(prefix)], prefix) {
			return i
		}
	}
	return -1
}

func requirePublicInstallCommand(t *testing.T, commands [][]string, want ...string) {
	t.Helper()
	for _, command := range commands {
		if reflect.DeepEqual(command, want) {
			return
		}
	}
	t.Fatalf("missing command %q; commands = %q", want, commands)
}

func (f *publicInstallFixture) requireStagedInstall(commands [][]string, updateShell bool) {
	f.t.Helper()
	want := []string{"install", "--source", f.state.InstallSource, "--force"}
	if updateShell {
		want = append(want, "--update-shell")
	}
	requirePublicInstallCommand(f.t, commands, want...)
	if f.state.InstallSource == "" || f.state.InstallSource == f.source || strings.HasPrefix(f.state.InstallSource, f.amuxHome+string(os.PathSeparator)) {
		f.t.Fatalf("install source was not safely staged outside AMUX_HOME: %q", f.state.InstallSource)
	}
	if _, err := os.Stat(f.state.InstallSource); !os.IsNotExist(err) {
		f.t.Fatalf("installer did not clean up its staging directory: %v", err)
	}
}

func TestPublicInstallScriptSelectsMatchingBuiltinProfiles(t *testing.T) {
	t.Parallel()
	for _, runtime := range []string{"codex", "claude", "pi"} {
		t.Run(runtime, func(t *testing.T) {
			f := newPublicInstallFixture(t, runtime)
			args := []string{"--no-modify-path"}
			if runtime == "claude" {
				args = append(args, "--claude-sidecar", f.sidecar())
			}
			output, commands, err := f.run("", args...)
			if err != nil {
				t.Fatalf("install: %v\n%s\ncommands=%q", err, output, commands)
			}
			requirePublicInstallCommand(t, commands, "agent", "show", "builtin/"+runtime)
			requirePublicInstallCommand(t, commands, "agent", "doctor", "builtin/"+runtime, "--runtime", runtime)
			f.requireStagedInstall(commands, false)
			requirePublicInstallCommand(t, commands, "setup", "local", "--no-start")
			requirePublicInstallCommand(t, commands, "system", "config", "set", "bridge.feishu.workspace_defaults.agent", "builtin/"+runtime)
			if publicInstallCommandIndex(commands, "agent", "show") > publicInstallCommandIndex(commands, "install") {
				t.Fatal("profile validation must precede installation")
			}
			if publicInstallCommandIndex(commands, "bridge") >= 0 || publicInstallCommandIndex(commands, "session") >= 0 {
				t.Fatalf("CLI-only install contacted Feishu or created a session: %q", commands)
			}
			if publicInstallCommandIndex(commands, "system", "stop") >= 0 || publicInstallCommandIndex(commands, "agent", "pack", "autosync", "stop") >= 0 {
				t.Fatalf("fresh installation should not stop another installation: %q", commands)
			}
			if runtime == "claude" {
				for _, path := range []string{"dist/index.js", "node_modules/@anthropic-ai/claude-agent-sdk/index.js", "package.json"} {
					if _, err := os.Stat(filepath.Join(f.amuxHome, "sidecars", "claude-sdk", path)); err != nil {
						t.Fatalf("missing installed sidecar file %s: %v", path, err)
					}
				}
				info, err := os.Stat(filepath.Join(f.amuxHome, "sidecars", "claude-sdk", "dist", "index.js"))
				if err != nil || info.Mode()&0o111 == 0 {
					t.Fatalf("sidecar entrypoint not executable: %v, %v", info, err)
				}
			}
		})
	}
}

func TestPublicInstallScriptRuntimeSelection(t *testing.T) {
	t.Parallel()
	for _, selection := range []string{"2\n", "pi\n"} {
		t.Run(strings.TrimSpace(selection), func(t *testing.T) {
			f := newPublicInstallFixture(t, "codex", "pi")
			output, commands, err := f.run(selection)
			if err != nil {
				t.Fatalf("select runtime: %v\n%s", err, output)
			}
			requirePublicInstallCommand(t, commands, "agent", "show", "builtin/pi")
			f.requireStagedInstall(commands, true)
		})
	}
	t.Run("provider alias", func(t *testing.T) {
		f := newPublicInstallFixture(t, "codex", "pi")
		output, commands, err := f.run("", "--provider", "pi", "--model", "provider/model")
		if err != nil {
			t.Fatalf("provider alias: %v\n%s", err, output)
		}
		requirePublicInstallCommand(t, commands, "agent", "doctor", "builtin/pi", "--runtime", "pi", "--model", "provider/model")
	})
}

func TestPublicInstallScriptCleanFreshInstallWithoutOptionalHelper(t *testing.T) {
	t.Parallel()
	f := newPublicInstallFixture(t, "codex")
	if err := os.Remove(filepath.Join(f.source, "amux-isolation-helper")); err != nil {
		t.Fatal(err)
	}
	output, commands, err := f.run("", "--clean")
	if err != nil {
		t.Fatalf("fresh clean install without optional helper: %v\n%s\ncommands=%q", err, output, commands)
	}
	f.requireStagedInstall(commands, true)
	for _, forbidden := range [][]string{{"system", "stop"}, {"system", "uninstall"}, {"agent", "pack", "autosync", "stop"}} {
		if publicInstallCommandIndex(commands, forbidden...) >= 0 {
			t.Fatalf("fresh install stopped or purged nonexistent installation: %q", commands)
		}
	}
	for _, name := range []string{"amux", "amuxd", "amuxlet"} {
		if _, err := os.Stat(filepath.Join(f.amuxHome, "bin", name)); err != nil {
			t.Fatalf("missing installed %s: %v", name, err)
		}
	}
}

func TestPublicInstallScriptRejectsInvalidInputBeforeInstall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		runtimes []string
		args     []string
		profile  bool
	}{
		{name: "no runtimes"},
		{name: "multiple without selection", runtimes: []string{"codex", "pi"}},
		{name: "missing selected runtime", runtimes: []string{"codex"}, args: []string{"--runtime", "pi"}},
		{name: "unknown runtime", runtimes: []string{"codex"}, args: []string{"--runtime", "unknown-agent"}},
		{name: "mismatched profile", runtimes: []string{"codex", "pi"}, args: []string{"--runtime", "codex", "--agent-profile", "builtin/pi"}},
		{name: "nonexistent default profile", runtimes: []string{"codex"}, args: []string{"--agent-profile", "default"}},
		{name: "conflicting runtime aliases", runtimes: []string{"codex", "pi"}, args: []string{"--runtime", "codex", "--provider", "pi"}},
		{name: "invalid interaction mode", runtimes: []string{"codex"}, args: []string{"--feishu", "--interaction-mode", "invalid"}},
		{name: "missing flag value", runtimes: []string{"codex"}, args: []string{"--runtime"}},
		{name: "missing value before flag", runtimes: []string{"codex"}, args: []string{"--model", "--feishu"}},
		{name: "unknown option", runtimes: []string{"codex"}, args: []string{"--unknown-option"}},
		{name: "conflicting source modes", runtimes: []string{"codex"}, args: []string{"--from-source"}},
		{name: "Feishu option without Feishu", runtimes: []string{"codex"}, args: []string{"--interaction-mode", "thread"}},
		{name: "missing source profile", runtimes: []string{"codex"}, profile: true},
		{name: "missing Claude sidecar", runtimes: []string{"claude"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPublicInstallFixture(t, tt.runtimes...)
			f.state.RejectProfile = tt.profile
			output, commands, err := f.run("", tt.args...)
			if err == nil {
				t.Fatalf("invalid input succeeded:\n%s\ncommands=%q", output, commands)
			}
			if publicInstallCommandIndex(commands, "install") >= 0 {
				t.Fatalf("invalid input mutated installation: %q", commands)
			}
			if _, err := os.Stat(f.amuxHome); !os.IsNotExist(err) {
				t.Fatalf("invalid input created AMUX_HOME: %v", err)
			}
			if tt.name == "multiple without selection" && !strings.Contains(output, "--runtime") {
				t.Fatalf("unattended failure should explain --runtime:\n%s", output)
			}
		})
	}
}

func TestPublicInstallScriptSingletonProvisioningAndReuse(t *testing.T) {
	t.Parallel()
	f := newPublicInstallFixture(t, "codex")
	cwd := filepath.Join(f.dir, "project with spaces")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"--feishu", "--feishu-profile", "work", "--session-name", "work-main", "--session-cwd", cwd, "--model", "gpt-custom"}
	output, commands, err := f.run("", args...)
	if err != nil {
		t.Fatalf("singleton onboarding: %v\n%s\ncommands=%q", err, output, commands)
	}
	requirePublicInstallCommand(t, commands, "session", "run", "builtin/codex", "-n", "work-main", "--on", "local", "--runtime", "codex", "--cwd", cwd, "--model", "gpt-custom")
	requirePublicInstallCommand(t, commands, "bridge", "feishu", "bot-session", "bind", "--profile", "work", "runtime-laptop/work-main")
	setup := publicInstallCommandIndex(commands, "bridge", "feishu", "setup")
	status := publicInstallCommandIndex(commands, "bridge", "feishu", "bot-session", "status")
	run := publicInstallCommandIndex(commands, "session", "run")
	bind := publicInstallCommandIndex(commands, "bridge", "feishu", "bot-session", "bind")
	if setup < 0 || status < setup || run < status || bind < run {
		t.Fatalf("provision/status/session/bind order violated: %q", commands)
	}
	if f.state.Binding != "runtime-laptop/work-main" || f.state.Mode != "singleton" {
		t.Fatalf("incorrect final binding: %+v", f.state)
	}
	output, commands, err = f.run("", args...)
	if err != nil {
		t.Fatalf("repeat onboarding: %v\n%s\ncommands=%q", err, output, commands)
	}
	requirePublicInstallOrder(t, commands, []string{"agent", "doctor"}, []string{"system", "stop"}, []string{"install"}, []string{"system", "start"})
	f.requireStagedInstall(commands, true)
	for _, prefix := range [][]string{{"setup", "local"}, {"bridge", "feishu", "setup"}, {"session", "run"}} {
		if publicInstallCommandIndex(commands, prefix...) >= 0 {
			t.Fatalf("repeat onboarding needlessly reconfigured %q: %q", prefix, commands)
		}
	}
}

func TestPublicInstallScriptReinstallUsesDefaultRuntimeID(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing=%v", missing), func(t *testing.T) {
			f := newPublicInstallFixture(t, "codex")
			f.seedExisting(true, true)
			f.state.Configured, f.state.SystemRunning = true, true
			f.state.MissingRuntimeID, f.state.EmptyRuntimeID = missing, !missing
			f.write(filepath.Join(f.path, "hostname"), "#!/bin/bash\necho runtime-laptop\n", 0o755)
			output, commands, err := f.run("", "--feishu")
			if err != nil {
				t.Fatalf("reinstall with default Runtime ID: %v\n%s", err, output)
			}
			requirePublicInstallCommand(t, commands, "bridge", "feishu", "bot-session", "bind", "--profile", "default", "runtime-laptop/default-main")
		})
	}
}

func TestPublicInstallScriptCleanReprovisionsFeishu(t *testing.T) {
	t.Parallel()
	f := newPublicInstallFixture(t, "pi")
	if output, _, err := f.run("", "--feishu"); err != nil {
		t.Fatalf("initial install: %v\n%s", err, output)
	}
	output, commands, err := f.run("", "--clean", "--feishu")
	if err != nil {
		t.Fatalf("clean reinstall: %v\n%s", err, output)
	}
	requirePublicInstallOrder(t, commands, []string{"system", "uninstall"}, []string{"setup", "local"}, []string{"bridge", "feishu", "setup"}, []string{"session", "run"}, []string{"bridge", "feishu", "bot-session", "bind"})
	if len(f.state.Sessions) != 1 || !f.state.Configured || f.state.Binding != "runtime-laptop/default-main" {
		t.Fatalf("clean reinstall failed to create a fresh configured Session: %+v", f.state)
	}
}

func TestPublicInstallScriptStopsBeforeSessionsOnFeishuFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"setup canceled", "status error"} {
		t.Run(failure, func(t *testing.T) {
			f := newPublicInstallFixture(t, "pi")
			f.state.CancelSetup = failure == "setup canceled"
			f.state.Configured = failure == "status error"
			f.state.StatusError = failure == "status error"
			output, commands, err := f.run("", "--feishu")
			if err == nil {
				t.Fatalf("%s reported success:\n%s", failure, output)
			}
			if publicInstallCommandIndex(commands, "session") >= 0 || publicInstallCommandIndex(commands, "bridge", "feishu", "bot-session", "bind") >= 0 {
				t.Fatalf("failed Feishu setup created or bound a session: %q", commands)
			}
		})
	}
}

func TestPublicInstallScriptChecksFinalBinding(t *testing.T) {
	t.Parallel()
	f := newPublicInstallFixture(t, "codex")
	f.state.Configured, f.state.IgnoreBind = true, true
	output, commands, err := f.run("", "--feishu")
	if err == nil {
		t.Fatalf("bind exit 0 without active binding reported success:\n%s", output)
	}
	bind := publicInstallCommandIndex(commands, "bridge", "feishu", "bot-session", "bind")
	if bind < 0 || publicInstallCommandIndex(commands[bind+1:], "bridge", "feishu", "bot-session", "status") < 0 {
		t.Fatalf("did not verify status after binding: %q", commands)
	}
}

func TestPublicInstallScriptStopsWhenAgentDoctorFails(t *testing.T) {
	t.Parallel()
	f := newPublicInstallFixture(t, "pi")
	f.state.RejectDoctor = true
	output, commands, err := f.run("", "--feishu")
	if err == nil {
		t.Fatalf("failed agent doctor reported success:\n%s", output)
	}
	for _, command := range [][]string{{"setup"}, {"system", "start"}, {"system", "restart"}, {"bridge"}, {"session"}} {
		if publicInstallCommandIndex(commands, command...) >= 0 {
			t.Fatalf("agent doctor failure started onboarding: %q", commands)
		}
	}
}

func TestPublicInstallScriptPreservesConflictingSessions(t *testing.T) {
	t.Parallel()
	for _, conflict := range []string{"binding", "remote binding with same name", "profile", "runtime", "archived"} {
		t.Run(conflict, func(t *testing.T) {
			f := newPublicInstallFixture(t, "codex")
			f.state.Configured = true
			f.state.Mode = "singleton"
			f.state.Profile, f.state.Runtime = "builtin/codex", "codex"
			session := publicInstallSession{Name: "default-main", Runtime: "codex", Profile: "builtin/codex", Cwd: f.home}
			switch conflict {
			case "binding":
				f.state.Binding = "remote/existing"
			case "remote binding with same name":
				f.state.Binding = "remote/default-main"
			case "profile":
				session.Profile = "builtin/pi"
			case "runtime":
				session.Runtime = "pi"
			case "archived":
				session.State = "archived"
			}
			f.state.Sessions = []publicInstallSession{session}
			output, commands, err := f.run("", "--feishu", "--session-name", "default-main")
			if err == nil {
				t.Fatalf("conflicting %s reported success:\n%s", conflict, output)
			}
			if publicInstallCommandIndex(commands, "session", "run") >= 0 || publicInstallCommandIndex(commands, "bridge", "feishu", "bot-session", "bind") >= 0 {
				t.Fatalf("conflicting session overwritten: %q", commands)
			}
		})
	}
}

func TestPublicInstallScriptReusesCompatibleUnboundSession(t *testing.T) {
	t.Parallel()
	f := newPublicInstallFixture(t, "pi")
	f.state.Configured = true
	f.state.Sessions = []publicInstallSession{{Name: "default-main", Runtime: "pi", Profile: "builtin/pi", Cwd: f.home}}
	output, commands, err := f.run("", "--feishu", "--session-name", "default-main")
	if err != nil {
		t.Fatalf("reuse session: %v\n%s\ncommands=%q", err, output, commands)
	}
	if publicInstallCommandIndex(commands, "session", "run") >= 0 {
		t.Fatalf("existing compatible session duplicated: %q", commands)
	}
	requirePublicInstallCommand(t, commands, "bridge", "feishu", "bot-session", "bind", "--profile", "default", "runtime-laptop/default-main")
}

func TestPublicInstallScriptThreadSavesLaunchDefaults(t *testing.T) {
	t.Parallel()
	f := newPublicInstallFixture(t, "pi")
	f.state.Configured = true
	f.state.Mode, f.state.Binding = "singleton", "local/existing"
	f.state.Profile, f.state.Runtime = "builtin/pi", "pi"
	output, commands, err := f.run("", "--feishu", "--interaction-mode", "thread", "--model", "provider/model")
	if err != nil {
		t.Fatalf("thread onboarding: %v\n%s\ncommands=%q", err, output, commands)
	}
	if publicInstallCommandIndex(commands, "session") >= 0 || publicInstallCommandIndex(commands, "bridge", "feishu", "bot-session", "bind") >= 0 {
		t.Fatalf("thread onboarding created a singleton session: %q", commands)
	}
	requirePublicInstallCommand(t, commands, "bridge", "feishu", "bot-session", "off", "--profile", "default")
	setup := publicInstallCommandIndex(commands, "bridge", "feishu", "setup")
	if setup < 0 {
		t.Fatalf("thread setup missing: %q", commands)
	}
	for flag, value := range map[string]string{"--interaction-mode": "thread", "--default-agent": "builtin/pi", "--default-runtime": "pi", "--default-model": "provider/model"} {
		if got := publicInstallFlag(commands[setup], flag); got != value {
			t.Fatalf("thread %s = %q, want %q: %q", flag, got, value, commands[setup])
		}
	}
	if f.state.Mode != "thread" || f.state.Binding != "" {
		t.Fatalf("thread did not disable singleton: %+v", f.state)
	}
}

func (f *publicInstallFixture) seedExisting(binary, config bool) map[string]string {
	f.t.Helper()
	if binary {
		data, err := os.ReadFile(filepath.Join(f.source, "amux"))
		if err != nil {
			f.t.Fatal(err)
		}
		f.write(filepath.Join(f.amuxHome, "bin", "amux"), string(data), 0o755)
	}
	files := map[string]string{
		"runtime.db": "sessions", "bridge.db": "bindings", "schedule.db": "schedules",
		"secrets/feishu": "credential", "agent-packs/registry.json": "registered packs",
		"sessions/history.jsonl": "conversation", "keep-user-data": "unrecognized user file",
	}
	if config {
		files["config.toml"] = "# existing customized config\n"
	}
	for path, content := range files {
		f.write(filepath.Join(f.amuxHome, path), content, 0o600)
	}
	return files
}

func requirePublicInstallOrder(t *testing.T, commands [][]string, prefixes ...[]string) {
	t.Helper()
	previous := -1
	for _, prefix := range prefixes {
		index := publicInstallCommandIndex(commands, prefix...)
		if index <= previous {
			t.Fatalf("missing or out-of-order %q in %q", prefix, commands)
		}
		previous = index
	}
}

func TestPublicInstallScriptReinstallPreservesData(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"running", "stopped", "config without binary", "binary without config", "metadata only", "PID only", "socket only"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPublicInstallFixture(t, "codex")
			binary, config := true, true
			switch scenario {
			case "config without binary":
				binary = false
			case "binary without config":
				config = false
			case "metadata only", "PID only", "socket only":
				binary, config = false, false
			}
			files := map[string]string{}
			if binary || config {
				files = f.seedExisting(binary, config)
			}
			switch scenario {
			case "metadata only":
				f.write(filepath.Join(f.amuxHome, "install.json"), "{}", 0o600)
			case "PID only":
				f.write(filepath.Join(f.amuxHome, "amuxd.pid"), "999999", 0o600)
			case "socket only":
				f.write(filepath.Join(f.amuxHome, "runtime.sock"), "stale socket marker", 0o600)
			}
			f.state.SystemRunning = scenario == "running"
			f.state.AutoSyncRunning = scenario == "running"
			output, commands, err := f.run("")
			if err != nil {
				t.Fatalf("reinstall %s: %v\n%s\ncommands=%q", scenario, err, output, commands)
			}
			requirePublicInstallOrder(t, commands, []string{"agent", "doctor"}, []string{"agent", "pack", "autosync", "status"}, []string{"agent", "pack", "autosync", "stop"}, []string{"system", "stop"}, []string{"install"}, []string{"system", "start"})
			f.requireStagedInstall(commands, true)
			if publicInstallCommandIndex(commands, "system", "uninstall") >= 0 || publicInstallCommandIndex(commands, "system", "restart") >= 0 {
				t.Fatalf("preserving reinstall should stop/install/start without purge: %q", commands)
			}
			if got := publicInstallCommandIndex(commands, "setup", "local") >= 0; got == config {
				t.Fatalf("local setup called=%v with existing config=%v", got, config)
			}
			for path, want := range files {
				data, err := os.ReadFile(filepath.Join(f.amuxHome, path))
				if err != nil || string(data) != want {
					t.Fatalf("reinstall lost %s: %q, %v", path, data, err)
				}
			}
			if !f.state.SystemRunning || f.state.AutoSyncRunning != (scenario == "running") {
				t.Fatalf("incorrect final process state: %+v", f.state)
			}
			wantStopCLI := f.state.DoctorExecutable
			if binary {
				wantStopCLI = filepath.Join(f.amuxHome, "bin", "amux")
			}
			if f.state.StopExecutable != wantStopCLI {
				t.Fatalf("stop executable=%q, want %q", f.state.StopExecutable, wantStopCLI)
			}
			if f.state.AutoSyncRunning {
				requirePublicInstallOrder(t, commands, []string{"system", "start"}, []string{"agent", "pack", "autosync", "start"})
			} else if publicInstallCommandIndex(commands, "agent", "pack", "autosync", "start") >= 0 {
				t.Fatalf("reinstall started previously stopped autosync: %q", commands)
			}
		})
	}
}

func TestPublicInstallScriptCleanStagesSourcesBeforePurge(t *testing.T) {
	t.Parallel()
	for _, runtime := range []string{"codex", "claude"} {
		t.Run(runtime, func(t *testing.T) {
			f := newPublicInstallFixture(t, runtime)
			files := f.seedExisting(true, true)
			f.state.SystemRunning, f.state.AutoSyncRunning = true, true
			args := []string{"--clean"}
			if runtime == "claude" {
				sdk := f.sidecar()
				inside := filepath.Join(f.amuxHome, "sidecars", "claude-sdk")
				if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(sdk, inside); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--claude-sidecar", inside)
			}
			insideSource := filepath.Join(f.amuxHome, "release binaries")
			if err := os.Rename(f.source, insideSource); err != nil {
				t.Fatal(err)
			}
			f.source = insideSource
			output, commands, err := f.run("", args...)
			if err != nil {
				t.Fatalf("clean %s install: %v\n%s\ncommands=%q", runtime, err, output, commands)
			}
			requirePublicInstallOrder(t, commands, []string{"agent", "doctor"}, []string{"system", "stop"}, []string{"system", "uninstall", "--yes", "--purge"}, []string{"install"}, []string{"setup", "local"}, []string{"system", "start"})
			f.requireStagedInstall(commands, true)
			if f.state.PurgeExecutable != f.state.DoctorExecutable {
				t.Fatalf("purge did not use validated staged executable: %q vs %q", f.state.PurgeExecutable, f.state.DoctorExecutable)
			}
			if f.state.AutoSyncRunning || publicInstallCommandIndex(commands, "agent", "pack", "autosync", "start") >= 0 {
				t.Fatalf("clean install restored autosync after deleting its registered Packs: %q", commands)
			}
			for path, old := range files {
				data, err := os.ReadFile(filepath.Join(f.amuxHome, path))
				if path == "config.toml" {
					if err != nil || string(data) == old {
						t.Fatalf("clean did not generate fresh configuration: %q, %v", data, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("clean retained %s: %q, %v", path, data, err)
				}
			}
			for _, name := range []string{"amux", "amuxd", "amuxlet"} {
				if _, err := os.Stat(filepath.Join(f.amuxHome, "bin", name)); err != nil {
					t.Fatalf("clean lost staged binary %s: %v", name, err)
				}
			}
			if runtime == "claude" {
				for _, path := range []string{"dist/index.js", "package.json", "node_modules/@anthropic-ai/claude-agent-sdk/index.js"} {
					if _, err := os.Stat(filepath.Join(f.amuxHome, "sidecars", "claude-sdk", path)); err != nil {
						t.Fatalf("clean lost staged Claude SDK %s: %v", path, err)
					}
				}
			}
		})
	}
}

func TestPublicInstallScriptReinstallFailsBeforeReplacement(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"preflight", "autosync status", "autosync status error", "autosync stop", "system stop"} {
		t.Run(failure, func(t *testing.T) {
			f := newPublicInstallFixture(t, "pi")
			files := f.seedExisting(true, true)
			f.state.SystemRunning, f.state.AutoSyncRunning = true, true
			f.state.RejectDoctor = failure == "preflight"
			f.state.RejectAutoSyncStatus = failure == "autosync status"
			f.state.AutoSyncStatusError = failure == "autosync status error"
			f.state.RejectAutoSyncStop = failure == "autosync stop"
			f.state.RejectSystemStop = failure == "system stop"
			output, commands, err := f.run("", "--clean")
			if err == nil {
				t.Fatalf("failed %s reported success:\n%s", failure, output)
			}
			if publicInstallCommandIndex(commands, "install") >= 0 || publicInstallCommandIndex(commands, "system", "uninstall") >= 0 {
				t.Fatalf("failure did not prevent replacement or purge: %q", commands)
			}
			if failure == "preflight" && publicInstallCommandIndex(commands, "system", "stop") >= 0 {
				t.Fatalf("preflight failure stopped installed system: %q", commands)
			}
			for path, want := range files {
				data, err := os.ReadFile(filepath.Join(f.amuxHome, path))
				if err != nil || string(data) != want {
					t.Fatalf("failed reinstall lost %s: %q, %v", path, data, err)
				}
			}
		})
	}
}

func TestPublicInstallScriptCleanRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"root", "home", "home ancestor", "home symlink", "system root", "cwd inside purge root"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newPublicInstallFixture(t, "codex")
			args := []string{"--clean"}
			switch scenario {
			case "root":
				f.amuxHome = "/"
			case "home":
				f.amuxHome = f.home
			case "home ancestor":
				f.amuxHome = f.dir
			case "home symlink":
				if err := os.Symlink(f.home, f.amuxHome); err != nil {
					t.Fatal(err)
				}
			case "system root":
				f.amuxHome = "/usr"
			case "cwd inside purge root":
				f.seedExisting(true, true)
				cwd := filepath.Join(f.amuxHome, "workspaces", "project")
				if err := os.MkdirAll(cwd, 0o755); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--feishu", "--session-cwd", cwd)
			}
			output, commands, err := f.run("", args...)
			if err == nil {
				t.Fatalf("unsafe purge path accepted:\n%s", output)
			}
			for _, forbidden := range [][]string{{"install"}, {"system", "uninstall"}, {"system", "stop"}, {"agent", "pack", "autosync", "stop"}} {
				if publicInstallCommandIndex(commands, forbidden...) >= 0 {
					t.Fatalf("unsafe path reached mutation %q: %q", forbidden, commands)
				}
			}
		})
	}
}

func publicInstallFlag(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

// This subprocess double models only commands the onboarding script is allowed
// to call. Unknown commands fail closed; no installed amux or service is used.
func TestPublicInstallScriptHelperProcess(t *testing.T) {
	if os.Getenv("PUBLIC_INSTALL_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	self, args := os.Args[separator+1], os.Args[separator+2:]
	dir := os.Getenv("PUBLIC_INSTALL_TEST_DIR")
	fail := func(format string, values ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", values...)
		os.Exit(9)
	}
	check := func(err error) {
		if err != nil {
			fail("helper: %v", err)
		}
	}
	log, err := os.OpenFile(filepath.Join(dir, "commands.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	check(err)
	check(json.NewEncoder(log).Encode(args))
	check(log.Close())
	var state publicInstallState
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	check(err)
	check(json.Unmarshal(data, &state))
	prefix := func(parts ...string) bool {
		return len(args) >= len(parts) && reflect.DeepEqual(args[:len(parts)], parts)
	}
	amuxHome := os.Getenv("AMUX_HOME")
	guardInstallHome := func() {
		// Fail closed even if a regression reaches a mutating stub with /, HOME
		// or another unsafe path. Only fixture-owned descendants may be changed.
		if !strings.HasPrefix(amuxHome, dir+string(os.PathSeparator)) || amuxHome == filepath.Join(dir, "home") {
			fail("unsafe test AMUX_HOME: %q", amuxHome)
		}
	}
	switch {
	case prefix("agent", "show"):
		if len(args) != 3 || !strings.HasPrefix(args[2], "builtin/") || state.RejectProfile {
			fail("profile does not exist")
		}
	case prefix("install"):
		guardInstallHome()
		source := publicInstallFlag(args, "--source")
		if source != filepath.Dir(self) {
			fail("install source must be the validated source executable's directory")
		}
		if source == os.Getenv("PUBLIC_INSTALL_BINARY_SOURCE") || strings.HasPrefix(source, amuxHome+string(os.PathSeparator)) {
			fail("installation must use a staged source outside AMUX_HOME")
		}
		check(os.MkdirAll(filepath.Join(amuxHome, "bin"), 0o755))
		// Match the real CLI installer: the privileged isolation helper has a
		// separate installation flow and is not installed by this command.
		for _, name := range []string{"amux", "amuxd", "amuxlet"} {
			data, err := os.ReadFile(filepath.Join(source, name))
			check(err)
			check(os.WriteFile(filepath.Join(amuxHome, "bin", name), data, 0o755))
		}
		state.InstallSource = source
	case prefix("agent", "doctor"):
		if len(args) < 5 || args[2] != "builtin/"+publicInstallFlag(args, "--runtime") || state.RejectDoctor {
			fail("native agent doctor failed")
		}
		if strings.HasPrefix(self, os.Getenv("PUBLIC_INSTALL_REAL_AMUX_HOME")+string(os.PathSeparator)) || filepath.Dir(self) == os.Getenv("PUBLIC_INSTALL_BINARY_SOURCE") {
			fail("agent preflight must run staged CLI before replacing or stopping installed binaries")
		}
		if publicInstallFlag(args, "--runtime") == "claude" {
			for _, path := range []string{"dist/index.js", "package.json", "node_modules/@anthropic-ai/claude-agent-sdk/index.js"} {
				if _, err := os.Stat(filepath.Join(amuxHome, "sidecars", "claude-sdk", path)); err != nil {
					fail("Claude preflight missing staged SDK %s: %v", path, err)
				}
			}
		}
		state.DoctorExecutable = self
	case prefix("agent", "pack", "autosync", "status"):
		if state.RejectAutoSyncStatus {
			fail("cannot inspect autosync")
		}
		if state.AutoSyncRunning {
			fmt.Println("state: running\npid: 1234")
		} else {
			fmt.Println("state: stopped")
		}
		if state.AutoSyncStatusError {
			fmt.Println("error: PID ownership cannot be verified")
		}
	case prefix("agent", "pack", "autosync", "stop"):
		if state.RejectAutoSyncStop {
			fail("autosync did not stop")
		}
		state.AutoSyncRunning = false
	case prefix("agent", "pack", "autosync", "start"):
		state.AutoSyncRunning = true
	case prefix("system", "stop"):
		if !reflect.DeepEqual(args, []string{"system", "stop", "--force"}) || state.RejectSystemStop {
			fail("system did not stop")
		}
		if state.AutoSyncRunning {
			fail("autosync must stop before the managed system")
		}
		state.SystemRunning, state.StopExecutable = false, self
	case prefix("system", "uninstall"):
		guardInstallHome()
		if !reflect.DeepEqual(args, []string{"system", "uninstall", "--yes", "--purge"}) {
			fail("unexpected uninstall arguments: %q", args)
		}
		if state.SystemRunning || state.AutoSyncRunning || strings.HasPrefix(self, amuxHome+string(os.PathSeparator)) {
			fail("purge must run staged CLI after stopping all old processes")
		}
		check(os.RemoveAll(amuxHome))
		state.PurgeExecutable = self
		state.Configured, state.Mode, state.Binding = false, "", ""
		state.Profile, state.Runtime, state.Cwd, state.Sessions = "", "", "", nil
	case prefix("setup", "local"):
		if !reflect.DeepEqual(args, []string{"setup", "local", "--no-start"}) {
			fail("local setup must not start services")
		}
		check(os.WriteFile(filepath.Join(amuxHome, "config.toml"), []byte("# isolated configuration\n"), 0o600))
	case prefix("system", "config", "set", "bridge.feishu.workspace_defaults.agent"):
		if len(args) != 5 || !strings.HasPrefix(args[4], "builtin/") {
			fail("unexpected default profile")
		}
	case prefix("system", "config", "get", "runtime.id"):
		// Match a normal setup where the Runtime is identified by its hostname,
		// while CLI Session commands also support the client-side local alias.
		if state.MissingRuntimeID {
			fail("config key runtime.id is not set")
		}
		if !state.EmptyRuntimeID {
			fmt.Println("runtime-laptop")
		}
	case prefix("system", "start"), prefix("system", "restart"), prefix("system", "doctor"), prefix("system", "status"):
		if len(args) != 2 && !(reflect.DeepEqual(args, []string{"system", "restart", "--force"})) {
			fail("unexpected system args: %q", args)
		}
		if prefix("system", "start") || prefix("system", "restart") {
			state.SystemRunning = true
		}
	case prefix("bridge", "feishu", "test"):
		if !state.Configured {
			fail("Feishu profile is not configured")
		}
	case prefix("bridge", "feishu", "setup"):
		if !strings.Contains(strings.Join(args, " "), "--no-start") {
			fail("Feishu setup must defer service start")
		}
		if state.CancelSetup {
			break // QR cancellation can return exit 0 without creating the Profile.
		}
		if publicInstallFlag(args, "--interaction-mode") == "thread" {
			if !strings.Contains(strings.Join(args, " "), "--manual") {
				fail("thread setup must reuse saved credentials in manual mode")
			}
			var input [1]byte
			if _, err := os.Stdin.Read(input[:]); err != io.EOF {
				fail("manual thread setup must receive EOF")
			}
			state.Mode = "thread"
			state.Profile = publicInstallFlag(args, "--default-agent")
		} else {
			state.Mode = "singleton"
		}
		state.Configured = true
	case prefix("bridge", "feishu", "bot-session", "status"):
		if !state.Configured {
			fail("status queried before Feishu provisioning completed")
		}
		enabled := "no"
		if state.Binding != "" {
			enabled = "yes"
		}
		fmt.Printf("Feishu bot-session:\n  profile:      %s\n  mode:         %s\n  default_agent: %s\n  enabled:      %s\n  session:      %s\n  agent:        %s\n  agent_type:   %s\n  cwd:          %s\n", publicInstallFlag(args, "--profile"), state.Mode, state.Profile, enabled, state.Binding, state.Profile, state.Runtime, state.Cwd)
		if state.StatusError {
			fmt.Println("  error:        runtime unavailable") // The CLI exits 0 here.
		}
	case prefix("bridge", "feishu", "bot-session", "bind"):
		if len(args) != 7 || args[4] != "--profile" || !strings.HasPrefix(args[6], "runtime-laptop/") {
			fail("unexpected bind arguments: %q", args)
		}
		if state.IgnoreBind {
			break
		}
		state.Binding, state.Mode = args[6], "singleton"
		for _, session := range state.Sessions {
			if state.Binding == "runtime-laptop/"+session.Name {
				state.Profile, state.Runtime, state.Cwd = session.Profile, session.Runtime, session.Cwd
			}
		}
	case prefix("bridge", "feishu", "bot-session", "off"):
		state.Binding = ""
	case prefix("session", "list", "local"):
		if len(args) != 3 {
			fail("unexpected session list arguments: %q", args)
		}
		fmt.Println("NAME  TYPE  PROFILE  STATE  PID  TARGET  CAPS  IDLE_TIMEOUT  LAST_ACTIVITY  SLEEP_REASON  AUTO_SLEEP  CWD  CREATED  UPDATED")
		for _, session := range state.Sessions {
			if session.State == "" {
				session.State = "running"
			}
			if session.Target == "" {
				session.Target = "local"
			}
			fmt.Printf("%s  %s  %s  %s  1234  %s  -  -  -  -  no  %s  -  -\n", session.Name, session.Runtime, session.Profile, session.State, session.Target, session.Cwd)
		}
	case prefix("session", "set-model"):
		if len(args) != 4 {
			fail("unexpected set-model arguments: %q", args)
		}
	case prefix("session", "run"):
		if len(args) < 3 || publicInstallFlag(args, "--on") != "local" || args[2] != "builtin/"+publicInstallFlag(args, "--runtime") {
			fail("invalid session launch: %q", args)
		}
		state.Sessions = append(state.Sessions, publicInstallSession{Name: publicInstallFlag(args, "-n"), Runtime: publicInstallFlag(args, "--runtime"), Profile: args[2], Cwd: publicInstallFlag(args, "--cwd")})
	default:
		fail("unexpected amux command: %q", args)
	}
	data, err = json.Marshal(state)
	check(err)
	check(os.WriteFile(filepath.Join(dir, "state.json"), data, 0o600))
	os.Exit(0)
}
