package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/amuxos/amux/internal/clientop"
)

var scheduleFlagMappings = map[string]string{
	"extra_args":               "arg",
	"timeout_seconds":          "timeout",
	"workspace.policy":         "workspace-policy",
	"workspace.local_dir":      "local-dir",
	"workspace.repo.kind":      "repo-kind",
	"workspace.repo.remote":    "repo-remote",
	"workspace.repo.cache_key": "repo-cache-key",
	"workspace.repo.commit":    "repo-commit",
	"workspace.repo.subdir":    "repo-subdir",
}

// TestScheduleCreateTaskFlagsCoverRequestStruct prevents a shared request field
// from silently becoming impossible to set through the CLI.
func TestScheduleCreateTaskFlagsCoverRequestStruct(t *testing.T) {
	cmd := clientopCoverageFindSubcommand(t, newScheduleCmd(), "create-task")
	clientopCoverageCheckFields(t, reflect.TypeOf(clientop.ScheduledTaskSpec{}), "", cmd)
}

func clientopCoverageCheckFields(t *testing.T, typ reflect.Type, prefix string, cmd *cobra.Command) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		fieldType := field.Type
		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() == reflect.Struct {
			clientopCoverageCheckFields(t, field.Type, path, cmd)
			continue
		}
		flagName, ok := scheduleFlagMappings[path]
		if !ok {
			flagName = strings.ReplaceAll(name, "_", "-")
		}
		if cmd.Flags().Lookup(flagName) == nil {
			t.Errorf("ScheduledTaskSpec.%s has no --%s flag on `amux schedule create-task` and is not covered (path %q)", field.Name, flagName, path)
		}
	}
}

// TestScheduleActionsRegisteredOnBothSurfaces keeps schedule capabilities
// aligned without prematurely requiring P4 schedule update or prompt parity.
func TestScheduleActionsRegisteredOnBothSurfaces(t *testing.T) {
	for _, action := range []struct {
		cli string
		mcp string
	}{
		{"create-task", "amux.schedule_create"},
		{"create", "amux.schedule_create"},
		{"list", "amux.schedule_list"},
		{"runs", "amux.schedule_get"},
		{"remove", "amux.schedule_delete"},
		{"enable", "amux.schedule_set_enabled"},
		{"disable", "amux.schedule_set_enabled"},
		{"run", "amux.schedule_trigger"},
	} {
		t.Run(action.cli, func(t *testing.T) {
			if clientopCoverageFindSubcommandOrNil(newScheduleCmd(), action.cli) == nil {
				t.Errorf("CLI has no `amux schedule %s`", action.cli)
			}
			if !clientopCoverageMCPToolRegistered(action.mcp) {
				t.Errorf("MCP has no tool %q", action.mcp)
			}
		})
	}
}

// TestSessionActionsRegisteredOnBothSurfaces keeps the session control plane
// aligned across the CLI and MCP surfaces: every session action an agent can
// reach through an amux.session_* tool must also exist as a CLI subcommand
// under `amux session`, and vice versa.
func TestSessionActionsRegisteredOnBothSurfaces(t *testing.T) {
	for _, action := range []struct {
		cli []string
		mcp string
	}{
		{[]string{"list"}, "amux.session_list"},
		{[]string{"run"}, "amux.session_run"},
		{[]string{"send"}, "amux.session_send"},
		{[]string{"interrupt"}, "amux.session_interrupt"},
		{[]string{"sleep"}, "amux.session_sleep"},
		{[]string{"resume"}, "amux.session_resume"},
		{[]string{"stop"}, "amux.session_stop"},
		{[]string{"restart"}, "amux.session_restart"},
		// CLI attach is the human live tail of a session; MCP exposes the
		// same read as a one-shot drained transcript.
		{[]string{"attach"}, "amux.session_stream"},
		// Bare `amux session queue` prints the pending list; the destructive
		// verbs nest under it.
		{[]string{"queue"}, "amux.session_queue_list"},
		{[]string{"queue", "clear"}, "amux.session_queue_clear"},
		{[]string{"queue", "cancel"}, "amux.session_queue_cancel"},
		{[]string{"prune"}, "amux.session_prune"},
	} {
		t.Run(strings.Join(action.cli, " "), func(t *testing.T) {
			cmd := newSessionCmd()
			for _, name := range action.cli {
				cmd = clientopCoverageFindSubcommandOrNil(cmd, name)
				if cmd == nil {
					t.Fatalf("CLI has no `amux session %s`", strings.Join(action.cli, " "))
				}
			}
			if !clientopCoverageMCPToolRegistered(action.mcp) {
				t.Errorf("MCP has no tool %q", action.mcp)
			}
		})
	}
}

func clientopCoverageFindSubcommand(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	cmd := clientopCoverageFindSubcommandOrNil(parent, name)
	if cmd == nil {
		t.Fatalf("no subcommand %q under %q", name, parent.Name())
	}
	return cmd
}

func clientopCoverageFindSubcommandOrNil(parent *cobra.Command, name string) *cobra.Command {
	for _, cmd := range parent.Commands() {
		if cmd.Name() == name {
			return cmd
		}
	}
	return nil
}

func clientopCoverageMCPToolRegistered(name string) bool {
	for _, tool := range amuxTools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}
