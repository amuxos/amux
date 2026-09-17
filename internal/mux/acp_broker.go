package mux

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/acp"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/isolation"
)

const (
	acpFrameInput          byte = 0x00
	acpFrameInterrupt      byte = 0x02
	acpFramePermissionResp byte = 0x03
	acpFrameShutdown       byte = 0x04
	acpFrameUpgrade        byte = 0x05
	acpFrameTurnDispatch   byte = 0x06
	acpFrameTurnRebind     byte = 0x07
	acpFrameEmission       byte = 0x14
	acpFrameCapabilities   byte = 0x15
	acpFrameAck            byte = 0x20
)

type acpPermissionResponseFrame struct {
	PermissionId string   `json:"permission_id"`
	OptionId     string   `json:"option_id"`
	OptionIds    []string `json:"option_ids,omitempty"`
}

type acpBroker struct {
	proc           *acp.Proc
	agentSessionID string
	brokerTurns

	conns    []*acpBrokerConn
	stopping bool // guarded by mu; prevents requests.Add racing shutdown's Wait
	requests sync.WaitGroup
}

type acpBrokerConn = brokerConn

type ACPLaunchOptions struct {
	Cmd             *exec.Cmd
	Cwd             string
	ResumeSessionID string
	PromptTailDrain time.Duration
	LaunchContext   isolation.LaunchContext
}

func LaunchACPContext(ctx context.Context, amuxletBin, sessID, amuxDir string, opts ACPLaunchOptions) (pid int, socket string, err error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	if opts.Cmd == nil {
		return 0, "", fmt.Errorf("acp command is required")
	}
	sessDir := filepath.Join(amuxDir, "sessions")
	if err = os.MkdirAll(sessDir, 0700); err != nil {
		return 0, "", fmt.Errorf("mkdir sessions: %w", err)
	}

	sockPath := filepath.Join(sessDir, sessID+".acp.sock")
	pidFile := filepath.Join(sessDir, sessID+".pid")
	os.Remove(sockPath)
	os.Remove(pidFile)

	resumeSessionIDFile, err := writeBrokerResumeSessionIDFile(sessDir, sessID, opts.ResumeSessionID)
	if err != nil {
		return 0, "", err
	}
	if resumeSessionIDFile != "" {
		defer os.Remove(resumeSessionIDFile)
	}
	launchContextFile, err := writeBrokerLaunchContextFile(sessDir, sessID, opts.LaunchContext)
	if err != nil {
		return 0, "", err
	}
	defer os.Remove(launchContextFile)
	args := buildACPBrokerArgs(sockPath, pidFile, opts.Cwd, opts.Cmd, resumeSessionIDFile, launchContextFile, opts.PromptTailDrain)

	logPath := filepath.Join(sessDir, sessID+".acp.log")
	logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)

	cmd := exec.Command(amuxletBin, args...)
	configureBrokerProcess(cmd)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err = cmd.Start(); err != nil {
		if logErr == nil {
			logFile.Close()
		}
		return 0, "", fmt.Errorf("start acp broker: %w", err)
	}
	if logErr == nil {
		logFile.Close()
	}

	// Reaping the broker also tells the wait below that it will never publish a
	// pid, which turns "timed out" into "exited, and here is the log".
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	agentPID, err := waitPIDFileContext(ctx, pidFile, 300*time.Second, exited)
	if err != nil {
		if !errors.Is(err, errBrokerExited) {
			_ = killBrokerProcess(cmd)
		}
		if logData, readErr := os.ReadFile(logPath); readErr == nil && len(logData) > 0 {
			return 0, "", fmt.Errorf("acp broker did not start: %w\nbroker log:\n%s", err, logData)
		}
		return 0, "", fmt.Errorf("acp broker did not start: %w", err)
	}
	return agentPID, sockPath, nil
}

func writeBrokerResumeSessionIDFile(sessDir, sessID, resumeSessionID string) (string, error) {
	resumeSessionID = strings.TrimSpace(resumeSessionID)
	if resumeSessionID == "" {
		return "", nil
	}
	path := filepath.Join(sessDir, sessID+".resume-session-id")
	if err := os.WriteFile(path, []byte(resumeSessionID), 0600); err != nil {
		return "", fmt.Errorf("write broker resume session id file: %w", err)
	}
	return path, nil
}

