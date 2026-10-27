//go:build linux

package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A helper that predates read_home_files must fail the request, not the
// launch: callers fall back to the service projection.
func TestReadHomeFilesUnsupportedHelperFallsBack(t *testing.T) {
	address := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: address, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		payload := make([]byte, MaxHelperFrameBytes+4)
		n, _, _, _, err := conn.ReadMsgUnix(payload, nil)
		if err != nil {
			done <- err
			return
		}
		var req HelperRequest
		if err := decodeHelperFrame(payload[:n], &req); err != nil {
			done <- err
			return
		}
		srv := &helperServer{}
		done <- srv.writeResponse(conn, HelperResponse{ProtocolVersion: HelperProtocolVersion, RequestID: req.RequestID, ErrorCode: "unsupported_operation", Error: "unsupported helper operation"}, nil)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = NewHelperClient(address).ReadHomeFiles(ctx, AccountMapping{}, []string{"a.json"})
	if err == nil || !errors.Is(err, ErrHomeFilesUnsupported) {
		t.Fatalf("error = %v, want ErrHomeFilesUnsupported", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The client decodes the fd payload the helper returns from its worker.
func TestReadHomeFilesClientDecodesPayload(t *testing.T) {
	address := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: address, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		payload := make([]byte, MaxHelperFrameBytes+4)
		n, _, _, _, err := conn.ReadMsgUnix(payload, nil)
		if err != nil {
			done <- err
			return
		}
		var req HelperRequest
		if err := decodeHelperFrame(payload[:n], &req); err != nil {
			done <- err
			return
		}
		out, err := os.CreateTemp("", "amux-home-read-test-*")
		if err != nil {
			done <- err
			return
		}
		defer os.Remove(out.Name())
		if err := json.NewEncoder(out).Encode(homeFilesPayload{Files: map[string][]byte{"a.json": []byte("token-1")}}); err != nil {
			done <- err
			return
		}
		if _, err := out.Seek(0, 0); err != nil {
			done <- err
			return
		}
		srv := &helperServer{}
		done <- srv.writeResponse(conn, HelperResponse{ProtocolVersion: HelperProtocolVersion, RequestID: req.RequestID}, []*os.File{out})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	files, err := NewHelperClient(address).ReadHomeFiles(ctx, AccountMapping{}, []string{"a.json"})
	if err != nil {
		t.Fatal(err)
	}
	if string(files["a.json"]) != "token-1" {
		t.Fatalf("files = %v", files)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Explicit opt-in on a disposable helper (root): the worker must drop to the
// principal uid, so a root-owned file in the same HOME stays unreadable and a
// traversal never escapes. No OS accounts or production helper state are
// created: the worker uses an otherwise unused numeric UID.
func TestReadHomeFilesTargetUID(t *testing.T) {
	binary := os.Getenv("AMUX_TEST_HELPER_BINARY")
	uidText := os.Getenv("AMUX_TEST_FILE_UID")
	if binary == "" || uidText == "" || os.Geteuid() != 0 {
		t.Skip("requires root, AMUX_TEST_HELPER_BINARY and unused AMUX_TEST_FILE_UID")
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid < 10000 {
		t.Fatal("invalid test uid")
	}
	root, err := os.MkdirTemp("/tmp", "amux-home-read-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err = os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := HelperConfig{SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"), PrincipalRoot: filepath.Join(root, "principals"), ServiceUID: 0, ServiceGID: 0, UIDRangeStart: uid, UIDRangeEnd: uid, MaterializerPath: "/bin/false", ExecWrapperPath: binary, SkipAccountProvision: true}
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
	mapping := AccountMapping{PrincipalID: uuid.NewString(), Account: "amux_" + uuid.NewString()[:8] + "abcd", UID: uid, GID: uid}
	paths, err := client.PrepareRun(ctx, mapping, "homeread_test", "homeread-launch")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Home == "" {
		t.Fatal("helper omitted the principal home path")
	}
	dir := filepath.Join(paths.Home, ".local", "share")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, owned := range []string{filepath.Join(paths.Home, ".local"), dir} {
		if err := os.Chown(owned, uid, uid); err != nil {
			t.Fatal(err)
		}
	}
	tokenRel := ".local/share/token.json"
	if err := os.WriteFile(filepath.Join(paths.Home, tokenRel), []byte("principal-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(filepath.Join(paths.Home, tokenRel), uid, uid); err != nil {
		t.Fatal(err)
	}
	files, err := client.ReadHomeFiles(ctx, mapping, []string{tokenRel, ".local/share/missing.json"})
	if err != nil {
		t.Fatal(err)
	}
	if string(files[tokenRel]) != "principal-token" {
		t.Fatalf("files = %v", files)
	}
	if _, ok := files[".local/share/missing.json"]; ok {
		t.Fatal("missing file was reported")
	}
	// A root-owned file the principal cannot read must not leak through the
	// privileged helper.
	rootOnly := filepath.Join(dir, "root-only.json")
	if err := os.WriteFile(rootOnly, []byte("root-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadHomeFiles(ctx, mapping, []string{".local/share/root-only.json"}); err == nil {
		t.Fatal("root-owned home file leaked into the principal read")
	}
	if _, err := client.ReadHomeFiles(ctx, mapping, []string{"../escape"}); err == nil {
		t.Fatal("accepted traversal")
	}
}
