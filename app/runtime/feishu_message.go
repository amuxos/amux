package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessioncredential"
)

// SendPersonalFeishuMessage is reachable only through the trusted Hub tunnel.
func (s *server) SendPersonalFeishuMessage(ctx context.Context, req feishumsg.PersonalRequest) (feishumsg.PersonalResult, error) {
	var empty feishumsg.PersonalResult
	send := distributionHooks.SendPersonalFeishuMessage
	if send == nil || s.isolationRuntime == nil {
		return empty, errors.New(feishumsg.Copy("personal_unavailable"))
	}
	if req.Message.ReceiveIDType != feishumsg.Self {
		return empty, errors.New(feishumsg.Copy("self_destination"))
	}
	if err := req.Message.Validate(); err != nil {
		return empty, err
	}
	sess, err := s.mgr.Get(req.SessionID)
	if err != nil {
		return empty, errors.New(feishumsg.Copy("session"))
	}
	snap := sess.Snapshot()
	if !personalFeishuSessionMatches(snap, req) {
		return empty, errors.New(feishumsg.Copy("personal_session"))
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	msg := req.Message
	// Preserve deduplication across retries of this Session's send.
	digest := sha256.Sum256([]byte(snap.ID + "\x00" + msg.UUID))
	msg.UUID = fmt.Sprintf("%x", digest[:16])
	return send(ctx, msg, func(ctx context.Context, name string, args, env []string, input string) (sessioncredential.CommandOutput, error) {
		return runIsolatedCommand(ctx, s.isolationRuntime, snap.ExecutionIdentity, name, args, env, input, nil)
	})
}

func personalFeishuSessionMatches(snap session.Snapshot, req feishumsg.PersonalRequest) bool {
	return snap.ID == req.SessionID && snap.State != session.StateArchived &&
		feishumsg.IsPersonalSession(snap.ExecutionIdentity, req.PrincipalID)
}

func (s *maintenanceSessionServer) SendPersonalFeishuMessage(ctx context.Context, req feishumsg.PersonalRequest) (feishumsg.PersonalResult, error) {
	runtime, ok := s.AmuxletServer.(*server)
	if !ok {
		return feishumsg.PersonalResult{}, errors.New(feishumsg.Copy("personal_unavailable"))
	}
	done, err := s.maintenance.enter(true)
	if err != nil {
		return feishumsg.PersonalResult{}, errors.New(feishumsg.Copy("personal_unavailable"))
	}
	defer done()
	return runtime.SendPersonalFeishuMessage(ctx, req)
}
