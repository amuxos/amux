package isolation

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

func shellSpec(script string) LaunchSpec {
	if runtime.GOOS == "windows" {
		return LaunchSpec{Path: "cmd.exe", Args: []string{"/c", script}}
	}
	return LaunchSpec{Path: "/bin/sh", Args: []string{"-c", script}}
}

func TestPassthroughWaitIsIdempotent(t *testing.T) {
	h, err := NewPassthroughBackend().Start(context.Background(), shellSpec("exit 7"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	a, errA := h.Wait(context.Background())
	b, errB := h.Wait(context.Background())
	if a.Code != 7 || b != a || (errA == nil) != (errB == nil) {
		t.Fatalf("wait results a=%+v/%v b=%+v/%v", a, errA, b, errB)
	}
}

func TestWaitCancellationDoesNotKill(t *testing.T) {
	h, err := NewPassthroughBackend().Start(context.Background(), shellSpec("sleep 0.2; echo ok"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v", err)
	}
	data, err := io.ReadAll(h.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	status, _ := h.Wait(context.Background())
	if status.Code != 0 || string(data) == "" {
		t.Fatalf("process was killed: status=%+v output=%q", status, data)
	}
}

func TestCloseDoesNotKill(t *testing.T) {
	h, err := NewPassthroughBackend().Start(context.Background(), shellSpec("sleep 0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	status, _ := h.Wait(context.Background())
	if status.Code != 0 {
		t.Fatalf("status = %+v", status)
	}
}
