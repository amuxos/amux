package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
)

func TestCodexExecutableUsesExecutionHomeAndPATH(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(bin, "codex-team")
	if err := os.WriteFile(want, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=/wrong", "HOME=" + home, "PATH=" + bin}
	for _, name := range []string{"~/bin/codex-team", "codex-team", want} {
		got, err := resolveCodexExecutable(name, env)
		if err != nil || got != want {
			t.Fatalf("%q => %q, %v", name, got, err)
		}
	}
	for _, name := range []string{"./codex-team", "missing-launcher", "bad\nname"} {
		if _, err := resolveCodexExecutable(name, env); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if got, err := resolveCodexExecutable("", env); err != nil || got != "codex" {
		t.Fatalf("default = %q %v", got, err)
	}
}

func TestCodexExecutableFrozenInSessionLaunch(t *testing.T) {
	testExecutableFrozenInSessionLaunch(t, "codex")
}

func testExecutableFrozenInSessionLaunch(t *testing.T, agent string) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	key := config.AgentExecutableKey(agent)
	old := viper.Get(key)
	defer viper.Set(key, old)
	viper.Set(key, "/configured/launcher")
	srv := &server{mgr: mgr, amuxDir: dir, disableMCPAutoInject: true,
		startSessionContextHook: func(_ context.Context, _ adapter.Adapter, _ *session.Session, _, _ []string) error { return nil }}
	resp, err := srv.Run(context.Background(), &pb.RunRequest{AgentType: agent, Name: "configured", Cwd: dir, AgentExecutable: "/forwarded/launcher"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := mgr.Get(resp.Session.Name)
	if err != nil {
		t.Fatal(err)
	}
	snap := sess.Snapshot()
	if got := launchSpecExecutable(snap.LaunchSpec); got != "/forwarded/launcher" {
		t.Fatalf("launch = %q", got)
	}
	viper.Set(key, "/changed/launcher")
	resumed := runRequestFromLaunchSnapshot(snap)
	if resumed.AgentExecutable != "/forwarded/launcher" {
		t.Fatalf("resume changed launcher: %+v", resumed)
	}
	if launchSpecExecutable(`{"agent_type":"codex"}`) != "codex" {
		t.Fatal("legacy session adopted configured launcher")
	}
}

func TestRegisteredExecutableFrozenInSessionLaunch(t *testing.T) {
	const child = "AMUX_TEST_RUNTIME_EXECUTABLE"
	if os.Getenv(child) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRegisteredExecutableFrozenInSessionLaunch$")
		cmd.Env = append(os.Environ(), child+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return
	}
	if err := config.RegisterExtension(config.Extension{AgentExecutables: map[string]string{"custom": "Custom"}}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Register("custom", func() adapter.Adapter { return &adapter.CodexAdapter{} }); err != nil {
		t.Fatal(err)
	}
	testExecutableFrozenInSessionLaunch(t, "custom")
	if got := launchSpecExecutable("", "custom"); got != "custom" {
		t.Fatalf("legacy launcher: %s", got)
	}
}
