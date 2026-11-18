//go:build !linux

package cli

import (
	"context"
	"errors"
)

func prepareIsolationHelperDir(string, bool) error { return errors.ErrUnsupported }

func runInstalledHelperUpgrade(context.Context, string, string, int) helperUpgradeResult {
	return helperUpgradeResult{Status: "absent"}
}

func helperUpgradeTarget(string, int) (bool, error) { return false, nil }