func buildACPBrokerArgs(sockPath, pidFile, cwd string, agentCmd *exec.Cmd, resumeSessionIDFile, launchContextFile string, promptTailDrain time.Duration) []string {
	args := []string{"_acp_broker", "--socket", sockPath, "--pid-file", pidFile, "--cwd", cwd}
	if resumeSessionIDFile != "" {
		args = append(args, "--resume-session-id-file", resumeSessionIDFile)
	}
	if launchContextFile != "" {
		args = append(args, "--launch-context-file", launchContextFile)
	}
	if promptTailDrain > 0 {
		args = append(args, "--prompt-tail-drain", promptTailDrain.String())
	}
	for _, e := range agentCmd.Env {
		args = append(args, "--env", e)
	}
	args = append(args, "--")
	args = append(args, agentCmd.Path)
	args = append(args, agentCmd.Args[1:]...)
	return args
}

type ACPBrokerOptions struct {
	Cwd             string
	Cmd             *exec.Cmd
	ResumeSessionID string
	PromptTailDrain time.Duration
	Backend         isolation.LaunchBackend
	LaunchContext   isolation.LaunchContext
}

func RunACPBrokerWithOptions(socketPath, pidFile string, opts ACPBrokerOptions) error {
	if opts.Cmd == nil {
		return fmt.Errorf("acp command is required")
	}
	proc, outCh, err := acp.StartWithOptions(context.Background(), opts.Cmd, acp.StartOptions{
		Cwd:             opts.Cwd,
		ResumeSessionID: opts.ResumeSessionID,
		PromptTailDrain: opts.PromptTailDrain,
		Backend:         opts.Backend,
		LaunchContext:   opts.LaunchContext,
	})
	if err != nil {
		return fmt.Errorf("acp start: %w", err)
	}
	defer proc.Close()

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	defer os.Remove(socketPath)
	defer lis.Close()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", proc.PID())), 0600); err != nil {
		return fmt.Errorf("write pidfile: %w", err)
	}

	b := &acpBroker{proc: proc, agentSessionID: proc.SessionID()}
	proc.SetEventSink(func(ev agentstream.ProducerEmission) bool {
		b.broadcastEvent(ev)
		return false
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Assistant text is forwarded through correlated emissions together with
		// the remaining semantic events. Drain the provider byte stream only so text
		// cannot race ahead of or behind result/turn_done on another goroutine.
		for range outCh {
		}
	}()

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			bc := b.addConn(conn)
			if bc != nil {
				go b.handleConn(bc)
			}
		}
	}()

	<-done
	lis.Close()
	b.shutdown(time.Now().Add(5 * time.Second))
	return nil
}

func ConnectACP(socketPath string, agentPID int) (*ACPBrokerProc, <-chan struct{}, error) {
	return ConnectACPContext(context.Background(), socketPath, agentPID)
}

func ConnectACPContext(ctx context.Context, socketPath string, agentPID int) (*ACPBrokerProc, <-chan struct{}, error) {
	p, err := connectACPProc(ctx, socketPath, agentPID)
	if err != nil {
		return nil, nil, err
	}
	if err := p.negotiateTurnCorrelation(ctx); err != nil {
		_ = p.conn.Close()
		return nil, nil, err
	}
	return p, p.done, nil
}

func connectACPProc(ctx context.Context, socketPath string, agentPID int) (*ACPBrokerProc, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial acp broker %s: %w", socketPath, err)
	}
	p := &ACPBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          acpTurnWire,
			pid:               agentPID,
			conn:              conn,
			socketPath:        socketPath,
			ackCh:             make(chan error, 1),
			done:              make(chan struct{}),
			capabilitiesReady: make(chan struct{}),
		},
	}
	go p.readLoop()
	return p, nil
}

