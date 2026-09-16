package isolation

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxWorkspaceMaterializeBytes int64 = 1 << 30

type BundleSource struct {
	Source string
	Prefix string
}

var allowedBundlePrefixes = map[string]bool{
	"workspace":    true,
	"agent-config": true,
	"secret":       true,
	"tmp":          true,
	"home":         true,
}

// WriteMaterializeBundle serializes regular files/directories from trusted
// service-owned staging trees. The bundle contains logical prefixes only; it
// never carries a destination path or ownership instruction for the helper.
func WriteMaterializeBundle(writer io.Writer, sources []BundleSource) error {
	tw := tar.NewWriter(writer)
	var total int64
	for _, source := range sources {
		prefix := strings.TrimSpace(source.Prefix)
		if !allowedBundlePrefixes[prefix] {
			_ = tw.Close()
			return fmt.Errorf("unsupported materialization prefix %q", prefix)
		}
		info, err := os.Lstat(source.Source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			_ = tw.Close()
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = tw.Close()
			return fmt.Errorf("materialization source %s is not a real directory", prefix)
		}
		err = filepath.WalkDir(source.Source, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(source.Source, filePath)
			if err != nil {
				return err
			}
			name := prefix
			if rel != "." {
				name = path.Join(prefix, filepath.ToSlash(rel))
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("materialization symlink %s is not allowed", name)
			}
			header := &tar.Header{Name: name, ModTime: info.ModTime()}
			switch {
			case info.IsDir():
				header.Typeflag = tar.TypeDir
				header.Mode = 0o700
				return tw.WriteHeader(header)
			case info.Mode().IsRegular():
				total += info.Size()
				if total > maxWorkspaceMaterializeBytes {
					return fmt.Errorf("materialization bundle exceeds %d bytes", maxWorkspaceMaterializeBytes)
				}
				header.Typeflag = tar.TypeReg
				header.Size = info.Size()
				header.Mode = int64(info.Mode().Perm() & 0o700)
				if prefix == "secret" {
					header.Mode = 0o600
				}
				if err := tw.WriteHeader(header); err != nil {
					return err
				}
				file, err := os.Open(filePath)
				if err != nil {
					return err
				}
				_, copyErr := io.Copy(tw, file)
				closeErr := file.Close()
				if copyErr != nil {
					return copyErr
				}
				return closeErr
			default:
				return fmt.Errorf("materialization special file %s is not allowed", name)
			}
		})
		if err != nil {
			_ = tw.Close()
			return err
		}
	}
	return tw.Close()
}

// ApplyMaterializeBundle is the fixed target-UID worker. It maps the logical
// prefixes into helper-prepared roots, rejects traversal/symlinks and special
// files, and never executes profile/git logic as root.
func ApplyMaterializeBundle(reader io.Reader, workspace, run, home string) error {
	if !filepath.IsAbs(workspace) || !filepath.IsAbs(run) || !filepath.IsAbs(home) {
		return fmt.Errorf("materialization roots must be absolute")
	}
	if err := ensureBundleDir(workspace, workspace); err != nil {
		return err
	}
	if err := ensureBundleDir(run, run); err != nil {
		return err
	}
	if err := ensureBundleDir(home, home); err != nil {
		return err
	}
	tr := tar.NewReader(io.LimitReader(reader, maxWorkspaceMaterializeBytes+(64<<20)))
	var total int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimSpace(header.Name))
		if name == "." || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("invalid materialization entry %q", header.Name)
		}
		parts := strings.Split(name, "/")
		if len(parts) == 0 || !allowedBundlePrefixes[parts[0]] {
			return fmt.Errorf("unsupported materialization entry %q", header.Name)
		}
		root := run
		rel := name
		switch parts[0] {
		case "workspace":
			root = workspace
			rel = strings.TrimPrefix(strings.TrimPrefix(name, "workspace"), "/")
		case "home":
			// HOME-scoped provider credentials keep their own layout under the
			// principal's home rather than under the per-launch run directory.
			root = home
			rel = strings.TrimPrefix(strings.TrimPrefix(name, "home"), "/")
		}
		target := root
		if rel != "" {
			target = filepath.Join(root, filepath.FromSlash(rel))
		}
		if !pathWithin(root, target) {
			return fmt.Errorf("materialization entry escapes destination")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureBundleDir(root, target); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				return fmt.Errorf("negative materialization entry size")
			}
			total += header.Size
			if total > maxWorkspaceMaterializeBytes {
				return fmt.Errorf("materialization bundle exceeds %d bytes", maxWorkspaceMaterializeBytes)
			}
			if err := ensureBundleDir(root, filepath.Dir(target)); err != nil {
				return err
			}
			if info, err := os.Lstat(target); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
				return fmt.Errorf("materialization target is not a regular file")
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			mode := fs.FileMode(header.Mode) & 0o700
			if parts[0] == "secret" {
				mode = 0o600
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, tr, header.Size)
			chmodErr := file.Chmod(mode)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if chmodErr != nil {
				return chmodErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("materialization entry %q is not a regular file or directory", header.Name)
		}
	}
}

func ensureBundleDir(root, target string) error {
	if !pathWithin(root, target) {
		return fmt.Errorf("materialization directory escapes root")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := filepath.Clean(root)
	if info, err := os.Lstat(current); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("materialization root is not a real directory")
	}
	if rel == "." {
		return os.Chmod(current, 0o700)
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid materialization directory component")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("materialization path contains a non-directory")
		}
	}
	return os.Chmod(target, 0o700)
}

// MaterializeWorkspace copies a trusted control-plane workspace into a helper
// prepared principal workspace. It accepts regular files and directories only;
// symlinks and special files are rejected so later root-side ownership changes
// cannot be redirected outside the fixed destination.
func MaterializeWorkspace(source, destination string) error {
	if source == "" || filepath.Clean(source) == filepath.Clean(destination) {
		return nil
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace source must be a real directory")
	}
	var copied int64
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if !pathWithin(destination, target) {
			return fmt.Errorf("workspace entry escapes destination")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace symlink %s is not allowed", rel)
		}
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o700)
		case info.Mode().IsRegular():
			copied += info.Size()
			if copied > maxWorkspaceMaterializeBytes {
				return fmt.Errorf("workspace exceeds %d bytes", maxWorkspaceMaterializeBytes)
			}
			return copyRegularFile(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("workspace special file %s is not allowed", rel)
		}
	})
}

func copyRegularFile(source, destination string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode&0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, maxWorkspaceMaterializeBytes+1)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
