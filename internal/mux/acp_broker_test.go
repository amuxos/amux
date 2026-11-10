package mux

import (
	"bufio"
	"bytes"
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
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
)

func TestACPBrokerConnectionAdvertisesV2BeforeMetadata(t *testing.T) {
	b := &acpBroker{agentSessionID: "native-session-1"}
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		b.closeConns()
	})
	done := make(chan struct{})
	go func() {
		b.addConn(serverConn)
		close(done)
	}()

	kind, payload, err := readACPFrame(clientConn)
	if err != nil {
		t.Fatalf("read capabilities: %v", err)
	}
	var capabilities brokerCapabilitiesFrame
	if err := json.Unmarshal(payload, &capabilities); err != nil {
		t.Fatalf("unmarshal capabilities: %v", err)
	}
	if kind != acpFrameCapabilities || capabilities.TurnCorrelationVersion != brokerTurnProtocolV3 || capabilities.TurnReplayVersion != brokerTurnReplayV1 {
		t.Fatalf("first frame kind=%x capabilities=%+v", kind, capabilities)
	}
	kind, payload, err = readACPFrame(clientConn)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata brokerEmissionFrame
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	metadataEvent := capturedBrokerEmission(metadata)
	if kind != acpFrameEmission || metadata.TurnToken != "" || metadataEvent.Metadata.AgentSessionID != "native-session-1" {
		t.Fatalf("second frame kind=%x metadata=%+v", kind, metadata)
	}
	<-done
}

func TestACPBrokerRejectsMissingTurnCorrelationCapability(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	p := &ACPBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          acpTurnWire,
			capabilitiesReady: ready,
		},
	}
	if err := p.negotiateTurnCorrelation(context.Background()); err == nil {
		t.Fatal("negotiateTurnCorrelation() error = nil, want unsupported broker")
	}
}

func TestACPAdvertisedV3NegotiatesUpgrade(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	p := &ACPBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          acpTurnWire,
			conn:              clientConn,
			ackCh:             make(chan error, 1),
			done:              make(chan struct{}),
			capabilitiesReady: make(chan struct{}),
		},
	}
	go p.readLoop()

	serverErr := make(chan error, 1)
	go func() {
		capabilities, _ := json.Marshal(brokerCapabilitiesFrame{TurnCorrelationVersion: brokerTurnProtocolV3})
		err := writeACPFrame(serverConn, acpFrameCapabilities, capabilities)
		var kind byte
		var payload []byte
		if err == nil {
			kind, payload, err = readACPFrame(serverConn)
		}
		var upgrade brokerUpgradeFrame
		if err == nil {
			err = json.Unmarshal(payload, &upgrade)
		}
		if err == nil && (kind != acpFrameUpgrade || upgrade.Version != brokerTurnProtocolV3) {
			err = fmt.Errorf("upgrade kind=%x frame=%+v", kind, upgrade)
		}
		if err == nil {
			err = writeACPFrame(serverConn, acpFrameAck, nil)
		}
		serverErr <- err
	}()

	if err := p.negotiateTurnCorrelation(context.Background()); err != nil {
		t.Fatalf("negotiateTurnCorrelation() error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if !p.turnCorrelationEnabled() {
		t.Fatal("advertised protocol v3 was not enabled")
	}
	if modalities, source := p.InputCapabilities(); len(modalities) != 0 || source != "" {
		t.Fatalf("older broker invented input capabilities: %v, %q", modalities, source)
	}
}

func TestACPBrokerConnectsAndForwardsTurnDone(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "amux-acp-broker-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "acp.sock")
	pidFile := filepath.Join(dir, "acp.pid")
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPBrokerServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP_BROKER=1", "AMUX_FAKE_ACP_STOP_REASON=cancelled")

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunACPBrokerWithOptions(sock, pidFile, ACPBrokerOptions{Cwd: dir, Cmd: cmd})
	}()
	pid := 0
	pidDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(pidDeadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			if v, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && v > 0 {
				pid = v
				break
			}
		}
		select {
		case err := <-errCh:
			t.Fatalf("broker exited early: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if pid == 0 {
		t.Fatal("timeout waiting for pid file")
	}

	proc, _, err := ConnectACP(sock, pid)
	if err != nil {
		t.Fatalf("ConnectACP: %v", err)
	}
	defer proc.Close()
	if !proc.turnCorrelationEnabled() {
		t.Fatal("new ACP broker did not negotiate correlated-turn protocol v3")
	}

	events := make(chan testEmission, 8)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "turn-ping",
		Input:     session.TextUserInput("ping"),
	}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	}); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}

	deadline := time.After(2 * time.Second)
	sawAssistant := false
