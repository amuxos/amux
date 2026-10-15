package mux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/distribution"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/provider/claude"
	"github.com/amuxos/amux/internal/provider/codex"
	"github.com/amuxos/amux/internal/provider/pi"
	"github.com/amuxos/amux/internal/session"
)

const (
	structuredFrameInput          byte = 0x00
	structuredFrameInterrupt      byte = 0x02
	structuredFramePermissionResp byte = 0x03
	structuredFrameControl        byte = 0x04
	structuredFrameShutdown       byte = 0x05
	structuredFrameUserInput      byte = 0x06
	structuredFrameReconfigure    byte = 0x07
	structuredFrameUpgrade        byte = 0x08
	structuredFrameTurnDispatch   byte = 0x09
	structuredFrameTurnRebind     byte = 0x0A
	structuredFrameGoal           byte = 0x0B
	structuredFrameGoalAdmission  byte = 0x0C
	structuredFrameGoalReady      byte = 0x0D
	structuredFrameTurnSteer      byte = 0x0E
	structuredFrameCapabilities   byte = 0x14
	structuredFrameEmission       byte = 0x15
	structuredFrameGoalOffer      byte = 0x16
	structuredFrameGoalResult     byte = 0x17
	structuredFrameGoalRejected   byte = 0x18
	structuredFrameAck            byte = 0x20
)

type structuredBroker struct {
	proc                  session.Proc
	agentSessionID        string
	effectiveModel        string
	inputModalities       []string
	inputCapabilitySource string
	goalSupported         bool
	goalSnapshot          *agentstream.GoalChanged
	goalOffer             *brokerGoalOffer
	brokerTurns

	conns []*structuredBrokerConn
}

type structuredBrokerConn = brokerConn

type StructuredLaunchOptions struct {
	Provider               string
	SessionID              string
	ResumeSessionID        string
	ResumeModel            string
	ResumeReasoningEffort  string
	Cwd                    string
	Env                    []string
	ExtraArgs              []string
	ExcludedSkillRoots     []string
	TurnIdleTimeoutSeconds int
	TurnHardCapSeconds     int
	ToolIdleTimeoutSeconds int
	RepeatToolCallLimit    int
	Backend                isolation.LaunchBackend
	LaunchContext          isolation.LaunchContext
	ResponsesChat          *ResponsesChatOptions
}

type structuredReconfigureFrame struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

type ResponsesChatOptions struct {
	UpstreamBaseURL string
	APIKeyEnv       string
}

type structuredPermissionResponseFrame struct {
	PermissionId string `json:"permission_id"`
	OptionId     string `json:"option_id"`
	// OptionIds carries multiple selected options for multiSelect user-input
	// permissions (item/tool/requestUserInput). Non-empty => multi-select path.
	OptionIds []string `json:"option_ids,omitempty"`
	Cancel    bool     `json:"cancel,omitempty"`
}

func LaunchStructuredContext(ctx context.Context, amuxletBin, sessID, amuxDir string, opts StructuredLaunchOptions) (pid int, socket string, err error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	if opts.ResponsesChat != nil {
		if err := distribution.RequireResponsesChat(); err != nil {
			return 0, "", err
		}
	}
	sessDir := filepath.Join(amuxDir, "sessions")
	if err = os.MkdirAll(sessDir, 0700); err != nil {
		return 0, "", fmt.Errorf("mkdir sessions: %w", err)
	}
	sockPath := filepath.Join(sessDir, sessID+".structured.sock")
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
	credentialFile, err := writeResponsesChatCredentialFile(sessDir, sessID, &opts)
	if err != nil {
		return 0, "", err
	}
	if credentialFile != "" {
		defer os.Remove(credentialFile)
	}
	args := buildStructuredBrokerArgs(sockPath, pidFile, opts, resumeSessionIDFile, launchContextFile, credentialFile)

	logPath := filepath.Join(sessDir, sessID+".structured.log")
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
		return 0, "", fmt.Errorf("start structured broker: %w", err)
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
			return 0, "", fmt.Errorf("structured broker did not start: %w\nbroker log:\n%s", err, logData)
		}
		return 0, "", fmt.Errorf("structured broker did not start: %w", err)
	}
	return agentPID, sockPath, nil
}

