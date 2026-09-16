package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func writeServiceIsolationConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestHelperInstallHardeningFollowsServiceConfig(t *testing.T) {
	home := writeServiceIsolationConfig(t, `[isolation]
enabled = true

[isolation.hardening]
pid_namespace = true
mount_namespace = false
seccomp = false
`)
	opts := isolationHelperInstallOptions{AmuxHome: home, PIDNamespace: true, MountNamespace: true, Seccomp: true}
	cmd := &cobra.Command{}
	cmd.Flags().Bool("pid-namespace", true, "")
	cmd.Flags().Bool("mount-namespace", true, "")
	cmd.Flags().Bool("seccomp", true, "")

	if err := applyServiceHardeningDefaults(cmd, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.PIDNamespace || opts.MountNamespace || opts.Seccomp {
		t.Fatalf("hardening = %+v", opts)
	}
}

func TestHelperInstallExplicitHardeningFlagWinsOverConfig(t *testing.T) {
	home := writeServiceIsolationConfig(t, `[isolation.hardening]
seccomp = false
`)
	opts := isolationHelperInstallOptions{AmuxHome: home, PIDNamespace: true, MountNamespace: true, Seccomp: true}
	cmd := &cobra.Command{}
	cmd.Flags().Bool("pid-namespace", true, "")
	cmd.Flags().Bool("mount-namespace", true, "")
	cmd.Flags().Bool("seccomp", true, "")
	if err := cmd.Flags().Set("seccomp", "true"); err != nil {
		t.Fatal(err)
	}

	if err := applyServiceHardeningDefaults(cmd, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.Seccomp {
		t.Fatal("explicit --seccomp=true must win over the service config")
	}
}

func TestHelperInstallHardeningDefaultsWithoutServiceConfig(t *testing.T) {
	opts := isolationHelperInstallOptions{AmuxHome: t.TempDir()}
	cmd := &cobra.Command{}
	cmd.Flags().Bool("pid-namespace", true, "")
	cmd.Flags().Bool("mount-namespace", true, "")
	cmd.Flags().Bool("seccomp", true, "")

	if err := applyServiceHardeningDefaults(cmd, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.PIDNamespace || !opts.MountNamespace || !opts.Seccomp {
		t.Fatalf("hardening = %+v", opts)
	}
}

// The helper accepts control connections only from the service user and closes
// on every other peer, root included. install/uninstall run as root, so probing
// the drain state directly always failed with a bare connection reset and the
// documented upgrade path could never complete.
func TestDrainHelperForControlDropsToSocketOwnerWhenCallerDiffers(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "isolation-helper.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ownerUID, ownerGID := os.Getuid(), os.Getgid()

	var droppedUID, droppedGID int
	direct := false
	active, err := drainHelperForControl(context.Background(), socket, ownerUID+1, drainProbe{
		direct: func(context.Context) (int, error) {
			direct = true
			return 0, nil
		},
		dropped: func(_ context.Context, uid, gid int) (int, error) {
			droppedUID, droppedGID = uid, gid
			return 2, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct {
		t.Fatal("probed the helper directly while running as another user")
	}
	if droppedUID != ownerUID || droppedGID != ownerGID {
		t.Fatalf("dropped to %d:%d, want the socket owner %d:%d", droppedUID, droppedGID, ownerUID, ownerGID)
	}
	if active != 2 {
		t.Fatalf("active = %d, want the dropped probe's answer", active)
	}
}

func TestDrainHelperForControlProbesDirectlyAsSocketOwner(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "isolation-helper.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	active, err := drainHelperForControl(context.Background(), socket, os.Getuid(), drainProbe{
		direct: func(context.Context) (int, error) { return 1, nil },
		dropped: func(context.Context, int, int) (int, error) {
			t.Fatal("re-executed as another user while already the socket owner")
			return 0, nil
		},
	})
	if err != nil || active != 1 {
		t.Fatalf("active=%d err=%v", active, err)
	}
}