waitTurnDone:
	for {
		select {
		case ev := <-events:
			if ev.Body != nil && ev.Body.Type == string(agentstream.KindAssistantDelta) {
				if string([]byte(ev.Text)) != "pong" {
					t.Fatalf("assistant event data = %q, want pong", []byte(ev.Text))
				}
				sawAssistant = true
				continue
			}
			if ev.Terminal == nil {
				continue
			}
			if !sawAssistant {
				t.Fatal("turn_done arrived before assistant event")
			}
			if ev.Terminal.Outcome != agentstream.OutcomeCancelled || ev.Terminal.Message != "cancelled" {
				t.Fatalf("event = %+v, want turn_done cancelled", ev)
			}
			break waitTurnDone
		case <-deadline:
			t.Fatal("timed out waiting for turn_done")
		}
	}
	_ = proc.Close()
	select {
	case <-errCh:
	case <-time.After(100 * time.Millisecond):
		// The broker owns the fake Acp process; it may keep running until the
		// test process exits. This is fine for reconnect behavior.
	}
}

func TestACPBrokerProcDispatchTurnCarriesOpaqueToken(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	proc := &ACPBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:        acpTurnWire,
			conn:            clientConn,
			ackCh:           make(chan error, 1),
			done:            make(chan struct{}),
			turnCorrelation: true,
			turnSinks:       make(map[string]*turnSinkEntry),
		},
	}
	go proc.readLoop()

	serverErr := make(chan error, 1)
	go func() {
		kind, payload, err := readACPFrame(serverConn)
		var req brokerTurnDispatchFrame
		if err == nil {
			err = json.Unmarshal(payload, &req)
		}
		if err == nil && (kind != acpFrameTurnDispatch || req.TurnToken != "turn-1" || req.Input.Text() != "hello") {
			err = fmt.Errorf("turn dispatch kind=%x req=%+v", kind, req)
		}
		if err == nil {
			emission, _ := json.Marshal(testBrokerEmissionFrame(req.TurnToken, 1, testTerminalEmission(agentstream.OutcomeSuccess, "completed", "")))
			err = writeACPFrame(serverConn, acpFrameEmission, emission)
		}
		if err == nil {
			err = writeACPFrame(serverConn, acpFrameAck, nil)
		}
		serverErr <- err
	}()

	got := make(chan testEmission, 1)
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-1", Input: session.TextUserInput("hello")}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		got <- ev
		return false
	}); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		if ev.Terminal == nil || ev.Terminal.Outcome != agentstream.OutcomeSuccess {
			t.Fatalf("event = %+v, want terminal", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("correlated terminal was not delivered")
	}
}

func TestACPBrokerProcRebindTurnDeliversReconnectWindow(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	p := &ACPBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          acpTurnWire,
			conn:              clientConn,
			ackCh:             make(chan error, 1),
			done:              make(chan struct{}),
			turnCorrelation:   true,
			turnReplayVersion: brokerTurnReplayV1,
			turnSinks:         make(map[string]*turnSinkEntry),
		},
	}
	go p.readLoop()

	serverErr := make(chan error, 1)
	go func() {
		kind, payload, err := readACPFrame(serverConn)
		var req brokerTurnRebindFrame
		if err == nil {
			err = json.Unmarshal(payload, &req)
		}
		if err == nil && (kind != acpFrameTurnRebind || req.TurnToken != "turn-1" || req.AfterSequence != 1) {
			err = fmt.Errorf("turn rebind kind=%x req=%+v", kind, req)
		}
		if err == nil {
			emission, _ := json.Marshal(testBrokerEmissionFrame("turn-1", 2, testAssistantEmission("continued")))
			err = writeACPFrame(serverConn, acpFrameEmission, emission)
		}
		if err == nil {
			err = writeACPFrame(serverConn, acpFrameAck, nil)
		}
		serverErr <- err
	}()

	got := make(chan testEmission, 1)
	if err := p.RebindTurn("turn-1", 1, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		got <- ev
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		if string([]byte(ev.Text)) != "continued" || ev.Origin.TransportSeq != 2 {
			t.Fatalf("rebound event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnect-window event was not rebound")
	}
}