func (b *acpBroker) addConn(c net.Conn) *acpBrokerConn {
	bc := &acpBrokerConn{conn: c}
	// Serialize publication with provider broadcasts so the capability frame is
	// always the first observable frame without inverting broker/connection
	// lock order.
	b.broadcastMu.Lock()
	defer b.broadcastMu.Unlock()
	b.mu.Lock()
	if b.stopping {
		b.mu.Unlock()
		_ = c.Close()
		return nil
	}
	b.conns = append(b.conns, bc)
	agentSessionID := b.agentSessionID
	b.mu.Unlock()
	bc.mu.Lock()
	payload, _ := json.Marshal(brokerCapabilitiesFrame{
		TurnCorrelationVersion: brokerTurnProtocolV3,
		TurnReplayVersion:      brokerTurnReplayV1,
	})
	_ = writeBrokerFrame(bc.conn, acpFrameCapabilities, payload)
	if agentSessionID != "" {
		payload, _ := marshalBrokerEmissionFrame("", 0, agentstream.ProducerEmission{
			Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", NativeType: "session_metadata"},
			Session: agentstream.SessionMetadataChanged{AgentSessionID: agentSessionID},
		})
		_ = writeBrokerFrame(bc.conn, acpFrameEmission, payload)
	}
	bc.mu.Unlock()
	return bc
}

func (b *acpBroker) setAgentSessionID(id string) {
	b.mu.Lock()
	b.agentSessionID = id
	b.mu.Unlock()
}

func (b *acpBroker) closeConns() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		_ = c.conn.Close()
	}
	b.conns = nil
}

func (b *acpBroker) shutdown(deadline time.Time) {
	b.mu.Lock()
	b.stopping = true
	b.mu.Unlock()
	// Keep the whole drain bounded in addition to each frame's write timeout.
	// Healthy clients retain their connection until pending requests finish.
	forceClose := time.AfterFunc(time.Until(deadline), b.closeConns)
	defer forceClose.Stop()
	// Output EOF can precede process exit. Stop the child to unblock pending
	// calls and prompt-tail draining, after the stdout reader delivered all RPC
	// responses. Their handlers still own emission and ACK delivery below.
	if b.proc != nil {
		_ = b.proc.Close()
	}
	b.requests.Wait()
	b.closeConns()
}

func (b *acpBroker) broadcastEvent(ev agentstream.ProducerEmission) {
	if metadata, ok := ev.Session.(agentstream.SessionMetadataChanged); ok && metadata.AgentSessionID != "" {
		b.setAgentSessionID(metadata.AgentSessionID)
	}
	b.broadcastEventForTurn("", ev)
}

func (b *acpBroker) broadcastCorrelatedEvent(turnToken string, ev agentstream.ProducerEmission) {
	if metadata, ok := ev.Session.(agentstream.SessionMetadataChanged); ok && metadata.AgentSessionID != "" {
		b.setAgentSessionID(metadata.AgentSessionID)
	}
	b.broadcastEventForTurn(turnToken, ev)
}

func (b *acpBroker) broadcastEventForTurn(turnToken string, ev agentstream.ProducerEmission) {
	// Provider callbacks may originate on more than one goroutine (metadata,
	// process exit, permission resolution). Serialize the event stream without
	// holding the lifecycle/state mutex across socket I/O.
	b.broadcastMu.Lock()
	defer b.broadcastMu.Unlock()
	if turnToken != "" {
		b.broadcastCorrelatedEventLocked(turnToken, ev)
		return
	}

	b.mu.Lock()
	b.turnAdmission.finishEmission(turnToken, ev.IsTerminal())
	targets := make([]*acpBrokerConn, 0, len(b.conns))
	for _, conn := range b.conns {
		targets = append(targets, conn)
	}
	b.mu.Unlock()

	failed := make(map[*acpBrokerConn]struct{})
	for _, target := range targets {
		payload, _ := marshalBrokerEmissionFrame("", 0, ev)
		err := target.write(acpFrameEmission, payload)
		if err != nil {
			failed[target] = struct{}{}
			_ = target.conn.Close()
		}
	}
	if len(failed) == 0 {
		return
	}
	b.mu.Lock()
	alive := b.conns[:0]
	for _, conn := range b.conns {
		if _, lost := failed[conn]; !lost {
			alive = append(alive, conn)
		}
	}
	b.conns = alive
	b.mu.Unlock()
}

func (b *acpBroker) broadcastCorrelatedEventLocked(turnToken string, ev agentstream.ProducerEmission) {
	b.brokerTurns.broadcastCorrelatedEventLocked(turnToken, ev, acpFrameEmission, b.removeConn)
}

