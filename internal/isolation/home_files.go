package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/amuxos/amux/internal/principal"
)

// Read-home bounds are deliberately small: this operation exists to bring a
// handful of per-principal login files out of an isolated HOME, not to move
// data.
const (
	MaxHomeReadFiles      = 16
	MaxHomeReadFileBytes  = 64 << 10
	MaxHomeReadTotalBytes = 256 << 10

	// Base64 JSON overhead has to fit the helper frame budget too.
	maxHomeReadPayloadBytes = MaxHomeReadTotalBytes/3*4 + (64 << 10)
)

// ErrHomeFilesUnsupported marks a helper that predates the read_home_files
// operation. Callers treat it like any other read failure and keep the service
// projection; the marker exists for tests and diagnostics.
var ErrHomeFilesUnsupported = errors.New("home_files_helper_upgrade_required")

type homeFilesPayload struct {
	Files map[string][]byte `json:"files"`
}

// ReadPrincipalHomeFiles reads HOME-relative files from one principal's own
// HOME as that principal. The service user cannot open a 0700 principal HOME
// itself, so the read goes through the helper, which runs a target-UID worker:
// files the principal cannot read (or that only root owns) stay unread. It
// backs per-principal credential derivation; callers treat any error as "keep
// the service projection".
func (r *Runtime) ReadPrincipalHomeFiles(ctx context.Context, identity principal.ExecutionIdentity, rels []string) (map[string][]byte, error) {
	if r == nil || identity.IsolationBackend != principal.BackendLinuxDAC || !identity.Identified() || r.store == nil {
		return nil, fmt.Errorf("linux-dac home read runtime is unavailable")
	}
	mapping, err := r.store.Ensure(identity.EffectivePrincipalID)
	if err != nil {
		return nil, err
	}
	return r.client.ReadHomeFiles(ctx, mapping, rels)
}

// ReadHomeFilesWorker runs as the execution identity (the helper's target-UID
// worker) and copies HOME-relative regular files to out as a JSON object keyed
// by relative path. Missing files are omitted so the caller can distinguish
// "not logged in" from a read failure. The home root confines every
// resolution, so a swapped symlink cannot escape it.
func ReadHomeFilesWorker(home string, rels []string, out io.Writer) error {
	if len(rels) == 0 || len(rels) > MaxHomeReadFiles {
		return fmt.Errorf("home file read needs 1-%d files", MaxHomeReadFiles)
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	files := map[string][]byte{}
	total := 0
	for _, rel := range rels {
		cleaned, err := cleanHomeReadRel(rel)
		if err != nil {
			return err
		}
		data, err := readHomeRegularFile(root, cleaned, MaxHomeReadFileBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", cleaned, err)
		}
		total += len(data)
		if total > MaxHomeReadTotalBytes {
			return fmt.Errorf("home files exceed %d bytes", MaxHomeReadTotalBytes)
		}
		files[cleaned] = data
	}
	return json.NewEncoder(out).Encode(homeFilesPayload{Files: files})
}

func cleanHomeReadRel(rel string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(rel))
	if cleaned == "" || cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid home file path %q", rel)
	}
	return cleaned, nil
}

func readHomeRegularFile(root *os.Root, rel string, maxBytes int64) ([]byte, error) {
	// NONBLOCK keeps a malicious FIFO replacement from blocking the worker.
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("home file %s is not a regular file", rel)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("home file %s exceeds %d bytes", rel, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("home file %s exceeds %d bytes", rel, maxBytes)
	}
	return data, nil
}
