package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/system"
)

func writeSystemSnapshot(out io.Writer, snapshot system.Snapshot, jsonOutput bool) error {
	if jsonOutput {
		return writeSystemJSON(out, snapshot)
	}
	st := styleFor(out)
	fmt.Fprintf(out, "state: %s\n", st.state(string(snapshot.State)))
	if snapshot.Version != "" {
		fmt.Fprintf(out, "version: %s\n", snapshot.Version)
	}
	if snapshot.Update != nil && snapshot.Update.Available {
		fmt.Fprintf(out, "update: available (latest %s", snapshot.Update.LatestVersion)
		if !snapshot.Update.CheckedAt.IsZero() {
			fmt.Fprintf(out, ", checked %s", snapshot.Update.CheckedAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(out, ")")
	}
	fmt.Fprintf(out, "scope: %s\n", snapshot.Scope.Kind)
	writeSystemSummary(out, st, snapshot.Summary)
	if len(snapshot.Components) > 0 {
		fmt.Fprintln(out, st.heading("components:"))
		for _, component := range snapshot.Components {
			fmt.Fprintf(out, "  %s: %s", component.Name, st.state(component.State))
			if component.Detail != "" {
				fmt.Fprintf(out, " (%s)", statusOneLine(component.Detail))
			}
			fmt.Fprintln(out)
		}
	}
	if len(snapshot.Checks) > 0 {
		fmt.Fprintln(out, st.heading("checks:"))
		for _, check := range snapshot.Checks {
			fmt.Fprintf(out, "  %s: %s", check.Name, st.state(check.State))
			if check.Required {
				fmt.Fprint(out, " [required]")
			}
			if check.Message != "" {
				fmt.Fprintf(out, " (%s)", statusOneLine(check.Message))
			}
			fmt.Fprintln(out)
		}
	}
	if len(snapshot.NextActions) > 0 {
		fmt.Fprintln(out, st.heading("next_actions:"))
		for _, action := range snapshot.NextActions {
			fmt.Fprintf(out, "  - %s", action.Command)
			if action.Reason != "" {
				fmt.Fprintf(out, " (%s)", statusOneLine(action.Reason))
			}
			if action.RequiresConfirmation {
				fmt.Fprint(out, " [requires confirmation]")
			}
			fmt.Fprintln(out)
		}
	}
	if snapshot.ActiveOperation != nil {
		fmt.Fprintf(out, "active_operation: %s (%s)\n", snapshot.ActiveOperation.ID, st.state(string(snapshot.ActiveOperation.State)))
	}
	return nil
}

func writeSystemSummary(out io.Writer, st cliStyle, summary *system.SnapshotSummary) {
	if summary == nil {
		return
	}
	if summary.Installation != nil {
		fmt.Fprintln(out, st.heading("installation:"))
		installation := summary.Installation
		if installation.Home != "" {
			fmt.Fprintf(out, "  home: %s\n", installation.Home)
		}
		if installation.ConfigPath != "" {
			fmt.Fprintf(out, "  config: %s (%s)\n", installation.ConfigPath, st.state(existsWord(installation.ConfigExists)))
		}
		if installation.Mode != "" {
			fmt.Fprintf(out, "  mode: %s\n", installation.Mode)
		}
		if installation.HubAddress != "" {
			fmt.Fprintf(out, "  hub_addr: %s\n", installation.HubAddress)
		}
		if installation.RuntimeID != "" || installation.RuntimeName != "" {
			fmt.Fprintf(out, "  runtime: %s / %s\n", valueOrDash(installation.RuntimeID), valueOrDash(installation.RuntimeName))
		}
	}
	if summary.Workloads != nil {
		fmt.Fprintln(out, st.heading("workloads:"))
		writeSessionWorkloadSummary(out, st, "sessions", "local", summary.Workloads.LocalSessions)
		writeSessionWorkloadSummary(out, st, "target_sessions", "", summary.Workloads.TargetSessions)
		writeTaskWorkloadSummary(out, st, summary.Workloads.Tasks)
		writeScheduleWorkloadSummary(out, st, summary.Workloads.Schedules)
	}
	if summary.Integrations != nil {
		fmt.Fprintln(out, st.heading("integrations:"))
		writeFeishuIntegrationSummary(out, st, summary.Integrations.Feishu)
		writeMCPIntegrationSummary(out, st, summary.Integrations.MCP)
		writeLocalServiceSummary(out, st, summary.Integrations.LocalServices)
	}
	if summary.Operations != nil {
		fmt.Fprintln(out, st.heading("operations:"))
		writeOperationSummary(out, st, summary.Operations)
	}
}

func writeSessionWorkloadSummary(out io.Writer, st cliStyle, label, scope string, summary system.SessionWorkloadSummary) {
	if summary.Error != "" {
		fmt.Fprintf(out, "  %s: %s (%s)\n", label, st.state("unavailable"), statusOneLine(summary.Error))
		return
	}
	scopeText := ""
	if scope != "" {
		scopeText = " " + scope
	}
	fmt.Fprintf(out, "  %s: %d%s total, %d running", label, summary.Total, scopeText, summary.Running)
	if summary.Archived > 0 {
		fmt.Fprintf(out, ", %d archived", summary.Archived)
	}
	if summary.Other > 0 {
		fmt.Fprintf(out, ", %d other", summary.Other)
	}
	if len(summary.Agents) > 0 {
		fmt.Fprintf(out, " (%s)", formatAgentCounts(summary.Agents))
	}
	fmt.Fprintln(out)
}

func writeTaskWorkloadSummary(out io.Writer, st cliStyle, summary system.TaskWorkloadSummary) {
	if summary.Error != "" {
		fmt.Fprintf(out, "  tasks: %s (%s)\n", st.state("unavailable"), statusOneLine(summary.Error))
		return
	}
	fmt.Fprintf(out, "  tasks: %d total, %d running, %d terminal", summary.Total, summary.Running, summary.Terminal)
	if summary.Other > 0 {
		fmt.Fprintf(out, ", %d other", summary.Other)
	}
	fmt.Fprintln(out)
}

func writeScheduleWorkloadSummary(out io.Writer, st cliStyle, summary system.ScheduleWorkloadSummary) {
	if summary.Error != "" {
		fmt.Fprintf(out, "  schedules: %s (%s)\n", st.state("unavailable"), statusOneLine(summary.Error))
		return
	}
	fmt.Fprintf(out, "  schedules: %d total, %d enabled, %d disabled\n", summary.Total, summary.Enabled, summary.Disabled)
}

func writeFeishuIntegrationSummary(out io.Writer, st cliStyle, summary system.FeishuIntegrationSummary) {
	state := "not_configured"
	if summary.Configured {
		state = "configured"
	}
	fmt.Fprintf(out, "  feishu: %s", st.state(state))
	if summary.ProfilesTotal > 0 {
		fmt.Fprintf(out, ", profiles %d/%d", summary.ProfilesConfigured, summary.ProfilesTotal)
	}
	if summary.BridgeDBHealthy {
		fmt.Fprintf(out, ", bridge_db %s", st.state("ok"))
	}
	if summary.Error != "" {
		fmt.Fprintf(out, " (%s)", statusOneLine(summary.Error))
	}
	fmt.Fprintln(out)
}

func writeMCPIntegrationSummary(out io.Writer, st cliStyle, summary system.MCPIntegrationSummary) {
	enabled := "disabled"
	if summary.Enabled {
		enabled = "enabled"
	}
	autoInject := "disabled"
	if summary.AutoInject {
		autoInject = "enabled"
	}
	fmt.Fprintf(out, "  mcp: %s, auto_inject %s", st.state(enabled), st.state(autoInject))
	if summary.Backend != "" {
		fmt.Fprintf(out, ", backend %s", summary.Backend)
	}
	if summary.State != "" {
		fmt.Fprintf(out, ", %s", st.state(summary.State))
	}
	if summary.Error != "" {
		fmt.Fprintf(out, " (%s)", statusOneLine(summary.Error))
	}
	fmt.Fprintln(out)
}

func writeLocalServiceSummary(out io.Writer, st cliStyle, summary system.LocalServiceSummary) {
	if summary.Error != "" {
		fmt.Fprintf(out, "  local_services: %s (%s)\n", st.state("unavailable"), statusOneLine(summary.Error))
		return
	}
	fmt.Fprintf(out, "  local_services: %d total, %d restartable, %d session-owned\n", summary.Total, summary.Servers, summary.SessionOwned)
}

func writeOperationSummary(out io.Writer, st cliStyle, summary *system.OperationSummary) {
	if summary.Active == nil {
		fmt.Fprintln(out, "  active: none")
	} else {
		fmt.Fprintf(out, "  active: %s\n", formatOperationBrief(st, summary.Active))
	}
	if summary.LastFailed == nil {
		fmt.Fprintln(out, "  last_failed: none")
	} else {
		fmt.Fprintf(out, "  last_failed: %s\n", formatOperationBrief(st, summary.LastFailed))
	}
}

func formatOperationBrief(st cliStyle, brief *system.OperationBrief) string {
	if brief == nil {
		return "none"
	}
	text := strings.TrimSpace(fmt.Sprintf("%s %s %s", brief.ID, brief.Action, st.state(string(brief.State))))
	if brief.Error != "" {
		text += " (" + statusOneLine(brief.Error) + ")"
	}
	return text
}

func writeSystemOperationList(out io.Writer, snapshot system.Snapshot, jsonOutput bool) error {
	if jsonOutput {
		operations := snapshot.Operations
		if operations == nil {
			operations = []system.Operation{}
		}
		return writeSystemJSON(out, struct {
			Schema     string             `json:"schema"`
			Scope      system.Scope       `json:"scope"`
			State      system.State       `json:"state"`
			Operations []system.Operation `json:"operations"`
		}{
			Schema:     snapshot.Schema,
			Scope:      snapshot.Scope,
			State:      snapshot.State,
			Operations: operations,
		})
	}
	if len(snapshot.Operations) == 0 {
		fmt.Fprintln(out, "No system operations.")
		return nil
	}
	fmt.Fprintln(out, "ID\tACTION\tSTATE\tCREATED_AT")
	for _, operation := range snapshot.Operations {
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", operation.ID, operation.Action, operation.State, formatSystemTime(operation.CreatedAt))
	}
	return nil
}

func writeSystemOperation(out io.Writer, operation system.Operation, jsonOutput bool) error {
	operation = operationWithDisplayNextActions(operation)
	if jsonOutput {
		return writeSystemJSON(out, struct {
			Schema string `json:"schema"`
			system.Operation
		}{Schema: "amux.system.operation.v1", Operation: operation})
	}
	fmt.Fprintf(out, "id: %s\n", operation.ID)
	fmt.Fprintf(out, "action: %s\n", operation.Action)
	fmt.Fprintf(out, "scope: %s\n", operation.Scope.Kind)
	fmt.Fprintf(out, "state: %s\n", operation.State)
	fmt.Fprintf(out, "created_at: %s\n", formatSystemTime(operation.CreatedAt))
	fmt.Fprintf(out, "updated_at: %s\n", formatSystemTime(operation.UpdatedAt))
	if operation.FinishedAt != nil {
		fmt.Fprintf(out, "finished_at: %s\n", formatSystemTime(*operation.FinishedAt))
	}
	if operation.Error != "" {
		fmt.Fprintf(out, "error: %s\n", statusOneLine(operation.Error))
	}
	if len(operation.NextActions) > 0 {
		fmt.Fprintln(out, "next_actions:")
		for _, action := range operation.NextActions {
			fmt.Fprintf(out, "  - %s", action.Command)
			if action.Reason != "" {
				fmt.Fprintf(out, " (%s)", statusOneLine(action.Reason))
			}
			fmt.Fprintln(out)
		}
	}
	return nil
}

func operationWithDisplayNextActions(operation system.Operation) system.Operation {
	if len(operation.NextActions) > 0 {
		return operation
	}
	switch operation.State {
	case system.OperationFailed, system.OperationRolledBack:
		operation.NextActions = []system.SuggestedAction{{
			Command: "amux system doctor",
			Reason:  "inspect the installation after a failed lifecycle operation",
		}}
	case system.OperationPending, system.OperationDraining, system.OperationStaging:
		operation.NextActions = []system.SuggestedAction{{
			Command: "amux system operation cancel " + string(operation.ID),
			Reason:  "cancel before the operation reaches an irreversible phase",
		}}
	case system.OperationApplying, system.OperationVerifying:
		operation.NextActions = []system.SuggestedAction{{
			Command: "amux system operation show " + string(operation.ID),
			Reason:  "operation is applying; wait and re-check status",
		}}
	}
	return operation
}

func writeSystemCancelResult(out io.Writer, result system.CancelResult, jsonOutput bool) error {
	if jsonOutput {
		return writeSystemJSON(out, struct {
			Schema string `json:"schema"`
			system.CancelResult
		}{Schema: "amux.system.operation.cancel.v1", CancelResult: result})
	}
	if result.Accepted {
		fmt.Fprintf(out, "cancelled %s\n", result.OperationID)
		return nil
	}
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		reason = "cancellation was rejected"
	}
	fmt.Fprintf(out, "not cancelled %s: %s (state %s)\n", result.OperationID, statusOneLine(reason), result.State)
	return nil
}

func writeSystemPlanPreview(out io.Writer, preview system.PlanPreview, jsonOutput bool) error {
	if jsonOutput {
		return writeSystemJSON(out, preview)
	}
	fmt.Fprintf(out, "plan: %s\n", preview.Action)
	fmt.Fprintf(out, "scope: %s\n", preview.Scope.Kind)
	fmt.Fprintf(out, "will_restart_hub: %t\n", preview.Impact.WillRestartHub)
	fmt.Fprintf(out, "will_restart_runtime: %t\n", preview.Impact.WillRestartRuntime)
	if len(preview.Impact.MayInterruptSessions) > 0 {
		fmt.Fprintf(out, "may_interrupt_sessions: %s\n", strings.Join(preview.Impact.MayInterruptSessions, ", "))
	}
	if preview.Impact.RequiresForce {
		fmt.Fprintln(out, "requires_force: true")
	}
	if len(preview.NextActions) > 0 {
		fmt.Fprintf(out, "apply: %s\n", preview.NextActions[0].Command)
	}
	return nil
}

func writeSystemJSON(out io.Writer, value any) error {
	if err := printJSONLine(out, value); err != nil {
		return fmt.Errorf("encode system JSON: %w", err)
	}
	return nil
}

func formatSystemTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339Nano)
}