func (b *acpBroker) handleConn(conn *acpBrokerConn) {
	defer conn.conn.Close()
	defer b.removeConn(conn)
	for {
		kind, payload, err := readACPFrame(conn.conn)
		if err != nil {
			return
		}
		if !b.handleFrame(conn, kind, payload) {
			return
		}
	}
}

func (b *acpBroker) handleFrame(conn *acpBrokerConn, kind byte, payload []byte) bool {
	b.mu.Lock()
	if b.stopping {
		b.mu.Unlock()
		return false
	}
	b.requests.Add(1)
	b.mu.Unlock()
	defer b.requests.Done()

	switch kind {
	case acpFrameUpgrade:
		var upgrade brokerUpgradeFrame
		errText := ""
		if json.Unmarshal(payload, &upgrade) != nil || upgrade.Version != brokerTurnProtocolV3 {
			errText = "unsupported ACP broker protocol"
		} else {
			b.mu.Lock()
			conn.protocolV3 = true
			b.mu.Unlock()
		}
		_ = conn.write(acpFrameAck, []byte(errText))
	case acpFrameTurnDispatch:
		errText := b.dispatchCorrelatedTurn(conn, payload)
		_ = conn.write(acpFrameAck, []byte(errText))
	case acpFrameTurnRebind:
		b.rebindCorrelatedTurn(conn, payload)
	case acpFrameInput:
		_ = conn.write(acpFrameAck, []byte("correlated turn dispatch is required"))
	case acpFrameInterrupt:
		err := ""
		if interruptErr := b.proc.Interrupt(); interruptErr != nil {
			err = interruptErr.Error()
		}
		_ = conn.write(acpFrameAck, []byte(err))
	case acpFrameShutdown:
		err := ""
		if closeErr := b.proc.Close(); closeErr != nil {
			err = closeErr.Error()
		}
		_ = conn.write(acpFrameAck, []byte(err))
		return false
	case acpFramePermissionResp:
		err := ""
		var resp acpPermissionResponseFrame
		if unmarshalErr := json.Unmarshal(payload, &resp); unmarshalErr != nil {
			err = unmarshalErr.Error()
		} else if len(resp.OptionIds) > 0 {
			if respondErr := b.proc.RespondPermissionMulti(resp.PermissionId, resp.OptionIds); respondErr != nil {
				err = respondErr.Error()
			}
		} else if respondErr := b.proc.RespondPermission(resp.PermissionId, resp.OptionId); respondErr != nil {
			err = respondErr.Error()
		}
		_ = conn.write(acpFrameAck, []byte(err))
	}
	return true
}

func (b *acpBroker) dispatchCorrelatedTurn(conn *acpBrokerConn, payload []byte) string {
	return b.brokerTurns.dispatchCorrelatedTurn(conn, payload, b.proc.DispatchTurn, b.broadcastCorrelatedEvent)
}

func (b *acpBroker) rebindCorrelatedTurn(conn *acpBrokerConn, payload []byte) {
	b.brokerTurns.rebindCorrelatedTurn(conn, payload, acpFrameAck, acpFrameEmission)
}

func ShutdownACPBroker(socketPath string) error {
	return sendBrokerControl(socketPath, acpFrameShutdown)
}

func sendBrokerControl(socketPath string, kind byte) error {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeACPFrame(conn, kind, nil); err != nil {
		return err
	}
	for {
		frameKind, payload, err := readACPFrame(conn)
		if err != nil {
			return err
		}
		if frameKind != acpFrameAck {
			continue
		}
		if len(payload) != 0 {
			return fmt.Errorf("%s", payload)
		}
		return nil
	}
}

func (b *acpBroker) removeConn(conn *acpBrokerConn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turnAdmission.disconnect(conn)
	for i, c := range b.conns {
		if c == conn {
			b.conns = append(b.conns[:i], b.conns[i+1:]...)
			return
		}
	}
}

func writeACPFrame(w io.Writer, kind byte, payload []byte) error {
	frame := make([]byte, 5+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	copy(frame[5:], payload)
	_, err := w.Write(frame)
	return err
}

func readACPFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return hdr[0], payload, nil
}
