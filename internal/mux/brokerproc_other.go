//go:build !linux

package mux

// defaultProcRoot is empty on non-Linux platforms: there is no procfs, so
// ListBrokerProcesses returns nil and the process reaper stays a no-op (the
// session-file reaper still runs). Broker orphan reaping is only needed on the
// Linux sandboxes where amuxlet hosts long-lived detached brokers.
const defaultProcRoot = ""
