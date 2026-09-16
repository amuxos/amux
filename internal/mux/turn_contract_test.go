package mux

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

type correlatedProc interface {
	DispatchTurn(context.Context, session.TurnDispatchRequest, func(agentstream.ProducerEmission) bool) error
	RebindTurn(string, uint64, func(agentstream.ProducerEmission) bool) error
}

type brokerContractProtocol struct {
	name          string
	wire          brokerTurnWire
	ack, emission byte
}

var brokerContractProtocols = []brokerContractProtocol{
	{"ACP", acpTurnWire, acpFrameAck, acpFrameEmission},
	{"structured", structuredTurnWire, structuredFrameAck, structuredFrameEmission},
}

// Exercise the concrete Proc adapters through the Session dispatch/rebind seam
// and real frame I/O, so sharing an implementation cannot hide miswired kinds.
func (protocol brokerContractProtocol) connect(t *testing.T) (correlatedProc, net.Conn) {
	t.Helper()
	server, client := net.Pipe()
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	done := make(chan struct{})
	newClient := func() brokerTurnClient {
		return brokerTurnClient{protocol: protocol.wire, conn: client, ackCh: make(chan error, 1), done: done, turnCorrelation: true, turnReplayVersion: brokerTurnReplayV1}
	}
	var proc correlatedProc
	if protocol.name == "ACP" {
		p := &ACPBrokerProc{
			brokerTurnClient: newClient(),
		}
		proc = p
		go p.readLoop()
	} else {
		p := &StructuredBrokerProc{
			brokerTurnClient: newClient(),
		}
		proc = p
		go p.readLoop()
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("broker reader did not exit")
		}
	})
	return proc, server
}

func TestBrokerProcDispatchTurnRejectsInvalidAndDuplicateAdmissions(t *testing.T) {
	for _, protocol := range brokerContractProtocols {
		t.Run(protocol.name, func(t *testing.T) {
			p, serverConn := protocol.connect(t)
			if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{}, func(testProducerEmission) bool { return false }); err == nil || !strings.Contains(err.Error(), "turn token") {
				t.Fatalf("DispatchTurn(empty token) error = %v", err)
			}
			if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-1"}, nil); err == nil || !strings.Contains(err.Error(), "sink") {
				t.Fatalf("DispatchTurn(nil sink) error = %v", err)
			}

			requestRead := make(chan struct{})
			allowAck := make(chan struct{})
			serverErr := make(chan error, 1)
			go func() {
				kind, _, err := readACPFrame(serverConn)
				if err == nil && kind != protocol.wire.dispatch {
					err = fmt.Errorf("frame kind=%x, want turn dispatch", kind)
				}
				close(requestRead)
				<-allowAck
				if err == nil {
					err = writeACPFrame(serverConn, protocol.ack, nil)
				}
				serverErr <- err
			}()
			firstDone := make(chan error, 1)
			go func() {
				firstDone <- p.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-1"}, func(testProducerEmission) bool { return false })
			}()
			<-requestRead
			duplicateDone := make(chan error, 1)
			go func() {
				duplicateDone <- p.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-1"}, func(testProducerEmission) bool { return false })
			}()
			select {
			case err := <-duplicateDone:
				if !errors.Is(err, session.ErrTurnSinkActive) {
					t.Fatalf("DispatchTurn(duplicate) error = %v, want ErrTurnSinkActive", err)
				}
			case <-time.After(time.Second):
				t.Fatal("duplicate token blocked behind the first ACK")
			}
			close(allowAck)
			if err := <-firstDone; err != nil {
				t.Fatalf("DispatchTurn(first) error = %v", err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}

		})
	}
}

func TestBrokerProcDispatchCancellationClosesConnection(t *testing.T) {
	for _, protocol := range brokerContractProtocols {
		t.Run(protocol.name, func(t *testing.T) {
			p, serverConn := protocol.connect(t)
			lateAckErr := make(chan error, 1)
			go func() {
				_, _, err := readACPFrame(serverConn)
				if err == nil {
					time.Sleep(50 * time.Millisecond)
					err = writeACPFrame(serverConn, protocol.ack, nil)
				}
				lateAckErr <- err
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			err := p.DispatchTurn(ctx, session.TurnDispatchRequest{TurnToken: "turn-1"}, func(testProducerEmission) bool { return false })
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("DispatchTurn() error = %v, want context deadline", err)
			}
			if err := <-lateAckErr; err == nil {
				t.Fatal("late ACK succeeded after canceled dispatch; connection remained reusable")
			}
			if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-2"}, func(testProducerEmission) bool { return false }); err == nil {
				t.Fatal("dispatch reused connection after canceled ACK wait")
			}

		})
	}
}

