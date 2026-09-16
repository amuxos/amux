package mux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A broker that dies during provider startup used to be indistinguishable from
// a slow one: the launch waited out the full pid-file timeout and reported a
// timeout, burying the reason the provider never came up.
func TestLaunchStructuredContextSurfacesEarlyBrokerExit(t *testing.T) {
	amuxDir := t.TempDir()
	fakeAmuxlet := filepath.Join(t.TempDir(), "amuxlet")
	script := "#!/bin/sh\necho 'permission denied' >&2\nexit 126\n"
	if err := os.WriteFile(fakeAmuxlet, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake amuxlet: %v", err)
	}

	started := time.Now()
	_, _, err := LaunchStructuredContext(context.Background(), fakeAmuxlet, "sess-early-exit", amuxDir,
		StructuredLaunchOptions{Provider: "example-provider", SessionID: "sess-early-exit", Cwd: amuxDir})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected the launch to fail")
	}
	if elapsed > 15*time.Second {
		t.Fatalf("launch waited %s for a broker that had already exited", elapsed)
	}
	for _, want := range []string{"exited", "permission denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
