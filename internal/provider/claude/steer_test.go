package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"
	"github.com/amuxos/amux/internal/session"
)

type rpcCall struct {
	method string
	params map[string]any
}

// errNoReply makes the fake sidecar swallow a request.
var errNoReply = &providerjsonrpc.Error{Code: -1, Message: "no reply"}

func fakeSidecarClient(t *testing.T, reply func(method string) *providerjsonrpc.Error) (*providerjsonrpc.Client, <-chan rpcCall) {
	t.Helper()
	clientRead, sidecarWrite := io.Pipe()
	sidecarRead, clientWrite := io.Pipe()
	client := providerjsonrpc.NewClient(clientRead, clientWrite)
	go func() { _ = client.Run() }()
	calls := make(chan rpcCall, 16)
	go func() {
		scanner := bufio.NewScanner(sidecarRead)
		for scanner.Scan() {
			var msg providerjsonrpc.Message
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				continue
			}
			var params map[string]any
			_ = json.Unmarshal(msg.Params, &params)
			calls <- rpcCall{method: msg.Method, params: params}
			rpcErr := reply(msg.Method)
			if rpcErr == errNoReply {
				continue
			}
			resp := providerjsonrpc.Message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{"ok":true}`)}
			if rpcErr != nil {
				resp = providerjsonrpc.Message{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr}
			}
			line, _ := json.Marshal(resp)
			_, _ = sidecarWrite.Write(append(line, '\n'))
		}
	}()
	t.Cleanup(func() {
		_ = clientWrite.Close()
		_ = sidecarWrite.Close()
	})
	return client, calls
}

func TestProcSteerTurnNamesTheTurnAndClassifiesFailures(t *testing.T) {
	var steerReply *providerjsonrpc.Error
	client, calls := fakeSidecarClient(t, func(method string) *providerjsonrpc.Error {
		if method == "turn.steer" {
			return steerReply
		}
		return nil
	})
	p := &Proc{client: client}
	if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn-1", Input: session.TextUserInput("work")}, func(testProducerEmission) bool { return true }); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}
	if start := <-calls; start.method != "turn.start" || start.params["turnToken"] != "turn-1" {
		t.Fatalf("turn.start = %+v, want the amux turn token", start)
	}

	steer := func(ctx context.Context, token string) error {
		return p.SteerTurn(ctx, session.TurnSteerRequest{TurnToken: token, Input: session.TextUserInput("use dry-run")})
	}
	if err := steer(context.Background(), "turn-2"); !errors.Is(err, session.ErrSteerUnavailable) {
		t.Fatalf("other turn: %v, want unavailable", err)
	}
	if err := steer(context.Background(), "turn-1"); err != nil {
		t.Fatalf("SteerTurn: %v", err)
	}
	if call := <-calls; call.method != "turn.steer" || call.params["turnToken"] != "turn-1" || call.params["text"] != "use dry-run" {
		t.Fatalf("turn.steer = %+v", call)
	}

	steerReply = &providerjsonrpc.Error{Code: -32000, Message: "no claude turn in flight"}
	if err := steer(context.Background(), "turn-1"); !errors.Is(err, session.ErrSteerUnavailable) || errors.Is(err, session.ErrSteerOutcomeUnknown) {
		t.Fatalf("sidecar rejection: %v, want unavailable", err)
	}
	<-calls

	steerReply = errNoReply
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := steer(ctx, "turn-1"); !errors.Is(err, session.ErrSteerOutcomeUnknown) {
		t.Fatalf("lost reply: %v, want unknown outcome", err)
	}
}
