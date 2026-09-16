//go:build linux

package isolation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
)

func TestHelperUnprivilegedLifecycleAndSCMRights(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := HelperConfig{
		SocketPath:           filepath.Join(root, "helper.sock"),
		StateDir:             filepath.Join(root, "state"),
		PrincipalRoot:        filepath.Join(root, "principals"),
		ServiceUID:           os.Getuid(),
		ServiceGID:           os.Getgid(),
		UIDRangeStart:        max(os.Getuid(), 1),
		UIDRangeEnd:          max(os.Getuid(), 1) + 10,
		AllowUnprivileged:    true,
		SkipAccountProvision: true,
	}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)

	mapping := AccountMapping{
		PrincipalID: "d7eaf95f-4c18-43b2-81fd-f5f013ac901d",
		Account:     "amux_0123456789ab",
		UID:         max(os.Getuid(), 1),
		GID:         max(os.Getuid(), 1),
	}
	paths, err := client.PrepareRun(context.Background(), mapping, "exec_test", "launch-test")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := client.Start(context.Background(), mapping, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", "printf helper-ok"},
		Env: []string{"PATH=/usr/bin:/bin"}, Dir: paths.Workspace,
		Context: LaunchContext{LaunchID: "launch-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(handle.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	status, err := handle.Wait(context.Background())
	if err != nil || status.Code != 0 || string(output) != "helper-ok" {
		t.Fatalf("status=%+v output=%q err=%v", status, output, err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not stop")
	}
}

// A provider binary the principal cannot execute has to fail the Start RPC with
// the blocking component named. Spawning it anyway leaves the caller waiting on
// a child that dies with a bare errno on a stderr nobody reads until the broker
// pid-file timeout fires.
func TestHelperStartRejectsUnreachableLaunchBinary(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := HelperConfig{
		SocketPath:           filepath.Join(root, "helper.sock"),
		StateDir:             filepath.Join(root, "state"),
		PrincipalRoot:        filepath.Join(root, "principals"),
		ServiceUID:           os.Getuid(),
		ServiceGID:           os.Getgid(),
		UIDRangeStart:        max(os.Getuid(), 1),
		UIDRangeEnd:          max(os.Getuid(), 1) + 10,
		AllowUnprivileged:    true,
		SkipAccountProvision: true,
	}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)

	mapping := AccountMapping{
		PrincipalID: "d7eaf95f-4c18-43b2-81fd-f5f013ac901d",
		Account:     "amux_0123456789ab",
		UID:         max(os.Getuid(), 1),
		GID:         max(os.Getuid(), 1),
	}
	paths, err := client.PrepareRun(context.Background(), mapping, "exec_test", "launch-unreachable")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "example-agent")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	handle, err := client.Start(context.Background(), mapping, LaunchSpec{
		Path: binary, Env: []string{"PATH=/usr/bin:/bin"}, Dir: paths.Workspace,
		Context: LaunchContext{LaunchID: "launch-unreachable"},
	})
	if err == nil {
		_ = handle.Kill(context.Background())
		_ = handle.Close()
		t.Fatal("start accepted a launch binary the principal cannot execute")
	}
	for _, want := range []string{binary, "not executable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("start error %q does not mention %q", err, want)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not stop")
	}
}

