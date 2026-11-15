package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// FileCredentials is a CredentialProvider backed by per-profile JSON files under
// Dir (default <amuxDir>/secrets). A profile "<name>" resolves to
// "<Dir>/<name>.json":
//
//	{
//	  "kind":  "long-lived" | "short-lived",
//	  "env":   { "ANTHROPIC_API_KEY": "sk-..." },
//	  "files": { ".config/agent/credentials.json": "<content>" }
//	}
//
// The resolved material is injected and redacted by Sandbox.Materialize. Secrets
// stay in the control plane; main agents only ever pass the profile name.
type FileCredentials struct {
	Dir string
}

type credentialFile struct {
	Kind  string            `json:"kind"`
	Env   map[string]string `json:"env"`
	Files map[string]string `json:"files"`
}

// CredentialEnvNotFoundError reports that no credential profile defines an env.
type CredentialEnvNotFoundError struct {
	EnvName string
}

func (e *CredentialEnvNotFoundError) Error() string {
	return fmt.Sprintf("credential store does not define env %s", e.EnvName)
}

// CredentialEnvAmbiguousError reports that more than one profile defines an env.
type CredentialEnvAmbiguousError struct {
	EnvName       string
	FirstProfile  string
	SecondProfile string
}

func (e *CredentialEnvAmbiguousError) Error() string {
	return fmt.Sprintf("credential env %s is provided by multiple profiles: %q and %q", e.EnvName, e.FirstProfile, e.SecondProfile)
}

var fileCredentialProfileNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var fileCredentialEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var fileCredentialWriteMu sync.Mutex

// CredentialProfileMetadata is the non-secret view used by credential
// management surfaces. Values are deliberately never included.
type CredentialProfileMetadata struct {
	Profile   string
	Kind      string
	EnvNames  []string
	FileNames []string
	Path      string
}

// SetEnv updates one environment secret while preserving the profile's other
// env and file entries.
func (f FileCredentials) SetEnv(profile, envName, value string) (CredentialProfileMetadata, error) {
	profile = strings.TrimSpace(profile)
	envName = strings.TrimSpace(envName)
	if !fileCredentialProfileNamePattern.MatchString(profile) {
		return CredentialProfileMetadata{}, fmt.Errorf("invalid credential profile %q", profile)
	}
	if !fileCredentialEnvNamePattern.MatchString(envName) {
		return CredentialProfileMetadata{}, fmt.Errorf("invalid credential env name %q", envName)
	}
	if value == "" {
		return CredentialProfileMetadata{}, fmt.Errorf("credential env %s value must not be empty", envName)
	}

	fileCredentialWriteMu.Lock()
	defer fileCredentialWriteMu.Unlock()

	cf, err := f.readProfile(profile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return CredentialProfileMetadata{}, err
		}
		cf = credentialFile{}
	}
	if cf.Kind == "" {
		cf.Kind = "long-lived"
	}
	if cf.Env == nil {
		cf.Env = make(map[string]string)
	}
	cf.Env[envName] = value
	if err := f.writeProfile(profile, cf); err != nil {
		return CredentialProfileMetadata{}, err
	}
	return credentialMetadata(profile, filepath.Join(f.Dir, profile+".json"), cf), nil
}

// PutEnv is SetEnv for management surfaces: it refuses to replace a store file
// that exists but is not a credential profile.
func (f FileCredentials) PutEnv(profile, envName, value string) (CredentialProfileMetadata, error) {
	envName = strings.TrimSpace(envName)
	if !fileCredentialEnvNamePattern.MatchString(envName) {
		return CredentialProfileMetadata{}, fmt.Errorf("invalid credential env name %q", envName)
	}
	if value == "" {
		return CredentialProfileMetadata{}, fmt.Errorf("credential env %s value must not be empty", envName)
	}
	return f.upsert(profile, func(cf *credentialFile) error {
		if cf.Env == nil {
			cf.Env = make(map[string]string)
		}
		cf.Env[envName] = value
		return nil
	})
}

