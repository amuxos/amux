package mux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStructuredBrokerForwardsSessionEventsLosslessly(t *testing.T) {
	tests := []struct {
		name     string
		emission agentstream.ProducerEmission
	}{
		{
			name: "terminal state",
			emission: agentstream.ProducerEmission{
				Origin:   agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: "codex", NativeType: "turn_done", NativeID: "event-1", NativeSeq: 11},
				Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "timeout", Message: "provider stopped"},
			},
		},
		{
			name: "permission request",
			emission: agentstream.ProducerEmission{
				Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: "claude", NativeType: "permission_requested", NativeID: "event-2", NativeSeq: 12},
				Turn: agentstream.PermissionRequested{Request: agentstream.PermissionRequest{
					ID: "permission-1", Provider: "claude",
					ToolCall: &agentstream.PermissionToolCall{ID: "tool-1", Name: "Bash", RawInput: json.RawMessage(`{"command":"pwd"}`)},
					Options:  []agentstream.PermissionOption{{ID: "allow", Name: "Allow", Kind: "allow_once"}},
					Details:  json.RawMessage(`{"source":"sidecar"}`),
				}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverConn, clientConn := net.Pipe()
			defer serverConn.Close()
			defer clientConn.Close()
			broker := &structuredBroker{conns: []*structuredBrokerConn{{conn: serverConn}}}
			forwarded := make(chan bool, 1)
			want := tt.emission
			go func() { forwarded <- broker.forwardProcEvent(want) }()

			kind, payload, err := readACPFrame(clientConn)
			if err != nil {
				t.Fatalf("read event frame: %v", err)
			}
			if kind != structuredFrameEmission {
				t.Fatalf("frame kind = %x, want structuredFrameEmission", kind)
			}
			var emission brokerEmissionFrame
			if err := json.Unmarshal(payload, &emission); err != nil {
				t.Fatalf("unmarshal session event: %v", err)
			}
			got, err := agentstream.UnmarshalProducerEmission(emission.Emission)
			if err != nil {
				t.Fatal(err)
			}
			if emission.TurnToken != "" || !reflect.DeepEqual(got, want) {
				t.Fatalf("event round trip mismatch\n got: %#v\nwant: %#v", emission, tt.emission)
			}
			if consumed := <-forwarded; consumed {
				t.Fatal("forwardProcEvent consumed event")
			}
		})
	}
}

func TestStructuredBrokerReplaysSessionMetadataToNewConnection(t *testing.T) {
	b := &structuredBroker{inputModalities: []string{"text", "image"}, inputCapabilitySource: "codex:model/list"}
	b.setSessionMetadata("thread-1", "example-model")
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	done := make(chan struct{})
	go func() {
		b.addConn(serverConn)
		close(done)
	}()

	kind, payload, err := readACPFrame(clientConn)
	if err != nil {
		t.Fatalf("read capabilities frame: %v", err)
	}
	if kind != structuredFrameCapabilities {
		t.Fatalf("first frame kind = %x, want structuredFrameCapabilities", kind)
	}
	var caps structuredCapabilitiesFrame
	if err := json.Unmarshal(payload, &caps); err != nil {
		t.Fatalf("unmarshal capabilities: %v", err)
	}
	if caps.TurnCorrelationVersion != brokerTurnProtocolV3 || caps.TurnReplayVersion != brokerTurnReplayV1 || strings.Join(caps.InputModalities, ",") != "text,image" || caps.Source != "codex:model/list" {
		t.Fatalf("capabilities = %+v", caps)
	}
	kind, payload, err = readACPFrame(clientConn)
	if err != nil {
		t.Fatalf("read metadata frame: %v", err)
	}
	if kind != structuredFrameEmission {
		t.Fatalf("second frame kind = %x, want structuredFrameEmission", kind)
	}
	var emission brokerEmissionFrame
	if err := json.Unmarshal(payload, &emission); err != nil {
		t.Fatalf("unmarshal metadata event: %v", err)
	}
	ev := capturedBrokerEmission(emission)
	if ev.Metadata.AgentSessionID != "thread-1" || ev.Metadata.EffectiveModel != "example-model" {
		t.Fatalf("metadata = %+v, want provider thread and model", ev)
	}
	<-done
	b.closeConns()
}

