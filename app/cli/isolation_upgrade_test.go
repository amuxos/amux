package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestUpdateHelperFailureStopsBeforeServiceRestart(t *testing.T) {
	old, oldSync := updateIsolationHelper, updateAgentPackAutoSyncRefresher
	t.Cleanup(func() { updateIsolationHelper = old; updateAgentPackAutoSyncRefresher = oldSync })
	cause := errors.New("helper_busy")
	updateIsolationHelper = func(*cobra.Command, *installOptions) error { return cause }
	updateAgentPackAutoSyncRefresher = func() error { t.Fatal("service restart after helper failure"); return nil }
	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--post-install", "--restart"})
	if err := cmd.Execute(); !errors.Is(err, cause) {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(out.String(), "Updated amux") {
		t.Fatalf("false success: %s", out.String())
	}
}

func TestHelperUpgradeResultLocales(t *testing.T) {
	for _, lang := range []string{"en", "zh-CN", "fr"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("AMUX_LANGUAGE", lang)
			t.Setenv("LC_ALL", "C")
			for _, status := range []string{"updated", "unchanged", "busy", "failed"} {
				cmd := &cobra.Command{}
				var out bytes.Buffer
				cmd.SetOut(&out)
				err := reportHelperUpgradeResult(cmd, helperUpgradeResult{Status: status, Active: 3, Diagnostic: "EACCES"})
				if (err != nil) != (status == "busy" || status == "failed") {
					t.Fatalf("%s: %v", status, err)
				}
				text := out.String()
				if err != nil {
					text = err.Error()
				}
				if (strings.Contains(text, "隔离")) != (lang == "zh-CN") || strings.Contains(text, "update.helper.") {
					t.Fatalf("%s: %s", status, text)
				}
				if status == "busy" && !strings.Contains(text, "3") {
					t.Fatal(text)
				}
				if status == "failed" && !strings.Contains(text, "EACCES") {
					t.Fatal(text)
				}
			}
		})
	}
}

func TestHelperUpgradeProblemLanguages(t *testing.T) {
	for _, key := range []string{"legacy_helper", "failed", "privilege_failed", "bundle_missing", "root_required", "invalid_unit", "invalid_source", "custom_unit", "service_transition", "invalid_target", "upgrade_locked"} {
		cmd := &cobra.Command{}
		t.Setenv("AMUX_LANGUAGE", "en")
		en := helperUpgradeText(cmd, key)
		t.Setenv("AMUX_LANGUAGE", "zh-CN")
		zh := helperUpgradeText(cmd, key)
		if en == zh || strings.Contains(en, "update.helper.") || strings.Contains(zh, "update.helper.") {
			t.Fatalf("missing translation %s: %q %q", key, en, zh)
		}
	}
}

func TestInstallStagesOptionalHelperWithRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "rollback", false: "success"}[fail], func(t *testing.T) {
			t.Setenv("AMUX_HOME", t.TempDir())
			source, target := writeFakeInstallSource(t, "new"), writeFakeInstallSource(t, "old")
			for dir, data := range map[string]string{source: "new-helper", target: "old-helper"} {
				if err := os.WriteFile(filepath.Join(dir, exeName("amux-isolation-helper")), []byte(data), 0755); err != nil {
					t.Fatal(err)
				}
			}
			err := replaceInstallBinaries(source, target, func(from, to string) error {
				if fail && strings.Contains(from, ".amux-stage-") && filepath.Base(to) == exeName("amux-isolation-helper") {
					return errors.New("injected helper write failure")
				}
				return os.Rename(from, to)
			})
			if (err != nil) != fail {
				t.Fatal(err)
			}
			for name, want := range map[string]string{"amux": "amux-new", "amux-isolation-helper": "new-helper"} {
				if fail {
					want = strings.ReplaceAll(want, "new", "old")
				}
				got, err := os.ReadFile(filepath.Join(target, exeName(name)))
				if err != nil || string(got) != want {
					t.Fatalf("%s: %q %v", name, got, err)
				}
			}
		})
	}
}
