package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestHubUnavailableErrorIsActionable(t *testing.T) {
	err := hubUnavailableError("127.0.0.1:17999", context.DeadlineExceeded)
	got := err.Error()

	for _, want := range []string{
		"hub is not reachable at 127.0.0.1:17999",
		"context deadline exceeded",
		"amux system status",
		"amux system start",
		"hub.addr",
		"AMUX_HUB_ADDR",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("hub unavailable error = %q, want substring %q", got, want)
		}
	}
	for _, notWant := range []string{"amux hub start", "runtime.sock", "fallback"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("hub unavailable error = %q, must not contain %q", got, notWant)
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hub unavailable error lost cause: %v", err)
	}
}

func TestTaskAndScheduleClientsUseCanonicalHubRecoveryError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	addr := freeStatusTCPAddr(t)
	t.Setenv("AMUX_HUB_ADDR", addr)
	viper.Reset()
	t.Cleanup(viper.Reset)

	tests := []struct {
		name string
		dial func() error
	}{
		{
			name: "task",
			dial: func() error {
				_, _, err := newTaskClientWithTimeout(50 * time.Millisecond)
				return err
			},
		},
		{
			name: "schedule",
			dial: func() error {
				_, _, err := newAmuxdClientWithTimeout(50 * time.Millisecond)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.dial()
			if err == nil {
				t.Fatal("dial error = nil, want unavailable hub")
			}
			got := err.Error()
			for _, want := range []string{addr, "amux system status", "amux system start", "hub.addr", "AMUX_HUB_ADDR"} {
				if !strings.Contains(got, want) {
					t.Fatalf("dial error = %q, want %q", got, want)
				}
			}
			if strings.Contains(got, "amux hub start") {
				t.Fatalf("dial error = %q, must not use legacy recovery", got)
			}
		})
	}
}