func TestStructuredBrokerRejectsMissingTurnCorrelationCapability(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          structuredTurnWire,
			capabilitiesReady: ready,
		},
	}
	if err := p.negotiateTurnCorrelation(context.Background()); err == nil {
		t.Fatal("negotiateTurnCorrelation() error = nil, want unsupported broker")
	}
}

func TestStructuredAdvertisedV3NegotiatesUpgrade(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          structuredTurnWire,
			conn:              clientConn,
			ackCh:             make(chan error, 1),
			done:              make(chan struct{}),
			capabilitiesReady: make(chan struct{}),
		},
	}
	go p.readLoop()

	serverErr := make(chan error, 1)
	go func() {
		capabilities, _ := json.Marshal(structuredCapabilitiesFrame{TurnCorrelationVersion: brokerTurnProtocolV3})
		err := writeACPFrame(serverConn, structuredFrameCapabilities, capabilities)
		var kind byte
		var payload []byte
		if err == nil {
			kind, payload, err = readACPFrame(serverConn)
		}
		var upgrade brokerUpgradeFrame
		if err == nil {
			err = json.Unmarshal(payload, &upgrade)
		}
		if err == nil && (kind != structuredFrameUpgrade || upgrade.Version != brokerTurnProtocolV3) {
			err = fmt.Errorf("upgrade kind=%x frame=%+v", kind, upgrade)
		}
		if err == nil {
			err = writeACPFrame(serverConn, structuredFrameAck, nil)
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
}

func TestStructuredAdvertisedV3UpgradeFailureIsFatal(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          structuredTurnWire,
			conn:              clientConn,
			ackCh:             make(chan error, 1),
			done:              make(chan struct{}),
			capabilitiesReady: make(chan struct{}),
		},
	}
	go p.readLoop()
	go func() {
		capabilities, _ := json.Marshal(structuredCapabilitiesFrame{TurnCorrelationVersion: brokerTurnProtocolV3})
		_ = writeACPFrame(serverConn, structuredFrameCapabilities, capabilities)
		_, _, _ = readACPFrame(serverConn)
		_ = writeACPFrame(serverConn, structuredFrameAck, []byte("upgrade rejected"))
	}()

	err := p.negotiateTurnCorrelation(context.Background())
	if err == nil || !strings.Contains(err.Error(), "upgrade rejected") {
		t.Fatalf("negotiateTurnCorrelation() error = %v, want upgrade rejection", err)
	}
	if p.turnCorrelationEnabled() {
		t.Fatal("failed upgrade enabled turn correlation")
	}
}

func TestStructuredBrokerProcReplaysMetadataWhenSinkAttachesLate(t *testing.T) {
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol: structuredTurnWire,
		},
	}
	p.emitEvent(testMetadataEmission("thread-1", "example-model"))
	events := make(chan testEmission, 1)
	p.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	select {
	case ev := <-events:
		if ev.Metadata.AgentSessionID != "thread-1" || ev.Metadata.EffectiveModel != "example-model" {
			t.Fatalf("metadata = %+v, want provider thread and model", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("late event sink did not receive cached metadata")
	}
}

func TestStructuredBrokerProcReplaysStickyMetadataToReplacementSink(t *testing.T) {
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol: structuredTurnWire,
		},
	}
	p.SetEventSink(func(testProducerEmission) bool { return false })
	p.emitEvent(testMetadataEmission("thread-1", "example-model"))

	events := make(chan testEmission, 1)
	p.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	select {
	case ev := <-events:
		if ev.Metadata.AgentSessionID != "thread-1" || ev.Metadata.EffectiveModel != "example-model" {
			t.Fatalf("sticky metadata = %+v, want provider thread and model", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement event sink did not receive sticky metadata")
	}
}

func TestStructuredBrokerProcPreservesTerminalBeforeEventSinkAttaches(t *testing.T) {
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol: structuredTurnWire,
		},
	}
	p.emitEvent(testAssistantEmission("before-terminal"))
	p.emitEvent(testTerminalEmission(agentstream.OutcomeSuccess, "completed", "end_turn"))

	events := make(chan testEmission, 2)
	p.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	first := <-events
	second := <-events
	if first.Text != "before-terminal" || second.Terminal == nil || second.Terminal.Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("pending events = [%+v, %+v], want output then terminal", first, second)
	}
}

