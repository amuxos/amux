//go:build windows

package winsvc

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// GrantSystemAccess grants the local SYSTEM account modify rights on amuxHome
// so the LocalSystem service can read Feishu secrets and write runtime state.
func GrantSystemAccess(amuxHome string) error {
	amuxHome = strings.TrimSpace(amuxHome)
	if amuxHome == "" {
		return fmt.Errorf("amux home is required")
	}
	if err := os.MkdirAll(amuxHome, 0700); err != nil {
		return err
	}
	// icacls is the supported ACL tool on consumer Windows; keep the grant
	// minimal (SYSTEM:(OI)(CI)F) without disabling inheritance.
	cmd := exec.Command("icacls", amuxHome, "/grant", "SYSTEM:(OI)(CI)F")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("grant SYSTEM access to %s: %w (%s)", amuxHome, err, strings.TrimSpace(string(out)))
	}
	return nil
}
