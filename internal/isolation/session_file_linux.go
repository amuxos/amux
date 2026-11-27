//go:build linux

package isolation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/amuxos/amux/internal/sessionfiles"
)

func (c *HelperClient) SessionFile(ctx context.Context, mapping AccountMapping, workspaceID, action, name string, data []byte) ([]byte, error) {
	var inputs []*os.File
	if action == "put" {
		f, err := os.CreateTemp("", "amux-inbox-*")
		if err != nil {
			return nil, err
		}
		defer f.Close()
		_ = os.Remove(f.Name())
		if _, err = f.Write(data); err != nil {
			return nil, err
		}
		if _, err = f.Seek(0, 0); err != nil {
			return nil, err
		}
		inputs = []*os.File{f}
	}
	resp, files, err := c.requestWithFiles(ctx, HelperRequest{Operation: OpSessionFile, Mapping: &mapping, ExecutionID: workspaceID, FileAction: action, FileName: name}, inputs, 1)
	if err != nil {
		switch resp.ErrorCode {
		case "unsupported_operation":
			return nil, fmt.Errorf("%w: %v", sessionfiles.ErrHelperUpgradeRequired, err)
		case sessionfiles.ErrArtifactTooLarge.Error():
			return nil, sessionfiles.ErrArtifactTooLarge
		case "session_file_failed":
			// The previous worker reports oversize through a fixed stderr message.
			legacy := fmt.Sprintf("target-uid file worker: exit status 125: file exceeds %d bytes", sessionfiles.MaxArtifactBytes)
			if action == "read" && strings.TrimSpace(resp.Error) == legacy {
				return nil, sessionfiles.ErrArtifactTooLarge
			}
		}
		return nil, err
	}
	defer files[0].Close()
	data, err = io.ReadAll(io.LimitReader(files[0], sessionfiles.MaxArtifactBytes+1))
	if len(data) > sessionfiles.MaxArtifactBytes {
		return nil, sessionfiles.ErrArtifactTooLarge
	}
	return data, err
}

func (s *helperServer) sessionFile(req HelperRequest, files []*os.File) (*os.File, error) {
	if err := sessionfiles.Validate(req.FileAction, req.FileName); err != nil {
		return nil, err
	}
	want := 0
	if req.FileAction == "put" {
		want = 1
	}
	if len(files) != want {
		return nil, fmt.Errorf("invalid session file fd count")
	}
	if !executionPattern.MatchString(req.ExecutionID) {
		return nil, fmt.Errorf("invalid workspace id")
	}
	if err := s.validateMapping(req.Mapping); err != nil {
		return nil, err
	}
	paths := s.pathsFor(*req.Mapping, req.ExecutionID)
	if err := rejectSymlinkPath(paths.Root, paths.Workspace); err != nil {
		return nil, err
	}
	out, err := os.CreateTemp("", "amux-outbox-*")
	if err != nil {
		return nil, err
	}
	defer out.Close()
	defer os.Remove(out.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The running helper supplies the file protocol, including its worker. A
	// separately installed profile materializer can predate that protocol.
	worker := s.cfg.ExecWrapperPath
	if worker == "" {
		worker, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, worker, "_session-file", paths.Workspace, req.FileAction, req.FileName)
	if want == 1 {
		cmd.Stdin = files[0]
	}
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Env = []string{"HOME=" + paths.Home, "PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if !s.cfg.AllowUnprivileged {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(req.Mapping.UID), Gid: uint32(req.Mapping.GID), NoSetGroups: true}
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == sessionfiles.ArtifactSizeExitCode {
			return nil, sessionfiles.ErrArtifactTooLarge
		}
		return nil, fmt.Errorf("target-uid file worker: %w: %.4096s", err, stderr.String())
	}
	// Return a read-only descriptor. The privileged helper never opens an
	// agent-selected file: the worker has already dropped to the principal UID.
	return os.Open(out.Name())
}
