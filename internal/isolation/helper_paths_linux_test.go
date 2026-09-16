//go:build linux

package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The principal owns the intermediate account/workspace/run directories, so the
// root helper must never resolve a symlink when it applies ownership or mode.
// These tests pin the no-follow behaviour without needing root: a path-based
// chmod/chown would succeed on the decoy target instead of failing.

func TestEnsureOwnedDirRefusesSymlinkedPath(t *testing.T) {
	root := t.TempDir()
	decoy := filepath.Join(root, "decoy")
	if err := os.Mkdir(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	swapped := filepath.Join(root, "home")
	if err := os.Symlink(decoy, swapped); err != nil {
		t.Fatal(err)
	}

	if err := ensureOwnedDir(swapped, 0o700, os.Getuid(), os.Getgid(), true); err == nil {
		t.Fatal("ensureOwnedDir followed a symlinked directory")
	}
	info, err := os.Stat(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("decoy mode changed through the symlink: %v", info.Mode().Perm())
	}
}

func TestEnsureOwnedDirIsIdempotentOnRealDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace")
	for range 2 {
		if err := ensureOwnedDir(path, 0o700, os.Getuid(), os.Getgid(), true); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("ensured dir = %v", info.Mode())
	}
}

// chownTreeSecure re-owns the agent's own workspace on every launch, and agents
// legitimately create symlinks there — one `npm install` leaves several in
// node_modules/.bin. Rejecting them made that session permanently unlaunchable.
//
// The escape defense is chowning the link itself and never descending through
// it, which is what this asserts: the FIFO planted in the symlink's target
// would fail the walk if it were ever followed, and special files inside the
// tree are still rejected (see TestChownTreeSecureRejectsSpecialFiles).
func TestChownTreeSecureKeepsSymlinksAndDoesNotDescendThroughThem(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := makeFIFO(filepath.Join(outside, "tripwire")); err != nil {
		t.Skipf("cannot create FIFO: %v", err)
	}
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(nested, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if err := chownTreeSecure(root, os.Getuid(), os.Getgid(), true); err != nil {
		t.Fatalf("chownTreeSecure rejected a symlink the agent legitimately created: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink did not survive the walk: %v %v", info, err)
	}
}

func TestChownTreeSecureRejectsSpecialFiles(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "fifo")
	if err := makeFIFO(fifo); err != nil {
		t.Skipf("cannot create FIFO: %v", err)
	}
	if err := chownTreeSecure(root, os.Getuid(), os.Getgid(), true); err == nil {
		t.Fatal("chownTreeSecure accepted a FIFO in the tree")
	}
}

func TestChownTreeSecureWalksRegularTree(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "file"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := chownTreeSecure(root, os.Getuid(), os.Getgid(), true); err != nil {
		t.Fatal(err)
	}
}

func TestHelperWaitHonoursCallerCancellation(t *testing.T) {
	root := t.TempDir()
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	uid := max(os.Getuid(), 1)
	cfg := HelperConfig{
		SocketPath:           filepath.Join(root, "helper.sock"),
		StateDir:             filepath.Join(root, "state"),
		PrincipalRoot:        filepath.Join(root, "principals"),
		ServiceUID:           os.Getuid(),
		ServiceGID:           os.Getgid(),
		UIDRangeStart:        uid,
		UIDRangeEnd:          uid + 10,
		AllowUnprivileged:    true,
		SkipAccountProvision: true,
	}
	go func() { _ = ServeHelper(serveCtx, cfg) }()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)

	mapping := AccountMapping{
		PrincipalID: "5b3f6d10-0e2a-4d43-9a63-6a2a2f0f61c4",
		Account:     "amux_abcdef012345",
		UID:         uid,
		GID:         uid,
	}
	paths, err := client.PrepareRun(context.Background(), mapping, "exec_cancel", "launch-cancel")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := client.Start(context.Background(), mapping, LaunchSpec{
		Path: "/bin/sh", Args: []string{"-c", "sleep 300"}, Env: []string{"PATH=/usr/bin:/bin"},
		Dir: paths.Workspace, Context: LaunchContext{LaunchID: "launch-cancel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = handle.Kill(context.Background())
		_ = handle.Close()
	}()

	waitCtx, cancelWait := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, waitErr := handle.Wait(waitCtx)
		done <- waitErr
	}()
	time.Sleep(100 * time.Millisecond)
	cancelWait()
	select {
	case waitErr := <-done:
		if waitErr != context.Canceled {
			t.Fatalf("cancelled Wait returned %v, want context.Canceled", waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Wait did not return")
	}

	// Cancelling an observer must not kill the child.
	desc, err := client.Describe(context.Background(), "launch-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if desc.State != "running" {
		t.Fatalf("cancelled Wait changed launch state to %q", desc.State)
	}
}

func makeFIFO(path string) error { return unix.Mkfifo(path, 0o600) }

// TestServeHelperKeepsPrincipalRootTraversable pins the directory-creation
// order. StateDir is root-only 0700 and usually shares an ancestor with
// PrincipalRoot; creating it first left that ancestor at 0700 and made every
// principal path unreachable for the amuxlet service uid, which surfaced only
// as an opaque "mkdir ...: permission denied" on the first identified launch.
func TestServeHelperKeepsPrincipalRootTraversable(t *testing.T) {
	base := t.TempDir()
	shared := filepath.Join(base, "amux")
	cfg := HelperConfig{
		SocketPath:           filepath.Join(base, "helper.sock"),
		StateDir:             filepath.Join(shared, "isolation", "1001"),
		PrincipalRoot:        filepath.Join(shared, "principals"),
		ServiceUID:           os.Getuid(),
		ServiceGID:           os.Getgid(),
		UIDRangeStart:        max(os.Getuid(), 1),
		UIDRangeEnd:          max(os.Getuid(), 1) + 10,
		AllowUnprivileged:    true,
		SkipAccountProvision: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	waitHelperReady(t, NewHelperClient(cfg.SocketPath))
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not stop")
	}

	for path := filepath.Clean(cfg.PrincipalRoot); path != base; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is mode %04o; the service identity cannot traverse it", path, info.Mode().Perm())
		}
	}
	// The root-only state dir itself must stay private.
	info, err := os.Stat(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state dir is mode %04o; it must not be group/world accessible", info.Mode().Perm())
	}
}

func TestVerifyServiceTraversalRejectsUnreachableRoot(t *testing.T) {
	base := t.TempDir()
	blocked := filepath.Join(base, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	principals := filepath.Join(blocked, "principals")
	if err := os.Mkdir(principals, 0o711); err != nil {
		t.Fatal(err)
	}
	// A uid/gid that owns nothing here has neither owner nor group rights.
	err := verifyServiceTraversal(principals, os.Getuid()+4242, os.Getgid()+4242)
	if err == nil {
		t.Fatal("expected an unreachable principal root to be rejected at startup")
	}
	if !strings.Contains(err.Error(), "cannot traverse") {
		t.Fatalf("error = %v", err)
	}
}
