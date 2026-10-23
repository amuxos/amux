package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUnacknowledgedConfigurationTimeoutRetiresProcess(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "requests.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=^TestACPDelayedConfigAgent$")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP_DELAYED_CONFIG=1", "AMUX_FAKE_ACP_DELAYED_CAPTURE="+capture)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{
		Cwd: t.TempDir(), Model: "model-a",
		ExecutionConfig: func(model, _ string) (map[string]string, error) { return map[string]string{"model": model}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Close() })
	proc.configTimeout = 20 * time.Millisecond
	err = proc.ReconfigureExecution("model-b", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reconfigure error=%v, want deadline exceeded", err)
	}
	for name, barrier := range map[string]<-chan struct{}{"agent": proc.done, "reader": proc.readDone} {
		select {
		case <-barrier:
		default:
			t.Fatalf("unacknowledged setter left %s alive for a late mutation", name)
		}
	}
	if proc.EffectiveModel() != "model-a" {
		t.Fatalf("unconfirmed native model published as %q", proc.EffectiveModel())
	}
	var setters int
	for _, request := range configRequests(t, capture) {
		if request["method"] != "session/set_config_option" {
			continue
		}
		setters++
		if request["params"].(map[string]any)["value"] != "model-b" {
			t.Fatal("rollback was sent while the original write still lacked a completion barrier")
		}
	}
	if setters != 1 {
		t.Fatalf("setter count=%d, want the one unacknowledged model change", setters)
	}
}

func TestACPDelayedConfigAgent(t *testing.T) {
	if os.Getenv("AMUX_FAKE_ACP_DELAYED_CONFIG") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		f, err := os.OpenFile(os.Getenv("AMUX_FAKE_ACP_DELAYED_CAPTURE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = f.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		_ = f.Close()
		var req rpcMessage
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
		case "session/new":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"delayed-config","configOptions":[{"id":"model","type":"select","currentValue":"model-a","options":[{"value":"model-a"},{"value":"model-b"}]}]}}`+"\n", req.ID)
		case "session/set_config_option":
			// A late original mutation can overtake a rollback. The client must
			// retire this process instead of trusting a newer setter's response.
			id := req.ID
			go func() {
				time.Sleep(time.Second)
				fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"configOptions":[{"id":"model","type":"select","currentValue":"model-b","options":[{"value":"model-a"},{"value":"model-b"}]}]}}`+"\n", id)
			}()
		}
	}
	os.Exit(0)
}
