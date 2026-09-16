package mux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// errBrokerExited marks a broker that terminated before publishing its pid. The
// launch is over either way, but the exit is worth waiting no longer for and is
// worth reporting differently: a timeout reads as "too slow", while this reads
// as "it died, and the broker log says why".
var errBrokerExited = errors.New("broker exited before writing its pid file")

func waitPIDFileContext(ctx context.Context, path string, timeout time.Duration, exited <-chan error) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if pid, ok := readPIDFile(path); ok {
			return pid, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case waitErr := <-exited:
			// The pid file may have landed in the same instant the process
			// exited; prefer the published pid over the exit report.
			if pid, ok := readPIDFile(path); ok {
				return pid, nil
			}
			if waitErr != nil {
				return 0, fmt.Errorf("%w: %v", errBrokerExited, waitErr)
			}
			return 0, errBrokerExited
		case <-time.After(50 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("timeout waiting for %s", path)
}

func readPIDFile(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
