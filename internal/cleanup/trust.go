package cleanup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// sweepTrust preserves unrelated TOML verbatim. It validates the complete file,
// backs it up once, and refuses to replace a concurrently changed file.
func sweepTrust(ctx context.Context, home string, p HomePolicy, dry bool) Step {
	s := Step{Name: "provider_trust"}
	root, err := openRoot(home)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		s.Error(err)
		return s
	}
	defer root.Close()
	st, err := root.Lstat(p.TrustFile)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		s.Error(err)
		return s
	}
	if !st.Mode().IsRegular() || st.Size() > 16<<20 {
		s.Skipped++
		return s
	}
	data, err := root.ReadFile(p.TrustFile)
	if err != nil {
		s.Error(err)
		return s
	}
	var parsed map[string]any
	if err = toml.Unmarshal(data, &parsed); err != nil {
		s.Error(err)
		return s
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	var out []byte
	for i := 0; i < len(lines); {
		if ctx.Err() != nil {
			s.Error(ctx.Err())
			return s
		}
		line := strings.TrimSpace(string(lines[i]))
		j := i + 1
		missing := false
		if strings.HasPrefix(line, "[projects.\"") && strings.HasSuffix(line, "\"]") {
			path, parseErr := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "[projects."), "]"))
			if parseErr == nil && filepath.IsAbs(path) {
				_, statErr := os.Stat(path)
				missing = os.IsNotExist(statErr)
				if statErr != nil && !missing {
					s.Skipped++
				}
			}
			for j < len(lines) && !strings.HasPrefix(strings.TrimSpace(string(lines[j])), "[") {
				j++
			}
		}
		if missing {
			s.Candidates++
		} else {
			for _, ln := range lines[i:j] {
				out = append(out, ln...)
			}
		}
		i = j
	}
	if s.Candidates == 0 || dry {
		return s
	}
	current, err := root.ReadFile(p.TrustFile)
	if err != nil {
		s.Error(err)
		return s
	}
	if !bytes.Equal(current, data) {
		s.Error(fmt.Errorf("trust configuration changed during cleanup"))
		return s
	}
	backup, err := root.OpenFile(p.TrustFile+".cleanup.bak", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = backup.Write(data)
		closeErr := backup.Close()
		if err == nil {
			err = closeErr
		}
	} else if os.IsExist(err) {
		err = nil
	}
	if err != nil {
		s.Error(err)
		return s
	}
	tmp := p.TrustFile + ".cleanup.tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		s.Error(err)
		return s
	}
	defer root.Remove(tmp)
	err = preserveOwnership(f, st)
	if err == nil {
		err = f.Chmod(st.Mode().Perm())
	}
	if err == nil {
		_, err = f.Write(out)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		current, err = root.ReadFile(p.TrustFile)
		if err == nil && !bytes.Equal(current, data) {
			err = fmt.Errorf("trust configuration changed during cleanup")
		}
	}
	if err == nil {
		err = root.Rename(tmp, p.TrustFile)
	}
	if err != nil {
		s.Error(err)
		return s
	}
	s.Deleted = s.Candidates
	s.Bytes = int64(len(data) - len(out))
	return s
}
