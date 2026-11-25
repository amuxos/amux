package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/system"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/updatecheck"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func TestSystemStatusHumanAndJSON(t *testing.T) {
	snapshot := healthySystemSnapshot()

	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "human", args: []string{"status"}, want: []string{"state: healthy", "hub: reachable", "targets: available (2 connected)"}},
		{name: "json", args: []string{"status", "--json"}, want: []string{`"schema":"amux.system.snapshot.v1"`, `"scope":{"kind":"current"}`, `"state":"healthy"`, `"components":[{"name":"hub","state":"reachable"}`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := &fakeSystemLifecycle{snapshot: snapshot}
			out, errOut, err := runSystemCommand(t, lifecycle, nil, tt.args...)
			if err != nil {
				t.Fatalf("system %v: %v", tt.args, err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Fatalf("output missing %q:\n%s", want, out)
				}
			}
			if errOut != "" {
				t.Fatalf("stderr = %q, want empty", errOut)
			}
			if !reflect.DeepEqual(lifecycle.inspectScopes, []system.Scope{system.CurrentScope()}) {
				t.Fatalf("inspect scopes = %#v", lifecycle.inspectScopes)
			}
			if lifecycle.mutationCalls != 0 {
				t.Fatalf("read path made %d mutation calls", lifecycle.mutationCalls)
			}
		})
	}
}

func TestSystemStatusDegradedStillSucceeds(t *testing.T) {
	lifecycle := &fakeSystemLifecycle{snapshot: system.Snapshot{
		State: system.StateDegraded,
		Components: []system.Component{
			{Name: "hub", State: "configured", Detail: "connection refused"},
		},
	}}
	out, _, err := runSystemCommand(t, lifecycle, nil, "status", "--json")
	if err != nil {
		t.Fatalf("degraded status returned error: %v", err)
	}
	if !strings.Contains(out, `"state":"degraded"`) {
		t.Fatalf("status JSON missing degraded state:\n%s", out)
	}
}

