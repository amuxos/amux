package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type cliStatusReport struct {
	Version      string
	ConfigPath   string
	ConfigExists bool
	ConfigError  string
	AmuxDir      string

	Runtime     serviceReport
	RuntimeID   string
	RuntimeName string
	RuntimeAuto bool

	Hub           serviceReport
	HubConfigured bool
	HubTargets    targetSummary
	Tasks         taskSummary
	Schedules     scheduleSummary

	MCP           mcpStatus
	LocalServices localServicesStatus
	LocalSessions sessionSummary
	Feishu        *pb.FeishuStatusResponse
	FeishuBots    []*pb.FeishuBot
	FeishuBotsErr string
}

type serviceReport struct {
	Name       string
	State      string
	Version    string
	PID        int
	StartedAt  time.Time
	PIDPath    string
	SocketPath string
	SocketOK   bool
	LogPath    string
	LogExists  bool
	LogSize    int64
	LogMod     time.Time
	Addr       string
	Error      string
}

type sessionSummary struct {
	Total    int
	Running  int
	Archived int
	Other    int
	Agents   map[string]int
	Error    string
}

type targetSummary struct {
	Total           int
	Sessions        int
	RunningSessions int
	Runtimes        []targetRuntimeSummary
	Error           string
}

type targetRuntimeSummary struct {
	ID              string
	Name            string
	Version         string
	Sessions        int
	RunningSessions int
}

type taskSummary struct {
	Total    int
	Running  int
	Terminal int
	Other    int
	Error    string
}

type scheduleSummary struct {
	Total    int
	Enabled  int
	Disabled int
	Error    string
}

type mcpStatus struct {
	Enabled      bool
	AutoInject   bool
	Server       string
	Backend      string
	BackendOK    bool
	BackendError string
	HubBackend   bool
}

type localServicesStatus struct {
	Processes []trackedProcess
	Error     string
}

func collectCLIStatus(ctx context.Context) cliStatusReport {
	cfgPath := defaultConfigPath()
	cfgErr := loadUserConfig()
	mcpCfg := config.GetMCP()
	report := cliStatusReport{
		Version:      version,
		ConfigPath:   cfgPath,
		ConfigExists: fileExists(cfgPath),
		AmuxDir:      defaultAmuxDir(),
		MCP: mcpStatus{
			Enabled:    mcpCfg.Enabled,
			AutoInject: mcpCfg.AutoInject,
			Server:     "stdio on-demand (`amux mcp`)",
		},
	}
	if cfgErr != nil && !isMissingConfigError(cfgErr) {
		report.ConfigError = cfgErr.Error()
	}
	runtimeCfg := config.GetRuntime()
	report.RuntimeID = runtimeCfg.ID
	report.RuntimeName = runtimeCfg.Name
	report.RuntimeAuto = runtimeCfg.AutoStart
	report.HubConfigured = true

	report.Runtime = inspectService(runtimeService(), defaultRuntimeSocketPath(), "")
	report.Hub = inspectService(hubService(), "", effectiveHubAddr())
	report.MCP = fillMCPBackend(report.MCP, report.Hub.Addr)
	if procs, err := listTrackedProcesses(trackedProcessRoleMCP, trackedProcessRoleGUI); err == nil {
		report.LocalServices.Processes = procs
	} else {
		report.LocalServices.Error = err.Error()
	}

	if report.Runtime.SocketOK {
		if lc, err := newLocalRuntimeClient(); err == nil {
			defer lc.Close()
			if info, infoErr := lc.Info(ctx); infoErr == nil {
				applyServiceInfo(&report.Runtime, info.Version, info.StartedAt)
			}
			sessions, listErr := lc.List(ctx)
			if listErr != nil {
				report.LocalSessions.Error = listErr.Error()
			} else {
				report.LocalSessions = summarizeSessionEntries(sessions)
			}
		} else {
			report.LocalSessions.Error = err.Error()
		}
	} else {
		report.LocalSessions.Error = "runtime socket missing"
	}

	inspectConfiguredHub(ctx, &report, hubAddr())
	report.MCP = finalizeMCPBackend(report.MCP, report.Hub)

	if feishu, err := feishuStatus(ctx, ""); err == nil {
		report.Feishu = feishu
	} else {
		report.Feishu = &pb.FeishuStatusResponse{Error: err.Error()}
	}
	if bots, err := feishuBots(ctx); err == nil {
		report.FeishuBots = bots
	} else {
		report.FeishuBotsErr = err.Error()
	}
	return report
}