// UnsetEnv removes one environment secret and keeps the profile's other env
// and file entries. Removing a name the profile does not define is an error.
func (f FileCredentials) UnsetEnv(profile, envName string) (CredentialProfileMetadata, error) {
	envName = strings.TrimSpace(envName)
	if !fileCredentialEnvNamePattern.MatchString(envName) {
		return CredentialProfileMetadata{}, fmt.Errorf("invalid credential env name %q", envName)
	}
	return f.update(profile, func(cf *credentialFile) error {
		if _, ok := cf.Env[envName]; !ok {
			return fmt.Errorf("credential profile %q does not define env %s: %w", profile, envName, fs.ErrNotExist)
		}
		delete(cf.Env, envName)
		return nil
	})
}

// SetFile stores one credential file, addressed by a relative path inside the
// execution home, while preserving the profile's other entries.
func (f FileCredentials) SetFile(profile, name, content string) (CredentialProfileMetadata, error) {
	name = strings.TrimSpace(name)
	if !ValidCredentialFileName(name) {
		return CredentialProfileMetadata{}, fmt.Errorf("invalid credential file name %q", name)
	}
	if content == "" {
		return CredentialProfileMetadata{}, fmt.Errorf("credential file %s content must not be empty", name)
	}
	return f.upsert(profile, func(cf *credentialFile) error {
		if cf.Files == nil {
			cf.Files = make(map[string]string)
		}
		cf.Files[name] = content
		return nil
	})
}

// UnsetFile removes one credential file entry.
func (f FileCredentials) UnsetFile(profile, name string) (CredentialProfileMetadata, error) {
	name = strings.TrimSpace(name)
	return f.update(profile, func(cf *credentialFile) error {
		if _, ok := cf.Files[name]; !ok {
			return fmt.Errorf("credential profile %q does not define file %s: %w", profile, name, fs.ErrNotExist)
		}
		delete(cf.Files, name)
		return nil
	})
}

// DeleteProfile removes a whole credential profile. Only files that parse as a
// credential profile are removed; any other JSON in the store is left alone.
func (f FileCredentials) DeleteProfile(profile string) error {
	profile = strings.TrimSpace(profile)
	if !fileCredentialProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("invalid credential profile %q", profile)
	}
	fileCredentialWriteMu.Lock()
	defer fileCredentialWriteMu.Unlock()
	cf, err := f.readProfile(profile)
	if err != nil {
		return err
	}
	if !cf.credentialProfile() {
		return fmt.Errorf("%s is not a credential profile: %w", profile, ErrNotCredentialProfile)
	}
	if err := os.Remove(filepath.Join(f.Dir, profile+".json")); err != nil {
		return fmt.Errorf("remove credential profile %q: %w", profile, err)
	}
	return nil
}

// ErrNotCredentialProfile marks a store file that exists but holds something
// other than a credential profile. Writers refuse to replace it.
var ErrNotCredentialProfile = errors.New("not a credential profile")