func TestStructuredBrokerProcRebindTurnReplaysReconnectWindowInOrder(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          structuredTurnWire,
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
		if err == nil && (kind != structuredFrameTurnRebind || req.TurnToken != "turn-1" || req.AfterSequence != 1) {
			err = fmt.Errorf("turn rebind kind=%x req=%+v", kind, req)
		}
		if err == nil {
			first, _ := json.Marshal(testBrokerEmissionFrame("turn-1", 2, testAssistantEmission("before-terminal")))
			err = writeACPFrame(serverConn, structuredFrameEmission, first)
		}
		if err == nil {
			terminal, _ := json.Marshal(testBrokerEmissionFrame("turn-1", 3, testTerminalEmission(agentstream.OutcomeSuccess, "completed", "")))
			err = writeACPFrame(serverConn, structuredFrameEmission, terminal)
		}
		if err == nil {
			err = writeACPFrame(serverConn, structuredFrameAck, nil)
		}
		serverErr <- err
	}()

	var got []testEmission
	if err := p.RebindTurn("turn-1", 1, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		got = append(got, ev)
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "before-terminal" || got[1].Terminal == nil || got[1].Terminal.Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("rebound events = %+v, want output then terminal", got)
	}
	if got[0].Origin.TransportSeq != 2 || got[1].Origin.TransportSeq != 3 {
		t.Fatalf("rebound transport sequences = [%d,%d], want [2,3]", got[0].Origin.TransportSeq, got[1].Origin.TransportSeq)
	}
	if _, exists := p.turnSinks["turn-1"]; exists {
		t.Fatal("terminal reconnect backlog left a live turn sink")
	}
}

func TestStructuredBrokerProcRebindFailsClosedWithoutReplayCapability(t *testing.T) {
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:        structuredTurnWire,
			turnCorrelation: true,
		},
	}
	err := p.RebindTurn("turn-1", 0, func(testProducerEmission) bool { return false })
	if !errors.Is(err, session.ErrTurnReplayUnsupported) {
		t.Fatalf("RebindTurn() error = %v, want ErrTurnReplayUnsupported", err)
	}
}

func TestStructuredBrokerRebindReplaysEventsEmittedWithNoRuntimeConnection(t *testing.T) {
	b := &structuredBroker{}
	b.turnAdmission.turnToken = "turn-1"
	b.turnReplay.begin("turn-1")
	b.broadcastCorrelatedEvent("turn-1", testAssistantEmission("gap"))
	b.broadcastCorrelatedEvent("turn-1", testTerminalEmission(agentstream.OutcomeSuccess, "completed", ""))

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	conn := &structuredBrokerConn{conn: serverConn, protocolV3: true}
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
		if kind != structuredFrameEmission || emission.Sequence != wantSeq {
			t.Fatalf("replay frame kind=%x emission=%+v, want sequence %d", kind, emission, wantSeq)
		}
	}
	kind, payload, err := readACPFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if kind != structuredFrameAck || len(payload) != 0 {
		t.Fatalf("rebind ack kind=%x payload=%q", kind, payload)
	}
}

func TestStructuredBrokerEncodeFallbackReleasesCorrelatedTurn(t *testing.T) {
	b := &structuredBroker{}
	// A disconnected turn intentionally has no current delivery owner while
	// its admission and replay buffer remain active.
	b.turnAdmission.turnToken = "turn-1"
	b.turnReplay.begin("turn-1")

	b.broadcastCorrelatedEvent("turn-1", agentstream.ProducerEmission{Turn: agentstream.ToolChanged{
		Phase: agentstream.ToolPhaseUpdated,
		Tool:  agentstream.Tool{ID: "tool-1", Input: json.RawMessage(`{"unterminated"`)},
	}})

	if reason := b.turnAdmission.reserveCorrelated(&structuredBrokerConn{}, "turn-2"); reason != "" {
		t.Fatalf("broker-generated terminal retained admission: %q", reason)
	}
}

