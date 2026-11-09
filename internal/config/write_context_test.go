package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestConfigContextMutexCancellation(t *testing.T) {
	for _, operation := range []string{"update", "save", "read"} {
		t.Run(operation, func(t *testing.T) {
			path, original := configContextTestFile(t)
			configWriteMu.Lock()
			var once sync.Once
			release := func() { once.Do(configWriteMu.Unlock) }
			t.Cleanup(release)

			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			var mutated atomic.Bool
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "update":
					done <- UpdateConfigFileContext(ctx, path, func(raw map[string]any) error {
						mutated.Store(true)
						raw["unexpected"] = true
						return nil
					})
				case "save":
					done <- SaveDailyCleanupEnabledContext(ctx, false)
				case "read":
					_, err := ReadDailyCleanupSettingsContext(ctx)
					done <- err
				}
			}()
			if err := configContextTestResult(t, done); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting on mutex: got %v, want deadline exceeded", err)
			}
			release()
			if err := UpdateConfigFileContext(context.Background(), path, func(map[string]any) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if mutated.Load() {
				t.Fatal("cancelled mutation ran after the mutex was released")
			}
			// The follow-up update can reformat TOML, so compare parsed values.
			got, err := readConfigFileForWrite(path)
			if err != nil || !reflect.DeepEqual(got, original) {
				t.Fatalf("cancelled operation changed config: got %#v, want %#v (err %v)", got, original, err)
			}
		})
	}
}

func TestConfigContextFileLockCancellation(t *testing.T) {
	for _, operation := range []string{"update", "save"} {
		t.Run(operation, func(t *testing.T) {
			path, original := configContextTestFile(t)
			release := holdConfigContextTestFileLock(t, path)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			var mutated atomic.Bool
			done := make(chan error, 1)
			go func() {
				if operation == "save" {
					done <- SaveDailyCleanupEnabledContext(ctx, false)
					return
				}
				done <- UpdateConfigFileContext(ctx, path, func(raw map[string]any) error {
					mutated.Store(true)
					raw["unexpected"] = true
					return nil
				})
			}()
			if err := configContextTestResult(t, done); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting on file lock: got %v, want deadline exceeded", err)
			}
			if !configWriteMu.TryLock() {
				t.Fatal("cancelled file-lock wait did not release the process mutex")
			}
			configWriteMu.Unlock()
			release()
			if err := UpdateConfigFileContext(context.Background(), path, func(map[string]any) error { return nil }); err != nil {
				t.Fatal(err)
			}
			got, err := readConfigFileForWrite(path)
			if mutated.Load() || err != nil || !reflect.DeepEqual(got, original) {
				t.Fatalf("cancelled operation changed config after release: mutated=%v, got %#v, err %v", mutated.Load(), got, err)
			}
		})
	}
}

func TestConfigContextWaitsUntilLockReleased(t *testing.T) {
	for _, lockKind := range []string{"mutex", "file"} {
		t.Run(lockKind, func(t *testing.T) {
			path, _ := configContextTestFile(t)
			var release func()
			if lockKind == "mutex" {
				configWriteMu.Lock()
				var once sync.Once
				release = func() { once.Do(configWriteMu.Unlock) }
				t.Cleanup(release)
			} else {
				release = holdConfigContextTestFileLock(t, path)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- UpdateConfigFileContext(ctx, path, func(raw map[string]any) error {
					raw["updated"] = true
					return nil
				})
			}()
			select {
			case err := <-done:
				t.Fatalf("update finished before lock release: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			release()
			if err := configContextTestResult(t, done); err != nil {
				t.Fatal(err)
			}
			raw, err := readConfigFileForWrite(path)
			if err != nil || raw["updated"] != true {
				t.Fatalf("update after release: %#v, %v", raw, err)
			}
		})
	}
}

func TestConfigContextCancellationDuringMutation(t *testing.T) {
	path, original := configContextTestFile(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := UpdateConfigFileContext(ctx, path, func(raw map[string]any) error {
		raw["unexpected"] = true
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancelled", err)
	}
	raw, err := readConfigFileForWrite(path)
	if err != nil || !reflect.DeepEqual(raw, original) {
		t.Fatalf("cancelled mutation was persisted: %#v, %v", raw, err)
	}
	if err := UpdateConfigFile(path, func(raw map[string]any) error {
		raw["updated"] = true
		return nil
	}); err != nil {
		t.Fatalf("locks not released after cancellation: %v", err)
	}
}

func TestConfigContextCancelledBeforeRename(t *testing.T) {
	path, _ := configContextTestFile(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel at the final Err check: after the temporary file is synced and
	// closed, but before rename. No production filesystem hooks are needed.
	checking := &configContextCancelOnCheck{Context: ctx, cancel: cancel, remaining: 3}
	err = writeConfigFileForWriteContext(checking, path, map[string]any{"unexpected": true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancelled before rename", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("destination changed before commit: %q, %v", got, err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.toml.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files not cleaned up: %v, %v", matches, err)
	}
}

func TestDailyCleanupContextPreservesOtherSettings(t *testing.T) {
	path, _ := configContextTestFile(t)
	if err := SaveDailyCleanupEnabledContext(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadDailyCleanupSettingsContext(context.Background())
	if err != nil || settings.Enabled || settings.At != "05:30" || !reflect.DeepEqual(settings.ProtectedPaths, []string{filepath.Join(filepath.Dir(path), "keep")}) {
		t.Fatalf("saved settings=%+v, err=%v", settings, err)
	}
	raw, err := readConfigFileForWrite(path)
	if err != nil || raw["other"].(map[string]any)["keep"] != "yes" {
		t.Fatalf("lost unrelated settings: %#v, %v", raw, err)
	}
	if err := SaveDailyCleanupEnabled(true); err != nil {
		t.Fatal(err)
	}
	settings, err = ReadDailyCleanupSettings()
	if err != nil || !settings.Enabled {
		t.Fatalf("legacy wrappers: %+v, %v", settings, err)
	}
}

func TestConfigContextAlreadyCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "config.toml")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mutated := false
	err := UpdateConfigFileContext(ctx, path, func(map[string]any) error {
		mutated = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || mutated {
		t.Fatalf("already cancelled update: mutated=%v, err=%v", mutated, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("cancelled update created config directory: %v", err)
	}
}

func configContextTestFile(t *testing.T) (string, map[string]any) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.toml")
	viper.SetConfigFile(path)
	data := fmt.Sprintf("[other]\nkeep = 'yes'\n[maintenance.daily_cleanup]\nenabled = true\nat = '05:30'\nprotected_paths = ['%s']\n", filepath.Join(filepath.Dir(path), "keep"))
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := readConfigFileForWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, raw
}

func holdConfigContextTestFileLock(t *testing.T, path string) func() {
	t.Helper()
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockConfigFile(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if err := unlockConfigFile(file); err != nil {
				t.Error(err)
			}
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

func configContextTestResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("config operation did not finish promptly")
		return nil
	}
}

type configContextCancelOnCheck struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (ctx *configContextCancelOnCheck) Err() error {
	ctx.remaining--
	if ctx.remaining == 0 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}
