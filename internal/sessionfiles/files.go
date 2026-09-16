// Package sessionfiles owns bounded workspace inbox/outbox exchange. It knows
// nothing about chat platforms, providers, or runtime routing.
package sessionfiles

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"syscall"
)

const MaxBytes = 10 << 20

func Validate(action, name string) error {
	if action != "put" && action != "read" {
		return fmt.Errorf("invalid file operation")
	}
	if name == "" || len(name) > 240 || strings.ContainsAny(name, "\\\x00\r\n") || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return fmt.Errorf("invalid relative file name")
	}
	if action == "put" && strings.Contains(name, "/") {
		return fmt.Errorf("attachment name must be a basename")
	}
	return nil
}

// Exchange runs as the execution identity, including in a DAC worker. A root
// confines all path resolution, even if the agent changes a symlink concurrently.
func Exchange(workspace, action, name string, input io.Reader, output io.Writer) error {
	if err := Validate(action, name); err != nil {
		return err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	dir := "amux-outbox"
	var data []byte
	if action == "put" {
		dir = "amux-inbox"
		data, err = io.ReadAll(io.LimitReader(input, MaxBytes+1))
		if err != nil {
			return err
		}
		if len(data) > MaxBytes {
			return fmt.Errorf("file exceeds %d bytes", MaxBytes)
		}
		if err = root.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	info, err := root.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("file exchange directory must be a real directory")
	}
	sub, err := root.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer sub.Close()
	opened, err := sub.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("file exchange directory changed")
	}
	if action == "put" {
		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		if err := sub.MkdirAll(digest, 0o700); err != nil {
			return err
		}
		rel := path.Join(digest, name)
		err := publishInboxFile(sub, rel, func(f *os.File) error {
			_, err := f.Write(data)
			return err
		})
		if os.IsExist(err) {
			// Never overwrite an agent-owned file, even if it originally contained
			// this attachment. Verify before reusing an idempotent destination.
			existing, readErr := readRegular(sub, rel)
			if readErr != nil || sha256.Sum256(existing) != sha256.Sum256(data) {
				return fmt.Errorf("attachment destination already exists with different content")
			}
		} else if err != nil {
			return err
		}
		_, err = io.WriteString(output, path.Join(dir, rel))
		return err
	}
	data, err = readRegular(sub, name)
	if err != nil {
		return err
	}
	_, err = output.Write(data)
	return err
}

// Publish only a complete, closed file. Link provides no-replace semantics so
// concurrent retries cannot overwrite an agent-owned destination as Rename can.
func publishInboxFile(root *os.Root, name string, write func(*os.File) error) error {
	temp := path.Join(path.Dir(name), ".amux-upload-"+rand.Text())
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	writeErr := write(f)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Link(temp, name)
}

func readRegular(root *os.Root, name string) ([]byte, error) {
	// NONBLOCK prevents a malicious FIFO replacement from blocking an RPC.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact must be a regular file")
	}
	if info.Size() > MaxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", MaxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", MaxBytes)
	}
	return data, err
}