func TestSystemStatusShowsCachedAvailableUpdateWithoutCheckingNetwork(t *testing.T) {
	checkedAt := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	previousReader := systemUpdateStateReader
	systemUpdateStateReader = func() (updatecheck.State, error) {
		return updatecheck.State{
			Schema:           updatecheck.StateSchema,
			CurrentVersion:   "1.0.0.1457",
			LatestVersion:    "1.0.0.1458",
			Comparison:       updatecheck.ComparisonNewer,
			CheckedAt:        checkedAt,
			DismissedVersion: "1.0.0.1458",
		}, nil
	}
	t.Cleanup(func() { systemUpdateStateReader = previousReader })

	snapshot := healthySystemSnapshot()
	snapshot.Version = "1.0.0.1457"
	human, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: snapshot}, nil, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"update: available", "latest 1.0.0.1458", "checked 2026-08-10T08:00:00Z"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human status missing %q:\n%s", want, human)
		}
	}

	jsonOutput, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: snapshot}, nil, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"update":{`, `"available":true`, `"latest_version":"1.0.0.1458"`, `"prompt_dismissed":true`} {
		if !strings.Contains(jsonOutput, want) {
			t.Fatalf("JSON status missing %q:\n%s", want, jsonOutput)
		}
	}
}

func TestSystemStatusIgnoresUnavailableUpdateCache(t *testing.T) {
	previousReader := systemUpdateStateReader
	systemUpdateStateReader = func() (updatecheck.State, error) {
		return updatecheck.State{}, errors.New("cache is corrupt")
	}
	t.Cleanup(func() { systemUpdateStateReader = previousReader })

	out, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: healthySystemSnapshot()}, nil, "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "update:") {
		t.Fatalf("status exposed unavailable update cache:\n%s", out)
	}
}

func TestSystemStatusHumanPrintsNextActions(t *testing.T) {
	snapshot := healthySystemSnapshot()
	snapshot.State = system.StateDegraded
	snapshot.NextActions = []system.SuggestedAction{{
		Command:              "amux system restart --force",
		Reason:               "runtime is reachable but not registered",
		RequiresConfirmation: true,
	}}
	out, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: snapshot}, nil, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"next_actions:", "amux system restart --force", "requires confirmation"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestSystemStatusHumanColorizesWhenForced(t *testing.T) {
	t.Setenv("AMUX_COLOR", "always")
	t.Setenv("NO_COLOR", "")
	snapshot := healthySystemSnapshot()
	snapshot.Summary = &system.SnapshotSummary{
		Installation: &system.InstallationSummary{
			Home:         "/tmp/amux-home",
			ConfigPath:   "/tmp/amux-home/config.toml",
			ConfigExists: true,
			Mode:         "standalone",
			HubAddress:   "127.0.0.1:7100",
			RuntimeID:    "runtime-a",
		},
	}

	out, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: snapshot}, nil, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"state: \x1b[32mhealthy\x1b[0m",
		"\x1b[1minstallation:\x1b[22m",
		"config: /tmp/amux-home/config.toml (\x1b[32mexists\x1b[0m)",
		"  hub: \x1b[32mreachable\x1b[0m",
		"  targets: \x1b[32mavailable\x1b[0m (2 connected)",
		"  hub: \x1b[32mpass\x1b[0m [required]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("colored system status missing %q:\n%q", want, out)
		}
	}
}

func TestSystemDoctorHumanColorizesRequiredFailuresWhenForced(t *testing.T) {
	t.Setenv("AMUX_COLOR", "always")
	t.Setenv("NO_COLOR", "")
	snapshot := system.Snapshot{
		State: system.StateDegraded,
		Components: []system.Component{
			{Name: "hub", State: "configured", Detail: "connection refused"},
		},
		Checks: []system.Check{
			{Name: "hub", State: "fail", Required: true, Message: "connection refused"},
		},
		NextActions: []system.SuggestedAction{{
			Command: "amux system start",
			Reason:  "required component is unavailable",
		}},
	}
	out, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: snapshot}, nil, "doctor")
	if err == nil || !strings.Contains(err.Error(), "hub") {
		t.Fatalf("doctor error = %v, want required check failure", err)
	}
	for _, want := range []string{
		"state: \x1b[33mdegraded\x1b[0m",
		"  hub: \x1b[32mconfigured\x1b[0m",
		"  hub: \x1b[31mfail\x1b[0m [required]",
		"\x1b[1mnext_actions:\x1b[22m",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("colored system doctor missing %q:\n%q", want, out)
		}
	}
}

func TestSystemStatusHumanColorCanBeDisabled(t *testing.T) {
	t.Setenv("AMUX_COLOR", "always")
	t.Setenv("NO_COLOR", "1")
	out, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: healthySystemSnapshot()}, nil, "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("system status should not contain ANSI when NO_COLOR is set:\n%q", out)
	}
}

func TestSystemStatusHumanPrintsSummarySections(t *testing.T) {
	snapshot := healthySystemSnapshot()
	snapshot.Summary = &system.SnapshotSummary{
		Installation: &system.InstallationSummary{
			Home:         "/tmp/amux-home",
			ConfigPath:   "/tmp/amux-home/config.toml",
			ConfigExists: true,
			Mode:         "standalone",
			HubAddress:   "127.0.0.1:7100",
			RuntimeID:    "runtime-a",
			RuntimeName:  "host-a",
		},
		Workloads: &system.WorkloadSummary{
			LocalSessions:  system.SessionWorkloadSummary{Total: 3, Running: 1, Archived: 1, Other: 1},
			TargetSessions: system.SessionWorkloadSummary{Total: 5, Running: 2},
			Tasks:          system.TaskWorkloadSummary{Total: 4, Running: 1, Terminal: 2, Other: 1},
			Schedules:      system.ScheduleWorkloadSummary{Total: 3, Enabled: 2, Disabled: 1},
		},
		Integrations: &system.IntegrationSummary{
			Feishu:        system.FeishuIntegrationSummary{Configured: true, ProfilesConfigured: 1, ProfilesTotal: 2, BridgeDBHealthy: true},
			MCP:           system.MCPIntegrationSummary{Enabled: true, AutoInject: true, Backend: "hub 127.0.0.1:7100", State: "ok"},
			LocalServices: system.LocalServiceSummary{Servers: 1, SessionOwned: 1, Total: 2},
		},
		Operations: &system.OperationSummary{
			Active:     &system.OperationBrief{ID: "op-active", Action: system.ActionRestart, State: system.OperationApplying},
			LastFailed: &system.OperationBrief{ID: "op-failed", Action: system.ActionStart, State: system.OperationFailed, Error: "boom"},
		},
	}

	out, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: snapshot}, nil, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"installation:",
		"home: /tmp/amux-home",
		"config: /tmp/amux-home/config.toml (exists)",
		"mode: standalone",
		"hub_addr: 127.0.0.1:7100",
		"runtime: runtime-a / host-a",
		"workloads:",
		"sessions: 3 local total, 1 running, 1 archived, 1 other",
		"target_sessions: 5 total, 2 running",
		"tasks: 4 total, 1 running, 2 terminal, 1 other",
		"schedules: 3 total, 2 enabled, 1 disabled",
		"integrations:",
		"feishu: configured, profiles 1/2, bridge_db ok",
		"mcp: enabled, auto_inject enabled, backend hub 127.0.0.1:7100, ok",
		"local_services: 2 total, 1 restartable, 1 session-owned",
		"operations:",
		"active: op-active restart applying",
		"last_failed: op-failed start failed (boom)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestSystemStatusReturnsCloseErrorAfterOutput(t *testing.T) {
	closeErr := errors.New("close journal failed")
	out, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: healthySystemSnapshot()}, func() error {
		return closeErr
	}, "status", "--json")
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want close error", err)
	}
	if !strings.Contains(out, `"state":"healthy"`) {
		t.Fatalf("status output was lost on close error:\n%s", out)
	}
}

func TestSystemDoctorJSONOutputsSnapshotBeforeRequiredCheckError(t *testing.T) {
	lifecycle := &fakeSystemLifecycle{snapshot: system.Snapshot{
		State: system.StateDegraded,
		Components: []system.Component{
			{Name: "hub", State: "configured"},
		},
		Checks: []system.Check{
			{Name: "hub", State: "fail", Required: true, Message: "connection refused"},
			{Name: "targets", State: "fail", Message: "unavailable"},
		},
	}}
	out, errOut, err := runSystemCommand(t, lifecycle, nil, "doctor", "--json")
	if err == nil || !strings.Contains(err.Error(), "hub") {
		t.Fatalf("doctor error = %v, want required hub check failure", err)
	}
	var got system.Snapshot
	if decodeErr := json.Unmarshal([]byte(out), &got); decodeErr != nil {
		t.Fatalf("doctor did not emit complete JSON before error: %v\n%s", decodeErr, out)
	}
	if got.Schema != system.SnapshotSchema || got.Scope != system.CurrentScope() || len(got.Checks) != 2 {
		t.Fatalf("doctor snapshot = %#v", got)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q, want returned error only", errOut)
	}
	if lifecycle.mutationCalls != 0 {
		t.Fatalf("doctor made %d mutation calls", lifecycle.mutationCalls)
	}
}

func TestSystemDoctorSucceedsWhenRequiredChecksPass(t *testing.T) {
	snapshot := healthySystemSnapshot()
	snapshot.Checks = append(snapshot.Checks, system.Check{Name: "optional", State: "fail", Message: "not configured"})
	_, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: snapshot}, nil, "doctor", "--json")
	if err != nil {
		t.Fatalf("doctor rejected optional failed check: %v", err)
	}
}

func TestLegacySystemSnapshotIncludesNextActions(t *testing.T) {
	hubAddr := freeLoopbackAddr(t)
	report := cliStatusReport{
		Version:     "1.2.3",
		RuntimeID:   "runtime-a",
		ConfigError: "",
		Hub: serviceReport{
			State: "configured",
			Addr:  hubAddr,
			Error: "connection refused",
		},
		Runtime: serviceReport{
			State: "not running",
		},
		LocalSessions: sessionSummary{Error: "runtime socket missing"},
		HubTargets:    targetSummary{Error: "connection refused"},
	}

	snapshot := legacySystemSnapshot(report)
	if snapshot.State != system.StateStopped {
		t.Fatalf("state = %s, want stopped", snapshot.State)
	}
	if len(snapshot.NextActions) == 0 {
		t.Fatalf("next actions missing: %#v", snapshot)
	}
	if snapshot.NextActions[0].Command != "amux system start" {
		t.Fatalf("first next action = %#v, want amux system start", snapshot.NextActions[0])
	}
}

func TestSystemStartStopRestartUseJournalBackedLifecycle(t *testing.T) {
	useIsolatedSystemConfig(t)
	for _, tt := range []struct {
		name   string
		args   []string
		action system.Action
		force  bool
	}{
		{name: "start", args: []string{"start"}, action: system.ActionStart},
		{name: "stop", args: []string{"stop", "--force"}, action: system.ActionStop, force: true},
		{name: "restart", args: []string{"restart", "--force"}, action: system.ActionRestart, force: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := &fakeSystemLifecycle{
				applyID:   "op-mutation",
				operation: testSystemOperation("op-mutation", system.OperationSucceeded, time.Unix(100, 0)),
			}
			out, _, err := runSystemCommand(t, lifecycle, nil, tt.args...)
			if err != nil {
				t.Fatalf("system %s: %v", tt.name, err)
			}
			want := system.ChangeRequest{Action: tt.action, Scope: system.CurrentScope(), Force: tt.force}
			if !reflect.DeepEqual(lifecycle.planRequests, []system.ChangeRequest{want}) {
				t.Fatalf("Plan requests = %#v, want %#v", lifecycle.planRequests, []system.ChangeRequest{want})
			}
			if len(lifecycle.applyPlans) != 1 || lifecycle.applyPlans[0].Request != want {
				t.Fatalf("Apply plans = %#v, want request %#v", lifecycle.applyPlans, want)
			}
			if !reflect.DeepEqual(lifecycle.getIDs, []system.OperationID{"op-mutation"}) {
				t.Fatalf("GetOperation IDs = %v", lifecycle.getIDs)
			}
			if !strings.Contains(out, "op-mutation") {
				t.Fatalf("output missing operation ID:\n%s", out)
			}
		})
	}
}

func TestSystemMutationPlanJSONDoesNotApply(t *testing.T) {
	useIsolatedSystemConfig(t)
	lifecycle := &fakeSystemLifecycle{
		planID: "op-plan",
		snapshot: system.Snapshot{
			Schema: system.SnapshotSchema,
			Scope:  system.CurrentScope(),
			State:  system.StateDegraded,
			Components: []system.Component{
				{Name: "hub", State: "reachable"},
				{Name: "runtime", State: "reachable"},
				{Name: "targets", State: "unavailable"},
			},
			Checks: []system.Check{
				{Name: "hub", State: "pass", Required: true},
				{Name: "runtime", State: "pass", Required: true},
				{Name: "targets", State: "fail", Required: true, Message: "expected runtime not registered"},
			},
		},
	}

	out, _, err := runSystemCommand(t, lifecycle, nil, "restart", "--plan", "--json")
	if err != nil {
		t.Fatalf("system restart --plan --json: %v", err)
	}
	if lifecycle.mutationCalls != 0 {
		t.Fatalf("plan mode applied mutation %d time(s)", lifecycle.mutationCalls)
	}
	var preview system.PlanPreview
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatalf("decode plan preview: %v\n%s", err, out)
	}
	if preview.Schema != system.PlanPreviewSchema || preview.Action != system.ActionRestart || preview.Scope != system.CurrentScope() {
		t.Fatalf("preview = %#v", preview)
	}
	if !preview.Impact.WillRestartHub || !preview.Impact.WillRestartRuntime {
		t.Fatalf("impact = %#v, want hub and runtime restart", preview.Impact)
	}
}

func TestSystemMutationJSONMatchesOperationShowSchema(t *testing.T) {
	useIsolatedSystemConfig(t)
	lifecycle := &fakeSystemLifecycle{
		applyID:   "op-json",
		operation: testSystemOperation("op-json", system.OperationSucceeded, time.Unix(100, 0)),
	}
	out, _, err := runSystemCommand(t, lifecycle, nil, "start", "--json")
	if err != nil {
		t.Fatalf("system start --json: %v", err)
	}
	var got struct {
		Schema    string             `json:"schema"`
		ID        system.OperationID `json:"id"`
		Operation json.RawMessage    `json:"operation"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode system mutation JSON: %v\n%s", err, out)
	}
	if got.Schema != "amux.system.operation.v1" || got.ID != "op-json" || got.Operation != nil {
		t.Fatalf("system mutation JSON = %#v", got)
	}
}

