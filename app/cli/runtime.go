package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
)

func localRunningSessions(ctx context.Context) ([]*sessionEntry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := newLocalRuntimeClientWithTimeout(500 * time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	sessions, err := client.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*sessionEntry, 0, len(sessions))
	for _, sess := range sessions {
		if sess.State == "running" {
			out = append(out, sess)
		}
	}
	return out, nil
}

func guardRuntimeStopWithGuidance(force bool, sessions []*sessionEntry, guidance string) error {
	running := runningSessionNames(sessions)
	if len(running) == 0 || force {
		return nil
	}
	return fmt.Errorf("runtime stop will make local target temporarily unavailable for running session(s): %s\nUse `%s` to continue.", strings.Join(running, ", "), guidance)
}

func runningSessionNames(sessions []*sessionEntry) []string {
	var names []string
	for _, sess := range sessions {
		if sess != nil && sess.State == "running" {
			names = append(names, sess.Name)
		}
	}
	return names
}

func runtimeService() managedService {
	return managedService{
		Name:    "runtime",
		Binary:  "amuxlet",
		PIDPath: filepath.Join(defaultAmuxDir(), "runtime.pid"),
		LogPath: filepath.Join(defaultAmuxDir(), "runtime.log"),
	}
}

func runSiblingBinary(name string, args ...string) error {
	path, err := siblingBinaryPath(name)
	if err != nil {
		return err
	}
	c := exec.Command(path, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func defaultRuntimeSocketPath() string {
	return filepath.Join(defaultAmuxDir(), "runtime.sock")
}

func defaultAmuxDir() string {
	return amuxhome.Dir()
}
