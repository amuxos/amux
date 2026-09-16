//go:build !windows

package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// execAccessRoot builds fixtures under /tmp rather than t.TempDir(): the checker
// walks every ancestor, and the per-user temp root is 0700 on macOS, which would
// otherwise deny the fixture before the interesting component is reached.
func execAccessRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "amux-execaccess-")
	if err != nil {
		t.Fatalf("mkdir temp root: %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatalf("chmod temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// /tmp is a symlink to /private/tmp on macOS; canonicalise so fixtures and
	// reported components are expressed in the same form.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	return resolved
}

func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func foreignIDs(t *testing.T) (int, int) {
	t.Helper()
	uid, gid := os.Getuid()+700001, os.Getgid()+700001
	return uid, gid
}

// A provider binary can be 0755 while a release directory on its symlink
// target chain is 0700. The denied directory must be reported instead of a
// bare EACCES from execve.
func TestCheckExecAccessForUIDReportsBlockingDirectoryBehindSymlink(t *testing.T) {
	root := execAccessRoot(t)
	share := filepath.Join(root, "share")
	release := filepath.Join(share, "release")
	binDir := filepath.Join(root, "bin")
	mkdirMode(t, share, 0o755)
	mkdirMode(t, release, 0o700)
	mkdirMode(t, binDir, 0o755)
	target := filepath.Join(release, "example-agent")
	writeExecutable(t, target, 0o755)
	link := filepath.Join(binDir, "example-agent")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	uid, gid := foreignIDs(t)
	denial := CheckExecAccessForUID(link, uid, gid)
	if denial == nil {
		t.Fatalf("expected a denial for uid %d on %s", uid, link)
	}
	if denial.Component != release {
		t.Fatalf("denial.Component = %q, want %q", denial.Component, release)
	}
	if !denial.Directory {
		t.Fatalf("denial.Directory = false, want true for %s", release)
	}
	msg := denial.Error()
	for _, want := range []string{link, release, "0700"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("denial message %q does not mention %q", msg, want)
		}
	}
}

func TestCheckExecAccessForUIDAllowsWorldExecutableChain(t *testing.T) {
	root := execAccessRoot(t)
	binDir := filepath.Join(root, "bin")
	mkdirMode(t, binDir, 0o755)
	bin := filepath.Join(binDir, "example-agent")
	writeExecutable(t, bin, 0o755)

	uid, gid := foreignIDs(t)
	if denial := CheckExecAccessForUID(bin, uid, gid); denial != nil {
		t.Fatalf("unexpected denial: %v", denial)
	}
}

func TestCheckExecAccessForUIDReportsNonExecutableFile(t *testing.T) {
	root := execAccessRoot(t)
	bin := filepath.Join(root, "example-agent")
	writeExecutable(t, bin, 0o644)

	uid, gid := foreignIDs(t)
	denial := CheckExecAccessForUID(bin, uid, gid)
	if denial == nil {
		t.Fatalf("expected a denial for a non-executable file")
	}
	if denial.Component != bin || denial.Directory {
		t.Fatalf("denial = %+v, want file denial on %s", denial, bin)
	}
}

func TestCheckExecAccessForUIDAllowsOwnerOfPrivateDirectory(t *testing.T) {
	root := execAccessRoot(t)
	private := filepath.Join(root, "private")
	mkdirMode(t, private, 0o700)
	bin := filepath.Join(private, "example-agent")
	writeExecutable(t, bin, 0o700)

	if denial := CheckExecAccessForUID(bin, os.Getuid(), os.Getgid()); denial != nil {
		t.Fatalf("unexpected denial for the owning uid: %v", denial)
	}
}

// A missing binary is ENOENT, not EACCES. Reporting a permission denial there
// would send operators after the wrong problem.
func TestCheckExecAccessForUIDIgnoresMissingPath(t *testing.T) {
	root := execAccessRoot(t)
	uid, gid := foreignIDs(t)
	if denial := CheckExecAccessForUID(filepath.Join(root, "absent"), uid, gid); denial != nil {
		t.Fatalf("unexpected denial for a missing path: %v", denial)
	}
}

