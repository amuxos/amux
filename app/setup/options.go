package setup

import (
	"fmt"
	"strings"
)

// Options configures the Windows Setup.exe wizard (phase 1).
//
// Defaults mirror scripts/install.sh --feishu:
//   interaction-mode=singleton, feishu-profile=default,
//   session-name=<profile>-main, agent-profile=builtin/<runtime>.
type Options struct {
	SourceDir       string
	InstallBinDir   string
	AmuxHome        string
	Runtime         string
	AgentProfile    string
	Model           string
	FeishuProfile   string
	InteractionMode string
	SessionName     string
	SessionCWD      string
	SkipPATH        bool
	OpenBrowser     bool
}

// Normalize applies install.sh-compatible defaults and validates required fields.
func (o *Options) Normalize() error {
	if o == nil {
		return fmt.Errorf("setup options are required")
	}
	o.Runtime = strings.TrimSpace(o.Runtime)
	o.AgentProfile = strings.TrimSpace(o.AgentProfile)
	o.Model = strings.TrimSpace(o.Model)
	o.FeishuProfile = strings.TrimSpace(o.FeishuProfile)
	o.InteractionMode = strings.TrimSpace(o.InteractionMode)
	o.SessionName = strings.TrimSpace(o.SessionName)
	o.SessionCWD = strings.TrimSpace(o.SessionCWD)
	o.SourceDir = strings.TrimSpace(o.SourceDir)
	o.InstallBinDir = strings.TrimSpace(o.InstallBinDir)
	o.AmuxHome = strings.TrimSpace(o.AmuxHome)

	if o.FeishuProfile == "" {
		o.FeishuProfile = "default"
	}
	if o.InteractionMode == "" {
		o.InteractionMode = "singleton"
	}
	if o.InteractionMode != "singleton" && o.InteractionMode != "thread" {
		return fmt.Errorf("interaction-mode must be singleton or thread")
	}
	if o.SessionName == "" {
		o.SessionName = o.FeishuProfile + "-main"
	}
	switch o.Runtime {
	case "codex", "claude", "pi":
	case "":
		return fmt.Errorf("--runtime is required (codex|claude|pi); install and authenticate the agent first")
	default:
		return fmt.Errorf("unsupported runtime %q", o.Runtime)
	}
	if o.AgentProfile == "" {
		o.AgentProfile = "builtin/" + o.Runtime
	}
	switch o.AgentProfile {
	case "builtin/codex", "builtin/claude", "builtin/pi":
	default:
		return fmt.Errorf("public Setup Profiles are builtin/codex, builtin/claude and builtin/pi")
	}
	return nil
}