// ValidCredentialFileName accepts a clean relative path that stays inside the
// execution home: no absolute path, parent segment, or control character.
func ValidCredentialFileName(name string) bool {
	if name == "" || len(name) > 256 || filepath.IsAbs(name) || strings.HasPrefix(name, "~") {
		return false
	}
	if filepath.Clean(name) != name || name == "." || strings.Contains(name, "\\") {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (cf credentialFile) credentialProfile() bool {
	return cf.Kind != "" || len(cf.Env) > 0 || len(cf.Files) > 0
}

// update edits an existing credential profile under the store lock.
func (f FileCredentials) update(profile string, edit func(*credentialFile) error) (CredentialProfileMetadata, error) {
	profile = strings.TrimSpace(profile)
	if !fileCredentialProfileNamePattern.MatchString(profile) {
		return CredentialProfileMetadata{}, fmt.Errorf("invalid credential profile %q", profile)
	}
	fileCredentialWriteMu.Lock()
	defer fileCredentialWriteMu.Unlock()
	cf, err := f.readProfile(profile)
	if err != nil {
		return CredentialProfileMetadata{}, err
	}
	if !cf.credentialProfile() {
		return CredentialProfileMetadata{}, fmt.Errorf("%s is not a credential profile: %w", profile, ErrNotCredentialProfile)
	}
	if err := edit(&cf); err != nil {
		return CredentialProfileMetadata{}, err
	}
	if err := f.writeProfile(profile, cf); err != nil {
		return CredentialProfileMetadata{}, err
	}
	return credentialMetadata(profile, filepath.Join(f.Dir, profile+".json"), cf), nil
}

// upsert edits a credential profile, creating it when absent. A store file that
// is not a credential profile is never overwritten.
func (f FileCredentials) upsert(profile string, edit func(*credentialFile) error) (CredentialProfileMetadata, error) {
	profile = strings.TrimSpace(profile)
	if !fileCredentialProfileNamePattern.MatchString(profile) {
		return CredentialProfileMetadata{}, fmt.Errorf("invalid credential profile %q", profile)
	}
	fileCredentialWriteMu.Lock()
	defer fileCredentialWriteMu.Unlock()
	cf, err := f.readProfile(profile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cf = credentialFile{}
	case err != nil:
		return CredentialProfileMetadata{}, err
	case !cf.credentialProfile():
		return CredentialProfileMetadata{}, fmt.Errorf("%s is not a credential profile: %w", profile, ErrNotCredentialProfile)
	}
	if cf.Kind == "" {
		cf.Kind = "long-lived"
	}
	if err := edit(&cf); err != nil {
		return CredentialProfileMetadata{}, err
	}
	if err := f.writeProfile(profile, cf); err != nil {
		return CredentialProfileMetadata{}, err
	}
	return credentialMetadata(profile, filepath.Join(f.Dir, profile+".json"), cf), nil
}

// Metadata reads one profile without exposing any secret values.
func (f FileCredentials) Metadata(profile string) (CredentialProfileMetadata, error) {
	profile = strings.TrimSpace(profile)
	if !fileCredentialProfileNamePattern.MatchString(profile) {
		return CredentialProfileMetadata{}, fmt.Errorf("invalid credential profile %q", profile)
	}
	cf, err := f.readProfile(profile)
	if err != nil {
		return CredentialProfileMetadata{}, err
	}
	return credentialMetadata(profile, filepath.Join(f.Dir, profile+".json"), cf), nil
}

// ListMetadata lists valid, non-empty credential profiles without values.
func (f FileCredentials) ListMetadata() ([]CredentialProfileMetadata, error) {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read credential store %s: %w", f.Dir, err)
	}
	profiles := make([]CredentialProfileMetadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		profile := strings.TrimSuffix(entry.Name(), ".json")
		if !fileCredentialProfileNamePattern.MatchString(profile) {
			continue
		}
		meta, err := f.Metadata(profile)
		if err != nil {
			return nil, err
		}
		if meta.Kind == "" && len(meta.EnvNames) == 0 && len(meta.FileNames) == 0 {
			continue
		}
		profiles = append(profiles, meta)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Profile < profiles[j].Profile })
	return profiles, nil
}

// Materialize implements CredentialProvider. An empty profile injects nothing
// (parity with NoCredentials); a missing or malformed profile file is an error.
func (f FileCredentials) Materialize(_ context.Context, profile, _ string) (CredentialMaterial, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return CredentialMaterial{}, nil
	}
	if !fileCredentialProfileNamePattern.MatchString(profile) {
		return CredentialMaterial{}, fmt.Errorf("invalid credential profile %q", profile)
	}
	path := filepath.Join(f.Dir, profile+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CredentialMaterial{}, fmt.Errorf("credential profile %q not found at %s", profile, path)
		}
		return CredentialMaterial{}, fmt.Errorf("read credential profile %q: %w", profile, err)
	}
	var cf credentialFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return CredentialMaterial{}, fmt.Errorf("parse credential profile %q: %w", profile, err)
	}

	// Deterministic env ordering so injection (and any logging) is stable.
	keys := make([]string, 0, len(cf.Env))
	for k := range cf.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+cf.Env[k])
	}

	return CredentialMaterial{
		Env:      env,
		Files:    cf.Files,
		Kind:     cf.Kind,
		Profiles: []string{profile},
	}, nil
}

