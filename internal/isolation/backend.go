package isolation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/amuxos/amux/internal/cleanup"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
)

type Runtime struct {
	cfg    config.IsolationConfig
	store  *MappingStore
	client *HelperClient

	mu       sync.RWMutex
	ready    bool
	degraded string
	warnOnce sync.Once
}

type RuntimeStatus struct {
	Platform          string          `json:"platform"`
	Enabled           bool            `json:"enabled"`
	Configured        string          `json:"configured_backend"`
	Selected          string          `json:"selected_backend"`
	HelperReady       bool            `json:"helper_ready"`
	ProtocolVersion   int             `json:"protocol_version"`
	HelperVersion     string          `json:"helper_version,omitempty"`
	DegradedReason    string          `json:"degraded_reason,omitempty"`
	HelperSocket      string          `json:"helper_socket,omitempty"`
	MappingDB         string          `json:"mapping_db,omitempty"`
	Hardening         map[string]bool `json:"hardening,omitempty"`
	HardeningDegraded []string        `json:"hardening_degraded,omitempty"`
}

func NewRuntime(cfg config.IsolationConfig) (*Runtime, error) {
	if err := config.ValidateIsolation(cfg); err != nil {
		return nil, err
	}
	rt := &Runtime{cfg: cfg, client: NewHelperClient(cfg.HelperSocket)}
	if !cfg.Enabled || cfg.Backend == principal.BackendPassthrough {
		rt.degraded = "isolation disabled or passthrough configured"
		return rt, nil
	}
	if runtime.GOOS != "linux" {
		rt.degraded = "linux-dac is unavailable on this platform"
		return rt, nil
	}
	store, err := OpenMappingStoreRange(cfg.MappingDB, cfg.UIDRangeStart, cfg.UIDRangeEnd)
	if err != nil {
		rt.degraded = "mapping store unavailable"
		return rt, nil
	}
	rt.store = store
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := rt.client.Handshake(ctx); err != nil {
		rt.degraded = "helper handshake unavailable"
		return rt, nil
	}
	rt.ready = true
	return rt, nil
}

func (r *Runtime) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *Runtime) Status(ctx context.Context) RuntimeStatus {
	if r == nil {
		return RuntimeStatus{Platform: runtime.GOOS, Selected: principal.BackendPassthrough, DegradedReason: "runtime not initialized"}
	}
	if r.cfg.Enabled && runtime.GOOS == "linux" && r.cfg.Backend != principal.BackendPassthrough {
		r.refreshHelperReady(ctx)
	}
	r.mu.RLock()
	ready, degraded := r.ready, r.degraded
	r.mu.RUnlock()
	status := RuntimeStatus{
		Platform:        runtime.GOOS,
		Enabled:         r.cfg.Enabled,
		Configured:      r.cfg.Backend,
		Selected:        principal.BackendPassthrough,
		HelperReady:     ready,
		ProtocolVersion: HelperProtocolVersion,
		DegradedReason:  degraded,
		HelperSocket:    r.cfg.HelperSocket,
		MappingDB:       r.cfg.MappingDB,
	}
	if ready {
		status.Selected = principal.BackendLinuxDAC
		if resp, err := r.client.Handshake(ctx); err == nil {
			status.HelperVersion = resp.HelperVersion
			status.Hardening = resp.Hardening
			status.HardeningDegraded = append([]string(nil), resp.DegradedReasons...)
		}
	}
	return status
}

// ErrUnidentifiedExecution rejects a new execution that carries no principal on
// a host configured to isolate by principal.
//
// Without this rule an isolated agent escapes its own sandbox simply by asking
// the control plane for a second session: the sandboxed process cannot read the
// service user's actor token, so its call arrives unauthenticated, falls back to
// a legacy-host identity, and the resulting agent runs as the service account —
// a strictly wider identity than the one that asked for it. Refusing the launch
// costs an operator one `amux isolation enroll`; allowing it costs the whole
// isolation boundary.
var ErrUnidentifiedExecution = errors.New("this host isolates executions by principal; an unidentified execution cannot be created (run `amux isolation enroll`, or set AMUX_ACTOR_TOKEN)")

