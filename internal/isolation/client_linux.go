//go:build linux

package isolation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/cleanup"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type HelperClient struct {
	SocketPath string
}

func NewHelperClient(socketPath string) *HelperClient {
	return &HelperClient{SocketPath: socketPath}
}

func (c *HelperClient) request(ctx context.Context, req HelperRequest, wantFDs int) (HelperResponse, []*os.File, error) {
	return c.requestWithFiles(ctx, req, nil, wantFDs)
}

func (c *HelperClient) requestWithFiles(ctx context.Context, req HelperRequest, sendFiles []*os.File, wantFDs int) (HelperResponse, []*os.File, error) {
	if err := ctx.Err(); err != nil {
		return HelperResponse{}, nil, err
	}
	req.ProtocolVersion = HelperProtocolVersion
	if strings.TrimSpace(req.RequestID) == "" {
		req.RequestID = uuid.NewString()
	}
	frame, err := encodeHelperFrame(req)
	if err != nil {
		return HelperResponse{}, nil, err
	}
	addr := &net.UnixAddr{Name: c.SocketPath, Net: "unixpacket"}
	conn, err := net.DialUnix("unixpacket", nil, addr)
	if err != nil {
		return HelperResponse{}, nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Wait is an open-ended request, so cancellation has to reach the socket
	// itself; otherwise a caller that cancels a linux-dac Wait blocks forever
	// while the same call on the passthrough backend returns immediately.
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
		defer stop()
	}
	var sendOOB []byte
	if len(sendFiles) > 0 {
		fds := make([]int, 0, len(sendFiles))
		for _, file := range sendFiles {
			if file == nil {
				return HelperResponse{}, nil, fmt.Errorf("nil helper request file")
			}
			fds = append(fds, int(file.Fd()))
		}
		sendOOB = unix.UnixRights(fds...)
	}
	if _, _, err := conn.WriteMsgUnix(frame, sendOOB, nil); err != nil {
		return HelperResponse{}, nil, contextRequestError(ctx, err)
	}
	payload := make([]byte, MaxHelperFrameBytes+4)
	oob := make([]byte, unix.CmsgSpace(4*max(wantFDs, 1)))
	n, oobn, flags, _, err := conn.ReadMsgUnix(payload, oob)
	if err != nil {
		return HelperResponse{}, nil, contextRequestError(ctx, err)
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return HelperResponse{}, nil, fmt.Errorf("truncated helper response")
	}
	var resp HelperResponse
	if err := decodeHelperFrame(payload[:n], &resp); err != nil {
		return HelperResponse{}, nil, err
	}
	if resp.ProtocolVersion != HelperProtocolVersion {
		return HelperResponse{}, nil, fmt.Errorf("helper protocol mismatch: got %d want %d", resp.ProtocolVersion, HelperProtocolVersion)
	}
	if resp.RequestID != req.RequestID {
		return HelperResponse{}, nil, fmt.Errorf("helper request id mismatch")
	}
	if !resp.OK {
		return resp, nil, helperResponseError(resp)
	}
	files, err := receivedFiles(oob[:oobn])
	if err != nil {
		return HelperResponse{}, nil, err
	}
	if wantFDs > 0 && len(files) != wantFDs {
		for _, file := range files {
			_ = file.Close()
		}
		return HelperResponse{}, nil, fmt.Errorf("helper returned %d file descriptors, want %d", len(files), wantFDs)
	}
	return resp, files, nil
}

// contextRequestError reports cancellation as the caller's own context error so
// a cancelled Wait is distinguishable from a helper transport failure.
func contextRequestError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func helperResponseError(resp HelperResponse) error {
	switch resp.ErrorCode {
	case "not_found":
		return fmt.Errorf("%w: %s", ErrNotFound, resp.Error)
	case "isolation_lost":
		return fmt.Errorf("%w: %s", ErrIsolationLost, resp.Error)
	default:
		if resp.Error == "" {
			return fmt.Errorf("helper request failed (%s)", resp.ErrorCode)
		}
		return fmt.Errorf("helper request failed (%s): %s", resp.ErrorCode, resp.Error)
	}
}

func receivedFiles(oob []byte) ([]*os.File, error) {
	if len(oob) == 0 {
		return nil, nil
	}
	messages, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	var files []*os.File
	for _, message := range messages {
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			for _, file := range files {
				_ = file.Close()
			}
			return nil, err
		}
		for _, fd := range fds {
			unix.CloseOnExec(fd)
			files = append(files, os.NewFile(uintptr(fd), "amux-isolation-fd"))
		}
	}
	return files, nil
}

func (c *HelperClient) Handshake(ctx context.Context) (HelperResponse, error) {
	resp, _, err := c.request(ctx, HelperRequest{Operation: OpHandshake}, 0)
	return resp, err
}

