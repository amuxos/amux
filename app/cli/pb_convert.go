package cli

import (
	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func targetCapabilitiesToRegistry(caps *pb.TargetCapabilities) *registry.Capabilities {
	if caps == nil {
		return nil
	}
	out := &registry.Capabilities{
		Labels:                  cloneStringMap(caps.Labels),
		IsolationBackends:       append([]string(nil), caps.Isolation...),
		WorkspacePolicies:       append([]string(nil), caps.WorkspacePolicies...),
		CredentialProfiles:      make(map[string][]string, len(caps.CredentialProfiles)),
		MaxSessions:             int(caps.MaxSessions),
		AgentTypes:              append([]string(nil), caps.AgentTypes...),
		Features:                append([]string(nil), caps.Features...),
		RuntimeTools:            append([]string(nil), caps.RuntimeTools...),
		WorkspaceRoots:          append([]string(nil), caps.WorkspaceRoots...),
		Shells:                  append([]string(nil), caps.Shells...),
		PathStyle:               caps.PathStyle,
		NetworkProfile:          caps.NetworkProfile,
		CredentialRefs:          append([]string(nil), caps.CredentialRefs...),
		IsolationHelperProtocol: caps.IsolationHelperProtocol,
		IsolationDegradedReason: caps.IsolationDegradedReason,
	}
	for _, agent := range caps.Agents {
		out.Agents = append(out.Agents, registry.AgentCapacity{
			Type:           agent.Type,
			Protocol:       agent.Protocol,
			MaxConcurrency: int(agent.MaxConcurrency),
		})
	}
	for agentType, names := range caps.CredentialProfiles {
		if names != nil {
			out.CredentialProfiles[agentType] = append([]string(nil), names.Names...)
		}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
