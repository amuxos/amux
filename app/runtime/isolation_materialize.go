package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

// materializeLinuxDACTaskLaunch moves launch-scoped task artifacts out of the
// service-owned legacy sandbox into the helper-prepared run directory. The
// fixed target-UID worker writes the complete run/workspace trees, so neither
// the root helper nor the provider needs access to the amuxlet user's sandbox.
func materializeLinuxDACTaskLaunch(
	materialize func([]isolation.BundleSource) error,
	sourceWorkspace string,
	paths isolation.PrincipalPaths,
	home isolatedHomeProjection,
	env, recordEnv, args []string,
) ([]string, []string, []string, error) {
	sourceWorkspace = filepath.Clean(sourceWorkspace)
	if filepath.Base(sourceWorkspace) != "workspace" {
		return nil, nil, nil, fmt.Errorf("task workspace does not use the managed sandbox layout")
	}
	sourceRoot := filepath.Dir(sourceWorkspace)
	if strings.TrimSpace(paths.Workspace) == "" || strings.TrimSpace(paths.Run) == "" {
		return nil, nil, nil, fmt.Errorf("principal workspace and run paths are required")
	}
	mappings := []pathMapping{
		{from: filepath.Join(sourceRoot, "agent-config"), to: filepath.Join(paths.Run, "agent-config"), prefix: "agent-config"},
		{from: filepath.Join(sourceRoot, "secret"), to: filepath.Join(paths.Run, "secret"), prefix: "secret"},
		{from: filepath.Join(sourceRoot, "tmp"), to: filepath.Join(paths.Run, "tmp"), prefix: "tmp"},
		{from: sourceWorkspace, to: paths.Workspace, prefix: "workspace"},
	}
	sources := make([]isolation.BundleSource, 0, len(mappings))
	for _, mapping := range mappings {
		info, err := os.Lstat(mapping.from)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, nil, fmt.Errorf("%s is not a real directory", mapping.from)
		}
		sources = append(sources, isolation.BundleSource{
			Source: mapping.from,
			Prefix: mapping.prefix,
		})
	}
	stagedHome, err := stageIsolatedHomeFiles(home)
	if err != nil {
		return nil, nil, nil, err
	}
	if stagedHome != "" {
		defer os.RemoveAll(stagedHome)
		sources = append(sources, isolation.BundleSource{Source: stagedHome, Prefix: "home"})
	}
	if materialize == nil {
		return nil, nil, nil, fmt.Errorf("target-uid materializer is required")
	}
	if err := materialize(sources); err != nil {
		return nil, nil, nil, err
	}
	return rewriteLaunchPaths(env, mappings), rewriteLaunchPaths(recordEnv, mappings), rewriteLaunchPaths(args, mappings), nil
}

