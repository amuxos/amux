package cleanup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// openRoot refuses a symlink root. All subsequent I/O remains inside os.Root,
// including when a principal concurrently changes descendants to symlinks.
func openRoot(path string) (*os.Root, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("cleanup root is not a real directory")
	}
	return os.OpenRoot(path)
}
func treeBytes(root *os.Root, path string) (int64, error) {
	var n int64
	err := fs.WalkDir(root.FS(), filepath.ToSlash(path), func(_ string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type().IsRegular() {
			st, e := d.Info()
			if e != nil {
				return e
			}
			n += st.Size()
		}
		return nil
	})
	return n, err
}
func remove(root *os.Root, path string, dry bool, s *Step) {
	n, err := treeBytes(root, path)
	if err != nil {
		s.Error(err)
		return
	}
	s.Candidates++
	if !dry {
		if err = root.RemoveAll(path); err != nil {
			s.Error(err)
			return
		}
		s.Deleted++
	}
	s.Bytes += n
}
func SweepWorkspaces(ctx context.Context, path string, refs References, depth int, now time.Time, dry bool) Step {
	s := Step{Name: "workspaces"}
	root, err := openRoot(path)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		s.Error(err)
		return s
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if strings.Count(rel, "/")+1 < depth {
			return nil
		}
		abs := filepath.Join(path, filepath.FromSlash(rel))
		for _, ref := range append(append([]string{}, refs.Workspaces...), refs.ProtectedPaths...) {
			if Related(abs, ref) {
				s.Skipped++
				return fs.SkipDir
			}
		}
		st, err := root.Stat(rel)
		if err != nil {
			s.Error(err)
			return fs.SkipDir
		}
		entries, err := fs.ReadDir(root.FS(), rel)
		if err != nil {
			s.Error(err)
			return fs.SkipDir
		}
		// A short grace also protects newly prepared, still-empty workspaces whose
		// Session is being committed concurrently with this sweep.
		retention := 14 * 24 * time.Hour
		if len(entries) == 0 {
			retention = 3 * time.Hour
		}
		if now.Sub(st.ModTime()) < retention {
			s.Skipped++
			return fs.SkipDir
		}
		remove(root, rel, dry, &s)
		return fs.SkipDir
	})
	s.Error(err)
	return s
}

// SweepSessionArtifacts retains Session-owned data. For Sessions whose brokers
// are stably stopped, only disposable broker diagnostics can age out in place.
func SweepSessionArtifacts(ctx context.Context, path string, refs References, stoppedBrokers map[string]bool, now time.Time, dry bool) Step {
	s := Step{Name: "session_artifacts"}
	root, err := openRoot(path)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		s.Error(err)
		return s
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		s.Error(err)
		return s
	}
	ids := map[string]bool{}
	for _, id := range refs.SessionIDs {
		ids[id] = true
	}
	for _, d := range entries {
		if ctx.Err() != nil {
			s.Error(ctx.Err())
			break
		}
		id, suffix, _ := strings.Cut(d.Name(), ".")
		if _, err := uuid.Parse(id); err != nil {
			s.Skipped++
			continue
		}
		if d.Type()&os.ModeSymlink != 0 || (ids[id] && (!stoppedBrokers[id] || !isBrokerDiagnostic(suffix) || !d.Type().IsRegular())) {
			s.Skipped++
			continue
		}
		st, err := d.Info()
		if err != nil {
			s.Error(err)
			continue
		}
		if now.Sub(st.ModTime()) < 3*time.Hour {
			s.Skipped++
			continue
		}
		remove(root, d.Name(), dry, &s)
	}
	return s
}

func isBrokerDiagnostic(suffix string) bool {
	switch suffix {
	case "pid", "log", "acp.log", "structured.log":
		return true
	default:
		return false
	}
}

func SweepHome(ctx context.Context, home string, refs References, now time.Time, dry bool) []Step {
	var out []Step
	for _, p := range HomePolicies() {
		if p.RolloutDir != "" {
			s := sweepRollouts(ctx, home, p, refs, now, dry)
			out = append(out, s)
		}
		if p.TrustFile != "" {
			s := sweepTrust(ctx, home, p, dry)
			out = append(out, s)
		}
	}
	return out
}
func sweepRollouts(ctx context.Context, home string, p HomePolicy, refs References, now time.Time, dry bool) Step {
	s := Step{Name: "provider_rollouts"}
	root, err := openRoot(home)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		s.Error(err)
		return s
	}
	defer root.Close()
	live := map[string]bool{}
	for _, id := range refs.AgentSessionIDs {
		live[id] = true
	}
	var emptyDirs []string
	err = fs.WalkDir(root.FS(), filepath.ToSlash(p.RolloutDir), func(rel string, d fs.DirEntry, e error) error {
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			emptyDirs = append(emptyDirs, rel)
			return nil
		}
		name := d.Name()
		if d.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		stem := strings.TrimSuffix(name, ".jsonl")
		if len(stem) < 36 {
			s.Skipped++
			return nil
		}
		id := stem[len(stem)-36:]
		if _, err := uuid.Parse(id); err != nil {
			s.Skipped++
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if live[id] || now.Sub(st.ModTime()) < 3*time.Hour {
			s.Skipped++
			return nil
		}
		// Related artifacts are cleaned before the index JSONL. A failed artifact
		// deletion leaves the index in place so the next run can retry.
		for _, related := range []string{rel + ".lock", strings.TrimSuffix(rel, ".jsonl") + ".artifacts", rel} {
			st, err := root.Lstat(related)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				s.Error(err)
				break
			}
			if st.Mode()&os.ModeSymlink != 0 {
				s.Skipped++
				continue
			}
			before := len(s.Errors)
			remove(root, related, dry, &s)
			if len(s.Errors) > before {
				break
			}
		}
		return nil
	})
	s.Error(err)
	if !dry {
		for i := len(emptyDirs) - 1; i > 0; i-- {
			_ = root.Remove(emptyDirs[i])
		}
	}
	return s
}
func RotateLogs(ctx context.Context, dir string, names []string, dry bool) Step {
	s := Step{Name: "logs"}
	root, err := openRoot(dir)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		s.Error(err)
		return s
	}
	defer root.Close()
	for _, name := range names {
		if ctx.Err() != nil {
			s.Error(ctx.Err())
			break
		}
		st, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			s.Error(err)
			continue
		}
		if !st.Mode().IsRegular() || st.Size() <= 20_000_000 {
			continue
		}
		s.Candidates++
		if dry {
			s.Bytes += st.Size() - 5_000_000
			continue
		}
		f, err := root.OpenFile(name, os.O_RDWR, 0)
		if err != nil {
			s.Error(err)
			continue
		}
		data := make([]byte, 5_000_000)
		_, err = f.ReadAt(data, st.Size()-int64(len(data)))
		if err == nil {
			_, err = f.WriteAt(data, 0)
		}
		if err == nil {
			err = f.Truncate(int64(len(data)))
		}
		closeErr := f.Close()
		if err != nil {
			s.Error(err)
		} else if closeErr != nil {
			s.Error(closeErr)
		} else {
			s.Deleted++
			s.Bytes += st.Size() - int64(len(data))
		}
	}
	return s
}

// SweepServiceHomeTrust must never delete native user histories: they can
// belong to sessions launched outside this installation.
func SweepServiceHomeTrust(ctx context.Context, home string, dry bool) []Step {
	var steps []Step
	for _, p := range HomePolicies() {
		if p.TrustFile != "" {
			steps = append(steps, sweepTrust(ctx, home, p, dry))
		}
	}
	return steps
}
