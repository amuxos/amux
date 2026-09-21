package mux

import (
	"errors"
	"reflect"
	"testing"

	"github.com/amuxos/amux/internal/distribution"
)

func TestResponsesChatCommandRegistrationAndLaunch(t *testing.T) {
	previous := responsesChatCommand
	supported := distribution.SupportsResponsesChat
	responsesChatCommand = nil
	distribution.SupportsResponsesChat = true
	t.Cleanup(func() { responsesChatCommand = previous; distribution.SupportsResponsesChat = supported })
	opts := StructuredLaunchOptions{Provider: "codex-app-server", ExtraArgs: []string{"agent-argument"}, ResponsesChat: &ResponsesChatOptions{UpstreamBaseURL: "https://example.invalid", APIKeyEnv: "EXAMPLE_KEY"}}
	if _, _, err := StartStructuredProc(opts); err == nil {
		t.Fatal("gateway without a command builder was admitted")
	}
	if err := RegisterResponsesChatCommand(nil); err == nil {
		t.Fatal("nil builder was accepted")
	}
	wantErr := errors.New("command construction failed")
	if err := RegisterResponsesChatCommand(func(options ResponsesChatOptions, args []string) ([]string, error) {
		if options != *opts.ResponsesChat || !reflect.DeepEqual(args, opts.ExtraArgs) {
			t.Fatalf("lost command inputs: %+v, %v", options, args)
		}
		args[0] = "changed"
		return nil, wantErr
	}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterResponsesChatCommand(func(ResponsesChatOptions, []string) ([]string, error) { return nil, nil }); err == nil {
		t.Fatal("duplicate builder was accepted")
	}
	if _, _, err := StartStructuredProc(opts); !errors.Is(err, wantErr) {
		t.Fatalf("builder error was not propagated before launch: %v", err)
	}
	if opts.ExtraArgs[0] != "agent-argument" {
		t.Fatal("builder mutated caller arguments")
	}
}
