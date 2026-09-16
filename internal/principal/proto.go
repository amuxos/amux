package principal

import pb "github.com/amuxos/amux/internal/transport/pbgen"

func ToProtoIdentity(i ExecutionIdentity) *pb.ExecutionIdentity {
	if i.IdentityMode == "" {
		return nil
	}
	return &pb.ExecutionIdentity{
		IdentityMode:                    string(i.IdentityMode),
		OriginActorPrincipalId:          i.OriginActorPrincipalID,
		EffectivePrincipalId:            i.EffectivePrincipalID,
		RequestedRunAs:                  i.RequestedRunAs,
		Source:                          string(i.Source),
		ParentExecutionId:               i.ParentExecutionID,
		DelegationId:                    i.DelegationID,
		CreationAuthorizationDecisionId: i.CreationAuthorizationDecisionID,
		CreationPolicyRevision:          i.CreationPolicyRevision,
		PrincipalEnvironmentRef:         i.PrincipalEnvironmentRef,
		ExecutionStateRef:               i.ExecutionStateRef,
		IsolationBackend:                i.IsolationBackend,
	}
}

func FromProtoIdentity(i *pb.ExecutionIdentity) ExecutionIdentity {
	if i == nil {
		return ExecutionIdentity{}
	}
	return ExecutionIdentity{
		IdentityMode:                    IdentityMode(i.IdentityMode),
		OriginActorPrincipalID:          i.OriginActorPrincipalId,
		EffectivePrincipalID:            i.EffectivePrincipalId,
		RequestedRunAs:                  i.RequestedRunAs,
		Source:                          Source(i.Source),
		ParentExecutionID:               i.ParentExecutionId,
		DelegationID:                    i.DelegationId,
		CreationAuthorizationDecisionID: i.CreationAuthorizationDecisionId,
		CreationPolicyRevision:          i.CreationPolicyRevision,
		PrincipalEnvironmentRef:         i.PrincipalEnvironmentRef,
		ExecutionStateRef:               i.ExecutionStateRef,
		IsolationBackend:                i.IsolationBackend,
	}
}
