package adapter

import (
	"path/filepath"
	"strings"
)

// IsolationEnvBinding declares one environment variable pinned when an engine
// runs inside an isolated HOME (both task private-home sandboxes and Linux DAC
// principal homes). When HomeRel is non-empty, the value is joined onto the
// isolated HOME; when HomeRel is empty, the variable is set to the empty string
// so any shared service-user override is masked and the engine falls back to
// HOME. EnsureDir requests creating the HOME-relative directory during isolated
// home materialization even when no credentials are staged.
type IsolationEnvBinding struct {
	Key       string
	HomeRel   string
	EnsureDir bool
}

// ValueForHome resolves the binding's value against an isolated HOME root.
func (b IsolationEnvBinding) ValueForHome(home string) string {
	if strings.TrimSpace(b.HomeRel) == "" {
		return ""
	}
	return filepath.Join(home, filepath.FromSlash(b.HomeRel))
}

// PermissionSurface declares how an engine encodes permission modes and
// task permission_policy controls into launch arguments.
type PermissionSurface string

const (
	// PermissionSurfaceUnspecified defers to distribution profile hooks (if
	// registered) or treats the engine as having no native permission flags.
	PermissionSurfaceUnspecified PermissionSurface = ""
	// PermissionSurfaceNone declares that the engine has no native permission
	// flags (only policy-allow is supported for tasks; no bypass defaults apply).
	PermissionSurfaceNone PermissionSurface = "none"
	// PermissionSurfaceCodexConfig uses Codex-style `-c approval_policy=...`
	// and `-c sandbox_mode=...` flags.
	PermissionSurfaceCodexConfig PermissionSurface = "codex_config"
	// PermissionSurfaceClaudeMode uses `--permission-mode <mode>`.
	PermissionSurfaceClaudeMode PermissionSurface = "claude_mode"
	// PermissionSurfaceCustom delegates native permission mapping and stripping
	// to distribution-registered profile hooks (e.g. config-file projected engines).
	PermissionSurfaceCustom PermissionSurface = "custom"
)

// MCPArgSurface declares how amux injects, detects, and strips its built-in
// MCP server on an engine's launch arguments.
type MCPArgSurface string

const (
	// MCPArgSurfaceNone means the engine either does not support MCP or projects
	// MCP into workspace/home config files rather than launch arguments.
	MCPArgSurfaceNone MCPArgSurface = ""
	// MCPArgSurfaceClaudeConfig uses `--mcp-config <path>`.
	MCPArgSurfaceClaudeConfig MCPArgSurface = "claude_config"
	// MCPArgSurfaceCodexConfig uses `-c mcp_servers.amux.*` config overrides.
	MCPArgSurfaceCodexConfig MCPArgSurface = "codex_config"
)

// MemoryContract declares an engine's verified amux memory integration contract.
// Adapters must opt in explicitly; Codex protocol compatibility alone does not
// imply a verified native-memory contract.
type MemoryContract struct {
	// Supported reports whether the engine has a verified native-memory contract.
	Supported bool
	// PromptInjection injects the memory protocol instruction via
	// --append-system-prompt when amux memory is active.
	PromptInjection bool
	// AlwaysUseCLIInstruction forces the CLI instruction variant even when
	// PromptInjection is used (for engines without MCP support).
	AlwaysUseCLIInstruction bool
	// DisableNativeAutoMemoryEnv names an env variable set to "1" while amux
	// memory is active to suppress the engine's own auto-memory.
	DisableNativeAutoMemoryEnv string
}

// SessionInstructionsMode declares how out-of-band session instructions reach
// an engine process at launch.
type SessionInstructionsMode string

const (
	SessionInstructionsNone         SessionInstructionsMode = ""
	SessionInstructionsSystemPrompt SessionInstructionsMode = "system_prompt"
	SessionInstructionsEnv          SessionInstructionsMode = "env"
)

