//go:build linux

package isolation

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Explicit opt-in on a disposable helper. No OS accounts or production helper
// state are created: the worker uses an otherwise unused numeric UID.
func TestSessionFileTargetUID(t *testing.T) {
	binary := os.Getenv("AMUX_TEST_HELPER_BINARY")
	uidText := os.Getenv("AMUX_TEST_FILE_UID")
	if binary == "" || uidText == "" || os.Geteuid() != 0 {
		t.Skip("requires root, AMUX_TEST_HELPER_BINARY and unused AMUX_TEST_FILE_UID")
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid < 10000 {
		t.Fatal("invalid test uid")
	}
	root, err := os.MkdirTemp("/tmp", "amux-session-files-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err = os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := HelperConfig{SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"), PrincipalRoot: filepath.Join(root, "principals"), ServiceUID: 0, ServiceGID: 0, UIDRangeStart: uid, UIDRangeEnd: uid, MaterializerPath: binary, SkipAccountProvision: true}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("helper did not stop")
		}
	}()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)
	id := uuid.NewString()
	mapping := AccountMapping{PrincipalID: id, Account: "amux_" + uuid.NewString()[:8] + "abcd", UID: uid, GID: uid}
	paths, err := client.PrepareRun(ctx, mapping, "files_test", "files-launch")
	if err != nil {
		t.Fatal(err)
	}
	// More than the helper JSON frame limit, proving the byte path uses FDs.
	input := bytes.Repeat([]byte("file"), (5<<20)/4)
	rel, err := client.SessionFile(ctx, mapping, "files_test", "put", "input.bin", input)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(paths.Workspace, string(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if int(info.Sys().(*syscall.Stat_t).Uid) != uid {
		t.Fatal("attachment was written by wrong uid")
	}
	outbox := filepath.Join(paths.Workspace, "amux-outbox")
	if err = os.Mkdir(outbox, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(outbox, uid, uid); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(outbox, "report.bin")
	if err = os.WriteFile(output, input, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(output, uid, uid); err != nil {
		t.Fatal(err)
	}
	data, err := client.SessionFile(ctx, mapping, "files_test", "read", "report.bin", nil)
	if err != nil || !bytes.Equal(data, input) {
		t.Fatalf("output mismatch: %v", err)
	}
	secret := filepath.Join(outbox, "root-secret")
	if err = os.WriteFile(secret, []byte("root-owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = client.SessionFile(ctx, mapping, "files_test", "read", "root-secret", nil); err == nil {
		t.Fatal("root privilege leaked into reader")
	}
	if err = syscall.Mkfifo(filepath.Join(outbox, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chown(filepath.Join(outbox, "pipe"), uid, uid)
	if _, err = client.SessionFile(ctx, mapping, "files_test", "read", "pipe", nil); err == nil {
		t.Fatal("accepted FIFO")
	}
	if _, err = client.SessionFile(ctx, mapping, "files_test", "read", "../secret", nil); err == nil {
		t.Fatal("accepted traversal")
	}
}