func TestSystemMutationPlanHumanOutput(t *testing.T) {
	useIsolatedSystemConfig(t)
	lifecycle := &fakeSystemLifecycle{
		planID:   "op-plan",
		snapshot: healthySystemSnapshot(),
	}
	out, _, err := runSystemCommand(t, lifecycle, nil, "start", "--plan")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"plan: start", "scope: current", "apply: amux system start"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q:\n%s", want, out)
		}
	}
	if lifecycle.mutationCalls != 0 {
		t.Fatalf("plan mode applied mutation %d time(s)", lifecycle.mutationCalls)
	}
}

func TestSystemStartStopRestartErrorsIncludeOperationID(t *testing.T) {
	useIsolatedSystemConfig(t)
	t.Run("executor failure", func(t *testing.T) {
		lifecycle := &fakeSystemLifecycle{applyID: "op-exec", applyErr: errors.New("runtime start failed")}
		_, _, err := runSystemCommand(t, lifecycle, nil, "start")
		if err == nil || !strings.Contains(err.Error(), "op-exec") || !strings.Contains(err.Error(), "runtime start failed") {
			t.Fatalf("error = %v, want executor failure and operation ID", err)
		}
		if !reflect.DeepEqual(lifecycle.getIDs, []system.OperationID{"op-exec"}) {
			t.Fatalf("GetOperation IDs after executor failure = %v", lifecycle.getIDs)
		}
	})

	t.Run("apply failure uses planned operation ID", func(t *testing.T) {
		lifecycle := &fakeSystemLifecycle{
			planID:   "op-planned",
			applyErr: errors.New("persist applying state failed"),
			getErr:   errors.New("operation lookup failed"),
		}
		_, _, err := runSystemCommand(t, lifecycle, nil, "start")
		if err == nil || !strings.Contains(err.Error(), "op-planned") ||
			!strings.Contains(err.Error(), "persist applying state failed") ||
			!strings.Contains(err.Error(), "operation lookup failed") {
			t.Fatalf("error = %v, want planned ID plus apply and lookup failures", err)
		}
		if !reflect.DeepEqual(lifecycle.getIDs, []system.OperationID{"op-planned"}) {
			t.Fatalf("GetOperation IDs after apply failure = %v", lifecycle.getIDs)
		}
	})

	t.Run("terminal failure", func(t *testing.T) {
		lifecycle := &fakeSystemLifecycle{
			applyID: "op-terminal",
			operation: system.Operation{
				ID:    "op-terminal",
				State: system.OperationFailed,
				Error: "verification failed",
			},
		}
		_, _, err := runSystemCommand(t, lifecycle, nil, "restart")
		if err == nil || !strings.Contains(err.Error(), "op-terminal") || !strings.Contains(err.Error(), "verification failed") {
			t.Fatalf("error = %v, want terminal failure and operation ID", err)
		}
	})
}

func TestSystemStartStopRestartLegacyServiceOrder(t *testing.T) {
	for _, tt := range []struct {
		name       string
		hubAddress string
		action     system.Action
		want       []string
	}{
		{name: "local start", hubAddress: "127.0.0.1:7100", action: system.ActionStart, want: []string{"start hub", "start runtime"}},
		{name: "local stop", hubAddress: "localhost:7100", action: system.ActionStop, want: []string{"guard runtime", "guard hub", "stop runtime", "stop hub"}},
		{name: "local restart", hubAddress: "[::1]:7100", action: system.ActionRestart, want: []string{"guard runtime", "guard hub", "stop agent-pack-autosync", "stop runtime", "stop hub", "start hub", "start runtime", "start agent-pack-autosync"}},
		{name: "remote start", hubAddress: "hub.example.com:7100", action: system.ActionStart, want: []string{"start runtime"}},
		{name: "remote stop", hubAddress: "10.0.0.12:7100", action: system.ActionStop, want: []string{"guard runtime", "stop runtime"}},
		{name: "remote restart", hubAddress: "10.0.0.12:7100", action: system.ActionRestart, want: []string{"guard runtime", "stop agent-pack-autosync", "stop runtime", "start runtime", "start agent-pack-autosync"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			hooks := fakeLegacySystemMutationHooks(&calls, tt.hubAddress)
			execute := legacySystemExecutor(io.Discard, hooks)
			err := execute(context.Background(), system.ChangePlan{Request: system.ChangeRequest{
				Action: tt.action,
				Scope:  system.CurrentScope(),
			}})
			if err != nil {
				t.Fatalf("execute %s: %v", tt.action, err)
			}
			if !reflect.DeepEqual(calls, tt.want) {
				t.Fatalf("calls = %v, want %v", calls, tt.want)
			}
		})
	}
}