func buildStructuredBrokerArgs(sockPath, pidFile string, opts StructuredLaunchOptions, resumeSessionIDFile, launchContextFile, credentialFile string) []string {
	args := []string{"_structured_broker", "--provider", opts.Provider, "--session-id", opts.SessionID, "--socket", sockPath, "--pid-file", pidFile, "--cwd", opts.Cwd}
	if resumeSessionIDFile != "" {
		args = append(args, "--resume-session-id-file", resumeSessionIDFile)
	}
	if opts.ResumeModel != "" {
		args = append(args, "--resume-model", opts.ResumeModel)
	}
	if opts.ResumeReasoningEffort != "" {
		args = append(args, "--resume-reasoning-effort", opts.ResumeReasoningEffort)
	}
	if launchContextFile != "" {
		args = append(args, "--launch-context-file", launchContextFile)
	}
	if opts.TurnIdleTimeoutSeconds >= 0 {
		args = append(args, "--turn-idle-timeout-seconds", strconv.Itoa(opts.TurnIdleTimeoutSeconds))
	}
	if opts.TurnHardCapSeconds >= 0 {
		args = append(args, "--turn-hard-cap-seconds", strconv.Itoa(opts.TurnHardCapSeconds))
	}
	if opts.ToolIdleTimeoutSeconds >= 0 {
		args = append(args, "--tool-idle-timeout-seconds", strconv.Itoa(opts.ToolIdleTimeoutSeconds))
	}
	if opts.RepeatToolCallLimit >= 0 {
		args = append(args, "--repeat-tool-call-limit", strconv.Itoa(opts.RepeatToolCallLimit))
	}
	if opts.ResponsesChat != nil {
		args = append(args,
			"--responses-chat-upstream-base-url", opts.ResponsesChat.UpstreamBaseURL,
			"--responses-chat-api-key-env", opts.ResponsesChat.APIKeyEnv,
		)
		if credentialFile != "" {
			args = append(args, "--responses-chat-credential-file", credentialFile)
		}
	}
	for _, e := range opts.Env {
		args = append(args, "--env", e)
	}
	for _, root := range opts.ExcludedSkillRoots {
		args = append(args, "--excluded-skill-root", root)
	}
	args = append(args, "--")
	args = append(args, opts.ExtraArgs...)
	return args
}

func writeResponsesChatCredentialFile(sessDir, sessID string, opts *StructuredLaunchOptions) (string, error) {
	if opts == nil || opts.ResponsesChat == nil {
		return "", nil
	}
	key := strings.TrimSpace(opts.ResponsesChat.APIKeyEnv)
	prefix := key + "="
	value := ""
	filtered := make([]string, 0, len(opts.Env))
	for _, entry := range opts.Env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
			continue
		}
		filtered = append(filtered, entry)
	}
	if key == "" || value == "" {
		return "", fmt.Errorf("responses-chat credential environment %s is not set", key)
	}
	opts.Env = filtered
	path := filepath.Join(sessDir, sessID+".responses-chat-credential")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("write responses-chat credential file: %w", err)
	}
	if _, err := file.WriteString(value); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write responses-chat credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close responses-chat credential file: %w", err)
	}
	return path, nil
}

