//go:build !windows

package winsvc

import "fmt"

func unsupported() error {
	return fmt.Errorf("Windows Service management is only available on Windows")
}

// Install is not supported on this platform.
func Install(string, Config) error { return unsupported() }

// Uninstall is not supported on this platform.
func Uninstall() error { return unsupported() }

// Start is not supported on this platform.
func Start() error { return unsupported() }

// Stop is not supported on this platform.
func Stop() error { return unsupported() }

// Status is not supported on this platform.
func Status() (string, error) { return "", unsupported() }

// IsWindowsService is always false off Windows.
func IsWindowsService() (bool, error) { return false, nil }