func TestCheckExecAccessForUIDIgnoresRelativePath(t *testing.T) {
	uid, gid := foreignIDs(t)
	if denial := CheckExecAccessForUID("example-agent", uid, gid); denial != nil {
		t.Fatalf("unexpected denial for a relative path: %v", denial)
	}
}

// The wrapper runs as the principal already, so it asks the kernel rather than
// re-deriving DAC bits: that answer also covers ACLs and namespace id mapping.
func TestCheckExecAccessForCallerReportsNonExecutableFile(t *testing.T) {
	root := execAccessRoot(t)
	bin := filepath.Join(root, "example-agent")
	writeExecutable(t, bin, 0o644)

	denial := CheckExecAccessForCaller(bin)
	if denial == nil {
		t.Fatalf("expected a denial for a non-executable file")
	}
	if denial.Component != bin {
		t.Fatalf("denial.Component = %q, want %q", denial.Component, bin)
	}
}

func TestCheckExecAccessForCallerAllowsExecutableFile(t *testing.T) {
	root := execAccessRoot(t)
	bin := filepath.Join(root, "example-agent")
	writeExecutable(t, bin, 0o755)

	if denial := CheckExecAccessForCaller(bin); denial != nil {
		t.Fatalf("unexpected denial: %v", denial)
	}
}

// execve reports only the errno. Without the blocking component the operator
// sees a bare "permission denied" in the broker log and has nothing to act on.
func TestDescribeExecFailureAddsBlockingComponent(t *testing.T) {
	root := execAccessRoot(t)
	bin := filepath.Join(root, "example-agent")
	writeExecutable(t, bin, 0o644)

	msg := DescribeExecFailure(bin, syscall.EACCES)
	for _, want := range []string{bin, "permission denied", "not executable"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q does not mention %q", msg, want)
		}
	}
}

func TestDescribeExecFailureKeepsUnrelatedErrors(t *testing.T) {
	root := execAccessRoot(t)
	bin := filepath.Join(root, "example-agent")
	writeExecutable(t, bin, 0o755)

	msg := DescribeExecFailure(bin, syscall.ENOEXEC)
	if !strings.Contains(msg, bin) || !strings.Contains(msg, syscall.ENOEXEC.Error()) {
		t.Fatalf("message %q lost the original failure", msg)
	}
	if strings.Contains(msg, "not executable") {
		t.Fatalf("message %q invented an access denial: %s", msg, bin)
	}
}

// The principal often cannot stat the binary at all: resolving the leaf symlink
// crosses the very directory that denies it. The walk has to read the link
// itself rather than lean on a stat that is already refused.
func TestCheckExecAccessForCallerReportsBlockedSymlinkTarget(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses DAC on the closed directory")
	}
	root := execAccessRoot(t)
	share := filepath.Join(root, "share")
	release := filepath.Join(share, "release")
	binDir := filepath.Join(root, "bin")
	mkdirMode(t, share, 0o755)
	mkdirMode(t, release, 0o755)
	mkdirMode(t, binDir, 0o755)
	target := filepath.Join(release, "example-agent")
	writeExecutable(t, target, 0o755)
	link := filepath.Join(binDir, "example-agent")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Chmod(release, 0o000); err != nil {
		t.Fatalf("close release dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(release, 0o755) })

	denial := CheckExecAccessForCaller(link)
	if denial == nil {
		t.Fatal("expected a denial for a binary behind a closed directory")
	}
	if denial.Component != release {
		t.Fatalf("denial.Component = %q, want %q", denial.Component, release)
	}
	if denial.Path != link {
		t.Fatalf("denial.Path = %q, want the requested binary %q", denial.Path, link)
	}
}