func TestACPBrokerProcRebindFailsClosedWithoutReplayCapability(t *testing.T) {
	p := &ACPBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:        acpTurnWire,
			turnCorrelation: true,
		},
	}
	err := p.RebindTurn("turn-1", 0, func(testProducerEmission) bool { return false })
	if !errors.Is(err, session.ErrTurnReplayUnsupported) {
		t.Fatalf("RebindTurn() error = %v, want ErrTurnReplayUnsupported", err)
	}
}

func TestACPBrokerRebindReplaysEventsEmittedWithNoRuntimeConnection(t *testing.T) {
	b := &acpBroker{}
	b.turnAdmission.turnToken = "turn-1"
	b.turnReplay.begin("turn-1")
	b.broadcastCorrelatedEvent("turn-1", testAssistantEmission("gap"))
	b.broadcastCorrelatedEvent("turn-1", testTerminalEmission(agentstream.OutcomeSuccess, "completed", ""))

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	conn := &acpBrokerConn{conn: serverConn, protocolV3: true}
	payload, _ := json.Marshal(brokerTurnRebindFrame{TurnToken: "turn-1", AfterSequence: 0})
	go b.rebindCorrelatedTurn(conn, payload)

	for wantSeq := uint64(1); wantSeq <= 2; wantSeq++ {
		kind, frame, err := readACPFrame(clientConn)
		if err != nil {
			t.Fatal(err)
		}
		var emission brokerEmissionFrame
		if err := json.Unmarshal(frame, &emission); err != nil {
			t.Fatal(err)
		}
		if kind != acpFrameEmission || emission.Sequence != wantSeq {
			t.Fatalf("replay frame kind=%x emission=%+v, want sequence %d", kind, emission, wantSeq)
		}
	}
	kind, payload, err := readACPFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if kind != acpFrameAck || len(payload) != 0 {
		t.Fatalf("rebind ack kind=%x payload=%q", kind, payload)
	}
}

func TestACPBrokerEncodeFallbackReleasesCorrelatedTurn(t *testing.T) {
	b := &acpBroker{}
	// A disconnected turn intentionally has no current delivery owner while
	// its admission and replay buffer remain active.
	b.turnAdmission.turnToken = "turn-1"
	b.turnReplay.begin("turn-1")

	b.broadcastCorrelatedEvent("turn-1", agentstream.ProducerEmission{Turn: agentstream.ToolChanged{
		Phase: agentstream.ToolPhaseUpdated,
		Tool:  agentstream.Tool{ID: "tool-1", Input: json.RawMessage(`{"unterminated"`)},
	}})

	if reason := b.turnAdmission.reserveCorrelated(&acpBrokerConn{}, "turn-2"); reason != "" {
		t.Fatalf("broker-generated terminal retained admission: %q", reason)
	}
}

