// Package isolation abstracts the lifecycle of the actual agent/provider
// process. Trusted amuxlet/broker processes stay outside this boundary.
package isolation

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/amuxos/amux/internal/principal"
)

var (
	ErrClosed        = errors.New("process handle closed")
	ErrNotFound      = errors.New("launch not found")
	ErrIsolationLost = errors.New("isolation lifecycle authority lost")
)

type LaunchContext struct {
	LaunchID string                       `json:"launch_id"`
	Identity *principal.ExecutionIdentity `json:"identity,omitempty"`
	Backend  string                       `json:"backend,omitempty"`
}

type LaunchSpec struct {
	Path            string        `json:"path"`
	Args            []string      `json:"args,omitempty"` // excludes argv[0]
	Env             []string      `json:"env,omitempty"`
	Dir             string        `json:"dir,omitempty"`
	SetProcessGroup bool          `json:"set_process_group,omitempty"`
	Context         LaunchContext `json:"context"`
}

type ExitStatus struct {
	Code       int       `json:"code"`
	Signal     string    `json:"signal,omitempty"`
	CoreDumped bool      `json:"core_dumped,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	ExitedAt   time.Time `json:"exited_at"`
}

type LaunchBackend interface {
	Start(context.Context, LaunchSpec) (ProcessHandle, error)
}

type ProcessHandle interface {
	LaunchID() string
	PID() int
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait(context.Context) (ExitStatus, error)
	Signal(context.Context, string) error
	Kill(context.Context) error
	// Close releases local descriptors. It never kills the process.
	Close() error
}
