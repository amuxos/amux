//go:build !windows

package config

import (
	"context"
	"errors"
	"os"
	"syscall"
)

func lockConfigFile(file *os.File) error {
	return lockConfigFileContext(context.Background(), file)
}

func lockConfigFileContext(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = unlockConfigFile(file)
				return err
			}
			return nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if err := waitConfigLockRetry(ctx); err != nil {
			return err
		}
	}
}

func unlockConfigFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