func RunStructuredBroker(socketPath, pidFile string, opts StructuredLaunchOptions) error {
	if prepareStructuredLaunch != nil {
		cleanup, err := prepareStructuredLaunch(&opts)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}
	}
	proc, outCh, err := startStructuredProc(opts)
	if err != nil {
		return err
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
	b := &structuredBroker{proc: proc}
	if goals, ok := proc.(session.GoalController); ok {
		_, native := proc.(session.NativeGoalTurnProvider)
		b.goalSupported = native && goals.GoalSupported()
	}
	if native, ok := proc.(session.NativeGoalTurnProvider); ok {
		native.SetGoalTurnAdmitter(b.admitGoalTurn)
	}
	if caps, ok := proc.(session.InputCapabilityProvider); ok {
		b.inputModalities, b.inputCapabilitySource = caps.InputCapabilities()
	}
	proc.SetEventSink(func(ev agentstream.ProducerEmission) bool {
		b.broadcastEvent(ev)
		return false
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Assistant deltas use the ordered event sink above. The byte channel is
		// retained only as a Proc lifecycle signal; broadcasting it would both
		// duplicate text and let turn_done overtake buffered output.
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
			go b.handleConn(bc)
		}
	}()
	<-done
	lis.Close()
	b.closeConns()
	return nil
}

func startStructuredProc(opts StructuredLaunchOptions) (session.Proc, <-chan []byte, error) {
	if opts.ResponsesChat != nil {
		if err := distribution.RequireResponsesChat(); err != nil {
			return nil, nil, err
		}
	}
	watchdog := config.StructuredProviderConfig{
		TurnIdleTimeoutSeconds: opts.TurnIdleTimeoutSeconds,
		TurnHardCapSeconds:     opts.TurnHardCapSeconds,
		ToolIdleTimeoutSeconds: opts.ToolIdleTimeoutSeconds,
		RepeatToolCallLimit:    opts.RepeatToolCallLimit,
	}
	switch opts.Provider {
	case "claude-sdk":
		return claude.Start(context.Background(), claude.StartOptions{SessionID: opts.SessionID, ResumeSessionID: opts.ResumeSessionID, Cwd: opts.Cwd, Env: opts.Env, ExtraArgs: opts.ExtraArgs, Backend: opts.Backend, LaunchContext: opts.LaunchContext})
	case "codex-app-server":
		startOpts := codex.StartOptions{Cwd: opts.Cwd, Env: opts.Env, ExtraArgs: opts.ExtraArgs, ResumeSessionID: opts.ResumeSessionID, ResumeModel: opts.ResumeModel, ResumeReasoningEffort: opts.ResumeReasoningEffort, DiscoverSkills: true, ExcludedSkillRoots: opts.ExcludedSkillRoots, Watchdog: watchdog, Backend: opts.Backend, LaunchContext: opts.LaunchContext}
		if opts.ResponsesChat != nil {
			executable, err := responsesChatSupervisorExecutable()
			if err != nil {
				return nil, nil, fmt.Errorf("resolve amuxlet executable for responses-chat supervisor: %w", err)
			}
			startOpts.Executable = executable
			startOpts.ExtraArgs, err = responsesChatCommandArgs(*opts.ResponsesChat, opts.ExtraArgs)
			if err != nil {
				return nil, nil, err
			}
		}
		return codex.Start(context.Background(), startOpts)
	case "pi-rpc":
		return pi.Start(context.Background(), pi.StartOptions{Cwd: opts.Cwd, Env: opts.Env, ExtraArgs: opts.ExtraArgs, ResumeSessionID: opts.ResumeSessionID, Backend: opts.Backend, LaunchContext: opts.LaunchContext})
	default:
		return startDistributionStructuredProc(opts, watchdog)
	}
}

func responsesChatSupervisorExecutable() (string, error) {
	installed := amuxhome.Path("bin", "amuxlet")
	if info, err := os.Stat(installed); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
		return installed, nil
	}
	return os.Executable()
}

func ConnectStructured(socketPath string, agentPID int) (*StructuredBrokerProc, <-chan struct{}, error) {
	return ConnectStructuredContext(context.Background(), socketPath, agentPID)
}

func ConnectStructuredContext(ctx context.Context, socketPath string, agentPID int) (*StructuredBrokerProc, <-chan struct{}, error) {
	p, err := connectStructuredProc(ctx, socketPath, agentPID)
	if err != nil {
		return nil, nil, err
	}
	if err := p.negotiateTurnCorrelation(ctx); err != nil {
		_ = p.conn.Close()
		return nil, nil, err
	}
	return p, p.done, nil
}