func TestSystemRestartLeavesStoppedAgentPackAutoSyncStopped(t *testing.T) {
	var calls []string
	hooks := fakeLegacySystemMutationHooks(&calls, defaultLocalHubAddr)
	hooks.serviceRunning = func(service managedService) bool {
		if service.Name == "agent-pack-autosync" {
			return false
		}
		return true
	}
	execute := legacySystemExecutor(io.Discard, hooks)
	if err := execute(context.Background(), system.ChangePlan{Request: system.ChangeRequest{
		Action: system.ActionRestart,
		Scope:  system.CurrentScope(),
	}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"guard runtime", "guard hub", "stop runtime", "stop hub", "start hub", "start runtime"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want stopped autosync preserved: %v", calls, want)
	}
}

func TestSystemRestartRestoresAgentPackAutoSyncWhenSystemStopFails(t *testing.T) {
	var calls []string
	hooks := fakeLegacySystemMutationHooks(&calls, defaultLocalHubAddr)
	stopErr := errors.New("runtime stop failed")
	hooks.stopService = func(service managedService) error {
		calls = append(calls, "stop "+service.Name)
		if service.Name == "runtime" {
			return stopErr
		}
		return nil
	}
	execute := legacySystemExecutor(io.Discard, hooks)
	err := execute(context.Background(), system.ChangePlan{Request: system.ChangeRequest{
		Action: system.ActionRestart,
		Scope:  system.CurrentScope(),
	}})
	if !errors.Is(err, stopErr) {
		t.Fatalf("error = %v, want runtime stop failure", err)
	}
	want := []string{"guard runtime", "guard hub", "stop agent-pack-autosync", "stop runtime", "start agent-pack-autosync"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want autosync restored after failure: %v", calls, want)
	}
}

func TestSystemStartReconcilesRunningRuntimeMissingFromLocalHub(t *testing.T) {
	plan := system.ChangePlan{
		Request: system.ChangeRequest{Action: system.ActionStart, Scope: system.CurrentScope()},
		Before: system.Snapshot{
			State:      system.StateDegraded,
			Components: []system.Component{{Name: "runtime", State: "reachable"}},
			Checks: []system.Check{
				{Name: "hub", State: "pass", Required: true},
				{Name: "targets", State: "fail", Required: true, Message: `expected runtime "local" not registered`},
			},
		},
	}
	for _, tt := range []struct {
		name                    string
		skipStaleRuntimeRestart bool
		want                    []string
	}{
		{name: "ordinary system start", want: []string{"start hub", "guard runtime", "stop runtime", "start runtime"}},
		{name: "update start after refresh", skipStaleRuntimeRestart: true, want: []string{"start hub", "start runtime"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			hooks := fakeLegacySystemMutationHooks(&calls, defaultLocalHubAddr)
			hooks.skipStaleRuntimeRestart = tt.skipStaleRuntimeRestart
			if err := legacySystemExecutor(io.Discard, hooks)(t.Context(), plan); err != nil {
				t.Fatalf("execute start: %v", err)
			}
			if !reflect.DeepEqual(calls, tt.want) {
				t.Fatalf("calls = %v, want %v", calls, tt.want)
			}
		})
	}
}

func TestSystemStartRuntimeReconciliationSafety(t *testing.T) {
	tests := []struct {
		name         string
		hubAddress   string
		before       system.Snapshot
		guardErr     error
		wantCalls    []string
		wantBlocked  bool
		wantGuidance string
	}{
		{
			name:       "healthy local runtime stays running",
			hubAddress: defaultLocalHubAddr,
			before: system.Snapshot{
				State:      system.StateHealthy,
				Components: []system.Component{{Name: "runtime", State: "reachable"}},
				Checks:     []system.Check{{Name: "hub", State: "pass"}, {Name: "targets", State: "pass"}},
			},
			wantCalls: []string{"start hub", "start runtime"},
		},
		{
			name:       "remote runtime reconciles only when hub is reachable",
			hubAddress: "hub.example.com:7100",
			before: system.Snapshot{
				State:      system.StateDegraded,
				Components: []system.Component{{Name: "runtime", State: "reachable"}},
				Checks:     []system.Check{{Name: "hub", State: "pass"}, {Name: "targets", State: "fail"}},
			},
			wantCalls: []string{"guard runtime", "stop runtime", "start runtime"},
		},
		{
			name:       "unreachable remote hub does not disrupt runtime",
			hubAddress: "hub.example.com:7100",
			before: system.Snapshot{
				State:      system.StateDegraded,
				Components: []system.Component{{Name: "runtime", State: "reachable"}},
				Checks:     []system.Check{{Name: "hub", State: "fail"}, {Name: "targets", State: "fail"}},
			},
			wantCalls: []string{"start runtime"},
		},
		{
			name:       "active work blocks reconciliation before mutation",
			hubAddress: defaultLocalHubAddr,
			before: system.Snapshot{
				State:      system.StateDegraded,
				Components: []system.Component{{Name: "runtime", State: "reachable"}},
				Checks:     []system.Check{{Name: "hub", State: "pass"}, {Name: "targets", State: "fail"}},
			},
			guardErr:     errors.New("running session smoke blocks runtime stop"),
			wantCalls:    []string{"start hub", "guard runtime"},
			wantBlocked:  true,
			wantGuidance: "amux system restart --force",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			var gotGuidance string
			hooks := fakeLegacySystemMutationHooks(&calls, tt.hubAddress)
			if tt.guardErr != nil {
				hooks.guardRuntimeStop = func(_ context.Context, _ bool, guidance string) error {
					calls = append(calls, "guard runtime")
					gotGuidance = guidance
					return tt.guardErr
				}
			}
			execute := legacySystemExecutor(io.Discard, hooks)
			err := execute(context.Background(), system.ChangePlan{
				Request: system.ChangeRequest{Action: system.ActionStart, Scope: system.CurrentScope()},
				Before:  tt.before,
			})
			var blocked *system.BlockedError
			if tt.wantBlocked {
				if !errors.As(err, &blocked) || !errors.Is(err, tt.guardErr) {
					t.Fatalf("error = %T %v, want blocked guard error", err, err)
				}
			} else if err != nil {
				t.Fatalf("execute start: %v", err)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, tt.wantCalls)
			}
			if gotGuidance != tt.wantGuidance {
				t.Fatalf("guard guidance = %q, want %q", gotGuidance, tt.wantGuidance)
			}
		})
	}
}

func TestSystemStopRestartGuardFailureStopsBeforeMutation(t *testing.T) {
	guardErr := errors.New("running sessions block stop")
	for _, tt := range []struct {
		name         string
		action       system.Action
		wantGuidance string
	}{
		{name: "stop", action: system.ActionStop, wantGuidance: "amux system stop --force"},
		{name: "restart", action: system.ActionRestart, wantGuidance: "amux system restart --force"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			var gotGuidance string
			hooks := fakeLegacySystemMutationHooks(&calls, "127.0.0.1:7100")
			hooks.guardRuntimeStop = func(_ context.Context, _ bool, guidance string) error {
				calls = append(calls, "guard runtime")
				gotGuidance = guidance
				return guardErr
			}
			execute := legacySystemExecutor(io.Discard, hooks)
			err := execute(context.Background(), system.ChangePlan{Request: system.ChangeRequest{
				Action: tt.action,
				Scope:  system.CurrentScope(),
			}})
			if !errors.Is(err, guardErr) {
				t.Fatalf("error = %v, want guard failure", err)
			}
			var blocked *system.BlockedError
			if !errors.As(err, &blocked) {
				t.Fatalf("error = %T %v, want BlockedError", err, err)
			}
			if blocked.ExitCode() != 3 {
				t.Fatalf("BlockedError.ExitCode() = %d, want 3", blocked.ExitCode())
			}
			if gotGuidance != tt.wantGuidance {
				t.Fatalf("guard guidance = %q, want %q", gotGuidance, tt.wantGuidance)
			}
			if !reflect.DeepEqual(calls, []string{"guard runtime"}) {
				t.Fatalf("calls after guard failure = %v", calls)
			}
		})
	}
}

