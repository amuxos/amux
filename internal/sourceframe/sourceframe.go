package sourceframe

import (
	"bytes"
	"strings"
)

const (
	SourceAgent   = "agent"
	SourceCLI     = "cli"
	SourceDesktop = "desktop"
	SourceLark    = "lark"
	SourceSystem  = "system"
	SourceGoal    = "goal"
)

var (
	larkLabel    = []byte("📱 来自飞书: ")
	cliLabel     = []byte("💻 来自终端: ")
	desktopLabel = []byte("🖥️ 来自桌面: ")
)

type Classification struct {
	Source string
	Input  bool
}

func FormatInput(source, text string) []byte {
	if source == SourceGoal {
		return []byte("\r\n" + text + "\r\n")
	}
	return []byte("\r\n" + label(source) + text + "\r\n")
}

func Classify(data []byte) Classification {
	switch {
	case bytes.Contains(data, larkLabel):
		return Classification{Source: SourceLark, Input: true}
	case bytes.Contains(data, desktopLabel):
		return Classification{Source: SourceDesktop, Input: true}
	case bytes.Contains(data, cliLabel):
		return Classification{Source: SourceCLI, Input: true}
	default:
		return Classification{Source: SourceAgent}
	}
}

// ExtractInput returns the original text from one FormatInput frame.
func ExtractInput(data []byte) (string, string, bool) {
	text := strings.TrimSpace(string(data))
	for source, marker := range map[string]string{
		SourceLark: string(larkLabel), SourceDesktop: string(desktopLabel), SourceCLI: string(cliLabel),
	} {
		if strings.HasPrefix(text, marker) {
			return source, strings.TrimSpace(strings.TrimPrefix(text, marker)), true
		}
	}
	return "", "", false
}

func label(source string) string {
	switch source {
	case SourceLark, "feishu":
		return string(larkLabel)
	case SourceDesktop:
		return string(desktopLabel)
	default:
		return string(cliLabel)
	}
}
