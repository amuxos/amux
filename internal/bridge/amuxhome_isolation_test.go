package bridge

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/config"
)

const amuxHomeIsolationHelperEnv = "AMUX_TEST_AMUX_HOME_ISOLATION_HELPER"

// TestPackageTestsNeverWriteInheritedAMUXHome reproduces running `go test`
// inside an amux-managed Agent Session: the test binary inherits AMUX_HOME
// pointing at a real amux home, and a test saves Feishu secrets and config
// without isolating AMUX_HOME itself. TestMain must redirect those writes.
func TestPackageTestsNeverWriteInheritedAMUXHome(t *testing.T) {
	sentinel := t.TempDir()
	secretsPath := filepath.Join(sentinel, "secrets", "feishu", DefaultProfileName+".json")
	configPath := filepath.Join(sentinel, "config.toml")
	files := map[string][]byte{
		secretsPath: []byte(`{"app_id":"cli_real","app_secret":"real-secret","open_id":"ou_real"}` + "\n"),
		configPath:  []byte("[bridge.feishu.profiles.default.interaction]\nmode = \"singleton\"\n"),
	}
	for path, data := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestAMUXHomeIsolationHelperProcess$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(),
		amuxHomeIsolationHelperEnv+"=1",
		"AMUX_HOME="+sentinel,
		"HOME="+t.TempDir(),
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "isolated-amux-home-ok") {
		t.Fatalf("helper process did not run its writes:\n%s", out.String())
	}
	for path, want := range files {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read sentinel %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("test run modified inherited AMUX_HOME file %s:\n%s", path, got)
		}
	}
}

// TestAMUXHomeIsolationHelperProcess runs only as the child of
// TestPackageTestsNeverWriteInheritedAMUXHome. It deliberately does not call
// t.Setenv for HOME or AMUX_HOME, like a test helper that forgot to.
func TestAMUXHomeIsolationHelperProcess(t *testing.T) {
	t.Parallel()
	if os.Getenv(amuxHomeIsolationHelperEnv) != "1" {
		t.Skip("helper process for TestPackageTestsNeverWriteInheritedAMUXHome")
	}
	if err := SaveProfileSecrets(DefaultProfileName, &Secrets{OpenId: "owner-open"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	if err := config.SetBridgeFeishuInteractionMode(DefaultProfileName, config.FeishuInteractionThread); err != nil {
		t.Fatalf("SetBridgeFeishuInteractionMode: %v", err)
	}
	path, err := ProfileSecretsPath(DefaultProfileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("secrets were not written to the isolated home: %v", err)
	}
	if _, err := os.Stat(amuxhome.ConfigPath()); err != nil {
		t.Fatalf("config was not written to the isolated home: %v", err)
	}
	t.Log("isolated-amux-home-ok")
}
