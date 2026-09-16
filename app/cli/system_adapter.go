package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/amuxos/amux/internal/system"
)

func openSystemLifecycle(out io.Writer) (system.Lifecycle, func() error, error) {
	return openSystemLifecycleWithHooks(out, defaultLegacySystemMutationHooks())
}

func openSystemLifecycleWithHooks(out io.Writer, hooks legacySystemMutationHooks) (system.Lifecycle, func() error, error) {
	journal, err := system.OpenJournal(system.DefaultJournalPath())
	if err != nil {
		return nil, nil, err
	}
	lifecycle := system.New(system.Dependencies{
		Journal: journal,
		Inspect: legacySystemInspector,
		Execute: legacySystemExecutor(out, hooks),
	})
	return lifecycle, journal.Close, nil
}

type legacySystemMutationHooks struct {
	runInit          func(io.Writer, initOptions) error
	hubAddress       func() (string, error)
	runtimeService   func() managedService
	hubService       func() managedService
	autoSyncService  func() managedService
	serviceRunning   func(managedService) bool
	startService     func(managedService) error
	stopService      func(managedService) error
	guardRuntimeStop func(context.Context, bool, string) error
	guardHubStop     func(context.Context, bool) error
}

func defaultLegacySystemMutationHooks() legacySystemMutationHooks {
	return legacySystemMutationHooks{
		runInit:         runInit,
		hubAddress:      validatedConfiguredHubAddress,
		runtimeService:  runtimeService,
		hubService:      func() managedService { return hubService() },
		autoSyncService: agentPackAutoSyncService,
		serviceRunning: func(service managedService) bool {
			_, ok := readRunningPID(service.PIDPath)
			return ok
		},
		startService: startIfNotRunning,
		stopService: func(service managedService) error {
			return service.Stop()
		},
		guardRuntimeStop: func(ctx context.Context, force bool, guidance string) error {
			sessions, err := localRunningSessions(ctx)
			if err != nil {
				return nil
			}
			return guardRuntimeStopWithGuidance(force, sessions, guidance)
		},
		guardHubStop: func(ctx context.Context, force bool) error {
			return guardHubStop(force, collectHubStopImpacts(ctx))
		},
	}
}

func legacySystemExecutor(out io.Writer, hooks legacySystemMutationHooks) system.Executor {
	return func(ctx context.Context, plan system.ChangePlan) error {
		request := plan.Request
		if request.Scope != system.CurrentScope() {
			return fmt.Errorf("legacy system executor does not support scope %q", request.Scope.Kind)
		}
		switch request.Action {
		case system.ActionSetup:
			if request.Setup == nil {
				return fmt.Errorf("setup action requires a setup specification")
			}
			return hooks.runInit(out, initOptions{
				profile: request.Setup.Profile,
				agent:   request.Setup.Agent,
				hubAddr: request.Setup.HubAddress,
				noStart: request.Setup.NoStart,
			})
		case system.ActionStart:
			manageHub, err := resolveLegacySystemTopology(hooks)
			if err != nil {
				return err
			}
			restartRuntime := legacySystemStartNeedsRuntimeRestart(plan.Before, manageHub)
			return startLegacySystemServicesWithRuntimeRestart(ctx, manageHub, restartRuntime, request.Force, "amux system restart --force", hooks)
		case system.ActionStop:
			manageHub, err := resolveLegacySystemTopology(hooks)
			if err != nil {
				return err
			}
			if err := guardLegacySystemStop(ctx, request.Force, manageHub, "amux system stop --force", hooks); err != nil {
				return err
			}
			return stopLegacySystemServices(manageHub, hooks)
		case system.ActionRestart:
			manageHub, err := resolveLegacySystemTopology(hooks)
			if err != nil {
				return err
			}
			if err := guardLegacySystemStop(ctx, request.Force, manageHub, "amux system restart --force", hooks); err != nil {
				return err
			}
			return restartLegacySystemServices(manageHub, hooks)
		default:
			return fmt.Errorf("legacy system executor does not support action %q", request.Action)
		}
	}
}

