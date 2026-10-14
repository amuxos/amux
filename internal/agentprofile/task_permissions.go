package agentprofile

import (
	"strings"

	"github.com/amuxos/amux/internal/task"
)

// TaskPermissionArgs makes permission_policy authoritative for Task children.
// Codex-compatible allow and escalation policies disable the native sandbox;
// the execution backend supplies any outer isolation.
// Direct Session launches do not call this projector.
func TaskPermissionArgs(agentType string, policy task.PermissionPolicy, args []string, outerConfined bool) ([]string, error) {
	args, err := taskPermissionArgs(agentType, policy, args, outerConfined)
	if err != nil {
		return nil, err
	}
	return launchProcessArgs(agentType, args), nil
}

func taskPermissionArgs(agentType string, policy task.PermissionPolicy, args []string, outerConfined bool) ([]string, error) {
	if policy == "" {
		policy = task.PolicyAllow
	}
	var native []string
	switch policy {
	case task.PolicyAllow, task.PolicyDenyAll, task.PolicyEscalateToMain:
	default:
		return nil, profileError("task_permission_invalid", policy)
	}
	protocol := profileConfigProtocol(agentType)
	switch protocol {
	case "codex":
		approval, sandbox := "never", "danger-full-access"
		if policy == task.PolicyDenyAll {
			sandbox = "read-only"
		} else if policy == task.PolicyEscalateToMain {
			approval = "on-request"
		}
		native = []string{"-c", `approval_policy="` + approval + `"`, "-c", `sandbox_mode="` + sandbox + `"`}
	case "pi":
		if policy != task.PolicyAllow {
			return nil, profileError("task_permission_unsupported", agentType, policy)
		}
	default:
		mode := "default"
		if policy == task.PolicyDenyAll {
			mode = "plan"
		} else if policy == task.PolicyAllow && outerConfined && protocol == "claude" {
			mode = "bypass"
		}
		var err error
		native, err = permissionModeArgs(agentType, mode)
		if err != nil {
			return nil, err
		}
		if len(native) == 0 && policy != task.PolicyAllow {
			return nil, profileError("task_permission_unsupported", agentType, policy)
		}
	}
	args = stripTaskPermissionArgs(args, protocol)
	if customization.StripTaskPermissionArgs != nil {
		args = customization.StripTaskPermissionArgs(agentType, args)
	}
	for i, arg := range args {
		if arg == "--" {
			return append(append(args[:i:i], native...), args[i:]...), nil
		}
	}
	return append(args, native...), nil
}

// Legacy native permission flags remain readable in saved definitions, but
// cannot override permission_policy. Non-permission arguments keep their order.
func stripTaskPermissionArgs(args []string, protocol string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag, inline, hasInline := strings.Cut(arg, "=")
		switch flag {
		case "-c", "--config":
			if protocol != "codex" {
				break
			}
			value := inline
			if !hasInline && i+1 < len(args) {
				value = args[i+1]
			}
			key, _, _ := strings.Cut(value, "=")
			key = strings.TrimSpace(key)
			if key == "approval_policy" || key == "sandbox_mode" || key == "sandbox_workspace_write" || strings.HasPrefix(key, "sandbox_workspace_write.") {
				if !hasInline && i+1 < len(args) {
					i++
				}
				continue
			}
		case "-a", "-s":
			if protocol != "codex" {
				break
			}
			if !hasInline && i+1 < len(args) {
				i++
			}
			continue
		case "--ask-for-approval", "--sandbox", "--permission-mode", "--add-dir":
			if !hasInline && i+1 < len(args) {
				i++
			}
			continue
		case "--full-auto", "--dangerously-bypass-approvals-and-sandbox", "--yolo", "--dangerously-skip-permissions", "--bypass-permissions", "--allow-dangerously-skip-permissions":
			continue
		}
		if protocol == "codex" && len(arg) > 2 && (strings.HasPrefix(arg, "-a") || strings.HasPrefix(arg, "-s")) && !strings.HasPrefix(arg, "--") {
			continue
		}
		// Common compact short-option forms such as -capproval_policy="never".
		if protocol == "codex" && strings.HasPrefix(arg, "-c") && len(arg) > 2 {
			key, _, _ := strings.Cut(arg[2:], "=")
			if key == "approval_policy" || key == "sandbox_mode" || key == "sandbox_workspace_write" || strings.HasPrefix(key, "sandbox_workspace_write.") {
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

func ApplyTaskPermissionPolicy(spec *task.Spec, outerConfined bool) error {
	if spec == nil {
		return nil
	}
	policy := spec.PermissionPolicy
	if policy == "" && spec.ResolvedProfile != nil {
		policy = task.PermissionPolicy(spec.ResolvedProfile.PermissionPolicy)
		spec.PermissionPolicy = policy
	}
	// Some distribution engines project permissions into Profile config files
	// rather than process flags. A raw launch cannot enforce that restriction.
	if spec.ResolvedProfile == nil && policy != "" && policy != task.PolicyAllow {
		native, err := taskPermissionArgs(spec.Agent, policy, nil, outerConfined)
		if err != nil {
			return err
		}
		if len(native) > 0 && len(launchProcessArgs(spec.Agent, native)) == 0 {
			return profileError("task_permission_unsupported", spec.Agent, policy)
		}
	}
	args, err := TaskPermissionArgs(spec.Agent, policy, spec.ExtraArgs, outerConfined)
	if err != nil {
		return err
	}
	spec.ExtraArgs = args
	if spec.ResolvedProfile != nil {
		if policy == "" {
			policy = task.PolicyAllow
		}
		spec.ResolvedProfile.PermissionPolicy = string(policy)
		spec.ResolvedProfile.DefaultArgs, err = taskPermissionArgs(spec.Agent, policy, spec.ResolvedProfile.DefaultArgs, outerConfined)
	}
	return err
}

// TaskPermissionResumeError explains a frozen persistent execution boundary.
func TaskPermissionResumeError(saved, requested string) error {
	return profileError("task_permission_resume", saved, requested)
}

func TaskPermissionLegacyResumeError() error {
	return profileError("task_permission_resume_unknown")
}