func TestRunSystemStopUsesBlockedExitCodeButServiceFailuresRemainGeneric(t *testing.T) {
	useIsolatedSystemConfig(t)
	tests := []struct {
		name      string
		configure func(*legacySystemMutationHooks, error)
		wantCode  int
	}{
		{
			name: "active workload blocks stop",
			configure: func(hooks *legacySystemMutationHooks, cause error) {
				hooks.guardRuntimeStop = func(context.Context, bool, string) error { return cause }
			},
			wantCode: 3,
		},
		{
			name: "runtime service stop fails",
			configure: func(hooks *legacySystemMutationHooks, cause error) {
				hooks.stopService = func(service managedService) error {
					if service.Name == "runtime" {
						return cause
					}
					return nil
				}
			},
			wantCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			journal, err := system.OpenJournal(filepath.Join(t.TempDir(), "operations.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = journal.Close() })
			cause := errors.New("stop sentinel")
			var calls []string
			hooks := fakeLegacySystemMutationHooks(&calls, defaultLocalHubAddr)
			tt.configure(&hooks, cause)
			lifecycle := system.New(system.Dependencies{
				Journal: journal,
				Inspect: func(context.Context, system.Scope) (system.Snapshot, error) {
					return system.Snapshot{State: system.StateHealthy}, nil
				},
				Execute: legacySystemExecutor(io.Discard, hooks),
				IDGen:   func() string { return "op-stop" },
				Now:     func() time.Time { return time.Unix(100, 0).UTC() },
			})
			opener := fixedSystemLifecycleOpener(lifecycle)
			original := rootCommandBuilder
			rootCommandBuilder = func() *cobra.Command {
				root := &cobra.Command{Use: "amux"}
				root.AddCommand(newSystemCmdWithOpener(opener))
				installCommandValidation(root)
				return root
			}
			t.Cleanup(func() { rootCommandBuilder = original })

			var stderr bytes.Buffer
			if got := run([]string{"system", "stop"}, &stderr); got != tt.wantCode {
				t.Fatalf("run(system stop) exit code = %d, want %d; stderr:\n%s", got, tt.wantCode, stderr.String())
			}
			for _, want := range []string{"op-stop", cause.Error()} {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("stderr = %q, want %q", stderr.String(), want)
				}
			}
		})
	}
}

func TestSystemStartStopRestartDefaultHooksLoadConfiguredHubAddress(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", filepath.Join(home, ".amux"))
	cfg, err := readCLIConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := setConfigValue(cfg, "hub.addr", "hub.example.com:7100"); err != nil {
		t.Fatal(err)
	}
	if err := replaceCLIConfigFileForTest(cfg); err != nil {
		t.Fatal(err)
	}

	got, err := defaultLegacySystemMutationHooks().hubAddress()
	if err != nil {
		t.Fatalf("resolve effective hub address: %v", err)
	}
	if got != "hub.example.com:7100" {
		t.Fatalf("effective hub address = %q, want configured external hub", got)
	}
}

func TestLegacySystemManagesSupportedLocalListenerAddresses(t *testing.T) {
	for _, address := range []string{":7100", "0.0.0.0:7100", "[::]:7100", "127.0.0.1:7100", "localhost:7100"} {
		t.Run(address, func(t *testing.T) {
			if !legacySystemManagesHub(address) {
				t.Fatalf("legacySystemManagesHub(%q) = false, want local management", address)
			}
		})
	}
	if legacySystemManagesHub("hub.example.com:7100") {
		t.Fatal("external hub address was classified as locally managed")
	}
}

func TestSystemMutationRejectsInvalidConfigBeforeLifecyclePlan(t *testing.T) {
	tests := []struct {
		name       string
		configText string
		wantText   string
	}{
		{name: "malformed TOML", configText: "[hub\naddr = broken", wantText: "parse config"},
		{name: "hub address missing port", configText: "[hub]\naddr = 'hub.example.com'\n", wantText: "host:port"},
		{name: "hub port out of range", configText: "[hub]\naddr = 'hub.example.com:70000'\n", wantText: "port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			home := t.TempDir()
			amuxHome := filepath.Join(home, ".amux")
			t.Setenv("HOME", home)
			t.Setenv("AMUX_HOME", amuxHome)
			if err := os.MkdirAll(amuxHome, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(amuxHome, "config.toml"), []byte(tt.configText), 0o600); err != nil {
				t.Fatal(err)
			}

			opened := false
			cmd := newSystemCmdWithOpener(func(io.Writer) (system.Lifecycle, func() error, error) {
				opened = true
				return &fakeSystemLifecycle{snapshot: healthySystemSnapshot()}, func() error { return nil }, nil
			})
			cmd.SetArgs([]string{"start"})
			err := cmd.Execute()
			var validation *system.ValidationError
			if !errors.As(err, &validation) || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("system start error = %v, want validation containing %q", err, tt.wantText)
			}
			if opened {
				t.Fatal("invalid configuration opened lifecycle before Plan")
			}
			if _, err := os.Stat(filepath.Join(amuxHome, "system", "operations.db")); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration created operation journal, err=%v", err)
			}
		})
	}
}

func TestSystemLifecycleJournalRecordsTopologyAndServiceFailures(t *testing.T) {
	useIsolatedSystemConfig(t)
	for _, tt := range []struct {
		name      string
		args      []string
		operation system.OperationID
		configure func(*legacySystemMutationHooks, *[]string)
		wantCalls []string
		wantCause string
	}{
		{
			name:      "local runtime start fails after hub start",
			args:      []string{"start"},
			operation: "op-runtime-start",
			configure: func(hooks *legacySystemMutationHooks, calls *[]string) {
				hooks.startService = func(service managedService) error {
					*calls = append(*calls, "start "+service.Name)
					if service.Name == "runtime" {
						return errors.New("runtime start failed")
					}
					return nil
				}
			},
			wantCalls: []string{"start hub", "start runtime"},
			wantCause: "runtime start failed",
		},
		{
			name:      "local hub stop fails after runtime stop",
			args:      []string{"stop", "--force"},
			operation: "op-hub-stop",
			configure: func(hooks *legacySystemMutationHooks, calls *[]string) {
				hooks.stopService = func(service managedService) error {
					*calls = append(*calls, "stop "+service.Name)
					if service.Name == "hub" {
						return errors.New("hub stop failed")
					}
					return nil
				}
			},
			wantCalls: []string{"guard runtime", "guard hub", "stop runtime", "stop hub"},
			wantCause: "hub stop failed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			journal, err := system.OpenJournal(filepath.Join(t.TempDir(), "operations.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			var calls []string
			hooks := fakeLegacySystemMutationHooks(&calls, defaultLocalHubAddr)
			if tt.configure != nil {
				tt.configure(&hooks, &calls)
			}
			lifecycle := system.New(system.Dependencies{
				Journal: journal,
				Inspect: func(context.Context, system.Scope) (system.Snapshot, error) {
					return system.Snapshot{State: system.StateStopped}, nil
				},
				Execute: legacySystemExecutor(io.Discard, hooks),
				IDGen:   func() string { return string(tt.operation) },
				Now:     func() time.Time { return time.Unix(100, 0).UTC() },
			})

			_, _, commandErr := runSystemCommand(t, lifecycle, nil, tt.args...)
			if commandErr == nil || !strings.Contains(commandErr.Error(), string(tt.operation)) || !strings.Contains(commandErr.Error(), tt.wantCause) {
				t.Fatalf("command error = %v, want operation %s and cause %q", commandErr, tt.operation, tt.wantCause)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, tt.wantCalls)
			}
			operation, err := lifecycle.GetOperation(context.Background(), tt.operation)
			if err != nil {
				t.Fatal(err)
			}
			if operation.ID != tt.operation || operation.State != system.OperationFailed || !strings.Contains(operation.Error, tt.wantCause) {
				t.Fatalf("operation = %#v, want failed %s containing %q", operation, tt.operation, tt.wantCause)
			}
		})
	}
}

