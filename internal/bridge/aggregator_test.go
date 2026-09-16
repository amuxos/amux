package bridge

import (
	"strings"
	"testing"
	"time"
)

func TestStripANSI(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"\x1b[0mhello\x1b[m", "hello"},
		{"\x1b[32mgreen\x1b[0m text", "green text"},
		{"\x1b]0;title\x07prompt", "prompt"},
		{"no escape", "no escape"},
		{"\r\nhello\r\nworld", "\nhello\nworld"},
		{"\x1b[2J\x1b[H", ""}, // erase + cursor home
	}
	for _, c := range cases {
		got := stripANSI(c.in)
		if got != c.want {
			t.Errorf("stripANSI(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestAggregator_Flush(t *testing.T) {
	var got []string
	agg := NewAggregator(50*time.Millisecond, 200, func(text string) error {
		got = append(got, text)
		return nil
	})

	agg.Add([]byte("hello "))
	agg.Add([]byte("world"))

	// Wait for timer flush.
	time.Sleep(120 * time.Millisecond)

	if len(got) == 0 {
		t.Fatal("expected at least one flush, got none")
	}
	combined := strings.Join(got, "")
	if !strings.Contains(combined, "hello world") {
		t.Errorf("flushed text %q does not contain 'hello world'", combined)
	}
}

func TestAggregator_MaxBytes(t *testing.T) {
	var got []string
	agg := NewAggregator(10*time.Second, 5, func(text string) error {
		got = append(got, text)
		return nil
	})

	agg.Add([]byte("abcde")) // exactly maxBytes → immediate flush
	time.Sleep(20 * time.Millisecond)

	if len(got) == 0 {
		t.Fatal("expected immediate flush on maxBytes, got none")
	}
}

func TestAggregator_TUIMode(t *testing.T) {
	// Simulate a TUI app that:
	//  1. enters alternate screen
	//  2. writes "Hello TUI!" at a fixed position
	//  3. exits alternate screen
	// The aggregator should render the visible screen rather than the raw bytes.
	var got []string
	agg := NewAggregator(50*time.Millisecond, 1<<20, func(text string) error {
		got = append(got, text)
		return nil
	})

	// Build a minimal TUI sequence: enter alt-screen, position cursor, write text.
	tui := "\x1b[?1049h" // enter alt-screen (triggers TUI detection)
	tui += "\x1b[1;1H"   // cursor to row 1, col 1
	tui += "Hello TUI!"  // write text
	agg.Add([]byte(tui))

	// Wait for timer flush.
	time.Sleep(120 * time.Millisecond)

	if len(got) == 0 {
		t.Fatal("expected at least one flush in TUI mode, got none")
	}
	combined := strings.Join(got, "\n")
	if !strings.Contains(combined, "Hello TUI!") {
		t.Errorf("TUI flush %q does not contain 'Hello TUI!'", combined)
	}
}

func TestAggregator_TUIMode_OSCRemnants(t *testing.T) {
	// Simulate a TUI app that emits OSC 10/11 colour-set sequences.
	// vt10x does not handle OSC 10/11, so it may write the parameter bytes as
	// literal cell text ("]11;rgb:1e1e/1e1e/2e2e\").  The aggregator should
	// strip those remnants so they don't appear in the Feishu message.
	var got []string
	agg := NewAggregator(50*time.Millisecond, 1<<20, func(text string) error {
		got = append(got, text)
		return nil
	})

	// Enter TUI mode, write real content, then emit OSC 11 colour sequence.
	tui := "\x1b[?1049h"                      // enter alt-screen
	tui += "\x1b[1;1H"                        // cursor home
	tui += "Hello TUI!"                       // visible content
	tui += "\x1b[10;1H"                       // move to line 10
	tui += "> "                               // prompt prefix
	tui += "\x1b]11;rgb:1e1e/1e1e/2e2e\x07"   // OSC 11 (BEL-terminated)
	tui += "\x1b]11;rgb:1e1e/1e1e/2e2e\x1b\\" // OSC 11 (ST-terminated)
	agg.Add([]byte(tui))

	time.Sleep(120 * time.Millisecond)

	if len(got) == 0 {
		t.Fatal("expected at least one flush, got none")
	}
	combined := strings.Join(got, "\n")
	if !strings.Contains(combined, "Hello TUI!") {
		t.Errorf("TUI flush %q does not contain 'Hello TUI!'", combined)
	}
	// OSC remnants must not appear in output.
	if strings.Contains(combined, "rgb:") || strings.Contains(combined, "]11;") {
		t.Errorf("TUI flush %q still contains OSC remnants", combined)
	}
}

func TestAggregator_SkipsEmptyAfterStrip(t *testing.T) {
	flushed := false
	agg := NewAggregator(50*time.Millisecond, 200, func(text string) error {
		flushed = true
		return nil
	})

	agg.Add([]byte("\x1b[2J\x1b[H\r\n")) // ANSI noise → empty after strip
	time.Sleep(120 * time.Millisecond)

	if flushed {
		t.Error("expected no flush for ANSI-only content")
	}
}