func inspectConfiguredHub(ctx context.Context, report *cliStatusReport, addr string) {
	if report == nil || addr == "" {
		return
	}
	if errText, ok := staleLocalHubNoListenerError(report.Hub, addr); ok {
		report.Hub.Error = errText
		report.HubTargets.Error = errText
		return
	}
	if hc, err := newHubClient(addr); err == nil {
		defer hc.Close()
		report.Hub.State = "reachable"
		if info, infoErr := hc.c.Info(ctx, &pb.HubInfoRequest{}); infoErr == nil {
			applyServiceInfo(&report.Hub, info.Version, info.StartedAt)
		}
		targets, listErr := hc.ListTargets(ctx)
		if listErr != nil {
			report.HubTargets.Error = listErr.Error()
		} else {
			report.HubTargets = summarizeTargets(targets)
		}
		if tasks, taskErr := pb.NewAmuxTaskClient(hc.conn).ListSubtasks(ctx, &pb.ListSubtasksRequest{}); taskErr == nil {
			report.Tasks = summarizeTaskJSON(tasks.TasksJson)
		} else {
			report.Tasks.Error = taskErr.Error()
		}
		if schedules, scheduleErr := hc.c.ListSchedules(ctx, &pb.ListSchedulesRequest{}); scheduleErr == nil {
			report.Schedules = summarizeSchedules(schedules.Schedules)
		} else {
			report.Schedules.Error = scheduleErr.Error()
		}
	} else {
		report.Hub.Error = err.Error()
		if report.Hub.State == "not running" {
			report.Hub.State = "configured"
		}
		report.HubTargets.Error = err.Error()
		report.Tasks.Error = err.Error()
		report.Schedules.Error = err.Error()
	}
}

func staleLocalHubNoListenerError(hub serviceReport, addr string) (string, bool) {
	if hub.State != "stale pid" || !isLocalHubListenAddr(addr) || addrInUse(hubListenAddr(addr)) {
		return "", false
	}
	return fmt.Sprintf("hub pid file is stale and no listener is active at %s; inspect the installation with `amux system status`, then recover it with `amux system start`", addr), true
}

func isLocalHubListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func fillMCPBackend(m mcpStatus, hubAddr string) mcpStatus {
	if !m.Enabled {
		m.Backend = "disabled"
		return m
	}
	m.HubBackend = true
	m.Backend = "hub " + hubAddr
	return m
}

func finalizeMCPBackend(m mcpStatus, hub serviceReport) mcpStatus {
	if !m.Enabled {
		return m
	}
	m.BackendOK = hub.State == "reachable" || hub.State == "running"
	m.BackendError = hub.Error
	return m
}

func inspectService(svc managedService, socketPath, addr string) serviceReport {
	r := serviceReport{
		Name:       svc.Name,
		State:      "not running",
		PIDPath:    svc.PIDPath,
		SocketPath: socketPath,
		LogPath:    svc.LogPath,
		Addr:       addr,
	}
	if pid, ok := readRunningPID(svc.PIDPath); ok {
		r.State = "running"
		r.PID = pid
		if startedAt, ok := processStartedAt(pid); ok {
			r.StartedAt = startedAt
		}
	} else if pid, ok := readPIDFile(svc.PIDPath); ok {
		r.State = "stale pid"
		r.PID = pid
	}
	if socketPath != "" {
		r.SocketOK = fileExists(socketPath)
		if r.State == "not running" && r.SocketOK {
			r.State = "socket present"
		}
	}
	if info, err := os.Stat(svc.LogPath); err == nil {
		r.LogExists = true
		r.LogSize = info.Size()
		r.LogMod = info.ModTime()
	}
	return r
}

func applyServiceInfo(r *serviceReport, version string, startedAt int64) {
	if strings.TrimSpace(version) != "" {
		r.Version = version
	}
	if startedAt > 0 {
		r.StartedAt = time.Unix(startedAt, 0)
	}
}

func processStartedAt(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", raw, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func effectiveHubAddr() string {
	return hubAddr()
}

func summarizeSessionEntries(sessions []*sessionEntry) sessionSummary {
	sum := sessionSummary{Total: len(sessions), Agents: map[string]int{}}
	for _, entry := range sessions {
		if entry == nil {
			continue
		}
		addSessionToSummary(&sum, &entry.Session)
	}
	return sum
}