func TestLegacySystemExecutorSetupUsesFrozenSpec(t *testing.T) {
	for _, tt := range []struct {
		name      string
		spec      system.SetupSpec
		wantCalls []string
		wantHub   string
	}{
		{
			name:      "default local starts hub then runtime",
			spec:      system.SetupSpec{Profile: "local", Agent: "codex"},
			wantCalls: []string{"start hub", "start runtime"},
			wantHub:   defaultLocalHubAddr,
		},
		{
			name:      "remote starts runtime only",
			spec:      system.SetupSpec{Profile: "remote", Agent: "codex", HubAddress: "hub.example.com:7100"},
			wantCalls: []string{"start runtime"},
			wantHub:   "hub.example.com:7100",
		},
		{
			name:    "no start only writes config",
			spec:    system.SetupSpec{Profile: "local", Agent: "codex", NoStart: true},
			wantHub: defaultLocalHubAddr,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AMUX_HOME", "")
			var calls []string
			hooks := fakeLegacySystemMutationHooks(&calls, defaultLocalHubAddr)
			execute := legacySystemExecutor(io.Discard, hooks)
			err := execute(context.Background(), system.ChangePlan{Request: system.ChangeRequest{
				Action: system.ActionSetup,
				Scope:  system.CurrentScope(),
				Setup:  &tt.spec,
			}})
			if err != nil {
				t.Fatalf("execute setup: %v", err)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, tt.wantCalls)
			}
			config := readTestConfig(t, home)
			if !strings.Contains(config, "addr = '"+tt.wantHub+"'") {
				t.Fatalf("config missing hub %q:\n%s", tt.wantHub, config)
			}
		})
	}
}

func TestSystemOperationListPreservesJournalOrder(t *testing.T) {
	newer := testSystemOperation("op-new", system.OperationSucceeded, time.Unix(200, 0))
	older := testSystemOperation("op-old", system.OperationFailed, time.Unix(100, 0))
	lifecycle := &fakeSystemLifecycle{snapshot: system.Snapshot{
		Operations: []system.Operation{newer, older},
	}}
	out, _, err := runSystemCommand(t, lifecycle, nil, "operation", "list", "--json")
	if err != nil {
		t.Fatalf("operation list: %v", err)
	}
	var got system.Snapshot
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode operation list: %v\n%s", err, out)
	}
	if got.Schema != system.SnapshotSchema || got.Scope.Kind != system.ScopeOperations {
		t.Fatalf("operation snapshot = %#v", got)
	}
	if ids := []system.OperationID{got.Operations[0].ID, got.Operations[1].ID}; !reflect.DeepEqual(ids, []system.OperationID{"op-new", "op-old"}) {
		t.Fatalf("operation IDs = %v, want journal order", ids)
	}
	if !reflect.DeepEqual(lifecycle.inspectScopes, []system.Scope{{Kind: system.ScopeOperations}}) {
		t.Fatalf("inspect scopes = %#v", lifecycle.inspectScopes)
	}
}

func TestSystemOperationListJSONUsesEmptyArray(t *testing.T) {
	out, _, err := runSystemCommand(t, &fakeSystemLifecycle{snapshot: system.Snapshot{}}, nil, "operation", "list", "--json")
	if err != nil {
		t.Fatalf("operation list: %v", err)
	}
	var got struct {
		Operations []system.Operation `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode operation list: %v\n%s", err, out)
	}
	if got.Operations == nil || len(got.Operations) != 0 {
		t.Fatalf("operations = %#v, want []", got.Operations)
	}
}

func TestSystemOperationShowUsesTypedLookup(t *testing.T) {
	want := testSystemOperation("op-show", system.OperationFailed, time.Unix(100, 0))
	want.Error = "restart failed"
	lifecycle := &fakeSystemLifecycle{operation: want}
	out, _, err := runSystemCommand(t, lifecycle, nil, "operation", "show", "op-show", "--json")
	if err != nil {
		t.Fatalf("operation show: %v", err)
	}
	var got system.Operation
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode operation: %v\n%s", err, out)
	}
	if got.ID != want.ID || got.State != want.State || got.Error != want.Error {
		t.Fatalf("operation = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(lifecycle.getIDs, []system.OperationID{"op-show"}) {
		t.Fatalf("GetOperation IDs = %v", lifecycle.getIDs)
	}
}

func TestSystemOperationShowJSONIncludesNextActionsForFailure(t *testing.T) {
	op := testSystemOperation("op-failed", system.OperationFailed, time.Unix(100, 0))
	op.Action = system.ActionRestart
	op.Error = "runtime start failed"
	lifecycle := &fakeSystemLifecycle{operation: op}
	out, _, err := runSystemCommand(t, lifecycle, nil, "operation", "show", "op-failed", "--json")
	if err != nil {
		t.Fatalf("operation show: %v", err)
	}
	var got struct {
		Schema      string                   `json:"schema"`
		ID          system.OperationID       `json:"id"`
		State       system.OperationState    `json:"state"`
		NextActions []system.SuggestedAction `json:"next_actions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode operation: %v\n%s", err, out)
	}
	if got.Schema != "amux.system.operation.v1" || got.ID != "op-failed" || got.State != system.OperationFailed {
		t.Fatalf("operation = %#v", got)
	}
	if len(got.NextActions) != 1 || got.NextActions[0].Command != "amux system doctor" {
		t.Fatalf("next actions = %#v", got.NextActions)
	}
}

func TestSystemOperationCancelAcceptedAndRejected(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		lifecycle := &fakeSystemLifecycle{cancelResult: system.CancelResult{
			OperationID: "op-pending",
			Accepted:    true,
			State:       system.OperationCancelled,
		}}
		out, _, err := runSystemCommand(t, lifecycle, nil, "operation", "cancel", "op-pending", "--json")
		if err != nil {
			t.Fatalf("cancel pending: %v", err)
		}
		var got system.CancelResult
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decode cancel result: %v\n%s", err, out)
		}
		if !got.Accepted || got.State != system.OperationCancelled {
			t.Fatalf("cancel result = %#v", got)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		lifecycle := &fakeSystemLifecycle{cancelResult: system.CancelResult{
			OperationID: "op-applying",
			State:       system.OperationApplying,
			Reason:      "operation is no longer pending",
		}}
		out, _, err := runSystemCommand(t, lifecycle, nil, "operation", "cancel", "op-applying", "--json")
		if err == nil || !strings.Contains(err.Error(), "not cancelled") || !strings.Contains(err.Error(), "applying") {
			t.Fatalf("cancel rejected error = %v", err)
		}
		var got system.CancelResult
		if decodeErr := json.Unmarshal([]byte(out), &got); decodeErr != nil {
			t.Fatalf("rejected cancel did not emit typed JSON: %v\n%s", decodeErr, out)
		}
		if got.Accepted || got.Reason == "" {
			t.Fatalf("cancel result = %#v", got)
		}
	})
}

func TestSystemOperationNotFoundReturnsErrorWithoutSuccessOutput(t *testing.T) {
	notFound := errors.New("operation not found")
	lifecycle := &fakeSystemLifecycle{getErr: notFound}
	out, _, err := runSystemCommand(t, lifecycle, nil, "operation", "show", "missing")
	if !errors.Is(err, notFound) {
		t.Fatalf("show error = %v, want not found", err)
	}
	if out != "" {
		t.Fatalf("not-found show output = %q, want empty", out)
	}
}

func TestSystemOperationReadPathUsesAMUXHomeWithNilExecutor(t *testing.T) {
	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)

	cmd := newJSONCommandTestRoot(newSystemCmd())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"system", "operation", "list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("operation list with default lifecycle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(amuxHome, "system", "operations.db")); err != nil {
		t.Fatalf("journal was not opened below AMUX_HOME: %v", err)
	}
	var got system.Snapshot
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode operation list: %v\n%s", err, out.String())
	}
	if got.Scope.Kind != system.ScopeOperations {
		t.Fatalf("scope = %#v", got.Scope)
	}
}