// Failed ACKs must remove exactly the attempted sink. A terminal received
// before that ACK still reaches its original sink, and retry can install a new
// sink without receiving observations from the failed attempt.
func TestBrokerProcFailedAckReleasesSink(t *testing.T) {
	for _, protocol := range brokerContractProtocols {
		for _, operation := range []string{"dispatch", "rebind"} {
			for _, terminalFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/terminal-first=%t", protocol.name, operation, terminalFirst), func(t *testing.T) {
					p, server := protocol.connect(t)
					kindWant := protocol.wire.dispatch
					send := func(sink func(agentstream.ProducerEmission) bool) error {
						return p.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn"}, sink)
					}
					if operation == "rebind" {
						kindWant = protocol.wire.rebind
						send = func(sink func(agentstream.ProducerEmission) bool) error { return p.RebindTurn("turn", 0, sink) }
					}
					wireDone := make(chan error, 1)
					go func() {
						for attempt := 0; attempt < 2; attempt++ {
							kind, _, err := readACPFrame(server)
							if err != nil {
								wireDone <- err
								return
							}
							if kind != kindWant {
								wireDone <- fmt.Errorf("dispatch kind = %x", kind)
								return
							}
							if attempt == 1 || terminalFirst {
								payload, err := marshalBrokerEmissionFrame("turn", 1, testTerminalEmission(agentstream.OutcomeSuccess, "completed", ""))
								if err == nil {
									err = writeACPFrame(server, protocol.emission, payload)
								}
								if err != nil {
									wireDone <- err
									return
								}
							}
							var ack []byte
							if attempt == 0 {
								ack = []byte("dispatch rejected")
							}
							if err := writeACPFrame(server, protocol.ack, ack); err != nil {
								wireDone <- err
								return
							}
						}
						wireDone <- nil
					}()
					first := make(chan agentstream.ProducerEmission, 2)
					if err := send(func(e agentstream.ProducerEmission) bool { first <- e; return false }); err == nil || !strings.Contains(err.Error(), "dispatch rejected") {
						t.Fatalf("first dispatch: %v", err)
					}
					if terminalFirst {
						select {
						case e := <-first:
							if !e.IsTerminal() {
								t.Fatal("expected terminal before failed ACK")
							}
						case <-time.After(time.Second):
							t.Fatal("terminal before ACK lost")
						}
					}
					second := make(chan agentstream.ProducerEmission, 1)
					if err := send(func(e agentstream.ProducerEmission) bool { second <- e; return false }); err != nil {
						t.Fatalf("retry: %v", err)
					}
					select {
					case e := <-second:
						if !e.IsTerminal() || e.Origin.TransportSeq != 1 {
							t.Fatalf("retry emission: %+v", e)
						}
					case <-time.After(time.Second):
						t.Fatal("retry terminal lost")
					}
					select {
					case <-first:
						t.Fatal("retry reached old sink")
					default:
					}
					if err := <-wireDone; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestBrokerProcLateOldTurnFrameFailsClosed(t *testing.T) {
	for _, protocol := range brokerContractProtocols {
		t.Run(protocol.name, func(t *testing.T) {
			p, server := protocol.connect(t)
			wireDone := make(chan error, 1)
			go func() {
				_, _, err := readACPFrame(server)
				if err == nil {
					var payload []byte
					payload, err = marshalBrokerEmissionFrame("old", 1, testTerminalEmission(agentstream.OutcomeSuccess, "completed", ""))
					if err == nil {
						err = writeACPFrame(server, protocol.emission, payload)
					}
				}
				if err == nil {
					err = writeACPFrame(server, protocol.ack, nil)
				}
				if err == nil {
					_, _, err = readACPFrame(server)
				}
				if err == nil {
					var payload []byte
					payload, err = marshalBrokerEmissionFrame("old", 2, testAssistantEmission("late"))
					if err == nil {
						err = writeACPFrame(server, protocol.emission, payload)
					}
				}
				wireDone <- err
			}()
			if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "old"}, func(agentstream.ProducerEmission) bool { return false }); err != nil {
				t.Fatal(err)
			}
			got := make(chan agentstream.ProducerEmission, 1)
			if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "new"}, func(e agentstream.ProducerEmission) bool { got <- e; return false }); err == nil {
				t.Fatal("late old frame did not close connection")
			}
			select {
			case e := <-got:
				t.Fatalf("old emission reached new sink: %+v", e)
			default:
			}
			if err := <-wireDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
