package tunnel

import (
	"github.com/amuxos/amux/internal/registry"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func registryCapabilitiesToPB(caps *registry.Capabilities) *pb.TargetCapabilities {
	if caps == nil {
		return nil
	}
	out := &pb.TargetCapabilities{
		AgentTypes:              cloneStringSlice(caps.AgentTypes),
		Features:                cloneStringSlice(caps.Features),
		RuntimeTools:            cloneStringSlice(caps.RuntimeTools),
		WorkspaceRoots:          cloneStringSlice(caps.WorkspaceRoots),
		Shells:                  cloneStringSlice(caps.Shells),
		PathStyle:               caps.PathStyle,
		NetworkProfile:          caps.NetworkProfile,
		CredentialRefs:          cloneStringSlice(caps.CredentialRefs),
		Labels:                  cloneStringMap(caps.Labels),
		Isolation:               cloneStringSlice(caps.IsolationBackends),
		WorkspacePolicies:       cloneStringSlice(caps.WorkspacePolicies),
		CredentialProfiles:      make(map[string]*pb.CredentialProfileList, len(caps.CredentialProfiles)),
		MaxSessions:             int32(caps.MaxSessions),
		IsolationHelperProtocol: caps.IsolationHelperProtocol,
		IsolationDegradedReason: caps.IsolationDegradedReason,
	}
	for _, agent := range caps.Agents {
		out.Agents = append(out.Agents, &pb.AgentCapacity{
			Type:           agent.Type,
			Protocol:       agent.Protocol,
			MaxConcurrency: int32(agent.MaxConcurrency),
		})
	}
	for agentType, names := range caps.CredentialProfiles {
		out.CredentialProfiles[agentType] = &pb.CredentialProfileList{Names: cloneStringSlice(names)}
	}
	return out
}

func pbCapabilitiesToRegistry(caps *pb.TargetCapabilities) registry.Capabilities {
	if caps == nil {
		return registry.Capabilities{}
	}
	out := registry.Capabilities{
		Labels:                  cloneStringMap(caps.Labels),
		IsolationBackends:       cloneStringSlice(caps.Isolation),
		WorkspacePolicies:       cloneStringSlice(caps.WorkspacePolicies),
		CredentialProfiles:      make(map[string][]string, len(caps.CredentialProfiles)),
		MaxSessions:             int(caps.MaxSessions),
		AgentTypes:              cloneStringSlice(caps.AgentTypes),
		Features:                cloneStringSlice(caps.Features),
		RuntimeTools:            cloneStringSlice(caps.RuntimeTools),
		WorkspaceRoots:          cloneStringSlice(caps.WorkspaceRoots),
		Shells:                  cloneStringSlice(caps.Shells),
		PathStyle:               caps.PathStyle,
		NetworkProfile:          caps.NetworkProfile,
		CredentialRefs:          cloneStringSlice(caps.CredentialRefs),
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
			out.CredentialProfiles[agentType] = cloneStringSlice(names.Names)
		}
	}
	return out
}

func registryLoadToPB(load registry.Load) *pb.RuntimeLoad {
	return &pb.RuntimeLoad{
		CurrentSessions:  int32(load.CurrentSessions),
		ReservedSlots:    int32(load.ReservedSlots),
		PerAgentRunning:  intMapToInt32Map(load.PerAgentRunning),
		PerAgentReserved: intMapToInt32Map(load.PerAgentReserved),
		DiskPressure:     load.DiskPressure,
	}
}

func pbLoadToRegistry(load *pb.RuntimeLoad) registry.Load {
	if load == nil {
		return registry.Load{}
	}
	return registry.Load{
		CurrentSessions:  int(load.CurrentSessions),
		ReservedSlots:    int(load.ReservedSlots),
		PerAgentRunning:  int32MapToIntMap(load.PerAgentRunning),
		PerAgentReserved: int32MapToIntMap(load.PerAgentReserved),
		DiskPressure:     load.DiskPressure,
	}
}

func intMapToInt32Map(in map[string]int) map[string]int32 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int32, len(in))
	for k, v := range in {
		out[k] = int32(v)
	}
	return out
}

func int32MapToIntMap(in map[string]int32) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = int(v)
	}
	return out
}