func TestSystemStatusLegacyInspectorProjectsOnlySystemFields(t *testing.T) {
	report := cliStatusReport{
		Version:      "v1.2.3",
		ConfigPath:   "/tmp/amux/config.toml",
		ConfigExists: true,
		AmuxDir:      "/tmp/amux",
		RuntimeID:    "local-runtime",
		RuntimeName:  "box-a",
		Runtime:      serviceReport{State: "running", SocketOK: true},
		Hub:          serviceReport{State: "reachable", Addr: "127.0.0.1:7100"},
		HubTargets: targetSummary{
			Total:           2,
			Sessions:        5,
			RunningSessions: 2,
			Runtimes:        []targetRuntimeSummary{{ID: "local-runtime"}, {ID: "remote-runtime"}},
		},
		LocalSessions: sessionSummary{Total: 3, Running: 1, Archived: 1, Other: 1},
		Tasks:         taskSummary{Total: 4, Running: 1, Terminal: 2, Other: 1},
		Schedules:     scheduleSummary{Total: 3, Enabled: 2, Disabled: 1},
		MCP:           mcpStatus{Enabled: true, AutoInject: true, Backend: "hub 127.0.0.1:7100", BackendOK: true},
		LocalServices: localServicesStatus{Processes: []trackedProcess{{Role: trackedProcessRoleMCP}, {Role: trackedProcessRoleGUI, SessionID: "session-a"}}},
		Feishu:        &pb.FeishuStatusResponse{Configured: true, BridgeDbHealthy: true},
		FeishuBots:    []*pb.FeishuBot{{Configured: true}, {Configured: false}},
	}
	snapshot := legacySystemSnapshot(report)
	if snapshot.State != system.StateHealthy || snapshot.Version != "v1.2.3" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Summary == nil || snapshot.Summary.Installation == nil || snapshot.Summary.Workloads == nil || snapshot.Summary.Integrations == nil {
		t.Fatalf("summary missing: %#v", snapshot.Summary)
	}
	if got := snapshot.Summary.Installation; got.Home != "/tmp/amux" || got.ConfigPath != "/tmp/amux/config.toml" || !got.ConfigExists || got.Mode != "standalone" || got.HubAddress != "127.0.0.1:7100" || got.RuntimeID != "local-runtime" || got.RuntimeName != "box-a" {
		t.Fatalf("installation summary = %#v", got)
	}
	if got := snapshot.Summary.Workloads; got.LocalSessions.Total != 3 || got.LocalSessions.Running != 1 || got.TargetSessions.Total != 5 || got.Tasks.Total != 4 || got.Schedules.Enabled != 2 {
		t.Fatalf("workload summary = %#v", got)
	}
	if got := snapshot.Summary.Integrations; !got.Feishu.Configured || got.Feishu.ProfilesConfigured != 1 || got.Feishu.ProfilesTotal != 2 || !got.MCP.Enabled || got.MCP.State != "ok" || got.LocalServices.Total != 2 || got.LocalServices.SessionOwned != 1 {
		t.Fatalf("integration summary = %#v", got)
	}
	if len(snapshot.Components) != 3 || snapshot.Components[0] != (system.Component{Name: "hub", State: "reachable", Detail: "127.0.0.1:7100"}) {
		t.Fatalf("components = %#v", snapshot.Components)
	}
	for _, component := range snapshot.Components {
		if strings.Contains(strings.ToLower(component.Name), "feishu") || strings.Contains(strings.ToLower(component.Name), "mcp") {
			t.Fatalf("system snapshot leaked integration component: %#v", component)
		}
	}
	for _, check := range snapshot.Checks {
		if !check.Required || check.State != "pass" {
			t.Fatalf("healthy required check = %#v", check)
		}
	}
}

