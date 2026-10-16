//go:build linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Only the unit written by helper install is managed here. Never regenerate it
// from CLI defaults: it owns the installed UID range, roots and hardening flags.
func installedHelperIdentity(unit []byte) (string, int, error) {
	var args []string
	section := ""
	for _, line := range strings.Split(string(unit), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section != "[Service]" || !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		if args != nil {
			return "", 0, helperUpgradeProblem("invalid_unit")
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "ExecStart="))
		for rest != "" {
			if rest[0] != '"' {
				return "", 0, helperUpgradeProblem("invalid_unit")
			}
			end := 1
			for end < len(rest) {
				if rest[end] == '\\' {
					end += 2
					continue
				}
				if rest[end] == '"' {
					break
				}
				end++
			}
			if end >= len(rest) {
				return "", 0, helperUpgradeProblem("invalid_unit")
			}
			arg, err := strconv.Unquote(rest[:end+1])
			if err != nil {
				return "", 0, err
			}
			if strings.Contains(arg, "$") || strings.Contains(strings.ReplaceAll(arg, "%%", ""), "%") {
				return "", 0, helperUpgradeProblem("invalid_unit")
			}
			args = append(args, strings.ReplaceAll(arg, "%%", "%"))
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	if len(args) < 2 || args[0] != "/usr/local/libexec/amux/amux-isolation-helper" || args[1] != "serve" {
		return "", 0, helperUpgradeProblem("invalid_unit")
	}
	values := map[string]string{}
	for i := 2; i < len(args); i++ {
		key, val, found := strings.Cut(args[i], "=")
		if !found {
			if i+1 >= len(args) {
				return "", 0, helperUpgradeProblem("invalid_unit")
			}
			i++
			val = args[i]
		}
		if _, exists := values[key]; exists {
			return "", 0, helperUpgradeProblem("invalid_unit")
		}
		values[key] = val
	}
	uid, err := strconv.Atoi(values["--service-uid"])
	if err != nil || uid < 0 || !filepath.IsAbs(values["--socket"]) || values["--materializer-path"] != "/usr/local/libexec/amux/amuxlet-materializer" {
		return "", 0, helperUpgradeProblem("invalid_unit")
	}
	return values["--socket"], uid, nil
}

func helperUpgradeTarget(socket string, uid int) (bool, error) {
	unit, err := os.ReadFile(isolationHelperUnitPath)
	if err != nil {
		return false, err
	}
	installedSocket, installedUID, err := installedHelperIdentity(unit)
	return installedSocket == socket && installedUID == uid, err
}

type helperUpgradeHooks struct {
	running        bool
	probe          func(context.Context) (int, func(), error)
	stop           func(context.Context) error
	start          func(context.Context) error
	runningMatches func([]byte) bool
}

// The source bundle is authorized by the invoking administrator (direct root or
// sudo). Root-owned targets and the service unit never come from that bundle.
func runInstalledHelperUpgrade(ctx context.Context, source, socket string, uid int) helperUpgradeResult {
	fail := helperUpgradeFailed
	if os.Geteuid() != 0 {
		return fail(helperUpgradeProblem("root_required"))
	}
	info, err := os.Lstat(isolationHelperUnitPath)
	if os.IsNotExist(err) {
		return helperUpgradeResult{Status: "absent"}
	}
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&022 != 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return fail(helperUpgradeProblem("invalid_unit"))
	}
	unit, err := os.ReadFile(isolationHelperUnitPath)
	if err != nil {
		return fail(err)
	}
	installedSocket, installedUID, err := installedHelperIdentity(unit)
	if err != nil {
		return fail(err)
	}
	if installedUID != uid || installedSocket != socket {
		return helperUpgradeResult{Status: "other_service"}
	}
	if !filepath.IsAbs(source) {
		return fail(helperUpgradeProblem("invalid_source"))
	}
	ctl := func(callCtx context.Context, args ...string) (string, error) {
		out, err := exec.CommandContext(callCtx, "systemctl", args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("systemctl: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	fragment, err := ctl(ctx, "show", isolationHelperUnit, "--property=FragmentPath", "--value")
	if err != nil {
		return fail(err)
	}
	dropins, err := ctl(ctx, "show", isolationHelperUnit, "--property=DropInPaths", "--value")
	if err != nil {
		return fail(err)
	}
	if fragment != isolationHelperUnitPath || dropins != "" {
		return fail(helperUpgradeProblem("custom_unit"))
	}
	state, err := ctl(ctx, "show", isolationHelperUnit, "--property=ActiveState", "--value")
	if err != nil {
		return fail(err)
	}
	if state != "active" && state != "inactive" && state != "failed" {
		return fail(helperUpgradeProblem("service_transition"))
	}
	target := "/usr/local/libexec/amux"
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fail(err)
	}
	info, err = os.Stat(target)
	if err != nil {
		return fail(err)
	}
	if real != target || !info.IsDir() || info.Mode().Perm()&022 != 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return fail(helperUpgradeProblem("invalid_target"))
	}
	lock, err := os.OpenFile(filepath.Join(target, ".upgrade.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fail(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(helperUpgradeProblem("upgrade_locked"))
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	hooks := helperUpgradeHooks{running: state == "active",
		probe: func(ctx context.Context) (int, func(), error) { return guardHelperForUpgrade(ctx, socket) },
		stop:  func(ctx context.Context) error { _, err := ctl(ctx, "stop", isolationHelperUnit); return err },
		start: func(ctx context.Context) error {
			_, err := ctl(ctx, "start", isolationHelperUnit)
			if err != nil {
				return err
			}
			readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			for {
				_, err = drainHelper(readyCtx, socket, false)
				if err == nil {
					return nil
				}
				select {
				case <-readyCtx.Done():
					return err
				case <-time.After(25 * time.Millisecond):
				}
			}
		},
		runningMatches: func(want []byte) bool {
			pid, err := ctl(ctx, "show", isolationHelperUnit, "--property=MainPID", "--value")
			if err != nil {
				return false
			}
			n, err := strconv.Atoi(pid)
			if err != nil || n <= 0 {
				return false
			}
			got, err := os.ReadFile(filepath.Join("/proc", pid, "exe"))
			return err == nil && bytes.Equal(got, want)
		},
	}
	return upgradeHelperFiles(ctx, source, target, hooks, installFileAtomically)
}

func upgradeHelperFiles(ctx context.Context, source, target string, hooks helperUpgradeHooks, install func(string, []byte, os.FileMode) error) helperUpgradeResult {
	fail := helperUpgradeFailed
	sources := []string{"amux-isolation-helper", "amuxlet"}
	targets := []string{"amux-isolation-helper", "amuxlet-materializer"}
	fresh, old := make([][]byte, 2), make([][]byte, 2)
	same := true
	for i, name := range sources {
		var err error
		fresh[i], err = os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return fail(err)
		}
		old[i], err = os.ReadFile(filepath.Join(target, targets[i]))
		if err != nil {
			return fail(err)
		}
		same = same && bytes.Equal(fresh[i], old[i])
	}
	if same && (!hooks.running || hooks.runningMatches(fresh[0])) {
		return helperUpgradeResult{Status: "unchanged"}
	}
	if hooks.running {
		active, release, err := hooks.probe(ctx)
		if release != nil {
			defer release()
		}
		if err != nil {
			return fail(err)
		}
		if active < 0 {
			return fail(helperUpgradeProblem("legacy_helper"))
		}
		if active != 0 {
			return helperUpgradeResult{Status: "busy", Active: active}
		}
		if err := hooks.stop(ctx); err != nil {
			return fail(err)
		}
	}
	startAttempted := false
	rollback := func(cause error) helperUpgradeResult {
		errs := []error{cause}
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if startAttempted {
			if err := hooks.stop(recovery); err != nil {
				return fail(errors.Join(cause, err))
			}
		}
		// Restore the pair even when the failing write partially replaced a target.
		for i, name := range targets {
			errs = append(errs, install(filepath.Join(target, name), old[i], 0755))
		}
		if hooks.running {
			errs = append(errs, hooks.start(recovery))
		}
		return fail(errors.Join(errs...))
	}
	for i, name := range targets {
		if err := install(filepath.Join(target, name), fresh[i], 0755); err != nil {
			return rollback(err)
		}
	}
	if hooks.running {
		startAttempted = true
		if err := hooks.start(ctx); err != nil {
			return rollback(err)
		}
	}
	return helperUpgradeResult{Status: "updated"}
}

// The lease writer stays in this process. Closing it, including on updater
// termination, releases the helper's admission guard without another RPC.
func guardHelperForUpgrade(ctx context.Context, socket string) (int, func(), error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return 0, nil, err
	}
	defer reader.Close()
	active, err := probeHelper(ctx, socket, false, reader)
	if err != nil || active != 0 {
		_ = writer.Close()
		return active, nil, err
	}
	return active, func() { _ = writer.Close() }, nil
}