func materializeLinuxDACSessionLaunch(
	materialize func([]isolation.BundleSource) error,
	sourceWorkspace, sourceConfig, previousLaunchID string,
	paths isolation.PrincipalPaths,
	home isolatedHomeProjection,
	env, recordEnv, args []string,
) ([]string, []string, []string, error) {
	var sources []isolation.BundleSource
	var mappings []pathMapping
	if strings.TrimSpace(sourceWorkspace) != "" {
		info, err := os.Lstat(sourceWorkspace)
		if err != nil {
			return nil, nil, nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, nil, fmt.Errorf("session launch workspace is not a real directory")
		}
		sources = append(sources, isolation.BundleSource{Source: sourceWorkspace, Prefix: "workspace"})
		mappings = append(mappings, pathMapping{from: filepath.Clean(sourceWorkspace), to: paths.Workspace})
	}
	if strings.TrimSpace(sourceConfig) != "" {
		info, err := os.Lstat(sourceConfig)
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, nil, err
		}
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, nil, nil, fmt.Errorf("session launch config is not a real directory")
			}
			target := filepath.Join(paths.Run, "agent-config")
			sources = append(sources, isolation.BundleSource{Source: sourceConfig, Prefix: "agent-config"})
			mappings = append(mappings, pathMapping{from: filepath.Clean(sourceConfig), to: target})
			if previousLaunchID != "" {
				mappings = append(mappings, pathMapping{
					from: filepath.Join(filepath.Dir(paths.Run), previousLaunchID, "agent-config"),
					to:   target,
				})
			}
		}
	}
	stagedHome, err := stageIsolatedHomeFiles(home)
	if err != nil {
		return nil, nil, nil, err
	}
	if stagedHome != "" {
		defer os.RemoveAll(stagedHome)
		sources = append(sources, isolation.BundleSource{Source: stagedHome, Prefix: "home"})
	}
	if len(sources) == 0 {
		return env, recordEnv, args, nil
	}
	if materialize == nil {
		return nil, nil, nil, fmt.Errorf("target-uid materializer is required")
	}
	if err := materialize(sources); err != nil {
		return nil, nil, nil, err
	}
	return rewriteLaunchPaths(env, mappings), rewriteLaunchPaths(recordEnv, mappings), rewriteLaunchPaths(args, mappings), nil
}

type pathMapping struct {
	from   string
	to     string
	prefix string
}

func rewriteLaunchPaths(values []string, mappings []pathMapping) []string {
	out := append([]string(nil), values...)
	for i, value := range out {
		for _, mapping := range mappings {
			value = strings.ReplaceAll(value, mapping.from, mapping.to)
		}
		out[i] = value
	}
	return out
}

// isolatedHomeProjection describes the provider credentials copied into a
// principal HOME for one launch, plus how to judge whether they are still
// usable. Expiry/Warn are optional; without them the projection is silent.
type isolatedHomeProjection struct {
	SourceHome string
	Files      []string
	// Expiry reports when a credential lapses. The second result is false when
	// the format carries no expiry or cannot be parsed, which is never treated
	// as expired.
	Expiry func(relPath string, data []byte) (time.Time, bool)
	Now    time.Time
	Warn   func(string)
}

// credentialExpiryWarnWindow is how long before a credential lapses amux starts
// saying so. One day gives the operator an actionable reminder without warning
// every newly launched session for several days.
const credentialExpiryWarnWindow = 24 * time.Hour

// stageIsolatedHomeFiles copies HOME-relative provider credentials from the
// service user's home into a staging tree the materialization bundle can carry.
// The bundle only transports logical prefixes, so the files have to be laid out
// under a real directory first. Returns "" when the agent declares no such
// files or none of them exist; the caller owns the returned directory.
//
// An expired credential is reported, not refused: the projection is a copy of
// whatever the service account currently holds, and blocking the launch on a
// parsed timestamp would turn a warning into an outage.
func stageIsolatedHomeFiles(projection isolatedHomeProjection) (string, error) {
	sourceHome := strings.TrimSpace(projection.SourceHome)
	if sourceHome == "" || len(projection.Files) == 0 {
		return "", nil
	}
	var staged string
	for _, rel := range projection.Files {
		rel = filepath.Clean(strings.TrimSpace(rel))
		if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("isolated home file %q escapes HOME", rel)
		}
		source := filepath.Join(sourceHome, rel)
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("isolated home file %s is not a regular file", rel)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return "", err
		}
		reportCredentialExpiry(projection, rel, data)
		if staged == "" {
			if staged, err = os.MkdirTemp("", "amux-isolated-home-"); err != nil {
				return "", err
			}
		}
		target := filepath.Join(staged, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			_ = os.RemoveAll(staged)
			return "", err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			_ = os.RemoveAll(staged)
			return "", err
		}
	}
	return staged, nil
}