func TestSystemStatusLegacyInspectorRequiresExpectedRuntimeRegistration(t *testing.T) {
	for _, tt := range []struct {
		name     string
		runtimes []targetRuntimeSummary
	}{
		{name: "zero targets"},
		{name: "only another runtime", runtimes: []targetRuntimeSummary{{ID: "remote-runtime"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report := cliStatusReport{
				RuntimeID:     "local-runtime",
				Runtime:       serviceReport{State: "running", SocketOK: true},
				Hub:           serviceReport{State: "reachable"},
				HubTargets:    targetSummary{Total: len(tt.runtimes), Runtimes: tt.runtimes},
				LocalSessions: sessionSummary{},
			}
			snapshot := legacySystemSnapshot(report)
			if snapshot.State != system.StateDegraded {
				t.Fatalf("state = %s, want degraded", snapshot.State)
			}
			component := systemComponentByName(t, snapshot.Components, "targets")
			if component.State != "unavailable" || !strings.Contains(component.Detail, `expected runtime "local-runtime" not registered`) {
				t.Fatalf("targets component = %#v", component)
			}
			check := systemCheckByName(t, snapshot.Checks, "targets")
			if check.State != "fail" || !check.Required || !strings.Contains(check.Message, `expected runtime "local-runtime" not registered`) {
				t.Fatalf("targets check = %#v", check)
			}
		})
	}
}

func TestSystemStatusLegacyInspectorStateMapping(t *testing.T) {
	tests := []struct {
		name   string
		report cliStatusReport
		want   system.State
	}{
		{
			name: "stopped",
			report: cliStatusReport{
				Runtime:       serviceReport{State: "not running"},
				Hub:           serviceReport{State: "configured"},
				LocalSessions: sessionSummary{Error: "runtime socket missing"},
			},
			want: system.StateStopped,
		},
		{
			name: "partial",
			report: cliStatusReport{
				Runtime:       serviceReport{State: "not running"},
				Hub:           serviceReport{State: "reachable"},
				LocalSessions: sessionSummary{Error: "runtime socket missing"},
			},
			want: system.StateDegraded,
		},
		{
			name: "stopped with invalid config",
			report: cliStatusReport{
				ConfigError:   "decode config: invalid value",
				Runtime:       serviceReport{State: "not running"},
				Hub:           serviceReport{State: "configured"},
				LocalSessions: sessionSummary{Error: "runtime socket missing"},
			},
			want: system.StateDegraded,
		},
		{
			name: "error",
			report: cliStatusReport{
				Runtime:       serviceReport{State: "running", SocketOK: true},
				Hub:           serviceReport{State: "reachable"},
				HubTargets:    targetSummary{Error: "list targets failed"},
				LocalSessions: sessionSummary{},
			},
			want: system.StateDegraded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := legacySystemSnapshot(tt.report).State; got != tt.want {
				t.Fatalf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSystemStatusLegacyInspectorDoesNotHideStoppedErrors(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*cliStatusReport)
	}{
		{name: "hub error", mutate: func(report *cliStatusReport) { report.Hub.Error = "connection failed" }},
		{name: "runtime error", mutate: func(report *cliStatusReport) { report.Runtime.Error = "runtime probe failed" }},
		{name: "targets error", mutate: func(report *cliStatusReport) { report.HubTargets.Error = "list targets failed" }},
		{name: "runtime API error", mutate: func(report *cliStatusReport) { report.LocalSessions.Error = "runtime API failed" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report := cliStatusReport{
				Runtime:       serviceReport{State: "not running"},
				Hub:           serviceReport{State: "configured"},
				LocalSessions: sessionSummary{Error: "runtime socket missing"},
			}
			tt.mutate(&report)
			if got := legacySystemSnapshot(report).State; got != system.StateDegraded {
				t.Fatalf("state = %s, want degraded for real error", got)
			}
		})
	}
}

func TestSystemStatusLegacyInspectorRecognizesExpectedStoppedLocalDialFailure(t *testing.T) {
	addr := unusedLocalTCPAddr(t)
	dialErr := "dial tcp " + addr + ": connect: connection refused"
	report := cliStatusReport{
		Runtime: serviceReport{State: "not running"},
		Hub: serviceReport{
			State: "configured",
			Addr:  addr,
			Error: dialErr,
		},
		HubTargets:    targetSummary{Error: dialErr},
		LocalSessions: sessionSummary{Error: "runtime socket missing"},
	}
	if got := legacySystemSnapshot(report).State; got != system.StateStopped {
		t.Fatalf("state = %s, want stopped for expected local no-listener failure", got)
	}
}

func TestSystemStatusLegacyInspectorRejectsUnexpectedStoppedHubFailures(t *testing.T) {
	freeLocalAddr := unusedLocalTCPAddr(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	listenerAddr := listener.Addr().String()

	for _, tt := range []struct {
		name      string
		hubState  string
		hubAddr   string
		hubError  string
		targetErr string
	}{
		{
			name:      "remote hub",
			hubState:  "configured",
			hubAddr:   "192.0.2.10:7100",
			hubError:  "dial failed",
			targetErr: "dial failed",
		},
		{
			name:      "mismatched probe errors",
			hubState:  "configured",
			hubAddr:   freeLocalAddr,
			hubError:  "dial failed",
			targetErr: "list targets failed",
		},
		{
			name:      "invalid local address",
			hubState:  "configured",
			hubAddr:   "127.0.0.1:99999",
			hubError:  "dial failed",
			targetErr: "dial failed",
		},
		{
			name:      "stale pid",
			hubState:  "stale pid",
			hubAddr:   freeLocalAddr,
			hubError:  "dial failed",
			targetErr: "dial failed",
		},
		{
			name:      "local listener still active",
			hubState:  "configured",
			hubAddr:   listenerAddr,
			hubError:  "TLS handshake failed",
			targetErr: "TLS handshake failed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report := cliStatusReport{
				Runtime: serviceReport{State: "not running"},
				Hub: serviceReport{
					State: tt.hubState,
					Addr:  tt.hubAddr,
					Error: tt.hubError,
				},
				HubTargets:    targetSummary{Error: tt.targetErr},
				LocalSessions: sessionSummary{Error: "runtime socket missing"},
			}
			if got := legacySystemSnapshot(report).State; got != system.StateDegraded {
				t.Fatalf("state = %s, want degraded", got)
			}
		})
	}
}

func unusedLocalTCPAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func systemComponentByName(t *testing.T, components []system.Component, name string) system.Component {
	t.Helper()
	for _, component := range components {
		if component.Name == name {
			return component
		}
	}
	t.Fatalf("component %q not found in %#v", name, components)
	return system.Component{}
}

func systemCheckByName(t *testing.T, checks []system.Check, name string) system.Check {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %q not found in %#v", name, checks)
	return system.Check{}
}

func runSystemCommand(t *testing.T, lifecycle system.Lifecycle, closeFn func() error, args ...string) (string, string, error) {
	t.Helper()
	if closeFn == nil {
		closeFn = func() error { return nil }
	}
	systemCmd := newSystemCmdWithOpener(func(io.Writer) (system.Lifecycle, func() error, error) {
		return lifecycle, closeFn, nil
	})
	cmd := newJSONCommandTestRoot(systemCmd)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"system"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func useIsolatedSystemConfig(t *testing.T) {
	t.Helper()
	t.Setenv("AMUX_HOME", t.TempDir())
}

func healthySystemSnapshot() system.Snapshot {
	return system.Snapshot{
		State:   system.StateHealthy,
		Version: "v1.2.3",
		Components: []system.Component{
			{Name: "hub", State: "reachable"},
			{Name: "runtime", State: "reachable"},
			{Name: "targets", State: "available", Detail: "2 connected"},
		},
		Checks: []system.Check{
			{Name: "hub", State: "pass", Required: true},
			{Name: "runtime", State: "pass", Required: true},
		},
	}
}

func testSystemOperation(id system.OperationID, state system.OperationState, created time.Time) system.Operation {
	return system.Operation{
		ID:        id,
		Action:    system.ActionRestart,
		Scope:     system.CurrentScope(),
		State:     state,
		CreatedAt: created.UTC(),
		UpdatedAt: created.UTC(),
	}
}

func fakeLegacySystemMutationHooks(calls *[]string, hubAddress string) legacySystemMutationHooks {
	hooks := legacySystemMutationHooks{
		hubAddress: func() (string, error) { return hubAddress, nil },
		runtimeService: func() managedService {
			return managedService{Name: "runtime"}
		},
		hubService: func() managedService {
			return managedService{Name: "hub"}
		},
		autoSyncService: func() managedService {
			return managedService{Name: "agent-pack-autosync"}
		},
		serviceRunning: func(service managedService) bool {
			return service.Name == "agent-pack-autosync"
		},
		startService: func(service managedService) error {
			*calls = append(*calls, "start "+service.Name)
			return nil
		},
		stopService: func(service managedService) error {
			*calls = append(*calls, "stop "+service.Name)
			return nil
		},
		guardRuntimeStop: func(context.Context, bool, string) error {
			*calls = append(*calls, "guard runtime")
			return nil
		},
		guardHubStop: func(context.Context, bool) error {
			*calls = append(*calls, "guard hub")
			return nil
		},
	}
	hooks.runInit = func(out io.Writer, opts initOptions) error {
		return runInitWithHooks(out, opts, hooks)
	}
	return hooks
}

type fakeSystemLifecycle struct {
	snapshot      system.Snapshot
	inspectErr    error
	planErr       error
	planID        system.OperationID
	applyID       system.OperationID
	applyErr      error
	applyFunc     func(context.Context, system.ChangePlan) (system.OperationID, error)
	operation     system.Operation
	getErr        error
	cancelResult  system.CancelResult
	cancelErr     error
	inspectScopes []system.Scope
	getIDs        []system.OperationID
	cancelIDs     []system.OperationID
	planRequests  []system.ChangeRequest
	applyPlans    []system.ChangePlan
	mutationCalls int
}

func (f *fakeSystemLifecycle) Inspect(_ context.Context, scope system.Scope) (system.Snapshot, error) {
	f.inspectScopes = append(f.inspectScopes, scope)
	if f.inspectErr != nil {
		return system.Snapshot{}, f.inspectErr
	}
	snapshot := f.snapshot
	snapshot.Schema = system.SnapshotSchema
	snapshot.Scope = scope
	return snapshot, nil
}

func (f *fakeSystemLifecycle) Plan(_ context.Context, request system.ChangeRequest) (system.ChangePlan, error) {
	f.planRequests = append(f.planRequests, request)
	if f.planErr != nil {
		return system.ChangePlan{}, f.planErr
	}
	operationID := f.planID
	if operationID == "" {
		operationID = f.applyID
	}
	before := f.snapshot
	before.Schema = system.SnapshotSchema
	before.Scope = request.Scope
	return system.ChangePlan{OperationID: operationID, Request: request, Before: before, CreatedAt: time.Unix(100, 0).UTC()}, nil
}

func (f *fakeSystemLifecycle) Apply(ctx context.Context, plan system.ChangePlan) (system.OperationID, error) {
	f.mutationCalls++
	f.applyPlans = append(f.applyPlans, plan)
	if f.applyFunc != nil {
		return f.applyFunc(ctx, plan)
	}
	return f.applyID, f.applyErr
}

func (f *fakeSystemLifecycle) GetOperation(_ context.Context, id system.OperationID) (system.Operation, error) {
	f.getIDs = append(f.getIDs, id)
	return f.operation, f.getErr
}

func (f *fakeSystemLifecycle) Cancel(_ context.Context, id system.OperationID) (system.CancelResult, error) {
	f.cancelIDs = append(f.cancelIDs, id)
	return f.cancelResult, f.cancelErr
}
