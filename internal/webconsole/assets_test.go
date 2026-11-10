package webconsole

import (
	"os/exec"
	"testing"
)

// Keep the dependency-free browser contract suite in the ordinary Go gate.
func TestBrowserAssetContracts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	cmd := exec.Command(node, "--test", "assets/tests/unit.test.cjs", "assets/tests/dom.test.cjs", "assets/tests/transition.test.cjs")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser contract tests: %v\n%s", err, out)
	}
}
