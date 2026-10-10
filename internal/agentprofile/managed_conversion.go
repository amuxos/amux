package agentprofile

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ConvertV2 is an explicit operator repair, never an install/update side effect.
// The source commit remains in history; execution settings are not transferred
// to runtime configuration or existing Session/Task snapshots.
func (m *ManagedLibrary) ConvertV2(ctx context.Context, expectedRevision string) (ManagedLibraryMutation, error) {
	expectedRevision = strings.TrimSpace(expectedRevision)
	if !managedRevisionPattern.MatchString(expectedRevision) {
		return ManagedLibraryMutation{}, profileError("conversion_revision_required")
	}
	var result ManagedLibraryMutation
	err := m.withLock(func() error {
		// Normalize legacy registry metadata only as part of publication, so
		// rejected conversions and v2 no-ops leave the registry untouched.
		if err := m.validateIdentityWithRepair(false); err != nil {
			return err
		}
		if !m.originExists() {
			return profileError("conversion_uninitialized")
		}
		current, err := m.currentRevision(ctx)
		if err != nil {
			return err
		}
		if current != expectedRevision {
			return profileError("conversion_revision_conflict", expectedRevision, current)
		}
		data, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "show", current+":agent-pack.toml")
		if err != nil {
			return err
		}
		var source map[string]any
		if err := toml.Unmarshal([]byte(data), &source); err != nil {
			return err
		}
		switch source["schema_version"] {
		case "v2":
			result = managedMutation("", current)
			return nil
		case "v1.1":
		default:
			return profileError("conversion_schema_unsupported", source["schema_version"])
		}
		// An initialized but unpublished library has no profiles or manifest.
		// Convert its scaffold without registering an invalid empty Pack.
		paths, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "ls-tree", "-r", "-z", "--name-only", current, "--", "profiles")
		if err != nil {
			return err
		}
		empty := true
		for _, path := range strings.Split(paths, "\x00") {
			if strings.HasSuffix(path, ".toml") {
				empty = false
				break
			}
		}
		if empty {
			registry, err := LoadPackRegistry(m.registryPath)
			if err != nil {
				return err
			}
			if _, registered := registry.Packs[ManagedLocalPackNamespace]; registered {
				return profileError("conversion_empty_registered")
			}
		}
		result, err = m.mutateExistingSource(ctx, current, "", convertManagedV2Source, "amux: convert local expert library source to v2", empty)
		return err
	})
	return result, err
}

func convertManagedV2Source(root string) error {
	if err := rewriteManagedTOML(filepath.Join(root, "agent-pack.toml"), func(source map[string]any) {
		source["schema_version"] = "v2"
		delete(source, "providers")
	}); err != nil {
		return err
	}
	profiles := filepath.Join(root, "profiles")
	if info, err := os.Lstat(profiles); err == nil {
		if !info.IsDir() {
			return profileError("conversion_symlink", profiles)
		}
		if err := filepath.WalkDir(profiles, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return profileError("conversion_symlink", path)
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".toml") {
				return nil
			}
			return rewriteManagedTOML(path, func(source map[string]any) {
				for _, key := range []string{"type", "model", "providers", "supported_models", "permission"} {
					delete(source, key)
				}
			})
		}); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Provider connection files remain recoverable from the previous commit.
	return os.RemoveAll(filepath.Join(root, "providers"))
}

func rewriteManagedTOML(path string, update func(map[string]any)) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return profileError("conversion_symlink", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var source map[string]any
	if err := toml.Unmarshal(data, &source); err != nil {
		return err
	}
	update(source)
	data, err = toml.Marshal(source)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, info.Mode().Perm())
}