// RequireIdentifiedExecution gates the creation of a new execution. It
// deliberately keys off configuration rather than helper readiness: when the
// helper is down, identified executions degrade to passthrough and every live
// sandbox keeps running, so a readiness-gated rule would open the escape hatch
// exactly during an outage. Relaunching an execution created earlier is not
// affected — the identity it was created with is already frozen.
func (r *Runtime) RequireIdentifiedExecution(identity principal.ExecutionIdentity) error {
	if r == nil || identity.Identified() {
		return nil
	}
	if !r.cfg.Enabled || r.cfg.Backend == principal.BackendPassthrough {
		return nil
	}
	return ErrUnidentifiedExecution
}

func (r *Runtime) Select(identity principal.ExecutionIdentity) (LaunchBackend, string, string) {
	if r == nil {
		return NewPassthroughBackend(), principal.BackendPassthrough, "runtime not initialized"
	}
	reason := ""
	switch {
	case runtime.GOOS != "linux":
		reason = "non-Linux platform"
	case !r.cfg.Enabled:
		reason = "isolation disabled"
	case r.cfg.Backend == principal.BackendPassthrough:
		reason = "passthrough configured"
	case !identity.Identified():
		reason = "execution is not identified"
	case !r.refreshHelperReady(context.Background()):
		reason = r.degraded
	default:
		return &linuxDACBackend{runtime: r}, principal.BackendLinuxDAC, ""
	}
	if r.cfg.Enabled && identity.Identified() && reason != "passthrough configured" {
		r.warnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "runtime: linux-dac unavailable; falling back to passthrough (%s)\n", reason)
		})
	}
	return NewPassthroughBackend(), principal.BackendPassthrough, reason
}

func (r *Runtime) refreshHelperReady(parent context.Context) bool {
	if r == nil || r.client == nil || r.store == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()
	_, err := r.client.Handshake(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.ready = false
		r.degraded = "helper handshake unavailable"
		return false
	}
	r.ready = true
	r.degraded = ""
	return true
}

// BackendForContext obeys the backend frozen by the outer runtime. A selected
// linux-dac launch does not silently weaken if the helper disappears between
// preparation and actual exec; the start fails and the persisted backend stays
// truthful.
func (r *Runtime) BackendForContext(ctx LaunchContext) (LaunchBackend, error) {
	switch ctx.Backend {
	case "", principal.BackendPassthrough:
		return NewPassthroughBackend(), nil
	case principal.BackendLinuxDAC:
		if r == nil || !r.helperReady() || r.store == nil {
			return nil, fmt.Errorf("selected linux-dac helper is unavailable")
		}
		if ctx.Identity == nil || !ctx.Identity.Identified() {
			return nil, fmt.Errorf("selected linux-dac launch lacks an identified principal")
		}
		return &linuxDACBackend{runtime: r}, nil
	default:
		return nil, fmt.Errorf("unknown selected isolation backend %q", ctx.Backend)
	}
}

func (r *Runtime) helperReady() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ready
}

// PrepareExecution readies a principal's workspace and run directory.
// workspaceDir is the execution's existing workspace when it has one (relaunch,
// resume) and empty when it is being created; see ResolveExecutionWorkspaceID.
func (r *Runtime) PrepareExecution(ctx context.Context, identity principal.ExecutionIdentity, launchID, workspaceDir string) (PrincipalPaths, string, error) {
	backend := identity.IsolationBackend
	if backend == "" {
		_, backend, _ = r.Select(identity)
	}
	if backend != principal.BackendLinuxDAC {
		return PrincipalPaths{}, backend, nil
	}
	if !r.refreshHelperReady(ctx) || r.store == nil {
		return PrincipalPaths{}, "", fmt.Errorf("saved linux-dac backend is unavailable")
	}
	mapping, err := r.store.Ensure(identity.EffectivePrincipalID)
	if err != nil {
		return PrincipalPaths{}, "", err
	}
	workspaceID := ResolveExecutionWorkspaceID(workspaceDir, identity)
	paths, err := r.client.PrepareRun(ctx, mapping, workspaceID, launchID)
	if err != nil {
		return PrincipalPaths{}, "", err
	}
	return paths, backend, nil
}

func (r *Runtime) RemoveRun(ctx context.Context, identity principal.ExecutionIdentity, launchID string) error {
	if r == nil || identity.IsolationBackend != principal.BackendLinuxDAC || r.store == nil {
		return nil
	}
	mapping, err := r.store.Ensure(identity.EffectivePrincipalID)
	if err != nil {
		return err
	}
	return r.client.RemoveRun(ctx, mapping, launchID)
}

