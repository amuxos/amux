//go:build windows

package agentprofile

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockPackFile mirrors the unix helper: poll with FAIL_IMMEDIATELY so a
// cancelled context does not strand the caller behind a held registry lock.
func lockPackFile(ctx context.Context, file *os.File) error {
	for {
		var overlapped windows.Overlapped
		err := windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, &overlapped,
		)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
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
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