func TestACPBrokerSlowClientDoesNotBlockTurnState(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	conn := &acpBrokerConn{conn: serverConn, protocolV3: true}
	b := &acpBroker{
		conns: []*acpBrokerConn{conn},
	}
	b.turnAdmission.reserveCorrelated(conn, "turn-1")
	b.turnReplay.begin("turn-1")

	// Hold this connection's writer so the terminal broadcast cannot finish.
	// Broker lifecycle state must nevertheless be released before socket I/O.
	conn.mu.Lock()
	broadcastDone := make(chan struct{})
	go func() {
		b.broadcastCorrelatedEvent("turn-1", testTerminalEmission(agentstream.OutcomeSuccess, "completed", ""))
		close(broadcastDone)
	}()

	stateResult := make(chan string, 1)
	go func() {
		for {
			b.mu.Lock()
			reason := b.turnAdmission.reserveCorrelated(&acpBrokerConn{}, "turn-2")
			b.mu.Unlock()
			if reason == "" {
				stateResult <- reason
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case reason := <-stateResult:
		if reason != "" {
			t.Fatalf("next admission remained blocked after terminal: %q", reason)
		}
	case <-time.After(250 * time.Millisecond):
		conn.mu.Unlock()
		_ = clientConn.Close()
		_ = serverConn.Close()
		t.Fatal("slow client write held the broker lifecycle mutex")
	}

	_ = clientConn.Close()
	conn.mu.Unlock()
	select {
	case <-broadcastDone:
	case <-time.After(time.Second):
		_ = serverConn.Close()
		t.Fatal("broadcast did not stop after the slow client disconnected")
	}
	_ = serverConn.Close()
}

type acpShutdownTestConn struct {
	net.Conn
	writeStarted chan struct{}
}

func (c *acpShutdownTestConn) Write(p []byte) (int, error) {
	select {
	case c.writeStarted <- struct{}{}:
	default:
	}
	return c.Conn.Write(p)
}

func TestACPBrokerShutdownDrainsPendingAck(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(fmt.Sprintf("stalled_client=%v", stalled), func(t *testing.T) {
			server, client := net.Pipe()
			t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			observed := &acpShutdownTestConn{
				Conn: server, writeStarted: make(chan struct{}, 1),
			}
			conn := &acpBrokerConn{conn: observed}
			b := &acpBroker{conns: []*acpBrokerConn{conn}}
			handlerDone := make(chan struct{})
			go func() {
				b.handleConn(conn)
				close(handlerDone)
			}()
			upgrade, _ := json.Marshal(brokerUpgradeFrame{Version: brokerTurnProtocolV3})
			if err := writeACPFrame(client, acpFrameUpgrade, upgrade); err != nil {
				t.Fatal(err)
			}
			select {
			case <-observed.writeStarted:
			case <-time.After(time.Second):
				t.Fatal("request did not reach its ACK write")
			}

			drainTimeout := time.Second
			if stalled {
				drainTimeout = 50 * time.Millisecond
			}
			shutdownDone := make(chan struct{})
			go func() {
				b.shutdown(time.Now().Add(drainTimeout))
				close(shutdownDone)
			}()
			stoppingDeadline := time.Now().Add(time.Second)
			for {
				b.mu.Lock()
				stopping := b.stopping
				b.mu.Unlock()
				if stopping {
					break
				}
				if time.Now().After(stoppingDeadline) {
					t.Fatal("shutdown did not stop request admission")
				}
				time.Sleep(time.Millisecond)
			}
			if b.handleFrame(conn, acpFrameUpgrade, upgrade) {
				t.Fatal("shutdown accepted a new request")
			}
			lateServer, lateClient := net.Pipe()
			defer lateClient.Close()
			defer lateServer.Close()
			if b.addConn(lateServer) != nil {
				t.Fatal("shutdown accepted a new connection")
			}
			if !stalled {
				kind, payload, err := readACPFrame(client)
				if err != nil || kind != acpFrameAck || len(payload) != 0 {
					t.Fatalf("pending ACK: kind=%x payload=%q err=%v", kind, payload, err)
				}
			}
			select {
			case <-shutdownDone:
			case <-time.After(time.Second):
				t.Fatal("shutdown blocked on the client")
			}
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Fatal("connection handler did not exit")
			}
			if _, _, err := readACPFrame(client); err == nil {
				t.Fatal("connection remained open after shutdown")
			}
		})
	}
}

