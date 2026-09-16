package hub

import (
	"github.com/amuxos/amux/internal/executionauth"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// sessionByRef finds a live session on a target by name or id for callers that
// need its frozen execution identity.
func sessionByRef(targets []*pb.Target, targetID, name string) *pb.Session {
	for _, target := range targets {
		if target == nil || target.Id != targetID {
			continue
		}
		for _, sess := range target.Sessions {
			if sess != nil && (sess.Name == name || sess.Id == name) {
				return sess
			}
		}
	}
	return nil
}

func sessionFactsFromTargets(targets []*pb.Target) []executionauth.SessionFacts {
	var facts []executionauth.SessionFacts
	for _, target := range targets {
		if target == nil {
			continue
		}
		for _, session := range target.Sessions {
			if session == nil {
				continue
			}
			facts = append(facts, executionauth.SessionFacts{
				ID:       session.Id,
				TargetID: target.Id,
				Name:     session.Name,
				LaunchID: session.LaunchId,
				Identity: principal.FromProtoIdentity(session.ExecutionIdentity),
			})
		}
	}
	return facts
}

func filterTargetsBySessionAccess(targets []*pb.Target, actor *principal.ActorContext, isolationActive bool) []*pb.Target {
	facts := sessionFactsFromTargets(targets)
	policy := executionauth.New(nil)
	out := make([]*pb.Target, 0, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}
		visible := make([]*pb.Session, 0, len(target.Sessions))
		for _, sess := range target.Sessions {
			if sess == nil {
				continue
			}
			decision := policy.SessionListAccess(executionauth.SessionAccessRequest{
				Actor: actor,
				Target: executionauth.SessionFacts{
					ID:       sess.Id,
					TargetID: target.Id,
					Name:     sess.Name,
					LaunchID: sess.LaunchId,
					Identity: principal.FromProtoIdentity(sess.ExecutionIdentity),
				},
				Sessions:        facts,
				IsolationActive: isolationActive,
			})
			if decision.Allowed {
				visible = append(visible, sess)
			}
		}
		narrowed := *target
		narrowed.Sessions = visible
		out = append(out, &narrowed)
	}
	return out
}