// restartLegacySystemServices preserves a running Agent Pack autosync worker
// across a system restart. The worker is a separate process from amuxd and
// amuxlet, so leaving it alive would keep the old executable mapped after an
// update while restarting it unconditionally would override an operator's
// explicit stopped state.
func restartLegacySystemServices(manageHub bool, hooks legacySystemMutationHooks) error {
	autoSync := hooks.autoSyncService()
	refreshAutoSync := hooks.serviceRunning(autoSync)
	if refreshAutoSync {
		if err := hooks.stopService(autoSync); err != nil {
			return fmt.Errorf("stop Agent Pack auto sync: %w", err)
		}
	}
	restoreAutoSync := func(cause error) error {
		if !refreshAutoSync {
			return cause
		}
		if err := hooks.startService(autoSync); err != nil {
			return errors.Join(cause, fmt.Errorf("restore Agent Pack auto sync: %w", err))
		}
		return cause
	}
	if err := stopLegacySystemServices(manageHub, hooks); err != nil {
		return restoreAutoSync(err)
	}
	if err := startLegacySystemServices(manageHub, hooks); err != nil {
		return restoreAutoSync(err)
	}
	if refreshAutoSync {
		if err := hooks.startService(autoSync); err != nil {
			return fmt.Errorf("start Agent Pack auto sync: %w", err)
		}
	}
	return nil
}

func resolveLegacySystemTopology(hooks legacySystemMutationHooks) (bool, error) {
	address, err := hooks.hubAddress()
	if err != nil {
		return false, fmt.Errorf("resolve system topology: %w", err)
	}
	return legacySystemManagesHub(address), nil
}

func startLegacySystemServices(manageHub bool, hooks legacySystemMutationHooks) error {
	return startLegacySystemServicesWithRuntimeRestart(context.Background(), manageHub, false, false, "amux system restart --force", hooks)
}

func startLegacySystemServicesWithRuntimeRestart(ctx context.Context, manageHub, restartRuntime, force bool, runtimeGuardGuidance string, hooks legacySystemMutationHooks) error {
	if manageHub {
		if err := hooks.startService(hooks.hubService()); err != nil {
			return fmt.Errorf("start hub: %w", err)
		}
	}
	if restartRuntime {
		if err := hooks.guardRuntimeStop(ctx, force, runtimeGuardGuidance); err != nil {
			return &system.BlockedError{Err: fmt.Errorf("runtime is not registered with hub and must restart: %w", err)}
		}
		if err := hooks.stopService(hooks.runtimeService()); err != nil {
			return fmt.Errorf("stop stale runtime: %w", err)
		}
	}
	if err := hooks.startService(hooks.runtimeService()); err != nil {
		return fmt.Errorf("start runtime: %w", err)
	}
	return nil
}

func legacySystemStartNeedsRuntimeRestart(before system.Snapshot, manageHub bool) bool {
	runtimeReachable := false
	for _, component := range before.Components {
		if component.Name == "runtime" {
			runtimeReachable = component.State == "reachable"
			break
		}
	}
	if !runtimeReachable {
		return false
	}

	targetsFailed := false
	hubPassed := false
	for _, check := range before.Checks {
		switch check.Name {
		case "targets":
			targetsFailed = check.State == "fail"
		case "hub":
			hubPassed = check.State == "pass"
		}
	}
	return targetsFailed && (manageHub || hubPassed)
}

func guardLegacySystemStop(ctx context.Context, force, manageHub bool, runtimeGuardGuidance string, hooks legacySystemMutationHooks) error {
	if err := hooks.guardRuntimeStop(ctx, force, runtimeGuardGuidance); err != nil {
		return &system.BlockedError{Err: err}
	}
	if manageHub {
		if err := hooks.guardHubStop(ctx, force); err != nil {
			return &system.BlockedError{Err: err}
		}
	}
	return nil
}

func stopLegacySystemServices(manageHub bool, hooks legacySystemMutationHooks) error {
	if err := hooks.stopService(hooks.runtimeService()); err != nil {
		return fmt.Errorf("stop runtime: %w", err)
	}
	if manageHub {
		if err := hooks.stopService(hooks.hubService()); err != nil {
			return fmt.Errorf("stop hub: %w", err)
		}
	}
	return nil
}