func connectStructuredProc(ctx context.Context, socketPath string, agentPID int) (*StructuredBrokerProc, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial structured broker %s: %w", socketPath, err)
	}
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          structuredTurnWire,
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

func (b *structuredBroker) addConn(c net.Conn) *structuredBrokerConn {
	bc := &structuredBrokerConn{conn: c}
	// Serialize publication with provider broadcasts so the capability frame is
	// always the first observable frame without inverting broker/connection
	// lock order.
	b.broadcastMu.Lock()
	defer b.broadcastMu.Unlock()
	b.mu.Lock()
	b.conns = append(b.conns, bc)
	agentSessionID := b.agentSessionID
	effectiveModel := b.effectiveModel
	modalities := append([]string(nil), b.inputModalities...)
	capabilitySource := b.inputCapabilitySource
	goalSupported := b.goalSupported
	goalSnapshot := b.goalSnapshot
	b.mu.Unlock()
	bc.mu.Lock()
	payload, _ := json.Marshal(structuredCapabilitiesFrame{
		InputModalities:        modalities,
		Source:                 capabilitySource,
		TurnCorrelationVersion: brokerTurnProtocolV3,
		TurnReplayVersion:      brokerTurnReplayV1,
		GoalSupported:          goalSupported,
		TurnSteerVersion:       b.turnSteerVersion(),
	})
	_ = writeBrokerFrame(bc.conn, structuredFrameCapabilities, payload)
	if agentSessionID != "" || effectiveModel != "" {
		payload, _ := marshalBrokerEmissionFrame("", 0, agentstream.ProducerEmission{
			Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", NativeType: "session_metadata"},
			Session: agentstream.SessionMetadataChanged{AgentSessionID: agentSessionID, EffectiveModel: effectiveModel},
		})
		_ = writeBrokerFrame(bc.conn, structuredFrameEmission, payload)
	}
	if goalSnapshot != nil {
		payload, _ := marshalBrokerEmissionFrame("", 0, agentstream.ProducerEmission{Session: *goalSnapshot})
		_ = writeBrokerFrame(bc.conn, structuredFrameEmission, payload)
	}
	bc.mu.Unlock()
	return bc
}

func (b *structuredBroker) setSessionMetadata(agentSessionID, effectiveModel string) {
	b.mu.Lock()
	if agentSessionID != "" {
		b.agentSessionID = agentSessionID
	}
	if effectiveModel != "" {
		b.effectiveModel = effectiveModel
	}
	b.mu.Unlock()
}

func (b *structuredBroker) forwardProcEvent(ev agentstream.ProducerEmission) bool {
	b.broadcastEvent(ev)
	return false
}

func (b *structuredBroker) closeConns() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		_ = c.conn.Close()
	}
	b.conns = nil
}

func (b *structuredBroker) broadcastEvent(ev agentstream.ProducerEmission) {
	if goal, ok := ev.Session.(agentstream.GoalChanged); ok {
		b.mu.Lock()
		copy := goal
		copy.Goal = goal.Goal.Clone()
		_, native := b.proc.(session.NativeGoalTurnProvider)
		b.goalSupported = native && goal.Supported
		copy.Supported = b.goalSupported
		b.goalSnapshot = &copy
		ev.Session = copy
		b.mu.Unlock()
	}
	if metadata, ok := ev.Session.(agentstream.SessionMetadataChanged); ok {
		b.setSessionMetadata(metadata.AgentSessionID, metadata.EffectiveModel)
	}
	b.broadcastEventForTurn("", ev)
}

func (b *structuredBroker) broadcastCorrelatedEvent(turnToken string, ev agentstream.ProducerEmission) {
	if metadata, ok := ev.Session.(agentstream.SessionMetadataChanged); ok {
		b.setSessionMetadata(metadata.AgentSessionID, metadata.EffectiveModel)
	}
	b.broadcastEventForTurn(turnToken, ev)
}

