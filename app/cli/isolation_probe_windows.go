//go:build windows

package cli

import (
	"fmt"
	"os/exec"
)

func helperSocketOwner(string) (int, int, error) {
	return 0, 0, fmt.Errorf("the isolation helper is Linux-only")
}

func setProcessCredential(*exec.Cmd, int, int) {}