func TestACPBrokerResumesSessionAndForwardsAgentSessionID(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "amux-acp-broker-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "acp.sock")
	pidFile := filepath.Join(dir, "acp.pid")
	capture := filepath.Join(dir, "capture.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPBrokerServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP_BROKER=1", "AMUX_FAKE_ACP_RESUME=1", "AMUX_FAKE_ACP_CAPTURE="+capture)

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunACPBrokerWithOptions(sock, pidFile, ACPBrokerOptions{Cwd: dir, Cmd: cmd, ResumeSessionID: "existing-session"})
	}()
	pid := waitTestPIDFile(t, pidFile, errCh)

	proc, _, err := ConnectACP(sock, pid)
	if err != nil {
		t.Fatalf("ConnectACP: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 1)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	select {
	case ev := <-events:
		if ev.Metadata == nil || ev.Metadata.AgentSessionID != "existing-session" || ev.Origin.Channel != "agent" {
			t.Fatalf("event = %+v, want AgentSessionID existing-session from agent", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AgentSessionID event")
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !bytes.Contains(data, []byte(`"method":"session/resume"`)) {
		t.Fatalf("capture missing session/resume:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`"sessionId":"existing-session"`)) {
		t.Fatalf("capture missing resume session id:\n%s", data)
	}

	_ = proc.Close()
	select {
	case <-errCh:
	case <-time.After(100 * time.Millisecond):
	}
}

func TestACPBrokerProcSetEventSinkPreservesPendingEventOrderWhenLiveEventArrives(t *testing.T) {
	proc := &ACPBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol: acpTurnWire,
			pendingEvents: []agentstream.ProducerEmission{
				{Session: agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Code: "test", Message: "pending-1"}},
				{Session: agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Code: "test", Message: "pending-2"}},
			},
		},
	}
	var got []string

	proc.SetEventSink(func(emission testProducerEmission) bool {
		diagnostic := emission.Session.(agentstream.SessionDiagnostic)
		got = append(got, diagnostic.Message)
		if diagnostic.Message == "pending-1" {
			proc.emitEvent(agentstream.ProducerEmission{Session: agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Code: "test", Message: "live"}})
		}
		return false
	})

	want := []string{"pending-1", "pending-2", "live"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestBuildACPBrokerArgsPassesResumeSessionIDFile(t *testing.T) {
	cmd := exec.Command("/usr/bin/env", "test-acp-agent", "acp", "serve")
	cmd.Env = []string{"ACP_TEST_HOME=/tmp/acp-test"}

	args := buildACPBrokerArgs("/tmp/acp.sock", "/tmp/acp.pid", ACPLaunchOptions{
		Cwd: "/tmp/work", Cmd: cmd, PromptTailDrain: 200 * time.Millisecond,
		AgentType: "test-acp-agent", Model: "model-a", ReasoningEffort: "high",
	}, "/tmp/native-session.id", "/tmp/launch-context.json")

	want := []string{
		"_acp_broker",
		"--socket", "/tmp/acp.sock",
		"--pid-file", "/tmp/acp.pid",
		"--cwd", "/tmp/work",
		"--agent-type", "test-acp-agent",
		"--model", "model-a",
		"--reasoning-effort", "high",
		"--resume-session-id-file", "/tmp/native-session.id",
		"--launch-context-file", "/tmp/launch-context.json",
		"--prompt-tail-drain", "200ms",
		"--env", "ACP_TEST_HOME=/tmp/acp-test",
		"--",
		"/usr/bin/env", "test-acp-agent", "acp", "serve",
	}
	if fmt.Sprint(args) != fmt.Sprint(want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if strings.Contains(fmt.Sprint(args), "native-session ") {
		t.Fatalf("args leaked raw resume session id: %#v", args)
	}
}

func TestACPBrokerInterruptDoesNotWaitForPromptAck(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "amux-acp-broker-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "acp.sock")
	pidFile := filepath.Join(dir, "acp.pid")
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPBrokerServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP_BROKER=1", "AMUX_FAKE_ACP_WAIT_CANCEL=1", "AMUX_FAKE_ACP_CLOSE_STDOUT=1")

	errCh := make(chan error, 1)
	go func() {
		// Hold prompt completion until the broker observes output EOF and stops
		// the child. This deterministically exercises shutdown while the prompt
		// result and dispatch ACK are still pending.
		errCh <- RunACPBrokerWithOptions(sock, pidFile, ACPBrokerOptions{Cwd: dir, Cmd: cmd, PromptTailDrain: 5 * time.Second})
	}()
	pid := waitTestPIDFile(t, pidFile, errCh)

	proc, disconnected, err := ConnectACP(sock, pid)
	if err != nil {
		t.Fatalf("ConnectACP: %v", err)
	}
	defer proc.Close()

	events := make(chan agentstream.ProducerEmission, 8)
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
			TurnToken: "turn-interrupt",
			Input:     session.TextUserInput("ping"),
		}, func(ev agentstream.ProducerEmission) bool {
			events <- ev
			return false
		})
	}()

	// Wait for observable prompt activity instead of assuming it started after
	// a sleep. Interrupt must use its control connection while dispatch waits.
	select {
	case ev := <-events:
		if delta, ok := ev.Turn.(agentstream.AssistantDelta); !ok || delta.Text != "pong" {
			t.Fatalf("first prompt event = %+v, want pong", ev)
		}
	case err := <-writeDone:
		t.Fatalf("DispatchTurn returned before prompt activity: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for prompt activity")
	}
	select {
	case err := <-writeDone:
		t.Fatalf("DispatchTurn returned before interrupt: %v", err)
	default:
	}

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- proc.Interrupt()
	}()
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Interrupt: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Interrupt blocked behind pending Acp prompt")
	}

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("DispatchTurn after interrupt: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for prompt write to finish")
	}

	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("broker did not close the connection after draining the prompt")
	}
	close(events)
	var resultSeen bool
	terminals := 0
	for ev := range events {
		if result, ok := ev.Turn.(agentstream.ResultObserved); ok && result.Text == "pong" {
			resultSeen = true
		}
		if ev.Terminal != nil {
			terminals++
			if !resultSeen || ev.Terminal.Outcome != agentstream.OutcomeCancelled {
				t.Fatalf("terminal = %+v, resultSeen = %v; want result before cancellation", ev.Terminal, resultSeen)
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal count = %d, want 1", terminals)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("broker exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker did not exit after draining the prompt")
	}
}

func TestFakeACPBrokerServer(t *testing.T) {
	if os.Getenv("AMUX_FAKE_ACP_BROKER") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	var pendingPromptID int64
	for scanner.Scan() {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if capture := os.Getenv("AMUX_FAKE_ACP_CAPTURE"); capture != "" {
			f, err := os.OpenFile(capture, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err == nil {
				_, _ = f.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
				_ = f.Close()
			}
		}
		switch req.Method {
		case "initialize":
			if os.Getenv("AMUX_FAKE_ACP_IMAGES") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"agentCapabilities":{"promptCapabilities":{"image":true}}}}`+"\n", req.ID)
			} else if os.Getenv("AMUX_FAKE_ACP_RESUME") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"agentCapabilities":{"sessionCapabilities":{"resume":{}}}}}`+"\n", req.ID)
			} else {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
			}
		case "session/new":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"fake-session"}}`+"\n", req.ID)
		case "session/resume":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
		case "session/prompt":
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fake-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"pong"}}}}`)
			if os.Getenv("AMUX_FAKE_ACP_WAIT_CANCEL") == "1" {
				pendingPromptID = req.ID
				continue
			}
			if reason := os.Getenv("AMUX_FAKE_ACP_STOP_REASON"); reason != "" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"stopReason":%q}}`+"\n", req.ID, reason)
			} else {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
			}
			os.Exit(0)
		case "session/cancel":
			if pendingPromptID != 0 {
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"stopReason":"cancelled"}}`+"\n", pendingPromptID)
			}
			if os.Getenv("AMUX_FAKE_ACP_CLOSE_STDOUT") == "1" {
				_ = os.Stdout.Close()
				continue
			}
			os.Exit(0)
		}
	}
	os.Exit(0)
}