func TestLinuxDACRootE2E(t *testing.T) {
	if os.Getenv("AMUX_LINUX_DAC_E2E") != "1" {
		t.Skip("set AMUX_LINUX_DAC_E2E=1 and run as root")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root E2E must run as root")
	}
	uidStart := 780000
	if raw := os.Getenv("AMUX_TEST_UID_START"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
		uidStart = value
	}
	root, err := os.MkdirTemp("/tmp", "amux-linux-dac-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o711); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := HelperConfig{
		SocketPath:       filepath.Join(root, "helper.sock"),
		StateDir:         filepath.Join(root, "state"),
		PrincipalRoot:    filepath.Join(root, "principals"),
		ServiceUID:       0,
		ServiceGID:       0,
		UIDRangeStart:    uidStart,
		UIDRangeEnd:      uidStart + 20,
		MaterializerPath: os.Getenv("AMUX_TEST_HELPER_BINARY"),
	}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)

	mappingA := AccountMapping{PrincipalID: "024cd073-7c3f-4767-a69e-aad5074cc844", Account: "amux_a024cd0737c3", UID: uidStart, GID: uidStart}
	mappingB := AccountMapping{PrincipalID: "b5239eac-5b97-4d5c-90b4-a27fb6d95185", Account: "amux_bb5239eac5b9", UID: uidStart + 1, GID: uidStart + 1}
	removeLinuxTestAccount(mappingA.Account, mappingA.UID)
	removeLinuxTestAccount(mappingB.Account, mappingB.UID)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		for _, account := range []string{mappingA.Account, mappingB.Account} {
			removeLinuxTestAccount(account, 0)
		}
	})

	pathsA, err := client.PrepareRun(context.Background(), mappingA, "exec_a", "launch-a")
	if err != nil {
		t.Fatal(err)
	}
	pathsB, err := client.PrepareRun(context.Background(), mappingB, "exec_b", "launch-b")
	if err != nil {
		t.Fatal(err)
	}
	bundleRoot := filepath.Join(root, "service-staging")
	if err := os.MkdirAll(filepath.Join(bundleRoot, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundleRoot, "secret"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleRoot, "workspace", "materialized"), []byte("target-uid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleRoot, "secret", "bundle-token"), []byte("bundle-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.MaterializeRun(context.Background(), mappingA, "exec_a", "launch-a", []BundleSource{
		{Source: filepath.Join(bundleRoot, "workspace"), Prefix: "workspace"},
		{Source: filepath.Join(bundleRoot, "secret"), Prefix: "secret"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(pathsA.Workspace, "materialized"),
		filepath.Join(pathsA.Run, "secret", "bundle-token"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if int(stat.Uid) != mappingA.UID {
			t.Fatalf("target-uid materializer owner for %s = %d, want %d", path, stat.Uid, mappingA.UID)
		}
	}
	bundleSecretInfo, err := os.Stat(filepath.Join(pathsA.Run, "secret", "bundle-token"))
	if err != nil {
		t.Fatal(err)
	}
	if bundleSecretInfo.Mode().Perm() != 0o600 {
		t.Fatalf("target-uid materialized secret mode=%v, want 0600", bundleSecretInfo.Mode().Perm())
	}
	fileA := filepath.Join(pathsA.Workspace, "owner-only")
	fileB := filepath.Join(pathsB.Workspace, "owner-only")
	secretCanary := "amux-e2e-canary-7d2c1a"
	secretA := filepath.Join(pathsA.Run, "credential")
	if err := os.WriteFile(fileA, []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("B"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretA, []byte(secretCanary), 0o600); err != nil {
		t.Fatal(err)
	}

	outputA := runHelperShell(t, client, mappingA, "launch-a", pathsA.Workspace,
		fmt.Sprintf("printf 'uid='; id -u; printf 'own='; cat %q; printf ' secret='; cat %q; if cat %q >/dev/null 2>&1; then echo cross=BAD; else echo cross=EACCES; fi", fileA, secretA, fileB))
	if !strings.Contains(outputA, fmt.Sprintf("uid=%d", mappingA.UID)) ||
		!strings.Contains(outputA, "own=A") || !strings.Contains(outputA, "secret="+secretCanary) ||
		!strings.Contains(outputA, "cross=EACCES") {
		t.Fatalf("principal A output:\n%s", outputA)
	}
	outputB := runHelperShell(t, client, mappingB, "launch-b", pathsB.Workspace,
		fmt.Sprintf("printf 'uid='; id -u; printf 'own='; cat %q; if cat %q >/dev/null 2>&1 || cat %q >/dev/null 2>&1; then echo cross=BAD; else echo cross=EACCES; fi", fileB, fileA, secretA))
	if !strings.Contains(outputB, fmt.Sprintf("uid=%d", mappingB.UID)) ||
		!strings.Contains(outputB, "own=B") || !strings.Contains(outputB, "cross=EACCES") {
		t.Fatalf("principal B output:\n%s", outputB)
	}

	pathsA2, err := client.PrepareRun(context.Background(), mappingA, "exec_a2", "launch-a2")
	if err != nil {
		t.Fatal(err)
	}
	outputA2 := runHelperShell(t, client, mappingA, "launch-a2", pathsA2.Workspace,
		fmt.Sprintf("cat %q", fileA))
	if outputA2 != "A" {
		t.Fatalf("same principal could not read durable workspace: %q", outputA2)
	}
	if err := client.RemoveRun(context.Background(), mappingA, "launch-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secretA); !os.IsNotExist(err) {
		t.Fatalf("terminal credential file still exists: %v", err)
	}
	registryBytes, err := os.ReadFile(filepath.Join(cfg.StateDir, "launches.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(registryBytes), secretCanary) {
		t.Fatal("canary secret leaked into helper registry")
	}

	pathsKill, err := client.PrepareRun(context.Background(), mappingA, "exec_kill", "launch-kill")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := client.Start(context.Background(), mappingA, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", "sleep 300"}, Env: []string{"PATH=/usr/bin:/bin"},
		Dir: pathsKill.Workspace, Context: LaunchContext{LaunchID: "launch-kill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Kill(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Signal != "KILLED" && status.Signal != "KILL" {
		t.Fatalf("kill status = %+v", status)
	}
	_ = handle.Close()

	pathsSignal, err := client.PrepareRun(context.Background(), mappingB, "exec_signal", "launch-signal")
	if err != nil {
		t.Fatal(err)
	}
	signalHandle, err := client.Start(context.Background(), mappingB, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", "trap 'exit 42' TERM; echo ready; while :; do sleep 1; done"},
		Env: []string{"PATH=/usr/bin:/bin"}, Dir: pathsSignal.Workspace,
		Context: LaunchContext{LaunchID: "launch-signal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(signalHandle.Stdout()).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("signal probe readiness = %q, %v", line, err)
	}
	if err := signalHandle.Signal(context.Background(), "TERM"); err != nil {
		t.Fatal(err)
	}
	signalStatus, _ := signalHandle.Wait(context.Background())
	if signalStatus.Code != 42 {
		t.Fatalf("signal exit = %+v", signalStatus)
	}
	_ = signalHandle.Close()

	pathsShared, err := client.PrepareRun(context.Background(), mappingA, "exec_shared", "launch-shared")
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Start(context.Background(), mappingA, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", "sleep 300"}, Env: []string{"PATH=/usr/bin:/bin"},
		Dir: pathsShared.Workspace, Context: LaunchContext{LaunchID: "launch-shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Start(context.Background(), mappingA, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", "sleep 300"}, Env: []string{"PATH=/usr/bin:/bin"},
		Dir: pathsShared.Workspace, Context: LaunchContext{LaunchID: "launch-shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.PID() != second.PID() {
		t.Fatalf("idempotent Start returned different pids: %d != %d", first.PID(), second.PID())
	}
	waitResults := make(chan ExitStatus, 2)
	for range 2 {
		go func() {
			status, _ := client.Wait(context.Background(), "launch-shared")
			waitResults <- status
		}()
	}
	if err := client.KillPrincipal(context.Background(), mappingA); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		status := <-waitResults
		if status.Signal == "" {
			t.Fatalf("concurrent wait did not observe signal: %+v", status)
		}
	}
	_ = first.Close()
	_ = second.Close()
	if uidHasProcesses(mappingA.UID) {
		t.Fatalf("process remains for revoked uid %d", mappingA.UID)
	}

	// A status drain (force=false) is read-only: it must report active launches
	// without entering draining, so an install/uninstall guard that aborts on
	// active launches cannot wedge the helper into rejecting new launches.
	active, err := client.DrainStatus(context.Background(), false)
	if err != nil || active != 0 {
		t.Fatalf("drain status active=%d err=%v", active, err)
	}
	if _, err := client.Start(context.Background(), mappingA, LaunchSpec{
		Path: "/bin/true", Dir: pathsShared.Workspace,
		Context: LaunchContext{LaunchID: "launch-after-status-drain"},
	}); err != nil {
		t.Fatalf("status drain must not reject new launches: %v", err)
	}
	// A forced drain (force=true) enters draining and must reject new launches.
	if _, err := client.DrainStatus(context.Background(), true); err != nil {
		t.Fatalf("forced drain: %v", err)
	}
	if _, err := client.Start(context.Background(), mappingA, LaunchSpec{
		Path: "/bin/true", Dir: pathsShared.Workspace,
		Context: LaunchContext{LaunchID: "launch-after-forced-drain"},
	}); err == nil {
		t.Fatal("draining helper accepted a new launch")
	}
}

func TestLinuxDACHardeningRootE2E(t *testing.T) {
	if os.Getenv("AMUX_LINUX_DAC_E2E") != "1" {
		t.Skip("set AMUX_LINUX_DAC_E2E=1 and run as root")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root E2E must run as root")
	}
	wrapper := os.Getenv("AMUX_TEST_HELPER_BINARY")
	if wrapper == "" {
		t.Skip("AMUX_TEST_HELPER_BINARY is required for seccomp re-exec validation")
	}
	uidStart, _ := strconv.Atoi(os.Getenv("AMUX_TEST_UID_START"))
	if uidStart == 0 {
		uidStart = 780000
	}
	uid := uidStart + 10
	root, err := os.MkdirTemp("/tmp", "amux-linux-hardening-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o711); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := HelperConfig{
		SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"),
		PrincipalRoot: filepath.Join(root, "principals"), ServiceUID: 0, ServiceGID: 0,
		UIDRangeStart: uidStart, UIDRangeEnd: uidStart + 20,
		PIDNamespace: true, MountNamespace: true, Seccomp: true, ExecWrapperPath: wrapper,
	}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)
	mapping := AccountMapping{
		PrincipalID: "985193b7-f6de-4b7f-b49c-67ade69bb16f",
		Account:     "amux_c985193b7f6d", UID: uid, GID: uid,
	}
	sibling := AccountMapping{
		PrincipalID: "a7fd033f-bae0-441a-84d0-9f3246528658",
		Account:     "amux_da7fd033fbae0", UID: uid + 1, GID: uid + 1,
	}
	removeLinuxTestAccount(mapping.Account, mapping.UID)
	removeLinuxTestAccount(sibling.Account, sibling.UID)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		removeLinuxTestAccount(mapping.Account, 0)
		removeLinuxTestAccount(sibling.Account, 0)
	})
	if _, err := client.PrepareRun(context.Background(), sibling, "exec_sibling", "launch-sibling"); err != nil {
		t.Fatal(err)
	}
	paths, err := client.PrepareRun(context.Background(), mapping, "exec_hardening", "launch-hardening")
	if err != nil {
		t.Fatal(err)
	}
	hostMount, _ := os.Readlink("/proc/self/ns/mnt")
	siblingRoot := filepath.Join(cfg.PrincipalRoot, sibling.Account)
	output := runHelperShell(t, client, mapping, "launch-hardening", paths.Workspace,
		"printf 'uid='; id -u; grep '^NSpid:' /proc/self/status; grep '^Seccomp:' /proc/self/status; "+
			"printf 'mnt='; readlink /proc/self/ns/mnt; "+
			"if [ \"$(readlink /proc/1/ns/pid)\" = \"$(readlink /proc/self/ns/pid)\" ]; then echo proc=local; else echo proc=host; fi; "+
			"if [ -e "+siblingRoot+" ]; then echo sibling=visible; else echo sibling=hidden; fi")
	handshake, err := client.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !handshake.Hardening["seccomp"] || !strings.Contains(output, "Seccomp:\t2") {
		t.Fatalf("seccomp hardening not active: status=%v degraded=%v output=%q", handshake.Hardening, handshake.DegradedReasons, output)
	}
	if handshake.Hardening["pid_namespace"] {
		if !strings.Contains(output, "uid=0") {
			t.Fatalf("PID/user namespace did not map namespace root: %q", output)
		}
		if !strings.Contains(output, "proc=local") {
			t.Fatalf("PID namespace does not have a matching procfs mount: %q", output)
		}
	}
	if handshake.Hardening["mount_namespace"] && strings.Contains(output, "mnt="+hostMount) {
		t.Fatalf("mount namespace did not change: host=%s output=%q", hostMount, output)
	}
	if handshake.Hardening["mount_namespace"] && !strings.Contains(output, "sibling=hidden") {
		t.Fatalf("mount namespace did not hide sibling principal roots: %q", output)
	}
	assertNohupChildRunsUntilAgentExit(t, client, mapping, paths.Workspace)
	t.Logf("hardening=%v degraded=%v", handshake.Hardening, handshake.DegradedReasons)
}

func assertNohupChildRunsUntilAgentExit(t *testing.T, client *HelperClient, mapping AccountMapping, workspace string) {
	t.Helper()
	activityLog := filepath.Join(workspace, "nohup-activity.log")
	nohupLog := filepath.Join(workspace, "nohup-output.log")
	script := `
nohup sh -c 'while :; do echo tick >> "$AMUX_TEST_ACTIVITY_LOG"; sleep 1; done' \
  </dev/null >"$AMUX_TEST_NOHUP_LOG" 2>&1 &
child=$!
deadline=10
while [ ! -s "$AMUX_TEST_ACTIVITY_LOG" ] && [ "$deadline" -gt 0 ]; do
  sleep 1
  deadline=$((deadline - 1))
done
kill -0 "$child"
ps -o pid= -p "$child" | grep -q "[0-9]"
pgrep -P "$$" | grep -qx "$child"
test "$(readlink /proc/1/ns/pid)" = "$(readlink /proc/self/ns/pid)"
printf 'nohup-ready child=%s\n' "$child"
wait
`
	handle, err := client.Start(context.Background(), mapping, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", script},
		Env: []string{
			"PATH=/usr/bin:/bin",
			"AMUX_TEST_ACTIVITY_LOG=" + activityLog,
			"AMUX_TEST_NOHUP_LOG=" + nohupLog,
		},
		Dir: workspace, Context: LaunchContext{LaunchID: "launch-nohup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = handle.Kill(context.Background())
		_ = handle.Close()
	})
	ready := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, readErr := bufio.NewReader(handle.Stdout()).ReadString('\n')
		ready <- struct {
			line string
			err  error
		}{line: line, err: readErr}
	}()
	select {
	case result := <-ready:
		if result.err != nil || !strings.HasPrefix(result.line, "nohup-ready child=") {
			stderr, _ := io.ReadAll(handle.Stderr())
			t.Fatalf("nohup child did not become ready: line=%q stderr=%q err=%v", result.line, stderr, result.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for nohup child readiness")
	}
	if err := handle.Kill(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _ = handle.Wait(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for uidHasLiveProcesses(mapping.UID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if uidHasLiveProcesses(mapping.UID) {
		t.Fatalf("agent exit left nohup child running for uid %d", mapping.UID)
	}
}

func TestLinuxDACHelperCrashRootE2E(t *testing.T) {
	if raw := os.Getenv("AMUX_HELPER_CRASH_CHILD_CONFIG"); raw != "" {
		var cfg HelperConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if err := ServeHelper(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		return
	}
	if os.Getenv("AMUX_LINUX_DAC_E2E") != "1" {
		t.Skip("set AMUX_LINUX_DAC_E2E=1 and run as root")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root E2E must run as root")
	}
	uidStart, _ := strconv.Atoi(os.Getenv("AMUX_TEST_UID_START"))
	if uidStart == 0 {
		uidStart = 780000
	}
	root, err := os.MkdirTemp("/tmp", "amux-linux-crash-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o711); err != nil {
		t.Fatal(err)
	}
	cfg := HelperConfig{
		SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"),
		PrincipalRoot: filepath.Join(root, "principals"), ServiceUID: 0, ServiceGID: 0,
		UIDRangeStart: uidStart, UIDRangeEnd: uidStart + 20,
	}
	encoded, _ := json.Marshal(cfg)
	helperProcess := exec.Command(os.Args[0], "-test.run=^TestLinuxDACHelperCrashRootE2E$")
	helperProcess.Env = append(os.Environ(), "AMUX_HELPER_CRASH_CHILD_CONFIG="+string(encoded))
	// The installed helper runs in a systemd control group with
	// KillMode=control-group. Give the test helper its own process group so the
	// crash path can exercise the same descendant cleanup guarantee.
	helperProcess.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := helperProcess.Start(); err != nil {
		t.Fatal(err)
	}
	helperProcessGroup := helperProcess.Process.Pid
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)
	mapping := AccountMapping{
		PrincipalID: "a9343ca9-982b-4597-9549-bf62620196ed",
		Account:     "amux_da9343ca9982", UID: uidStart + 15, GID: uidStart + 15,
	}
	removeLinuxTestAccount(mapping.Account, mapping.UID)
	t.Cleanup(func() {
		if helperProcess.Process != nil {
			_ = syscall.Kill(-helperProcessGroup, syscall.SIGKILL)
			_ = helperProcess.Process.Kill()
		}
		removeLinuxTestAccount(mapping.Account, mapping.UID)
	})
	paths, err := client.PrepareRun(context.Background(), mapping, "exec_crash", "launch-crash")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := client.Start(context.Background(), mapping, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", "sleep 300"}, Env: []string{"PATH=/usr/bin:/bin"},
		Dir: paths.Workspace, Context: LaunchContext{LaunchID: "launch-crash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostPID := handle.PID()
	if err := helperProcess.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Simulate systemd KillMode=control-group: a direct helper SIGKILL alone
	// cannot deliver Pdeathsig to grandchildren spawned by the actual agent,
	// and the launch intentionally has its own session via Setsid.
	_ = syscall.Kill(-helperProcessGroup, syscall.SIGKILL)
	_ = syscall.Kill(-hostPID, syscall.SIGKILL)
	_ = helperProcess.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for (processIsLive(hostPID) || uidHasLiveProcesses(mapping.UID)) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processIsLive(hostPID) || uidHasLiveProcesses(mapping.UID) {
		t.Fatalf("helper crash left live child process for pid %d / uid %d", hostPID, mapping.UID)
	}
	_ = handle.Close()

	recoveryCtx, recoveryCancel := context.WithCancel(context.Background())
	defer recoveryCancel()
	recovered := make(chan error, 1)
	go func() { recovered <- ServeHelper(recoveryCtx, cfg) }()
	recoveryClient := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, recoveryClient)
	if _, err := recoveryClient.Describe(context.Background(), "launch-crash"); !errors.Is(err, ErrIsolationLost) {
		t.Fatalf("recovered launch error = %v, want isolation_lost", err)
	}
	recoveryCancel()
	select {
	case err := <-recovered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("recovered helper did not stop")
	}
}

func TestLinuxDACFallbackRecoveryRootE2E(t *testing.T) {
	if os.Getenv("AMUX_LINUX_DAC_E2E") != "1" {
		t.Skip("set AMUX_LINUX_DAC_E2E=1 and run as root")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root E2E must run as root")
	}
	uidStart, _ := strconv.Atoi(os.Getenv("AMUX_TEST_UID_START"))
	if uidStart == 0 {
		uidStart = 780000
	}
	root, err := os.MkdirTemp("/tmp", "amux-linux-fallback-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o711); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "helper.sock")
	runtimeCfg := config.IsolationConfig{
		Enabled: true, Backend: "auto", UIDRangeStart: uidStart, UIDRangeEnd: uidStart + 20,
		HelperSocket: socket, MappingDB: filepath.Join(root, "mappings.db"),
		PrincipalRoot: filepath.Join(root, "principals"),
	}
	runtimeState, err := NewRuntime(runtimeCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeState.Close()
	identity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: "dc70bc83-0088-49d2-acbb-156b0b8666aa",
		EffectivePrincipalID:   "dc70bc83-0088-49d2-acbb-156b0b8666aa",
		Source:                 principal.SourceCLI,
		ExecutionStateRef:      "fallback-e2e",
	}
	if runtimeState.store != nil {
		mapping, err := runtimeState.store.Ensure(identity.EffectivePrincipalID)
		if err != nil {
			t.Fatal(err)
		}
		removeLinuxTestAccount(mapping.Account, mapping.UID)
	}
	if _, selected, _ := runtimeState.Select(identity); selected != principal.BackendPassthrough {
		t.Fatalf("missing helper selected %q", selected)
	}
	helperCfg := HelperConfig{
		SocketPath: socket, StateDir: filepath.Join(root, "state"), PrincipalRoot: runtimeCfg.PrincipalRoot,
		ServiceUID: 0, ServiceGID: 0, UIDRangeStart: uidStart, UIDRangeEnd: uidStart + 20,
	}
	helperCtx, helperCancel := context.WithCancel(context.Background())
	helperDone := make(chan error, 1)
	go func() { helperDone <- ServeHelper(helperCtx, helperCfg) }()
	waitHelperReady(t, NewHelperClient(socket))
	if _, selected, _ := runtimeState.Select(identity); selected != principal.BackendLinuxDAC {
		t.Fatalf("recovered helper selected %q", selected)
	}
	finalIdentity, err := principal.FinalizeBackend(identity, principal.BackendLinuxDAC)
	if err != nil {
		t.Fatal(err)
	}
	if _, selected, err := runtimeState.PrepareExecution(context.Background(), finalIdentity, "launch-fallback-a", ""); err != nil || selected != principal.BackendLinuxDAC {
		t.Fatalf("prepare selected=%q err=%v", selected, err)
	}
	helperCancel()
	select {
	case err := <-helperDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not stop")
	}
	if _, _, err := runtimeState.PrepareExecution(context.Background(), finalIdentity, "launch-fallback-b", ""); err == nil {
		t.Fatal("saved linux-dac execution silently weakened after helper loss")
	}
	if _, selected, _ := runtimeState.Select(identity); selected != principal.BackendPassthrough {
		t.Fatalf("new execution after helper loss selected %q", selected)
	}

	restartCtx, restartCancel := context.WithCancel(context.Background())
	restartDone := make(chan error, 1)
	go func() { restartDone <- ServeHelper(restartCtx, helperCfg) }()
	waitHelperReady(t, NewHelperClient(socket))
	if _, selected, _ := runtimeState.Select(identity); selected != principal.BackendLinuxDAC {
		t.Fatalf("new execution after helper recovery selected %q", selected)
	}
	restartCancel()
	select {
	case <-restartDone:
	case <-time.After(3 * time.Second):
		t.Fatal("restarted helper did not stop")
	}
	mappingStore, err := OpenMappingStoreRange(runtimeCfg.MappingDB, uidStart, runtimeCfg.UIDRangeEnd)
	if err == nil {
		mappings, _ := mappingStore.List()
		_ = mappingStore.Close()
		for _, mapping := range mappings {
			removeLinuxTestAccount(mapping.Account, mapping.UID)
		}
	}
}

func removeLinuxTestAccount(account string, uid int) {
	if uid >= 65536 {
		_ = exec.Command("pkill", "-KILL", "-u", strconv.Itoa(uid)).Run()
	}
	_ = exec.Command("userdel", account).Run()
	_ = exec.Command("groupdel", account).Run()
}

func processIsLive(pid int) bool {
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	return err == nil && !strings.Contains(string(status), "\nState:\tZ")
}

func uidHasProcesses(uid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	prefix := strconv.Itoa(uid)
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if err == nil && strings.Contains(string(status), "\nUid:\t"+prefix+"\t") {
			return true
		}
	}
	return false
}

func uidHasLiveProcesses(uid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	prefix := strconv.Itoa(uid)
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if err != nil {
			continue
		}
		value := string(status)
		if strings.Contains(value, "\nUid:\t"+prefix+"\t") &&
			!strings.Contains(value, "\nState:\tZ") {
			return true
		}
	}
	return false
}

func runHelperShell(t *testing.T, client *HelperClient, mapping AccountMapping, launchID, dir, script string) string {
	t.Helper()
	handle, err := client.Start(context.Background(), mapping, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin"},
		Dir: dir, Context: LaunchContext{LaunchID: launchID},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(handle.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	status, err := handle.Wait(context.Background())
	if err != nil || status.Code != 0 {
		stderr, _ := io.ReadAll(handle.Stderr())
		t.Fatalf("status=%+v stdout=%q stderr=%q err=%v", status, output, stderr, err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func waitHelperReady(t *testing.T, client *HelperClient) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, err := client.Handshake(ctx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("helper did not become ready")
}

// TestLinuxDACRevokeEscapedRootE2E pins the revocation guarantee against a
// descendant that left its launch process group. Signalling the tracked process
// groups is not enough: the dedicated uid is the principal's identity, so
// revocation has to finish with a kernel-precise sweep by uid.
func TestLinuxDACRevokeEscapedRootE2E(t *testing.T) {
	if os.Getenv("AMUX_LINUX_DAC_E2E") != "1" {
		t.Skip("set AMUX_LINUX_DAC_E2E=1 and run as root")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root E2E must run as root")
	}
	uidStart := 780000
	if raw := os.Getenv("AMUX_TEST_UID_START"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
		uidStart = value
	}
	// Use the tail of the reserved range so this case cannot collide with the
	// principals the other root E2E cases provision.
	uid := uidStart + 9

	root, err := os.MkdirTemp("/tmp", "amux-linux-dac-escape-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o711); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := HelperConfig{
		SocketPath:       filepath.Join(root, "helper.sock"),
		StateDir:         filepath.Join(root, "state"),
		PrincipalRoot:    filepath.Join(root, "principals"),
		ServiceUID:       0,
		ServiceGID:       0,
		UIDRangeStart:    uidStart,
		UIDRangeEnd:      uidStart + 20,
		MaterializerPath: os.Getenv("AMUX_TEST_HELPER_BINARY"),
	}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)

	mapping := AccountMapping{
		PrincipalID: "3f1c9c02-1a4e-4c8f-9f0a-2b6f4c9d7e51",
		Account:     "amux_e3f1c9c021a4",
		UID:         uid,
		GID:         uid,
	}
	removeLinuxTestAccount(mapping.Account, mapping.UID)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		removeLinuxTestAccount(mapping.Account, 0)
	})

	paths, err := client.PrepareRun(context.Background(), mapping, "exec_escape", "launch-escape")
	if err != nil {
		t.Fatal(err)
	}
	// The inner setsid leaves the launch process group entirely, which is what a
	// daemonising agent child does in practice.
	handle, err := client.Start(context.Background(), mapping, LaunchSpec{
		Path:    "/bin/sh",
		Args:    []string{"-c", "setsid sleep 300 >/dev/null 2>&1 & sleep 300"},
		Env:     []string{"PATH=/usr/bin:/bin"},
		Dir:     paths.Workspace,
		Context: LaunchContext{LaunchID: "launch-escape"},
	})
	if err != nil {
		t.Fatal(err)
	}

	escaped := waitForEscapedPID(t, uid, handle.PID())
	t.Logf("launch pid=%d escaped pid=%d (own process group)", handle.PID(), escaped)

	if err := client.KillPrincipal(context.Background(), mapping); err != nil {
		t.Fatal(err)
	}
	_ = handle.Close()

	deadline := time.Now().Add(10 * time.Second)
	for uidHasProcesses(uid) {
		if time.Now().After(deadline) {
			t.Fatalf("processes remain for revoked uid %d after KillPrincipal", uid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForEscapedPID returns a pid running as uid whose process group differs
// from the launch leader, proving the child really left the tracked group.
func waitForEscapedPID(t *testing.T, uid, launchPID int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			pid, convErr := strconv.Atoi(entry.Name())
			if convErr != nil || pid == launchPID {
				continue
			}
			var stat syscall.Stat_t
			if err := syscall.Stat(filepath.Join("/proc", entry.Name()), &stat); err != nil {
				continue
			}
			if int(stat.Uid) != uid {
				continue
			}
			if pgid, err := syscall.Getpgid(pid); err == nil && pgid != launchPID {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no escaped process appeared for uid %d", uid)
	return 0
}

// A pruned session leaves its principal workspace behind unless the helper is
// asked to reclaim it: the service user cannot delete a 0700 tree owned by the
// principal, so the directories accumulate for the life of the account.
func TestHelperRemoveWorkspaceReclaimsExecutionDirectory(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := HelperConfig{
		SocketPath:           filepath.Join(root, "helper.sock"),
		StateDir:             filepath.Join(root, "state"),
		PrincipalRoot:        filepath.Join(root, "principals"),
		ServiceUID:           os.Getuid(),
		ServiceGID:           os.Getgid(),
		UIDRangeStart:        max(os.Getuid(), 1),
		UIDRangeEnd:          max(os.Getuid(), 1) + 10,
		AllowUnprivileged:    true,
		SkipAccountProvision: true,
	}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)

	mapping := AccountMapping{
		PrincipalID: "d7eaf95f-4c18-43b2-81fd-f5f013ac901d",
		Account:     "amux_0123456789ab",
		UID:         max(os.Getuid(), 1),
		GID:         max(os.Getuid(), 1),
	}
	paths, err := client.PrepareRun(context.Background(), mapping, "exec_test", "launch-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Workspace, "file"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := client.RemoveWorkspace(context.Background(), mapping, "exec_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace survived removal: %v", err)
	}
	// The account root and its long-lived HOME must outlive a single execution.
	for _, keep := range []string{filepath.Dir(paths.Workspace), paths.Home} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("removal took out %s: %v", keep, err)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not stop")
	}
}

// The provider database only gets rotated when it actually grew; deleting a
// small one on every teardown would throw away the agent's recent history for
// no reason.
func TestHelperReclaimHomeHonoursSizeThreshold(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := HelperConfig{
		SocketPath:           filepath.Join(root, "helper.sock"),
		StateDir:             filepath.Join(root, "state"),
		PrincipalRoot:        filepath.Join(root, "principals"),
		ServiceUID:           os.Getuid(),
		ServiceGID:           os.Getgid(),
		UIDRangeStart:        max(os.Getuid(), 1),
		UIDRangeEnd:          max(os.Getuid(), 1) + 10,
		AllowUnprivileged:    true,
		SkipAccountProvision: true,
	}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)

	mapping := AccountMapping{
		PrincipalID: "d7eaf95f-4c18-43b2-81fd-f5f013ac901d",
		Account:     "amux_0123456789ab",
		UID:         max(os.Getuid(), 1),
		GID:         max(os.Getuid(), 1),
	}
	paths, err := client.EnsurePrincipal(context.Background(), mapping)
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(paths.Home, ".trae", "cli")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(cacheDir, "logs_2.sqlite")
	wal := logs + "-wal"
	for _, path := range []string{logs, wal} {
		if err := os.WriteFile(path, make([]byte, 64), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec := []HomeCacheSpec{{
		Path:     ".trae/cli/logs_2.sqlite",
		MaxBytes: 1024,
		Remove:   []string{".trae/cli/logs_2.sqlite", ".trae/cli/logs_2.sqlite-wal"},
	}}

	if err := client.ReclaimHome(context.Background(), mapping, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logs); err != nil {
		t.Fatalf("a database under the threshold was reclaimed: %v", err)
	}

	if err := os.WriteFile(logs, make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.ReclaimHome(context.Background(), mapping, spec); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{logs, wal} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived reclamation: %v", path, err)
		}
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Fatalf("reclamation removed the containing directory: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not stop")
	}
}

func TestHelperReclaimHomeRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := HelperConfig{
		SocketPath:           filepath.Join(root, "helper.sock"),
		StateDir:             filepath.Join(root, "state"),
		PrincipalRoot:        filepath.Join(root, "principals"),
		ServiceUID:           os.Getuid(),
		ServiceGID:           os.Getgid(),
		UIDRangeStart:        max(os.Getuid(), 1),
		UIDRangeEnd:          max(os.Getuid(), 1) + 10,
		AllowUnprivileged:    true,
		SkipAccountProvision: true,
	}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)

	mapping := AccountMapping{
		PrincipalID: "d7eaf95f-4c18-43b2-81fd-f5f013ac901d",
		Account:     "amux_0123456789ab",
		UID:         max(os.Getuid(), 1),
		GID:         max(os.Getuid(), 1),
	}
	if _, err := client.EnsurePrincipal(context.Background(), mapping); err != nil {
		t.Fatal(err)
	}
	err := client.ReclaimHome(context.Background(), mapping, []HomeCacheSpec{{
		Path: "../../etc/passwd", Remove: []string{"../../etc/passwd"},
	}})
	if err == nil {
		t.Fatal("an escaping cache path was accepted")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not stop")
	}
}
