//go:build linux

package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	MountViewRootEnv       = "AMUX_ISOLATION_PRINCIPAL_ROOT"
	MountViewAccountEnv    = "AMUX_ISOLATION_ACCOUNT"
	MountViewEnabledEnv    = "AMUX_ISOLATION_MOUNT_VIEW"
	PIDNamespaceEnabledEnv = "AMUX_ISOLATION_PID_NAMESPACE"
	SeccompEnabledEnv      = "AMUX_ISOLATION_SECCOMP"
)

var hardeningAccountPattern = regexp.MustCompile(`^amux_[a-f0-9]{12,32}$`)

// ApplyPrincipalMountView hides sibling principal roots while keeping the
// current principal's account root at its stable absolute path. It must run
// inside a dedicated user+mount namespace before seccomp is installed.
func ApplyPrincipalMountView(principalRoot, account string) error {
	principalRoot = filepath.Clean(principalRoot)
	if !filepath.IsAbs(principalRoot) || principalRoot == "/" {
		return fmt.Errorf("invalid principal root %q", principalRoot)
	}
	if !hardeningAccountPattern.MatchString(account) {
		return fmt.Errorf("invalid principal account %q", account)
	}
	ownRoot := filepath.Join(principalRoot, account)
	info, err := os.Lstat(ownRoot)
	if err != nil {
		return fmt.Errorf("stat principal root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("principal account root is not a real directory")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mount tree private: %w", err)
	}
	stage, err := os.MkdirTemp("/tmp", ".amux-mount-view-")
	if err != nil {
		return fmt.Errorf("create mount staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	stagedRoot := filepath.Join(stage, "principal")
	if err := os.Mkdir(stagedRoot, 0o700); err != nil {
		return err
	}
	if err := unix.Mount(ownRoot, stagedRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("stage principal bind mount: %w", err)
	}
	defer unix.Unmount(stagedRoot, unix.MNT_DETACH)
	if err := unix.Mount("tmpfs", principalRoot, "tmpfs",
		unix.MS_NOSUID|unix.MS_NODEV, "mode=0711,size=1m"); err != nil {
		return fmt.Errorf("hide principal root: %w", err)
	}
	exposedRoot := filepath.Join(principalRoot, account)
	if err := os.Mkdir(exposedRoot, 0o700); err != nil {
		return fmt.Errorf("create exposed principal root: %w", err)
	}
	if err := unix.Mount(stagedRoot, exposedRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("expose principal bind mount: %w", err)
	}
	return nil
}

// ApplyPIDNamespaceProcView mounts procfs from inside the launch's PID and
// mount namespaces. Reusing the host procfs mount makes namespace-local PIDs
// disagree with /proc and breaks process tools such as ps and pgrep.
func ApplyPIDNamespaceProcView() error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mount tree private for procfs: %w", err)
	}
	if err := unix.Mount("proc", "/proc", "proc",
		unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount PID namespace procfs: %w", err)
	}
	return nil
}

// ApplySeccompDenylist installs a launch-local filter. It is called only by the
// unprivileged re-exec worker, never by the privileged helper server.
func ApplySeccompDenylist() error {
	var auditArch uint32
	switch runtime.GOARCH {
	case "amd64":
		auditArch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		auditArch = unix.AUDIT_ARCH_AARCH64
	default:
		return fmt.Errorf("unsupported seccomp architecture %q", runtime.GOARCH)
	}
	denied := []uint32{
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_UMOUNT2),
		uint32(unix.SYS_KEYCTL),
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_OPEN_BY_HANDLE_AT),
		uint32(unix.SYS_INIT_MODULE),
		uint32(unix.SYS_FINIT_MODULE),
		uint32(unix.SYS_DELETE_MODULE),
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4}, // offsetof(seccomp_data, arch)
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: auditArch},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}, // offsetof(seccomp_data, nr)
	}
	if runtime.GOARCH == "amd64" {
		// The x32 ABI uses tagged syscall numbers and would otherwise bypass
		// exact denylist comparisons.
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, Jf: 1, K: 0x40000000},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		)
	}
	for _, syscallNumber := range denied {
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: syscallNumber},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		)
	}
	// PTRACE_TRACEME (request=0) is harmless: it only marks the calling
	// process as traceable by its own parent, which is already the case in
	// our isolated environment.  procps-ng (ps, top, pgrep) calls it during
	// library initialisation; denying it makes those tools fail with
	// "fatal library error, lookup self".  All other ptrace requests remain
	// denied because they can attach to or manipulate other processes.
	//
	// BPF layout for the ptrace exception:
	//   if nr != SYS_PTRACE → fall through to ALLOW
	//   if nr == SYS_PTRACE:
	//     if args[0] == 0 (PTRACE_TRACEME) → ALLOW
	//     else → EPERM
	filter = append(filter,
		// Check: nr == SYS_PTRACE?  If not, skip 4 instructions to default ALLOW.
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 4, K: uint32(unix.SYS_PTRACE)},
		// Load args[0] (seccomp_data offset 16)
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		// Check: args[0] == 0 (PTRACE_TRACEME)?  If yes, skip 1 to ALLOW.
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: 0},
		// Not TRACEME → deny
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		// TRACEME → allow
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	)
	// Default allow for all other syscalls.
	filter = append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})

	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filter)
	if errno != 0 {
		return fmt.Errorf("seccomp filter: %w", errno)
	}
	return nil
}
