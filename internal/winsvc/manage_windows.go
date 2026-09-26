//go:build windows

package winsvc

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// Install registers the Amux Windows Service to run amux.exe in service mode.
func Install(amuxExe string, cfg Config) error {
	amuxExe = strings.TrimSpace(amuxExe)
	if amuxExe == "" {
		return fmt.Errorf("amux executable path is required")
	}
	abs, err := filepath.Abs(amuxExe)
	if err != nil {
		return err
	}
	if err := SaveConfig(cfg); err != nil {
		return fmt.Errorf("save service config: %w", err)
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists; uninstall it first", ServiceName)
	}

	s, err = m.CreateService(ServiceName, abs, mgr.Config{
		DisplayName:      ServiceDisplayName,
		Description:      ServiceDescription,
		StartType:        mgr.StartAutomatic,
		ServiceStartName: "", // LocalSystem — see package doc
	}, "system", "service", "run")
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	if err := eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		// Non-fatal if the source already exists from a previous install.
		if !strings.Contains(strings.ToLower(err.Error()), "already exists") &&
			!strings.Contains(err.Error(), "exist") {
			_ = s.Delete()
			return fmt.Errorf("install event log: %w", err)
		}
	}
	return nil
}

// Uninstall stops and removes the Amux Windows Service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		_ = RemoveConfig()
		_ = eventlog.Remove(ServiceName)
		return nil // already gone
	}
	defer s.Close()

	_, _ = s.Control(svc.Stop)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil || status.State == svc.Stopped {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	_ = eventlog.Remove(ServiceName)
	_ = RemoveConfig()
	return nil
}

// Start starts the Amux Windows Service.
func Start() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	return s.Start()
}

// Stop stops the Amux Windows Service.
func Stop() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	status, err := s.Control(svc.Stop)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for status.State != svc.Stopped && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return err
		}
	}
	if status.State != svc.Stopped {
		return fmt.Errorf("service did not stop in time")
	}
	return nil
}

// Status returns a short human-readable service state.
func Status() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return "not installed", nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", err
	}
	switch st.State {
	case svc.Stopped:
		return "stopped", nil
	case svc.StartPending:
		return "start pending", nil
	case svc.StopPending:
		return "stop pending", nil
	case svc.Running:
		return "running", nil
	case svc.ContinuePending:
		return "continue pending", nil
	case svc.PausePending:
		return "pause pending", nil
	case svc.Paused:
		return "paused", nil
	default:
		return fmt.Sprintf("state %d", st.State), nil
	}
}

// IsWindowsService reports whether this process was started by the SCM.
func IsWindowsService() (bool, error) {
	return svc.IsWindowsService()
}
