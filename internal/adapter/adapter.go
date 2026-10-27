package adapter

import (
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type InterruptStrategy string

const (
	InterruptCtrlC InterruptStrategy = "ctrl_c"
	InterruptACP   InterruptStrategy = "acp_cancel"
)

type Protocol string

const (
	ProtocolACP              Protocol = "acp"
	ProtocolClaudeSDKSidecar Protocol = "claude_sdk_sidecar"
	ProtocolCodexAppServer   Protocol = "codex_app_server"
	// ProtocolPiRPC drives the `pi --mode rpc` JSONL protocol over stdio. pi
	// has no ACP mode; its RPC protocol carries prompt/abort commands and
	// streamed agent events (message_update, tool_execution_*, agent_settled).
	ProtocolPiRPC Protocol = "pi_rpc"
)

type CwdSwitchStrategy string

const (
	CwdSwitchUnsupported CwdSwitchStrategy = ""
	// CwdSwitchResume relaunches the provider in the new cwd and resumes the
	// existing native conversation id.
	CwdSwitchResume CwdSwitchStrategy = "resume"
)

// Surface is an Agent Profile injection surface (a kind of capability/behavior
// configuration a definition can carry into the child).
type Surface int

const (
	SurfaceSkills Surface = iota
	SurfaceRules
	SurfaceSystemPrompt
	SurfaceMCP
)

// Placement declares whether an agent supports a surface and where the surface
// materializes. Kind is one of "home-rel" (relative to the child HOME),
// "cwd-rel" (relative to the child cwd), or "extra-arg" (a CLI flag); Path is the
// relative destination, or the flag name when Kind == "extra-arg".
type Placement struct {
	Supported bool
	Kind      string
	Path      string
}

type Capabilities struct {
	Protocol          Protocol
	Acp               bool
	Interrupt         bool
	InterruptStrategy InterruptStrategy
	CwdSwitch         CwdSwitchStrategy
	// NativeResult reports whether the agent's provider emits canonical
	// ResultObserved. When true the task runner records the
	// summary from that event and skips injecting the fallback result envelope
	// into the child prompt; when false it falls back to the envelope contract.
	NativeResult bool
	// ACPPromptTailDrain is an internal adapter compatibility window used when
	// an ACP agent may emit final assistant chunks after session/prompt returns.
	// Zero preserves the ACP v1 response-as-terminal behavior.
	ACPPromptTailDrain time.Duration
	// ConfigLayout declares which Agent Profile surfaces this agent supports
	// and where each materializes. nil means "no surfaces supported" (a nil-map
	// read yields the zero Placement, i.e. Supported=false). See internal/agentprofile.
	ConfigLayout map[Surface]Placement
}

// Adapter builds the exec.Cmd for a specific agent type.
type Adapter interface {
	Cmd(extraArgs []string) *exec.Cmd
	Capabilities() Capabilities
}

// ACPExecutionConfigurator maps caller-owned execution settings to native ACP
// select configuration values. The ACP driver validates every ID/value against
// the agent's advertised configOptions and verifies each write by readback.
// An empty map leaves native defaults untouched. Implementations must not read
// credentials or mutate process-global configuration.
type ACPExecutionConfigurator interface {
	ACPExecutionConfig(model, reasoningEffort string) (map[string]string, error)
}

// WorkspaceTruster is implemented by adapters whose agent gates on a
// per-directory trust prompt before doing any real work. amux runs agents
// non-interactively (CLI/Lark bridge inject input), so such a prompt would
// block forever - the agent echoes input back and never reaches the model.
// Runtime calls EnsureWorkspaceTrusted before launch so the session cwd is
// pre-accepted. Implementations must be best-effort and non-fatal.
//
// CleanupWorkspaceTrust is called when a session is archived, removing the
// trust entry so the global config does not grow unbounded. Best-effort.
type WorkspaceTruster interface {
	EnsureWorkspaceTrusted(cwd string, env []string) error
	CleanupWorkspaceTrust(cwd string, env []string) error
}

// IsolatedHomeProvisioner is implemented by adapters whose agent keeps its
// account credential in the user's HOME rather than in an env variable amux can
// inject. Under UID/DAC isolation the agent runs with a dedicated, empty
// principal HOME, so without projection it starts unauthenticated and every
// turn ends with no answer at all. Paths are HOME-relative; missing ones are
// skipped.
type IsolatedHomeProvisioner interface {
	IsolatedHomeFiles() []string
}

// IsolatedHomeCache describes a provider cache inside an isolated principal
// HOME that amux may reclaim. Path gates the decision by size; when it exceeds
// MaxBytes (or MaxBytes is 0) every entry in Remove is deleted. All paths are
// HOME-relative.
type IsolatedHomeCache struct {
	Path     string
	MaxBytes int64
	Remove   []string
}

// IsolatedHomeReclaimer is implemented by adapters whose agent grows unbounded
// state in HOME. Under UID/DAC isolation that state lives in the principal's
// own HOME, which the service user cannot reach, so the reclamation has to be
// described rather than performed here.
type IsolatedHomeReclaimer interface {
	IsolatedHomeCaches() []IsolatedHomeCache
}

// IsolatedHomeCredentialInspector is implemented by adapters whose projected
// HOME credential carries an expiry. amux stays out of the credential format:
// the adapter parses its own file and reports only the instant it lapses.
// ok=false means "no expiry in this format, or unreadable" and must never be
// treated as expired — an unparseable credential still launches.
type IsolatedHomeCredentialInspector interface {
	CredentialExpiry(relPath string, data []byte) (time.Time, bool)
}

// IsolatedHomePrincipalCredentials is implemented by adapters whose isolated
// HOME credential can come from a login the principal itself performed in its
// own HOME (for example a per-user CLI sign-in), so the agent calls its models
// under the user's own identity instead of the service account's. The runtime
// reads PrincipalHomeSources from the principal HOME as the principal and asks
// DerivePrincipalHomeFile to synthesize the HOME file; every missing or
// unusable case keeps the service projection unchanged.
type IsolatedHomePrincipalCredentials interface {
	// PrincipalHomeSources returns HOME-relative files read from the
	// principal's own HOME. Missing files are simply absent from the map.
	PrincipalHomeSources() []string
	// DerivePrincipalHomeFile converts source contents into the HOME-relative
	// file to stage for this principal. ok=false keeps the service projection.
	DerivePrincipalHomeFile(sources map[string][]byte, now time.Time) (relPath string, data []byte, ok bool)
}

// AutoModelSelectionConfig is the runtime-owned launch policy supplied to an
// AutoModelSelector. Adapter-specific config parsing stays in cmd wiring.
type AutoModelSelectionConfig struct {
	Priority             []string
	LoadThresholdPercent int
}

// AutoModelSelector is optionally implemented by adapters that can select an
// optimal model at launch time based on runtime conditions (e.g. provider-side
// model load/queue status). The runtime calls SelectAutoModel before launching
// the agent process when no explicit --model is present in extraArgs, and
// injects the returned model ref as --model <ref>. A return of ("", nil) means
// "no auto-selection; use the agent default". Implementations must be
// best-effort: a non-nil error is logged but does not block launch (the session
// proceeds with whatever model it would have used otherwise).
//
// cwd and env are the session launch environment. Config contains only the
// resolved generic selection policy; implementations do not read global
// application config directly.
type AutoModelSelector interface {
	SelectAutoModel(cwd string, env []string, cfg AutoModelSelectionConfig) (string, error)
}

// Get returns the adapter for the given agent type name.
func Get(agentType string) (Adapter, error) {
	adapterMu.RLock()
	for _, entry := range registeredAdapters {
		if entry.name == agentType {
			adapterMu.RUnlock()
			return entry.create(), nil
		}
	}
	adapterMu.RUnlock()
	return nil, fmt.Errorf("unknown agent type %q", agentType)
}

type adapterRegistration struct {
	name   string
	create func() Adapter
}

var adapterMu sync.RWMutex

// Names returns the registered agent type names.
func Names() []string {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	names := make([]string, 0, len(registeredAdapters))
	for _, entry := range registeredAdapters {
		names = append(names, entry.name)
	}
	return names
}

// Layout returns the agent's Agent Profile placement map. It is the default
// LayoutFunc consumed by internal/agentprofile.
func Layout(agentType string) (map[Surface]Placement, error) {
	a, err := Get(agentType)
	if err != nil {
		return nil, err
	}
	return a.Capabilities().ConfigLayout, nil
}

func normalizeCapabilities(c Capabilities) Capabilities {
	c.Acp = c.Protocol == ProtocolACP
	return c
}
