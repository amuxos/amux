package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"
	"github.com/amuxos/amux/internal/session"
)

type steerCall struct {
	method string
	params map[string]any
}

var errSteerNoReply = &providerjsonrpc.Error{Code: -1, Message: "no reply"}

func fakeAppServerClient(t *testing.T, reply func(method string) *providerjsonrpc.Error) (*providerjsonrpc.Client, <-chan steerCall) {
	t.Helper()
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	client := providerjsonrpc.NewClient(clientRead, clientWrite)
	go func() { _ = client.Run() }()
	calls := make(chan steerCall, 16)
	go func() {
		scanner := bufio.NewScanner(serverRead)
		for scanner.Scan() {
			var msg providerjsonrpc.Message
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				continue
			}
			var params map[string]any
			_ = json.Unmarshal(msg.Params, &params)
			calls <- steerCall{method: msg.Method, params: params}
			rpcErr := reply(msg.Method)
			if rpcErr == errSteerNoReply {
				continue
			}
			resp := providerjsonrpc.Message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{}`)}
			if rpcErr != nil {
				resp = providerjsonrpc.Message{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr}
			}
			line, _ := json.Marshal(resp)
			_, _ = serverWrite.Write(append(line, '\n'))
		}
	}()
	t.Cleanup(func() {
		_ = clientWrite.Close()
		_ = serverWrite.Close()
	})
	return client, calls
}

func TestProcSteerTurnTargetsTheAmuxTurnsNativeTurn(t *testing.T) {
	var reply *providerjsonrpc.Error
	client, calls := fakeAppServerClient(t, func(string) *providerjsonrpc.Error { return reply })
	p := &Proc{client: client, threadID: "thread-1", turnID: "native-1"}
	if err := p.eventSinks.BeginTurnFor("turn-1", func(agentstream.ProducerEmission) bool { return true }); err != nil {
		t.Fatalf("BeginTurnFor: %v", err)
	}
	steer := func(ctx context.Context, token string) error {
		return p.SteerTurn(ctx, session.TurnSteerRequest{TurnToken: token, Input: session.TextUserInput("use dry-run")})
	}

	if err := steer(context.Background(), "turn-2"); !errors.Is(err, session.ErrSteerUnavailable) {
		t.Fatalf("other amux turn: %v, want unavailable", err)
	}
	if err := steer(context.Background(), "turn-1"); err != nil {
		t.Fatalf("SteerTurn: %v", err)
	}
	call := <-calls
	if call.method != "turn/steer" || call.params["threadId"] != "thread-1" || call.params["expectedTurnId"] != "native-1" {
		t.Fatalf("turn/steer = %+v", call)
	}

	reply = &providerjsonrpc.Error{Code: -32600, Message: "expected turn is not active"}
	if err := steer(context.Background(), "turn-1"); !errors.Is(err, session.ErrSteerUnavailable) || errors.Is(err, session.ErrSteerOutcomeUnknown) {
		t.Fatalf("server rejection: %v, want unavailable", err)
	}
	<-calls

	reply = errSteerNoReply
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := steer(ctx, "turn-1"); !errors.Is(err, session.ErrSteerOutcomeUnknown) {
		t.Fatalf("lost reply: %v, want unknown outcome", err)
	}
	<-calls

	// The native turn id is not known yet (turn/start has not answered).
	p.threadMu.Lock()
	p.turnID = ""
	p.threadMu.Unlock()
	if err := steer(context.Background(), "turn-1"); !errors.Is(err, session.ErrSteerUnavailable) {
		t.Fatalf("unknown native turn: %v, want unavailable", err)
	}
}