func (c *HelperClient) EnsurePrincipal(ctx context.Context, mapping AccountMapping) (PrincipalPaths, error) {
	resp, _, err := c.request(ctx, HelperRequest{Operation: OpEnsurePrincipal, Mapping: &mapping}, 0)
	if err != nil {
		return PrincipalPaths{}, err
	}
	if resp.Paths == nil {
		return PrincipalPaths{}, fmt.Errorf("helper omitted principal paths")
	}
	return *resp.Paths, nil
}

func (c *HelperClient) PrepareRun(ctx context.Context, mapping AccountMapping, workspaceID, launchID string) (PrincipalPaths, error) {
	resp, _, err := c.request(ctx, HelperRequest{Operation: OpPrepareRun, Mapping: &mapping, ExecutionID: workspaceID, LaunchID: launchID}, 0)
	if err != nil {
		return PrincipalPaths{}, err
	}
	if resp.Paths == nil {
		return PrincipalPaths{}, fmt.Errorf("helper omitted run paths")
	}
	return *resp.Paths, nil
}

func (c *HelperClient) MaterializeRun(
	ctx context.Context,
	mapping AccountMapping,
	workspaceID,
	launchID string,
	sources []BundleSource,
) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	writeDone := make(chan error, 1)
	go func() {
		err := WriteMaterializeBundle(writer, sources)
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		writeDone <- err
	}()
	_, _, requestErr := c.requestWithFiles(ctx, HelperRequest{
		Operation:   OpMaterializeRun,
		Mapping:     &mapping,
		ExecutionID: workspaceID,
		LaunchID:    launchID,
	}, []*os.File{reader}, 0)
	_ = reader.Close()
	if requestErr != nil {
		_ = writer.Close()
	}
	writeErr := <-writeDone
	if requestErr != nil {
		return requestErr
	}
	return writeErr
}

func (c *HelperClient) Start(ctx context.Context, mapping AccountMapping, spec LaunchSpec) (ProcessHandle, error) {
	resp, files, err := c.request(ctx, HelperRequest{
		Operation: OpStart,
		LaunchID:  spec.Context.LaunchID,
		Mapping:   &mapping,
		Spec:      &spec,
	}, 3)
	if err != nil {
		return nil, err
	}
	if resp.Description == nil {
		for _, file := range files {
			_ = file.Close()
		}
		return nil, fmt.Errorf("helper omitted launch description")
	}
	return &helperProcessHandle{
		client: c,
		desc:   *resp.Description,
		stdin:  files[0],
		stdout: files[1],
		stderr: files[2],
	}, nil
}

func (c *HelperClient) Describe(ctx context.Context, launchID string) (LaunchDescription, error) {
	resp, _, err := c.request(ctx, HelperRequest{Operation: OpDescribe, LaunchID: launchID}, 0)
	if err != nil {
		return LaunchDescription{}, err
	}
	if resp.Description == nil {
		return LaunchDescription{}, fmt.Errorf("helper omitted launch description")
	}
	return *resp.Description, nil
}

func (c *HelperClient) Wait(ctx context.Context, launchID string) (ExitStatus, error) {
	resp, _, err := c.request(ctx, HelperRequest{Operation: OpWait, LaunchID: launchID}, 0)
	if err != nil {
		return ExitStatus{}, err
	}
	if resp.Description == nil || resp.Description.ExitStatus == nil {
		return ExitStatus{}, fmt.Errorf("helper omitted exit status")
	}
	return *resp.Description.ExitStatus, nil
}

func (c *HelperClient) Signal(ctx context.Context, launchID, signal string) error {
	_, _, err := c.request(ctx, HelperRequest{Operation: OpSignal, LaunchID: launchID, Signal: signal}, 0)
	return err
}

func (c *HelperClient) Kill(ctx context.Context, launchID string) error {
	_, _, err := c.request(ctx, HelperRequest{Operation: OpKill, LaunchID: launchID}, 0)
	return err
}

func (c *HelperClient) CloseHandle(ctx context.Context, launchID string) error {
	_, _, err := c.request(ctx, HelperRequest{Operation: OpCloseHandle, LaunchID: launchID}, 0)
	return err
}

func (c *HelperClient) RemoveRun(ctx context.Context, mapping AccountMapping, executionID string) error {
	_, _, err := c.request(ctx, HelperRequest{Operation: OpRemoveRun, Mapping: &mapping, ExecutionID: executionID}, 0)
	return err
}

// RemoveWorkspace reclaims a finished execution's workspace. The tree is owned
// by the principal at 0700, so the service user cannot delete it itself.
func (c *HelperClient) RemoveWorkspace(ctx context.Context, mapping AccountMapping, executionID string) error {
	_, _, err := c.request(ctx, HelperRequest{Operation: OpRemoveWorkspace, Mapping: &mapping, ExecutionID: executionID}, 0)
	return err
}

