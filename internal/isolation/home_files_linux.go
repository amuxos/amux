//go:build linux

package isolation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ReadHomeFiles copies bounded HOME-relative regular files out of one
// principal's HOME. The helper runs a target-UID worker, so the read is bounded
// by the principal's own DAC permissions and by os.Root confinement; the
// privileged process never opens the files itself.
func (c *HelperClient) ReadHomeFiles(ctx context.Context, mapping AccountMapping, rels []string) (map[string][]byte, error) {
	if len(rels) == 0 || len(rels) > MaxHomeReadFiles {
		return nil, fmt.Errorf("home file read needs 1-%d files", MaxHomeReadFiles)
	}
	resp, files, err := c.request(ctx, HelperRequest{Operation: OpReadHomeFiles, Mapping: &mapping, HomeFiles: rels}, 1)
	if err != nil {
		if resp.ErrorCode == "unsupported_operation" {
			return nil, fmt.Errorf("%w: %v", ErrHomeFilesUnsupported, err)
		}
		return nil, err
	}
	defer files[0].Close()
	data, err := io.ReadAll(io.LimitReader(files[0], maxHomeReadPayloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHomeReadPayloadBytes {
		return nil, fmt.Errorf("home file payload exceeds %d bytes", maxHomeReadPayloadBytes)
	}
	var payload homeFilesPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode home files: %w", err)
	}
	for rel, content := range payload.Files {
		if _, err := cleanHomeReadRel(rel); err != nil {
			return nil, err
		}
		if len(content) > MaxHomeReadFileBytes {
			return nil, fmt.Errorf("home file %s exceeds %d bytes", rel, MaxHomeReadFileBytes)
		}
	}
	return payload.Files, nil
}

func (s *helperServer) readHomeFiles(req HelperRequest) (*os.File, error) {
	if err := s.validateMapping(req.Mapping); err != nil {
		return nil, err
	}
	if len(req.HomeFiles) == 0 || len(req.HomeFiles) > MaxHomeReadFiles {
		return nil, fmt.Errorf("home file read needs 1-%d files", MaxHomeReadFiles)
	}
	paths := s.pathsFor(*req.Mapping, "")
	rels := make([]string, 0, len(req.HomeFiles))
	for _, rel := range req.HomeFiles {
		cleaned, err := cleanHomeReadRel(rel)
		if err != nil {
			return nil, err
		}
		rels = append(rels, cleaned)
	}
	if err := rejectSymlinkPath(paths.Root, paths.Home); err != nil {
		return nil, err
	}
	out, err := os.CreateTemp("", "amux-home-files-*")
	if err != nil {
		return nil, err
	}
	defer out.Close()
	defer os.Remove(out.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The running helper supplies the worker; a separately installed
	// materializer can predate this worker protocol.
	worker := s.cfg.ExecWrapperPath
	if worker == "" {
		worker, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, worker, append([]string{"_read-home-files", paths.Home}, rels...)...)
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Env = []string{"HOME=" + paths.Home, "PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if !s.cfg.AllowUnprivileged {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(req.Mapping.UID), Gid: uint32(req.Mapping.GID), NoSetGroups: true}
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("target-uid home reader: %w: %.4096s", err, stderr.String())
	}
	return os.Open(out.Name())
}
