//go:build windows

package config

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockConfigFile(file *os.File) error {
	return lockConfigFileContext(context.Background(), file)
}

func lockConfigFileContext(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var overlapped windows.Overlapped
		err := windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, &overlapped,
		)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = unlockConfigFile(file)
				return err
			}
			return nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		if err := waitConfigLockRetry(ctx); err != nil {
			return err
		}
	}
}

func unlockConfigFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
