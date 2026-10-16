package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/cobra"
)

const isolationHelperUnit = "amux-isolation-helper.service"
const isolationHelperUnitPath = "/etc/systemd/system/" + isolationHelperUnit

type helperUpgradeResult struct {
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Active     int    `json:"active,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

type helperUpgradeProblem string

func (p helperUpgradeProblem) Error() string { return string(p) }
func helperUpgradeFailed(err error) helperUpgradeResult {
	var problem helperUpgradeProblem
	if errors.As(err, &problem) {
		return helperUpgradeResult{Status: "failed", Reason: string(problem)}
	}
	return helperUpgradeResult{Status: "failed", Diagnostic: err.Error()}
}

func helperUpgradeText(cmd *cobra.Command, key string, args ...any) string {
	return commandCatalog.Text("update.helper."+key, args...).For(commandLocale(cmd))
}

func refreshInstalledIsolationHelper(cmd *cobra.Command, opts *installOptions) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := os.Stat(isolationHelperUnitPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("%s: %w", helperUpgradeText(cmd, "failed"), err)
	}
	cfg, err := config.LoadIsolationFrom(filepath.Join(defaultAmuxDir(), "config.toml"))
	if err != nil {
		return fmt.Errorf("%s: %w", helperUpgradeText(cmd, "failed"), err)
	}
	socket := cfg.HelperSocket
	if socket == "" {
		socket = filepath.Join(defaultAmuxDir(), "isolation-helper.sock")
	}
	uid, _, _ := helperServiceDefaults()
	owned, err := helperUpgradeTarget(socket, uid)
	if err != nil {
		return reportHelperUpgradeResult(cmd, helperUpgradeFailed(err))
	}
	if !owned {
		return nil
	}
	meta, err := readInstallMetadata()
	if err != nil {
		return fmt.Errorf("%s: %w", helperUpgradeText(cmd, "failed"), err)
	}
	if !slices.Contains(meta.Binaries, "amux-isolation-helper") {
		return fmt.Errorf("%s", helperUpgradeText(cmd, "bundle_missing"))
	}
	binDir, err := filepath.Abs(effectiveInstallBinDir(opts.binDir))
	if err != nil {
		return err
	}
	binary := filepath.Join(binDir, "amux")
	args := []string{"isolation", "helper", "_upgrade", "--source", binDir, "--socket", socket, "--service-uid", strconv.Itoa(uid)}
	if os.Geteuid() != 0 {
		args = append([]string{"-n", "--", binary}, args...)
		binary = "sudo"
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
	defer cancel()
	child := exec.CommandContext(ctx, binary, args...)
	output, err := child.Output()
	if err != nil {
		var detail string
		if exit, ok := err.(*exec.ExitError); ok {
			detail = strings.TrimSpace(string(exit.Stderr))
		}
		return fmt.Errorf("%s: %w %s", helperUpgradeText(cmd, "privilege_failed"), err, detail)
	}
	var result helperUpgradeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("%s: %w", helperUpgradeText(cmd, "failed"), err)
	}
	return reportHelperUpgradeResult(cmd, result)
}

func reportHelperUpgradeResult(cmd *cobra.Command, result helperUpgradeResult) error {
	switch result.Status {
	case "absent", "other_service":
		return nil
	case "unchanged":
		fmt.Fprintln(cmd.OutOrStdout(), helperUpgradeText(cmd, "unchanged"))
		return nil
	case "updated":
		fmt.Fprintln(cmd.OutOrStdout(), helperUpgradeText(cmd, "updated"))
		return nil
	case "busy":
		return fmt.Errorf("%s", helperUpgradeText(cmd, "busy", result.Active))
	default:
		switch result.Reason {
		case "legacy_helper", "root_required", "invalid_unit", "invalid_source", "custom_unit", "service_transition", "invalid_target", "upgrade_locked":
			return fmt.Errorf("%s %s", helperUpgradeText(cmd, "failed"), helperUpgradeText(cmd, result.Reason))
		}
		return fmt.Errorf("%s: %s", helperUpgradeText(cmd, "failed"), result.Diagnostic)
	}
}

func newIsolationHelperUpgradeCmd() *cobra.Command {
	var source, socket string
	var uid int
	cmd := &cobra.Command{Use: "_upgrade", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result := runInstalledHelperUpgrade(cmd.Context(), source, socket, uid)
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	cmd.Flags().StringVar(&source, "source", "", "")
	cmd.Flags().StringVar(&socket, "socket", "", "")
	cmd.Flags().IntVar(&uid, "service-uid", -1, "")
	return cmd
}
