package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/isolation"
)

func TestCloseReleasesInheritedPipesBeforeWaitingForReaders(t *testing.T) {
	handle := newRetainedPipeHandle()
	defer handle.Close()
	go handle.serve()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, exec.Command("fake-acp"), StartOptions{Backend: retainedPipeBackend{handle}})
	if err != nil {
		t.Fatal(err)
	}
	// The agent has exited, but a descendant still owns stdout/stderr. The
	// process handle deliberately keeps its reader descriptors open until Close.
	handle.exitOnce.Do(func() { close(handle.exited) })
	closed := make(chan struct{})
	go func() { _ = proc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		// Unblock the failed implementation before reporting the regression.
		_ = handle.Close()
		<-closed
		t.Fatal("Close waited for inherited pipe EOF before releasing descriptors")
	}
	for name, barrier := range map[string]<-chan struct{}{"process and stderr": proc.done, "stdout reader": proc.readDone, "handle release": handle.closed} {
		select {
		case <-barrier:
		default:
			t.Fatalf("Close returned before %s finished", name)
		}
	}
}

type retainedPipeBackend struct{ handle *retainedPipeHandle }

func (b retainedPipeBackend) Start(context.Context, isolation.LaunchSpec) (isolation.ProcessHandle, error) {
	return b.handle, nil
}

type retainedPipeHandle struct {
	stdinR, stdoutR, stderrR *io.PipeReader
	stdinW, stdoutW, stderrW *io.PipeWriter
	exited, closed           chan struct{}
	exitOnce, closeOnce      sync.Once
}

func newRetainedPipeHandle() *retainedPipeHandle {
	h := &retainedPipeHandle{exited: make(chan struct{}), closed: make(chan struct{})}
	h.stdinR, h.stdinW = io.Pipe()
	h.stdoutR, h.stdoutW = io.Pipe()
	h.stderrR, h.stderrW = io.Pipe()
	return h
}

func (h *retainedPipeHandle) LaunchID() string      { return "retained-pipes" }
func (h *retainedPipeHandle) PID() int              { return 1 }
func (h *retainedPipeHandle) Stdin() io.WriteCloser { return h.stdinW }
func (h *retainedPipeHandle) Stdout() io.ReadCloser { return h.stdoutR }
func (h *retainedPipeHandle) Stderr() io.ReadCloser { return h.stderrR }
func (h *retainedPipeHandle) Wait(ctx context.Context) (isolation.ExitStatus, error) {
	select {
	case <-h.exited:
		return isolation.ExitStatus{Code: 0}, nil
	case <-ctx.Done():
		return isolation.ExitStatus{}, ctx.Err()
	}
}
func (h *retainedPipeHandle) Signal(context.Context, string) error { return nil }
func (h *retainedPipeHandle) Kill(context.Context) error {
	h.exitOnce.Do(func() { close(h.exited) })
	return nil
}
func (h *retainedPipeHandle) Close() error {
	h.closeOnce.Do(func() {
		for _, pipe := range []io.Closer{h.stdinR, h.stdinW, h.stdoutR, h.stdoutW, h.stderrR, h.stderrW} {
			_ = pipe.Close()
		}
		close(h.closed)
	})
	return nil
}

func (h *retainedPipeHandle) serve() {
	scanner := bufio.NewScanner(h.stdinR)
	for scanner.Scan() {
		var req rpcMessage
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		result := `{}`
		if req.Method == "session/new" {
			result = `{"sessionId":"retained-pipes"}`
		}
		if _, err := fmt.Fprintf(h.stdoutW, `{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", req.ID, result); err != nil {
			return
		}
	}
}