func TestStructuredBrokerProcDispatchTurnRoutesOnlyMatchingEmission(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	proc := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:        structuredTurnWire,
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
		if err == nil && (kind != structuredFrameTurnDispatch || req.TurnToken != "turn-1" || req.Input.Text() != "hello") {
			err = fmt.Errorf("turn dispatch kind=%x req=%+v", kind, req)
		}
		if err == nil {
			matched, _ := json.Marshal(testBrokerEmissionFrame("turn-1", 1, testAssistantEmission("hello")))
			err = writeACPFrame(serverConn, structuredFrameEmission, matched)
		}
		if err == nil {
			err = writeACPFrame(serverConn, structuredFrameAck, nil)
		}
		serverErr <- err
	}()

	got := make(chan testEmission, 2)
	err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "turn-1",
		Input:     session.TextUserInput("hello"),
	}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		got <- ev
		return false
	})
	if err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		if string([]byte(ev.Text)) != "hello" {
			t.Fatalf("event = %+v, want matching emission", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("matching correlated emission was not delivered")
	}
	select {
	case ev := <-got:
		t.Fatalf("foreign emission leaked into turn sink: %+v", ev)
	default:
	}
}

func TestStructuredBrokerSlowClientDoesNotBlockTurnState(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	conn := &structuredBrokerConn{conn: serverConn, protocolV3: true}
	b := &structuredBroker{
		conns: []*structuredBrokerConn{conn},
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
			reason := b.turnAdmission.reserveCorrelated(&structuredBrokerConn{}, "turn-2")
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

func TestStructuredBrokerProcTerminalRemovesTurnSink(t *testing.T) {
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:        structuredTurnWire,
			turnCorrelation: true,
			turnSinks:       make(map[string]*turnSinkEntry),
		},
	}
	got := make(chan testEmission, 2)
	p.turnSinks["turn-1"] = &turnSinkEntry{sink: func(emission testProducerEmission) bool { ev := captureEmission(emission); got <- ev; return false }}
	if err := p.emitCorrelated(testBrokerEmissionFrame("turn-1", 1, testTerminalEmission(agentstream.OutcomeSuccess, "completed", ""))); err != nil {
		t.Fatal(err)
	}
	if err := p.emitCorrelated(testBrokerEmissionFrame("turn-1", 2, testAssistantEmission("late"))); err == nil {
		t.Fatal("late event after terminal did not fail closed")
	}
	if ev := <-got; ev.Terminal == nil || ev.Terminal.Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("first event = %+v, want terminal", ev)
	}
	select {
	case ev := <-got:
		t.Fatalf("late event reached completed turn sink: %+v", ev)
	default:
	}
}

func TestStructuredBrokerRoutesCorrelatedEmissionOnlyToOwningV3Connection(t *testing.T) {
	v3Server, v3Client := net.Pipe()
	defer v3Client.Close()
	unupgradedServer, unupgradedClient := net.Pipe()
	defer unupgradedClient.Close()
	b := &structuredBroker{
		conns: []*structuredBrokerConn{
			{conn: v3Server, protocolV3: true},
			{conn: unupgradedServer},
		},
	}
	b.turnAdmission.reserveCorrelated(b.conns[0], "turn-1")
	b.turnReplay.begin("turn-1")

	done := make(chan struct{})
	go func() {
		b.broadcastCorrelatedEvent("turn-1", testTerminalEmission(agentstream.OutcomeSuccess, "completed", "end_turn"))
		close(done)
	}()

	kind, payload, err := readACPFrame(v3Client)
	if err != nil {
		t.Fatal(err)
	}
	var emission brokerEmissionFrame
	if err := json.Unmarshal(payload, &emission); err != nil {
		t.Fatal(err)
	}
	decoded := capturedBrokerEmission(emission)
	if kind != structuredFrameEmission || emission.TurnToken != "turn-1" || emission.Sequence != 1 || decoded.Terminal == nil || decoded.Terminal.Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("v3 frame kind=%x emission=%+v", kind, emission)
	}
	_ = unupgradedClient.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if kind, payload, err = readACPFrame(unupgradedClient); err == nil {
		t.Fatalf("non-owner unupgraded connection received correlated frame kind=%x payload=%q", kind, payload)
	}
	<-done
	if b.turnAdmission.turnToken != "" {
		t.Fatalf("terminal retained admission %+v", b.turnAdmission)
	}
}