// MaterializeEnv implements EnvCredentialProvider by reading only the requested
// env keys from one profile.
func (f FileCredentials) MaterializeEnv(ctx context.Context, profile, agentType string, envNames []string) (CredentialMaterial, error) {
	mat, err := f.Materialize(ctx, profile, agentType)
	if err != nil {
		return CredentialMaterial{}, err
	}
	return filterCredentialMaterial(mat, profile, envNames)
}

// MaterializeEnvByName implements EnvLookupCredentialProvider. It searches all
// profile JSON files in the credential store and requires each requested env to
// be provided by exactly one profile.
func (f FileCredentials) MaterializeEnvByName(_ context.Context, _ string, envNames []string) (CredentialMaterial, error) {
	requested := uniqueCredentialEnvNames(envNames)
	if len(requested) == 0 {
		return CredentialMaterial{}, nil
	}
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return CredentialMaterial{}, &CredentialEnvNotFoundError{EnvName: requested[0]}
		}
		return CredentialMaterial{}, fmt.Errorf("read credential store %s: %w", f.Dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	type foundEnv struct {
		profile string
		value   string
		kind    string
	}
	found := make(map[string]foundEnv, len(requested))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		profile := strings.TrimSuffix(entry.Name(), ".json")
		if !fileCredentialProfileNamePattern.MatchString(profile) {
			continue
		}
		cf, err := f.readProfile(profile)
		if err != nil {
			return CredentialMaterial{}, err
		}
		for _, envName := range requested {
			value, ok := cf.Env[envName]
			if !ok {
				continue
			}
			if prev, exists := found[envName]; exists && prev.profile != profile {
				return CredentialMaterial{}, &CredentialEnvAmbiguousError{
					EnvName:       envName,
					FirstProfile:  prev.profile,
					SecondProfile: profile,
				}
			}
			found[envName] = foundEnv{profile: profile, value: value, kind: cf.Kind}
		}
	}

	out := CredentialMaterial{}
	for _, envName := range requested {
		item, ok := found[envName]
		if !ok {
			return CredentialMaterial{}, &CredentialEnvNotFoundError{EnvName: envName}
		}
		out.Env = append(out.Env, envName+"="+item.value)
		out.Profiles = appendUniqueString(out.Profiles, item.profile)
		if out.Kind == "" {
			out.Kind = item.kind
		}
	}
	return out, nil
}

func (f FileCredentials) readProfile(profile string) (credentialFile, error) {
	path := filepath.Join(f.Dir, profile+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return credentialFile{}, fmt.Errorf("credential profile %q not found at %s: %w", profile, path, err)
		}
		return credentialFile{}, fmt.Errorf("read credential profile %q: %w", profile, err)
	}
	var cf credentialFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return credentialFile{}, fmt.Errorf("parse credential profile %q: %w", profile, err)
	}
	return cf, nil
}

func (f FileCredentials) writeProfile(profile string, cf credentialFile) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return fmt.Errorf("create credential store %s: %w", f.Dir, err)
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credential profile %q: %w", profile, err)
	}
	data = append(data, '\n')
	path := filepath.Join(f.Dir, profile+".json")
	tmp, err := os.CreateTemp(f.Dir, ".credential-*.tmp")
	if err != nil {
		return fmt.Errorf("create credential profile temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace credential profile %q: %w", profile, err)
	}
	return os.Chmod(path, 0o600)
}

func credentialMetadata(profile, path string, cf credentialFile) CredentialProfileMetadata {
	return CredentialProfileMetadata{
		Profile:   profile,
		Kind:      cf.Kind,
		EnvNames:  sortedCredentialMapKeys(cf.Env),
		FileNames: sortedCredentialMapKeys(cf.Files),
		Path:      path,
	}
}

func sortedCredentialMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueCredentialEnvNames(envNames []string) []string {
	out := make([]string, 0, len(envNames))
	for _, envName := range envNames {
		out = appendUniqueString(out, envName)
	}
	return out
}
