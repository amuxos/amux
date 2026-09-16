//go:build !windows

package acptui

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyTerminalResize(ch chan<- os.Signal) func() {
	signal.Notify(ch, syscall.SIGWINCH)
	return func() { signal.Stop(ch) }
}

func notifyTermination(ch chan<- os.Signal) func() {
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	return func() { signal.Stop(ch) }
}
