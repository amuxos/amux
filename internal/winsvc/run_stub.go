//go:build !windows

package winsvc

import "fmt"

// Host is the service lifecycle implemented by the CLI.
type Host interface {
	Start() error
	Stop() error
}

// RunService is not supported off Windows.
func RunService(Host) error {
	return fmt.Errorf("Windows Service run is only available on Windows")
}