// RemoveWorkspace reclaims a finished execution's workspace. The tree is owned
// by the principal at 0700, so the service user cannot delete it itself and the
// directories would otherwise accumulate for the life of the account.
func (r *Runtime) RemoveWorkspace(ctx context.Context, identity principal.ExecutionIdentity, workspaceDir string) error {
	if r == nil || identity.IsolationBackend != principal.BackendLinuxDAC || r.store == nil {
		return nil
	}
	mapping, err := r.store.Ensure(identity.EffectivePrincipalID)
	if err != nil {
		return err
	}
	return r.client.RemoveWorkspace(ctx, mapping, ResolveExecutionWorkspaceID(workspaceDir, identity))
}

// ReclaimHome drops oversized provider caches from a principal HOME.
func (r *Runtime) ReclaimHome(ctx context.Context, identity principal.ExecutionIdentity, caches []HomeCacheSpec) error {
	if r == nil || identity.IsolationBackend != principal.BackendLinuxDAC || r.store == nil || len(caches) == 0 {
		return nil
	}
	mapping, err := r.store.Ensure(identity.EffectivePrincipalID)
	if err != nil {
		return err
	}
	return r.client.ReclaimHome(ctx, mapping, caches)
}

func (r *Runtime) MaterializeExecution(
	ctx context.Context,
	identity principal.ExecutionIdentity,
	launchID string,
	workspaceDir string,
	sources []BundleSource,
) error {
	if r == nil || identity.IsolationBackend != principal.BackendLinuxDAC || r.store == nil {
		return fmt.Errorf("linux-dac materialization runtime is unavailable")
	}
	mapping, err := r.store.Ensure(identity.EffectivePrincipalID)
	if err != nil {
		return err
	}
	return r.client.MaterializeRun(ctx, mapping, ResolveExecutionWorkspaceID(workspaceDir, identity), launchID, sources)
}

func (r *Runtime) KillPrincipal(ctx context.Context, principalID string) error {
	if r == nil || !r.helperReady() || r.store == nil || strings.TrimSpace(principalID) == "" {
		return nil
	}
	mapping, err := r.store.Ensure(principalID)
	if err != nil {
		return err
	}
	return r.client.KillPrincipal(ctx, mapping)
}

// ExecutionWorkspaceID is the workspace name for a NEW execution under a
// principal root. Callers outside this package need it to tell whether two
// sessions address the same durable workspace.
//
// The name carries the creating source ("feishu-exec_…", "cli-exec_…") because
// under isolation the directory is chosen by principal alone: one person's
// Feishu and CLI executions land side by side in the same account with nothing
// to tell them apart. The prefix makes that visible in `ls` and makes a
// per-source sweep a glob instead of a database join. It changes nothing about
// the security boundary — same principal still means same uid.
//
// Use ResolveExecutionWorkspaceID for an execution that already has a
// workspace; this function is only correct for one being created.
func ExecutionWorkspaceID(identity principal.ExecutionIdentity) string {
	return executionWorkspaceID(identity)
}

// ResolveExecutionWorkspaceID returns the workspace name an execution actually
// uses: the directory it is already sitting in when there is one, otherwise the
// derived name.
//
// Executions created before the source prefix existed live in bare
// `exec_<hash>` directories, and their identity would now derive a prefixed
// name. Recomputing would make relaunch fail its "workspace changed" assertion
// and make reclamation delete nothing, so the saved path wins wherever one
// exists. An implausible saved path is ignored rather than trusted: the result
// is handed to the privileged helper as a directory name.
func ResolveExecutionWorkspaceID(workspaceDir string, identity principal.ExecutionIdentity) string {
	if base := filepath.Base(filepath.Clean(strings.TrimSpace(workspaceDir))); executionWorkspacePattern.MatchString(base) {
		return base
	}
	return executionWorkspaceID(identity)
}

// executionWorkspacePattern matches exactly what this scheme produces, with or
// without a source prefix. Anything else in the saved path is not a workspace
// name we wrote, so it is not one we will hand the privileged helper.
var executionWorkspacePattern = regexp.MustCompile(`^(?:[a-z0-9][a-z0-9-]{0,31}-)?exec_[0-9a-f]{24}$`)

