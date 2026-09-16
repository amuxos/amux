package bridge

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hinshun/vt10x"
)

// ansiRe matches common ANSI/VT100 escape sequences in legacy raw output.
// OSC must come before the single-byte C1 pattern because C1 includes ']'.
var ansiRe = regexp.MustCompile(
	`\x1b(?:` +
		`\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC: ESC ] ... BEL or ST (first!)
		`|\[[0-9;?]*[A-Za-z]` + // CSI: ESC [ ... letter
		`|[@-Z\\-_]` + // C1: ESC + single byte (fallback)
		`|\(.[^\x1b]*` + // charset designation
		`)`,
)

// oscRemnantRe matches OSC sequence content that vt10x may write as literal
// cell text when it encounters OSC sequences it does not handle (e.g. OSC 10/11
// terminal colour set/query).  After the emulator consumes the leading ESC,
// the remainder "]11;rgb:…\" ends up in the cell buffer as plain characters.
// Pattern: ] + digits + ; + non-] content + optional trailing backslash.
var oscRemnantRe = regexp.MustCompile(`\]\d+;[^]\n]*\\?`)

// tuiRe detects TUI-style output: cursor positioning, clear-screen, or
// alternate-screen sequences.  Plain colour/style codes are not TUI.
var tuiRe = regexp.MustCompile(
	`\x1b\[` +
		`(?:\d*;\d*H` + // cursor position ESC[row;colH
		`|\d*H` + // cursor home ESC[H or ESC[nH
		`|2J` + // erase display
		`|\?1049[hl]` + // alt-screen on/off
		`|\d*[ABCDK])`, // cursor movement / erase-line
)

// vtCols / vtRows is the virtual terminal size for TUI rendering.
const (
	vtCols = 220
	vtRows = 50
)

// stripANSI removes ANSI escape sequences and normalises line endings.
func stripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// screenText reads the current visible screen from a VT terminal and returns
// it as plain text with trailing spaces trimmed per line.
func screenText(term vt10x.Terminal) string {
	term.Lock()
	defer term.Unlock()

	_, rows := term.Size()
	var sb strings.Builder
	for y := 0; y < rows; y++ {
		var line strings.Builder
		for x := 0; x < vtCols; x++ {
			ch := term.Cell(x, y).Char
			if ch == 0 {
				ch = ' '
			}
			line.WriteRune(ch)
		}
		sb.WriteString(strings.TrimRight(line.String(), " \t"))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// keepLines removes blank lines and trims surrounding whitespace.
func keepLines(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.IndexFunc(l, func(r rune) bool {
			return unicode.IsPrint(r) && r != ' '
		}) >= 0 {
			kept = append(kept, l)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// Aggregator buffers raw output bytes and flushes readable text to Lark in
// batches to avoid spamming one message per byte.
//
// Two rendering paths:
//   - Plain text (default): accumulate bytes, strip ANSI on flush.
//   - TUI mode (auto-detected): maintain a persistent VT100 terminal;
//     on flush dump the current visible screen.  Activated the first
//     time cursor-positioning or clear-screen sequences are seen.
type Aggregator struct {
	flush    func(text string) error
	interval time.Duration
	maxBytes int

	mu     sync.Mutex
	buf    bytes.Buffer   // plain-text accumulator (non-TUI path)
	term   vt10x.Terminal // persistent VT emulator (TUI path)
	isTUI  bool           // latched once TUI sequences detected
	timer  *time.Timer
	closed bool
}

// NewAggregator creates a new Aggregator.
func NewAggregator(interval time.Duration, maxBytes int, flush func(text string) error) *Aggregator {
	return &Aggregator{
		flush:    flush,
		interval: interval,
		maxBytes: maxBytes,
		term:     vt10x.New(vt10x.WithSize(vtCols, vtRows)),
	}
}

// Add buffers incoming raw data and schedules a flush.
func (a *Aggregator) Add(data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || len(data) == 0 {
		return
	}
	// Detect TUI once; latch for the lifetime of the session.
	if !a.isTUI && tuiRe.Match(data) {
		a.isTUI = true
	}
	if a.isTUI {
		// Feed into the VT emulator so it tracks the current screen state.
		_, _ = a.term.Write(data)
	} else {
		a.buf.Write(data)
	}
	size := a.buf.Len() // for non-TUI flush threshold
	if a.isTUI {
		size = len(data) // always flush on next tick for TUI
	}
	if size >= a.maxBytes {
		a.doFlush()
		return
	}
	if a.timer == nil {
		a.timer = time.AfterFunc(a.interval, func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.doFlush()
		})
	}
}

// Flush forces an immediate flush of any buffered data without closing the
// aggregator. Safe to call from any goroutine.
func (a *Aggregator) Flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.doFlush()
	}
}

// Close flushes any remaining data and stops the timer.
func (a *Aggregator) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	a.doFlush()
}

// doFlush must be called with a.mu held.
func (a *Aggregator) doFlush() {
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}

	text := a.textLocked()
	if text == "" {
		return
	}
	fn := a.flush
	go func() { _ = fn(text) }()
}

func (a *Aggregator) textLocked() string {
	var raw string
	if a.isTUI {
		// Dump the current visible VT screen (no buffer to reset).
		raw = screenText(a.term)
		// vt10x may not handle all OSC sequences (e.g. OSC 10/11 colour set);
		// it consumes the leading ESC but writes the rest ("]11;rgb:…\") as
		// literal cell characters.  Strip residual ESC sequences first, then
		// strip OSC parameter remnants that lack the leading ESC.
		raw = stripANSI(raw)
		raw = oscRemnantRe.ReplaceAllString(raw, "")
	} else {
		if a.buf.Len() == 0 {
			return ""
		}
		raw = a.buf.String()
		a.buf.Reset()
		raw = stripANSI(raw)
	}

	text := keepLines(raw)
	return text
}
