//go:build windows

package acptui

import (
	"os"
	"os/signal"
)

func notifyTerminalResize(_ chan<- os.Signal) func() {
	return func() {}
}

func notifyTermination(ch chan<- os.Signal) func() {
	signal.Notify(ch, os.Interrupt)
	return func() { signal.Stop(ch) }
}
