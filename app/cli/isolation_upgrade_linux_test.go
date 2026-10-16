//go:build linux

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledHelperIdentityPreservesConfiguration(t *testing.T) {
	args := []string{"/usr/local/libexec/amux/amux-isolation-helper", "serve", "--socket", "/srv/amux user/%files/helper.sock", "--service-uid", "1001", "--service-gid", "1002", "--principal-root", "/srv/principals", "--state-dir", "/srv/state", "--uid-range-start", "810000", "--uid-range-end", "819999", "--materializer-path", "/usr/local/libexec/amux/amuxlet-materializer", "--seccomp=false", "--pid-namespace=false", "--mount-namespace=false"}
	for i, arg := range args {
		var err error
		args[i], err = quoteSystemdExecArg(arg)
		if err != nil {
			t.Fatal(err)
		}
	}
	unit := "[Unit]\nDescription=example\n[Service]\nExecStart=" + strings.Join(args, " ") + "\nRestart=on-failure\n"
	socket, uid, err := installedHelperIdentity([]byte(unit))
	if err != nil || socket != "/srv/amux user/%files/helper.sock" || uid != 1001 {
		t.Fatalf("%s %d %v", socket, uid, err)
	}
	for _, bad := range []string{strings.Replace(unit, "--socket", "--socket-bad", 1), strings.Replace(unit, "%%files", "%i", 1), strings.Replace(unit, "serve", "$MODE", 1), unit + "ExecStart=\"another\"\n", strings.Replace(unit, "amuxlet-materializer", "custom-materializer", 1)} {
		if _, _, err := installedHelperIdentity([]byte(bad)); err == nil {
			t.Fatalf("accepted unmanaged unit: %s", bad)
		}
	}
}

func TestHelperUpgradeTransaction(t *testing.T) {
	for _, scenario := range []string{"success", "busy", "legacy", "probe_error", "stop_error", "write_error", "start_error", "unchanged", "stale_process", "materializer_only", "stopped", "missing_source"} {
		t.Run(scenario, func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			for name, data := range map[string]string{"amux-isolation-helper": "new-helper", "amuxlet": "new-materializer"} {
				if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0755); err != nil {
					t.Fatal(err)
				}
			}
			oldHelper, oldMaterializer := "old-helper", "old-materializer"
			if scenario == "unchanged" || scenario == "stale_process" {
				oldHelper = "new-helper"
				oldMaterializer = "new-materializer"
			}
			if scenario == "materializer_only" {
				oldHelper = "new-helper"
			}
			for name, data := range map[string]string{"amux-isolation-helper": oldHelper, "amuxlet-materializer": oldMaterializer} {
				if err := os.WriteFile(filepath.Join(target, name), []byte(data), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing_source" {
				if err := os.Remove(filepath.Join(source, "amux-isolation-helper")); err != nil {
					t.Fatal(err)
				}
			}
			var events []string
			starts := 0
			hooks := helperUpgradeHooks{running: scenario != "stopped", runningMatches: func([]byte) bool { return scenario != "stale_process" },
				probe: func(context.Context) (int, func(), error) {
					events = append(events, "probe")
					if scenario == "busy" {
						return 2, nil, nil
					}
					if scenario == "probe_error" {
						return 0, nil, errors.New("probe failed")
					}
					if scenario == "legacy" {
						return -1, nil, nil
					}
					return 0, func() { events = append(events, "release") }, nil
				},
				stop: func(context.Context) error {
					events = append(events, "stop")
					if scenario == "stop_error" {
						return errors.New("stop failed")
					}
					return nil
				},
				start: func(context.Context) error {
					events = append(events, "start")
					starts++
					if scenario == "start_error" && starts == 1 {
						return errors.New("start failed")
					}
					return nil
				}}
			failedWrite := false
			result := upgradeHelperFiles(context.Background(), source, target, hooks, func(path string, data []byte, mode os.FileMode) error {
				events = append(events, "write:"+filepath.Base(path))
				if scenario == "write_error" && !failedWrite && filepath.Base(path) == "amuxlet-materializer" {
					failedWrite = true
					return errors.New("disk full")
				}
				return installFileAtomically(path, data, mode)
			})
			wantStatus := "updated"
			wantHelper, wantMaterializer := "new-helper", "new-materializer"
			switch scenario {
			case "busy":
				wantStatus = "busy"
			case "legacy", "probe_error", "stop_error", "write_error", "start_error", "missing_source":
				wantStatus = "failed"
			case "unchanged":
				wantStatus = "unchanged"
			}
			if wantStatus != "updated" {
				wantHelper, wantMaterializer = oldHelper, oldMaterializer
			}
			if result.Status != wantStatus {
				t.Fatalf("result=%+v events=%v", result, events)
			}
			for name, want := range map[string]string{"amux-isolation-helper": wantHelper, "amuxlet-materializer": wantMaterializer} {
				got, err := os.ReadFile(filepath.Join(target, name))
				if err != nil || string(got) != want {
					t.Fatalf("%s=%q want %q: %v", name, got, want, err)
				}
			}
			if (scenario == "busy" || scenario == "legacy") && strings.Join(events, ",") != "probe" {
				t.Fatal(events)
			}
			if (scenario == "write_error" || scenario == "start_error") && starts == 0 {
				t.Fatal("old service not recovered")
			}
			if scenario == "stop_error" && strings.Join(events, ",") != "probe,stop,release" {
				t.Fatal(events)
			}
			if scenario == "unchanged" && len(events) != 0 {
				t.Fatal(events)
			}
			if scenario == "stopped" && (strings.Contains(strings.Join(events, ","), "start") || strings.Contains(strings.Join(events, ","), "stop")) {
				t.Fatal(events)
			}
		})
	}
}