// IsolatedTrustPolicy declares how workspace trust behaves under an isolated
// OS principal (Linux DAC), where the Runtime service user must not mutate the
// principal's native HOME directly.
type IsolatedTrustPolicy struct {
	// SkipHostTrustUnderDAC disables host-side WorkspaceTruster calls when
	// running under Linux DAC.
	SkipHostTrustUnderDAC bool
	// LaunchApproveArg, when non-empty, is appended under Linux DAC unless one
	// of ExplicitApproveFlags is already present in the launch arguments.
	LaunchApproveArg     string
	ExplicitApproveFlags []string
}

// ExecutionCapabilities is the declarative Engine × Permission × Isolation
// capability vector for an adapter. Subsystems (isolation, task sandboxes,
// permission projection, MCP wiring, memory, session capability/instructions)
// query this matrix instead of branching on engine names.
type ExecutionCapabilities struct {
	IsolatedHomeEnv             []IsolationEnvBinding
	PermissionSurface           PermissionSurface
	MCPArgSurface               MCPArgSurface
	AlwaysUseCLIForCapabilities bool
	Memory                      MemoryContract
	SessionInstructions         SessionInstructionsMode
	IsolatedTrust               IsolatedTrustPolicy
}

// SupportsTaskPermissionPolicy reports whether the declared permission surface
// can enforce a Task permission_policy value ("", "policy-allow", "deny-all",
// "escalate-to-main").
func (e ExecutionCapabilities) SupportsTaskPermissionPolicy(policy string) bool {
	switch strings.TrimSpace(policy) {
	case "", "policy-allow":
		return true
	case "deny-all", "escalate-to-main":
		return e.PermissionSurface != PermissionSurfaceNone && e.PermissionSurface != PermissionSurfaceUnspecified
	default:
		return false
	}
}

// ExecutionMatrixEntry is a read-only snapshot of one registered adapter's
// declarative execution capabilities.
type ExecutionMatrixEntry struct {
	AgentType    string
	Protocol     Protocol
	Execution    ExecutionCapabilities
	ConfigLayout map[Surface]Placement
}

func isCodexProtocol(p Protocol) bool {
	return p == ProtocolCodexAppServer || distributionCodexCompatible(p)
}

func resolveExecutionCapabilities(caps Capabilities) ExecutionCapabilities {
	exec := caps.Execution
	if len(exec.IsolatedHomeEnv) > 0 {
		exec.IsolatedHomeEnv = append([]IsolationEnvBinding(nil), exec.IsolatedHomeEnv...)
	}
	if len(exec.IsolatedTrust.ExplicitApproveFlags) > 0 {
		exec.IsolatedTrust.ExplicitApproveFlags = append([]string(nil), exec.IsolatedTrust.ExplicitApproveFlags...)
	}
	if isCodexProtocol(caps.Protocol) {
		if exec.PermissionSurface == PermissionSurfaceUnspecified {
			exec.PermissionSurface = PermissionSurfaceCodexConfig
		}
		if exec.MCPArgSurface == MCPArgSurfaceNone {
			exec.MCPArgSurface = MCPArgSurfaceCodexConfig
		}
		if exec.SessionInstructions == SessionInstructionsNone {
			exec.SessionInstructions = SessionInstructionsEnv
		}
	}
	return exec
}

// ExecutionCapabilitiesFor returns the resolved declarative execution
// capabilities for agentType. Unregistered agent types yield the zero value.
func ExecutionCapabilitiesFor(agentType string) ExecutionCapabilities {
	a, err := Get(strings.TrimSpace(agentType))
	if err != nil {
		return ExecutionCapabilities{}
	}
	return resolveExecutionCapabilities(a.Capabilities())
}

// ExecutionMatrix returns the full Engine × Permission × Isolation capability
// matrix for all currently registered adapters in registration order.
func ExecutionMatrix() []ExecutionMatrixEntry {
	adapterMu.RLock()
	entries := append([]adapterRegistration(nil), registeredAdapters...)
	adapterMu.RUnlock()

	matrix := make([]ExecutionMatrixEntry, 0, len(entries))
	for _, entry := range entries {
		caps := entry.create().Capabilities()
		layout := make(map[Surface]Placement, len(caps.ConfigLayout))
		for k, v := range caps.ConfigLayout {
			layout[k] = v
		}
		matrix = append(matrix, ExecutionMatrixEntry{
			AgentType:    entry.name,
			Protocol:     caps.Protocol,
			Execution:    resolveExecutionCapabilities(caps),
			ConfigLayout: layout,
		})
	}
	return matrix
}

