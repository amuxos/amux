package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

const sessionDiscoveryName = "amux-runtime-session-schedule"

// Hosts without a native append-instructions API discover this entry through
// their declared Skill catalog. Its content is stable across Sessions; only the
// non-secret launch environment carries the current path, transport and role.
const sessionDiscoverySkill = `---
name: amux-runtime-session-schedule
description: Discover Amux session control and scheduled work. Use for creating or managing agent conversations, delayed follow-ups, periodic checks, and schedules.
---

Read only the AMUX_SESSION_SCHEDULE_CONTEXT environment value with your shell
tool. It contains the shared Skill's exact path, your configured MCP/CLI
transport, and your execution role. Read that Skill before session or schedule
operations and follow its role restrictions. If the value is absent, report
that Runtime guidance is unavailable; do not infer control permissions.
`

func (s *server) materializeSessionDiscovery(ctx context.Context, snap session.Snapshot, env []string, placement adapter.Placement) error {
	if !placement.Supported || (placement.Kind != "cwd-rel" && placement.Kind != "home-rel") {
		return nil
	}
	rel := filepath.Clean(placement.Path)
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return os.ErrInvalid
	}
	rel = filepath.Join(rel, sessionDiscoveryName, "SKILL.md")
	if snap.ExecutionIdentity.IsolationBackend == principal.BackendLinuxDAC {
		stage, err := os.MkdirTemp("", "amux-native-skill-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if err := writeSessionDiscovery(filepath.Join(stage, rel)); err != nil {
			return err
		}
		prefix := "workspace"
		if placement.Kind == "home-rel" {
			prefix = "home"
		}
		return s.isolationRuntime.MaterializeExecution(ctx, snap.ExecutionIdentity, snap.LaunchID, snap.Cwd, []isolation.BundleSource{{Source: stage, Prefix: prefix}})
	}
	root := snap.Cwd
	if placement.Kind == "home-rel" {
		root = launchEnvValue(env, "HOME")
		if root == "" {
			var err error
			root, err = os.UserHomeDir()
			if err != nil {
				return err
			}
		}
	}
	return writeSessionDiscovery(filepath.Join(root, rel))
}

func writeSessionDiscovery(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(sessionDiscoverySkill), 0600)
}
