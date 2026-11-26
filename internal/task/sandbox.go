package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/adapter"
)

// SandboxesDir returns the per-task sandbox root under the amux directory. The
// sweeper only ever scans this directory and never touches durable instance
// state (which lives elsewhere, e.g. ~/.amux/instances/).
func SandboxesDir(amuxDir string) string { return filepath.Join(amuxDir, "sandboxes") }

// Sandbox is a prepared per-run execution environment. For private-home it owns
// a private HOME/TMPDIR/config; for process isolation it only owns a workspace
// and shares the user's real HOME.
type Sandbox struct {
	TaskID      string
	InstanceID  string // persistent agent instance id; empty for ephemeral tasks
	Isolation   IsolationMode
	Root        string
	Home        string // empty for process isolation
	Tmp         string
	Workspace   string
	AgentConfig string
	Secret      string

	secrets []string // literal secret values for redaction
}

// SandboxKey returns the per-sandbox directory name. Persistent agent instances
// key by instance-<id> so the durable environment (HOME/workspace/non-secret
// cache) is reused across every task on that instance; ephemeral tasks key by
// task id and get a fresh tree each run. The sweeper only ever scans
// SandboxesDir and never touches ~/.amux/instances/.
func SandboxKey(taskID, agentInstanceID string) string {
	if agentInstanceID != "" {
		return "instance-" + agentInstanceID
	}
	return taskID
}

// PrepareSandbox creates the sandbox directory layout for a task. For a
// persistent agent instance (agentInstanceID != "") the layout is keyed by the
// instance so it is reused across tasks; existing directories are left in place
// (MkdirAll is idempotent) so a follow-up task inherits the durable workspace
// and HOME rather than rebuilding them. For ephemeral tasks the layout is keyed
// by taskID and is always fresh.
func PrepareSandbox(amuxDir, taskID, agentInstanceID string, iso IsolationMode) (*Sandbox, error) {
	key := SandboxKey(taskID, agentInstanceID)
	root := filepath.Join(SandboxesDir(amuxDir), key)
	sb := &Sandbox{
		TaskID:      taskID,
		InstanceID:  agentInstanceID,
		Isolation:   iso,
		Root:        root,
		Tmp:         filepath.Join(root, "tmp"),
		Workspace:   filepath.Join(root, "workspace"),
		AgentConfig: filepath.Join(root, "agent-config"),
		Secret:      filepath.Join(root, "secret"),
	}
	dirs := []string{root, sb.Tmp, sb.Workspace, sb.AgentConfig, sb.Secret}
	if iso == IsolationPrivateHome {
		sb.Home = filepath.Join(root, "home")
		dirs = append(dirs,
			sb.Home,
			filepath.Join(sb.Home, ".config"),
			filepath.Join(sb.Home, ".cache"),
		)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0700); err != nil {
			return nil, fmt.Errorf("sandbox mkdir %s: %w", d, err)
		}
	}
	// Restrict the secret directory.
	_ = os.Chmod(sb.Secret, 0700)
	return sb, nil
}

// BaseEnv returns the isolation environment overrides (HOME/TMPDIR/XDG_*). For
// process isolation only TMPDIR is set so temp files stay inside the sandbox.
func (s *Sandbox) BaseEnv() []string {
	env := []string{"TMPDIR=" + s.Tmp}
	if s.Isolation == IsolationPrivateHome && s.Home != "" {
		env = append(env, "HOME="+s.Home)
		for _, binding := range adapter.AllIsolatedHomeEnvBindings() {
			env = append(env, binding.Key+"="+binding.ValueForHome(s.Home))
		}
		env = append(env,
			"XDG_CONFIG_HOME="+filepath.Join(s.Home, ".config"),
			"XDG_CACHE_HOME="+filepath.Join(s.Home, ".cache"),
		)
	}
	return env
}