// workspaceSourcePattern keeps the prefix to a plain token. A source that does
// not match contributes no prefix at all: an unvetted path component is worse
// than an unlabeled directory.
var workspaceSourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func executionWorkspaceID(identity principal.ExecutionIdentity) string {
	source := strings.TrimSpace(identity.ExecutionStateRef)
	if source == "" {
		source = identity.EffectivePrincipalID + ":" + identity.ParentExecutionID
	}
	sum := sha256.Sum256([]byte(source))
	id := "exec_" + hex.EncodeToString(sum[:12])
	if origin := strings.TrimSpace(string(identity.Source)); workspaceSourcePattern.MatchString(origin) {
		id = origin + "-" + id
	}
	return id
}

type linuxDACBackend struct {
	runtime *Runtime
}

func (b *linuxDACBackend) Start(ctx context.Context, spec LaunchSpec) (ProcessHandle, error) {
	if b == nil || b.runtime == nil || b.runtime.store == nil || spec.Context.Identity == nil {
		return nil, fmt.Errorf("linux-dac backend is not initialized")
	}
	identity := *spec.Context.Identity
	if !identity.Identified() {
		return nil, fmt.Errorf("linux-dac requires an identified execution")
	}
	mapping, err := b.runtime.store.Ensure(identity.EffectivePrincipalID)
	if err != nil {
		return nil, err
	}
	paths, err := b.runtime.client.EnsurePrincipal(ctx, mapping)
	if err != nil {
		return nil, err
	}
	spec.Env = principalLaunchEnv(spec.Env, mapping.Account, paths, spec.Dir)
	return b.runtime.client.Start(ctx, mapping, spec)
}

// principalLaunchEnv rewrites the service user's environment into one that
// describes the principal actually running the process. The launch env is
// inherited from amuxlet, so without this the agent is told it is the service
// user, sitting in the service user's directory, with access to that user's
// ssh-agent and session bus — none of which is true once the process runs under
// a dedicated uid.
func principalLaunchEnv(env []string, account string, paths PrincipalPaths, dir string) []string {
	env = replaceLaunchEnv(env, "HOME", paths.Home)
	env = replaceLaunchEnv(env, "CODEX_HOME", paths.Home+"/.codex")
	// Empty preserves Claude's default layout (including HOME/.claude.json)
	// while masking a shared service-user override.
	env = replaceLaunchEnv(env, "CLAUDE_CONFIG_DIR", "")
	env = replaceLaunchEnv(env, "PI_CODING_AGENT_DIR", paths.Home+"/.pi/agent")
	env = replaceLaunchEnv(env, "XDG_CONFIG_HOME", paths.Home+"/.config")
	env = replaceLaunchEnv(env, "XDG_CACHE_HOME", paths.Home+"/.cache")
	env = replaceLaunchEnv(env, "AMUX_ISOLATION_BACKEND", principal.BackendLinuxDAC)
	// The helper validates a dedicated non-root host UID. With namespace
	// hardening the child sees UID 0, so advertise the actual sandbox context
	// to clients such as Claude without changing their selected permissions.
	env = replaceLaunchEnv(env, "IS_SANDBOX", "1")
	if account = strings.TrimSpace(account); account != "" {
		env = replaceLaunchEnv(env, "USER", account)
		env = replaceLaunchEnv(env, "LOGNAME", account)
	}
	if dir = strings.TrimSpace(dir); dir != "" {
		env = replaceLaunchEnv(env, "PWD", dir)
	} else {
		env = dropLaunchEnv(env, "PWD")
	}
	// These point at service-user resources guarded by that user's own uid: the
	// principal cannot open them, so they are dead references that only mislead.
	return dropLaunchEnv(env, "SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR")
}

func dropLaunchEnv(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		drop := false
		for _, key := range keys {
			if strings.HasPrefix(entry, key+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return out
}

func replaceLaunchEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if !replaced {
				out = append(out, prefix+value)
				replaced = true
			}
			continue
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}

func (r *Runtime) DailyCleanup(ctx context.Context, refs cleanup.References, dry bool) ([]cleanup.Step, error) {
	if r == nil || !r.cfg.Enabled || r.cfg.Backend == principal.BackendPassthrough {
		return nil, nil
	}
	accounts, err := r.client.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var steps []cleanup.Step
	for _, a := range accounts {
		result, err := r.client.DailyCleanup(ctx, a, DailyCleanupRequest{References: &refs, DryRun: dry})
		if err != nil {
			steps = append(steps, cleanup.Step{Name: "isolation", Errors: []string{err.Error()}})
			continue
		}
		steps = append(steps, result...)
	}
	return steps, nil
}
