package cli

import (
	"net"
	"strings"
	"testing"
)

func TestStaleLocalHubNoListenerError(t *testing.T) {
	addr := freeStatusTCPAddr(t)
	errText, ok := staleLocalHubNoListenerError(serviceReport{State: "stale pid"}, addr)
	if !ok {
		t.Fatalf("stale local hub with free addr should short-circuit")
	}
	for _, want := range []string{"hub pid file is stale", addr, "amux system status", "amux system start"} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error %q missing %q", errText, want)
		}
	}
	for _, notWant := range []string{"amux hub stop", "amux hub start", "runtime.sock", "fallback"} {
		if strings.Contains(errText, notWant) {
			t.Fatalf("error %q must not contain %q", errText, notWant)
		}
	}
}

func TestStaleLocalHubWithListenerDoesNotShortCircuit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if errText, ok := staleLocalHubNoListenerError(serviceReport{State: "stale pid"}, ln.Addr().String()); ok {
		t.Fatalf("stale local hub with active listener should not short-circuit: %s", errText)
	}
}

func freeStatusTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}
