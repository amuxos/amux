package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// Only the in-process Bridge capability can set this marker. RPC clients cannot
// supply a trusted identity or claim an idle session as an active Task parent.
type conversationSubmitContextKey struct{}

func (r *bridgeSubtaskReader) conversationIdentity(targetID, sessionName string) (principal.ExecutionIdentity, error) {
	if r == nil || r.router == nil || r.router.hub == nil || strings.TrimSpace(targetID) == "" || strings.TrimSpace(sessionName) == "" {
		return principal.ExecutionIdentity{}, fmt.Errorf("conversation task context is unavailable")
	}
	for _, target := range r.router.hub.ListTargets() {
		if target.Id != targetID {
			continue
		}
		for _, session := range target.Sessions {
			if session.Name != sessionName {
				continue
			}
			identity := principal.FromProtoIdentity(session.ExecutionIdentity)
			if identity.IdentityMode == "" {
				identity = principal.LegacyHostIdentity(principal.SourceFeishu)
			}
			if err := identity.Validate(); err != nil {
				return principal.ExecutionIdentity{}, err
			}
			if !identity.Identified() && r.router.fleetIsolationActive() {
				return principal.ExecutionIdentity{}, fmt.Errorf("conversation has no execution principal on an isolated host")
			}
			if identity.Identified() {
				if r.router.principals == nil {
					return principal.ExecutionIdentity{}, fmt.Errorf("conversation principal registry is unavailable")
				}
				if err := r.router.principals.AuthorizeScheduledExecution(identity); err != nil {
					return principal.ExecutionIdentity{}, fmt.Errorf("conversation authority is no longer active: %w", err)
				}
			}
			return identity, nil
		}
	}
	return principal.ExecutionIdentity{}, fmt.Errorf("conversation session is unavailable on its target")
}

func (r *bridgeSubtaskReader) SubmitConversationTask(ctx context.Context, targetID, sessionName string, raw []byte) (string, error) {
	identity, err := r.conversationIdentity(targetID, sessionName)
	if err != nil {
		return "", err
	}
	var spec task.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return "", err
	}
	if spec.ExecutionIdentity != nil || spec.RunAs != "" || spec.ParentSessionID != "" || spec.ParentTaskID != "" || spec.ParentTurnID != "" || spec.AgentInstanceID != "" || (spec.Mode != "" && spec.Mode != task.ModeEphemeral) {
		return "", fmt.Errorf("conversation background tasks require an independent ephemeral execution")
	}
	spec.ExecutionIdentity = &identity
	spec.Mode = task.ModeEphemeral
	notify := false
	spec.NotifyParent = &notify
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return r.router.SubmitScheduledTask(context.WithValue(ctx, conversationSubmitContextKey{}, true), encoded)
}

func (r *bridgeSubtaskReader) ReadConversationTask(ctx context.Context, targetID, sessionName, id string) ([]byte, error) {
	identity, err := r.conversationIdentity(targetID, sessionName)
	if err != nil {
		return nil, err
	}
	target, err := r.router.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	response, err := r.router.hub.Route(ctx, target, pb.TaskMethodGet, &pb.GetSubtaskRequest{Id: id})
	if err != nil {
		return nil, err
	}
	var result pb.GetSubtaskResponse
	if err := json.Unmarshal(response.Payload, &result); err != nil {
		return nil, err
	}
	var saved struct {
		Identity principal.ExecutionIdentity `json:"execution_identity"`
	}
	if err := json.Unmarshal(result.TaskJson, &saved); err != nil {
		return nil, err
	}
	if saved.Identity.IdentityMode == "" {
		saved.Identity = principal.LegacyHostIdentity(principal.SourceMCP)
	}
	if err := saved.Identity.Validate(); err != nil {
		return nil, err
	}
	if saved.Identity.IdentityMode != identity.IdentityMode || saved.Identity.OriginActorPrincipalID != identity.OriginActorPrincipalID || saved.Identity.EffectivePrincipalID != identity.EffectivePrincipalID || saved.Identity.DelegationID != identity.DelegationID {
		return nil, fmt.Errorf("task does not belong to the conversation execution identity")
	}
	return result.TaskJson, nil
}
