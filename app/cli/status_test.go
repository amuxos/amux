package cli

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestInspectConfiguredHubShortCircuitsStoppedLocalHub(t *testing.T) {
	t.Parallel()
	addr := freeStatusTCPAddr(t)
	report := cliStatusReport{Hub: serviceReport{State: "not running", Addr: addr}}
	start := time.Now()
	hc, err := inspectConfiguredHub(context.Background(), &report, addr)
	if hc != nil {
		_ = hc.Close()
		t.Fatal("expected nil hub client for stopped local hub")
	}
	if err == nil {
		t.Fatal("expected unavailable error for stopped local hub")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("inspectConfiguredHub blocked for %s on unbound local addr %s", elapsed, addr)
	}
	if report.Hub.State != "configured" {
		t.Fatalf("Hub.State = %q, want configured", report.Hub.State)
	}
	for _, want := range []string{"hub is not reachable at " + addr, "amux system status", "amux system start"} {
		if !strings.Contains(report.Hub.Error, want) {
			t.Fatalf("Hub.Error = %q, missing %q", report.Hub.Error, want)
		}
	}
}

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
