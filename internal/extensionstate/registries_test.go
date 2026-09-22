package extensionstate_test

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/amuxos/amux/app/cli"
	"github.com/amuxos/amux/app/runtime"
	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/distribution"
	"github.com/amuxos/amux/internal/extensionstate"
	"github.com/amuxos/amux/internal/mux"
	"github.com/amuxos/amux/internal/provider/codex"
)

func TestFreezeClosesAllRegistries(t *testing.T) {
	if os.Getenv("AMUX_TEST_FREEZE_REGISTRIES") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFreezeClosesAllRegistries$")
		cmd.Env = append(os.Environ(), "AMUX_TEST_FREEZE_REGISTRIES=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("registry freeze subprocess: %v\n%s", err, output)
		}
		return
	}
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Get("codex"); err != nil {
		release()
		t.Fatal(err)
	}
	factory := func() adapter.Adapter { return &adapter.CodexAdapter{} }
	if err := adapter.Register("freeze-test", factory); err != nil {
		release()
		t.Fatal(err)
	}
	if err := adapter.Register("freeze-test", factory); err == nil {
		release()
		t.Fatal("duplicate startup registration was accepted")
	}
	release()
	extensionstate.Freeze()
	registries := map[string]func() error{
		"adapter": func() error { return adapter.Register("late-test", factory) },
		"protocol": func() error {
			return adapter.RegisterProtocol("late-test", adapter.ProtocolRegistration{ProviderID: "late-test"})
		},
		"app-server":     func() error { return codex.RegisterProvider("late-test", codex.ProviderRegistration{}) },
		"responses-chat": func() error { return mux.RegisterResponsesChatCommand(nil) },
		"structured":     func() error { return mux.RegisterStructuredProvider("late-test", nil) },
		"config":         func() error { return config.RegisterExtension(config.Extension{}) },
		"distribution":   func() error { return distribution.Register(distribution.Options{}) },
		"profiles":       func() error { return agentprofile.RegisterCustomization(agentprofile.Customization{}) },
		"bridge":         func() error { return bridge.RegisterExtension(bridge.Extension{}) },
		"CLI":            func() error { return cli.RegisterHooks(cli.Hooks{}) },
		"runtime":        func() error { return runtime.RegisterHooks(runtime.Hooks{}) },
	}
	for name, register := range registries {
		t.Run(name, func(t *testing.T) {
			if err := register(); !errors.Is(err, extensionstate.ErrFrozen) {
				t.Fatalf("late registration = %v", err)
			}
		})
	}
	if _, err := adapter.Get("freeze-test"); err != nil {
		t.Fatalf("Freeze changed registered adapters: %v", err)
	}
}