func legacySystemManagesHub(address string) bool {
	address = strings.TrimSpace(address)
	if address == "" {
		return true
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func legacySystemInspector(ctx context.Context, scope system.Scope) (system.Snapshot, error) {
	if scope.Kind != system.ScopeCurrent {
		return system.Snapshot{}, fmt.Errorf("legacy system inspector does not support scope %q", scope.Kind)
	}
	return legacySystemSnapshot(collectCLIStatus(ctx)), nil
}

func legacySystemSnapshot(report cliStatusReport) system.Snapshot {
	hubOK := legacyHubHealthy(report)
	runtimeOK := legacyRuntimeHealthy(report)
	targetsOK, targetDetail := legacyExpectedRuntimeRegistered(report)
	hubStopped := legacyHubStopped(report.Hub)
	runtimeStopped := legacyRuntimeStopped(report.Runtime)
	stoppedWithoutErrors := report.ConfigError == "" &&
		report.Runtime.Error == "" &&
		legacyStoppedHubErrorsIgnorable(report.Hub, report.HubTargets) &&
		legacyStoppedRuntimeAPIErrorIgnorable(report.LocalSessions.Error)

	state := system.StateDegraded
	switch {
	case hubOK && runtimeOK && targetsOK && report.ConfigError == "":
		state = system.StateHealthy
	case hubStopped && runtimeStopped && stoppedWithoutErrors:
		state = system.StateStopped
	}

	hubState := strings.TrimSpace(report.Hub.State)
	if hubState == "" {
		hubState = "unknown"
	}
	runtimeState := strings.TrimSpace(report.Runtime.State)
	if runtimeOK {
		runtimeState = "reachable"
	} else if runtimeStopped {
		runtimeState = "stopped"
	} else if runtimeState == "" {
		runtimeState = "unknown"
	}
	targetState := "unavailable"
	if targetsOK {
		targetState = "available"
	}
	if report.HubTargets.Error != "" {
		targetState = "unavailable"
		targetDetail = statusOneLine(report.HubTargets.Error)
	}

	components := []system.Component{
		{Name: "hub", State: hubState, Detail: legacyServiceDetail(report.Hub)},
		{Name: "runtime", State: runtimeState, Detail: legacyRuntimeDetail(report)},
		{Name: "targets", State: targetState, Detail: targetDetail},
	}
	checks := []system.Check{
		legacySystemCheck("config", report.ConfigError == "", report.ConfigError),
		legacySystemCheck("hub", hubOK, firstNonEmpty(report.Hub.Error, hubState)),
		legacySystemCheck("runtime", runtimeOK, firstNonEmpty(report.Runtime.Error, report.LocalSessions.Error, runtimeState)),
		legacySystemCheck("targets", targetsOK, targetDetail),
	}

	return system.Snapshot{
		State:       state,
		Version:     report.Version,
		Summary:     legacySystemSummary(report),
		Components:  components,
		Checks:      checks,
		NextActions: legacySystemNextActions(state, components, checks),
	}
}

func legacySystemSummary(report cliStatusReport) *system.SnapshotSummary {
	return &system.SnapshotSummary{
		Installation: &system.InstallationSummary{
			Home:         report.AmuxDir,
			ConfigPath:   report.ConfigPath,
			ConfigExists: report.ConfigExists,
			Mode:         legacySystemMode(report),
			HubAddress:   report.Hub.Addr,
			RuntimeID:    report.RuntimeID,
			RuntimeName:  report.RuntimeName,
		},
		Workloads: &system.WorkloadSummary{
			LocalSessions:  sessionWorkloadSummary(report.LocalSessions),
			TargetSessions: targetSessionWorkloadSummary(report.HubTargets),
			Tasks:          taskWorkloadSummary(report.Tasks),
			Schedules:      scheduleWorkloadSummary(report.Schedules),
		},
		Integrations: &system.IntegrationSummary{
			Feishu:        feishuIntegrationSummary(report),
			MCP:           mcpIntegrationSummary(report.MCP),
			LocalServices: localServiceSummary(report.LocalServices),
		},
	}
}

func legacySystemMode(report cliStatusReport) string {
	if report.Hub.Addr == "" || isLocalHubListenAddr(report.Hub.Addr) {
		return "standalone"
	}
	return "remote-worker"
}

func sessionWorkloadSummary(sum sessionSummary) system.SessionWorkloadSummary {
	return system.SessionWorkloadSummary{
		Total:    sum.Total,
		Running:  sum.Running,
		Archived: sum.Archived,
		Other:    sum.Other,
		Agents:   cloneSessionAgentCounts(sum.Agents),
		Error:    sum.Error,
	}
}

func cloneSessionAgentCounts(counts map[string]int) map[string]int {
	if counts == nil {
		return nil
	}
	cloned := make(map[string]int, len(counts))
	for name, count := range counts {
		cloned[name] = count
	}
	return cloned
}

func targetSessionWorkloadSummary(sum targetSummary) system.SessionWorkloadSummary {
	return system.SessionWorkloadSummary{
		Total:   sum.Sessions,
		Running: sum.RunningSessions,
		Error:   sum.Error,
	}
}

func taskWorkloadSummary(sum taskSummary) system.TaskWorkloadSummary {
	return system.TaskWorkloadSummary{
		Total:    sum.Total,
		Running:  sum.Running,
		Terminal: sum.Terminal,
		Other:    sum.Other,
		Error:    sum.Error,
	}
}

func scheduleWorkloadSummary(sum scheduleSummary) system.ScheduleWorkloadSummary {
	return system.ScheduleWorkloadSummary{
		Total:    sum.Total,
		Enabled:  sum.Enabled,
		Disabled: sum.Disabled,
		Error:    sum.Error,
	}
}

func feishuIntegrationSummary(report cliStatusReport) system.FeishuIntegrationSummary {
	summary := system.FeishuIntegrationSummary{
		Configured:         hasConfiguredFeishu(report),
		ProfilesTotal:      len(report.FeishuBots),
		ProfilesConfigured: countConfiguredFeishuBots(report.FeishuBots),
	}
	if report.Feishu != nil {
		summary.BridgeDBHealthy = report.Feishu.BridgeDbHealthy
		summary.Error = report.Feishu.Error
	}
	if report.FeishuBotsErr != "" && summary.Error == "" {
		summary.Error = report.FeishuBotsErr
	}
	return summary
}

func mcpIntegrationSummary(m mcpStatus) system.MCPIntegrationSummary {
	state := "disabled"
	if m.Enabled {
		state = "unavailable"
		if m.BackendOK {
			state = "ok"
		}
	}
	return system.MCPIntegrationSummary{
		Enabled:    m.Enabled,
		AutoInject: m.AutoInject,
		Backend:    m.Backend,
		State:      state,
		Error:      m.BackendError,
	}
}

func localServiceSummary(services localServicesStatus) system.LocalServiceSummary {
	restartable, sessionOwned := splitLocalServiceProcesses(services.Processes)
	return system.LocalServiceSummary{
		Total:        len(services.Processes),
		Servers:      len(restartable),
		SessionOwned: len(sessionOwned),
		Error:        services.Error,
	}
}

func legacySystemNextActions(state system.State, components []system.Component, checks []system.Check) []system.SuggestedAction {
	if activeRequiredCheckFailed(checks, "config") {
		return []system.SuggestedAction{{
			Command: "amux system doctor",
			Reason:  "configuration check failed; inspect details before mutating services",
		}}
	}
	if state == system.StateStopped {
		return []system.SuggestedAction{{
			Command: "amux system start",
			Reason:  "local amux system services are stopped",
		}}
	}
	if activeRequiredCheckFailed(checks, "targets") && componentState(components, "runtime") == "reachable" {
		return []system.SuggestedAction{{
			Command:              "amux system restart --force",
			Reason:               "runtime is reachable but not registered with the configured hub",
			Destructive:          true,
			RequiresConfirmation: true,
		}}
	}
	if activeRequiredCheckFailed(checks, "hub") || activeRequiredCheckFailed(checks, "runtime") {
		return []system.SuggestedAction{{
			Command: "amux system start",
			Reason:  "required local system component is unavailable",
		}}
	}
	return nil
}

func activeRequiredCheckFailed(checks []system.Check, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Required && check.State == "fail" {
			return true
		}
	}
	return false
}

func componentState(components []system.Component, name string) string {
	for _, component := range components {
		if component.Name == name {
			return component.State
		}
	}
	return ""
}

func legacySystemPlanPreview(ctx context.Context, plan system.ChangePlan) system.PlanPreview {
	preview := system.PlanPreview{
		Schema:    system.PlanPreviewSchema,
		Action:    plan.Request.Action,
		Scope:     plan.Request.Scope,
		Force:     plan.Request.Force,
		CreatedAt: plan.CreatedAt,
	}
	manageHub := false
	if address, err := validatedConfiguredHubAddress(); err == nil {
		manageHub = legacySystemManagesHub(address)
	}
	switch plan.Request.Action {
	case system.ActionStart:
		preview.Impact.WillRestartHub = manageHub
		preview.Impact.WillRestartRuntime = legacySystemStartNeedsRuntimeRestart(plan.Before, manageHub)
	case system.ActionStop:
		preview.Impact.WillRestartHub = manageHub
		preview.Impact.WillRestartRuntime = true
		preview.Impact.RequiresForce = !plan.Request.Force && len(planImpactRunningSessions(ctx, &preview)) > 0
	case system.ActionRestart:
		preview.Impact.WillRestartHub = manageHub
		preview.Impact.WillRestartRuntime = true
		preview.Impact.RequiresForce = !plan.Request.Force && len(planImpactRunningSessions(ctx, &preview)) > 0
	}
	preview.NextActions = planPreviewNextActions(preview)
	return preview
}

func planImpactRunningSessions(ctx context.Context, preview *system.PlanPreview) []string {
	sessions, err := localRunningSessions(ctx)
	if err != nil {
		preview.Impact.Notes = append(preview.Impact.Notes, "running session impact unavailable: "+statusOneLine(err.Error()))
		return nil
	}
	names := runningSessionNames(sessions)
	preview.Impact.MayInterruptSessions = names
	return names
}

func planPreviewNextActions(preview system.PlanPreview) []system.SuggestedAction {
	command := "amux system " + string(preview.Action)
	if preview.Impact.RequiresForce {
		command += " --force"
	}
	return []system.SuggestedAction{{
		Command:              command,
		Reason:               "apply this reviewed lifecycle plan",
		Destructive:          preview.Action == system.ActionStop || preview.Action == system.ActionRestart || preview.Impact.WillRestartRuntime,
		RequiresConfirmation: preview.Impact.RequiresForce,
	}}
}

func legacyExpectedRuntimeRegistered(report cliStatusReport) (bool, string) {
	if report.HubTargets.Error != "" {
		return false, statusOneLine(report.HubTargets.Error)
	}
	expectedID := strings.TrimSpace(report.RuntimeID)
	if expectedID == "" {
		return false, "expected runtime ID is empty"
	}
	for _, runtime := range report.HubTargets.Runtimes {
		if strings.TrimSpace(runtime.ID) == expectedID {
			return true, fmt.Sprintf("%d connected", report.HubTargets.Total)
		}
	}
	return false, fmt.Sprintf("expected runtime %q not registered", expectedID)
}

func legacyHubHealthy(report cliStatusReport) bool {
	return report.Hub.State == "reachable" && report.Hub.Error == ""
}

func legacyRuntimeHealthy(report cliStatusReport) bool {
	stateOK := report.Runtime.State == "running" || report.Runtime.State == "socket present"
	return stateOK && report.Runtime.SocketOK && report.Runtime.Error == "" && report.LocalSessions.Error == ""
}

func legacyHubStopped(report serviceReport) bool {
	return report.State == "not running" || report.State == "configured"
}

func legacyRuntimeStopped(report serviceReport) bool {
	return report.State == "not running" && !report.SocketOK
}

func legacyStoppedRuntimeAPIErrorIgnorable(message string) bool {
	message = strings.TrimSpace(message)
	return message == "" || message == "runtime socket missing"
}

func legacyStoppedHubErrorsIgnorable(hub serviceReport, targets targetSummary) bool {
	hubError := strings.TrimSpace(hub.Error)
	targetsError := strings.TrimSpace(targets.Error)
	if hubError == "" && targetsError == "" {
		return true
	}
	return hub.State == "configured" &&
		isLocalHubListenAddr(hub.Addr) &&
		hubError != "" &&
		hubError == targetsError &&
		legacyHubAddrConfirmedFree(hub.Addr)
}

func legacyHubAddrConfirmedFree(addr string) bool {
	listener, err := net.Listen("tcp", hubListenAddr(addr))
	if err != nil {
		return false
	}
	return listener.Close() == nil
}

func legacyServiceDetail(report serviceReport) string {
	return firstNonEmpty(report.Error, report.Addr)
}

func legacyRuntimeDetail(report cliStatusReport) string {
	return firstNonEmpty(report.Runtime.Error, report.LocalSessions.Error, report.Runtime.SocketPath)
}

func legacySystemCheck(name string, ok bool, message string) system.Check {
	state := "pass"
	if !ok {
		state = "fail"
	}
	check := system.Check{Name: name, State: state, Required: true}
	if !ok {
		check.Message = statusOneLine(message)
	}
	return check
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
