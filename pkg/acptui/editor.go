package acptui

import (
	"sync"
	"time"
	"unicode/utf8"
)

// LineEditor is a line-mode input editor that handles keystroke echoing,
// backspace, and line submission. It buffers input until the user presses
// Enter, then invokes the onLine callback with the complete line.
type LineEditor struct {
	mu       sync.Mutex
	buf      []rune
	prompted bool
	onLine   func([]byte)
}

// NewLineEditor creates a LineEditor that calls onLine when the user submits
// a line (presses Enter). The callback receives the line content with a
// trailing \r.
func NewLineEditor(onLine func([]byte)) *LineEditor {
	return &LineEditor{onLine: onLine}
}

func (e *LineEditor) Text() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return string(e.buf)
}

// Write processes raw input bytes (keystrokes) and returns the echo bytes
// that should be written to the terminal.
func (e *LineEditor) Write(data []byte) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]byte, 0, len(data)+4)
	for i := 0; i < len(data); {
		b := data[i]
		switch b {
		case '\r', '\n':
			out = append(out, '\r', '\n')
			if len(e.buf) > 0 {
				line := append([]byte(string(e.buf)), '\r')
				e.onLine(line)
				e.buf = e.buf[:0]
			}
			e.prompted = false
			i++
		case 0x7f, 0x08:
			if len(e.buf) > 0 {
				r := e.buf[len(e.buf)-1]
				e.buf = e.buf[:len(e.buf)-1]
				for range RuneDisplayWidth(r) {
					out = append(out, '\b', ' ', '\b')
				}
			}
			i++
		default:
			if !e.prompted {
				out = append(out, '>', ' ')
				e.prompted = true
			}
			r, size := utf8.DecodeRune(data[i:])
			if r == utf8.RuneError && size == 1 {
				e.buf = append(e.buf, rune(b))
				out = append(out, b)
				i++
				continue
			}
			e.buf = append(e.buf, r)
			out = append(out, data[i:i+size]...)
			i += size
		}
	}
	return out
}

// DelayedTimer fires a callback after a quiet period. Repeated Schedule
// calls reset the timer.
type DelayedTimer struct {
	mu   sync.Mutex
	t    *time.Timer
	d    time.Duration
	fire func()
}

// NewDelayedTimer creates a DelayedTimer with the given delay and callback.
func NewDelayedTimer(delay time.Duration, fire func()) *DelayedTimer {
	return &DelayedTimer{d: delay, fire: fire}
}

// Schedule (re)starts the timer. If called again before the timer fires,
// the previous timer is cancelled and a new one starts.
func (p *DelayedTimer) Schedule() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.t != nil {
		p.t.Stop()
	}
	p.t = time.AfterFunc(p.d, func() {
		p.fire()
	})
}

// Stop cancels any pending timer.
func (p *DelayedTimer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.t != nil {
		p.t.Stop()
		p.t = nil
	}
}
