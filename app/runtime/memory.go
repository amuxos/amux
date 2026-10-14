package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

func supportsMemory(agent string) bool {
	return agent == "codex" || agent == "claude" || agent == "pi"
}

// Only fresh execution creation resolves a binding. Resumes replay the stored
// marker, even if the current Hub policy changes; tools then fail closed.
func (s *server) resolveMemoryBinding(ctx context.Context, actor string, identity principal.ExecutionIdentity, agent string) (string, error) {
	if s.memoryBindingResolver == nil {
		return "", nil
	}
	binding, err := s.memoryBindingResolver(ctx, actor)
	if err != nil {
		return "", &memoryUnavailableError{err}
	}
	if err := validateMemoryBinding(binding, identity); err != nil {
		return "", err
	}
	if binding != "" && !supportsMemory(agent) {
		return "", &memoryUnavailableError{fmt.Errorf("this agent provider has no verified native-memory contract")}
	}
	return binding, nil
}

func validateMemoryBinding(binding string, identity principal.ExecutionIdentity) error {
	if binding == "" || binding == "global" {
		return nil
	}
	if identity.EffectivePrincipalID == "" || binding != "principal:"+identity.EffectivePrincipalID {
		return fmt.Errorf("memory binding does not match the effective execution principal")
	}
	return nil
}

func (s *server) configureSessionMemory(ctx context.Context, sess *session.Session, args, env []string) ([]string, []string, error) {
	snap := sess.Snapshot()
	binding := memoryprotocol.Binding(snap.Env)
	if binding != "" && !supportsMemory(snap.AgentType) {
		s.reportLaunchWarning(sess)("amux memory is unavailable for this agent provider; its original binding remains frozen.")
		env = memoryprotocol.SetEnv(env, memoryprotocol.BindingEnv, "")
		if adapter.IsCodexCompatible(snap.AgentType) && runArgsContainAmuxMCP(snap.AgentType, args) {
			args = append(append([]string(nil), args...), "-c", "mcp_servers.amux.env.AMUX_MEMORY_BINDING=\"\"")
		}
		return args, env, nil
	}
	env = memoryprotocol.SetEnv(env, memoryprotocol.BindingEnv, binding)
	if binding == "" {
		// Fresh discovery reports unavailability once. An unmanaged resume
		// does not query current policy or advertise an experiment that may
		// now be disabled; a new native conversation is required to opt in.
		return args, env, nil
	}
	cli := !memoryMCPConfigured(snap.AgentType, args)
	var err error
	args, env, err = s.prepareAmuxTools(ctx, snap, args, env, cli)
	if err != nil {
		return nil, nil, err
	}
	if cli {
		env = memoryprotocol.SetEnv(env, "AMUX_MEMORY_CLI", "1")
	} else {
		env = memoryprotocol.SetEnv(env, "AMUX_MEMORY_CLI", "")
	}
	switch snap.AgentType {
	case "claude":
		args = memoryprotocol.AppendPrompt(args, memoryprotocol.Instruction(cli))
		env = memoryprotocol.SetEnv(env, "CLAUDE_CODE_DISABLE_AUTO_MEMORY", "1")
	case "pi":
		args = memoryprotocol.AppendPrompt(args, memoryprotocol.Instruction(true))

	}
	return args, env, nil
}

func sameMemoryPrincipal(a, b principal.ExecutionIdentity) bool {
	return strings.TrimSpace(a.EffectivePrincipalID) == strings.TrimSpace(b.EffectivePrincipalID)
}

// Discovery failures disable memory for this new native conversation. They do
// not reinterpret policy as global and do not block ordinary agent work.
type memoryUnavailableError struct{ err error }

func (e *memoryUnavailableError) Error() string {
	return "amux memory unavailable for this session: " + e.err.Error()
}

func launchEnvValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], key+"=") {
			return strings.TrimPrefix(env[i], key+"=")
		}
	}
	return ""
}

// A user-supplied Claude MCP config does not necessarily contain amux. Use
// the CLI protocol unless this is the Runtime's generated built-in config.
func memoryMCPConfigured(agent string, args []string) bool {
	if agent != "claude" {
		return runArgsContainAmuxMCP(agent, args)
	}
	for i, arg := range args {
		path := ""
		if arg == "--mcp-config" && i+1 < len(args) {
			path = args[i+1]
		}
		if strings.HasPrefix(arg, "--mcp-config=") {
			path = strings.TrimPrefix(arg, "--mcp-config=")
		}
		if filepath.Base(path) == "claude-mcp.json" && strings.Contains(filepath.ToSlash(path), "/agent-config/") {
			return true
		}
	}
	return false
}
