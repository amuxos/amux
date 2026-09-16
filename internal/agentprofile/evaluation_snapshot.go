package agentprofile

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxSnapshotSkillFiles = 512
	maxSnapshotSkillBytes = 16 << 20
)

func attachSnapshotSkillFiles(profile Profile) (Profile, error) {
	if len(profile.Skills) == 0 || strings.TrimSpace(profile.PackPath) == "" {
		return profile, nil
	}
	dirs := profile.SkillDirs
	if len(dirs) == 0 {
		dirs = defaultSkillDirs()
	}
	total := 0
	for _, skill := range profile.Skills {
		source, ok, err := findProfileSkillDir(profile.PackPath, dirs, skill)
		if err != nil {
			return Profile{}, err
		}
		if !ok {
			continue
		}
		err = filepath.WalkDir(source, func(name string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("snapshot skill %s contains unsupported file %s", skill, name)
			}
			rel, err := filepath.Rel(source, name)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			total += len(data)
			if len(profile.SnapshotSkillFiles) >= maxSnapshotSkillFiles || total > maxSnapshotSkillBytes {
				return fmt.Errorf("snapshot skills exceed %d files or %d bytes", maxSnapshotSkillFiles, maxSnapshotSkillBytes)
			}
			profile.SnapshotSkillFiles = append(profile.SnapshotSkillFiles, ProfileSkillFile{
				Skill: skill, Path: filepath.ToSlash(rel), Data: data, Mode: uint32(info.Mode().Perm()),
			})
			return nil
		})
		if err != nil {
			return Profile{}, err
		}
	}
	return profile, nil
}

func projectSnapshotSkillFiles(profile Profile, root, dstBase string) ([]string, error) {
	declared := make(map[string]struct{}, len(profile.Skills))
	for _, skill := range profile.Skills {
		declared[skill] = struct{}{}
	}
	files := make([]string, 0, len(profile.SnapshotSkillFiles))
	for _, file := range profile.SnapshotSkillFiles {
		if _, ok := declared[file.Skill]; !ok || strings.Contains(file.Skill, "/") || file.Skill == "" || file.Skill == "." || file.Skill == ".." {
			return nil, fmt.Errorf("snapshot skill file has invalid skill %q", file.Skill)
		}
		rel := strings.TrimSpace(filepath.ToSlash(file.Path))
		if rel == "" || rel == "." || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("snapshot skill %s has invalid path %q", file.Skill, file.Path)
		}
		dstRel := filepath.Join(dstBase, file.Skill, filepath.FromSlash(rel))
		mode := os.FileMode(file.Mode).Perm()
		if mode == 0 {
			mode = 0600
		}
		if err := writeSnapshotFile(filepath.Join(root, dstRel), file.Data, mode); err != nil {
			return nil, err
		}
		files = append(files, dstRel)
	}
	return files, nil
}

func writeSnapshotFile(name string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	return os.WriteFile(name, data, mode)
}
