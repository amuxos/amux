package sessionfiles

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInboxPublicationFailureLeavesDestinationRetryable(t *testing.T) {
	for _, failure := range []string{"write", "close"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = publishInboxFile(root, "input.txt", func(f *os.File) error {
				if _, err := f.WriteString("partial"); err != nil {
					return err
				}
				if _, err := root.Stat("input.txt"); !os.IsNotExist(err) {
					t.Error("destination visible before write completed")
				}
				if failure == "write" {
					return io.ErrShortWrite
				}
				// Closing the descriptor forces the publisher's Close to fail.
				return f.Close()
			})
			if err == nil {
				t.Fatal("accepted failed write/close")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("left incomplete file: %v, %v", entries, err)
			}
			write := func(f *os.File) error { _, err := f.WriteString("complete"); return err }
			if err := publishInboxFile(root, "input.txt", write); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			if err := publishInboxFile(root, "input.txt", func(f *os.File) error {
				_, err := f.WriteString("replacement")
				return err
			}); !errors.Is(err, os.ErrExist) {
				t.Fatalf("replaced existing file: %v", err)
			}
			data, err := root.ReadFile("input.txt")
			if err != nil || string(data) != "complete" {
				t.Fatalf("destination = %q, %v", data, err)
			}
		})
	}
}

func TestConcurrentInboxRetriesSeeOnlyCompleteFile(t *testing.T) {
	dir := t.TempDir()
	data := strings.Repeat("attachment", 10000)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			var out bytes.Buffer
			if err := Exchange(dir, "put", "input.txt", strings.NewReader(data), &out); err != nil {
				t.Error(err)
				return
			}
			got, err := os.ReadFile(filepath.Join(dir, out.String()))
			if err != nil || string(got) != data {
				t.Errorf("incomplete destination: %d bytes, %v", len(got), err)
			}
		})
	}
	wg.Wait()
}

func TestWorkspaceExchange(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Exchange(root, "put", "需求.txt", strings.NewReader("用户素材"), &out); err != nil {
		t.Fatal(err)
	}
	rel := out.String()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil || string(data) != "用户素材" {
		t.Fatalf("material = %s, %v", data, err)
	}
	out.Reset()
	if err := Exchange(root, "put", "需求.txt", strings.NewReader("用户素材"), &out); err != nil || out.String() != rel {
		t.Fatalf("idempotence: %s %v", out.String(), err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Exchange(root, "put", "需求.txt", strings.NewReader("用户素材"), &out); err == nil {
		t.Fatal("overwrote changed attachment")
	}
	if err := os.MkdirAll(filepath.Join(root, "amux-outbox", "reports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "amux-outbox", "reports", "report.txt"), []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Exchange(root, "read", "reports/report.txt", nil, &out); err != nil || out.String() != "result" {
		t.Fatalf("read: %s %v", out.String(), err)
	}
}
func TestWorkspaceExchangeRejectsEscapeAndOversize(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	var out bytes.Buffer
	_ = os.Mkdir(filepath.Join(root, "amux-outbox"), 0700)
	_ = os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600)
	_ = os.Symlink(outside, filepath.Join(root, "amux-outbox", "escape"))
	for _, name := range []string{"../secret", "/etc/passwd", "a/../../secret", "escape/secret", ".", "a\\b", "a\nsecret"} {
		if err := Exchange(root, "read", name, nil, &out); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if err := Exchange(root, "read", "escape", nil, &out); err == nil {
		t.Fatal("accepted directory")
	}
	if err := Exchange(root, "put", "x", bytes.NewReader(make([]byte, MaxBytes+1)), &out); err == nil {
		t.Fatal("accepted oversized input")
	}
	f, err := os.Create(filepath.Join(root, "amux-outbox", "large"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(MaxBytes + 1)
	_ = f.Close()
	if err := Exchange(root, "read", "large", nil, &out); err == nil {
		t.Fatal("accepted oversized output")
	}
	_ = os.RemoveAll(filepath.Join(root, "amux-outbox"))
	_ = os.Symlink(outside, filepath.Join(root, "amux-outbox"))
	if err := Exchange(root, "read", "secret", nil, &out); err == nil {
		t.Fatal("accepted symlink exchange directory")
	}
}