// Materialize writes credential material into the per-run secret directory and
// returns the env vars to inject. Every injected secret value — the VALUE half
// of each env pair, file contents, and explicit Secrets — is tracked for
// redaction so a provider that supplies a secret only via Env/Files is still
// masked (not left to the regex backstop). The secret directory is always
// destroyed at cleanup.
func (s *Sandbox) Materialize(mat CredentialMaterial) ([]string, error) {
	env := append([]string(nil), mat.Env...)
	for _, kv := range mat.Env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			if v := kv[i+1:]; v != "" {
				s.secrets = append(s.secrets, v)
			}
		}
	}
	for rel, content := range mat.Files {
		dst := filepath.Join(s.Secret, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return nil, fmt.Errorf("materialize mkdir: %w", err)
		}
		if err := os.WriteFile(dst, []byte(content), 0600); err != nil {
			return nil, fmt.Errorf("materialize write %s: %w", rel, err)
		}
		if content != "" {
			s.secrets = append(s.secrets, content)
		}
	}
	s.secrets = append(s.secrets, mat.Secrets...)
	return env, nil
}

// Secrets returns the literal secret values materialized for this run, for use
// in result redaction.
func (s *Sandbox) Secrets() []string { return append([]string(nil), s.secrets...) }

// DestroySecrets removes only the per-run secret materialization. Safe to call
// multiple times; called at every terminal state regardless of debug retention.
func (s *Sandbox) DestroySecrets() error {
	s.secrets = nil
	return os.RemoveAll(s.Secret)
}

// Cleanup destroys secrets unconditionally, then removes home/workspace per the
// retention flags. When neither is retained the whole sandbox root is removed.
func (s *Sandbox) Cleanup(keepWorkspace, keepHome bool) error {
	if err := s.DestroySecrets(); err != nil {
		return err
	}
	if !keepWorkspace && !keepHome {
		return os.RemoveAll(s.Root)
	}
	if !keepWorkspace {
		if err := os.RemoveAll(s.Workspace); err != nil {
			return err
		}
	}
	if !keepHome && s.Home != "" {
		if err := os.RemoveAll(s.Home); err != nil {
			return err
		}
	}
	return nil
}

type CredentialMaterial struct {
	Env      []string          // env vars to inject (KEY=VALUE)
	Files    map[string]string // path relative to secret/ -> file content
	Secrets  []string          // literal secret values, for redaction
	Kind     string            // "long-lived" | "short-lived"
	Profiles []string          // credential profile names that provided this material
}

// CredentialProvider resolves a credential profile name into per-run material.
// main agents only ever pass a profile name; secrets stay in the control plane.
type CredentialProvider interface {
	Materialize(ctx context.Context, profile, agentType string) (CredentialMaterial, error)
}

// EnvCredentialProvider is an optional extension for providers that can filter
// credential material at the source instead of returning a whole profile.
type EnvCredentialProvider interface {
	MaterializeEnv(ctx context.Context, profile, agentType string, envNames []string) (CredentialMaterial, error)
}

// EnvLookupCredentialProvider is an optional extension for providers that can
// resolve env-only credential requirements against the whole credential store.
type EnvLookupCredentialProvider interface {
	MaterializeEnvByName(ctx context.Context, agentType string, envNames []string) (CredentialMaterial, error)
}

type credentialActorTokenContextKey struct{}

// WithCredentialActorToken carries the hub-minted launch credential only for
// credential materialization. It is never persisted in task or session state.
func WithCredentialActorToken(ctx context.Context, token string) context.Context {
	if strings.TrimSpace(token) == "" {
		return ctx
	}
	return context.WithValue(ctx, credentialActorTokenContextKey{}, strings.TrimSpace(token))
}

// WithoutCredentialActorToken explicitly selects installation-scoped
// credential materialization even when an upstream context carries a token.
func WithoutCredentialActorToken(ctx context.Context) context.Context {
	return context.WithValue(ctx, credentialActorTokenContextKey{}, "")
}

