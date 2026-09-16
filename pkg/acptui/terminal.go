package acptui

import "bytes"

// TermReset undoes terminal modes a TUI agent may have set on the local
// terminal via its output stream.
const TermReset = "\033[!p" + // DECSTR: soft terminal reset
	"\033[?1049l" + // exit alternate screen
	"\033[>4;0m" + // disable Kitty keyboard protocol
	"\033[?25h" + // show cursor
	"\033[0m\r\n" // reset attributes, move to new line

// KittyReset disables the Kitty keyboard protocol that TUI agents may enable
// on the local terminal. Without this, Ghostty (and other Kitty-capable
// terminals) encode Ctrl-] as \e[93;5u instead of raw 0x1d.
const KittyReset = "\033[>4;0m"

// kittyCtrlBracket is the Kitty keyboard protocol encoding of Ctrl-]:
// CSI 93 ; 5 u  (codepoint 93 = ']', modifier 5 = Ctrl).
var kittyCtrlBracket = []byte{0x1b, 0x5b, 0x39, 0x33, 0x3b, 0x35, 0x75}

// IsDetach reports whether buf contains the detach sequence.
// Handles both raw 0x1d (standard) and \e[93;5u (Kitty keyboard protocol).
func IsDetach(buf []byte) bool {
	for i := 0; i < len(buf); i++ {
		if buf[i] == 0x1d {
			return true
		}
		if i+len(kittyCtrlBracket) <= len(buf) &&
			string(buf[i:i+len(kittyCtrlBracket)]) == string(kittyCtrlBracket) {
			return true
		}
	}
	return false
}

// FilterTerminalResponses strips terminal response sequences (OSC replies,
// cursor position reports) from the input buffer, returning only user input.
func FilterTerminalResponses(buf []byte) []byte {
	out := make([]byte, 0, len(buf))
	for i := 0; i < len(buf); {
		if buf[i] != 0x1b {
			out = append(out, buf[i])
			i++
			continue
		}

		// OSC terminal responses, e.g. ESC ] 10;rgb:... BEL or ESC \.
		if i+2 < len(buf) && buf[i+1] == ']' {
			j := i + 2
			for j < len(buf) {
				if buf[j] == 0x07 {
					j++
					break
				}
				if buf[j] == 0x1b && j+1 < len(buf) && buf[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			if j <= len(buf) {
				i = j
				continue
			}
		}

		// Cursor position report response, e.g. ESC [ 24 ; 80 R.
		if i+2 < len(buf) && buf[i+1] == '[' {
			j := i + 2
			for j < len(buf) && ((buf[j] >= '0' && buf[j] <= '9') || buf[j] == ';') {
				j++
			}
			if j < len(buf) && buf[j] == 'R' {
				i = j + 1
				continue
			}
		}

		out = append(out, buf[i])
		i++
	}
	return out
}

// NormalizeLineModeOutput ensures all newlines in data are \r\n for proper
// terminal display in raw mode.
func NormalizeLineModeOutput(data []byte) []byte {
	if !bytes.Contains(data, []byte{'\n'}) {
		return data
	}
	out := make([]byte, 0, len(data)+bytes.Count(data, []byte{'\n'}))
	for i, b := range data {
		if b == '\n' && (i == 0 || data[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, b)
	}
	return out
}