func (b *structuredBroker) broadcastEventForTurn(turnToken string, ev agentstream.ProducerEmission) {
	b.broadcastMu.Lock()
	defer b.broadcastMu.Unlock()
	if turnToken != "" {
		b.broadcastCorrelatedEventLocked(turnToken, ev)
		return
	}

	b.mu.Lock()
	b.turnAdmission.finishEmission(turnToken, ev.IsTerminal())
	targets := make([]*structuredBrokerConn, 0, len(b.conns))
	for _, conn := range b.conns {
		targets = append(targets, conn)
	}
	b.mu.Unlock()

	failed := make(map[*structuredBrokerConn]struct{})
	for _, target := range targets {
		payload, _ := marshalBrokerEmissionFrame("", 0, ev)
		err := target.write(structuredFrameEmission, payload)
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

func (b *structuredBroker) broadcastCorrelatedEventLocked(turnToken string, ev agentstream.ProducerEmission) {
	b.brokerTurns.broadcastCorrelatedEventLocked(turnToken, ev, structuredFrameEmission, b.removeConn)
}

func (b *structuredBroker) handleConn(conn *structuredBrokerConn) {
	defer conn.conn.Close()
	defer b.removeConn(conn)
	for {
		kind, payload, err := readACPFrame(conn.conn)
		if err != nil {
			return
		}
		switch kind {
		case structuredFrameGoal:
			b.handleGoalControl(conn, payload)
		case structuredFrameGoalAdmission:
			b.handleGoalAdmission(conn, payload)
		case structuredFrameGoalReady:
			b.mu.Lock()
			upgraded := conn.protocolV3
			b.mu.Unlock()
			if upgraded {
				if ready, ok := b.proc.(session.GoalTurnReadyProvider); ok {
					ready.EnableGoalTurns()
				}
			}
		case structuredFrameUpgrade:
			var upgrade brokerUpgradeFrame
			errText := ""
			if json.Unmarshal(payload, &upgrade) != nil || upgrade.Version != brokerTurnProtocolV3 {
				errText = "unsupported structured broker protocol"
			} else {
				b.mu.Lock()
				conn.protocolV3 = true
				b.mu.Unlock()
			}
			_ = conn.write(structuredFrameAck, []byte(errText))
		case structuredFrameTurnDispatch:
			errText := b.dispatchCorrelatedTurn(conn, payload)
			_ = conn.write(structuredFrameAck, []byte(errText))
		case structuredFrameTurnRebind:
			b.rebindCorrelatedTurn(conn, payload)
		case structuredFrameTurnSteer:
			_ = conn.write(structuredFrameAck, []byte(b.steerCorrelatedTurn(payload)))
		case structuredFrameInput:
			_ = conn.write(structuredFrameAck, []byte("correlated turn dispatch is required"))
		case structuredFrameUserInput:
			_ = conn.write(structuredFrameAck, []byte("correlated turn dispatch is required"))
		case structuredFrameInterrupt:
			err := ""
			if interruptErr := b.proc.Interrupt(); interruptErr != nil {
				err = interruptErr.Error()
			}
			_ = conn.write(structuredFrameAck, []byte(err))
		case structuredFrameShutdown:
			b.removeConn(conn)
			err := ""
			if closeErr := b.proc.Close(); closeErr != nil {
				err = closeErr.Error()
			}
			_ = conn.write(structuredFrameAck, []byte(err))
			return
		case structuredFramePermissionResp:
			err := ""
			var resp structuredPermissionResponseFrame
			if unmarshalErr := json.Unmarshal(payload, &resp); unmarshalErr != nil {
				err = unmarshalErr.Error()
			} else if resp.Cancel {
				if canceller, ok := b.proc.(session.UserInputCanceller); ok {
					if cancelErr := canceller.CancelUserInput(resp.PermissionId); cancelErr != nil {
						err = cancelErr.Error()
					}
				} else {
					err = "provider does not support cancelling user input"
				}
			} else if len(resp.OptionIds) > 0 {
				if respondErr := b.proc.RespondPermissionMulti(resp.PermissionId, resp.OptionIds); respondErr != nil {
					err = respondErr.Error()
				}
			} else if respondErr := b.proc.RespondPermission(resp.PermissionId, resp.OptionId); respondErr != nil {
				err = respondErr.Error()
			}
			_ = conn.write(structuredFrameAck, []byte(err))
		case structuredFrameControl:
			_ = conn.write(structuredFrameAck, []byte("correlated turn dispatch is required"))
		case structuredFrameReconfigure:
			err := ""
			var req structuredReconfigureFrame
			reconfigurer, ok := b.proc.(session.ExecutionReconfigurer)
			if unmarshalErr := json.Unmarshal(payload, &req); unmarshalErr != nil {
				err = unmarshalErr.Error()
			} else if !ok {
				err = "structured provider does not support execution reconfiguration"
			} else if reconfigureErr := reconfigurer.ReconfigureExecution(req.Model, req.ReasoningEffort); reconfigureErr != nil {
				err = reconfigureErr.Error()
			}
			_ = conn.write(structuredFrameAck, []byte(err))
		}
	}
}

func (b *structuredBroker) dispatchCorrelatedTurn(conn *structuredBrokerConn, payload []byte) string {
	return b.brokerTurns.dispatchCorrelatedTurn(conn, payload, b.proc.DispatchTurn, b.broadcastCorrelatedEvent)
}

// turnSteerVersion advertises mid-turn steering only when the provider
// process implements it, so runtimes fall back to queued input otherwise.
func (b *structuredBroker) turnSteerVersion() int {
	if _, ok := b.proc.(session.TurnSteerer); ok {
		return brokerTurnSteerV1
	}
	return 0
}

// steerCorrelatedTurn injects input into the admitted turn. The turn token
// must match the turn this broker admitted; a finished or different turn is
// rejected so the runtime can queue the input instead.
func (b *structuredBroker) steerCorrelatedTurn(payload []byte) string {
	var req brokerTurnSteerFrame
	if err := json.Unmarshal(payload, &req); err != nil {
		return err.Error()
	}
	if strings.TrimSpace(req.TurnToken) == "" {
		return "turn token is required"
	}
	steerer, ok := b.proc.(session.TurnSteerer)
	if !ok {
		return "structured provider does not support steering"
	}
	b.mu.Lock()
	active := b.turnAdmission.turnToken
	b.mu.Unlock()
	if active != req.TurnToken {
		return "steer target turn is not active"
	}
	ctx, cancel := context.WithTimeout(context.Background(), brokerSteerWait)
	defer cancel()
	if err := steerer.SteerTurn(ctx, session.TurnSteerRequest{TurnToken: req.TurnToken, Input: req.Input}); err != nil {
		if errors.Is(err, session.ErrSteerOutcomeUnknown) {
			return brokerSteerOutcomeUnknownPrefix + err.Error()
		}
		return err.Error()
	}
	return ""
}

func (b *structuredBroker) rebindCorrelatedTurn(conn *structuredBrokerConn, payload []byte) {
	b.brokerTurns.rebindCorrelatedTurn(conn, payload, structuredFrameAck, structuredFrameEmission)
}

func ShutdownStructuredBroker(socketPath string) error {
	return sendBrokerControl(socketPath, structuredFrameShutdown)
}

func (b *structuredBroker) removeConn(conn *structuredBrokerConn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if offer := b.goalOffer; offer != nil && offer.owner == conn {
		select {
		case offer.done <- brokerGoalAdmission{NativeTurnID: offer.request.NativeTurnID, Error: "runtime goal owner disconnected"}:
		default:
		}
	}
	b.turnAdmission.disconnect(conn)
	for i, c := range b.conns {
		if c == conn {
			b.conns = append(b.conns[:i], b.conns[i+1:]...)
			return
		}
	}
}
