package mux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/acp"
	"github.com/amuxos/amux/internal/agentstream"
)

func TestACPExecutionConfigurationFrameAndAck(t *testing.T) {
	for _, ack := range []string{"", "native configuration rejected"} {
		t.Run(fmt.Sprintf("ack=%q", ack), func(t *testing.T) {
			server, client := net.Pipe()
			t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
			proc := configPipeProc(client)
			go proc.readLoop()
			serverErr := make(chan error, 1)
			go func() {
				capabilities, _ := json.Marshal(acpCapabilitiesFrame{ExecutionReconfigure: true})
				if err := writeACPFrame(server, acpFrameCapabilities, capabilities); err != nil {
					serverErr <- err
					return
				}
				kind, payload, err := readACPFrame(server)
				var config acpExecutionConfigFrame
				if err == nil {
					err = json.Unmarshal(payload, &config)
				}
				if err == nil && (kind != acpFrameReconfigure || config.Model != "model-b" || config.ReasoningEffort != "low") {
					err = fmt.Errorf("configuration frame kind=%x body=%+v", kind, config)
				}
				if err == nil {
					err = writeACPFrame(server, acpFrameAck, []byte(ack))
				}
				serverErr <- err
			}()
			awaitConfigCapabilities(t, proc)
			err := proc.ReconfigureExecution("model-b", "low")
			if ack == "" && err != nil || ack != "" && (err == nil || err.Error() != ack) {
				t.Fatalf("ReconfigureExecution() = %v, want %q", err, ack)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestACPOlderBrokerRejectsConfigurationWithoutWritingUnknownFrame(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	proc := configPipeProc(client)
	go proc.readLoop()
	capabilities, _ := json.Marshal(brokerCapabilitiesFrame{TurnCorrelationVersion: brokerTurnProtocolV3})
	if err := writeACPFrame(server, acpFrameCapabilities, capabilities); err != nil {
		t.Fatal(err)
	}
	awaitConfigCapabilities(t, proc)
	if err := proc.ReconfigureExecution("model-b", "low"); !errors.Is(err, acp.ErrExecutionConfigurationUnsupported) {
		t.Fatalf("ReconfigureExecution() = %v, want unsupported", err)
	}
	_ = server.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := readACPFrame(server); err == nil {
		t.Fatal("older broker received an unknown configuration frame")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("connection failed instead of remaining idle: %v", err)
	}
}

func TestACPConfigurationMetadataSurvivesBrokerReconnect(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestFakeACPConfigBrokerServer$")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP_CONFIG_BROKER=1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	native, _, err := acp.StartWithOptions(ctx, cmd, acp.StartOptions{
		Cwd: t.TempDir(), Model: "model-a", ReasoningEffort: "high",
		ExecutionConfig: func(model, effort string) (map[string]string, error) {
			return map[string]string{"model": model + "/" + effort}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker := &acpBroker{proc: native, agentSessionID: native.SessionID(), effectiveModel: native.EffectiveModel()}
	native.SetEventSink(func(ev agentstream.ProducerEmission) bool { broker.broadcastEvent(ev); return false })
	t.Cleanup(func() { broker.closeConns(); _ = native.Close() })
	first := connectConfigPipe(t, broker)
	metadata := make(chan agentstream.SessionMetadataChanged, 4)
	first.SetEventSink(func(ev agentstream.ProducerEmission) bool {
		if value, ok := ev.Session.(agentstream.SessionMetadataChanged); ok {
			metadata <- value
		}
		return false
	})
	assertConfigMetadata(t, metadata, "model-a")
	if err := first.ReconfigureExecution("model-b", "low"); err != nil {
		t.Fatalf("native configuration: %v", err)
	}
	assertConfigMetadata(t, metadata, "model-b")
	if err := first.ReconfigureExecution("unadvertised-model", "low"); err == nil {
		t.Fatal("configuration accepted an unadvertised model")
	}
	_ = first.conn.Close()
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("first client did not disconnect")
	}
	second := connectConfigPipe(t, broker)
	second.SetEventSink(func(ev agentstream.ProducerEmission) bool {
		if value, ok := ev.Session.(agentstream.SessionMetadataChanged); ok {
			metadata <- value
		}
		return false
	})
	assertConfigMetadata(t, metadata, "model-b")
	if err := second.ReconfigureExecution("model-a", "high"); err != nil {
		t.Fatalf("configuration capability lost after reconnect: %v", err)
	}
	assertConfigMetadata(t, metadata, "model-a")
}

func configPipeProc(conn net.Conn) *ACPBrokerProc {
	return &ACPBrokerProc{brokerTurnClient: brokerTurnClient{
		protocol: acpTurnWire, conn: conn, ackCh: make(chan error, 1), done: make(chan struct{}), capabilitiesReady: make(chan struct{}),
	}}
}

func awaitConfigCapabilities(t *testing.T, proc *ACPBrokerProc) {
	t.Helper()
	select {
	case <-proc.capabilitiesReady:
	case <-time.After(time.Second):
		t.Fatal("broker did not advertise capabilities")
	}
}

func connectConfigPipe(t *testing.T, broker *acpBroker) *ACPBrokerProc {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	proc := configPipeProc(client)
	go proc.readLoop()
	go func() {
		if conn := broker.addConn(server); conn != nil {
			broker.handleConn(conn)
		}
	}()
	awaitConfigCapabilities(t, proc)
	return proc
}

func assertConfigMetadata(t *testing.T, events <-chan agentstream.SessionMetadataChanged, model string) {
	t.Helper()
	select {
	case got := <-events:
		if got.AgentSessionID != "config-session" || got.EffectiveModel != model {
			t.Fatalf("session metadata = %+v, want config-session/%s", got, model)
		}
	case <-time.After(time.Second):
		t.Fatalf("no session metadata for %s", model)
	}
}

func TestFakeACPConfigBrokerServer(t *testing.T) {
	if os.Getenv("AMUX_FAKE_ACP_CONFIG_BROKER") != "1" {
		return
	}
	value := "model-a/high"
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
		case "session/new", "session/set_config_option":
			if req.Method == "session/set_config_option" {
				if req.Params.ConfigID != "model" || req.Params.Value != "model-a/high" && req.Params.Value != "model-b/low" {
					fmt.Printf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32602,"message":"unexpected config"}}`+"\n", req.ID)
					continue
				}
				value = req.Params.Value
			}
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"config-session","configOptions":[{"id":"model","type":"select","currentValue":%q,"options":[{"value":"model-a/high"},{"value":"model-b/low"}]}]}}`+"\n", req.ID, value)
		}
	}
	os.Exit(0)
}