func TestResponsesChatSupervisorExecutablePreservesInstallPath(t *testing.T) {
	amuxDir := filepath.Join(t.TempDir(), "logical-amux-home")
	t.Setenv("AMUX_HOME", amuxDir)
	executable := filepath.Join(amuxDir, "bin", "amuxlet")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := responsesChatSupervisorExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if got != executable {
		t.Fatalf("supervisor executable = %q, want logical install path %q", got, executable)
	}
}

func TestBuildStructuredBrokerArgsPassesResumeSessionIDFile(t *testing.T) {
	opts := StructuredLaunchOptions{
		Provider:              "codex-app-server",
		SessionID:             "amux-session",
		ResumeModel:           "example-model",
		ResumeReasoningEffort: "xhigh",
		Cwd:                   "/tmp/work",
		Env:                   []string{"CODEX_HOME=/tmp/codex"},
		ExtraArgs:             []string{"--model", "gpt-5-codex"},
		ExcludedSkillRoots: []string{
			"/tmp/work/.agents/skills/profile-only",
		},
	}

	args := buildStructuredBrokerArgs("/tmp/structured.sock", "/tmp/structured.pid", opts, "/tmp/provider-session.id", "/tmp/launch-context.json", "")

	want := []string{
		"_structured_broker",
		"--provider", "codex-app-server",
		"--session-id", "amux-session",
		"--socket", "/tmp/structured.sock",
		"--pid-file", "/tmp/structured.pid",
		"--cwd", "/tmp/work",
		"--resume-session-id-file", "/tmp/provider-session.id",
		"--resume-model", "example-model",
		"--resume-reasoning-effort", "xhigh",
		"--launch-context-file", "/tmp/launch-context.json",
		"--turn-idle-timeout-seconds", "0",
		"--turn-hard-cap-seconds", "0",
		"--tool-idle-timeout-seconds", "0",
		"--repeat-tool-call-limit", "0",
		"--env", "CODEX_HOME=/tmp/codex",
		"--excluded-skill-root", "/tmp/work/.agents/skills/profile-only",
		"--",
		"--model", "gpt-5-codex",
	}
	if fmt.Sprint(args) != fmt.Sprint(want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if strings.Contains(fmt.Sprint(args), "provider-session ") {
		t.Fatalf("args leaked raw resume session id: %#v", args)
	}
}

func TestBuildStructuredBrokerArgsPassesResponsesChatMetadataWithoutSecret(t *testing.T) {
	opts := StructuredLaunchOptions{
		Provider:  "codex-app-server",
		SessionID: "session-1",
		Cwd:       "/workspace",
		Env:       []string{"GLM_API_KEY=real-secret"},
		ExtraArgs: []string{"-c", `model_providers.amux.base_url="http://glm/v1"`},
		ResponsesChat: &ResponsesChatOptions{
			UpstreamBaseURL: "http://glm/v1",
			APIKeyEnv:       "GLM_API_KEY",
		},
	}
	credentialFile, err := writeResponsesChatCredentialFile(t.TempDir(), "session-1", &opts)
	if err != nil {
		t.Fatal(err)
	}
	args := buildStructuredBrokerArgs("/tmp/sock", "/tmp/pid", opts, "", "/tmp/context", credentialFile)
	joined := strings.Join(args, "\n")
	for _, want := range []string{"--responses-chat-upstream-base-url", "http://glm/v1", "--responses-chat-api-key-env", "GLM_API_KEY"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args = %#v, missing %q", args, want)
		}
	}
	if strings.Contains(joined, "real-secret") {
		t.Fatalf("broker args leaked credential: %#v", args)
	}
	if len(opts.Env) != 0 {
		t.Fatalf("broker env = %#v, credential was not removed", opts.Env)
	}
}