// AllIsolatedHomeEnvBindings returns the deduplicated set of isolated HOME env
// bindings declared across all registered adapters in deterministic order.
func AllIsolatedHomeEnvBindings() []IsolationEnvBinding {
	adapterMu.RLock()
	entries := append([]adapterRegistration(nil), registeredAdapters...)
	adapterMu.RUnlock()

	seen := make(map[string]struct{})
	var bindings []IsolationEnvBinding
	for _, entry := range entries {
		exec := resolveExecutionCapabilities(entry.create().Capabilities())
		for _, b := range exec.IsolatedHomeEnv {
			key := strings.TrimSpace(b.Key)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			bindings = append(bindings, IsolationEnvBinding{
				Key:       key,
				HomeRel:   strings.TrimSpace(b.HomeRel),
				EnsureDir: b.EnsureDir,
			})
		}
	}
	return bindings
}

// IsolatedHomeEnsureDirs returns the deduplicated HOME-relative directories
// that must exist inside an isolated principal HOME before agent launch.
func IsolatedHomeEnsureDirs() []string {
	var dirs []string
	seen := make(map[string]struct{})
	for _, b := range AllIsolatedHomeEnvBindings() {
		if !b.EnsureDir || b.HomeRel == "" {
			continue
		}
		if _, ok := seen[b.HomeRel]; ok {
			continue
		}
		seen[b.HomeRel] = struct{}{}
		dirs = append(dirs, b.HomeRel)
	}
	return dirs
}

// ArgsContainAmuxMCP reports whether launch args already configure the built-in
// amux MCP server according to agentType's declared MCPArgSurface.
func ArgsContainAmuxMCP(agentType string, args []string) bool {
	switch ExecutionCapabilitiesFor(agentType).MCPArgSurface {
	case MCPArgSurfaceClaudeConfig:
		for _, arg := range args {
			arg = strings.TrimSpace(arg)
			if arg == "--mcp-config" || strings.HasPrefix(arg, "--mcp-config=") {
				return true
			}
		}
	case MCPArgSurfaceCodexConfig:
		for i, arg := range args {
			arg = strings.TrimSpace(arg)
			if strings.Contains(arg, "mcp_servers.amux.") {
				return true
			}
			if (arg == "-c" || arg == "--config") && i+1 < len(args) && strings.Contains(args[i+1], "mcp_servers.amux.") {
				return true
			}
		}
	}
	return false
}

// StripAmuxMCPArgs removes previously injected amux MCP server entries from
// launch args according to agentType's declared MCPArgSurface, preserving all
// other arguments verbatim.
func StripAmuxMCPArgs(agentType string, args []string) []string {
	surface := ExecutionCapabilitiesFor(agentType).MCPArgSurface
	if surface == MCPArgSurfaceNone {
		return args
	}
	out := make([]string, 0, len(args))
	switch surface {
	case MCPArgSurfaceClaudeConfig:
		for i := 0; i < len(args); i++ {
			arg := strings.TrimSpace(args[i])
			if arg == "--mcp-config" {
				i++
				continue
			}
			if strings.HasPrefix(arg, "--mcp-config=") {
				continue
			}
			out = append(out, args[i])
		}
	case MCPArgSurfaceCodexConfig:
		for i := 0; i < len(args); i++ {
			arg := strings.TrimSpace(args[i])
			if (arg == "-c" || arg == "--config") && i+1 < len(args) && strings.Contains(args[i+1], "mcp_servers.amux.") {
				i++
				continue
			}
			if strings.HasPrefix(arg, "mcp_servers.amux.") {
				continue
			}
			out = append(out, args[i])
		}
	default:
		return args
	}
	return out
}
