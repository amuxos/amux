package replay

import "sync"

const DefaultCap = 1000

// Buffer is a thread-safe ring buffer that stores the most recent N byte slices.
type Buffer struct {
	mu      sync.Mutex
	entries [][]byte
	head    int // index of the oldest entry (next write position)
	count   int // number of valid entries
}

func New(cap int) *Buffer {
	return &Buffer{entries: make([][]byte, cap)}
}

func (b *Buffer) cap() int { return len(b.entries) }

// Append adds data to the buffer, overwriting the oldest entry when full.
func (b *Buffer) Append(data []byte) {
	if len(data) == 0 {
		return
	}
	cp := make([]byte, len(data))
	copy(cp, data)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.entries[b.head] = cp
	b.head = (b.head + 1) % b.cap()
	if b.count < b.cap() {
		b.count++
	}
}

// Snapshot returns a copy of all entries in order (oldest first).
func (b *Buffer) Snapshot() [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		return nil
	}
	out := make([][]byte, b.count)
	start := (b.head - b.count + b.cap()) % b.cap()
	for i := 0; i < b.count; i++ {
		src := b.entries[(start+i)%b.cap()]
		cp := make([]byte, len(src))
		copy(cp, src)
		out[i] = cp
	}
	return out
}