func TestWriteResponsesChatCredentialFileRefusesExistingPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("do-not-overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-1.responses-chat-credential")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	opts := StructuredLaunchOptions{
		Env: []string{"GLM_API_KEY=real-secret"},
		ResponsesChat: &ResponsesChatOptions{
			UpstreamBaseURL: "http://glm/v1",
			APIKeyEnv:       "GLM_API_KEY",
		},
	}
	if _, err := writeResponsesChatCredentialFile(dir, "session-1", &opts); err == nil {
		t.Fatal("credential creation followed an existing symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do-not-overwrite" {
		t.Fatalf("symlink target was overwritten: %q", data)
	}
}

func TestStartStructuredProcSupportsPiRPC(t *testing.T) {
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "pi")
	if err := os.WriteFile(fake, []byte(fakePiRPCScript), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	proc, _, err := startStructuredProc(StructuredLaunchOptions{
		Provider:  "pi-rpc",
		SessionID: "amux-session",
		Cwd:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("startStructuredProc(pi-rpc): %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 16)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	// Startup metadata from get_state: pi session id + confirmed model.
	select {
	case ev := <-events:
		if ev.Metadata.AgentSessionID != "pi-sess-1" || ev.Metadata.EffectiveModel != "deepseek-v4-flash" {
			t.Fatalf("startup metadata = %+v, want pi session and model", ev)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("did not observe pi startup metadata")
	}

	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "turn-pi",
		Input:     session.TextUserInput("hello"),
	}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	}); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}

	// A full pi run through the broker launch path must end in a turn_done
	// with the native result delivered first.
	timeout := time.After(10 * time.Second)
	gotResult := false
	for {
		select {
		case ev := <-events:
			if ev.Body != nil {
				switch ev.Body.Type {
				case string(agentstream.KindAssistantDelta):
					if ev.Body.Text != "hello from fake" {
						t.Fatalf("delta = %q, want hello from fake", ev.Body.Text)
					}
				case string(agentstream.KindResultObserved):
					if ev.Body.Result == nil || ev.Body.Result.Text != "hello from fake" {
						t.Fatalf("result = %+v, want hello from fake", ev.Body.Result)
					}
					gotResult = true
				case string(agentstream.KindTurnFinished):
					if !gotResult {
						t.Fatal("turn_done arrived before native result")
					}
					if ev.Body.Provider != "pi" {
						t.Fatalf("provider = %q, want pi", ev.Body.Provider)
					}
					return
				}
			}
		case <-timeout:
			t.Fatal("did not observe pi turn_done through broker launch path")
		}
	}
}

func waitForStructuredEmissionKind(t *testing.T, events <-chan testEmission, kind string, timeout time.Duration) *testEmission {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Body != nil && ev.Body.Type == kind {
				return &ev
			}
		case <-timer.C:
			return nil
		}
	}
}

// fakePiRPCScript simulates a `pi --mode rpc` server: get_state metadata on
// startup, then a single settled agent run for each prompt.
const fakePiRPCScript = `#!/usr/bin/env python3
import json, sys

def out(obj):
    print(json.dumps(obj), flush=True)

for line in sys.stdin:
    msg = json.loads(line)
    typ = msg.get("type")
    if typ == "get_state":
        out({"type":"response","command":"get_state","success":True,
             "data":{"sessionId":"pi-sess-1","model":{"id":"deepseek-v4-flash"}}})
    elif typ == "prompt":
        out({"type":"response","command":"prompt","success":True})
        out({"type":"agent_start"})
        out({"type":"message_start","message":{"id":"m1","role":"assistant","content":[],"stopReason":"pending"}})
        out({"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hello from fake"}})
        out({"type":"message_end","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hello from fake"}],"stopReason":"stop"}})
        out({"type":"turn_end","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hello from fake"}],"stopReason":"stop"}})
        out({"type":"agent_end","messages":[],"willRetry":False})
        out({"type":"agent_settled"})
`

