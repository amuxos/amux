//go:build windows

package winsvc

import (
	"fmt"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/svc/eventlog"
)

// Host is implemented by the CLI to start/stop hub and runtime under the
// service account environment.
type Host interface {
	Start() error
	Stop() error
}

type serviceHandler struct {
	host Host
	elog debug.Log
}

func (h *serviceHandler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	if err := h.host.Start(); err != nil {
		_ = h.elog.Error(1, fmt.Sprintf("amux service start failed: %v", err))
		return true, 1
	}
	changes <- svc.Status{State: svc.Running, Accepts: accepts}
	_ = h.elog.Info(1, "amux service running")

loop:
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			if err := h.host.Stop(); err != nil {
				_ = h.elog.Warning(1, fmt.Sprintf("amux service stop: %v", err))
			}
			break loop
		default:
			_ = h.elog.Warning(1, fmt.Sprintf("unexpected service control %d", c.Cmd))
		}
	}
	changes <- svc.Status{State: svc.Stopped}
	return false, 0
}

// RunService enters the Windows service control dispatcher.
func RunService(host Host) error {
	if host == nil {
		return fmt.Errorf("service host is required")
	}
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isSvc {
		return fmt.Errorf("not running as a Windows Service; use `amux system service install`")
	}
	elog, err := eventlog.Open(ServiceName)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer elog.Close()
	return svc.Run(ServiceName, &serviceHandler{host: host, elog: elog})
}