// The ACP launch path shares the pid-file wait, so it has to report an early
// broker exit the same way instead of sitting out the timeout.
func TestLaunchACPContextSurfacesEarlyBrokerExit(t *testing.T) {
	amuxDir := t.TempDir()
	fakeAmuxlet := filepath.Join(t.TempDir(), "amuxlet")
	script := "#!/bin/sh\necho 'permission denied' >&2\nexit 126\n"
	if err := os.WriteFile(fakeAmuxlet, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake amuxlet: %v", err)
	}

	started := time.Now()
	_, _, err := LaunchACPContext(context.Background(), fakeAmuxlet, "sess-early-exit", amuxDir,
		ACPLaunchOptions{Cmd: exec.Command("demo-agent", "acp", "serve"), Cwd: amuxDir})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected the launch to fail")
	}
	if elapsed > 15*time.Second {
		t.Fatalf("launch waited %s for a broker that had already exited", elapsed)
	}
	for _, want := range []string{"exited", "permission denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func waitTestPIDFile(t *testing.T, pidFile string, errCh <-chan error) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			if v, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && v > 0 {
				return v
			}
		}
		select {
		case err := <-errCh:
			t.Fatalf("broker exited early: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("timeout waiting for pid file")
	return 0
}

func TestACPBrokerArgsPreserveExplicitDisabledWatchdogs(t *testing.T) {
	cmd := exec.Command("/bin/test-agent")
	defaults := buildACPBrokerArgs("socket", "pid", ACPLaunchOptions{Cmd: cmd}, "", "")
	for _, arg := range defaults {
		if strings.Contains(arg, "timeout-seconds") || arg == "--turn-hard-cap-seconds" || arg == "--repeat-tool-call-limit" {
			t.Fatalf("nil watchdog emitted override: %v", defaults)
		}
	}
	args := buildACPBrokerArgs("socket", "pid", ACPLaunchOptions{Cmd: cmd, Watchdog: &config.StructuredProviderConfig{}}, "", "")
	for _, flag := range []string{"--turn-idle-timeout-seconds", "--turn-hard-cap-seconds", "--tool-idle-timeout-seconds", "--repeat-tool-call-limit"} {
		found := false
		for i, arg := range args {
			if arg == flag && i+1 < len(args) && args[i+1] == "0" {
				found = true
			}
		}
		if !found {
			t.Fatalf("explicit disabled flag %s missing: %v", flag, args)
		}
	}
}
