package isolation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type PassthroughBackend struct{}

func NewPassthroughBackend() LaunchBackend { return PassthroughBackend{} }

func (PassthroughBackend) Start(ctx context.Context, spec LaunchSpec) (ProcessHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.Path) == "" {
		return nil, errors.New("launch path is required")
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = append([]string(nil), spec.Env...)
	}
	setProcessGroup(cmd, spec.SetProcessGroup)
	// Own the pipes explicitly instead of using Cmd.StdoutPipe/StderrPipe/
	// StdinPipe: Cmd.Wait closes those the moment the child is reaped, which
	// races any reader still draining buffered output and surfaces as
	// "file already closed" instead of a clean EOF. The handle owns these fds
	// and only Close releases them, matching the helper-backed backend.
	pipes, err := newStdioPipes()
	if err != nil {
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pipes.stdinR, pipes.stdoutW, pipes.stderrW
	started := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		pipes.closeAll()
		return nil, err
	}
	// The child holds its own descriptors now; dropping the parent's copies is
	// what makes the reader ends see EOF when the child exits.
	pipes.closeChildEnds()
	launchID := strings.TrimSpace(spec.Context.LaunchID)
	if launchID == "" {
		launchID = "launch-" + uuid.NewString()
	}
	h := &passthroughHandle{
		id:      launchID,
		cmd:     cmd,
		stdin:   pipes.stdinW,
		stdout:  pipes.stdoutR,
		stderr:  pipes.stderrR,
		started: started,
		done:    make(chan struct{}),
	}
	go h.reap()
	return h, nil
}

type stdioPipes struct {
	stdinR, stdinW   *os.File
	stdoutR, stdoutW *os.File
	stderrR, stderrW *os.File
}

func newStdioPipes() (*stdioPipes, error) {
	p := &stdioPipes{}
	var err error
	if p.stdinR, p.stdinW, err = os.Pipe(); err != nil {
		return nil, err
	}
	if p.stdoutR, p.stdoutW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, err
	}
	if p.stderrR, p.stderrW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, err
	}
	return p, nil
}

func (p *stdioPipes) closeChildEnds() {
	for _, file := range []*os.File{p.stdinR, p.stdoutW, p.stderrW} {
		if file != nil {
			_ = file.Close()
		}
	}
}

func (p *stdioPipes) closeAll() {
	for _, file := range []*os.File{p.stdinR, p.stdinW, p.stdoutR, p.stdoutW, p.stderrR, p.stderrW} {
		if file != nil {
			_ = file.Close()
		}
	}
}

type passthroughHandle struct {
	id      string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	started time.Time
	done    chan struct{}

	resultMu  sync.RWMutex
	status    ExitStatus
	waitErr   error
	closeOnce sync.Once
}

func (h *passthroughHandle) reap() {
	err := h.cmd.Wait()
	status := exitStatus(h.cmd.ProcessState, h.started, time.Now().UTC())
	h.resultMu.Lock()
	h.status = status
	h.waitErr = err
	h.resultMu.Unlock()
	close(h.done)
}

func (h *passthroughHandle) LaunchID() string { return h.id }
func (h *passthroughHandle) PID() int {
	if h.cmd == nil || h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
}
func (h *passthroughHandle) Stdin() io.WriteCloser { return h.stdin }
func (h *passthroughHandle) Stdout() io.ReadCloser { return h.stdout }
func (h *passthroughHandle) Stderr() io.ReadCloser { return h.stderr }

func (h *passthroughHandle) Wait(ctx context.Context) (ExitStatus, error) {
	select {
	case <-h.done:
		h.resultMu.RLock()
		defer h.resultMu.RUnlock()
		return h.status, h.waitErr
	case <-ctx.Done():
		return ExitStatus{}, ctx.Err()
	}
}

func (h *passthroughHandle) Signal(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.cmd == nil || h.cmd.Process == nil {
		return ErrNotFound
	}
	sig, err := parseSignal(name)
	if err != nil {
		return err
	}
	if err := signalProcess(h.cmd.Process, sig, h.cmd.Process.Pid); err != nil && !processFinished(err) {
		return err
	}
	return nil
}

func (h *passthroughHandle) Kill(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.cmd == nil || h.cmd.Process == nil {
		return ErrNotFound
	}
	if err := killProcess(h.cmd.Process, h.cmd.Process.Pid); err != nil && !processFinished(err) {
		return err
	}
	return nil
}

func (h *passthroughHandle) Close() error {
	var first error
	h.closeOnce.Do(func() {
		for _, c := range []io.Closer{h.stdin, h.stdout, h.stderr} {
			if c != nil {
				if err := c.Close(); err != nil && first == nil {
					first = err
				}
			}
		}
	})
	return first
}

func processFinished(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "process already finished") ||
		strings.Contains(text, "no such process")
}

func (h *passthroughHandle) String() string {
	return fmt.Sprintf("passthrough(%s,pid=%d)", h.id, h.PID())
}
