//go:build !windows

package agentprofile

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// lockPackFile takes the registry lock, waiting politely until it is free or
// the caller's context ends. A blocking LOCK_EX would hold the pack registry
// hostage to whichever process grabs it first, so the wait loops on LOCK_NB
// and lets the context win.
func lockPackFile(ctx context.Context, file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func unlockPackFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
