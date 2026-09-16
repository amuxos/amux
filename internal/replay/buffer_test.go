package replay

import (
	"sync"
	"testing"
)

func TestBufferBasic(t *testing.T) {
	b := New(3)
	b.Append([]byte("a"))
	b.Append([]byte("b"))
	b.Append([]byte("c"))

	snap := b.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("want 3, got %d", len(snap))
	}
	for i, want := range []string{"a", "b", "c"} {
		if string(snap[i]) != want {
			t.Errorf("[%d] want %q, got %q", i, want, snap[i])
		}
	}
}

func TestBufferWrap(t *testing.T) {
	b := New(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		b.Append([]byte(s))
	}
	snap := b.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("want 3, got %d", len(snap))
	}
	for i, want := range []string{"c", "d", "e"} {
		if string(snap[i]) != want {
			t.Errorf("[%d] want %q, got %q", i, want, snap[i])
		}
	}
}

func TestBufferEmpty(t *testing.T) {
	b := New(10)
	if snap := b.Snapshot(); snap != nil {
		t.Fatalf("expected nil, got %v", snap)
	}
}

func TestBufferConcurrent(t *testing.T) {
	b := New(100)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Append([]byte("x"))
			_ = b.Snapshot()
		}()
	}
	wg.Wait()
}
