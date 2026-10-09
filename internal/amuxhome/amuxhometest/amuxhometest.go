// Package amuxhometest keeps Go tests away from the real amux home.
//
// amuxhome.Dir resolves AMUX_HOME before HOME, so a test that only redirects
// HOME still reads and writes the caller's real amux home when the test binary
// inherits AMUX_HOME, for example when `go test` runs inside an amux-managed
// Agent Session. Packages whose tests can reach amuxhome call Main from
// TestMain so every test in the package starts with AMUX_HOME pointing at a
// throwaway directory. Individual tests may still override AMUX_HOME (or clear
// it after redirecting HOME) with t.Setenv.
package amuxhometest

import (
	"fmt"
	"os"
	"testing"
)

// EnvVar is the environment variable that overrides the amux home.
const EnvVar = "AMUX_HOME"

// PreserveEnvVar, set to "1", tells Isolate to keep the inherited AMUX_HOME.
// Set it only in the explicit environment of a helper subprocess that re-execs
// a test binary after its parent test chose a temporary AMUX_HOME, so the
// helper sees that directory instead of a fresh one. Never export it from a
// shell or CI job: that would disable the isolation.
const PreserveEnvVar = "AMUX_TEST_PRESERVE_AMUX_HOME"

// Main isolates AMUX_HOME for the package and exits with the result of m.Run.
func Main(m *testing.M) {
	os.Exit(Run(m))
}

// Run isolates AMUX_HOME for the package, runs m, removes the throwaway home,
// and returns the exit code. Use it when TestMain needs extra setup or cleanup.
func Run(m *testing.M) int {
	restore, err := Isolate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "amuxhometest: %v\n", err)
		return 1
	}
	defer restore()
	return m.Run()
}

// Isolate points AMUX_HOME at a new temporary directory and returns a function
// that restores the previous value and removes the directory. When
// PreserveEnvVar is "1", Isolate leaves AMUX_HOME unchanged.
func Isolate() (func(), error) {
	if os.Getenv(PreserveEnvVar) == "1" {
		return func() {}, nil
	}
	dir, err := os.MkdirTemp("", "amux-test-home-")
	if err != nil {
		return nil, fmt.Errorf("create isolated %s: %w", EnvVar, err)
	}
	previous, hadPrevious := os.LookupEnv(EnvVar)
	if err := os.Setenv(EnvVar, dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("set isolated %s: %w", EnvVar, err)
	}
	return func() {
		if hadPrevious {
			_ = os.Setenv(EnvVar, previous)
		} else {
			_ = os.Unsetenv(EnvVar)
		}
		_ = os.RemoveAll(dir)
	}, nil
}