// ReclaimHome asks the helper to drop oversized provider caches from the
// principal HOME.
func (c *HelperClient) ReclaimHome(ctx context.Context, mapping AccountMapping, caches []HomeCacheSpec) error {
	if len(caches) == 0 {
		return nil
	}
	_, _, err := c.request(ctx, HelperRequest{Operation: OpReclaimHome, Mapping: &mapping, HomeCaches: caches}, 0)
	return err
}

func (c *HelperClient) KillPrincipal(ctx context.Context, mapping AccountMapping) error {
	_, _, err := c.request(ctx, HelperRequest{Operation: OpKillPrincipal, Mapping: &mapping}, 0)
	return err
}

func (c *HelperClient) Drain(ctx context.Context, force bool) error {
	_, err := c.DrainStatus(ctx, force)
	return err
}

func (c *HelperClient) DrainStatus(ctx context.Context, force bool) (int, error) {
	resp, _, err := c.request(ctx, HelperRequest{Operation: OpDrain, Force: force}, 0)
	return resp.ActiveLaunches, err
}

// AcquireUpgradeGuard atomically checks for idle launches and prevents new
// launches while the writer of lease remains open. The caller owns the pipe.
// -1 means that the running helper predates the guard protocol.
func (c *HelperClient) AcquireUpgradeGuard(ctx context.Context, lease *os.File) (int, error) {
	resp, _, err := c.requestWithFiles(ctx, HelperRequest{Operation: OpUpgradeGuard}, []*os.File{lease}, 0)
	if resp.ErrorCode == "unsupported_operation" {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	if resp.ActiveLaunches == 0 && !resp.Draining {
		return 0, fmt.Errorf("helper did not acquire upgrade guard")
	}
	return resp.ActiveLaunches, nil
}

func (c *HelperClient) ListAccounts(ctx context.Context) ([]AccountMapping, error) {
	resp, _, err := c.request(ctx, HelperRequest{Operation: OpListAccounts}, 0)
	return resp.Accounts, err
}

func (c *HelperClient) RemoveAccount(ctx context.Context, mapping AccountMapping) error {
	_, _, err := c.request(ctx, HelperRequest{Operation: OpRemoveAccount, Mapping: &mapping}, 0)
	return err
}

type helperProcessHandle struct {
	client *HelperClient
	desc   LaunchDescription
	stdin  *os.File
	stdout *os.File
	stderr *os.File

	closeOnce sync.Once
	closeErr  error
}

func (h *helperProcessHandle) LaunchID() string      { return h.desc.LaunchID }
func (h *helperProcessHandle) PID() int              { return h.desc.PID }
func (h *helperProcessHandle) Stdin() io.WriteCloser { return h.stdin }
func (h *helperProcessHandle) Stdout() io.ReadCloser { return h.stdout }
func (h *helperProcessHandle) Stderr() io.ReadCloser { return h.stderr }
func (h *helperProcessHandle) Wait(ctx context.Context) (ExitStatus, error) {
	return h.client.Wait(ctx, h.desc.LaunchID)
}
func (h *helperProcessHandle) Signal(ctx context.Context, signal string) error {
	return h.client.Signal(ctx, h.desc.LaunchID, signal)
}
func (h *helperProcessHandle) Kill(ctx context.Context) error {
	return h.client.Kill(ctx, h.desc.LaunchID)
}
func (h *helperProcessHandle) Close() error {
	h.closeOnce.Do(func() {
		for _, closer := range []io.Closer{h.stdin, h.stdout, h.stderr} {
			if closer != nil {
				if err := closer.Close(); err != nil && h.closeErr == nil {
					h.closeErr = err
				}
			}
		}
		if err := h.client.CloseHandle(context.Background(), h.desc.LaunchID); err != nil && h.closeErr == nil {
			h.closeErr = err
		}
	})
	return h.closeErr
}

func (c *HelperClient) DailyCleanup(ctx context.Context, mapping AccountMapping, req DailyCleanupRequest) ([]cleanup.Step, error) {
	wire := HelperRequest{Operation: OpDailyCleanup, Mapping: &mapping, Cleanup: &req}
	data, err := json.Marshal(req.References)
	if err != nil {
		return nil, err
	}
	if len(data) <= maxInlineCleanupReferencesBytes {
		resp, _, err := c.request(ctx, wire, 0)
		return resp.CleanupSteps, err
	}
	file, err := sealCleanupReferences(data)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	req.References = nil
	resp, _, err := c.requestWithFiles(ctx, wire, []*os.File{file}, 0)
	return resp.CleanupSteps, err
}