func TestStructuredBrokerProcOnlyEmitsUnifiedEventFrames(t *testing.T) {
	tests := []struct {
		name    string
		kind    byte
		payload []byte
	}{
		{name: "removed state frame", kind: 0x11, payload: []byte(`{"state":"failed"}`)},
		{name: "removed permission frame", kind: 0x12, payload: []byte(`{"permission_id":"permission-1"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverConn, clientConn := net.Pipe()
			defer serverConn.Close()

			proc := &StructuredBrokerProc{
				brokerTurnClient: brokerTurnClient{
					protocol: structuredTurnWire,
					conn:     clientConn,
					ackCh:    make(chan error, 1),
					done:     make(chan struct{}),
				},
			}
			go proc.readLoop()

			events := make(chan testEmission, 2)
			proc.SetEventSink(func(emission testProducerEmission) bool {
				ev := captureEmission(emission)
				events <- ev
				return false
			})

			if err := writeACPFrame(serverConn, tt.kind, tt.payload); err != nil {
				t.Fatalf("write removed frame: %v", err)
			}
			terminal := agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}
			canonical := agentstream.ProducerEmission{
				Origin:   agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: "codex", NativeType: "turn_done"},
				Terminal: &terminal,
			}
			want := captureEmission(canonical)
			encoded, err := agentstream.MarshalProducerEmission(canonical)
			if err != nil {
				t.Fatalf("marshal unified event: %v", err)
			}
			payload, err := json.Marshal(brokerEmissionFrame{Emission: encoded})
			if err != nil {
				t.Fatalf("marshal unified frame: %v", err)
			}
			if err := writeACPFrame(serverConn, structuredFrameEmission, payload); err != nil {
				t.Fatalf("write unified event frame: %v", err)
			}

			select {
			case got := <-events:
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("first emitted event = %#v, want only unified event %#v", got, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no unified event emitted from readLoop")
			}
		})
	}
}

func TestStructuredBrokerProcSendsExecutionReconfigureFrame(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	proc := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol: structuredTurnWire,
			conn:     clientConn,
			ackCh:    make(chan error, 1),
			done:     make(chan struct{}),
		},
	}
	go proc.readLoop()
	serverErr := make(chan error, 1)
	go func() {
		kind, payload, err := readACPFrame(serverConn)
		var req structuredReconfigureFrame
		if err == nil {
			err = json.Unmarshal(payload, &req)
		}
		if err == nil && (kind != structuredFrameReconfigure || req.Model != "example-model" || req.ReasoningEffort != "xhigh") {
			err = fmt.Errorf("reconfigure frame kind=%x payload=%q", kind, payload)
		}
		if err == nil {
			err = writeACPFrame(serverConn, structuredFrameAck, nil)
		}
		serverErr <- err
	}()
	if err := proc.ReconfigureExecution("example-model", "xhigh"); err != nil {
		t.Fatalf("ReconfigureExecution: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

type shutdownAckTestProc struct{ session.Proc }

func (p *shutdownAckTestProc) Close() error { return nil }

func TestStructuredBrokerShutdownDrainsPendingAck(t *testing.T) {
	for _, requestKind := range []byte{structuredFrameUpgrade, structuredFrameShutdown} {
		for _, stalled := range []bool{false, true} {
			t.Run(fmt.Sprintf("kind=%x/stalled_client=%v", requestKind, stalled), func(t *testing.T) {
				server, client := net.Pipe()
				t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
				_ = client.SetDeadline(time.Now().Add(2 * time.Second))
				observed := &acpShutdownTestConn{
					Conn: server, writeStarted: make(chan struct{}, 1),
				}
				conn := &structuredBrokerConn{conn: observed}
				b := &structuredBroker{proc: &shutdownAckTestProc{}, conns: []*structuredBrokerConn{conn}}
				handlerDone := make(chan struct{})
				go func() {
					b.handleConn(conn)
					close(handlerDone)
				}()
				upgrade, _ := json.Marshal(brokerUpgradeFrame{Version: brokerTurnProtocolV3})
				if err := writeACPFrame(client, requestKind, upgrade); err != nil {
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
				if b.handleFrame(conn, structuredFrameUpgrade, upgrade) {
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
					if err != nil || kind != structuredFrameAck || len(payload) != 0 {
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
}
