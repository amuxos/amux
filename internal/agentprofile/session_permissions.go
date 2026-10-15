package agentprofile

import (
	"strings"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// SessionPermissionDefaults applies caller-owned bypass defaults to a new
// Session. Explicit native arguments and Task permission policies take priority.
// The Profile snapshot also receives the defaults for config-file projections
// and subsequent model switches; source Pack content is never changed.
func SessionPermissionDefaults(agentType string, args []string, spec *pb.AgentProfileSpec) ([]string, error) {
	all := cloneStrings(args)
	if spec != nil {
		if strings.TrimSpace(spec.PermissionPolicy) != "" {
			return args, nil
		}
		all = append(all, spec.DefaultArgs...)
	}
	if hasSessionPermissionArgs(agentType, all) {
		return args, nil
	}
	defaults, err := permissionModeArgs(agentType, "bypass")
	if err != nil {
		return nil, err
	}
	if spec != nil {
		spec.DefaultArgs = append(cloneStrings(defaults), spec.DefaultArgs...)
	}
	return append(launchProcessArgs(agentType, defaults), args...), nil
}

func hasSessionPermissionArgs(agentType string, args []string) bool {
	stripped := stripNativePermissionArgs(args, profileConfigProtocol(agentType), false)
	if customization.StripTaskPermissionArgs != nil {
		stripped = customization.StripTaskPermissionArgs(agentType, stripped)
	}
	return len(stripped) != len(args)
}
