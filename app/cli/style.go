package cli

import (
	"io"
	"os"
	"strings"

	"github.com/fatih/color"
	"golang.org/x/term"
)

type cliStyle struct {
	color bool
}

func styleFor(out io.Writer) cliStyle {
	if os.Getenv("NO_COLOR") != "" {
		return cliStyle{}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AMUX_COLOR"))) {
	case "always", "1", "true", "yes", "on":
		return cliStyle{color: true}
	case "never", "0", "false", "no", "off":
		return cliStyle{}
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return cliStyle{}
	}
	f, ok := out.(*os.File)
	return cliStyle{color: ok && term.IsTerminal(int(f.Fd()))}
}

func (s cliStyle) paint(text string, attrs ...color.Attribute) string {
	c := color.New(attrs...)
	if s.color {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c.Sprint(text)
}

func (s cliStyle) heading(text string) string {
	return s.paint(text, color.Bold)
}

func (s cliStyle) ok(text string) string {
	return s.paint(text, color.FgGreen)
}

func (s cliStyle) warn(text string) string {
	return s.paint(text, color.FgYellow)
}

func (s cliStyle) fail(text string) string {
	return s.paint(text, color.FgRed)
}

func (s cliStyle) state(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running", "reachable", "active", "ok", "exists", "configured", "yes", "true", "healthy", "available", "pass", "succeeded", "enabled":
		return s.ok(value)
	case "not running", "stale pid", "missing", "not configured", "not ok", "no", "false", "unavailable", "degraded", "stopped", "pending", "draining", "staging", "applying", "verifying", "disabled", "rolled_back":
		return s.warn(value)
	case "error", "failed", "fail", "cancelled":
		return s.fail(value)
	default:
		return value
	}
}
