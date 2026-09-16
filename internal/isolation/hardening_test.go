//go:build linux

package isolation

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSeccompFilterAllowsOrdinaryProcessIO(t *testing.T) {
	if os.Getenv("AMUX_SECCOMP_TEST_CHILD") == "1" {
		if err := ApplySeccompDenylist(); err != nil {
			os.Exit(90)
		}
		if _, err := os.ReadFile("/dev/null"); err != nil {
			os.Exit(91)
		}
		_, _, errno := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_TRACEME, 0, 0, 0, 0, 0)
		if errno != 0 {
			os.Exit(92)
		}
		_, _, errno = unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_PEEKDATA, 0, 0, 0, 0, 0)
		if !errors.Is(errno, unix.EPERM) {
			os.Exit(93)
		}
		os.Exit(0)
	}
	if testing.Short() {
		t.Skip("subprocess hardening test")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSeccompFilterAllowsOrdinaryProcessIO$")
	cmd.Env = append(os.Environ(), "AMUX_SECCOMP_TEST_CHILD=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seccomp child: %v: %s", err, output)
	}
}