func summarizeTargets(targets []targetSessions) targetSummary {
	var sum targetSummary
	sum.Total = len(targets)
	for _, target := range targets {
		rt := targetRuntimeSummary{
			ID:      target.target.ID,
			Name:    target.target.Name,
			Version: target.target.Version,
		}
		for _, session := range target.sessions {
			sum.Sessions++
			if session != nil && session.State == "running" {
				sum.RunningSessions++
				rt.RunningSessions++
			}
			rt.Sessions++
		}
		sum.Runtimes = append(sum.Runtimes, rt)
	}
	return sum
}

func summarizeTaskJSON(tasksJSON [][]byte) taskSummary {
	sum := taskSummary{Total: len(tasksJSON)}
	for _, raw := range tasksJSON {
		run, err := decodeTask(raw)
		if err != nil {
			sum.Other++
			continue
		}
		switch {
		case run.Status.IsTerminal():
			sum.Terminal++
		case taskStatusRunning(run.Status):
			sum.Running++
		default:
			sum.Other++
		}
	}
	return sum
}

func taskStatusRunning(status task.Status) bool {
	switch status {
	case task.StatusRunning, task.StatusWaitingPermission, task.StatusCollecting, task.StatusReconciling, task.StatusCancelling:
		return true
	default:
		return false
	}
}

func summarizeSchedules(schedules []*pb.ScheduleItem) scheduleSummary {
	sum := scheduleSummary{Total: len(schedules)}
	for _, item := range schedules {
		if item != nil && item.Enabled {
			sum.Enabled++
		} else {
			sum.Disabled++
		}
	}
	return sum
}

func summarizePBSessions(sessions []*pb.Session) sessionSummary {
	sum := sessionSummary{Total: len(sessions), Agents: map[string]int{}}
	for _, session := range sessions {
		addSessionToSummary(&sum, session)
	}
	return sum
}

func addSessionToSummary(sum *sessionSummary, s *pb.Session) {
	if s == nil {
		return
	}
	switch s.State {
	case "running":
		sum.Running++
	case "archived":
		sum.Archived++
	default:
		sum.Other++
	}
	if sum.Agents == nil {
		sum.Agents = map[string]int{}
	}
	if s.AgentType != "" {
		sum.Agents[s.AgentType]++
	}
}
func splitLocalServiceProcesses(procs []trackedProcess) ([]trackedProcess, []trackedProcess) {
	restartable := make([]trackedProcess, 0, len(procs))
	sessionOwned := make([]trackedProcess, 0)
	for _, proc := range procs {
		if strings.TrimSpace(proc.SessionID) != "" {
			sessionOwned = append(sessionOwned, proc)
			continue
		}
		restartable = append(restartable, proc)
	}
	return restartable, sessionOwned
}
func hasConfiguredFeishu(report cliStatusReport) bool {
	if report.Feishu != nil && report.Feishu.Configured {
		return true
	}
	for _, bot := range report.FeishuBots {
		if bot != nil && bot.Configured {
			return true
		}
	}
	return false
}

func countConfiguredFeishuBots(bots []*pb.FeishuBot) int {
	count := 0
	for _, bot := range bots {
		if bot != nil && bot.Configured {
			count++
		}
	}
	return count
}
func formatAgentCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

// formatTargetCapacity renders a target's registry load against its capacity as
// "current(+reserved)/max", or "-" for a Phase 1 runtime that reports no
// capabilities.
func formatTargetCapacity(t *pb.Target) string {
	if t.Capabilities == nil || t.Capabilities.MaxSessions == 0 {
		return "-"
	}
	cur := 0
	reserved := 0
	if t.Load != nil {
		cur = int(t.Load.CurrentSessions)
		reserved = int(t.Load.ReservedSlots)
	}
	if reserved > 0 {
		return fmt.Sprintf("%d+%d/%d", cur, reserved, t.Capabilities.MaxSessions)
	}
	return fmt.Sprintf("%d/%d", cur, t.Capabilities.MaxSessions)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func isMissingConfigError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return os.IsNotExist(err) || strings.Contains(text, "config file") && strings.Contains(text, "not found")
}

func existsWord(ok bool) string {
	if ok {
		return "exists"
	}
	return "missing"
}
func yesNo(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}
func statusOneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func formatStatusTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04:05")
}
