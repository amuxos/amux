//go:build !linux

package isolation

import "fmt"

const (
	MountViewRootEnv       = "AMUX_ISOLATION_PRINCIPAL_ROOT"
	MountViewAccountEnv    = "AMUX_ISOLATION_ACCOUNT"
	MountViewEnabledEnv    = "AMUX_ISOLATION_MOUNT_VIEW"
	PIDNamespaceEnabledEnv = "AMUX_ISOLATION_PID_NAMESPACE"
	SeccompEnabledEnv      = "AMUX_ISOLATION_SECCOMP"
)

func ApplyPrincipalMountView(_, _ string) error {
	return fmt.Errorf("principal mount view is only available on Linux")
}

func ApplyPIDNamespaceProcView() error {
	return fmt.Errorf("PID namespace hardening is only available on Linux")
}

func ApplySeccompDenylist() error {
	return fmt.Errorf("seccomp hardening is only available on Linux")
}