// CredentialActorToken returns the launch credential attached by the runtime.
func CredentialActorToken(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	token, _ := ctx.Value(credentialActorTokenContextKey{}).(string)
	return strings.TrimSpace(token)
}

// NoCredentials is a CredentialProvider that injects nothing. Used when a task
// has no credential profile (the child inherits the runtime's own auth).
type NoCredentials struct{}

// Materialize implements CredentialProvider.
func (NoCredentials) Materialize(context.Context, string, string) (CredentialMaterial, error) {
	return CredentialMaterial{}, nil
}

// repoLocks serialises git worktree mutations per repo cache (shared object
// store). See "git-worktree 并发约束".
var (
	repoLocksMu sync.Mutex
	repoLocks   = map[string]*sync.Mutex{}
)

func repoLock(repoDir string) *sync.Mutex {
	repoLocksMu.Lock()
	defer repoLocksMu.Unlock()
	if repoLocks[repoDir] == nil {
		repoLocks[repoDir] = &sync.Mutex{}
	}
	return repoLocks[repoDir]
}

// PrepareGitWorktree creates a git worktree of repoCacheDir at commit into
// dst, serialised per repo cache. The commit must be a concrete ref; a moving
// HEAD is resolved to a SHA before branching to avoid races. It returns the
// resolved base SHA so the caller can diff the workspace against it.
func PrepareGitWorktree(ctx context.Context, repoCacheDir, dst, commit string) (string, error) {
	if repoCacheDir == "" {
		return "", fmt.Errorf("git-worktree requires a repo cache")
	}
	if _, err := os.Stat(filepath.Join(repoCacheDir, ".git")); err != nil {
		// Bare repo or missing cache: surface a clear error.
		if _, err2 := os.Stat(repoCacheDir); err2 != nil {
			return "", fmt.Errorf("repo cache %s not found", repoCacheDir)
		}
	}
	lock := repoLock(repoCacheDir)
	lock.Lock()
	defer lock.Unlock()

	ref := commit
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := runGit(ctx, "-C", repoCacheDir, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	sha = trimNL(sha)
	// Remove any stale worktree registration for dst, then add fresh.
	_, _ = runGit(ctx, "-C", repoCacheDir, "worktree", "prune")
	_ = os.RemoveAll(dst)
	if _, err := runGit(ctx, "-C", repoCacheDir, "worktree", "add", "--detach", dst, sha); err != nil {
		return "", fmt.Errorf("worktree add: %w", err)
	}
	return sha, nil
}

// RemoveGitWorktree tears down a worktree created by PrepareGitWorktree.
func RemoveGitWorktree(ctx context.Context, repoCacheDir, dst string) {
	lock := repoLock(repoCacheDir)
	lock.Lock()
	defer lock.Unlock()
	_, _ = runGit(ctx, "-C", repoCacheDir, "worktree", "remove", "--force", dst)
	_, _ = runGit(ctx, "-C", repoCacheDir, "worktree", "prune")
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// persistentWorktreeReady reports whether the sandbox workspace already holds a
// git worktree from a prior task on the same persistent instance. A worktree's
// workspace has a .git file (or dir) rather than a full .git repository; its
// presence means the durable workspace is already populated and must not be
// rebuilt. Ephemeral sandboxes are always fresh, so this is only meaningful for
// instance-keyed sandboxes.
func persistentWorktreeReady(workspaceDir string) bool {
	if workspaceDir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(workspaceDir, ".git"))
	return err == nil && info != nil
}

// resolveBaseCommit resolves a commit ref (empty -> HEAD) to a concrete SHA in
// the repo cache, without creating a worktree. Used when reusing an existing
// persistent worktree so changed_files can still diff against the base.
func resolveBaseCommit(ctx context.Context, repoCacheDir, commit string) (string, error) {
	ref := commit
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := runGit(ctx, "-C", repoCacheDir, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return trimNL(sha), nil
}