func reportCredentialExpiry(projection isolatedHomeProjection, rel string, data []byte) {
	if projection.Expiry == nil || projection.Warn == nil {
		return
	}
	at, ok := projection.Expiry(rel, data)
	if !ok {
		return
	}
	now := projection.Now
	if now.IsZero() {
		now = time.Now()
	}
	loginCommand := distributionCredentialLoginCommand(rel)
	switch {
	case now.After(at):
		projection.Warn(fmt.Sprintf(
			"provider credential %s expired at %s; the agent will answer nothing until the service account runs "+
				"%s to re-authenticate "+
				"(its copy is what every principal launch projects)",
			rel, at.UTC().Format(time.RFC3339), loginCommand))
	case at.Sub(now) < credentialExpiryWarnWindow:
		projection.Warn(fmt.Sprintf(
			"provider credential %s expires in %s (%s); run %s on the service account before then",
			rel, at.Sub(now).Round(time.Hour), at.UTC().Format(time.RFC3339), loginCommand))
	}
}

// isolatedHomeCachesFor translates an agent's declared reclaimable caches into
// the helper wire form. The adapter layer stays free of isolation types.
func isolatedHomeCachesFor(agentType string) []isolation.HomeCacheSpec {
	a, err := adapter.Get(agentType)
	if err != nil {
		return nil
	}
	reclaimer, ok := a.(adapter.IsolatedHomeReclaimer)
	if !ok {
		return nil
	}
	caches := reclaimer.IsolatedHomeCaches()
	specs := make([]isolation.HomeCacheSpec, 0, len(caches))
	for _, cache := range caches {
		specs = append(specs, isolation.HomeCacheSpec{
			Path:     filepath.ToSlash(cache.Path),
			MaxBytes: cache.MaxBytes,
			Remove:   append([]string(nil), cache.Remove...),
		})
	}
	return specs
}

// isolatedHomeFilesFor reports the HOME-relative credentials the agent type
// needs projected into a principal HOME. Unknown agent types and adapters
// without the capability contribute nothing.
func isolatedHomeFilesFor(agentType string) []string {
	a, err := adapter.Get(agentType)
	if err != nil {
		return nil
	}
	provisioner, ok := a.(adapter.IsolatedHomeProvisioner)
	if !ok {
		return nil
	}
	return provisioner.IsolatedHomeFiles()
}

// serviceHomeDir is the home of the amuxlet service user, where provider CLIs
// stored their account credentials before isolation moved the agent elsewhere.
func serviceHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return os.Getenv("HOME")
}

// isolatedHomeProjectionFor assembles one launch's credential projection,
// including the adapter's own expiry reader when it has one.
func isolatedHomeProjectionFor(agentType string, warn func(string)) isolatedHomeProjection {
	projection := isolatedHomeProjection{
		SourceHome: serviceHomeDir(),
		Files:      isolatedHomeFilesFor(agentType),
		Now:        time.Now(),
		Warn:       warn,
	}
	if a, err := adapter.Get(agentType); err == nil {
		if inspector, ok := a.(adapter.IsolatedHomeCredentialInspector); ok {
			projection.Expiry = inspector.CredentialExpiry
		}
	}
	return projection
}

// principalWorkspaceInUse reports whether another session still addresses the
// same principal workspace. Persistent agent instances deliberately share one
// durable workspace across executions, so reclaiming on the first teardown
// would delete a live agent's state.
func principalWorkspaceInUse(self session.Snapshot, all []session.Snapshot) bool {
	if self.ExecutionIdentity.IsolationBackend != principal.BackendLinuxDAC {
		return false
	}
	want := isolation.ResolveExecutionWorkspaceID(self.Cwd, self.ExecutionIdentity)
	for _, other := range all {
		if other.ID == self.ID {
			continue
		}
		if other.ExecutionIdentity.IsolationBackend != principal.BackendLinuxDAC {
			continue
		}
		if isolation.ResolveExecutionWorkspaceID(other.Cwd, other.ExecutionIdentity) == want {
			return true
		}
	}
	return false
}
